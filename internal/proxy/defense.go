//go:build !nodefense && !nodefense3

package proxy

// Hai phòng tuyến của phase 3. `-tags nodefense3` tắt cả hai và bộ test PHẢI
// đỏ (G1, G5) — cùng khuôn với internal/frame (phase 1). Xem defense_nodefense.go.
// P4-4 (trả 2026-10-02): mỗi phase một tag (nodefense1/3/4, nodefensepool);
// `nodefense` trơn còn giữ = tắt cả 1+3+4 cho lệnh cũ trong diary.
const (
	// drainOnUpstreamError: upstream hỏng trước khi proxy đụng body request ⇒
	// đọc HẾT body rồi mới trả 502 và giữ connection. Không drain mà vẫn giữ
	// ⇒ byte body thành request-line của request kế tiếp (bẫy #3).
	drainOnUpstreamError = true
	// rawCopyResponse=false: body response đi qua httpx framing (CL / chunked /
	// tới-EOF), không io.Copy thô từ upstream. io.Copy thô chỉ dừng khi
	// upstream đóng — upstream keep-alive thì treo tới deadline (bẫy #2).
	rawCopyResponse = false
)
