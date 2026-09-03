package httpx

import (
	"bufio"
	"bytes"
	"io"
	"strconv"
)

// chunkedReader giải mã Transfer-Encoding: chunked (RFC 9112 §7.1) từ br.
//
// I2 ở đây: chunk-size là một "length" do peer bịa. Reader KHÔNG BAO GIỜ
// make theo size — nó chỉ nhớ "còn n byte của chunk hiện tại" và cấp cho
// caller theo len(p). Chunk-size FFFFFFFF hợp lệ về cú pháp; nó chỉ có nghĩa
// là "sẽ có 4 GiB tới", và reader cứ stream. Cái duy nhất được gom là dòng
// chunk-size (trần MaxLineBytes) và trailer (trần như header).
type chunkedReader struct {
	br      *bufio.Reader
	lim     Limits
	n       uint64 // byte còn lại của chunk hiện tại
	done    bool
	err     error
	Trailer Header // điền sau chunk cuối; nil nếu không có trailer
	chunks  int    // số chunk đã thấy (đo)
}

func newChunkedReader(br *bufio.Reader, lim Limits) *chunkedReader {
	return &chunkedReader{br: br, lim: lim}
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	for {
		if c.err != nil {
			return 0, c.err
		}
		if c.done {
			return 0, io.EOF
		}
		if c.n == 0 {
			if err := c.beginChunk(); err != nil {
				c.err = err
				return 0, err
			}
			continue // done có thể vừa thành true
		}
		if len(p) == 0 {
			return 0, nil
		}
		if uint64(len(p)) > c.n {
			p = p[:c.n]
		}
		k, err := c.br.Read(p)
		c.n -= uint64(k)
		if err == nil && c.n == 0 {
			err = c.expectCRLF()
		}
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		if err != nil {
			c.err = err
		}
		if k > 0 {
			return k, nil // lỗi (nếu có) trả ở lần Read sau, sau khi giao data
		}
		if c.err != nil {
			return 0, c.err
		}
	}
}

// beginChunk đọc dòng chunk-size; size 0 ⇒ đọc trailer, done.
func (c *chunkedReader) beginChunk() error {
	line, err := readLine(c.br, c.lim.MaxLineBytes)
	if err != nil {
		if err == io.EOF {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	size, err := parseChunkSize(line)
	if err != nil {
		return err
	}
	c.chunks++
	if size == 0 {
		// trailer-section: 0 hay nhiều dòng header, kết thúc bằng dòng trống.
		// Dùng cùng readHeaders ⇒ cùng trần với header.
		tr, _, err := readHeaders(c.br, c.lim)
		if err != nil {
			return err
		}
		if len(tr) > 0 {
			c.Trailer = tr
		}
		c.done = true
		return nil
	}
	c.n = size
	return nil
}

// expectCRLF nuốt "\r\n" sau chunk data.
func (c *chunkedReader) expectCRLF() error {
	var crlf [2]byte
	if _, err := io.ReadFull(c.br, crlf[:]); err != nil {
		if err == io.EOF {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	if crlf != [2]byte{'\r', '\n'} {
		return ErrBadChunk
	}
	return nil
}

// parseChunkSize: chunk-size [ ";" chunk-ext ]. Hex, 1..16 chữ số, KHÔNG
// whitespace ở bất kỳ đâu trước ';'. Ext bị bỏ qua (RFC cho phép nhận rồi bỏ).
//
// RFC 9112 §7.1.1 cho BWS trước ';' — bản turn 1 trim nó. Turn 2 đối chiếu
// oracle: net/http từ chối " 3" VÀ "3 ;x" nhưng nhận "3  " (trailing ws).
// Hai bên cùng nhận head mà một bên đọc được body, một bên lỗi = đúng lớp
// lệch diff-fuzz phải bắt. Chọn strict hơn cả hai: từ chối mọi whitespace.
// Hướng "mình từ chối, oracle nhận" là vô hại.
func parseChunkSize(line []byte) (uint64, error) {
	if i := bytes.IndexByte(line, ';'); i >= 0 {
		line = line[:i]
	}
	if len(line) == 0 || len(line) > 16 {
		return 0, ErrBadChunk
	}
	for _, c := range line {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return 0, ErrBadChunk
		}
	}
	n, err := strconv.ParseUint(string(line), 16, 64)
	if err != nil {
		return 0, ErrBadChunk
	}
	return n, nil
}

// ChunkedWriter mã hoá chunked ra w. Mỗi Write = một chunk = ĐÚNG MỘT lần
// w.Write (size-line + data + CRLF ghép trong một buffer — phase 0: write-write
// + Nagle = 44ms). Close ghi chunk cuối "0\r\n\r\n" (không trailer).
type ChunkedWriter struct {
	w   io.Writer
	buf []byte
}

func NewChunkedWriter(w io.Writer) *ChunkedWriter { return &ChunkedWriter{w: w} }

func (cw *ChunkedWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil // chunk rỗng nghĩa là "hết body" — không được vô tình phát
	}
	cw.buf = cw.buf[:0]
	cw.buf = strconv.AppendUint(cw.buf, uint64(len(p)), 16)
	cw.buf = append(cw.buf, '\r', '\n')
	cw.buf = append(cw.buf, p...)
	cw.buf = append(cw.buf, '\r', '\n')
	if _, err := cw.w.Write(cw.buf); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (cw *ChunkedWriter) Close() error {
	_, err := io.WriteString(cw.w, "0\r\n\r\n")
	return err
}
