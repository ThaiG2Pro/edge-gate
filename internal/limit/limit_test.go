package limit

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// Token bucket 50/s burst 10 trong 5 s ảo, gọi 100 lần/s ⇒ 50·5 + 10 = 260.
func TestTokenBucketRate(t *testing.T) {
	l := NewKeyed(KeyedConfig{Rate: 50, Burst: 10})
	t0 := time.Unix(1_000_000, 0)
	ok := 0
	for i := 0; i < 500; i++ {
		if l.Allow("a", t0.Add(time.Duration(i)*10*time.Millisecond)) {
			ok++
		}
	}
	// 500 lần trong 4.99 s: 10 burst + 4.99·50 = 259.5 ⇒ 259 hoặc 260.
	if ok < 255 || ok > 262 {
		t.Fatalf("cho qua %d, kỳ vọng ≈ 260", ok)
	}
	t.Logf("50/s burst 10, 100 lần/s × 5 s: cho %d", ok)
}

// Hai khoá độc lập: khoá b không ăn token của a.
func TestKeyedIndependent(t *testing.T) {
	l := NewKeyed(KeyedConfig{Rate: 1, Burst: 3})
	now := time.Unix(1_000_000, 0)
	for i := 0; i < 3; i++ {
		if !l.Allow("a", now) {
			t.Fatalf("a lần %d phải qua (burst 3)", i)
		}
	}
	if l.Allow("a", now) {
		t.Fatal("a lần 4 phải bị chặn")
	}
	if !l.Allow("b", now) {
		t.Fatal("b phải có bucket riêng")
	}
}

// LRU: đầy thì đuổi khoá dùng lâu nhất, không phải khoá vừa dùng.
func TestKeyedEvictsLRU(t *testing.T) {
	if !limiterCapped {
		t.Skip("nodefense7: không trần")
	}
	l := NewKeyed(KeyedConfig{Rate: 1, Burst: 1, MaxKeys: 3})
	now := time.Unix(1_000_000, 0)
	l.Allow("a", now) // a hết token
	l.Allow("b", now)
	l.Allow("c", now)
	l.Allow("a", now) // a lên đầu (bị chặn nhưng vẫn là "vừa dùng")
	l.Allow("d", now) // đuổi b
	if l.Len() != 3 || l.Evictions() != 1 {
		t.Fatalf("len %d evict %d", l.Len(), l.Evictions())
	}
	if l.Allow("a", now) {
		t.Fatal("a không được bị đuổi (vừa dùng) ⇒ vẫn hết token")
	}
	if !l.Allow("b", now) {
		t.Fatal("b bị đuổi ⇒ quay lại với bucket đầy (giá của trần, D4)")
	}
}

// G4 bộ nhớ: 1 000 000 khoá khác nhau. Có trần 10 000 ⇒ heap tăng ≤ 5 MiB.
// nodefense7 (không trần) ⇒ heap tăng ≥ 100 MiB ⇒ test ĐỎ — đúng ý.
func TestLimiterMemory(t *testing.T) {
	l := NewKeyed(KeyedConfig{Rate: 10, Burst: 10, MaxKeys: 10000})
	keys := make([]string, 1_000_000)
	for i := range keys {
		keys[i] = fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	now := time.Unix(1_000_000, 0)
	for _, k := range keys {
		l.Allow(k, now)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	grow := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("1e6 khoá: giữ %d khoá, đuổi %d, heap tăng %.1f MiB (%.0f B/khoá giữ)",
		l.Len(), l.Evictions(), float64(grow)/(1<<20), float64(grow)/float64(max(l.Len(), 1)))
	if grow > 5<<20 {
		t.Fatalf("heap tăng %.1f MiB > 5 MiB — map per-IP không trần là rò rỉ attacker điều khiển được", float64(grow)/(1<<20))
	}
	runtime.KeepAlive(keys)
}

// G6 đơn vị: 1000 request/s trong 10 s, request nào cũng muốn retry. Cửa sổ
// 10 s chứa tối đa 10 000 request ⇒ trần 0.1·10 000 + sàn 10/s·10 s = 1 100.
func TestRetryBudget(t *testing.T) {
	if !retryBudgetOn {
		t.Skip("nodefense7: retry mù")
	}
	rb := &RetryBudget{Percent: 0.1, MinPerSec: 10, Window: 10 * time.Second}
	t0 := time.Unix(1_000_000, 0)
	got := 0
	for i := 0; i < 10000; i++ {
		now := t0.Add(time.Duration(i) * time.Millisecond)
		rb.Deposit(now)
		if rb.TryWithdraw(now) {
			got++
		}
	}
	ratio := float64(got) / 10000
	t.Logf("10 000 request trong 10 s, mỗi cái muốn retry: cho %d (%.1f %%)", got, ratio*100)
	if ratio > 0.12 || ratio < 0.09 {
		t.Fatalf("retry/request = %.3f, kỳ vọng ≈ 0.10-0.11", ratio)
	}
	// Cửa sổ trượt: sau 10 s im lặng, budget hồi lại sàn.
	later := t0.Add(30 * time.Second)
	n := 0
	for rb.TryWithdraw(later) {
		n++
		if n > 1000 {
			break
		}
	}
	if n != 100 {
		t.Fatalf("sau 20 s im lặng phải còn đúng sàn 10/s × 10 s = 100, được %d", n)
	}
}
