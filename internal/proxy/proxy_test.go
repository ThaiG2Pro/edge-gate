package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http" // ORACLE phía client + fixture upstream — chỉ trong _test.go
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thaivro/edgegate/internal/fixture"
	"github.com/thaivro/edgegate/internal/httpx"
)

// startProxy chạy proxy trên :0 trỏ tới upstream, trả địa chỉ proxy.
func startProxy(t *testing.T, upstream string, mut func(*Config)) string {
	t.Helper()
	lim := httpx.DefaultLimits()
	lim.HeaderTimeout, lim.BodyTimeout, lim.IdleTimeout = 2*time.Second, 2*time.Second, 2*time.Second
	cfg := Config{Listen: "127.0.0.1:0", Upstream: upstream, Limits: lim,
		DialTimeout: time.Second, UpstreamHeaderTimeout: 2 * time.Second, UpstreamBodyTimeout: 2 * time.Second,
		Logf: t.Logf}
	if mut != nil {
		mut(&cfg)
	}
	s := New(cfg)
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve(ln)
	t.Cleanup(func() { s.Close() })
	return ln.Addr().String()
}

func startFixture(t *testing.T) string {
	t.Helper()
	us := httptest.NewServer(fixture.Handler())
	t.Cleanup(us.Close)
	return us.Listener.Addr().String()
}

// client net/http KHÔNG keep-alive (mỗi request một connection) để test đơn lẻ
// không lẫn với test keep-alive.
func oracleClient() *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
}

func TestGET(t *testing.T) {
	p := startProxy(t, startFixture(t), nil)
	resp, err := oracleClient().Get("http://" + p + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "hello from upstream\n" {
		t.Fatalf("status %d body %q", resp.StatusCode, b)
	}
	if resp.Header.Get("Content-Length") != "20" {
		t.Fatalf("CL phải được giữ nguyên, có %q", resp.Header.Get("Content-Length"))
	}
}

func TestPOSTEcho(t *testing.T) {
	p := startProxy(t, startFixture(t), nil)
	for _, n := range []int{0, 1, 1000, 100 << 10, 3 << 20} {
		want := fixture.Pattern(n)
		resp, err := oracleClient().Post("http://"+p+"/echo", "application/octet-stream", bytes.NewReader(want))
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !bytes.Equal(got, want) {
			t.Fatalf("n=%d: status %d, body %d byte, khớp=%v", n, resp.StatusCode, len(got), bytes.Equal(got, want))
		}
	}
}

// Request body chunked từ client (net/http gửi chunked khi body là io.Reader
// không biết độ dài) ⇒ proxy phải forward lại dạng chunked (TE được đặt lại).
func TestPOSTChunkedRequestBody(t *testing.T) {
	p := startProxy(t, startFixture(t), nil)
	want := fixture.Pattern(70000)
	req, _ := http.NewRequest("POST", "http://"+p+"/echo", io.NopCloser(bytes.NewReader(want)))
	req.ContentLength = -1 // buộc chunked
	resp, err := oracleClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, want) {
		t.Fatalf("body lệch: %d vs %d byte", len(got), len(want))
	}
}

// Bẫy #2: response chunked (không CL) không được treo, và client phải thấy
// đúng chunked (không phải body-tới-EOF).
func TestChunkedResponse(t *testing.T) {
	p := startProxy(t, startFixture(t), nil)
	t0 := time.Now()
	resp, err := oracleClient().Get("http://" + p + "/chunked")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	want := "chunk 0\nchunk 1\nchunk 2\nchunk 3\nchunk 4\n"
	if string(b) != want {
		t.Fatalf("body %q", b)
	}
	if len(resp.TransferEncoding) != 1 || resp.TransferEncoding[0] != "chunked" {
		t.Fatalf("client phải thấy chunked, thấy TE=%v CL=%d", resp.TransferEncoding, resp.ContentLength)
	}
	if d := time.Since(t0); d > time.Second {
		t.Fatalf("mất %v — có mùi treo tới deadline", d)
	}
}

