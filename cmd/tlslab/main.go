// tlslab — đo TLS termination của phase 8 (G4, G6, G7, G8). In-process: proxy
// (internal/proxy, listener TLS bằng crypto/tls), upstream fixture, client
// crypto/tls verify thật bằng CA lab (internal/tlsx). Cùng tiến trình ⇒ thời
// gian handshake là TỔNG CPU hai phía (server ký + client verify).
//
// Mode:
//
//	handshake  G6: tuần tự -n lần dial + handshake, mỗi biến thể (ECDSA/RSA ×
//	           TLS 1.3/1.2 × full/resumed); + rps "mỗi request một connection"
//	           TLS vs plaintext.
//	rtt        G7: thời gian tới byte đầu response trên connection MỚI, chia cho
//	           RTT ĐO bằng TCP connect trơn (chạy sau `make rtt-up`).
//	upstream   G8: proxy plaintext ↔ upstream thường / TLS, pool bật / tắt;
//	           khoản tiết kiệm của pool theo ms và theo RTT đo.
//	reload     G4 dưới tải: -conns connection keep-alive, reload mỗi -every.
package main

import (
	"bufio"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ThaiG2Pro/edge-gate/internal/fixture"
	"github.com/ThaiG2Pro/edge-gate/internal/httpx"
	"github.com/ThaiG2Pro/edge-gate/internal/lb"
	"github.com/ThaiG2Pro/edge-gate/internal/proxy"
	"github.com/ThaiG2Pro/edge-gate/internal/tlsx"
)

var noHealth = lb.Config{Health: lb.HealthConfig{Disabled: true}}

// kex: CurvePreferences của client (nil = mặc định Go). Server chọn trong cái
// client đề nghị, nên đặt ở client là đủ quyết key exchange của cả hai phía.
var kex []tls.CurveID

func main() {
	mode := flag.String("mode", "handshake", "handshake | rtt | upstream | reload")
	n := flag.Int("n", 500, "số lần đo mỗi biến thể")
	conns := flag.Int("conns", 64, "reload: số connection keep-alive")
	dur := flag.Duration("duration", 10*time.Second, "reload: thời gian")
	every := flag.Duration("every", 100*time.Millisecond, "reload: khoảng giữa hai lần reload")
	kexFlag := flag.String("kex", "default", "key exchange phía client: default (Go 1.26: có X25519MLKEM768 lai hậu lượng tử) | x25519 | p256")
	flag.Parse()
	switch *kexFlag {
	case "x25519":
		kex = []tls.CurveID{tls.X25519}
	case "p256":
		kex = []tls.CurveID{tls.CurveP256}
	case "default":
	default:
		fatal(fmt.Errorf("kex %q", *kexFlag))
	}

	ca, err := tlsx.NewCA()
	if err != nil {
		fatal(err)
	}
	fmt.Printf("tlslab: mode=%s n=%d kex=%s\n", *mode, *n, *kexFlag)
	fmt.Println("  (loopback; proxy + upstream + client cùng tiến trình ⇒ thời gian handshake là CPU hai phía cộng lại; không ghim core)")
	switch *mode {
	case "handshake":
		handshake(ca, *n)
	case "rtt":
		rtt(ca, *n)
	case "upstream":
		upstream(ca, *n)
	case "reload":
		reload(ca, *conns, *dur, *every)
	default:
		fatal(fmt.Errorf("mode %q", *mode))
	}
}

// startProxy: listener TLS (store != nil) hoặc thường, một vhost a.test → up.
func startProxy(store *tlsx.CertStore, up string, mut func(*proxy.Config)) (*proxy.Server, string) {
	cfg := proxy.Config{Listen: "127.0.0.1:0", Logf: func(string, ...any) {}}
	if store != nil {
		cfg.TLS = &proxy.TLSConfig{Store: store}
		cfg.VHosts = []proxy.VHost{{Names: []string{"a.test"}, Upstreams: []string{up}, LB: noHealth}}
	} else {
		cfg.Upstreams, cfg.LB = []string{up}, noHealth
	}
	if mut != nil {
		mut(&cfg)
	}
	s := proxy.New(cfg)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	go s.Serve(ln)
	return s, ln.Addr().String()
}

func storeFor(ca *tlsx.CA, kt tlsx.KeyType) *tlsx.CertStore {
	c, err := ca.LeafTLS([]string{"a.test"}, kt, 42)
	if err != nil {
		fatal(err)
	}
	s, err := tlsx.NewCertStore([]tlsx.Entry{{Names: []string{"a.test"}, Cert: &c}})
	if err != nil {
		fatal(err)
	}
	return s
}

