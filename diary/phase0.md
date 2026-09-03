# Phase 0 — Nền tảng vật lý mạng

- **Thời lượng dự kiến:** 0.5 ngày · **thực tế:** ~0.5 ngày
- **Bắt đầu:** 2026-09-03 · **Kết thúc:** 2026-09-03
- **Trạng thái:** ✅ xong trên máy này · ⬜ **còn một thí nghiệm (G3) chưa chạy được vì thiếu sudo**
- **Commit:** `_______` — mọi số đo trong file này thuộc về cây làm việc của commit đó

> **Đường đi thô, kể cả ngõ cụt:** [`phase0-log.md`](phase0-log.md). File này là bản biên tập.
>
> **Quy tắc ghi nhật ký:** mọi con số, mọi kết luận đều phải kèm **lệnh shell sinh ra nó**
> và **output thật** (dán nguyên, không tóm tắt). Sáu tháng sau đọc lại phải chạy lại được.
> Luật đầy đủ: [`../skills/diary/SKILL.md`](../skills/diary/SKILL.md).

## Môi trường

Khối này sinh tự động, không chép tay: `./scripts/envcap.sh` → `bench/env-<host>.txt`.

```console
$ ./scripts/envcap.sh
đã ghi bench/env-GOTIT-00663.txt

--- kiểm tra ba cái bẫy trước khi tin bất kỳ số nào ---
✅ ulimit -n = 1048576
✅ lo không có netem: RTT ~0. Kết luận nào phụ thuộc RTT thì CHƯA đo được.
ℹ️  nproc = 6. Generator và thứ bị đo phải ở core khác nhau (taskset).

$ cat bench/env-GOTIT-00663.txt
Linux 6.6.87.2-microsoft-standard-WSL2 x86_64 GNU/Linux
go version go1.26.2 linux/amd64
nproc: 6
model name	: 12th Gen Intel(R) Core(TM) i5-1235U
Mem:           11962 MB total
net.ipv4.ip_local_port_range = 32768	60999     # = 28232 port
net.core.somaxconn = 4096
net.ipv4.tcp_max_syn_backlog = 1024
net.ipv4.tcp_tw_reuse = 2                        # <- CON SỐ NÀY PHÁ MỘT THÍ NGHIỆM, xem G6
WSL2
```

⚠️ Mọi lần chạy dùng `ulimit -n 65536` (netlab in lại giá trị thật ở đầu output, không tin flag).

⚠️ **WSL2:** loopback không phải NIC thật. Đã dùng đúng vào việc: mọi kết luận dưới đây là
**tỉ số**, và mỗi tỉ số ghi kèm môi trường đã sinh ra nó.

## Mục tiêu phase

Đo trên chính máy này 5 sự thật vật lý quyết định mọi thiết kế proxy về sau. Chưa code proxy.

## Dụng cụ đo: `cmd/netlab`

Một quyết định thiết kế đáng ghi lại: **mọi tham số thí nghiệm nằm trong chính request**
(`flags | svcMicros | respSize`), không nằm trong flag của server.

Vì sao: nếu hành vi server do flag server quyết định thì bộ đo chỉ chạy được khi client và
server cùng process — tức là **vĩnh viễn bị khoá trên loopback**. Đặt tham số vào request thì:

```bash
netlab -role server -addr :9000          # máy A
netlab -role client -addr <A>:9000 -all  # máy B  <- RTT thật, không cần tc netem
```

Đây là "đường sang máy khác" của phase này, và nó phải có **từ đầu**, không phải sửa sau.

---

## Giả thuyết đăng ký TRƯỚC khi đo

Đăng ký sau khi `cmd/netlab` build xong, trước lệnh đo đầu tiên. **Kết quả cuối: 5/8 sai.**

| # | Giả thuyết | Tỉ số kỳ vọng | Thực tế | Đúng/Sai |
|---|---|---|---|---|
| G1 | 2 lần `Write` chậm hơn 1 lần (2 syscall + có thể 2 packet) | 1.5-2x | 1.41x (p50), 1.07x (p99) | ⚠️ **gần sai** |
| G2 | `Dial` mới vs reuse, **RTT 0** — gần như không khác, pool trông vô dụng | 1.0-1.3x | **36.69x** | ❌ **SAI HẲN** |
| G3 | `Dial` mới vs reuse, **RTT 20ms** | > 15x | **2.00x** | ❌ **SAI HẲN** |
| G4 | p99 closed-loop **thấp hơn** p99 open-loop ở cùng rate danh nghĩa | > 3x | **1787x** / 33x | ✅ |
| G5 | RSS/conn ở 10k conn rỗi lớn hơn nhiều so với 2KB stack | > 4x | **9.70x** | ✅ |
| G6 | Không pool sẽ cạn **ephemeral port** trước khi cạn CPU | port trước | đúng hướng, **sai hình dạng** | ❌ |
| G7 | Nagle + delayed ACK cho spike đúng ~40ms | 40ms ± 5 | **44.03ms** | ✅ |

Đăng ký thêm **sau** khi trả nợ P0-1, **trước** khi chạy lệnh kiểm chứng — vì kết quả G3 làm nảy
ra một nghi vấn về chính con số 36.69x của G2:

| # | Giả thuyết | Tỉ số kỳ vọng | Thực tế | Đúng/Sai |
|---|---|---|---|---|
| G8 | Con số 36.69x của G2 **bị chính hiệu ứng G6 làm phồng**: lần đo đó thực hiện 2000 dial liên tiếp hết tốc lực, làm TIME_WAIT đầy dần, và ta đã ĐO ĐƯỢC rằng dial đắt dần khi tw đầy. Nếu chỉ dial 100 lần (tw còn thấp) thì `dial p50` phải **thấp hơn hẳn** | 2-3x thấp hơn | phẳng: 341 / 345 / 304 µs | ❌ **SAI** |

---

## Nhật ký

### 2026-09-03 — G1, và bài học đắt nhất của phase: bộ đo sai trước khi kết luận sai

Lần chạy đầu tiên, server để mặc định `-bufio=true`:

```console
$ ulimit -n 65536 && go run ./cmd/netlab -exp syscall -n 5000 -tag "wsl2-rtt0"

--- G1: 1 lần Write vs 2 lần Write (1 connection reuse, không pool)
biến thể                           loop            n      mean       p50       p90       p99       max    err
----------------------------------------------------------------------------------------------------------------------
1 Write (resp 1024B)               -            5000    17.9µs    12.0µs    34.4µs    97.7µs    1.31ms      0
2 Write (resp 1024B)               -            5000    16.4µs    12.2µs    18.6µs    75.4µs   736.8µs      0
2 Write + Nagle server (resp 1024B) -            5000    22.1µs    12.4µs    42.3µs   119.1µs   569.0µs      0
  [G1] p50 2-Write / p50 1-Write                        1.02x   (kỳ vọng 1.5-2x)
  [G1] p99 2-Write / p99 1-Write                        0.77x   (kỳ vọng 1.5-2x)
```

**Đọc kết quả:** 1.02x, và p99 còn *nhỏ hơn 1*. Nếu tin con số này thì kết luận sẽ là "syscall
gần như miễn phí, `bufio.Writer` trong proxy là tối ưu hoá vô nghĩa" — sai hoàn toàn, và sai
theo hướng sẽ làm hỏng thiết kế của phase 2 và phase 9.

**Đang nghĩ gì:** áp dụng đúng thủ tục của SKILL.md — *khi số đo trông vô lý, giả định BỘ ĐO
sai trước, đừng giả định cái máy lạ*. Server đang chạy `-bufio=true`. Hai lần `Write` của tôi
đi vào `bufio.Writer` và bị **gộp lại thành đúng một syscall lúc `Flush`**. Tức là phép đo
đang đo chính cái thứ nó lẽ ra phải loại bỏ. Đây đúng là bản mạng của cái bẫy page-cache trong
`db/diary/phase0.md`: tôi tưởng đang đo syscall, thật ra đang đo `memcpy` vào buffer userspace.

Chạy lại với `-bufio=false`:

