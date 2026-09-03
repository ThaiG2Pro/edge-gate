package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// rssKB đọc VmRSS từ /proc/self/status. Cố ý KHÔNG dùng runtime.MemStats làm
// thước chính: MemStats đo heap Go, còn RSS đo cả goroutine stack và mọi thứ
// runtime đã chạm. Câu hỏi "một connection rỗi tốn bao nhiêu bộ nhớ" là câu hỏi
// về RSS — đó là con số OOM killer nhìn vào.
func rssKB() int {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				n, _ := strconv.Atoi(f[1])
				return n
			}
		}
	}
	return -1
}

// G5 — giá bộ nhớ của một connection rỗi.
//
// CẢNH BÁO diễn giải, phải nhớ khi đọc số: client và server nằm CÙNG một
// process ở chế độ -role both, nên mỗi "connection" tính ở đây là một CẶP
// (2 fd, 2 goroutine, và buffer của cả hai đầu). Muốn tách riêng phía server
// thì chạy `-role server` ở process khác và đo RSS của nó.
func expMem(addr string, conns int, useBufio bool) {
	if lim := fdLimit(); lim > 0 && conns*2+64 > lim {
		warn("conns=%d cần ~%d fd nhưng `ulimit -n` = %d. Thí nghiệm này sẽ chết vì fd, KHÔNG vì code. Chạy `ulimit -n 65536` trước.", conns, conns*2+64, lim)
	}

	runtime.GC()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)
	base := rssKB()
	baseGo := runtime.NumGoroutine()

	req := request{respSize: 64}
	held := make([]*clientConn, 0, conns)
	failed := 0
	var firstErr error
	for i := 0; i < conns; i++ {
		cc, err := dial(addr, true)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			break
		}
		// Một request để connection thật sự "sống" ở cả hai đầu: server đã
		// accept, đã cấp bufio (nếu bật), đã chạm buffer. Connection chỉ mới
		// accept mà chưa đọc gì thì chưa tốn đúng chi phí thật.
		if _, err := cc.roundtrip(req); err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			cc.close()
			break
		}
		held = append(held, cc)
	}

	time.Sleep(300 * time.Millisecond) // để server kịp xử lý xong hết
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	after := rssKB()
	afterGo := runtime.NumGoroutine()

	n := len(held)
	fmt.Printf("\n--- G5: bộ nhớ của %d connection rỗi (bufio=%v)\n", n, useBufio)
	if n == 0 {
		warn("không giữ được connection nào: %v", firstErr)
		return
	}
	if after <= base {
		// Đã xảy ra thật ở lần chạy `-all` đầu tiên: RSS/conn ra -7.99 KB.
		// Nguyên nhân: các thí nghiệm TRƯỚC đó (omission mở 512 conn) đã đẩy RSS
		// lên, rồi scavenger của Go trả bộ nhớ về OS trong lúc phép đo này chạy.
		// Mốc nền vì thế cao hơn mức thật => hiệu ra số âm.
		// Phép đo RSS chỉ có nghĩa trong một process SẠCH.
		warn("RSS sau (%d KB) <= RSS trước (%d KB): phép đo này KHÔNG dùng được.", after, base)
		warn("G5 phải chạy trong process sạch. Dùng `-exp mem` một mình, đừng dùng trong `-all`.")
		return
	}
	fmt.Printf("  RSS       : %d KB -> %d KB   (delta %d KB)\n", base, after, after-base)
	fmt.Printf("  RSS/conn  : %.2f KB   (đây là CẶP client+server trong 1 process)\n", float64(after-base)/float64(n))
	fmt.Printf("  HeapAlloc : %.1f MB -> %.1f MB  => %.2f KB/conn\n",
		float64(m0.HeapAlloc)/1e6, float64(m1.HeapAlloc)/1e6, float64(m1.HeapAlloc-m0.HeapAlloc)/1024/float64(n))
	fmt.Printf("  goroutine : %d -> %d   (%.1f/conn)\n", baseGo, afterGo, float64(afterGo-baseGo)/float64(n))
	fmt.Printf("  StackSys  : %.1f MB -> %.1f MB  => %.2f KB/conn\n",
		float64(m0.StackSys)/1e6, float64(m1.StackSys)/1e6, float64(m1.StackSys-m0.StackSys)/1024/float64(n))
	if failed > 0 {
		warn("%d connection lỗi, lỗi đầu tiên: %v", failed, firstErr)
	}
	fmt.Printf("  => so với 2KB stack khởi tạo của goroutine: %.2fx\n", float64(after-base)/float64(n)/2.0)

	for _, cc := range held {
		cc.close()
	}
}
