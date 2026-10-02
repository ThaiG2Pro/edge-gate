package h2

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thaivro/edgegate/internal/h2/hpack"
)

// Config của một Server h2. Zero value được điền mặc định.
type Config struct {
	MaxConcurrentStreams uint32 // mặc định 100
	// InitialWindow: SETTINGS_INITIAL_WINDOW_SIZE của ta = byte body mỗi
	// stream được đệm tối đa (D5, I2). ConnWindow: window connection của ta
	// (chỉ đổi được bằng WINDOW_UPDATE trên stream 0). Mặc định 65 535 cả hai.
	InitialWindow uint32
	ConnWindow    uint32
	// MaxHeaderListSize: SETTINGS_MAX_HEADER_LIST_SIZE; cũng là trần header
	// block đệm qua CONTINUATION (D6 b). Mặc định 64 KiB.
	MaxHeaderListSize uint32

	IdleTimeout   time.Duration // không stream nào mở ⇒ đóng sau ngần này (mặc định 60 s)
	HeaderTimeout time.Duration // giữa HEADERS và END_HEADERS (mặc định 10 s)
	// WriteTimeout: mỗi lần ghi frame, VÀ mỗi lần chờ window để gửi DATA
	// (client không bao giờ WINDOW_UPDATE = Slowloris phía đọc). Mặc định 30 s.
	WriteTimeout time.Duration
	// BodyTimeout: toàn bộ body request của một stream (I3 — đọc body không có
	// deadline socket vì connection còn phục vụ stream khác). Mặc định 30 s.
	BodyTimeout time.Duration

	Logf func(format string, args ...any)
}

