// proxylab — bộ đo phase 3 (G2/G3/G4). Client thô: chỉ `net` + httpx, KHÔNG
// net/http, để con số không lẫn overhead của một client khác.
//
// Mọi số ở đây là closed-loop, 1 connection, cùng máy với proxy và upstream
// (P-env-2 chưa trả). Nó đo OVERHEAD TƯƠNG ĐỐI của một hop L7, không đo
// throughput hay tail dưới tải.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ThaiG2Pro/edge-gate/internal/httpx"
)

var lim = httpx.DefaultLimits()

func main() {
	proxy := flag.String("proxy", "127.0.0.1:8080", "địa chỉ proxy")
	upstream := flag.String("upstream", "127.0.0.1:8081", "địa chỉ upstream (gọi thẳng)")
	mode := flag.String("mode", "all", "overhead | keepalive | nagle | all")
	n := flag.Int("n", 2000, "số request mỗi mẫu")
	label := flag.String("label", "", "nhãn in kèm (vd nodelay=false)")
	chunkMS := flag.Int("chunkms", 0, "G4: ms upstream ngủ giữa hai chunk")
	flag.Parse()

	switch *mode {
	case "overhead":
		overhead(*proxy, *upstream, *n)
	case "keepalive":
		keepalive(*proxy, *n)
	case "nagle":
		nagle(*proxy, *n, *chunkMS, *label)
	case "all":
		overhead(*proxy, *upstream, *n)
		keepalive(*proxy, *n)
		nagle(*proxy, *n, *chunkMS, *label)
	default:
		fmt.Fprintln(os.Stderr, "mode không hợp lệ")
		os.Exit(2)
	}
}

// ---------------------------------------------------------------------------

type conn struct {
	c  net.Conn
	br *bufio.Reader
}

func dial(addr string) *conn {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	c.(*net.TCPConn).SetNoDelay(true)
	return &conn{c: c, br: bufio.NewReaderSize(c, 8<<10)}
}

func (k *conn) close() { k.c.Close() }

// get gửi GET target, đọc hết response, trả latency. close=true gửi
// Connection: close (mẫu "mỗi request một connection").
func (k *conn) get(target string, close bool) (time.Duration, error) {
	var sb strings.Builder
	sb.WriteString("GET " + target + " HTTP/1.1\r\nHost: proxylab\r\n")
	if close {
		sb.WriteString("Connection: close\r\n")
	}
	sb.WriteString("\r\n")
	t0 := time.Now()
	k.c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(k.c, sb.String()); err != nil {
		return 0, err
	}
	resp, err := httpx.ReadResponse(k.br, lim, "GET")
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return 0, err
	}
	if resp.Status != 200 {
		return 0, fmt.Errorf("status %d", resp.Status)
	}
	return time.Since(t0), nil
}

type sample struct {
	name string
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
	i := int(float64(len(sorted)-1) * p / 100)
	return sorted[i]
}

// oneConn: n request GET target trên MỘT connection keep-alive.
func oneConn(name, addr, target string, n int) *sample {
	s := &sample{name: name}
	k := dial(addr)
	defer k.close()
	for i := 0; i < 50; i++ { // warm-up không tính
		if _, err := k.get(target, false); err != nil {
			fmt.Fprintln(os.Stderr, name, "warm-up:", err)
			os.Exit(1)
		}
	}
	t0 := time.Now()
	for i := 0; i < n; i++ {
		lat, err := k.get(target, false)
		if err != nil {
			s.errs++
			continue
		}
		s.lats = append(s.lats, lat)
	}
	s.wall = time.Since(t0)
	return s
}

// perConn: n request, mỗi request dial một connection mới + Connection: close.
// Latency tính CẢ dial, vì đó là cái client không keep-alive phải trả.
func perConn(name, addr, target string, n int) *sample {
	s := &sample{name: name}
	t0 := time.Now()
	for i := 0; i < n; i++ {
		t := time.Now()
		k := dial(addr)
		_, err := k.get(target, true)
		k.close()
		if err != nil {
			s.errs++
			continue
		}
		s.lats = append(s.lats, time.Since(t))
	}
	s.wall = time.Since(t0)
	return s
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

func ratio(g, what string, a, b time.Duration, expect string) {
	if b == 0 {
		fmt.Printf("  %s %s: n/a\n", g, what)
		return
	}
	fmt.Printf("  %s %s = %.2fx (kỳ vọng %s)\n", g, what, float64(a)/float64(b), expect)
}

// ---------------------------------------------------------------------------

// G2: overhead L7. p50 qua proxy / p50 gọi thẳng, GET /hello (20 B), 1 conn.
func overhead(proxy, upstream string, n int) {
	direct := oneConn("thẳng upstream, 1 conn keep-alive", upstream, "/hello", n)
	viaProxy := oneConn("qua proxy, 1 conn keep-alive (D1: dial mới)", proxy, "/hello", n)
	printTable(fmt.Sprintf("G2: overhead L7, GET /hello, closed-loop, cùng máy, n=%d", n), []*sample{direct, viaProxy})
	ratio("G2", "p50 proxy / p50 thẳng", viaProxy.pct(50), direct.pct(50), "3-5x")
	ratio("G2", "p99 proxy / p99 thẳng", viaProxy.pct(99), direct.pct(99), "—")
	fmt.Printf("  chênh tuyệt đối p50: %s (= parse 2 chiều + Dial upstream mới + goroutine)\n",
		rd(viaProxy.pct(50)-direct.pct(50)))
}

// G3: keep-alive phía client. 1 conn vs mỗi request một conn, cùng qua proxy.
func keepalive(proxy string, n int) {
	ka := oneConn("qua proxy, 1 conn keep-alive", proxy, "/hello", n)
	pc := perConn("qua proxy, mỗi request 1 conn mới", proxy, "/hello", n)
	printTable(fmt.Sprintf("G3: keep-alive client, GET /hello, closed-loop, n=%d", n), []*sample{ka, pc})
	ratio("G3", "p50 mỗi-conn / p50 keep-alive", pc.pct(50), ka.pct(50), "1.2-1.6x")
	ratio("G3", "wall mỗi-conn / wall keep-alive", pc.wall, ka.wall, "—")
	fmt.Println("  D1 làm proxy Dial upstream mới cho CẢ hai mẫu; khác biệt chỉ là dial client↔proxy.")
}

// G4: Nagle. GET /chunked?n=5&ms=0 — 5 chunk nhỏ dồn sát, proxy Flush mỗi chunk.
// Chạy hai lần: proxy -nodelay=true và -nodelay=false, so p50.
//
// chunkMS=0: 5 chunk tới proxy trong MỘT lần Read ⇒ proxy chỉ write một lần ⇒
// Nagle không có gì để giữ. Cần chunkMS > 0 (nhưng < 40 ms) để proxy write
// nhiều lần trong khi chunk trước còn chưa được ACK.
func nagle(proxy string, n, chunkMS int, label string) {
	if n > 300 {
		n = 300 // mỗi request có thể mất ~40-200 ms khi Nagle bật
	}
	target := fmt.Sprintf("/chunked?n=5&ms=%d", chunkMS)
	s := oneConn("qua proxy, GET "+target+" "+label, proxy, target, n)
	printTable(fmt.Sprintf("G4: Nagle, 5 chunk cách %d ms, closed-loop, n=%d %s", chunkMS, n, label), []*sample{s})
	fmt.Println("  So p50 giữa hai lần chạy (-nodelay=true / false). Đăng ký: +40 ms hằng số. Đo được turn 2: SÀN ≈ 44 ms (terminator chunk chờ delayed-ACK).")
}
