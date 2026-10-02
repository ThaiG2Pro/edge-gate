package hpack

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func vec(t *testing.T, id string) rfcVector {
	for _, v := range rfcVectors {
		if v.id == id {
			return v
		}
	}
	t.Fatalf("không có vector %s", id)
	return rfcVector{}
}

func fieldsOf(list []string) []HeaderField {
	out := make([]HeaderField, len(list))
	for i, s := range list {
		// tên có thể bắt đầu bằng ':' — cắt ở ": " đầu tiên SAU ký tự đầu
		k := strings.Index(s[1:], ": ") + 1
		out[i] = HeaderField{Name: s[:k], Value: s[k+2:]}
	}
	return out
}

func sameFields(a, b []HeaderField) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Value != b[i].Value {
			return false
		}
	}
	return true
}

// TestRFC7541AppendixC: mọi ví dụ của Appendix C, decode theo chuỗi (C.3, C.4,
// C.5, C.6 dùng chung một decoder qua ba block — bảng động nối tiếp) và so cả
// header list lẫn "Table size" RFC in sau mỗi block.
func TestRFC7541AppendixC(t *testing.T) {
	groups := []struct {
		ids   []string
		table uint32
	}{
		{[]string{"C.2.1"}, 4096}, {[]string{"C.2.2"}, 4096}, {[]string{"C.2.3"}, 4096}, {[]string{"C.2.4"}, 4096},
		{[]string{"C.3.1", "C.3.2", "C.3.3"}, 4096},
		{[]string{"C.4.1", "C.4.2", "C.4.3"}, 4096},
		{[]string{"C.5.1", "C.5.2", "C.5.3"}, 256}, // C.5: SETTINGS_HEADER_TABLE_SIZE = 256
		{[]string{"C.6.1", "C.6.2", "C.6.3"}, 256},
	}
	n := 0
	for _, g := range groups {
		d := NewDecoder(g.table, 0)
		for _, id := range g.ids {
			v := vec(t, id)
			raw, err := hex.DecodeString(v.hex)
			if err != nil {
				t.Fatal(err)
			}
			got, err := d.Decode(raw)
			if err != nil {
				t.Fatalf("%s: %v", id, err)
			}
			if want := fieldsOf(v.list); !sameFields(got, want) {
				t.Fatalf("%s: got %v want %v", id, got, want)
			}
			if int(d.TableSize()) != v.size {
				t.Fatalf("%s: table size %d, RFC %d", id, d.TableSize(), v.size)
			}
			n++
		}
	}
	if n != len(rfcVectors) {
		t.Fatalf("chạy %d/%d vector", n, len(rfcVectors))
	}
}

// TestEncoderMatchesRFC: encoder của ta (Huffman khi ngắn hơn, incremental
// indexing) phải sinh ĐÚNG byte của C.4 và C.6 — hai ví dụ RFC dùng Huffman và
// cùng chiến lược. Khớp từng byte = bảng mã Huffman, chỉ số động, và đuổi mục
// (C.6.2/C.6.3 đuổi với bảng 256) đều đúng.
func TestEncoderMatchesRFC(t *testing.T) {
	for _, g := range []struct {
		ids   []string
		table uint32
	}{{[]string{"C.4.1", "C.4.2", "C.4.3"}, 4096}, {[]string{"C.6.1", "C.6.2", "C.6.3"}, 256}} {
		e := NewEncoder()
		e.t.maxSize = g.table // không qua SetMaxTableSize: RFC không in size update
		for _, id := range g.ids {
			v := vec(t, id)
			got := hex.EncodeToString(e.Encode(nil, fieldsOf(v.list)))
			if got != v.hex {
				t.Fatalf("%s:\n got %s\nwant %s", id, got, v.hex)
			}
		}
	}
}

func TestHuffmanRoundTrip(t *testing.T) {
	for _, s := range []string{"", "a", "www.example.com", "no-cache", "\x00\xff\x7f", strings.Repeat("z", 300)} {
		enc := AppendHuffman(nil, s)
		if len(enc) != HuffmanEncodedLen(s) {
			t.Fatalf("%q: len %d vs %d", s, len(enc), HuffmanEncodedLen(s))
		}
		got, err := HuffmanDecode(enc, 1<<20)
		if err != nil || got != s {
			t.Fatalf("%q: %q %v", s, got, err)
		}
	}
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	if got, err := HuffmanDecode(AppendHuffman(nil, string(all)), 1<<20); err != nil || got != string(all) {
		t.Fatalf("256 byte: %v", err)
	}
}

