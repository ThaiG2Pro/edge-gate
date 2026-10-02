package proxy

import (
	"bufio"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/thaivro/edgegate/internal/h2"
	"github.com/thaivro/edgegate/internal/h2/hpack"
	"github.com/thaivro/edgegate/internal/httpx"
)

// Phase 10 D7-D8: h2c prior knowledge trên listener plaintext, mỗi stream
// forward xuống upstream HTTP/1.1 qua pool cũ. Phía client là h2, phía
// upstream KHÔNG đổi — đúng mô hình nginx (`http2 on;` + proxy_pass http/1.1).

// isH2Preface (D7): byte đầu đã có trong br. So TỪNG PHẦN với preface để một
// request h1 ngắn bắt đầu bằng 'P' (POST/PUT/PATCH, < 24 byte) không bị Peek(24)
// chặn tới HeaderTimeout: chỉ đọc thêm khi mọi byte tới giờ còn khớp preface.
func isH2Preface(br *bufio.Reader) bool {
	n := 1
	for {
		b, err := br.Peek(n)
		if err != nil || !strings.HasPrefix(h2.Preface, string(b)) {
			return false
		}
		if n == len(h2.Preface) {
			return true
		}
		n = max(n+1, min(br.Buffered(), len(h2.Preface)))
	}
}

// serveH2: connection đã gửi preface. Đọc tiếp qua CÙNG br (preface và có thể
// cả SETTINGS đã nằm trong buffer).
func (s *Server) serveH2(c net.Conn, st *connState, br *bufio.Reader) {
	cfg := s.cfg.H2
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = s.cfg.Limits.IdleTimeout
	}
	if cfg.HeaderTimeout == 0 {
		cfg.HeaderTimeout = s.cfg.Limits.HeaderTimeout
	}
	if cfg.BodyTimeout == 0 {
		cfg.BodyTimeout = s.cfg.Limits.BodyTimeout
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = s.cfg.Limits.BodyTimeout
	}
	if cfg.MaxHeaderListSize == 0 {
		cfg.MaxHeaderListSize = uint32(s.cfg.Limits.MaxHeaderBytes)
	}
	if cfg.Logf == nil {
		cfg.Logf = s.cfg.Logf
	}
	s.h2conns.Add(1)
	hc := h2.NewConn(c, br, cfg, func(w *h2.Stream, r *h2.Request) { s.serveH2Stream(c, st, w, r) }, &s.h2stats)
	st.h2.Store(hc)
	// Drain đã quét trước khi Store ⇒ tự Shutdown (cặp Dekker như idle/closeIdle).
	if s.closeIdle.Load() {
		hc.Shutdown()
	}
	hc.Serve()
}

// H2Stats: bộ đếm h2 cộng dồn mọi connection (test, lab).
func (s *Server) H2Stats() *h2.Stats { return &s.h2stats }

// h2Error: response lỗi do proxy sinh (chưa gửi head nào cho stream). extra:
// field thêm (retry-after cho 429/503).
func h2Error(w *h2.Stream, status int, detail string, extra ...hpack.HeaderField) {
	body := strconv.Itoa(status) + " " + reasonPhrase(status) + ": " + detail + "\n"
	w.WriteHeaders(status, append([]hpack.HeaderField{
		{Name: "content-type", Value: "text/plain; charset=utf-8"},
		{Name: "content-length", Value: strconv.Itoa(len(body))},
	}, extra...), false)
	w.WriteData([]byte(body), true)
}

// toUpstream: Request h2 → head h1 cho upstream. Trả cả bản httpx.Request
// "giả" (Header có Host) để dùng lại balancerFor / XFF như h1.
func toUpstream(r *h2.Request) *httpx.Request {
	h := httpx.Header{}
	for _, f := range r.Header {
		h.Add(f.Name, f.Value)
	}
	// §8.3.1: :authority là nguồn chuẩn; Host (nếu có) phải trùng — ta lấy
	// :authority khi có, để upstream và vhost thấy cùng một tên.
	if r.Authority != "" {
		h.Set("Host", r.Authority)
	}
	h.StripHopByHop() // te: trailers và mọi thứ nodefense10 để lọt
	up := &httpx.Request{Method: r.Method, Target: r.Path, Proto: "HTTP/1.1", Header: h, ContentLength: r.ContentLength}
	switch {
	case r.EndStream:
		up.ContentLength = 0
		if r.ContentLength < 0 && methodHasBody(r.Method) {
			h.Set("Content-Length", "0")
		}
	case r.ContentLength < 0:
		// Không khai báo độ dài và body chưa hết: ranh giới h2 là END_STREAM,
		// chặng h1 phải dựng lại ranh giới bằng chunked (D8).
		up.Chunked = true
		h.Set("Transfer-Encoding", "chunked")
	}
	return up
}

