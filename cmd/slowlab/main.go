// slowlab — Slowloris thật cho phase 7 (G2, G3). In-process: một fixture
// upstream, proxy (internal/proxy), attacker và probe.
//
// Attacker: -conns connection từ -src (mặc định 127.0.0.2; loopback nhận mọi
// 127/8), mỗi cái gửi "GET / HTTP/1.1\r\n" rồi mỗi -byte-every một dòng header
// "X-a: b\r\n" — head không bao giờ xong. Bị đóng thì nối lại ngay (đếm).
// Probe: open-loop -probe-rate rps từ 127.0.0.1 (internal/loadgen, latency từ
// giờ hẹn). Ba pha: baseline (không attacker) → attack → hold (attacker NGỪNG
// gửi nhưng giữ socket) để xem proxy còn giữ bao nhiêu connection.
//
// Bộ nhớ đo bằng runtime.MemStats của CẢ tiến trình (proxy + attacker cùng
// tiến trình): HeapInuse + StackInuse. Phía attacker mỗi conn tốn một goroutine
// + 2 KiB buffer — trừ ước lượng đó khi đọc.
//
// `-tags nodefense7` tắt HeaderTimeout (G2 b).
package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thaivro/edgegate/internal/fixture"
	"github.com/thaivro/edgegate/internal/httpx"
	"github.com/thaivro/edgegate/internal/loadgen"
	"github.com/thaivro/edgegate/internal/proxy"
)

func main() {
	conns := flag.Int("conns", 500, "số connection Slowloris")
	every := flag.Duration("byte-every", 10*time.Second, "mỗi bao lâu gửi một dòng header")
	src := flag.String("src", "127.0.0.2", "IP nguồn của attacker")
	dur := flag.Duration("duration", 30*time.Second, "thời gian probe dưới tấn công")
	hold := flag.Duration("hold", 12*time.Second, "attacker ngừng gửi nhưng giữ socket bao lâu")
	rate := flag.Float64("probe-rate", 50, "probe rps (open-loop)")
	headerTO := flag.Duration("header-timeout", 10*time.Second, "Limits.HeaderTimeout")
	maxConns := flag.Int("max-conns", 0, "Config.MaxConns (D3) — 0 = không trần")
	inflight := flag.Int("max-inflight", 0, "Shed.MaxInflight (D7) — 0 = tắt")
	target := flag.String("target", "proxy", "proxy | null — null: attacker nối vào listener chỉ accept rồi giữ (không goroutine, không buffer) ⇒ hiệu chuẩn phần bộ nhớ của CHÍNH attacker")
	flag.Parse()

	up, stop, err := fixture.ListenAndServe("127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	defer stop()
	lim := httpx.DefaultLimits()
	lim.HeaderTimeout = *headerTO
	srv := proxy.New(proxy.Config{Listen: "127.0.0.1:0", Upstream: up, Limits: lim, MaxConns: *maxConns,
		Shed: proxy.ShedConfig{MaxInflight: *inflight, MaxQueue: *inflight}, Logf: func(string, ...any) {}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	go srv.Serve(ln)
	addr := ln.Addr().String()
	atkAddr := addr
	var parked []net.Conn
	var pmu sync.Mutex
	if *target == "null" {
		nl, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fatal(err)
		}
		go func() {
			for {
				c, err := nl.Accept()
				if err != nil {
					return
				}
				pmu.Lock()
				parked = append(parked, c)
				pmu.Unlock()
			}
		}()
		atkAddr = nl.Addr().String()
	}
	fmt.Printf("slowlab: conns=%d byte-every=%s src=%s duration=%s hold=%s probe=%.0f rps header-timeout=%s (áp dụng: %v) max-conns=%d max-inflight=%d\n",
		*conns, *every, *src, *dur, *hold, *rate, *headerTO, headerTimeoutApplied(), *maxConns, *inflight)
	fmt.Println("  (probe open-loop, latency từ giờ hẹn; loopback; proxy + attacker + probe cùng tiến trình, không ghim core)")

	probe := func(d time.Duration) (loadgen.Summary, time.Duration) {
		t0 := time.Now()
		ss := loadgen.Run(loadgen.Config{Addr: addr, Rate: *rate, Duration: d, Workers: 64, Timeout: 5 * time.Second, LocalIP: "127.0.0.1"})
		return loadgen.Summarize(ss), time.Since(t0)
	}
	mem := func() uint64 {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return m.HeapInuse + m.StackInuse
	}

	sum, el := probe(5 * time.Second)
	loadgen.Print(os.Stdout, "baseline", sum, el)
	m0, c0 := mem(), srv.ResilienceStats().ConnsActive

	// --- attacker -----------------------------------------------------------
	var reconnects, dialErr atomic.Int64
	var quiet atomic.Bool // pha hold: ngừng gửi byte
	stopAtk := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < *conns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first := true
			for {
				select {
				case <-stopAtk:
					return
				default:
				}
				if !first {
					reconnects.Add(1)
				}
				first = false
				d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(*src)}, Timeout: 5 * time.Second}
				c, err := d.Dial("tcp", atkAddr)
				if err != nil {
					dialErr.Add(1)
					time.Sleep(100 * time.Millisecond)
					continue
				}
				closed := make(chan struct{})
				go func() { io.Copy(io.Discard, c); close(closed) }() // 408 + đóng ⇒ biết ngay
				io.WriteString(c, "GET / HTTP/1.1\r\n")
				t := time.NewTicker(*every)
			loop:
				for {
					select {
					case <-stopAtk:
						t.Stop()
						c.Close()
						return
					case <-closed:
						break loop
					case <-t.C:
						if !quiet.Load() {
							if _, err := io.WriteString(c, "X-a: b\r\n"); err != nil {
								break loop
							}
						}
					}
				}
				t.Stop()
				c.Close()
				if quiet.Load() {
					return // pha hold: không nối lại — đếm xem proxy tự đóng được bao nhiêu
				}
			}
		}()
	}
	time.Sleep(2 * time.Second) // để attacker nối đủ
	c1 := srv.ResilienceStats().ConnsActive
	if *target == "null" {
		pmu.Lock()
		c1 = int64(len(parked))
		pmu.Unlock()
	}
	m1 := mem()
	fmt.Printf("attack:        proxy giữ %d connection (trước %d); bộ nhớ tiến trình +%.1f MiB ⇒ %.1f KiB / connection (gồm cả phía attacker)\n",
		c1, c0, float64(int64(m1)-int64(m0))/(1<<20), float64(int64(m1)-int64(m0))/1024/float64(max(c1-c0, 1)))
	sum, el = probe(*dur)
	loadgen.Print(os.Stdout, "probe/attack", sum, el)
	fmt.Printf("               attacker: reconnect %d, dial lỗi %d; proxy giữ %d connection lúc hết probe\n",
		reconnects.Load(), dialErr.Load(), srv.ResilienceStats().ConnsActive)

	quiet.Store(true)
	time.Sleep(*hold)
	fmt.Printf("hold %s:      attacker ngừng gửi, giữ socket ⇒ proxy còn giữ %d connection\n", *hold, srv.ResilienceStats().ConnsActive)
	close(stopAtk)
	wg.Wait()
	srv.Close()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "slowlab:", err)
	os.Exit(1)
}
