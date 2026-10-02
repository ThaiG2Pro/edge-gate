package proxy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/thaivro/edgegate/internal/httpx"
)

// P3-3: upstream chết giữa body response. Head (CL 100) đã gửi cho client ⇒
// "một response cho một request": proxy KHÔNG được ghi thêm 502, chỉ được ĐÓNG;
// client đọc body nhận đúng 50 byte rồi io.ErrUnexpectedEOF; lỗi tính cho
// backend (outlier, phase 6 D7).
func TestUpstreamDiesMidBody(t *testing.T) {
	up := rawServer(t, func(c net.Conn, br *bufio.Reader) {
		if _, err := httpx.ReadRequest(br, httpx.DefaultLimits()); err != nil {
			return
		}
		fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n%050d", 0)
		// return ⇒ đóng sau 50/100 byte
	})
	s, p := startProxyS(t, up, nil)
	rc := dialRaw(t, p)
	rc.c.SetDeadline(time.Now().Add(3 * time.Second))
	io.WriteString(rc.c, "GET /x HTTP/1.1\r\nHost: x\r\n\r\n")
	resp, err := httpx.ReadResponse(rc.br, httpx.DefaultLimits(), "GET")
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	b, err := io.ReadAll(resp.Body)
	if len(b) != 50 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("body: %d byte, err %v — muốn 50 byte + ErrUnexpectedEOF", len(b), err)
	}
	// Sau body cụt: connection đóng, KHÔNG có byte nào nữa (không "HTTP/1.1 502").
	extra, err := io.ReadAll(rc.br)
	if len(extra) != 0 {
		t.Fatalf("proxy ghi thêm %q sau body cụt — hai response cho một request", extra)
	}
	if err != nil {
		t.Fatalf("đọc sau body: %v (muốn EOF sạch = connection đóng)", err)
	}
	deadline := time.Now().Add(time.Second)
	for s.LBStats().Backends[0].Fails == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f := s.LBStats().Backends[0].Fails; f != 1 {
		t.Fatalf("lỗi body upstream tính cho backend: fails=%d, muốn 1", f)
	}
}
