// Package hpack: nén header HTTP/2 theo RFC 7541 — số nguyên prefix N bit,
// chuỗi literal (thường hoặc Huffman), bảng tĩnh 61 mục và bảng động.
//
// Vì sao không deflate như SPDY: deflate nén header CHUNG ngữ cảnh với dữ liệu
// attacker điều khiển được ⇒ CRIME đoán cookie từng byte qua độ dài nén. HPACK
// không có so khớp chuỗi con: một field hoặc khớp NGUYÊN (index) hoặc không.
//
// Cái giá (D1, câu 1): bảng động là trạng thái CHUNG giữa encoder bên kia và
// decoder bên này. Decode hỏng một block ⇒ không biết bên kia đã thêm gì vào
// bảng ⇒ mọi block sau đều vô nghĩa ⇒ lỗi decode luôn là lỗi CONNECTION
// (COMPRESSION_ERROR), không bao giờ "bỏ qua stream này".
package hpack

import (
	"errors"
	"fmt"
)

// HeaderField: một cặp name/value đã giải nén. Sensitive = literal
// never-indexed (proxy phải giữ nguyên tính chất này khi nén lại, RFC 7541 §7.1.3).
type HeaderField struct {
	Name, Value string
	Sensitive   bool
}

// Size theo RFC 7541 §4.1: name + value + 32 (32 = ước lượng chi phí con trỏ
// của mục trong bảng — để hai đầu đồng ý bảng "đầy" lúc nào).
func (f HeaderField) Size() uint32 { return uint32(len(f.Name) + len(f.Value) + 32) }

// ErrDecode: mọi lỗi giải nén. Caller (h2) đổi thành COMPRESSION_ERROR.
var ErrDecode = errors.New("hpack: lỗi giải nén")

// ErrListTooLarge: tổng header list vượt trần (D1, I2) — kiểm TRONG lúc decode.
var ErrListTooLarge = errors.New("hpack: header list vượt trần")

func decodeErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrDecode, fmt.Sprintf(format, a...))
}

// staticTable: RFC 7541 Appendix A. Chỉ số bắt đầu từ 1.
var staticTable = [...]HeaderField{
	{Name: ":authority"},
	{Name: ":method", Value: "GET"},
	{Name: ":method", Value: "POST"},
	{Name: ":path", Value: "/"},
	{Name: ":path", Value: "/index.html"},
	{Name: ":scheme", Value: "http"},
	{Name: ":scheme", Value: "https"},
	{Name: ":status", Value: "200"},
	{Name: ":status", Value: "204"},
	{Name: ":status", Value: "206"},
	{Name: ":status", Value: "304"},
	{Name: ":status", Value: "400"},
	{Name: ":status", Value: "404"},
	{Name: ":status", Value: "500"},
	{Name: "accept-charset"},
	{Name: "accept-encoding", Value: "gzip, deflate"},
	{Name: "accept-language"},
	{Name: "accept-ranges"},
	{Name: "accept"},
	{Name: "access-control-allow-origin"},
	{Name: "age"},
	{Name: "allow"},
	{Name: "authorization"},
	{Name: "cache-control"},
	{Name: "content-disposition"},
	{Name: "content-encoding"},
	{Name: "content-language"},
	{Name: "content-length"},
	{Name: "content-location"},
	{Name: "content-range"},
	{Name: "content-type"},
	{Name: "cookie"},
	{Name: "date"},
	{Name: "etag"},
	{Name: "expect"},
	{Name: "expires"},
	{Name: "from"},
	{Name: "host"},
	{Name: "if-match"},
	{Name: "if-modified-since"},
	{Name: "if-none-match"},
	{Name: "if-range"},
	{Name: "if-unmodified-since"},
	{Name: "last-modified"},
	{Name: "link"},
	{Name: "location"},
	{Name: "max-forwards"},
	{Name: "proxy-authenticate"},
	{Name: "proxy-authorization"},
	{Name: "range"},
	{Name: "referer"},
	{Name: "refresh"},
	{Name: "retry-after"},
	{Name: "server"},
	{Name: "set-cookie"},
	{Name: "strict-transport-security"},
	{Name: "transfer-encoding"},
	{Name: "user-agent"},
	{Name: "vary"},
	{Name: "via"},
	{Name: "www-authenticate"},
}

// staticByPair / staticByName: tra ngược cho Encoder (chỉ số nhỏ nhất).
var staticByPair = map[HeaderField]int{}
var staticByName = map[string]int{}

func init() {
	for i := len(staticTable) - 1; i >= 0; i-- {
		f := staticTable[i]
		staticByPair[HeaderField{Name: f.Name, Value: f.Value}] = i + 1
		staticByName[f.Name] = i + 1
	}
}