```console
$ ulimit -n 65536 && go run ./cmd/netlab -exp syscall -n 5000 -bufio=false -tag "wsl2-rtt0-NOBUFIO"

--- G1: 1 lần Write vs 2 lần Write (1 connection reuse, không pool)
biến thể                           loop            n      mean       p50       p90       p99       max    err
----------------------------------------------------------------------------------------------------------------------
1 Write (resp 1024B)               -            5000    13.5µs    10.9µs    14.5µs    55.0µs   550.6µs      0
2 Write (resp 1024B)               -            5000    17.6µs    15.3µs    17.9µs    58.6µs   671.9µs      0
2 Write + Nagle server (resp 1024B) -            5000   44.54ms   44.01ms   47.92ms   48.21ms   61.14ms      0
  [G1] p50 2-Write / p50 1-Write                        1.41x   (kỳ vọng 1.5-2x)
  [G1] p99 2-Write / p99 1-Write                        1.07x   (kỳ vọng 1.5-2x)
  [G1] p50 2-Write+Nagle / p50 2-Write                2875.08x
```

**Đọc kết quả:** 1.41x ở p50 — vẫn thấp hơn khoảng kỳ vọng 1.5-2x, nên G1 tính là **gần sai**.
Chênh 4.4µs cho một syscall `write` thêm là hợp lý về độ lớn. p99 chỉ 1.07x, tức ở đuôi phân
phối thì nhiễu lịch trình goroutine lớn hơn hẳn chi phí một syscall — **syscall không phải thứ
quyết định tail latency**, và đó là một điều chỉnh trực giác đáng giá cho phase 9.

**Nhưng dòng thứ ba mới là chuyện lớn: 44.01ms.** Lệnh này ban đầu bị tôi tưởng là **treo** —
nó chạy hơn 200 giây và tôi đã đi kiểm `pgrep` để tìm deadlock. Không treo: nó đang chạy đúng
5000 request × 44ms. **G7 tự lộ ra trong thí nghiệm không dành cho nó**, và nó chỉ lộ ra được
vì vừa tắt `bufio` — `bufio.Writer` đã che luôn cả cái spike 40ms này ở lần chạy đầu (12.4µs).

Một dòng `bufio` che hai sự thật khác nhau cùng lúc.

**Đang nghĩ gì:** con số 200 giây đó không phải sự cố, nó là **dữ liệu**: nó nói rằng biến thể
Nagle không thể chạy cùng số request với các biến thể khác. Đã sửa code cho `nagleN = n/50`,
và **ghi lý do vào comment kèm con số**, để lần sau không ai đặt lại `n=5000` ở đó:

```go
// Biến thể Nagle chạy ÍT request hơn 50 lần, và con số 50 đó là một kết quả
// đo, không phải lựa chọn tuỳ ý: mỗi request ở đây tốn ~44ms [...]
// Lần đo đầu tiên của phase này đã mất đúng 200 giây vì không biết điều đó.
```

### 2026-09-03 — G2/G3: giả thuyết sai nặng nhất của phase

```console
$ ulimit -n 65536 && go run ./cmd/netlab -exp rtt -n 2000 -bufio=false -tag rtt0

--- G2/G3: dial mới vs reuse (đọc kèm RTT của môi trường!)
biến thể                           loop            n      mean       p50       p90       p99       max    err
----------------------------------------------------------------------------------------------------------------------
reuse 1 connection                 -            2000    32.7µs    23.5µs    46.6µs   188.9µs    1.09ms      0
dial mới mỗi request               -            2000   986.0µs   861.7µs    1.36ms    3.84ms   30.76ms      0
  [G2/G3] p50 dial / p50 reuse                            36.69x   (kỳ vọng RTT 0: 1.0-1.3x · RTT 20ms: >15x)
  [G2/G3] p99 dial / p99 reuse                            20.34x
```

**Đọc kết quả:** kỳ vọng 1.0-1.3x, thực tế **36.69x**. Sai hẳn một bậc độ lớn, và sai theo
hướng tôi không lường: tôi dựng sẵn cái bẫy "pool trông vô dụng trên loopback" cho chính mình,
rồi cái bẫy **không nổ**.

**Đang nghĩ gì:** giả thuyết G2 dựa trên một suy luận đúng nhưng **thiếu**: "handshake tốn 1
RTT, loopback không có RTT, vậy dial gần như miễn phí". Vế đầu đúng. Vế kết luận sai, vì
**RTT không phải chi phí duy nhất của một `Dial`**. 861µs cho một dial trên loopback RTT ~30µs
nghĩa là ~96% chi phí không phải RTT: tạo socket, `connect`, kernel cấp ephemeral port, phía
server `accept` + tạo goroutine + cấp buffer, rồi cả hai đầu bị lên lịch lại. Trên WSL2 (network
stack ảo hoá) phần này còn đắt hơn Linux thuần.

Hệ quả quan trọng hơn con số: **tôi vẫn chưa đo được G3, nên tôi vẫn chưa tách được hai nguyên
nhân.** 36.69x này là chi phí *dựng socket*. Chi phí *RTT* vẫn còn vô hình. Và dự đoán ngược
đời cho lần chạy có RTT 20ms: `reuse` sẽ lên ~20ms, `dial` lên ~40ms, nên **tỉ số sẽ TỤT xuống
~2x** trong khi **khoản tiết kiệm tuyệt đối tăng từ 0.86ms lên ~20.9ms**.

Nghĩa là ở đây tỉ số là cách tóm tắt **sai**. Với connection pool, đơn vị đúng không phải "bao
nhiêu lần" mà là **"bao nhiêu RTT mỗi request"**. Đây là điều chỉnh phải mang sang phase 5 —
nếu không, phase 5 sẽ báo cáo "pool cải thiện 36x" ở RTT 0 rồi "chỉ 2x" ở RTT 20ms và kết luận
ngược hoàn toàn với thực tế.

**Chưa làm được:** `tc qdisc` cần sudo, chưa chạy. Ghi thành nợ `P0-1`, kèm nguyên lệnh.

### 2026-09-03 — G4: coordinated omission, và nó lớn hơn tôi tưởng 500 lần

Server bị bó ở `workers=1`, `svc=1ms` ⇒ capacity danh nghĩa 1000 rps. Cố ý áp 1200 rps.

```console
$ ulimit -n 65536 && go run ./cmd/netlab -exp omission -rate 1200 -duration 10s -svc 1ms -workers 1 -bufio=false

--- G4: coordinated omission (server capacity ≈ 1000 rps, áp 1200 rps)
biến thể                           loop            n      mean       p50       p90       p99       max    err
----------------------------------------------------------------------------------------------------------------------
closed-loop, 1 conn                closed       7545    1.32ms    1.28ms    1.57ms    1.87ms    6.82ms      0
closed-loop, 50 conn               closed       7389   67.90ms   64.05ms   83.32ms  100.71ms  104.07ms      0
open-loop, rate cố định            open         9040  617.50ms  139.59ms     2.09s     3.35s     4.15s      0
  rps closed-loop, 1 conn            yêu cầu     1200  đạt được      754  (62.9%)
  rps closed-loop, 50 conn           yêu cầu     1200  đạt được      733  (61.1%)
  rps open-loop, rate cố định        yêu cầu     1200  đạt được      714  (59.5%)
  ghi chú open-loop, rate cố định    2960 request bị drop vì hết connection rỗi (backlog thật)
  [G4] p99 open-loop / p99 closed-loop 1 conn         1787.50x   (kỳ vọng >3x)
  [G4] p99 open-loop / p99 closed-loop 50 conn         33.26x   (kỳ vọng >3x)
```

**Đọc kết quả:** ba dòng này là **cùng một hệ thống, cùng một trạng thái quá tải**. p99 báo về
là 1.87ms, 100.71ms, hoặc 3.35s — chênh nhau **1787 lần** — tuỳ vào việc dùng dụng cụ nào.

Chi tiết đáng chú ý hơn cả tỉ số: **cả ba đều chỉ đạt ~60% rate đã đặt**, và `closed-loop 1 conn`
"đạt" 754 rps trong khi báo p99 = 1.87ms. Một báo cáo kiểu "hệ thống chịu được 754 rps với
p99 dưới 2ms" **hoàn toàn đúng về mặt số học và hoàn toàn vô nghĩa**: nó là p99 của một tải mà
chính server đã tự điều tiết xuống mức nó chịu được.

