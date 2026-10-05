# Bài 9 — Xếp hàng hay từ chối?

> Series [Mở nắp reverse proxy](README.md) · bài 9/16 · cần đọc trước: [bài 1](01-benchmark-noi-doi.md)

Một chiến dịch marketing chạy sớm hơn dự kiến. Traffic tăng gấp đôi những gì cụm backend xử lý nổi.
Proxy phía trước có hai lựa chọn với request thừa:

- **xếp hàng**: giữ lại, chờ backend rảnh. Không ai bị từ chối, ai rồi cũng được phục vụ.
- **từ chối**: trả 503 ngay cho phần vượt quá. Một nửa người dùng thấy lỗi.

Lựa chọn đầu nghe tử tế hơn. Bài này đo xem nó tử tế với ai.

## Thí nghiệm

4 backend, mỗi backend xử lý tối đa 4 request song song, mỗi request 10 ms. Capacity là 1 600 rps.
Bắn **3 200 rps** (gấp đôi) trong 10 giây, **open-loop**: request được hẹn giờ trước, latency tính
từ giờ hẹn, không phải từ lúc gửi (bài 1). Chạy hai chế độ:

- `noshed`: không giới hạn gì, request nào cũng chờ tới lượt.
- `shed`: tối đa 16 request đang chạy, tối đa 16 request chờ, chờ quá 50 ms thì 503.

```bash
make shedlab     # khoảng 33 giây
```

```console
$ ./bin/shedlab -x 2 -duration 10s
shedlab: 4 backend × conc 4 × 10ms ⇒ capacity 1600 rps; tải 3200 rps (2.0x) trong 10s;
         shed inflight=16 queue=16 queue-timeout=50ms; generator 256 worker

noshed         n=32000 200:100.0%
               200: p50 5.80686s p90 10.53345s p99 11.63283s ... · goodput 1471/s
               bắn xong lịch sau 21.747s (lịch 10s)
               p99 200 theo giây: s0:1.169s s1:2.307s s2:3.448s s3:4.586s s4:5.796s
                                  s5:6.965s s6:8.174s s7:9.364s s8:10.522s s9:11.737s

shed           n=32000 200:45.1% 503:54.9%
               200: p50 21.57ms p90 24.1ms p99 33.08ms ... · goodput 1441/s
               bắn xong lịch sau 10.02s (lịch 10s); proxy {ShedQueueFull:17563 ShedTimeout:0 ...}
               503: p50 710µs p99 8.58ms
               p99 200 theo giây: s0:37ms s1:32ms s2:25ms s3:24ms s4:24ms
                                  s5:26ms s6:49ms s7:38ms s8:28ms s9:27ms
```

(Số đo gốc: `bench/p7-shedlab.txt`, WSL2, 2026-10-01. Đã bỏ bớt cột và xuống dòng cho dễ đọc.)

In cả hai lượt, cộng số Linux thuần:

| 2x capacity, 10 s | WSL2 lượt 1 (2026-10-01) | WSL2 lượt 2 (2026-10-05) | Linux thuần (CachyOS), 3 lượt |
|---|---|---|---|
| không shed: p99 của 200 | **11.63283 s** | 11.56674 s | 11.36169-11.42581 s |
| không shed: goodput | 1471/s | 1475/s | 1485-1490/s |
| shed: p99 của 200 | **33.08 ms** | 23.84 ms | 23.57-23.67 ms |
| shed: goodput | 1441/s | 1488/s | 1484-1486/s |
| shed: phần nhận 503 | 54.9 % | 53.4 % | 53.5 % |
| shed: p99 của 503 | 8.58 ms | 1.63 ms | 1.35 ms |

(Lượt 2 chỉ ghi lại các số trên, không có file output riêng. Linux:
`bench/baseline/thai-computer-20261004/p7-shedlab.txt`. Máy đo chính là WSL2, nên chỉ tỉ số là đáng
tin. Ở đây cả ba cột kể cùng một chuyện.)

Hai điều đáng nhìn.

**Goodput gần như bằng nhau.** Không shed thì 1471 request/s thành công, có shed thì 1441/s (lượt
2: 1475 vs 1488). Shed không phục vụ ít người hơn. Backend vẫn chạy hết công suất trong cả hai chế
độ.

**Ai phải chờ thì khác hẳn.** Không shed, p99 là 11.63 giây, và dòng "theo giây" cho thấy nó tăng
khoảng 1.15 giây cho **mỗi giây** chạy. Lịch 10 giây mà 21.747 giây sau mới bắn xong. Có shed, request
được nhận có p99 33.08 ms, phẳng suốt bài. Nhật ký phase 7 chốt tỉ số **352x**. Người bị từ chối nhận
503 trong vòng 0.71 ms (p50).

## Bên trong: hàng đợi có trần

### Vì sao hàng đợi không trần làm hại tất cả

Thừa 1 600 rps nghĩa là mỗi giây có thêm 1 600 request phải chờ, tức thêm một giây chờ cho **mọi
người đến sau**. Hàng đợi không trần là FIFO: request thứ N chờ mọi request trước nó, kể cả những
request mà client đã bỏ cuộc từ lâu. Sau 10 giây quá tải, người dùng mới tới phải chờ 11 giây, dù
backend vẫn làm việc hết sức.