// D3: upstream trả body tới EOF (không CL, không TE) ⇒ client HTTP/1.1 nhận chunked.
func TestEOFBodyBecomesChunked(t *testing.T) {
	p := startProxy(t, startFixture(t), nil)
	resp, err := oracleClient().Get("http://" + p + "/eof")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "body until eof\n" {
		t.Fatalf("body %q", b)
	}
	if len(resp.TransferEncoding) != 1 || resp.TransferEncoding[0] != "chunked" {
		t.Fatalf("muốn chunked, thấy TE=%v CL=%d", resp.TransferEncoding, resp.ContentLength)
	}
}

func TestHEADAnd204HaveNoBody(t *testing.T) {
	p := startProxy(t, startFixture(t), nil)
	c := oracleClient()
	resp, err := c.Head("http://" + p + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.ContentLength != 20 {
		t.Fatalf("HEAD: status %d CL %d", resp.StatusCode, resp.ContentLength)
	}
	resp, err = c.Get("http://" + p + "/nobody")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 204 || len(b) != 0 || len(resp.TransferEncoding) != 0 {
		t.Fatalf("204: status %d body %d TE %v", resp.StatusCode, len(b), resp.TransferEncoding)
	}
}

// Hop-by-hop bị strip, header do Connection liệt kê động cũng bị strip, XFF
// được nối, Host giữ nguyên (D2).
func TestHopByHopAndXFF(t *testing.T) {
	// Peer 127.0.0.1 nằm trong TrustedProxies ⇒ XFF client gửi được giữ và
	// append (G6 nửa "tin"). Nửa "không tin" ở TestXFFUntrustedReplaced.
	p := startProxy(t, startFixture(t), func(c *Config) { c.TrustedProxies = []string{"127.0.0.0/8"} })
	req, _ := http.NewRequest("GET", "http://"+p+"/headers", nil)
	req.Host = "vhost.example"
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	req.Header.Set("X-Secret", "smuggled")
	req.Header.Set("Connection", "X-Secret, keep-alive")
	req.Header.Set("Keep-Alive", "timeout=5")
	req.Header.Set("Proxy-Authorization", "Basic abc")
	req.Header.Set("X-Keep", "yes")
	resp, err := oracleClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	got := string(b)
	for _, bad := range []string{"X-Secret:", "Keep-Alive:", "Proxy-Authorization:"} {
		if strings.Contains(got, bad) {
			t.Errorf("%s lọt sang upstream:\n%s", bad, got)
		}
	}
	// net/http bên upstream tự tách Connection ra khỏi r.Header, nên không kiểm
	// được nó bằng fixture; StripHopByHop có test riêng ở httpx.
	for _, want := range []string{"Host: vhost.example\n", "X-Keep: yes\n", "X-Forwarded-For: 10.0.0.1, 127.0.0.1\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("thiếu %q:\n%s", want, got)
		}
	}
}

// rawConn: client tay, để kiểm keep-alive ở mức byte.
type rawConn struct {
	c  net.Conn
	br *bufio.Reader
}

func dialRaw(t *testing.T, addr string) *rawConn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &rawConn{c: c, br: bufio.NewReader(c)}
}

func (r *rawConn) do(t *testing.T, method, raw string) (*httpx.Response, []byte) {
	t.Helper()
	r.c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(r.c, raw); err != nil {
		t.Fatalf("ghi: %v", err)
	}
	resp, err := httpx.ReadResponse(r.br, httpx.DefaultLimits(), method)
	if err != nil {
		t.Fatalf("đọc response: %v", err)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("đọc body: %v", err)
	}
	return resp, b
}

