package h2

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thaivro/edgegate/internal/h2/hpack"
)

// testServer: listener + ServeConn mỗi connection với handler h.
type testServer struct {
	ln    net.Listener
	stats Stats
	wg    sync.WaitGroup
}

func startServer(t *testing.T, cfg Config, h Handler) *testServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &testServer{ln: ln}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				ServeConn(c, c, cfg, h, &s.stats)
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *testServer) addr() string { return s.ln.Addr().String() }

// echo: GET /size/N ⇒ N byte; POST ⇒ trả số byte body + sha-free tổng; GET khác ⇒ "hello".
func echo(w *Stream, r *Request) {
	switch {
	case r.Method == "POST":
		n, err := io.Copy(io.Discard, w)
		if err != nil {
			w.WriteHeaders(400, nil, false)
			w.WriteData([]byte(err.Error()), true)
			return
		}
		w.WriteHeaders(200, nil, false)
		w.WriteData([]byte(strconv.FormatInt(n, 10)), true)
	case strings.HasPrefix(r.Path, "/size/"):
		n, _ := strconv.Atoi(strings.TrimPrefix(r.Path, "/size/"))
		w.WriteHeaders(200, []hpack.HeaderField{{Name: "content-length", Value: strconv.Itoa(n)}}, false)
		w.WriteData(bytes.Repeat([]byte("x"), n), true)
	default:
		w.WriteHeaders(200, []hpack.HeaderField{{Name: "content-type", Value: "text/plain"}}, false)
		w.WriteData([]byte("hello"), true)
	}
}

func h2cClient() *http.Client {
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: &http.Transport{Protocols: &p}, Timeout: 10 * time.Second}
}

// TestInteropNetHTTP (D9 peer 2): client net/http độc lập — GET, response
// 300 KB (> window 65 535 ⇒ cần WINDOW_UPDATE của client), POST 1 MiB (>
// window của ta ⇒ ta phải WINDOW_UPDATE khi đọc), 50 stream song song trên
// MỘT connection.
func TestInteropNetHTTP(t *testing.T) {
	s := startServer(t, Config{}, echo)
	cl := h2cClient()
	base := "http://" + s.addr()

	resp, err := cl.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.ProtoMajor != 2 || string(b) != "hello" {
		t.Fatalf("proto %s body %q", resp.Proto, b)
	}

	resp, err = cl.Get(base + "/size/300000")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(b) != 300000 {
		t.Fatalf("size: %d", len(b))
	}

	body := make([]byte, 1<<20)
	rand.Read(body)
	resp, err = cl.Post(base+"/up", "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != strconv.Itoa(len(body)) {
		t.Fatalf("POST: %q", b)
	}

	var wg sync.WaitGroup
	var bad atomic.Int64
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := cl.Get(base + "/size/20000")
			if err != nil {
				bad.Add(1)
				return
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if len(b) != 20000 {
				bad.Add(1)
			}
		}()
	}
	wg.Wait()
	if bad.Load() != 0 {
		t.Fatalf("%d request lỗi", bad.Load())
	}
	t.Logf("streams=%d resets_sent=%d max_active=%d", s.stats.Streams.Load(), s.stats.ResetsSent.Load(), s.stats.MaxActive.Load())
}

// TestCurlInterop (D9 peer 3): libnghttp2 qua curl.
func TestCurlInterop(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("không có curl")
	}
	s := startServer(t, Config{}, echo)
	out, err := exec.Command("curl", "-sS", "--http2-prior-knowledge", "-w", " %{http_version}", "http://"+s.addr()+"/").CombinedOutput()
	if err != nil {
		t.Fatalf("curl: %v %s", err, out)
	}
	if string(out) != "hello 2" {
		t.Fatalf("curl: %q", out)
	}
}

