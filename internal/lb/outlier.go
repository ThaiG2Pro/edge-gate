package lb

import "time"

// OutlierConfig: passive outlier ejection (D7, Envoy consecutive_5xx). Zero
// value = bật, 5 lỗi liên tiếp, eject 30 s × 2^(n-1), trần 5 phút, không eject
// quá 50 % backend.
type OutlierConfig struct {
	Disabled        bool
	Consecutive     int
	BaseEject       time.Duration
	MaxEject        time.Duration
	MaxEjectPercent int
}

func (o *OutlierConfig) withDefaults() {
	if o.Consecutive == 0 {
		o.Consecutive = 5
	}
	if o.BaseEject == 0 {
		o.BaseEject = 30 * time.Second
	}
	if o.MaxEject == 0 {
		o.MaxEject = 5 * time.Minute
	}
	if o.MaxEjectPercent == 0 {
		o.MaxEjectPercent = 50
	}
}

// recordOutcome: một request xong. failed = lỗi transport hoặc 5xx.
func (bl *Balancer) recordOutcome(b *Backend, failed bool, now time.Time) {
	if failed {
		b.fails.Add(1)
	}
	if bl.cfg.Outlier.Disabled {
		return
	}
	if !failed {
		b.consecFails.Store(0)
		return
	}
	if b.consecFails.Add(1) < int64(bl.cfg.Outlier.Consecutive) {
		return
	}
	bl.tryEject(b, now)
}

// tryEject: eject b nếu chưa vượt trần MaxEjectPercent. Vượt trần ⇒ KHÔNG
// eject và đếm ejectRefused — upstream chậm đồng loạt (thường do chính proxy
// quá tải) không được biến thành 503 toàn tập.
func (bl *Balancer) tryEject(b *Backend, now time.Time) {
	oc := bl.cfg.Outlier
	bl.mu.Lock()
	defer bl.mu.Unlock()
	b.consecFails.Store(0)
	if now.UnixNano() < b.ejectedUntil.Load() {
		return // đang bị eject rồi (request cũ vừa xong)
	}
	ejected := 0
	for _, o := range bl.backends {
		if o != b && now.UnixNano() < o.ejectedUntil.Load() {
			ejected++
		}
	}
	if (ejected+1)*100 > oc.MaxEjectPercent*len(bl.backends) {
		bl.ejectRefused.Add(1)
		bl.cfg.Logf("lb: %s vượt %d lỗi liên tiếp nhưng KHÔNG eject: đã eject %d/%d (trần %d %%)",
			b.Addr, oc.Consecutive, ejected, len(bl.backends), oc.MaxEjectPercent)
		return
	}
	// Backoff mũ; đã yên ổn quá 2×MaxEject kể từ lần eject trước ⇒ bắt đầu lại từ Base.
	if !b.lastEject.IsZero() && now.Sub(b.lastEject) > 2*oc.MaxEject {
		b.ejectCount = 0
	}
	b.ejectCount++
	d := oc.BaseEject
	for i := 1; i < b.ejectCount && d < oc.MaxEject; i++ {
		d *= 2
	}
	if d > oc.MaxEject {
		d = oc.MaxEject
	}
	b.lastEject = now
	b.ejectedUntil.Store(now.Add(d).UnixNano())
	b.ejections.Add(1)
	bl.cfg.Logf("lb: eject %s trong %s (lần %d)", b.Addr, d, b.ejectCount)
}
