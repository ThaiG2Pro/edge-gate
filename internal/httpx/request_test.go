package httpx

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadRequestBasic(t *testing.T) {
	in := "GET /a?b=c HTTP/1.1\r\nHost: example\r\nX-Two: 1\r\nx-two: 2\r\nAccept:  text/*  \r\n\r\nREST"
	br := rd(in)
	req, err := ReadRequest(br, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "GET" || req.Target != "/a?b=c" || req.Proto != "HTTP/1.1" {
		t.Fatalf("request line: %+v", req)
	}
	if got := req.Header.Values("X-Two"); len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Fatalf("header lặp phải giữ thứ tự: %v", got)
	}
	if req.Header.Get("Accept") != "text/*" {
		t.Fatalf("OWS phải trim: %q", req.Header.Get("Accept"))
	}
	if req.ContentLength != 0 || req.Chunked || req.Close {
		t.Fatalf("GET không body, keep-alive: %+v", req)
	}
	if b := readAll(t, req.Body); len(b) != 0 {
		t.Fatalf("body phải rỗng, có %q", b)
	}
	if req.HeaderBytes != len(in)-len("REST") {
		t.Fatalf("HeaderBytes %d, muốn %d", req.HeaderBytes, len(in)-4)
	}
	// Byte sau head phải còn nguyên cho request kế tiếp.
	rest, _ := io.ReadAll(br)
	if string(rest) != "REST" {
		t.Fatalf("phần dư %q", rest)
	}
}

func TestReadRequestBodyFraming(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		body  string
		rest  string
		cl    int64
		chunk bool
	}{
		{"CL", "POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 5\r\n\r\nhelloNEXT", "hello", "NEXT", 5, false},
		{"CL 0", "POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 0\r\n\r\nNEXT", "", "NEXT", 0, false},
		{"CL trùng giống nhau (D5)", "POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 2\r\nContent-Length: 2\r\n\r\nokNEXT", "ok", "NEXT", 2, false},
		{"CL danh sách phẩy giống nhau", "POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 2, 2\r\n\r\nokNEXT", "", "", 0, false}, // "2, 2": phần tử " 2" có space ⇒ từ chối — kiểm ở TestContentLengthStrict
		{"chunked", "POST / HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nwiki\r\n5\r\npedia\r\n0\r\n\r\nNEXT", "wikipedia", "NEXT", -1, true},
		{"chunked hoa", "POST / HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: Chunked\r\n\r\n0\r\n\r\nNEXT", "", "NEXT", -1, true},
		{"chunked ext + trailer", "POST / HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\n\r\n3;ext=1\r\nabc\r\n0\r\nX-Sum: 9\r\n\r\nNEXT", "abc", "NEXT", -1, true},
		{"CL+TE ⇒ chunked, CL xoá (D4)", "POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 100\r\nTransfer-Encoding: chunked\r\n\r\n1\r\nz\r\n0\r\n\r\nNEXT", "z", "NEXT", -1, true},
		{"1.0 không CL ⇒ 0", "POST / HTTP/1.0\r\n\r\nNEXT", "", "NEXT", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "CL danh sách phẩy giống nhau" {
				_, err := ReadRequest(rd(c.in), DefaultLimits())
				mustProto(t, err, ErrBadContentLength)
				return
			}
			br := rd(c.in)
			req, err := ReadRequest(br, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			if req.ContentLength != c.cl || req.Chunked != c.chunk {
				t.Fatalf("framing cl=%d chunked=%v, muốn %d/%v", req.ContentLength, req.Chunked, c.cl, c.chunk)
			}
			if c.chunk && req.Header.Has("Content-Length") {
				t.Fatal("D4: CL phải bị xoá khi có TE")
			}
			if b := readAll(t, req.Body); string(b) != c.body {
				t.Fatalf("body %q, muốn %q", b, c.body)
			}
			rest, _ := io.ReadAll(br)
			if string(rest) != c.rest {
				t.Fatalf("phần dư %q, muốn %q — ranh giới body sai", rest, c.rest)
			}
			if c.name == "chunked ext + trailer" && req.Trailer().Get("X-Sum") != "9" {
				t.Fatalf("trailer: %v", req.Trailer())
			}
		})
	}
}