// dynTable: bảng động (RFC 7541 §2.3.2). Mục mới nhất có chỉ số nhỏ nhất
// (62). Lưu dạng vòng: ents[0] là cũ nhất ⇒ thêm = append, đuổi = cắt đầu.
type dynTable struct {
	ents    []HeaderField
	size    uint32 // Σ Size()
	maxSize uint32 // trần hiện hành (≤ trần SETTINGS)
}

func (t *dynTable) add(f HeaderField) {
	f.Sensitive = false
	t.ents = append(t.ents, f)
	t.size += f.Size()
	t.evict()
}

// evict: đuổi từ cũ nhất tới khi vừa trần. Một mục lớn hơn cả trần ⇒ bảng
// rỗng (§4.4) — không phải lỗi.
func (t *dynTable) evict() {
	n := 0
	for t.size > t.maxSize && n < len(t.ents) {
		t.size -= t.ents[n].Size()
		n++
	}
	if n > 0 {
		copy(t.ents, t.ents[n:])
		for i := len(t.ents) - n; i < len(t.ents); i++ {
			t.ents[i] = HeaderField{} // nhả chuỗi cho GC
		}
		t.ents = t.ents[:len(t.ents)-n]
	}
}

func (t *dynTable) setMax(n uint32) {
	t.maxSize = n
	t.evict()
}

// at: chỉ số HPACK (1-based, tĩnh rồi động) → field.
func (t *dynTable) at(i uint64) (HeaderField, bool) {
	if i == 0 {
		return HeaderField{}, false
	}
	if i <= uint64(len(staticTable)) {
		return staticTable[i-1], true
	}
	k := i - uint64(len(staticTable)) // 1 = mới nhất
	if k > uint64(len(t.ents)) {
		return HeaderField{}, false
	}
	return t.ents[len(t.ents)-int(k)], true
}

// --- số nguyên prefix N bit (§5.1) --------------------------------------

// appendInt: first là các bit cờ ở phần cao của byte đầu.
func appendInt(dst []byte, first byte, n uint8, v uint64) []byte {
	max := uint64(1)<<n - 1
	if v < max {
		return append(dst, first|byte(v))
	}
	dst = append(dst, first|byte(max))
	v -= max
	for v >= 128 {
		dst = append(dst, byte(v&0x7f)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

// readInt đọc số nguyên prefix n bit từ p. Trần 2^32: số lớn hơn không có
// nghĩa nào trong HPACK (chỉ số, độ dài chuỗi, kích thước bảng) và để vòng
// lặp continuation vô hạn (0xff 0xff ...) không tràn uint64.
func readInt(p []byte, n uint8) (v uint64, rest []byte, err error) {
	if len(p) == 0 {
		return 0, nil, decodeErr("số nguyên cắt cụt")
	}
	max := uint64(1)<<n - 1
	v = uint64(p[0]) & max
	p = p[1:]
	if v < max {
		return v, p, nil
	}
	var m uint
	for {
		if len(p) == 0 {
			return 0, nil, decodeErr("số nguyên cắt cụt")
		}
		b := p[0]
		p = p[1:]
		v += uint64(b&0x7f) << m
		if v > 1<<32 {
			return 0, nil, decodeErr("số nguyên quá lớn")
		}
		if b&0x80 == 0 {
			return v, p, nil
		}
		m += 7
		if m > 28 {
			return 0, nil, decodeErr("số nguyên quá dài")
		}
	}
}

// --- chuỗi (§5.2) --------------------------------------------------------

func appendString(dst []byte, s string) []byte {
	if hl := HuffmanEncodedLen(s); hl <= len(s) && len(s) > 0 { // bằng nhau: Huffman (RFC C.6.2 "307" chọn vậy)
		dst = appendInt(dst, 0x80, 7, uint64(hl))
		return AppendHuffman(dst, s)
	}
	dst = appendInt(dst, 0, 7, uint64(len(s)))
	return append(dst, s...)
}

// readString: độ dài kiểm với phần còn lại của block TRƯỚC khi cấp phát (I2) —
// và với budget còn lại của header list (maxLen).
func readString(p []byte, maxLen uint64) (s string, rest []byte, err error) {
	if len(p) == 0 {
		return "", nil, decodeErr("chuỗi cắt cụt")
	}
	huff := p[0]&0x80 != 0
	n, p, err := readInt(p, 7)
	if err != nil {
		return "", nil, err
	}
	if n > uint64(len(p)) {
		return "", nil, decodeErr("chuỗi dài %d > %d byte còn lại", n, len(p))
	}
	if !huff {
		if n > maxLen {
			return "", nil, ErrListTooLarge
		}
		return string(p[:n]), p[n:], nil
	}
	s, err = HuffmanDecode(p[:n], maxLen)
	return s, p[n:], err
}
