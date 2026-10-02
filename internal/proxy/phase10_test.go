package proxy

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thaivro/edgegate/internal/fixture"
	"github.com/thaivro/edgegate/internal/h2"
	"github.com/thaivro/edgegate/internal/h2/hpack"
	"github.com/thaivro/edgegate/internal/httpx"
)

func h2cClient() *http.Client {
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: &http.Transport{Protocols: &p}, Timeout: 10 * time.Second}
}

func h2on(c *Config) { c.H2C = true }

// TestH2CProxyInterop (D7-D9): client net/http h2c → EdgeGate → fixture h1.
// Cùng port vẫn phục vụ h1, kể cả request h1 NGẮN hơn preface bắt đầu bằng
// 'P' (isH2Preface không được chặn chờ đủ 24 byte).
func TestH2CProxyInterop(t *testing.T) {
	s, addr := startProxyS(t, startFixture(t), h2on)
	cl := h2cClient()
	base := "http://" + addr

	get := func(path string) (*http.Response, []byte) {
		t.Helper()
		resp, err := cl.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.ProtoMajor != 2 {
			t.Fatalf("%s: proto %s", path, resp.Proto)
		}
		return resp, b
	}
	if _, b := get("/hello"); string(b) != "hello from upstream\n" {
		t.Fatalf("/hello %q", b)
	}
	if _, b := get("/chunked?n=3&ms=1"); len(b) == 0 {
		t.Fatal("/chunked rỗng")
	}
	if resp, b := get("/nobody"); resp.StatusCode != 204 || len(b) != 0 {
		t.Fatalf("/nobody %d %q", resp.StatusCode, b)
	}
	_, b := get("/headers")
	hs := string(b)
	if !strings.Contains(hs, "X-Forwarded-For: 127.0.0.1") || !strings.Contains(hs, "Host: "+addr) {
		t.Fatalf("/headers thiếu XFF/Host:\n%s", hs)
	}

	body := make([]byte, 1<<20)
	rand.Read(body)
	resp, err := cl.Post(base+"/echo", "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("/echo 1 MiB: %d byte, khớp=%v", len(got), bytes.Equal(got, body))
	}

	var wg sync.WaitGroup
	var bad atomic.Int64
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := cl.Get(base + "/large?n=20000")
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
		t.Fatalf("%d/50 stream lỗi", bad.Load())
	}

	// h1 trên cùng port: "PUT /x HTTP/1.0\r\n\r\n" = 19 byte < 24, bắt đầu bằng 'P'.
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second)) // < HeaderTimeout 2 s: chặn chờ preface sẽ lộ
	io.WriteString(c, "PUT /x HTTP/1.0\r\n\r\n")
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "HTTP/1.1 ") {
		t.Fatalf("h1 ngắn trên port h2c: %q %v", line, err)
	}
	st := s.H2Stats()
	t.Logf("h2 conns=%d streams=%d pool=%+v", s.h2conns.Load(), st.Streams.Load(), s.PoolStats())
	if s.PoolStats().Reuses == 0 {
		t.Fatal("stream h2 không dùng lại connection upstream")
	}
}

// recUpstream: upstream h1 thô ghi lại target của MỌI request nó parse được
// trên mỗi connection — thấy "/smuggled" = ranh giới proxy và upstream lệch.
type recUpstream struct {
	ln      net.Listener
	mu      sync.Mutex
	targets []string
}

func startRecUpstream(t *testing.T) *recUpstream {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := &recUpstream{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					c.SetReadDeadline(time.Now().Add(2 * time.Second))
					req, err := httpx.ReadRequest(br, httpx.DefaultLimits())
					if err != nil {
						return
					}
					u.mu.Lock()
					u.targets = append(u.targets, req.Method+" "+req.Target)
					u.mu.Unlock()
					io.Copy(io.Discard, req.Body)
					io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
				}
			}()
		}
	}()
	return u
}

func (u *recUpstream) seen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.targets...)
}

