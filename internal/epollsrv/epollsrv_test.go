//go:build linux

package epollsrv

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

type server interface {
	Addr() string
	Close()
}

func both(t *testing.T, resp []byte, f func(t *testing.T, s server)) {
	t.Run("epoll", func(t *testing.T) {
		s, err := ServeEpoll("127.0.0.1:0", 3, resp)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		f(t, s)
	})
	t.Run("netpoller", func(t *testing.T) {
		s, err := ServeNetpoller("127.0.0.1:0", resp)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		f(t, s)
	})
}

func readN(t *testing.T, br *bufio.Reader, resp []byte, k int) {
	t.Helper()
	got := make([]byte, len(resp))
	for i := 0; i < k; i++ {
		if _, err := io.ReadFull(br, got); err != nil {
			t.Fatalf("response %d: %v", i, err)
		}
		if !bytes.Equal(got, resp) {
			t.Fatalf("response %d sai", i)
		}
	}
}

// Head cắt ngang nhiều lần ghi (kể cả giữa "\r\n\r\n"), pipelining 3 request
// một lần ghi, response lớn hơn socket buffer (EAGAIN ⇒ EPOLLOUT).
func TestProtocol(t *testing.T) {
	resp := Response(1024)
	both(t, resp, func(t *testing.T, s server) {
		c, err := net.Dial("tcp", s.Addr())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(3 * time.Second))
		br := bufio.NewReader(c)
		for _, part := range []string{"GET / HTTP/1.1\r\nHo", "st: x\r\n\r", "\n"} {
			io.WriteString(c, part)
			time.Sleep(10 * time.Millisecond)
		}
		readN(t, br, resp, 1)
		io.WriteString(c, "GET /a HTTP/1.1\r\n\r\nGET /b HTTP/1.1\r\n\r\nGET /c HTTP/1.1\r\n\r\n")
		readN(t, br, resp, 3)
	})
	big := Response(4 << 20)
	both(t, big, func(t *testing.T, s server) {
		c, _ := net.Dial("tcp", s.Addr())
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		io.WriteString(c, "GET / HTTP/1.1\r\n\r\nGET / HTTP/1.1\r\n\r\n")
		time.Sleep(50 * time.Millisecond) // để server đụng EAGAIN trước khi client đọc
		readN(t, bufio.NewReader(c), big, 2)
	})
}

// 1000 connection đồng thời, mỗi cái 5 request; SO_REUSEPORT chia cho cả 3 loop.
func TestManyConns(t *testing.T) {
	resp := Response(100)
	both(t, resp, func(t *testing.T, s server) {
		const n = 1000
		conns := make([]net.Conn, n)
		for i := range conns {
			c, err := net.Dial("tcp", s.Addr())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			conns[i] = c
		}
		for round := 0; round < 5; round++ {
			for _, c := range conns {
				io.WriteString(c, "GET / HTTP/1.1\r\n\r\n")
			}
			for _, c := range conns {
				c.SetDeadline(time.Now().Add(3 * time.Second))
				got := make([]byte, len(resp))
				if _, err := io.ReadFull(c, got); err != nil || !bytes.Equal(got, resp) {
					t.Fatalf("round %d: %v", round, err)
				}
			}
		}
		if es, ok := s.(*EpollServer); ok {
			t.Logf("epoll: %d request, connection mỗi loop %v", es.Requests.Load(), es.PerLoop())
		}
	})
}
