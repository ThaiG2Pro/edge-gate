//go:build linux

package epollsrv

import (
	"errors"
	"fmt"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
)

const soReusePort = 0xf // syscall không export SO_REUSEPORT (như proxy/reuseport_linux.go)

// conn: trạng thái MỘT connection trong loop. in = phần head dở (nil khi không
// dở — trường hợp thường của connection rỗi), out = byte response chưa ghi
// được (EAGAIN). Không goroutine, không buffer đọc riêng.
type conn struct {
	in, out []byte
}

type loop struct {
	lfd, ep int
	conns   map[int32]*conn
	buf     []byte // buffer đọc DÙNG CHUNG cả loop
	resp    []byte
	srv     *EpollServer
	nconn   atomic.Int64 // len(conns) đọc được từ goroutine khác
}

// EpollServer: n loop, mỗi loop một socket nghe SO_REUSEPORT cùng cổng (kernel
// chia connection), một epoll, một luồng OS (LockOSThread).
type EpollServer struct {
	addr   string
	loops  []*loop
	closed atomic.Bool
	wg     sync.WaitGroup
	// Requests: tổng số response đã xếp ghi (test, lab).
	Requests atomic.Int64
	Conns    atomic.Int64
}

// ServeEpoll mở n loop trên addr ("127.0.0.1:0" ⇒ cổng của loop đầu dùng cho
// mọi loop sau).
func ServeEpoll(addr string, n int, resp []byte) (*EpollServer, error) {
	ta, err := net.ResolveTCPAddr("tcp4", addr)
	if err != nil {
		return nil, err
	}
	s := &EpollServer{}
	for i := 0; i < n; i++ {
		l, port, err := newLoop(ta, resp)
		if err != nil {
			s.Close()
			return nil, err
		}
		ta.Port = port
		l.srv = s
		s.loops = append(s.loops, l)
	}
	s.addr = fmt.Sprintf("%s:%d", ta.IP, ta.Port)
	for _, l := range s.loops {
		s.wg.Add(1)
		go l.run()
	}
	return s, nil
}

func (s *EpollServer) Addr() string { return s.addr }

// PerLoop: số connection mỗi loop (SO_REUSEPORT chia đều không).
func (s *EpollServer) PerLoop() []int64 {
	out := make([]int64, len(s.loops))
	for i, l := range s.loops {
		out[i] = l.nconn.Load()
	}
	return out
}

// Close: loop thấy cờ trong ≤ 100 ms (EpollWait có timeout), tự đóng fd của nó.
func (s *EpollServer) Close() {
	s.closed.Store(true)
	s.wg.Wait()
}

func newLoop(ta *net.TCPAddr, resp []byte) (*loop, int, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, 0, err
	}
	syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, soReusePort, 1); err != nil {
		syscall.Close(fd)
		return nil, 0, err
	}
	sa := &syscall.SockaddrInet4{Port: ta.Port}
	copy(sa.Addr[:], ta.IP.To4())
	if err := syscall.Bind(fd, sa); err != nil {
		syscall.Close(fd)
		return nil, 0, err
	}
	if err := syscall.Listen(fd, 4096); err != nil {
		syscall.Close(fd)
		return nil, 0, err
	}
	got, _ := syscall.Getsockname(fd)
	port := got.(*syscall.SockaddrInet4).Port
	ep, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		syscall.Close(fd)
		return nil, 0, err
	}
	if err := syscall.EpollCtl(ep, syscall.EPOLL_CTL_ADD, fd, &syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(fd)}); err != nil {
		syscall.Close(fd)
		syscall.Close(ep)
		return nil, 0, err
	}
	return &loop{lfd: fd, ep: ep, conns: map[int32]*conn{}, buf: make([]byte, 64<<10), resp: resp}, port, nil
}

func (l *loop) run() {
	runtime.LockOSThread() // một loop = một luồng OS (điều kiện công bằng 1)
	defer l.srv.wg.Done()
	defer func() {
		for fd := range l.conns {
			syscall.Close(int(fd))
		}
		syscall.Close(l.lfd)
		syscall.Close(l.ep)
	}()
	events := make([]syscall.EpollEvent, 256)
	for !l.srv.closed.Load() {
		n, err := syscall.EpollWait(l.ep, events, 100)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return
		}
		for i := 0; i < n; i++ {
			ev := events[i]
			if int(ev.Fd) == l.lfd {
				l.accept()
				continue
			}
			c := l.conns[ev.Fd]
			if c == nil {
				continue
			}
			if ev.Events&syscall.EPOLLOUT != 0 && !l.flush(ev.Fd, c) {
				continue
			}
			if ev.Events&(syscall.EPOLLIN|syscall.EPOLLRDHUP|syscall.EPOLLHUP|syscall.EPOLLERR) != 0 {
				l.readAll(ev.Fd, c)
			}
		}
	}
}

