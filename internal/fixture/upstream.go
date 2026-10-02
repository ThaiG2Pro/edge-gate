// Package fixture là backend giả cho phase 3+. ĐÂY là chỗ DUY NHẤT ngoài file
// _test.go được dùng net/http trong repo: fixture là UPSTREAM, không phải data
// path của proxy. cmd/edgegate và internal/proxy không import package này.
package fixture

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Handler trả bộ route dùng cho `make proxylab` và test của internal/proxy.
//
//	/hello          GET  → body nhỏ, net/http tự đặt Content-Length
//	/echo           POST → trả nguyên body, Content-Length tường minh
//	/chunked        GET  → 5 chunk cách nhau 20 ms, KHÔNG có CL ⇒ Transfer-Encoding: chunked
//	/ws             GET  → Upgrade: echo ⇒ 101 + echo tới EOF (P3-2 tunnel)
//	/eof            GET  → Hijack: response KHÔNG CL, KHÔNG TE, body tới EOF rồi đóng (RFC 9112 §6.3 bước 8)
//	/nobody         GET  → 204, không body
//	/headers        GET  → liệt kê header nhận được, mỗi dòng "Name: value" (kiểm hop-by-hop, XFF)
//	/large?n=N      GET  → N byte xác định (kiểm đúng byte)
//	/slow?ms=N      GET  → ngủ N ms rồi 200 (kiểm 504)
//	/status?code=N  GET  → status N, body "status N"
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "hello from upstream\n")
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.Write(b)
	})
	// /chunked?n=5&ms=20 — n chunk, ngủ ms giữa mỗi chunk. ms=0 dùng cho G4
	// (Nagle): chunk phải dồn sát nhau thì delayed-ACK 40 ms mới lộ.
	mux.HandleFunc("/chunked", func(w http.ResponseWriter, r *http.Request) {
		n, ms := 5, 20
		if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 {
			n = v
		}
		if v, err := strconv.Atoi(r.URL.Query().Get("ms")); err == nil && v >= 0 {
			ms = v
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fl, _ := w.(http.Flusher)
		for i := 0; i < n; i++ {
			fmt.Fprintf(w, "chunk %d\n", i)
			if fl != nil {
				fl.Flush()
			}
			if ms > 0 {
				time.Sleep(time.Duration(ms) * time.Millisecond)
			}
		}
	})
	mux.HandleFunc("/eof", func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", 500)
			return
		}
		c, _, err := hj.Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\nbody until eof\n")
	})
	// /ws (P3-2): Upgrade: echo ⇒ 101 rồi echo byte tới EOF (tunnel hai chiều
	// sau head — hình dạng của WebSocket mà không cần framing).
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "echo" {
			http.Error(w, "cần Upgrade: echo", 426)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", 500)
			return
		}
		c, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		io.Copy(c, rw.Reader)
	})
	mux.HandleFunc("/nobody", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/headers", func(w http.ResponseWriter, r *http.Request) {
		names := make([]string, 0, len(r.Header))
		for k := range r.Header {
			names = append(names, k)
		}
		sort.Strings(names)
		var sb strings.Builder
		fmt.Fprintf(&sb, "Host: %s\n", r.Host)
		for _, k := range names {
			for _, v := range r.Header[k] {
				fmt.Fprintf(&sb, "%s: %s\n", k, v)
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, sb.String())
	})
	mux.HandleFunc("/large", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n <= 0 {
			n = 1 << 20
		}
		w.Header().Set("Content-Length", strconv.Itoa(n))
		w.Write(Pattern(n))
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		time.Sleep(time.Duration(ms) * time.Millisecond)
		io.WriteString(w, "slow done\n")
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		code, _ := strconv.Atoi(r.URL.Query().Get("code"))
		if code < 100 || code > 599 {
			code = 200
		}
		w.WriteHeader(code)
		fmt.Fprintf(w, "status %d\n", code)
	})
	return mux
}

// Pattern sinh n byte xác định: byte i = 'a' + i%26. Hai đầu so được từng byte.
func Pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a' + byte(i%26)
	}
	return b
}

