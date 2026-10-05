// perflab: phép đo phase 9 (D6). Mọi server bị đo là TIẾN TRÌNH CON — RSS và
// CPU đọc từ /proc/<pid> của riêng nó, không lẫn client.
//
//	-mode idle     G3/G6a: mở -conns connection, mỗi cái 1 request rồi giữ rỗi;
//	               in ΔVmRSS / conn của tiến trình -spawn
//	-mode l4l7     G4: body -size qua -impl direct|l4splice|l4copy|edgegate|edgegate-splice;
//	               MB/s, CPU proxy / GB, pipe fd của proxy lúc copy
//	-mode rps      G7: closed-loop -conns connection trong -duration (CO chưa loại trừ — chỉ đọc rps)
//	-mode open     G8: open-loop (internal/loadgen) -rate trong -duration
//	-mode l4proxy  (nội bộ) proxy L4 cho l4l7: -copy splice|copy
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ThaiG2Pro/edge-gate/internal/httpx"
	"github.com/ThaiG2Pro/edge-gate/internal/loadgen"
)

var (
	mode     = flag.String("mode", "idle", "idle | l4l7 | rps | open | l4proxy")
	spawn    = flag.String("spawn", "", "lệnh tiến trình bị đo (tách theo khoảng trắng); rỗng = không spawn")
	upCmd    = flag.String("up", "", "lệnh upstream spawn trước (không đo)")
	addr     = flag.String("addr", "127.0.0.1:18091", "địa chỉ bắn vào")
	upAddr   = flag.String("upaddr", "127.0.0.1:18100", "địa chỉ upstream (chờ sẵn sàng)")
	conns    = flag.Int("conns", 10000, "idle/rps: số connection")
	settle   = flag.Duration("settle", 3*time.Second, "idle: chờ sau khi mở xong rồi mới đọc RSS")
	path     = flag.String("path", "/", "path request")
	duration = flag.Duration("duration", 10*time.Second, "rps/open: thời gian")
	rate     = flag.Float64("rate", 5000, "open: request/s")
	workers  = flag.Int("workers", 64, "open: connection tối đa")
	label    = flag.String("label", "", "nhãn dòng kết quả")

	impl    = flag.String("impl", "edgegate", "l4l7: direct | l4splice | l4copy | edgegate | edgegate-splice")
	size    = flag.Int("size", 10<<20, "l4l7: byte body")
	reps    = flag.Int("n", 50, "l4l7: số response tuần tự")
	pcpu    = flag.String("proxy-cpus", "0", "l4l7: taskset cho proxy")
	ucpu    = flag.String("up-cpus", "2", "l4l7: taskset cho upstream")
	edgebin = flag.String("edgegate", "bin/edgegate", "l4l7: binary edgegate")

	copyMode = flag.String("copy", "splice", "l4proxy: splice | copy")
	listen   = flag.String("listen", "127.0.0.1:18201", "l4proxy: listen")
	target   = flag.String("target", "127.0.0.1:18100", "l4proxy: backend")
	upload   = flag.Bool("upload", false, "l4l7: đo chiều upload (POST CL >= 64 KiB qua spliceUpload)")
)

func main() {
	flag.Parse()
	switch *mode {
	case "idle":
		runIdle()
	case "l4l7":
		runL4L7()
	case "rps":
		runRPS()
	case "open":
		runOpen()
	case "l4proxy":
		runL4Proxy()
	default:
		log.Fatalf("mode lạ %q", *mode)
	}
}

// ---------------------------------------------------------------------------
// tiến trình con + /proc

type child struct {
	cmd *exec.Cmd
	pid int
}

// start chạy cmdline (tách khoảng trắng), chờ addr nhận connection.
func start(cmdline, waitAddr string) *child {
	args := strings.Fields(cmdline)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		log.Fatalf("spawn %q: %v", cmdline, err)
	}
	ch := &child{cmd: cmd, pid: cmd.Process.Pid}
	for i := 0; ; i++ {
		c, err := net.DialTimeout("tcp", waitAddr, 100*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		if i > 100 {
			ch.stop()
			log.Fatalf("%q không lắng nghe %s sau 10 s", cmdline, waitAddr)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return ch
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

// rssKiB: VmRSS. Lệnh "taskset -c X prog ..." — taskset exec thẳng prog (cùng
// pid), nên pid của cmd là pid của server.
func (c *child) rssKiB() int64 { return procStatusKiB(c.pid, "VmRSS:") }

func procStatusKiB(pid int, key string) int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, key) {
			f := strings.Fields(line)
			v, _ := strconv.ParseInt(f[1], 10, 64)
			return v
		}
	}
	return -1
}

