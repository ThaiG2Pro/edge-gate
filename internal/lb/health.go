package lb

import (
	"bufio"
	"net"
	"time"

	"github.com/thaivro/edgegate/internal/httpx"
)

// HealthConfig: active health check (D6). Zero value = bật, Interval 2 s,
// Timeout 1 s, chỉ dial TCP (Path rỗng), Fall 3, Rise 2.
type HealthConfig struct {
	Disabled bool
	Interval time.Duration
	Timeout  time.Duration
	// Path: nếu khác rỗng, sau khi dial gửi `GET Path` raw và đòi status < 400.
	// Rỗng = chỉ cần TCP connect thành công.
	Path string
	Fall int // lỗi liên tiếp ⇒ unhealthy (nginx max_fails)
	Rise int // tốt liên tiếp ⇒ healthy
	// Dial (P8-3): thay net.DialTimeout — proxy đặt khi upstream nói TLS để
	// probe bắt tay TLS trước khi gửi GET Path. nil = TCP trần. lb không biết
	// TLS: cert/SNI là việc của proxy, lb chỉ cần một net.Conn đã sẵn sàng.
	Dial func(addr string, timeout time.Duration) (net.Conn, error)
}

func (h *HealthConfig) withDefaults() {
	if h.Interval == 0 {
		h.Interval = 2 * time.Second
	}
	if h.Timeout == 0 {
		h.Timeout = time.Second
	}
	if h.Fall == 0 {
		h.Fall = 3
	}
	if h.Rise == 0 {
		h.Rise = 2
	}
}

// probe: một lần kiểm sức khoẻ. Không net/http: dial + WriteHead + ReadResponse.
func (bl *Balancer) probe(b *Backend) bool {
	hc := bl.cfg.Health
	dial := hc.Dial
	if dial == nil {
		dial = func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("tcp", addr, timeout)
		}
	}
	c, err := dial(b.Addr, hc.Timeout)
	if err != nil {
		return false
	}
	defer c.Close()
	if hc.Path == "" {
		return true
	}
	c.SetDeadline(time.Now().Add(hc.Timeout))
	req := &httpx.Request{Method: "GET", Target: hc.Path, Proto: "HTTP/1.1", Header: httpx.Header{}}
	req.Header.Set("Host", b.Addr)
	req.Header.Set("User-Agent", "edgegate-health")
	req.Header.Set("Connection", "close")
	bw := bufio.NewWriter(c)
	if err := req.WriteHead(bw); err != nil {
		return false
	}
	if err := bw.Flush(); err != nil {
		return false
	}
	resp, err := httpx.ReadResponse(bufio.NewReader(c), httpx.DefaultLimits(), "GET")
	if err != nil {
		return false
	}
	return resp.Status < 400
}

// healthLoop: một goroutine cho MỖI backend (probe chậm của con này không kéo
// lịch của con kia). Dừng khi bl.stop đóng.
func (bl *Balancer) healthLoop(b *Backend) {
	defer bl.wg.Done()
	t := time.NewTicker(bl.cfg.Health.Interval)
	defer t.Stop()
	for {
		select {
		case <-bl.stop:
			return
		case <-t.C:
		}
		bl.recordProbe(b, bl.probe(b))
	}
}

// recordProbe áp fall/rise. Tách khỏi probe để test không cần socket.
func (bl *Balancer) recordProbe(b *Backend, ok bool) {
	hc := bl.cfg.Health
	b.hmu.Lock()
	defer b.hmu.Unlock()
	if ok {
		b.hFails = 0
		b.hOK++
		if !b.healthy.Load() && b.hOK >= hc.Rise {
			b.healthy.Store(true)
			bl.cfg.Logf("lb: %s healthy trở lại (rise=%d)", b.Addr, hc.Rise)
		}
		return
	}
	b.hOK = 0
	b.hFails++
	if b.healthy.Load() && b.hFails >= hc.Fall {
		b.healthy.Store(false)
		bl.cfg.Logf("lb: %s unhealthy (fall=%d)", b.Addr, hc.Fall)
	}
}