func (c *Config) withDefaults() {
	if c.MaxConcurrentStreams == 0 {
		c.MaxConcurrentStreams = 100
	}
	if c.InitialWindow == 0 {
		c.InitialWindow = defaultInitWindow
	}
	if c.ConnWindow == 0 {
		c.ConnWindow = defaultInitWindow
	}
	if c.MaxHeaderListSize == 0 {
		c.MaxHeaderListSize = 64 << 10
	}
	if c.IdleTimeout == 0 {
		c.IdleTimeout = 60 * time.Second
	}
	if c.HeaderTimeout == 0 {
		c.HeaderTimeout = 10 * time.Second
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = 30 * time.Second
	}
	if c.BodyTimeout == 0 {
		c.BodyTimeout = 30 * time.Second
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
}

// Handler phục vụ MỘT stream trong goroutine riêng (D3). Trả về là xong
// stream: chưa gửi END_STREAM thì Conn RST_STREAM (INTERNAL_ERROR).
type Handler func(w *Stream, r *Request)

// Stats: bộ đếm một connection (test, lab).
type Stats struct {
	Streams, Refused, Resets, ResetsSent atomic.Int64
	MaxActive                            atomic.Int64 // đỉnh handler chạy đồng thời
	HeaderBlockMax                       atomic.Int64 // header block đệm lớn nhất (G7)
}

// Conn: một connection h2 phía server.
type Conn struct {
	cfg     Config
	nc      net.Conn
	fr      *Framer
	dec     *hpack.Decoder
	handler Handler
	Stats   *Stats

	wmu sync.Mutex // D3: encode + ghi frame
	enc *hpack.Encoder

	mu         sync.Mutex
	cond       *sync.Cond
	streams    map[uint32]*Stream
	active     int   // slot đang dùng (D6 a)
	sendWindow int64 // window connection phía GỬI (peer cho)
	recvWindow int64 // window connection phía NHẬN còn lại (ta cho)
	peerInit   int64 // SETTINGS_INITIAL_WINDOW_SIZE của peer
	peerFrame  uint32
	lastID     uint32
	goaway     bool // peer gửi GOAWAY hoặc ta đang đóng: không nhận stream mới
	draining   bool // ta đã gửi GOAWAY NO_ERROR (Shutdown): stream cuối xong ⇒ đóng
	closed     bool
	wg         sync.WaitGroup

	// localReset: id stream TA đã RST và đã rời map (handler thoát) — frame
	// client gửi trước khi thấy RST vẫn có thể tới (§5.1 closed: "minimally
	// process and then discard"). Có trần (FIFO) — I2.
	localReset  map[uint32]struct{}
	localResetQ []uint32

	// D6 a′: RST của client trong cửa sổ 1 s (Rapid Reset ở tầng tốc độ).
	rstWindow time.Time
	rstCount  int

	// header block đang ghép (HEADERS chưa END_HEADERS).
	hbStream uint32
	hbEnd    bool
	hb       []byte
}

// Stream: một stream. Phía handler: Write*, Body, Done.
type Stream struct {
	id   uint32
	c    *Conn
	done chan struct{} // đóng khi stream bị reset / connection chết

	// dưới c.mu
	sendWindow int64
	recvWindow int64
	body       []byte // DATA đã nhận, handler chưa đọc (≤ InitialWindow)
	bodyEOF    bool
	bodyErr    error
	declCL     int64 // content-length khai báo, -1 = không
	recvd      int64
	remoteDone bool // END_STREAM từ client
	reset      bool
	peerReset  bool // RST đến TỪ CLIENT (khác: ta RST) — §5.1 closed
	exited     bool // handler đã thoát
	slotFreed  bool // nodefense10: slot đã trả lúc RST
	bodyTimer  *time.Timer
	cancel     func() // SetCancel: gọi (dưới c.mu, không được chặn) lúc reset

	// chỉ goroutine handler
	wroteHeaders, ended bool
}

var errStreamReset = errors.New("h2: stream bị reset")
var errConnClosed = errors.New("h2: connection đã đóng")

// ErrBodyTimeout / ErrWindowTimeout: hết BodyTimeout / WriteTimeout của stream.
var ErrBodyTimeout = errors.New("h2: quá BodyTimeout khi đọc body stream")
var ErrWindowTimeout = errors.New("h2: chờ WINDOW_UPDATE quá WriteTimeout")

// ServeConn chạy connection h2 trên nc; r đọc byte (có thể là bufio đã Peek
// preface — D7: byte đã nằm trong buffer phải đọc qua CÙNG reader). Chặn tới
// khi connection kết thúc và mọi handler đã thoát.
func ServeConn(nc net.Conn, r io.Reader, cfg Config, h Handler, stats *Stats) error {
	return NewConn(nc, r, cfg, h, stats).Serve()
}

// Serve: như ServeConn, cho Conn tạo bằng NewConn (caller giữ con trỏ để Shutdown).
func (c *Conn) Serve() error {
	err := c.serve()
	c.shutdown()
	return err
}

// Shutdown (drain, turn 3): GOAWAY NO_ERROR với last-stream-id hiện tại —
// client biết stream nào ĐÃ được nhận (≤ last) và mở stream mới ở connection
// khác; stream đang chạy chạy tiếp; stream cuối xong ⇒ đóng connection. Gọi
// nhiều lần an toàn.
func (c *Conn) Shutdown() {
	c.mu.Lock()
	if c.draining || c.closed {
		c.mu.Unlock()
		return
	}
	c.draining, c.goaway = true, true
	last, idle := c.lastID, c.active == 0
	c.mu.Unlock()
	c.write(func(fr *Framer) error { return fr.WriteGoAway(last, ErrNo, "drain") })
	if idle {
		c.nc.SetReadDeadline(time.Now()) // đánh thức goroutine đọc ⇒ serve thoát
	}
}

// NewConn dựng Conn mà chưa chạy (Serve).
func NewConn(nc net.Conn, r io.Reader, cfg Config, h Handler, stats *Stats) *Conn {
	cfg.withDefaults()
	if stats == nil {
		stats = &Stats{}
	}
	c := &Conn{
		cfg: cfg, nc: nc, handler: h, Stats: stats,
		fr:         NewFramer(r, bufio.NewWriterSize(nc, 16<<10)),
		dec:        hpack.NewDecoder(4096, uint64(cfg.MaxHeaderListSize)),
		enc:        hpack.NewEncoder(),
		streams:    map[uint32]*Stream{},
		sendWindow: defaultInitWindow,
		recvWindow: int64(cfg.ConnWindow),
		peerInit:   defaultInitWindow,
		peerFrame:  defaultMaxFrame,
	}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *Conn) serve() error {
	c.nc.SetReadDeadline(time.Now().Add(c.cfg.HeaderTimeout))
	var pre [len(Preface)]byte
	if _, err := io.ReadFull(c.fr.r, pre[:]); err != nil {
		return err
	}
	if string(pre[:]) != Preface {
		return errors.New("h2: preface sai")
	}
	// SETTINGS của ta (§3.4: frame đầu tiên của server).
	err := c.write(func(fr *Framer) error {
		if err := fr.WriteSettings(
			Setting{SettingMaxConcurrentStreams, c.cfg.MaxConcurrentStreams},
			Setting{SettingInitialWindowSize, c.cfg.InitialWindow},
			Setting{SettingMaxHeaderListSize, c.cfg.MaxHeaderListSize},
		); err != nil {
			return err
		}
		if d := c.cfg.ConnWindow - defaultInitWindow; d > 0 {
			return fr.WriteWindowUpdate(0, d)
		}
		return nil
	})
	if err != nil {
		return err
	}
	first := true
	for {
		c.setReadDeadline()
		f, err := c.fr.ReadFrame()
		if err == nil && first && (f.Type != FrameSettings || f.Has(FlagAck)) {
			err = ConnError{ErrProtocol, "frame đầu tiên sau preface không phải SETTINGS"}
		}
		first = false
		if err == nil {
			err = c.processFrame(f)
		}
		if err == nil {
			continue
		}
		var se StreamError
		if errors.As(err, &se) {
			c.resetStream(se.Stream, se.Code)
			continue
		}
		var ce ConnError
		if errors.As(err, &ce) {
			c.cfg.Logf("h2: %v", ce)
			c.write(func(fr *Framer) error { return fr.WriteGoAway(c.lastIDLocked(), ce.Code, ce.Reason) })
			return ce
		}
		var hb hpackErr
		if errors.As(err, &hb) {
			c.write(func(fr *Framer) error { return fr.WriteGoAway(c.lastIDLocked(), ErrCompression, hb.Error()) })
			return err
		}
		return err // I/O: EOF, timeout
	}
}

type hpackErr struct{ error }

func (c *Conn) lastIDLocked() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastID
}