// cpuSec: utime + stime của mọi luồng (/proc/<pid>/stat trường 14, 15; tick 100 Hz).
func cpuSec(pid int) float64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return -1
	}
	s := string(b)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+2:]) // sau "(comm) " — comm có thể chứa space
	ut, _ := strconv.ParseFloat(f[11], 64)
	st, _ := strconv.ParseFloat(f[12], 64)
	return (ut + st) / 100
}

func threads(pid int) int64 { return procStatusKiB(pid, "Threads:") }

// pipeFDs: số fd là pipe của tiến trình — splice(2) của Go đi qua pipe
// (internal/poll/splice_linux.go), copy userspace thì không.
func pipeFDs(pid int) int {
	ents, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	n := 0
	for _, e := range ents {
		if fd, _ := strconv.Atoi(e.Name()); fd <= 2 {
			continue // stdio có thể là pipe (perflab chạy sau `|`)
		}
		if l, err := os.Readlink(filepath.Join(fmt.Sprintf("/proc/%d/fd", pid), e.Name())); err == nil && strings.HasPrefix(l, "pipe:") {
			n++
		}
	}
	return n
}

func reqLine() string { return "GET " + *path + " HTTP/1.1\r\nHost: bench\r\n\r\n" }

// readResp đọc một response (head + body CL) và bỏ body.
func readResp(br *bufio.Reader) (int, int64, error) {
	resp, err := httpx.ReadResponse(br, httpx.DefaultLimits(), "GET")
	if err != nil {
		return 0, 0, err
	}
	n, err := io.Copy(io.Discard, resp.Body)
	return resp.Status, n, err
}

// ---------------------------------------------------------------------------
// idle (G3, G6a)

