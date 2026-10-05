// chaoslab — test quyết định của phase 7 (G9) và kịch bản retry (G6).
//
// -scenario chaos (mặc định): 4 backend fixture.SimServer, mỗi -tick một hành
// động ngẫu nhiên trên một backend ngẫu nhiên: kill / revive / slow 10x / lỗi
// 50 % / treo (accept rồi không trả gì) / về bình thường. Generator open-loop
// -rate rps trong -duration. Rồi kiểm bốn invariant ROADMAP:
//
//	(a) không panic          — tiến trình còn sống tới dòng cuối
//	(b) goroutine về nền     — NumGoroutine sau Close proxy so với trước New
//	(c) connection về nền    — fd mở (/proc/self/fd) sau Close so với trước New;
//	                           connection client còn mở ở proxy sau tải = 0
//	(d) không treo           — 0 request quá deadline client; mọi request có
//	                           một status, hoặc lỗi rõ (body cụt); "io-nohead"
//	                           (đóng mà không trả gì) được liệt kê riêng
//
// -scenario retry: 4 backend đóng connection DÙNG LẠI trước khi trả byte nào
// với xác suất -drop; đếm số lần gửi sang upstream / số request client. Chạy
// thường (budget 10 %) và `-tags nodefense7` (retry mù) để so (G6).
//
// Thoát mã 1 khi một invariant vỡ.
package main

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ThaiG2Pro/edge-gate/internal/fixture"
	"github.com/ThaiG2Pro/edge-gate/internal/lb"
	"github.com/ThaiG2Pro/edge-gate/internal/loadgen"
	"github.com/ThaiG2Pro/edge-gate/internal/proxy"
)

