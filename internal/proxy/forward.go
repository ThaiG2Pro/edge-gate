package proxy

import (
	"bufio"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/thaivro/edgegate/internal/httpx"
	"github.com/thaivro/edgegate/internal/lb"
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
func (s *Server) roundTrip(c net.Conn, st *connState, br *bufio.Reader, bw *bufio.Writer, req *httpx.Request) bool {
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
	clientIP := s.forwardedHeaders(up.Header, c.RemoteAddr()) // phase 4 D9: XFF có ranh giới tin cậy
	// Phase 4 D4 (đóng P-arch-1): Host GIỮ NGUYÊN (nginx `$host`) — reverse
	// proxy đứng trước virtual host, đổi Host là phá routing của upstream.
	// httpx.ReadRequest đã (a) kiểm cú pháp Host, (b) viết absolute-form về
	// origin-form và đặt Host := authority (D5). Chỉ còn một trường hợp buộc
	// sinh Host: client HTTP/1.0 không gửi — ta nâng request lên HTTP/1.1 nên
	// upstream đòi Host (net/http trả 400 "missing required Host header").
	// Phase 6: Host sinh ra là addr của backend ĐƯỢC CHỌN, đặt trong vòng lặp.
	noHost := !up.Header.Has("Host")

	// Khoá cho consistent hash (D5): header LB.HashHeader nếu có, không thì IP
	// client SAU ranh giới tin cậy — XFF chưa tin là để attacker chọn backend.
	key := clientIP
	if h := s.cfg.LB.HashHeader; h != "" {
		if v := req.Header.Get(h); v != "" {
			key = v
		}
	}

	// --- 1b. Phase 7: rate limit (D4) → shedding (D7) → budget retry (D6) -----
	// Sau khi đọc xong head (conn Slowloris không bao giờ tới đây ⇒ không giành
	// slot, G3), trước Pick (request bị từ chối không tốn gì của upstream).
	ok, keep, release := s.admit(c, bw, req, clientIP)
	if !ok {
		return keep
	}
	// Phase 8 (D2/D3): vhost — TLS theo SNI và Host phải khớp, plaintext theo Host.
	bl, status := s.balancerFor(st, req)
	if bl == nil {
		release()
		keep := s.drain(c, req) && !req.Close
		s.writeError(c, bw, status, "authority không thuộc vhost của connection này", keep)
		return keep
	}
	defer release() // I7: slot trả trên MỌI đường ra
	s.res.budget.Deposit(time.Now())

	// --- 2. Chọn backend (phase 6) + connection upstream: pool hoặc dial ------
	// attempt đếm số lần exchange trên CÙNG backend (D4 phase 5: retry một lần,
	// lần hai dialNew). repicked: đã đổi backend một lần vì dial lỗi (D9).
	var be *lb.Backend
	var p *pool
	var start time.Time
	repicked := false
	var failed *lb.Backend // P7-1: backend vừa dial lỗi — lượt chọn lại D9 loại nó
	for attempt := 0; ; attempt++ {
		if attempt == 0 {
			if failed != nil {
				be = bl.PickExcept(key, failed)
				if be == nil {
					// Không còn backend nào KHÁC ⇒ 502 ngay, không dial lại cái vừa lỗi.
					keep := s.drain(c, req) && !req.Close
					s.writeError(c, bw, 502, "không dial được upstream", keep)
					return keep
				}
			} else {
				be = bl.Pick(key)
			}
			if be == nil {
				// D8: không còn backend nào dùng được ⇒ 503 (không phải 502),
				// chưa đụng body ⇒ drain, giữ client.
				s.cfg.Logf("proxy: không còn backend nào available (%d upstream)", len(s.cfg.Upstreams))
				keep := s.drain(c, req) && !req.Close
				s.writeError(c, bw, 503, "không còn backend nào sống", keep)
				return keep
			}
			start = time.Now()
			p = s.poolFor(be.Addr)
			if noHost {
				up.Header.Set("Host", be.Addr)
			}
		}
		var pc *pooledConn
		var err error
		if attempt == 0 {
			pc, err = p.get()
		} else {
			pc, err = p.dialNew() // D4: lần hai luôn dial mới
		}
		if err != nil {
			s.cfg.Logf("proxy: dial %s: %v", be.Addr, err)
			bl.Done(be, time.Since(start), true)
			if !repicked && s.allowRetry() {
				// D9: dial lỗi = chưa gửi byte nào ⇒ chọn backend khác đúng một
				// lần, bất kể request có body (khác D4: body vẫn còn nguyên trong br).
				// Phase 7 D6: chỉ khi còn retry budget. Phase 8 turn 1: cả khi dial
				// lỗi ở lần THỨ HAI (D4: connection reused chết ⇒ dialNew cùng
				// backend ⇒ backend vừa chết ⇒ refused) — trước đây trả 502 dù chưa
				// byte nào đi đâu; lộ ra dưới -race ở TestLBKillRevive.
				repicked = true
				failed = be
				attempt = -1
				continue
			}
			// D6 phase 3: chưa đụng body ⇒ drain rồi 502, connection client giữ được.
			keep := s.drain(c, req) && !req.Close
			s.writeError(c, bw, 502, "không dial được upstream", keep)
			return keep
		}
		st.up.Store(&pc.c) // Close cưỡng bức đóng được cả chiều upstream
		// P6-2 (2026-10-02): EWMA đo thời gian BACKEND trả lời, tính từ khi có
		// connection — không tính dial. Mẫu đầu mỗi connection gồm dial (≈ 1 ms,
		// gấp vài lần trả lời), tau 1 s ⇒ thứ hạng giây đầu là may rủi (P2C
		// 15/28/29/28 % trên 4 node giống hệt nhau). Dial lỗi vẫn Done(start) ở trên.
		xstart := time.Now()
		keep, retry, upFail := s.exchange(c, br, bw, req, up, pc)
		st.up.Store(nil)
		if !retry {
			bl.Done(be, time.Since(xstart), upFail) // D2: Done TRƯỚC khi request kế đến, SAU put
			return keep
		}
		// D4 (a)(b)(c) đã thoả trong exchange. Chỉ một lần, cùng backend, và
		// (phase 7 D6) chỉ khi còn retry budget — hết budget ⇒ 502 như lần hai.
		if attempt == 0 && s.allowRetry() {
			p.retries.Add(1)
			s.cfg.Logf("proxy: %s đóng connection rỗi trước khi nhận request, retry một lần", be.Addr)
			continue
		}
		bl.Done(be, time.Since(start), true)
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

// exchange: bước 3-5 trên MỘT connection upstream pc. Trả (keep, retry, upFail):
// retry=true ⇔ chưa ghi gì cho client và D4 cho phép thử lại; khi đó caller
// quyết. upFail=true ⇔ lỗi thuộc về UPSTREAM (transport hoặc 5xx) — nuôi
// outlier ejection (phase 6 D7); client bỏ đi giữa body KHÔNG tính cho upstream.
// Mọi đường ra đều qua release: pc về pool chỉ khi clean (D2), còn lại đóng.
func (s *Server) exchange(c net.Conn, br *bufio.Reader, bw *bufio.Writer, req, up *httpx.Request, pc *pooledConn) (keep, retry, upFail bool) {
	lim := s.cfg.Limits
	uc, ubr, ubw := pc.c, pc.br, pc.bw
	pc.in.n = 0
	clean, headOK := false, false
	pool := pc.p
	defer func() {
		if !poolCheckClean && headOK {
			clean = true // nodefense: về pool ngay khi có head — bẫy phase 5
		}
		if clean {
			pool.put(pc)
		} else {
			pool.discard(pc)
		}
	}()

	// --- 3. Head + body sang upstream ---------------------------------------
	uc.SetWriteDeadline(time.Now().Add(s.cfg.UpstreamBodyTimeout))
	c.SetReadDeadline(time.Now().Add(lim.BodyTimeout)) // I3: body client có deadline
	writeErr := up.WriteHead(ubw)
	var readErr error
	hasBody := req.ContentLength != 0 || req.Chunked
	var early *httpx.Response // P4-5: status final upstream trả TRƯỚC khi nhận body
	if writeErr == nil && hasBody && expect100(req) && br.Buffered() == 0 {
		var cerr error
		early, cerr, writeErr = s.awaitContinue(bw, uc, ubr, ubw, req.Method)
		if cerr != nil {
			return false, false, false // client đi mất khi nhận 100
		}
		if writeErr != nil {
			// Body chưa đọc khỏi br ⇒ không giữ client; không retry (D4 c).
			s.cfg.Logf("proxy: chờ 100-continue: %v", writeErr)
			s.writeError(c, bw, 502, "upstream hỏng khi chờ 100-continue", false)
			return false, false, true
		}
	}
	if writeErr == nil && early == nil {
		// req.Body là stream ĐÚNG ranh giới (lengthReader / chunkedReader /
		// eofReader) trên br — nó trả io.EOF khi hết body, KHÔNG phải khi
		// client đóng. Đây là "một dòng" tránh bẫy #1: không bao giờ
		// io.Copy(upstream, c).
		if hasBody {
			// Phase 9 turn 3 (P9-3): GET không body không cần buffer 32 KiB.
			readErr, writeErr = copyBodyT(ubw, req.Body, req.Chunked, req.Trailer)
		}
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
		var pe *httpx.ProtoError
		switch {
		case isTimeout(readErr):
			status, detail = 408, "quá BodyTimeout khi đọc body"
		case errors.As(readErr, &pe):
			// P4-5: lỗi framing giữa body (chunk-size bẩn 400, chunk-ext quá
			// MaxLineBytes 431) giữ status của parser — giống lỗi ở head.
			status, detail = pe.Status, pe.Reason
		}
		s.writeError(c, bw, status, detail, false)
		return false, false, false
	case writeErr != nil:
		if canRetry(pc, req) {
			return false, true, false // D4: ghi lỗi ⇒ chắc chắn 0 byte response
		}
		s.cfg.Logf("proxy: ghi sang upstream: %v", writeErr)
		keep := drained && !req.Close
		s.writeError(c, bw, 502, "upstream đóng khi đang nhận request", keep)
		return keep, false, true
	}

	// --- 4. Head response từ upstream ---------------------------------------
	resp := early
	var err error
	for resp == nil {
		uc.SetReadDeadline(time.Now().Add(s.cfg.UpstreamHeaderTimeout))
		r, err := httpx.ReadResponse(ubr, lim, req.Method)
		if err != nil {
			// D4 (b): 0 byte response đã tới VÀ không phải timeout (timeout với
			// 0 byte = upstream sống nhưng chậm, có thể đang xử lý ⇒ không
			// idempotent nữa ⇒ 504, không retry).
			if pc.in.n == 0 && !isTimeout(err) && canRetry(pc, req) {
				return false, true, false
			}
			status, detail := 502, "upstream trả response không hợp lệ hoặc đóng sớm"
			if isTimeout(err) {
				status, detail = 504, "upstream không trả head trong UpstreamHeaderTimeout"
			}
			s.cfg.Logf("proxy: đọc response: %v", err)
			keep := drained && !req.Close
			s.writeError(c, bw, status, detail, keep)
			return keep, false, true
		}
		if r.Status/100 != 1 {
			resp = r
			break
		}
		// D7 phase 3: 1xx là interim — bỏ, đọc response kế. 101 là đổi giao
		// thức: không tunnel, trả 502 (chưa gửi gì cho client) và đóng.
		if r.Status == 101 {
			s.writeError(c, bw, 502, "upstream đòi Upgrade (101), chưa hỗ trợ", false)
			return false, false, false
		}
	}
	headOK = true
	// D7 phase 6: node "sống nhưng trả 5xx" — mỗi 5xx là một lỗi cho outlier.
	upFail = resp.Status >= 500

	// --- 5. Head + body về client -------------------------------------------
	out := &httpx.Response{Proto: "HTTP/1.1", Status: resp.Status, Reason: resp.Reason, Header: resp.Header.Clone()}
	out.Header.StripHopByHop()
	// D8: đang drain ⇒ response này là cái cuối. P4-5: early ⇒ body client
	// chưa đọc còn trên br ⇒ đóng (không drain body client chưa gửi).
	closeClient := req.Close || s.draining.Load() || early != nil
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
		return false, false, upFail
	}
	// Từ đây head đã đi: mọi lỗi đều phải đóng, không có "response thứ hai".
	bodyDone := mode == modeNone // NoBody: không có gì để đọc ⇒ đã "hết"
	upBodyErr := false           // lỗi body do UPSTREAM (rerr), không do client (werr)
	if rawCopyResponse {
		// Bẫy #2 (chỉ khi -tags nodefense): chép tới khi upstream đóng. Với
		// upstream keep-alive thì treo tới deadline. Xem TestRawCopyTrap.
		_, err = io.Copy(bw, ubr)
	} else {
		var rerr, werr error
		switch mode {
		case modeCopy:
			var spliced bool
			if spliced, rerr, werr = s.spliceBody(c, bw, resp, ubr, pc); !spliced {
				rerr, werr = copyBody(bw, resp.Body, false)
			}
		case modeChunked:
			// P3-1: trailer upstream (đã qua checkTrailer phía parser) về client.
			// Body tới-EOF đổi sang chunked thì resp.Trailer() == nil ⇒ chunk cuối trần.
			rerr, werr = copyBodyT(bw, resp.Body, true, resp.Trailer)
		}
		bodyDone = bodyDone || (rerr == nil && werr == nil) // resp.Body đã trả io.EOF
		if rerr != nil {
			err = rerr
			upBodyErr = true
		} else {
			err = werr
		}
	}
	if err != nil {
		// Upstream hỏng giữa body (rerr) HAY client bỏ đi giữa body (werr): cả
		// hai đều để lại byte chưa đọc trên connection upstream ⇒ bẩn (D2 e).
		s.cfg.Logf("proxy: body response: %v", err)
		return false, false, upFail || upBodyErr
	}
	if err := bw.Flush(); err != nil {
		return false, false, upFail
	}

	// D2: SẠCH ⇔ body đã EOF (b) ∧ không byte thừa (c) ∧ upstream không đòi
	// đóng / không phải body-tới-EOF (d) ∧ không lỗi (e, đã return ở trên).
	// P4-5: early ⇒ upstream đã nhận head có CL/chunked mà không có body ⇒ bẩn.
	clean = bodyDone && ubr.Buffered() == 0 && !resp.Close && early == nil
	return !closeClient, false, upFail
}

// expect100: request có "Expect: 100-continue" (RFC 9110 §10.1.1: so khớp
// không phân biệt hoa thường).
func expect100(req *httpx.Request) bool {
	return strings.EqualFold(req.Header.Get("Expect"), "100-continue")
}

// expectWait: chờ upstream trả 100 bao lâu trước khi cứ chuyển body. Bằng
// ExpectContinueTimeout thường dùng của client (curl 1 s): upstream im lặng
// (HTTP/1.0) thì client cũng sẽ tự gửi body sau chừng đó.
const expectWait = time.Second

// awaitContinue (P4-5): head đã ghi vào ubw, body chưa. Flush rồi chờ upstream:
//
//	100            → chuyển "100 Continue" cho client, trả (nil, nil, nil): gửi body
//	1xx khác       → bỏ (D7 phase 3), chờ tiếp
//	status final   → trả nó làm early: KHÔNG gửi body, response này là response
//	im lặng expectWait (0 byte) → (nil, nil, nil): cứ chuyển body
//	lỗi / 101 / head bẩn → uerr (lỗi upstream)
//
// cerr: lỗi ghi về client.
//
// RFC 9110 §10.1.1 chỉ cho proxy TỰ sinh 100 khi tin server kế là HTTP/1.0 ⇒
// ta không tự sinh, chỉ chuyển tiếp 100 của upstream.
func (s *Server) awaitContinue(bw *bufio.Writer, uc net.Conn, ubr *bufio.Reader, ubw *bufio.Writer, method string) (early *httpx.Response, cerr, uerr error) {
	if err := ubw.Flush(); err != nil {
		return nil, nil, err
	}
	deadline := time.Now().Add(expectWait)
	for {
		uc.SetReadDeadline(deadline)
		if _, err := ubr.Peek(1); err != nil {
			if isTimeout(err) && ubr.Buffered() == 0 {
				return nil, nil, nil
			}
			return nil, nil, err
		}
		uc.SetReadDeadline(time.Now().Add(s.cfg.UpstreamHeaderTimeout))
		r, err := httpx.ReadResponse(ubr, s.cfg.Limits, method)
		switch {
		case err != nil:
			return nil, nil, err
		case r.Status == 100:
			if _, err := io.WriteString(bw, "HTTP/1.1 100 Continue\r\n\r\n"); err != nil {
				return nil, err, nil
			}
			return nil, bw.Flush(), nil
		case r.Status == 101:
			return nil, nil, errors.New("upstream trả 101 khi chờ 100-continue")
		case r.Status/100 == 1:
			continue
		default:
			return r, nil, nil
		}
	}
}

// copyBody chép src (đã framing đúng ranh giới) vào dst, Flush sau mỗi lần
// Read để response streaming (SSE, chunked chậm) không bị bufio gom lại —
// nếu gom, G4 (Nagle) không đo được và client thấy từng chunk đến trễ.
// Trả riêng lỗi đọc và lỗi ghi: caller phân biệt "client/upstream nguồn
// hỏng" với "đích hỏng" để quyết keep-alive.
func copyBody(dst *bufio.Writer, src io.Reader, chunked bool) (readErr, writeErr error) {
	return copyBodyT(dst, src, chunked, nil)
}

// copyBodyT: như copyBody; chunked ⇒ trailer() (gọi SAU khi src EOF — trailer
// chỉ có sau chunk cuối) được ghi kèm chunk cuối (P3-1, trả 2026-10-02).
func copyBodyT(dst *bufio.Writer, src io.Reader, chunked bool, trailer func() httpx.Header) (readErr, writeErr error) {
	var w io.Writer = dst
	var cw *httpx.ChunkedWriter
	if chunked {
		cw = httpx.NewChunkedWriter(dst)
		w = cw
	}
	bp := getCopyBuf() // D1: 32 KiB mỗi response trước phase 9
	defer putCopyBuf(bp)
	buf := *bp
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
		var tr httpx.Header
		if trailer != nil {
			tr = trailer()
		}
		if werr := cw.CloseWithTrailer(tr); werr != nil {
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
//
// Trả về IP client "thật" theo ranh giới đó: peer tin ⇒ phần tử ĐẦU của XFF
// (nếu có), không thì peer. Phase 6 dùng làm khoá consistent hash (D5).
func (s *Server) forwardedHeaders(h httpx.Header, addr net.Addr) (clientIP string) {
	ipStr := addr.String()
	if host, _, err := net.SplitHostPort(ipStr); err == nil {
		ipStr = host
	}
	ip := net.ParseIP(ipStr)
	if ip != nil && s.cfg.isTrusted(ip) {
		vals := h.Values("X-Forwarded-For")
		xff := ipStr
		clientIP = ipStr
		if len(vals) > 0 {
			xff = strings.Join(vals, ", ") + ", " + ipStr
			if first, _, _ := strings.Cut(vals[0], ","); strings.TrimSpace(first) != "" {
				clientIP = strings.TrimSpace(first)
			}
		}
		h.Set("X-Forwarded-For", xff)
		return clientIP
	}
	h.Set("X-Forwarded-For", ipStr)
	h.Set("X-Real-Ip", ipStr)
	h.Del("Forwarded")
	return ipStr
}
