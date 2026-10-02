package httpx

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
)

// readLine đọc một dòng kết thúc bằng CRLF và trả về nội dung KHÔNG gồm CRLF.
// Slice trả về chỉ hợp lệ tới lần đọc kế tiếp trên br (nó trỏ vào buffer của
// bufio khi dòng nằm gọn trong một lần ReadSlice). Caller phải copy nếu giữ.
//
// Chính sách (I2 cho "dòng"):
//   - Nội dung dòng > max byte ⇒ ErrLineTooLong. Kiểm TRONG LÚC gom, không gom
//     xong rồi kiểm: bộ nhớ giữ tối đa max + kích thước buffer của br.
//   - LF không có CR trước ⇒ ErrBareLF (D1). CR lạc trong dòng ⇒ 400.
//   - EOF trước byte đầu ⇒ io.EOF (caller quyết: đầu request thì là "client
//     đóng lịch sự"); EOF giữa dòng ⇒ io.ErrUnexpectedEOF.
func readLine(br *bufio.Reader, max int) ([]byte, error) {
	var acc []byte
	for {
		s, err := br.ReadSlice('\n')
		switch {
		case err == nil:
			line := s
			if acc != nil {
				acc = append(acc, s...)
				line = acc
			}
			// line gồm cả "\n"; nội dung = len-2 nếu có CR.
			if len(line)-2 > max {
				return nil, ErrLineTooLong
			}
			if len(line) < 2 || line[len(line)-2] != '\r' {
				if strictLineEnding {
					return nil, ErrBareLF
				}
				// nodefense: proxy khoan dung nhận LF trần — đúng cách backend
				// lenient đọc, và là một nửa của cặp strict/lenient gây smuggling.
				return line[:len(line)-1], nil
			}
			line = line[:len(line)-2]
			if bytes.IndexByte(line, '\r') >= 0 {
				return nil, badRequest("CR lạc trong dòng")
			}
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			acc = append(acc, s...)
			if len(acc) > max {
				return nil, ErrLineTooLong
			}
		case err == io.EOF:
			if len(acc)+len(s) == 0 {
				return nil, io.EOF
			}
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, err
		}
	}
}

// isTokenByte theo RFC 9110 §5.6.2: tchar.
func isTokenByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	}
	return false
}

func isToken(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if !isTokenByte(c) {
			return false
		}
	}
	return true
}

// isFieldValue: VCHAR / obs-text / SP / HTAB. Cấm CTL (kể cả CR, LF, NUL).
func isFieldValue(b []byte) bool {
	for _, c := range b {
		if c < 0x20 && c != '\t' || c == 0x7f {
			return false
		}
	}
	return true
}

