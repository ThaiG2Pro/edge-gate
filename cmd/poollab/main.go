// poollab — bộ đo phase 5: pool-off vs pool-on, báo cáo bằng ms và RTT TIẾT
// KIỆM / REQUEST (phase 0 G2/G3 đã chứng minh tỉ số là đơn vị sai cho pool).
//
// Chạy in-process: upstream fixture (internal/fixture — chỗ duy nhất có
// net/http), proxy (internal/proxy, chỉ net), client raw (net + httpx). Chỉ
// package fixture đụng net/http; file này không import nó.
//
// Closed-loop, cùng máy (coordinated omission chưa loại trừ, P-env-2 chưa
// trả). Chạy HAI lần: RTT 0 (`make poollab`) và RTT 20 ms (`make poollab-rtt`).
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thaivro/edgegate/internal/fixture"
	"github.com/thaivro/edgegate/internal/httpx"
	"github.com/thaivro/edgegate/internal/proxy"
)

var lim = httpx.DefaultLimits()

func main() {
	mode := flag.String("pool", "both", "off | on | both")
	n := flag.Int("n", 2000, "tổng số request mỗi mẫu")
	conns := flag.Int("conns", 1, "số client song song (closed-loop mỗi client)")
	upstreamAddr := flag.String("upstream", "", "upstream ngoài (mặc định: fixture in-process)")
	target := flag.String("target", "/hello", "request-target")
	idlerace := flag.Bool("idlerace", false, "P5-4: đếm 502 khi upstream đóng rỗi ngẫu nhiên 1-50 ms (POST có body)")
	flag.Parse()
	if *idlerace {
		runIdleRace(*n, *conns)
		return
	}

	up := *upstreamAddr
	if up == "" {
		addr, stop, err := fixture.ListenAndServe("127.0.0.1:0")
		if err != nil {
			fatal(err)
		}
		defer stop()
		up = addr
	}
	fmt.Printf("upstream %s · target %s · n=%d · conns=%d · closed-loop, cùng máy\n", up, *target, *n, *conns)

	// RTT tại chỗ bằng ping (cùng cách `make rtt-up` kiểm), KHÔNG suy từ tham
	// số netem (phase 0: delay 10ms ⇒ RTT 20 ms trên lo) và KHÔNG lấy thời gian
	// net.Dial làm RTT: trên loopback một dial ≈ 300-900 µs trong khi RTT ≈ 30 µs
	// — 96 % là dựng socket, không phải mạng (phase 0 G2). In cả hai để so.
	// Lần chạy đầu turn 1: ping loopback WSL2 cho 2.78 ms trong khi MỘT net.Dial
	// trọn vẹn chỉ 312 µs — ping không phải RTT TCP thật ở đây. Nguồn đúng là
	// srtt của kernel trên chính connection TCP tới upstream (`ss -tin`).
	host, upPort, _ := net.SplitHostPort(up)
	pingD, pingSrc := pingRTT(host)
	dialP50 := measureDial(up, 20)
	rtt, rttSrc := tcpRTT(up, upPort)
	if rtt == 0 {
		rtt, rttSrc = pingD, pingSrc
	}
	fmt.Printf("RTT = %s (%s) · ping = %s (%s) · chi phí một net.Dial (p50/20) = %s\n", rd(rtt), rttSrc, rd(pingD), pingSrc, rd(dialP50))
	if rtt > 0 {
		fmt.Printf("  một net.Dial = %.1f RTT — phần vượt 1 RTT là dựng socket/accept/goroutine, không phải mạng (phase 0 G2)\n", float64(dialP50)/float64(rtt))
	}

	direct := run("thẳng upstream, keep-alive", up, *target, *n, *conns)
	printTable("baseline", []*sample{direct})

	var off, on *sample
	var stOff, stOn proxy.PoolStats
	if *mode == "off" || *mode == "both" {
		off, stOff = viaProxy(up, *target, *n, *conns, true)
	}
	if *mode == "on" || *mode == "both" {
		on, stOn = viaProxy(up, *target, *n, *conns, false)
	}
	rows := []*sample{}
	if off != nil {
		rows = append(rows, off)
	}
	if on != nil {
		rows = append(rows, on)
	}
	printTable(fmt.Sprintf("qua proxy, GET %s, closed-loop, n=%d, conns=%d", *target, *n, *conns), rows)
	if off != nil {
		fmt.Printf("  pool-off stats: %+v\n", stOff)
	}
	if on != nil {
		fmt.Printf("  pool-on  stats: %+v\n", stOn)
	}
	if off != nil && on != nil {
		saved := off.pct(50) - on.pct(50)
		fmt.Println()
		fmt.Printf("  G1/G2 tỉ số        p50 off / p50 on   = %.2fx\n", float64(off.pct(50))/float64(on.pct(50)))
		fmt.Printf("  G1/G2 tiết kiệm     p50 off − p50 on   = %s / request\n", rd(saved))
		if rtt > 0 {
			fmt.Printf("  G1/G2 theo RTT      tiết kiệm / RTT     = %.2f RTT / request\n", float64(saved)/float64(rtt))
		}
		fmt.Printf("  overhead còn lại    p50 on − p50 thẳng = %s (= parse 2 chiều + goroutine, KHÔNG còn dial)\n", rd(on.pct(50)-direct.pct(50)))
		fmt.Printf("  wall off / wall on = %.2fx\n", float64(off.wall)/float64(on.wall))
	}
}

