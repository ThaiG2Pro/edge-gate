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
}

// available: dùng được ⇔ active health nói sống ∧ không đang bị eject (D8).
func (b *Backend) available(now time.Time) bool {
	return b.healthy.Load() && now.UnixNano() >= b.ejectedUntil.Load()
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
}

func (b *Backend) stats(now time.Time) BackendStats {
	return BackendStats{
		Addr: b.Addr, Picks: b.picks.Load(), Inflight: b.inflight.Load(), Fails: b.fails.Load(),
		Ejections: b.ejections.Load(), EWMA: time.Duration(b.ewma.score(now)),
		Healthy: b.healthy.Load(), Ejected: now.UnixNano() < b.ejectedUntil.Load(),
	}
}
