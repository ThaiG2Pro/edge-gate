// slowlab — Slowloris thật cho phase 7 (G2, G3) và phase 8 (P8-4). In-process: một fixture
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
	"crypto/tls"
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
	"github.com/thaivro/edgegate/internal/lb"
	"github.com/thaivro/edgegate/internal/loadgen"
	"github.com/thaivro/edgegate/internal/proxy"
	"github.com/thaivro/edgegate/internal/tlsx"
)

var rawClientHello = []byte{
	0x16, 0x03, 0x01, 0x00, 0x30, // Handshake record, length 48
	0x01, 0x00, 0x00, 0x2c, // ClientHello, length 44
	0x03, 0x03, // TLS 1.2
	// Random (32 bytes)
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
	16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31,
	0x00,                   // Session ID length 0
	0x00, 0x02, 0x00, 0x9c, // Cipher suites (1 suite: TLS_RSA_WITH_AES_128_GCM_SHA256)
	0x01, 0x00, // Compression methods (1 method: null)
	0x00, 0x00, // Extensions length 0
}

func main() {
	conns := flag.Int("conns", 500, "số connection Slowloris")
	every := flag.Duration("byte-every", 10*time.Second, "mỗi bao lâu gửi một dòng header / byte ClientHello")
	src := flag.String("src", "127.0.0.2", "IP nguồn của attacker")
	dur := flag.Duration("duration", 30*time.Second, "thời gian probe dưới tấn công")
	hold := flag.Duration("hold", 12*time.Second, "attacker ngừng gửi nhưng giữ socket bao lâu")
	rate := flag.Float64("probe-rate", 50, "probe rps (open-loop)")
	headerTO := flag.Duration("header-timeout", 10*time.Second, "Limits.HeaderTimeout")
	maxConns := flag.Int("max-conns", 0, "Config.MaxConns (D3) — 0 = không trần")
	perIP := flag.Int("max-conns-per-ip", 0, "Config.MaxConnsPerIP (P7-2) — 0 = không trần")
	tarpit := flag.Duration("tarpit", 0, "Config.PerIPTarpit (P7-2b) — giữ connection bao lâu trước khi đóng")
	inflight := flag.Int("max-inflight", 0, "Shed.MaxInflight (D7) — 0 = tắt")
	target := flag.String("target", "proxy", "proxy | null — null: attacker nối vào listener chỉ accept rồi giữ (không goroutine, không buffer) ⇒ hiệu chuẩn phần bộ nhớ của CHÍNH attacker")
	useTLS := flag.Bool("tls", false, "chạy proxy và attacker qua TLS (P8-4)")
	dribbleHello := flag.Bool("dribble-hello", false, "attacker gửi TLS ClientHello nhỏ giọt từng byte (P8-4)")
	flag.Parse()

	up, stop, err := fixture.ListenAndServe("127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	defer stop()
	lim := httpx.DefaultLimits()
	lim.HeaderTimeout = *headerTO

	var tlsClientCfg *tls.Config
	var serverTLSStore *tlsx.CertStore
	if *useTLS {
		ca, err := tlsx.NewCA()
		if err != nil {
			fatal(err)
		}
		c, err := ca.LeafTLS([]string{"a.test"}, tlsx.ECDSA, 42)
		if err != nil {
			fatal(err)
		}
		serverTLSStore, err = tlsx.NewCertStore([]tlsx.Entry{{Names: []string{"a.test"}, Cert: &c}})
		if err != nil {
			fatal(err)
		}
		tlsClientCfg = &tls.Config{
			ServerName: "a.test",
			RootCAs:    ca.Pool,
		}
	}

	pcfg := proxy.Config{
		Listen:        "127.0.0.1:0",
		Upstream:      up,
		Limits:        lim,
		MaxConns:      *maxConns,
		MaxConnsPerIP: *perIP,
		PerIPTarpit:   *tarpit,
		Shed:          proxy.ShedConfig{MaxInflight: *inflight, MaxQueue: *inflight},
		Logf:          func(string, ...any) {},
	}
	if *useTLS {
		pcfg.TLS = &proxy.TLSConfig{Store: serverTLSStore}
		pcfg.VHosts = []proxy.VHost{{Names: []string{"a.test"}, Upstreams: []string{up}, LB: lb.Config{Health: lb.HealthConfig{Disabled: true}}}}
	}

	srv := proxy.New(pcfg)
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
	fmt.Printf("slowlab: conns=%d byte-every=%s src=%s duration=%s hold=%s probe=%.0f rps header-timeout=%s (áp dụng: %v) max-conns=%d max-conns-per-ip=%d tarpit=%s max-inflight=%d tls=%v dribble-hello=%v target=%s\n",
		*conns, *every, *src, *dur, *hold, *rate, *headerTO, headerTimeoutApplied(), *maxConns, *perIP, *tarpit, *inflight, *useTLS, *dribbleHello, *target)
	fmt.Println("  (probe open-loop, latency từ giờ hẹn; loopback; proxy + attacker + probe cùng tiến trình, không ghim core)")

	probe := func(d time.Duration) (loadgen.Summary, time.Duration) {
		t0 := time.Now()
		ss := loadgen.Run(loadgen.Config{
			Addr:     addr,
			Rate:     *rate,
			Duration: d,
			Workers:  64,
			Timeout:  5 * time.Second,
			LocalIP:  "127.0.0.1",
			TLS:      tlsClientCfg,
			Request: func(i int) string {
				if *useTLS {
					return "GET /hello HTTP/1.1\r\nHost: a.test\r\n\r\n"
				}
				return "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n"
			},
		})
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
				rawConn, err := d.Dial("tcp", atkAddr)
				if err != nil {
					dialErr.Add(1)
					time.Sleep(100 * time.Millisecond)
					continue
				}

				if *dribbleHello {
					// Dribble TLS ClientHello byte by byte
					closed := make(chan struct{})
					go func() { io.Copy(io.Discard, rawConn); close(closed) }()
					t := time.NewTicker(*every)
					helloIdx := 0
				dribbleLoop:
					for {
						select {
						case <-stopAtk:
							t.Stop()
							rawConn.Close()
							return
						case <-closed:
							break dribbleLoop
						case <-t.C:
							if !quiet.Load() && helloIdx < len(rawClientHello) {
								if _, err := rawConn.Write([]byte{rawClientHello[helloIdx]}); err != nil {
									break dribbleLoop
								}
								helloIdx++
							}
						}
					}
					t.Stop()
					rawConn.Close()
					if quiet.Load() {
						return
					}
					continue
				}

				var c io.ReadWriteCloser = rawConn
				if *target != "null" && *useTLS {
					tc := tls.Client(rawConn, tlsClientCfg)
					if err := tc.Handshake(); err != nil {
						rawConn.Close()
						dialErr.Add(1)
						time.Sleep(50 * time.Millisecond)
						continue
					}
					c = tc
				}

				closed := make(chan struct{})
				go func() { io.Copy(io.Discard, c); close(closed) }() // 408 + đóng ⇒ biết ngay
				io.WriteString(c, "GET / HTTP/1.1\r\nHost: a.test\r\n")
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

