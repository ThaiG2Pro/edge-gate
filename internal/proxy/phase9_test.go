package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/thaivro/edgegate/internal/fixture"
	"github.com/thaivro/edgegate/internal/httpx"
)

// waitLive chờ liveBufio về want (goroutine server trả bufio sau khi ghi xong
// response — client đọc xong trước khi server tới vòng rỗi).
func waitLive(want int64) int64 {
	deadline := time.Now().Add(500 * time.Millisecond) // ≪ IdleTimeout của test
	for time.Now().Before(deadline) {
		if v := liveBufio.Load(); v == want {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	return liveBufio.Load()
}

// D2 / G3 đơn vị: N connection keep-alive rỗi (mỗi cái đã xong 1 request)
// KHÔNG cầm bufio nào. Pool upstream tắt ⇒ upstream không giữ bufio.
// nodefense9: mỗi connection rỗi cầm 2 ⇒ đỏ.
func TestIdleReleasesBufio(t *testing.T) {
	base := liveBufio.Load()
	p := startProxy(t, startFixture(t), func(c *Config) {
		c.Pool.Disabled = true
		c.Limits.IdleTimeout = 10 * time.Second // connection đóng vì idle cũng trả bufio — phải loại
	})
	const n = 20
	for i := 0; i < n; i++ {
		rc := dialRaw(t, p)
		if resp, _ := rc.do(t, "GET", "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n"); resp.Status != 200 {
			t.Fatalf("status %d", resp.Status)
		}
	}
	got := waitLive(base) - base
	t.Logf("%d connection rỗi: bufio đang cầm %d (base %d)", n, got, base)
	if got != 0 {
		t.Fatalf("connection rỗi cầm %d bufio, muốn 0", got)
	}
}

// D2: hai request trong MỘT lần ghi (pipelining) — byte request 2 nằm sẵn
// trong br khi request 1 xong; trả br về pool là mất request 2. Và request mà
// byte đầu đến một mình (đường prefixReader) phải ra đúng.
func TestIdlePipeliningAndPrefix(t *testing.T) {
	p := startProxy(t, startFixture(t), nil)
	rc := dialRaw(t, p)
	rc.c.SetDeadline(time.Now().Add(3 * time.Second))
	io.WriteString(rc.c, "GET /hello HTTP/1.1\r\nHost: x\r\n\r\nGET /status?code=201 HTTP/1.1\r\nHost: x\r\n\r\n")
	for _, want := range []int{200, 201} {
		resp, err := httpx.ReadResponse(rc.br, httpx.DefaultLimits(), "GET")
		if err != nil {
			t.Fatalf("pipelining: %v", err)
		}
		io.ReadAll(resp.Body)
		if resp.Status != want {
			t.Fatalf("pipelining: status %d, muốn %d", resp.Status, want)
		}
	}
	// Byte đầu một mình, phần còn lại 50 ms sau.
	io.WriteString(rc.c, "G")
	time.Sleep(50 * time.Millisecond)
	io.WriteString(rc.c, "ET /status?code=202 HTTP/1.1\r\nHost: x\r\n\r\n")
	resp, err := httpx.ReadResponse(rc.br, httpx.DefaultLimits(), "GET")
	if err != nil || resp.Status != 202 {
		t.Fatalf("byte đầu tách: %v %v", resp, err)
	}
	io.ReadAll(resp.Body)
	t.Logf("pipelining 200, 201; byte đầu tách 202")
}

// D5 / G4 đơn vị: body CL 1 MiB đi bằng splice — đúng từng byte, connection
// upstream về pool sạch (request kế reuse), connection client còn dùng được.
// Phần body đọc lố cùng head (trong ubr) phải đi trước phần splice.
// nodefense9: không splice ⇒ SpliceStats 0 ⇒ đỏ.
func TestSpliceBody(t *testing.T) {
	s, p := startProxyS(t, startFixture(t), nil)
	rc := dialRaw(t, p)
	for _, n := range []int{1 << 20, 100_000, 1000} { // 1000 < spliceMinBody ⇒ copy
		resp, b := rc.do(t, "GET", fmt.Sprintf("GET /large?n=%d HTTP/1.1\r\nHost: x\r\n\r\n", n))
		if resp.Status != 200 || !bytes.Equal(b, fixture.Pattern(n)) {
			t.Fatalf("n=%d: status %d, %d byte, đúng=%v", n, resp.Status, len(b), bytes.Equal(b, fixture.Pattern(n)))
		}
	}
	resp, _ := rc.do(t, "GET", "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
	nr, nb := s.SpliceStats()
	ps := s.PoolStats()
	t.Logf("splice: %d response, %d byte; pool %+v; request sau: %d", nr, nb, ps, resp.Status)
	if nr != 2 || nb == 0 || nb > int64(1<<20+100_000) {
		t.Fatalf("muốn 2 response splice, %d byte (≤ tổng body)", nb)
	}
	if ps.Dials != 1 || ps.Reuses != 3 {
		t.Fatalf("upstream phải về pool sạch sau splice: %+v", ps)
	}
}

// D5: upstream hứa CL 1 MiB rồi đóng giữa body ⇒ client nhận ít hơn rồi
// connection client bị ĐÓNG (không trả response thứ hai trên luồng đã lệch);
// upstream không về pool.
func TestSpliceBodyShortUpstream(t *testing.T) {
	up := rawServer(t, func(c net.Conn, br *bufio.Reader) {
		httpx.ReadRequest(br, httpx.DefaultLimits())
		io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 1048576\r\n\r\n")
		c.Write(make([]byte, 300_000))
	})
	s, p := startProxyS(t, up, nil)
	c, _ := net.Dial("tcp", p)
	defer c.Close()
	io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	b, _, err := readUntilClose(c)
	head := len(b) - strings.Index(string(b), "\r\n\r\n") - 4
	_, nb := s.SpliceStats()
	t.Logf("upstream đóng giữa body: client nhận %d byte body rồi %v; spliced %d; pool %+v", head, err, nb, s.PoolStats())
	if head >= 1<<20 || err != io.EOF {
		t.Fatalf("muốn body thiếu rồi đóng")
	}
	// P9-1 đối chứng: upstream đóng giữa body là lỗi CỦA UPSTREAM.
	deadline := time.Now().Add(time.Second)
	for s.LBStats().Backends[0].Fails == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f := s.LBStats().Backends[0].Fails; f != 1 {
		t.Fatalf("upstream đóng giữa body splice: backend fails=%d, muốn 1", f)
	}
	if s.PoolStats().Idle != 0 {
		t.Fatal("upstream hỏng không được về pool")
	}
}

// P9-2 (2026-10-03, viết trước số đo) — splice chiều UPLOAD: POST body CL ≥ 64
// KiB qua /echo, upstream phải nhận ĐÚNG từng byte; body < 64 KiB đi copy
// userspace. Đối xứng TestSpliceBody. Số latency là việc của Linux (P9-2 ⏳);
// test này chốt ĐÚNG byte + ranh giới + pool sạch + bộ đếm.
func TestSpliceUpload(t *testing.T) {
	s, p := startProxyS(t, startFixture(t), nil)
	rc := dialRaw(t, p)
	post := func(n int) {
		body := fixture.Pattern(n)
		raw := fmt.Sprintf("POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n%s", n, body)
		resp, b := rc.do(t, "POST", raw)
		if resp.Status != 200 || !bytes.Equal(b, body) {
			t.Fatalf("n=%d: status %d, nhận %d byte, đúng=%v", n, resp.Status, len(b), bytes.Equal(b, body))
		}
	}
	post(1 << 20) // 1 MiB ≥ 64 KiB ⇒ splice
	post(100_000) // ≥ 64 KiB ⇒ splice
	post(1000)    // < 64 KiB ⇒ copy userspace, KHÔNG splice
	// request không body sau cùng: pool phải reuse sạch.
	resp, _ := rc.do(t, "GET", "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
	nr, nb := s.SpliceUploadStats()
	ps := s.PoolStats()
	t.Logf("splice upload: %d request, %d byte; pool %+v; request sau: %d", nr, nb, ps, resp.Status)
	if nr != 2 {
		t.Fatalf("muốn 2 upload splice (1 MiB + 100 KB), được %d", nr)
	}
	if nb < 1<<20 || nb > int64(1<<20+100_000) {
		t.Fatalf("byte splice = %d, muốn ∈ [1 MiB, 1 MiB+100 KB]", nb)
	}
	if ps.Dials != 1 || ps.Reuses != 3 {
		t.Fatalf("upstream phải về pool sạch sau splice upload: %+v", ps)
	}
}

// P9-2 — upload cắt cụt giữa splice: client hứa CL lớn rồi đóng sớm ⇒ readErr
// (upload cắt), KHÔNG tính lỗi upstream, connection đóng. Chốt phân loại rerr.
func TestSpliceUploadClientCut(t *testing.T) {
	s, p := startProxyS(t, startFixture(t), nil)
	c, err := net.Dial("tcp", p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Hứa 1 MiB, gửi head + 10 KB rồi đóng nửa ghi.
	fmt.Fprintf(c, "POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n", 1<<20)
	c.Write(fixture.Pattern(10_000))
	if tc, ok := c.(*net.TCPConn); ok {
		tc.CloseWrite()
	}
	br := bufio.NewReader(c)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = httpx.ReadResponse(br, httpx.DefaultLimits(), "POST") // 400/đóng — không panic là đủ
	// upstream không bị tính lỗi: không có backend nào bị eject.
	for _, b := range s.LBStats().Backends {
		if b.Fails != 0 {
			t.Fatalf("upload client cắt KHÔNG được tính lỗi upstream: %s fails=%d", b.Addr, b.Fails)
		}
	}
}
