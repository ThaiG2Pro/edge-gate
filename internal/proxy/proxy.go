// Package proxy là vertical slice của phase 3: accept → ReadRequest → Dial
// upstream → forward → ReadResponse → serialize về client. Chỉ dùng net;
// không net/http. Một backend, không pool (D1), không LB, không TLS.
//
// Vai trò của package này so với httpx: httpx nhận *bufio.Reader và không
// biết gì về deadline. Ở đây cầm net.Conn, nên I3 (mọi connection có
// deadline) là việc của package này — trước MỖI lần đọc head/body ở CẢ HAI
// connection.
package proxy

import (
	"bufio"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thaivro/edgegate/internal/httpx"
)

// Config của một Server. Zero value được điền mặc định bằng withDefaults.
type Config struct {
	Listen   string // ":8080"
	Upstream string // "127.0.0.1:8081" — MỘT backend hardcode (phase 3)

	DialTimeout           time.Duration // Dial sang upstream
	UpstreamHeaderTimeout time.Duration // từ khi gửi xong request tới khi đọc xong head response ⇒ 504
	UpstreamBodyTimeout   time.Duration // toàn bộ body response

	Limits httpx.Limits // trần parser + HeaderTimeout/BodyTimeout/IdleTimeout phía client

	// NoDelay: SetNoDelay(true) trên mọi socket. Mặc định true. Chỉ đặt false
	// để đo G4 (Nagle + delayed ACK = +40 ms hằng số, phase 0 G3).
	NoDelay *bool

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
}

func New(cfg Config) *Server {
	cfg.withDefaults()
	return &Server{cfg: cfg, conns: map[net.Conn]struct{}{}}
}

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

func (s *Server) setNoDelay(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok && *s.cfg.NoDelay {
		tc.SetNoDelay(true)
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