**Đang nghĩ gì:** capacity danh nghĩa là 1000 rps (1/svc) nhưng đo được 754. Không phải bug —
`time.Sleep(1ms)` thực tế ngủ ~1.3ms vì độ hạt của timer + lên lịch goroutine. Đây là lý do
`capacity ≈` trong tiêu đề bảng phải là chữ "≈", và là lý do **capacity phải được ĐO, không
được TÍNH**. Đã ghi nợ `P0-2`.

`p50` của open-loop là 139ms còn p99 là 3.35s: hàng đợi phình theo thời gian, không có điểm
dừng — đúng hình dạng của một hệ thống vượt capacity mà không có backpressure. Phase 7 (load
shedding) tồn tại vì cái đường cong này, và giờ tôi có nó bằng số của chính máy mình.

Một điều đã làm đúng trong bộ đo và đáng ghi lại: khi open-loop hết connection rỗi, nó **đếm
drop** chứ không im lặng chờ. Nếu chờ, bộ đo open-loop sẽ tự sinh ra coordinated omission ngay
bên trong chính nó — 2960 request kia sẽ biến thành "latency thấp" thay vì "không được phục vụ".

### 2026-09-03 — G5: giá bộ nhớ của một connection rỗi, và đo luôn phần của `bufio`

```console
$ ulimit -n 65536 && go run ./cmd/netlab -exp mem -conns 10000 -bufio=true

--- G5: bộ nhớ của 10000 connection rỗi (bufio=true)
  RSS       : 5376 KB -> 199424 KB   (delta 194048 KB)
  RSS/conn  : 19.40 KB   (đây là CẶP client+server trong 1 process)
  HeapAlloc : 1.2 MB -> 178.9 MB  => 17.35 KB/conn
  goroutine : 2 -> 10002   (1.0/conn)
  StackSys  : 0.4 MB -> 41.5 MB  => 4.01 KB/conn
  => so với 2KB stack khởi tạo của goroutine: 9.70x

$ ulimit -n 65536 && go run ./cmd/netlab -exp mem -conns 10000 -bufio=false

--- G5: bộ nhớ của 10000 connection rỗi (bufio=false)
  RSS       : 5632 KB -> 96512 KB   (delta 90880 KB)
  RSS/conn  : 9.09 KB
  HeapAlloc : 1.2 MB -> 95.4 MB  => 9.20 KB/conn
  StackSys  : 0.5 MB -> 21.0 MB  => 2.01 KB/conn
  => so với 2KB stack khởi tạo của goroutine: 4.54x
```

**Đọc kết quả:** G5 đúng (9.70x > 4x). Nhưng phần giá trị nhất là **hiệu của hai lần chạy**:
19.40 − 9.09 = **10.31 KB/conn là tiền trả riêng cho `bufio` phía server** (4KB Reader + 4KB
Writer + overhead). Không phải đi đọc blog, mà là bật/tắt một flag và trừ hai con số.

`StackSys/conn` còn nói một chuyện tinh hơn: **2.01 KB khi không có `bufio`** — khớp chính xác
`runtime._StackMin` = 2KB, và là bằng chứng con số 2KB kia không phải huyền thoại. Nhưng khi có
`bufio` thì thành **4.01 KB**: stack goroutine đã phải nới lên một lần vì đường gọi khởi tạo
`bufio` sâu hơn. Tức là `bufio` thu hai lần phí: heap của buffer, **và** một lần nới stack.

**Đang nghĩ gì:** con số phải mang sang phase 9: 10k connection rỗi = ~194 MB nếu cấp `bufio`
ngay lúc accept. Đây chính là lý do `sync.Pool` ở phase 9 không phải "microbenchmark cho vui" —
và cũng là lý do việc **hoãn cấp buffer đến khi có byte đầu tiên** có thể còn ăn hơn cả pool.
Đó là một hướng thiết kế tôi chưa từng nghĩ tới trước khi chạy phép đo này.

Cảnh báo diễn giải đã in ngay trong output và phải nhắc lại: đây là **cặp** client+server trong
một process, không phải chi phí một connection phía server. Muốn số của riêng server thì
`-role server` ở process khác. Nợ `P0-3`.

### 2026-09-03 — G6: trần OS không báo bằng lỗi, nó báo bằng thông lượng tụt dần

Lần chạy đầu, qua `127.0.0.1`:

```console
$ go run ./cmd/netlab -exp limits -duration 20s -bufio=false
  ip_local_port_range  : 32768	60999
  sockstat TRƯỚC       : TCP: inuse 30 orphan 0 tw 21155 alloc 70 mem 540
  sockstat SAU         : TCP: inuse 32 orphan 1 tw 33957 alloc 73 mem 540
  connection thành công: 30033 trong 20s = 1502 conn/s
  connection lỗi       : 0
```

**Đọc kết quả:** `tw` = 33957 trong khi chỉ có 28232 ephemeral port. Thoạt nhìn là vô lý.
Không vô lý: TIME_WAIT tính theo **4-tuple**, và mỗi lần chạy netlab dùng một port server khác,
nên cùng một local port vẫn dùng lại được với remote port khác. Con số `tw` **không** so trực
tiếp được với số port.

Và 30033 connection trong 20s không lỗi một cái nào, dù 28232 port là toàn bộ không gian. Đi
đọc lại khối môi trường thì thấy dòng đã in từ đầu mà tôi không để ý:

```
net.ipv4.tcp_tw_reuse = 2
```

Giá trị `2` = **chỉ bật cho loopback**. Kernel tái dùng socket TIME_WAIT ngay, nên
**ephemeral port exhaustion là thứ không thể tái hiện trên `127.0.0.1` với sysctl này.** Cái bẫy
loopback nổ lần thứ ba trong cùng một phase, và lần này ở chỗ tôi không hề đề phòng.

Chạy lại qua IP của `eth0` (172.19.62.65 — không thuộc 127/8 nên `tw_reuse=2` không áp), ba lần
liên tiếp để `tw` tích luỹ dần:

```console
$ go run ./cmd/netlab -exp limits -duration 25s -addr 172.19.62.65:9111   # tw nền 12907
  connection thành công: 18440 trong 25.002s = 738 conn/s ; lỗi 0

$ go run ./cmd/netlab -exp limits -duration 70s -addr 172.19.62.65:9112   # tw nền 18488
  sockstat SAU: TCP: ... tw 26936 ...
  connection thành công: 42087 trong 1m10.003s = 601 conn/s ; lỗi 0

$ go run ./cmd/netlab -exp limits -duration 40s -addr 172.19.62.65:9113   # tw nền 23592
  sockstat SAU: TCP: ... tw 32759 ...
  connection thành công: 18191 trong 40.007s = 455 conn/s ; lỗi 0
```

**Đọc kết quả:** `1502 → 738 → 601 → 455 conn/s`, tụt **3.3x**, và **không một lỗi nào** trong
suốt 155 giây. Giả thuyết G6 đúng về *thủ phạm* (không gian port, không phải CPU) nhưng **sai
về hình dạng của thất bại**: tôi chờ một `EADDRNOTAVAIL`. Thực tế trần này **không báo bằng
exception, nó báo bằng thông lượng suy giảm đơn điệu** — kernel phải quét ngày càng lâu để tìm
một 4-tuple rỗi, và mỗi `Dial` cứ thế đắt dần lên.

Đây là hình dạng khó chẩn đoán nhất trong vận hành thật: không log lỗi, không alert, chỉ là
"dạo này hệ thống chậm". Sáu tháng sau khi có ai hỏi "sao proxy chậm mà CPU rỗi", tôi có
`1502 → 455` của chính máy mình để trả lời.

Để câu "không phải CPU" là kết luận chứ không phải câu nói cho hay, đã thêm đo `/proc/stat` vào
`expLimits` rồi chạy lại:

```console
$ go run ./cmd/netlab -exp limits -duration 40s -addr 172.19.62.65:9114
  connection thành công: 24563 trong 40.005s = 614 conn/s
  connection lỗi       : 0
  CPU toàn máy         : 29.5% busy trong suốt thí nghiệm (6 core)
```

**Đọc kết quả:** 29.5% trên 6 core ≈ 1.8 core đang chạy. Chưa đủ để nói "CPU rỗi hoàn toàn"
nhưng đủ để loại CPU khỏi danh sách nghi phạm cho việc thông lượng bị chặn ở 614/s.

