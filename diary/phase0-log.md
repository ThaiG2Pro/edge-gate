# Phase 0 — log thô

> Đây **không** phải bản biên tập. [`phase0.md`](phase0.md) là kết quả đã sắp lại cho người
> khác đọc; file này là **đường đi thật**, theo đúng thứ tự đã xảy ra, kể cả ngõ cụt, code viết
> sai, và những chỗ mất thời gian vì công cụ chứ không vì bài toán.
>
> Lý do phải có hai file: bản biên tập luôn khiến mọi thứ trông như một đường thẳng, và đọc lại
> sáu tháng sau sẽ tưởng mình đã biết trước. File này giữ lại việc **thứ tự phát hiện là ngược
> với thứ tự trình bày** — G7 tìm ra trong lúc đo G1, và nguyên nhân của G2 chỉ hiểu được sau
> khi đã trả nợ P0-1.

Ngày: 2026-09-03. Máy: WSL2 6.6.87.2 / i5-1235U / 6 core / 12GB. `go1.26.2`.

---

## Phần 0 — Đăng ký giả thuyết TRƯỚC khi đo

Viết mục này khi `cmd/netlab` **vừa build xong** và **chưa chạy lệnh đo nào**. Không có con số
nào tồn tại ở thời điểm viết. Đây là điều kiện để bảng "giả thuyết sai" có giá trị — đo trước
rồi viết nhận định sau thì sẽ "giải thích được" mọi con số vừa thấy.

| # | Giả thuyết | Tỉ số kỳ vọng | Lý lẽ khi đăng ký |
|---|---|---|---|
| G1 | 2 lần `Write` chậm hơn 1 lần | 1.5-2x | 2 syscall thay vì 1, và có thể thành 2 packet |
| G2 | `Dial` vs reuse, **RTT 0** | 1.0-1.3x | Handshake tốn 1 RTT; loopback không có RTT ⇒ dial gần như miễn phí. **Tôi tự dựng cái bẫy này cho mình** |
| G3 | `Dial` vs reuse, **RTT 20ms** | > 15x | Mỗi request trả thêm 1 RTT handshake trên nền một request vài chục µs |
| G4 | p99 closed-loop < p99 open-loop | > 3x | Closed-loop tự điều tiết theo server nên không tạo được backlog |
| G5 | RSS/conn ở 10k conn vs 2KB stack | > 4x | `bufio.Reader`+`Writer` 4KB mỗi cái lớn hơn stack 2KB |
| G6 | Không pool cạn **port** trước cạn CPU | port trước | 28232 port / TIME_WAIT 60s ≈ 470 conn/s bền vững, thấp hơn nhiều so với trần CPU |
| G7 | Nagle + delayed ACK spike | 40ms ± 5 | Delayed ACK timeout của Linux là 40ms |