// Keep-alive phía client: 3 request khác framing trên MỘT connection; request
// sau không được nuốt byte của request trước (bẫy #3, đường bình thường).
func TestKeepAliveSequential(t *testing.T) {
	p := startProxy(t, startFixture(t), nil)
	rc := dialRaw(t, p)
	resp, b := rc.do(t, "GET", "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
	if resp.Status != 200 || string(b) != "hello from upstream\n" {
		t.Fatalf("1: %d %q", resp.Status, b)
	}
	body := "xin chao"
	resp, b = rc.do(t, "POST", fmt.Sprintf("POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n%s", len(body), body))
	if resp.Status != 200 || string(b) != body {
		t.Fatalf("2: %d %q", resp.Status, b)
	}
	resp, b = rc.do(t, "POST", "POST /echo HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n2\r\nde\r\n0\r\n\r\n")
	if resp.Status != 200 || string(b) != "abcde" {
		t.Fatalf("3: %d %q", resp.Status, b)
	}
	resp, b = rc.do(t, "GET", "GET /chunked HTTP/1.1\r\nHost: x\r\n\r\n")
	if resp.Status != 200 || !resp.Chunked || !strings.HasPrefix(string(b), "chunk 0\n") {
		t.Fatalf("4: %d chunked=%v %q", resp.Status, resp.Chunked, b)
	}
	if resp.Close {
		t.Fatal("proxy báo Connection: close dù client keep-alive")
	}
}

func TestHTTP10ClientGetsEOFBodyAndClose(t *testing.T) {
	p := startProxy(t, startFixture(t), nil)
	rc := dialRaw(t, p)
	resp, b := rc.do(t, "GET", "GET /eof HTTP/1.0\r\n\r\n")
	if resp.Status != 200 || string(b) != "body until eof\n" || !resp.Close || resp.Chunked {
		t.Fatalf("%d close=%v chunked=%v %q", resp.Status, resp.Close, resp.Chunked, b)
	}
	// HTTP/1.0 + body CL: proxy phải nói keep-alive tường minh
	rc2 := dialRaw(t, p)
	resp, _ = rc2.do(t, "GET", "GET /hello HTTP/1.0\r\nConnection: keep-alive\r\n\r\n")
	if resp.Close {
		t.Fatalf("HTTP/1.0 keep-alive bị đóng: %v", resp.Header)
	}
}

// G5 / bẫy #3: upstream không dial được, request có body 1000 B. Proxy phải
// DRAIN body rồi trả 502, và request kế tiếp trên cùng connection vẫn đúng.
// Với -tags nodefense: 1000 byte body thành request-line ⇒ request 2 = 400.
func TestDrainOnUpstreamDown(t *testing.T) {
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	deadAddr := dead.Addr().String()
	dead.Close() // cổng vừa đóng: dial bị từ chối ngay
	p := startProxy(t, deadAddr, nil)
	rc := dialRaw(t, p)
	// Body có KHOẢNG TRẮNG. Lần đầu dùng 1000 chữ "Z": không drain thì
	// "ZZZ…ZGET /hello HTTP/1.1" vẫn là request-line hợp lệ (method là token
	// dài) ⇒ vẫn 502 ⇒ phản chứng KHÔNG đỏ. Bài test yếu, không phải phòng
	// tuyến đúng. "Z Z Z … GET /hello HTTP/1.1" thì version = "Z Z …" ⇒ 505/400.
	body := strings.Repeat("Z ", 500)
	resp, _ := rc.do(t, "POST", fmt.Sprintf("POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n%s", len(body), body))
	if resp.Status != 502 || resp.Close {
		t.Fatalf("1: muốn 502 giữ connection, có %d close=%v", resp.Status, resp.Close)
	}
	resp, _ = rc.do(t, "GET", "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
	if resp.Status != 502 {
		t.Fatalf("2: muốn 502 (upstream vẫn chết) — có %d: byte body request 1 đã bị hiểu thành request 2", resp.Status)
	}
}

// G1 / bẫy #2: upstream RAW bỏ qua Connection: close, trả CL rồi GIỮ connection.
// Với framing đúng, proxy trả xong ngay và nhận request 2. Với -tags nodefense
// (io.Copy thô) proxy kẹt chờ EOF từ upstream tới UpstreamBodyTimeout ⇒ request
// 2 không được đọc trong 500 ms ⇒ đỏ.
func TestRawCopyTrap(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				br := bufio.NewReader(c)
				if _, err := httpx.ReadRequest(br, httpx.DefaultLimits()); err != nil {
					c.Close()
					return
				}
				io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
				// KHÔNG đóng: giả upstream keep-alive cứng đầu.
				time.Sleep(5 * time.Second)
				c.Close()
			}(c)
		}
	}()
	p := startProxy(t, ln.Addr().String(), func(c *Config) { c.UpstreamBodyTimeout = 3 * time.Second })
	rc := dialRaw(t, p)
	resp, b := rc.do(t, "GET", "GET /a HTTP/1.1\r\nHost: x\r\n\r\n")
	if resp.Status != 200 || string(b) != "ok" {
		t.Fatalf("1: %d %q", resp.Status, b)
	}
	// Request 2 phải được trả lời nhanh: proxy không được còn kẹt ở body 1.
	rc.c.SetDeadline(time.Now().Add(500 * time.Millisecond))
	io.WriteString(rc.c, "GET /b HTTP/1.1\r\nHost: x\r\n\r\n")
	resp2, err := httpx.ReadResponse(rc.br, httpx.DefaultLimits(), "GET")
	if err != nil {
		t.Fatalf("request 2 không được trả lời trong 500 ms: %v — proxy đang io.Copy chờ upstream EOF (bẫy #2)", err)
	}
	if resp2.Status != 200 {
		t.Fatalf("2: %d", resp2.Status)
	}
}

