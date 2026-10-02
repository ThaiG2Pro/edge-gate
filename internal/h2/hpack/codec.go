package hpack

// Decoder: một cho mỗi chiều nhận của một connection (bảng động riêng).
type Decoder struct {
	t dynTable
	// allowed: trần bảng do TA công bố (SETTINGS_HEADER_TABLE_SIZE). Encoder
	// bên kia được phép hạ (size update) nhưng không được vượt.
	allowed uint32
	// maxList: trần tổng Size() của header list (SETTINGS_MAX_HEADER_LIST_SIZE).
	// 0 = không trần.
	maxList uint64
}

// NewDecoder với trần bảng động maxTable (mặc định của chuẩn 4096).
func NewDecoder(maxTable uint32, maxList uint64) *Decoder {
	d := &Decoder{allowed: maxTable, maxList: maxList}
	d.t.maxSize = maxTable
	return d
}

// TableSize: kích thước bảng động hiện tại (test, lab).
func (d *Decoder) TableSize() uint32 { return d.t.size }

// TableLen: số mục bảng động.
func (d *Decoder) TableLen() int { return len(d.t.ents) }

// Decode giải nén MỘT header block hoàn chỉnh (HEADERS + mọi CONTINUATION đã
// nối). Lỗi ⇒ bảng động không còn tin được ⇒ caller phải đóng connection.
func (d *Decoder) Decode(p []byte) ([]HeaderField, error) {
	var out []HeaderField
	budget := d.maxList
	if budget == 0 {
		budget = 1 << 62
	}
	first := true
	for len(p) > 0 {
		b := p[0]
		var err error
		switch {
		case b&0x80 != 0: // indexed (§6.1)
			var i uint64
			if i, p, err = readInt(p, 7); err != nil {
				return nil, err
			}
			f, ok := d.t.at(i)
			if !ok {
				return nil, decodeErr("chỉ số %d ngoài bảng", i)
			}
			if out, budget, err = emit(out, budget, f); err != nil {
				return nil, err
			}
		case b&0xe0 == 0x20: // dynamic table size update (§6.3)
			// §4.2: chỉ ở ĐẦU block. Ở giữa block ⇒ lỗi.
			if !first {
				return nil, decodeErr("size update giữa block")
			}
			var n uint64
			if n, p, err = readInt(p, 5); err != nil {
				return nil, err
			}
			if n > uint64(d.allowed) {
				return nil, decodeErr("size update %d > %d đã công bố", n, d.allowed)
			}
			d.t.setMax(uint32(n))
			continue // vẫn tính là "đầu block": nhiều update liên tiếp hợp lệ
		default: // literal (§6.2): 01 = incremental indexing, 0000 = without, 0001 = never
			var n uint8
			index, sensitive := false, false
			switch {
			case b&0xc0 == 0x40:
				n, index = 6, true
			case b&0xf0 == 0x10:
				n, sensitive = 4, true
			case b&0xf0 == 0x00:
				n = 4
			}
			var ni uint64
			if ni, p, err = readInt(p, n); err != nil {
				return nil, err
			}
			var f HeaderField
			if ni > 0 {
				nf, ok := d.t.at(ni)
				if !ok {
					return nil, decodeErr("chỉ số tên %d ngoài bảng", ni)
				}
				f.Name = nf.Name
			} else {
				if f.Name, p, err = readString(p, budget); err != nil {
					return nil, err
				}
			}
			if f.Value, p, err = readString(p, budget); err != nil {
				return nil, err
			}
			f.Sensitive = sensitive
			if index {
				d.t.add(f)
			}
			if out, budget, err = emit(out, budget, f); err != nil {
				return nil, err
			}
		}
		first = false
	}
	return out, nil
}

func emit(out []HeaderField, budget uint64, f HeaderField) ([]HeaderField, uint64, error) {
	sz := uint64(f.Size())
	if sz > budget {
		return nil, 0, ErrListTooLarge
	}
	return append(out, f), budget - sz, nil
}

// Encoder: một cho mỗi chiều gửi. Không an toàn cho goroutine — caller giữ
// khoá ghi (h2 D3: encode và ghi frame trong CÙNG khoá, để thứ tự cập nhật
// bảng = thứ tự frame lên dây).
type Encoder struct {
	t dynTable
	// pendingMax: peer vừa đổi SETTINGS_HEADER_TABLE_SIZE ⇒ block kế phải mở
	// đầu bằng size update (§4.2). -1 = không có.
	pendingMax int64
	minMax     int64 // trần nhỏ nhất kể từ block trước (§4.2: phải báo cả lần hạ)
}

// NewEncoder với bảng 4096 (giá trị mặc định của SETTINGS_HEADER_TABLE_SIZE).
func NewEncoder() *Encoder {
	e := &Encoder{pendingMax: -1, minMax: -1}
	e.t.maxSize = 4096
	return e
}

// SetMaxTableSize: peer công bố SETTINGS_HEADER_TABLE_SIZE = n. Ta dùng
// min(n, 4096) — được phép dùng nhỏ hơn trần peer cho.
func (e *Encoder) SetMaxTableSize(n uint32) {
	if n > 4096 {
		n = 4096
	}
	if e.minMax < 0 || int64(n) < e.minMax {
		e.minMax = int64(n)
	}
	e.pendingMax = int64(n)
	e.t.setMax(n)
}

// Encode nén fields thành một header block, append vào dst.
func (e *Encoder) Encode(dst []byte, fields []HeaderField) []byte {
	if e.pendingMax >= 0 {
		if e.minMax < e.pendingMax {
			dst = appendInt(dst, 0x20, 5, uint64(e.minMax))
		}
		dst = appendInt(dst, 0x20, 5, uint64(e.pendingMax))
		e.pendingMax, e.minMax = -1, -1
	}
	for _, f := range fields {
		dst = e.encodeField(dst, f)
	}
	return dst
}

func (e *Encoder) encodeField(dst []byte, f HeaderField) []byte {
	key := HeaderField{Name: f.Name, Value: f.Value}
	if !f.Sensitive {
		if i, ok := staticByPair[key]; ok {
			return appendInt(dst, 0x80, 7, uint64(i))
		}
		if i := e.dynIndex(key, true); i > 0 {
			return appendInt(dst, 0x80, 7, uint64(i))
		}
	}
	ni := staticByName[f.Name]
	if ni == 0 {
		ni = e.dynIndex(key, false)
	}
	switch {
	case f.Sensitive:
		dst = appendInt(dst, 0x10, 4, uint64(ni))
	case f.Size() > e.t.maxSize:
		dst = appendInt(dst, 0x00, 4, uint64(ni)) // không vừa bảng: đừng xoá sạch bảng vì nó
	default:
		dst = appendInt(dst, 0x40, 6, uint64(ni))
		defer e.t.add(f)
	}
	if ni == 0 {
		dst = appendString(dst, f.Name)
	}
	return appendString(dst, f.Value)
}

// dynIndex: chỉ số HPACK của mục động khớp (cả cặp hoặc chỉ tên); 0 = không.
func (e *Encoder) dynIndex(f HeaderField, pair bool) int {
	for k := len(e.t.ents) - 1; k >= 0; k-- {
		en := e.t.ents[k]
		if en.Name == f.Name && (!pair || en.Value == f.Value) {
			return len(staticTable) + len(e.t.ents) - k
		}
	}
	return 0
}