G8 được đăng ký muộn hơn, sau khi trả nợ P0-1 — ghi ở [§13](#13-g8-ngõ-cụt-nhưng-lộ-ra-thứ-lớn-hơn).

---

## 1. Đọc lại template và Makefile trước khi viết code

Không có gì đáng ghi, ngoài một chuyện: `Makefile` đã có sẵn target `netlab` do tôi viết ở lượt
trước, và nó **không** có `-bufio=false`. Chi tiết này sẽ trả giá ở §5. Lúc đọc, tôi không thấy
gì bất thường — flag `bufio` lúc đó trong đầu tôi là "biến của G5", không phải "thứ có thể phá
G1".

## 2. Quyết định thiết kế: tham số thí nghiệm nằm trong request, không trong flag của server

Đây là quyết định duy nhất trong phase này mà tôi cho là *kiến trúc*, nên ghi lại lý lẽ.

Cách tự nhiên hơn là để server có flag `-two-writes`, `-svc`, `-nodelay`. Nhược điểm: hành vi
server do flag server quyết định ⇒ bộ đo chỉ chạy được khi client và server **cùng process** ⇒
bị khoá vĩnh viễn trên loopback ⇒ **không bao giờ đo được G3**.

Nên wire format thành:

```
request : uint32 len | uint8 flags | uint32 svcMicros | uint32 respSize | payload
response: uint32 len | payload
```

`flags` bit 0 = server ghi 2 lần `Write`; bit 1 = server `SetNoDelay(false)`. Nhờ vậy có
`-role server` / `-role client`, và mọi thí nghiệm chạy y nguyên qua hai máy.

Về sau nhìn lại: đây là quyết định đúng nhất của phase, nhưng **không phải vì lý do tôi nghĩ
lúc đó**. Tôi làm nó để tránh phải viết lại bộ đo. Giá trị thật là nó cho một đường thoát khỏi
loopback — và loopback hoá ra che **ba** thứ khác nhau (RTT, `tcp_tw_reuse`, và sự sạch sẽ giả
tạo của netem), không chỉ một.

## 3. Code viết sai lần đầu: `exp_nagle.go`

Viết vòng lặp đo bằng một mớ biến nháp còn sót:

```go
var lat = func() (d int64) { return 0 }
_ = lat
var l = func() (x int64) { return 0 }
_ = l
var dur, e = cc.roundtrip(req), error(nil)
if split { dur, e = cc.roundtripSplit(req) }
```

Nó **compile được**, `gofmt` sạch, `go vet` không nói gì — nhưng dòng `var dur, e = cc.roundtrip(req)`
chạy một roundtrip **thừa** mỗi vòng lặp ở nhánh `split`. Nếu để nguyên thì mỗi phép đo Nagle
sẽ gồm 2 roundtrip và con số 44ms sẽ thành ~88ms, và tôi sẽ đi tìm lý do "vì sao gấp đôi 40ms"
trong `man 7 tcp` thay vì trong code của mình.

Bắt được vì đọc lại trước khi build, không phải vì test — **`cmd/netlab` không có test nào**
(nợ `P0-5`). Đây là ngõ cụt suýt xảy ra, và lý do duy nhất nó không xảy ra là may.

## 4. Ma sát công cụ, không phải bài toán

Ghi lại để lần sau không mất thời gian:

- `Makefile` cần **tab thật**. Viết bằng 4 space rồi `sed -i 's/^    /\t/'` rồi `make -n` kiểm.
- Dòng recipe cần `$` của shell phải viết `$$`: `$$(ulimit -n)`, `-run '^$$'`.
- **`| tail -12` giữ toàn bộ output tới khi pipe đóng.** Lệnh chạy nền, tôi `cat` file output
  thấy **trống**, và kết luận sai là "lệnh treo". Thật ra `tail` đang buffer. Mất ~5 phút vì
  chuyện này. Sau đó chuyển sang `| tee file` rồi mới `tail`.

## 5. Lần đo đầu tiên của G1 — và bộ đo sai ngay từ lệnh đầu

```console
$ ulimit -n 65536 && go run ./cmd/netlab -exp syscall -n 5000 -tag "wsl2-rtt0"
1 Write (resp 1024B)               -            5000    17.9µs    12.0µs    34.4µs    97.7µs    1.31ms      0
2 Write (resp 1024B)               -            5000    16.4µs    12.2µs    18.6µs    75.4µs   736.8µs      0
2 Write + Nagle server (resp 1024B) -            5000    22.1µs    12.4µs    42.3µs   119.1µs   569.0µs      0
  [G1] p50 2-Write / p50 1-Write                        1.02x   (kỳ vọng 1.5-2x)
  [G1] p99 2-Write / p99 1-Write                        0.77x   (kỳ vọng 1.5-2x)
```

Cái làm tôi dừng lại không phải 1.02x — mà là **p99 = 0.77x**. Ghi 2 lần *nhanh hơn* ghi 1 lần
là chuyện không có cách nào đúng. Một tỉ số < 1 ở đây là chữ ký của bộ đo sai, không phải của
một kết quả bất ngờ.

Áp thủ tục trong SKILL.md: *giả định bộ đo sai trước, đừng giả định cái máy lạ*. Kiểm lại đường
ghi của server và thấy `s.useBufio` đang `true` ⇒ `w = bufio.NewWriter(c)` ⇒ hai lần `Write` vào
buffer userspace, gộp thành **một** syscall lúc `Flush`.

Đây đúng là bản mạng của cái bẫy page-cache trong `db/diary/phase0.md`: tưởng đang đo syscall,
thật ra đang đo `memcpy`.

## 6. Ngõ cụt lớn nhất: "lệnh bị treo" — và nó là số đo của G7

Chạy lại với `-bufio=false`. Lệnh **không xong trong 120 giây**, bị đẩy sang chạy nền.

```console
$ cat .../tasks/bnrkl1l9j.output
(trống)

$ pgrep -af netlab
297269 .../netlab -exp syscall -n 5000 -bufio=false -tag wsl2-rtt0-NOBUFIO
```

Trống + process còn sống ⇒ tôi kết luận **deadlock**, và bắt đầu đọc lại `readFrame` và
`writeResponse` để tìm chỗ hai bên cùng chờ nhau.

**Cả hai suy luận đều sai:**
1. Output trống là vì `tail` buffer (§4), không phải vì process không in gì.
2. Process không deadlock. Nó đang chạy **đúng 5000 request × 44ms** = ~220 giây.

Biến thể thứ ba của G1 (`flagTwoWrites|flagNagleOn`) chính là hình dạng `write-write-read` với
Nagle bật ở server. Tức là **G7 đang tự chạy bên trong thí nghiệm G1**, và nó chỉ hiện ra được
vì tôi vừa tắt `bufio` để sửa §5.

Một dòng `bufio` che **hai** sự thật khác nhau cùng lúc: chi phí syscall, và spike 44ms.

Kết quả khi nó chạy xong:

```console
1 Write (resp 1024B)               -            5000    13.5µs    10.9µs    14.5µs    55.0µs   550.6µs      0
2 Write (resp 1024B)               -            5000    17.6µs    15.3µs    17.9µs    58.6µs   671.9µs      0
2 Write + Nagle server (resp 1024B) -            5000   44.54ms   44.01ms   47.92ms   48.21ms   61.14ms      0
  [G1] p50 2-Write / p50 1-Write                        1.41x
  [G1] p99 2-Write / p99 1-Write                        1.07x
```

Sửa `nagleN = n/50` và **ghi con số 50 kèm lý do vào comment** — vì con số đó là một kết quả đo
(44ms/request), không phải một lựa chọn tuỳ ý. Người đọc sau sẽ đặt lại `n=5000` nếu không có
comment đó.

**Việc tôi làm trong lúc chờ 220 giây:** viết `scripts/envcap.sh`. Tình cờ mà đúng — nó in
`tcp_tw_reuse = 2` ngay từ lần chạy đầu, tức là **manh mối của §10 đã nằm trên màn hình từ
trước khi tôi biết mình cần nó**, và tôi đã không đọc.

## 7. G2: bẫy tôi tự dựng, và nó không nổ

```console
$ go run ./cmd/netlab -exp rtt -n 2000 -bufio=false -tag rtt0
reuse 1 connection                 -            2000    32.7µs    23.5µs    46.6µs   188.9µs    1.09ms      0
dial mới mỗi request               -            2000   986.0µs   861.7µs    1.36ms    3.84ms   30.76ms      0
  [G2/G3] p50 dial / p50 reuse                            36.69x   (kỳ vọng 1.0-1.3x)
```

Tôi đã viết sẵn cả đoạn text in ra cuối thí nghiệm để tự dạy mình rằng "1.x không có nghĩa pool
vô dụng". Đoạn text đó **không được dùng**, vì tỉ số ra 36.69x.

Lúc này tôi ghi vào nháp một cách hiểu **sai**: "WSL2 dial đắt bất thường, vậy trên Linux thuần
sẽ về ~1.x như dự đoán". Cách hiểu này sống sót suốt phần còn lại của buổi và chỉ bị lật ở §12
sau khi có số của G3. Ghi lại vì đó là điều bản biên tập không thể hiện: **tôi mang một kết luận
sai đi qua 6 thí nghiệm nữa mới phát hiện.**

## 8. Thử sudo, thất bại, và một ngõ cụt tôi đã cân nhắc rồi bỏ

```console
$ sudo -n true
sudo: a password is required
```

Cân nhắc phương án không cần root: viết một **relay trong userspace** tự chèn delay 20ms mỗi
chiều, dùng làm "RTT giả".

**Bỏ, và lý do đáng ghi lại:** một relay userspace `accept` ngay lập tức, nên `connect()` của
client hoàn tất **không** có delay. Mà toàn bộ điểm của G3 là *handshake phải trả thêm 1 RTT*.
Relay không thể làm chậm SYN/SYN-ACK. Nó sẽ cho ra một con số trông hợp lý và **sai về bản chất**
— đúng loại bằng chứng mà SKILL.md gọi là đồ trang trí. Thà nợ `P0-1` còn hơn có một số đo giả.

## 9. G4, G5: hai thí nghiệm chạy đúng ngay lần đầu

Không có ngõ cụt. Chi tiết duy nhất đáng ghi: capacity server tôi **tính** là 1000 rps
(`workers=1`, `svc=1ms`) nhưng cả ba dụng cụ chỉ đạt ~714-754. Không phải bug — `time.Sleep(1ms)`
ngủ ~1.3ms. Nhưng tôi đã in chữ "capacity ≈ 1000 rps" lên tiêu đề bảng, tức là **bảng của tôi
đang nói một con số chưa được đo**. Ghi nợ `P0-2`.

`-exp mem` chạy `-conns 1000` rồi `10000` rồi `10000 -bufio=false`. Hiệu hai lần cuối cho
10.31 KB/conn là giá riêng của `bufio` — con số này không có trong kế hoạch, nó xuất hiện chỉ
vì `bufio` vốn đã là một flag sẵn có cho G5.

## 10. G6: ba lần đo, ba lần hiểu lại

**Lần 1, qua `127.0.0.1`:** 30033 connection trong 20s, **0 lỗi**, `tw` = 33957.

Hai chuyện vô lý cùng lúc:
- `tw` 33957 > 28232 port. → Giải thích được: TIME_WAIT tính theo **4-tuple**, mỗi lần chạy
  netlab dùng port server khác nhau, nên cùng local port vẫn dùng lại được. `tw` **không** so
  trực tiếp được với số port. Đây là chỗ tôi suýt kết luận "sockstat sai".
- 0 lỗi dù đã tiêu hết không gian port. → Không giải thích được, phải đi đọc lại môi trường.

```console
$ cat /proc/sys/net/ipv4/tcp_tw_reuse
2
```

`2` = *chỉ bật cho loopback*. Kernel tái dùng socket TIME_WAIT ngay ⇒ **port exhaustion không
thể tái hiện trên `127.0.0.1`**. Con số này `envcap.sh` đã in ở §6.

**Lần 2, qua IP của `eth0`** (`ip -4 -o addr show scope global` → 172.19.62.65, không thuộc
127/8): 738 conn/s. Rồi 70s → 601 conn/s, `tw` 26936. Rồi 40s nữa → 455 conn/s, `tw` 32759.
**Vẫn 0 lỗi.**

Đây là chỗ tôi phải sửa lại giả thuyết G6 chứ không phải sửa lại phép đo: tôi chờ một
`EADDRNOTAVAIL`. Thực tế trần này **không báo bằng exception, nó báo bằng thông lượng tụt đơn
điệu** 1502 → 738 → 601 → 455 conn/s.

**Lần 3:** nhận ra câu "trần không ở CPU" của tôi **chưa có bằng chứng nào**. Thêm `cpuBusy()`
đọc `/proc/stat` vào `expLimits` rồi chạy lại → **29.5% / 6 core**. Đủ để loại CPU khỏi danh
sách nghi phạm, **không** đủ để nói "CPU rỗi".

Và một lỗ hổng tôi tự tìm ra nhưng **không sửa được trong phase này**: vòng dial của tôi là
**tuần tự**, nên nó không phân biệt được "trần ở không gian port" với "trần ở chính vòng lặp một
luồng của client". Ghi nợ `P0-4`, và **không** viết dòng này vào bảng tỉ số như kết luận đã
chứng minh.

## 11. Viết `phase0.md`, rồi bộ đo sai lần thứ ba

Sau khi đã viết xong bản biên tập, chạy `make netlab` (tức `-all`) như phép kiểm tra cuối:

```console
--- G5: bộ nhớ của 500 connection rỗi (bufio=false)
  RSS/conn  : -7.99 KB
```

Số **âm**. Không cần nghĩ gì về mạng — âm ở đây chỉ có một nghĩa. Các thí nghiệm chạy trước
(`omission` mở 512 conn) đẩy RSS lên, rồi scavenger của Go **trả bộ nhớ về OS** trong lúc
`expMem` đang chạy ⇒ mốc nền cao hơn mức thật.

Bài học: **RSS là con số toàn process và không đơn điệu**, nên chỉ có nghĩa trong process sạch.
Sửa hai chỗ: `expMem` **từ chối** in kết quả khi `after <= base` (thà không có số hơn có số
sai), và `-all` **tự fork process mới** cho G5.

Đáng chú ý: lỗi này chỉ hiện ra vì tôi chạy lệnh mà `README` dặn người khác chạy. Các số G5 trong
`phase0.md` vẫn đúng vì chúng vốn được chạy `-exp mem` một mình — tức là **bản biên tập đúng
nhờ tình cờ**, không nhờ bộ đo đúng.

Đến đây đếm được: `bufio` (§5), `tcp_tw_reuse` (§10), RSS không đơn điệu (§11). **Ba lần bộ đo
sai, không lần nào vì thiếu kiến thức về mạng.**

---

## 12. Trả nợ P0-1 — và lật lại cách hiểu đã mang từ §7

`modinfo sch_netem` có, `CONFIG_NET_SCH_NETEM=m`, chưa nạp ⇒ `tc` sẽ tự nạp ⇒ sudo sẽ chạy được.
Đóng gói thành `scripts/pay-P0-1.sh`, `trap ... EXIT INT TERM` để tháo qdisc trong mọi đường ra.

**Quyết định đo hai cấu hình delay thay vì một.** Lý lẽ khi viết script (trước khi có số): trên
`lo`, gói đi và gói hồi đều qua qdisc của `lo`, nên `delay X` có thể cho RTT `2X`. Nếu đúng thì
mọi nhãn "RTT 20ms" của tôi sai 2x. Cấu hình thứ hai không dùng để đo G3 — dùng để **chứng minh**
quan hệ nhân đôi thay vì tin nó.

```
delay 10ms  ->  RTT ĐO ĐƯỢC = 20.157 ms   ->  reuse 20.43ms / dial 40.90ms  ->  2.00x
delay 20ms  ->  RTT ĐO ĐƯỢC = 40.201 ms   ->  reuse 40.49ms / dial 80.96ms  ->  2.00x
```

Hai kết quả, và cái thứ hai quan trọng hơn:

**(a)** Hệ số 2 đúng ở cả hai điểm ⇒ netem trên `lo` áp delay **hai lần** cho một round-trip.
`Makefile` `rtt-up` của tôi đang đặt `delay 20ms` rồi ghi nhãn "RTT 20ms". Nếu không có `ping`
kiểm thì **mọi diary từ phase 5 trở đi đã sai nhãn 2x** — và sai theo hướng không ai phát hiện
được, vì mọi tỉ số vẫn đẹp.

**(b)** G3 = **2.00x**, kỳ vọng >15x. Nhưng 2.00x **trùng khít ở hai RTT khác nhau** thì không
phải một số đo của môi trường, nó là **hằng số tiệm cận**:

```
reuse = xử lý + 1 RTT          (request + response)
dial  = dựng socket + 2 RTT    (handshake, RỒI request + response)
tỉ số -> 2.0 khi RTT lớn dần
```

Kiểm: mô hình cho 2.015 ở RTT 20157µs và 2.007 ở RTT 40201µs. Đo được 2.00 và 2.00.

**Và đây là chỗ cách hiểu sai từ §7 bị lật.** Suốt buổi tôi tin "36.69x là artifact của WSL2,
trên Linux thuần sẽ về ~1.x như G2 dự đoán". Sai. Tỉ số **không** về 1.x, nó **đi lên 2.0 rồi
dừng** — và ở RTT 0 nó cao (36.69x) chỉ vì mẫu số (reuse ~10µs) quá nhỏ. Cả hai đầu của trục
RTT đều không phải chỗ tỉ số nói lên điều gì về pool:

> Tỉ số `dial/reuse` tiệm cận 2.00x và dừng, dù RTT tệ đến đâu. Nó chỉ nói "workload
> dial-mỗi-request trả 2 RTT, workload reuse trả 1 RTT". Trong khi **phí phạm tuyệt đối tăng
> tuyến tính theo RTT**: 0.33ms → 20.47ms → 40.47ms.

Đơn vị đúng cho pool: **1 RTT phí cho mỗi connection dựng mới**.

Ghi thêm một quan sát phụ mà bản biên tập chỉ nhắc một câu: phân phối cực chặt (p50 20.43 /
p99 20.66). `netem delay` trần không có jitter, không mất gói ⇒ một mạng **sạch bất thường**.
Mà jitter mới là thứ sinh ra tail latency, và tail latency là toàn bộ lý do phase 6-7 tồn tại.
Nợ `P0-6`.

## 13. G8: ngõ cụt, nhưng lộ ra thứ lớn hơn

Kết quả G3 làm nảy nghi vấn về chính 36.69x: lần đo G2 chạy 2000 dial liên tiếp hết tốc lực, và
§10 đã **đo được** rằng dial đắt dần khi TIME_WAIT đầy. Vậy 36.69x có bị chính hiệu ứng G6 làm
phồng?

Đăng ký **G8 trước khi đo**: nếu chỉ dial 100 lần (tw thấp) thì `dial p50` phải thấp hơn 2-3x.

```
n=100   tw 25   -> 2618 :  reuse p50  9.6µs   dial p50 341.3µs   35.52x
n=500   tw 126  -> 627  :  reuse p50 10.0µs   dial p50 345.0µs   34.35x
n=2000  tw 627  -> 2618 :  reuse p50 11.2µs   dial p50 303.9µs   27.01x
```

**G8 sai.** `dial p50` phẳng, nếu có xu hướng thì là *giảm*. Hợp lý khi nhìn lại §10: suy giảm
ở đó chỉ đến khi `tw` vượt ~20000 (~70% không gian port). 2618 mới là 9%.

**Nhưng ngõ cụt này lộ ra thứ tôi không đi tìm:** `dial p50` ở đây là **303-345µs**, còn lần đo
G2 gốc là **861.7µs**. Cùng lệnh, chênh 2.5x. Và `reuse p50` cũng chênh cùng hướng (23.5 → 9.6µs)
⇒ **cả hai vế cùng chậm** ⇒ máy lúc đó chậm hơn toàn cục, không phải `Dial` đắt hơn.

Nên tỉ số sống sót (36.69 → 35.52x) còn số tuyệt đối thì không (861.7 → 341.3µs).

Đây là lần đầu nguyên tắc *"chốt lại bằng tỉ số, số tuyệt đối đổi theo máy"* tự chứng minh bằng
số đo của chính nó — và mạnh hơn tôi từng viết: số tuyệt đối đổi **2.5x ngay trên cùng một máy,
cùng một lệnh, cách nhau 30 phút**. Nợ `P0-7`.

Và một khớp nối đẹp: phần phi-RTT của chi phí dial suy từ netem là **~313µs** (40.90ms −
2×20.157ms); đo trực tiếp ở RTT 0 với tw thấp là **331.7µs** (341.3 − 9.6). Hai môi trường hoàn
toàn khác nhau, cùng một con số. Chi phí dựng socket trên máy này là **~320µs**, và 861µs là
artifact của lúc máy chậm.

---

## Tổng kết đường đi

**Thứ tự phát hiện, ngược với thứ tự trình bày trong `phase0.md`:**

| # | Phát hiện | Tìm ra ở đâu | Đáng lẽ thuộc về |
|---|---|---|---|
| 1 | `bufio` gộp 2 `Write` thành 1 syscall | trong lúc đo G1 | G1 |
| 2 | spike 44ms | trong lúc đo **G1** (tưởng là treo) | **G7** |
| 3 | `tcp_tw_reuse=2` che port exhaustion | trong lúc đo G6, sau khi đọc lại `envcap.sh` | môi trường |
| 4 | trần OS báo bằng thông lượng tụt, không bằng lỗi | sau 4 lần chạy G6 | G6 |
| 5 | RSS không đơn điệu | khi chạy `make netlab` **sau khi đã viết xong bản biên tập** | G5 |
| 6 | netem trên `lo` nhân đôi delay | khi trả nợ P0-1 | môi trường / mọi phase sau |
| 7 | tỉ số là đơn vị **sai** cho pool | khi trả nợ P0-1, lật lại cách hiểu từ §7 | G2 + G3 |
| 8 | số tuyệt đối đổi 2.5x trên cùng máy | trong lúc đo **G8** (một ngõ cụt) | toàn bộ phase |

Ba mục 2, 5, 8 tìm ra trong lúc làm việc khác. Không mục nào tìm ra bằng cách "đo cái mình định đo".

**Bốn lần bộ đo sai:** `bufio` che syscall + che spike 44ms · `tcp_tw_reuse=2` che port
exhaustion · RSS không đơn điệu cho số âm · `netem delay` nhân đôi thành RTT. Không lần nào là
thiếu kiến thức về mạng — cả bốn là **bộ đo che mất thứ cần đo**, và cả bốn manh mối đều đã nằm
trong output của `envcap.sh` hoặc của chính netlab.

Thủ tục "*số đo trông vô lý ⇒ nghi bộ đo trước*" cứu được lần 1 (nhờ p99 = 0.77x) và lần 3 (nhờ
số âm) — cả hai đều nhờ có một giá trị **không thể đúng về mặt logic**. Lần 2 và lần 4 không có
tín hiệu như thế: 30033 connection không lỗi, và RTT 40ms khi đặt 20ms, **đều trông hợp lý**.
Hai lần đó chỉ tìm ra vì tình cờ đọc lại môi trường và vì cố ý đo hai cấu hình để tự kiểm.

Kết luận về phương pháp, mang sang 9 phase còn lại: **một con số hợp lý không phải bằng chứng
gì cả.** Cách duy nhất bắt được lần 2 và lần 4 là **đo hai điểm rồi kiểm quan hệ giữa chúng**,
thay vì đo một điểm rồi kiểm xem nó có hợp lý không.

## 5/8 giả thuyết sai

| # | Đăng ký | Đo được | |
|---|---|---|---|
| G1 | 1.5-2x | 1.41x p50 · 1.07x p99 | ⚠️ gần sai |
| G2 | 1.0-1.3x | 36.69x | ❌ |
| G3 | > 15x | 2.00x | ❌ |
| G4 | > 3x | 1787x | ✅ |
| G5 | > 4x | 9.70x | ✅ |
| G6 | port trước, có lỗi | đúng thủ phạm, sai hình dạng | ❌ |
| G7 | 40ms ± 5 | 44.03ms | ✅ |
| G8 | 2-3x thấp hơn | phẳng | ❌ |
