package epollsrv

import (
	"net"
	"sync"
	"sync/atomic"
)

// NetpollerServer: cùng giao thức, cách viết Go thông thường — một goroutine
// mỗi connection, buffer đọc 4 KiB của riêng nó, Read chặn (netpoller của
// runtime là epoll bên dưới — runtime/netpoll_epoll.go).
type NetpollerServer struct {
	ln       net.Listener
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	Requests atomic.Int64
	Conns    atomic.Int64
}

func ServeNetpoller(addr string, resp []byte) (*NetpollerServer, error) {
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		return nil, err
	}
	s := &NetpollerServer{ln: ln, conns: map[net.Conn]struct{}{}}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns[c] = struct{}{}
			s.mu.Unlock()
			s.Conns.Add(1)
			s.wg.Add(1)
			go s.serve(c, resp)
		}
	}()
	return s, nil
}

func (s *NetpollerServer) Addr() string { return s.ln.Addr().String() }

func (s *NetpollerServer) Close() {
	s.ln.Close()
	s.mu.Lock()
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *NetpollerServer) serve(c net.Conn, resp []byte) {
	defer func() {
		c.Close()
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		s.Conns.Add(-1)
		s.wg.Done()
	}()
	buf := make([]byte, 4096)
	have := 0 // byte head dở ở đầu buf
	for {
		n, err := c.Read(buf[have:])
		if n == 0 && err != nil {
			return
		}
		have += n
		heads, rest := scan(buf[:have])
		for i := 0; i < heads; i++ {
			s.Requests.Add(1)
			if _, err := c.Write(resp); err != nil {
				return
			}
		}
		have = copy(buf, buf[rest:have])
		if have == len(buf) {
			return // head > 4 KiB: đóng (bản tối giản)
		}
	}
}
