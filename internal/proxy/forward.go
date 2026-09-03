package proxy

import (
	"bufio"
	"io"
	"net"
	"time"

	"github.com/thaivro/edgegate/internal/httpx"
)

// roundTrip forward MỘT request đã parse xong head sang upstream và trả
// response về client. Trả về true nếu connection client còn dùng lại được.
//
// Invariant duy nhất quyết định keep-alive: connection client giữ được ⇔
// body request đã được đọc HẾT khỏi br (bằng forward hoặc bằng drain) VÀ
// chưa gửi head response nào rồi bỏ dở. Vi phạm vế 1 là bẫy #3; vi phạm vế 2
// là gửi hai response cho một request.
func (s *Server) roundTrip(c net.Conn, br *bufio.Reader, bw *bufio.Writer, req *httpx.Request) bool {
	lim := s.cfg.Limits

	// --- 1. Head sang upstream --------------------------------------------
	up := &httpx.Request{Method: req.Method, Target: req.Target, Proto: "HTTP/1.1", Header: req.Header.Clone()}
	up.Header.StripHopByHop()
	// D1: lối tắt phase 3 — upstream đóng sau response ⇒ body-tới-EOF luôn
	// đúng, chưa cần pool. Phase 5 tháo dòng này.
	up.Header.Set("Connection", "close")
	// StripHopByHop vừa xoá Transfer-Encoding: đúng, vì TE của client mô tả
	// chặng client→proxy. Chặng proxy→upstream do TA quyết: body chunked thì
	// ta gửi chunked lại (không biết tổng độ dài để đổi sang CL mà không buffer).
	if req.Chunked {
		up.Header.Set("Transfer-Encoding", "chunked")
	}
	appendXFF(up.Header, c.RemoteAddr()) // D5: append, chưa trust (phase 4)
	// D2: Host nguyên văn (P-arch-1 mở) — TRỪ khi client HTTP/1.0 không gửi
	// Host: ta nâng request lên HTTP/1.1 nên upstream đòi Host (net/http trả
	// 400 "missing required Host header" — lộ ở TestHTTP10ClientGetsEOFBody).
	// Điền bằng authority ta đang dial, giống nginx `proxy_set_header Host`.
	if !up.Header.Has("Host") {
		up.Header.Set("Host", s.cfg.Upstream)
	}

	// --- 2. Dial ------------------------------------------------------------
	uc, err := net.DialTimeout("tcp", s.cfg.Upstream, s.cfg.DialTimeout)
	if err != nil {
		s.cfg.Logf("proxy: dial %s: %v", s.cfg.Upstream, err)
		// D6: chưa đụng body ⇒ drain rồi 502, connection client giữ được.
		keep := s.drain(c, req) && !req.Close
		s.writeError(c, bw, 502, "không dial được upstream", keep)
		return keep
	}
	defer uc.Close()
	s.setNoDelay(uc)

	// --- 3. Head + body sang upstream ---------------------------------------
	ubw := bufio.NewWriterSize(uc, 8<<10)
	uc.SetWriteDeadline(time.Now().Add(s.cfg.UpstreamBodyTimeout))
	c.SetReadDeadline(time.Now().Add(lim.BodyTimeout)) // I3: body client có deadline
	writeErr := up.WriteHead(ubw)
	var readErr error
	if writeErr == nil {
		// req.Body là stream ĐÚNG ranh giới (lengthReader / chunkedReader /
		// eofReader) trên br — nó trả io.EOF khi hết body, KHÔNG phải khi
		// client đóng. Đây là "một dòng" tránh bẫy #1: không bao giờ
		// io.Copy(upstream, c).
		readErr, writeErr = copyBody(ubw, req.Body, req.Chunked)
	}
	if writeErr == nil {
		writeErr = ubw.Flush()
	}
	drained := readErr == nil // body đã ra khỏi br trọn vẹn
	switch {
	case readErr != nil:
		// Client cắt cụt body (ErrUnexpectedEOF) hoặc quá BodyTimeout: chưa
		// gửi gì cho client nên còn trả được 400/408, nhưng phải đóng.
		status, detail := 400, "body request cắt cụt"
		if isTimeout(readErr) {
			status, detail = 408, "quá BodyTimeout khi đọc body"
		}
		s.writeError(c, bw, status, detail, false)
		return false
	case writeErr != nil:
		s.cfg.Logf("proxy: ghi sang upstream: %v", writeErr)
		keep := drained && !req.Close
		s.writeError(c, bw, 502, "upstream đóng khi đang nhận request", keep)
		return keep
	}

	// --- 4. Head response từ upstream ---------------------------------------
	ubr := bufio.NewReaderSize(uc, 8<<10)
	var resp *httpx.Response
	for {
		uc.SetReadDeadline(time.Now().Add(s.cfg.UpstreamHeaderTimeout))
		resp, err = httpx.ReadResponse(ubr, lim, req.Method)
		if err != nil {
			status, detail := 502, "upstream trả response không hợp lệ hoặc đóng sớm"
			if isTimeout(err) {
				status, detail = 504, "upstream không trả head trong UpstreamHeaderTimeout"
			}
			s.cfg.Logf("proxy: đọc response: %v", err)
			keep := drained && !req.Close
			s.writeError(c, bw, status, detail, keep)
			return keep
		}
		if resp.Status/100 != 1 {
			break
		}
		// D7: 1xx là interim — bỏ, đọc response kế. 101 là đổi giao thức: không
		// tunnel ở phase 3, trả 502 (chưa gửi gì cho client) và đóng.
		if resp.Status == 101 {
			s.writeError(c, bw, 502, "upstream đòi Upgrade (101), chưa hỗ trợ", false)
			return false
		}
	}

	// --- 5. Head + body về client -------------------------------------------
	out := &httpx.Response{Proto: "HTTP/1.1", Status: resp.Status, Reason: resp.Reason, Header: resp.Header.Clone()}
	out.Header.StripHopByHop()
	closeClient := req.Close
	const (
		modeNone    = iota
		modeCopy    // CL hoặc tới-EOF: chép nguyên
		modeChunked // mã hoá chunked lại
	)
	mode := modeNone
	switch {
	case httpx.NoBody(req.Method, resp.Status):
		// HEAD / 1xx / 204 / 304: không body. StripHopByHop đã xoá TE và ta
		// không đặt lại ⇒ head về client không mang TE (đây chính là P2-2).
	case resp.Chunked:
		out.Header.Set("Transfer-Encoding", "chunked")
		mode = modeChunked
	case resp.ContentLength >= 0:
		mode = modeCopy // CL đã nằm sẵn trong out.Header
	default:
		// D3: body tới EOF. Client HTTP/1.1 nhận chunked để biết ranh giới mà
		// không cần đóng; HTTP/1.0 không có chunked ⇒ chép tới EOF rồi đóng.
		if req.Proto == "HTTP/1.1" {
			out.Header.Set("Transfer-Encoding", "chunked")
			mode = modeChunked
		} else {
			mode = modeCopy
			closeClient = true
		}
	}
	if closeClient {
		out.Header.Set("Connection", "close")
	} else if req.Proto == "HTTP/1.0" {
		out.Header.Set("Connection", "keep-alive")
	}

	c.SetWriteDeadline(time.Now().Add(lim.BodyTimeout))
	uc.SetReadDeadline(time.Now().Add(s.cfg.UpstreamBodyTimeout))
	if err := out.WriteHead(bw); err != nil {
		return false
	}
	// Từ đây head đã đi: mọi lỗi đều phải đóng, không có "response thứ hai".
	if rawCopyResponse {
		// Bẫy #2 (chỉ khi -tags nodefense): chép tới khi upstream đóng. Với
		// D1 upstream đóng thật nên trông vẫn "chạy" — cho tới khi gặp một
		// upstream bỏ qua Connection: close. Xem TestRawCopyTrap.
		_, err = io.Copy(bw, ubr)
	} else {
		switch mode {
		case modeCopy:
			_, err = copyBody(bw, resp.Body, false)
		case modeChunked:
			_, err = copyBody(bw, resp.Body, true)
		}
	}
	if err != nil {
		s.cfg.Logf("proxy: body response: %v", err)
		return false
	}
	if err := bw.Flush(); err != nil {
		return false
	}
	// D4: trailer của resp bị bỏ (resp.Trailer()). Nợ P3.
	return !closeClient
}