// setReadDeadline (I3): giữa header block ⇒ HeaderTimeout; không stream nào
// ⇒ IdleTimeout; có stream đang chạy ⇒ không deadline đọc (handler có
// timeout upstream riêng; khi stream cuối thoát, exitStream đặt lại Idle).
func (c *Conn) setReadDeadline() {
	switch {
	case c.hbStream != 0:
		c.nc.SetReadDeadline(time.Now().Add(c.cfg.HeaderTimeout))
	default:
		c.mu.Lock()
		n, dr := c.active, c.draining
		c.mu.Unlock()
		if n == 0 && dr {
			c.nc.SetReadDeadline(time.Now()) // Shutdown: hết stream ⇒ thoát
		} else if n == 0 {
			c.nc.SetReadDeadline(time.Now().Add(c.cfg.IdleTimeout))
		} else {
			c.nc.SetReadDeadline(time.Time{})
		}
	}
}

// write: một lần ghi frame dưới wmu, kèm deadline và Flush.
func (c *Conn) write(fn func(fr *Framer) error) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.nc.SetWriteDeadline(time.Now().Add(c.cfg.WriteTimeout))
	if err := fn(c.fr); err != nil {
		return err
	}
	return c.fr.Flush()
}

func (c *Conn) shutdown() {
	c.mu.Lock()
	c.closed = true
	c.goaway = true
	for _, s := range c.streams {
		s.markReset(errConnClosed)
	}
	c.cond.Broadcast()
	c.mu.Unlock()
	c.nc.Close() // handler đang chặn ghi thoát ra bằng lỗi
	c.wg.Wait()
}

// --- xử lý frame (goroutine đọc) ---------------------------------------

func (c *Conn) processFrame(f *Frame) error {
	// §6.10: đang ghép header block thì CHỈ được CONTINUATION cùng stream.
	if c.hbStream != 0 && (f.Type != FrameContinuation || f.Stream != c.hbStream) {
		return ConnError{ErrProtocol, fmt.Sprintf("%v giữa header block của stream %d", f.Type, c.hbStream)}
	}
	switch f.Type {
	case FrameData:
		return c.onData(f)
	case FrameHeaders:
		return c.onHeaders(f)
	case FrameContinuation:
		return c.onContinuation(f)
	case FramePriority:
		return c.onPriority(f)
	case FrameRSTStream:
		return c.onRST(f)
	case FrameSettings:
		return c.onSettings(f)
	case FramePushPromise:
		return ConnError{ErrProtocol, "client gửi PUSH_PROMISE"} // §8.4
	case FramePing:
		return c.onPing(f)
	case FrameGoAway:
		if f.Stream != 0 {
			return ConnError{ErrProtocol, "GOAWAY trên stream ≠ 0"}
		}
		if len(f.Payload) < 8 {
			return ConnError{ErrFrameSize, "GOAWAY ngắn"}
		}
		c.mu.Lock()
		c.goaway = true
		c.mu.Unlock()
		return nil
	case FrameWindowUpdate:
		return c.onWindowUpdate(f)
	}
	return nil // §5.5: loại lạ bị bỏ qua
}

// streamState cho một id KHÔNG có trong map: idle (chưa từng mở) hay closed.
func (c *Conn) isIdle(id uint32) bool { return id > c.lastID }