Với shed, hàng đợi chỉ dài 16. Request thứ 17 nhận 503 ngay, nên người được nhận không bao giờ phải
chờ quá một hàng ngắn.

### Đoạn code

Slot là một channel có buffer. Hàng chờ là một bộ đếm atomic có trần (`internal/proxy/resilience.go`,
rút gọn):

```go
select {
case r.slots <- struct{}{}:
	return 0, "", func() { <-r.slots } // còn slot: chạy ngay
default:
}
// Hết slot: xếp hàng nếu hàng còn chỗ. Hàng có trần là toàn bộ ý nghĩa của shedding.
if r.queued.Add(1) > int64(s.cfg.Shed.MaxQueue) {
	r.queued.Add(-1)
	r.shedFull.Add(1)
	return 503, "quá tải, shed: hàng đợi đầy", nil
}
t := time.NewTimer(s.cfg.Shed.QueueTimeout)
defer t.Stop()
select {
case r.slots <- struct{}{}:
	r.queued.Add(-1)
	return 0, "", func() { <-r.slots }
case <-t.C:
	r.queued.Add(-1)
	r.shedTimeout.Add(1)
	return 503, "quá tải, shed: chờ slot quá QueueTimeout", nil
}
```

Hàm này chạy **sau** khi đọc xong head của request, **trước** khi chọn backend. Request bị từ chối
không tốn gì của upstream. Hàm trả về một `release`, và `roundTrip` gọi nó bằng `defer`, nên slot
được trả trên mọi đường ra, kể cả khi upstream lỗi. 503 mang theo `Retry-After: 1`.

Trong lượt đo, cả 17 563 request bị từ chối đều do hàng đầy (`ShedQueueFull`), không request nào do
chờ quá 50 ms (`ShedTimeout:0`). Với tải gấp đôi, hàng luôn đầy.

### Tôi đã đoán sai chỗ hàng đợi nằm

Trước khi đo, nhật ký phase 7 đoán: không shed thì phần lớn request sẽ thành **504** sau 5 giây, vì
proxy chờ upstream quá deadline. Thực tế: **100 % là 200**, không có cái 504 nào.

Hàng đợi không nằm ở proxy. Nó nằm ở **client**: generator chỉ có 256 worker, và khi cả 256 đều đang
chờ thì request mới phải đợi worker rảnh. Bản đo trên Linux in thêm dòng lag của generator, tức khoảng
từ giờ hẹn tới lúc worker bắt đầu gửi:

```console
noshed   lag generator (Start−Sched, nằm trong latency): p50 5.5029s p99 11.23885s max 11.35561s
shed     lag generator (Start−Sched, nằm trong latency): p50 510µs p99 1.21ms max 4.22ms
```

(`bench/baseline/thai-computer-20261004/p7-shedlab.txt`, lượt 1.)

Gần như toàn bộ 11 giây chờ là chờ **trước khi gửi**. Phía proxy, mỗi request chỉ thấy một khoảng
chờ ngắn, nên không deadline nào kích hoạt. Hệ quả thực tế đáng sợ hơn con số: một dashboard đo
latency **tại proxy** sẽ không thấy gì bất thường, trong khi người dùng chờ 11 giây. Đây là lý do
shedding chỉ đo được bằng open-loop từ phía client. Một tool closed-loop như `wrk` sẽ tự gửi chậm lại
theo tốc độ server và không bao giờ thấy hàng này (bài 1).

### Shed theo request, không theo connection

Slot được giành sau khi request có head đầy đủ, không phải lúc accept connection. Lý do nằm ở bài
10: một trần đếm **connection** là đúng thứ mà Slowloris cần để chiếm chỗ. Connection Slowloris không
bao giờ gửi xong head, nên không bao giờ chạm tới slot.

## Mang về dùng

1. **Đặt trần cho hàng đợi, và trả 503 nhanh khi đầy.** Quá tải thì goodput không đổi, chỉ đổi **ai**
   phải chờ. 503 trong một mili giây cứu được client: nó retry, đổi sang region khác, hoặc báo lỗi rõ
   ràng. Chờ 11 giây thì không cứu được ai.
2. **Đo quá tải bằng open-loop, từ phía client.** Latency đo ở proxy có thể phẳng trong khi hàng đợi
   thật nằm ở phía client. Hỏi tool load test của bạn: latency tính từ giờ hẹn hay từ giờ gửi?
3. **Giới hạn số request đang chạy, không giới hạn số connection.** Trần theo request bảo vệ backend.
   Trần theo connection mở cửa cho kẻ tấn công.

---

Số đo gốc: [`bench/p7-shedlab.txt`](../bench/p7-shedlab.txt),
[`bench/baseline/thai-computer-20261004/p7-shedlab.txt`](../bench/baseline/thai-computer-20261004/p7-shedlab.txt) ·
nhật ký: [`diary/phase7.md`](../diary/phase7.md) (G7, câu 6, Giả thuyết sai) ·
code: [`internal/proxy/resilience.go`](../internal/proxy/resilience.go) (`admitDecision`),
[`internal/loadgen/loadgen.go`](../internal/loadgen/loadgen.go).

**Bài tiếp theo:** [Bài 10: Slowloris không giết được Go](10-slowloris.md)