// rawConn: client thô sau khi đã nhận SETTINGS của server.
func rawConn(t *testing.T, s *testServer, ss ...Setting) *RawClient {
	t.Helper()
	nc, err := net.Dial("tcp", s.addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	c, err := NewRawClient(nc, ss...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// expect đọc frame tới khi gặp loại t (bỏ qua SETTINGS/WINDOW_UPDATE/PING
// ack…); trả nil nếu connection đóng trước.
func expect(t *testing.T, c *RawClient, typ FrameType, stream uint32) *Frame {
	t.Helper()
	c.NC.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		f, err := c.Fr.ReadFrame()
		if err != nil {
			t.Fatalf("chờ %v stream %d: %v", typ, stream, err)
		}
		if f.Type == FrameSettings && !f.Has(FlagAck) {
			c.Frame(FrameSettings, FlagAck, 0, nil)
			continue
		}
		if f.Type == typ && (typ == FrameGoAway || f.Stream == stream) {
			cp := *f
			cp.Payload = append([]byte(nil), f.Payload...)
			return &cp
		}
	}
}

func TestGoAwayCases(t *testing.T) {
	cases := []struct {
		name string
		send func(c *RawClient)
		code ErrCode
	}{
		{"frame quá MAX_FRAME_SIZE", func(c *RawClient) { c.Frame(FrameData, 0, 1, make([]byte, 16385)) }, ErrFrameSize},
		{"stream id chẵn", func(c *RawClient) { c.Headers(2, true, GET("a", "/")...) }, ErrProtocol},
		{"window connection tràn", func(c *RawClient) { c.WindowUpdate(0, 1<<31-1) }, ErrFlowControl},
		{"WINDOW_UPDATE 0 trên connection", func(c *RawClient) { c.WindowUpdate(0, 0) }, ErrProtocol},
		{"PING dài 7", func(c *RawClient) { c.Frame(FramePing, 0, 0, make([]byte, 7)) }, ErrFrameSize},
		{"SETTINGS trên stream 1", func(c *RawClient) { c.Frame(FrameSettings, 0, 1, nil) }, ErrProtocol},
		{"CONTINUATION mồ côi", func(c *RawClient) { c.Frame(FrameContinuation, FlagEndHeaders, 1, nil) }, ErrProtocol},
		{"HPACK hỏng", func(c *RawClient) { c.Frame(FrameHeaders, FlagEndHeaders|FlagEndStream, 1, []byte{0x80}) }, ErrCompression},
		{"DATA trên stream idle", func(c *RawClient) { c.Frame(FrameData, 0, 5, []byte("x")) }, ErrProtocol},
		{"MAX_FRAME_SIZE < 16384", func(c *RawClient) { c.Fr.WriteSettings(Setting{SettingMaxFrameSize, 100}); c.Fr.Flush() }, ErrProtocol},
	}
	s := startServer(t, Config{}, echo)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := rawConn(t, s)
			tc.send(c)
			f := expect(t, c, FrameGoAway, 0)
			if got := GoAwayCode(f); got != tc.code {
				t.Fatalf("GOAWAY %v, muốn %v", got, tc.code)
			}
		})
	}
}

// Malformed (D6 c): RST_STREAM PROTOCOL_ERROR cho đúng stream đó, connection
// sống — request kế trên cùng connection vẫn 200.
func TestMalformedStreamReset(t *testing.T) {
	s := startServer(t, Config{}, echo)
	cases := []struct {
		name   string
		fields []hpack.HeaderField
	}{
		{"CRLF trong value", GET("a", "/", hpack.HeaderField{Name: "x", Value: "a\r\nTransfer-Encoding: chunked"})},
		{"tên in hoa", GET("a", "/", hpack.HeaderField{Name: "X-Up", Value: "1"})},
		{"transfer-encoding", GET("a", "/", hpack.HeaderField{Name: "transfer-encoding", Value: "chunked"})},
		{"connection", GET("a", "/", hpack.HeaderField{Name: "connection", Value: "close"})},
		{"te: gzip", GET("a", "/", hpack.HeaderField{Name: "te", Value: "gzip"})},
		{"thiếu :path", GET("a", "/")[:3]},
		{"pseudo sau header thường", append(GET("a", "/")[:3], hpack.HeaderField{Name: "x", Value: "1"}, hpack.HeaderField{Name: ":path", Value: "/"})},
		{"pseudo lạ", GET("a", "/", hpack.HeaderField{Name: ":foo", Value: "1"})},
	}
	c := rawConn(t, s)
	id := uint32(1)
	for _, tc := range cases {
		c.Headers(id, true, tc.fields...)
		f := expect(t, c, FrameRSTStream, id)
		if RSTCode(f) != ErrProtocol {
			t.Fatalf("%s: RST %v", tc.name, RSTCode(f))
		}
		id += 2
	}
	c.Headers(id, true, GET("a", "/")...)
	f := expect(t, c, FrameHeaders, id)
	p, _ := HeaderPayload(f)
	fs, err := c.Dec.Decode(p)
	if err != nil || fs[0].Value != "200" {
		t.Fatalf("request hợp lệ sau 8 malformed: %v %v", fs, err)
	}
}

