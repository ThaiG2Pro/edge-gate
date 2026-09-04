//go:build !nodefensepool

package proxy

// Phòng tuyến phase 5. `-tags nodefensepool` tắt và `make poollab-nodefense`
// PHẢI đỏ (G3). Tag riêng, không dùng chung `nodefense` của phase 3: dùng chung
// thì phản chứng đỏ vì io.Copy thô (bẫy #2) chứ không phải vì connection bẩn —
// đỏ đúng chỗ mới có giá trị (P4-4).
const (
	// poolCheckClean (D2/D7): connection upstream chỉ về pool khi SẠCH — body
	// response đã tới io.EOF, không byte thừa trong bufio, upstream không đòi
	// đóng, không lỗi nào ở cả hai chiều. false ⇒ về pool ngay khi có head
	// response: đuôi body của client A thành status-line của client B.
	poolCheckClean = true
)
