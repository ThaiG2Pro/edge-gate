package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaivro/edgegate/internal/httpx"
)

// rawServer: upstream tay (chỉ net + httpx) — mỗi connection một goroutine
// chạy serve(c, br) cho tới khi nó trả về; rồi đóng.
func rawServer(t *testing.T, serve func(c net.Conn, br *bufio.Reader)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				serve(c, bufio.NewReader(c))
			}()
		}
	}()
	return ln.Addr().String()
}

// waitStats chờ tới khi cond(stats) đúng hoặc hết 2 s.
func waitStats(t *testing.T, s *Server, what string, cond func(PoolStats) bool) PoolStats {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		st := s.PoolStats()
		if cond(st) || time.Now().After(deadline) {
			if !cond(st) {
				t.Logf("waitStats(%s) hết giờ: %+v", what, st)
			}
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// G6 — invariant sạch trên đường bình thường: 200 request xoay vòng 6 kiểu
// framing trên MỘT connection client ⇒ ĐÚNG MỘT dial upstream. Rồi 10 GET /eof
// (body tới EOF ⇒ resp.Close) ⇒ thêm đúng 10 dial, không con nào về pool.
func TestPoolReuseMixed(t *testing.T) {
	s, p := startProxyS(t, startFixture(t), nil)
	rc := dialRaw(t, p)
	body := "xin chao"
	steps := []struct{ method, raw, want string }{
		{"GET", "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n", "hello from upstream\n"},
		{"POST", fmt.Sprintf("POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n%s", len(body), body), body},
		{"POST", "POST /echo HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n2\r\nde\r\n0\r\n\r\n", "abcde"},
		{"GET", "GET /chunked?n=3&ms=0 HTTP/1.1\r\nHost: x\r\n\r\n", "chunk 0\nchunk 1\nchunk 2\n"},
		{"GET", "GET /nobody HTTP/1.1\r\nHost: x\r\n\r\n", ""},
		{"HEAD", "HEAD /hello HTTP/1.1\r\nHost: x\r\n\r\n", ""},
	}
	const n = 200
	for i := 0; i < n; i++ {
		st := steps[i%len(steps)]
		resp, b := rc.do(t, st.method, st.raw)
		if resp.Status/100 != 2 || string(b) != st.want {
			t.Fatalf("req %d (%s): status %d body %q, muốn %q", i, strings.SplitN(st.raw, "\r\n", 2)[0], resp.Status, b, st.want)
		}
	}
	// Client nhận xong response TRƯỚC khi proxy put (put nằm sau Flush) ⇒ đọc
	// stats ngay là race với chính proxy: lần chạy turn 2 thấy Puts:199 Idle:0.
	st := waitStats(t, s, "put cuối", func(st PoolStats) bool { return st.Puts == n })
	if st.Dials != 1 || st.Reuses != n-1 || st.Idle != 1 || st.DropDirty != 0 {
		t.Fatalf("G6 nửa đầu: muốn dials=1 reuses=%d idle=1 dropDirty=0, có %+v", n-1, st)
	}
	t.Logf("G6 sau %d request 6 kiểu framing: %+v", n, st)

	for i := 0; i < 10; i++ {
		resp, b := rc.do(t, "GET", "GET /eof HTTP/1.1\r\nHost: x\r\n\r\n")
		if resp.Status != 200 || string(b) != "body until eof\n" || !resp.Chunked {
			t.Fatalf("/eof %d: %d chunked=%v %q", i, resp.Status, resp.Chunked, b)
		}
	}
	st = waitStats(t, s, "eof released", func(st PoolStats) bool { return st.DropDirty == 10 })
	// Lần /eof đầu lấy connection rỗi (reuse) rồi bỏ; 9 lần sau pool rỗng ⇒ dial.
	if st.Dials != 1+9 || st.DropDirty != 10 || st.Idle != 0 {
		t.Fatalf("G6 nửa sau: muốn dials=10 dropDirty=10 idle=0, có %+v", st)
	}
	t.Logf("G6 sau 10 GET /eof: %+v", st)
}

// G3 / bẫy phase 5 — connection BẨN không được về pool. Client A nhận head
// CL=1 MiB rồi bỏ đi giữa body; client B GET /hello ngay sau phải thấy đúng
// "hello". Với -tags nodefensepool (poolCheckClean=false) connection của A —
// còn ~900 KiB body chưa đọc — về pool, B đọc đuôi body của A làm status-line.
//
// Probe TẮT có chủ đích: lần chạy đầu phản chứng đỏ nhưng vì probe MSG_PEEK
// thấy byte thừa của A và vì io.Copy thô của tag `nodefense` chung — đỏ sai
// chỗ. Ở đây chỉ còn đúng một phòng tuyến để lật: kiểm sạch D2.
func TestDirtyConnNotPooled(t *testing.T) {
	const big = 1 << 20
	up := rawServer(t, func(c net.Conn, br *bufio.Reader) {
		for {
			req, err := httpx.ReadRequest(br, httpx.DefaultLimits())
			if err != nil {
				return
			}
			io.Copy(io.Discard, req.Body)
			if req.Target == "/big" {
				fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", big)
				chunk := []byte(strings.Repeat("A", 64<<10))
				for sent := 0; sent < big; sent += len(chunk) {
					if _, err := c.Write(chunk); err != nil {
						return
					}
					time.Sleep(10 * time.Millisecond) // để A bỏ đi GIỮA body
				}
				continue
			}
			io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello")
		}
	})
	noProbe := false
	s, p := startProxyS(t, up, func(c *Config) { c.Pool.Probe = &noProbe })

	a := dialRaw(t, p)
	a.c.SetDeadline(time.Now().Add(3 * time.Second))
	io.WriteString(a.c, "GET /big HTTP/1.1\r\nHost: x\r\n\r\n")
	resp, err := httpx.ReadResponse(a.br, httpx.DefaultLimits(), "GET")
	if err != nil || resp.Status != 200 {
		t.Fatalf("A head: %v %v", err, resp)
	}
	buf := make([]byte, 100<<10)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("A đọc 100 KiB: %v", err)
	}
	a.c.Close() // bỏ đi giữa body: ~900 KiB còn trên đường upstream→proxy

	// Chờ proxy nhận ra A đã đi và release connection upstream (bẩn ⇒ đóng;
	// nodefense ⇒ về pool). Không chờ thì B dial mới và bài phản chứng không đỏ.
	st := waitStats(t, s, "A released", func(st PoolStats) bool { return st.DropDirty+st.Puts >= 1 })
	t.Logf("sau khi A bỏ đi: %+v", st)

	b := dialRaw(t, p)
	b.c.SetDeadline(time.Now().Add(3 * time.Second))
	io.WriteString(b.c, "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
	resp, err = httpx.ReadResponse(b.br, httpx.DefaultLimits(), "GET")
	if err != nil {
		t.Fatalf("B đọc head: %v — B nhận đuôi body của A làm status-line (connection bẩn về pool)", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.Status != 200 || string(body) != "hello" {
		t.Fatalf("B: status %d body %q — connection bẩn về pool", resp.Status, body)
	}
	st = s.PoolStats()
	if st.DropDirty < 1 {
		t.Fatalf("connection của A phải bị đóng (dropDirty ≥ 1): %+v", st)
	}
	t.Logf("G3: B thấy 200 hello; %+v", st)
}

// idleClosingUpstream: trả response rồi ĐÓNG NGAY mà không nói Connection:
// close — FIN nằm trong kernel của proxy trong khi pool tin connection còn sống.
// closed nhận một tín hiệu SAU mỗi lần Close (P10-9: test chờ sự kiện này thay
// vì ngủ cố định — dưới tải, goroutine upstream có thể chưa kịp chạy tới Close
// trong 20 ms).
func idleClosingUpstream(t *testing.T) (addr string, closed <-chan struct{}) {
	ch := make(chan struct{}, 256)
	addr = rawServer(t, func(c net.Conn, br *bufio.Reader) {
		req, err := httpx.ReadRequest(br, httpx.DefaultLimits())
		if err != nil {
			return
		}
		n, _ := io.Copy(io.Discard, req.Body)
		fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(fmt.Sprint(n)), fmt.Sprint(n))
		c.Close() // upstream "đóng rỗi lặng lẽ" (rawServer Close lần nữa: vô hại)
		ch <- struct{}{}
	})
	return addr, ch
}

// waitUpstreamClosed: chờ upstream báo đã Close connection vừa phục vụ, rồi
// 2 ms cho FIN đi qua loopback vào kernel proxy (probe đọc nó bằng MSG_PEEK).
func waitUpstreamClosed(t *testing.T, closed <-chan struct{}) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream không đóng connection trong 2 s")
	}
	time.Sleep(2 * time.Millisecond)
}

// G4 — upstream đóng connection rỗi lặng lẽ. Bốn nhánh của D3/D4.
func TestIdleClosedUpstream(t *testing.T) {
	const n = 50
	get := "GET /x HTTP/1.1\r\nHost: x\r\n\r\n"
	post := "POST /x HTTP/1.1\r\nHost: x\r\nContent-Length: 4\r\n\r\nabcd"
	run := func(t *testing.T, probe bool, raw, method, wantBody string, wantStatus int) PoolStats {
		up, closed := idleClosingUpstream(t)
		s, p := startProxyS(t, up, func(c *Config) { c.Pool.Probe = &probe })
		rc := dialRaw(t, p)
		rc.do(t, method, raw) // warm-up: dial đầu, put về pool (FIN sẽ tới ngay sau)
		ok := 0
		for i := 0; i < n; i++ {
			// FIN của upstream tới kernel proxy. Phase 5: sleep 3 ms; phase 8: 20 ms
			// (hụt dưới -race load ~7); P10-9: 20 ms vẫn hụt 1/30 lượt khi full suite
			// chạy song song ⇒ chờ SỰ KIỆN upstream đã Close, không chờ thời gian.
			waitUpstreamClosed(t, closed)
			resp, b := rc.do(t, method, raw)
			if resp.Status == wantStatus && (wantStatus != 200 || string(b) == wantBody) {
				ok++
			} else {
				t.Logf("req %d: %d %q", i, resp.Status, b)
			}
		}
		st := s.PoolStats()
		if ok != n {
			t.Fatalf("%d/%d đúng (muốn %d %q): %+v", ok, n, wantStatus, wantBody, st)
		}
		t.Logf("%d/%d; %+v", ok, n, st)
		return st
	}
	t.Run("probe-GET", func(t *testing.T) {
		st := run(t, true, get, "GET", "0", 200)
		if st.DeadOnProbe != n || st.Retries != 0 {
			t.Fatalf("muốn deadOnProbe=%d retries=0: %+v", n, st)
		}
	})
	t.Run("probe-POST-body", func(t *testing.T) {
		st := run(t, true, post, "POST", "4", 200)
		if st.DeadOnProbe != n || st.Retries != 0 {
			t.Fatalf("muốn deadOnProbe=%d retries=0: %+v", n, st)
		}
	})
	t.Run("noprobe-GET-retry", func(t *testing.T) {
		st := run(t, false, get, "GET", "0", 200)
		if st.Retries != n || st.DeadOnProbe != 0 {
			t.Fatalf("D4: muốn retries=%d (mỗi request cứu bằng retry): %+v", n, st)
		}
	})
	t.Run("noprobe-POST-body-502", func(t *testing.T) {
		// Body đã stream vào connection chết ⇒ KHÔNG retry ⇒ 502, client giữ
		// được. Nhưng KHÔNG phải 50/50 như G4 đăng ký: 502 làm connection bị
		// bỏ (dropDirty), pool rỗng, request kế dial mới ⇒ 200 rồi put ⇒ FIN ⇒
		// request kế nữa 502. Xen kẽ: đúng 25 lần 502 + 25 lần 200.
		up, closed := idleClosingUpstream(t)
		s, p := startProxyS(t, up, func(c *Config) { f := false; c.Pool.Probe = &f })
		rc := dialRaw(t, p)
		rc.do(t, "POST", post) // warm-up
		got := ""
		for i := 0; i < n; i++ {
			// Request 502 không tới upstream mới nào ⇒ không có Close mới để chờ
			// cho request KẾ; chỉ chờ sau request vừa thực sự được upstream phục vụ.
			if i == 0 || strings.HasSuffix(got, "2") {
				waitUpstreamClosed(t, closed)
			}
			resp, _ := rc.do(t, "POST", post)
			if resp.Close {
				t.Fatalf("req %d: 502 phải GIỮ connection client (body đã drain): %v", i, resp.Header)
			}
			got += map[int]string{200: "2", 502: "5"}[resp.Status]
		}
		st := s.PoolStats()
		want := strings.Repeat("52", n/2)
		if got != want {
			t.Fatalf("D4 (c): muốn xen kẽ 502/200 %q, có %q: %+v", want[:10], got[:10], st)
		}
		if st.Retries != 0 || st.DropDirty != n/2 {
			t.Fatalf("D4 (c): có body thì không retry, mỗi 502 một dropDirty: %+v", st)
		}
		t.Logf("%d × 502 xen kẽ %d × 200; %+v", n/2, n/2, st)
	})
	t.Run("noprobe-POST-nobody-502", func(t *testing.T) {
		// P5-4 (2026-10-02): RFC 9110 §9.2.2 "A proxy MUST NOT automatically
		// retry non-idempotent requests." POST không body vẫn là POST — D4 cũ
		// chỉ xét body nên retry nó. Muốn: như POST có body, xen kẽ 502/200.
		up, closed := idleClosingUpstream(t)
		s, p := startProxyS(t, up, func(c *Config) { f := false; c.Pool.Probe = &f })
		rc := dialRaw(t, p)
		empty := "POST /x HTTP/1.1\r\nHost: x\r\nContent-Length: 0\r\n\r\n"
		rc.do(t, "POST", empty)
		got := ""
		for i := 0; i < n; i++ {
			if i == 0 || strings.HasSuffix(got, "2") {
				waitUpstreamClosed(t, closed)
			}
			resp, _ := rc.do(t, "POST", empty)
			got += map[int]string{200: "2", 502: "5"}[resp.Status]
		}
		st := s.PoolStats()
		if want := strings.Repeat("52", n/2); got != want || st.Retries != 0 {
			t.Fatalf("POST không body bị retry: muốn %q retries=0, có %q: %+v", want[:10], got[:10], st)
		}
	})
}

// G5 — MaxIdle: 16 client song song × 50 request, MaxIdle=4 ⇒ idle == 4 sau
// khi xong, dropFull > 0, dials ≤ 16 + dropFull.
func TestPoolMaxIdle(t *testing.T) {
	s, p := startProxyS(t, startFixture(t), func(c *Config) { c.Pool.MaxIdle = 4 })
	const clients, per = 16, 50
	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.Dial("tcp", p)
			if err != nil {
				errs <- err
				return
			}
			defer c.Close()
			br := bufio.NewReader(c)
			for j := 0; j < per; j++ {
				c.SetDeadline(time.Now().Add(5 * time.Second))
				io.WriteString(c, "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
				resp, err := httpx.ReadResponse(br, httpx.DefaultLimits(), "GET")
				if err != nil {
					errs <- err
					return
				}
				if _, err := io.Copy(io.Discard, resp.Body); err != nil || resp.Status != 200 {
					errs <- fmt.Errorf("status %d err %v", resp.Status, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	st := waitStats(t, s, "800 put", func(st PoolStats) bool { return st.Puts == clients*per })
	if st.Idle != 4 || st.DropFull == 0 || st.Dials > clients+st.DropFull || st.DropDirty != 0 {
		t.Fatalf("G5: muốn idle=4, dropFull>0, dials ≤ 16+dropFull, dropDirty=0: %+v", st)
	}
	t.Logf("G5: %+v", st)
}

// Pool tắt: hành vi phase 3-4 — dial mỗi request, không reuse, không idle.
func TestPoolDisabled(t *testing.T) {
	s, p := startProxyS(t, startFixture(t), func(c *Config) { c.Pool.Disabled = true })
	rc := dialRaw(t, p)
	for i := 0; i < 20; i++ {
		if resp, _ := rc.do(t, "GET", "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n"); resp.Status != 200 {
			t.Fatal(resp.Status)
		}
	}
	if st := waitStats(t, s, "20 dial", func(st PoolStats) bool { return st.Dials == 20 }); st.Dials != 20 || st.Reuses != 0 || st.Idle != 0 {
		t.Fatalf("%+v", st)
	}
}

// MaxIdleTime: connection quá tuổi bị đóng lúc get (dropExpired), không dùng.
func TestPoolMaxIdleTime(t *testing.T) {
	s, p := startProxyS(t, startFixture(t), func(c *Config) { c.Pool.MaxIdleTime = 30 * time.Millisecond })
	rc := dialRaw(t, p)
	rc.do(t, "GET", "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
	time.Sleep(60 * time.Millisecond)
	rc.do(t, "GET", "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
	if st := s.PoolStats(); st.Dials != 2 || st.DropExpired != 1 || st.Reuses != 0 {
		t.Fatalf("%+v", st)
	}
}

// D8: Server.Close đóng cả connection rỗi trong pool.
func TestPoolClosedWithServer(t *testing.T) {
	s, p := startProxyS(t, startFixture(t), nil)
	rc := dialRaw(t, p)
	rc.do(t, "GET", "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
	if st := waitStats(t, s, "put", func(st PoolStats) bool { return st.Idle == 1 }); st.Idle != 1 {
		t.Fatalf("trước Close: %+v", st)
	}
	s.Close()
	if st := s.PoolStats(); st.Idle != 0 {
		t.Fatalf("sau Close: %+v", st)
	}
}