func (c *Conn) onData(f *Frame) error {
	if f.Stream == 0 {
		return ConnError{ErrProtocol, "DATA trên stream 0"}
	}
	data, err := stripPadding(f)
	if err != nil {
		return err
	}
	n := int64(len(f.Payload)) // flow control tính CẢ padding (§6.9.1)
	c.mu.Lock()
	if n > c.recvWindow {
		c.mu.Unlock()
		return ConnError{ErrFlowControl, fmt.Sprintf("DATA %d > window connection %d", n, c.recvWindow)}
	}
	c.recvWindow -= n
	s := c.streams[f.Stream]
	if s != nil && s.peerReset {
		// Client đã RST rồi vẫn gửi DATA: frame trên stream closed (§5.1).
		// Turn 2 (h2spec 5.1/8): bản đầu gộp với ca dưới và lặng lẽ bỏ qua.
		c.mu.Unlock()
		c.refundConn(n)
		return StreamError{f.Stream, ErrStreamClosed, "DATA sau RST_STREAM của client"}
	}
	if s != nil && s.reset && !s.remoteDone {
		// TA đã RST: frame client gửi trước khi thấy RST của ta là chuyện bình
		// thường (§5.1: "minimally process and then discard") — trả window
		// connection, KHÔNG RST thêm lần nữa.
		c.mu.Unlock()
		c.refundConn(n)
		return nil
	}
	if _, ok := c.localReset[f.Stream]; s == nil && ok {
		c.mu.Unlock()
		c.refundConn(n)
		return nil
	}
	if s == nil || s.remoteDone {
		idle := s == nil && c.isIdle(f.Stream)
		c.mu.Unlock()
		if idle {
			return ConnError{ErrProtocol, "DATA trên stream idle"}
		}
		// Stream đã đóng: phần window connection vẫn phải trả lại, không thì
		// mỗi DATA "lạc" thu hẹp connection vĩnh viễn.
		c.refundConn(n)
		return StreamError{f.Stream, ErrStreamClosed, "DATA trên stream đã đóng"}
	}
	if n > s.recvWindow {
		c.mu.Unlock()
		c.refundConn(n)
		return StreamError{f.Stream, ErrFlowControl, "DATA vượt window stream"}
	}
	s.recvWindow -= n
	s.recvd += int64(len(data))
	if validateDowngrade && s.declCL >= 0 && s.recvd > s.declCL {
		// D6 c / G6: byte vượt content-length KHÔNG BAO GIỜ vào body — với
		// proxy h2→h1 đó chính là request thứ hai trên connection upstream.
		c.mu.Unlock()
		c.refundConn(n)
		return malformed(f.Stream, "DATA vượt content-length")
	}
	s.body = append(s.body, data...)
	pad := n - int64(len(data))
	if f.Has(FlagEndStream) {
		s.remoteDone = true
		if validateDowngrade && s.declCL >= 0 && s.recvd != s.declCL {
			c.mu.Unlock()
			return malformed(f.Stream, "tổng DATA ≠ content-length")
		}
		s.bodyEOF = true
		s.stopBodyTimer()
	}
	c.cond.Broadcast()
	c.mu.Unlock()
	if pad > 0 {
		// Padding không bao giờ tới handler ⇒ trả window ngay (cả stream lẫn conn).
		c.mu.Lock()
		s.recvWindow += pad
		c.recvWindow += pad
		c.mu.Unlock()
		c.write(func(fr *Framer) error {
			if !f.Has(FlagEndStream) {
				fr.WriteWindowUpdate(f.Stream, uint32(pad))
			}
			return fr.WriteWindowUpdate(0, uint32(pad))
		})
	}
	return nil
}

func (c *Conn) refundConn(n int64) {
	if n == 0 {
		return
	}
	c.mu.Lock()
	c.recvWindow += n
	c.mu.Unlock()
	c.write(func(fr *Framer) error { return fr.WriteWindowUpdate(0, uint32(n)) })
}

func (c *Conn) onHeaders(f *Frame) error {
	if f.Stream == 0 {
		return ConnError{ErrProtocol, "HEADERS trên stream 0"}
	}
	p, err := stripPadding(f)
	if err != nil {
		return err
	}
	var depErr error
	if f.Has(FlagPriority) {
		if len(p) < priorityPayloadLen {
			return ConnError{ErrFrameSize, "HEADERS PRIORITY ngắn"}
		}
		if binary.BigEndian.Uint32(p)&streamIDMask == f.Stream {
			depErr = StreamError{f.Stream, ErrProtocol, "stream phụ thuộc chính nó"} // §5.3.1
		}
		p = p[priorityPayloadLen:]
	}
	c.hbStream, c.hbEnd = f.Stream, f.Has(FlagEndStream)
	c.hb = append(c.hb[:0], p...)
	c.noteHB()
	if !f.Has(FlagEndHeaders) {
		return c.checkHBCap()
	}
	return c.endHeaderBlock(depErr)
}

func (c *Conn) onContinuation(f *Frame) error {
	if c.hbStream == 0 {
		return ConnError{ErrProtocol, "CONTINUATION không theo sau HEADERS"}
	}
	c.hb = append(c.hb, f.Payload...)
	c.noteHB()
	if !f.Has(FlagEndHeaders) {
		return c.checkHBCap()
	}
	return c.endHeaderBlock(nil)
}

