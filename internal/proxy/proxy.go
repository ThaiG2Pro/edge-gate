// Package proxy là vertical slice của phase 3: accept → ReadRequest → lấy
// connection upstream (pool, phase 5) → forward → ReadResponse → serialize về
// client. Chỉ dùng net; không net/http. Một backend, không LB, không TLS.
//
// Vai trò của package này so với httpx: httpx nhận *bufio.Reader và không
// biết gì về deadline. Ở đây cầm net.Conn, nên I3 (mọi connection có
// deadline) là việc của package này — trước MỖI lần đọc head/body ở CẢ HAI
// connection.
package proxy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thaivro/edgegate/internal/httpx"
	"github.com/thaivro/edgegate/internal/lb"
)

// Config của một Server. Zero value được điền mặc định bằng withDefaults.
type Config struct {
	Listen   string // ":8080"
	Upstream string // "127.0.0.1:8081" — một backend (phase 3-5); phase 6: lối tắt cho Upstreams = [Upstream]
	// Upstreams (phase 6): danh sách backend. Rỗng ⇒ [Upstream].
	Upstreams []string
	// LB (phase 6): thuật toán chọn backend + health. Zero value = rr, health
	// và outlier bật với mặc định của lb.Config.
	LB lb.Config

	DialTimeout           time.Duration // Dial sang upstream
	UpstreamHeaderTimeout time.Duration // từ khi gửi xong request tới khi đọc xong head response ⇒ 504
	UpstreamBodyTimeout   time.Duration // toàn bộ body response

	Limits httpx.Limits // trần parser + HeaderTimeout/BodyTimeout/IdleTimeout phía client

	// NoDelay: SetNoDelay(true) trên mọi socket. Mặc định true. Chỉ đặt false
	// để đo G4 (Nagle + delayed ACK = +40 ms hằng số, phase 0 G3).
	NoDelay *bool

	// TrustedProxies (phase 4 D9): CIDR của các peer được tin về header
	// forwarding. Peer trong list ⇒ X-Forwarded-For của nó được GIỮ và append
	// IP peer. Peer ngoài list ⇒ XFF/Forwarded của nó bị bỏ, XFF := peer,
	// X-Real-IP := peer. Rỗng = không tin ai (mặc định an toàn).
	TrustedProxies []string
	trusted        []*net.IPNet

	// Pool (phase 5): connection pool tới upstream — phase 6 là MỘT pool cho
	// MỖI backend (D10), MaxIdle là per host. Zero value = bật, MaxIdle 64,
	// MaxIdleTime 30 s, probe bật. Pool.Disabled = hành vi phase 3-4.
	Pool PoolConfig

	Logf func(format string, args ...any)
}

func (c *Config) withDefaults() {
	if c.DialTimeout == 0 {
		c.DialTimeout = 2 * time.Second
	}
	if c.UpstreamHeaderTimeout == 0 {
		c.UpstreamHeaderTimeout = 5 * time.Second
	}
	if c.UpstreamBodyTimeout == 0 {
		c.UpstreamBodyTimeout = 30 * time.Second
	}
	if c.Limits == (httpx.Limits{}) {
		c.Limits = httpx.DefaultLimits()
	}
	if c.NoDelay == nil {
		t := true
		c.NoDelay = &t
	}
	if c.Logf == nil {
		c.Logf = log.Printf
	}
	c.Pool.withDefaults()
	if len(c.Upstreams) == 0 && c.Upstream != "" {
		c.Upstreams = []string{c.Upstream}
	}
	if c.Upstream == "" && len(c.Upstreams) > 0 {
		c.Upstream = c.Upstreams[0]
	}
	if c.LB.Logf == nil {
		c.LB.Logf = c.Logf
	}
	c.trusted = c.trusted[:0]
	for _, cidr := range c.TrustedProxies {
		if !strings.Contains(cidr, "/") {
			if strings.Contains(cidr, ":") {
				cidr += "/128"
			} else {
				cidr += "/32"
			}
		}
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			// Cấu hình tin cậy sai là lỗi khởi động, không phải thứ để "bỏ qua
			// rồi chạy tiếp": bỏ qua lặng lẽ = tin ít hơn người vận hành nghĩ,
			// hoặc tệ hơn, họ tưởng đã tin mà thật ra không.
			panic(fmt.Sprintf("proxy: trusted_proxies %q: %v", cidr, err))
		}
		c.trusted = append(c.trusted, n)
	}
}