func methodHasBody(m string) bool { return m == "POST" || m == "PUT" || m == "PATCH" }

func (s *Server) serveH2Stream(c net.Conn, st *connState, w *h2.Stream, r *h2.Request) {
	up := toUpstream(r)
	if !up.Header.Has("Host") {
		h2Error(w, 400, "thiếu :authority và host")
		return
	}
	orig := up.Header // rateKey (nodefense7) đọc XFF CLIENT gửi — trước khi forwardedHeaders ghi đè
	if !limitByTrustedIP {
		orig = up.Header.Clone()
	}
	clientIP := s.forwardedHeaders(up.Header, c.RemoteAddr())
	// P10-2: rate limit + shed như h1 (roundTrip bước 1b) — sau head, trước Pick.
	admitStatus, why, release := s.admitDecision(clientIP, orig)
	if admitStatus != 0 {
		h2Error(w, admitStatus, why, hpack.HeaderField{Name: "retry-after", Value: "1"})
		return
	}
	defer release() // I7
	key := clientIP
	if hh := s.cfg.LB.HashHeader; hh != "" {
		if v := up.Header.Get(hh); v != "" {
			key = v
		}
	}
	bl, status := s.balancerFor(st, up)
	if bl == nil {
		h2Error(w, status, "authority không thuộc vhost của connection này")
		return
	}
	s.res.budget.Deposit(time.Now())
	be := bl.Pick(key)
	if be == nil {
		h2Error(w, 503, "không còn backend nào sống")
		return
	}
	start := time.Now()
	p := s.poolFor(be.Addr)
	hasBody := !r.EndStream
	for attempt := 0; ; attempt++ {
		var pc *pooledConn
		var err error
		if attempt == 0 {
			pc, err = p.get()
		} else {
			pc, err = p.dialNew()
		}
		if err != nil {
			s.cfg.Logf("proxy: h2 dial %s: %v", be.Addr, err)
			bl.Done(be, time.Since(start), true)
			h2Error(w, 502, "không dial được upstream")
			return
		}
		retry, upFail := s.h2exchange(w, r, up, pc, hasBody)
		if !retry {
			bl.Done(be, time.Since(start), upFail)
			return
		}
		if attempt == 0 && s.allowRetry() {
			p.retries.Add(1)
			continue
		}
		bl.Done(be, time.Since(start), true)
		h2Error(w, 502, "upstream đóng khi đang nhận request (đã retry)")
		return
	}
}