// isVCHAR: 0x21..0x7E — request-target chỉ được có ký tự nhìn thấy ASCII.
// (turn 2: bản đầu dùng isFieldValue, cho lọt HTAB và obs-text; net/http từ chối.)
func isVCHAR(b []byte) bool {
	for _, c := range b {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

func trimOWS(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	return b
}

// parseVersion nhận đúng "HTTP/1.0" | "HTTP/1.1". "HTTP/d.d" khác ⇒ 505;
// không phải dạng đó ⇒ 400.
func parseVersion(b []byte) (string, error) {
	if len(b) == 8 && string(b[:5]) == "HTTP/" && b[6] == '.' &&
		b[5] >= '0' && b[5] <= '9' && b[7] >= '0' && b[7] <= '9' {
		switch string(b) {
		case "HTTP/1.1", "HTTP/1.0":
			return string(b), nil
		}
		return "", ErrVersion
	}
	return "", badRequest("phiên bản không hợp lệ: %q", b)
}

// readHeaders đọc khối header tới dòng trống. Trả về header, số byte đã đọc
// (kể cả CRLF và dòng trống). Ba trần kiểm TRONG LÚC đọc:
//   - MaxLineBytes: từng dòng (trong readLine)
//   - MaxHeaderBytes: tổng, cộng dồn từng dòng, kiểm trước khi Add
//   - MaxHeaderCount: số dòng, kiểm trước khi Add
//
// ⇒ header bomb "1 triệu dòng 1 byte" dừng sau MaxHeaderCount+1 dòng, không
// đọc hết input.
func readHeaders(br *bufio.Reader, lim Limits) (Header, int, error) {
	h := make(Header)
	total, count := 0, 0
	for {
		line, err := readLine(br, lim.MaxLineBytes)
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return nil, total, err
		}
		total += len(line) + 2
		if total > lim.MaxHeaderBytes {
			return nil, total, ErrHeaderTooLarge
		}
		if len(line) == 0 {
			return h, total, nil
		}
		if line[0] == ' ' || line[0] == '\t' {
			// obs-fold (RFC 9112 §5.2): proxy PHẢI từ chối hoặc thay bằng SP.
			// Chọn từ chối: thay thế là một cách "đoán" khác backend.
			return nil, total, badRequest("obs-fold không được hỗ trợ")
		}
		count++
		if count > lim.MaxHeaderCount {
			return nil, total, ErrHeaderTooLarge
		}
		i := bytes.IndexByte(line, ':')
		if i <= 0 {
			return nil, total, badRequest("dòng header thiếu ':'")
		}
		name := line[:i]
		if !strictHeaderName {
			name = trimOWS(name) // nodefense: "Transfer-Encoding : chunked" thành TE thật
		}
		if !isToken(name) {
			// bao gồm cả "Transfer-Encoding : chunked" (space trước ':') —
			// đúng vector TE.TE phase 4.
			return nil, total, badRequest("tên header không hợp lệ: %q", name)
		}
		value := trimOWS(line[i+1:])
		if !isFieldValue(value) {
			return nil, total, badRequest("giá trị header chứa CTL")
		}
		h.Add(string(name), string(value))
	}
}

// hasConnectionToken kiểm `Connection:` có chứa token (không phân biệt hoa/thường).
// P4-1 (trả 2026-10-02): strings.Cut thay bytes.Split([]byte(v)) — bản cũ cấp
// phát bản sao value + slice mỗi lần gọi (2 alloc / request).
func hasConnectionToken(h Header, token string) bool {
	for _, v := range h.Values("Connection") {
		for v != "" {
			var tok string
			tok, v, _ = strings.Cut(v, ",")
			if strings.EqualFold(strings.Trim(tok, " \t"), token) {
				return true
			}
		}
	}
	return false
}

// shouldClose quyết định connection phải đóng sau message này.
// HTTP/1.0: đóng trừ khi "Connection: keep-alive". HTTP/1.1: giữ trừ khi
// "Connection: close".
func shouldClose(proto string, h Header) bool {
	if proto == "HTTP/1.0" {
		return !hasConnectionToken(h, "keep-alive")
	}
	return hasConnectionToken(h, "close")
}

// checkConnectionTokens (D7): mọi tên trong `Connection:` phải là token, và
// không được là Host / Content-Length — RFC 9110 §7.6.1 cấm liệt kê chúng.
// Nếu cho qua, StripHopByHop sẽ xoá Host theo lệnh client rồi proxy tự sinh
// lại: client điều khiển được Host mà upstream nhìn thấy.
//
// Duyệt bằng strings.Cut trên string gốc, không bytes.Split: bản đầu (turn 1)
// tốn 2 alloc/request chỉ cho một kiểm tra — lộ ở BenchmarkReadRequest turn 2
// (26 → 28 allocs/op).
func checkConnectionTokens(h Header) error {
	for _, v := range h.Values("Connection") {
		for v != "" {
			var tok string
			tok, v, _ = strings.Cut(v, ",")
			tok = strings.Trim(tok, " \t")
			if tok == "" {
				continue
			}
			if !isTokenString(tok) {
				return badRequest("Connection: token không hợp lệ %q", tok)
			}
			if strings.EqualFold(tok, "Host") || strings.EqualFold(tok, "Content-Length") {
				return ErrBadConnectionToken
			}
		}
	}
	return nil
}

func isTokenString(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isTokenByte(s[i]) {
			return false
		}
	}
	return true
}