// isTrusted: peer có nằm trong TrustedProxies không.
func (c *Config) isTrusted(ip net.IP) bool {
	for _, n := range c.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Server: goroutine-per-connection. Đóng được sạch: Close() dừng Accept,
// đóng mọi connection đang mở và chờ handler thoát (G6: không leak goroutine).
type Server struct {
	cfg    Config
	ln     net.Listener
	closed atomic.Bool

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	wg    sync.WaitGroup

	// Phase 5-6: một pool cho mỗi backend (P5-3), tạo lười ở poolFor.
	pmu   sync.Mutex
	pools map[string]*pool

	lb *lb.Balancer // phase 6: chọn backend; Pick trước get, Done sau exchange
}

// New panic khi cấu hình LB sai (algo lạ, không upstream) — lỗi khởi động,
// cùng lý do với trusted_proxies.
func New(cfg Config) *Server {
	cfg.withDefaults()
	s := &Server{cfg: cfg, conns: map[net.Conn]struct{}{}, pools: map[string]*pool{}}
	bl, err := lb.New(cfg.Upstreams, cfg.LB)
	if err != nil {
		panic("proxy: " + err.Error())
	}
	s.lb = bl
	// Active health (D6) chạy từ New, không từ Serve: Serve thường được gọi
	// trong goroutine riêng nên mốc "goroutine nền" của test/ops phải tính
	// sẵn checker. Close dừng nó.
	s.lb.Start()
	return s
}

// poolFor trả pool của backend addr, tạo nếu chưa có. Sau Close mọi pool đã
// closed; pool tạo mới sau đó cũng đóng ngay khi put (closed=true).
func (s *Server) poolFor(addr string) *pool {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	if p, ok := s.pools[addr]; ok {
		return p
	}
	p := newPool(s.cfg.Pool, func() (net.Conn, error) {
		c, err := net.DialTimeout("tcp", addr, s.cfg.DialTimeout)
		if err == nil {
			s.setNoDelay(c)
		}
		return c, err
	})
	if s.closed.Load() {
		p.closed = true
	}
	s.pools[addr] = p
	return p
}

// PoolStats: TỔNG bộ đếm của mọi pool (D6). `cmd/poollab` và test dùng để
// chứng minh pool có chạy, không chỉ có flag. Từng backend: PoolStatsFor.
func (s *Server) PoolStats() PoolStats {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	var t PoolStats
	for _, p := range s.pools {
		st := p.stats()
		t.Dials += st.Dials
		t.Reuses += st.Reuses
		t.Puts += st.Puts
		t.Retries += st.Retries
		t.DropDirty += st.DropDirty
		t.DropFull += st.DropFull
		t.DropExpired += st.DropExpired
		t.DeadOnProbe += st.DeadOnProbe
		t.Idle += st.Idle
	}
	return t
}

// PoolStatsFor: bộ đếm pool của một backend (chưa có pool ⇒ zero).
func (s *Server) PoolStatsFor(addr string) PoolStats {
	s.pmu.Lock()
	p, ok := s.pools[addr]
	s.pmu.Unlock()
	if !ok {
		return PoolStats{}
	}
	return p.stats()
}

// LBStats: ảnh chụp balancer (phase 6): picks/inflight/EWMA/health mỗi backend.
func (s *Server) LBStats() lb.Stats { return s.lb.Stats() }

func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Serve chạy accept loop trên ln cho tới khi Close hoặc lỗi không tạm thời.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	for {
		c, err := ln.Accept()
		if err != nil {
			if s.closed.Load() {
				return nil
			}
			// EMFILE/ENFILE (hết fd) là lỗi tạm thời: đừng chết, lùi một nhịp.
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			if errors.Is(err, os.ErrDeadlineExceeded) || isTemporary(err) {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			return err
		}
		s.track(c)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.untrack(c)
			defer c.Close()
			s.serveConn(c)
		}()
	}
}