func runIdle() {
	var up *child
	if *upCmd != "" {
		up = start(*upCmd, *upAddr)
		defer up.stop()
	}
	srv := start(*spawn, *addr)
	defer srv.stop()
	// Một request khởi động (pool upstream, cấp phát lần đầu) trước mốc RSS.
	warm, err := net.Dial("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	warm.Write([]byte(reqLine()))
	if _, _, err := readResp(bufio.NewReader(warm)); err != nil {
		log.Fatalf("request khởi động: %v", err)
	}
	time.Sleep(*settle)
	rss0, th0 := srv.rssKiB(), threads(srv.pid)
	cs := make([]net.Conn, 0, *conns)
	t0 := time.Now()
	req := []byte(reqLine())
	for i := 0; i < *conns; i++ {
		c, err := net.Dial("tcp", *addr)
		if err != nil {
			log.Fatalf("dial %d: %v", i, err)
		}
		c.SetDeadline(time.Now().Add(10 * time.Second))
		c.Write(req)
		if st, _, err := readResp(bufio.NewReaderSize(c, 2048)); err != nil || st != 200 {
			log.Fatalf("conn %d: status %d %v", i, st, err)
		}
		c.SetDeadline(time.Time{})
		cs = append(cs, c)
	}
	open := time.Since(t0)
	time.Sleep(*settle)
	rss1, th1 := srv.rssKiB(), threads(srv.pid)
	fmt.Printf("%-24s conns %d (mở trong %s) · VmRSS %d → %d KiB · Δ %.2f KiB/conn · luồng OS %d → %d\n",
		*label, *conns, open.Round(time.Millisecond), rss0, rss1, float64(rss1-rss0)/float64(*conns), th0, th1)
	for _, c := range cs {
		c.Close()
	}
}

// ---------------------------------------------------------------------------
// l4l7 (G4)

func runL4L7() {
	upCmd := fmt.Sprintf("taskset -c %s bin/epolllab -impl netpoller -addr %s -body %d", *ucpu, *upAddr, *size)
	if *upload {
		upCmd = fmt.Sprintf("taskset -c %s bin/upstream -addr %s", *ucpu, *upAddr)
	}
	up := start(upCmd, *upAddr)
	defer up.stop()
	dst := *upAddr
	var px *child
	proxyAddr := "127.0.0.1:18201"
	self, _ := os.Executable()
	switch *impl {
	case "direct":
	case "l4splice", "l4copy":
		cp := strings.TrimPrefix(*impl, "l4")
		px = start(fmt.Sprintf("taskset -c %s %s -mode l4proxy -copy %s -listen %s -target %s", *pcpu, self, cp, proxyAddr, *upAddr), proxyAddr)
	case "edgegate", "edgegate-splice":
		sp := " -nosplice" // P9-7: splice mặc định bật ⇒ "edgegate" trần phải tắt tường minh
		if *impl == "edgegate-splice" {
			sp = ""
		}
		px = start(fmt.Sprintf("taskset -c %s %s -config config/bench.json -listen %s -upstream %s%s", *pcpu, *edgebin, proxyAddr, *upAddr, sp), proxyAddr)
	default:
		log.Fatalf("impl lạ %q", *impl)
	}
	if px != nil {
		defer px.stop()
		dst = proxyAddr
	}
	c, err := net.Dial("tcp", dst)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()
	br := bufio.NewReaderSize(c, 64<<10)

	var reqHead []byte
	var reqBody []byte
	if *upload {
		reqHead = []byte(fmt.Sprintf("POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n", *size))
		reqBody = bytes.Repeat([]byte{'a'}, *size)
	} else {
		reqHead = []byte(reqLine())
	}

	writeReq := func() {
		c.Write(reqHead)
		if len(reqBody) > 0 {
			c.Write(reqBody)
		}
	}

	// khởi động: một response (pool, cấp phát lần đầu)
	writeReq()
	if _, _, err := readResp(br); err != nil {
		log.Fatal(err)
	}
	var cpu0 float64
	if px != nil {
		cpu0 = cpuSec(px.pid)
	}
	var maxPipes atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	if px != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				case <-time.After(20 * time.Millisecond):
					if n := int64(pipeFDs(px.pid)); n > maxPipes.Load() {
						maxPipes.Store(n)
					}
				}
			}
		}()
	}
	t0 := time.Now()
	var total int64
	for i := 0; i < *reps; i++ {
		writeReq()
		st, n, err := readResp(br)
		if err != nil || st != 200 || n != int64(*size) {
			log.Fatalf("response %d: %d %d byte %v", i, st, n, err)
		}
		total += n
	}
	el := time.Since(t0)
	close(stop)
	wg.Wait()
	gb := float64(total) / (1 << 30)
	line := fmt.Sprintf("%-16s %d × %d MiB trong %s · %.0f MiB/s", *impl, *reps, *size>>20, el.Round(time.Millisecond), float64(total)/(1<<20)/el.Seconds())
	if px != nil {
		cpu := cpuSec(px.pid) - cpu0
		line += fmt.Sprintf(" · CPU proxy %.2f s = %.2f s/GiB · pipe fd tối đa %d", cpu, cpu/gb, maxPipes.Load())
	}
	fmt.Println(line)
}

// runL4Proxy: proxy L4 — mỗi connection client ↔ một connection backend, hai
// io.Copy. "splice": TCPConn→TCPConn (Go dùng splice(2)); "copy": giấu
// ReadFrom/WriteTo để io.Copy rơi về buffer 32 KiB userspace.
func runL4Proxy() {
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	type onlyR struct{ io.Reader }
	type onlyW struct{ io.Writer }
	cp := func(dst, src net.Conn) {
		if *copyMode == "copy" {
			io.Copy(onlyW{dst}, onlyR{src})
		} else {
			io.Copy(dst, src)
		}
		if tc, ok := dst.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			u, err := net.Dial("tcp", *target)
			if err != nil {
				return
			}
			defer u.Close()
			done := make(chan struct{})
			go func() { cp(u, c); close(done) }()
			cp(c, u)
			<-done
		}()
	}
}

// ---------------------------------------------------------------------------
// rps (G7) — closed-loop: chỉ đọc throughput, KHÔNG đọc latency.

