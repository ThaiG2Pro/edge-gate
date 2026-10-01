package httpx

import "sync"

// Pool buffer head cho WriteHead (phase 9 D1). Bẫy (G2): head to (tới
// MaxHeaderBytes = 64 KiB) làm buffer tăng cap; trả nguyên vào pool thì pool
// phình theo request TỆ NHẤT và giữ mãi — mọi request nhỏ sau đó cầm 64 KiB.
// Put bỏ buffer cap > maxPooledHead.
var (
	headPool      sync.Pool // *[]byte
	maxPooledHead = 16 << 10
)

func getHead() *[]byte {
	if poolHead {
		if v := headPool.Get(); v != nil {
			return v.(*[]byte)
		}
	}
	b := make([]byte, 0, 512)
	return &b
}

func putHead(p *[]byte) {
	if !poolHead || cap(*p) > maxPooledHead {
		return
	}
	*p = (*p)[:0]
	headPool.Put(p)
}
