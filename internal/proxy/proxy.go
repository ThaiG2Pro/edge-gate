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
	"crypto/tls"
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

	"github.com/ThaiG2Pro/edge-gate/internal/h2"
	"github.com/ThaiG2Pro/edge-gate/internal/httpx"
	"github.com/ThaiG2Pro/edge-gate/internal/lb"
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

	// Phase 7. RateLimit (D4): token bucket per-IP sau ranh giới tin cậy;
	// Rate 0 = tắt. Shed (D7): trần request đang chạy + hàng đợi có trần;
	// MaxInflight 0 = tắt. RetryBudget (D6): luôn bật, zero value = 10 %.
	RateLimit   RateLimitConfig
	Shed        ShedConfig
	RetryBudget RetryBudgetConfig
	// MaxConns (D3): trần connection client toàn cục — Accept chờ khi đầy.
	// 0 = không trần (mặc định). CHỈ để chứng minh G3: trần này chính là thứ
	// Slowloris cần; shed theo request (Shed) mới là cách đúng.
	MaxConns int
	// MaxConnsPerIP (P7-2, trả 2026-10-02): trần connection đồng thời mỗi IP
	// PEER (không XFF — lúc Accept chưa có byte nào). Vượt ⇒ đóng ngay sau Accept.
	// HeaderTimeout giới hạn THỜI GIAN một connection Slowloris sống, không giới
	// hạn SỐ connection một IP mở lại liên tục (phase 7: 500 conn × 20.7 KiB).
	// 0 = không trần. Đếm MỌI connection (kể cả keep-alive rỗi, kiểu HAProxy
	// src_conn_cur) ⇒ ngưỡng phải > độ đồng thời hợp lệ của MỘT IP (NAT!):
	// slowlab với 32 chặn luôn probe 64 worker (51.6 % 200 ở baseline). Đóng ngay
	// ⇒ attacker nối lại liên tục (828 913 lần / 30 s) ⇒ đổi RAM lấy CPU: p99
	// probe 4.7 → 89 ms (P7-2b).
	MaxConnsPerIP int
	// PerIPTarpit (P7-2b): khi IP vượt MaxConnsPerIP, giữ connection trong
	// khoảng thời gian này trước khi đóng (tarpit) để giảm bão reconnect của attacker.
	// 0 = đóng ngay (mặc định).
	PerIPTarpit time.Duration
	// ReusePort (D9): SO_REUSEPORT ở ListenAndServe — hai instance cùng port.
	ReusePort bool
	// DrainIdleGrace (D8′, phase 7 turn 2): lúc Drain, connection RỖI được chờ
	// tối đa ngần này để gửi request kế (response kèm Connection: close) thay vì
	// bị đóng ngay. 0 = đóng ngay (D8). Đóng ngay làm mất request client vừa ghi
	// lên connection rỗi — client không thấy FIN khi không đọc (G8 b).
	DrainIdleGrace time.Duration

	// Phase 8. TLS: listener nói TLS, cert theo SNI (nil = plaintext).
	// VHosts: server_name → nhóm upstream + LB riêng, chỉ số trùng TLS.Store
	// (D2). Upstreams/LB ở trên = vhost mặc định (không bắt buộc khi có VHosts).
	// HandshakeTimeout (D6): mặc định 10 s. UpstreamTLS (D8): nói TLS tới upstream.
	TLS              *TLSConfig
	VHosts           []VHost
	HandshakeTimeout time.Duration
	UpstreamTLS      *UpstreamTLSConfig

	// NoSplice (phase 9 D5, P9-7 lật mặc định 2026-10-02: BẬT trừ khi tắt
	// tường minh — chaoslab 60 s body 256 KiB xanh, P9-1 đã trả).
	// Splice: body response CL ≥ 64 KiB đi bằng splice(2)
	// khi cả hai phía là TCP trần (không TLS). Mặc định tắt.
	NoSplice bool

	// Phase 10 D7: H2C nhận HTTP/2 cleartext prior knowledge trên listener
	// plaintext (cùng port với h1; phân biệt bằng preface). H2: SETTINGS/trần;
	// timeout zero ⇒ lấy từ Limits.
	H2C bool
	H2  h2.Config
	// H2ALPN (P10-5): listener TLS công bố ALPN ["h2","http/1.1"]; đàm phán
	// được "h2" ⇒ serveH2 ngay sau handshake. Kéo theo cipher TLS 1.2 bị
	// giới hạn còn ECDHE+AEAD (RFC 9113 §9.2.2) cho mọi client của listener đó.
	H2ALPN bool
	// H2COnly (P10-6): listener plaintext chỉ nhận h2c — byte đầu không phải
	// preface ⇒ h2.Conn từ chối và đóng, không rơi về h1 400 (D7 chung port).
	// Cần H2C. Dùng khi listener dành riêng cho h2 (h2spec 145/145).
	H2COnly bool

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
	if c.HandshakeTimeout == 0 {
		c.HandshakeTimeout = 10 * time.Second
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
	conns map[net.Conn]*connState
	wg    sync.WaitGroup

	// Phase 7: rate limit / shed / retry budget / trần connection (resilience.go).
	res      resilience
	draining atomic.Bool
	// closeIdle: Drain đã quét connection rỗi (ngay, hoặc sau DrainIdleGrace).
	// Từ lúc này connection về rỗi tự đóng. Tách khỏi draining: một response
	// ghi TRƯỚC khi draining bật không mang Connection: close — đóng connection
	// đó lúc nó về rỗi trong grace là làm mất request kế của client (D8′).
	closeIdle atomic.Bool

	// Phase 5-6: một pool cho mỗi backend (P5-3), tạo lười ở poolFor.
	pmu   sync.Mutex
	pools map[string]*pool

	lb *lb.Balancer // phase 6: chọn backend; Pick trước get, Done sau exchange
	// Phase 8: vhost (D2), cấu hình TLS listener, cache session phía upstream.
	vhosts     []*vhostRT
	tlsCfg     *tls.Config
	upTLSCache tls.ClientSessionCache
	// Phase 9 D5: body response đi bằng splice — số response và số byte.
	spliced, splicedBytes atomic.Int64
	// P9-2: body request (upload) đi bằng splice — số request và số byte.
	splicedUp, splicedUpBytes atomic.Int64
	// Phase 10: connection h2 đã nhận, bộ đếm stream cộng dồn.
	h2conns atomic.Int64
	h2stats h2.Stats
	// P7-2: số connection đang mở theo IP peer (dưới mu).
	perIP map[string]int
}

