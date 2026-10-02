// Package h2: HTTP/2 cleartext (h2c, prior knowledge) phía server — frame
// layer (RFC 9113 §4), stream (§5), flow control (§5.2, §6.9), SETTINGS,
// PING, GOAWAY, RST_STREAM. Không dùng net/http, không golang.org/x/net.
//
// Khác HTTP/1.1 ở đúng một chỗ gốc: ranh giới message là FRAMING (độ dài
// 24 bit trong header frame + cờ END_STREAM), không phải cú pháp văn bản. Nên
// I2 ở đây không phải "trần dòng" mà là trần frame (SETTINGS_MAX_FRAME_SIZE,
// kiểm TRƯỚC khi đọc payload) và trần header list (kiểm trong lúc decode).
package h2

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

// Preface: 24 byte client gửi đầu tiên (§3.4). Chọn sao cho server HTTP/1.1
// đọc thấy method "PRI" lạ và từ chối, thay vì hiểu nhầm.
const Preface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"

// FrameType (§6).
type FrameType uint8

const (
	FrameData         FrameType = 0x0
	FrameHeaders      FrameType = 0x1
	FramePriority     FrameType = 0x2
	FrameRSTStream    FrameType = 0x3
	FrameSettings     FrameType = 0x4
	FramePushPromise  FrameType = 0x5
	FramePing         FrameType = 0x6
	FrameGoAway       FrameType = 0x7
	FrameWindowUpdate FrameType = 0x8
	FrameContinuation FrameType = 0x9
)

var frameNames = [...]string{"DATA", "HEADERS", "PRIORITY", "RST_STREAM", "SETTINGS", "PUSH_PROMISE", "PING", "GOAWAY", "WINDOW_UPDATE", "CONTINUATION"}

func (t FrameType) String() string {
	if int(t) < len(frameNames) {
		return frameNames[t]
	}
	return fmt.Sprintf("UNKNOWN(0x%x)", uint8(t))
}

// Cờ (§6). END_STREAM và ACK cùng bit 0x1 — nghĩa tuỳ loại frame.
const (
	FlagEndStream  = 0x1
	FlagAck        = 0x1
	FlagEndHeaders = 0x4
	FlagPadded     = 0x8
	FlagPriority   = 0x20
)

// ErrCode (§7).
type ErrCode uint32

const (
	ErrNo                 ErrCode = 0x0
	ErrProtocol           ErrCode = 0x1
	ErrInternal           ErrCode = 0x2
	ErrFlowControl        ErrCode = 0x3
	ErrSettingsTimeout    ErrCode = 0x4
	ErrStreamClosed       ErrCode = 0x5
	ErrFrameSize          ErrCode = 0x6
	ErrRefusedStream      ErrCode = 0x7
	ErrCancel             ErrCode = 0x8
	ErrCompression        ErrCode = 0x9
	ErrConnect            ErrCode = 0xa
	ErrEnhanceYourCalm    ErrCode = 0xb
	ErrInadequateSecurity ErrCode = 0xc
	ErrHTTP11Required     ErrCode = 0xd
)

var errNames = [...]string{"NO_ERROR", "PROTOCOL_ERROR", "INTERNAL_ERROR", "FLOW_CONTROL_ERROR", "SETTINGS_TIMEOUT", "STREAM_CLOSED", "FRAME_SIZE_ERROR", "REFUSED_STREAM", "CANCEL", "COMPRESSION_ERROR", "CONNECT_ERROR", "ENHANCE_YOUR_CALM", "INADEQUATE_SECURITY", "HTTP_1_1_REQUIRED"}

func (c ErrCode) String() string {
	if int(c) < len(errNames) {
		return errNames[c]
	}
	return fmt.Sprintf("ERR(0x%x)", uint32(c))
}

// SettingID (§6.5.2).
type SettingID uint16

const (
	SettingHeaderTableSize      SettingID = 0x1
	SettingEnablePush           SettingID = 0x2
	SettingMaxConcurrentStreams SettingID = 0x3
	SettingInitialWindowSize    SettingID = 0x4
	SettingMaxFrameSize         SettingID = 0x5
	SettingMaxHeaderListSize    SettingID = 0x6
)

// Setting: một cặp id/value.
type Setting struct {
	ID  SettingID
	Val uint32
}