// get gửi một GET trên c và đọc hết; trả thời điểm byte đầu response tới.
func get(c net.Conn, br *bufio.Reader) (first time.Time, err error) {
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err = io.WriteString(c, "GET /hello HTTP/1.1\r\nHost: a.test\r\n\r\n"); err != nil {
		return
	}
	if _, err = br.Peek(1); err != nil {
		return
	}
	first = time.Now()
	resp, err := httpx.ReadResponse(br, httpx.DefaultLimits(), "GET")
	if err != nil {
		return
	}
	_, err = io.Copy(io.Discard, resp.Body)
	return
}

type variant struct {
	name    string
	kt      tlsx.KeyType
	maxVer  uint16
	resumed bool
}

var variants = []variant{
	{"ecdsa-1.3-full", tlsx.ECDSA, tls.VersionTLS13, false},
	{"ecdsa-1.3-resumed", tlsx.ECDSA, tls.VersionTLS13, true},
	{"ecdsa-1.2-full", tlsx.ECDSA, tls.VersionTLS12, false},
	{"ecdsa-1.2-resumed", tlsx.ECDSA, tls.VersionTLS12, true},
	{"rsa-1.3-full", tlsx.RSA, tls.VersionTLS13, false},
	{"rsa-1.3-resumed", tlsx.RSA, tls.VersionTLS13, true},
}

func clientCfg(ca *tlsx.CA, v variant) *tls.Config {
	cfg := &tls.Config{ServerName: "a.test", RootCAs: ca.Pool, MaxVersion: v.maxVer, NextProtos: []string{"http/1.1"}, CurvePreferences: kex}
	if v.resumed {
		cfg.ClientSessionCache = tls.NewLRUClientSessionCache(8)
	}
	return cfg
}

// prime: một connection đầy đủ để client nhận session ticket (TLS 1.3 gửi
// ticket SAU handshake — client chỉ đọc nó ở lần Read đầu, nên phải GET một lần).
func prime(addr string, cfg *tls.Config) {
	c, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		fatal(err)
	}
	get(c, bufio.NewReader(c))
	c.Close()
}

