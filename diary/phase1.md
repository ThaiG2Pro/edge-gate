# Phase 1 — Framing: TCP không có ranh giới tin nhắn

- **Thời lượng dự kiến:** 1 ngày · **thực tế:** ~0.5 ngày (3 turn: code / run+fix / diary)
- **Bắt đầu:** 2026-09-03 · **Kết thúc:** 2026-09-03
- **Trạng thái:** ✅ xong. Hai bài quyết định xanh, fuzz 120s sạch, **bài phản chứng đỏ đúng chỗ**
- **Commit:** `_______` — repo chưa init git; mọi số đo thuộc cây làm việc lúc viết file này

> **Đường đi thô, kể cả ngõ cụt:** [`phase1-log.md`](phase1-log.md). File này là bản biên tập.
>
> **Quy tắc ghi nhật ký:** mọi con số, mọi kết luận đều phải kèm **lệnh shell sinh ra nó**
> và **output thật**. Luật đầy đủ: [`../skills/diary/SKILL.md`](../skills/diary/SKILL.md).

## Môi trường

Cùng máy phase 0 (`bench/env-GOTIT-00663.txt`). Phase này không có số latency, chỉ có số
**đếm** (lần Read, byte, byte cấp phát) nên ba bẫy đo mạng không áp dụng — trừ một chỗ: mọi
test chạy trên **TCP thật qua loopback**, không `net.Pipe`, xem lý do ở G6.

```console
$ uname -srmo && go version && nproc && ulimit -n
Linux 6.6.87.2-microsoft-standard-WSL2 x86_64 GNU/Linux
go version go1.26.2 linux/amd64
6
1048576
```

## Mục tiêu phase

Viết tầng cắt dòng byte TCP thành tin nhắn (`internal/frame`) sao cho hai sự thật của TCP —
**Read trả ít hơn cần** và **Read trả nhiều frame dính nhau** — không còn là việc của tầng trên.
Và cài bất biến **I2**: trần `length` kiểm **trước** `make()`, có bằng chứng là bài phản chứng đỏ.

## Câu hỏi phải trả lời được (viết trước khi code)

1. Vì sao `io.ReadFull` là bắt buộc, và một decoder không dùng nó chết ở byte thứ mấy?
2. Coalescing nhìn thấy ở đâu? Decoder đúng có "thấy" nó không?
3. Bài phản chứng cho I2 đỏ vì lý do gì — và có đỏ vì **đúng** lý do không?
4. `make([]byte, 0xFFFFFFFF)` có giết process không? (câu này có câu trả lời bất ngờ, xem G5)
5. I2 áp cho chiều nào — đọc, hay cả ghi?

## Giả thuyết đăng ký TRƯỚC khi đo

Phase này ít số hơn phase 0 nên các giả thuyết là về **con số đếm** mà lab sẽ in. G1, G2, G6 nằm
nguyên văn trong comment của `cmd/framelab/main.go` bản đầu (trước lần chạy đầu); G3-G5, G7 đăng
ký ở turn 2 trước khi chạy lệnh tương ứng. Chi tiết trong log.

| # | Giả thuyết | Kỳ vọng | Kết quả | Đúng? |
|---|---|---|---|---|
| G1 | coalesce: 3 frame trong 1 Write ⇒ decoder cần **ít hơn 1 Read/frame** | Read/frame < 1 | **1.67** | ❌ |
| G2 | dribble 1 byte/10ms ⇒ mỗi byte một Read | 62 Read cho 62 byte, Read/frame = 20.67 | 62 Read, 20.67 | ✅ |
| G3 | tắt `checkLength` ⇒ `TestCapBeforeAlloc` đỏ vì **thiếu `ErrTooBig`** | đỏ, 1 lý do | đỏ, **2 lý do**: `unexpected EOF` + cấp phát 4294967376 byte | ✅ nửa — đỏ đúng, nhưng lý do dự đoán là lý do yếu |
| G4 | fuzz 120s không tìm được crash (decoder chỉ có 3 nhánh lỗi) | 0 crash | 0 crash, 1.58M execs | ✅ |
| G5 | `make([]byte, 4 GiB)` trong bài nodefense sẽ **OOM hoặc rất chậm** trên máy 12 GB | test chết / >1s | 0.01s trên heap sạch — **nhưng 7.138s + RSS 4.27 GB** nếu span đè lên 64 MB trang bẩn (P1-6) | ❌ rồi ✅ một nửa: sai ở heap sạch, đúng ở process thật |
| G6 | `net.Pipe` không tái tạo được cả dribble lẫn coalesce (giao nguyên khối mỗi Write) | Pipe khác TCP ở cả hai bài | **Đo (P1-4):** Pipe tái tạo được short read 7/7 **và** nhiều-frame-một-Read 62/62. Chỉ khác ở Write bất đồng bộ và gom Write rời | ❌ một nửa — lý do đúng (TCP thật) nhưng lời giải thích sai |
| G7 | dribble với `-interval 0` vẫn cho Read thô đầu = 1 byte (NoDelay đã bật) | 1 byte | **11 byte** | ❌ |

