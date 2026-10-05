# Bài 10 — Slowloris không giết được Go

> Series [Mở nắp reverse proxy](README.md) · bài 10/16 · cần đọc trước: [bài 9](09-shed.md)

Kẻ tấn công không cần băng thông lớn. Hắn mở 500 connection tới proxy của bạn, và trên mỗi
connection gửi request header **thật chậm**: một dòng mỗi 10 giây, không bao giờ gửi dòng trống kết
thúc header. Server cứ chờ phần còn lại. Đó là Slowloris, đòn đã hạ rất nhiều Apache.

Lời khuyên quen thuộc là đặt header timeout. Bài này đo xem với một proxy viết bằng Go, header
timeout có thật sự là thứ cứu bạn không, và thứ gì mới thật sự làm proxy chết.

## Thí nghiệm

Attacker mở 500 connection từ `127.0.0.2`, mỗi 10 giây gửi một dòng header. Cùng lúc, một probe từ
`127.0.0.1` gửi request bình thường 50 lần/giây (open-loop, bài 1) trong 30 giây. Câu hỏi duy nhất:
probe có còn được phục vụ không?

```bash
make slowlab              # HeaderTimeout 10 s; thêm -max-conns 256; thêm -max-inflight 64
make slowlab-nodefense    # build tag nodefense7: TẮT HeaderTimeout
```

```console
$ ./bin/slowlab -conns 500 -byte-every 10s
probe/attack   n=1500 200:100.0%
               200: p50 1.59ms p90 2.17ms p99 4.71ms p99.9 9.82ms max 10.68ms · goodput 50/s
               attacker: reconnect 1500, dial lỗi 0; proxy giữ 500 connection lúc hết probe
hold 12s:      attacker ngừng gửi, giữ socket ⇒ proxy còn giữ 0 connection

$ ./bin/slowlab-nd -conns 500 -byte-every 10s    # -tags nodefense7: không HeaderTimeout
probe/attack   n=1500 200:100.0%
               200: p50 1.42ms p90 2.12ms p99 3.72ms p99.9 6.19ms max 7.7ms · goodput 50/s
               attacker: reconnect 0, dial lỗi 0; proxy giữ 500 connection lúc hết probe
hold 12s:      attacker ngừng gửi, giữ socket ⇒ proxy còn giữ 500 connection
```

(Số đo gốc: `bench/p7-slowlab.txt` và `bench/p7-slowlab-nodefense.txt`, WSL2.)

**Tắt hẳn HeaderTimeout, probe vẫn sống 100 %**, p99 3.72 ms. 500 connection Slowloris không làm
proxy chậm đi chút nào. Bật HeaderTimeout thì khác ở đúng một chỗ: connection **im lặng** bị đóng
(dòng `hold`: 0 còn lại so với 500). Nhưng attacker chịu khó nối lại thì timeout cũng không giảm được
số connection: 1 500 lần reconnect trong 30 giây, proxy vẫn giữ 500 suốt bài.

Trên Linux thuần (CachyOS), 3 lượt mỗi chế độ, kết quả y hệt: probe 100 %, p99 lúc bị tấn công
2.1-2.33 ms có HeaderTimeout, 2.15-2.29 ms không có
(`bench/baseline/thai-computer-20261004/p7-slowlab.txt`, `p7-slowlab-nodefense.txt`). Máy đo chính
là WSL2, nên chỉ tỉ số là đáng tin. Ở đây không cần tỉ số: 100 % là 100 %.

### Vậy cái gì giết được proxy?

Thêm một dòng cấu hình nghe rất "an toàn": `MaxConns 256`, trần tổng số connection.

```console
$ ./bin/slowlab -conns 500 -byte-every 10s -max-conns 256
probe/attack   n=1500 200:78.4% timeout:324
               200: p50 5.45ms p90 4.01393s p99 6.11091s ... · goodput 35/s
$ ./bin/slowlab-nd -conns 500 -byte-every 10s -max-conns 256   # không HeaderTimeout
probe/attack   n=1500 timeout:1500
$ ./bin/slowlab -conns 500 -byte-every 10s -max-inflight 64
probe/attack   n=1500 200:100.0%
               200: p50 1.48ms p90 2.21ms p99 5.14ms ...
```

(Số đo gốc: `bench/p7-slowlab.txt`, `bench/p7-slowlab-nodefense.txt`, WSL2.)

