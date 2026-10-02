// lblab — bộ đo phase 6: 4 backend cố ý lệch, so p99 và PHẦN TẢI mỗi backend
// nhận giữa rr / leastconn / p2c / p2c-slow (tau dài) / chash. Có cột "chỗ
// P2C thua": -flap biến một backend nhanh thành chậm 10x ở giữa bài, in p99
// nửa đầu / nửa sau riêng.
//
// Chạy in-process: 4 fixture.Sim (internal/fixture — chỗ duy nhất có
// net/http), proxy (internal/proxy, chỉ net), client raw (net + httpx).
//
// Closed-loop (-conns goroutine, mỗi con gửi request kế sau khi nhận response
// trước): backend chậm làm client tự gửi ít hơn ⇒ p99 dịu hơn tải thật.
// Cùng máy, cùng tiến trình ⇒ so GIỮA thuật toán công bằng; số tuyệt đối không
// mang sang máy khác. Loopback: chênh lệch chỉ đến từ service time giả lập.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thaivro/edgegate/internal/fixture"
	"github.com/thaivro/edgegate/internal/httpx"
	"github.com/thaivro/edgegate/internal/lb"
	"github.com/thaivro/edgegate/internal/proxy"
)

var lim = httpx.DefaultLimits()

type sample struct {
	lat    time.Duration
	status int
	second bool // nửa sau của bài (sau mốc flap)
}

type result struct {
	algo                   string
	lats, first, second    []time.Duration
	n, err5xx, ioErr       int
	share                  []float64
	share2                 []float64       // phần tải nửa sau (sau mốc flap) — p99 không tách được thuật toán khi b2 còn > 1 % tải
	ewmaMid, ewmaEnd       []time.Duration // EWMA mỗi backend lúc flap và lúc hết bài
	ejections, ejectRefuse int64
	wall                   time.Duration
}

