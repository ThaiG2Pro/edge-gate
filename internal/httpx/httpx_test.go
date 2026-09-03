package httpx

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"strings"
	"testing"
)

// rd tạo *bufio.Reader từ chuỗi. Dấu "\n" trong test được viết tay là CRLF
// đầy đủ — KHÔNG tự đổi LF→CRLF, vì bare LF là thứ cần test (D1).
func rd(s string) *bufio.Reader { return bufio.NewReader(strings.NewReader(s)) }

// rdSmall: bufio buffer nhỏ (16 byte, min của bufio) để ép readLine đi qua
// nhánh ErrBufferFull với dòng bình thường — nhánh mà buffer 4 KiB che mất.
func rdSmall(s string) *bufio.Reader { return bufio.NewReaderSize(strings.NewReader(s), 16) }

// countingReader đếm byte tiêu thụ từ nguồn (dưới bufio) — đo "parser đọc
// vào bao nhiêu trước khi từ chối".
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	k, err := c.r.Read(p)
	c.n += k
	return k, err
}

// pair mở TCP thật trên loopback — lý do như phase 1 (P1-4): net.Pipe không
// có Write bất đồng bộ và không gom Write rời, còn keep-alive/pipelining/
// slowloris đúng là chuyện gom-hay-không-gom.
func pair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Biến lỗi RIÊNG: bản đầu dùng chung `err` với Dial bên dưới ⇒ data
		// race thật (-race bắt được ở lần chạy đầu). Test helper cũng là code.
		c, aerr := ln.Accept()
		if aerr != nil {
			t.Error(aerr)
			return
		}
		server = c
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if server == nil {
		t.FailNow()
	}
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

func mustProto(t *testing.T, err error, want *ProtoError) {
	t.Helper()
	pe, ok := IsProtoError(err)
	if !ok {
		t.Fatalf("muốn *ProtoError %v, có %v", want, err)
	}
	if pe.Status != want.Status {
		t.Fatalf("muốn status %d, có %d (%s)", want.Status, pe.Status, pe.Reason)
	}
}

func readAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("đọc body: %v", err)
	}
	return b
}

var _ = bytes.Equal
