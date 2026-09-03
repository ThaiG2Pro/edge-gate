package httpx

import (
	"bufio"
	"bytes"
	"io"
)

// Request là một request HTTP/1.x đã parse xong phần head. Body CHƯA đọc —
// nó là stream trên cùng *bufio.Reader, cắt đúng ranh giới theo framing.
type Request struct {
	Method string
	// Target là request-target nguyên văn (origin-form "/a?b", absolute-form
	// "http://h/a", authority-form cho CONNECT, "*"). Phase 2 không parse URL:
	// proxy forward nguyên văn; phase 4 mới quyết Host/authority.
	Target string
	Proto  string // "HTTP/1.1" | "HTTP/1.0"
	Header Header

	// ContentLength: số byte body theo CL; -1 khi chunked. 0 khi không có body.
	ContentLength int64
	Chunked       bool
	// Body đọc ĐÚNG số byte body rồi io.EOF. Không bao giờ đọc lố sang request
	// sau. Body bị cắt cụt ⇒ io.ErrUnexpectedEOF (D6).
	Body io.Reader
	// Close: connection phải đóng sau request này.
	Close bool
	// HeaderBytes: kích thước request line + khối header (kể cả CRLF) — để đo.
	HeaderBytes int

	cr *chunkedReader
}

// Trailer trả trailer của body chunked, chỉ có nghĩa sau khi Body trả io.EOF.
func (r *Request) Trailer() Header {
	if r.cr == nil {
		return nil
	}
	return r.cr.Trailer
}

// ReadRequest parse một request từ br theo lim.
//
// Vì sao *bufio.Reader chứ không io.Reader: HTTP/1.1 là giao thức theo dòng,
// không có prefix độ dài ⇒ phải nhìn trước để tìm CRLF ⇒ cần buffer. Hệ quả
// caller phải chấp nhận: byte của request kế tiếp có thể đã nằm trong buffer
// của br. Muốn giữ keep-alive thì mọi lần đọc trên connection PHẢI đi qua
// cùng một br, và body của request N phải đọc hết trước khi ReadRequest lần N+1.
//
// Lỗi: io.EOF nếu connection đóng sạch trước byte đầu (không phải lỗi);
// io.ErrUnexpectedEOF nếu đứt giữa head; *ProtoError nếu sai giao thức.
// Deadline KHÔNG phải việc ở đây (I3 thuộc tầng connection).
func ReadRequest(br *bufio.Reader, lim Limits) (*Request, error) {
	line, err := readLine(br, lim.MaxLineBytes)
	if err != nil {
		return nil, err // io.EOF nguyên vẹn: "client đóng lịch sự"
	}
	req := &Request{HeaderBytes: len(line) + 2}

	// request-line = method SP request-target SP HTTP-version. Đúng hai SP.
	sp1 := bytes.IndexByte(line, ' ')
	if sp1 <= 0 {
		return nil, badRequest("request line không hợp lệ")
	}
	sp2 := bytes.IndexByte(line[sp1+1:], ' ')
	if sp2 < 0 {
		return nil, badRequest("request line thiếu phiên bản")
	}
	sp2 += sp1 + 1
	method, target, version := line[:sp1], line[sp1+1:sp2], line[sp2+1:]
	if !isToken(method) {
		return nil, badRequest("method không hợp lệ: %q", method)
	}
	if len(target) == 0 || !isVCHAR(target) {
		return nil, badRequest("request-target không hợp lệ")
	}
	if req.Proto, err = parseVersion(version); err != nil {
		return nil, err
	}
	req.Method, req.Target = string(method), string(target)

	h, n, err := readHeaders(br, lim)
	req.HeaderBytes += n
	if err != nil {
		return nil, err
	}
	req.Header = h

	// RFC 9112 §3.2: HTTP/1.1 request PHẢI có đúng một Host. Nhiều Host ⇒ 400.
	// (Giá trị Host có khớp authority không là việc phase 4, P-arch-1.)
	if hosts := h.Values("Host"); len(hosts) > 1 || (req.Proto == "HTTP/1.1" && len(hosts) == 0) {
		return nil, badRequest("Host: cần đúng một dòng, có %d", len(hosts))
	}

	req.ContentLength, req.Chunked, err = framing(req.Proto, h, false, false)
	if err != nil {
		return nil, err
	}
	req.Body, req.cr = bodyReader(br, lim, req.ContentLength, req.Chunked)
	req.Close = shouldClose(req.Proto, h)
	return req, nil
}
