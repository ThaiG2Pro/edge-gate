package proxy

// Phase 9 D1-D3: buffer dùng lại qua sync.Pool. Pool là buffer REUSE, không
// phải zero-copy (zero-copy là splice — D5, splice.go).
//
// Luật trả về pool: chỉ người cầm DUY NHẤT trả, và chỉ khi không còn byte nào
// của ai nằm trong đó (bufio.Reader còn Buffered() > 0 = byte request kế tiếp
// — trả là mất byte, I1). Pool giữ *T, không giữ []byte trần: đổi slice sang
// interface là một lần cấp phát.

import (
	"bufio"
	"io"
	"sync"
	"sync/atomic"
)

const (
	copyBufSize = 32 << 10
	bufioRSize  = 8 << 10
	bufioWSize  = 8 << 10
)

var (
	copyBufPool sync.Pool // *[]byte len copyBufSize
	readerPool  sync.Pool // *bufio.Reader size bufioRSize
	writerPool  sync.Pool // *bufio.Writer size bufioWSize

	// liveBufio: số bufio.Reader/Writer đang có người cầm (get − put), cả hai
	// build — test D2 đếm nó để biết connection rỗi có còn giữ bufio không.
	liveBufio atomic.Int64
)

func getCopyBuf() *[]byte {
	if poolBuffers {
		if v := copyBufPool.Get(); v != nil {
			return v.(*[]byte)
		}
	}
	b := make([]byte, copyBufSize)
	return &b
}

func putCopyBuf(p *[]byte) {
	if poolBuffers && cap(*p) == copyBufSize {
		copyBufPool.Put(p)
	}
}

func getReader(r io.Reader) *bufio.Reader {
	liveBufio.Add(1)
	if poolBuffers {
		if v := readerPool.Get(); v != nil {
			br := v.(*bufio.Reader)
			br.Reset(r)
			return br
		}
	}
	return bufio.NewReaderSize(r, bufioRSize)
}

// putReader: caller bảo đảm br.Buffered() == 0 hoặc connection sắp chết.
func putReader(br *bufio.Reader) {
	liveBufio.Add(-1)
	if poolBuffers {
		br.Reset(nil) // nhả tham chiếu tới conn
		readerPool.Put(br)
	}
}

func getWriter(w io.Writer) *bufio.Writer {
	liveBufio.Add(1)
	if poolBuffers {
		if v := writerPool.Get(); v != nil {
			bw := v.(*bufio.Writer)
			bw.Reset(w)
			return bw
		}
	}
	return bufio.NewWriterSize(w, bufioWSize)
}

// putWriter: byte chưa Flush bị bỏ — caller Flush trước nếu còn cần.
func putWriter(bw *bufio.Writer) {
	liveBufio.Add(-1)
	if poolBuffers {
		bw.Reset(nil)
		writerPool.Put(bw)
	}
}

// prefixReader (D2): byte đầu của request đã được đọc bằng Read 1 byte lúc
// connection rỗi (không cầm bufio); bufio mới đọc qua đây — trả byte đó trước
// rồi đọc thẳng conn. Sống trong connState ⇒ không cấp phát mỗi request.
type prefixReader struct {
	first [1]byte
	has   bool
	r     io.Reader
}

func (p *prefixReader) Read(b []byte) (int, error) {
	if p.has {
		if len(b) == 0 {
			return 0, nil
		}
		b[0] = p.first[0]
		p.has = false
		return 1, nil
	}
	return p.r.Read(b)
}
