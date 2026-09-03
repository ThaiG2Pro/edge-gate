package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func fdLimit() int {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		return -1
	}
	return int(rl.Cur)
}

// sockstat trả về dòng TCP của /proc/net/sockstat: inuse/orphan/tw/alloc.
// tw = số socket đang TIME_WAIT. Đây là con số phải xem TRƯỚC và SAU thí nghiệm
// không-pool, vì TIME_WAIT giữ ephemeral port 60s sau khi connection đã đóng.
func sockstat() string {
	b, err := os.ReadFile("/proc/net/sockstat")
	if err != nil {
		return "n/a"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "TCP:") {
			return line
		}
	}
	return "n/a"
}

// cpuBusy đọc /proc/stat và trả về tổng jiffies (busy, total).
// Cần con số này để câu "port chạm trần TRƯỚC CPU" là một kết luận có bằng
// chứng, không phải một câu nói cho hay. Không đo CPU thì không được phép nói
// "CPU còn rỗi".
func cpuBusy() (busy, total uint64) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		for i, f := range strings.Fields(line)[1:] {
			var v uint64
			_, _ = fmt.Sscanf(f, "%d", &v)
			total += v
			// cột 3 (index 3) là idle, cột 4 là iowait; phần còn lại là busy
			if i != 3 && i != 4 {
				busy += v
			}
		}
		break
	}
	return busy, total
}

func portRange() string {
	b, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return "n/a"
	}
	return strings.TrimSpace(string(b))
}

// G6 — khi không pool, cái gì chạm trần trước: CPU, fd, hay ephemeral port?
//
// Mỗi request một connection mới rồi đóng ngay. Phía chủ động đóng giữ socket ở
// TIME_WAIT ~60s, và mỗi socket đó giữ một ephemeral port. ip_local_port_range
// thường ~28k port ⇒ trần lý thuyết ~28000/60 ≈ 470 connection/giây bền vững,
// dù CPU còn rỗi. Đây là lý do connection pool không phải "tối ưu hoá".
func expLimits(addr string, dur time.Duration) {
	fmt.Printf("\n--- G6: cái gì chạm trần trước khi không pool\n")
	fmt.Printf("  ulimit -n            : %d\n", fdLimit())
	fmt.Printf("  ip_local_port_range  : %s\n", portRange())
	fmt.Printf("  sockstat TRƯỚC       : %s\n", sockstat())

	req := request{respSize: 64}
	deadline := time.Now().Add(dur)
	var ok, failed int
	var firstErr error
	b0, t0 := cpuBusy()
	start := time.Now()
	for time.Now().Before(deadline) {
		cc, err := dial(addr, true)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			if failed > 100 {
				break
			}
			continue
		}
		if _, err := cc.roundtrip(req); err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
		} else {
			ok++
		}
		cc.close() // client chủ động đóng => client giữ TIME_WAIT
	}
	elapsed := time.Since(start)
	b1, t1 := cpuBusy()

	fmt.Printf("  sockstat SAU         : %s\n", sockstat())
	fmt.Printf("  connection thành công: %d trong %s = %.0f conn/s\n", ok, elapsed.Round(time.Millisecond), float64(ok)/elapsed.Seconds())
	fmt.Printf("  connection lỗi       : %d\n", failed)
	if t1 > t0 {
		fmt.Printf("  CPU toàn máy         : %.1f%% busy trong suốt thí nghiệm (%d core)\n",
			100*float64(b1-b0)/float64(t1-t0), runtime.NumCPU())
		fmt.Printf("  => nếu conn/s thấp mà CPU rỗi thì trần KHÔNG ở CPU. Đó là điểm của G6.\n")
	}
	if firstErr != nil {
		fmt.Printf("  lỗi đầu tiên         : %v\n", firstErr)
		fmt.Printf("  => ĐỌC KỸ lỗi này. \"cannot assign requested address\" = cạn port,\n")
		fmt.Printf("     \"too many open files\" = cạn fd. Hai thứ này KHÁC nhau và cách sửa khác nhau.\n")
	} else {
		fmt.Printf("  => chưa chạm trần trong %s. Tăng -duration, hoặc so conn/s này với\n", dur)
		fmt.Printf("     (số port khả dụng / 60s) để biết mức bền vững thật.\n")
	}
}
