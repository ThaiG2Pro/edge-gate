package frame

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

// pair mở một cặp TCP thật trên loopback. Không dùng net.Pipe — nhưng vì lý do
// đã ĐO (TestTransportDifference), không phải lý do tôi viết lúc đầu: net.Pipe
// tái tạo được short read và nhiều-frame-một-Read; thứ nó không có là kernel
// socket buffer, nên Write chờ reader và hai Write rời không bao giờ gom thành
// một Read. Hình dạng "gom Write rời" (G7, 11 byte) chỉ thấy được trên TCP.
func pair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			t.Error(err)
			close(accepted)
			return
		}
		accepted <- c
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server = <-accepted
	if server == nil {
		t.FailNow()
	}
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

var three = []Frame{
	{Type: TypeData, Payload: []byte("alpha")},
	{Type: TypePing, Payload: nil},
	{Type: TypeData, Payload: []byte("gamma-with-a-longer-payload")},
}

func decodeN(t *testing.T, r io.Reader, n int) []Frame {
	t.Helper()
	d := NewDecoder(r, 0)
	out := make([]Frame, 0, n)
	for i := 0; i < n; i++ {
		f, err := d.Decode()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		out = append(out, f)
	}
	return out
}

func assertSame(t *testing.T, got, want []Frame) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d frame, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Type != want[i].Type || !bytes.Equal(got[i].Payload, want[i].Payload) {
			t.Fatalf("frame %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

// Bài quyết định 1 — sender nhỏ giọt: 1 byte mỗi 10ms. Mỗi Read của decoder
// nhận đúng 1 byte, tức header 10 byte cần ít nhất 10 lần Read. Không ReadFull
// là bài này đỏ ngay ở byte thứ 2.
func TestDribble(t *testing.T) {
	client, server := pair(t)
	var wire []byte
	for _, f := range three {
		wire = Append(wire, f)
	}
	if err := client.(*net.TCPConn).SetNoDelay(true); err != nil {
		t.Fatal(err)
	}
	go func() {
		for i := range wire {
			if _, err := client.Write(wire[i : i+1]); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	server.SetReadDeadline(time.Now().Add(10 * time.Second))
	assertSame(t, decodeN(t, server, len(three)), three)
}

// Bài quyết định 2 — sender dính gói: 3 frame trong MỘT lần Write. Decoder kiểu
// "1 Read = 1 frame" sẽ nhận 3 frame trong một buffer và vứt 2 frame sau.
func TestCoalesce(t *testing.T) {
	client, server := pair(t)
	var wire []byte
	for _, f := range three {
		wire = Append(wire, f)
	}
	if _, err := client.Write(wire); err != nil {
		t.Fatal(err)
	}
	server.SetReadDeadline(time.Now().Add(10 * time.Second))
	assertSame(t, decodeN(t, server, len(three)), three)
}

// Bất biến I2: length = 0xFFFFFFFF phải bị từ chối TRƯỚC khi cấp phát.
// Kiểm bằng hai cách độc lập — loại lỗi VÀ số byte đã cấp phát — vì với
// `-tags nodefense` cách 1 có thể đỏ vì lý do khác (ReadFull đứt), còn cách 2
// đỏ đúng chỗ: TotalAlloc nhảy ~4 GiB.
func TestCapBeforeAlloc(t *testing.T) {
	hdr := make([]byte, HeaderSize)
	binary.BigEndian.PutUint32(hdr[0:4], Magic)
	hdr[4] = Version
	hdr[5] = byte(TypeData)
	binary.BigEndian.PutUint32(hdr[6:10], 0xFFFFFFFF)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	_, err := NewDecoder(bytes.NewReader(hdr), 0).Decode()

	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrTooBig) {
		t.Errorf("err = %v, want ErrTooBig", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<16 {
		t.Errorf("decoder cấp phát %d byte cho một frame bị từ chối", grew)
	}
}

// Trần là của caller: max = 16 thì 17 byte payload hợp lệ về mọi mặt khác vẫn
// bị từ chối, và stream không được đọc tiếp qua chỗ đó.
func TestCustomMax(t *testing.T) {
	wire := Append(nil, Frame{Type: TypeData, Payload: bytes.Repeat([]byte("x"), 17)})
	_, err := NewDecoder(bytes.NewReader(wire), 16).Decode()
	if !errors.Is(err, ErrTooBig) {
		t.Fatalf("err = %v, want ErrTooBig", err)
	}
	wire = Append(nil, Frame{Type: TypeData, Payload: bytes.Repeat([]byte("x"), 16)})
	if _, err := NewDecoder(bytes.NewReader(wire), 16).Decode(); err != nil {
		t.Fatalf("16 byte ở trần 16 phải hợp lệ: %v", err)
	}
}

func TestBadMagicAndVersion(t *testing.T) {
	// Một HTTP request lạc vào cổng này phải chết ở magic, không phải ở length.
	_, err := NewDecoder(bytes.NewReader([]byte("GET / HTTP/1.1\r\n\r\n")), 0).Decode()
	if !errors.Is(err, ErrBadMagic) {
		t.Errorf("HTTP vào cổng frame: err = %v, want ErrBadMagic", err)
	}
	wire := Append(nil, Frame{Type: TypeData, Payload: []byte("v")})
	wire[4] = Version + 1
	_, err = NewDecoder(bytes.NewReader(wire), 0).Decode()
	if !errors.Is(err, ErrBadVersion) {
		t.Errorf("version lạ: err = %v, want ErrBadVersion", err)
	}
}

// EOF đúng ranh giới là io.EOF; EOF giữa frame là io.ErrUnexpectedEOF — ở
// mọi vị trí cắt, kể cả cắt sau header trọn với payload rỗng-nhưng-chưa-đủ.
func TestEOFSemantics(t *testing.T) {
	wire := Append(nil, Frame{Type: TypeData, Payload: []byte("hello")})
	d := NewDecoder(bytes.NewReader(wire), 0)
	if _, err := d.Decode(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Decode(); err != io.EOF {
		t.Errorf("sau frame cuối: err = %v, want io.EOF", err)
	}
	for cut := 1; cut < len(wire); cut++ {
		_, err := NewDecoder(bytes.NewReader(wire[:cut]), 0).Decode()
		if err != io.ErrUnexpectedEOF {
			t.Errorf("cắt tại %d/%d: err = %v, want io.ErrUnexpectedEOF", cut, len(wire), err)
		}
	}
}

// Bug tìm ra ở turn 2: Append từng làm uint32(len) im lặng. Payload 4 GiB + 1
// byte cấp phát được vì trang chưa chạm không tốn RSS — NHƯNG chỉ khi span
// không đè lên trang bẩn (P1-6, xem cmd/needzerolab): trong process đã có rác
// thì make(4 GiB) zero cả 4 GiB, 7s kernel, RSS +4 GiB. Đó cũng là lý do I2
// quan trọng hơn "4 GiB địa chỉ ảo" gợi ý.
func TestPayloadOverUint32(t *testing.T) {
	if testing.Short() {
		t.Skip("cấp phát 4 GiB ảo")
	}
	// P1-3: hai máy mà bài này KHÔNG chạy được, và không phải vì code sai.
	const want uint64 = MaxPayload + 1
	if uint64(^uint(0)>>1) < want {
		t.Skip("P1-3: int 32-bit không chứa được 4 GiB")
	}
	if b, err := os.ReadFile("/proc/sys/vm/overcommit_memory"); err == nil && strings.TrimSpace(string(b)) == "2" {
		t.Skip("P1-3: vm.overcommit_memory=2 — kernel từ chối 4 GiB chưa chạm")
	}
	// P1-6: nếu span 4 GiB đè lên trang vừa free còn bẩn (rác của test trước,
	// chưa scavenge) thì runtime phải zero CẢ span ⇒ chạm 4 GiB ⇒ ~1M page fault
	// ⇒ 5-8s kernel. Scavenge trước thì Go biết trang là zero và không chạm.
	runtime.GC()
	debug.FreeOSMemory()
	t0 := time.Now()
	huge := make([]byte, int(want))
	tMake := time.Since(t0)
	err := Encode(io.Discard, Frame{Type: TypeData, Payload: huge})
	// P1-6: 4 GiB "không tốn gì" đo được 0.00s lẫn 4.49s. In riêng thời gian make.
	t.Logf("make(4 GiB) = %v", tMake.Round(time.Microsecond))
	if !errors.Is(err, ErrPayloadTooBig) {
		t.Fatalf("Encode: err = %v, want ErrPayloadTooBig", err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("Append phải panic thay vì cắt cụt length")
		}
	}()
	Append(nil, Frame{Payload: huge})
}

func TestEncodeIsOneWrite(t *testing.T) {
	var w countingWriter
	if err := Encode(&w, Frame{Type: TypeData, Payload: []byte("payload")}); err != nil {
		t.Fatal(err)
	}
	if w.calls != 1 {
		t.Fatalf("Encode gọi Write %d lần, want 1 (write-write-read + Nagle = 44ms, phase 0)", w.calls)
	}
}

type countingWriter struct {
	calls int
	bytes.Buffer
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.calls++
	return c.Buffer.Write(p)
}

// FuzzFrameDecode: mọi byte string — không panic, không cấp phát quá trần, và
// mọi frame decode được phải encode lại thành đúng byte đã đọc (round-trip).
// Trần đặt nhỏ (4 KiB) để fuzzer với tay tới được vùng "length > trần" thường
// xuyên thay vì phải đoán đúng 4 byte lớn.
func FuzzFrameDecode(f *testing.F) {
	f.Add(Append(nil, Frame{Type: TypeData, Payload: []byte("seed")}))
	f.Add(Append(Append(nil, three[0]), three[2]))
	f.Add([]byte("GET / HTTP/1.1\r\n"))
	big := Append(nil, Frame{})
	binary.BigEndian.PutUint32(big[6:10], 0xFFFFFFFF)
	f.Add(big)

	const max = 4096
	f.Fuzz(func(t *testing.T, in []byte) {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		d := NewDecoder(bytes.NewReader(in), max)
		off := 0
		for {
			fr, err := d.Decode()
			if err != nil {
				break
			}
			if uint32(len(fr.Payload)) > max {
				t.Fatalf("payload %d byte vượt trần %d", len(fr.Payload), max)
			}
			n := HeaderSize + len(fr.Payload)
			if !bytes.Equal(Append(nil, fr), in[off:off+n]) {
				t.Fatalf("round-trip lệch tại offset %d", off)
			}
			off += n
		}
		runtime.ReadMemStats(&after)
		// Cộng cả input vì fuzzer có thể nhồi vài frame hợp lệ liên tiếp.
		if grew := after.TotalAlloc - before.TotalAlloc; grew > uint64(len(in))+max+1<<16 {
			t.Fatalf("cấp phát %d byte cho input %d byte", grew, len(in))
		}
	})
}

// P1-2. Hai nửa, cùng một kịch bản: sender gửi 1 byte rồi im.
//
//	(a) Không deadline: Decode KHÔNG trả về sau 300ms — đây là slowloris ở tầng
//	    frame, và là điều TestDribble ngầm cho phép. Không thể "assert treo mãi",
//	    nên chốt bằng mốc 300ms và ghi rõ: đây là bằng chứng của hình dạng, không
//	    của vô hạn.
//	(b) Có SetReadDeadline: Decode trả lỗi timeout trong vòng ~50ms.
//
// Decoder cố ý không tự đặt deadline — đó là quyết định của tầng connection
// (phase 7). Test này tồn tại để điều đó là quyết định có ghi chép, không phải quên.
func TestDecodeHangsWithoutDeadline(t *testing.T) {
	client, server := pair(t)
	if _, err := client.Write([]byte{0x45}); err != nil { // byte đầu của Magic, rồi im
		t.Fatal(err)
	}
	d := NewDecoder(server, 0)
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := d.Decode()
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("(a) Decode trả về sau %v không có deadline: %v — mong nó treo", time.Since(start), err)
	case <-time.After(300 * time.Millisecond):
		// treo đúng như mong đợi
	}

	if err := server.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			t.Fatalf("(b) err = %v, mong lỗi timeout", err)
		}
		t.Logf("treo %v không deadline; deadline giải sau %v; err = %v",
			300*time.Millisecond, time.Since(start).Round(time.Millisecond), err)
	case <-time.After(2 * time.Second):
		t.Fatal("(b) đã đặt deadline mà Decode vẫn không trả về")
	}
}
