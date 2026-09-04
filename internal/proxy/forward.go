package proxy

import (
	"bufio"
	"io"
	"net"
	"strings"
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
//
// Phase 5: connection upstream lấy từ pool. Vòng for là D4 — retry ĐÚNG MỘT
// lần, chỉ khi exchange báo "chưa có byte response nào và connection là đồ
// dùng lại"; lần hai luôn dial mới.
func (s *Server) roundTrip(c net.Conn, br *bufio.Reader, bw *bufio.Writer, req *httpx.Request) bool {
	// --- 1. Head sang upstream --------------------------------------------
	up := &httpx.Request{Method: req.Method, Target: req.Target, Proto: "HTTP/1.1", Header: req.Header.Clone()}
	up.Header.StripHopByHop()
	// Phase 5 D5: KHÔNG còn `Connection: close` sang upstream (lối tắt D1 phase 3
	// đã tháo). Connection: close của client bị StripHopByHop xoá và không lan
	// sang — vòng đời hai connection độc lập, đó là toàn bộ ý nghĩa của pool.
	//
	// StripHopByHop vừa xoá Transfer-Encoding: đúng, vì TE của client mô tả
	// chặng client→proxy. Chặng proxy→upstream do TA quyết: body chunked thì
	// ta gửi chunked lại (không biết tổng độ dài để đổi sang CL mà không buffer).
	if req.Chunked {
		up.Header.Set("Transfer-Encoding", "chunked")
	}
	s.forwardedHeaders(up.Header, c.RemoteAddr()) // phase 4 D9: XFF có ranh giới tin cậy
	// Phase 4 D4 (đóng P-arch-1): Host GIỮ NGUYÊN (nginx `$host`) — reverse
	// proxy đứng trước virtual host, đổi Host là phá routing của upstream.
	// httpx.ReadRequest đã (a) kiểm cú pháp Host, (b) viết absolute-form về
	// origin-form và đặt Host := authority (D5). Chỉ còn một trường hợp buộc
	// sinh Host: client HTTP/1.0 không gửi — ta nâng request lên HTTP/1.1 nên
	// upstream đòi Host (net/http trả 400 "missing required Host header").
	if !up.Header.Has("Host") {
		up.Header.Set("Host", s.cfg.Upstream)
	}

	// --- 2. Connection upstream: pool hoặc dial (phase 5) ---------------------
	for attempt := 0; ; attempt++ {
		var pc *pooledConn
		var err error
		if attempt == 0 {
			pc, err = s.pool.get()
		} else {
			pc, err = s.pool.dialNew() // D4: lần hai luôn dial mới
		}
		if err != nil {
			s.cfg.Logf("proxy: dial %s: %v", s.cfg.Upstream, err)
			// D6 phase 3: chưa đụng body ⇒ drain rồi 502, connection client giữ được.
			keep := s.drain(c, req) && !req.Close
			s.writeError(c, bw, 502, "không dial được upstream", keep)
			return keep
		}
		keep, retry := s.exchange(c, bw, req, up, pc)
		if !retry {
			return keep
		}
		// D4 (a)(b)(c) đã thoả trong exchange. Chỉ một lần.
		if attempt == 0 {
			s.pool.retries.Add(1)
			s.cfg.Logf("proxy: upstream đóng connection rỗi trước khi nhận request, retry một lần")
			continue
		}
		keep = !req.Close // body rỗng (điều kiện (c)) ⇒ br sạch, client giữ được
		s.writeError(c, bw, 502, "upstream đóng khi đang nhận request (đã retry)", keep)
		return keep
	}
}

// canRetry: điều kiện (a) và (c) của D4 — connection là đồ dùng lại VÀ request
// không có body (không replay được body đã stream sang connection chết).
// Điều kiện (b) — lỗi I/O khi 0 byte response — do exchange kiểm tại chỗ lỗi.
func canRetry(pc *pooledConn, req *httpx.Request) bool {
	return pc.reused && req.ContentLength == 0 && !req.Chunked
}

// exchange: bước 3-5 trên MỘT connection upstream pc. Trả (keep, retry):
// retry=true ⇔ chưa ghi gì cho client và D4 cho phép thử lại; khi đó caller
// quyết. Mọi đường ra đều qua release: pc về pool chỉ khi clean (D2), còn lại
// đóng.
func (s *Server) exchange(c net.Conn, bw *bufio.Writer, req, up *httpx.Request, pc *pooledConn) (keep, retry bool) {
	lim := s.cfg.Limits
	uc, ubr, ubw := pc.c, pc.br, pc.bw
	pc.in.n = 0
	clean, headOK := false, false
	defer func() {
		if !poolCheckClean && headOK {
			clean = true // nodefense: về pool ngay khi có head — bẫy phase 5
		}
		if clean {
			s.pool.put(pc)
		} else {
			s.pool.discard(pc)
		}
	}()

	// --- 3. Head + body sang upstream ---------------------------------------
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
		return false, false
	case writeErr != nil:
		if canRetry(pc, req) {
			return false, true // D4: ghi lỗi ⇒ chắc chắn 0 byte response
		}
		s.cfg.Logf("proxy: ghi sang upstream: %v", writeErr)
		keep := drained && !req.Close
		s.writeError(c, bw, 502, "upstream đóng khi đang nhận request", keep)
		return keep, false
	}

	// --- 4. Head response từ upstream ---------------------------------------
	var resp *httpx.Response
	var err error
	for {
		uc.SetReadDeadline(time.Now().Add(s.cfg.UpstreamHeaderTimeout))
		resp, err = httpx.ReadResponse(ubr, lim, req.Method)
		if err != nil {
			// D4 (b): 0 byte response đã tới VÀ không phải timeout (timeout với
			// 0 byte = upstream sống nhưng chậm, có thể đang xử lý ⇒ không
			// idempotent nữa ⇒ 504, không retry).
			if pc.in.n == 0 && !isTimeout(err) && canRetry(pc, req) {
				return false, true
			}
			status, detail := 502, "upstream trả response không hợp lệ hoặc đóng sớm"
			if isTimeout(err) {
				status, detail = 504, "upstream không trả head trong UpstreamHeaderTimeout"
			}
			s.cfg.Logf("proxy: đọc response: %v", err)
			keep := drained && !req.Close
			s.writeError(c, bw, status, detail, keep)
			return keep, false
		}
		if resp.Status/100 != 1 {
			break
		}
		// D7 phase 3: 1xx là interim — bỏ, đọc response kế. 101 là đổi giao
		// thức: không tunnel, trả 502 (chưa gửi gì cho client) và đóng.
		if resp.Status == 101 {
			s.writeError(c, bw, 502, "upstream đòi Upgrade (101), chưa hỗ trợ", false)
			return false, false
		}
	}
	headOK = true

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
		// D3 phase 3: body tới EOF. Client HTTP/1.1 nhận chunked để biết ranh
		// giới mà không cần đóng; HTTP/1.0 không có chunked ⇒ chép tới EOF rồi đóng.
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
		return false, false
	}
	// Từ đây head đã đi: mọi lỗi đều phải đóng, không có "response thứ hai".
	bodyDone := mode == modeNone // NoBody: không có gì để đọc ⇒ đã "hết"
	if rawCopyResponse {
		// Bẫy #2 (chỉ khi -tags nodefense): chép tới khi upstream đóng. Với
		// upstream keep-alive thì treo tới deadline. Xem TestRawCopyTrap.
		_, err = io.Copy(bw, ubr)
	} else {
		var rerr, werr error
		switch mode {
		case modeCopy:
			rerr, werr = copyBody(bw, resp.Body, false)
		case modeChunked:
			rerr, werr = copyBody(bw, resp.Body, true)
		}
		bodyDone = bodyDone || (rerr == nil && werr == nil) // resp.Body đã trả io.EOF
		if rerr != nil {
			err = rerr
		} else {
			err = werr
		}
	}
	if err != nil {
		// Upstream hỏng giữa body (rerr) HAY client bỏ đi giữa body (werr): cả
		// hai đều để lại byte chưa đọc trên connection upstream ⇒ bẩn (D2 e).
		s.cfg.Logf("proxy: body response: %v", err)
		return false, false
	}
	if err := bw.Flush(); err != nil {
		return false, false
	}
	// D4 phase 3: trailer của resp bị bỏ (resp.Trailer()). Nợ P3-1.

	// D2: SẠCH ⇔ body đã EOF (b) ∧ không byte thừa (c) ∧ upstream không đòi
	// đóng / không phải body-tới-EOF (d) ∧ không lỗi (e, đã return ở trên).
	clean = bodyDone && ubr.Buffered() == 0 && !resp.Close
	return !closeClient, false
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

