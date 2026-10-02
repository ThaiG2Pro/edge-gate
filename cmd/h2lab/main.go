// h2lab: thí nghiệm phase 10 (D10). net/http ở đây là PEER độc lập (client
// h2c, upstream fixture) — data path của proxy vẫn là internal/h2 + internal/proxy.
//
//	-mode hpack   G1: byte HEADERS mỗi request từ client net/http, #1 vs #2..N, vs head h1
//	-mode hol     G3: /slow 500 ms rồi /fast trên MỘT connection — h1 vs h2
//	-mode flow    G4: một stream 8 MiB, client tự đặt window (65 535 vs 8 MiB)
//	-mode tcphol  G5: 32 GET 64 KiB song song — h2 1 conn × 32 stream vs h1 32 conn
//	-mode flood   P10-4: PING / SETTINGS / malformed — syscall ghi + thời gian server trên mỗi frame client
//	-mode cpu     G8: CPU + context switch / request của bin/edgegate (tiến trình con) h2 vs h1
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/thaivro/edgegate/internal/fixture"
	"github.com/thaivro/edgegate/internal/h2"
	"github.com/thaivro/edgegate/internal/h2/hpack"
	"github.com/thaivro/edgegate/internal/httpx"
	"github.com/thaivro/edgegate/internal/proxy"
)

var (
	mode     = flag.String("mode", "hpack", "hpack | hol | flow | tcphol | cpu")
	n        = flag.Int("n", 100, "hpack: số request; hol/tcphol: số vòng")
	rounds   = flag.Int("rounds", 3, "flow/cpu: số lượt xen kẽ")
	size     = flag.Int("size", 8<<20, "flow: byte response")
	par      = flag.Int("par", 32, "tcphol: số request song song")
	objSize  = flag.Int("obj", 64<<10, "tcphol: byte mỗi response")
	slowMs   = flag.Int("slow", 500, "hol: ms của /slow")
	dur      = flag.Duration("dur", 10*time.Second, "cpu: thời lượng mỗi lượt")
	edgegate = flag.String("edgegate", "bin/edgegate", "cpu: binary proxy")
	epolllab = flag.String("epolllab", "bin/epolllab", "cpu: binary upstream")
	pinProxy = flag.String("pin-proxy", "0-1", "cpu: taskset proxy")
	pinUp    = flag.String("pin-up", "2-3", "cpu: taskset upstream")
	h2conns  = flag.Int("h2conns", 8, "cpu: số connection h2")
	h2strm   = flag.Int("h2streams", 8, "cpu: stream đồng thời mỗi connection h2")
	h1conns  = flag.Int("h1conns", 64, "cpu: số connection h1")
	ref      = flag.Bool("ref", false, "tcphol: đo trên server net/http (h1 + h2c) của Go, KHÔNG qua EdgeGate — đối chứng cài đặt")
)

func main() {
	flag.Parse()
	switch *mode {
	case "hpack":
		runHPACK()
	case "hol":
		runHOL()
	case "flow":
		runFlow()
	case "tcphol":
		runTCPHOL()
	case "cpu":
		runCPU()
	case "flood":
		runFlood()
	default:
		log.Fatalf("mode lạ %q", *mode)
	}
}

func h2cTransport() *http.Transport {
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return &http.Transport{Protocols: &p}
}

func h1Transport(conns int) *http.Transport {
	var p http.Protocols
	p.SetHTTP1(true)
	return &http.Transport{Protocols: &p, MaxConnsPerHost: conns, MaxIdleConnsPerHost: conns}
}

// fixtureUpstream: upstream h1 in-process (net/http — đây là upstream, không phải proxy).
func fixtureUpstream() string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	go http.Serve(ln, fixture.Handler())
	return ln.Addr().String()
}

// startProxy: EdgeGate in-process, h2c bật (hol/flow/tcphol: đo latency /
// throughput, không đo CPU riêng của proxy — cpu mode dùng tiến trình con).
func startProxy(upstream string) string {
	lim := httpx.DefaultLimits()
	s := proxy.New(proxy.Config{Listen: "127.0.0.1:0", Upstreams: []string{upstream}, H2C: true, Limits: lim,
		Logf: func(string, ...any) {}})
	ln, err := proxy.Listen("127.0.0.1:0", false)
	if err != nil {
		log.Fatal(err)
	}
	go s.Serve(ln)
	return ln.Addr().String()
}

