package main

import (
	"fmt"
	"time"
)

// G7 — Nagle + delayed ACK: spike ~40ms.
//
// Hình dạng kinh điển: write-write-read. Client ghi request bằng HAI lần Write
// và Nagle đang bật (SetNoDelay(false)).
//   - Write thứ nhất đi ngay (không có dữ liệu nào đang bay).
//   - Write thứ hai bị Nagle GIỮ LẠI, vì còn dữ liệu chưa được ACK.
//   - Server không thể trả lời: nó còn thiếu phần sau của request. Nên nó không
//     có dữ liệu để piggyback ACK, và delayed ACK giữ ACK lại tới ~40ms.
//   - Hết 40ms, ACK về, Nagle nhả Write thứ hai, request mới đủ.
//
// Hai cơ chế đều "đúng" khi xét riêng. Ghép lại thành deadlock 40ms. Đây là lý
// do mọi proxy đều SetNoDelay(true), và là lý do phải ghi header+body bằng MỘT
// lần Write (hoặc qua bufio.Writer) thay vì hai.
func expNagle(addr string, n int) {
	run := func(name string, nodelayClient bool, split bool, serverNagle bool) *sample {
		s := &sample{name: name, loop: "-"}
		cc, err := dial(addr, nodelayClient)
		if err != nil {
			warn("dial: %v", err)
			s.errs++
			return s
		}
		defer cc.close()
		var flags uint8
		if serverNagle {
			flags |= flagNagleOn
		}
		// payload đủ nhỏ để Nagle coi là "small packet" (< MSS).
		req := request{flags: flags, respSize: 64, payload: make([]byte, 64)}
		for i := 0; i < 20; i++ {
			if split {
				_, _ = cc.roundtripSplit(req)
			} else {
				_, _ = cc.roundtrip(req)
			}
		}
		for i := 0; i < n; i++ {
			var dur time.Duration
			var err error
			if split {
				dur, err = cc.roundtripSplit(req)
			} else {
				dur, err = cc.roundtrip(req)
			}
			if err != nil {
				s.errs++
				continue
			}
			s.lats = append(s.lats, dur)
		}
		return s
	}

	base := run("1 Write, NoDelay(true)", true, false, false)
	split := run("2 Write, NoDelay(true)", true, true, false)
	nagle := run("2 Write, NoDelay(FALSE) — write-write-read", false, true, true)

	printTable(fmt.Sprintf("G7: Nagle + delayed ACK (%d request mỗi biến thể)", n),
		[]*sample{base, split, nagle})
	ratio("G7", "p99 Nagle-write-write / p99 NoDelay-write-write", nagle.pct(99), split.pct(99), "rất lớn nếu spike xảy ra")
	fmt.Printf("  Số phải tìm: p99 hoặc max của dòng cuối ≈ 40ms (delayed ACK timeout của Linux).\n")
	fmt.Printf("  ĐO ĐƯỢC 2026-09-03 trên WSL2 loopback: p50 44.03ms, p90 44.57ms, p99 47.99ms.\n")
	fmt.Printf("  Tức spike này tái hiện HOÀN HẢO ngay trên loopback, trái với phỏng đoán ban đầu\n")
	fmt.Printf("  rằng loopback (MSS lớn, ít delayed ACK) sẽ che nó. Và nó là HẰNG SỐ, không phải\n")
	fmt.Printf("  spike ở đuôi: p50 và p99 chỉ chênh 4ms. Nếu lần chạy này KHÔNG ra ~40ms thì\n")
	fmt.Printf("  nghi bộ đo trước: kiểm -bufio=false (bufio.Writer gộp 2 Write và che sạch spike).\n")
}