func (c *Conn) noteHB() {
	if n := int64(len(c.hb)); n > c.Stats.HeaderBlockMax.Load() {
		c.Stats.HeaderBlockMax.Store(n)
	}
}

// checkHBCap (D6 b): block đang ghép vượt trần ⇒ không chờ END_HEADERS nữa.
// Không thể RST riêng stream: block chưa decode xong thì bảng HPACK của hai
// bên đã lệch ⇒ chỉ còn cách đóng connection.
func (c *Conn) checkHBCap() error {
	if capHeaderBlock && len(c.hb) > int(c.cfg.MaxHeaderListSize) {
		return ConnError{ErrEnhanceYourCalm, fmt.Sprintf("header block %d byte > %d (CONTINUATION flood?)", len(c.hb), c.cfg.MaxHeaderListSize)}
	}
	return nil
}

// endHeaderBlock: block đủ ⇒ decode (LUÔN decode, kể cả khi sẽ từ chối stream
// — bỏ qua block là làm lệch bảng động với encoder bên kia), rồi mở stream /
// nhận trailer.
func (c *Conn) endHeaderBlock(depErr error) error {
	id, end := c.hbStream, c.hbEnd
	c.hbStream = 0
	fields, err := c.dec.Decode(c.hb)
	if len(c.hb) > 64<<10 {
		c.hb = nil // đừng giữ buffer to suốt đời connection (bẫy G2 phase 9)
	}
	if err != nil {
		if errors.Is(err, hpack.ErrListTooLarge) {
			// Decoder đã dừng giữa block ⇒ bảng có thể lệch ⇒ connection error.
			return ConnError{ErrEnhanceYourCalm, "header list vượt SETTINGS_MAX_HEADER_LIST_SIZE"}
		}
		return hpackErr{err}
	}
	c.mu.Lock()
	s := c.streams[id]
	if s != nil {
		// Trailer (§8.1): phải mang END_STREAM, stream còn mở phía remote.
		if s.remoteDone || s.reset {
			c.mu.Unlock()
			return ConnError{ErrStreamClosed, "HEADERS trên stream half-closed"}
		}
		if !end {
			c.mu.Unlock()
			return malformed(id, "trailer thiếu END_STREAM")
		}
		if err := checkTrailers(id, fields); err != nil {
			c.mu.Unlock()
			return err
		}
		s.remoteDone = true
		if validateDowngrade && s.declCL >= 0 && s.recvd != s.declCL {
			c.mu.Unlock()
			return malformed(id, "tổng DATA ≠ content-length")
		}
		s.bodyEOF = true
		s.stopBodyTimer()
		c.cond.Broadcast()
		c.mu.Unlock()
		return nil // trailer bị bỏ (như P3-1 phía h1)
	}
	if id%2 == 0 {
		c.mu.Unlock()
		return ConnError{ErrProtocol, "stream id chẵn từ client"}
	}
	if id <= c.lastID {
		c.mu.Unlock()
		return ConnError{ErrStreamClosed, fmt.Sprintf("HEADERS trên stream %d đã đóng / id không tăng", id)}
	}
	c.lastID = id
	if depErr != nil {
		c.mu.Unlock()
		return depErr
	}
	if c.goaway {
		c.mu.Unlock()
		return StreamError{id, ErrRefusedStream, "đang đóng"}
	}
	if c.active >= int(c.cfg.MaxConcurrentStreams) {
		c.mu.Unlock()
		c.Stats.Refused.Add(1)
		// §5.1.2: vượt trần ⇒ PROTOCOL_ERROR hoặc REFUSED_STREAM; REFUSED cho
		// client biết chưa xử lý gì, được thử lại.
		return StreamError{id, ErrRefusedStream, "vượt MAX_CONCURRENT_STREAMS"}
	}
	req, err := buildRequest(id, fields, end)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	s = &Stream{id: id, c: c, done: make(chan struct{}), declCL: req.ContentLength,
		sendWindow: c.peerInit, recvWindow: int64(c.cfg.InitialWindow), remoteDone: end, bodyEOF: end}
	if !end {
		s.bodyTimer = time.AfterFunc(c.cfg.BodyTimeout, func() {
			c.mu.Lock()
			if !s.bodyEOF && s.bodyErr == nil {
				s.bodyErr = ErrBodyTimeout
				c.cond.Broadcast()
			}
			c.mu.Unlock()
		})
	}
	c.streams[id] = s
	c.active++
	if int64(c.active) > c.Stats.MaxActive.Load() {
		c.Stats.MaxActive.Store(int64(c.active))
	}
	c.wg.Add(1)
	c.mu.Unlock()
	c.Stats.Streams.Add(1)
	go c.runHandler(s, req)
	return nil
}

