package hpack

// Huffman (RFC 7541 §5.2, Appendix B). Giải mã bằng cây nhị phân dựng từ
// bảng mã lúc init — đi từng bit. Chậm hơn bảng tra 8 bit (cách của x/net)
// nhưng ba luật lỗi của §5.2 hiện ra thẳng trong code:
//  1. padding > 7 bit ⇒ lỗi;
//  2. padding không phải toàn bit 1 (tiền tố của EOS) ⇒ lỗi;
//  3. gặp nguyên mã EOS trong chuỗi ⇒ lỗi.

const eosSym = 256

// huffNode: con [0]/[1] là chỉ số node (0 = chưa có); sym ≥ 0 ở lá.
type huffNode struct {
	next [2]int32
	sym  int32
}

var huffTree []huffNode

func init() {
	huffTree = []huffNode{{sym: -1}}
	add := func(code uint32, n uint8, sym int32) {
		cur := int32(0)
		for i := int(n) - 1; i >= 0; i-- {
			b := (code >> uint(i)) & 1
			if huffTree[cur].next[b] == 0 {
				huffTree = append(huffTree, huffNode{sym: -1})
				huffTree[cur].next[b] = int32(len(huffTree) - 1)
			}
			cur = huffTree[cur].next[b]
		}
		huffTree[cur].sym = sym
	}
	for i := 0; i < 256; i++ {
		add(huffmanCodes[i], huffmanCodeLen[i], int32(i))
	}
	add(0x3fffffff, 30, eosSym) // EOS: 30 bit 1
}

// HuffmanDecode giải mã p; kết quả dài hơn maxLen ⇒ ErrListTooLarge (dừng
// sớm, không cấp phát theo đầu vào xấu — Huffman nở tối đa 8/5 lần).
func HuffmanDecode(p []byte, maxLen uint64) (string, error) {
	out := make([]byte, 0, len(p)*8/5+1)
	cur := int32(0)
	pad := 0     // số bit đã đi từ biên ký tự gần nhất
	ones := true // các bit đó toàn 1?
	for _, b := range p {
		for i := 7; i >= 0; i-- {
			bit := (b >> uint(i)) & 1
			cur = huffTree[cur].next[bit]
			if cur == 0 {
				return "", decodeErr("mã Huffman không hợp lệ")
			}
			pad++
			ones = ones && bit == 1
			if s := huffTree[cur].sym; s >= 0 {
				if s == eosSym {
					return "", decodeErr("EOS trong chuỗi Huffman")
				}
				if uint64(len(out)) >= maxLen {
					return "", ErrListTooLarge
				}
				out = append(out, byte(s))
				cur, pad, ones = 0, 0, true
			}
		}
	}
	if pad > 7 {
		return "", decodeErr("padding Huffman %d bit > 7", pad)
	}
	if !ones {
		return "", decodeErr("padding Huffman không phải tiền tố EOS")
	}
	return string(out), nil
}

// HuffmanEncodedLen: số byte sau khi mã hoá s.
func HuffmanEncodedLen(s string) int {
	var bits uint64
	for i := 0; i < len(s); i++ {
		bits += uint64(huffmanCodeLen[s[i]])
	}
	return int((bits + 7) / 8)
}

// AppendHuffman mã hoá s vào dst; byte cuối đệm bằng bit 1 (tiền tố EOS).
func AppendHuffman(dst []byte, s string) []byte {
	var acc uint64
	var n uint
	for i := 0; i < len(s); i++ {
		c := s[i]
		acc = acc<<huffmanCodeLen[c] | uint64(huffmanCodes[c])
		n += uint(huffmanCodeLen[c])
		for n >= 8 {
			n -= 8
			dst = append(dst, byte(acc>>n))
		}
	}
	if n > 0 {
		acc = acc<<(8-n) | (1<<(8-n) - 1)
		dst = append(dst, byte(acc))
	}
	return dst
}