**Đang nghĩ gì:** phép đo này **vẫn còn một lỗ hổng và tôi không được tự cho là đã xong**: vòng
lặp dial của tôi là **tuần tự** (dial → 1 request → close → dial), nên nó không thể phân biệt
"trần ở không gian port" với "trần ở chính vòng lặp một luồng của client". Muốn tách thì phải
có N dialer song song. Ghi nợ `P0-4` kèm lệnh, **không** viết vào bảng tỉ số như một kết luận
đã chứng minh.

### 2026-09-03 — bộ đo sai lần thứ ba: RSS âm

Chạy `make netlab` (tức `-all`) như một phép kiểm tra cuối, và nhận:

```console
$ ulimit -n 65536 && go run ./cmd/netlab -all -bufio=false -n 1000 -duration 5s -conns 500
--- G5: bộ nhớ của 500 connection rỗi (bufio=false)
  RSS/conn  : -7.99 KB   (đây là CẶP client+server trong 1 process)
```

**Đọc kết quả:** âm. Không cần suy nghĩ gì về mạng — một con số âm ở đây chỉ có một nghĩa: bộ
đo sai. Các thí nghiệm chạy trước (`omission` mở 512 connection) đã đẩy RSS lên cao, rồi
scavenger của Go **trả bộ nhớ về OS** trong lúc `expMem` đang chạy. Mốc nền `base` vì thế cao
hơn mức thật, và hiệu ra số âm.

**Đang nghĩ gì:** RSS là con số **toàn process và không đơn điệu**, nên nó chỉ có nghĩa trong
một process **sạch**. Đây là điều kiện của phép đo, không phải chi tiết cài đặt — nên đã sửa
hai chỗ: `expMem` **từ chối** in kết quả khi `after <= base` (thà không có số hơn có số sai),
và `-all` **tự fork một process mới** cho G5. Con số 19.40 KB/conn ở mục G5 phía trên vẫn đúng
vì nó vốn được chạy `-exp mem` một mình.

Ba lần trong một phase: `bufio`, `tcp_tw_reuse`, và giờ là RSS không đơn điệu. Không lần nào
là thiếu kiến thức về mạng — cả ba là **bộ đo che mất thứ cần đo**.

### 2026-09-03 — G7: 44.03ms, và nó là tổng của hai cơ chế đều đúng

```console
$ ulimit -n 65536 && go run ./cmd/netlab -exp nagle -n 150 -bufio=false

--- G7: Nagle + delayed ACK (50 request mỗi biến thể)
biến thể                           loop            n      mean       p50       p90       p99       max    err
----------------------------------------------------------------------------------------------------------------------
1 Write, NoDelay(true)             -              50    45.5µs    41.2µs    46.0µs   148.1µs   330.9µs      0
2 Write, NoDelay(true)             -              50    34.4µs    32.0µs    34.2µs    75.2µs    85.4µs      0
2 Write, NoDelay(FALSE) — write-write-read -              50   44.32ms   44.03ms   44.57ms   47.99ms   48.02ms      0
  [G7] p99 Nagle-write-write / p99 NoDelay-write-write 638.01x
```

**Đọc kết quả:** 44.03ms ở p50, nằm ngoài khoảng 40 ± 5 một chút nhưng đúng bản chất. Điều
đáng nói là **độ chặt của phân phối**: p50 44.03, p90 44.57, p99 47.99. Không phải một cái
spike ngẫu nhiên ở đuôi — nó là một **hằng số**. Mỗi request đều trả đúng 44ms, vì mỗi request
đều đợi đúng một chu kỳ delayed-ACK.

**Đang nghĩ gì:** đây là loại lỗi khiến người ta đi tìm sai chỗ hàng tuần. 44ms quá đều để
giống nghẽn mạng, quá lớn để giống chi phí xử lý, và **CPU rỗi hoàn toàn trong lúc đó**. Không
có gì "chậm": hai cơ chế xét riêng đều đúng (Nagle gộp gói nhỏ; delayed ACK giảm ACK rỗng),
ghép lại thành deadlock 40ms.

Và nó tái hiện được **ngay trên loopback WSL2** — trái ngược cảnh báo tôi tự viết trong code
rằng loopback gần như không có delayed ACK. Cảnh báo đó sai, đã sửa lại comment.

Hai hệ quả trực tiếp, không phải lý thuyết:
1. Phase 2/3 phải ghi header + body bằng **một** lần `Write` hoặc qua `bufio.Writer` có `Flush`
   tường minh. Một proxy ghi header rồi `io.Copy` body trên socket trần chính là
   `write-write-read`.
2. Mọi socket của EdgeGate, **cả hướng client lẫn hướng upstream**, phải `SetNoDelay(true)`
   tường minh. Go đã mặc định bật, nhưng để nó thành mặc định ngầm thì nửa năm sau không ai
   biết vì sao có 44ms.

### 2026-09-03 — trả nợ P0-1: G3 sai, và tỉ số hoá ra là đơn vị sai

`tc` cần root, nên đóng gói thành `scripts/pay-P0-1.sh` với `trap ... EXIT INT TERM` để qdisc
được tháo trong **mọi** đường ra, kể cả Ctrl-C. Quên tháo là làm sai mọi benchmark sau đó trên
máy này, và nửa năm sau không ai nhớ vì sao.

Script cố ý đo **hai** cấu hình delay, không phải một — vì có một câu hỏi phải trả lời trước
khi con số G3 có nghĩa.

```console
$ bash ./scripts/pay-P0-1.sh
=== mốc nền, chưa có netem ===
qdisc noqueue 0: root refcnt 2
RTT nền: 0.386 ms

=== netem delay 10ms  (n=300 request mỗi biến thể) ===
qdisc netem 8001: root refcnt 2 limit 1000 delay 10ms
>>> ĐẶT delay=10ms mỗi chiều  =>  RTT ĐO ĐƯỢC = 20.157 ms

--- G2/G3: dial mới vs reuse (đọc kèm RTT của môi trường!)
biến thể                           loop            n      mean       p50       p90       p99       max    err
----------------------------------------------------------------------------------------------------------------------
reuse 1 connection                 -             300   20.45ms   20.43ms   20.55ms   20.66ms   26.75ms      0
dial mới mỗi request               -             300   41.01ms   40.90ms   41.15ms   43.90ms   49.49ms      0
  [G2/G3] p50 dial / p50 reuse                             2.00x   (kỳ vọng RTT 0: 1.0-1.3x · RTT 20ms: >15x)

=== netem delay 20ms  (n=150 request mỗi biến thể) ===
>>> ĐẶT delay=20ms mỗi chiều  =>  RTT ĐO ĐƯỢC = 40.201 ms

reuse 1 connection                 -             150   40.57ms   40.49ms   40.65ms   42.20ms   46.03ms      0
dial mới mỗi request               -             150   81.24ms   80.96ms   81.39ms   85.79ms   90.73ms      0
  [G2/G3] p50 dial / p50 reuse                             2.00x

--- dọn dẹp: tháo qdisc khỏi lo ---
qdisc noqueue 0: root refcnt 2
```

**Đọc kết quả — trước hết là câu hỏi treo của nợ `P-env-1`, nay đã có câu trả lời dứt điểm:**
đặt `delay 10ms` → RTT **20.157ms**; đặt `delay 20ms` → RTT **40.201ms**. Đúng hệ số 2 ở cả hai
điểm. Trên `lo`, gói đi và gói hồi **đều** qua qdisc của `lo`, nên netem áp delay **hai lần cho
một round-trip**. Hệ quả thực dụng: muốn RTT 20ms thì đặt `delay 10ms`. Nếu tôi làm theo đúng
lệnh mình đã viết sẵn trong `Makefile` (`netem delay 20ms`) rồi ghi nhãn "RTT 20ms" thì **mọi
diary từ phase 5 trở đi đã sai nhãn 2x** — và sai theo hướng không ai phát hiện được, vì mọi
tỉ số vẫn đẹp.

**Rồi đến G3: kỳ vọng > 15x, đo được 2.00x.** Sai hẳn. Dự đoán tôi đăng ký trong `docs/debts.md`
lúc trả nợ (tỉ số **tụt** về ~2x, tiết kiệm tuyệt đối **tăng** lên ~20.9ms) thì đúng: tiết kiệm
thật là 40.90 − 20.43 = **20.47ms**.