// Ba luật lỗi của §5.2.
func TestHuffmanErrors(t *testing.T) {
	// "a" = 00011 (5 bit) + 3 bit đệm 111 = 0x1f. Đệm 0 ⇒ lỗi (không phải tiền tố EOS).
	if _, err := HuffmanDecode([]byte{0x18}, 100); !errors.Is(err, ErrDecode) {
		t.Fatalf("đệm 0: %v", err)
	}
	if s, err := HuffmanDecode([]byte{0x1f}, 100); err != nil || s != "a" {
		t.Fatalf("đệm đúng: %q %v", s, err)
	}
	// Một byte 0xff toàn đệm (8 bit > 7) ⇒ lỗi.
	if _, err := HuffmanDecode([]byte{0x1f, 0xff}, 100); !errors.Is(err, ErrDecode) {
		t.Fatalf("đệm 8 bit: %v", err)
	}
	// EOS đủ 30 bit ⇒ lỗi.
	if _, err := HuffmanDecode([]byte{0xff, 0xff, 0xff, 0xff}, 100); !errors.Is(err, ErrDecode) {
		t.Fatalf("EOS: %v", err)
	}
}

func TestIntegerEdge(t *testing.T) {
	// RFC 7541 C.1.2: 1337 với prefix 5 bit = 1f 9a 0a.
	if got := appendInt(nil, 0, 5, 1337); !bytes.Equal(got, []byte{0x1f, 0x9a, 0x0a}) {
		t.Fatalf("1337: % x", got)
	}
	v, _, err := readInt([]byte{0x1f, 0x9a, 0x0a}, 5)
	if err != nil || v != 1337 {
		t.Fatalf("đọc 1337: %d %v", v, err)
	}
	// Continuation vô hạn ⇒ lỗi, không lặp mãi / tràn.
	if _, _, err := readInt(bytes.Repeat([]byte{0xff}, 20), 7); !errors.Is(err, ErrDecode) {
		t.Fatalf("tràn: %v", err)
	}
}

func TestDecoderErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
	}{
		{"index 0", []byte{0x80}},
		{"index ngoài bảng", []byte{0xbe}}, // 62 khi bảng động rỗng
		{"chuỗi dài hơn block", []byte{0x40, 0x0a, 'a'}},
		{"size update vượt trần", []byte{0x3f, 0xe2, 0x1f}}, // 31+98+3968 = 4097 > 4096
		{"size update giữa block", []byte{0x82, 0x20}},
	}
	for _, c := range cases {
		if _, err := NewDecoder(4096, 0).Decode(c.raw); !errors.Is(err, ErrDecode) {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	// Size update đầu block hợp lệ, kể cả hai cái liên tiếp.
	if _, err := NewDecoder(4096, 0).Decode([]byte{0x20, 0x3f, 0x01, 0x82}); err != nil {
		t.Fatalf("size update hợp lệ: %v", err)
	}
}

// Trần header list kiểm TRONG lúc decode (I2): một value 100 KB không được
// cấp phát khi trần là 1 KB.
func TestDecoderListLimit(t *testing.T) {
	e := NewEncoder()
	block := e.Encode(nil, []HeaderField{{Name: "x", Value: strings.Repeat("v", 2000)}})
	if _, err := NewDecoder(4096, 1024).Decode(block); !errors.Is(err, ErrListTooLarge) {
		t.Fatalf("trần list: %v", err)
	}
	if _, err := NewDecoder(4096, 4096).Decode(block); err != nil {
		t.Fatalf("dưới trần: %v", err)
	}
}

// Peer hạ bảng ⇒ block kế mở đầu bằng size update; decoder bên kia theo được.
func TestEncoderTableSizeUpdate(t *testing.T) {
	e := NewEncoder()
	d := NewDecoder(4096, 0)
	h := []HeaderField{{Name: "custom", Value: "v1"}}
	for i := 0; i < 2; i++ {
		if _, err := d.Decode(e.Encode(nil, h)); err != nil {
			t.Fatal(err)
		}
	}
	e.SetMaxTableSize(0)
	e.SetMaxTableSize(100)
	b := e.Encode(nil, h)
	if b[0] != 0x20 { // update về 0 (trần nhỏ nhất) trước
		t.Fatalf("thiếu size update 0: % x", b)
	}
	got, err := d.Decode(b)
	if err != nil || len(got) != 1 || got[0].Value != "v1" {
		t.Fatalf("%v %v", got, err)
	}
	if d.TableLen() != 1 {
		t.Fatalf("bảng sau update: %d mục", d.TableLen())
	}
}

func TestSensitiveNeverIndexed(t *testing.T) {
	e := NewEncoder()
	b := e.Encode(nil, []HeaderField{{Name: "authorization", Value: "secret", Sensitive: true}})
	if b[0]&0xf0 != 0x10 {
		t.Fatalf("không phải never-indexed: % x", b)
	}
	got, err := NewDecoder(4096, 0).Decode(b)
	if err != nil || !got[0].Sensitive {
		t.Fatalf("%v %v", got, err)
	}
}

func FuzzDecode(f *testing.F) {
	for _, v := range rfcVectors {
		raw, _ := hex.DecodeString(v.hex)
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, p []byte) {
		d := NewDecoder(4096, 16<<10)
		fs, err := d.Decode(p)
		if err != nil {
			return
		}
		// Decode được ⇒ encode lại rồi decode bằng decoder mới phải ra cùng list.
		got, err := NewDecoder(4096, 0).Decode(NewEncoder().Encode(nil, fs))
		if err != nil || !sameFields(got, fs) {
			t.Fatalf("round trip: %v", err)
		}
	})
}