func main() {
	algos := flag.String("algos", "rr,leastconn,p2c,chash", "thuật toán, phẩy: rr,leastconn,p2c,p2c-slow,chash")
	n := flag.Int("n", 20000, "số request mỗi thuật toán")
	conns := flag.Int("conns", 32, "số connection client song song (closed-loop)")
	base := flag.Duration("base", 2*time.Millisecond, "service time nền của mỗi backend")
	skew := flag.String("skew", "", "lệch tải, vd: slow=10x,err=30% (b0 chậm 10x, b1 trả 503 nhanh 30 %)")
	flap := flag.Bool("flap", false, "giữa bài b2 (đang nhanh) đổi thành chậm 10x — chỗ P2C tau dài thua")
	recov := flag.Bool("recover", false, "với -flap: chiều ngược — b2 chậm 10x từ đầu, hồi phục ở n/2 (EWMA cũ giữ b2 bị né)")
	tau := flag.Duration("tau", time.Second, "tau EWMA của p2c")
	tauSlow := flag.Duration("tau-slow", 30*time.Second, "tau của p2c-slow")
	outlier := flag.Bool("outlier", true, "passive outlier ejection (5 lỗi liên tiếp, eject 2 s)")
	health := flag.Bool("health", true, "active health check 200 ms")
	sessions := flag.Int("sessions", 1000, "số giá trị X-Session (khoá chash)")
	maxRatio := flag.Float64("max-share-ratio", 0, "P6-2: >0 ⇒ thoát 1 nếu max/min share của một algo vượt ngưỡng (chỉ có nghĩa với backend giống hệt)")
	flag.Parse()

	// --- backend giả lập ----------------------------------------------------
	sims := make([]*fixture.Sim, 4)
	ups := make([]string, 4)
	for i := range sims {
		sims[i] = fixture.NewSim(fmt.Sprintf("b%d", i), *base, 0)
		addr, stop, err := fixture.ListenAndServeSim("127.0.0.1:0", sims[i])
		if err != nil {
			fatal(err)
		}
		defer stop()
		ups[i] = addr
	}
	slowX := 1
	for _, kv := range strings.Split(*skew, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
		switch k {
		case "slow":
			slowX, _ = strconv.Atoi(strings.TrimSuffix(v, "x"))
			sims[0].SetDelay(*base * time.Duration(slowX))
		case "err":
			pct, _ := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64)
			sims[1].SetErrRate(pct / 100)
		case "":
		default:
			fatal(fmt.Errorf("skew %q không hiểu", kv))
		}
	}
	if *flap && slowX == 1 {
		slowX = 10
	}

	fmt.Printf("lblab: n=%d conns=%d base=%s skew=%q flap=%v recover=%v tau=%s tau-slow=%s outlier=%v health=%v sessions=%d\n",
		*n, *conns, *base, *skew, *flap, *recov, *tau, *tauSlow, *outlier, *health, *sessions)
	for i, s := range sims {
		fmt.Printf("  b%d %s delay=%s err=%s\n", i, ups[i], s.Delay(), errOf(s))
	}
	fmt.Println("  (closed-loop, coordinated omission chưa loại trừ; loopback; 4 backend + proxy + client cùng tiến trình)")

	var rows []*result
	for _, algo := range strings.Split(*algos, ",") {
		algo = strings.TrimSpace(algo)
		b2Before, b2After := *base, *base*time.Duration(slowX)
		if *recov {
			b2Before, b2After = b2After, b2Before
		}
		sims[2].SetDelay(b2Before) // hoàn flap của lần trước
		cfg := lb.Config{Algo: algo, Tau: *tau, HashHeader: "X-Session",
			Health:  lb.HealthConfig{Disabled: !*health, Interval: 200 * time.Millisecond, Timeout: 500 * time.Millisecond},
			Outlier: lb.OutlierConfig{Disabled: !*outlier, Consecutive: 5, BaseEject: 2 * time.Second, MaxEject: 10 * time.Second},
			Logf:    func(string, ...any) {}}
		if algo == "p2c-slow" {
			cfg.Algo, cfg.Tau = "p2c", *tauSlow
		}
		srv := proxy.New(proxy.Config{Listen: "127.0.0.1:0", Upstreams: ups, LB: cfg, Logf: func(string, ...any) {}})
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fatal(err)
		}
		go srv.Serve(ln)
		var mid lb.Stats // chụp đúng lúc flap; đọc sau wg.Wait trong run ⇒ không race
		r := run(algo, ln.Addr().String(), *n, *conns, *sessions, *flap, func() {
			mid = srv.LBStats()
			sims[2].SetDelay(b2After)
		})
		st := srv.LBStats()
		for i, b := range st.Backends {
			r.share = append(r.share, float64(b.Picks)/float64(*n)*100)
			r.ejections += b.Ejections
			if *flap {
				r.share2 = append(r.share2, float64(b.Picks-mid.Backends[i].Picks)/float64(*n-*n/2)*100)
				r.ewmaMid = append(r.ewmaMid, mid.Backends[i].EWMA)
				r.ewmaEnd = append(r.ewmaEnd, b.EWMA)
			}
		}
		r.ejectRefuse = st.EjectRefused
		srv.Close()
		rows = append(rows, r)
	}
	printTable(rows, *flap, *recov)
	if *maxRatio > 0 {
		bad := false
		for _, r := range rows {
			mx, mn := 0.0, 101.0
			for _, v := range r.share {
				mx, mn = max(mx, v), min(mn, v)
			}
			if mn == 0 || mx/mn > *maxRatio {
				fmt.Printf("FAIL %s: max/min share %.2f > %.2f\n", r.algo, mx/mn, *maxRatio)
				bad = true
			}
		}
		if bad {
			os.Exit(1)
		}
	}
}

func errOf(s *fixture.Sim) string {
	if s.Errors() == 0 && s.Served() == 0 {
		return "?"
	}
	return fmt.Sprintf("%d/%d", s.Errors(), s.Served())
}