// viaProxy dựng proxy in-process với pool tắt/bật, chạy mẫu, in TIME_WAIT (G7).
func viaProxy(up, target string, n, conns int, disabled bool) (*sample, proxy.PoolStats) {
	name := "qua proxy, pool-on"
	if disabled {
		name = "qua proxy, pool-off (dial mỗi request)"
	}
	srv := proxy.New(proxy.Config{Listen: "127.0.0.1:0", Upstream: up,
		Pool: proxy.PoolConfig{Disabled: disabled},
		Logf: func(string, ...any) {}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	go srv.Serve(ln)
	_, upPort, _ := net.SplitHostPort(up)
	twProxy0, twUp0 := timeWait("dport", upPort), timeWait("sport", upPort)
	s := run(name, ln.Addr().String(), target, n, conns)
	st := srv.PoolStats()
	// G7: TIME_WAIT thuộc bên ĐÓNG TRƯỚC. Đếm DELTA trong mẫu này (TIME_WAIT
	// sống 60 s nên số tuyệt đối gộp cả mẫu trước và cả measureDial).
	fmt.Printf("  [%s] Δ TIME_WAIT: phía proxy (dport=%s) %+d · phía upstream (sport=%s) %+d\n",
		name, upPort, timeWait("dport", upPort)-twProxy0, upPort, timeWait("sport", upPort)-twUp0)
	srv.Close()
	return s, st
}

// timeWait đếm socket TIME_WAIT khớp port; -1 nếu không có `ss`.
func timeWait(side, port string) int {
	out, err := exec.Command("ss", "-Htan", "state", "time-wait", "( "+side+" = :"+port+" )").Output()
	if err != nil {
		return -1
	}
	t := strings.TrimSpace(string(out))
	if t == "" {
		return 0
	}
	return len(strings.Split(t, "\n"))
}

// tcpRTT: mở một connection tới upstream, chạy 20 GET để kernel có mẫu, rồi
// đọc srtt của CHÍNH connection đó từ `ss -tin` (trường `rtt:<srtt>/<rttvar>`
// ms). Đây là RTT mà TCP thấy — cả netem lẫn loopback thật. 0 nếu không có ss.
func tcpRTT(addr, port string) (time.Duration, string) {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return 0, ""
	}
	defer c.Close()
	br := bufio.NewReaderSize(c, 8<<10)
	for i := 0; i < 20; i++ {
		if _, err := get(c, br, "/hello"); err != nil {
			return 0, ""
		}
	}
	_, localPort, _ := net.SplitHostPort(c.LocalAddr().String())
	out, err := exec.Command("ss", "-Htin", "state", "established", "( sport = :"+localPort+" and dport = :"+port+" )").Output()
	if err != nil {
		return 0, ""
	}
	for _, f := range strings.Fields(string(out)) {
		if strings.HasPrefix(f, "rtt:") {
			var srtt, rttvar float64
			if _, err := fmt.Sscanf(strings.TrimPrefix(f, "rtt:"), "%g/%g", &srtt, &rttvar); err == nil {
				return time.Duration(srtt * float64(time.Millisecond)), fmt.Sprintf("srtt kernel, ss -tin, rttvar %.3f ms", rttvar)
			}
		}
	}
	return 0, ""
}

// pingRTT: rtt avg của `ping -c 5 -i 0.2 host`. Không có ping ⇒ dùng dial p50
// và NÓI RÕ trong nhãn — con số đó là chi phí dial, không phải RTT.
func pingRTT(host string) (time.Duration, string) {
	out, err := exec.Command("ping", "-c", "5", "-i", "0.2", host).Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			// rtt min/avg/max/mdev = 0.031/0.045/0.061/0.010 ms
			if i := strings.Index(line, "min/avg/max"); i >= 0 {
				if eq := strings.Index(line, "= "); eq >= 0 {
					f := strings.Split(strings.Fields(line[eq+2:])[0], "/")
					if len(f) >= 2 {
						var avg float64
						if _, err := fmt.Sscanf(f[1], "%g", &avg); err == nil {
							return time.Duration(avg * float64(time.Millisecond)), "ping avg, 5 gói"
						}
					}
				}
			}
		}
	}
	return 0, "KHÔNG có ping — RTT không đo được, cột theo-RTT bị bỏ"
}

