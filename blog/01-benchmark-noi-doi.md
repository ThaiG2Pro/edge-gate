# Bài 1 — Benchmark của bạn đang nói dối

> Series [Mở nắp reverse proxy](README.md) · bài 1/16 · cần đọc trước: [bài 0](00-mot-request.md)

Trước khi lên production, bạn chạy load test. Công cụ báo p99 dưới 2 ms. Bạn chụp màn hình, gửi vào
channel, mọi người yên tâm. Tuần sau traffic tăng, và người dùng bắt đầu than mỗi lần bấm nút phải
chờ vài giây. Không ai sửa gì. Load test cũng không sai phép tính nào.

Nó chỉ đo một tải khác với tải bạn tưởng. Bài này cho bạn thấy cùng một server quá tải có thể báo
p99 chênh nhau vài trăm lần, chỉ tuỳ cách công cụ đo gửi request.

Bài này chỉ cần Go. Ba biến thể, mỗi biến thể chạy 10 giây (`-duration 10s`):

```bash
git clone https://github.com/ThaiG2Pro/edge-gate.git && cd edge-gate
make netlab-omission
# tức là: go run ./cmd/netlab -exp omission -rate 885 -duration 10s -svc 1ms -workers 1 -bufio=false
```

## Thí nghiệm

Server trong thí nghiệm bị bó tay có chủ đích: xử lý một request mỗi lúc (`-workers 1`), mỗi request
ngủ 1 ms (`-svc 1ms`). Trên giấy, capacity là 1000 request/giây. Đo thật thì chỉ được **737 rps**, vì
`time.Sleep(1ms)` thật ra ngủ khoảng 1.3 ms (`bench/p0-2-capacity.txt`). Thí nghiệm áp **885 rps**,
tức 1.2 lần capacity đo được. Server chắc chắn quá tải.

Rồi đo cùng trạng thái quá tải đó bằng ba cách:

- **closed-loop, 1 conn:** một client, gửi request kế tiếp sau khi nhận xong response. Đây là cách
  `wrk`, `ab`, `hey` làm việc.
- **closed-loop, 50 conn:** như trên, nhưng 50 client song song.
- **open-loop:** request phát đúng lịch 885 cái mỗi giây, bất kể server đã trả lời hay chưa.

Trên Linux thuần (CachyOS), lượt 1:

```console
--- G4: coordinated omission (server capacity ≈ 1000 rps, áp 885 rps)
biến thể                           loop            n      mean       p50       p90       p99       max    err
closed-loop, 1 conn                closed       7060    1.41ms    1.28ms    1.80ms    2.19ms    5.07ms      0
closed-loop, 50 conn               closed       8036   62.41ms   62.05ms   63.93ms   75.01ms   84.91ms      0
open-loop, rate cố định            open         8832  325.40ms  328.60ms  570.33ms  604.86ms  607.45ms      0
  rps closed-loop, 1 conn            yêu cầu      885  đạt được      706  (79.8%)
  rps closed-loop, 50 conn           yêu cầu      885  đạt được      799  (90.2%)
  rps open-loop, rate cố định        yêu cầu      885  đạt được      834  (94.2%)
  [G4] p99 open-loop / p99 closed-loop 1 conn         276.30x   (kỳ vọng >3x)
  [G4] p99 open-loop / p99 closed-loop 50 conn          8.06x   (kỳ vọng >3x)
```

(File gốc: `bench/baseline/thai-computer-20261004/p0-omission.txt`, ba lượt. Dòng "capacity ≈ 1000" là
capacity trên giấy, in sẵn trong tiêu đề.)

Cả ba lượt, đặt cạnh nhau:

| p99, cùng server quá tải | Linux thuần, lượt 1 | lượt 2 | lượt 3 |
|---|---|---|---|
| closed-loop, 1 conn | **2.19 ms** | 1.31 ms | 1.33 ms |
| closed-loop, 50 conn | 75.01 ms | 66.67 ms | 64.44 ms |
| open-loop | **604.86 ms** | 625.49 ms | 620.64 ms |
| open / closed-1 | **276.30x** | 476.00x | 465.91x |

