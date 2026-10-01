package proxy

import (
	"bufio"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// PoolConfig: pool connection tới upstream (phase 5). Zero value = mặc định.
type PoolConfig struct {
	// Disabled: dial mới mỗi request và đóng sau response — đúng hành vi
	// phase 3-4 (D1 cũ), giữ lại để `cmd/poollab` đo pool-off / pool-on.
	Disabled bool
	// MaxIdle: số connection rỗi tối đa giữ lại (MaxIdlePerHost; phase 5 chỉ
	// có một host). Mặc định 64. Đầy ⇒ đóng con GIÀ NHẤT (đáy stack), không
	// phải con vừa dùng (D9).
	MaxIdle int
	// MaxIdleTime: connection rỗi quá tuổi này bị đóng lúc lấy ra (và quét ở
	// đáy stack lúc put — P5-2). Mặc định 30 s (P5-5): PHẢI ngắn hơn idle
	// timeout của upstream (nginx keepalive_timeout upstream 60 s, Go
	// http.Server IdleTimeout thường 60-120 s) để pool là bên đóng trước —
	// bên đóng trước không bao giờ thấy FIN bất ngờ; probe/retry chỉ còn là lưới đỡ.
	MaxIdleTime time.Duration
	// Probe: trước khi dùng lại, recv(MSG_PEEK|MSG_DONTWAIT) để bắt FIN/RST
	// upstream đã gửi khi connection rỗi (D3). Mặc định true. Đặt false CHỈ để
	// ép đường retry (D4) trong test.
	Probe *bool
}

func (p *PoolConfig) withDefaults() {
	if p.MaxIdle == 0 {
		p.MaxIdle = 64
	}
	if p.MaxIdleTime == 0 {
		p.MaxIdleTime = 30 * time.Second
	}
	if p.Probe == nil {
		t := true
		p.Probe = &t
	}
}

// PoolStats: bộ đếm để `cmd/poollab` và test nhìn được pool có chạy thật không
// (D6). Không có nó, "pool bật" chỉ là một flag có tên (bài học G4 phase 3).
type PoolStats struct {
	Dials       int64 // dial mới (kể cả dial cho lần retry)
	Reuses      int64 // lấy được connection rỗi còn sống
	Puts        int64 // trả về pool thành công
	Retries     int64 // D4: retry đúng một lần
	DropDirty   int64 // release(clean=false): đóng, không trả về
	DropFull    int64 // pool đầy: đóng con già nhất
	DropExpired int64 // quá MaxIdleTime lúc get
	DeadOnProbe int64 // probe thấy FIN/RST/byte lạ lúc get
	Idle        int64 // đang rỗi trong pool (ảnh chụp)
}

// pooledConn: một connection upstream cùng bufio của nó. bufio đi theo
// connection, không theo request: byte thừa (nếu có) nằm trong br, và đó
// chính là thứ D2 phải kiểm trước khi put.
type pooledConn struct {
	p   *pool // pool sở hữu — phase 6 có nhiều pool, release phải về đúng pool
	c   net.Conn
	raw net.Conn     // TCP bên dưới — probe FIN cần fd thật (phase 8 D8); = c khi plaintext
	in  *countReader // đếm byte response đã tới — quyết retry (D4: 0 byte)
	br  *bufio.Reader
	bw  *bufio.Writer

	idleSince time.Time
	reused    bool // lấy từ pool (không phải vừa dial) — điều kiện (a) của D4
	uses      int
}

// close đóng connection và trả bufio về pool (D3). Chỉ chủ duy nhất gọi;
// gọi lần hai là no-op (trả hai lần = hai người cầm chung một buffer).
func (pc *pooledConn) close() {
	pc.c.Close()
	if pc.br != nil {
		putReader(pc.br)
		putWriter(pc.bw)
		pc.br, pc.bw = nil, nil
	}
}

// countReader đếm byte đọc được từ upstream. Reset về 0 đầu mỗi exchange.
type countReader struct {
	r io.Reader
	n int64
}

func (cr *countReader) Read(p []byte) (int, error) {
	k, err := cr.r.Read(p)
	cr.n += int64(k)
	return k, err
}

// pool: stack LIFO + mutex (D1). Không có goroutine nào của riêng nó (D9).
type pool struct {
	cfg  PoolConfig
	dial func() (c, raw net.Conn, err error) // raw = conn TCP bên dưới (phase 8: c có thể là tls.Conn)

	mu     sync.Mutex
	idle   []*pooledConn // đỉnh = cuối slice = trẻ nhất
	closed bool

	dials, reuses, puts, retries, dropDirty, dropFull, dropExpired, deadOnProbe atomic.Int64
}

func newPool(cfg PoolConfig, dial func() (c, raw net.Conn, err error)) *pool {
	cfg.withDefaults()
	return &pool{cfg: cfg, dial: dial}
}

// get trả một connection dùng được: từ pool (đã probe) hoặc dial mới.
// reused=true ⇔ từ pool.
func (p *pool) get() (*pooledConn, error) {
	if !p.cfg.Disabled {
		for {
			pc := p.pop()
			if pc == nil {
				break
			}
			if time.Since(pc.idleSince) > p.cfg.MaxIdleTime {
				p.dropExpired.Add(1)
				pc.close()
				continue
			}
			if *p.cfg.Probe {
				if dead, known := probeIdle(pc.raw); known && dead {
					p.deadOnProbe.Add(1)
					pc.close()
					continue
				}
			}
			pc.reused = true
			pc.uses++
			p.reuses.Add(1)
			return pc, nil
		}
	}
	return p.dialNew()
}

// dialNew luôn dial (lần retry của D4 dùng thẳng hàm này).
func (p *pool) dialNew() (*pooledConn, error) {
	c, raw, err := p.dial()
	if err != nil {
		return nil, err
	}
	p.dials.Add(1)
	in := &countReader{r: c}
	return &pooledConn{p: p, c: c, raw: raw, in: in, br: getReader(in), bw: getWriter(c), uses: 1}, nil
}

func (p *pool) pop() *pooledConn {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.idle)
	if n == 0 {
		return nil
	}
	pc := p.idle[n-1]
	p.idle[n-1] = nil
	p.idle = p.idle[:n-1]
	return pc
}