**4/7 sai** (G6 sai một nửa, tính là sai — chấm ✅ theo tài liệu rồi đo ra khác). G1 và G7 sai theo cùng một hướng: tôi nghĩ số lần `Read` phản ánh gói tin. Nó không —
nó phản ánh **decoder xin gì** (G1) và **scheduler cho client chạy được bao lâu trước khi server
kịp Read** (G7).

## Deliverable

| Deliverable | Lệnh | Kết quả |
|---|---|---|
| Sender nhỏ giọt | `go test ./internal/frame -run TestDribble -race` | PASS 0.65s |
| Sender dính gói | `go test ./internal/frame -run TestCoalesce -race` | PASS |
| Fuzz | `make fuzz-frame` | PASS, 1580258 execs / 120s |
| **Phản chứng I2** | `make framelab-nodefense` | **FAIL** đúng nghĩa: 4294967376 byte cấp phát |
| Lab nhìn bằng mắt | `make framelab` | `bench/p1-framelab-GOTIT-00663.txt` |

## Reproduce toàn bộ phase

```bash
go test ./internal/frame/ -count=1 -race -v          # 8 test + fuzz seed
make fuzz-frame                                      # 120s, cần -fuzzminimizetime 1s
make framelab-nodefense                              # PHẢI đỏ
make framelab | tee bench/p1-framelab-$(hostname).txt
go run ./cmd/framelab -mode dribble -interval 0      # G7
```

Không cần `tc`, không cần `taskset`: phase này không có số latency nào.

## Nhật ký

### 2026-09-03 — Turn 1: `internal/frame` + test + lab, và G1 sai ngay lần chạy đầu

```console
$ go test ./internal/frame/ -count=1 -race
ok  	github.com/thaivro/edgegate/internal/frame	1.660s

$ go test ./internal/frame/ -run 'TestCapBeforeAlloc|TestCustomMax' -count=1 -tags nodefense
--- FAIL: TestCapBeforeAlloc (0.01s)
    frame_test.go:136: err = unexpected EOF, want ErrTooBig
    frame_test.go:139: decoder cấp phát 4294967376 byte cho một frame bị từ chối
--- FAIL: TestCustomMax (0.00s)
    frame_test.go:149: err = <nil>, want ErrTooBig
FAIL

$ for m in dribble coalesce oversize; do go run ./cmd/framelab -mode $m; done
mode=dribble  gửi 3 frame = 62 byte  (header 10 byte/frame)
client Write : 62 lần
server Read  : 62 lần, 62 byte  (nhỏ nhất 1, lớn nhất 1 byte/Read)
Read/frame   : 20.67
server nhận  : 3 frame trong 667ms
✔ tách đúng ranh giới
mode=coalesce  gửi 3 frame = 62 byte  (header 10 byte/frame)
client Write : 1 lần
server Read  : 5 lần, 62 byte  (nhỏ nhất 5, lớn nhất 27 byte/Read)
Read/frame   : 1.67
server nhận  : 3 frame trong 1ms
✔ tách đúng ranh giới
mode=oversize ...
server từ chối: frame: length vượt trần: 4294967295 > 65536  ✔ (không cấp phát)
```

**Đọc kết quả:** Dribble đúng G2 tới từng con số: 62 byte, 62 Read, min = max = 1. Header 10 byte
cần 10 lần Read — decoder nào làm `conn.Read(hdr)` một lần rồi parse là chết ở byte thứ 2.

Coalesce **sai G1**: 5 Read cho 3 frame, không phải < 3. Nhìn `nhỏ nhất 5, lớn nhất 27`: đó chính
là kích cỡ **payload** của frame 1 và frame 3. Tức 5 Read = hdr(10) · payload(5) · hdr(10) ·
hdr(10) · payload(27). Decoder dùng `ReadFull` với buffer đúng cỡ nên kernel chỉ giao đúng cỡ đó,
dù trong socket buffer đang có sẵn cả 62 byte. **Số lần Read do decoder xin quyết định, không do
gói tin.** Coalescing tồn tại nhưng **vô hình với decoder đúng** — mà đó chính là mục tiêu.

Hệ quả cho bộ đo: `Read/frame` không đo được coalescing. Muốn *thấy* nó phải làm một `Read` thô với
buffer lớn **trước** decoder, rồi nối phần đọc được lại bằng `io.MultiReader`.

Phản chứng đỏ, và đỏ **hai lý do** thay vì một như G3 dự đoán. Lý do tôi dự đoán (`want ErrTooBig`)
hoá ra yếu: nó đỏ vì `ReadFull` đứt ở EOF, tức test sẽ đỏ với **bất kỳ** lỗi nào, kể cả khi
decoder cấp phát rồi mới lỗi. Lý do mạnh là dòng 139: `TotalAlloc` nhảy 4294967376 byte =
4 GiB + 80 byte. Không có kiểm `MemStats` thì bài phản chứng đỏ nhưng **không chứng minh
"trước make()"**, chỉ chứng minh "có lỗi".

**Đang nghĩ gì:** sửa lab để có Read thô. Và câu hỏi 4: máy này vừa cấp phát 4 GiB trong 0.01s
mà không sao — vì sao?

### 2026-09-03 — Turn 1 (tiếp): Read thô đầu tiên

