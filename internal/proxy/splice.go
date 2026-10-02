package proxy

// Phase 9 D5: L7 mất splice ở HEAD (phải parse và ghi lại), nhưng không bắt
// buộc mất ở BODY. Head đã đi, ranh giới body là CL đã biết ⇒ phần còn lại chỉ
// là "chép đúng N byte từ fd upstream sang fd client" — đúng việc của L4.
// (*net.TCPConn).ReadFrom với *io.LimitedReader bọc *net.TCPConn ⇒ Go dùng
// splice(2) qua một pipe (internal/poll/splice_linux.go), byte không lên
// userspace.
//
// Không splice được: TLS (byte phải giải/mã hoá), chunked (phải đọc khung),
// body tới-EOF (ranh giới không biết trước), và body nhỏ (đã nằm cả trong ubr).

import (
	"bufio"
	"errors"
	"io"
	"net"
	"syscall"

	"github.com/thaivro/edgegate/internal/httpx"
)

const spliceMinBody = 64 << 10

// spliceBody: điều kiện đủ thì chép body CL bằng splice và trả used=true. Phần
// body đã nằm trong ubr (đọc lố cùng head) chép qua bw trước — splice chỉ thấy
// byte còn trong kernel. Sạch (caller) ⇔ chép đủ CL ∧ ubr.Buffered() == 0.
func (s *Server) spliceBody(c net.Conn, bw *bufio.Writer, resp *httpx.Response, ubr *bufio.Reader, pc *pooledConn) (used bool, rerr, werr error) {
	if !spliceBodyOn || !s.cfg.SpliceBody || resp.ContentLength < spliceMinBody {
		return false, nil, nil
	}
	ct, ok := c.(*net.TCPConn)
	ut, ok2 := pc.c.(*net.TCPConn)
	if !ok || !ok2 {
		return false, nil, nil
	}
	n0 := int64(ubr.Buffered())
	if n0 > resp.ContentLength {
		n0 = resp.ContentLength
	}
	// resp.Body đọc từ ubr; n0 byte đầu không chạm socket.
	if _, err := io.CopyN(bw, resp.Body, n0); err != nil {
		return true, err, nil
	}
	if err := bw.Flush(); err != nil {
		return true, nil, err
	}
	rem := resp.ContentLength - n0
	n, err := ct.ReadFrom(&io.LimitedReader{R: ut, N: rem})
	s.spliced.Add(1)
	s.splicedBytes.Add(n)
	if n < rem {
		if spliceClientFault(err, ct, ut) {
			return true, nil, err // werr: client bỏ đi — không nuôi outlier
		}
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return true, err, nil
	}
	return true, nil, nil
}

// spliceClientFault (P9-1, trả 2026-10-02): ReadFrom là MỘT lời gọi, MỘT lỗi —
// không nói lỗi đến từ đọc upstream hay ghi client. Bản cũ coi mọi thiếu byte là
// upstream ⇒ client bỏ giữa body splice nuôi outlier ejection oan. Phân loại:
//
//	err == nil (thiếu byte)       ⇒ upstream EOF sớm            → upstream
//	EPIPE                         ⇒ chỉ phát sinh khi GHI       → client
//	còn lại (ECONNRESET, timeout) ⇒ dò hai socket (MSG_PEEK): upstream chết →
//	                                upstream; không, mà client chết → client;
//	                                không rõ → upstream (như cũ)
func spliceClientFault(err error, client, upstream net.Conn) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EPIPE) {
		return true
	}
	if g, known := peerGone(upstream); known && g {
		return false
	}
	g, known := peerGone(client)
	return known && g
}
