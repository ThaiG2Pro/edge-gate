package httpx

import (
	"bufio"
	"bytes"
	"io"
)

// Response là một response HTTP/1.x đã parse xong phần head (từ upstream).
// Nửa "người ta hay bỏ" của phase 2: không có nó thì phase 3 io.Copy treo.
type Response struct {
	Proto  string
	Status int
	Reason string
	Header Header

	// ContentLength: theo CL; -1 khi chunked HOẶC khi đọc tới EOF (D7).
	ContentLength int64
	Chunked       bool
	// Body: stream đúng ranh giới. Với ContentLength == -1 && !Chunked, Body là
	// chính br — đọc tới khi upstream đóng — và Close == true.
	Body io.Reader
	// Close: connection upstream không dùng lại được sau response này.
	Close       bool
	HeaderBytes int

	cr *chunkedReader
}

func (r *Response) Trailer() Header {
	if r.cr == nil {
		return nil
	}
	return r.cr.Trailer
}

// NoBody: RFC 9112 §6.3 bước 1-2 — HEAD, 1xx, 204, 304 không có body dù header nói gì.
func NoBody(method string, status int) bool {
	return method == "HEAD" || status/100 == 1 || status == 204 || status == 304
}

// ReadResponse parse response từ br. method là method của request tương ứng
// (HEAD ⇒ không body). Cùng quy ước bufio/deadline như ReadRequest.
func ReadResponse(br *bufio.Reader, lim Limits, method string) (*Response, error) {
	line, err := readLine(br, lim.MaxLineBytes)
	if err != nil {
		if err == io.EOF {
			// upstream đóng trước khi gửi byte nào: với response đây LÀ lỗi
			// (client đang chờ), khác ReadRequest.
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	resp := &Response{HeaderBytes: len(line) + 2}

	// status-line = HTTP-version SP status-code SP [ reason-phrase ]
	// Chấp nhận "HTTP/1.1 200" (thiếu SP cuối) vì upstream thật có gửi vậy;
	// đây là khoan dung KHÔNG ảnh hưởng ranh giới body nên vô hại.
	sp1 := bytes.IndexByte(line, ' ')
	if sp1 < 0 {
		return nil, badRequest("status line không hợp lệ")
	}
	if resp.Proto, err = parseVersion(line[:sp1]); err != nil {
		return nil, err
	}
	rest := line[sp1+1:]
	if len(rest) < 3 {
		return nil, badRequest("status code thiếu")
	}
	code := rest[:3]
	for _, c := range code {
		if c < '0' || c > '9' {
			return nil, badRequest("status code không phải số: %q", code)
		}
	}
	resp.Status = int(code[0]-'0')*100 + int(code[1]-'0')*10 + int(code[2]-'0')
	if resp.Status < 100 {
		return nil, badRequest("status code %d ngoài khoảng", resp.Status)
	}
	switch {
	case len(rest) == 3:
	case rest[3] == ' ':
		if !isFieldValue(rest[4:]) {
			return nil, badRequest("reason chứa CTL")
		}
		resp.Reason = string(rest[4:])
	default:
		return nil, badRequest("status line: sau mã phải là SP")
	}

	h, n, err := readHeaders(br, lim)
	resp.HeaderBytes += n
	if err != nil {
		return nil, err
	}
	resp.Header = h

	resp.ContentLength, resp.Chunked, err = framing(resp.Proto, h, true, NoBody(method, resp.Status))
	if err != nil {
		return nil, err
	}
	resp.Body, resp.cr = bodyReader(br, lim, resp.ContentLength, resp.Chunked)
	resp.Close = shouldClose(resp.Proto, h) || (resp.ContentLength < 0 && !resp.Chunked)
	return resp, nil
}