**Đang nghĩ gì:** con số 2.00x này không phải "gần 2", nó là **đúng 2.00x ở cả hai RTT** — 2.00
ở RTT 20.157ms và 2.00 ở RTT 40.201ms. Một tỉ số trùng khít như thế ở hai điểm khác nhau không
phải trùng hợp, nó là một **hằng số tiệm cận**, và đây là mô hình giải thích nó:

```
reuse  = xử lý (~10µs)  + 1 RTT          <- request + response
dial   = dựng socket    + 2 RTT          <- handshake, RỒI request + response
tỉ số  = (dựng socket + 2·RTT) / (xử lý + RTT)
```

Khi RTT lớn dần, hai hằng số kia biến thành nhiễu và tỉ số → **đúng 2.0**. Kiểm lại bằng số:
ở RTT 20157µs, mô hình cho (320 + 40314)/(10 + 20157) = 2.015; ở RTT 40201µs cho 2.007. Đo được
2.00 và 2.00.

**Đây mới là kết luận thật của phase 0 về connection pool, và nó lật ngược cách tôi định báo cáo:**

> Tỉ số `dial/reuse` **tiệm cận 2.00x và dừng ở đó**, dù RTT có tệ đến đâu. Nó không đo lợi ích
> của pool — nó chỉ nói "một workload dial-mỗi-request trả 2 RTT, workload reuse trả 1 RTT".
> Trong khi đó **lượng phí phạm tuyệt đối tăng tuyến tính theo RTT**: 0.33ms ở loopback,
> 20.47ms ở RTT 20ms, 40.47ms ở RTT 40ms.

Nên nếu phase 5 báo cáo "pool cải thiện 36.69x" (số ở RTT 0) rồi sang môi trường thật thấy
"chỉ 2x" thì sẽ kết luận **ngược hoàn toàn với sự thật** — trong khi thực tế pool vừa tiết kiệm
nhiều hơn 60 lần. Đơn vị đúng không phải "bao nhiêu lần" mà là **1 RTT bị phí cho mỗi
connection dựng mới**.

Chi tiết phụ nhưng đáng ghi: phân phối cực chặt (p50 20.43 / p99 20.66). `netem delay 20ms`
trần không có jitter, không có mất gói — đó là một mạng **sạch một cách không thực tế**. Mạng
thật có jitter, và jitter là thứ tạo ra tail latency. Ghi nợ `P0-6`.

### 2026-09-03 — G8 sai, và cái sai đó xác nhận đúng nguyên tắc "kết luận phải là tỉ số"

Kết quả G3 làm nảy ra một nghi vấn về chính con số 36.69x của G2: lần đo đó chạy 2000 dial liên
tiếp hết tốc lực, và G6 đã **đo được** rằng dial đắt dần khi TIME_WAIT đầy. Vậy 36.69x có bị
chính hiệu ứng G6 làm phồng? Đăng ký G8 trước, rồi đo `n` = 100 / 500 / 2000 và theo dõi `tw`:

```console
$ for n in 100 500 2000; do echo "tw TRƯỚC: $(grep '^TCP:' /proc/net/sockstat)";     go run ./cmd/netlab -exp rtt -n $n -bufio=false; done

########## n=100 ##########
tw TRƯỚC: TCP: inuse 30 orphan 0 tw 25 alloc 70 mem 0
reuse 1 connection                 -             100    11.3µs     9.6µs    12.7µs    33.8µs    63.0µs      0
dial mới mỗi request               -             100   351.7µs   341.3µs   463.7µs   579.9µs   591.4µs      0
  [G2/G3] p50 dial / p50 reuse                            35.52x
tw SAU  : TCP: inuse 30 orphan 0 tw 126 alloc 70 mem 0
########## n=500 ##########
reuse 1 connection                 -             500    10.5µs    10.0µs    12.3µs    19.5µs    72.0µs      0
dial mới mỗi request               -             500   344.9µs   345.0µs   444.9µs   611.4µs    1.64ms      0
  [G2/G3] p50 dial / p50 reuse                            34.35x
tw SAU  : TCP: inuse 30 orphan 0 tw 627 alloc 70 mem 0
########## n=2000 ##########
reuse 1 connection                 -            2000    18.0µs    11.2µs    26.1µs    69.8µs    1.54ms      0
dial mới mỗi request               -            2000   295.0µs   303.9µs   377.9µs   517.1µs    1.32ms      0
  [G2/G3] p50 dial / p50 reuse                            27.01x
tw SAU  : TCP: inuse 30 orphan 0 tw 2618 alloc 70 mem 0
```

**Đọc kết quả:** `dial p50` = 341.3 → 345.0 → 303.9µs khi `tw` đi từ 25 lên 2618. **Phẳng**, và
nếu có xu hướng thì là xu hướng *giảm*. G8 sai: ở mức tw này TIME_WAIT không làm dial đắt hơn.
Hợp lý khi nhìn lại G6 — ở đó độ suy giảm chỉ xuất hiện khi `tw` vượt ~20000, tức ~70% không
gian 28232 port. 2618 mới là 9%.

**Nhưng có một thứ khác lộ ra, và nó lớn hơn G8:** `dial p50` ở đây là **303-345µs**, còn lần
đo G2 gốc là **861.7µs**. Cùng một lệnh, chênh **2.5x**. Và `reuse p50` cũng chênh đúng hướng
đó: 23.5µs (gốc) so với 9.6-11.2µs (giờ). **Cả hai vế cùng chậm ~2.5x** trong lần chạy gốc — máy
lúc đó đang chậm hơn toàn cục, không phải `Dial` đắt hơn.

Nên **tỉ số sống sót**: 36.69x (gốc) so với 35.52 / 34.35 / 27.01x (giờ). Còn **số tuyệt đối
thì không**: 861.7µs so với 341.3µs.

**Đang nghĩ gì:** đây là lần đầu nguyên tắc "*chốt lại bằng tỉ số, số tuyệt đối đổi theo máy*"
tự chứng minh bằng số đo của chính nó — và nó chứng minh còn mạnh hơn tôi viết ra: số tuyệt đối
đổi **2.5x ngay trên cùng một máy, cùng một lệnh, cách nhau 30 phút**. Nếu tôi đã viết
"dial tốn 861µs" vào CV thì con số đó sai 2.5x và tôi sẽ không có cách nào biết.

Và nó khớp một cách thoả mãn với mô hình dựng từ G3: phần phi-RTT của chi phí dial tính từ hai
lần chạy netem là **~313µs** (40.90ms − 2 × 20.157ms), còn đo trực tiếp ở RTT 0 với tw thấp là
**331.7µs** (341.3 − 9.6). Hai môi trường hoàn toàn khác nhau, cùng một con số. Chi phí dựng
socket trên máy này là **~320µs**, và 861µs kia là một artifact của lúc máy chậm.

Nợ mới `P0-7`: mọi số tuyệt đối cần median của N lần chạy, không phải một lần.

---

## Giả thuyết sai

Cột giá trị nhất của cả file.

