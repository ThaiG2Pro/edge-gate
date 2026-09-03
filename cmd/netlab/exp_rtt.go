package main

// G2 + G3 — RTT là đơn vị tiền tệ của mạng.
//
// Dial mới mỗi request so với reuse một connection. CÙNG một lệnh, chạy hai
// lần: RTT 0 (loopback trần) và RTT 20ms (tc netem, hoặc hai máy thật).
// Toàn bộ lý do connection pool tồn tại nằm ở CHÊNH LỆCH giữa hai lần chạy đó,
// không nằm ở con số nào trong một lần chạy.
func expRTT(addr string, n int, respSize int) {
	req := request{respSize: uint32(respSize)}

	reuse := &sample{name: "reuse 1 connection", loop: "-"}
	if cc, err := dial(addr, true); err != nil {
		warn("dial: %v", err)
		reuse.errs++
	} else {
		for i := 0; i < 100; i++ {
			_, _ = cc.roundtrip(req)
		}
		for i := 0; i < n; i++ {
			lat, err := cc.roundtrip(req)
			if err != nil {
				reuse.errs++
				continue
			}
			reuse.lats = append(reuse.lats, lat)
		}
		cc.close()
	}

	// Dial được tính VÀO latency — đó là điểm của phép đo. Một pool ẩn chi phí
	// này khỏi request, nó không làm chi phí biến mất.
	fresh := &sample{name: "dial mới mỗi request", loop: "-"}
	for i := 0; i < n; i++ {
		start := nowFunc()
		cc, err := dial(addr, true)
		if err != nil {
			fresh.errs++
			continue
		}
		if _, err := cc.roundtrip(req); err != nil {
			fresh.errs++
			cc.close()
			continue
		}
		fresh.lats = append(fresh.lats, sinceFunc(start))
		cc.close()
	}

	printTable("G2/G3: dial mới vs reuse (đọc kèm RTT của môi trường!)", []*sample{reuse, fresh})
	ratio("G2/G3", "p50 dial / p50 reuse", fresh.pct(50), reuse.pct(50), "RTT 0: 1.0-1.3x · RTT 20ms: >15x")
	ratio("G2/G3", "p99 dial / p99 reuse", fresh.pct(99), reuse.pct(99), "—")
	println()
	println("  Nếu tỉ số trên ~1.x thì KẾT LUẬN KHÔNG PHẢI \"pool vô dụng\".")
	println("  Kết luận đúng là: môi trường đo không có RTT. Chạy lại với")
	println("  `sudo tc qdisc add dev lo root netem delay 20ms`, hoặc `-role server`/`-role client` trên 2 máy.")
}