// G6 tầng h2: content-length 5 mà DATA 10 byte ⇒ RST, và handler KHÔNG BAO
// GIỜ đọc được byte thứ 6 (với proxy h2→h1, byte đó là request smuggled).
func TestContentLengthMismatch(t *testing.T) {
	got := make(chan string, 1)
	s := startServer(t, Config{}, func(w *Stream, r *Request) {
		b, err := io.ReadAll(w)
		got <- fmt.Sprintf("%q %v", b, err)
		w.WriteHeaders(200, nil, true)
	})
	c := rawConn(t, s)
	c.Headers(1, false, POST("a", "/", hpack.HeaderField{Name: "content-length", Value: "5"})...)
	c.Frame(FrameData, FlagEndStream, 1, []byte("12345GET /"))
	f := expect(t, c, FrameRSTStream, 1)
	if RSTCode(f) != ErrProtocol {
		t.Fatalf("RST %v", RSTCode(f))
	}
	select {
	case g := <-got:
		if strings.Contains(g, "GET") {
			t.Fatalf("handler đọc được byte vượt CL: %s", g)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler không thoát")
	}
}

// Flow control (D5): client window 0 ⇒ server gửi HEADERS nhưng DATA phải chờ
// đúng tới khi client WINDOW_UPDATE, và chỉ đúng ngần đó byte.
func TestFlowControlStreamWindow(t *testing.T) {
	s := startServer(t, Config{}, echo)
	c := rawConn(t, s, Setting{SettingInitialWindowSize, 0})
	c.Headers(1, true, GET("a", "/size/100")...)
	expect(t, c, FrameHeaders, 1)
	c.NC.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	for {
		f, err := c.Fr.ReadFrame()
		if err != nil {
			break // timeout: đúng, không có DATA
		}
		if f.Type == FrameData && len(f.Payload) > 0 {
			t.Fatalf("DATA %d byte khi window = 0", len(f.Payload))
		}
	}
	c.WindowUpdate(1, 7)
	f := expect(t, c, FrameData, 1)
	if len(f.Payload) != 7 {
		t.Fatalf("DATA %d byte, window 7", len(f.Payload))
	}
	c.WindowUpdate(1, 1000)
	total := 7
	for total < 100 {
		f = expect(t, c, FrameData, 1)
		total += len(f.Payload)
	}
	if !f.Has(FlagEndStream) || total != 100 {
		t.Fatalf("tổng %d end %v", total, f.Has(FlagEndStream))
	}
}

// I3 phía gửi: client không bao giờ WINDOW_UPDATE ⇒ handler không treo mãi.
func TestWindowTimeout(t *testing.T) {
	errc := make(chan error, 1)
	s := startServer(t, Config{WriteTimeout: 200 * time.Millisecond}, func(w *Stream, r *Request) {
		w.WriteHeaders(200, nil, false)
		errc <- w.WriteData(make([]byte, 10), true)
	})
	c := rawConn(t, s, Setting{SettingInitialWindowSize, 0})
	c.Headers(1, true, GET("a", "/")...)
	select {
	case err := <-errc:
		if !errors.Is(err, ErrWindowTimeout) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler treo chờ window")
	}
}

// I3 phía nhận: body không bao giờ tới ⇒ Read trả ErrBodyTimeout.
func TestBodyTimeout(t *testing.T) {
	errc := make(chan error, 1)
	s := startServer(t, Config{BodyTimeout: 200 * time.Millisecond}, func(w *Stream, r *Request) {
		_, err := io.ReadAll(w)
		errc <- err
	})
	c := rawConn(t, s)
	c.Headers(1, false, POST("a", "/")...)
	select {
	case err := <-errc:
		if !errors.Is(err, ErrBodyTimeout) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler treo chờ body")
	}
}

// MAX_CONCURRENT_STREAMS: stream vượt trần ⇒ REFUSED_STREAM; block của nó vẫn
// được decode ⇒ request sau dùng mục bảng động của block bị từ chối vẫn 200.
func TestRefusedKeepsHPACKInSync(t *testing.T) {
	release := make(chan struct{})
	s := startServer(t, Config{MaxConcurrentStreams: 2}, func(w *Stream, r *Request) {
		if r.Path == "/hold" {
			<-release
		}
		w.WriteHeaders(200, nil, true)
	})
	c := rawConn(t, s)
	c.Headers(1, true, GET("a", "/hold")...)
	c.Headers(3, true, GET("a", "/hold")...)
	extra := hpack.HeaderField{Name: "x-new", Value: "chỉ có trong block bị từ chối"}
	c.Headers(5, true, GET("a", "/", extra)...)
	if f := expect(t, c, FrameRSTStream, 5); RSTCode(f) != ErrRefusedStream {
		t.Fatalf("RST %v", RSTCode(f))
	}
	close(release)
	// Thứ tự HEADERS của 1 và 3 tuỳ lịch goroutine — chờ cả hai, bất kể thứ tự
	// (bản đầu expect(1) rồi expect(3) nuốt mất frame của 3 khi nó tới trước).
	c.NC.SetReadDeadline(time.Now().Add(3 * time.Second))
	for seen := map[uint32]bool{}; !seen[1] || !seen[3]; {
		f, err := c.Fr.ReadFrame()
		if err != nil {
			t.Fatalf("chờ HEADERS 1 và 3: %v (đã thấy %v)", err, seen)
		}
		if f.Type == FrameHeaders {
			p, _ := HeaderPayload(f)
			if _, err := c.Dec.Decode(p); err != nil { // giữ bảng động của client đồng bộ
				t.Fatal(err)
			}
			seen[f.Stream] = true
		}
	}
	c.Headers(7, true, GET("a", "/", extra)...) // encoder giờ gửi x-new dạng indexed
	f := expect(t, c, FrameHeaders, 7)
	p, _ := HeaderPayload(f)
	if fs, err := c.Dec.Decode(p); err != nil || fs[0].Value != "200" {
		t.Fatalf("%v %v", fs, err)
	}
}

// G7 (a) Rapid Reset: 5 000 cặp HEADERS+RST, handler giữ 200 ms bỏ qua reset
// (như proxy đang chờ upstream). Phòng tuyến ⇒ handler đồng thời ≤ 100.
// -tags nodefense10 ⇒ PHẢI ĐỎ.
func TestRapidReset(t *testing.T) {
	var cur, peak atomic.Int64
	s := startServer(t, Config{}, func(w *Stream, r *Request) {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
		cur.Add(-1)
	})
	c := rawConn(t, s)
	go io.Copy(io.Discard, c.NC) // đừng để server chặn ghi RST
	const N = 5000
	for i := 0; i < N; i++ {
		id := uint32(2*i + 1)
		c.Fr.WriteFrame(FrameHeaders, FlagEndHeaders|FlagEndStream, id, c.Enc.Encode(nil, GET("a", "/")))
		c.Fr.WriteRSTStream(id, ErrCancel)
	}
	c.Fr.Flush()
	deadline := time.Now().Add(5 * time.Second)
	for s.stats.Streams.Load()+s.stats.Refused.Load() < N && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("streams=%d refused=%d resets=%d peak_handlers=%d", s.stats.Streams.Load(), s.stats.Refused.Load(), s.stats.Resets.Load(), peak.Load())
	if peak.Load() > 100 {
		t.Fatalf("Rapid Reset: %d handler đồng thời > MAX_CONCURRENT_STREAMS 100", peak.Load())
	}
}

// G7 (b) CONTINUATION flood: 4 MiB CONTINUATION không END_HEADERS. Phòng
// tuyến ⇒ GOAWAY ENHANCE_YOUR_CALM, đệm ≤ 64 KiB + 1 frame. nodefense10 ⇒ ĐỎ.
func TestContinuationFlood(t *testing.T) {
	s := startServer(t, Config{}, echo)
	c := rawConn(t, s)
	c.Fr.WriteFrame(FrameHeaders, 0, 1, c.Enc.Encode(nil, GET("a", "/")))
	chunk := make([]byte, 1024)
	for i := 0; i < 4096; i++ {
		if err := c.Fr.WriteFrame(FrameContinuation, 0, 1, chunk); err != nil {
			break // server đã đóng: đúng
		}
	}
	c.Fr.Flush()
	time.Sleep(100 * time.Millisecond)
	got := s.stats.HeaderBlockMax.Load()
	t.Logf("header block đệm tối đa = %d byte", got)
	if got > 64<<10+16<<10 {
		t.Fatalf("CONTINUATION flood: đệm %d byte > 80 KiB", got)
	}
}

// PING ⇒ ACK cùng payload.
func TestPing(t *testing.T) {
	s := startServer(t, Config{}, echo)
	c := rawConn(t, s)
	c.Frame(FramePing, 0, 0, []byte("12345678"))
	f := expect(t, c, FramePing, 0)
	if !f.Has(FlagAck) || string(f.Payload) != "12345678" {
		t.Fatalf("%+v", f)
	}
}

// Không rò goroutine: đóng client ⇒ ServeConn thoát.
func TestServeConnExits(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		c, _ := ln.Accept()
		done <- ServeConn(c, c, Config{}, echo, nil)
	}()
	nc, _ := net.Dial("tcp", ln.Addr().String())
	c, _ := NewRawClient(nc)
	c.Headers(1, true, GET("a", "/")...)
	expect(t, c, FrameData, 1)
	nc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("ServeConn không thoát sau khi client đóng")
	}
}

