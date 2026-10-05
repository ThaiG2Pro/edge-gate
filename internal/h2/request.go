package h2

import (
	"strconv"
	"strings"

	"github.com/ThaiG2Pro/edge-gate/internal/h2/hpack"
)

// Request: head của một stream đã kiểm (§8.3.1). Header thường giữ tên
// lowercase như trên dây; cookie nhiều field đã nối bằng "; " (§8.2.3).
type Request struct {
	Method, Scheme, Authority, Path string
	Header                          []hpack.HeaderField
	// ContentLength: -1 = không khai báo.
	ContentLength int64
	// EndStream: HEADERS mang END_STREAM ⇒ không có body.
	EndStream bool
}

// Get: value đầu tiên của tên (lowercase).
func (r *Request) Get(name string) string {
	for _, f := range r.Header {
		if f.Name == name {
			return f.Value
		}
	}
	return ""
}

// connectionSpecific (§8.2.2): ở h2 là malformed — và chính là thứ downgrade
// sang h1 sẽ đọc như framing.
var connectionSpecific = map[string]bool{
	"connection": true, "proxy-connection": true, "keep-alive": true,
	"transfer-encoding": true, "upgrade": true,
}

func malformed(id uint32, why string) error {
	return StreamError{id, ErrProtocol, "malformed: " + why}
}

// validName / validValue (§8.2.1): luật tối thiểu bắt buộc.
func validName(n string, pseudo bool) bool {
	if n == "" {
		return false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if c <= 0x20 || (c >= 'A' && c <= 'Z') || c >= 0x7f {
			return false
		}
		if c == ':' && !(pseudo && i == 0) {
			return false
		}
	}
	return true
}

func validValue(v string) bool {
	if v == "" {
		return true
	}
	if v[0] == ' ' || v[0] == '\t' || v[len(v)-1] == ' ' || v[len(v)-1] == '\t' {
		return false
	}
	return !strings.ContainsAny(v, "\x00\r\n")
}

// buildRequest: pseudo-header + header thường → Request. Luật pseudo (§8.3)
// luôn bật (giao thức); luật downgrade (D6 c) tắt được bằng nodefense10.
func buildRequest(id uint32, fields []hpack.HeaderField, end bool) (*Request, error) {
	r := &Request{ContentLength: -1, EndStream: end}
	regular := false
	var cookies []string
	for _, f := range fields {
		if strings.HasPrefix(f.Name, ":") {
			if regular {
				return nil, malformed(id, "pseudo-header sau header thường")
			}
			var dst *string
			switch f.Name {
			case ":method":
				dst = &r.Method
			case ":scheme":
				dst = &r.Scheme
			case ":authority":
				dst = &r.Authority
			case ":path":
				dst = &r.Path
			default:
				return nil, malformed(id, "pseudo-header lạ "+f.Name)
			}
			if *dst != "" {
				return nil, malformed(id, "pseudo-header lặp "+f.Name)
			}
			if f.Value == "" && f.Name != ":authority" {
				return nil, malformed(id, "pseudo-header rỗng "+f.Name)
			}
			*dst = f.Value
			continue
		}
		regular = true
		if validateDowngrade {
			if !validName(f.Name, false) {
				return nil, malformed(id, "tên field không hợp lệ "+strconv.Quote(f.Name))
			}
			if !validValue(f.Value) {
				return nil, malformed(id, "value có CR/LF/NUL hoặc khoảng trắng hai đầu: "+f.Name)
			}
			if connectionSpecific[f.Name] {
				return nil, malformed(id, "header connection-specific "+f.Name)
			}
			if f.Name == "te" && f.Value != "trailers" {
				return nil, malformed(id, "te khác trailers")
			}
		}
		switch f.Name {
		case "cookie":
			cookies = append(cookies, f.Value)
			continue
		case "content-length":
			n, err := strconv.ParseInt(f.Value, 10, 64)
			if err != nil || n < 0 {
				return nil, malformed(id, "content-length không hợp lệ")
			}
			if r.ContentLength >= 0 && r.ContentLength != n {
				return nil, malformed(id, "hai content-length khác nhau")
			}
			if r.ContentLength >= 0 {
				continue // trùng giá trị: giữ một
			}
			r.ContentLength = n
		}
		r.Header = append(r.Header, f)
	}
	if len(cookies) > 0 {
		r.Header = append(r.Header, hpack.HeaderField{Name: "cookie", Value: strings.Join(cookies, "; ")})
	}
	if r.Method == "CONNECT" {
		return nil, malformed(id, "CONNECT chưa hỗ trợ") // §8.5: không :scheme/:path; proxy không tunnel (D8 phase 3)
	}
	if r.Method == "" || r.Scheme == "" || r.Path == "" {
		return nil, malformed(id, "thiếu :method/:scheme/:path")
	}
	if r.Path[0] != '/' && !(r.Method == "OPTIONS" && r.Path == "*") {
		return nil, malformed(id, ":path không phải origin-form")
	}
	if validateDowngrade && end && r.ContentLength > 0 {
		return nil, malformed(id, "content-length > 0 nhưng END_STREAM trên HEADERS")
	}
	return r, nil
}

// checkTrailers (§8.1): trailer không được có pseudo-header.
func checkTrailers(id uint32, fields []hpack.HeaderField) error {
	for _, f := range fields {
		if strings.HasPrefix(f.Name, ":") {
			return malformed(id, "pseudo-header trong trailer")
		}
		if validateDowngrade && (!validName(f.Name, false) || !validValue(f.Value)) {
			return malformed(id, "trailer không hợp lệ")
		}
	}
	return nil
}