// TestH2Smuggle (G6, D6 c): downgrade h2 → h1. Mỗi ca gửi một request mà nếu
// proxy dịch ngây thơ thì upstream đọc thấy request THỨ HAI "/smuggled".
// -tags nodefense10 ⇒ ca CL và CRLF PHẢI ĐỎ.
func TestH2Smuggle(t *testing.T) {
	cases := []struct {
		name string
		send func(c *h2.RawClient)
	}{
		{"H2.CL: content-length 0 + DATA là request", func(c *h2.RawClient) {
			c.Headers(1, false, h2.POST("a", "/", hpack.HeaderField{Name: "content-length", Value: "0"})...)
			c.Frame(h2.FrameData, h2.FlagEndStream, 1, []byte("GET /smuggled HTTP/1.1\r\nHost: a\r\n\r\n"))
		}},
		// CRLF: lớp hai là phase 4 — httpx.appendWire từ chối CR/LF/NUL
		// (ErrHeaderInjection) nên KHÔNG đỏ dưới nodefense10 một mình.
		{"H2.CRLF: CRLF trong value (lớp phase 4 appendWire)", func(c *h2.RawClient) {
			c.Headers(1, true, h2.GET("a", "/", hpack.HeaderField{Name: "x", Value: "a\r\n\r\nGET /smuggled HTTP/1.1\r\nHost: a"})...)
		}},
		{"H2.TE: transfer-encoding chunked (lớp phase 4 StripHopByHop)", func(c *h2.RawClient) {
			c.Headers(1, false, h2.POST("a", "/", hpack.HeaderField{Name: "transfer-encoding", Value: "chunked"})...)
			c.Frame(h2.FrameData, h2.FlagEndStream, 1, []byte("0\r\n\r\nGET /smuggled HTTP/1.1\r\nHost: a\r\n\r\n"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := startRecUpstream(t)
			_, addr := startProxyS(t, u.ln.Addr().String(), h2on)
			nc, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			defer nc.Close()
			c, err := h2.NewRawClient(nc)
			if err != nil {
				t.Fatal(err)
			}
			go io.Copy(io.Discard, nc)
			tc.send(c)
			time.Sleep(300 * time.Millisecond)
			seen := u.seen()
			t.Logf("upstream thấy: %q", seen)
			for _, s := range seen {
				if strings.Contains(s, "/smuggled") {
					t.Fatalf("SMUGGLED: upstream thấy %q", seen)
				}
			}
		})
	}
}

// TestH2RapidResetProxy (G7 qua proxy, D6 a′): 5 000 cặp HEADERS+RST tới
// upstream giữ mỗi request 200 ms. Đếm request upstream NHẬN được. Phòng tuyến
// ⇒ GOAWAY sau 2×100 RST ⇒ upstream thấy vài trăm. nodefense10 ⇒ ĐỎ.
func TestH2RapidResetProxy(t *testing.T) {
	var got atomic.Int64
	us := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Add(1)
		fixture.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(us.Close)
	s, addr := startProxyS(t, us.Listener.Addr().String(), h2on)
	nc, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	c, err := h2.NewRawClient(nc)
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, nc)
	// Từng lô 100 HEADERS (= MAX_CONCURRENT_STREAMS), chờ 20 ms cho proxy kịp
	// ghi request sang upstream, rồi RST cả lô. Bản đầu gửi HEADERS+RST liền
	// nhau: RST tới trước khi proxy đụng upstream (SetCancel trả false) ⇒
	// upstream thấy 0 request ở CẢ HAI build — test không đo được gì. Chờ 5 ms:
	// nodefense10 thấy 502-2353 (một lần < 400 ⇒ phản chứng xanh nhầm).
	const batches, per = 50, 100
	block := func() []byte { return c.Enc.Encode(nil, h2.GET("a", "/slow?ms=200")) }
	id := uint32(1)
	for b := 0; b < batches; b++ {
		first := id
		for i := 0; i < per; i++ {
			c.Fr.WriteFrame(h2.FrameHeaders, h2.FlagEndHeaders|h2.FlagEndStream, id, block())
			id += 2
		}
		if c.Fr.Flush() != nil {
			break // GOAWAY + đóng: đúng
		}
		time.Sleep(20 * time.Millisecond)
		for x := first; x < id; x += 2 {
			c.Fr.WriteRSTStream(x, h2.ErrCancel)
		}
		if c.Fr.Flush() != nil {
			break
		}
	}
	time.Sleep(500 * time.Millisecond)
	st := s.H2Stats()
	t.Logf("upstream nhận %d request; h2 streams=%d refused=%d resets=%d", got.Load(), st.Streams.Load(), st.Refused.Load(), st.Resets.Load())
	if got.Load() > 400 {
		t.Fatalf("Rapid Reset qua proxy: upstream nhận %d request > 400", got.Load())
	}
}

// TestH2Drain (trả nợ turn 3): Drain với một connection h2 đang chạy stream
// /slow 300 ms VÀ một connection h2 rỗi. Đúng: stream đang chạy xong (200),
// connection rỗi được đóng ngay bằng GOAWAY NO_ERROR, Drain trả 0 connection bị
// ép, không chờ hết timeout. Trước turn 3: connection h2 không bao giờ "idle"
// với Drain ⇒ chờ timeout rồi đóng cưỡng bức.
func TestH2Drain(t *testing.T) {
	s, addr := startProxyS(t, startFixture(t), func(c *Config) {
		h2on(c)
		c.Limits.IdleTimeout = 10 * time.Second // connection rỗi không tự đóng trong lúc test
	})
	busy := h2cClient()
	idle := h2cClient()
	if resp, err := idle.Get("http://" + addr + "/hello"); err != nil {
		t.Fatal(err)
	} else {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	res := make(chan string, 1)
	go func() {
		resp, err := busy.Get("http://" + addr + "/slow?ms=300")
		if err != nil {
			res <- err.Error()
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		res <- resp.Status + " " + string(b)
	}()
	time.Sleep(100 * time.Millisecond)
	t0 := time.Now()
	forced := s.Drain(3 * time.Second)
	el := time.Since(t0)
	got := <-res
	t.Logf("Drain %v, forced=%d, stream đang chạy: %q", el.Round(time.Millisecond), forced, got)
	if !strings.HasPrefix(got, "200") {
		t.Fatalf("stream đang chạy bị cắt khi drain: %q", got)
	}
	if forced != 0 || el > 2*time.Second {
		t.Fatalf("Drain ép %d connection sau %v (muốn 0, < 2 s)", forced, el)
	}
}

// TestH2RateLimit (P10-2): rate limit phase 7 phải áp cho stream h2 như h1 —
// không thì client chỉ cần nói h2c trên CÙNG port là thoát token bucket.
// Burst 2, rate 0.1/s: 6 GET liên tiếp trên một connection h2 ⇒ 2 × 200, 4 × 429
// (có Retry-After); h1 cùng IP sau đó cũng 429 (cùng bucket, không phải bucket riêng).
func TestH2RateLimit(t *testing.T) {
	s, addr := startProxyS(t, startFixture(t), func(c *Config) {
		h2on(c)
		c.RateLimit = RateLimitConfig{Rate: 0.1, Burst: 2}
	})
	cl := h2cClient()
	codes := map[int]int{}
	retryAfter := 0
	for i := 0; i < 6; i++ {
		resp, err := cl.Get("http://" + addr + "/hello")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		codes[resp.StatusCode]++
		if resp.StatusCode == 429 && resp.Header.Get("Retry-After") != "" {
			retryAfter++
		}
	}
	h1, err := http.Get("http://" + addr + "/hello") // transport mặc định: h1
	if err != nil {
		t.Fatal(err)
	}
	h1.Body.Close()
	t.Logf("h2: %v (Retry-After trên %d), h1 sau đó: %d %s, stats %+v", codes, retryAfter, h1.StatusCode, h1.Proto, s.ResilienceStats())
	if codes[200] != 2 || codes[429] != 4 || retryAfter != 4 {
		t.Fatalf("h2 vòng qua rate limit: %v", codes)
	}
	if h1.StatusCode != 429 {
		t.Fatalf("h1 sau 6 request h2 cùng IP: %d, muốn 429 (bucket chung)", h1.StatusCode)
	}
}

// TestH2Shed (P10-2): trần inflight phase 7 áp cho stream h2. MaxInflight 1,
// không hàng đợi: stream thứ hai trong lúc /slow chạy ⇒ 503.
func TestH2Shed(t *testing.T) {
	s, addr := startProxyS(t, startFixture(t), func(c *Config) {
		h2on(c)
		c.Shed = ShedConfig{MaxInflight: 1, MaxQueue: 0, QueueTimeout: 10 * time.Millisecond}
	})
	cl := h2cClient()
	slow := make(chan int, 1)
	go func() {
		resp, err := cl.Get("http://" + addr + "/slow?ms=300")
		if err != nil {
			slow <- -1
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		slow <- resp.StatusCode
	}()
	time.Sleep(100 * time.Millisecond)
	resp, err := cl.Get("http://" + addr + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	first := <-slow
	t.Logf("/slow %d, /hello trong lúc đó %d, stats %+v", first, resp.StatusCode, s.ResilienceStats())
	if first != 200 || resp.StatusCode != 503 {
		t.Fatalf("shed không áp cho h2: /slow %d, /hello %d (muốn 200, 503)", first, resp.StatusCode)
	}
	if st := s.ResilienceStats(); st.Inflight != 0 {
		t.Fatalf("slot không trả (I7): inflight %d", st.Inflight)
	}
}
