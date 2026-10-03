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
	if !spliceBodyOn || s.cfg.NoSplice || resp.ContentLength < spliceMinBody {
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

// spliceUpload (P9-2): đối xứng spliceBody cho chiều UPLOAD (client → upstream).
// Head đã ghi vào ubw (chưa flush), body request có CL ≥ 64 KiB, cả hai phía
// TCP trần, không chunked ⇒ chép N byte từ fd client sang fd upstream bằng
// splice(2). Phần body đã nằm trong br (đọc lố cùng head) chép qua ubw trước
// (cùng head), flush, rồi `upTCP.ReadFrom(&io.LimitedReader{clientTCP, còn lại})`.
//
// Retry không đổi: canRetry đòi ContentLength == 0 nên request có body không bao
// giờ replay — body stream đi là mất, hệt copyBody userspace.
//
// (used, rerr, werr): rerr = lỗi ĐỌC từ client (upload cắt cụt) → caller 400/408;
// werr = lỗi GHI sang upstream (upstream chết) → caller 502. Phân loại như
// spliceClientFault nhưng đổi vai: nguồn là client, đích là upstream.
func (s *Server) spliceUpload(uc net.Conn, ubw *bufio.Writer, req *httpx.Request, br *bufio.Reader, c net.Conn) (used bool, rerr, werr error) {
	if !spliceBodyOn || s.cfg.NoSplice || req.Chunked || req.ContentLength < spliceMinBody {
		return false, nil, nil
	}
	ct, ok := c.(*net.TCPConn)
	ut, ok2 := uc.(*net.TCPConn)
	if !ok || !ok2 {
		return false, nil, nil
	}
	n0 := int64(br.Buffered())
	if n0 > req.ContentLength {
		n0 = req.ContentLength
	}
	// req.Body đọc từ br; n0 byte đầu không chạm socket. Ghi chung ubw với head.
	if _, err := io.CopyN(ubw, req.Body, n0); err != nil {
		// Lỗi đọc n0 byte đã đệm = client; hiếm (đã trong buffer) nhưng phân đúng.
		return true, err, nil
	}
	if err := ubw.Flush(); err != nil {
		return true, nil, err // flush head+đệm sang upstream lỗi = upstream
	}
	rem := req.ContentLength - n0
	n, err := ut.ReadFrom(&io.LimitedReader{R: ct, N: rem})
	s.splicedUp.Add(1)
	s.splicedUpBytes.Add(n)
	if n < rem {
		// ReadFrom một lời gọi, một lỗi. nil ⇒ client EOF sớm (upload cắt).
		if err == nil {
			return true, io.ErrUnexpectedEOF, nil
		}
		if spliceUploadClientFault(err, ct, ut) {
			return true, err, nil // client cắt upload → rerr
		}
		return true, nil, err // upstream chết → werr
	}
	return true, nil, nil
}

// spliceUploadClientFault: đổi vai của spliceClientFault — nguồn là CLIENT, đích
// là UPSTREAM. EPIPE (lỗi ghi) ⇒ upstream chết (werr, trả false). Còn lại dò
// hai socket: client chết → rerr (true); upstream chết → werr (false); không rõ
// → werr (false, coi như upstream — an toàn hơn trả 400 oan cho client).
func spliceUploadClientFault(err error, client, upstream net.Conn) bool {
	if err == nil {
		return true // EOF sớm phía đọc = client (đã xử ở caller, phòng hờ)
	}
	if errors.Is(err, syscall.EPIPE) {
		return false // chỉ phát sinh khi GHI ⇒ upstream
	}
	if g, known := peerGone(client); known && g {
		return true
	}
	return false
}