// Addr trả địa chỉ đang lắng nghe (test dùng với Listen ":0").
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Close dừng accept, đóng mọi connection và chờ handler thoát.
func (s *Server) Close() error {
	s.closed.Store(true)
	s.mu.Lock()
	var err error
	if s.ln != nil {
		err = s.ln.Close()
	}
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.lb.Close()
	// Handler đã thoát hết ⇒ không ai đang cầm connection upstream; đóng idle.
	// put sau thời điểm này (nếu có) cũng đóng vì pool.closed (D8).
	s.pmu.Lock()
	pools := make([]*pool, 0, len(s.pools))
	for _, p := range s.pools {
		pools = append(pools, p)
	}
	s.pmu.Unlock()
	for _, p := range pools {
		p.closeAll()
	}
	return err
}

func (s *Server) track(c net.Conn) {
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

// serveConn: vòng đời một connection client. Một *bufio.Reader cho cả đời
// connection — byte của request N+1 có thể đã nằm trong buffer khi đang xử lý
// request N, nên KHÔNG BAO GIỜ đọc trực tiếp từ c (bẫy #3).
func (s *Server) serveConn(c net.Conn) {
	s.setNoDelay(c)
	lim := s.cfg.Limits
	br := bufio.NewReaderSize(c, 8<<10)
	bw := bufio.NewWriterSize(c, 8<<10)
	for {
		// Rỗi: chờ byte đầu của request kế tiếp trong IdleTimeout. Hết hạn hay
		// client đóng (io.EOF) đều là kết thúc bình thường, không trả gì.
		c.SetReadDeadline(time.Now().Add(lim.IdleTimeout))
		if _, err := br.Peek(1); err != nil {
			return
		}
		// Có byte đầu: đồng hồ Slowloris bắt đầu. Toàn bộ head phải xong trong
		// HeaderTimeout tính từ ĐÂY, không phải từ mỗi byte.
		c.SetReadDeadline(time.Now().Add(lim.HeaderTimeout))
		req, err := httpx.ReadRequest(br, lim)
		if err != nil {
			s.replyReadError(c, bw, err)
			return
		}
		if !s.roundTrip(c, br, bw, req) {
			return
		}
	}
}

// replyReadError: lỗi khi đọc head request. Lỗi giao thức ⇒ status của nó;
// quá HeaderTimeout ⇒ 408; đứt giữa head (I/O) ⇒ không có ai để trả lời.
// Luôn đóng connection: parser đã lệch, không tin được byte tiếp theo.
func (s *Server) replyReadError(c net.Conn, bw *bufio.Writer, err error) {
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return
	case isTimeout(err):
		s.writeError(c, bw, 408, "quá HeaderTimeout khi đọc head", false)
	default:
		if pe, ok := httpx.IsProtoError(err); ok {
			s.writeError(c, bw, pe.Status, pe.Reason, false)
			return
		}
		s.cfg.Logf("proxy: đọc request %s: %v", c.RemoteAddr(), err)
	}
}

// setNoDelay đặt TCP_NODELAY = cfg.NoDelay một cách TƯỚNG MINH cả hai chiều.
// Bản đầu chỉ gọi SetNoDelay(true) khi cfg.NoDelay=true và "bỏ qua" khi false —
// vô nghĩa, vì Go đã setNoDelay(fd, true) trong newTCPConn (net/tcpsock.go):
// muốn Nagle bật để đo G4 thì phải gọi SetNoDelay(false). Lộ ở turn 2 phase 3
// khi -nodelay=false không đổi một micro giây nào.
func (s *Server) setNoDelay(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetNoDelay(*s.cfg.NoDelay)
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

func isTemporary(err error) bool {
	var te interface{ Temporary() bool }
	return errors.As(err, &te) && te.Temporary()
}