// ConnError: lỗi connection — GOAWAY rồi đóng (§5.4.1).
type ConnError struct {
	Code   ErrCode
	Reason string
}

func (e ConnError) Error() string {
	return fmt.Sprintf("h2: connection error %v: %s", e.Code, e.Reason)
}

// StreamError: lỗi một stream — RST_STREAM, connection sống tiếp (§5.4.2).
type StreamError struct {
	Stream uint32
	Code   ErrCode
	Reason string
}

func (e StreamError) Error() string {
	return fmt.Sprintf("h2: stream %d error %v: %s", e.Stream, e.Code, e.Reason)
}

const (
	frameHeaderLen     = 9
	defaultMaxFrame    = 16384      // §4.2: giá trị khởi đầu, cũng là sàn
	maxFrameSizeLimit  = 1<<24 - 1  // §6.5.2
	maxWindow          = 1<<31 - 1  // §6.9.1
	defaultInitWindow  = 65535      // §6.9.2
	streamIDMask       = 0x7fffffff // bit R bị bỏ qua khi nhận (§4.1)
	priorityPayloadLen = 5
)

// Frame: một frame đã đọc. Payload trỏ vào buffer của Framer — chỉ hợp lệ tới
// lần ReadFrame kế tiếp (goroutine đọc xử lý xong frame rồi mới đọc frame sau,
// D3), nên không cấp phát payload mỗi frame.
type Frame struct {
	Type    FrameType
	Flags   uint8
	Stream  uint32
	Payload []byte
}

func (f *Frame) Has(flag uint8) bool { return f.Flags&flag != 0 }

// Framer đọc/ghi frame trên một connection.
type Framer struct {
	r io.Reader
	w *bufio.Writer
	// MaxRead: trần Length frame NHẬN (SETTINGS_MAX_FRAME_SIZE của ta). Kiểm
	// trước khi đọc payload (I2).
	MaxRead uint32
	hdr     [frameHeaderLen]byte
	buf     []byte
	// Stats: số byte payload theo loại (lab G1 đếm byte HEADERS).
	ReadBytes [10]int64
	ReadCount [10]int64
}

func NewFramer(r io.Reader, w *bufio.Writer) *Framer {
	return &Framer{r: r, w: w, MaxRead: defaultMaxFrame}
}

// ReadFrame đọc một frame. Length > MaxRead ⇒ ConnError FRAME_SIZE_ERROR mà
// KHÔNG đọc payload (§4.2: "MUST send an error code of FRAME_SIZE_ERROR").
func (fr *Framer) ReadFrame() (*Frame, error) {
	if _, err := io.ReadFull(fr.r, fr.hdr[:]); err != nil {
		return nil, err
	}
	n := uint32(fr.hdr[0])<<16 | uint32(fr.hdr[1])<<8 | uint32(fr.hdr[2])
	f := &Frame{Type: FrameType(fr.hdr[3]), Flags: fr.hdr[4], Stream: binary.BigEndian.Uint32(fr.hdr[5:]) & streamIDMask}
	if n > fr.MaxRead {
		return f, ConnError{ErrFrameSize, fmt.Sprintf("%v dài %d > %d", f.Type, n, fr.MaxRead)}
	}
	if uint32(cap(fr.buf)) < n {
		fr.buf = make([]byte, n, max(n, defaultMaxFrame))
	}
	f.Payload = fr.buf[:n]
	if _, err := io.ReadFull(fr.r, f.Payload); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	if int(f.Type) < len(fr.ReadBytes) {
		fr.ReadBytes[f.Type] += int64(n)
		fr.ReadCount[f.Type]++
	}
	return f, nil
}

// WriteFrame ghi header + payload vào bufio (chưa Flush).
func (fr *Framer) WriteFrame(t FrameType, flags uint8, stream uint32, payload ...[]byte) error {
	n := 0
	for _, p := range payload {
		n += len(p)
	}
	var h [frameHeaderLen]byte
	h[0], h[1], h[2] = byte(n>>16), byte(n>>8), byte(n)
	h[3], h[4] = byte(t), flags
	binary.BigEndian.PutUint32(h[5:], stream&streamIDMask)
	if _, err := fr.w.Write(h[:]); err != nil {
		return err
	}
	for _, p := range payload {
		if _, err := fr.w.Write(p); err != nil {
			return err
		}
	}
	return nil
}

