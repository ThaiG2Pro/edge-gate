// needzerolab: trả nợ P1-6. "make([]byte, 4 GiB) không tốn gì" chỉ đúng khi span
// nằm trên địa chỉ chưa từng dùng. Nếu nó đè lên trang vừa free còn bẩn (chưa
// scavenge), runtime phải zero CẢ span ⇒ chạm 4 GiB ⇒ ~1M page fault ⇒ nhiều giây
// kernel và RSS +4 GiB. Chỉ 64 MB rác là đủ kích hoạt — khuếch đại 64x.
//
// Hệ quả cho I2: một process server thật LUÔN có rác bẩn, nên length=0xFFFFFFFF
// không phải "4 GiB địa chỉ ảo vô hại" — nó là 7 giây CPU kernel và 4 GiB RSS thật,
// mỗi connection.
package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

func rss() string {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmRSS") {
			return strings.Join(strings.Fields(l)[1:], "")
		}
	}
	return "?"
}

var sink []byte
var junk [][]byte

func alloc(label string) {
	t := time.Now()
	sink = make([]byte, 1<<32)
	fmt.Printf("%-44s make = %-10v RSS sau = %s\n", label, time.Since(t).Round(time.Millisecond), rss())
	sink = nil
}

// dirty: cấp 64 MB rác, CHẠM hết (để trang thật sự bẩn), thả, GC — nhưng KHÔNG
// FreeOSMemory, nên trang còn nằm trong process và Go biết chúng không zero.
func dirty() {
	for i := 0; i < 64; i++ {
		b := make([]byte, 1<<20)
		for j := range b {
			b[j] = 1
		}
		junk = append(junk, b)
	}
	junk = nil
	runtime.GC()
}

func main() {
	fmt.Println("RSS đầu:", rss())
	alloc("A. heap sạch")
	dirty()
	fmt.Println("sau 64MB rác + GC (không scavenge)  RSS =", rss())
	alloc("B. sau vùng bẩn — dự đoán: CHẬM, RSS 4 GiB")
	runtime.GC()
	dirty()
	debug.FreeOSMemory()
	fmt.Println("sau 64MB rác + FreeOSMemory          RSS =", rss())
	alloc("C. sau vùng bẩn đã scavenge — dự đoán: nhanh")
}