func (l *loop) accept() {
	for {
		nfd, _, err := syscall.Accept4(l.lfd, syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC)
		if err != nil {
			return // EAGAIN: hết hàng đợi accept (hoặc lỗi tạm — epoll báo lại)
		}
		syscall.SetsockoptInt(nfd, syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 1)
		ev := &syscall.EpollEvent{Events: syscall.EPOLLIN | syscall.EPOLLRDHUP | -syscall.EPOLLET, Fd: int32(nfd)}
		if err := syscall.EpollCtl(l.ep, syscall.EPOLL_CTL_ADD, nfd, ev); err != nil {
			syscall.Close(nfd)
			continue
		}
		l.conns[int32(nfd)] = &conn{}
		l.nconn.Add(1)
		l.srv.Conns.Add(1)
	}
}

func (l *loop) close(fd int32) {
	delete(l.conns, fd)
	syscall.Close(int(fd)) // close tự gỡ khỏi epoll
	l.nconn.Add(-1)
	l.srv.Conns.Add(-1)
}

// readAll: edge-triggered ⇒ đọc tới EAGAIN, không thì sự kiện kế không tới.
func (l *loop) readAll(fd int32, c *conn) {
	for {
		n, err := syscall.Read(int(fd), l.buf)
		if n > 0 {
			if !l.handle(fd, c, l.buf[:n]) {
				return
			}
			continue
		}
		if err == syscall.EAGAIN {
			return
		}
		if err == syscall.EINTR {
			continue
		}
		l.close(fd) // n == 0 (FIN) hoặc lỗi
		return
	}
}

// handle: ghép phần head dở + chunk, trả một response cho mỗi head đủ, giữ
// phần dở (copy — l.buf dùng chung, bị ghi đè ở lần đọc sau).
func (l *loop) handle(fd int32, c *conn, chunk []byte) bool {
	data := chunk
	if len(c.in) > 0 {
		data = append(c.in, chunk...)
	}
	heads, rest := scan(data)
	if tail := data[rest:]; len(tail) > 0 {
		if len(tail) > MaxHead {
			l.close(fd)
			return false
		}
		c.in = append(c.in[:0:0], tail...)
	} else {
		c.in = nil // rỗi: không giữ byte nào
	}
	for i := 0; i < heads; i++ {
		l.srv.Requests.Add(1)
		if !l.write(fd, c, l.resp) {
			return false
		}
	}
	return true
}

// write: ghi thẳng nếu không có gì đang chờ; EAGAIN/ghi thiếu ⇒ giữ phần còn
// lại trong out và xin EPOLLOUT.
func (l *loop) write(fd int32, c *conn, b []byte) bool {
	if len(c.out) > 0 {
		c.out = append(c.out, b...)
		return true
	}
	for len(b) > 0 {
		n, err := syscall.Write(int(fd), b)
		if n > 0 {
			b = b[n:]
			continue
		}
		if err == syscall.EAGAIN {
			c.out = append([]byte(nil), b...)
			syscall.EpollCtl(l.ep, syscall.EPOLL_CTL_MOD, int(fd), &syscall.EpollEvent{Events: syscall.EPOLLIN | syscall.EPOLLOUT | syscall.EPOLLRDHUP | -syscall.EPOLLET, Fd: fd})
			return true
		}
		if err == syscall.EINTR {
			continue
		}
		l.close(fd)
		return false
	}
	return true
}

func (l *loop) flush(fd int32, c *conn) bool {
	for len(c.out) > 0 {
		n, err := syscall.Write(int(fd), c.out)
		if n > 0 {
			c.out = c.out[n:]
			continue
		}
		if err == syscall.EAGAIN {
			return true
		}
		if err == syscall.EINTR {
			continue
		}
		l.close(fd)
		return false
	}
	c.out = nil
	syscall.EpollCtl(l.ep, syscall.EPOLL_CTL_MOD, int(fd), &syscall.EpollEvent{Events: syscall.EPOLLIN | syscall.EPOLLRDHUP | -syscall.EPOLLET, Fd: fd})
	return true
}
