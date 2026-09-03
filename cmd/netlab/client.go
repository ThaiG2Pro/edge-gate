package main

import (
	"bufio"
	"net"
	"time"

	"github.com/thaivro/edgegate/internal/frame"
)

// conn là một đầu client. Cố ý KHÔNG dùng bufio ở client cho các phép đo latency:
// bufio ở client sẽ gộp write và làm nhoè đúng cái thứ G1 đang đo.
type clientConn struct {
	c   net.Conn
	br  *bufio.Reader
	dec *frame.Decoder
}

func dial(addr string, nodelay bool) (*clientConn, error) {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(nodelay)
	}
	br := bufio.NewReaderSize(c, 8192)
	return &clientConn{c: c, br: br, dec: frame.NewDecoder(br, maxFrameSize)}, nil
}

func (cc *clientConn) close() { _ = cc.c.Close() }

// roundtrip gửi một request và đọc trọn response. Trả về thời gian tường.
func (cc *clientConn) roundtrip(req request) (time.Duration, error) {
	buf := req.encode()
	start := time.Now()
	if _, err := cc.c.Write(buf); err != nil {
		return 0, err
	}
	if _, err := cc.dec.Decode(); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

// roundtripSplit ghi request bằng HAI lần Write. Dùng cho phép đo Nagle:
// write-write-read là hình dạng kinh điển kích hoạt Nagle + delayed ACK.
func (cc *clientConn) roundtripSplit(req request) (time.Duration, error) {
	buf := req.encode()
	split := frame.HeaderSize + reqHeaderSize
	if split >= len(buf) {
		split = len(buf) / 2
	}
	start := time.Now()
	if _, err := cc.c.Write(buf[:split]); err != nil {
		return 0, err
	}
	if _, err := cc.c.Write(buf[split:]); err != nil {
		return 0, err
	}
	if _, err := cc.dec.Decode(); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}