Cùng một server, cùng một lúc, cùng một mức quá tải. Một công cụ báo p99 1.31–2.19 ms, công cụ kia
báo 604.86–625.49 ms. Chênh nhau **276–476 lần**. Không có con số nào trong bảng là sai phép tính.

Trên WSL2, lượt đo sau khi sửa generator (xem mục dưới), áp 1200 rps, chạy dưới `go run -race` trong
5 giây để chắc bản sửa không còn race:

```console
closed-loop, 1 conn                closed       3100    1.61ms    1.60ms    1.87ms    2.13ms    4.02ms      0
closed-loop, 50 conn               closed       3521   71.46ms   71.74ms   76.23ms   80.64ms   82.02ms      0
open-loop, rate cố định            open         3841  648.65ms  751.81ms  779.10ms  790.41ms  793.93ms      0
  [G4] p99 open-loop / p99 closed-loop 1 conn         370.56x   (kỳ vọng >3x)
```

(File gốc: `bench/p0-2-race.txt`. Một lượt khác cùng ngày, áp hẳn 100000 rps, cho 377.09x:
`bench/p0-2-capacity.txt`. Máy đo chính là laptop chạy WSL2, số tuyệt đối không mang sang máy khác
được. Chỉ tỉ số là đáng tin, và ở đây hai môi trường cùng cho vài trăm lần.)

### Cột mà không ai đọc

Nhìn lại dòng `rps ... đạt được` của closed-loop 1 conn: yêu cầu 885, **đạt 706**. Client đó không bao
giờ áp nổi tải bạn đặt. Nó chỉ gửi khi được trả lời, nên server chậm đi thì nó tự gửi ít đi. Con số
2.19 ms là p99 của một tải **nhẹ hơn** tải bạn tưởng đang áp, một tải mà chính server đã tự điều tiết
xuống mức nó chịu được.

Một báo cáo kiểu "hệ thống chịu được 706 rps với p99 2 ms" đúng từng chữ. Và nó hoàn toàn vô nghĩa.

### Con số 1787 lần, và vì sao tôi rút lại nó

Lần đo đầu tiên của dự án, trên WSL2, cho một tỉ số còn đẹp hơn:

```console
$ go run ./cmd/netlab -exp omission -rate 1200 -duration 10s -svc 1ms -workers 1 -bufio=false
closed-loop, 1 conn                closed       7545    1.32ms    1.28ms    1.57ms    1.87ms    6.82ms      0
open-loop, rate cố định            open         9040  617.50ms  139.59ms     2.09s     3.35s     4.15s      0
  [G4] p99 open-loop / p99 closed-loop 1 conn         1787.50x   (kỳ vọng >3x)
```

(File gốc: `bench/p0-omission-rtt0.txt`.)

1.87 ms so với 3.35 giây. Con số này từng nằm trong README. Nó sai, vì hai lý do mà tôi chỉ thấy khi
đi trả một món nợ khác (`docs/debts.md`, P0-2):

1. **Generator open-loop có race.** Nó chọn connection theo `i % len(pool)`, nên hai goroutine có thể
   cầm cùng một connection và đọc chung một `bufio.Reader`. Ở 100000 rps nó panic ngay. Ở 1200 rps nó
   âm thầm va mỗi khi hàng đợi dài quá 427 ms. Tức là có va trong chính lần đo 1787 lần.
2. **1200 rps không phải 1.2 lần capacity.** Capacity đo được là 737, nên 1200 là 1.63 lần.

Sửa generator xong, đo lại ở 1200 rps ra **370.56 lần** (khối WSL2 ở trên). Kết luận vẫn đứng: open-loop
thấy hàng đợi, closed-loop không thấy. Nhưng con số 1787 thì không tin được nữa. Bộ đo coordinated
omission tự nó cũng đã có bug, và bug đó làm con số **đẹp hơn**, không phải xấu đi.

## Bên trong: tính giờ từ lúc hẹn, không từ lúc gửi

Toàn bộ khác biệt giữa hai cách đo nằm ở **mốc bắt đầu** của đồng hồ. Đây là vòng phát của open-loop
trong `cmd/netlab/exp_omission.go` (bỏ bớt phần phụ):

