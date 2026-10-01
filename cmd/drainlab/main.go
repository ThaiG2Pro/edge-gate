// drainlab — rolling restart (phase 7 G8, D8, D9). Hai instance proxy cùng
// port bằng SO_REUSEPORT: mỗi -every, mở instance MỚI trên port đó rồi
// Drain(-drain) instance CŨ. Generator open-loop -rate rps, keep-alive, nối
// lại khi thấy Connection: close hoặc EOF.
//
// Đếm theo loại mất:
//   - io-nohead: request đã ghi lên connection mà không nhận byte nào — cửa sổ
//     idle-close (server đóng connection rỗi đúng lúc client ghi). Server
//     không tránh được; client retry request idempotent khi 0 byte thì vá được
//     (-retry, đúng D4 phase 5 phía client).
//   - dial: connection nằm trong backlog của listener cũ lúc nó đóng ⇒ RST.
//
// Chạy hai lượt: không retry, rồi retry (G8 b).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/thaivro/edgegate/internal/fixture"
	"github.com/thaivro/edgegate/internal/loadgen"
	"github.com/thaivro/edgegate/internal/proxy"
)

func main() {
	restarts := flag.Int("restarts", 20, "số lần restart")
	every := flag.Duration("every", 500*time.Millisecond, "khoảng giữa hai restart")
	drain := flag.Duration("drain", 5*time.Second, "Drain timeout")
	rate := flag.Float64("rate", 2000, "rps open-loop")
	workers := flag.Int("workers", 64, "connection tối đa của generator")
	service := flag.Duration("service", 5*time.Millisecond, "service time upstream")
	runs := flag.String("runs", "noretry,retry", "lượt, phẩy")
	grace := flag.Duration("grace", 0, "Config.DrainIdleGrace (D8′): 0 = đóng connection rỗi ngay")
	flag.Parse()

	sim := fixture.NewSim("up", *service, 0)
	up, stop, err := fixture.ListenAndServeSim("127.0.0.1:0", sim)
	if err != nil {
		fatal(err)
	}
	defer stop()
	fmt.Printf("drainlab: restarts=%d every=%s drain=%s grace=%s rate=%.0f workers=%d service=%s\n", *restarts, *every, *drain, *grace, *rate, *workers, *service)
	fmt.Println("  (open-loop, loopback, SO_REUSEPORT, mọi instance cùng tiến trình)")

	for _, run := range strings.Split(*runs, ",") {
		newSrv := func(addr string) (*proxy.Server, string) {
			ln, err := proxy.Listen(addr, true)
			if err != nil {
				fatal(err)
			}
			s := proxy.New(proxy.Config{Listen: addr, Upstream: up, DrainIdleGrace: *grace, Logf: func(string, ...any) {}})
			go s.Serve(ln)
			return s, ln.Addr().String()
		}
		cur, addr := newSrv("127.0.0.1:0")
		dur := time.Duration(*restarts+2) * *every
		var ss []loadgen.Sample
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			ss = loadgen.Run(loadgen.Config{Addr: addr, Rate: *rate, Duration: dur, Workers: *workers, Timeout: 10 * time.Second,
				RetryZeroByte: run == "retry"})
		}()
		var forced, drainedIdle int64
		var drainMax time.Duration
		for i := 0; i < *restarts; i++ {
			time.Sleep(*every)
			next, _ := newSrv(addr)
			t0 := time.Now()
			forced += int64(cur.Drain(*drain))
			drainMax = max(drainMax, time.Since(t0))
			drainedIdle += cur.ResilienceStats().DrainedIdle
			cur = next
		}
		wg.Wait()
		cur.Close()
		sum := loadgen.Summarize(ss)
		closes, lostNew := 0, 0
		for _, s := range ss {
			if s.Close {
				closes++
			}
			if s.Kind == "io-nohead" && !s.Reused {
				lostNew++ // connection VỪA dial mà không có byte nào: RST từ backlog listener cũ (G8 c)
			}
		}
		fmt.Println()
		loadgen.Print(os.Stdout, run, sum, dur)
		fmt.Printf("%-14s io-nohead trên connection vừa dial (backlog RST): %d\n", "", lostNew)
		fmt.Printf("%-14s mất: io-nohead %d, io-body %d, dial %d, timeout %d · retried %d · response mang Connection: close %d · drain: idle đóng %d, cưỡng bức %d, lâu nhất %s\n", "",
			sum.ByKind["io-nohead"], sum.ByKind["io-body"], sum.ByKind["dial"], sum.ByKind["timeout"], sum.Retried, closes,
			drainedIdle, forced, drainMax.Round(time.Millisecond))
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "drainlab:", err)
	os.Exit(1)
}
