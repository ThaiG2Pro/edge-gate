package main

import (
	"fmt"
	"sync"
	"time"
)

// G4 — coordinated omission.
//
// Server bị giới hạn ở workers=1 và svc mỗi request, nên capacity = 1/svc.
// Ta cố ý áp tải VƯỢT capacity, rồi đo cùng trạng thái quá tải đó bằng hai cách:
//
//   - closed-loop (ab/wrk/hey): mỗi worker gửi request kế tiếp SAU khi nhận
//     response. Server chậm đi thì client tự gửi ít đi. Tải áp lên hệ thống bị
//     chính hệ thống điều tiết ⇒ không bao giờ tạo được backlog ⇒ tail bốc hơi.
//   - open-loop (vegeta -rate, wrk2 -R): request phát đúng theo lịch, bất kể
//     server có kịp hay không. Latency tính từ THỜI ĐIỂM ĐÁNG LẼ PHẢI GỬI, nên
//     thời gian nằm chờ trong hàng đợi được tính vào — đó là điều người dùng thật cảm nhận.
func expOmission(addr string, rate float64, dur time.Duration, svc time.Duration, capacity float64) {
	req := request{svcMicros: uint32(svc.Microseconds()), respSize: 256}

	closed := func(conc int) *sample {
		s := &sample{name: fmt.Sprintf("closed-loop, %d conn", conc), loop: "closed", requestedRPS: rate}
		var mu sync.Mutex
		var wg sync.WaitGroup
		deadline := time.Now().Add(dur)
		start := time.Now()
		for w := 0; w < conc; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cc, err := dial(addr, true)
				if err != nil {
					mu.Lock()
					s.errs++
					mu.Unlock()
					return
				}
				defer cc.close()
				var local []time.Duration
				for time.Now().Before(deadline) {
					lat, err := cc.roundtrip(req)
					if err != nil {
						break
					}
					local = append(local, lat)
				}
				mu.Lock()
				s.lats = append(s.lats, local...)
				mu.Unlock()
			}()
		}
		wg.Wait()
		s.achievedRPS = float64(len(s.lats)) / time.Since(start).Seconds()
		s.note = fmt.Sprintf("client KHÔNG BAO GIỜ áp nổi %.0f rps: nó chỉ gửi khi được trả lời", rate)
		return s
	}

	open := func() *sample {
		s := &sample{name: "open-loop, rate cố định", loop: "open", requestedRPS: rate}
		conns := 512
		pool := make([]*clientConn, 0, conns)
		for i := 0; i < conns; i++ {
			cc, err := dial(addr, true)
			if err != nil {
				break
			}
			pool = append(pool, cc)
		}
		if len(pool) == 0 {
			warn("open-loop: không dial được connection nào")
			return s
		}
		defer func() {
			for _, cc := range pool {
				cc.close()
			}
		}()

		var mu sync.Mutex
		var wg sync.WaitGroup
		interval := time.Duration(float64(time.Second) / rate)
		total := int(rate * dur.Seconds())
		start := time.Now()
		// free là free-list chứa CHÍNH connection rỗi. Bản trước dùng semaphore
		// đếm slot rồi chọn pool[i%len(pool)]: hai goroutine có thể nhận cùng
		// một conn ⇒ race trên bufio.Reader ⇒ panic "slice bounds out of range"
		// (lộ khi trả P0-2 ở -rate 100000; ở 1200 chỉ va khi hàng đợi > 427 ms).
		free := make(chan *clientConn, len(pool))
		for _, cc := range pool {
			free <- cc
		}
		var dropped int
		for i := 0; i < total; i++ {
			// scheduled = thời điểm request NÀY đáng lẽ phải được gửi.
			// Latency đo từ mốc này, không từ lúc thật sự gửi được. Đây là
			// một dòng code, và nó là toàn bộ khác biệt của phép đo này.
			scheduled := start.Add(time.Duration(i) * interval)
			if wait := time.Until(scheduled); wait > 0 {
				time.Sleep(wait)
			}
			var cc *clientConn
			select {
			case cc = <-free:
			default:
				// Hết connection rỗi: ghi nhận là DROP, không im lặng chờ.
				// Im lặng chờ ở đây chính là tự tạo ra coordinated omission
				// ngay trong bộ đo open-loop.
				mu.Lock()
				dropped++
				mu.Unlock()
				continue
			}
			wg.Add(1)
			go func(cc *clientConn, sched time.Time) {
				defer wg.Done()
				defer func() { free <- cc }()
				_, err := cc.roundtrip(req)
				lat := time.Since(sched)
				mu.Lock()
				if err != nil {
					s.errs++
				} else {
					s.lats = append(s.lats, lat)
				}
				mu.Unlock()
			}(cc, scheduled)
		}
		wg.Wait()
		s.achievedRPS = float64(len(s.lats)) / time.Since(start).Seconds()
		s.note = fmt.Sprintf("%d request bị drop vì hết connection rỗi (backlog thật)", dropped)
		return s
	}

	c1 := closed(1)
	c50 := closed(50)
	o := open()

	printTable(fmt.Sprintf("G4: coordinated omission (server capacity ≈ %.0f rps, áp %.0f rps)", capacity, rate),
		[]*sample{c1, c50, o})
	ratio("G4", "p99 open-loop / p99 closed-loop 1 conn", o.pct(99), c1.pct(99), ">3x")
	ratio("G4", "p99 open-loop / p99 closed-loop 50 conn", o.pct(99), c50.pct(99), ">3x")
	println()
	println("  Đọc cột `rps yêu cầu` vs `đạt được`: closed-loop không đạt được rate đã đặt.")
	println("  Nó báo p99 của một tải NHẸ HƠN tải mình tưởng đang áp. Đó là lời nói dối.")
}
