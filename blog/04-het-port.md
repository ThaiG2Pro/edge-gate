# Bài 4 — Hết port mà không báo lỗi? (tôi đã sai)

> Series [Mở nắp reverse proxy](README.md) · bài 4/16 · cần đọc trước: [bài 3](03-keep-alive.md)

Bài này là một lời đính chính. README của chính repo này từng có một dòng trong bảng "những điều
làm tôi bất ngờ":

```text
| Ephemeral port exhaustion shows no errors. Throughput just drops. | **1502 → 455** conn/s |
```

Nghĩa là: proxy không dùng connection pool, mỗi request mở một connection mới tới upstream, và
khi cạn ephemeral port thì không có lỗi nào cả. Chỉ có thông lượng tụt dần. Nhật ký phase 0 còn
viết thêm rằng đây là "hình dạng khó chẩn đoán nhất trong vận hành thật: không log lỗi, không
alert".

Câu đó đúng trên laptop của tôi. Nó sai trên một máy Linux thật. Bài này kể cả hai lần đo, cái gì
đã lật kết luận, và phần nào tôi vẫn chưa giải thích được.

## Thí nghiệm

`netlab -exp limits` làm một việc đơn giản: dial, gửi một request, đọc response, đóng, rồi dial
tiếp, liên tục trong một khoảng thời gian. Không pool. Nó đếm connection thành công, connection
lỗi, và in số socket TIME_WAIT (`tw`) trước và sau.

```bash
make netlab-limits   # tự lấy IP không phải loopback của máy, chạy 40 s
# hoặc tự chọn IP và thời lượng:
go run ./cmd/netlab -exp limits -duration 20s -bufio=false -addr <ip-máy>:9200
```

Phải dùng IP thật của máy, không dùng `127.0.0.1`. Lý do ở mục "Bẫy loopback" bên dưới.

### Lần đo đầu: WSL2, không một lỗi nào

Bốn lần chạy liên tiếp trên laptop WSL2. Lần đầu qua `127.0.0.1`, ba lần sau qua IP của `eth0`,
để TIME_WAIT tích luỹ dần:

```console
$ go run ./cmd/netlab -exp limits -duration 20s -bufio=false        # 127.0.0.1
  sockstat TRƯỚC       : TCP: inuse 30 orphan 0 tw 21155 alloc 70 mem 540
  sockstat SAU         : TCP: inuse 32 orphan 1 tw 33957 alloc 73 mem 540
  connection thành công: 30033 trong 20s = 1502 conn/s
  connection lỗi       : 0
$ go run ./cmd/netlab -exp limits -duration 25s -addr 172.19.62.65:9111
  connection thành công: 18440 trong 25.002s = 738 conn/s
  connection lỗi       : 0
$ go run ./cmd/netlab -exp limits -duration 70s -addr 172.19.62.65:9112
  connection thành công: 42087 trong 1m10.003s = 601 conn/s
  connection lỗi       : 0
$ go run ./cmd/netlab -exp limits -duration 40s -addr 172.19.62.65:9113
  sockstat TRƯỚC       : TCP: inuse 52 orphan 0 tw 23592 alloc 92 mem 0
  sockstat SAU         : TCP: inuse 50 orphan 0 tw 32759 alloc 90 mem 0
  connection thành công: 18191 trong 40.007s = 455 conn/s
  connection lỗi       : 0
```

(Nguồn: `bench/p0-limits-rtt0.txt`, `bench/p0-limits-eth0.txt`, `bench/p0-limits-eth0-70s.txt`,
`bench/p0-limits-eth0-saturated.txt`. Máy WSL2, nên chỉ tỉ số giữa các lần là đáng tin, số tuyệt
đối thì không.)

Thông lượng tụt từ 1502 xuống 455 conn/s, và cột lỗi luôn bằng 0. Một lần chạy thêm có đo CPU:
614 conn/s, CPU toàn máy 29.5% trên 6 core (`bench/p0-limits-eth0-cpu.txt`). Vậy không phải CPU.
Từ đó tôi viết kết luận: *trần OS không báo bằng lỗi, nó báo bằng thông lượng tụt dần*.

Nhìn lại, kết luận đó có hai vết nứt mà chính nhật ký đã ghi. Một: con số 1502 đo qua loopback,
ba con số sau đo qua `eth0`, nên chuỗi `1502 → 455` trộn hai đường mạng khác nhau. Hai: vòng dial
là tuần tự, nên chưa tách được "trần ở không gian port" khỏi "trần ở chính client một luồng" (nợ
`P0-4`).