| Tôi tưởng là | Thực tế là | Lệnh + output đã lật tẩy nó | Đã sửa thế nào |
|---|---|---|---|
| Ở RTT 20ms, `dial`/reuse sẽ **> 15x** (G3) | **2.00x**, và đúng 2.00x ở cả RTT 20ms lẫn 40ms — một hằng số tiệm cận, không phải một số đo | `scripts/pay-P0-1.sh` | Bỏ hẳn tỉ số làm đơn vị cho pool; đơn vị đúng là **1 RTT phí mỗi connection dựng mới** |
| `netem delay 20ms` trên `lo` cho RTT 20ms | Cho RTT **40.201ms**. `lo` áp qdisc cho **cả hai chiều**. Muốn RTT 20ms phải đặt `delay 10ms` | `ping` trước/sau ở hai cấu hình: 10ms→20.157ms, 20ms→40.201ms | Sửa `Makefile` `rtt-up` thành `delay 10ms` + ép in RTT đo được; trả xong câu hỏi treo của `P-env-1` |
| 36.69x của G2 bị TIME_WAIT làm phồng (G8) | Không. `dial p50` phẳng (341/345/304µs) khi `tw` 25→2618. Suy giảm của G6 chỉ đến khi `tw` > ~70% không gian port | `for n in 100 500 2000` + `grep '^TCP:' /proc/net/sockstat` | Bỏ giả thuyết; nhưng phát hiện thứ khác: xem hàng dưới |
| Số tuyệt đối trên **cùng một máy** thì ổn định, chỉ đổi khi sang máy khác | Đổi **2.5x** trên cùng máy, cùng lệnh, cách 30 phút (`dial p50` 861.7 → 341.3µs; `reuse` 23.5 → 9.6µs — cả hai vế cùng chậm). Tỉ số thì sống sót (36.69 → 35.52x) | so `bench/p0-rtt-rtt0.txt` với `bench/p0-rtt-G8-dial-rate.txt` | Nợ `P0-7`: số tuyệt đối phải là median của N lần chạy. Nguyên tắc "kết luận là tỉ số" vừa tự chứng minh |
| 2 `Write` chậm hơn 1 `Write` ~1.5-2x, đo là thấy | 1.02x, vì `bufio.Writer` gộp 2 `Write` thành 1 syscall. **Bộ đo sai, không phải kết luận.** | `netlab -exp syscall` (bufio=true) → 1.02x; thêm `-bufio=false` → 1.41x | Bắt buộc `-bufio=false` cho G1; in cảnh báo ngay trong output của `expSyscall` |
| Trên loopback RTT ~0 nên `Dial` ≈ reuse, "pool trông vô dụng" (bẫy tôi tự dựng cho mình) | **36.69x**. RTT chỉ là một phần chi phí dial; ~96% còn lại là tạo socket + connect + accept + lên lịch | `netlab -exp rtt -n 2000` → p50 861.7µs vs 23.5µs | Sửa lại nhận thức: pool tiết kiệm **hai** thứ khác nhau; phải đo cả hai riêng ra |
| Tỉ số là cách tóm tắt đúng cho lợi ích của pool | Ở RTT 20ms tỉ số sẽ **TỤT** (~2x) trong khi tiết kiệm tuyệt đối **TĂNG** (0.86ms → ~20.9ms). Đơn vị đúng là **RTT/request** | suy ra từ G2; chờ G3 xác nhận (nợ `P0-1`) | Mang cảnh báo này sang phase 5 trước khi phase 5 báo cáo sai |
| Không pool sẽ cạn ephemeral port và **báo lỗi** `EADDRNOTAVAIL` | 0 lỗi trong 155 giây. Trần báo bằng **thông lượng tụt đơn điệu** 1502→738→601→455 conn/s, CPU 29.5% | 4 lần `netlab -exp limits`, xem mục G6 | Sửa lại `expLimits` để in CPU%; ghi nợ `P0-4` vì vòng dial tuần tự chưa loại được nghi phạm thứ hai |
| Ephemeral port exhaustion đo được trên `127.0.0.1` | Không. `tcp_tw_reuse = 2` = *chỉ bật cho loopback* ⇒ kernel tái dùng TIME_WAIT ngay | `cat /proc/sys/net/ipv4/tcp_tw_reuse` → `2`; đổi sang `-addr 172.19.62.65` thì conn/s tụt hẳn | Mọi thí nghiệm về TIME_WAIT/port phải dùng IP **không thuộc 127/8**; `envcap.sh` đã in `tw_reuse` sẵn từ đầu — tôi in ra mà không đọc |
| Loopback gần như không có delayed ACK nên khó tái hiện spike 40ms (tôi tự viết cảnh báo này trong code) | Tái hiện hoàn hảo: 44.03ms và cực chặt (p50 44.03 / p99 47.99) | `netlab -exp nagle` | Sửa lại comment trong `exp_nagle.go` |
| `netlab -exp syscall -n 5000 -bufio=false` bị **treo** (đã đi `pgrep` tìm deadlock) | Không treo. Đang chạy 5000 × 44ms = ~200s. "Sự cố" chính là số đo của G7 | `pgrep -af netlab` cho thấy tiến trình vẫn sống | `nagleN = n/50`, kèm comment giải thích con số 50 từ đâu ra |
| `-exp mem` chạy được trong `-all` cùng các thí nghiệm khác | **RSS/conn = -7.99 KB**, số âm. Các thí nghiệm trước phình RSS rồi scavenger của Go trả bộ nhớ về OS trong lúc đang đo ⇒ mốc nền cao hơn mức thật | `netlab -all -bufio=false -conns 500` → `RSS/conn : -7.99 KB` | `expMem` từ chối kết quả khi `after <= base`; `-all` **tự fork process sạch** để đo G5 |

## Số đo

**Đo 2026-09-03, commit `_______`, WSL2 6.6.87.2 / i5-1235U / 6 core, `ulimit -n` 65536,
RTT ~0 (không netem), `-role both` (client+server cùng process, KHÔNG ghim core riêng).**

| Hiện tượng | Số đo | Lệnh |
|---|---|---|
| Round-trip 1 `Write`, resp 1KB, connection reuse | p50 10.9µs · p99 55.0µs | `netlab -exp syscall -n 5000 -bufio=false` |
| Round-trip 2 `Write` (không bufio) | p50 15.3µs · p99 58.6µs | ↑ |
| Round-trip 2 `Write`, Nagle bật ở server | **p50 44.01ms** | ↑ |
| `Dial` mới mỗi request (gồm cả dial) | p50 861.7µs · p99 3.84ms — **nhưng đo lại 30 phút sau ra 341.3µs**, xem `P0-7` | `netlab -exp rtt -n 2000 -bufio=false` |
| Reuse 1 connection | p50 23.5µs · p99 188.9µs — đo lại: 9.6µs | ↑ |
| Chi phí **dựng socket** (phần phi-RTT của dial) | **~320µs** — khớp từ hai môi trường độc lập: 313µs (suy từ netem) và 331.7µs (đo trực tiếp RTT 0, tw thấp) | `pay-P0-1.sh` + `-exp rtt -n 100` |
| RTT thật khi `netem delay 10ms` trên `lo` | **20.157ms** (= 2 × delay) | `ping -c 5 127.0.0.1` |
| RTT thật khi `netem delay 20ms` trên `lo` | **40.201ms** (= 2 × delay) | ↑ |
| Reuse / dial ở **RTT 20.157ms** | p50 20.43ms / 40.90ms | `netlab -exp rtt -n 300 -bufio=false` (có netem) |
| Reuse / dial ở **RTT 40.201ms** | p50 40.49ms / 80.96ms | `netlab -exp rtt -n 150 -bufio=false` (có netem) |
| p99, quá tải 1.2x, **closed-loop 1 conn** | 1.87ms (đạt 754/1200 rps) | `netlab -exp omission -rate 1200 -svc 1ms -workers 1` |
| p99, quá tải 1.2x, **closed-loop 50 conn** | 100.71ms (đạt 733/1200 rps) | ↑ |
| p99, quá tải 1.2x, **open-loop rate cố định** | **3.35s** (đạt 714/1200 rps, 2960 drop) | ↑ |
| RSS/conn, 10k conn rỗi, có `bufio` server | 19.40 KB (cặp client+server) | `netlab -exp mem -conns 10000 -bufio=true` |
| RSS/conn, 10k conn rỗi, không `bufio` server | 9.09 KB | `netlab -exp mem -conns 10000 -bufio=false` |
| `StackSys`/conn, không `bufio` | 2.01 KB (= `runtime._StackMin`) | ↑ |
| conn/s không pool, `tw` nền thấp → cao (qua eth0 IP) | 1502 → 738 → 601 → 455 | `netlab -exp limits -addr 172.19.62.65:911x` |
| CPU toàn máy trong lúc conn/s bị chặn ở 614 | 29.5% / 6 core | `netlab -exp limits -duration 40s -addr 172.19.62.65:9114` |

Chốt lại bằng **tỉ số** (số tuyệt đối đổi theo máy, tỉ số thì bền):