| Cấu hình | probe thành công |
|---|---|
| không trần, không HeaderTimeout | **100 %** |
| không trần, HeaderTimeout 10 s | **100 %** |
| `MaxConns 256` + HeaderTimeout 10 s | 78.4 % (324 timeout) |
| `MaxConns 256`, không HeaderTimeout | **0 %** (1 500/1 500 timeout) |
| `MaxInflight 64` (trần request, bài 9) + HeaderTimeout | **100 %** |

Thứ giết proxy là **trần connection**, thứ mà ta thêm vào để bảo vệ nó. Attacker chiếm hết 256 chỗ,
probe xếp hàng bên ngoài. Bật HeaderTimeout thì còn 78.4 % probe sống: connection Slowloris bị đóng
sau 10 giây và phải giành chỗ lại, nên probe đôi khi chen được vào. Giảm thiệt hại, không phải chữa.

## Bên trong: không có worker pool thì không có gì để cạn

Slowloris giết Apache prefork vì mỗi connection giữ một **worker** trong một pool có hạn
(`MaxClients`). 500 connection gửi một byte mỗi 10 giây là đủ chiếm hết pool, và request thật xếp
hàng ngoài cửa.

Proxy Go chạy một goroutine cho mỗi connection. Goroutine không có pool để cạn. Connection Slowloris
ngồi chờ trong `Read`, không chiếm CPU, không chặn ai. Cái giá thật là **bộ nhớ**:

```console
$ ./bin/slowlab -conns 500 -byte-every 10s -target null -duration 5s -hold 1s   # chỉ attacker
attack:        ... bộ nhớ tiến trình +9.1 MiB ⇒ 18.6 KiB / connection (gồm cả phía attacker)
$ ./bin/slowlab -conns 500 -duration 5s -hold 1s                                # attacker + proxy
attack:        ... bộ nhớ tiến trình +19.3 MiB ⇒ 39.5 KiB / connection (gồm cả phía attacker)
```

(Số đo gốc: `bench/p7-slowlab-calib.txt`, 3 lượt, WSL2.)

Trừ phần của attacker đi, proxy tốn khoảng **20.6-20.9 KiB mỗi connection** treo: 8 KiB buffer đọc,
8 KiB buffer ghi, cộng stack goroutine (`diary/phase7.md`). 500 connection là khoảng 10 MiB. Nhật ký
ước tính 100 000 connection là khoảng 2 GiB, và mỗi cái một file descriptor. Đó mới là trần thật, và
nó lớn hơn nhiều so với 500.

Trần connection biến proxy Go trở lại thành Apache. Trong vòng accept (`internal/proxy/proxy.go`,
rút gọn):

```go
for {
	// D3: trần connection, giành chỗ TRƯỚC Accept. Đầy ⇒ không Accept ⇒ connection
	// mới nằm trong backlog kernel. Đây là hành vi worker-pool mà Slowloris cần.
	if s.res.connSem != nil {
		s.res.connSem <- struct{}{}
	}
	c, err := ln.Accept()
	// ...
	st, ok := s.track(c) // P7-2: đếm theo IP peer
	if !ok {
		s.res.perIPRejected.Add(1)
		// ... trả connSem
		if s.cfg.PerIPTarpit > 0 {
			go func(conn net.Conn, d time.Duration) { time.Sleep(d); conn.Close() }(c, s.cfg.PerIPTarpit)
		} else {
			c.Close()
		}
		continue
	}
	go func() { /* ... */ s.serveConn(c, st) }()
}
```

Ngược lại, trần **request** (`MaxInflight`, bài 9) được giành sau khi đọc xong head. Connection
Slowloris không bao giờ gửi xong head, nên không bao giờ chạm tới slot. Đó là lý do dòng cuối của
bảng ở trên là 100 %.

### Trần theo IP: đổi RAM lấy CPU

HeaderTimeout giới hạn **tuổi** của một connection, không giới hạn **số** connection một IP mở lại.
Cách chặn là `MaxConnsPerIP`: IP nào đã giữ đủ N connection thì connection kế bị đóng ngay sau
`Accept`, trước khi có goroutine hay buffer nào.

```bash
go run ./cmd/slowlab -conns 500 -byte-every 10s -max-conns-per-ip 100
```

