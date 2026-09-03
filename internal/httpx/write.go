package httpx

import (
	"io"
	"strconv"
)

// WriteHead ghi request line + header + dòng trống trong ĐÚNG MỘT lần w.Write.
// Không ghi body: body là stream, caller io.Copy (CL) hoặc qua ChunkedWriter.
// Header có CR/LF/NUL ⇒ ErrHeaderInjection, không ghi gì.
func (r *Request) WriteHead(w io.Writer) error {
	buf := make([]byte, 0, 256)
	buf = append(buf, r.Method...)
	buf = append(buf, ' ')
	buf = append(buf, r.Target...)
	buf = append(buf, ' ')
	buf = append(buf, r.Proto...)
	buf = append(buf, '\r', '\n')
	buf, err := r.Header.appendWire(buf)
	if err != nil {
		return err
	}
	buf = append(buf, '\r', '\n')
	_, err = w.Write(buf)
	return err
}

// WriteHead ghi status line + header + dòng trống trong ĐÚNG MỘT lần w.Write.
func (r *Response) WriteHead(w io.Writer) error {
	buf := make([]byte, 0, 256)
	buf = append(buf, r.Proto...)
	buf = append(buf, ' ')
	buf = strconv.AppendInt(buf, int64(r.Status), 10)
	buf = append(buf, ' ')
	buf = append(buf, r.Reason...)
	buf = append(buf, '\r', '\n')
	buf, err := r.Header.appendWire(buf)
	if err != nil {
		return err
	}
	buf = append(buf, '\r', '\n')
	_, err = w.Write(buf)
	return err
}
