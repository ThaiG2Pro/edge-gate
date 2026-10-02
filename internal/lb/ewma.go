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
	mu   sync.Mutex
	v    float64
	last time.Time
	n    int64
	tau  time.Duration
}

func (e *ewma) observe(sample time.Duration, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := float64(sample)
	if e.n == 0 {
		e.v = s
	} else if ewmaDecayByTime {
		d := math.Exp(-now.Sub(e.last).Seconds() / e.tau.Seconds())
		e.v = e.v*d + s*(1-d)
	} else {
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
