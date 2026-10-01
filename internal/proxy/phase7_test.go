package proxy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaivro/edgegate/internal/fixture"
	"github.com/thaivro/edgegate/internal/httpx"
)

const dl = 300 * time.Millisecond // mọi deadline trong TestDeadline

// readUntilClose đọc tới khi server đóng (hoặc deadline client 3 s); trả byte
// nhận được, thời gian, và lỗi cuối (io.EOF = server đóng sạch).
func readUntilClose(c net.Conn) ([]byte, time.Duration, error) {
	t0 := time.Now()
	c.SetReadDeadline(t0.Add(3 * time.Second))
	b, err := io.ReadAll(c)
	if err == nil {
		err = io.EOF
	}
	return b, time.Since(t0), err
}

func near(d, want time.Duration) bool {
	return d >= want-50*time.Millisecond && d <= want+150*time.Millisecond
}

// brokenUpstream: listener trả lời mọi connection bằng fn (upstream "hỏng có
// chủ đích" cho deadline phía upstream).
func brokenUpstream(t *testing.T, fn func(net.Conn)) string {
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
			go func() { defer c.Close(); fn(c) }()
		}
	}()
	return ln.Addr().String()
}

// G1 — bảy deadline (I3), mỗi cái fire trong test, đúng status, đúng thời điểm.
func TestDeadline(t *testing.T) {
	tight := func(c *Config) {
		c.Limits.HeaderTimeout, c.Limits.BodyTimeout, c.Limits.IdleTimeout = dl, dl, dl
		c.DialTimeout, c.UpstreamHeaderTimeout, c.UpstreamBodyTimeout = dl, dl, dl
		c.LB.Health.Disabled = true
	}
	t.Run("header-408", func(t *testing.T) {
		p := startProxy(t, startFixture(t), tight)
		c, _ := net.Dial("tcp", p)
		defer c.Close()
		io.WriteString(c, "GET /hello HTTP/1.1\r\nHost: x\r\n") // head không bao giờ xong
		b, el, err := readUntilClose(c)
		t.Logf("head dở: %q sau %s (%v)", firstLine(b), el.Round(time.Millisecond), err)
		if !strings.HasPrefix(string(b), "HTTP/1.1 408") || !near(el, dl) {
			t.Fatalf("muốn 408 sau ≈ %s", dl)
		}
	})
	t.Run("body-408", func(t *testing.T) {
		p := startProxy(t, startFixture(t), tight)
		c, _ := net.Dial("tcp", p)
		defer c.Close()
		io.WriteString(c, "POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\nabc")
		b, el, err := readUntilClose(c)
		t.Logf("body dở: %q sau %s (%v)", firstLine(b), el.Round(time.Millisecond), err)
		if !strings.HasPrefix(string(b), "HTTP/1.1 408") || !near(el, dl) {
			t.Fatalf("muốn 408 sau ≈ %s", dl)
		}
	})
	t.Run("idle-close", func(t *testing.T) {
		p := startProxy(t, startFixture(t), tight)
		c, _ := net.Dial("tcp", p)
		defer c.Close()
		b, el, err := readUntilClose(c)
		t.Logf("không gửi gì: %d byte sau %s (%v)", len(b), el.Round(time.Millisecond), err)
		if len(b) != 0 || !errors.Is(err, io.EOF) || !near(el, dl) {
			t.Fatalf("muốn đóng im lặng sau ≈ %s", dl)
		}
	})
	t.Run("dial-502", func(t *testing.T) {
		// 10.255.255.1: không route ⇒ SYN rơi ⇒ chỉ deadline mới cứu. Một backend
		// ⇒ D9 chọn lại CHÍNH nó ⇒ hai lần dial.
		p := startProxy(t, "10.255.255.1:81", tight)
		c, _ := net.Dial("tcp", p)
		defer c.Close()
		t0 := time.Now()
		io.WriteString(c, "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
		br := bufio.NewReader(c)
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		resp, err := httpx.ReadResponse(br, httpx.DefaultLimits(), "GET")
		el := time.Since(t0)
		if err != nil {
			t.Fatalf("đọc: %v sau %s", err, el)
		}
		t.Logf("dial không route: %d sau %s (DialTimeout %s)", resp.Status, el.Round(time.Millisecond), dl)
		if resp.Status != 502 || el > 2*dl+150*time.Millisecond {
			t.Fatalf("muốn 502 trong ≤ 2×DialTimeout")
		}
	})
	t.Run("upstream-head-504", func(t *testing.T) {
		p := startProxy(t, startFixture(t), tight)
		c, _ := net.Dial("tcp", p)
		defer c.Close()
		t0 := time.Now()
		io.WriteString(c, "GET /slow?ms=2000 HTTP/1.1\r\nHost: x\r\n\r\n")
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		resp, err := httpx.ReadResponse(bufio.NewReader(c), httpx.DefaultLimits(), "GET")
		el := time.Since(t0)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("upstream không trả head: %d sau %s", resp.Status, el.Round(time.Millisecond))
		if resp.Status != 504 || !near(el, dl) {
			t.Fatalf("muốn 504 sau ≈ %s", dl)
		}
	})
	t.Run("upstream-body-close", func(t *testing.T) {
		up := brokenUpstream(t, func(c net.Conn) {
			bufio.NewReader(c).ReadString('\n')
			io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nabc")
			time.Sleep(3 * time.Second) // 97 byte không bao giờ tới
		})
		p := startProxy(t, up, tight)
		c, _ := net.Dial("tcp", p)
		defer c.Close()
		t0 := time.Now()
		io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
		b, _, err := readUntilClose(c)
		el := time.Since(t0)
		t.Logf("upstream dừng giữa body: %d byte, %q sau %s (%v)", len(b), firstLine(b), el.Round(time.Millisecond), err)
		if strings.Count(string(b), "HTTP/1.1 ") != 1 || !strings.HasSuffix(string(b), "abc") || !near(el, dl) {
			t.Fatalf("muốn đúng MỘT head + 3 byte body rồi đóng sau ≈ %s", dl)
		}
	})
	t.Run("slow-reader", func(t *testing.T) {
		// Chỉ deadline GHI phía client được fire ở ca này: hai deadline phía
		// upstream nới ra 2 s — dưới -race fixture sinh 64 MiB chậm tới mức
		// UpstreamHeaderTimeout 300 ms fire trước (504) và test đo nhầm deadline.
		s, p := startProxyS(t, startFixture(t), func(c *Config) {
			tight(c)
			c.UpstreamHeaderTimeout, c.UpstreamBodyTimeout = 2*time.Second, 2*time.Second
		})
		c, _ := net.Dial("tcp", p)
		defer c.Close()
		if tc, ok := c.(*net.TCPConn); ok {
			tc.SetReadBuffer(4 << 10) // buffer nhận nhỏ ⇒ proxy bị chặn ghi sớm
		}
		io.WriteString(c, "GET /large?n=67108864 HTTP/1.1\r\nHost: x\r\n\r\n") // 64 MiB
		// Đồng hồ bắt đầu khi byte đầu của head tới: proxy đặt write deadline
		// ngay trước khi ghi head. Đọc đúng 1 byte rồi ngừng đọc hẳn.
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err != nil {
			t.Fatalf("không nhận được byte đầu: %v", err)
		}
		t0 := time.Now()
		for time.Since(t0) < 3*time.Second && s.ResilienceStats().ConnsActive > 0 {
			time.Sleep(5 * time.Millisecond)
		}
		el := time.Since(t0)
		ps := s.PoolStats()
		t.Logf("client không đọc 64 MiB: goroutine proxy thoát sau %s; pool %+v", el.Round(time.Millisecond), ps)
		if s.ResilienceStats().ConnsActive != 0 || !near(el, dl) {
			t.Fatalf("goroutine proxy phải thoát sau ≈ BodyTimeout %s", dl)
		}
		if ps.Idle != 0 || ps.DropDirty == 0 {
			t.Fatalf("connection upstream dở body không được về pool: %+v", ps)
		}
	})
}

func firstLine(b []byte) string {
	s, _, _ := strings.Cut(string(b), "\r\n")
	return s
}

// G4 (đơn vị) — rate limit per-IP SAU ranh giới tin cậy (I6). Peer không tin
// gửi XFF ngẫu nhiên mỗi request: vẫn chung một bucket. nodefense7 (khoá =
// XFF thô) ⇒ spoof qua hết ⇒ ĐỎ.
func TestRateLimitPerIP(t *testing.T) {
	s, p := startProxyS(t, startFixture(t), func(c *Config) { c.RateLimit = RateLimitConfig{Rate: 1, Burst: 5} })
	rc := dialRaw(t, p)
	st := map[int]int{}
	var retryAfter string
	for i := 0; i < 20; i++ {
		resp, _ := rc.do(t, "GET", fmt.Sprintf("GET /hello HTTP/1.1\r\nHost: x\r\nX-Forwarded-For: 203.0.113.%d\r\n\r\n", i))
		st[resp.Status]++
		if resp.Status == 429 {
			retryAfter = resp.Header.Get("Retry-After")
		}
	}
	t.Logf("20 request, XFF giả khác nhau, peer không tin: %v (Retry-After %q), stats %+v", st, retryAfter, s.ResilienceStats())
	if st[200] != 5 || st[429] != 15 || retryAfter != "1" {
		t.Fatalf("XFF giả không được tách bucket: %v", st)
	}

	// Peer TIN (127.0.0.1 trong TrustedProxies): XFF là của proxy ta tin ⇒ mỗi
	// client thật một bucket.
	_, p2 := startProxyS(t, startFixture(t), func(c *Config) {
		c.RateLimit = RateLimitConfig{Rate: 1, Burst: 5}
		c.TrustedProxies = []string{"127.0.0.1"}
	})
	rc2 := dialRaw(t, p2)
	st2 := map[int]int{}
	for i := 0; i < 20; i++ {
		resp, _ := rc2.do(t, "GET", fmt.Sprintf("GET /hello HTTP/1.1\r\nHost: x\r\nX-Forwarded-For: 198.51.100.%d\r\n\r\n", i%2))
		st2[resp.Status]++
	}
	if st2[200] != 10 {
		t.Fatalf("qua proxy tin, 2 client thật × burst 5 = 10 cái 200, được %v", st2)
	}
}

// G7 (đơn vị) — shed: MaxInflight 2, MaxQueue 2, QueueTimeout 50 ms, upstream
// 200 ms. 10 request cùng lúc ⇒ 2 chạy, 2 chờ rồi hết giờ, 6 bị từ chối ngay.
// Sau cùng slot và hàng về 0 (I7).
func TestShedQueue(t *testing.T) {
	sim := fixture.NewSim("slow", 200*time.Millisecond, 0)
	up, stop, _ := fixture.ListenAndServeSim("127.0.0.1:0", sim)
	defer stop()
	s, p := startProxyS(t, up, func(c *Config) {
		c.Shed = ShedConfig{MaxInflight: 2, MaxQueue: 2, QueueTimeout: 50 * time.Millisecond}
	})
	var mu sync.Mutex
	st := map[int]int{}
	fast := 0
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rc := dialRaw(t, p)
			t0 := time.Now()
			resp, _ := rc.do(t, "GET", "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
			mu.Lock()
			st[resp.Status]++
			if resp.Status == 503 && time.Since(t0) < 100*time.Millisecond {
				fast++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	rs := s.ResilienceStats()
	t.Logf("10 request cùng lúc: %v (503 < 100 ms: %d), stats %+v", st, fast, rs)
	if st[200] != 2 || st[503] != 8 || rs.ShedQueueFull != 6 || rs.ShedTimeout != 2 {
		t.Fatalf("muốn 2×200, 8×503 (6 hàng đầy + 2 hết giờ)")
	}
	if rs.Inflight != 0 || rs.Queued != 0 {
		t.Fatalf("slot/hàng không về 0 sau khi xong: %+v", rs)
	}
}

// G6 (đơn vị) — retry budget. Upstream đóng MỌI connection dùng lại trước khi
// trả byte nào; connection MỚI thì phục vụ. Budget 0 % + sàn 0.1/s × 10 s =
// đúng 1 retry. Chuỗi: #1 dial mới 200; #2 reused bị đóng ⇒ retry (budget) ⇒
// 200; #3 reused bị đóng ⇒ hết budget ⇒ 502 (connection bị bỏ); #4 pool rỗng
// ⇒ dial mới ⇒ 200; … ⇒ 11×200, 9×502. nodefense7 (retry mù) ⇒ 20×200 ⇒ ĐỎ.
func TestRetryBudget(t *testing.T) {
	sim := fixture.NewSim("drop", 0, 0)
	sim.SetDropReused(1)
	up, stop, _ := fixture.ListenAndServeSim("127.0.0.1:0", sim)
	defer stop()
	s, p := startProxyS(t, up, func(c *Config) {
		c.RetryBudget = RetryBudgetConfig{Percent: -1, MinPerSec: 0.1, Window: 10 * time.Second}
		c.LB.Outlier.Disabled = true
	})
	rc := dialRaw(t, p)
	st := getN(t, rc, 20, "/hello", "")
	rs := s.ResilienceStats()
	t.Logf("20 GET, upstream đóng mọi connection dùng lại: %v, retry cho %d / từ chối %d, sim dropped %d",
		st, rs.RetryAllowed, rs.RetryDenied, sim.Dropped())
	if rs.RetryAllowed != 1 || rs.RetryDenied != 9 || st[502] != 9 || st[200] != 11 {
		t.Fatalf("budget 1 retry ⇒ 11×200, 9×502, retry cho 1 / từ chối 9")
	}
}

// D8 — drain: connection rỗi bị đóng ngay không byte nào; request đang chạy
// nhận đủ response kèm Connection: close; listener đóng.
func TestDrainInflightAndIdle(t *testing.T) {
	sim := fixture.NewSim("slow", 300*time.Millisecond, 0)
	up, stop, _ := fixture.ListenAndServeSim("127.0.0.1:0", sim)
	defer stop()
	s, p := startProxyS(t, up, nil)
	idle := dialRaw(t, p)
	idle.do(t, "GET", "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n") // keep-alive, giờ rỗi
	busy, _ := net.Dial("tcp", p)
	defer busy.Close()
	io.WriteString(busy, "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
	time.Sleep(50 * time.Millisecond) // request của busy đang ở upstream
	done := make(chan int)
	go func() { done <- s.Drain(2 * time.Second) }()

	b, el, err := readUntilClose(idle.c)
	t.Logf("connection rỗi: %d byte, đóng sau %s (%v)", len(b), el.Round(time.Millisecond), err)
	if len(b) != 0 || el > 100*time.Millisecond {
		t.Fatalf("connection rỗi phải bị đóng ngay, không byte nào")
	}
	busy.SetReadDeadline(time.Now().Add(2 * time.Second))
	br := bufio.NewReader(busy)
	resp, err := httpx.ReadResponse(br, httpx.DefaultLimits(), "GET")
	if err != nil {
		t.Fatalf("request đang chạy mất response: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	t.Logf("request đang chạy: %d Connection=%q", resp.Status, resp.Header.Get("Connection"))
	if resp.Status != 200 || !resp.Close {
		t.Fatal("muốn 200 + Connection: close")
	}
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("sau response cuối phải EOF: %v", err)
	}
	if f := <-done; f != 0 {
		t.Fatalf("Drain cưỡng bức %d connection", f)
	}
	if c, err := net.DialTimeout("tcp", p, 200*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("listener còn nhận connection sau drain")
	}
	rs := s.ResilienceStats()
	if rs.DrainedIdle != 1 || rs.ConnsActive != 0 {
		t.Fatalf("stats: %+v", rs)
	}
}

// D8 — hết hạn drain: upstream treo ⇒ Close cưỡng bức, đếm được.
func TestDrainTimeoutForces(t *testing.T) {
	sim := fixture.NewSim("hang", 0, 0)
	sim.SetHang(true)
	up, stop, _ := fixture.ListenAndServeSim("127.0.0.1:0", sim)
	defer stop()
	s, p := startProxyS(t, up, func(c *Config) { c.UpstreamHeaderTimeout = 5 * time.Second })
	c, _ := net.Dial("tcp", p)
	defer c.Close()
	io.WriteString(c, "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
	time.Sleep(50 * time.Millisecond)
	t0 := time.Now()
	f := s.Drain(200 * time.Millisecond)
	if f != 1 || time.Since(t0) > time.Second {
		t.Fatalf("Drain(200ms) với upstream treo: forced %d sau %s", f, time.Since(t0))
	}
}

// G3 (đơn vị) — MaxConns 2: hai connection rỗi chiếm hết ⇒ connection thứ ba
// nối được (backlog kernel) nhưng KHÔNG được phục vụ; nhả một ⇒ được.
func TestMaxConnsBlocksAccept(t *testing.T) {
	_, p := startProxyS(t, startFixture(t), func(c *Config) { c.MaxConns = 2; c.Limits.IdleTimeout = 5 * time.Second })
	a, _ := net.Dial("tcp", p)
	b, _ := net.Dial("tcp", p)
	defer b.Close()
	time.Sleep(50 * time.Millisecond)
	c, err := net.Dial("tcp", p)
	if err != nil {
		t.Fatalf("dial thứ ba phải thành công (backlog): %v", err)
	}
	defer c.Close()
	io.WriteString(c, "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
	c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("connection thứ ba không được phục vụ khi đầy, muốn timeout: %v", err)
	}
	a.Close()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := httpx.ReadResponse(bufio.NewReader(c), httpx.DefaultLimits(), "GET")
	if err != nil || resp.Status != 200 {
		t.Fatalf("nhả một chỗ ⇒ thứ ba được phục vụ: %v", err)
	}
}
