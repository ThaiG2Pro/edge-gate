package lb

import (
	"hash/fnv"
	"math/rand/v2"
	"sort"
	"sync/atomic"
	"time"
)

// Picker chọn một backend đang available trong all; nil nếu không còn ai.
// Nhận cả danh sách (không lọc trước) vì consistent hash cần vị trí gốc trên ring.
type Picker interface {
	Pick(all []*Backend, now time.Time, key string) *Backend
}

// ---- Round robin (baseline) ------------------------------------------------

// roundRobin xoay trên tập ĐANG AVAILABLE, không trên danh sách gốc. Bản đầu
// xoay trên danh sách gốc rồi "bỏ qua node unhealthy": node đứng ngay sau node
// chết nhận GẤP ĐÔI (75/75/150 trong TestRoundRobinEvenAndSkipsUnhealthy) —
// chính là lỗi nginx tránh bằng cách chỉ đếm trên danh sách "up".
type roundRobin struct{ next atomic.Uint64 }

func (r *roundRobin) Pick(all []*Backend, now time.Time, _ string) *Backend {
	avail := make([]*Backend, 0, len(all))
	for _, b := range all {
		if b.available(now) {
			avail = append(avail, b)
		}
	}
	if len(avail) == 0 {
		return nil
	}
	return avail[(r.next.Add(1)-1)%uint64(len(avail))]
}

// ---- Least connections ------------------------------------------------------

// leastConn: ít inflight nhất; hoà thì ngẫu nhiên (reservoir) để không dồn
// vào backend đầu danh sách lúc tất cả rỗi.
//
// Điểm mù (câu hỏi 1): inflight thấp ≠ khoẻ. Node trả 503 trong 50 µs có
// inflight ≈ 0 mãi ⇒ được chọn nhiều hơn node khoẻ. Xem G2.
type leastConn struct{}

func (leastConn) Pick(all []*Backend, now time.Time, _ string) *Backend {
	var best *Backend
	var bestN int64
	ties := 0
	for _, b := range all {
		if !b.available(now) {
			continue
		}
		n := b.inflight.Load()
		switch {
		case best == nil || n < bestN:
			best, bestN, ties = b, n, 1
		case n == bestN:
			ties++
			if rand.IntN(ties) == 0 {
				best = b
			}
		}
	}
	return best
}

// ---- P2C + EWMA -------------------------------------------------------------

// p2c: hai backend ngẫu nhiên khác nhau, điểm = ewma × (inflight+1), lấy nhỏ
// hơn (D4). O(1) thay O(n) của least-conn và không cần trạng thái toàn cục —
// đó là lý do Finagle/Envoy dùng; giá là chỉ so hai con nên node xấu vẫn có
// cơ hội khi cả hai con bốc đều xấu, và EWMA tau dài thì phản ứng chậm (G3).
type p2c struct{}

func (p2c) Pick(all []*Backend, now time.Time, _ string) *Backend {
	avail := make([]*Backend, 0, len(all))
	for _, b := range all {
		if b.available(now) {
			avail = append(avail, b)
		}
	}
	switch len(avail) {
	case 0:
		return nil
	case 1:
		return avail[0]
	}
	i := rand.IntN(len(avail))
	j := rand.IntN(len(avail) - 1)
	if j >= i {
		j++
	}
	a, b := avail[i], avail[j]
	if a.score(now) <= b.score(now) {
		return a
	}
	return b
}

// noSamplePenalty: điểm của node CHƯA có mẫu mà đang gánh request — lớn hơn mọi
// ewma×(inflight+1) thực tế (1 s latency × 1000 inflight = 1e12 ns).
const noSamplePenalty = 1e13

func (b *Backend) score(now time.Time) float64 {
	e := b.ewma.score(now)
	n := b.inflight.Load()
	if e == 0 && n > 0 {
		// P6-3 (trả 2026-10-02), như Finagle PeakEwma: "if (lcost == 0.0 &&
		// pending != 0) Penalty + pending". Bản cũ: 0 × (n+1) = 0 ⇒ node mới/vừa
		// hồi phục hút MỌI lượt bốc trúng nó trong lúc chờ mẫu đầu (10/32 với
		// 5 node). Giờ: lượt thử đầu (n == 0) vẫn được ngay, lượt sau phải đợi mẫu.
		return noSamplePenalty + float64(n)
	}
	return e * float64(n+1)
}

// ---- Consistent hashing -----------------------------------------------------

// Hash32: FNV-1a 64 + bước trộn cuối của MurmurHash3 (fmix64), lấy 32 bit thấp.
//
// Bản đầu chỉ FNV-1a gập 64→32: FNV không có avalanche, các vnode
// "addr#0", "addr#1", … của CÙNG một backend rơi gần nhau trên ring ⇒ 150
// vnode vẫn lệch tải 3.57x, và 1000 vnode vẫn 2.85x (đo ở turn 1, xem
// phase6-log §1). Vnode chỉ làm mịn khi hash rải đều; fmix64 đưa về 1.17x.
// Export để test tính `hash % N` đối chứng.
func Hash32(s string) uint32 {
	h := fnv.New64a()
	h.Write([]byte(s))
	k := h.Sum64()
	k ^= k >> 33
	k *= 0xff51afd7ed558ccd
	k ^= k >> 33
	k *= 0xc4ceb9fe1a85ec53
	k ^= k >> 33
	return uint32(k)
}

type vnode struct {
	hash uint32
	idx  int
}

// consistentHash: ring VNodes điểm ảo / backend (D5). Thêm hay bớt một backend
// chỉ đổi chủ của ~1/N key (G4), khác `hash % N` đổi ~(N-1)/N... không — đổi
// ~80 % với N=4→5, xem TestConsistentHashRehash để khỏi tin lời mình.
type consistentHash struct {
	ring []vnode
}

func newConsistentHash(all []*Backend, vnodes int) *consistentHash {
	ring := make([]vnode, 0, len(all)*vnodes)
	for _, b := range all {
		for i := 0; i < vnodes; i++ {
			ring = append(ring, vnode{hash: Hash32(b.Addr + "#" + itoa(i)), idx: b.idx})
		}
	}
	sort.Slice(ring, func(i, j int) bool { return ring[i].hash < ring[j].hash })
	return &consistentHash{ring: ring}
}

func (c *consistentHash) Pick(all []*Backend, now time.Time, key string) *Backend {
	if len(c.ring) == 0 {
		return nil
	}
	h := Hash32(key)
	i := sort.Search(len(c.ring), func(k int) bool { return c.ring[k].hash >= h })
	// Node đích không dùng được ⇒ đi tiếp trên ring (key vẫn ổn định với các
	// node còn lại — đó là điểm modulo không có).
	for tries := 0; tries < len(c.ring); tries++ {
		b := all[c.ring[(i+tries)%len(c.ring)].idx]
		if b.available(now) {
			return b
		}
	}
	return nil
}

// owner: chủ của key bỏ qua sức khoẻ — test rehash dùng.
func (c *consistentHash) owner(key string) int {
	h := Hash32(key)
	i := sort.Search(len(c.ring), func(k int) bool { return c.ring[k].hash >= h })
	return c.ring[i%len(c.ring)].idx
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}