func TestContentLengthStrict(t *testing.T) {
	for _, v := range []string{"+5", " 5", "5 ", "0x5", "-1", "", "5,6", "2, 2", "5,5", "1e3", "٣", "9223372036854775808", "99999999999999999999"} {
		in := "POST / HTTP/1.1\r\nHost: h\r\nContent-Length: " + v + "\r\n\r\n"
		_, err := ReadRequest(rd(in), DefaultLimits())
		if v == " 5" || v == "5 " || v == "" {
			// OWS quanh value đã bị trim ở tầng header ⇒ " 5" == "5" hợp lệ;
			// "" thì thành rỗng ⇒ từ chối. Ghi rõ để không tự lừa mình.
			if v == "" {
				mustProto(t, err, ErrBadContentLength)
			} else if err != nil {
				t.Fatalf("%q: OWS quanh value là hợp lệ, có %v", v, err)
			}
			continue
		}
		if !errors.Is(err, ErrBadContentLength) {
			t.Errorf("CL %q: muốn ErrBadContentLength, có %v", v, err)
		}
	}
	// Hai CL khác nhau — vector smuggling kinh điển.
	_, err := ReadRequest(rd("POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 5\r\nContent-Length: 6\r\n\r\n"), DefaultLimits())
	mustProto(t, err, ErrBadContentLength)
}

func TestTransferEncodingStrict(t *testing.T) {
	for _, v := range []string{"gzip, chunked", "chunked, chunked", "xchunked", "identity", "chunked;q=1"} {
		in := "POST / HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: " + v + "\r\n\r\n"
		_, err := ReadRequest(rd(in), DefaultLimits())
		if !errors.Is(err, ErrUnsupportedTE) {
			t.Errorf("TE %q: muốn ErrUnsupportedTE, có %v", v, err)
		}
	}
	// Hai dòng TE
	_, err := ReadRequest(rd("POST / HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\nTransfer-Encoding: chunked\r\n\r\n"), DefaultLimits())
	mustProto(t, err, ErrUnsupportedTE)
	// "Transfer-Encoding : chunked" — space trước ':' là tên header không hợp lệ (TE.TE)
	_, err = ReadRequest(rd("POST / HTTP/1.1\r\nHost: h\r\nTransfer-Encoding : chunked\r\n\r\n"), DefaultLimits())
	mustProto(t, err, &ProtoError{Status: 400})
	// D3: HTTP/1.0 + TE
	_, err = ReadRequest(rd("POST / HTTP/1.0\r\nTransfer-Encoding: chunked\r\n\r\n"), DefaultLimits())
	mustProto(t, err, &ProtoError{Status: 400})
}

func TestReadRequestRejects(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want *ProtoError
	}{
		{"bare LF request line", "GET / HTTP/1.1\nHost: h\r\n\r\n", ErrBareLF},
		{"bare LF header", "GET / HTTP/1.1\r\nHost: h\n\r\n", ErrBareLF},
		{"bare LF dòng trống", "GET / HTTP/1.1\r\nHost: h\r\n\n", ErrBareLF},
		{"CR lạc", "GET / HTTP/1.1\r\nHost: h\rX: y\r\n\r\n", &ProtoError{Status: 400}},
		{"thiếu Host 1.1", "GET / HTTP/1.1\r\n\r\n", &ProtoError{Status: 400}},
		{"hai Host", "GET / HTTP/1.1\r\nHost: a\r\nHost: b\r\n\r\n", &ProtoError{Status: 400}},
		{"ba phần tử thừa", "GET / HTTP/1.1 extra\r\nHost: h\r\n\r\n", &ProtoError{Status: 400}},
		{"hai space", "GET  / HTTP/1.1\r\nHost: h\r\n\r\n", &ProtoError{Status: 400}},
		{"method rỗng", " / HTTP/1.1\r\nHost: h\r\n\r\n", &ProtoError{Status: 400}},
		{"method không token", "GE\x01T / HTTP/1.1\r\nHost: h\r\n\r\n", &ProtoError{Status: 400}},
		{"target rỗng", "GET  HTTP/1.1\r\nHost: h\r\n\r\n", &ProtoError{Status: 400}},
		{"target có tab", "GET /\tx HTTP/1.1\r\nHost: h\r\n\r\n", &ProtoError{Status: 400}},
		{"target có byte >0x7E", "GET /\xc3\xa9 HTTP/1.1\r\nHost: h\r\n\r\n", &ProtoError{Status: 400}},
		{"version 2.0", "GET / HTTP/2.0\r\nHost: h\r\n\r\n", ErrVersion},
		{"version rác", "GET / HTTPS/1.1\r\nHost: h\r\n\r\n", &ProtoError{Status: 400}},
		{"version thường", "GET / http/1.1\r\nHost: h\r\n\r\n", &ProtoError{Status: 400}},
		{"obs-fold", "GET / HTTP/1.1\r\nHost: h\r\n continued\r\n\r\n", &ProtoError{Status: 400}},
		{"header thiếu ':'", "GET / HTTP/1.1\r\nHost h\r\n\r\n", &ProtoError{Status: 400}},
		{"tên header rỗng", "GET / HTTP/1.1\r\n: h\r\n\r\n", &ProtoError{Status: 400}},
		{"value có NUL", "GET / HTTP/1.1\r\nHost: h\x00\r\n\r\n", &ProtoError{Status: 400}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ReadRequest(rd(c.in), DefaultLimits())
			mustProto(t, err, c.want)
		})
	}
}

