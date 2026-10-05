# Bài 13 — HTTP/2 có nhanh hơn HTTP/1.1 không?

> Series [Mở nắp reverse proxy](README.md) · bài 13/16 · cần đọc trước: [bài 3](03-keep-alive.md)

Bạn bật HTTP/2 ở proxy. Lý do ai cũng nghe: thay vì 6 hay 32 connection song song, trình duyệt dồn mọi
request vào **một** connection, multiplex thành nhiều stream, header được nén. Ít connection hơn, ít
handshake hơn, nhanh hơn.

Trên loopback thì có thể đúng. Còn trên mạng di động, nơi cứ vài chục gói lại mất một gói thì sao? Bài
này đo bốn thứ trên cùng proxy EdgeGate: hai thứ HTTP/2 hứa, và hai thứ nó đòi bạn trả.

(Máy đo là laptop WSL2, nên chỉ tỉ số là đáng tin. Mọi lệnh cần `make h2-bins` trước. Các thí nghiệm
có RTT và mất gói cần `sudo` để bật `tc netem`, và **phải** tháo ra sau khi đo.)

## Thí nghiệm

### Lời hứa 1: request chậm không chặn request nhanh

Trên **một** connection, gửi `/slow` (upstream trả sau 500 ms), 10 ms sau gửi `/fast`:

```bash
./bin/h2lab -mode hol -n 20
```

```console
hol: 20 vòng, RTT loopback, /slow=500 ms, /fast gửi sau 10 ms trên CÙNG connection
  /fast một mình (h2)     p50 0.59 ms
  /fast sau /slow, h2     p50 0.84 ms  p99 1.52 ms
  /fast sau /slow, h1     p50 490.63 ms  p99 491.36 ms
  h2 / một mình = 1.44x ; h1 / h2 = 581.3x
```

(Nguồn: `bench/p10-hol.txt`. Ba lượt cho h1/h2 = 581.3x, 694.8x, 746.6x.)

Với HTTP/1.1, response phải về đúng thứ tự request, nên `/fast` xếp hàng sau `/slow`: 490 ms. Với h2,
mỗi stream đi riêng, `/fast` về sau chưa tới 1 ms. Lời hứa này có thật.

### Lời hứa 2: header gần như miễn phí từ request thứ hai

```bash
./bin/h2lab -mode hpack -n 100
```

```console
hpack: 100 request, một connection h2c (client net/http)
  HEADERS #1        = 318 byte
  HEADERS #2..#100    trung vị = 12 byte (min 12, max 12)
  head h1 tương đương = 508 byte
  tỉ số h1 / trung vị h2 #2+ = 42.3x
```

(Nguồn: `bench/p10-hpack.txt`, ba lượt như nhau từng byte.)

318 byte còn 12 byte, ít hơn head h1 42 lần. Lời hứa này cũng có thật.

### Cái giá 1: mất gói

Giờ tới thí nghiệm chính. 32 GET song song, mỗi response 64 KiB, lặp 20 vòng. Hai cách: h2 dồn 32
stream vào **một** connection, h1 mở **32** connection. Chạy dưới `netem delay 10ms` (RTT 20 ms, vì netem
trên `lo` áp cả hai chiều, xem bài 3), trước không mất gói, sau mất 2 %:

```bash
sudo tc qdisc add dev lo root netem delay 10ms && make h2lab-tcphol; sudo tc qdisc del dev lo root
sudo tc qdisc add dev lo root netem delay 10ms loss 2% && make h2lab-tcphol; sudo tc qdisc del dev lo root
```

Lượt đầu dưới loss 2 %:

```console
tcphol: 20 vòng × 32 GET 65536 byte song song
  h2 1 conn × 32 stream: p50 112.2 ms  p90 163.4  p99 332.2  max 1246.1
  h1 32 conn            : p50 42.3 ms  p90 71.9  p99 293.3  max 525.4
  h2/h1: p50 2.65x  p99 1.13x
```

Gom cả ba lượt mỗi chế độ:

| 32 GET × 64 KiB, RTT 20 ms | loss 0 % | loss 2 % |
|---|---|---|
| h2 1 conn, p50 | 47.6–48.7 ms | 112.2–124.0 ms |
| h1 32 conn, p50 | 41.8–42.0 ms | 42.2–42.3 ms |
| **h2/h1 p50** | 1.14–1.16x | **2.65–2.93x** |
| h2 1 conn, p99 | 79.9–81.1 ms | 309.5–332.2 ms |
| h1 32 conn, p99 | 44.1–45.0 ms | 293.2–299.7 ms |
| **h2/h1 p99** | 1.78–1.84x | **1.06–1.13x** |

