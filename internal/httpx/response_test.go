package httpx

import (
	"io"
	"strings"
	"testing"
)

func TestReadResponseFraming(t *testing.T) {
	cases := []struct {
		name   string
		method string
		in     string
		status int
		body   string
		rest   string
		cl     int64
		chunk  bool
		close  bool
	}{
		{"200 CL", "GET", "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhelloNEXT", 200, "hello", "NEXT", 5, false, false},
		{"200 không reason", "GET", "HTTP/1.1 200\r\nContent-Length: 0\r\n\r\nNEXT", 200, "", "NEXT", 0, false, false},
		{"204 kèm CL:5 — body 0, 5 byte còn trong buffer (G7)", "GET", "HTTP/1.1 204 No Content\r\nContent-Length: 5\r\n\r\nhello", 204, "", "hello", 0, false, false},
		{"304 kèm TE — không body", "GET", "HTTP/1.1 304 Not Modified\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n", 304, "", "5\r\nhello\r\n0\r\n\r\n", 0, false, false},
		{"HEAD kèm CL — không body", "HEAD", "HTTP/1.1 200 OK\r\nContent-Length: 1234\r\n\r\nNEXT", 200, "", "NEXT", 0, false, false},
		{"100 Continue", "POST", "HTTP/1.1 100 Continue\r\n\r\nHTTP/1.1 200 OK\r\n", 100, "", "HTTP/1.1 200 OK\r\n", 0, false, false},
		{"chunked + trailer", "GET", "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nwiki\r\n5\r\npedia\r\n0\r\nX-T: 1\r\n\r\nNEXT", 200, "wikipedia", "NEXT", -1, true, false},
		{"không CL không TE ⇒ tới EOF, Close (D7)", "GET", "HTTP/1.1 200 OK\r\nX: y\r\n\r\nall the rest is body", 200, "all the rest is body", "", -1, false, true},
		{"1.0 mặc định close", "GET", "HTTP/1.0 200 OK\r\nContent-Length: 2\r\n\r\nokNEXT", 200, "ok", "NEXT", 2, false, true},
		{"1.1 Connection close", "GET", "HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Length: 2\r\n\r\nokNEXT", 200, "ok", "NEXT", 2, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			br := rd(c.in)
			resp, err := ReadResponse(br, DefaultLimits(), c.method)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Status != c.status || resp.ContentLength != c.cl || resp.Chunked != c.chunk || resp.Close != c.close {
				t.Fatalf("status=%d cl=%d chunked=%v close=%v; muốn %d/%d/%v/%v", resp.Status, resp.ContentLength, resp.Chunked, resp.Close, c.status, c.cl, c.chunk, c.close)
			}
			if b := readAll(t, resp.Body); string(b) != c.body {
				t.Fatalf("body %q muốn %q", b, c.body)
			}
			rest, _ := io.ReadAll(br)
			if string(rest) != c.rest {
				t.Fatalf("dư %q muốn %q", rest, c.rest)
			}
			if c.chunk && resp.Trailer().Get("X-T") != "1" {
				t.Fatalf("trailer %v", resp.Trailer())
			}
		})
	}
}

func TestReadResponseRejects(t *testing.T) {
	cases := map[string]*ProtoError{
		"HTTP/1.1 20 OK\r\n\r\n":        {Status: 400},
		"HTTP/1.1 abc OK\r\n\r\n":       {Status: 400},
		"HTTP/1.1 099 OK\r\n\r\n":       {Status: 400},
		"HTTP/1.1 200OK\r\n\r\n":        {Status: 400},
		"HTTP/2.0 200 OK\r\n\r\n":       ErrVersion,
		"HTTP/1.1 200 OK\nX: y\r\n\r\n": ErrBareLF,
		"HTTP/1.1 200 OK\r\nContent-Length: 5\r\nContent-Length: 6\r\n\r\n": ErrBadContentLength,
		"HTTP/1.1 200 OK\r\nTransfer-Encoding: gzip\r\n\r\n":                ErrUnsupportedTE,
	}
	for in, want := range cases {
		_, err := ReadResponse(rd(in), DefaultLimits(), "GET")
		mustProto(t, err, want)
	}
	// Upstream đóng trước byte đầu: với response là LỖI (khác ReadRequest).
	if _, err := ReadResponse(rd(""), DefaultLimits(), "GET"); err != io.ErrUnexpectedEOF {
		t.Fatalf("muốn ErrUnexpectedEOF, có %v", err)
	}
}

func TestResponseWriteHead(t *testing.T) {
	resp := &Response{Proto: "HTTP/1.1", Status: 502, Reason: "Bad Gateway", Header: Header{}}
	resp.Header.Set("Content-Length", "0")
	resp.Header.Add("Via", "1.1 edgegate")
	var sb strings.Builder
	if err := resp.WriteHead(&sb); err != nil {
		t.Fatal(err)
	}
	want := "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nVia: 1.1 edgegate\r\n\r\n"
	if sb.String() != want {
		t.Fatalf("%q", sb.String())
	}
	again, err := ReadResponse(rd(sb.String()), DefaultLimits(), "GET")
	if err != nil || again.Status != 502 || again.ContentLength != 0 {
		t.Fatalf("%v %+v", err, again)
	}
}
