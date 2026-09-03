// Package frame là tầng framing của EdgeGate: cắt dòng byte TCP thành tin nhắn.
//
// TCP không có ranh giới tin nhắn. Hai hệ quả, và mọi thứ trong package này
// tồn tại vì hai hệ quả đó:
//
//  1. conn.Read trả về ÍT hơn số byte cần là chuyện bình thường, không phải lỗi.
//     → io.ReadFull, không bao giờ Read trần.
//  2. Một lần Read có thể trả về NHIỀU frame dính nhau (sender ghi liên tiếp,
//     hoặc Nagle gom gói). → Decoder là vòng lặp trên một io.Reader, không phải
//     "1 Read = 1 frame".
//
// Wire format (big-endian), header cố định 10 byte:
//
//	uint32 magic | uint8 version | uint8 type | uint32 length | payload[length]
//
// Bất biến I2 của ROADMAP: length có trần (MaxFrameSize) và trần được kiểm
// TRƯỚC khi make([]byte, length). Xem Decoder.Decode và checkLength — hàm này
// nằm trong file riêng có build tag để `make framelab-nodefense` tắt được nó
// và chứng minh bộ test đỏ.
package frame

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	// Magic là 4 byte đầu mỗi frame: "EDGG". Không phải để bảo mật — để một
	// client nói nhầm giao thức (HTTP, TLS ClientHello) bị từ chối ở byte thứ 4
	// thay vì bị đọc thành một length ngẫu nhiên.
	Magic uint32 = 0x45444747

	// Version là byte thứ 5. Đổi wire format thì tăng, decoder cũ từ chối sạch.
	Version uint8 = 1

	// HeaderSize là phần cố định đứng trước payload.
	HeaderSize = 4 + 1 + 1 + 4

	// DefaultMaxFrameSize là trần payload nếu caller không nói gì. 1 MiB —
	// cùng con số netlab đã dùng ở phase 0; lớn hơn thì phải có lý do đo được.
	DefaultMaxFrameSize uint32 = 1 << 20
)

// Type là loại frame. Phase 1 chỉ cần phân biệt được vài loại để test; các
// phase sau định nghĩa thêm ở tầng trên, package này không diễn giải chúng.
type Type uint8

const (
	TypeData Type = 1
	TypePing Type = 2
	TypePong Type = 3
)

var (
	ErrBadMagic   = errors.New("frame: sai magic")
	ErrBadVersion = errors.New("frame: sai version")
	ErrTooBig     = errors.New("frame: length vượt trần")

	// ErrPayloadTooBig là lỗi phía GỬI: payload không mã hoá được vào uint32.
	// Tìm ra ở turn 2 phase 1: Append từng ép uint32(len) im lặng, tức một
	// payload 4 GiB + 1 byte sinh ra header hợp lệ với length = 1 — decoder
	// bên kia đọc lệch ranh giới từ đó về sau. I2 phải giữ ở cả hai chiều.
	ErrPayloadTooBig = errors.New("frame: payload vượt uint32")
)

// MaxPayload là payload lớn nhất wire format biểu diễn được. Trần thực tế của
// một decoder (DefaultMaxFrameSize) nhỏ hơn nhiều; đây chỉ là trần vật lý.
const MaxPayload = 1<<32 - 1

// Frame là một tin nhắn đã tách ranh giới. Payload là slice riêng, caller giữ
// được sau lần Decode kế tiếp.
type Frame struct {
	Type    Type
	Payload []byte
}

// Append nối frame đã mã hoá vào dst và trả về slice mới. Dùng cho sender
// muốn gom nhiều frame vào MỘT lần Write — chính là kịch bản "sender dính gói".
//
// Panic nếu payload > MaxPayload: đó là lỗi lập trình cùng hạng với slice
// vượt biên, và im lặng cắt cụt (hành vi cũ) tệ hơn panic rất nhiều. Đường
// đi nhận dữ liệu từ ngoài phải qua Encode, hàm đó trả lỗi thay vì panic.
func Append(dst []byte, f Frame) []byte {
	if len(f.Payload) > MaxPayload {
		panic(fmt.Sprintf("frame.Append: payload %d byte > MaxPayload", len(f.Payload)))
	}
	var hdr [HeaderSize]byte
	PutHeader(hdr[:], f.Type, uint32(len(f.Payload)))
	dst = append(dst, hdr[:]...)
	return append(dst, f.Payload...)
}