// forwardedHeaders (D9) viết X-Forwarded-For / X-Real-IP / Forwarded theo
// ranh giới tin cậy:
//
//   - peer TIN (trong TrustedProxies): XFF client gửi là của một proxy ta tin
//     ⇒ giữ và APPEND IP peer. X-Real-IP / Forwarded giữ nguyên.
//   - peer KHÔNG tin: mọi thứ nó nói về "client thật" là dữ liệu không tin
//     được ⇒ XFF := peer (THAY, không append), X-Real-IP := peer, xoá Forwarded.
//
// Không có ranh giới này, rate limiter phase 7 đếm theo IP trong XFF bị bypass
// bằng một header giả — "append, không overwrite" mới là nửa bài.
func (s *Server) forwardedHeaders(h httpx.Header, addr net.Addr) {
	ipStr := addr.String()
	if host, _, err := net.SplitHostPort(ipStr); err == nil {
		ipStr = host
	}
	ip := net.ParseIP(ipStr)
	if ip != nil && s.cfg.isTrusted(ip) {
		vals := h.Values("X-Forwarded-For")
		xff := ipStr
		if len(vals) > 0 {
			xff = strings.Join(vals, ", ") + ", " + ipStr
		}
		h.Set("X-Forwarded-For", xff)
		return
	}
	h.Set("X-Forwarded-For", ipStr)
	h.Set("X-Real-Ip", ipStr)
	h.Del("Forwarded")
}