// h2spec 5.1/8 (turn 2): client RST rồi gửi DATA ⇒ STREAM_CLOSED. Ngược lại,
// TA RST rồi client gửi DATA (đang bay) ⇒ bỏ qua, không RST thêm (§5.1 closed).
func TestDataAfterRST(t *testing.T) {
	hold := make(chan struct{})
	s := startServer(t, Config{}, func(w *Stream, r *Request) {
		if r.Path == "/self-reset" {
			w.Reset(ErrCancel)
			return
		}
		<-hold
	})
	defer close(hold)
	c := rawConn(t, s)
	c.Headers(1, false, POST("a", "/")...)
	time.Sleep(20 * time.Millisecond) // stream đã mở trong server
	c.RST(1, ErrCancel)
	c.Frame(FrameData, FlagEndStream, 1, []byte("test"))
	if f := expect(t, c, FrameRSTStream, 1); RSTCode(f) != ErrStreamClosed {
		t.Fatalf("client RST rồi DATA: RST %v, muốn STREAM_CLOSED", RSTCode(f))
	}

	c.Headers(3, false, POST("a", "/self-reset")...)
	if f := expect(t, c, FrameRSTStream, 3); RSTCode(f) != ErrCancel {
		t.Fatalf("server RST: %v", RSTCode(f))
	}
	c.Frame(FrameData, 0, 3, []byte("đang bay"))
	c.Frame(FramePing, 0, 0, []byte("12345678"))
	c.NC.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		f, err := c.Fr.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		if f.Type == FrameRSTStream && f.Stream == 3 {
			t.Fatalf("RST lần hai cho stream 3 (%v) — phải bỏ qua DATA đang bay", RSTCode(f))
		}
		if f.Type == FramePing && f.Has(FlagAck) {
			return // PING sau DATA đã về: DATA đã được xử lý mà không RST
		}
	}
}