// SpliceStats (phase 9 D5): số response có body đi bằng splice, và số byte.
func (s *Server) SpliceStats() (responses, bytes int64) {
	return s.spliced.Load(), s.splicedBytes.Load()
}

// SpliceUploadStats (P9-2): số request có body (upload) đi bằng splice, và số byte.
func (s *Server) SpliceUploadStats() (requests, bytes int64) {
	return s.splicedUp.Load(), s.splicedUpBytes.Load()
}

// New panic khi cấu hình LB sai (algo lạ, không upstream) — lỗi khởi động,
// cùng lý do với trusted_proxies.
func New(cfg Config) *Server {
	cfg.withDefaults()
	s := &Server{cfg: cfg, conns: map[net.Conn]*connState{}, pools: map[string]*pool{}}
	s.initResilience()
	if u := cfg.UpstreamTLS; u != nil {
		// Trước lb.New: health loop (Start) gọi healthDial đọc upTLSCache ngay.
		s.upTLSCache = tls.NewLRUClientSessionCache(256)
		if cfg.LB.Health.Dial == nil {
			cfg.LB.Health.Dial = s.healthDial // P8-3: probe active bắt tay TLS như data path
		}
	}
	if len(cfg.Upstreams) > 0 || len(cfg.VHosts) == 0 {
		bl, err := lb.New(cfg.Upstreams, cfg.LB)
		if err != nil {
			panic("proxy: " + err.Error())
		}
		s.lb = bl
		// Active health (D6) chạy từ New, không từ Serve: Serve thường được gọi
		// trong goroutine riêng nên mốc "goroutine nền" của test/ops phải tính
		// sẵn checker. Close dừng nó.
		s.lb.Start()
	}
	s.initVHosts()
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
	p := newPool(s.cfg.Pool, func() (net.Conn, net.Conn, error) { return s.dialUpstream(addr) })
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
func (s *Server) LBStats() lb.Stats {
	if s.lb == nil {
		return lb.Stats{}
	}
	return s.lb.Stats()
}

// VHostStats (phase 8): balancer của vhost thứ i.
func (s *Server) VHostStats(i int) lb.Stats { return s.vhosts[i].lb.Stats() }

func (s *Server) ListenAndServe() error {
	ln, err := Listen(s.cfg.Listen, s.cfg.ReusePort)
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
		// D3: trần connection — giành chỗ TRƯỚC Accept. Đầy ⇒ không Accept ⇒
		// connection mới nằm trong backlog kernel (client tưởng đã nối) rồi
		// tràn backlog. Đây là hành vi worker-pool mà Slowloris cần (G3).
		if s.res.connSem != nil {
			s.res.connSem <- struct{}{}
		}
		c, err := ln.Accept()
		if err != nil {
			if s.res.connSem != nil {
				<-s.res.connSem
			}
			if s.closed.Load() || s.draining.Load() {
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
		st, ok := s.track(c)
		if !ok {
			// P7-2: IP này đã đủ MaxConnsPerIP — đóng trước khi có goroutine/bufio.
			// P7-2b: nếu bật PerIPTarpit, giữ connection trong thời gian tarpit để dập bão reconnect.
			s.res.perIPRejected.Add(1)
			if s.res.connSem != nil {
				<-s.res.connSem
			}
			if s.cfg.PerIPTarpit > 0 {
				s.wg.Add(1)
				go func(conn net.Conn, d time.Duration) {
					defer s.wg.Done()
					time.Sleep(d)
					conn.Close()
				}(c, s.cfg.PerIPTarpit)
			} else {
				c.Close()
			}
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.untrack(c)
			defer c.Close()
			s.serveConn(c, st)
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
	for c, st := range s.conns {
		c.Close()
		if up := st.up.Load(); up != nil {
			(*up).Close()
		}
	}
	// Serve chặn ở "giành chỗ trước Accept" khi MaxConns đầy sẽ được nhả khi
	// các handler vừa bị đóng ở trên chạy untrack — rồi Accept thấy listener đóng.
	s.mu.Unlock()
	s.wg.Wait()
	if s.lb != nil {
		s.lb.Close()
	}
	for _, v := range s.vhosts {
		v.lb.Close()
	}
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

// connState: trạng thái một connection client mà Drain cần đọc (D8).
type connState struct {
	idle atomic.Bool // đang chờ byte đầu của request kế (vòng keep-alive)
	tls  *tls.Conn   // phase 8: connection TLS (nil = plaintext) — vhost theo SNI
	// up: connection upstream request hiện tại đang cầm (nil khi không có).
	// Close đóng nó cùng connection client — không thì handler đang chờ
	// upstream treo giữ Close (và Drain) tới UpstreamHeaderTimeout/BodyTimeout.
	up atomic.Pointer[net.Conn]
	// pre: byte đầu đọc lúc rỗi khi không cầm bufio (phase 9 D2).
	pre prefixReader
	ip  string // P7-2: khoá perIP ("" = không đếm)
	// h2: connection đã sang h2c (phase 10) — Drain gửi GOAWAY qua đây.
	h2 atomic.Pointer[h2.Conn]
}

func (s *Server) track(c net.Conn) (*connState, bool) {
	st := &connState{}
	s.mu.Lock()
	if lim := s.cfg.MaxConnsPerIP; lim > 0 {
		ip := c.RemoteAddr().String()
		if h, _, err := net.SplitHostPort(ip); err == nil {
			ip = h
		}
		if s.perIP == nil {
			s.perIP = map[string]int{}
		}
		if s.perIP[ip] >= lim {
			s.mu.Unlock()
			return nil, false
		}
		s.perIP[ip]++
		st.ip = ip
	}
	s.conns[c] = st
	s.mu.Unlock()
	s.res.connsActive.Add(1)
	return st, true
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	if st := s.conns[c]; st != nil && st.ip != "" {
		if s.perIP[st.ip]--; s.perIP[st.ip] <= 0 {
			delete(s.perIP, st.ip) // map không phình theo số IP từng thấy
		}
	}
	delete(s.conns, c)
	s.mu.Unlock()
	s.res.connsActive.Add(-1)
	if s.res.connSem != nil {
		<-s.res.connSem
	}
}

// serveConn: vòng đời một connection client. Một *bufio.Reader cho cả đời
// connection — byte của request N+1 có thể đã nằm trong buffer khi đang xử lý
// request N, nên KHÔNG BAO GIỜ đọc trực tiếp từ c (bẫy #3).
func (s *Server) serveConn(c net.Conn, st *connState) {
	s.setNoDelay(c)
	if s.tlsCfg != nil {
		tc, ok := s.handshake(c, st) // D6: chỗ đọc socket thứ 8 của I3
		if !ok {
			return
		}
		c = tc
		// P10-5: ALPN đã quyết giao thức — không sniff preface (h2.Conn vẫn
		// kiểm preface là frame đầu, RFC 9113 §3.4).
		if tc.ConnectionState().NegotiatedProtocol == "h2" {
			br := getReader(c)
			defer putReader(br)
			s.serveH2(c, st, br)
			return
		}
	}
	lim := s.cfg.Limits
	// D2: bufio chỉ cầm khi có request. Rỗi ⇒ trả pool, chờ byte đầu bằng Read
	// 1 byte vào st.pre. nodefense9: cầm suốt đời connection (Peek).
	var br *bufio.Reader
	var bw *bufio.Writer
	defer func() {
		if br != nil {
			putReader(br)
			putWriter(bw)
		}
	}()
	st.pre.r = c
	first := true
	for {
		// Rỗi: chờ byte đầu của request kế tiếp trong IdleTimeout. Hết hạn hay
		// client đóng (io.EOF) đều là kết thúc bình thường, không trả gì.
		c.SetReadDeadline(time.Now().Add(lim.IdleTimeout))
		// D8: đánh dấu rỗi RỒI mới đọc draining (cặp Dekker với Drain).
		st.idle.Store(true)
		pipelined := br != nil && br.Buffered() > 0
		if s.closeIdle.Load() && !pipelined {
			return
		}
		switch {
		case pipelined:
			// Byte request kế đã nằm trong br (pipelining): KHÔNG trả br — trả
			// là mất byte (I1). Không chờ gì.
		case releaseIdleBufio:
			if br != nil {
				putReader(br)
				putWriter(bw) // đã Flush ở cuối response
				br, bw = nil, nil
			}
			if n, _ := c.Read(st.pre.first[:]); n == 0 {
				st.idle.Store(false)
				return
			}
			st.pre.has = true
			br, bw = getReader(&st.pre), getWriter(c)
		default:
			if br == nil {
				br, bw = getReader(c), getWriter(c)
			}
			if _, err := br.Peek(1); err != nil {
				st.idle.Store(false)
				return
			}
		}
		st.idle.Store(false)
		// Phase 10 D7: chỉ byte ĐẦU TIÊN của connection quyết h2c (prior
		// knowledge) — không đổi giao thức giữa chừng.
		if first && s.cfg.H2C && s.tlsCfg == nil {
			c.SetReadDeadline(time.Now().Add(lim.HeaderTimeout))
			if s.cfg.H2COnly || isH2Preface(br) {
				s.serveH2(c, st, br)
				return
			}
		}
		first = false
		// Có byte đầu: đồng hồ Slowloris bắt đầu. Toàn bộ head phải xong trong
		// HeaderTimeout tính từ ĐÂY, không phải từ mỗi byte.
		if headerTimeoutOn {
			c.SetReadDeadline(time.Now().Add(lim.HeaderTimeout))
		} else {
			c.SetReadDeadline(time.Time{}) // nodefense7: Slowloris sống mãi (G2 b)
		}
		req, err := httpx.ReadRequest(br, lim)
		if err != nil {
			s.replyReadError(c, bw, err)
			return
		}
		// Đang drain thì response vừa rồi đã mang Connection: close ⇒ roundTrip
		// trả false. true lúc draining = response ghi trước khi draining bật ⇒
		// client chưa được báo ⇒ quay lại vòng rỗi (closeIdle quyết).
		if !s.roundTrip(c, st, br, bw, req) {
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