func measureDial(addr string, k int) time.Duration {
	if k == 0 {
		return 0
	}
	var ds []time.Duration
	for i := 0; i < k; i++ {
		t0 := time.Now()
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			fatal(err)
		}
		ds = append(ds, time.Since(t0))
		c.Close()
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[len(ds)/2]
}

// ---------------------------------------------------------------------------

type sample struct {
	name string
	mu   sync.Mutex
	lats []time.Duration
	wall time.Duration
	errs int
}

func (s *sample) pct(p float64) time.Duration {
	if len(s.lats) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), s.lats...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[int(float64(len(sorted)-1)*p/100)]
}

// run: conns client song song, mỗi client MỘT connection keep-alive tới addr,
// tổng n request GET target. 20 request warm-up mỗi client không tính.
func run(name, addr, target string, n, conns int) *sample {
	s := &sample{name: name}
	var wg sync.WaitGroup
	per := n / conns
	t0 := time.Now()
	for i := 0; i < conns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.DialTimeout("tcp", addr, 2*time.Second)
			if err != nil {
				fatal(err)
			}
			defer c.Close()
			c.(*net.TCPConn).SetNoDelay(true)
			br := bufio.NewReaderSize(c, 8<<10)
			for j := 0; j < 20; j++ {
				if _, err := get(c, br, target); err != nil {
					fatal(fmt.Errorf("%s warm-up: %v", name, err))
				}
			}
			lats := make([]time.Duration, 0, per)
			errs := 0
			for j := 0; j < per; j++ {
				lat, err := get(c, br, target)
				if err != nil {
					errs++
					continue
				}
				lats = append(lats, lat)
			}
			s.mu.Lock()
			s.lats = append(s.lats, lats...)
			s.errs += errs
			s.mu.Unlock()
		}()
	}
	wg.Wait()
	s.wall = time.Since(t0)
	return s
}

func get(c net.Conn, br *bufio.Reader, target string) (time.Duration, error) {
	t0 := time.Now()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, "GET "+target+" HTTP/1.1\r\nHost: poollab\r\n\r\n"); err != nil {
		return 0, err
	}
	resp, err := httpx.ReadResponse(br, lim, "GET")
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return 0, err
	}
	if resp.Status != 200 {
		return 0, fmt.Errorf("status %d", resp.Status)
	}
	if resp.Close {
		return 0, fmt.Errorf("server đóng connection client")
	}
	return time.Since(t0), nil
}

func printTable(title string, rows []*sample) {
	fmt.Printf("\n== %s ==\n", title)
	fmt.Printf("%-42s %8s %9s %9s %9s %9s %5s\n", "mẫu", "n", "p50", "p90", "p99", "wall", "err")
	for _, s := range rows {
		fmt.Printf("%-42s %8d %9s %9s %9s %9s %5d\n", s.name, len(s.lats),
			rd(s.pct(50)), rd(s.pct(90)), rd(s.pct(99)), s.wall.Round(time.Millisecond), s.errs)
	}
}

func rd(d time.Duration) string { return d.Round(time.Microsecond).String() }

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "poollab:", err)
	os.Exit(1)
}