func TestReadRequestEOF(t *testing.T) {
	// Đóng sạch trước byte đầu: io.EOF, không phải lỗi.
	if _, err := ReadRequest(rd(""), DefaultLimits()); err != io.EOF {
		t.Fatalf("muốn io.EOF, có %v", err)
	}
	// Cắt ở mọi vị trí giữa head: ErrUnexpectedEOF, không phải EOF, không panic.
	full := "POST /x HTTP/1.1\r\nHost: h\r\nContent-Length: 3\r\n\r\n"
	for i := 1; i < len(full); i++ {
		_, err := ReadRequest(rd(full[:i]), DefaultLimits())
		if err != io.ErrUnexpectedEOF {
			t.Fatalf("cắt ở %d (%q): muốn ErrUnexpectedEOF, có %v", i, full[:i], err)
		}
	}
	// Frame nhị phân của phase 1 ném vào parser HTTP: không có CRLF nào ⇒
	// ErrUnexpectedEOF, không panic (đối xứng với TestBadMagicAndVersion phase 1).
	if _, err := ReadRequest(rd("EDGG\x01\x01\x00\x00\x00\x05hello"), DefaultLimits()); err != io.ErrUnexpectedEOF {
		t.Fatalf("frame EDGG: %v", err)
	}
	// Cắt trong body: head OK, Body trả ErrUnexpectedEOF (D6) — KHÔNG io.EOF.
	req, err := ReadRequest(rd(full+"ab"), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(req.Body)
	if string(b) != "ab" || err != io.ErrUnexpectedEOF {
		t.Fatalf("body cắt cụt: %q %v — proxy sẽ tưởng body xong", b, err)
	}
}

func TestSmallBufferSameResult(t *testing.T) {
	// Cùng input, bufio 16 byte vs 4 KiB phải cho cùng kết quả: readLine
	// đi qua nhánh ErrBufferFull nhiều lần.
	in := "POST /long/path/that/exceeds/sixteen/bytes HTTP/1.1\r\nHost: host.example.com\r\nContent-Length: 30\r\n\r\n" + strings.Repeat("z", 30) + "TAIL"
	for _, mk := range []func(string) *bufio.Reader{rd, rdSmall} {
		br := mk(in)
		req, err := ReadRequest(br, DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		if req.Target != "/long/path/that/exceeds/sixteen/bytes" || len(readAll(t, req.Body)) != 30 {
			t.Fatalf("%+v", req)
		}
		if rest, _ := io.ReadAll(br); string(rest) != "TAIL" {
			t.Fatalf("dư %q", rest)
		}
	}
}

func TestClose(t *testing.T) {
	cases := map[string]bool{
		"GET / HTTP/1.1\r\nHost: h\r\n\r\n":                         false,
		"GET / HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n":    true,
		"GET / HTTP/1.1\r\nHost: h\r\nConnection: X, Close\r\n\r\n": true,
		"GET / HTTP/1.0\r\n\r\n":                                    true,
		"GET / HTTP/1.0\r\nConnection: keep-alive\r\n\r\n":          false,
		"GET / HTTP/1.0\r\nConnection: Keep-Alive, X\r\n\r\n":       false,
		"GET / HTTP/1.1\r\nHost: h\r\nConnection: closed\r\n\r\n":   false, // token khác
	}
	for in, want := range cases {
		req, err := ReadRequest(rd(in), DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		if req.Close != want {
			t.Errorf("%q: Close=%v muốn %v", in, req.Close, want)
		}
	}
}

func TestWriteHeadRoundTrip(t *testing.T) {
	in := "POST /p HTTP/1.1\r\nHost: h\r\nB: 2\r\nA: 1\r\nA: 1b\r\nContent-Length: 3\r\n\r\nxyz"
	req, err := ReadRequest(rd(in), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if err := req.WriteHead(&sb); err != nil {
		t.Fatal(err)
	}
	want := "POST /p HTTP/1.1\r\nA: 1\r\nA: 1b\r\nB: 2\r\nContent-Length: 3\r\nHost: h\r\n\r\n"
	if sb.String() != want {
		t.Fatalf("wire:\n%q\nmuốn:\n%q", sb.String(), want)
	}
	again, err := ReadRequest(rd(sb.String()), DefaultLimits())
	if err != nil || again.ContentLength != 3 || len(again.Header) != 4 {
		t.Fatalf("parse lại: %v %+v", err, again)
	}
	// Injection: giá trị có CRLF không được ghi ra.
	req.Header.Set("X-Evil", "a\r\nContent-Length: 0")
	if err := req.WriteHead(io.Discard); !errors.Is(err, ErrHeaderInjection) {
		t.Fatalf("muốn ErrHeaderInjection, có %v", err)
	}
}
