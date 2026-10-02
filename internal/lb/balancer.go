package lb

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// Config của Balancer. Zero value: rr, tau 1 s, 150 vnode, health + outlier bật.
type Config struct {
	// Algo: "rr" | "leastconn" | "p2c" | "chash".
	Algo string
	// Tau: hằng thời gian EWMA của p2c. Mặc định 1 s. Quá dài ⇒ phản ứng chậm (G3).
	Tau time.Duration
	// HashHeader: header làm khoá cho chash; rỗng ⇒ proxy dùng IP client sau
	// ranh giới tin cậy. Header thiếu ⇒ cũng rơi về IP.
	HashHeader string
	// VNodes: điểm ảo / backend cho chash. Mặc định 150.
	VNodes int

	Health  HealthConfig
	Outlier OutlierConfig

	Logf func(format string, args ...any)
}

func (c *Config) withDefaults() {
	if c.Algo == "" {
		c.Algo = "rr"
	}
	if c.Tau == 0 {
		c.Tau = time.Second
	}
	if c.VNodes == 0 {
		c.VNodes = 150
	}
	if c.Logf == nil {
		c.Logf = log.Printf
	}
	c.Health.withDefaults()
	c.Outlier.withDefaults()
}

// Balancer = danh sách backend + picker + sức khoẻ. Proxy chỉ gọi Pick và Done.
type Balancer struct {
	cfg      Config
	backends []*Backend
	picker   Picker

	mu           sync.Mutex // eject accounting
	noAvailable  atomic.Int64
	ejectRefused atomic.Int64

	stop    chan struct{}
	wg      sync.WaitGroup
	started atomic.Bool
}

// New: addrs không rỗng, Algo hợp lệ. Mọi backend ban đầu healthy (D6).
func New(addrs []string, cfg Config) (*Balancer, error) {
	cfg.withDefaults()
	if len(addrs) == 0 {
		return nil, fmt.Errorf("lb: cần ít nhất một upstream")
	}
	bl := &Balancer{cfg: cfg, stop: make(chan struct{})}
	for i, a := range addrs {
		b := &Backend{Addr: a, idx: i}
		b.ewma.tau = cfg.Tau
		b.healthy.Store(true)
		bl.backends = append(bl.backends, b)
	}
	switch cfg.Algo {
	case "rr":
		bl.picker = &roundRobin{}
	case "leastconn":
		bl.picker = leastConn{}
	case "p2c":
		bl.picker = p2c{}
	case "chash":
		bl.picker = newConsistentHash(bl.backends, cfg.VNodes)
	default:
		return nil, fmt.Errorf("lb: algo %q không hợp lệ (rr|leastconn|p2c|chash)", cfg.Algo)
	}
	return bl, nil
}

// Start chạy active health check (nếu bật). Gọi nhiều lần vô hại.
func (bl *Balancer) Start() {
	if bl.cfg.Health.Disabled || !bl.started.CompareAndSwap(false, true) {
		return
	}
	for _, b := range bl.backends {
		bl.wg.Add(1)
		go bl.healthLoop(b)
	}
}

// Close dừng health check và chờ goroutine thoát.
func (bl *Balancer) Close() {
	if bl.started.Load() {
		close(bl.stop)
		bl.wg.Wait()
		bl.started.Store(false)
		bl.stop = make(chan struct{})
	}
}

// Pick chọn backend cho request có khoá key (chỉ chash dùng) và tăng inflight
// (D2: đếm theo request, từ ĐÂY). nil ⇒ không còn backend nào dùng được ⇒ 503.
func (bl *Balancer) Pick(key string) *Backend { return bl.pick(bl.backends, key) }

// unavailable: chỗ giữ cho backend bị loại trong PickExcept — healthy=false
// (zero value) ⇒ available() luôn false. Giữ ĐÚNG vị trí trong danh sách để
// consistent hash (ring tham chiếu chỉ số) đi tiếp sang vnode kế, không lệch.
var unavailable = &Backend{}

// PickExcept: như Pick nhưng không bao giờ trả ex (P7-1, trả 2026-10-02). Proxy
// dùng cho lượt chọn lại D9 sau khi dial ex lỗi — bản cũ gọi Pick ⇒ least-conn
// hoà inflight / rr một backend chọn lại CHÍNH ex ⇒ 2× DialTimeout rồi 502.
func (bl *Balancer) PickExcept(key string, ex *Backend) *Backend {
	all := make([]*Backend, len(bl.backends))
	for i, b := range bl.backends {
		if b == ex {
			b = unavailable
		}
		all[i] = b
	}
	return bl.pick(all, key)
}

func (bl *Balancer) pick(all []*Backend, key string) *Backend {
	now := time.Now()
	for try := 0; try < 3; try++ {
		b := bl.picker.Pick(all, now, key)
		if b == nil {
			break
		}
		if b.halfOpen(now) {
			// D5: giành lượt thử duy nhất. Thua CAS ⇒ có người vừa giành;
			// available() giờ loại b ⇒ chọn lại.
			if !b.probing.CompareAndSwap(false, true) {
				continue
			}
			bl.mu.Lock()
			b.probeAt = now
			bl.mu.Unlock()
			b.probes.Add(1)
		}
		b.inflight.Add(1)
		b.picks.Add(1)
		return b
	}
	bl.noAvailable.Add(1)
	return nil
}

// Done: request trên b đã xong (exchange trả về — client đã nhận response VÀ
// connection đã put/discard trong defer của exchange; D2 ghi "trước put" là
// sai, chênh vài µs, xem diary phase 6 câu 2). latency nuôi EWMA; failed nuôi outlier.
func (bl *Balancer) Done(b *Backend, latency time.Duration, failed bool) {
	now := time.Now()
	b.inflight.Add(-1)
	b.ewma.observe(latency, now)
	if b.probing.Load() && bl.resolveProbe(b, now.Add(-latency), failed, now) {
		return
	}
	bl.recordOutcome(b, failed, now)
}

// Backends: danh sách gốc (không lọc).
func (bl *Balancer) Backends() []*Backend { return bl.backends }

// Stats: ảnh chụp toàn bộ.
type Stats struct {
	Algo         string
	Backends     []BackendStats
	NoAvailable  int64 // Pick trả nil
	EjectRefused int64 // vượt trần MaxEjectPercent
}

func (bl *Balancer) Stats() Stats {
	now := time.Now()
	st := Stats{Algo: bl.cfg.Algo, NoAvailable: bl.noAvailable.Load(), EjectRefused: bl.ejectRefused.Load()}
	for _, b := range bl.backends {
		st.Backends = append(st.Backends, b.stats(now))
	}
	return st
}