// put nhận một connection mà CALLER đã xác nhận sạch (D2). put chỉ lo
// sức chứa, tuổi và deadline (D8). Pool tắt hoặc đã đóng ⇒ đóng connection.
func (p *pool) put(pc *pooledConn) {
	if p.cfg.Disabled {
		pc.close()
		return
	}
	// D8: deadline của lần dùng trước còn treo ⇒ request kế tiếp lỗi
	// timeout ma. Xoá trước khi ai khác cầm.
	pc.c.SetDeadline(time.Time{})
	now := time.Now()
	pc.idleSince = now
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		pc.close()
		return
	}
	// P5-2: quét từ ĐÁY bỏ mọi con quá tuổi. LIFO ⇒ đáy già nhất, gặp con đầu
	// còn hạn là dừng. get chỉ nhìn đỉnh nên con chết ở đáy sống mãi nếu không
	// có bước này (5 idle quá tuổi + 1 request ⇒ 4 con chết nằm lại tới lần đầy).
	var expired []*pooledConn
	k := 0
	for k < len(p.idle) && now.Sub(p.idle[k].idleSince) > p.cfg.MaxIdleTime {
		k++
	}
	if k > 0 {
		expired = append(expired, p.idle[:k]...)
		n := copy(p.idle, p.idle[k:])
		for i := n; i < len(p.idle); i++ {
			p.idle[i] = nil
		}
		p.idle = p.idle[:n]
	}
	var evict *pooledConn
	if len(p.idle) >= p.cfg.MaxIdle {
		// Đầy: bỏ con GIÀ NHẤT (đáy), giữ con vừa dùng (D9). Slice dịch O(n)
		// với n ≤ MaxIdle — rẻ hơn một syscall dial.
		evict = p.idle[0]
		copy(p.idle, p.idle[1:])
		p.idle[len(p.idle)-1] = pc
	} else {
		p.idle = append(p.idle, pc)
	}
	p.mu.Unlock()
	p.puts.Add(1)
	if evict != nil {
		p.dropFull.Add(1)
		evict.close()
	}
	for _, e := range expired {
		p.dropExpired.Add(1)
		e.close()
	}
}

// discard: connection không sạch (hoặc lỗi) ⇒ đóng, đếm.
func (p *pool) discard(pc *pooledConn) {
	p.dropDirty.Add(1)
	pc.close()
}

// closeAll đóng mọi connection rỗi; put sau đó cũng đóng.
func (p *pool) closeAll() {
	p.mu.Lock()
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.mu.Unlock()
	for _, pc := range idle {
		pc.close()
	}
}

func (p *pool) stats() PoolStats {
	p.mu.Lock()
	idle := int64(len(p.idle))
	p.mu.Unlock()
	return PoolStats{
		Dials: p.dials.Load(), Reuses: p.reuses.Load(), Puts: p.puts.Load(), Retries: p.retries.Load(),
		DropDirty: p.dropDirty.Load(), DropFull: p.dropFull.Load(), DropExpired: p.dropExpired.Load(),
		DeadOnProbe: p.deadOnProbe.Load(), Idle: idle,
	}
}