func main() {
	scenario := flag.String("scenario", "chaos", "chaos | retry | overload (P7-6: rate > capacity, retry budget có giá trị)")
	dur := flag.Duration("duration", 60*time.Second, "thời gian bắn")
	rate := flag.Float64("rate", 500, "rps open-loop")
	tick := flag.Duration("tick", 300*time.Millisecond, "mỗi bao lâu một hành động chaos")
	drop := flag.Float64("drop", 0.5, "retry: xác suất backend đóng connection dùng lại")
	seed := flag.Uint64("seed", 1, "seed của chaos (in ra để chạy lại được)")
	body := flag.Int("body", 0, "P9-7: mỗi request thứ 4 là GET /large?n=N (N ≥ 64 KiB đi splice); 0 = chỉ /hello")
	healW := flag.Int("heal-weight", 0, "P7-4: thêm N trọng số cho hành động heal (cân lại chaoslab thiên về hại ⇒ status mix có nghĩa)")
	flag.Parse()
	// P7-6: overload = cơ chế retry (drop reused, outlier/health off) nhưng
	// chạy ở rate > capacity để blind-retry khuếch đại tải thấy rõ; so
	// budget-on vs `-tags nodefense7`.
	retryLike := *scenario == "retry" || *scenario == "overload"
	_ = retryLike

	// Khởi động netpoller (epoll fd + eventfd) TRƯỚC khi đo nền: lần dùng mạng
	// đầu tiên mở thêm 2 fd vĩnh viễn — đo trước đó thì nền lệch 2 tuỳ lượt.
	if l, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
		l.Close()
	}
	fdBase, gBase := countFDs(), runtime.NumGoroutine()
	sims := make([]*fixture.SimServer, 4)
	ups := make([]string, 4)
	for i := range sims {
		ss, err := fixture.NewSimServer("127.0.0.1:0", fixture.NewSim(fmt.Sprintf("b%d", i), 2*time.Millisecond, 0))
		if err != nil {
			fatal(err)
		}
		sims[i], ups[i] = ss, ss.Addr
		if *scenario == "overload" {
			ss.Sim.SetDropReused(*drop)
			ss.Sim.SetConcurrency(2)
			ss.Sim.SetDelay(10 * time.Millisecond)
		} else if retryLike {
			ss.Sim.SetDropReused(*drop)
		}
	}
	time.Sleep(50 * time.Millisecond)
	fd0, g0 := countFDs(), runtime.NumGoroutine() // nền = sau khi có backend, TRƯỚC proxy

	cfg := proxy.Config{Listen: "127.0.0.1:0", Upstreams: ups, Logf: func(string, ...any) {},
		DialTimeout: 500 * time.Millisecond, UpstreamHeaderTimeout: time.Second, UpstreamBodyTimeout: 2 * time.Second,
		Shed: proxy.ShedConfig{MaxInflight: 256, MaxQueue: 256, QueueTimeout: 100 * time.Millisecond},
		LB: lb.Config{Algo: "p2c", Health: lb.HealthConfig{Interval: 200 * time.Millisecond, Timeout: 200 * time.Millisecond},
			Outlier: lb.OutlierConfig{BaseEject: time.Second, MaxEject: 5 * time.Second}}}
	cfg.LB.Logf = cfg.Logf
	if retryLike {
		cfg.LB.Outlier.Disabled = true // đo retry, không đo eject
		cfg.LB.Health.Disabled = true
	}
	srv := proxy.New(cfg)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	go srv.Serve(ln)
	// Deadline client: chuỗi dài nhất của proxy = dial (0.5 s) + đổi backend dial
	// (0.5 s) + head (1 s) + retry D4 head (1 s) + body (2 s) = 5 s; +2 s dư.
	clientTO := 7 * time.Second
	fmt.Printf("chaoslab: scenario=%s duration=%s rate=%.0f tick=%s seed=%d drop=%.2f body=%d splice=default-on client-timeout=%s\n", *scenario, *dur, *rate, *tick, *seed, *drop, *body, clientTO)
	fmt.Printf("  nền (trước proxy): goroutine %d, fd %d (trước cả backend: %d / %d)\n", g0, fd0, gBase, fdBase)
	fmt.Println("  (open-loop, latency từ giờ hẹn; loopback; 4 backend + proxy + generator cùng tiến trình)")

	stopChaos := make(chan struct{})
	var actions []string
	var amu sync.Mutex
	var cwg sync.WaitGroup
	if *scenario == "chaos" {
		rng := rand.New(rand.NewPCG(*seed, 0))
		cwg.Add(1)
		go func() {
			defer cwg.Done()
			t := time.NewTicker(*tick)
			defer t.Stop()
			for {
				select {
				case <-stopChaos:
					return
				case <-t.C:
				}
				i := rng.IntN(4)
				s := sims[i]
				var a string
				// P7-4: heal chiếm 1 + healW / (6 + healW) hành động. healW=0 =
				// như cũ (1/6 heal, 5/6 hại) ⇒ status nghiêng 5xx vô nghĩa. Tăng
				// healW để tỉ lệ hại/lành gần thực tế hơn khi đọc status mix.
				switch pick := rng.IntN(6 + *healW); {
				case pick == 0:
					s.Kill()
					a = "kill"
				case pick == 1:
					s.Revive()
					a = "revive"
				case pick == 2:
					s.Sim.SetDelay(20 * time.Millisecond)
					a = "slow"
				case pick == 3:
					s.Sim.SetErrRate(0.5)
					a = "err50"
				case pick == 4:
					s.Sim.SetHang(true)
					a = "hang"
				default: // 5 .. 5+healW
					s.Sim.SetDelay(2 * time.Millisecond)
					s.Sim.SetErrRate(0)
					s.Sim.SetHang(false)
					s.Revive()
					a = "heal"
				}
				amu.Lock()
				actions = append(actions, fmt.Sprintf("b%d:%s", i, a))
				amu.Unlock()
			}
		}()
	}

	t0 := time.Now()
	lg := loadgen.Config{Addr: ln.Addr().String(), Rate: *rate, Duration: *dur, Workers: 512, Timeout: clientTO}
	if *body > 0 {
		large := fmt.Sprintf("GET /large?n=%d HTTP/1.1\r\nHost: chaoslab\r\n\r\n", *body)
		lg.Request = func(i int) string {
			if i%4 == 3 {
				return large
			}
			return "GET /hello HTTP/1.1\r\nHost: chaoslab\r\n\r\n"
		}
	}
	ss := loadgen.Run(lg)
	el := time.Since(t0)
	close(stopChaos)
	cwg.Wait()
	sum := loadgen.Summarize(ss)
	fmt.Println()
	loadgen.Print(os.Stdout, *scenario, sum, el)
	if *scenario == "chaos" {
		counts := map[string]int{}
		for _, a := range actions {
			_, k, _ := strings.Cut(a, ":")
			counts[k]++
		}
		fmt.Printf("%-14s %d hành động chaos: %v\n", "", len(actions), counts)
	}
	st := srv.LBStats()
	for _, b := range st.Backends {
		fmt.Printf("%-14s %s picks %d fails %d ejections %d probes %d reopens %d state %s healthy %v\n", "",
			b.Addr, b.Picks, b.Fails, b.Ejections, b.Probes, b.Reopens, b.State, b.Healthy)
	}
	rs := srv.ResilienceStats()
	fmt.Printf("%-14s proxy: retry cho %d / từ chối %d, shed %d+%d, no-available %d, eject-refused %d\n", "",
		rs.RetryAllowed, rs.RetryDenied, rs.ShedQueueFull, rs.ShedTimeout, st.NoAvailable, st.EjectRefused)

	if retryLike {
		var served, dropped int64
		for _, s := range sims {
			served += s.Sim.Served()
			dropped += s.Sim.Dropped()
		}
		fmt.Printf("%-14s upstream nhận %d lần gửi cho %d request client ⇒ khuếch đại %.3fx; backend đóng %d connection dùng lại; 502 = %.1f %%\n", "",
			served, sum.N, float64(served)/float64(sum.N), dropped, float64(sum.ByStatus[502])/float64(sum.N)*100)
	}

	// --- invariant ------------------------------------------------------------
	ok := true
	time.Sleep(500 * time.Millisecond)
	connsAfter := rs.ConnsActive
	rs = srv.ResilienceStats()
	fmt.Printf("\ninvariant (c'): connection client còn mở ở proxy 500 ms sau tải: %d (ngay khi tải xong: %d)\n", rs.ConnsActive, connsAfter)
	if rs.ConnsActive != 0 {
		ok = false
	}
	for _, s := range sims { // về bình thường để connection pool đóng sạch
		s.Sim.SetHang(false)
	}
	srv.Close()
	for _, s := range sims {
		s.Kill()
	}
	var g1, fd1, pipes int
	for i := 0; i < 40; i++ { // tối đa 2 s cho goroutine/fd về
		time.Sleep(50 * time.Millisecond)
		// P9-7: splice(2) của Go cầm pipe trong sync.Pool (internal/poll
		// splicePipePool), đóng bằng finalizer ⇒ cần GC mới trả fd. Không phải
		// rò: đếm riêng `pipe:` để thấy, và GC trước khi kết luận.
		runtime.GC()
		g1, fd1, pipes = runtime.NumGoroutine(), countFDs(), countPipes()
		if g1 <= gBase+2 && fd1 <= fdBase {
			break
		}
	}
	fmt.Printf("invariant (b): goroutine sau Close proxy + kill backend: %d (nền trước mọi thứ %d, ±2)\n", g1, gBase)
	fmt.Printf("invariant (c): fd sau Close proxy + kill backend: %d (nền trước mọi thứ %d, ±0; trong đó pipe splice: %d)\n", fd1, fdBase, pipes)
	fmt.Printf("invariant (d): treo quá %s: %d; đóng không trả gì (io-nohead): %d; body cụt (io-body): %d; dial lỗi: %d\n",
		clientTO, sum.ByKind["timeout"], sum.ByKind["io-nohead"], sum.ByKind["io-body"], sum.ByKind["dial"])
	if g1 > gBase+2 || fd1 > fdBase || sum.ByKind["timeout"] > 0 {
		ok = false
	}
	fmt.Println("invariant (a): không panic — tới được dòng này")
	if !ok {
		fmt.Println("FAIL")
		os.Exit(1)
	}
	fmt.Println("PASS")
}

func countFDs() int {
	es, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(es)
}

// countPipes: fd là pipe (splice của Go giữ theo cặp trong pool).
func countPipes() int {
	es, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	n := 0
	for _, e := range es {
		if l, err := os.Readlink("/proc/self/fd/" + e.Name()); err == nil && strings.HasPrefix(l, "pipe:") {
			n++
		}
	}
	return n
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "chaoslab:", err)
	os.Exit(1)
}