func median(xs []float64) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	if len(s) == 0 {
		return 0
	}
	return s[len(s)/2]
}

func pct(xs []float64, p float64) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	if len(s) == 0 {
		return 0
	}
	i := int(float64(len(s)-1) * p)
	return s[i]
}

// ---------------------------------------------------------------------------
// G1: HPACK. Server mini bằng Framer của internal/h2 — đếm payload từng
// HEADERS client gửi; trả HEADERS :status 200 END_STREAM.

func runHPACK() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	var mu sync.Mutex
	var sizes []int
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		br := bufio.NewReader(c)
		pre := make([]byte, len(h2.Preface))
		io.ReadFull(br, pre)
		fr := h2.NewFramer(br, bufio.NewWriter(c))
		enc := hpack.NewEncoder()
		fr.WriteSettings()
		fr.Flush()
		for {
			f, err := fr.ReadFrame()
			if err != nil {
				return
			}
			switch f.Type {
			case h2.FrameSettings:
				if !f.Has(h2.FlagAck) {
					fr.WriteSettingsAck()
					fr.Flush()
				}
			case h2.FrameHeaders:
				p, _ := h2.HeaderPayload(f)
				mu.Lock()
				sizes = append(sizes, len(p))
				mu.Unlock()
				fr.WriteHeaderBlock(f.Stream, true, enc.Encode(nil, []hpack.HeaderField{{Name: ":status", Value: "200"}}), 16384)
				fr.Flush()
			}
		}
	}()
	cl := &http.Client{Transport: h2cTransport()}
	hdr := func(r *http.Request) {
		r.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0 Safari/537.36")
		r.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		r.Header.Set("Accept-Language", "vi-VN,vi;q=0.9,en-US;q=0.8")
		r.Header.Set("Cookie", "session=2f8a9c1e4b7d6a3f0e9c8b7a6d5c4b3a; theme=dark; _ga=GA1.1.123456789.1700000000")
		r.Header.Set("Authorization", "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NSJ9.abcdefghijklmnopqrstuvwxyz0123456789")
	}
	url := "http://" + ln.Addr().String() + "/api/v1/items?page=1"
	for i := 0; i < *n; i++ {
		req, _ := http.NewRequest("GET", url, nil)
		hdr(req)
		resp, err := cl.Do(req)
		if err != nil {
			log.Fatal(err)
		}
		resp.Body.Close()
	}
	// head h1 tương đương (cùng header + Host + Accept-Encoding mà client Go thêm)
	req := &httpx.Request{Method: "GET", Target: "/api/v1/items?page=1", Proto: "HTTP/1.1", Header: httpx.Header{}}
	r0, _ := http.NewRequest("GET", url, nil)
	hdr(r0)
	for k, vs := range r0.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Host", ln.Addr().String())
	req.Header.Set("Accept-Encoding", "gzip")
	var sb strings.Builder
	req.WriteHead(&sb)
	mu.Lock()
	defer mu.Unlock()
	rest := make([]float64, 0, len(sizes))
	for _, s := range sizes[1:] {
		rest = append(rest, float64(s))
	}
	m := median(rest)
	fmt.Printf("hpack: %d request, một connection h2c (client net/http)\n", len(sizes))
	fmt.Printf("  HEADERS #1        = %d byte\n", sizes[0])
	fmt.Printf("  HEADERS #2..#%d    trung vị = %.0f byte (min %.0f, max %.0f)\n", len(sizes), m, pct(rest, 0), pct(rest, 1))
	fmt.Printf("  head h1 tương đương = %d byte\n", sb.Len())
	fmt.Printf("  tỉ số #1 / trung vị #2+   = %.1fx\n", float64(sizes[0])/m)
	fmt.Printf("  tỉ số h1 / trung vị h2 #2+ = %.1fx\n", float64(sb.Len())/m)
}

// ---------------------------------------------------------------------------
// G3: HOL tầng HTTP.

