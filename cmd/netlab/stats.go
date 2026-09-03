package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

type sample struct {
	name string
	lats []time.Duration

	// requested/achieved là cặp số PHẢI in cùng nhau. Nếu achieved < requested
	// thì mọi percentile bên dưới là số của một tải KHÁC với tải ta tưởng mình
	// đang áp — đó chính là coordinated omission, và nó lộ ra ở đúng hai cột này.
	requestedRPS float64
	achievedRPS  float64
	loop         string // "closed" | "open" | "-"
	errs         int
	note         string
}

func (s *sample) pct(p float64) time.Duration {
	if len(s.lats) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(s.lats))
	copy(sorted, s.lats)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(p / 100 * float64(len(sorted)-1))
	return sorted[idx]
}

func (s *sample) mean() time.Duration {
	if len(s.lats) == 0 {
		return 0
	}
	var sum time.Duration
	for _, d := range s.lats {
		sum += d
	}
	return sum / time.Duration(len(s.lats))
}

func d(x time.Duration) string {
	switch {
	case x == 0:
		return "-"
	case x < time.Microsecond:
		return fmt.Sprintf("%dns", x.Nanoseconds())
	case x < time.Millisecond:
		return fmt.Sprintf("%.1fµs", float64(x.Nanoseconds())/1e3)
	case x < time.Second:
		return fmt.Sprintf("%.2fms", float64(x.Nanoseconds())/1e6)
	default:
		return fmt.Sprintf("%.2fs", x.Seconds())
	}
}

func printTable(title string, ss []*sample) {
	fmt.Printf("\n--- %s\n", title)
	fmt.Printf("%-34s %-7s %9s %9s %9s %9s %9s %9s %6s\n",
		"biến thể", "loop", "n", "mean", "p50", "p90", "p99", "max", "err")
	fmt.Println(strings.Repeat("-", 118))
	for _, s := range ss {
		fmt.Printf("%-34s %-7s %9d %9s %9s %9s %9s %9s %6d\n",
			s.name, s.loop, len(s.lats), d(s.mean()), d(s.pct(50)), d(s.pct(90)),
			d(s.pct(99)), d(s.pct(100)), s.errs)
	}
	for _, s := range ss {
		if s.requestedRPS > 0 {
			fmt.Printf("  rps %-30s yêu cầu %8.0f  đạt được %8.0f  (%.1f%%)\n",
				s.name, s.requestedRPS, s.achievedRPS, 100*s.achievedRPS/s.requestedRPS)
		}
		if s.note != "" {
			fmt.Printf("  ghi chú %-26s %s\n", s.name, s.note)
		}
	}
}

// ratio in ra tỉ số kèm tên giả thuyết. Kết luận của repo này luôn là TỈ SỐ:
// số tuyệt đối đổi theo máy, tỉ số thì sống sót qua việc đổi máy.
func ratio(g, label string, a, b time.Duration, expect string) {
	if b == 0 {
		fmt.Printf("  [%s] %s: không tính được (mẫu số 0)\n", g, label)
		return
	}
	fmt.Printf("  [%s] %-46s %6.2fx   (kỳ vọng %s)\n", g, label, float64(a)/float64(b), expect)
}

func warn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "⚠️  "+format+"\n", args...)
}
