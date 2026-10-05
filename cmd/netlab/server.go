package main

import (
	"bufio"
	"errors"
	"github.com/ThaiG2Pro/edge-gate/internal/frame"
	"io"
	"log"
	"net"
	"sync/atomic"
	"time"
)

type server struct {
	ln net.Listener

	// sem giới hạn số request được XỬ LÝ đồng thời. Đây là "capacity" của server.
	// Thí nghiệm coordinated omission cần một server có trần rõ ràng, nếu không
	// thì không tạo được trạng thái quá tải để tail latency lộ ra.
	sem chan struct{}

	// useBufio quyết định có cấp bufio.Reader+Writer ngay khi accept hay không.
	// Đây chính là biến của câu hỏi 4 (giá bộ nhớ của một connection rỗi).
	useBufio bool

	accepted atomic.Int64
	served   atomic.Int64
	errs     atomic.Int64

	closed atomic.Bool
}

func newServer(addr string, workers int, useBufio bool) (*server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &server{ln: ln, sem: make(chan struct{}, workers), useBufio: useBufio}
	go s.acceptLoop()
	return s, nil
}

func (s *server) addr() string { return s.ln.Addr().String() }

func (s *server) close() {
	s.closed.Store(true)
	_ = s.ln.Close()
}

func (s *server) acceptLoop() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if s.closed.Load() {
				return
			}
			// Trần fd/port chạm ở đây trước khi chạm ở client. Không im lặng bỏ qua.
			s.errs.Add(1)
			log.Printf("server accept: %v", err)
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(time.Millisecond)
			continue
		}
		s.accepted.Add(1)
		go s.handle(c)
	}
}

func (s *server) handle(c net.Conn) {
	defer c.Close()

	tc, _ := c.(*net.TCPConn)
	if tc != nil {
		// Mặc định Go đã bật TCP_NODELAY. Ghi ra đây cho tường minh: bộ đo này
		// KHÔNG dựa vào mặc định của thư viện, vì phép đo Nagle phụ thuộc nó.
		_ = tc.SetNoDelay(true)
	}

	var r io.Reader = c
	var w io.Writer = c
	var bw *bufio.Writer
	if s.useBufio {
		r = bufio.NewReader(c)
		bw = bufio.NewWriter(c)
		w = bw
	}

	dec := frame.NewDecoder(r, maxFrameSize)
	nagleApplied := false
	for {
		f, err := dec.Decode()
		if err != nil {
			if err != io.EOF && !errors.Is(err, net.ErrClosed) {
				s.errs.Add(1)
			}
			return
		}
		req, err := decodeRequest(f)
		if err != nil {
			s.errs.Add(1)
			return
		}

		if req.flags&flagNagleOn != 0 && !nagleApplied && tc != nil {
			_ = tc.SetNoDelay(false)
			nagleApplied = true
		}

		s.sem <- struct{}{}
		if req.svcMicros > 0 {
			time.Sleep(time.Duration(req.svcMicros) * time.Microsecond)
		}
		err = writeResponse(w, int(req.respSize), req.flags&flagTwoWrites != 0)
		<-s.sem

		if err == nil && bw != nil {
			err = bw.Flush()
		}
		if err != nil {
			s.errs.Add(1)
			return
		}
		s.served.Add(1)
	}
}

// writeResponse là tâm của thí nghiệm G1. twoWrites=false ghi length và payload
// trong MỘT lần Write; twoWrites=true ghi hai lần, như một proxy viết header
// rồi io.Copy body mà không có bufio.Writer ở giữa.
func writeResponse(w io.Writer, size int, twoWrites bool) error {
	var hdr [frame.HeaderSize]byte
	frame.PutHeader(hdr[:], typeResp, uint32(size))
	if twoWrites {
		if _, err := w.Write(hdr[:]); err != nil {
			return err
		}
		if size == 0 {
			return nil
		}
		_, err := w.Write(respPad[:size])
		return err
	}
	buf := make([]byte, frame.HeaderSize+size)
	copy(buf, hdr[:])
	copy(buf[frame.HeaderSize:], respPad[:size])
	_, err := w.Write(buf)
	return err
}

var respPad = make([]byte, maxFrameSize)
