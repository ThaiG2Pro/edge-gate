package httpx

import (
	"bufio"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// TestHeaderBomb là G2: 100 000 dòng "a: b" (600 KB). Trần nào nổ trước, và
// parser đọc vào bao nhiêu byte trước khi từ chối?
func TestHeaderBomb(t *testing.T) {
	bomb := "GET / HTTP/1.1\r\nHost: h\r\n" + strings.Repeat("a: b\r\n", 100_000) + "\r\n"
	src := &countingReader{r: strings.NewReader(bomb)}
	_, err := ReadRequest(bufio.NewReader(src), DefaultLimits())
	t.Logf("bomb %d byte: err=%v, tiêu thụ %d byte", len(bomb), err, src.n)
	if !errors.Is(err, ErrHeaderTooLarge) {
		t.Fatalf("muốn ErrHeaderTooLarge, có %v", err)
	}
	if src.n > 8<<10 {
		t.Fatalf("parser đọc vào %d byte trước khi từ chối — không dừng sớm", src.n)
	}

	// Biến thể "ít dòng, dòng to": 60 dòng × 2 KB = 120 KB > MaxHeaderBytes,
	// nhưng 60 < MaxHeaderCount. Trần byte phải nổ, và trước khi đọc hết.
	big := "GET / HTTP/1.1\r\nHost: h\r\n" + strings.Repeat("a: "+strings.Repeat("v", 2000)+"\r\n", 60) + "\r\n"
	src = &countingReader{r: strings.NewReader(big)}
	_, err = ReadRequest(bufio.NewReader(src), DefaultLimits())
	t.Logf("big %d byte: err=%v, tiêu thụ %d byte", len(big), err, src.n)
	if !errors.Is(err, ErrHeaderTooLarge) || src.n > 72<<10 {
		t.Fatalf("err=%v tiêu thụ=%d", err, src.n)
	}

	// Một dòng dài: 9 KB > MaxLineBytes 8 KB. Không gom cả dòng rồi mới kiểm.
	long := "GET / HTTP/1.1\r\nHost: h\r\nX: " + strings.Repeat("v", 9000) + "\r\n\r\n"
	_, err = ReadRequest(rd(long), DefaultLimits())
	if !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("muốn ErrLineTooLong, có %v", err)
	}
	// Request line dài cũng vậy (target 1 MB).
	_, err = ReadRequest(rd("GET /"+strings.Repeat("a", 1<<20)+" HTTP/1.1\r\n"), DefaultLimits())
	if !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("muốn ErrLineTooLong, có %v", err)
	}
}

// TestKeepAlivePipelined là G5: ba request trong MỘT Write. Parser trả đúng
// ba, body không lẫn, sau cái cuối không còn byte nào trong buffer.
func TestKeepAlivePipelined(t *testing.T) {
	client, server := pair(t)
	wire := "POST /1 HTTP/1.1\r\nHost: h\r\nContent-Length: 5\r\n\r\nhello" +
		"GET /2 HTTP/1.1\r\nHost: h\r\n\r\n" +
		"POST /3 HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nwiki\r\n5\r\npedia\r\n0\r\n\r\n"
	if _, err := client.Write([]byte(wire)); err != nil {
		t.Fatal(err)
	}
	server.SetReadDeadline(time.Now().Add(2 * time.Second))
	br := bufio.NewReader(server)
	var targets, bodies []string
	for i := 0; i < 3; i++ {
		req, err := ReadRequest(br, DefaultLimits())
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		targets = append(targets, req.Target)
		bodies = append(bodies, string(readAll(t, req.Body)))
	}
	if strings.Join(targets, ",") != "/1,/2,/3" || strings.Join(bodies, "|") != "hello||wikipedia" {
		t.Fatalf("%v %v", targets, bodies)
	}
	if br.Buffered() != 0 {
		t.Fatalf("còn %d byte trong buffer sau request cuối", br.Buffered())
	}
}

// TestBodyNotDrainedBreaksNextRequest: bẫy #3 phase 3, cho thấy bằng test.
// Quên đọc body request 1 ⇒ "hello" dính vào request line của request 2.
//
// Dự đoán ban đầu (turn 1): parser trả 400. SAI — "helloGET" là token hợp lệ
// theo RFC 9110 §5.6.2, nên parser NHẬN một request có method "helloGET" mà
// không kêu gì. Bẫy này im lặng hơn tưởng: không có lỗi để log, chỉ có
// upstream nhận method lạ (hoặc, với body khác, một request hoàn toàn khác).
func TestBodyNotDrainedBreaksNextRequest(t *testing.T) {
	br := rd("POST /1 HTTP/1.1\r\nHost: h\r\nContent-Length: 5\r\n\r\nhelloGET /2 HTTP/1.1\r\nHost: h\r\n\r\n")
	if _, err := ReadRequest(br, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	// KHÔNG đọc Body.
	req2, err := ReadRequest(br, DefaultLimits())
	if err != nil {
		t.Fatalf("dự đoán cũ là 400; thực tế còn tệ hơn — nhưng giờ lại có lỗi %v?", err)
	}
	if req2.Method != "helloGET" || req2.Target != "/2" {
		t.Fatalf("%+v", req2)
	}
	t.Logf("quên drain body ⇒ request sau parse THÀNH CÔNG với method %q — không lỗi, không log", req2.Method)
}

// TestSlowlorisNeedsConnectionDeadline là G6 và I3: parser không biết thời
// gian; tầng connection phải đặt deadline. Client 1 byte/20ms; server: chờ
// byte đầu (IdleTimeout), rồi HeaderTimeout 200ms cho cả head.
func TestSlowlorisNeedsConnectionDeadline(t *testing.T) {
	client, server := pair(t)
	lim := DefaultLimits()
	lim.HeaderTimeout = 200 * time.Millisecond
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		head := "GET / HTTP/1.1\r\nHost: example.com\r\nUser-Agent: slow\r\n"
		for i := 0; i < len(head); i++ {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			if _, err := client.Write([]byte{head[i]}); err != nil {
				return
			}
		}
	}()
	src := &countingReader{r: server}
	br := bufio.NewReader(src)
	server.SetReadDeadline(time.Now().Add(lim.IdleTimeout))
	if _, err := br.Peek(1); err != nil { // byte đầu tới ⇒ đồng hồ header bắt đầu
		t.Fatal(err)
	}
	t0 := time.Now()
	server.SetReadDeadline(t0.Add(lim.HeaderTimeout))
	_, err := ReadRequest(br, lim)
	el := time.Since(t0)
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("muốn timeout, có %v sau %v", err, el)
	}
	t.Logf("slowloris: deadline cắt sau %v, nhận %d byte, err=%v", el.Round(time.Millisecond), src.n, err)
	if el < lim.HeaderTimeout || el > 3*lim.HeaderTimeout {
		t.Fatalf("thời gian %v ngoài [200ms, 600ms]", el)
	}
	if src.n < 5 || src.n > 15 {
		t.Fatalf("nhận %d byte, dự đoán ≈ 10", src.n)
	}
	_ = io.EOF
}