```console
$ for m in dribble coalesce oversize; do go run ./cmd/framelab -mode $m | grep 'Read thô'; done
Read thô đầu : 1 byte  (TCP giao bao nhiêu là tuỳ nó — đây là lý do phải ReadFull)
Read thô đầu : 62 byte  (TCP giao bao nhiêu là tuỳ nó — đây là lý do phải ReadFull)
Read thô đầu : 10 byte  (TCP giao bao nhiêu là tuỳ nó — đây là lý do phải ReadFull)
```

**Đọc kết quả:** 1 vs 62 — cùng 62 byte, cùng 3 frame, cùng loopback; khác duy nhất là client
gọi `Write` 62 lần hay 1 lần. Đây là hình ảnh của "TCP là dòng byte": ranh giới `Write` của bên gửi
**không tồn tại** ở bên nhận.

### 2026-09-03 — Turn 2: chạy mạnh hơn, và hai bug tìm bằng mắt

```console
$ go test ./internal/frame/ -count=20 -race
ok  	github.com/thaivro/edgegate/internal/frame	(240 passed)

$ go test ./internal/frame/ -run '^$' -fuzz FuzzFrameDecode -fuzztime 10s -fuzzminimizetime 1s -tags nodefense
failure while testing seed corpus entry: FuzzFrameDecode/seed#3
--- FAIL: FuzzFrameDecode (0.03s)
        frame_test.go:265: cấp phát 4294990520 byte cho input 10 byte
FAIL

$ go run ./cmd/framelab -mode dribble -interval 0 | grep -E 'Read'
Read thô đầu : 11 byte  (TCP giao bao nhiêu là tuỳ nó — đây là lý do phải ReadFull)
server Read  : 6 lần, 62 byte  (nhỏ nhất 1, lớn nhất 27 byte/Read)
Read/frame   : 2.00

$ make fuzz-frame          # bench/p1-fuzz-120s.txt
fuzz: elapsed: 3s, execs: 36270 (12088/sec), new interesting: 0 (total: 16)
fuzz: elapsed: 1m0s, execs: 849282 (16253/sec), new interesting: 2 (total: 18)
fuzz: elapsed: 2m0s, execs: 1580258 (5285/sec), new interesting: 4 (total: 20)
ok  	github.com/thaivro/edgegate/internal/frame	120.175s
```

**Đọc kết quả:** Fuzz với `nodefense` đỏ ngay ở **seed corpus** (seed#3 = header có length
`0xFFFFFFFF`), chưa cần fuzzer sinh gì — lời hứa "fuzz PHẢI đỏ" trong `limit.go` đã được kiểm thay
vì chỉ nói. Con số 4294990520 khác lần trước (4294967376) vì `TotalAlloc` cộng cả rác của
harness fuzz; phần 4 GiB thì giống.

G7 sai: `-interval 0` cho **11 byte** ở Read đầu, không phải 1. `SetNoDelay(true)` chỉ ngăn
**Nagle** gom; nó không ngăn việc client `Write` được 11 lần trước khi goroutine server được
lên lịch để `Read`. Coalescing có **hai** nguồn — Nagle (bên gửi) và tốc độ tương đối của hai
bên — và NoDelay chỉ tắt một.

Fuzz 120s: 1.58M execs, 20 corpus entries, 0 crash. **Nhưng** đọc code tìm được hai bug mà 1.58M
lần chạy không chạm tới:

1. `Append` làm `uint32(len(f.Payload))` im lặng. Payload 4 GiB + 1 byte ⇒ header hợp lệ với
   `length = 1` ⇒ decoder bên kia đọc lệch ranh giới từ đó trở đi. Fuzz không thấy vì fuzz chỉ mổ
   **decoder**; bug nằm ở **encoder**. I2 mới cài ở chiều đọc; chiều ghi để hở.
2. `framelab` có hai channel `done` và `firstRead`; đường lỗi của `Accept`/Read đầu chỉ gửi
   `done` ⇒ `main` treo vĩnh viễn ở `<-firstRead`. Ba mode đều xanh vì không mode nào đi qua
   đường lỗi.

```console
$ go test ./internal/frame/ -count=1 -race -run 'TestPayloadOverUint32|TestEncode' -v
--- PASS: TestPayloadOverUint32 (0.03s)
--- PASS: TestEncodeIsOneWrite (0.00s)
ok  	github.com/thaivro/edgegate/internal/frame	1.180s
```

**Đọc kết quả:** `TestPayloadOverUint32` cấp phát `make([]byte, 1<<32)` thật và chạy trong
0.03s. Đây là câu trả lời cho câu hỏi 4 và cho G5 sai: `make` một slice lớn lấy trang từ `mmap`
mới, đã zero sẵn, **chưa chạm** ⇒ không tốn RSS, không tốn thời gian. Máy 12 GB "cấp phát" 4 GiB
mà không sao — và đó cũng là lý do bài nodefense không giết máy. Điều này **không** làm I2 kém
quan trọng đi: process thật sẽ `ReadFull` vào slice đó, tức **chạm** từng trang khi attacker gửi
byte tới, hoặc giữ 4 GiB địa chỉ ảo × N connection cho tới khi `vm.overcommit` nói không.

**Đang nghĩ gì:** cả hai bug đều ở **đường không có bài chạy nào đi qua**. Fuzz mổ decoder rất kỹ
và hoàn toàn mù với encoder; lab chạy 3 đường xanh và mù với mọi đường đỏ.