(Nguồn: `bench/p10-tcphol-d10-loss0.txt`, `bench/p10-tcphol-d10-loss2.txt`, WSL2.)

Mất 2 % gói làm trung vị của h1 gần như không nhúc nhích (42 ms). Trung vị của h2 tăng gần gấp ba.

Trước khi đo, nhật ký đăng ký: dưới loss 2 %, h2 tệ hơn h1 ít nhất 1.5 lần **cả ở p50 lẫn p99**. Vế p50
đúng. Vế p99 **sai**: đuôi gần như bằng nhau, 1.06–1.13 lần. Mất gói không làm đuôi của h2 dài hơn. Nó
làm **trung vị** tệ đi. Phần "Bên trong" giải thích vì sao. (Còn p99 1.8 lần ở loss 0 % thì nhật ký
ghi số nhưng chưa giải thích.)

Một đối chứng: ở RTT 0, không netem, h2/h1 p50 là 2.82x (`bench/p10-tcphol-rtt0.txt`). Đổi EdgeGate
thành server `net/http` của Go (`./bin/h2lab -mode tcphol -n 20 -par 32 -ref`) vẫn ra 1.89–2.97x
(`bench/p10-tcphol-rtt0-ref.txt`). Nên ở RTT 0, chậm là do 32 stream dồn qua một socket, không phải do
cài đặt h2 của EdgeGate. RTT 20 ms che phần đó gần hết, còn 1.14–1.16x.

### Cái giá 2: trần của flow control

Một stream, response 8 MiB. Client tự đặt window: 65 535 byte (mặc định của RFC) hoặc 8 MiB. Đo ở RTT 0
rồi ở RTT 40 ms:

```bash
make h2lab-flow                         # RTT 0
sudo tc qdisc add dev lo root netem delay 20ms && make h2lab-flow; sudo tc qdisc del dev lo root
```

```console
flow: lượt 1 window    65535: 8388608 byte trong 5.562s = 1.51 MB/s
flow: lượt 1 window  8388608: 8388608 byte trong 411ms = 20.43 MB/s
flow: lượt 2 window    65535: 8388608 byte trong 5.327s = 1.57 MB/s
flow: lượt 2 window  8388608: 8388608 byte trong 371ms = 22.61 MB/s
flow: lượt 3 window    65535: 8388608 byte trong 5.371s = 1.56 MB/s
flow: lượt 3 window  8388608: 8388608 byte trong 381ms = 22.02 MB/s
```

(Nguồn: `bench/p10-flow-rtt40.txt`. `ping` lúc đó cho RTT trung bình 40.307 ms, ghi trong
`diary/phase10.md`.)

Window mặc định: **1.51–1.57 MB/s**, bất kể đường truyền nhanh tới đâu. Window 8 MiB nhanh hơn
13.5–14.4 lần. Ở RTT 0 thì gần như không thấy: hai window chỉ chênh 1.50–1.78 lần
(`bench/p10-flow-rtt0.txt`). Trên máy dev bạn sẽ không bao giờ thấy trần này.

## Bên trong: một hàng đợi byte cho tất cả

### Mất một gói, mọi stream đứng

HTTP/2 multiplex ở tầng **HTTP**. Bên dưới vẫn là **một** connection TCP, và TCP chỉ giao byte cho ứng
dụng theo đúng thứ tự:

```text
h2, một connection:   [s1][s7][s3][s1][ ✗ mất ][s9][s3][s7][s1] ...
                                       ▲
                       kernel giữ mọi byte phía sau chỗ mất,
                       không stream nào đọc tiếp được cho tới khi gói được gửi lại

h1, 32 connection:    conn 1  [....][....]
                      conn 2  [....][ ✗ ]   ◄── chỉ request trên conn 2 phải chờ
                      conn 3  [....][....]
                      ...
```

Với h1, 2 % gói mất chỉ trúng vài phần trăm request. Những request đó chịu một lần chờ gửi lại (RTO),
và chính chúng tạo ra đuôi: p99 của h1 khoảng 293 ms, cỡ RTO tối thiểu 200 ms cộng RTT
(`diary/phase10.md`). Phần lớn request còn lại không bị gì, nên trung vị vẫn 42 ms.

