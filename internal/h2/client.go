package h2

import (
	"bufio"
	"encoding/binary"
	"net"

	"github.com/thaivro/edgegate/internal/h2/hpack"
)

// RawClient: client h2 tối giản, KHÔNG có logic giao thức — chỉ ghi/đọc frame
// theo lệnh. Dùng cho test ca biên (byte xấu mà client thật không gửi) và cho
// h2lab -mode flow (tự đặt window). Peer độc lập là net/http và curl (D9).
type RawClient struct {
	NC  net.Conn
	Fr  *Framer
	Enc *hpack.Encoder
	Dec *hpack.Decoder
}

// NewRawClient gửi preface + SETTINGS (ss) và trả client. Không chờ gì.
func NewRawClient(nc net.Conn, ss ...Setting) (*RawClient, error) {
	bw := bufio.NewWriterSize(nc, 64<<10)
	c := &RawClient{NC: nc, Fr: NewFramer(bufio.NewReaderSize(nc, 64<<10), bw), Enc: hpack.NewEncoder(), Dec: hpack.NewDecoder(4096, 0)}
	c.Fr.MaxRead = maxFrameSizeLimit
	if _, err := bw.WriteString(Preface); err != nil {
		return nil, err
	}
	if err := c.Fr.WriteSettings(ss...); err != nil {
		return nil, err
	}
	return c, c.Fr.Flush()
}

// Headers mã hoá fields rồi ghi một HEADERS (END_HEADERS) — flags thêm tuỳ ý.
func (c *RawClient) Headers(stream uint32, end bool, fields ...hpack.HeaderField) error {
	block := c.Enc.Encode(nil, fields)
	fl := uint8(FlagEndHeaders)
	if end {
		fl |= FlagEndStream
	}
	if err := c.Fr.WriteFrame(FrameHeaders, fl, stream, block); err != nil {
		return err
	}
	return c.Fr.Flush()
}

// GET là các pseudo-header tối thiểu cho path.
func GET(authority, path string, extra ...hpack.HeaderField) []hpack.HeaderField {
	return append([]hpack.HeaderField{
		{Name: ":method", Value: "GET"}, {Name: ":scheme", Value: "http"},
		{Name: ":authority", Value: authority}, {Name: ":path", Value: path},
	}, extra...)
}

// POST tương tự GET.
func POST(authority, path string, extra ...hpack.HeaderField) []hpack.HeaderField {
	f := GET(authority, path, extra...)
	f[0].Value = "POST"
	return f
}

// Frame ghi một frame thô rồi Flush.
func (c *RawClient) Frame(t FrameType, flags uint8, stream uint32, payload []byte) error {
	if err := c.Fr.WriteFrame(t, flags, stream, payload); err != nil {
		return err
	}
	return c.Fr.Flush()
}

// RST gửi RST_STREAM.
func (c *RawClient) RST(stream uint32, code ErrCode) error {
	if err := c.Fr.WriteRSTStream(stream, code); err != nil {
		return err
	}
	return c.Fr.Flush()
}

// WindowUpdate gửi WINDOW_UPDATE.
func (c *RawClient) WindowUpdate(stream, inc uint32) error {
	if err := c.Fr.WriteWindowUpdate(stream, inc); err != nil {
		return err
	}
	return c.Fr.Flush()
}

// GoAwayCode: mã lỗi trong payload GOAWAY.
func GoAwayCode(f *Frame) ErrCode {
	if len(f.Payload) < 8 {
		return ErrCode(0xffffffff)
	}
	return ErrCode(binary.BigEndian.Uint32(f.Payload[4:]))
}

// RSTCode: mã lỗi trong payload RST_STREAM.
func RSTCode(f *Frame) ErrCode {
	if len(f.Payload) < 4 {
		return ErrCode(0xffffffff)
	}
	return ErrCode(binary.BigEndian.Uint32(f.Payload))
}

// HeaderPayload: block của frame HEADERS (bỏ padding/priority).
func HeaderPayload(f *Frame) ([]byte, error) {
	p, err := stripPadding(f)
	if err != nil {
		return nil, err
	}
	if f.Has(FlagPriority) {
		p = p[priorityPayloadLen:]
	}
	return p, nil
}