### 2026-09-03 — Trả P1-1 và P1-2 ngay trong ngày

```console
$ grep -n "func readFrame" cmd/netlab/*.go
0 matches
$ go run ./cmd/netlab -exp syscall -n 5000 -bufio=false | tee bench/p1-netlab-on-frame-GOTIT-00663.txt
1 Write (resp 1024B)               -            5000    13.6µs    10.8µs    14.0µs    56.2µs   570.1µs      0
2 Write (resp 1024B)               -            5000    17.4µs    14.8µs    16.2µs    56.4µs   459.0µs      0
2 Write + Nagle server (resp 1024B) -             100   44.24ms   44.00ms   44.12ms   48.05ms   48.09ms      0
  [G1] p50 2-Write / p50 1-Write                        1.42x   (kỳ vọng 1.5-2x)
  [G1] p50 2-Write+Nagle / p50 2-Write                1456.68x   (kỳ vọng ?)

$ go test ./internal/frame/ -run TestDecodeHangsWithoutDeadline -count=1 -race -v
    frame_test.go:307: treo 300ms không deadline; deadline giải sau 352ms; err = read tcp 127.0.0.1:46571->127.0.0.1:55712: i/o timeout
--- PASS: TestDecodeHangsWithoutDeadline (0.36s)
```

**Đọc kết quả:** Đổi framer của netlab (header 4 byte → 10 byte, thêm magic/version) mà G1 vẫn
1.42x (trước 1.41x) và Nagle vẫn 44.00ms (trước 44.03ms). Đây là bằng chứng ngược cho phase 0:
tỉ số bền qua cả việc **thay code bị đo**, miễn hình dạng syscall giữ nguyên. `writeResponse`
hai-lần-Write cần `frame.PutHeader` để không copy body — nếu dùng `Append` thì biến thể 2-Write
tốn thêm một `copy` mà biến thể 1-Write cũng tốn, tỉ số vẫn đúng nhưng số tuyệt đối lệch.

P1-2: không thể "assert treo vô hạn", nên test chốt mốc 300ms rồi **giải** bằng deadline — hai
nửa cùng một kịch bản. Kết quả `352ms` = 300 chờ + 50 deadline + 2 lịch. Điều test chứng minh
không phải "decoder có bug" mà là "decoder không có deadline **là quyết định**, và ai dùng nó
phải đặt deadline ở tầng connection".

**Một số không hợp lý xuất hiện khi chạy lại cả bộ:** `go test ./... -race` mất **36s**, trước là
1.9s. Truy ra: `TestPayloadOverUint32` 5.84s dưới `-race`, trong khi lần đầu đo 0.03s. Race
detector có shadow memory cho vùng 4 GiB — "cấp phát không tốn gì" (kết luận G5) không đúng vô
điều kiện. Ghi vào P1-3. Cùng bài P0-7: một lần chạy không phải số.

### 2026-09-03 — Trả P1-3, P1-4, P1-5: một chẩn đoán sai và một giả thuyết lật một nửa

```console
$ go test ./internal/frame/ -run TestTransportDifference -count=1 -race -v
    transport_test.go:60: tcp=62 pipe=62          # coalesce: 1 Write 62 byte, Read 4096
    transport_test.go:71: tcp=7 pipe=7            # short read: Read buffer 7
    transport_test.go:82: tcp=1 pipe=1            # 62 Write 1 byte không sleep
    transport_test.go:104: Write trả về trong 200ms không có reader: tcp=true pipe=false
--- PASS: TestTransportDifference (0.22s)

$ make framelab-split
Read thô đầu : 7 byte / buffer 7   → server Read: 6 lần (nhỏ nhất 3, lớn nhất 27)  ✔ 3 frame
Read thô đầu : 12 byte / buffer 12 → server Read: 6 lần (nhỏ nhất 2, lớn nhất 27)  ✔ 3 frame

$ for i in 1 2 3; do go test ./internal/frame/ -run TestPayloadOverUint32 -count=1 -v | grep '^---'; done
--- PASS: TestPayloadOverUint32 (0.01s)  /  (0.00s)  /  (0.00s)
$ for i in 1 2 3; do FRAME_HUGE=1 go test ./internal/frame/ -run TestPayloadOverUint32 -count=1 -race -v | grep '^---'; done
--- PASS: TestPayloadOverUint32 (0.02s)  /  (0.01s)  /  (0.01s)
$ for i in 1 2 3; do FRAME_HUGE=1 go test ./internal/frame/ -count=1 -race -v | grep 'TestPayloadOverUint32'; done
--- PASS: TestPayloadOverUint32 (4.49s)  /  (3.76s)  /  (0.00s)
```

**Đọc kết quả — G6 sai một nửa.** Tôi đã viết trong `pair()`: "net.Pipe không tái tạo được cả
dribble lẫn coalesce". Đo ra: Pipe cho short read 7/7 y như TCP, và cho 62 byte một Read y như
TCP. Thứ Pipe **không** có là kernel socket buffer — nên `Write` chờ reader (`pipe=false`) và
hai `Write` rời không bao giờ gom thành một `Read`. Lý do dùng TCP thật vẫn đúng; lời giải thích
tôi viết cho nó thì sai. Test phase 1 nhờ TCP mà bắt được hình dạng gom-Write (11 byte ở G7);
với Pipe thì không bao giờ thấy. Cũng ghi: bài "gom Write rời" lần này TCP cho **1**, framelab
từng cho **11** — kết quả này không xác định, phụ thuộc scheduler, nên test chỉ assert phía Pipe.

