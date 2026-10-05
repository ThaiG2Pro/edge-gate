package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/thaivro/edgegate/internal/fixture"
	"github.com/thaivro/edgegate/internal/httpx"
	"github.com/thaivro/edgegate/internal/lb"
)

// startLB dựng proxy trước n fixture + các addr thêm (chết), thuật toán algo.
func startLB(t *testing.T, algo string, n int, extra []string, mut func(*Config)) (*Server, string, []string) {
	t.Helper()
	var ups []string
	for i := 0; i < n; i++ {
		ups = append(ups, startFixture(t))
	}
	ups = append(ups, extra...)
	s, p := startProxyS(t, ups[0], func(c *Config) {
		c.Upstreams = ups
		c.LB = lb.Config{Algo: algo, Health: lb.HealthConfig{Disabled: true}, Outlier: lb.OutlierConfig{Disabled: true}}
		if mut != nil {
			mut(c)
		}
	})
	return s, p, ups
}

// deadAddr: một port vừa đóng — dial bị RST ngay.
func deadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := ln.Addr().String()
	ln.Close()
	return a
}

// getN gửi n GET target trên MỘT connection keep-alive, extra header tuỳ ý;
// trả map status→count.
func getN(t *testing.T, rc *rawConn, n int, target, extra string) map[int]int {
	t.Helper()
	st := map[int]int{}
	for i := 0; i < n; i++ {
		resp, _ := rc.do(t, "GET", fmt.Sprintf("GET %s HTTP/1.1\r\nHost: x\r\n%s\r\n", target, extra))
		st[resp.Status]++
	}
	return st
}

// G7 — pool THEO HOST (P5-3): 400 request RR qua 4 backend trên một client
// keep-alive ⇒ đúng 4 dial, 396 reuse, 4 idle; balancer chia 100/100/100/100.
func TestLBPoolPerHost(t *testing.T) {
	s, p, ups := startLB(t, "rr", 4, nil, nil)
	rc := dialRaw(t, p)
	if st := getN(t, rc, 400, "/hello", ""); st[200] != 400 {
		t.Fatalf("status: %v", st)
	}
	ps := waitStats(t, s, "idle=4", func(ps PoolStats) bool { return ps.Idle == 4 })
	if ps.Dials != 4 || ps.Reuses != 396 || ps.Idle != 4 {
		t.Fatalf("pool theo host: %+v", ps)
	}
	for _, u := range ups {
		if one := s.PoolStatsFor(u); one.Dials != 1 || one.Idle != 1 {
			t.Fatalf("pool %s: %+v", u, one)
		}
	}
	for _, b := range s.LBStats().Backends {
		if b.Picks != 100 || b.Inflight != 0 {
			t.Fatalf("lb: %+v", b)
		}
	}
}

