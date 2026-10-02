package lb

import (
	"math"
	"sync"
	"time"
)

// ewma: trung bình mũ của latency, decay theo THỜI GIAN (D3), đơn vị nanô giây.
//
//	decay = exp(-elapsed/tau); v = v·decay + sample·(1-decay)
//
// score() cũng áp decay từ lần cập nhật cuối tới "bây giờ" (không ghi lại):
// một node bị điểm xấu rồi không được chọn nữa thì điểm tự hạ theo thời gian
// ⇒ lại được thử ⇒ có mẫu mới. Decay theo SỐ REQUEST (tag nodefenselb) không
// có đường về đó — bẫy ROADMAP phase 6, xem TestP2CRecovers.
//
// Cold-start: chưa có mẫu ⇒ 0 (được thử ngay). P6-3: điểm 0 chỉ đúng cho lượt
// THỬ ĐẦU — Backend.score phạt node chưa mẫu mà đang có inflight (Finagle).
type ewma struct {
	mu    sync.Mutex
	v     float64
	last  time.Time
	first time.Time // mẫu đầu — P6-2: khởi động bằng trung bình cộng trong tau đầu
	n     int64
	tau   time.Duration
}

func (e *ewma) observe(sample time.Duration, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := float64(sample)
	switch {
	case e.n == 0:
		e.v = s
		e.first = now
	case ewmaDecayByTime && now.Sub(e.first) < e.tau:
		// P6-2 (trả 2026-10-02): chưa đủ tau lịch sử ⇒ mọi mẫu nặng như nhau
		// (trung bình cộng). Bản cũ đặt v = mẫu ĐẦU rồi mẫu sau chỉ nặng ~dt/tau
		// (4·10⁻⁴ ở 2.5k rps): một mẫu đầu ngoại lai 40 ms (máy 2 ms) làm node bị
		// chấm 20x suốt ~ln(20)·tau ≈ 3 s — P2C chia 15/29/28/28 % trên 4 node
		// giống hệt. Đây là EWMA "hiệu chỉnh độ lệch khởi đầu" theo thời gian.
		e.v += (s - e.v) / float64(e.n+1)
	case ewmaDecayByTime:
		d := math.Exp(-now.Sub(e.last).Seconds() / e.tau.Seconds())
		e.v = e.v*d + s*(1-d)
	default:
		const d = 0.5 // nodefenselb: mỗi mẫu một nấc, thời gian không có tiếng nói
		e.v = e.v*d + s*(1-d)
	}
	e.n++
	e.last = now
}

func (e *ewma) score(now time.Time) float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.n == 0 {
		return 0
	}
	if !ewmaDecayByTime {
		return e.v
	}
	el := now.Sub(e.last)
	if el <= 0 {
		return e.v
	}
	return e.v * math.Exp(-el.Seconds()/e.tau.Seconds())
}
