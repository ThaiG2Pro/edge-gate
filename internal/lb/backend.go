// Package lb chọn backend cho mỗi request (phase 6): round robin, least
// connections, P2C+EWMA, consistent hashing; kèm hai cơ chế sức khoẻ song
// song — active health check (goroutine nền) và passive outlier ejection
// (đếm lỗi trên traffic thật). Chỉ dùng net + httpx; không net/http.
//
// Ranh giới với proxy: proxy gọi đúng hai hàm — Pick(key) trước khi lấy
// connection, Done(b, latency, failed) sau khi exchange xong (D2). Inflight
// đếm theo REQUEST, không theo connection trong pool (P5-3).
package lb

import (
	"sync"
	"sync/atomic"
	"time"
)

// Backend là một upstream. Mọi trường đếm là atomic vì Pick/Done chạy từ
// nhiều goroutine client cùng lúc; health checker ghi healthy từ goroutine riêng.
type Backend struct {
	Addr string
	idx  int // vị trí trong Balancer.backends — chash dùng

	inflight atomic.Int64 // request đang gánh (Pick++ / Done--)
	picks    atomic.Int64
	fails    atomic.Int64 // tổng Done(failed=true)
	ewma     ewma

	// Active health (D6). Chỉ health checker ghi; Pick đọc.
	healthy atomic.Bool
	hmu     sync.Mutex
	hFails  int // lỗi probe liên tiếp
	hOK     int // probe tốt liên tiếp

	// Passive outlier (D7). ejectedUntil = 0 ⇒ không bị eject.
	consecFails  atomic.Int64
	ejectedUntil atomic.Int64 // UnixNano
	ejectCount   int          // số lần eject (backoff mũ) — dưới Balancer.mu
	lastEject    time.Time    // dưới Balancer.mu
	ejections    atomic.Int64

	// Phase 7 D5: circuit breaker 3 trạng thái trên outlier. closed ⇔
	// ejectedUntil == 0; open ⇔ now < ejectedUntil; half-open ⇔ ejectedUntil ≠ 0
	// ∧ now ≥ ejectedUntil. Ở half-open chỉ MỘT request được qua (probing CAS);
	// probeAt = lúc nó được Pick — Done nhận ra nó vì chỉ nó bắt đầu ≥ probeAt
	// (request cũ đang chạy từ trước khi eject đều bắt đầu sớm hơn).
	probing atomic.Bool
	probeAt time.Time // dưới Balancer.mu
	probes  atomic.Int64
	reopens atomic.Int64
}

// halfOpen: đã hết hạn eject nhưng chưa có request thử nào thành công.
func (b *Backend) halfOpen(now time.Time) bool {
	u := b.ejectedUntil.Load()
	return breakerHalfOpen && u != 0 && now.UnixNano() >= u
}

// State: "closed" | "open" | "half-open" (stats, chaoslab).
func (b *Backend) State(now time.Time) string {
	u := b.ejectedUntil.Load()
	switch {
	case u != 0 && now.UnixNano() < u:
		return "open"
	case b.halfOpen(now):
		return "half-open"
	}
	return "closed"
}

// available: dùng được ⇔ active health nói sống ∧ không đang bị eject (D8).
func (b *Backend) available(now time.Time) bool {
	if !b.healthy.Load() || now.UnixNano() < b.ejectedUntil.Load() {
		return false
	}
	// half-open: chỉ khi chưa ai giữ lượt thử. Pick giành lượt bằng CAS.
	return !(b.halfOpen(now) && b.probing.Load())
}

// Inflight: số request đang gánh (test và stats).
func (b *Backend) Inflight() int64 { return b.inflight.Load() }

// BackendStats: ảnh chụp một backend.
type BackendStats struct {
	Addr      string
	Picks     int64
	Inflight  int64
	Fails     int64
	Ejections int64
	EWMA      time.Duration // điểm EWMA hiện tại (đã decay tới lúc chụp)
	Healthy   bool          // active
	Ejected   bool          // passive
	State     string        // breaker: closed | open | half-open (phase 7)
	Probes    int64         // số request thử ở half-open
	Reopens   int64         // half-open thử hỏng ⇒ open lại
}

func (b *Backend) stats(now time.Time) BackendStats {
	return BackendStats{
		Addr: b.Addr, Picks: b.picks.Load(), Inflight: b.inflight.Load(), Fails: b.fails.Load(),
		Ejections: b.ejections.Load(), EWMA: time.Duration(b.ewma.score(now)),
		Healthy: b.healthy.Load(), Ejected: now.UnixNano() < b.ejectedUntil.Load(),
		State: b.State(now), Probes: b.probes.Load(), Reopens: b.reopens.Load(),
	}
}
