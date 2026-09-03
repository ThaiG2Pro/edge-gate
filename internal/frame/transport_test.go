package frame

import (
	"net"
	"testing"
	"time"
)

// P1-4. G6 chấm ✅ theo tài liệu: "net.Pipe không tái tạo được dribble lẫn
// coalesce". Đây là phép đo. Cùng ba kịch bản trên hai transport, ghi "Read thô
// đầu" — con số framelab in.
//
// Kết quả (xem diary): G6 đúng MỘT NỬA. net.Pipe tái tạo được short read
// (Read buffer nhỏ hơn Write) và "nhiều frame trong một Read" (nếu gửi trong một
// Write). Nó KHÔNG tái tạo được hai thứ: gom nhiều Write rời thành một Read, và
// Write trả về trước khi bên kia Read. Hai thứ đó là kernel socket buffer — thứ
// net.Pipe không có, và là thứ làm TCP khác một cái ống.

func pipePair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

// firstRawRead: sender ghi wire theo cách `write`, receiver làm một Read thô
// với buffer bufSize, trả về số byte nhận được ở Read đầu.
func firstRawRead(t *testing.T, client, server net.Conn, wire []byte, bufSize int, write func(net.Conn, []byte)) int {
	t.Helper()
	go write(client, wire)
	buf := make([]byte, bufSize)
	server.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := server.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func oneWrite(c net.Conn, w []byte) { c.Write(w) }
func byteWrites(c net.Conn, w []byte) {
	for i := range w {
		if _, err := c.Write(w[i : i+1]); err != nil {
			return
		}
	}
}

func TestTransportDifference(t *testing.T) {
	var wire []byte
	for _, f := range three {
		wire = Append(wire, f)
	}

	t.Run("coalesce/1 Write 62 byte, Read 4096", func(t *testing.T) {
		tc, ts := pair(t)
		pc, ps := pipePair(t)
		tcp := firstRawRead(t, tc, ts, wire, 4096, oneWrite)
		pipe := firstRawRead(t, pc, ps, wire, 4096, oneWrite)
		t.Logf("tcp=%d pipe=%d", tcp, pipe)
		if pipe != len(wire) {
			t.Errorf("pipe: %d, mong %d — net.Pipe PHẢI tái tạo được 'nhiều frame một Read'", pipe, len(wire))
		}
	})

	t.Run("short read/1 Write 62 byte, Read 7", func(t *testing.T) {
		tc, ts := pair(t)
		pc, ps := pipePair(t)
		tcp := firstRawRead(t, tc, ts, wire, 7, oneWrite)
		pipe := firstRawRead(t, pc, ps, wire, 7, oneWrite)
		t.Logf("tcp=%d pipe=%d", tcp, pipe)
		if pipe != 7 || tcp != 7 {
			t.Errorf("cả hai phải cho 7: tcp=%d pipe=%d", tcp, pipe)
		}
	})

	t.Run("merge/62 Write 1 byte không sleep, Read 4096", func(t *testing.T) {
		tc, ts := pair(t)
		pc, ps := pipePair(t)
		tcp := firstRawRead(t, tc, ts, wire, 4096, byteWrites)
		pipe := firstRawRead(t, pc, ps, wire, 4096, byteWrites)
		t.Logf("tcp=%d pipe=%d", tcp, pipe)
		if pipe != 1 {
			t.Errorf("pipe: %d, mong đúng 1 — net.Pipe không gom Write rời", pipe)
		}
		// tcp có thể là 1 hoặc nhiều — không assert, chỉ ghi. Đây là chỗ hai
		// transport KHÁC nhau, và là lý do test dribble/coalesce phải dùng TCP.
	})

	t.Run("async/Write khi không ai Read", func(t *testing.T) {
		tc, _ := pair(t)
		pc, _ := pipePair(t)
		returned := func(c net.Conn) bool {
			done := make(chan struct{})
			go func() { c.Write(wire); close(done) }()
			select {
			case <-done:
				return true
			case <-time.After(200 * time.Millisecond):
				return false
			}
		}
		tcp, pipe := returned(tc), returned(pc)
		t.Logf("Write trả về trong 200ms không có reader: tcp=%v pipe=%v", tcp, pipe)
		if !tcp || pipe {
			t.Errorf("mong tcp=true (kernel buffer) pipe=false (đồng bộ); được tcp=%v pipe=%v", tcp, pipe)
		}
	})
}
