package httpx

import (
	"errors"
	"fmt"
)

// ProtoError là lỗi giao thức có thể map trực tiếp sang status code trả về
// client. Phân biệt với lỗi I/O (connection đứt) — loại đó không trả response.
//
// Với ReadResponse, ProtoError nghĩa là UPSTREAM gửi rác; tầng proxy (phase 3)
// sẽ map thành 502 cho client. Parser không tự quyết chuyện đó.
type ProtoError struct {
	Status int
	Reason string
}

func (e *ProtoError) Error() string {
	return fmt.Sprintf("http %d: %s", e.Status, e.Reason)
}

// Is cho phép errors.Is(err, ErrLineTooLong) so theo Status+Reason-class.
func (e *ProtoError) Is(target error) bool {
	t, ok := target.(*ProtoError)
	return ok && t.Status == e.Status && (t.Reason == "" || t.Reason == e.Reason)
}

func badRequest(format string, args ...any) *ProtoError {
	return &ProtoError{Status: 400, Reason: fmt.Sprintf(format, args...)}
}

// clipMax: số byte tối đa của một trường peer gửi được chép vào Reason.
const clipMax = 32

// clip (P2-5, trả 2026-10-02): Reason chỉ mang 32 byte đầu của trường bẩn.
// "%q" trên cả trường = ~4 byte/byte input (\xa9 → `\xa9`) cộng tăng trưởng
// của fmt: request-line 7.4 KB cho 196 KB cấp phát, và Reason đó đi vào log.
func clip[T ~string | ~[]byte](v T) string {
	if len(v) <= clipMax {
		return string(v)
	}
	return fmt.Sprintf("%s…(+%d byte)", v[:clipMax], len(v)-clipMax)
}

func protoError(status int, format string, args ...any) *ProtoError {
	return &ProtoError{Status: status, Reason: fmt.Sprintf(format, args...)}
}

// Các lỗi "lớp" để test so bằng errors.Is mà không phải khớp chuỗi Reason.
var (
	// ErrLineTooLong: một dòng (request line / header / chunk-size) vượt MaxLineBytes.
	ErrLineTooLong = &ProtoError{Status: 431, Reason: "line too long"}
	// ErrHeaderTooLarge: tổng khối header vượt MaxHeaderBytes hoặc MaxHeaderCount.
	ErrHeaderTooLarge = &ProtoError{Status: 431, Reason: "header too large"}
	// ErrBareLF: dòng kết thúc bằng LF không có CR. Quyết định D1: strict.
	ErrBareLF = &ProtoError{Status: 400, Reason: "bare LF"}
	// ErrBadContentLength: CL không phải chữ số thuần, hoặc nhiều CL khác nhau.
	ErrBadContentLength = &ProtoError{Status: 400, Reason: "bad Content-Length"}
	// ErrUnsupportedTE: Transfer-Encoding khác đúng một token "chunked" (D2).
	ErrUnsupportedTE = &ProtoError{Status: 501, Reason: "unsupported Transfer-Encoding"}
	// ErrVersion: HTTP/x.y không phải 1.0 / 1.1.
	ErrVersion = &ProtoError{Status: 505, Reason: "unsupported HTTP version"}
	// ErrBadChunk: chunk-size không phải hex, tràn, hoặc chunk data không kết thúc CRLF.
	ErrBadChunk = &ProtoError{Status: 400, Reason: "bad chunk"}
	// ErrAmbiguousFraming: Content-Length và Transfer-Encoding cùng có (D1 phase 4).
	// RFC 9112 §6.1 cho phép bỏ CL; ta từ chối, vì bỏ CL chính là CL.TE.
	ErrAmbiguousFraming = &ProtoError{Status: 400, Reason: "Content-Length and Transfer-Encoding both present"}
	// ErrBadHost: Host sai cú pháp uri-host[:port], hoặc mâu thuẫn với authority
	// trong absolute-form request-target (D4/D5 phase 4).
	ErrBadHost = &ProtoError{Status: 400, Reason: "bad Host"}
	// ErrBadTarget: request-target sai dạng (scheme không phải http, "*" không
	// đi với OPTIONS, authority-form không đi với CONNECT…) (D5 phase 4).
	ErrBadTarget = &ProtoError{Status: 400, Reason: "bad request-target"}
	// ErrBadConnectionToken: Connection: liệt kê field không được là hop-by-hop
	// (Host, Content-Length) (D7 phase 4).
	ErrBadConnectionToken = &ProtoError{Status: 400, Reason: "Connection lists a non-hop-by-hop field"}
	// ErrBadTrailer: trailer mang framing/routing field (D8 phase 4, RFC 9110 §6.5.1).
	ErrBadTrailer = &ProtoError{Status: 400, Reason: "forbidden field in trailer"}
	// ErrConnectNotSupported: CONNECT không tunnel (D6 phase 4).
	ErrConnectNotSupported = &ProtoError{Status: 501, Reason: "CONNECT not supported"}
	// ErrHeaderInjection: tên/giá trị header chứa CR, LF hoặc NUL khi serialize.
	ErrHeaderInjection = errors.New("httpx: header chứa CR/LF/NUL")
)

// IsProtoError trả về *ProtoError nếu err (hoặc lỗi bọc bên trong) là lỗi giao thức.
func IsProtoError(err error) (*ProtoError, bool) {
	var pe *ProtoError
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}