**P1-5:** Read thô cắt frame 1 ở byte 7 (giữa header) và byte 12 (giữa payload). `nhỏ nhất 3`
và `nhỏ nhất 2` là phần đuôi `ReadFull` phải vá sau khi `MultiReader` cạn phần thô. Cả hai vẫn
3 frame. Đây là điều `TestCoalesce` đã chứng minh, nhưng giờ **cho thấy** được.

**Chẩn đoán sai của mục trước.** Tôi viết "race detector có shadow memory cho vùng 4 GiB" như
nguyên nhân của 5.84s. Đo: một mình dưới `-race` → 0.02/0.01/0.01s. Sai. Cả package dưới `-race`
→ 4.49/3.76/0.00s: tái hiện được, **không xác định**, và không cặp test nào tái hiện được. Tôi
đã làm đúng thứ phase 0 cảnh báo: thấy một số bất thường, gán cho nó nguyên nhân hợp lý nhất
trong đầu, rồi ghi vào diary như sự thật. Skip dưới `-race` giữ lại (để `make test` không gánh
4s ngẫu nhiên) nhưng lời skip đổi thành "tốn nhiều giây", và nguyên nhân thành P1-6.

### 2026-09-03 — Trả P1-6: `-race` vô can, thủ phạm là trang bẩn

Đăng ký trước khi đo: "3.4s sys cho một `make` không chạm trang là **page fault ×1M** — tức runtime
đã zero span vì địa chỉ đó từng được dùng. Dự đoán: lần chậm có RSS ≈ 4 GB, lần nhanh không."

```console
$ go test -c -o frame.norace.test ./internal/frame/ && for i in $(seq 8); do /usr/bin/time -f " sys=%S maxrss=%MKB" ./frame.norace.test -test.count=1 -test.v 2>&1 | grep -E 'make\(4 GiB\)|sys='; done
make(4 GiB) = 3.889ms  sys=0.03 maxrss=12544KB
make(4 GiB) = 5.229521s  sys=5.73 maxrss=4206848KB
make(4 GiB) = 8.845ms  sys=0.06 maxrss=12412KB
make(4 GiB) = 4.783ms  sys=0.05 maxrss=12664KB
make(4 GiB) = 3.941ms  sys=0.03 maxrss=12032KB
make(4 GiB) = 8.510867s  sys=8.42 maxrss=4206592KB
make(4 GiB) = 4.672ms  sys=0.03 maxrss=12544KB
make(4 GiB) = 4.79ms  sys=0.05 maxrss=12416KB

$ go run ./cmd/needzerolab          # bench/p1-needzero-GOTIT-00663.txt
RSS đầu: 2176kB
A. heap sạch                                 make = 7ms        RSS sau = 7040kB
sau 64MB rác + GC (không scavenge)  RSS = 73216kB
B. sau vùng bẩn — dự đoán: CHẬM, RSS 4 GiB   make = 7.138s     RSS sau = 4267520kB
sau 64MB rác + FreeOSMemory          RSS = 7900kB
C. sau vùng bẩn đã scavenge — dự đoán: nhanh make = 3ms        RSS sau = 7900kB

$ # test gọi runtime.GC(); debug.FreeOSMemory() trước make — cả package, 8 lần
make(4 GiB) = 3.627ms  sys=0.04 maxrss=12544KB     (… 8/8 trong 2.7–3.8ms, maxrss 12 MB)

$ for i in 1 2 3; do go test ./... -count=1 -race; done
ok  	github.com/thaivro/edgegate/internal/frame	2.386s / 2.348s / 2.351s
```

**Đọc kết quả:** Ba tầng bằng chứng, mỗi tầng một dự đoán đăng ký trước:

1. **Không `-race` cũng chậm** (7.73s, 7.34s ở 2/8 lần) ⇒ chẩn đoán "shadow memory" ở hai mục
   trước sai hoàn toàn, không chỉ sai một phần. Skip-dưới-race đã gỡ.
2. **Lần chậm ⇔ RSS 4.2 GB** (8/8 khớp) ⇒ bộ nhớ *bị chạm*, không phải mmap chậm. `sys` 5.7–8.4s
   với `user` 0.3s ⇒ thời gian là page fault trong kernel; phần `memclr` ở user chỉ 0.3s.
3. **Tái hiện xác định** trong 40 dòng: 64 MB rác chạm-rồi-thả + `GC()` (không scavenge) ⇒ 7.138s;
   cùng rác + `FreeOSMemory()` ⇒ 3ms. Cơ chế: span 4 GiB đặt first-fit bắt đầu ở vùng vừa free;
   vùng đó còn bẩn nên runtime đánh dấu **cả span** `needzero` và zero hết — 64 MB bẩn kéo theo
   4 GiB bị chạm, khuếch đại **64x**. Scavenge rồi thì Go biết trang là zero, không chạm.

Không xác định 2/8 vì phụ thuộc GC đã chạy hay chưa trước lúc `make` — tức phụ thuộc thứ tự và
timing của các test trước, và không cặp test nào đủ để tạo rác đúng chỗ. Đó là lý do bisect 4 cặp
đều 0.00s.