Với h2, cùng một lần mất gói bắt **cả 32 stream** đang bay chờ chung một RTO. Gần như request nào cũng
dính, nên trung vị dịch lên. Đuôi thì vẫn là một RTO, nên p99 gần như giữ nguyên. HOL ở tầng TCP không
tạo ra đuôi mới. Nó **dời** giá của mất gói từ vài request sang tất cả.

Không cách nào sửa chuyện này trong một cài đặt HTTP/2. Muốn mỗi stream mất gói độc lập thì stream phải
nằm ở tầng transport. Đó chính là lý do QUIC (và HTTP/3) tồn tại.

### Flow control: một window mỗi RTT

TCP đã có flow control, vì sao h2 cần thêm? Vì window của TCP là của **cả connection**. Proxy có một
upstream chậm phải chặn được một stream mà không chặn 99 stream kia (RFC 9113 §5.2.2 lấy đúng ví dụ
proxy). Nên h2 có window riêng cho từng stream, cộng một window cho connection.

Bên gửi trong EdgeGate (`internal/h2/conn.go`, `Stream.WriteData`, bỏ bớt phần timeout):

```go
for {
	c.mu.Lock()
	for !s.reset && len(p) > 0 && (s.sendWindow <= 0 || c.sendWindow <= 0) {
		c.cond.Wait() // hết window: ngủ tới khi có WINDOW_UPDATE
	}
	// ...
	n := int64(len(p))
	n = min(n, s.sendWindow, c.sendWindow, int64(c.peerFrame))
	s.sendWindow -= n
	c.sendWindow -= n
	c.mu.Unlock()
	chunk := p[:n]
	p = p[n:]
	// ... ghi một frame DATA, rồi lặp lại cho tới hết p
}
```

Hết window thì goroutine ngủ trên `cond`, chờ client gửi WINDOW_UPDATE. WINDOW_UPDATE đó cần nửa RTT để
tới. Cứ thế, mỗi RTT chỉ chạy được **một window**: 65 535 byte / 40.3 ms = **1.63 MB/s** theo lý
thuyết, đo được 1.51–1.57, tức 93–96 % trần (`diary/phase10.md`).

Chiều ngược lại, khi client upload, EdgeGate trả window trong `Stream.Read`, tức lúc handler **đọc**
body, không phải lúc nhận frame. Handler chậm thì chỉ client của đúng stream đó bị chặn.

## Mang về dùng

1. **HTTP/2 dời giá của mất gói từ vài request sang tất cả.** Trên mạng sạch, một connection h2 rất tốt.
   Trên mạng mất gói, 32 connection h1 có trung vị tốt hơn 2.65–2.93 lần trong thí nghiệm này. Nếu client
   của bạn ở mạng di động yếu, đo trước khi tin. Câu trả lời dài hạn là HTTP/3 trên QUIC.
2. **Nới window khi đường truyền có RTT lớn.** Mặc định 65 535 byte là trần window/RTT, tức khoảng
   1.6 MB/s ở RTT 40 ms. Proxy h2 nên đặt `SETTINGS_INITIAL_WINDOW_SIZE` và window connection theo
   băng thông nhân độ trễ, không để mặc định.
3. **Nhìn cả trung vị, không chỉ đuôi.** Dự đoán "h2 tệ hơn ở p99" sai. Cái tệ nằm ở p50, chỗ mà
   dashboard chỉ theo dõi p99 sẽ không bao giờ báo.

Giá CPU của h2 (một goroutine đọc, mỗi stream một goroutine) đã nằm ở [bài 12](12-reverseproxy.md).

---

Số đo gốc: `bench/p10-hol.txt`, `bench/p10-hpack.txt`, `bench/p10-tcphol-*.txt`, `bench/p10-flow-*.txt` ·
code: [`internal/h2/conn.go`](../internal/h2/conn.go) (`WriteData`, `Stream.Read`),
[`cmd/h2lab/main.go`](../cmd/h2lab/main.go) · nhật ký: [`diary/phase10.md`](../diary/phase10.md) (G1, G3, G4, G5).

**Bài tiếp theo:** [Bài 14: Qua hết bài kiểm tra chuẩn vẫn bị đánh sập](14-h2spec.md)
