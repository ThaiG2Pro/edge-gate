package proxy

// Phase 7: các cơ chế chống quá tải nằm giữa "đã đọc xong head" và "chọn
// backend" — rate limit per-IP (D4), load shedding theo request (D7), retry
// budget (D6) — cộng vòng đời server: trần connection (D3, chỉ để chứng minh
// G3), graceful drain (D8), SO_REUSEPORT (D9).

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/thaivro/edgegate/internal/httpx"
	"github.com/thaivro/edgegate/internal/limit"
)

// RateLimitConfig: token bucket per-IP. Rate 0 = tắt.
type RateLimitConfig struct {
	Rate    float64 // token/s mỗi IP
	Burst   float64
	MaxKeys int // trần số IP nhớ (LRU); 0 = 10 000
}

// ShedConfig: trần request đang chạy + hàng đợi có trần. MaxInflight 0 = tắt.
type ShedConfig struct {
	MaxInflight  int
	MaxQueue     int           // số request được chờ slot; vượt ⇒ 503 ngay
	QueueTimeout time.Duration // chờ quá ⇒ 503; 0 = 50 ms
}

// RetryBudgetConfig: retry ≤ Percent·request + MinPerSec·Window trong cửa sổ
// trượt. Zero value = 10 %, 10/s, 10 s. Percent < 0 ⇒ 0 % (chỉ còn sàn).
type RetryBudgetConfig struct {
	Percent   float64
	MinPerSec float64
	Window    time.Duration
}

// ResilienceStats: bộ đếm phase 7.
type ResilienceStats struct {
	RateLimited   int64 // 429
	ShedQueueFull int64 // 503 vì hàng đợi đầy
	ShedTimeout   int64 // 503 vì chờ slot quá QueueTimeout
	Inflight      int64 // slot đang giữ
	Queued        int64 // đang chờ slot
	RetryAllowed  int64
	RetryDenied   int64 // hết budget ⇒ trả lỗi gốc
	LimiterKeys   int
	ConnsActive   int64
	DrainedIdle   int64 // connection rỗi bị đóng khi drain
	DrainForced   int64 // connection còn sống lúc hết hạn drain ⇒ Close cưỡng bức
	Misdirected   int64 // phase 8: 421 (Host không thuộc vhost của SNI / không vhost nào)
	HandshakeFail int64 // phase 8: handshake TLS hỏng hoặc quá HandshakeTimeout
}

type resilience struct {
	limiter *limit.KeyedLimiter
	budget  *limit.RetryBudget

	slots   chan struct{} // nil ⇒ không shed
	queued  atomic.Int64
	connSem chan struct{} // MaxConns (D3); nil ⇒ không trần

	rateLimited, shedFull, shedTimeout atomic.Int64
	connsActive                        atomic.Int64
	drainedIdle, drainForced           atomic.Int64
	misdirected, handshakeFail         atomic.Int64 // phase 8: 421, handshake hỏng
}

func (s *Server) initResilience() {
	r := &s.res
	if rl := s.cfg.RateLimit; rl.Rate > 0 {
		r.limiter = limit.NewKeyed(limit.KeyedConfig{Rate: rl.Rate, Burst: rl.Burst, MaxKeys: rl.MaxKeys})
	}
	rb := s.cfg.RetryBudget
	switch {
	case rb.Percent == 0:
		rb.Percent = 0.1
	case rb.Percent < 0:
		rb.Percent = 0 // chỉ còn sàn MinPerSec (test)
	}
	if rb.MinPerSec == 0 {
		rb.MinPerSec = 10
	}
	if rb.Window == 0 {
		rb.Window = 10 * time.Second
	}
	r.budget = &limit.RetryBudget{Percent: rb.Percent, MinPerSec: rb.MinPerSec, Window: rb.Window}
	if sh := s.cfg.Shed; sh.MaxInflight > 0 {
		r.slots = make(chan struct{}, sh.MaxInflight)
		if s.cfg.Shed.QueueTimeout == 0 {
			s.cfg.Shed.QueueTimeout = 50 * time.Millisecond
		}
	}
	if s.cfg.MaxConns > 0 {
		r.connSem = make(chan struct{}, s.cfg.MaxConns)
	}
}

// rateKey: khoá rate limit. Phòng tuyến (I6): clientIP đã qua ranh giới tin
// cậy. nodefense7: phần tử đầu của X-Forwarded-For client gửi — ai cũng tự
// chọn được ⇒ mỗi request một IP giả ⇒ không bao giờ hết token.
func rateKey(clientIP string, orig httpx.Header) string {
	if limitByTrustedIP {
		return clientIP
	}
	if v := orig.Get("X-Forwarded-For"); v != "" {
		first, _, _ := strings.Cut(v, ",")
		return strings.TrimSpace(first)
	}
	return clientIP
}

