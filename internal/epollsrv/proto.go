// Package epollsrv: thí nghiệm phase 9 — cùng một giao thức tối giản phục vụ
// bằng (a) vòng epoll tự viết, đa luồng SO_REUSEPORT, một loop mỗi luồng OS
// (D7), và (b) goroutine-per-connection trên netpoller của Go. Không phải data
// path của EdgeGate: chỉ để so hai mô hình I/O với cùng việc phải làm.
//
// Giao thức: đọc tới "\r\n\r\n" (head request, không body), trả response cố
// định, keep-alive; nhận pipelining và head cắt ngang nhiều lần đọc.
package epollsrv

import (
	"bytes"
	"strconv"
)

// MaxHead: head dài hơn ⇒ đóng (I2 của repo — có trần trước khi giữ).
const MaxHead = 8 << 10

var crlf2 = []byte("\r\n\r\n")

// Response dựng response 200 với body n byte.
func Response(n int) []byte {
	h := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: " + strconv.Itoa(n) + "\r\n\r\n"
	return append([]byte(h), bytes.Repeat([]byte{'x'}, n)...)
}

// scan: số head đủ trong data và vị trí byte đầu chưa thuộc head nào.
func scan(data []byte) (heads, rest int) {
	for {
		i := bytes.Index(data[rest:], crlf2)
		if i < 0 {
			return heads, rest
		}
		heads++
		rest += i + 4
	}
}