### Lần đo thứ hai: Linux thuần, lỗi rõ ràng

Một tháng sau, cùng lệnh, chạy trên một máy Linux thuần (CachyOS, kernel 7.2.8, 15 luồng), qua IP
LAN của máy. Ba lần liên tiếp:

```console
### lần 1 — 10:27:49
  sockstat TRƯỚC       : TCP: inuse 40 orphan 0 tw 61127 alloc 148 mem 1364
  sockstat SAU         : TCP: inuse 40 orphan 0 tw 82857 alloc 148 mem 596
  connection thành công: 21782 trong 20.002s = 1089 conn/s
  connection lỗi       : 0
### lần 2 — 10:28:09
  sockstat SAU         : TCP: inuse 40 orphan 0 tw 88791 alloc 148 mem 596
  connection thành công: 6447 trong 16.694s = 386 conn/s
  connection lỗi       : 101
  lỗi đầu tiên         : dial tcp 192.168.1.52:9200: connect: cannot assign requested address
### lần 3 — 10:28:25
  connection thành công: 0 trong 1.423s = 0 conn/s
  connection lỗi       : 101
  lỗi đầu tiên         : dial tcp 192.168.1.52:9200: connect: cannot assign requested address
```

(Nguồn: `bench/baseline/thai-computer-20261004/p0-limits.txt`, Linux thuần (CachyOS).)

Gom lại:

| | WSL2 | Linux thuần (CachyOS) |
|---|---|---|
| conn/s lần đầu → lần cuối có kết quả | 1502 → 738 → 601 → 455 | 1089 → 386 → 0 |
| connection lỗi | 0 ở cả bốn lần | 0, rồi 101, rồi 101 |
| lỗi | không có | `cannot assign requested address` (`EADDRNOTAVAIL`) |
| `tw` lớn nhất thấy được | 33957 (loopback), 32759 (`eth0`) | 88791 |

Trên Linux, thông lượng cũng tụt, nhưng rồi kernel nói thẳng: hết địa chỉ để gán. Lần thứ ba
không dial được connection nào, và dừng sau 1.423 giây. Kết luận "không báo lỗi" không sống sót.

Một chi tiết để bạn đừng đọc sai con số 101. Đó không phải tổng số lỗi. Vòng lặp tự dừng khi số
lỗi vượt 100 (`cmd/netlab/exp_limits.go`):

```go
cc, err := dial(addr, true)
if err != nil {
	atomic.AddInt64(&failed, 1)
	// ...
	if atomic.LoadInt64(&failed) > 100 {
		return
	}
	continue
}
// ...
cc.close() // client chủ động đóng => client giữ TIME_WAIT
```

Vậy 101 nghĩa là "đã hỏng, và đủ nhiều để dừng đo".

## Bên trong: ai đóng trước thì giữ port

Mỗi connection TCP là một bộ bốn: IP nguồn, port nguồn, IP đích, port đích. Khi proxy dial tới
một upstream cố định, ba phần đã cố định. Chỉ còn port nguồn thay đổi, và port nguồn lấy từ dải
ephemeral. Cả hai máy dùng cùng dải:

```text
net.ipv4.ip_local_port_range = 32768	60999     # = 28232 port
```

(Nguồn: `diary/phase0.md`; cùng giá trị trong `bench/baseline/thai-computer-20261004/env.txt`.)

Bên nào gọi `close()` trước thì socket của bên đó vào trạng thái TIME_WAIT và giữ bộ bốn đó thêm
một lúc. Trong `netlab`, client đóng trước, nên client giữ. Trong một proxy không pool, proxy là
client của upstream. Nhật ký phase 5 đo đúng chuyện này bằng `make poollab`, 2000 request qua
proxy:

```console
  [qua proxy, pool-off (dial mỗi request)] Δ TIME_WAIT: phía proxy (dport=36557) +1964 · phía upstream (sport=36557) +0
  [qua proxy, pool-on] Δ TIME_WAIT: phía proxy (dport=36557) +0 · phía upstream (sport=36557) +0
```

(Nguồn: `bench/p5-poollab-rtt0.txt`, WSL2. Linux thuần (CachyOS) cho +2020 rồi +0, trong
`bench/baseline/thai-computer-20261004/p5-poollab-rtt0.txt`.)

TIME_WAIT nằm ở proxy, không ở upstream. Bật pool thì con số về 0. Nhật ký phase 5 tính ra trần
cứng của trường hợp này: khoảng 28k port chia cho 60 giây TIME_WAIT, cỡ 470 request/s cho một cặp
proxy và upstream, bất kể CPU còn rỗi bao nhiêu.