| Tỉ số | Giá trị | Nói lên điều gì |
|---|---|---|
| 2 `Write` / 1 `Write`, **không** bufio | 1.41x (p50) · 1.07x (p99) | Một syscall thêm là thật nhưng nhỏ; **nó không quyết định tail latency** |
| 2 `Write` / 1 `Write`, **có** bufio | 1.02x | Đây là số của **bộ đo sai**. Giữ lại để nhớ hình dạng của nó |
| Nagle write-write-read / NoDelay | 638x (p99) · 2875x (p50) | Hai tính năng đúng ghép thành deadlock 40ms. `SetNoDelay(true)` là bắt buộc, không phải tinh chỉnh |
| `Dial` / reuse, **RTT ~0** | **36.69x** (p50) | Pool tiết kiệm chi phí *dựng socket*, tách biệt với chi phí *RTT*. Ở RTT 0 chỉ thấy được vế đầu |
| `Dial` / reuse, **RTT 20.157ms** | **2.00x** (tiết kiệm 20.47ms) | Dự đoán ở `P0-1` đúng. Tỉ số **tụt**, tiết kiệm tuyệt đối **tăng 62x** |
| `Dial` / reuse, **RTT 40.201ms** | **2.00x** (tiết kiệm 40.47ms) | Trùng khít 2.00x ở RTT gấp đôi ⇒ đây là **hằng số tiệm cận**, không phải số đo của môi trường |
| RTT đo được / `netem delay` đặt | **2.00x** (20.157/10 và 40.201/20) | Trên `lo`, netem áp delay cho **cả hai chiều**. Muốn RTT X thì đặt `delay X/2` |
| `dial p50` cùng lệnh, cách nhau 30 phút | **2.5x** (861.7 → 341.3µs) | Số tuyệt đối không ổn định **ngay trên cùng một máy**. Đây là bằng chứng cho chính nguyên tắc "kết luận là tỉ số" |
| p99 open-loop / p99 closed-loop 1 conn | **1787x** | Cùng một hệ thống, cùng một lúc. Chọn dụng cụ sai thì p99 báo về sai 3 bậc độ lớn |
| p99 open-loop / p99 closed-loop 50 conn | 33x | Tăng concurrency của closed-loop **giảm** được sai lệch nhưng không xoá được |
| rps đạt / rps yêu cầu, cả 3 dụng cụ | 0.60-0.63 | Không đọc cột này thì đang báo cáo p99 của một tải khác với tải mình tưởng |
| RSS/conn / 2KB stack | 9.70x (có bufio) · 4.54x (không) | Chi phí thật của một connection rỗi là **buffer**, không phải goroutine |
| RSS/conn có bufio / không bufio | 2.13x (hiệu tuyệt đối **10.31 KB/conn**) | Giá riêng của `bufio` server, đo bằng cách bật/tắt một flag |
| conn/s không pool: `tw` thấp / `tw` cao | 3.3x (1502 → 455) | Trần OS không báo bằng lỗi, nó báo bằng **thông lượng tụt dần trong khi CPU rỗi** |

## Invariant + lệnh kiểm chứng

Phase 0 chưa có code proxy nên chưa có invariant của sản phẩm. Nhưng có invariant của **bộ đo**:
một bộ đo sai tạo ra kết luận sai suốt 9 phase còn lại — phase này đã chứng minh điều đó **ba lần**.

| Invariant của bộ đo | Cài ở | Lệnh kiểm chứng | Kết quả |
|---|---|---|---|
| Mọi số latency dưới tải đến từ open-loop rate cố định | `cmd/netlab/exp_omission.go` | output in cột `loop` = `open` và cặp `yêu cầu`/`đạt được` | ✅ |
| Bộ đo open-loop **đếm drop**, không im lặng chờ khi hết conn rỗi | `exp_omission.go` (`select` + `dropped`) | output: "2960 request bị drop" | ✅ |
| Phép đo RSS chỉ chạy trong process sạch, và **từ chối** kết quả âm | `exp_mem.go` (`after <= base`) + `main.go` `forkMem` | `netlab -all` in "[fork process sạch để đo G5]" | ✅ |
| Khối môi trường in ở **đầu mọi lần chạy**, không chép tay | `cmd/netlab/main.go` `printEnv` | `netlab -exp rtt \| head -20` | ✅ |
| Đo G1 phải tắt `bufio` phía server | `expSyscall` in cảnh báo dưới bảng | so 1.02x (bufio) vs 1.41x (không) | ✅ |
| Thí nghiệm TIME_WAIT/port dùng IP **không** thuộc 127/8 | `-addr <eth0-ip>` | `cat /proc/sys/net/ipv4/tcp_tw_reuse` = 2 | ✅ |
| Generator và thứ bị đo không dùng chung core | `scripts/phase0-run.sh` (`taskset`) | — | ❌ **chưa** ở `-role both`: cùng process. Nợ `P0-3` |
| `tc qdisc` đã được tháo sau mỗi thí nghiệm RTT | `scripts/pay-P0-1.sh` `trap ... EXIT INT TERM` | `tc qdisc show dev lo` → `qdisc noqueue 0: root` | ✅ |
| RTT được **đo** bằng `ping`, không được suy từ tham số `netem` | `pay-P0-1.sh` `ping_rtt()`, nhãn `-tag` chứa RTT đo được | output: `RTT ĐO ĐƯỢC = 20.157 ms` với `delay 10ms` | ✅ — và nó đã bắt được sai số 2x |
| Thí nghiệm 10k conn không bị `ulimit` chặn | `expMem` cảnh báo nếu `conns*2+64 > ulimit` | `ulimit -n` = 65536 ≫ 20064 | ✅ |
| Trần `MaxFrameSize` kiểm **trước** `make([]byte, n)` | `cmd/netlab/wire.go` `readFrame` | đọc code; phase 1 sẽ fuzz | ✅ |

## Chạy lại phase này ở MÁY KHÁC

Ba mức, mức càng cao càng gần sự thật:

```bash
# Mức 1 — máy mới, vẫn loopback. Xác lập mức nền của máy đó.
git clone <repo> && cd edge-gate
./scripts/envcap.sh                    # BẮT BUỘC trước tiên. So với bench/env-*.txt của máy cũ
./scripts/phase0-run.sh rtt0           # -> bench/p0-<host>-rtt0-*.txt

# Mức 2 — vẫn một máy, nhưng bơm RTT giả. Trả nợ P0-1.
make rtt-up                            # sudo tc qdisc add dev lo root netem delay 20ms
./scripts/phase0-run.sh rtt20ms
make rtt-down                          # KHÔNG được quên
tc qdisc show dev lo                   # phải thấy `noqueue`

# Mức 3 — HAI máy thật. RTT thật, không giả lập, và hết luôn bẫy chung-CPU.
# máy A:
netlab -role server -addr :9000 -workers 1 -bufio=false
# máy B:
ping -c 5 <A>                          # ghi RTT thật vào diary TRƯỚC khi đo
netlab -role client -addr <A>:9000 -all -tag "lan-rtt<x>ms"
```

**Đừng so số tuyệt đối giữa hai máy — chỉ so cột "Tỉ số".** Nếu một tỉ số đổi hẳn bậc độ lớn
khi sang máy khác thì đó là một mục mới của bảng "Giả thuyết sai", không phải một con số để
sửa lại cho khớp.

Riêng ba tỉ số này **dự kiến sẽ đổi** khi rời WSL2, và đổi thế nào là một phép kiểm tra xem
mình có hiểu đúng hay không:

| Tỉ số | Trên WSL2 loopback | Dự đoán trên Linux thuần / 2 máy | Vì sao |
|---|---|---|---|
| `Dial` / reuse | 36.69x | **thấp hơn** ở RTT 0 (WSL2 ảo hoá network stack nên dial đắt bất thường), **cao hơn nhiều** khi có RTT thật | tách được chi phí socket khỏi chi phí RTT |
| conn/s không pool | 1502 → 455 | trần **rõ ràng hơn**, có thể ra hẳn `EADDRNOTAVAIL` nếu `tcp_tw_reuse=0` | `tw_reuse=2` không còn che |
| Nagle spike | 44.03ms | ~40ms, chặt hơn | delayed ACK timeout của Linux là 40ms |

## Đọc gì

- **Gil Tene — "How NOT to Measure Latency"**. Đọc *sau* khi thấy con số 1787x ở trên thì hiểu
  ngay, đọc trước thì chỉ là một khái niệm. Đây là thứ tự đúng.
