//go:build linux

package proxy

import (
	"errors"
	"net"
	"syscall"
)

// probeIdle (D3): connection rỗi lấy từ pool còn sống không?
//
// Upstream đóng connection rỗi thì FIN (hoặc RST) đã nằm trong receive queue
// của kernel — Go chưa báo gì chỉ vì CHƯA AI GỌI Read. Một recv(MSG_PEEK |
// MSG_DONTWAIT) trả lời ngay, không tiêu byte, không chờ:
//
//	EAGAIN      ⇒ rỗi thật, còn sống      → dùng
//	n == 0      ⇒ FIN: upstream đã đóng   → bỏ
//	lỗi khác    ⇒ RST / hỏng              → bỏ
//	n > 0       ⇒ upstream gửi byte ta không hỏi (response thừa, rác) ⇒ BẨN → bỏ
//
// known=false khi không lấy được fd (không phải *net.TCPConn) — caller coi
// như "không biết", đi tiếp và trông vào retry (D4). Đây không phải 100 %:
// upstream có thể đóng giữa probe và Write. D4 đỡ phần còn lại.
func probeIdle(c net.Conn) (dead, known bool) {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return false, false
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return true, true
	}
	var (
		n    int
		rerr error
	)
	err = rc.Read(func(fd uintptr) bool {
		var b [1]byte
		n, _, rerr = syscall.Recvfrom(int(fd), b[:], syscall.MSG_PEEK|syscall.MSG_DONTWAIT)
		return true // không chờ readable: MSG_DONTWAIT đã trả lời rồi
	})
	switch {
	case err != nil:
		return true, true
	case errors.Is(rerr, syscall.EAGAIN) || errors.Is(rerr, syscall.EWOULDBLOCK):
		return false, true
	case rerr != nil:
		return true, true
	case n == 0:
		return true, true // FIN
	default:
		return true, true // byte lạ ⇒ bẩn
	}
}
