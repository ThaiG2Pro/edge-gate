package httpx

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// framing quyết định cách đọc body — RFC 9112 §6.3, ĐÚNG thứ tự các bước:
//
//  1. Response cho HEAD, hay status 1xx/204/304 ⇒ không body, kể cả có CL/TE (D8).
//  2. Transfer-Encoding có ⇒ chunked (D2: phải đúng một token "chunked"); nếu
//     CL CŨNG có ⇒ TỪ CHỐI (phase 4 D1, ErrAmbiguousFraming). Phase 2 D4 từng
//     bỏ CL theo RFC 9112 §6.1 — đúng RFC, nhưng backend nào ưu tiên CL là
//     CL.TE; "cho phép" không phải "an toàn". HTTP/1.0 có TE ⇒ 400 (D3).
//  3. Content-Length ⇒ đúng n byte (chữ số thuần; phase 4 D2: chỉ MỘT dòng).
//  4. Không có gì ⇒ request: 0; response: tới EOF (length = -1) (D7).
//
// Trả về length (-1 = tới EOF hoặc chunked), chunked, lỗi.
func framing(proto string, h Header, isResponse, noBody bool) (int64, bool, error) {
	if noBody {
		return 0, false, nil
	}
	if te := h.Values("Transfer-Encoding"); len(te) > 0 {
		if proto == "HTTP/1.0" {
			return 0, false, badRequest("Transfer-Encoding trên HTTP/1.0")
		}
		if h.Has("Content-Length") {
			if rejectCLWithTE {
				return 0, false, ErrAmbiguousFraming
			}
			// nodefense: ưu tiên CL, bỏ TE — đúng cách front-end trong CL.TE
			// đọc. Backend đọc TE ⇒ hai ranh giới khác nhau.
			h.Del("Transfer-Encoding")
			n, err := parseContentLength(h.Values("Content-Length"))
			return n, false, err
		}
		if !teIsChunked(te) {
			return 0, false, ErrUnsupportedTE
		}
		return -1, true, nil
	}
	if cl := h.Values("Content-Length"); len(cl) > 0 {
		n, err := parseContentLength(cl)
		if err != nil {
			return 0, false, err
		}
		return n, false, nil
	}
	if isResponse {
		return -1, false, nil
	}
	return 0, false, nil
}

// parseContentLength: mọi dòng CL phải là chữ số thuần và GIỐNG NHAU. Không
// dấu, không space, không hex, không rỗng, KHÔNG danh sách phẩy ("5,5" — RFC
// 9110 §8.6 cho phép về cú pháp, net/http từ chối; turn 2 chọn từ chối để
// không khoan dung hơn oracle một cách vô ích). Tối đa 18 chữ số để không
// tràn int64: "9223372036854775808" hợp lệ về cú pháp mà mình không biểu
// diễn được — từ chối còn hơn tràn âm.
func parseContentLength(values []string) (int64, error) {
	first := values[0]
	if len(values) > 1 {
		if rejectMultiCL {
			// Phase 4 D2: hai dòng CL — kể cả giống nhau — là dấu có ai đã chèn
			// header. RFC 9110 §8.6 cho phép cú pháp; Go nhận trùng giống nhau;
			// ta từ chối. Hướng lệch vô hại (request không tới backend).
			return 0, ErrBadContentLength
		}
		// nodefense: lấy dòng đầu, bỏ các dòng sau.
	}
	if !strictCLSyntax {
		// nodefense: khoan dung kiểu strconv — nhận "+5", " 5", "5 ".
		n, err := strconv.ParseInt(strings.TrimSpace(first), 10, 64)
		if err != nil || n < 0 {
			return 0, ErrBadContentLength
		}
		return n, nil
	}
	if first == "" || len(first) > 18 {
		return 0, ErrBadContentLength
	}
	var n int64
	for i := 0; i < len(first); i++ {
		c := first[i]
		if c < '0' || c > '9' {
			return 0, ErrBadContentLength
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

// bodyReader tạo reader cho body theo kết quả framing. Không cấp phát theo
// length: body là stream (I2). Trailer chỉ có với chunked.
func bodyReader(br *bufio.Reader, lim Limits, length int64, chunked bool) (io.Reader, *chunkedReader) {
	switch {
	case chunked:
		cr := newChunkedReader(br, lim)
		return cr, cr
	case length == 0:
		return eofReader{}, nil
	case length < 0:
		return br, nil
	default:
		return &lengthReader{r: br, n: length}, nil
	}
}

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

// lengthReader đọc đúng n byte rồi EOF. Khác io.LimitReader ở một điểm quyết
// định (D6): nguồn hết TRƯỚC khi đủ n ⇒ io.ErrUnexpectedEOF, không phải io.EOF.
// Proxy nhận io.EOF là "body xong, forward tiếp"; nhận ErrUnexpectedEOF là
// "client cắt cụt, đóng cả hai chiều". Nhầm hai cái này là smuggling ngược.
type lengthReader struct {
	r io.Reader
	n int64
}

func (l *lengthReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	k, err := l.r.Read(p)
	l.n -= int64(k)
	if err == io.EOF && l.n > 0 {
		err = io.ErrUnexpectedEOF
	}
	if err == io.EOF && l.n == 0 {
		// nguồn EOF đúng lúc đủ byte: báo EOF ở lần Read sau cho gọn
		err = nil
	}
	return k, err
}

// Remaining trả số byte body còn chưa đọc (đo/log).
func (l *lengthReader) Remaining() int64 { return l.n }

// teIsChunked: Transfer-Encoding phải là ĐÚNG MỘT dòng, đúng một token
// "chunked" (không phân biệt hoa/thường). "chunked, chunked", "xchunked",
// "identity, chunked", "chunked;q=1" đều là TE.TE obfuscation ⇒ false.
func teIsChunked(te []string) bool {
	if !strictTE {
		// nodefense: nhận mọi value có chữ "chunked" — cách nhiều proxy cũ làm,
		// và là lý do TE.TE tồn tại.
		for _, v := range te {
			if strings.Contains(strings.ToLower(v), "chunked") {
				return true
			}
		}
		return false
	}
	return len(te) == 1 && strings.EqualFold(strings.TrimSpace(te[0]), "chunked")
}