- `man 7 tcp` — `TCP_NODELAY`, `tcp_tw_reuse` (đọc lại kỹ giá trị `2`), `tcp_max_syn_backlog`.
- Beej's Guide — socket, `SO_REUSEADDR`, `TIME_WAIT`.
- `man 2 splice` — chưa dùng tới phase 9, nhưng biết nó tồn tại từ bây giờ.

## Rút ra

**1. Ghi bằng một `Write` thay vì hai đáng giá 1.41x, nhưng đó không phải phần quan trọng.**
Phần quan trọng là 44ms: `write-write-read` cộng với Nagle là một deadlock, không phải một
khoản kém hiệu quả. Nên quy tắc mang sang phase 2/3 không phải "nên gộp Write cho nhanh" mà là
**"không bao giờ để một request/response rời process bằng hai lần `Write` trên socket trần"**.
Và `SetNoDelay(true)` phải viết tường minh ở cả hai hướng, kể cả khi Go đã mặc định bật.

**2. Connection pool tiết kiệm hai thứ khác nhau, và tôi mới đo được một.** Ở RTT 0, dial đắt
hơn reuse **36.69x** — toàn bộ là chi phí dựng socket, không phải RTT. Chi phí RTT vẫn vô hình
cho đến khi trả nợ `P0-1`. Bài học lớn hơn con số: **giả thuyết G2 của tôi đúng về cơ chế (RTT
biến mất trên loopback) nhưng sai về kết luận, vì tôi ngầm cho rằng RTT là chi phí duy nhất.**
Đây là dạng sai khó thấy nhất — suy luận đúng, tiền đề thiếu.

**3. Coordinated omission trên máy này là 1787x, và nó không phải hiện tượng ở đuôi.** Nó làm
sai lệch **toàn bộ** báo cáo: cùng một hệ thống quá tải, p99 báo về 1.87ms hay 3.35s là do chọn
dụng cụ. Cột phải xem trước mọi percentile là **`rps yêu cầu` vs `đạt được`** — cả ba dụng cụ
đều chỉ đạt ~60%, nghĩa là con số nào ở đây cũng là p99 của một tải khác với tải mình tưởng
đang áp. Từ phase này trở đi, không có con số latency nào trong repo được viết ra mà không kèm
`closed/open-loop`.

**4. Một connection rỗi tốn 19.40 KB, và ~10.31 KB trong đó là `bufio`.** Không phải goroutine:
`StackSys` đo được đúng 2.01 KB/conn khi không có `bufio`, khớp `runtime._StackMin`. 10k
connection rỗi = 194 MB. Điều này mở ra một hướng tôi chưa nghĩ đến trước khi đo: ngoài
`sync.Pool` ở phase 9, còn cách **hoãn cấp buffer đến khi connection có byte đầu tiên** — với
10k connection rỗi thì đó là chênh lệch giữa 194 MB và ~20 MB.

**5. Trần của OS không báo bằng exception, nó báo bằng thông lượng tụt dần trong khi CPU rỗi.**
1502 → 455 conn/s, 0 lỗi, CPU 29.5%. Đây là hình dạng khó chẩn đoán nhất trong vận hành, và
là câu trả lời thật cho "vì sao cần connection pool": không phải để nhanh hơn vài phần trăm,
mà để **không tiêu ephemeral port với tốc độ 1 port/request**. Cần nói thêm cho trung thực:
vòng dial của tôi là tuần tự nên chưa loại được nghi phạm "trần ở chính client" (nợ `P0-4`).

**6. Và bài học bao trùm cả 5 điều trên: ba trong sáu giả thuyết sai không sai vì tôi thiếu
kiến thức, mà vì bộ đo che mất thứ cần đo.** `bufio` che chi phí syscall *và* che spike 44ms.
`tcp_tw_reuse=2` che port exhaustion. Loopback che RTT. Cả ba đều nằm sẵn trong output của
`envcap.sh` và của chính netlab — tôi đã **in ra và không đọc**. Thủ tục trong SKILL.md ("số đo
trông vô lý ⇒ giả định bộ đo sai trước") đã cứu được lần đầu; hai lần sau tôi tìm ra chậm hơn
vì đã bắt đầu tin vào bộ đo của mình. Đó là thứ phải giữ cảnh giác suốt 9 phase còn lại.

## Errata (2026-09-03, khi trả P0-2 / P0-7)

1. **G4 open-loop đo trên một generator có race.** `expOmission` open-loop chọn conn theo
   `i % len(pool)` nhưng semaphore chỉ đếm slot; khi latency > 427 ms (512 conn / 1200 rps) hai
   goroutine đọc chung một `bufio.Reader`. Ở rate 100000 nó panic ngay; ở 1200 nó âm thầm. Kết luận
   định tính của G4 (open-loop p99 ≫ closed-loop p99) vẫn đứng — đo lại sau khi sửa: **377x** vs
   closed-1 và **8.82x** vs closed-50 (`bench/p0-2-capacity.txt`) — nhưng con số cũ không tin được.
2. **Tỉ số không bất biến như đã viết ở "Rút ra".** 5 lần chạy hôm nay cho dial/reuse **15.8–18.2x**;
   phase 0 ghi 36.69x và 35.52x. Số tuyệt đối lệch 2.5–6x, tỉ số lệch 2x. Câu đúng: *chiều* và *bậc
   độ lớn* của tỉ số mang được sang phiên khác; *giá trị* của tỉ số thì không.
3. Mốc quá tải của `make netlab-omission` là **1.63x** capacity thật (1200 / 737), không phải 1.2x
   như thiết kế. Đã đổi về 885.

## Nợ kỹ thuật

Chi tiết và lệnh trả nợ: [`../docs/debts.md`](../docs/debts.md).

- [ ] **P0-1** · 📏 G3 chưa đo: `Dial` vs reuse ở RTT 20ms. Cần sudo. Đây là **nửa còn lại** của
      kết luận về connection pool — thiếu nó thì phase 5 sẽ báo cáo sai đơn vị.
- [x] ~~**P0-2**~~ · ✅ **đã trả 2026-09-03**: đo được **737 rps** (p50 1.34 ms), mốc 1.2x = 885 rps.
      Trả kèm một bug: generator open-loop cho hai goroutine dùng chung một conn — xem errata.
- [ ] **P0-3** · 📏 `-role both` để client và server cùng process, cùng core ⇒ mọi số RSS là của
      **cặp**, và mọi số latency có nhiễu tranh CPU. Chạy lại bằng `-role server`/`-role client`.
- [ ] **P0-4** · 📏 Vòng dial của `expLimits` là tuần tự nên chưa tách được "trần ở không gian
      port" khỏi "trần ở client một luồng".
- [x] ~~**P0-5**~~ · ✅ **đã trả qua P1-1**: `readFrame` riêng đã xoá, netlab dùng `frame.Decoder` có fuzz.

- [x] ~~**P0-1**~~ · ✅ **đã trả**: G3 = 2.00x ở RTT 20.157ms và 40.201ms. Trả kèm luôn câu hỏi
      treo của `P-env-1` (netem trên `lo` áp cả hai chiều).
- [ ] **P0-6** · 📏 `netem delay Xms` trần cho một mạng **sạch không thực tế**: phân phối chặt
      (p50 20.43 / p99 20.66), không jitter, không mất gói. Mà jitter mới là thứ tạo tail latency.
      Cần chạy lại phase 5-7 với `netem delay 10ms 3ms distribution normal loss 0.1%`.
- [x] ~~**P0-7**~~ · ✅ **đã trả 2026-09-03**: median 5 lần dial p50 = **138.8 µs** (phase 0: 861.7 / 341).
      Tỉ số cũng đổi 36.69x → ~17x ⇒ chỉ **thứ tự độ lớn** của tỉ số mang được. Xem errata.

**Đã trả trong phase:** phép đo RSS âm (sửa `expMem` + `forkMem`), biến thể Nagle chạy 200 giây
(`nagleN = n/50`), G1 đo qua `bufio` (bắt buộc `-bufio=false` + cảnh báo in trong output), G6
chạy trên loopback vô nghĩa (chuyển sang IP của interface thật), thiếu bằng chứng CPU cho G6
(thêm đọc `/proc/stat`).
