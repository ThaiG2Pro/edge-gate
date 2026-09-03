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