// h2exchange: một lần trao đổi trên pc. retry=true ⇔ chưa gửi gì cho client,
// conn là đồ dùng lại, request không body, 0 byte response (D4 phase 5).
func (s *Server) h2exchange(w *h2.Stream, r *h2.Request, up *httpx.Request, pc *pooledConn, hasBody bool) (retry, upFail bool) {
	lim := s.cfg.Limits
	uc, ubr, ubw := pc.c, pc.br, pc.bw
	pc.in.n = 0
	clean := false
	defer func() {
		w.SetCancel(nil)
		if clean {
			pc.p.put(pc)
		} else {
			pc.p.discard(pc)
		}
	}()
	// Client RST / connection h2 chết ⇒ đóng upstream ⇒ mọi Read/Write bên dưới
	// trả lỗi ngay (Rapid Reset không giữ connection upstream tới timeout).
	if !w.SetCancel(func() { uc.Close() }) {
		return false, false
	}
	canRetry := pc.reused && !hasBody

	uc.SetWriteDeadline(time.Now().Add(s.cfg.UpstreamBodyTimeout))
	err := up.WriteHead(ubw)
	var readErr error
	if err == nil && hasBody {
		readErr, err = s.h2copyRequestBody(ubw, w, up)
	}
	if err == nil {
		err = ubw.Flush()
	}
	switch {
	case readErr != nil:
		// Client hỏng body (reset, timeout, malformed CL): connection upstream
		// đã nhận nửa request ⇒ bẩn. Chưa gửi head ⇒ còn trả được lỗi nếu
		// stream chưa bị reset.
		if errors.Is(readErr, h2.ErrBodyTimeout) {
			h2Error(w, 408, "quá BodyTimeout khi đọc body")
		} else if errors.Is(readErr, errBodyOverCL) {
			h2Error(w, 400, "body dài hơn content-length")
		}
		return false, false
	case err != nil:
		if canRetry {
			return true, false
		}
		h2Error(w, 502, "upstream đóng khi đang nhận request")
		return false, true
	}

	var resp *httpx.Response
	for {
		uc.SetReadDeadline(time.Now().Add(s.cfg.UpstreamHeaderTimeout))
		resp, err = httpx.ReadResponse(ubr, lim, r.Method)
		if err != nil {
			if pc.in.n == 0 && !isTimeout(err) && canRetry {
				return true, false
			}
			select {
			case <-w.Done():
				return false, false // client huỷ: lỗi không thuộc upstream
			default:
			}
			if isTimeout(err) {
				h2Error(w, 504, "upstream không trả head trong UpstreamHeaderTimeout")
			} else {
				h2Error(w, 502, "upstream trả response không hợp lệ hoặc đóng sớm")
			}
			return false, true
		}
		if resp.Status/100 != 1 {
			break
		}
		if resp.Status == 101 {
			h2Error(w, 502, "upstream đòi Upgrade (101), h2 không có Upgrade")
			return false, false
		}
	}
	upFail = resp.Status >= 500

	out := resp.Header.Clone()
	out.StripHopByHop()
	fields := h2Fields(out)
	noBody := httpx.NoBody(r.Method, resp.Status)
	uc.SetReadDeadline(time.Now().Add(s.cfg.UpstreamBodyTimeout))
	if err := w.WriteHeaders(resp.Status, fields, noBody); err != nil {
		return false, upFail
	}
	if noBody {
		clean = ubr.Buffered() == 0 && !resp.Close
		return false, upFail
	}
	bp := getCopyBuf()
	defer putCopyBuf(bp)
	buf := (*bp)[:16<<10] // một DATA frame tối đa (peer MAX_FRAME_SIZE mặc định)
	var rerr error
	for {
		n, e := resp.Body.Read(buf)
		if n > 0 {
			if werr := w.WriteData(buf[:n], false); werr != nil {
				return false, upFail // client bỏ đi / window timeout: upstream bẩn
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			rerr = e
			break
		}
	}
	if rerr != nil {
		s.cfg.Logf("proxy: h2 body response: %v", rerr)
		w.Reset(h2.ErrInternal)
		return false, true
	}
	// P10-7: trailer upstream (chunked) ⇒ HEADERS cuối END_STREAM; không có ⇒ DATA rỗng END_STREAM.
	var endErr error
	if tr := resp.Trailer(); len(tr) > 0 {
		endErr = w.WriteTrailers(h2Fields(tr))
	} else {
		endErr = w.End()
	}
	if endErr != nil {
		return false, upFail
	}
	clean = ubr.Buffered() == 0 && !resp.Close
	return false, upFail
}

// h2Fields: header/trailer h1 → field h2. Tên lowercase (§8.2.1); value đã qua
// parser h1 (không CR/LF). Sensitive giữ cho set-cookie (không index).
func h2Fields(h httpx.Header) []hpack.HeaderField {
	fields := make([]hpack.HeaderField, 0, len(h)+1)
	for k, vs := range h {
		name := strings.ToLower(k)
		for _, v := range vs {
			fields = append(fields, hpack.HeaderField{Name: name, Value: v, Sensitive: name == "set-cookie"})
		}
	}
	return fields
}

var errBodyOverCL = errors.New("proxy: body h2 dài hơn content-length")

// h2copyRequestBody: body stream → upstream. CL khai báo ⇒ chép ĐÚNG CL byte
// rồi đòi EOF (D6 c, lớp hai — lớp một là h2 RST trước khi byte vượt CL vào
// buffer). nodefense10 tắt cả hai ⇒ chép mọi DATA, upstream thấy request thứ
// hai (G6). Không CL ⇒ chunked.
func (s *Server) h2copyRequestBody(ubw *bufio.Writer, src io.Reader, up *httpx.Request) (readErr, writeErr error) {
	if up.Chunked {
		// P10-7: trailer request h2 ⇒ trailer chunked sang upstream; field cấm
		// (§6.5.1) bị bỏ — h2 đã loại pseudo/CRLF (checkTrailers). CL ⇒ h1 không
		// chở được trailer ⇒ bỏ (ghi ở sổ nợ).
		trailer := func() httpx.Header {
			st, ok := src.(*h2.Stream)
			if !ok {
				return nil
			}
			var h httpx.Header
			for _, f := range st.Trailer() {
				if httpx.ForbiddenTrailer(f.Name) {
					continue
				}
				if h == nil {
					h = httpx.Header{}
				}
				h.Add(f.Name, f.Value)
			}
			return h
		}
		return copyBodyT(ubw, src, true, trailer)
	}
	if !h2LimitBodyToCL || up.ContentLength < 0 {
		return copyBody(ubw, src, false)
	}
	readErr, writeErr = copyBody(ubw, io.LimitReader(src, up.ContentLength), false)
	if readErr != nil || writeErr != nil {
		return
	}
	var one [1]byte
	if n, err := src.Read(one[:]); n > 0 {
		return errBodyOverCL, nil
	} else if err != nil && err != io.EOF {
		return err, nil
	}
	return nil, nil
}