func (c *Conn) runHandler(s *Stream, req *Request) {
	defer c.wg.Done()
	defer c.exitStream(s)
	c.handler(s, req)
}

// exitStream: handler thoát. Chưa END_STREAM ⇒ RST (INTERNAL_ERROR, hoặc
// không gì nếu đã bị reset). D6 a: slot trả Ở ĐÂY.
func (c *Conn) exitStream(s *Stream) {
	if !s.ended {
		c.mu.Lock()
		wasReset := s.reset
		c.mu.Unlock()
		if !wasReset {
			c.resetStream(s.id, ErrInternal)
		}
	}
	c.mu.Lock()
	s.exited = true
	s.stopBodyTimer()
	if !s.slotFreed {
		c.active--
	}
	delete(c.streams, s.id)
	if s.reset && !s.peerReset && !s.remoteDone {
		c.rememberLocalReset(s.id)
	}
	// Body chưa đọc hết: window connection đã bị trừ cho số byte đó ⇒ trả lại.
	unread := int64(len(s.body))
	s.body = nil
	idle, dr := c.active == 0, c.draining
	c.mu.Unlock()
	if unread > 0 {
		c.refundConn(unread)
	}
	switch {
	case idle && dr:
		c.nc.SetReadDeadline(time.Now())
	case idle:
		c.nc.SetReadDeadline(time.Now().Add(c.cfg.IdleTimeout))
	}
}

// resetStream: gửi RST_STREAM và đánh dấu stream (nếu còn).
func (c *Conn) resetStream(id uint32, code ErrCode) {
	c.mu.Lock()
	if s := c.streams[id]; s != nil {
		s.markReset(errStreamReset)
		c.cond.Broadcast()
	}
	c.mu.Unlock()
	c.Stats.ResetsSent.Add(1)
	c.write(func(fr *Framer) error { return fr.WriteRSTStream(id, code) })
}

const maxLocalReset = 256

// rememberLocalReset: dưới c.mu.
func (c *Conn) rememberLocalReset(id uint32) {
	if c.localReset == nil {
		c.localReset = map[uint32]struct{}{}
	}
	c.localReset[id] = struct{}{}
	c.localResetQ = append(c.localResetQ, id)
	if len(c.localResetQ) > maxLocalReset {
		delete(c.localReset, c.localResetQ[0])
		c.localResetQ = c.localResetQ[1:]
	}
}

func (s *Stream) stopBodyTimer() {
	if s.bodyTimer != nil {
		s.bodyTimer.Stop()
	}
}