**Sửa hai chỗ:** (a) test gọi `FreeOSMemory()` trước `make` — không phải để "nhanh" mà để phép đo
đo đúng thứ nó định đo (encoder từ chối payload > uint32), không đo tình trạng heap; (b) gỡ skip
`-race` vì tiền đề sai. `make test` với `-race` giờ 2.35s ổn định.

**Điều quan trọng hơn bản thân nợ:** G5 và Rút ra §6 đã viết "4 GiB không tốn gì, I2 bảo vệ địa
chỉ ảo". Sai theo hướng nguy hiểm: một server thật **luôn** có rác bẩn, nên `length=0xFFFFFFFF`
không phải 4 GiB địa chỉ ảo vô hại — nó là **7 giây CPU kernel và 4 GiB RSS thật, mỗi connection,
trước khi attacker gửi byte payload nào**. I2 quan trọng hơn tôi nghĩ lúc viết nó, đúng 64 lần.

## Giả thuyết sai

| Tôi tưởng là | Thực tế là | Lệnh + output đã lật tẩy | Đã sửa thế nào |
|---|---|---|---|
| G1: coalesce ⇒ Read/frame < 1 | 1.67 — số Read do `ReadFull` xin, không do gói tin. Coalescing **vô hình** với decoder đúng | `framelab -mode coalesce` → `server Read: 5 lần (nhỏ nhất 5, lớn nhất 27)` | Lab thêm một `Read` thô 4 KiB trước decoder + `io.MultiReader`; comment đầu file ghi lại dự đoán sai |
| G3: nodefense đỏ vì thiếu `ErrTooBig` | Đỏ vì `unexpected EOF` — mọi lỗi đều làm nó đỏ; bằng chứng thật là `TotalAlloc` +4294967376 | `go test -tags nodefense` → 2 dòng FAIL trên cùng một test | Giữ cả hai kiểm; hiểu rằng kiểm lỗi một mình **không** chứng minh thứ tự check/make |
| G5: `make([]byte, 4 GiB)` sẽ OOM/chậm | 0.01–0.03s, trang chưa chạm không tốn RSS | `TestPayloadOverUint32 (0.03s)`; `TestCapBeforeAlloc -tags nodefense (0.01s)` | Test dùng luôn điều đó để kiểm encoder với payload thật > uint32; ghi P1-3 vì máy `overcommit=2` sẽ fail |
| G6: `net.Pipe` không tái tạo được short read lẫn coalesce | Tái tạo được cả hai (7/7, 62/62); chỉ thiếu Write bất đồng bộ và gom Write rời | `TestTransportDifference` 4 kịch bản | Sửa comment `pair()`; test giữ TCP vì lý do đúng |
| 5.84s của test 4 GiB là do shadow memory `-race` | `-race` vô can (không race: 7.73s, 2/8). Thủ phạm: span đè lên trang free-còn-bẩn ⇒ zero cả 4 GiB ⇒ 1M page fault | `/usr/bin/time` 8 lần: chậm ⇔ maxrss 4.2 GB; `needzerolab` B = 7.138s / C = 3ms | `FreeOSMemory()` trước `make`; gỡ skip race; G5 và Rút ra §6 viết lại |
| "4 GiB chưa chạm không tốn gì" (G5, Rút ra §6) | Chỉ trên heap sạch. Process có 64 MB rác bẩn ⇒ 7.138s + RSS 4.27 GB | `go run ./cmd/needzerolab` | I2 hiểu lại: bảo vệ CPU+RSS thật, không phải địa chỉ ảo |
| G7: NoDelay ⇒ dribble không sleep vẫn 1 byte/Read | 11 byte: coalescing còn nguồn thứ hai là scheduler, NoDelay chỉ tắt Nagle | `framelab -mode dribble -interval 0` → `Read thô đầu: 11 byte` | Không sửa code; ghi vào Rút ra |
| I2 = "kiểm trần trước make ở decoder" | I2 có **hai chiều**: encoder cắt cụt `uint32(len)` cũng phá ranh giới | Đọc code, không có lệnh — fuzz 1.58M execs không chạm tới | `MaxPayload`, `Encode` trả `ErrPayloadTooBig`, `Append` panic; `TestPayloadOverUint32` |
| Lab chạy 3 mode xanh = lab đúng | Đường lỗi treo vĩnh viễn, chưa có mode nào đi qua | Đọc code | Gộp `first` vào `result`, một channel |

## Số đo

Toàn bộ là số **đếm**, không có latency ⇒ không có closed/open-loop, không có RTT. Máy
GOTIT-00663, 2026-09-03, loopback, `-race` bật khi test.

| Đại lượng | Giá trị | Lệnh |
|---|---|---|
| Read thô đầu, dribble 10ms / dribble 0 / coalesce | **1 / 11 / 62** byte (cùng 62 byte gửi) | `framelab -mode ... [-interval 0]` |
| Read của decoder, dribble / coalesce | 62 / 5 lần cho 3 frame | ↑ |
| Cấp phát khi tắt I2 | 4294967376 byte cho 10 byte input | `go test -tags nodefense` |
| Cấp phát khi có I2 | < 65536 byte (ngưỡng test) | `TestCapBeforeAlloc` |
| Fuzz | 1580258 execs / 120s, 20 corpus, 0 crash | `make fuzz-frame` |
| `make([]byte, 1<<32)` | 0.03s, không đổi RSS | `TestPayloadOverUint32` |