// run: closed-loop, conns goroutine chia nhau n request qua bộ đếm chung.
func run(algo, addr string, n, conns, sessions int, flap bool, onFlap func()) *result {
	var idx atomic.Int64
	var mu sync.Mutex
	var out []sample
	ioErr := 0
	var wg sync.WaitGroup
	t0 := time.Now()
	for c := 0; c < conns; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var conn net.Conn
			var br *bufio.Reader
			local := make([]sample, 0, n/conns+1)
			for {
				i := int(idx.Add(1) - 1)
				if i >= n {
					break
				}
				if flap && i == n/2 {
					onFlap()
				}
				if conn == nil {
					var err error
					conn, err = net.Dial("tcp", addr)
					if err != nil {
						fatal(err)
					}
					br = bufio.NewReader(conn)
				}
				lat, status, err := get(conn, br, fmt.Sprintf("s%d", rand.IntN(sessions)))
				if err != nil {
					conn.Close()
					conn, br = nil, nil
					mu.Lock()
					ioErr++
					mu.Unlock()
					continue
				}
				local = append(local, sample{lat: lat, status: status, second: i >= n/2})
			}
			if conn != nil {
				conn.Close()
			}
			mu.Lock()
			out = append(out, local...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	r := &result{algo: algo, n: len(out), ioErr: ioErr, wall: time.Since(t0)}
	for _, s := range out {
		r.lats = append(r.lats, s.lat)
		if s.status >= 500 {
			r.err5xx++
		}
		if s.second {
			r.second = append(r.second, s.lat)
		} else {
			r.first = append(r.first, s.lat)
		}
	}
	return r
}

func get(c net.Conn, br *bufio.Reader, session string) (time.Duration, int, error) {
	t0 := time.Now()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c, "GET /hello HTTP/1.1\r\nHost: lblab\r\nX-Session: "+session+"\r\n\r\n"); err != nil {
		return 0, 0, err
	}
	resp, err := httpx.ReadResponse(br, lim, "GET")
	if err != nil {
		return 0, 0, err
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return 0, 0, err
	}
	if resp.Close {
		return 0, 0, fmt.Errorf("server đóng connection client")
	}
	return time.Since(t0), resp.Status, nil
}

func pct(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := make([]time.Duration, len(ds))
	copy(s, ds)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(float64(len(s)-1) * p)
	return s[i]
}

func printTable(rows []*result, flap, recov bool) {
	fmt.Println()
	fmt.Printf("%-9s %8s %8s %8s %8s %6s %6s  %-27s %s\n", "algo", "p50", "p90", "p99", "max", "5xx%", "rps", "share b0/b1/b2/b3 (%)", "eject/refused ioErr")
	for _, r := range rows {
		sh := make([]string, len(r.share))
		for i, v := range r.share {
			sh[i] = fmt.Sprintf("%.1f", v)
		}
		fmt.Printf("%-9s %8s %8s %8s %8s %5.2f%% %6.0f  %-27s %d/%d %d\n", r.algo,
			rd(pct(r.lats, .5)), rd(pct(r.lats, .9)), rd(pct(r.lats, .99)), rd(pct(r.lats, 1)),
			float64(r.err5xx)/float64(max(r.n, 1))*100, float64(r.n)/r.wall.Seconds(),
			strings.Join(sh, "/"), r.ejections, r.ejectRefuse, r.ioErr)
	}
	if flap {
		fmt.Println()
		dir := "nhanh→chậm 10x"
		if recov {
			dir = "chậm 10x→nhanh"
		}
		fmt.Printf("%-9s %12s %12s %12s %12s %12s  %-24s %s   (b2 đổi "+dir+" ở request n/2)\n", "algo", "p50 nửa đầu", "p99 nửa đầu", "p50 nửa sau", "p90 nửa sau", "p99 nửa sau", "share nửa sau b0/b1/b2/b3", "EWMA b2 lúc flap→cuối")
		for _, r := range rows {
			sh := make([]string, len(r.share2))
			for i, v := range r.share2 {
				sh[i] = fmt.Sprintf("%.1f", v)
			}
			fmt.Printf("%-9s %12s %12s %12s %12s %12s  %-24s %s→%s\n", r.algo, rd(pct(r.first, .5)), rd(pct(r.first, .99)),
				rd(pct(r.second, .5)), rd(pct(r.second, .9)), rd(pct(r.second, .99)), strings.Join(sh, "/"), rd(r.ewmaMid[2]), rd(r.ewmaEnd[2]))
		}
	}
}

func rd(d time.Duration) string { return d.Round(10 * time.Microsecond).String() }

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "lblab:", err)
	os.Exit(1)
}