```go
for i := 0; i < total; i++ {
	// scheduled = thời điểm request NÀY đáng lẽ phải được gửi.
	scheduled := start.Add(time.Duration(i) * interval)
	if wait := time.Until(scheduled); wait > 0 {
		time.Sleep(wait)
	}
	var cc *clientConn
	select {
	case cc = <-free:                    // free-list: mỗi conn chỉ một goroutine cầm (bản sửa race)
	default:
		dropped++                        // hết conn rỗi: ĐẾM là drop, không im lặng chờ
		continue
	}
	go func(cc *clientConn, sched time.Time) {
		defer func() { free <- cc }()
		_, err := cc.roundtrip(req)
		lat := time.Since(sched)         // tính từ giờ HẸN
		// ...
	}(cc, scheduled)
}
```

Closed-loop thì đo `time.Since(start)` với `start` là lúc nó thật sự gửi. Khi server nghẽn, closed-loop
**ngồi chờ** response trước rồi mới gửi tiếp. Khoảng chờ đó không nằm trong số đo nào. Người dùng thật
thì không chờ nhau: họ bấm nút theo nhịp của họ, và request thứ một trăm phải xếp sau chín mươi chín
request trước. Open-loop tính khoảng xếp hàng đó vào latency, vì nó đo từ lúc request **đáng lẽ** được gửi.

Dòng `dropped++` cũng quan trọng không kém. Nếu hết connection rỗi mà generator ngồi chờ, nó tự biến
thành closed-loop ngay bên trong bộ đo open-loop. Trên Linux, ba lượt đếm được 18, 66 và 80 request bị
drop. Đó là backlog thật, được ghi lại thay vì giấu đi.

Phần còn lại của repo dùng `internal/loadgen` cho mọi số đo dưới tải, theo đúng quy tắc này:

```go
// Request i có giờ hẹn t0 + i/Rate; latency = giờ đọc xong response − giờ HẸN,
// không phải giờ gửi.
func (s Sample) Latency() time.Duration { return s.Done.Sub(s.Sched) }
```

Vì sao closed-loop 50 conn tệ hơn 1 conn nhưng vẫn còn xa open-loop? 50 client cùng chờ thì server có
hàng đợi dài tối đa 50. Hàng đợi có trần, nên p99 có trần. Open-loop không có trần nào: hàng đợi phình
theo thời gian, cho tới khi hết connection để gửi.

## Mang về dùng

1. **Hỏi công cụ load test của bạn là open-loop hay closed-loop.** `wrk`, `ab`, `hey` là closed-loop.
   `wrk2 -R` và `vegeta -rate` phát theo lịch cố định. Một con số p99 không kèm chữ "open" hay "closed"
   thì chưa đọc được.
2. **Đọc cột "đạt được" trước mọi percentile.** Nếu rps đạt được thấp hơn rps bạn đặt, thì p99 kia là
   của một tải khác. Và hãy **đo** capacity, đừng tính nó: 1 ms mỗi request không có nghĩa là 1000 rps.
3. **Nghi bộ đo khi con số quá đẹp.** Con số 1787 lần đẹp vì generator có race. Chạy generator dưới
   `go run -race` một lần trước khi tin nó, và chạy lại thí nghiệm trên một máy thứ hai.

---

Số đo gốc: [`bench/baseline/thai-computer-20261004/p0-omission.txt`](../bench/baseline/thai-computer-20261004/p0-omission.txt)
(Linux thuần), [`bench/p0-2-race.txt`](../bench/p0-2-race.txt) (WSL2, sau khi sửa),
[`bench/p0-omission-rtt0.txt`](../bench/p0-omission-rtt0.txt) (WSL2, bản có race),
[`bench/p0-2-capacity.txt`](../bench/p0-2-capacity.txt) · nhật ký: [`diary/phase0.md`](../diary/phase0.md)
(mục G4 và errata), [`docs/debts.md`](../docs/debts.md) (P0-2).

**Bài tiếp theo:** [Bài 2: Bí ẩn 40 mili giây](02-40ms.md)