// P10-3 (RFC 9113 §10.5 "tiny increments … large number of DATA frames"),
// đóng 2026-10-02 bằng SỐ ĐO, không bằng phòng tuyến: client INITIAL_WINDOW_SIZE
// 0 rồi WINDOW_UPDATE 1 byte × 20 000 cho response 1 MiB. Vì ghi đồng bộ (D3) và
// mỗi DATA lấy TOÀN BỘ credit đang dồn, số DATA frame ≤ số WINDOW_UPDATE (+ phần
// cắt theo MAX_FRAME_SIZE) — không khuếch đại; đo được 878-1 208 frame cho
// 20 000 update (flush mỗi 1/10/100 update; dưới -race 2 822-3 350). Test chốt
// bất biến tất định đó (số frame tuỳ lịch chạy, không đem ra so).
func TestTinyWindowUpdates(t *testing.T) {
	for _, every := range []int{1, 100} {
		s := startServer(t, Config{}, echo)
		c := rawConn(t, s, Setting{SettingInitialWindowSize, 0})
		c.WindowUpdate(0, 1<<30) // window connection rộng: chỉ window STREAM nhỏ giọt
		c.Headers(1, true, GET("a", "/size/1048576")...)
		var frames, got atomic.Int64
		done := make(chan struct{})
		go func() {
			defer close(done)
			c.NC.SetReadDeadline(time.Now().Add(5 * time.Second))
			for {
				f, err := c.Fr.ReadFrame()
				if err != nil {
					return
				}
				if f.Type == FrameData {
					frames.Add(1)
					if got.Add(int64(len(f.Payload))) == 20000 {
						return
					}
				}
			}
		}()
		const updates = 20000
		for i := 0; i < updates; i++ {
			c.Fr.WriteWindowUpdate(1, 1)
			if i%every == every-1 {
				c.Fr.Flush()
			}
		}
		c.Fr.Flush()
		<-done
		t.Logf("flush mỗi %d update: %d WINDOW_UPDATE ⇒ %d DATA frame, %d byte", every, updates, frames.Load(), got.Load())
		if got.Load() != updates {
			t.Fatalf("nhận %d byte, cấp %d byte credit", got.Load(), updates)
		}
		if frames.Load() > updates+1<<20/defaultMaxFrame {
			t.Fatalf("khuếch đại: %d DATA frame > %d WINDOW_UPDATE", frames.Load(), updates)
		}
	}
}