func handshake(ca *tlsx.CA, n int) {
	up, stop, err := fixture.ListenAndServe("127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	defer stop()
	stores := map[tlsx.KeyType]string{}
	for _, kt := range []tlsx.KeyType{tlsx.ECDSA, tlsx.RSA} {
		s, addr := startProxy(storeFor(ca, kt), up, nil)
		defer s.Close()
		stores[kt] = addr
	}
	fmt.Printf("\n%-20s %9s %9s %9s %9s %10s   %s\n", "handshake (tuần tự)", "p50", "p90", "p99", "mean", "CPU/hs", "resumed / n")
	for _, v := range variants {
		addr := stores[v.kt]
		cfg := clientCfg(ca, v)
		if v.resumed {
			prime(addr, cfg)
		}
		var ds []time.Duration
		resumed := 0
		lastCurve := ""
		cpu0 := cpuNow()
		for i := 0; i < n; i++ {
			t0 := time.Now()
			c, err := tls.Dial("tcp", addr, cfg)
			if err != nil {
				fatal(fmt.Errorf("%s: %w", v.name, err))
			}
			ds = append(ds, time.Since(t0))
			lastCurve = c.ConnectionState().CurveID.String()
			if c.ConnectionState().DidResume {
				resumed++
			}
			// Không GET: client dùng lại ticket đã có trong cache (prime). Bản đầu
			// GET mỗi lần để nhận ticket mới ⇒ CPU của GET cộng vào cột resumed ⇒
			// resumed trông đắt hơn full. Cột "resumed / n" kiểm ticket cũ vẫn dùng được.
			c.Close()
		}
		cpu := (cpuNow() - cpu0) / time.Duration(n)
		p := pcts(ds)
		fmt.Printf("%-20s %9s %9s %9s %9s %10s   %d / %d  %s\n", v.name, rd(p[0]), rd(p[1]), rd(p[2]), rd(p[3]), rd(cpu), resumed, n, lastCurve)
	}

	// rps "mỗi request một connection mới": TLS (ECDSA 1.3 full) vs plaintext.
	plainS, plainAddr := startProxy(nil, up, nil)
	defer plainS.Close()
	fmt.Printf("\n%-20s %9s %9s\n", "conn mới / request", "rps", "p50")
	for _, tc := range []struct {
		name string
		dial func() (net.Conn, error)
	}{
		{"plaintext", func() (net.Conn, error) { return net.Dial("tcp", plainAddr) }},
		{"tls-ecdsa-1.3", func() (net.Conn, error) {
			return tls.Dial("tcp", stores[tlsx.ECDSA], clientCfg(ca, variants[0]))
		}},
		{"tls-rsa-1.3", func() (net.Conn, error) {
			return tls.Dial("tcp", stores[tlsx.RSA], clientCfg(ca, variants[4]))
		}},
	} {
		var ds []time.Duration
		t0 := time.Now()
		for i := 0; i < n; i++ {
			t1 := time.Now()
			c, err := tc.dial()
			if err != nil {
				fatal(err)
			}
			if _, err := get(c, bufio.NewReader(c)); err != nil {
				fatal(err)
			}
			c.Close()
			ds = append(ds, time.Since(t1))
		}
		fmt.Printf("%-20s %9.0f %9s\n", tc.name, float64(n)/time.Since(t0).Seconds(), rd(pcts(ds)[0]))
	}
}

// measureRTT: median thời gian TCP connect trơn = 1 RTT (SYN → SYN-ACK).
func measureRTT(addr string) time.Duration {
	var ds []time.Duration
	for i := 0; i < 21; i++ {
		t0 := time.Now()
		c, err := net.Dial("tcp", addr)
		if err != nil {
			fatal(err)
		}
		ds = append(ds, time.Since(t0))
		c.Close()
	}
	return pcts(ds)[0]
}

func rtt(ca *tlsx.CA, n int) {
	up, stop, err := fixture.ListenAndServe("127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	defer stop()
	s, addr := startProxy(storeFor(ca, tlsx.ECDSA), up, nil)
	defer s.Close()
	ps, paddr := startProxy(nil, up, nil)
	defer ps.Close()
	r := measureRTT(paddr)
	fmt.Printf("RTT đo (TCP connect trơn, median 21): %s\n", rd(r))
	fmt.Printf("\n%-20s %10s %8s   (tới byte đầu response trên connection MỚI; median %d lần)\n", "biến thể", "p50", "÷ RTT", n)
	row := func(name string, dial func() (net.Conn, error), check func(net.Conn)) {
		var ds []time.Duration
		for i := 0; i < n; i++ {
			t0 := time.Now()
			c, err := dial()
			if err != nil {
				fatal(err)
			}
			first, err := get(c, bufio.NewReader(c))
			if err != nil {
				fatal(err)
			}
			ds = append(ds, first.Sub(t0))
			if check != nil {
				check(c)
			}
			c.Close()
		}
		p := pcts(ds)[0]
		fmt.Printf("%-20s %10s %8.2f\n", name, rd(p), float64(p)/float64(r))
	}
	row("plaintext", func() (net.Conn, error) { return net.Dial("tcp", paddr) }, nil)
	for _, v := range variants[:4] {
		cfg := clientCfg(ca, v)
		if v.resumed {
			prime(addr, cfg)
		}
		var notResumed atomic.Int64
		row(v.name, func() (net.Conn, error) { return tls.Dial("tcp", addr, cfg) }, func(c net.Conn) {
			if v.resumed && !c.(*tls.Conn).ConnectionState().DidResume {
				notResumed.Add(1)
			}
		})
		if notResumed.Load() > 0 {
			fmt.Printf("%-20s   ! %d lần KHÔNG resume\n", "", notResumed.Load())
		}
	}
}

func upstream(ca *tlsx.CA, n int) {
	leaf, err := ca.LeafTLS([]string{"127.0.0.1"}, tlsx.ECDSA, 9)
	if err != nil {
		fatal(err)
	}
	plainUp, stop1, err := fixture.ListenAndServe("127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	defer stop1()
	tlsUp, stop2, err := fixture.ListenAndServeTLS("127.0.0.1:0", leaf, 0)
	if err != nil {
		fatal(err)
	}
	defer stop2()
	r := measureRTT(plainUp)
	fmt.Printf("RTT đo (TCP connect trơn tới upstream, median 21): %s\n", rd(r))
	fmt.Printf("\n%-14s %-6s %10s %10s %8s   (client keep-alive một connection, tuần tự %d request)\n", "upstream", "pool", "p50", "mean", "dials", n)
	means := map[string]time.Duration{}
	for _, upKind := range []string{"plain", "tls"} {
		for _, pool := range []bool{false, true} {
			up := plainUp
			mut := func(c *proxy.Config) { c.Pool.Disabled = !pool }
			if upKind == "tls" {
				up = tlsUp
				mut = func(c *proxy.Config) {
					c.Pool.Disabled = !pool
					c.UpstreamTLS = &proxy.UpstreamTLSConfig{RootCAs: ca.Pool}
				}
			}
			s, addr := startProxy(nil, up, mut)
			c, err := net.Dial("tcp", addr)
			if err != nil {
				fatal(err)
			}
			br := bufio.NewReader(c)
			var ds []time.Duration
			var sum time.Duration
			for i := 0; i < n; i++ {
				t0 := time.Now()
				if _, err := get(c, br); err != nil {
					fatal(err)
				}
				d := time.Since(t0)
				ds = append(ds, d)
				sum += d
			}
			c.Close()
			mean := sum / time.Duration(n)
			means[fmt.Sprint(upKind, pool)] = mean
			fmt.Printf("%-14s %-6v %10s %10s %8d\n", upKind, pool, rd(pcts(ds)[0]), rd(mean), s.PoolStats().Dials)
			s.Close()
		}
	}
	for _, k := range []string{"plain", "tls"} {
		save := means[k+"false"] - means[k+"true"]
		fmt.Printf("tiết kiệm của pool, upstream %-5s: %s / request = %.2f RTT\n", k, rd(save), float64(save)/float64(r))
	}
	fmt.Printf("tỉ số tiết kiệm tls / plain: %.2fx\n",
		float64(means["tlsfalse"]-means["tlstrue"])/float64(means["plainfalse"]-means["plaintrue"]))
}

func reload(ca *tlsx.CA, conns int, dur, every time.Duration) {
	up, stop, err := fixture.ListenAndServe("127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	defer stop()
	store := storeFor(ca, tlsx.ECDSA)
	s, addr := startProxy(store, up, nil)
	defer s.Close()
	var ok, errs, newConns atomic.Int64
	stopC := make(chan struct{})
	var wg sync.WaitGroup
	cfg := &tls.Config{ServerName: "a.test", RootCAs: ca.Pool}
	for i := 0; i < conns; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for k := 0; ; k++ {
				c, err := tls.Dial("tcp", addr, cfg)
				if err != nil {
					errs.Add(1)
					return
				}
				newConns.Add(1)
				br := bufio.NewReader(c)
				for j := 0; j < 50; j++ { // 50 request rồi nối lại ⇒ có handshake mới giữa các lần reload
					select {
					case <-stopC:
						c.Close()
						return
					default:
					}
					if _, err := get(c, br); err != nil {
						errs.Add(1)
						c.Close()
						return
					}
					ok.Add(1)
				}
				c.Close()
			}
		}(i)
	}
	var serial int64 = 100
	reloads := 0
	t0 := time.Now()
	for time.Since(t0) < dur {
		time.Sleep(every)
		serial++
		c, err := ca.LeafTLS([]string{"a.test"}, tlsx.ECDSA, serial)
		if err != nil {
			fatal(err)
		}
		if err := store.Replace([]tlsx.Entry{{Names: []string{"a.test"}, Cert: &c}}); err != nil {
			fatal(err)
		}
		reloads++
	}
	close(stopC)
	wg.Wait()
	fmt.Printf("reload: %d lần trong %s dưới %d connection: %d request ok, %d lỗi, %d handshake mới; serial cuối %s\n",
		reloads, dur, conns, ok.Load(), errs.Load(), newConns.Load(), strings.Join(store.Serials(), ","))
	if errs.Load() > 0 {
		os.Exit(1)
	}
}

// pcts: p50, p90, p99, mean.
func pcts(ds []time.Duration) [4]time.Duration {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	var sum time.Duration
	for _, d := range s {
		sum += d
	}
	at := func(p float64) time.Duration { return s[int(float64(len(s)-1)*p)] }
	return [4]time.Duration{at(.5), at(.9), at(.99), sum / time.Duration(len(s))}
}

// cpuNow: CPU user+sys của cả tiến trình (client + proxy cùng tiến trình ⇒
// tổng hai phía handshake). Ít nhạy với tải máy hơn wall-time (G6).
// Lượt resumed có thêm một GET mỗi lần (để nhận ticket) — CPU của nó cộng vào.
func cpuNow() time.Duration {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func rd(d time.Duration) string { return d.Round(time.Microsecond).String() }

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "tlslab:", err)
	os.Exit(1)
}