// G5 — một backend chết: dial lỗi ⇒ chọn backend khác (D9), client thấy 0 lỗi;
// active health đánh dấu unhealthy trong ≤ fall×interval rồi RR ngừng chọn nó.
func TestLBDeadBackend(t *testing.T) {
	dead := deadAddr(t)
	s, p, _ := startLB(t, "rr", 2, []string{dead}, func(c *Config) {
		c.LB.Health = lb.HealthConfig{Interval: 100 * time.Millisecond, Timeout: 200 * time.Millisecond, Fall: 3, Rise: 2}
	})
	rc := dialRaw(t, p)
	body := "abc"
	// Cả GET và POST có body: dial lỗi thì body còn nguyên trong br ⇒ re-pick được.
	for i := 0; i < 30; i++ {
		var resp *httpx.Response
		if i%2 == 0 {
			resp, _ = rc.do(t, "GET", "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
		} else {
			resp, _ = rc.do(t, "POST", fmt.Sprintf("POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n%s", len(body), body))
		}
		if resp.Status != 200 {
			t.Fatalf("request %d: status %d — dial lỗi phải được chọn lại backend, không 502", i, resp.Status)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	var deadSt lb.BackendStats
	for time.Now().Before(deadline) {
		for _, b := range s.LBStats().Backends {
			if b.Addr == dead {
				deadSt = b
			}
		}
		if !deadSt.Healthy {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if deadSt.Healthy {
		t.Fatalf("active health không đánh dấu %s unhealthy: %+v", dead, deadSt)
	}
	if deadSt.Fails == 0 || deadSt.Picks == 0 {
		t.Fatalf("backend chết chưa từng được chọn/thất bại? %+v", deadSt)
	}
	before := deadSt.Picks
	if st := getN(t, rc, 20, "/hello", ""); st[200] != 20 {
		t.Fatalf("sau unhealthy: %v", st)
	}
	for _, b := range s.LBStats().Backends {
		if b.Addr == dead && b.Picks != before {
			t.Fatalf("RR vẫn chọn backend unhealthy: %d → %d", before, b.Picks)
		}
	}
}

// D8 — không còn backend nào: 503 (không 502), connection client giữ được.
func TestLBAllDown503(t *testing.T) {
	dead := deadAddr(t)
	s, p, _ := startLB(t, "rr", 0, []string{dead}, func(c *Config) {
		c.LB.Health = lb.HealthConfig{Interval: 30 * time.Millisecond, Timeout: 100 * time.Millisecond, Fall: 1, Rise: 1}
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && s.LBStats().Backends[0].Healthy {
		time.Sleep(10 * time.Millisecond)
	}
	rc := dialRaw(t, p)
	st := getN(t, rc, 3, "/hello", "")
	if st[503] != 3 {
		t.Fatalf("kỳ vọng 3×503 trên cùng connection: %v", st)
	}
	body := "xyz"
	resp, _ := rc.do(t, "POST", fmt.Sprintf("POST /echo HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n%s", len(body), body))
	if resp.Status != 503 {
		t.Fatalf("POST có body khi không backend: %d", resp.Status)
	}
	if s.LBStats().NoAvailable != 4 {
		t.Fatalf("NoAvailable = %d", s.LBStats().NoAvailable)
	}
}

// D5 — consistent hash theo header: cùng X-Session ⇒ cùng backend; thiếu
// header ⇒ rơi về IP client (loopback ⇒ một backend).
func TestLBHashHeaderSticky(t *testing.T) {
	s, p, _ := startLB(t, "chash", 3, nil, func(c *Config) { c.LB.HashHeader = "X-Session" })
	rc := dialRaw(t, p)
	getN(t, rc, 30, "/hello", "X-Session: alice\r\n")
	one := 0
	for _, b := range s.LBStats().Backends {
		if b.Picks == 30 {
			one++
		} else if b.Picks != 0 {
			t.Fatalf("cùng session mà rải: %+v", b)
		}
	}
	if one != 1 {
		t.Fatalf("session alice không dính một backend: %+v", s.LBStats().Backends)
	}
	// 300 session khác nhau ⇒ cả 3 backend đều có tải.
	for i := 0; i < 300; i++ {
		getN(t, rc, 1, "/hello", fmt.Sprintf("X-Session: u%d\r\n", i))
	}
	for _, b := range s.LBStats().Backends {
		if b.Picks < 50 {
			t.Fatalf("300 session mà backend %s chỉ %d picks", b.Addr, b.Picks)
		}
	}
}

// D7 — outlier: backend trả 503 liên tiếp bị eject; con thứ hai KHÔNG bị eject
// vì trần 50 %; client vẫn nhận response từ con còn lại.
func TestLBOutlierEjects5xx(t *testing.T) {
	s, p, _ := startLB(t, "rr", 2, nil, func(c *Config) {
		c.LB.Outlier = lb.OutlierConfig{Consecutive: 3, BaseEject: 500 * time.Millisecond, MaxEject: time.Second, MaxEjectPercent: 50}
	})
	rc := dialRaw(t, p)
	st := getN(t, rc, 20, "/status?code=503", "")
	if st[503] != 20 {
		t.Fatalf("status: %v", st)
	}
	lbs := s.LBStats()
	ejected := 0
	for _, b := range lbs.Backends {
		if b.Ejected {
			ejected++
		}
	}
	if ejected != 1 || lbs.EjectRefused == 0 {
		t.Fatalf("kỳ vọng đúng 1 eject + có refused: %+v", lbs)
	}
	// Con còn lại gánh hết: 10 request /hello đều 200, picks chỉ tăng ở nó.
	if st := getN(t, rc, 10, "/hello", ""); st[200] != 10 {
		t.Fatalf("sau eject: %v", st)
	}
	for _, b := range s.LBStats().Backends {
		if b.Ejected && b.Picks != pickOf(lbs, b.Addr) {
			t.Fatalf("backend bị eject vẫn được chọn: %+v", b)
		}
	}
}

func pickOf(st lb.Stats, addr string) int64 {
	for _, b := range st.Backends {
		if b.Addr == addr {
			return b.Picks
		}
	}
	return -1
}

// P5-2 — con quá tuổi ở ĐÁY stack. `get` chỉ nhìn đỉnh: đỉnh luôn tươi vì
// được dùng liên tục, 4 con dưới già mãi. Sau sửa, `put` quét đáy ⇒ Idle 1.
func TestPoolExpiredAtBottom(t *testing.T) {
	s, p := startProxyS(t, startFixture(t), func(c *Config) { c.Pool.MaxIdleTime = 100 * time.Millisecond })
	// 5 client song song vào /slow?ms=80 ⇒ 5 connection upstream cùng lúc.
	done := make(chan struct{})
	for i := 0; i < 5; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			c, err := net.Dial("tcp", p)
			if err != nil {
				return
			}
			defer c.Close()
			io.WriteString(c, "GET /slow?ms=80 HTTP/1.1\r\nHost: x\r\n\r\n")
			resp, err := httpx.ReadResponse(bufio.NewReader(c), httpx.DefaultLimits(), "GET")
			if err == nil {
				io.Copy(io.Discard, resp.Body)
			}
		}()
	}
	for i := 0; i < 5; i++ {
		<-done
	}
	if ps := waitStats(t, s, "idle=5", func(ps PoolStats) bool { return ps.Idle == 5 }); ps.Idle != 5 {
		t.Fatalf("chưa có 5 idle: %+v", ps)
	}
	// Giữ ĐỈNH tươi: 30 request cách 10 ms (300 ms > MaxIdleTime 100 ms) trên một client.
	rc := dialRaw(t, p)
	for i := 0; i < 30; i++ {
		rc.do(t, "GET", "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
		time.Sleep(10 * time.Millisecond)
	}
	ps := waitStats(t, s, "idle=1", func(ps PoolStats) bool { return ps.Idle == 1 })
	if ps.Idle != 1 || ps.DropExpired != 4 {
		t.Fatalf("đáy stack không được dọn: %+v (kỳ vọng Idle 1, DropExpired 4)", ps)
	}
}

// P5-5 — MaxIdleTime phải NGẮN hơn idle timeout của upstream: bên đóng trước
// không bao giờ thấy FIN bất ngờ. Upstream raw đóng sau 100 ms rỗi; request
// cách 150 ms. MaxIdleTime 50 ms ⇒ pool bỏ trước (DropExpired), probe không
// thấy gì (DeadOnProbe 0). MaxIdleTime 60 s ⇒ probe phải đỡ 19 FIN.
func TestMaxIdleTimeShorterThanUpstream(t *testing.T) {
	var pc PoolConfig
	pc.withDefaults()
	if pc.MaxIdleTime >= 60*time.Second {
		t.Fatalf("MaxIdleTime mặc định %s ≥ 60 s = nginx keepalive_timeout upstream ⇒ pool là bên thấy FIN", pc.MaxIdleTime)
	}
	idleUpstream := func() string {
		return rawServer(t, func(c net.Conn, br *bufio.Reader) {
			for {
				c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
				if _, err := httpx.ReadRequest(br, httpx.DefaultLimits()); err != nil {
					return // rỗi quá 100 ms ⇒ upstream đóng (FIN)
				}
				io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
			}
		})
	}
	run := func(maxIdle time.Duration) PoolStats {
		s, p := startProxyS(t, idleUpstream(), func(c *Config) { c.Pool.MaxIdleTime = maxIdle })
		rc := dialRaw(t, p)
		for i := 0; i < 20; i++ {
			if i > 0 {
				time.Sleep(150 * time.Millisecond)
			}
			if resp, _ := rc.do(t, "GET", "GET /x HTTP/1.1\r\nHost: x\r\n\r\n"); resp.Status != 200 {
				t.Fatalf("MaxIdleTime %s, request %d: %d", maxIdle, i, resp.Status)
			}
		}
		return waitStats(t, s, "dials=20", func(ps PoolStats) bool { return ps.Dials == 20 })
	}
	short := run(50 * time.Millisecond)
	if short.DeadOnProbe != 0 || short.DropExpired != 19 || short.Dials != 20 {
		t.Fatalf("MaxIdleTime 50 ms: %+v (kỳ vọng DeadOnProbe 0, DropExpired 19)", short)
	}
	long := run(60 * time.Second)
	if long.DeadOnProbe != 19 || long.DropExpired != 0 {
		t.Fatalf("MaxIdleTime 60 s: %+v (kỳ vọng DeadOnProbe 19 — probe phải đỡ FIN)", long)
	}
	t.Logf("50 ms: %+v\n60 s: %+v", short, long)
}

// G5 (đúng kịch bản đăng ký) — giết b3 GIỮA bài rồi cho sống lại cùng port.
// interval 200 ms, fall 3, rise 2. Đo: kill → unhealthy, số lần b3 còn bị chọn
// sau unhealthy (phải 0), revive → healthy → nhận tải lại; client đếm non-200.
// TestLBDeadBackend chỉ phủ "chết từ đầu"; ca này phủ pool đang giữ connection
// tới b3 lúc nó chết (probe MSG_PEEK phase 5 phải bắt FIN, rồi dial lỗi ⇒ D9).
func TestLBKillRevive(t *testing.T) {
	// P6-5: so cửa sổ dial lỗi (số request đập vào backend chết trước khi bị
	// loại) giữa chỉ-active-health và có-passive-outlier. Active mất Fall×Interval
	// = 3×200 ms = 600 ms mới đánh dấu down; passive (Consecutive 5) loại sau ~5
	// lỗi liên tiếp. Đây là ĐẾM qua máy trạng thái, không phải percentile
	// latency ⇒ đo được cả trên WSL2. Từ 37cd9d9 kịch bản chạy 32 client không
	// nghỉ (trước: 1 client, 1 ms/request ⇒ 116-122 vs 8): WSL2 đo active-only
	// 6500-7200, outlier-on 11-34 — tối đa 32 request đang bay tới b3 lúc nó
	// chết đều lỗi trước khi passive kịp loại.
	t.Run("active-only", func(t *testing.T) {
		fails, growth := killReviveScenario(t, nil)
		t.Logf("active-only: fails trong cửa sổ = %d (cửa sổ rộng vì chỉ active 600 ms)", fails)
		if fails < 500 {
			t.Fatalf("active-only: cửa sổ phải rộng, fails=%d (kỳ vọng vài nghìn)", fails)
		}
		if growth != 0 {
			t.Fatalf("active-only: unhealthy rồi không được pick nữa, nhưng picks tăng %d", growth)
		}
	})
	t.Run("outlier-on", func(t *testing.T) {
		fails, _ := killReviveScenario(t, func(c *Config) {
			c.LB.Outlier = lb.OutlierConfig{Consecutive: 5, BaseEject: 150 * time.Millisecond,
				MaxEject: 150 * time.Millisecond, MaxEjectPercent: 50}
		})
		// Passive loại sau 5 lỗi; eject 150 ms, hết hạn ⇒ half-open cho 1 probe
		// (dial lỗi, D9 né sang backend khác ⇒ client vẫn 200). Qua ~600 ms cửa
		// sổ down: ≤ 32 đang bay + 5 + vài probe. Phải « vài nghìn của active-only.
		t.Logf("outlier-on: fails trong cửa sổ = %d (passive cắt còn vài chục)", fails)
		if fails > 100 {
			t.Fatalf("outlier-on: passive phải cắt cửa sổ, fails=%d (active-only vài nghìn)", fails)
		}
	})
}

// killReviveScenario: b0..b2 sống + b3; giết b3, chờ active đánh dấu down, hồi
// sinh b3, chờ về healthy. Trả (fails trong cửa sổ down, số picks tăng SAU khi
// down). Client keep-alive phải thấy TOÀN 200 (dial lỗi được D9 né sang backend
// khác). mut tinh chỉnh Config (vd bật outlier).
func killReviveScenario(t *testing.T, mut func(*Config)) (fails int64, growthAfterDown int64) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b3 := ln.Addr().String()
	us := &http.Server{Handler: fixture.Handler()}
	go us.Serve(ln)
	s, p, _ := startLB(t, "rr", 3, []string{b3}, func(c *Config) {
		c.LB.Health = lb.HealthConfig{Interval: 200 * time.Millisecond, Timeout: 200 * time.Millisecond, Fall: 3, Rise: 2}
		// 32 client, rr 4 node ⇒ ~25 % request trúng b3 lúc chết ⇒ cần retry
		// D9 cho 25 % > budget mặc định 10 % (phase 7 D6) ⇒ phần vượt thành 502
		// đúng thiết kế (đo: 502 = RetryDenied = 1160). Kịch bản này đo HEALTH,
		// không đo budget ⇒ nới budget để invariant « toàn 200 » chỉ còn phụ thuộc D9.
		c.RetryBudget = RetryBudgetConfig{Percent: 1}
		if mut != nil {
			mut(c)
		}
	})
	b3st := func() lb.BackendStats {
		for _, b := range s.LBStats().Backends {
			if b.Addr == b3 {
				return b
			}
		}
		t.Fatal("không thấy b3")
		return lb.BackendStats{}
	}
	waitB3 := func(healthy bool) time.Duration {
		t0 := time.Now()
		for time.Since(t0) < 3*time.Second {
			if b3st().Healthy == healthy {
				return time.Since(t0)
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Fatalf("b3 không về healthy=%v sau 3 s", healthy)
		return 0
	}

	stop := make(chan struct{})
	done := make(chan map[int]int)
	var mu sync.Mutex
	totalSt := map[int]int{}
	var wg sync.WaitGroup
	for w := 0; w < 32; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.Dial("tcp", p)
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			br := bufio.NewReader(c)
			bw := bufio.NewWriter(c)
			for {
				select {
				case <-stop:
					return
				default:
				}
				c.SetDeadline(time.Now().Add(3 * time.Second))
				bw.WriteString("GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
				if err := bw.Flush(); err != nil {
					return
				}
				resp, err := httpx.ReadResponse(br, httpx.DefaultLimits(), "GET")
				if err != nil {
					mu.Lock()
					totalSt[-1]++
					mu.Unlock()
					return
				}
				io.Copy(io.Discard, resp.Body)
				mu.Lock()
				totalSt[resp.Status]++
				mu.Unlock()
			}
		}()
	}
	go func() {
		wg.Wait()
		done <- totalSt
	}()

	time.Sleep(300 * time.Millisecond)
	picksKill := b3st().Picks
	us.Close() // đóng listener + mọi connection đang mở (pool đang giữ con tới b3)
	down := waitB3(false)
	picksDown := b3st()
	time.Sleep(300 * time.Millisecond)
	picksAfter := b3st().Picks

	ln2, err := net.Listen("tcp", b3)
	if err != nil {
		t.Fatalf("mở lại %s: %v", b3, err)
	}
	us2 := &http.Server{Handler: fixture.Handler()}
	go us2.Serve(ln2)
	t.Cleanup(func() { us2.Close() })
	up := waitB3(true)
	upPicks := b3st().Picks
	t0 := time.Now()
	for b3st().Picks == upPicks && time.Since(t0) < time.Second {
		time.Sleep(time.Millisecond)
	}
	firstPick := time.Since(t0)
	close(stop)
	st := <-done

	t.Logf("kill→unhealthy %s (fall 3 × 200 ms); b3 picks lúc kill %d, lúc unhealthy %d (fails %d), 300 ms sau %d",
		down.Round(time.Millisecond), picksKill, picksDown.Picks, picksDown.Fails, picksAfter)
	t.Logf("revive→healthy %s (rise 2), healthy→pick đầu %s; client: %v", up.Round(time.Millisecond), firstPick.Round(time.Millisecond), st)
	if st[200] == 0 || len(st) != 1 {
		t.Fatalf("client phải thấy toàn 200: %v (retry bị budget từ chối: %d)", st, s.ResilienceStats().RetryDenied)
	}
	if down > time.Second || up > time.Second {
		t.Fatalf("health quá chậm: down %s up %s", down, up)
	}
	return picksDown.Fails, picksAfter - picksDown.Picks
}