func (fr *Framer) Flush() error { return fr.w.Flush() }

func (fr *Framer) WriteSettings(ss ...Setting) error {
	p := make([]byte, 0, 6*len(ss))
	for _, s := range ss {
		p = binary.BigEndian.AppendUint16(p, uint16(s.ID))
		p = binary.BigEndian.AppendUint32(p, s.Val)
	}
	return fr.WriteFrame(FrameSettings, 0, 0, p)
}

func (fr *Framer) WriteSettingsAck() error { return fr.WriteFrame(FrameSettings, FlagAck, 0) }

func (fr *Framer) WritePing(ack bool, data [8]byte) error {
	var fl uint8
	if ack {
		fl = FlagAck
	}
	return fr.WriteFrame(FramePing, fl, 0, data[:])
}

func (fr *Framer) WriteWindowUpdate(stream, inc uint32) error {
	var p [4]byte
	binary.BigEndian.PutUint32(p[:], inc&streamIDMask)
	return fr.WriteFrame(FrameWindowUpdate, 0, stream, p[:])
}

func (fr *Framer) WriteRSTStream(stream uint32, code ErrCode) error {
	var p [4]byte
	binary.BigEndian.PutUint32(p[:], uint32(code))
	return fr.WriteFrame(FrameRSTStream, 0, stream, p[:])
}

func (fr *Framer) WriteGoAway(last uint32, code ErrCode, debug string) error {
	var p [8]byte
	binary.BigEndian.PutUint32(p[:4], last&streamIDMask)
	binary.BigEndian.PutUint32(p[4:], uint32(code))
	return fr.WriteFrame(FrameGoAway, 0, 0, p[:], []byte(debug))
}

func (fr *Framer) WriteData(stream uint32, end bool, p []byte) error {
	var fl uint8
	if end {
		fl = FlagEndStream
	}
	return fr.WriteFrame(FrameData, fl, stream, p)
}

// WriteHeaderBlock: HEADERS + CONTINUATION nếu block > maxFrame. Các frame
// phải LIỀN nhau trên dây (§6.10) — caller giữ khoá ghi suốt lời gọi.
func (fr *Framer) WriteHeaderBlock(stream uint32, end bool, block []byte, maxFrame uint32) error {
	t := FrameHeaders
	for first := true; first || len(block) > 0; first = false {
		chunk := block
		if uint32(len(chunk)) > maxFrame {
			chunk = chunk[:maxFrame]
		}
		block = block[len(chunk):]
		var fl uint8
		if len(block) == 0 {
			fl |= FlagEndHeaders
		}
		if first && end {
			fl |= FlagEndStream
		}
		if err := fr.WriteFrame(t, fl, stream, chunk); err != nil {
			return err
		}
		t = FrameContinuation
	}
	return nil
}

// ParseSettings: payload SETTINGS → cặp. Length % 6 ≠ 0 ⇒ FRAME_SIZE_ERROR (§6.5).
func ParseSettings(p []byte) ([]Setting, error) {
	if len(p)%6 != 0 {
		return nil, ConnError{ErrFrameSize, "SETTINGS dài không chia hết cho 6"}
	}
	out := make([]Setting, 0, len(p)/6)
	for ; len(p) > 0; p = p[6:] {
		out = append(out, Setting{SettingID(binary.BigEndian.Uint16(p)), binary.BigEndian.Uint32(p[2:])})
	}
	return out, nil
}

// stripPadding (§6.1, §6.2): PADDED ⇒ byte đầu là Pad Length; pad ≥ phần còn
// lại ⇒ PROTOCOL_ERROR.
func stripPadding(f *Frame) ([]byte, error) {
	p := f.Payload
	if !f.Has(FlagPadded) {
		return p, nil
	}
	if len(p) < 1 {
		return nil, ConnError{ErrFrameSize, f.Type.String() + " PADDED rỗng"}
	}
	pad := int(p[0])
	p = p[1:]
	if pad > len(p) { // pad ≥ Length của frame (len(p)+1)
		return nil, ConnError{ErrProtocol, fmt.Sprintf("%v pad %d ≥ payload %d", f.Type, pad, len(p))}
	}
	return p[:len(p)-pad], nil
}