func TestUpstreamSlowIs504(t *testing.T) {
	p := startProxy(t, startFixture(t), func(c *Config) { c.UpstreamHeaderTimeout = 200 * time.Millisecond })
	rc := dialRaw(t, p)
	t0 := time.Now()
	resp, _ := rc.do(t, "GET", "GET /slow?ms=2000 HTTP/1.1\r\nHost: x\r\n\r\n")
	if resp.Status != 504 || resp.Close {
		t.Fatalf("muốn 504 giữ connection, có %d close=%v", resp.Status, resp.Close)
	}
	if d := time.Since(t0); d > time.Second {
		t.Fatalf("504 mất %v, timeout không áp", d)
	}
	// connection vẫn dùng được
	resp, b := rc.do(t, "GET", "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
	if resp.Status != 200 || string(b) != "hello from upstream\n" {
		t.Fatalf("sau 504: %d %q", resp.Status, b)
	}
}

func TestBadRequestGets400AndClose(t *testing.T) {
	p := startProxy(t, startFixture(t), nil)
	rc := dialRaw(t, p)
	resp, _ := rc.do(t, "GET", "GET /x HTTP/1.1\nHost: x\n\n") // bare LF (D1 phase 2)
	if resp.Status != 400 || !resp.Close {
		t.Fatalf("%d close=%v", resp.Status, resp.Close)
	}
	rc2 := dialRaw(t, p)
	resp, _ = rc2.do(t, "GET", "GET /x HTTP/2.0\r\nHost: x\r\n\r\n")
	if resp.Status != 505 {
		t.Fatalf("%d", resp.Status)
	}
}

// G6: không leak goroutine sau nhiều request, nửa keep-alive nửa close.
func TestNoGoroutineLeak(t *testing.T) {
	p := startProxy(t, startFixture(t), nil)
	before := runtime.NumGoroutine()
	ka := &http.Client{Timeout: 5 * time.Second}
	nk := oracleClient()
	for i := 0; i < 200; i++ {
		c := ka
		if i%2 == 1 {
			c = nk
		}
		resp, err := c.Get("http://" + p + "/hello")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	ka.CloseIdleConnections()
	nk.CloseIdleConnections()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n := runtime.NumGoroutine(); n <= before+2 {
			t.Logf("G6: goroutine trước %d, sau %d (chờ %s)", before, n, time.Since(deadline.Add(-3*time.Second)).Round(time.Millisecond))
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutine trước %d, sau %d", before, runtime.NumGoroutine())
}
