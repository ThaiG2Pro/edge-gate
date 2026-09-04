//go:build !nodefense

package proxy

// Hai phòng tuyến của phase 3. `-tags nodefense` tắt cả hai và bộ test PHẢI
// đỏ (G1, G5) — cùng khuôn với internal/frame (phase 1). Xem defense_nodefense.go.
// Phòng tuyến phase 5 có tag RIÊNG (`nodefensepool`, defense_pool.go) — trả
// một phần P4-4: tag chung làm phản chứng phase 5 đỏ vì bẫy phase 3.
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