Còn chuyện thông lượng tụt trước khi hỏng, nhật ký phase 0 giải thích bằng giả thuyết: khi
TIME_WAIT gần đầy, kernel phải tìm lâu hơn mới ra một bộ bốn còn rỗi, nên mỗi `Dial` đắt dần.

### Bẫy loopback

Vì sao phải dùng IP thật? Cả hai máy đều có dòng này:

```text
net.ipv4.tcp_tw_reuse = 2
```

Giá trị `2` nghĩa là chỉ bật cho loopback. Trên `127.0.0.1`, kernel tái dùng socket TIME_WAIT
ngay, nên bạn không bao giờ cạn port ở đó. `make netlab-limits` vì vậy tự tìm IP không phải
loopback.

### Phần tôi chưa giải thích được

Lời giải thích dễ nghĩ tới nhất là `tcp_tw_reuse`. Nó không đứng được: máy Linux thuần cũng có
`tcp_tw_reuse = 2`, và cả hai lần đo đều đi qua IP không phải loopback. Vậy `tw_reuse` không phải
chỗ khác nhau giữa hai máy.

Repo hiện chưa có lời giải thích vì sao WSL2 không trả `EADDRNOTAVAIL`. Có một điểm cần nói thẳng:
hai lần đo không đứng ở cùng mức TIME_WAIT. Trên WSL2 qua `eth0`, `tw` lớn nhất tôi thấy là 32759. Trên Linux,
lần đầu đã bắt đầu ở 61127, và lần đầu đó cũng 0 lỗi, giống hệt WSL2. Lỗi chỉ xuất hiện ở lần thứ
hai, khi `tw` lên tới 88791. Có thể WSL2 im lặng thật. Cũng có thể tôi chưa bao giờ đẩy WSL2 đủ xa.
Hai khả năng đó chưa được tách ra.

Nợ `P0-4` (dial song song để tách trần client khỏi trần port) được ghi là đã trả trên Linux thuần
trong `docs/debts.md`, kèm kết luận rằng khi đủ nhiều dialer thì cạn cả dải port. Output thô của lượt
đo đó chưa có trong repo, nên bài này không dùng số của nó.

### Tôi đã sai ở đâu

Không phải sai ở con số. 1502 → 455 và 0 lỗi là output thật của máy tôi. Sai ở chỗ biến một quan
sát trên một môi trường thành một câu nói chung về "trần của OS". Nhật ký phase 0 còn ghi sẵn dự
đoán rằng trên Linux thuần trần sẽ "rõ ràng hơn, có thể ra hẳn `EADDRNOTAVAIL` nếu
`tcp_tw_reuse=0`". Lỗi ra thật, dù máy Linux vẫn để `tcp_tw_reuse = 2`. Vậy mà README vẫn in câu
kết luận cũ lên bảng đầu tiên. Lần sửa nằm ở commit `d104072`.

## Mang về dùng

1. **Đọc kỹ lỗi dial.** `cannot assign requested address` là cạn port. `too many open files` là
   cạn file descriptor. Hai thứ khác nhau, sửa khác nhau. Và đừng giả định trần nào cũng sẽ báo
   lỗi: trước khi hỏng, thông lượng đã tụt mà CPU vẫn rỗi.
2. **Proxy phải có pool tới upstream.** Không pool thì mỗi request tiêu một port và để lại một
   TIME_WAIT ở chính proxy. Muốn biết mình có đang tiêu port không, đếm Δ TIME_WAIT theo port của
   upstream trước và sau một đợt tải.
3. **Môi trường đo cũng là một biến.** Kết luận chỉ đo trên một môi trường thì ghi rõ môi trường
   đó ngay cạnh con số. WSL2, loopback, `tcp_tw_reuse` đều nằm sẵn trong output của
   `make envcap`. Tôi đã in chúng ra, và đã không đọc.

---

Số đo gốc: [`bench/p0-limits-*.txt`](../bench/), [`bench/baseline/thai-computer-20261004/p0-limits.txt`](../bench/baseline/thai-computer-20261004/p0-limits.txt),
[`bench/p5-poollab-rtt0.txt`](../bench/p5-poollab-rtt0.txt). Nhật ký: [`diary/phase0.md`](../diary/phase0.md)
mục G6, [`diary/phase5.md`](../diary/phase5.md) mục 5.

**Bài tiếp theo:** [Bài 5: Hai server đọc một request ra hai cách](05-smuggling.md)