// admit: rate limit rồi shedding. ok=false ⇒ đã trả 429/503 (keep cho biết
// connection client còn dùng được). release phải gọi trên MỌI đường ra khi
// ok=true (I7) — caller defer.
func (s *Server) admit(c net.Conn, bw *bufio.Writer, req *httpx.Request, clientIP string) (ok, keep bool, release func()) {
	r := &s.res
	if r.limiter != nil && !r.limiter.Allow(rateKey(clientIP, req.Header), time.Now()) {
		r.rateLimited.Add(1)
		keep = s.drain(c, req) && !req.Close && !s.draining.Load()
		s.writeErrorH(c, bw, 429, "quá rate limit", keep, "Retry-After", "1")
		return false, keep, nil
	}
	if r.slots == nil {
		return true, false, func() {}
	}
	select {
	case r.slots <- struct{}{}:
		return true, false, func() { <-r.slots }
	default:
	}
	// Hết slot: xếp hàng nếu hàng còn chỗ. Hàng có trần là toàn bộ ý nghĩa của
	// shedding — hàng không trần chỉ dời quá tải từ upstream sang RAM proxy và
	// làm MỌI request chờ lâu hơn, kể cả cái đáng ra được phục vụ (G7).
	if r.queued.Add(1) > int64(s.cfg.Shed.MaxQueue) {
		r.queued.Add(-1)
		r.shedFull.Add(1)
		return false, s.shedReply(c, bw, req, "hàng đợi đầy"), nil
	}
	t := time.NewTimer(s.cfg.Shed.QueueTimeout)
	defer t.Stop()
	select {
	case r.slots <- struct{}{}:
		r.queued.Add(-1)
		return true, false, func() { <-r.slots }
	case <-t.C:
		r.queued.Add(-1)
		r.shedTimeout.Add(1)
		return false, s.shedReply(c, bw, req, "chờ slot quá QueueTimeout"), nil
	}
}

func (s *Server) shedReply(c net.Conn, bw *bufio.Writer, req *httpx.Request, why string) bool {
	keep := s.drain(c, req) && !req.Close && !s.draining.Load()
	s.writeErrorH(c, bw, 503, "quá tải, shed: "+why, keep, "Retry-After", "1")
	return keep
}

// allowRetry: D6 — mọi retry (D4 cùng backend, D9 đổi backend) đi qua đây.
func (s *Server) allowRetry() bool { return s.res.budget.TryWithdraw(time.Now()) }

// ResilienceStats: ảnh chụp bộ đếm phase 7.
func (s *Server) ResilienceStats() ResilienceStats {
	r := &s.res
	st := ResilienceStats{
		RateLimited: r.rateLimited.Load(), ShedQueueFull: r.shedFull.Load(), ShedTimeout: r.shedTimeout.Load(),
		Queued: r.queued.Load(), ConnsActive: r.connsActive.Load(),
		DrainedIdle: r.drainedIdle.Load(), DrainForced: r.drainForced.Load(),
		Misdirected: r.misdirected.Load(), HandshakeFail: r.handshakeFail.Load(),
	}
	if r.slots != nil {
		st.Inflight = int64(len(r.slots))
	}
	st.RetryAllowed, st.RetryDenied = r.budget.Stats()
	if r.limiter != nil {
		st.LimiterKeys = r.limiter.Len()
	}
	return st
}

// Listen (D9): ReusePort ⇒ SO_REUSEPORT để hai instance cùng port (drainlab).
func Listen(addr string, reusePort bool) (net.Listener, error) {
	lc := net.ListenConfig{}
	if reusePort {
		lc.Control = func(_, _ string, rc syscall.RawConn) error {
			var serr error
			if err := rc.Control(func(fd uintptr) {
				serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePort, 1)
			}); err != nil {
				return err
			}
			return serr
		}
	}
	return lc.Listen(context.Background(), "tcp", addr)
}

// Drain (D8): ngừng accept, đóng connection RỖI ngay, connection đang có
// request nhận response kèm `Connection: close` rồi đóng; chờ tối đa timeout
// rồi Close cưỡng bức. Trả số connection bị đóng cưỡng bức.
//
// Thứ tự với serveConn là kiểu Dekker trên hai atomic: Drain ghi closeIdle rồi
// đọc idle; serveConn ghi idle rồi đọc closeIdle. Một connection vừa rỗi đúng
// lúc Drain quét thì một trong hai bên chắc chắn thấy bên kia.
func (s *Server) Drain(timeout time.Duration) int {
	s.draining.Store(true)
	s.mu.Lock()
	if s.ln != nil {
		s.ln.Close()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	deadline := time.After(timeout)
	// D8′: cho connection rỗi một khoảng grace để gửi request kế — request đó
	// nhận Connection: close (exchange/writeErrorH thấy draining) rồi đóng.
	// Connection về rỗi SAU khi drain bắt đầu thì đã nhận response kèm close
	// ⇒ serveConn tự thoát (kiểm draining trước Peek).
	if g := s.cfg.DrainIdleGrace; g > 0 {
		select {
		case <-done:
		case <-time.After(min(g, timeout)):
		}
	}
	s.closeIdle.Store(true) // trước khi quét: cặp Dekker với serveConn (idle rồi closeIdle)
	s.mu.Lock()
	for c, st := range s.conns {
		if hc := st.h2.Load(); hc != nil {
			// Phase 10 (turn 3): h2 không có "rỗi" kiểu h1 — GOAWAY NO_ERROR,
			// stream đang chạy chạy tiếp, connection tự đóng khi stream cuối xong.
			hc.Shutdown()
			continue
		}
		if st.idle.Load() {
			s.res.drainedIdle.Add(1)
			c.SetReadDeadline(time.Now()) // đánh thức Peek ⇒ serveConn thoát, không trả byte nào
		}
	}
	s.mu.Unlock()
	forced := 0
	select {
	case <-done:
	case <-deadline:
		s.mu.Lock()
		forced = len(s.conns)
		s.mu.Unlock()
		s.res.drainForced.Add(int64(forced))
	}
	s.Close()
	return forced
}

// Draining: server đang drain (proxy tự gắn Connection: close).
func (s *Server) Draining() bool { return s.draining.Load() }