```console
attack:        proxy giữ 100 connection (trước 0); ...
probe/attack   n=1500 200:100.0%
               200: p50 6.32ms p90 27.58ms p99 88.88ms p99.9 141.69ms max 175.59ms · goodput 50/s
               attacker: reconnect 828913, dial lỗi 0; proxy giữ 100 connection lúc hết probe
```

(Số đo gốc: `bench/p7-2-slowlab-perip100.txt`, WSL2.)

Proxy chỉ còn giữ 100 connection thay vì 500. Probe vẫn 100 %. Nhưng attacker bị đóng ngay thì nối
lại ngay: **828 913 lần** trong 30 giây. Accept loop bận, và p99 của probe tăng từ 4.71 ms lên
88.88 ms. Trần per-IP đổi RAM lấy CPU.

Hai cái bẫy đi kèm:

- **Ngưỡng quá thấp chặn người thật.** Lượt đầu chọn 32. Probe có 64 worker keep-alive từ cùng một
  IP, nên ngay cả khi chưa bị tấn công, probe chỉ được 51.6 % 200 (`bench/p7-2-slowlab-perip.txt`).
  Trần đếm mọi connection, kể cả keep-alive rỗi, nên phải lớn hơn độ đồng thời hợp lệ của **một** IP.
  Nhớ NAT: cả một văn phòng có thể đi ra từ một IP.
- **Bão reconnect.** Cách giảm là tarpit: giữ connection bị từ chối một lúc rồi mới đóng, để attacker
  không nối lại ngay được (`-tarpit 200ms`). Sổ nợ ghi số reconnect giảm 15.6 lần và p99 probe từ
  70.71 ms về 4.88 ms trên Linux thuần, nhưng các số này **(chưa có output thô trong repo)**, chỉ có
  trong `docs/debts.md` mục P7-2b.

### Tôi đã đoán sai

ROADMAP của dự án viết: trước khi có HeaderTimeout thì proxy không phục vụ được khi bị Slowloris.
Nhật ký phase 7 cũng đăng ký giả thuyết rằng HeaderTimeout sẽ làm attacker phải giữ ít connection
hơn. Cả hai đều sai với kiến trúc goroutine-per-connection. Không HeaderTimeout thì proxy vẫn sống.
Có HeaderTimeout thì attacker vẫn giữ 500 connection, chỉ cần nối lại.

HeaderTimeout vẫn cần: không có nó thì connection im lặng sống mãi, và RAM bị giữ mãi. Nhưng nó là
điều kiện cần, không phải điều kiện đủ.

## Mang về dùng

1. **Đặt trần theo request, đừng chỉ đặt trần tổng connection.** Trần connection toàn cục là đúng thứ
   Slowloris cần. Trần số request đang chạy, giành sau khi đọc xong head, thì miễn nhiễm.
2. **Thêm trần connection theo IP, với ngưỡng lớn hơn độ đồng thời hợp lệ của một IP.** Đo xem client
   thật (kể cả sau NAT) mở bao nhiêu connection, rồi đặt trên mức đó. Cân nhắc tarpit để attacker
   không biến trần thành bão reconnect.
3. **Tính ngân sách RAM cho connection treo.** Ở đây proxy tốn 20.6-20.9 KiB mỗi connection. Nhân với số
   connection bạn muốn chịu được, rồi đặt `ulimit -n` và giới hạn bộ nhớ cho khớp.

---

Số đo gốc: [`bench/p7-slowlab.txt`](../bench/p7-slowlab.txt),
[`bench/p7-slowlab-nodefense.txt`](../bench/p7-slowlab-nodefense.txt),
[`bench/p7-slowlab-calib.txt`](../bench/p7-slowlab-calib.txt),
[`bench/p7-2-slowlab-perip.txt`](../bench/p7-2-slowlab-perip.txt),
[`bench/p7-2-slowlab-perip100.txt`](../bench/p7-2-slowlab-perip100.txt),
[`bench/baseline/thai-computer-20261004/`](../bench/baseline/thai-computer-20261004/) (`p7-slowlab*.txt`) ·
nhật ký: [`diary/phase7.md`](../diary/phase7.md) (G2, G3, câu 2) ·
sổ nợ: [`docs/debts.md`](../docs/debts.md) (P7-2, P7-2b).

**Bài tiếp theo:** [Bài 11: Một connection rỗi tốn bao nhiêu RAM?](11-ram-connection.md)
