//go:build !nodefense7

package proxy

// Phòng tuyến phase 7 (D12). `-tags nodefense7` tắt và các lab `*-nodefense`
// PHẢI đỏ đúng dòng. Tag riêng của phase (P4-4). Shedding tắt bằng cấu hình
// (MaxInflight 0), không cần tag.
const (
	// headerTimeoutOn: head request phải xong trong HeaderTimeout tính từ byte
	// đầu (I3, chống Slowloris). false ⇒ không deadline cho head (G2 b).
	headerTimeoutOn = true
	// limitByTrustedIP: khoá rate limit = clientIP SAU ranh giới tin cậy
	// (phase 4 D9). false ⇒ khoá = phần tử đầu của X-Forwarded-For thô —
	// bypass bằng một header giả (G4, I6).
	limitByTrustedIP = true
)