// copyBody chép src (đã framing đúng ranh giới) vào dst, Flush sau mỗi lần
// Read để response streaming (SSE, chunked chậm) không bị bufio gom lại —
// nếu gom, G4 (Nagle) không đo được và client thấy từng chunk đến trễ.
// Trả riêng lỗi đọc và lỗi ghi: caller phân biệt "client/upstream nguồn
// hỏng" với "đích hỏng" để quyết keep-alive.
func copyBody(dst *bufio.Writer, src io.Reader, chunked bool) (readErr, writeErr error) {
	var w io.Writer = dst
	var cw *httpx.ChunkedWriter
	if chunked {
		cw = httpx.NewChunkedWriter(dst)
		w = cw
	}
	buf := make([]byte, 32<<10)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return nil, werr
			}
			if werr := dst.Flush(); werr != nil {
				return nil, werr
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr, nil
		}
	}
	if cw != nil {
		if werr := cw.Close(); werr != nil {
			return nil, werr
		}
	}
	return nil, dst.Flush()
}

// drain đọc hết body request khi upstream hỏng trước lúc forward body, để
// br sạch cho request kế tiếp. Trả về false nếu không đọc được hết (client
// cắt cụt / quá BodyTimeout) — caller phải đóng.
func (s *Server) drain(c net.Conn, req *httpx.Request) bool {
	if !drainOnUpstreamError {
		return true // nodefense: NÓI DỐI là đã sạch — bẫy #3 bật.
	}
	c.SetReadDeadline(time.Now().Add(s.cfg.Limits.BodyTimeout))
	_, err := io.Copy(io.Discard, req.Body)
	return err == nil
}

// appendXFF nối IP client vào X-Forwarded-For. Chưa có trust list (phase 4):
// giá trị client gửi được giữ nguyên rồi nối thêm — đúng dạng, chưa đáng tin.
func appendXFF(h httpx.Header, addr net.Addr) {
	ip := addr.String()
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	vals := h.Values("X-Forwarded-For")
	xff := ip
	if len(vals) > 0 {
		prev := vals[0]
		for _, v := range vals[1:] {
			prev += ", " + v
		}
		xff = prev + ", " + ip
	}
	h.Set("X-Forwarded-For", xff)
}
