// ratelab — rate limit per-IP sau ranh giới tin cậy (phase 7 G4).
//
// Hai pha, cùng một proxy (token bucket -rate/s, burst -burst):
//  1. -ips IP nguồn thật (127.0.1.1 …; loopback nhận mọi 127/8), mỗi IP một
//     generator open-loop -per-ip rps trong -duration ⇒ mỗi IP phải được
//     ≈ rate·duration + burst, còn lại 429.
//  2. spoof: MỘT peer không tin (127.0.0.3) gửi X-Forwarded-For ngẫu nhiên mỗi
//     request ⇒ phải bị đếm chung một bucket (I6). `-tags nodefense7` (khoá =
//     XFF thô) ⇒ spoof qua gần hết ⇒ chương trình thoát mã 1 (make *-nodefense).
package main

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"sync"
	"time"

	"github.com/thaivro/edgegate/internal/fixture"
	"github.com/thaivro/edgegate/internal/loadgen"
	"github.com/thaivro/edgegate/internal/proxy"
)

func main() {
	ips := flag.Int("ips", 20, "số IP nguồn thật")
	rate := flag.Float64("rate", 50, "token/s mỗi IP")
	burst := flag.Float64("burst", 10, "burst mỗi IP")
	perIP := flag.Float64("per-ip", 100, "rps mỗi IP (open-loop)")
	dur := flag.Duration("duration", 5*time.Second, "thời gian mỗi pha")
	flag.Parse()

	up, stop, err := fixture.ListenAndServe("127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	defer stop()
	srv := proxy.New(proxy.Config{Listen: "127.0.0.1:0", Upstream: up,
		RateLimit: proxy.RateLimitConfig{Rate: *rate, Burst: *burst}, Logf: func(string, ...any) {}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	go srv.Serve(ln)
	defer srv.Close()
	addr := ln.Addr().String()
	want := *rate*dur.Seconds() + *burst
	fmt.Printf("ratelab: ips=%d rate=%.0f/s burst=%.0f per-ip=%.0f rps duration=%s ⇒ kỳ vọng mỗi IP ≈ %.0f cái 200 / %.0f\n",
		*ips, *rate, *burst, *perIP, *dur, want, *perIP*dur.Seconds())
	fmt.Println("  (open-loop, loopback, proxy + generator cùng tiến trình)")

	// --- pha 1: IP thật ---------------------------------------------------------
	res := make([]loadgen.Summary, *ips)
	var wg sync.WaitGroup
	for i := 0; i < *ips; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ss := loadgen.Run(loadgen.Config{Addr: addr, Rate: *perIP, Duration: *dur, Workers: 16, LocalIP: fmt.Sprintf("127.0.1.%d", i+1)})
			res[i] = loadgen.Summarize(ss)
		}()
	}
	wg.Wait()
	lo, hi, other := 1<<30, 0, 0
	for _, s := range res {
		lo, hi = min(lo, s.ByStatus[200]), max(hi, s.ByStatus[200])
		other += s.N - s.ByStatus[200] - s.ByStatus[429]
	}
	fmt.Printf("ip-that        mỗi IP 200: min %d max %d (kỳ vọng %.0f ± 5 %%); khác 200/429: %d\n", lo, hi, want, other)

	// --- pha 2: spoof XFF từ một peer không tin ---------------------------------
	ss := loadgen.Run(loadgen.Config{Addr: addr, Rate: *perIP, Duration: *dur, Workers: 16, LocalIP: "127.0.0.3",
		Request: func(int) string {
			return fmt.Sprintf("GET /hello HTTP/1.1\r\nHost: lab\r\nX-Forwarded-For: %d.%d.%d.%d\r\n\r\n",
				rand.IntN(223)+1, rand.IntN(256), rand.IntN(256), rand.IntN(256))
		}})
	sp := loadgen.Summarize(ss)
	fmt.Printf("spoof-xff      peer 127.0.0.3, XFF ngẫu nhiên mỗi request: 200 = %d / %d (kỳ vọng %.0f ± 5 %%), 429 = %d\n",
		sp.ByStatus[200], sp.N, want, sp.ByStatus[429])
	st := srv.ResilienceStats()
	fmt.Printf("proxy          rate-limited %d, khoá đang nhớ %d\n", st.RateLimited, st.LimiterKeys)

	ok := float64(lo) >= want*0.95 && float64(hi) <= want*1.05 && other == 0 &&
		float64(sp.ByStatus[200]) <= want*1.05
	if !ok {
		fmt.Println("FAIL: rate limit per-IP sai (ngoài ± 5 %) hoặc XFF giả lọt qua ranh giới tin cậy")
		os.Exit(1)
	}
	fmt.Println("PASS")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ratelab:", err)
	os.Exit(1)
}
