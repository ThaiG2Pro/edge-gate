package httpx

import (
	"io"
	"net/textproto"
	"sort"
	"strings"
)

// Header giữ header HTTP đúng semantics RFC 9110: tên field không phân biệt
// hoa/thường, và một field name có thể xuất hiện nhiều lần (Set-Cookie, Via,
// X-Forwarded-For...). Vì vậy value là slice, không phải string.
type Header map[string][]string

func canonical(key string) string {
	return textproto.CanonicalMIMEHeaderKey(key)
}

// Add thêm một value mới, giữ lại các value đã có.
func (h Header) Add(key, value string) {
	k := canonical(key)
	h[k] = append(h[k], value)
}

// Set ghi đè toàn bộ value của field.
func (h Header) Set(key, value string) {
	h[canonical(key)] = []string{value}
}

// Get trả về value đầu tiên, hoặc "" nếu không có.
func (h Header) Get(key string) string {
	if v := h[canonical(key)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// Values trả về toàn bộ value của field.
func (h Header) Values(key string) []string {
	return h[canonical(key)]
}

func (h Header) Has(key string) bool {
	return len(h[canonical(key)]) > 0
}

func (h Header) Del(key string) {
	delete(h, canonical(key))
}

// Count đếm tổng số dòng header (không phải số field name).
func (h Header) Count() int {
	n := 0
	for _, v := range h {
		n += len(v)
	}
	return n
}

// hopByHop là các field chỉ có ý nghĩa trên một chặng TCP duy nhất, proxy
// không được forward sang chặng tiếp theo.
//
// RFC 9110 §7.6.1 liệt kê: Connection, Proxy-Connection, Keep-Alive, TE,
// Trailer, Transfer-Encoding, Upgrade.
//
// Proxy-Authenticate / Proxy-Authorization KHÔNG nằm trong danh sách đó — chúng
// là credential dành riêng cho chặng proxy này (RFC 9110 §11.7), nên cũng không
// được forward, nhưng theo một cơ chế khác. Ta strip cả hai nhóm.
var hopByHop = map[string]bool{
	"Connection":          true,
	"Proxy-Connection":    true, // non-standard nhưng client thật vẫn gửi
	"Keep-Alive":          true,
	"Te":                  true, // CanonicalMIMEHeaderKey("TE") == "Te"
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
}

// StripHopByHop xoá các header hop-by-hop khỏi h, bao gồm cả những field được
// liệt kê động trong value của `Connection:` (ví dụ `Connection: X-Foo, close`
// nghĩa là X-Foo cũng là hop-by-hop cho request này).
//
// Bỏ sót bước "đọc Connection để tìm thêm field" là bug kinh điển của proxy tự
// viết: nó cho attacker smuggle header xuyên qua proxy.
func (h Header) StripHopByHop() {
	for _, v := range h.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			h.Del(tok)
		}
	}
	for name := range hopByHop {
		delete(h, name)
	}
}

// Write serialize header ra dạng wire: "Key: value\r\n" cho từng value.
// Tên field sắp xếp để output xác định (map Go xáo thứ tự) — test so byte
// được, và hai lần serialize cùng header cho cùng wire. Một lần w.Write.
func (h Header) Write(w io.Writer) error {
	buf, err := h.appendWire(nil)
	if err != nil {
		return err
	}
	_, err = w.Write(buf)
	return err
}

// appendWire nối dạng wire vào buf. Từ chối CR/LF/NUL trong tên và giá trị:
// đó là header injection — một giá trị "x\r\nContent-Length: 0" nếu ghi ra sẽ
// tạo header mới ở phía nhận.
func (h Header) appendWire(buf []byte) ([]byte, error) {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !safeWire(name) {
			return nil, ErrHeaderInjection
		}
		for _, v := range h[name] {
			if !safeWire(v) {
				return nil, ErrHeaderInjection
			}
			buf = append(buf, name...)
			buf = append(buf, ':', ' ')
			buf = append(buf, v...)
			buf = append(buf, '\r', '\n')
		}
	}
	return buf, nil
}

func safeWire(s string) bool {
	return strings.IndexAny(s, "\r\n\x00") < 0
}

// Clone tạo bản sao sâu, để việc rewrite header cho upstream không làm bẩn
// request gốc của client.
func (h Header) Clone() Header {
	out := make(Header, len(h))
	for k, v := range h {
		cp := make([]string, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}
