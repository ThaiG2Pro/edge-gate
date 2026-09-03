package httpx

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func TestChunkedReject(t *testing.T) {
	cases := map[string]error{
		"G\r\nx\r\n0\r\n\r\n":                         ErrBadChunk,              // không hex
		"\r\n0\r\n\r\n":                               ErrBadChunk,              // size rỗng
		"1FFFFFFFFFFFFFFFF\r\n":                       ErrBadChunk,              // 17 chữ số
		"3\r\nabcX\r\n0\r\n\r\n":                      ErrBadChunk,              // data không kết thúc CRLF
		"3\r\nabc\r\n0\r\nX Y: z\r\n\r\n":             &ProtoError{Status: 400}, // trailer tên sai
		"3\nabc\r\n0\r\n\r\n":                         ErrBareLF,
		"3\r\nab":                                     io.ErrUnexpectedEOF,
		"3\r\nabc\r\n":                                io.ErrUnexpectedEOF, // thiếu chunk cuối
		"0\r\n":                                       io.ErrUnexpectedEOF, // thiếu dòng trống sau trailer
		" 3\r\nabc\r\n0\r\n\r\n":                      ErrBadChunk,         // turn 1 nhận (trim OWS); oracle từ chối ⇒ siết
		"3 ;x\r\nabc\r\n0\r\n\r\n":                    ErrBadChunk,         // như trên
		"3  \r\nabc\r\n0\r\n\r\n":                     ErrBadChunk,         // oracle NHẬN cái này; mình từ chối — vô hại
		"0003\r\nabc\r\n0\r\n\r\n":                    nil,
		"3;a=b;c\r\nabc\r\n0\r\nT: v\r\nT: w\r\n\r\n": nil,
	}
	for in, want := range cases {
		cr := newChunkedReader(rd(in), DefaultLimits())
		_, err := io.ReadAll(cr)
		switch {
		case want == nil && err != nil:
			t.Errorf("%q: muốn OK, có %v", in, err)
		case want != nil && !errors.Is(err, want):
			t.Errorf("%q: muốn %v, có %v", in, want, err)
		}
	}
}

func TestChunkedShortReadsOverTCP(t *testing.T) {
	// Chunk bị cắt vụn 1 byte/1ms qua TCP thật: decoder phải ráp lại đúng —
	// phase 1 TestDribble, phiên bản HTTP.
	client, server := pair(t)
	wire := "4\r\nwiki\r\n5\r\npedia\r\n0\r\nX: 1\r\n\r\nNEXT"
	go func() {
		for i := range wire {
			client.Write([]byte{wire[i]})
			time.Sleep(time.Millisecond)
		}
	}()
	server.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := rd("")
	br.Reset(server)
	cr := newChunkedReader(br, DefaultLimits())
	if b := readAll(t, cr); string(b) != "wikipedia" {
		t.Fatalf("%q", b)
	}
	if cr.Trailer.Get("X") != "1" || cr.chunks != 3 {
		t.Fatalf("trailer %v chunks %d", cr.Trailer, cr.chunks)
	}
	rest := make([]byte, 4)
	io.ReadFull(br, rest)
	if string(rest) != "NEXT" {
		t.Fatalf("dư %q", rest)
	}
}

// TestChunkSizeDoesNotAllocate là bài I2 của phase 2 (G3): chunk-size
// FFFFFFFF không làm reader make 4 GiB. Đo hai điểm: reader stream (Δ nhỏ) vs
// một decoder ngây thơ make theo size (Δ = 4 GiB) — cùng input.
func TestChunkSizeDoesNotAllocate(t *testing.T) {
	in := "FFFFFFFF\r\nabc"
	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	before := ms.TotalAlloc

	cr := newChunkedReader(rd(in), DefaultLimits())
	buf := make([]byte, 16)
	n, _ := cr.Read(buf)
	_, err := cr.Read(buf)

	runtime.ReadMemStats(&ms)
	delta := ms.TotalAlloc - before
	t.Logf("stream: đọc %d byte, err=%v, ΔTotalAlloc = %d byte", n, err, delta)
	if n != 3 || err != io.ErrUnexpectedEOF {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if delta > 1<<16 {
		t.Fatalf("I2 vỡ: chunked reader cấp phát %d byte theo chunk-size", delta)
	}

	if testing.Short() {
		return
	}
	// Điểm đo thứ hai: decoder ngây thơ. P1-6: scavenge trước để đo cấp phát,
	// không đo tình trạng heap bẩn.
	runtime.GC()
	debug.FreeOSMemory()
	runtime.ReadMemStats(&ms)
	before = ms.TotalAlloc
	t0 := time.Now()
	size, _ := parseChunkSize([]byte("FFFFFFFF"))
	naive := make([]byte, size) // ĐÂY là bug mà reader thật không có
	k, _ := io.ReadFull(strings.NewReader("abc"), naive)
	runtime.ReadMemStats(&ms)
	t.Logf("ngây thơ: make(%d) trong %v, đọc %d byte, ΔTotalAlloc = %d byte", size, time.Since(t0).Round(time.Microsecond), k, ms.TotalAlloc-before)
	if ms.TotalAlloc-before < 1<<32 {
		t.Fatalf("điểm đo đối chứng không như dự đoán: Δ=%d", ms.TotalAlloc-before)
	}
	runtime.KeepAlive(naive)
}

func TestChunkedWriterRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	cw := NewChunkedWriter(&countWriter{w: &buf})
	for _, p := range []string{"wiki", "", "pedia", strings.Repeat("x", 300)} {
		if _, err := cw.Write([]byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	cw.Close()
	writes := cw.w.(*countWriter).n
	if writes != 4 { // 3 chunk (rỗng bị bỏ) + chunk cuối, mỗi cái ĐÚNG MỘT Write
		t.Fatalf("số Write %d, muốn 4", writes)
	}
	if !strings.HasPrefix(buf.String(), "4\r\nwiki\r\n5\r\npedia\r\n12c\r\n") {
		t.Fatalf("%q", buf.String()[:40])
	}
	cr := newChunkedReader(rd(buf.String()+"NEXT"), DefaultLimits())
	if b := readAll(t, cr); string(b) != "wikipedia"+strings.Repeat("x", 300) {
		t.Fatalf("%q", b)
	}
}

type countWriter struct {
	w io.Writer
	n int
}

func (c *countWriter) Write(p []byte) (int, error) { c.n++; return c.w.Write(p) }