func runRPS() {
	var srv *child
	if *spawn != "" {
		srv = start(*spawn, *addr)
		defer srv.stop()
	}
	req := []byte(reqLine())
	var done atomic.Int64
	var errs atomic.Int64
	var wg sync.WaitGroup
	deadline := time.Now().Add(*duration)
	var cpu0 float64
	if srv != nil {
		cpu0 = cpuSec(srv.pid)
	}
	t0 := time.Now()
	for i := 0; i < *conns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.Dial("tcp", *addr)
			if err != nil {
				errs.Add(1)
				return
			}
			defer c.Close()
			br := bufio.NewReader(c)
			for time.Now().Before(deadline) {
				c.SetDeadline(time.Now().Add(5 * time.Second))
				if _, err := c.Write(req); err != nil {
					errs.Add(1)
					return
				}
				if st, _, err := readResp(br); err != nil || st != 200 {
					errs.Add(1)
					return
				}
				done.Add(1)
			}
		}()
	}
	wg.Wait()
	el := time.Since(t0)
	line := fmt.Sprintf("%-24s closed-loop %d conn %s: %d response, %.0f rps, lỗi %d", *label, *conns, el.Round(time.Millisecond), done.Load(), float64(done.Load())/el.Seconds(), errs.Load())
	if srv != nil {
		cpu := cpuSec(srv.pid) - cpu0
		line += fmt.Sprintf(" · CPU server %.2f s = %.1f µs/req", cpu, cpu/float64(max(done.Load(), 1))*1e6)
	}
	fmt.Println(line)
}

// ---------------------------------------------------------------------------
// open (G8) — open-loop, latency từ giờ hẹn.

// runOpen (G8; P-env-2): open-loop -rate trong -duration. Có -spawn/-up thì
// proxy/upstream là tiến trình con (ghim core bằng taskset trong lệnh) và in
// CPU-giây của proxy. Kiểm generator THEO KỊP: lịch hẹn n = rate×duration
// request; nếu bắn xong trễ quá 2 % so với duration thì generator (không phải
// proxy) là cổ chai ⇒ mọi số latency ở dòng đó VÔ NGHĨA — in rõ, exit 3.
func runOpen() {
	// os.Exit(3) ở dưới bỏ qua defer ⇒ dừng con tường minh: con sống sót
	// giữ pipe stdout ⇒ `make pinlab | grep` treo (lộ ra lượt chạy đầu).
	var kids []*child
	stopAll := func() {
		for i := len(kids) - 1; i >= 0; i-- {
			kids[i].stop()
		}
	}
	defer stopAll()
	if *upCmd != "" {
		kids = append(kids, start(*upCmd, *upAddr))
	}
	var srv *child
	if *spawn != "" {
		srv = start(*spawn, *addr)
		kids = append(kids, srv)
	}
	req := reqLine()
	var cpu0 float64
	if srv != nil {
		cpu0 = cpuSec(srv.pid)
	}
	t0 := time.Now()
	ss := loadgen.Run(loadgen.Config{Addr: *addr, Rate: *rate, Duration: *duration, Workers: *workers,
		Timeout: 5 * time.Second, Request: func(int) string { return req }})
	el := time.Since(t0)
	name := fmt.Sprintf("%s@%.0f", *label, *rate)
	sum := loadgen.Summarize(ss)
	loadgen.Print(os.Stdout, name, sum, *duration)
	achieved := float64(len(ss)) / el.Seconds()
	dev := (achieved - *rate) / *rate * 100
	line := fmt.Sprintf("%-14s generator: %d request trong %s ⇒ %.0f/s, yêu cầu %.0f/s, lệch %+.2f %%", "", len(ss), el.Round(time.Millisecond), achieved, *rate, dev)
	if srv != nil {
		line += fmt.Sprintf(" · proxy CPU %.2f s (%.1f µs/req)", cpuSec(srv.pid)-cpu0, (cpuSec(srv.pid)-cpu0)/float64(max(len(ss), 1))*1e6)
	}
	fmt.Println(line)
	// Hai cách generator hỏng số: (1) tổng rps lệch > 2 % (debt P-env-2);
	// (2) rps đúng nhưng từng request nhận việc trễ — lag p99 ≥ p99 latency/2
	// nghĩa là quá nửa cái "p99" là của generator.
	bad := dev < -2 || dev > 2
	if sum.OK.N > 0 && sum.Lag.P99*2 >= sum.OK.P99 && sum.Lag.P99 > time.Millisecond {
		fmt.Printf("%-14s lag generator p99 %s ≥ ½ p99 latency %s: p99 dòng trên là của GENERATOR (P-env-2)\n", "",
			sum.Lag.P99.Round(10*time.Microsecond), sum.OK.P99.Round(10*time.Microsecond))
		bad = true
	}
	if bad {
		fmt.Printf("%-14s GENERATOR KHÔNG THEO NỔI: số latency dòng trên vô nghĩa (P-env-2)\n", "")
		stopAll()
		os.Exit(3)
	}
}
