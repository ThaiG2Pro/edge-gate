package httpx

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// Request là một request HTTP/1.x đã parse xong phần head. Body CHƯA đọc —
// nó là stream trên cùng *bufio.Reader, cắt đúng ranh giới theo framing.
type Request struct {
	Method string
	// Target là request-target SAU chuẩn hoá (phase 4 D5): origin-form "/a?b",
	// hoặc "*" (chỉ OPTIONS). Absolute-form "http://h/a" đã được viết lại
	// thành "/a" và Host := "h"; authority-form (CONNECT) bị từ chối 501.
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
		return nil, badRequest("method không hợp lệ: %q", clip(method))
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
	hosts := h.Values("Host")
	if len(hosts) > 1 || (req.Proto == "HTTP/1.1" && len(hosts) == 0) {
		return nil, badRequest("Host: cần đúng một dòng, có %d", len(hosts))
	}
	// Phase 4 D4/D5/D6: Host phải đúng cú pháp uri-host[:port]; request-target
	// phải về được origin-form và không mâu thuẫn với Host; CONNECT ⇒ 501.
	if err := normalizeTarget(req, h, hosts); err != nil {
		return nil, err
	}
	// Phase 4 D7: Connection: không được liệt kê Host / Content-Length.
	if err := checkConnectionTokens(h); err != nil {
		return nil, err
	}

	req.ContentLength, req.Chunked, err = framing(req.Proto, h, false, false)
	if err != nil {
		return nil, err
	}
	req.Body, req.cr = bodyReader(br, lim, req.ContentLength, req.Chunked)
	req.Close = shouldClose(req.Proto, h)
	return req, nil
}

// normalizeTarget (RFC 9112 §3.2) đưa request-target về origin-form và làm
// cho "request này gửi tới ai" chỉ còn MỘT cách hiểu:
//
//   - CONNECT ⇒ 501 (D6): không tunnel.
//   - "*" ⇒ chỉ hợp lệ với OPTIONS (asterisk-form).
//   - "/..." ⇒ origin-form, giữ nguyên.
//   - "http://authority/path?q" ⇒ absolute-form: §3.2.2 nói authority trong
//     target THẮNG Host. Ta viết lại target = "/path?q", Host := authority;
//     nếu client gửi Host khác authority ⇒ 400 thay vì chọn một (D5). Scheme
//     khác http ⇒ 400 (không có TLS ở đây; "https://" tới cổng plaintext là mơ hồ).
//   - Dạng khác (authority-form không CONNECT, "//x", "foo") ⇒ 400.
//
// Sau đó Host (dù lấy từ đâu) phải đúng cú pháp uri-host[":" port].
//
// hosts là h.Values("Host") caller đã tra — tránh tra map (kèm
// CanonicalMIMEHeaderKey) thêm ba lần trên đường nóng (profile turn 2).
func normalizeTarget(req *Request, h Header, hosts []string) error {
	hasHost, host := len(hosts) > 0, ""
	if hasHost {
		host = hosts[0]
	}
	if req.Method == "CONNECT" {
		return ErrConnectNotSupported
	}
	t := req.Target
	switch {
	case t == "*":
		if req.Method != "OPTIONS" {
			return ErrBadTarget
		}
	case t[0] == '/':
		if len(t) > 1 && t[1] == '/' {
			return ErrBadTarget // "//host/x" là network-path reference, không phải origin-form
		}
	default:
		i := strings.Index(t, "://")
		if i < 0 {
			return ErrBadTarget
		}
		if !strings.EqualFold(t[:i], "http") {
			return ErrBadTarget
		}
		rest := t[i+3:]
		slash := strings.IndexAny(rest, "/?")
		authority, path := rest, "/"
		if slash >= 0 {
			authority, path = rest[:slash], rest[slash:]
			if path[0] == '?' {
				path = "/" + path
			}
		}
		if authority == "" || !validHost(authority) {
			return ErrBadTarget
		}
		if hasHost && !strings.EqualFold(host, authority) {
			return ErrBadHost // hai nguồn, hai địa chỉ ⇒ không đoán
		}
		h.Set("Host", authority)
		hasHost, host = true, authority
		req.Target = path
	}
	if hasHost && !validHost(host) {
		return ErrBadHost
	}
	return nil
}

// validHost: cùng bộ ký tự với httpguts.ValidHostHeader (Go stdlib) —
// uri-host [ ":" port ] theo RFC 3986: unreserved / sub-delims / pct / ":" /
// "[" "]" cho IPv6. Không SP, không "/", không "@", không CTL. Host rỗng
// ("Host:" trống) hợp lệ về cú pháp — RFC 9112 §3.2 cho phép khi không có
// authority — nên không từ chối ở đây.
func validHost(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '!', c == '$', c == '%', c == '&', c == '\'', c == '(', c == ')',
			c == '*', c == '+', c == ',', c == '-', c == '.', c == ':', c == ';',
			c == '=', c == '[', c == ']', c == '_', c == '~':
		default:
			return false
		}
	}
	return true
}
