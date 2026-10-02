package proxy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/thaivro/edgegate/internal/httpx"
)

// P3-3: upstream chết giữa body response. Head (CL 100) đã gửi cho client ⇒
// "một response cho một request": proxy KHÔNG được ghi thêm 502, chỉ được ĐÓNG;
// client đọc body nhận đúng 50 byte rồi io.ErrUnexpectedEOF; lỗi tính cho
// backend (outlier, phase 6 D7).
func TestUpstreamDiesMidBody(t *testing.T) {
	up := rawServer(t, func(c net.Conn, br *bufio.Reader) {
		if _, err := httpx.ReadRequest(br, httpx.DefaultLimits()); err != nil {
			return
		}
		fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n%050d", 0)
		// return ⇒ đóng sau 50/100 byte
	})
	s, p := startProxyS(t, up, nil)
	rc := dialRaw(t, p)
	rc.c.SetDeadline(time.Now().Add(3 * time.Second))
	io.WriteString(rc.c, "GET /x HTTP/1.1\r\nHost: x\r\n\r\n")
	resp, err := httpx.ReadResponse(rc.br, httpx.DefaultLimits(), "GET")
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	b, err := io.ReadAll(resp.Body)
	if len(b) != 50 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("body: %d byte, err %v — muốn 50 byte + ErrUnexpectedEOF", len(b), err)
	}
	// Sau body cụt: connection đóng, KHÔNG có byte nào nữa (không "HTTP/1.1 502").
	extra, err := io.ReadAll(rc.br)
	if len(extra) != 0 {
		t.Fatalf("proxy ghi thêm %q sau body cụt — hai response cho một request", extra)
	}
	if err != nil {
		t.Fatalf("đọc sau body: %v (muốn EOF sạch = connection đóng)", err)
	}
	deadline := time.Now().Add(time.Second)
	for s.LBStats().Backends[0].Fails == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f := s.LBStats().Backends[0].Fails; f != 1 {
		t.Fatalf("lỗi body upstream tính cho backend: fails=%d, muốn 1", f)
	}
}

// P7-1: hai backend (một không route, một sống), least-conn, outlier tắt (để
// backend xấu luôn được chọn lại được). Request nào bốc trúng backend xấu thì
// lượt chọn lại D9 PHẢI sang backend kia ⇒ mọi request 200. Bản cũ (Pick thường)
// chọn lại chính backend xấu ~50 % (hoà inflight ⇒ ngẫu nhiên) ⇒ 502.
func TestRepickExcludesFailedBackend(t *testing.T) {
	const dl = 150 * time.Millisecond
	s, p := startProxyS(t, "", func(c *Config) {
		c.Upstream = ""
		c.Upstreams = []string{"10.255.255.1:81", startFixture(t)}
		c.DialTimeout = dl
		c.LB.Algo = "leastconn"
		c.LB.Health.Disabled = true
		c.LB.Outlier.Disabled = true
	})
	codes := map[int]int{}
	for i := 0; i < 20; i++ {
		rc := dialRaw(t, p)
		resp, _ := rc.do(t, "GET", "GET /hello HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
		codes[resp.Status]++
	}
	st := s.LBStats()
	t.Logf("status %v; picks xấu=%d tốt=%d", codes, st.Backends[0].Picks, st.Backends[1].Picks)
	if codes[200] != 20 {
		t.Fatalf("D9 chọn lại backend vừa dial lỗi: %v", codes)
	}
	if st.Backends[0].Picks == 0 {
		t.Fatal("không request nào bốc trúng backend xấu — test không đo gì")
	}
}

// P9-1: client bỏ đi giữa một body splice (đọc 100 KB của 16 MiB rồi đóng ⇒
// RST) KHÔNG được tính là lỗi của backend (outlier phase 6 D7 sẽ eject oan).
// Upstream vẫn sống và đã gửi đủ. Đối chứng: upstream đóng giữa body ⇒ PHẢI tính.
func TestSpliceClientGone(t *testing.T) {
	const size = 16 << 20
	up := rawServer(t, func(c net.Conn, br *bufio.Reader) {
		if _, err := httpx.ReadRequest(br, httpx.DefaultLimits()); err != nil {
			return
		}
		fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", size)
		c.Write(make([]byte, size)) // chặn tới khi proxy đọc / connection bị đóng
		io.Copy(io.Discard, c)      // sống tiếp: upstream KHÔNG có lỗi gì
	})
	s, p := startProxyS(t, up, func(c *Config) { c.SpliceBody = true })
	c, err := net.Dial("tcp", p)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	buf := make([]byte, 100<<10)
	io.ReadFull(c, buf)
	c.Close() // dữ liệu chưa đọc trong receive queue ⇒ kernel gửi RST
	deadline := time.Now().Add(3 * time.Second)
	for s.res.connsActive.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	nr, nb := s.SpliceStats()
	f := s.LBStats().Backends[0].Fails
	t.Logf("client bỏ đi sau 100 KB: splice %d response %d byte; backend fails=%d", nr, nb, f)
	if nr != 1 {
		t.Fatalf("không đi đường splice (nr=%d) — test không đo gì", nr)
	}
	if f != 0 {
		t.Fatalf("client bỏ đi bị tính cho backend: fails=%d", f)
	}
}

// P3-1: trailer của response chunked từ upstream phải tới client (gRPC-web,
// `Trailer: X-Checksum`). Client net/http đọc trailer sau body.
func TestTrailerForwarded(t *testing.T) {
	up := rawServer(t, func(c net.Conn, br *bufio.Reader) {
		if _, err := httpx.ReadRequest(br, httpx.DefaultLimits()); err != nil {
			return
		}
		io.WriteString(c, "HTTP/1.1 200 OK\r\nTrailer: X-Checksum\r\nTransfer-Encoding: chunked\r\n\r\n"+
			"5\r\nhello\r\n0\r\nX-Checksum: abc123\r\n\r\n")
	})
	p := startProxy(t, up, nil)
	resp, err := oracleClient().Get("http://" + p + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	t.Logf("body %q, Trailer khai báo %q, trailer %v", b, resp.Header.Values("Trailer"), resp.Trailer)
	if string(b) != "hello" || resp.Trailer.Get("X-Checksum") != "abc123" {
		t.Fatalf("trailer mất qua proxy: %v", resp.Trailer)
	}
}

// P3-1 chiều request: trailer của body chunked từ client tới upstream; trailer
// cấm (Content-Length — RFC 9110 §6.5.1) bị parser phía proxy từ chối ⇒ 400.
func TestRequestTrailerForwarded(t *testing.T) {
	up := rawServer(t, func(c net.Conn, br *bufio.Reader) {
		for {
			req, err := httpx.ReadRequest(br, httpx.DefaultLimits())
			if err != nil {
				return
			}
			io.Copy(io.Discard, req.Body)
			v := req.Trailer().Get("X-Sig")
			fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(v), v)
		}
	})
	p := startProxy(t, up, nil)
	rc := dialRaw(t, p)
	_, b := rc.do(t, "POST", "POST / HTTP/1.1\r\nHost: x\r\nTrailer: X-Sig\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n0\r\nX-Sig: s1\r\n\r\n")
	if string(b) != "s1" {
		t.Fatalf("upstream thấy trailer %q, muốn s1", b)
	}
	resp, _ := rc.do(t, "POST", "POST / HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n0\r\nContent-Length: 5\r\n\r\n")
	if resp.Status != 400 {
		t.Fatalf("trailer cấm Content-Length: %d, muốn 400", resp.Status)
	}
}
