package main

import "fmt"

// G1 — giá của một syscall Write.
//
// Cùng một response, server ghi bằng 1 lần Write (length+payload trong một
// buffer) so với 2 lần Write (length rồi payload). Đây chính xác là hình dạng
// của một proxy ghi header rồi io.Copy body mà không có bufio.Writer ở giữa.
func expSyscall(addr string, n int, respSize int) {
	run := func(name string, flags uint8, nodelayClient bool, n int) *sample {
		s := &sample{name: name, loop: "-"}
		cc, err := dial(addr, nodelayClient)
		if err != nil {
			warn("dial: %v", err)
			s.errs++
			return s
		}
		defer cc.close()
		req := request{flags: flags, respSize: uint32(respSize)}
		// Warmup: bỏ đi các request đầu. Không warmup thì đo lẫn cả chi phí
		// cấp phát buffer lần đầu và cả việc goroutine chưa được lên lịch ổn định.
		warmup := min(200, n)
		for i := 0; i < warmup; i++ {
			if _, err := cc.roundtrip(req); err != nil {
				warn("warmup: %v", err)
				s.errs++
				return s
			}
		}
		for i := 0; i < n; i++ {
			lat, err := cc.roundtrip(req)
			if err != nil {
				s.errs++
				continue
			}
			s.lats = append(s.lats, lat)
		}
		return s
	}

	one := run(fmt.Sprintf("1 Write (resp %dB)", respSize), 0, true, n)
	two := run(fmt.Sprintf("2 Write (resp %dB)", respSize), flagTwoWrites, true, n)

	// Biến thể Nagle chạy ÍT request hơn 50 lần, và con số 50 đó là một kết quả
	// đo, không phải lựa chọn tuỳ ý: mỗi request ở đây tốn ~44ms (Nagle giữ
	// Write thứ hai + delayed ACK), nên n=5000 mất ~200 giây. Lần đo đầu tiên
	// của phase này đã mất đúng 200 giây vì không biết điều đó.
	nagleN := n / 50
	if nagleN < 30 {
		nagleN = 30
	}
	twoNagle := run(fmt.Sprintf("2 Write + Nagle server (resp %dB)", respSize), flagTwoWrites|flagNagleOn, true, nagleN)

	printTable("G1: 1 lần Write vs 2 lần Write (1 connection reuse, không pool)", []*sample{one, two, twoNagle})
	fmt.Printf("  LƯU Ý ĐỌC SỐ: nếu server chạy -bufio=true thì hai lần Write bị bufio.Writer\n")
	fmt.Printf("  gộp thành MỘT syscall lúc Flush, và tỉ số dưới đây sẽ ra ~1.0x. Đó là bộ đo\n")
	fmt.Printf("  sai, không phải kết luận \"syscall miễn phí\". Đo G1 phải chạy -bufio=false.\n")
	ratio("G1", "p50 2-Write / p50 1-Write", two.pct(50), one.pct(50), "1.5-2x")
	ratio("G1", "p99 2-Write / p99 1-Write", two.pct(99), one.pct(99), "1.5-2x")
	ratio("G1", "p50 2-Write+Nagle / p50 2-Write", twoNagle.pct(50), two.pct(50), "?")
}