func runHOL() {
	addr := startProxy(fixtureUpstream())
	slowPath := fmt.Sprintf("/slow?ms=%d", *slowMs)
	var alone, h1, h2v []float64

	// h2: một connection (Transport h2c gộp mọi request về một conn).
	cl := &http.Client{Transport: h2cTransport()}
	get := func(p string) time.Duration {
		t0 := time.Now()
		resp, err := cl.Get("http://" + addr + p)
		if err != nil {
			log.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return time.Since(t0)
	}
	get("/hello") // mở connection
	for i := 0; i < *n; i++ {
		alone = append(alone, ms(get("/hello")))
		done := make(chan struct{})
		go func() { get(slowPath); close(done) }()
		time.Sleep(10 * time.Millisecond)
		h2v = append(h2v, ms(get("/hello")))
		<-done
	}

	// h1: một connection, /slow rồi /fast ghi ngay sau (pipelining) — h1
	// không có cách nào trả /fast trước /slow trên cùng connection.
	c, err := net.Dial("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()
	br := bufio.NewReader(c)
	lim := httpx.DefaultLimits()
	readOne := func() {
		resp, err := httpx.ReadResponse(br, lim, "GET")
		if err != nil {
			log.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
	}
	for i := 0; i < *n; i++ {
		fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: a\r\n\r\n", slowPath)
		time.Sleep(10 * time.Millisecond)
		t0 := time.Now()
		fmt.Fprintf(c, "GET /hello HTTP/1.1\r\nHost: a\r\n\r\n")
		readOne()
		readOne()
		h1 = append(h1, ms(time.Since(t0)))
	}
	fmt.Printf("hol: %d vòng, RTT loopback, /slow=%d ms, /fast gửi sau 10 ms trên CÙNG connection\n", *n, *slowMs)
	fmt.Printf("  /fast một mình (h2)     p50 %.2f ms\n", median(alone))
	fmt.Printf("  /fast sau /slow, h2     p50 %.2f ms  p99 %.2f ms\n", median(h2v), pct(h2v, 0.99))
	fmt.Printf("  /fast sau /slow, h1     p50 %.2f ms  p99 %.2f ms\n", median(h1), pct(h1, 0.99))
	fmt.Printf("  h2 / một mình = %.2fx ; h1 / h2 = %.1fx\n", median(h2v)/median(alone), median(h1)/median(h2v))
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// ---------------------------------------------------------------------------
// G4: trần flow control. Client thô: tự đặt INITIAL_WINDOW_SIZE + window
// connection, WINDOW_UPDATE ngay mỗi DATA nhận được.

func runFlow() {
	addr := startProxy(fixtureUpstream())
	path := fmt.Sprintf("/large?n=%d", *size)
	for r := 0; r < *rounds; r++ {
		for _, w := range []uint32{65535, 8 << 20} {
			d := flowOnce(addr, path, w)
			fmt.Printf("flow: lượt %d window %8d: %d byte trong %v = %.2f MB/s\n", r+1, w, *size, d.Round(time.Millisecond), float64(*size)/d.Seconds()/1e6)
		}
	}
}

func flowOnce(addr, path string, w uint32) time.Duration {
	nc, err := net.Dial("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	defer nc.Close()
	c, err := h2.NewRawClient(nc, h2.Setting{ID: h2.SettingInitialWindowSize, Val: w})
	if err != nil {
		log.Fatal(err)
	}
	if w > 65535 {
		c.WindowUpdate(0, w-65535)
	}
	t0 := time.Now()
	c.Headers(1, true, h2.GET(addr, path)...)
	got := 0
	nc.SetReadDeadline(time.Now().Add(120 * time.Second))
	for {
		f, err := c.Fr.ReadFrame()
		if err != nil {
			log.Fatalf("flow: %v sau %d byte", err, got)
		}
		switch f.Type {
		case h2.FrameSettings:
			if !f.Has(h2.FlagAck) {
				c.Frame(h2.FrameSettings, h2.FlagAck, 0, nil)
			}
		case h2.FrameRSTStream, h2.FrameGoAway:
			log.Fatalf("flow: %v", f.Type)
		case h2.FrameData:
			got += len(f.Payload)
			if len(f.Payload) > 0 {
				c.Fr.WriteWindowUpdate(0, uint32(len(f.Payload)))
				if !f.Has(h2.FlagEndStream) {
					c.Fr.WriteWindowUpdate(1, uint32(len(f.Payload)))
				}
				c.Fr.Flush()
			}
			if f.Has(h2.FlagEndStream) {
				return time.Since(t0)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// G5: HOL tầng TCP.

// refServer: net/http.Server nói cả h1 lẫn h2c (Go 1.24+), phục vụ fixture
// trực tiếp — đối chứng: chênh h2/h1 là của EdgeGate hay của "một connection".
func refServer() string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: fixture.Handler(), Protocols: &p}
	go srv.Serve(ln)
	return ln.Addr().String()
}

func runTCPHOL() {
	var addr string
	if *ref {
		addr = refServer()
	} else {
		addr = startProxy(fixtureUpstream())
	}
	url := fmt.Sprintf("http://%s/large?n=%d", addr, *objSize)
	measure := func(cl *http.Client) []float64 {
		var mu sync.Mutex
		var lat []float64
		for r := 0; r < *n; r++ {
			var wg sync.WaitGroup
			for i := 0; i < *par; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					t0 := time.Now()
					resp, err := cl.Get(url)
					if err != nil {
						log.Print(err)
						return
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					mu.Lock()
					lat = append(lat, ms(time.Since(t0)))
					mu.Unlock()
				}()
			}
			wg.Wait()
		}
		return lat
	}
	h2c := &http.Client{Transport: h2cTransport()}
	h1c := &http.Client{Transport: h1Transport(*par)}
	// làm nóng: mở connection (h2: 1; h1: par)
	measure2 := func(cl *http.Client) { saved := *n; *n = 1; measure(cl); *n = saved }
	measure2(h2c)
	measure2(h1c)
	l2 := measure(h2c)
	l1 := measure(h1c)
	target := "EdgeGate → fixture"
	if *ref {
		target = "net/http server trực tiếp (ref)"
	}
	fmt.Printf("tcphol: %d vòng × %d GET %d byte song song — %s\n", *n, *par, *objSize, target)
	fmt.Printf("  h2 1 conn × %d stream: p50 %.1f ms  p90 %.1f  p99 %.1f  max %.1f\n", *par, median(l2), pct(l2, 0.9), pct(l2, 0.99), pct(l2, 1))
	fmt.Printf("  h1 %d conn            : p50 %.1f ms  p90 %.1f  p99 %.1f  max %.1f\n", *par, median(l1), pct(l1, 0.9), pct(l1, 0.99), pct(l1, 1))
	fmt.Printf("  h2/h1: p50 %.2fx  p99 %.2fx\n", median(l2)/median(l1), pct(l2, 0.99)/pct(l1, 0.99))
}

// ---------------------------------------------------------------------------
// G8: CPU / request và context switch / request của proxy tiến trình con.
// closed-loop — coordinated omission chưa loại trừ, CHỈ đọc CPU/req.

type child struct {
	cmd *exec.Cmd
	pid int
}

func spawn(cmdline, waitAddr string) *child {
	args := strings.Fields(cmdline)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout, cmd.Stderr = io.Discard, os.Stderr
	cmd.Env = append(os.Environ(), "GOMAXPROCS=2")
	if err := cmd.Start(); err != nil {
		log.Fatalf("spawn %q: %v", cmdline, err)
	}
	for i := 0; ; i++ {
		c, err := net.DialTimeout("tcp", waitAddr, 100*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		if i > 100 {
			cmd.Process.Kill()
			log.Fatalf("%q không nghe %s", cmdline, waitAddr)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return &child{cmd: cmd, pid: cmd.Process.Pid}
}

func (c *child) stop() {
	c.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { c.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		c.cmd.Process.Kill()
		<-done
	}
}

func cpuSec(pid int) float64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return -1
	}
	s := string(b)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+2:])
	ut, _ := strconv.ParseFloat(f[11], 64)
	st, _ := strconv.ParseFloat(f[12], 64)
	return (ut + st) / 100
}

// ctxsw: tổng voluntary_ctxt_switches của MỌI luồng (/proc/<pid>/task/*/status).
func ctxsw(pid int) int64 {
	tasks, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/status", pid))
	var sum int64
	for _, t := range tasks {
		b, err := os.ReadFile(t)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "voluntary_ctxt_switches:") {
				v, _ := strconv.ParseInt(strings.Fields(line)[1], 10, 64)
				sum += v
			}
		}
	}
	return sum
}

func runCPU() {
	up := spawn(fmt.Sprintf("taskset -c %s %s -impl epoll -loops 2 -addr 127.0.0.1:18100 -body 1024", *pinUp, *epolllab), "127.0.0.1:18100")
	defer up.stop()
	px := spawn(fmt.Sprintf("taskset -c %s %s -config config/h2.json", *pinProxy, *edgegate), "127.0.0.1:18093")
	defer px.stop()
	url := "http://127.0.0.1:18093/"
	load := func(clients []*http.Client, perClient int, d time.Duration) int64 {
		var reqs atomic.Int64
		stop := time.Now().Add(d)
		var wg sync.WaitGroup
		for _, cl := range clients {
			for j := 0; j < perClient; j++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for time.Now().Before(stop) {
						resp, err := cl.Get(url)
						if err != nil {
							continue
						}
						io.Copy(io.Discard, resp.Body)
						resp.Body.Close()
						reqs.Add(1)
					}
				}()
			}
		}
		wg.Wait()
		return reqs.Load()
	}
	mk := func(proto string) ([]*http.Client, int) {
		if proto == "h2" {
			cls := make([]*http.Client, *h2conns)
			for i := range cls {
				cls[i] = &http.Client{Transport: h2cTransport()} // mỗi Transport ⇒ một connection
			}
			return cls, *h2strm
		}
		return []*http.Client{{Transport: h1Transport(*h1conns)}}, *h1conns
	}
	fmt.Printf("cpu: proxy pid %d (taskset %s, GOMAXPROCS=2), upstream epoll (taskset %s), closed-loop %v/lượt — chỉ đọc CPU/req; h2 %d conn × %d stream, h1 %d conn\n", px.pid, *pinProxy, *pinUp, *dur, *h2conns, *h2strm, *h1conns)
	for r := 0; r < *rounds; r++ {
		for _, proto := range []string{"h2", "h1"} {
			cls, per := mk(proto)
			load(cls, per, time.Second) // làm nóng: mở connection, pool upstream
			c0, s0 := cpuSec(px.pid), ctxsw(px.pid)
			t0 := time.Now()
			nreq := load(cls, per, *dur)
			el := time.Since(t0)
			c1, s1 := cpuSec(px.pid), ctxsw(px.pid)
			fmt.Printf("  lượt %d %s: %d req (%.0f rps), CPU proxy %.2f s = %.1f µs/req, ctxsw %.3f /req\n",
				r+1, proto, nreq, float64(nreq)/el.Seconds(), c1-c0, (c1-c0)/float64(nreq)*1e6, float64(s1-s0)/float64(nreq))
			for _, cl := range cls {
				cl.CloseIdleConnections()
			}
		}
	}
}

// ---------------------------------------------------------------------------
// P10-4: flood frame điều khiển. Server internal/h2 in-process (handler giữ
// stream mở tới khi lab xong); client thô gom nhiều frame vào MỘT lần ghi.
// Đo: Flush phía server (= syscall ghi) trên mỗi frame client, và thời gian
// tới khi client nhận đủ phản hồi (ACK/RST) — chi phí server cho mỗi frame.

func runFlood() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	hold := make(chan struct{})
	defer close(hold)
	var stats h2.Stats
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go h2.ServeConn(c, c, h2.Config{}, func(w *h2.Stream, r *h2.Request) { <-hold }, &stats)
		}
	}()
	dial := func() *h2.RawClient {
		nc, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			log.Fatal(err)
		}
		c, err := h2.NewRawClient(nc)
		if err != nil {
			log.Fatal(err)
		}
		return c
	}
	// waitFor: đọc tới khi thấy want frame loại t (ACK hoặc RST).
	waitFor := func(c *h2.RawClient, t h2.FrameType, ack bool, want int) {
		got := 0
		c.NC.SetReadDeadline(time.Now().Add(10 * time.Second))
		for got < want {
			f, err := c.Fr.ReadFrame()
			if err != nil {
				// nodefense10: HEADERS tên in hoa không bị RST ⇒ chờ mãi — in ra, đi tiếp.
				log.Printf("  flood: chờ %v: %v sau %d/%d", t, err, got, want)
				return
			}
			if f.Type == h2.FrameGoAway {
				log.Printf("  GOAWAY %v sau %d/%d", h2.GoAwayCode(f), got, want)
				return
			}
			if f.Type == t && (!ack || f.Has(h2.FlagAck)) {
				got++
			}
		}
	}
	report := func(name string, frames int, el time.Duration, f0 int64) {
		fl := stats.Flushes.Load() - f0
		fmt.Printf("  %-34s %7d frame client trong %4d lần ghi ⇒ server %7d Flush (%.2f/frame), %v = %.2f µs/frame\n",
			name, frames, frames / *n, fl, float64(fl)/float64(frames), el.Round(time.Millisecond), float64(el.Microseconds())/float64(frames))
	}
	const total = 100000
	fmt.Printf("flood: %d frame mỗi bài, client gom %d frame / lần ghi\n", total, *n)

	// (1) PING
	c := dial()
	waitFor(c, h2.FrameSettings, false, 1)
	c.Frame(h2.FrameSettings, h2.FlagAck, 0, nil)
	f0 := stats.Flushes.Load()
	t0 := time.Now()
	go func() {
		for i := 0; i < total; i++ {
			c.Fr.WritePing(false, [8]byte{byte(i)})
			if i%*n == *n-1 {
				c.Fr.Flush()
			}
		}
		c.Fr.Flush()
	}()
	waitFor(c, h2.FramePing, true, total)
	report("PING", total, time.Since(t0), f0)
	c.NC.Close()

	// (2) SETTINGS INITIAL_WINDOW_SIZE × 2730 mỗi frame, 100 stream mở
	c = dial()
	waitFor(c, h2.FrameSettings, false, 1)
	for i := 0; i < 100; i++ {
		c.Headers(uint32(2*i+1), true, h2.GET("a", "/")...)
	}
	time.Sleep(200 * time.Millisecond)
	per := 16384 / 6
	ss := make([]h2.Setting, per)
	for i := range ss {
		ss[i] = h2.Setting{ID: h2.SettingInitialWindowSize, Val: uint32(65535 + i%2)}
	}
	frames := total / per
	f0 = stats.Flushes.Load()
	t0 = time.Now()
	go func() {
		for i := 0; i < frames; i++ {
			c.Fr.WriteSettings(ss...)
		}
		c.Fr.Flush()
	}()
	waitFor(c, h2.FrameSettings, true, frames)
	el := time.Since(t0)
	fmt.Printf("  %-34s %7d frame × %d setting, 100 stream mở ⇒ %v = %.2f µs/setting, %.0f µs/frame\n",
		"SETTINGS INITIAL_WINDOW_SIZE", frames, per, el.Round(time.Millisecond), float64(el.Microseconds())/float64(frames*per), float64(el.Microseconds())/float64(frames))
	c.NC.Close()

	// (3) HEADERS malformed (tên in hoa) ⇒ RST mỗi cái
	c = dial()
	waitFor(c, h2.FrameSettings, false, 1)
	f0 = stats.Flushes.Load()
	t0 = time.Now()
	go func() {
		for i := 0; i < total; i++ {
			blk := c.Enc.Encode(nil, h2.GET("a", "/", hpack.HeaderField{Name: "X-Bad", Value: "1"}))
			c.Fr.WriteFrame(h2.FrameHeaders, h2.FlagEndHeaders|h2.FlagEndStream, uint32(2*i+1), blk)
			if i%*n == *n-1 {
				c.Fr.Flush()
			}
		}
		c.Fr.Flush()
	}()
	waitFor(c, h2.FrameRSTStream, false, total)
	report("HEADERS malformed ⇒ RST", total, time.Since(t0), f0)
	c.NC.Close()
}