// ListenAndServe chạy Handler() trên addr trong goroutine, trả địa chỉ thật
// (addr có thể là ":0") và hàm dừng. Để `cmd/poollab` dựng upstream in-process
// mà KHÔNG import net/http — net/http chỉ sống trong package này.
func ListenAndServe(addr string) (string, func(), error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{Handler: Handler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	return ln.Addr().String(), func() { srv.Close() }, nil
}

// Sim bọc Handler() thành một backend "lệch" cho phase 6-7: ngủ Delay trước
// mỗi response và trả 503 NGAY (không ngủ) với xác suất ErrRate. Mọi núm đổi
// được lúc chạy (atomic) để lab biến một node nhanh thành chậm/hỏng giữa bài.
// Chỉ dùng qua ListenAndServeSim / NewSimServer — các lab không đụng net/http.
//
// Phase 7 (D11) thêm ba núm:
//   - SetConcurrency(n): tối đa n request xử lý cùng lúc, thừa thì XẾP HÀNG
//     trong backend — capacity hữu hạn cho shedlab (G7). 0 = không trần.
//   - SetHang(true): nhận request rồi không trả gì tới khi connection bị đóng
//     (proxy hết UpstreamHeaderTimeout) — "sống mà treo" cho chaoslab.
//   - SetDropReused(p): ở request thứ ≥ 2 trên CÙNG connection, với xác suất p
//     đóng connection trước khi ghi byte nào — upstream quá tải đóng keep-alive,
//     đúng ca proxy được retry (phase 5 D4) ⇒ đo khuếch đại retry (G6).
type Sim struct {
	Name        string
	delayNs     atomic.Int64
	errPermille atomic.Int64
	dropPermil  atomic.Int64
	hang        atomic.Bool
	sem         atomic.Pointer[chan struct{}]
	served      atomic.Int64
	errors      atomic.Int64
	dropped     atomic.Int64
	h           http.Handler
}

func NewSim(name string, delay time.Duration, errRate float64) *Sim {
	s := &Sim{Name: name, h: Handler()}
	s.SetDelay(delay)
	s.SetErrRate(errRate)
	return s
}

func (s *Sim) SetDelay(d time.Duration) { s.delayNs.Store(int64(d)) }
func (s *Sim) SetErrRate(r float64)     { s.errPermille.Store(int64(r * 1000)) }
func (s *Sim) SetDropReused(p float64)  { s.dropPermil.Store(int64(p * 1000)) }
func (s *Sim) SetHang(h bool)           { s.hang.Store(h) }
func (s *Sim) Delay() time.Duration     { return time.Duration(s.delayNs.Load()) }

// SetConcurrency: n ≤ 0 ⇒ bỏ trần. Request đang chờ trần cũ vẫn dùng trần cũ.
func (s *Sim) SetConcurrency(n int) {
	if n <= 0 {
		s.sem.Store(nil)
		return
	}
	c := make(chan struct{}, n)
	s.sem.Store(&c)
}

// Served, Errors, Dropped: đếm phía backend — đối chiếu với số phía proxy.
func (s *Sim) Served() int64  { return s.served.Load() }
func (s *Sim) Errors() int64  { return s.errors.Load() }
func (s *Sim) Dropped() int64 { return s.dropped.Load() }

type connSeqKey struct{}

func (s *Sim) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.served.Add(1)
	if p := s.dropPermil.Load(); p > 0 {
		if seq, _ := r.Context().Value(connSeqKey{}).(*atomic.Int64); seq != nil && seq.Add(1) >= 2 && rand.Int64N(1000) < p {
			if hj, ok := w.(http.Hijacker); ok {
				if c, _, err := hj.Hijack(); err == nil {
					s.dropped.Add(1)
					c.Close() // 0 byte response
					return
				}
			}
		}
	}
	if s.hang.Load() {
		<-r.Context().Done() // proxy đóng connection ⇒ context huỷ ⇒ không rò goroutine
		return
	}
	if sp := s.sem.Load(); sp != nil {
		select {
		case *sp <- struct{}{}:
			defer func() { <-*sp }()
		case <-r.Context().Done():
			return
		}
	}
	if p := s.errPermille.Load(); p > 0 && rand.Int64N(1000) < p {
		s.errors.Add(1)
		// Lỗi NHANH: đây là điểm mù của least-conn (inflight thấp ≠ khoẻ).
		w.Header().Set("X-Sim", s.Name)
		http.Error(w, "sim: 503 giả lập", http.StatusServiceUnavailable)
		return
	}
	if d := s.Delay(); d > 0 {
		time.Sleep(d)
	}
	w.Header().Set("X-Sim", s.Name)
	s.h.ServeHTTP(w, r)
}

func (s *Sim) server() *http.Server {
	return &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second,
		ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
			return context.WithValue(ctx, connSeqKey{}, new(atomic.Int64))
		}}
}

// ListenAndServeSim: như ListenAndServe nhưng phục vụ sim.
func ListenAndServeSim(addr string, sim *Sim) (string, func(), error) {
	ss, err := NewSimServer(addr, sim)
	if err != nil {
		return "", nil, err
	}
	return ss.Addr, ss.Kill, nil
}

// SimServer: một Sim gắn một địa chỉ cố định, Kill/Revive được (D11) — đóng
// listener + mọi connection, rồi mở lại ĐÚNG port đó (chaoslab, drainlab).
type SimServer struct {
	Sim  *Sim
	Addr string
	mu   sync.Mutex
	srv  *http.Server
}

func NewSimServer(addr string, sim *Sim) (*SimServer, error) {
	ss := &SimServer{Sim: sim}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	ss.Addr = ln.Addr().String()
	ss.srv = sim.server()
	go ss.srv.Serve(ln)
	return ss, nil
}

// Kill: đóng listener và mọi connection đang mở. Gọi hai lần vô hại.
func (ss *SimServer) Kill() {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.srv != nil {
		ss.srv.Close()
		ss.srv = nil
	}
}

// Revive: mở lại cùng port. Đang sống ⇒ không làm gì.
func (ss *SimServer) Revive() error {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.srv != nil {
		return nil
	}
	ln, err := net.Listen("tcp", ss.Addr)
	if err != nil {
		return err
	}
	ss.srv = ss.Sim.server()
	go ss.srv.Serve(ln)
	return nil
}

// Alive: đang lắng nghe không.
func (ss *SimServer) Alive() bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.srv != nil
}

// ListenAndServeTLS (phase 8 G8): upstream nói TLS bằng cert cho sẵn — để đo
// pool tới upstream TLS. idle > 0 ⇒ http.Server.IdleTimeout (upstream đóng
// connection rỗi bằng close_notify + FIN).
func ListenAndServeTLS(addr string, cert tls.Certificate, idle time.Duration) (string, func(), error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{Handler: Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: idle,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}
	go srv.ServeTLS(ln, "", "")
	return ln.Addr().String(), func() { srv.Close() }, nil
}