**Tỉ số:**

| Tỉ số | Giá trị | Nói lên điều gì |
|---|---|---|
| Read thô đầu coalesce / dribble | **62x** | Ranh giới `Write` của bên gửi không tồn tại ở bên nhận |
| Byte cấp phát: không I2 / có I2 | **> 65536x** (4.29 GB / < 64 KB) | Một dòng code đặt sai thứ tự = 4 GiB mỗi connection |
| Read/frame coalesce, dự đoán / đo | < 1 / **1.67** | Decoder đúng không "thấy" coalescing — số Read do nó xin |

## Invariant + lệnh kiểm chứng

| Invariant | Cài ở | Lệnh kiểm chứng | Kết quả |
|---|---|---|---|
| **I2** chiều đọc: trần kiểm trước `make` | `internal/frame/limit.go:checkLength`, gọi tại `frame.go:Decoder.Decode` ngay trước `make([]byte, n)` | `go test ./internal/frame -run TestCapBeforeAlloc` **và** `make framelab-nodefense` | xanh / **đỏ** (4294967376 byte) |
| **I2** chiều ghi: length không bị cắt cụt | `frame.go:Encode` (`ErrPayloadTooBig`), `frame.go:Append` (panic) | `go test ./internal/frame -run TestPayloadOverUint32` | xanh |
| **I1** (tiền thân): decoder và encoder đồng ý ranh giới với mọi input | `FuzzFrameDecode` round-trip `Append(Decode(x)) == x` | `make fuzz-frame` | 1.58M execs, 0 lệch |
| Short read không phải lỗi | `Decoder.Decode` chỉ dùng `io.ReadFull` | `TestDribble` | xanh, 62 Read |
| EOF đúng ranh giới ≠ EOF giữa frame | `Decoder.Decode` đổi `io.EOF` → `io.ErrUnexpectedEOF` sau header | `TestEOFSemantics` (cắt ở mọi vị trí) | xanh |
| Không write-write-read | `frame.go:Encode` một `Write` | `TestEncodeIsOneWrite` | xanh |
| Bài phản chứng có răng | `limit_nodefense.go` | `make framelab-nodefense` phải FAIL | FAIL ✔ |

| **I3** chưa cài ở tầng này — và có bằng chứng là chưa | `Decoder` không gọi `SetReadDeadline` | `go test ./internal/frame -run TestDecodeHangsWithoutDeadline -v` | Decode treo 300ms không deadline; deadline 50ms giải sau 352ms tổng |

**I3 cài ở tầng connection (phase 7)**, không ở decoder. Test trên tồn tại để điều đó là quyết định có ghi chép.

## Đọc gì

- `io.ReadFull` / `io.ReadAtLeast` (godoc): trả `io.EOF` chỉ khi **0 byte** được đọc, ngược lại
  `io.ErrUnexpectedEOF` — đây là nguồn của `TestEOFSemantics`.
- `net.Pipe` (godoc): "synchronous, in-memory, full duplex" — mỗi `Write` chờ đúng một `Read`
  nhận nó, nên không thể tái tạo short read hay coalescing. Lý do G6.
- `runtime.MemStats.TotalAlloc`: cộng dồn, không giảm khi GC — nên dùng được để đo "đã cấp phát
  bao nhiêu" giữa hai mốc, khác `HeapAlloc`.
- `man 2 mmap` / `man 5 proc` (`overcommit_memory`): vì sao 4 GiB chưa chạm không tốn gì, và
  máy nào thì **có** tốn (P1-3).
- Không đọc RFC nào: phase này là giao thức tự đặt. RFC 9112 §6 để trước phase 2.

## Rút ra

1. **`ReadFull` không phải "cách đúng để đọc", nó là cách duy nhất.** Dribble cho thấy header
   10 byte tới bằng 10 lần `Read` 1 byte. Bất kỳ code nào làm `n, _ := conn.Read(buf)` rồi parse
   `buf[:n]` như một tin nhắn là code chỉ chạy trên loopback với sender tử tế — và chết ở byte
   thứ 2 khi gặp sender chậm hoặc mạng chia gói.

2. **Coalescing là thật (62 byte trong một `Read`) và decoder đúng không thấy nó.** Hai câu này
   không mâu thuẫn: `ReadFull` xin đúng 10 byte thì kernel giao đúng 10, phần còn lại nằm chờ
   trong socket buffer. Tôi dự đoán `Read/frame < 1` và đo được 1.67 vì đã nghĩ số lần `Read`
   phản ánh gói tin. Nó phản ánh **decoder xin gì**. Muốn thấy gói tin thì phải hỏi kiểu khác
   (một `Read` thô buffer lớn) — bộ đo phải hỏi đúng câu, không phải hỏi câu quen.

3. **`SetNoDelay(true)` tắt một nửa coalescing.** Dribble không sleep cho 11 byte ở `Read` đầu
   dù NoDelay đã bật: Nagle bị tắt, nhưng client vẫn `Write` được 11 lần trước khi server kịp
   `Read`. Nguồn thứ hai là **tốc độ tương đối của hai bên**, và không có socket option nào tắt nó.

