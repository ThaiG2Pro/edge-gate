//go:build !nodefense10

package h2

// Phase 10 D6. Build tag nodefense10 tắt tất cả để bài phản chứng phải đỏ.
const (
	// holdSlotUntilExit (D6 a): slot MAX_CONCURRENT_STREAMS chỉ trả khi goroutine
	// stream THOÁT. Tắt ⇒ trả lúc nhận RST_STREAM ⇒ Rapid Reset (CVE-2023-44487):
	// client mở-rồi-huỷ liên tục, server chạy không giới hạn handler.
	holdSlotUntilExit = true
	// capResetRate (D6 a′, thêm 18:08 sau khi viết h2.go của proxy — trước khi
	// đo): > 2×MAX_CONCURRENT_STREAMS RST_STREAM của client trong 1 s ⇒ GOAWAY
	// ENHANCE_YOUR_CALM. Lý do: proxy huỷ upstream khi RST ⇒ slot về nhanh ⇒
	// (a) một mình không chặn được tốc độ việc bơm sang upstream.
	capResetRate = true
	// capHeaderBlock (D6 b): HEADERS + CONTINUATION cộng dồn có trần. Tắt ⇒
	// CONTINUATION flood (CVE-2024-27316) đệm không giới hạn.
	capHeaderBlock = true
	// validateDowngrade (D6 c): ký tự cấm, header connection-specific, te,
	// content-length ≠ tổng DATA ⇒ malformed (RFC 9113 §8.1.1, §8.2.1-8.2.2).
	validateDowngrade = true
	// coalesceCtl / collapseSettings (P10-4, trả 2026-10-02): phản hồi điều
	// khiển của goroutine đọc chỉ Flush khi buffer đọc cạn; nhiều
	// INITIAL_WINDOW_SIZE trong một SETTINGS áp một lần. Tắt ⇒ một Flush mỗi
	// PING/RST và O(setting × stream) mỗi frame SETTINGS.
	coalesceCtl      = true
	collapseSettings = true
)