// markReset: dưới c.mu.
func (s *Stream) markReset(err error) {
	if s.reset {
		return
	}
	s.reset = true
	if s.bodyErr == nil && !s.bodyEOF {
		s.bodyErr = err
	}
	close(s.done)
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

// SetCancel đăng ký fn gọi khi stream bị reset / connection chết (proxy: đóng
// connection upstream đang chờ ⇒ handler thoát ngay thay vì chờ timeout).
// fn chạy dưới khoá connection: chỉ được làm việc không chặn (net.Conn.Close).
// nil = huỷ đăng ký. Trả false nếu stream đã bị reset (fn KHÔNG được đăng ký).
func (s *Stream) SetCancel(fn func()) bool {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	if s.reset {
		return false
	}
	s.cancel = fn
	return true
}

func (c *Conn) onPriority(f *Frame) error {
	if f.Stream == 0 {
		return ConnError{ErrProtocol, "PRIORITY trên stream 0"}
	}
	if len(f.Payload) != priorityPayloadLen {
		return StreamError{f.Stream, ErrFrameSize, "PRIORITY dài ≠ 5"}
	}
	if binary.BigEndian.Uint32(f.Payload)&streamIDMask == f.Stream {
		return StreamError{f.Stream, ErrProtocol, "stream phụ thuộc chính nó"}
	}
	return nil // RFC 9113 bỏ lược đồ ưu tiên của 7540 (§5.3): nhận, không dùng
}

func (c *Conn) onRST(f *Frame) error {
	if f.Stream == 0 {
		return ConnError{ErrProtocol, "RST_STREAM trên stream 0"}
	}
	if len(f.Payload) != 4 {
		return ConnError{ErrFrameSize, "RST_STREAM dài ≠ 4"}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.isIdle(f.Stream) {
		return ConnError{ErrProtocol, "RST_STREAM trên stream idle"}
	}
	s := c.streams[f.Stream]
	if s == nil || s.reset {
		return nil
	}
	c.Stats.Resets.Add(1)
	// D6 a′: giữ slot tới khi handler thoát chặn số việc ĐỒNG THỜI trong proxy,
	// nhưng handler của proxy đóng upstream khi bị RST (SetCancel) ⇒ slot về
	// nhanh ⇒ client vẫn bơm được hàng nghìn request/giây SANG UPSTREAM rồi huỷ.
	// Trần tốc độ: > 2×MAX_CONCURRENT_STREAMS RST trong 1 s ⇒ GOAWAY.
	if capResetRate {
		now := time.Now()
		if now.Sub(c.rstWindow) > time.Second {
			c.rstWindow, c.rstCount = now, 0
		}
		c.rstCount++
		if c.rstCount > 2*int(c.cfg.MaxConcurrentStreams) {
			return ConnError{ErrEnhanceYourCalm, fmt.Sprintf("%d RST_STREAM trong 1 s (Rapid Reset)", c.rstCount)}
		}
	}
	s.peerReset = true
	s.markReset(errStreamReset)
	if !holdSlotUntilExit && !s.exited {
		c.active-- // nodefense10: Rapid Reset — slot về ngay, handler vẫn chạy
		s.slotFreed = true
	}
	c.cond.Broadcast()
	return nil
}

func (c *Conn) onSettings(f *Frame) error {
	if f.Stream != 0 {
		return ConnError{ErrProtocol, "SETTINGS trên stream ≠ 0"}
	}
	if f.Has(FlagAck) {
		if len(f.Payload) != 0 {
			return ConnError{ErrFrameSize, "SETTINGS ACK có payload"}
		}
		return nil
	}
	ss, err := ParseSettings(f.Payload)
	if err != nil {
		return err
	}
	c.mu.Lock()
	for _, st := range ss {
		switch st.ID {
		case SettingEnablePush:
			if st.Val > 1 {
				c.mu.Unlock()
				return ConnError{ErrProtocol, "ENABLE_PUSH ∉ {0,1}"}
			}
		case SettingInitialWindowSize:
			if st.Val > maxWindow {
				c.mu.Unlock()
				return ConnError{ErrFlowControl, "INITIAL_WINDOW_SIZE > 2^31-1"}
			}
			// §6.9.2: chênh lệch cộng vào MỌI stream đang mở, có thể làm âm.
			d := int64(st.Val) - c.peerInit
			for _, s := range c.streams {
				s.sendWindow += d
				if s.sendWindow > maxWindow {
					c.mu.Unlock()
					return ConnError{ErrFlowControl, "window stream tràn sau SETTINGS"}
				}
			}
			c.peerInit = int64(st.Val)
		case SettingMaxFrameSize:
			if st.Val < defaultMaxFrame || st.Val > maxFrameSizeLimit {
				c.mu.Unlock()
				return ConnError{ErrProtocol, "MAX_FRAME_SIZE ngoài [2^14, 2^24-1]"}
			}
			c.peerFrame = st.Val
		}
	}
	c.cond.Broadcast()
	c.mu.Unlock()
	return c.write(func(fr *Framer) error {
		for _, st := range ss {
			if st.ID == SettingHeaderTableSize {
				c.enc.SetMaxTableSize(st.Val) // dưới wmu: cùng khoá với Encode
			}
		}
		return fr.WriteSettingsAck()
	})
}

func (c *Conn) onPing(f *Frame) error {
	if f.Stream != 0 {
		return ConnError{ErrProtocol, "PING trên stream ≠ 0"}
	}
	if len(f.Payload) != 8 {
		return ConnError{ErrFrameSize, "PING dài ≠ 8"}
	}
	if f.Has(FlagAck) {
		return nil
	}
	var d [8]byte
	copy(d[:], f.Payload)
	return c.write(func(fr *Framer) error { return fr.WritePing(true, d) })
}

func (c *Conn) onWindowUpdate(f *Frame) error {
	if len(f.Payload) != 4 {
		return ConnError{ErrFrameSize, "WINDOW_UPDATE dài ≠ 4"}
	}
	inc := int64(binary.BigEndian.Uint32(f.Payload) & streamIDMask)
	c.mu.Lock()
	defer c.mu.Unlock()
	if f.Stream == 0 {
		if inc == 0 {
			return ConnError{ErrProtocol, "WINDOW_UPDATE 0 trên connection"}
		}
		if c.sendWindow+inc > maxWindow {
			return ConnError{ErrFlowControl, "window connection > 2^31-1"}
		}
		c.sendWindow += inc
		c.cond.Broadcast()
		return nil
	}
	if c.isIdle(f.Stream) {
		return ConnError{ErrProtocol, "WINDOW_UPDATE trên stream idle"}
	}
	s := c.streams[f.Stream]
	if s == nil || s.reset {
		return nil // stream đã đóng: §6.9 cho phép nhận và bỏ qua
	}
	if inc == 0 {
		return StreamError{f.Stream, ErrProtocol, "WINDOW_UPDATE 0"}
	}
	if s.sendWindow+inc > maxWindow {
		return StreamError{f.Stream, ErrFlowControl, "window stream > 2^31-1"}
	}
	s.sendWindow += inc
	c.cond.Broadcast()
	return nil
}

// --- phía handler -------------------------------------------------------

// ID của stream.
func (s *Stream) ID() uint32 { return s.id }

// Done đóng khi stream bị reset hoặc connection chết — proxy dừng việc upstream.
func (s *Stream) Done() <-chan struct{} { return s.done }

// Read đọc body request. io.EOF ở END_STREAM. Mỗi lần đọc trả window cho
// client (D5): handler chậm đọc ⇒ client bị chặn ĐÚNG stream này.
func (s *Stream) Read(p []byte) (int, error) {
	c := s.c
	c.mu.Lock()
	for len(s.body) == 0 && !s.bodyEOF && s.bodyErr == nil {
		c.cond.Wait()
	}
	if len(s.body) == 0 {
		err := s.bodyErr
		if err == nil {
			err = io.EOF
		}
		c.mu.Unlock()
		return 0, err
	}
	n := copy(p, s.body)
	s.body = s.body[n:]
	if len(s.body) == 0 {
		s.body = nil
	}
	more := !s.remoteDone
	c.recvWindow += int64(n)
	if more {
		s.recvWindow += int64(n)
	}
	c.mu.Unlock()
	c.write(func(fr *Framer) error {
		if more {
			fr.WriteWindowUpdate(s.id, uint32(n))
		}
		return fr.WriteWindowUpdate(0, uint32(n))
	})
	return n, nil
}

// WriteHeaders gửi head response. fields: tên lowercase, không pseudo.
func (s *Stream) WriteHeaders(status int, fields []hpack.HeaderField, end bool) error {
	if s.wroteHeaders {
		return errors.New("h2: WriteHeaders hai lần")
	}
	c := s.c
	c.mu.Lock()
	if s.reset {
		c.mu.Unlock()
		return errStreamReset
	}
	maxFrame := c.peerFrame
	c.mu.Unlock()
	s.wroteHeaders = true
	all := make([]hpack.HeaderField, 0, len(fields)+1)
	all = append(all, hpack.HeaderField{Name: ":status", Value: fmt.Sprint(status)})
	all = append(all, fields...)
	err := c.write(func(fr *Framer) error {
		block := c.enc.Encode(nil, all)
		return fr.WriteHeaderBlock(s.id, end, block, maxFrame)
	})
	if end && err == nil {
		s.ended = true
	}
	return err
}

// Write gửi DATA theo flow control (chặn khi window stream hoặc connection
// hết, D5). end ⇒ frame cuối mang END_STREAM.
func (s *Stream) WriteData(p []byte, end bool) error {
	c := s.c
	if len(p) == 0 && !end {
		return nil
	}
	for {
		c.mu.Lock()
		var timedOut bool
		var t *time.Timer
		for !s.reset && !timedOut && len(p) > 0 && (s.sendWindow <= 0 || c.sendWindow <= 0) {
			if t == nil {
				t = time.AfterFunc(c.cfg.WriteTimeout, func() {
					c.mu.Lock()
					timedOut = true
					c.cond.Broadcast()
					c.mu.Unlock()
				})
			}
			c.cond.Wait()
		}
		if t != nil {
			t.Stop()
		}
		if s.reset {
			c.mu.Unlock()
			return errStreamReset
		}
		if timedOut {
			c.mu.Unlock()
			return ErrWindowTimeout
		}
		n := int64(len(p))
		n = min(n, s.sendWindow, c.sendWindow, int64(c.peerFrame))
		s.sendWindow -= n
		c.sendWindow -= n
		c.mu.Unlock()
		chunk := p[:n]
		p = p[n:]
		last := end && len(p) == 0
		if err := c.write(func(fr *Framer) error { return fr.WriteData(s.id, last, chunk) }); err != nil {
			return err
		}
		if last {
			s.ended = true
		}
		if len(p) == 0 {
			return nil
		}
	}
}

// Write: io.Writer cho body (không END_STREAM).
func (s *Stream) Write(p []byte) (int, error) {
	if err := s.WriteData(p, false); err != nil {
		return 0, err
	}
	return len(p), nil
}

// End: DATA rỗng mang END_STREAM.
func (s *Stream) End() error {
	if s.ended {
		return nil
	}
	return s.WriteData(nil, true)
}

// Reset huỷ stream từ phía server.
func (s *Stream) Reset(code ErrCode) {
	s.ended = true // không RST lần hai ở exitStream
	s.c.resetStream(s.id, code)
}
