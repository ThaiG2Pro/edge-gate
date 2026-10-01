package httpx

import (
	"io"
	"strings"
	"testing"
)

// G2: một head 60 KiB làm buffer pool tăng cap. Không chặn ⇒ request nhỏ sau
// đó nhận lại buffer ≥ 60 KiB (pool phình theo request tệ nhất). Chặn (Put bỏ
// cap > maxPooledHead) ⇒ buffer nhỏ.
func TestHeadPoolDropsOversize(t *testing.T) {
	if !poolHead {
		t.Skip("nodefense9: không pool head")
	}
	big := &Response{Proto: "HTTP/1.1", Status: 200, Reason: "OK", Header: Header{}}
	big.Header.Add("X-Big", strings.Repeat("a", 60<<10))
	// Cap lớn nhất nhận lại qua 50 lượt "head to rồi lấy buffer". Một lượt là
	// đủ ở build thường (cùng goroutine, ngay sau Put ⇒ slot private của P),
	// nhưng dưới -race sync.Pool cố ý bỏ ngẫu nhiên một phần Put.
	capAfterBig := func() int {
		m := 0
		for i := 0; i < 50; i++ {
			if err := big.WriteHead(io.Discard); err != nil {
				t.Fatal(err)
			}
			p := getHead()
			m = max(m, cap(*p))
			putHead(p)
		}
		return m
	}
	old := maxPooledHead
	maxPooledHead = 1 << 30
	unbounded := capAfterBig()
	maxPooledHead = old
	bounded := capAfterBig()
	t.Logf("cap buffer head nhận lại sau head 60 KiB: không chặn %d, chặn (> %d bỏ) %d", unbounded, old, bounded)
	if unbounded < 60<<10 {
		t.Fatalf("không chặn mà pool không giữ buffer to (%d) — test không chứng minh gì", unbounded)
	}
	if bounded > old {
		t.Fatalf("chặn mà vẫn nhận lại cap %d > %d", bounded, old)
	}
}