4. **`TestDribble` chính là một cuộc tấn công slowloris ở tầng frame.** Một sender 1 byte/10ms
   giữ một goroutine server suốt 644ms cho 62 byte; với 1 byte/giây và 10000 connection thì
   `Decoder` hiện tại ngồi chờ mãi — nó không có deadline (I3). Phase này cố ý chưa cài, vì
   deadline là quyết định của tầng connection, không của decoder; nhưng phải ghi là **cùng một
   bài test** vừa là bằng chứng đúng của phase 1, vừa là kịch bản tấn công của phase 7.

5. **Bài phản chứng phải đỏ vì đúng lý do.** Với `nodefense`, kiểm `errors.Is(err, ErrTooBig)`
   đỏ — nhưng nó đỏ vì `unexpected EOF`, tức nó sẽ đỏ với **mọi** lỗi, kể cả decoder cấp phát rồi
   mới lỗi. Thứ chứng minh "trước `make()`" là `MemStats.TotalAlloc` +4294967376. Một bài phản
   chứng đỏ chưa đủ; phải đọc nó đỏ **ở dòng nào**.

6. **`make([]byte, 4 GiB)` không giết máy — trên heap sạch. Trong process thật nó là 7 giây và
   4 GiB RSS.** Lúc đầu đo 0.01s và kết luận "trang chưa chạm không tốn gì". Đúng, khi span nằm
   trên địa chỉ chưa từng dùng. Nhưng P1-6 cho thấy chỉ cần **64 MB** rác vừa free còn bẩn ở đầu
   span là runtime phải zero **cả** 4 GiB ⇒ 1M page fault ⇒ 7.138s kernel, RSS 4.27 GB — trước cả
   khi attacker gửi byte payload nào. Một server chạy lâu luôn có rác bẩn. Nên I2 không bảo vệ
   "địa chỉ ảo" như tôi tưởng lúc đầu; nó bảo vệ **7 giây CPU và 4 GiB RSS thật, mỗi connection**.

7. **I2 có hai chiều.** Tôi cài trần ở decoder rồi coi là xong; `Append` vẫn `uint32(len)` im
   lặng. Encoder cắt cụt length cũng phá ranh giới — phá ở phía **bên kia**, khó debug hơn nhiều.
   Fuzz 1.58M lần không chạm tới vì fuzz chỉ biết decoder. Bằng chứng tốt cho một nửa không phải
   bằng chứng cho cả hai.

8. **Cả hai bug của turn 2 nằm ở đường không có bài chạy nào đi qua.** Fuzz mổ decoder rất kỹ và
   mù hoàn toàn với encoder; lab chạy 3 đường xanh và mù với mọi đường lỗi. Đây là dạng khác của
   bài học phase 0 ("số hợp lý không phải bằng chứng"): **xanh trên đường đã chạy không nói gì về
   đường chưa chạy** — và đọc lại code vẫn là công cụ duy nhất cho đường chưa chạy.

## Nợ kỹ thuật

- [x] **P1-1** — *(trả cùng ngày, xem nhật ký mục cuối)* `cmd/netlab/wire.go` vẫn dùng framer riêng (`readFrame`), trùng logic với
      `internal/frame`. Nợ P0-5 (netlab không test) trả một nửa bằng cách thay `readFrame` bằng
      `frame.Decoder` — lúc đó fuzz của phase 1 bảo vệ luôn netlab.
- [x] **P1-2** — *(trả cùng ngày: `TestDecodeHangsWithoutDeadline`)* `Decoder` không có deadline; `TestDribble` là slowloris. Tầng connection phải
      `SetReadDeadline` trước mỗi `Decode`. Cài ở phase 7, nhưng phải có test **ở đây** chứng minh
      decoder treo vô hạn với sender 1 byte/phút (test đỏ có chủ đích, skip mặc định).
- [x] **P1-3** — *(trả: skip theo arch/overcommit/race; phần "vì sao chậm" → P1-6)* `TestPayloadOverUint32` cấp phát 4 GiB ảo; fail trên máy `vm.overcommit_memory=2`
      hoặc 32-bit. Cần `t.Skip` theo `runtime.GOARCH` và đọc `/proc/sys/vm/overcommit_memory`.
- [x] **P1-4** — *(trả: `TestTransportDifference`, lật G6 một nửa)* G6 (`net.Pipe` không tái tạo dribble/coalesce) chấm ✅ theo tài liệu, chưa đo.
      Một test 10 dòng chạy `TestCoalesce` qua `net.Pipe` và cho thấy Read thô đầu = 62 luôn.
- [x] **P1-5** — *(trả: `-rawbuf`, `make framelab-split`)* `framelab` chưa đo trường hợp frame **dính qua biên**: 3 frame trong 1 Write
      nhưng server `Read` thô 4 KiB nhận đúng 62 — nếu buffer thô là 15 byte thì frame 1 nằm giữa
      hai Read. Decoder xử lý được (ReadFull) nhưng lab chưa **cho thấy**.

- [x] **P1-6** — *(trả: `cmd/needzerolab`, cơ chế needzero; xem mục nhật ký cuối)*

Đã vào `docs/debts.md`.
