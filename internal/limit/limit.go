// Package limit: các bộ đếm chống quá tải của phase 7 — token bucket, token
// bucket theo khoá có trần bộ nhớ (D4), retry budget (D6). Không goroutine
// nền: mọi refill/hết hạn tính lười từ đồng hồ lúc gọi.
//
// Ranh giới với proxy: proxy đưa KHOÁ đã qua ranh giới tin cậy (clientIP của
// phase 4 D9). Package này không biết header là gì — cố tình, để không ai đọc
// X-Forwarded-For ở đây được (I6).
package limit

import (
	"container/list"
	"sync"
	"time"
)

// TokenBucket: Rate token/s, chứa tối đa Burst. Bắt đầu đầy. Không an toàn
// cho nhiều goroutine — KeyedLimiter khoá bên ngoài.
type TokenBucket struct {
	tokens float64
	last   time.Time
}

// take: refill theo thời gian đã trôi rồi lấy một token nếu có.
func (b *TokenBucket) take(now time.Time, rate, burst float64) bool {
	if b.last.IsZero() {
		b.tokens, b.last = burst, now
	}
	if el := now.Sub(b.last).Seconds(); el > 0 {
		b.tokens += el * rate
		if b.tokens > burst {
			b.tokens = burst
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// KeyedConfig: Rate/Burst cho MỖI khoá; MaxKeys trần số khoá nhớ (đầy ⇒ đuổi
// khoá dùng lâu nhất); MaxKeys 0 = 10 000.
type KeyedConfig struct {
	Rate    float64
	Burst   float64
	MaxKeys int
}

// KeyedLimiter: map khoá → bucket + LRU dưới MỘT mutex (D4: đo trước khi shard).
//
// Bẫy ROADMAP: map per-IP không bao giờ dọn = rò rỉ attacker điều khiển được
// (mỗi IP giả qua proxy tin một entry). Trần MaxKeys chặn nó. Cái giá: khoá bị
// đuổi quay lại với bucket ĐẦY — lợi cho chính khoá đó, không cho ai khác.
type KeyedLimiter struct {
	cfg KeyedConfig

	mu    sync.Mutex
	m     map[string]*list.Element
	lru   *list.List // Front = vừa dùng
	evict int64
}

type entry struct {
	key string
	b   TokenBucket
}

func NewKeyed(cfg KeyedConfig) *KeyedLimiter {
	if cfg.MaxKeys == 0 {
		cfg.MaxKeys = 10000
	}
	if !limiterCapped {
		cfg.MaxKeys = 0 // nodefense7: không trần — để TestLimiterMemory đỏ
	}
	if cfg.Burst < 1 {
		cfg.Burst = 1
	}
	return &KeyedLimiter{cfg: cfg, m: map[string]*list.Element{}, lru: list.New()}
}

// Allow: lấy một token của khoá key. false ⇒ 429.
func (l *KeyedLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	el, ok := l.m[key]
	if ok {
		l.lru.MoveToFront(el)
	} else {
		if l.cfg.MaxKeys > 0 && len(l.m) >= l.cfg.MaxKeys {
			old := l.lru.Back()
			l.lru.Remove(old)
			delete(l.m, old.Value.(*entry).key)
			l.evict++
		}
		el = l.lru.PushFront(&entry{key: key})
		l.m[key] = el
	}
	return el.Value.(*entry).b.take(now, l.cfg.Rate, l.cfg.Burst)
}

// Len, Evictions: số khoá đang nhớ / số lần đuổi (stats, test bộ nhớ).
func (l *KeyedLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.m)
}

func (l *KeyedLimiter) Evictions() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.evict
}

// RetryBudget (D6, Finagle RetryBudget): cửa sổ trượt Window chia thành
// 10 xô; mỗi request Deposit, mỗi retry TryWithdraw. Cho retry khi
//
//	retries < Percent·requests + MinPerSec·Window
//
// trong cửa sổ. Trần là TỈ LỆ: tải gấp đôi thì được retry gấp đôi, nhưng
// không bao giờ quá Percent tải thật — retry mù 3 lần thì cụm quá tải nhận 4x.
type RetryBudget struct {
	Percent   float64       // 0.1 = 10 %
	MinPerSec float64       // sàn: lưu lượng thấp vẫn retry được
	Window    time.Duration // 10 s

	mu      sync.Mutex
	reqs    [10]int64
	rets    [10]int64
	slot    int64 // chỉ số xô hiện tại = now / (Window/10)
	allowed int64
	denied  int64
}

func (r *RetryBudget) advance(now time.Time) {
	if r.Window == 0 {
		r.Window = 10 * time.Second
	}
	w := int64(r.Window / 10)
	s := now.UnixNano() / w
	if r.slot == 0 {
		r.slot = s
		return
	}
	for r.slot < s {
		r.slot++
		i := r.slot % 10
		r.reqs[i], r.rets[i] = 0, 0
		if s-r.slot >= 10 { // nghỉ lâu hơn cả cửa sổ: xoá hết một lần
			r.reqs, r.rets = [10]int64{}, [10]int64{}
			r.slot = s
		}
	}
}

// Deposit: một request gốc (không tính retry).
func (r *RetryBudget) Deposit(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advance(now)
	r.reqs[r.slot%10]++
}

// TryWithdraw: được retry không. true ⇒ đã trừ budget.
func (r *RetryBudget) TryWithdraw(now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !retryBudgetOn {
		r.allowed++
		return true // nodefense7: retry mù
	}
	r.advance(now)
	var reqs, rets int64
	for i := range r.reqs {
		reqs += r.reqs[i]
		rets += r.rets[i]
	}
	limit := r.Percent*float64(reqs) + r.MinPerSec*r.Window.Seconds()
	if float64(rets) >= limit {
		r.denied++
		return false
	}
	r.rets[r.slot%10]++
	r.allowed++
	return true
}

// Stats: số retry được cho / bị từ chối từ đầu.
func (r *RetryBudget) Stats() (allowed, denied int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allowed, r.denied
}
