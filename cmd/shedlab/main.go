// shedlab — load shedding dưới quá tải (phase 7 G7). OPEN-LOOP bắt buộc:
// closed-loop dưới 2x tải tự giảm về 1x (client chờ) nên không bao giờ thấy
// quá tải (phase 0 bài 3, nợ P6-1).
//
// 4 backend fixture.Sim, mỗi cái xử lý tối đa -backend-conc request song song
// × -service ⇒ capacity = 4·conc/service. Generator bắn -x lần capacity trong
// -duration, hai chế độ: không shed, rồi shed (MaxInflight/MaxQueue/QueueTimeout).
// In tỉ lệ từng status, percentile của 200 (từ giờ hẹn), goodput, và p99 của
// 200 THEO TỪNG GIÂY — hàng đợi không trần làm p99 tăng theo thời gian chạy.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ThaiG2Pro/edge-gate/internal/fixture"
	"github.com/ThaiG2Pro/edge-gate/internal/loadgen"
	"github.com/ThaiG2Pro/edge-gate/internal/proxy"
)

func main() {
	conc := flag.Int("backend-conc", 4, "request song song tối đa mỗi backend")
	service := flag.Duration("service", 10*time.Millisecond, "service time mỗi request ở backend")
	x := flag.Float64("x", 2, "tải = x × capacity")
	dur := flag.Duration("duration", 10*time.Second, "thời gian bắn")
	inflight := flag.Int("inflight", 16, "Shed.MaxInflight khi shed")
	queue := flag.Int("queue", 16, "Shed.MaxQueue khi shed")
	qto := flag.Duration("queue-timeout", 50*time.Millisecond, "Shed.QueueTimeout")
	modes := flag.String("modes", "noshed,shed", "chế độ, phẩy")
	workers := flag.Int("workers", 256, "connection tối đa của generator")
	flag.Parse()

	capacity := 4 * float64(*conc) / service.Seconds()
	rate := *x * capacity
	fmt.Printf("shedlab: 4 backend × conc %d × %s ⇒ capacity %.0f rps; tải %.0f rps (%.1fx) trong %s; shed inflight=%d queue=%d queue-timeout=%s; generator %d worker\n",
		*conc, *service, capacity, rate, *x, *dur, *inflight, *queue, *qto, *workers)
	fmt.Println("  (OPEN-LOOP, latency từ giờ hẹn; loopback; 4 backend + proxy + generator cùng tiến trình, không ghim core)")

	for _, mode := range strings.Split(*modes, ",") {
		var ups []string
		var stops []func()
		for i := 0; i < 4; i++ {
			sim := fixture.NewSim(fmt.Sprintf("b%d", i), *service, 0)
			sim.SetConcurrency(*conc)
			a, stop, err := fixture.ListenAndServeSim("127.0.0.1:0", sim)
			if err != nil {
				fatal(err)
			}
			ups, stops = append(ups, a), append(stops, stop)
		}
		cfg := proxy.Config{Listen: "127.0.0.1:0", Upstreams: ups, Logf: func(string, ...any) {}}
		cfg.LB.Algo = "leastconn"
		cfg.LB.Logf = cfg.Logf
		if mode == "shed" {
			cfg.Shed = proxy.ShedConfig{MaxInflight: *inflight, MaxQueue: *queue, QueueTimeout: *qto}
		}
		srv := proxy.New(cfg)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fatal(err)
		}
		go srv.Serve(ln)
		t0 := time.Now()
		ss := loadgen.Run(loadgen.Config{Addr: ln.Addr().String(), Rate: rate, Duration: *dur, Workers: *workers, Timeout: 15 * time.Second})
		el := time.Since(t0)
		sum := loadgen.Summarize(ss)
		fmt.Println()
		loadgen.Print(os.Stdout, mode, sum, el) // goodput chia thời gian THẬT tới khi xong hết, không chia lịch
		fmt.Printf("%-14s bắn xong lịch sau %s (lịch %s); proxy %+v\n", "", el.Round(time.Millisecond), *dur, srv.ResilienceStats())
		// 503 nhanh cỡ nào: shed phải trả lời ngay, không bắt client chờ.
		var fast []time.Duration
		for _, s := range ss {
			if s.Kind == "ok" && s.Status == 503 {
				fast = append(fast, s.Latency())
			}
		}
		if len(fast) > 0 {
			sort.Slice(fast, func(i, j int) bool { return fast[i] < fast[j] })
			fmt.Printf("%-14s 503: p50 %s p99 %s\n", "", fast[len(fast)/2].Round(10*time.Microsecond), fast[(len(fast)-1)*99/100].Round(10*time.Microsecond))
		}
		perSec(ss, *dur)
		srv.Close()
		for _, stop := range stops {
			stop()
		}
	}
}

// perSec: p99 của 200 theo giây của GIỜ HẸN.
func perSec(ss []loadgen.Sample, dur time.Duration) {
	if len(ss) == 0 {
		return
	}
	t0 := ss[0].Sched
	buckets := make([][]time.Duration, int(dur.Seconds())+1)
	for _, s := range ss {
		if s.Kind == "ok" && s.Status == 200 {
			i := int(s.Sched.Sub(t0).Seconds())
			buckets[i] = append(buckets[i], s.Latency())
		}
	}
	var parts []string
	for i, b := range buckets {
		if len(b) == 0 {
			continue
		}
		sort.Slice(b, func(x, y int) bool { return b[x] < b[y] })
		parts = append(parts, fmt.Sprintf("s%d:%s", i, b[(len(b)-1)*99/100].Round(time.Millisecond)))
	}
	fmt.Printf("%-14s p99 200 theo giây: %s\n", "", strings.Join(parts, " "))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "shedlab:", err)
	os.Exit(1)
}
