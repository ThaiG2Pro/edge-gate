package httpx

import "time"

// Limits là hàng rào chống DoS ở tầng parser. Mọi giá trị đều phải có trần —
// một parser không giới hạn là một lời mời tấn công header bomb / Slowloris.
type Limits struct {
	// MaxLineBytes: trần cho request line, status line và mỗi dòng header.
	MaxLineBytes int
	// MaxHeaderBytes: trần cho TỔNG kích thước block header.
	//
	// 8KB (mặc định của nginx) làm vỡ traffic thật: JWT trong Authorization
	// cộng cookie enterprise vượt 8KB rất dễ. Go stdlib mặc định 1MB; nginx
	// thực tế cho 4x8KB = 32KB qua large_client_header_buffers. Ta chọn 64KB.
	MaxHeaderBytes int
	// MaxHeaderCount: trần số dòng header, chống tấn công "1 triệu header 1 byte"
	// vốn lọt qua được giới hạn tổng bytes nhưng làm nổ map.
	MaxHeaderCount int
	// HeaderTimeout: deadline để đọc XONG toàn bộ request line + header.
	// Đây là phòng tuyến chính chống Slowloris (giữ connection bằng cách nhỏ
	// giọt 1 byte header mỗi 10 giây).
	HeaderTimeout time.Duration
	// BodyTimeout: deadline cho toàn bộ body.
	BodyTimeout time.Duration
	// IdleTimeout: thời gian tối đa giữ một keep-alive connection rỗi.
	IdleTimeout time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		MaxLineBytes:   8 * 1024,
		MaxHeaderBytes: 64 * 1024,
		MaxHeaderCount: 100,
		HeaderTimeout:  10 * time.Second,
		BodyTimeout:    30 * time.Second,
		IdleTimeout:    60 * time.Second,
	}
}