// PutHeader ghi header của một frame type t, payload dài length vào dst[:HeaderSize].
// Dành cho sender muốn tự ghi payload sau đó (io.Copy, hoặc cố ý hai lần Write
// như netlab đo G1) mà không copy payload vào buffer trung gian. Panic nếu dst
// ngắn hơn HeaderSize — cùng hạng với slice vượt biên.
func PutHeader(dst []byte, t Type, length uint32) {
	_ = dst[HeaderSize-1]
	binary.BigEndian.PutUint32(dst[0:4], Magic)
	dst[4] = Version
	dst[5] = byte(t)
	binary.BigEndian.PutUint32(dst[6:10], length)
}

// Encode ghi một frame ra w bằng ĐÚNG MỘT lần Write. Phase 0 đo được
// write-write-read + Nagle = 44ms mỗi request; header và payload đi chung
// một Write là cách rẻ nhất để không bao giờ rơi vào hình dạng đó.
func Encode(w io.Writer, f Frame) error {
	if len(f.Payload) > MaxPayload {
		return fmt.Errorf("%w: %d", ErrPayloadTooBig, len(f.Payload))
	}
	_, err := w.Write(Append(make([]byte, 0, HeaderSize+len(f.Payload)), f))
	return err
}

// Decoder đọc frame liên tiếp từ một io.Reader. Không có buffer riêng — nếu
// caller muốn bufio thì tự bọc r, và phải biết bufio giấu gì (phase 0, G1).
type Decoder struct {
	r   io.Reader
	max uint32
	hdr [HeaderSize]byte
}

// NewDecoder tạo decoder với trần payload là max byte. max = 0 nghĩa là
// DefaultMaxFrameSize; không có cách nào tạo decoder không trần.
func NewDecoder(r io.Reader, max uint32) *Decoder {
	if max == 0 {
		max = DefaultMaxFrameSize
	}
	return &Decoder{r: r, max: max}
}

// Decode đọc đúng một frame. Trả io.EOF khi stream kết thúc SẠCH ở ranh giới
// frame; io.ErrUnexpectedEOF khi kết thúc giữa frame — hai lỗi khác nhau vì
// caller phải xử lý khác nhau (peer đóng bình thường vs peer bị cắt).
func (d *Decoder) Decode() (Frame, error) {
	if _, err := io.ReadFull(d.r, d.hdr[:]); err != nil {
		return Frame{}, err
	}
	if m := binary.BigEndian.Uint32(d.hdr[0:4]); m != Magic {
		return Frame{}, fmt.Errorf("%w: %#08x", ErrBadMagic, m)
	}
	if v := d.hdr[4]; v != Version {
		return Frame{}, fmt.Errorf("%w: %d", ErrBadVersion, v)
	}
	n := binary.BigEndian.Uint32(d.hdr[6:10])

	// Thứ tự hai dòng dưới là toàn bộ bất biến I2. Đảo lại là một dòng code
	// giết process bằng length = 0xFFFFFFFF.
	if err := checkLength(n, d.max); err != nil {
		return Frame{}, err
	}
	payload := make([]byte, n)

	if _, err := io.ReadFull(d.r, payload); err != nil {
		// Header đọc trọn mà payload đứt: luôn là "đứt giữa frame", kể cả khi
		// ReadFull trả io.EOF vì n == 0 không thể tới đây với lỗi.
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return Frame{}, err
	}
	return Frame{Type: Type(d.hdr[5]), Payload: payload}, nil
}
