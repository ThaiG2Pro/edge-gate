# Sổ nợ kỹ thuật

Một món nợ = **một thứ tôi biết là còn thiếu**, kèm **lệnh để trả nó**. Không có lệnh thì
đó chỉ là lo lắng, không phải nợ.

Ba loại, và cách xử lý khác hẳn nhau:

| Loại | Nghĩa | Cách trả |
|---|---|---|
| 🔧 **code** | Sửa được ngay trên máy này, chỉ cần viết test trước | Viết test cho nó **fail**, rồi sửa cho **pass** |
| 📏 **đo** | Cần máy Linux thuần / mạng thật / nhiều máy mới có số đáng tin | Công cụ dựng sẵn, chạy lệnh, dán output vào diary |
| ⏳ **phase sau** | Chưa đủ ngữ cảnh để quyết định | Ghi lại, đừng đoán non |

---

## Đang nợ

### 📏 P0-6 · `netem` cho một mạng sạch một cách không thực tế

`netem delay 10ms` trần cho phân phối cực chặt: p50 20.43ms / p99 20.66ms. Không jitter, không
mất gói. Mà **jitter mới là thứ tạo ra tail latency**, và tail latency là toàn bộ lý do phase 6
(P2C+EWMA) và phase 7 (retry, circuit breaker) tồn tại. Đo trên một mạng không jitter thì P2C
sẽ trông vô dụng — và đó sẽ là một kết luận sai giống hệt "pool vô dụng trên loopback".

```bash
sudo tc qdisc add dev lo root netem delay 10ms 3ms distribution normal loss 0.1%
ping -c 20 -i 0.2 127.0.0.1 | tail -2      # phải thấy mdev lớn, và có gói mất
make lblab-skew
sudo tc qdisc del dev lo root
```

### 📏 P0-3 · `-role both`: client và server cùng process, cùng core

Hệ quả cụ thể, không phải lo xa: mọi số `RSS/conn` của phase 0 là của **một cặp** client+server
(19.40 KB), không phải chi phí một connection phía server. Và mọi số latency đều lẫn nhiễu
tranh CPU giữa generator và thứ bị đo.

```bash
# máy A (hoặc terminal A, đã ghim core):
taskset -c 0,1,2 go run ./cmd/netlab -role server -addr :9000 -workers 1 -bufio=false
# máy B (hoặc terminal B):
taskset -c 3,4,5 go run ./cmd/netlab -role client -addr <A>:9000 -all -tag "2-process"
```

Hai máy thật trả `P0-3` **và** `P0-6` trong một lần: RTT thật có jitter và mất gói sẵn, nên
không cần `netem` giả lập. `P0-1` đã trả bằng netem, nhưng netem cho một mạng sạch bất thường —
xem `P0-6`.

### 📏 P0-4 · `expLimits` dial tuần tự nên chưa loại được nghi phạm thứ hai

Đo được `1502 → 455 conn/s` khi `tw` tích luỹ, CPU 29.5%/6 core, 0 lỗi. Kết luận "trần ở không
gian ephemeral port" **chưa loại được** khả năng "trần ở chính vòng lặp một luồng của client".

```bash
# cần thêm -dialers N vào expLimits, rồi so conn/s ở N=1 và N=16.
# Nếu N=16 cũng chặn ở ~cùng conn/s => trần là port. Nếu tăng gần 16x => trần là client.
go run ./cmd/netlab -exp limits -duration 40s -dialers 1  -addr <eth0-ip>:9200
go run ./cmd/netlab -exp limits -duration 40s -dialers 16 -addr <eth0-ip>:9201
```

### 📏 P-env-2 · Generator và proxy dùng chung 6 core

`vegeta` ở rate cao ăn nhiều core; proxy chỉ còn phần còn lại. Số đo phản ánh cuộc tranh chấp
CPU, không phản ánh proxy.

```bash
taskset -c 0,1,2 ./bin/edgegate &
taskset -c 3,4,5 vegeta attack -rate=5000 -duration=30s -targets=t.txt | vegeta report
```

Đã ghim core vẫn chưa đủ: cần so `rps` đạt được với `rate` yêu cầu. Lệch > 2% nghĩa là
generator không theo nổi ⇒ **số latency vô nghĩa**, không phải "proxy chậm".

### ⏳ P-arch-1 · Chưa quyết: `Host` header giữ hay đổi khi forward

Hai lựa chọn hợp lệ (`proxy_set_header Host $host` vs `$proxy_host` của nginx), hệ quả khác
nhau về virtual hosting ở upstream. Phải quyết **có ý thức** ở phase 4 và ghi vào diary, không
để nó là tình cờ của lần viết code đầu tiên.

*Cập nhật phase 3:* đã có một trường hợp **buộc** sinh Host — client HTTP/1.0 không gửi `Host`,
proxy nâng lên HTTP/1.1 nên `net/http` upstream trả 400 "missing required Host header". Hiện
`forward.go` điền `Host = cfg.Upstream` **chỉ khi thiếu**; còn lại forward nguyên văn (D2).

### 🔧 P3-1 · Trailer chunked từ upstream bị bỏ (D4 phase 3)

`forward.go:copyBody` gọi `ChunkedWriter.Close()` không ghi trailer; `resp.Trailer()` bị bỏ.
gRPC-web / `Trailer: X-Checksum` sẽ mất. Test trước: fixture Hijack ghi chunked + trailer, kỳ
vọng client thấy trailer ⇒ fail ⇒ sửa.

```bash
go test ./internal/proxy -run TestTrailerForwarded -v
```

### ⏳ P3-2 · 101 Switching Protocols ⇒ 502

`forward.go` D7: 101 không thuộc phase 3. WebSocket cần tunnel hai chiều sau head. Phase 7 (khi có
mô hình connection lifetime) hoặc phase riêng.

### 🔧 P3-3 · Chưa có test upstream chết giữa body response

Invariant "một response cho một request" hiện chỉ đúng theo đọc code. Test: fixture Hijack ghi
`Content-Length: 100` rồi 50 byte rồi đóng ⇒ proxy **đóng** client, không ghi 502; client
đọc body phải nhận `io.ErrUnexpectedEOF`.

```bash
go test ./internal/proxy -run TestUpstreamDiesMidBody -v
```

### 🔧 P3-4 · G6 đăng ký 1000 request, test chạy 200

`TestNoGoroutineLeak` nâng lên 1000 (nửa keep-alive) và giữ `≤ before+2`, `-race -count=3`.

### 📏 P3-5 · G2/G3 dao động ±0.2x giữa hai lần chạy

3.39x / 3.20x và 1.44x / 1.63x cùng máy, 3 tiến trình chia 6 core. Trả cùng P-env-2:

```bash
taskset -c 0,1 ./bin/upstream -addr :8081 & taskset -c 2,3 ./bin/edgegate -config config/dev.json &
for i in 1 2 3 4 5; do taskset -c 4,5 ./bin/proxylab -mode overhead -n 2000 | grep 'G2 p50'; done
```

### 🔧 P2-3 · D4 (CL+TE ⇒ chunked, xoá CL) phải thành từ chối ở phase 4

Kèm phản chứng `make smugglelab-nodefense` đỏ. Test hiện tại
`TestReadRequestBodyFraming/"CL+TE ⇒ chunked, CL xoá (D4)"` sẽ phải đổi kỳ vọng.

## Đã trả

### ✅ P2-2 · 204/304 kèm TE: strip hay giữ? — trả 2026-09-04 (phase 3)

Đóng **bằng cấu trúc**: `StripHopByHop` xoá TE ở chiều response, proxy chỉ đặt lại TE khi chính
nó chunked; với NoBody (HEAD/1xx/204/304) mode là "không body" nên TE không bao giờ được đặt lại.
Không cần `if` riêng. Bằng chứng `TestHEADAnd204HaveNoBody` (`go test ./internal/proxy -run
TestHEADAnd204HaveNoBody -v`).

### ✅ P2-4 · `FuzzReadRequest` chậm 2x vì `ReadMemStats` — trả 2026-09-03

Đo trước khi sửa (không profile được với `-fuzz`, dùng benchmark tạm): thân fuzz **9.5 µs**/input,
một `runtime.ReadMemStats` **80 µs** (stop-the-world), `runtime/metrics.Read` **0.5 µs**. Hai lần
ReadMemStats đắt gấp ~17 lần việc thật. Giả thuyết lần này **đúng** — nhưng chỉ biết sau khi đo.
Sửa: kiểm hai tầng — tầng 1 `metrics.Read`, vượt trần mới xác nhận bằng **min của 3** lần
ReadMemStats. Lý do cần tầng 2: cả hai bộ đếm đều toàn tiến trình; lần chạy đầu bắt được dương
tính giả 506 KB cho input 44 byte (goroutine của fuzz engine cấp phát trong cửa sổ đo), không tái
hiện. Kết quả: **14 717 → 32 828 execs/s** (`bench/p2-fuzz-readrequest-metrics-60s.txt`, 1 874 459
execs, 0 đỏ giả). Phản chứng trần = 1 byte ⇒ đỏ 11 024 B/52 B (`bench/p2-fuzz-allocbound-counterproof.txt`).

### ✅ P0-5 · `cmd/netlab` không có test — trả gián tiếp ở phase 1 (P1-1), đóng 2026-09-03

Món nợ là `readFrame` riêng không được fuzz. P1-1 đã xoá nó: `cmd/netlab/wire.go` dùng
`frame.Decoder`, package `internal/frame` có `FuzzDecode`. `go test ./cmd/netlab` vẫn
`[no test files]` — đúng, vì netlab giờ chỉ còn logic thí nghiệm, không còn logic giao thức.

### ✅ P0-7 · Số tuyệt đối phase 0 là một lần chạy — trả 2026-09-03

5 lần `netlab -exp rtt -n 500 -bufio=false` liên tiếp (`bench/p0-7-rtt-5runs.txt`):

| run | p50 reuse | p50 dial | tỉ số |
|---|---|---|---|
| 1 | 8.5 µs | 154.4 µs | 18.24x |
| 2 | 7.8 µs | 141.5 µs | 18.05x |
| 3 | 7.5 µs | 125.6 µs | 16.65x |
| 4 | 8.1 µs | 127.7 µs | 15.77x |
| 5 | 7.6 µs | **138.8 µs** (median) | 18.23x |

Phase 0 ghi dial p50 **861.7 µs** (n=2000, tw đầy) và **341 µs** (n=100). Hôm nay median 138.8 µs
⇒ số tuyệt đối lệch **2.5–6x** giữa các phiên. Tỉ số cũng **không** bất biến: 36.69x → ~17x.
Kết luận sửa lại: chỉ có **thứ tự độ lớn** của tỉ số (>10x) và **chiều** của nó mang được sang
phiên khác; con số của tỉ số thì không. Mọi ô "tỉ số" ở phase 0 phải đọc là "cỡ chục lần".

### ✅ P0-2 · Capacity của `expOmission` được tính, chưa được đo — trả 2026-09-03

`netlab -exp omission -rate 100000 -svc 1ms -workers 1` (`bench/p0-2-capacity.txt`): closed-loop
1 conn đạt **737 rps**, p50 **1.34 ms** ⇒ `time.Sleep(1ms)` thật sự ngủ ~1.3 ms. Khớp 754 của
phase 0. Mốc "quá tải 1.2x" đúng là **~885 rps**; `make netlab-omission` cũ dùng 1200 = **1.63x**.
Đã đổi Makefile về 885.

**Bug lộ ra khi trả:** ở rate 100000, generator open-loop **panic** `slice bounds out of range`
trong `bufio.Reader.Read`. Nguyên nhân: `sem` đếm slot rỗi nhưng conn chọn theo `i % len(pool)`,
hai goroutine dùng chung một `clientConn`. Ở rate 1200 của phase 0 nó va khi hàng đợi > 427 ms
(512 conn / 1200 rps) — tức là **có va** trong lần đo G4 gốc (p99 open-loop hàng trăm ms). Sửa:
free-list `chan *clientConn`. `go run -race` 5 s: 0 DATA RACE (`bench/p0-2-race.txt`). Số G4
mới: p99 open/closed-1 = 377x (phase 0 ghi một số khác, xem errata phase0.md).

### ✅ P2-1 · Diff-fuzz không tới được lệch chỉ lộ ở oracle — trả 2026-09-03

Giả thuyết "fuzzer chỉ instrument package mình" **sai**: `rtk proxy go test -a -n -fuzz ...` cho
thấy 180/207 package compile với `-d=libfuzzer`, gồm `net/http`, `bufio`, `net/textproto`; 27
package không instrument là runtime/testing/fuzz engine. Nguyên nhân thật: lệch differential là
**quan hệ giữa hai đường đã được cover** (trimOWS của mình + nhánh lỗi chunk của Go), không tạo
edge mới ⇒ không có tín hiệu coverage; fuzz phẳng chỉ tới đó bằng may mắn.
Trả bằng: (b) `FuzzChunkLineAgainstNetHTTP` — head cố định, đột biến riêng dòng chunk-size — tìm
ra `" 3"` trên bản lenient trong **0.10s** (`bench/p2-structfuzz-lenient-60s.txt`), strict 120s sạch
(`bench/p2-structfuzz-strict-120s.txt`), `make difffuzz-chunk`; (c) 17 hàng bảng đối chiếu tay
thành seed `internal/httpx/testdata/fuzz/FuzzAgainstNetHTTP/hand-*` (32 seed chạy trong `go test`).

### ✅ P-code-1 · `internal/httpx` có test — trả ở phase 2 turn 1-2

`header_test.go` (canonical, `TE`→`Te`, `StripHopByHop` kể cả field liệt kê trong `Connection`,
`Write` sắp xếp + chặn CR/LF injection), `limits.go` được dùng thật bởi parser với 3 trần kiểm
trong lúc đọc (`TestHeaderBomb`: 431 sau 4096 byte), `errors.go` mở rộng (`Is`, lỗi lớp).
Bằng chứng: `go test ./internal/httpx -count=20 -race` ok (`bench/p2-race20.txt`).

| ID | Trả ở phase | Bằng lệnh nào |
|---|---|---|
| **P2-2** | 3 | `TestHEADAnd204HaveNoBody`: HEAD/204 qua proxy không mang `Transfer-Encoding`; hệ quả của `StripHopByHop` + chỉ đặt lại TE khi proxy tự chunked |
| **P1-6** | 1 | `cmd/needzerolab` tái hiện **xác định**: heap sạch `make(4 GiB)` = 7ms / RSS 7 MB; sau **64 MB rác bẩn + GC** = **7.138s / RSS 4.27 GB**; sau `FreeOSMemory` = 3ms. Cơ chế: span đè lên trang free-còn-bẩn ⇒ runtime zero CẢ span ⇒ ~1M page fault (sys 7.7s, user 0.3s). Không liên quan `-race` (không race cũng 7.73s, 2/8). Dự đoán "lần chậm RSS 4 GB" trúng 8/8. Fix test: `FreeOSMemory()` trước `make` ⇒ 8/8 nhanh; gỡ skip-dưới-race; `make test` -race 2.35s ×3 |
| **P1-3** | 1 | `TestPayloadOverUint32` skip khi int 32-bit, khi `vm.overcommit_memory=2`, ~~và dưới `-race`~~ (skip race đã gỡ khi P1-6 chỉ ra `-race` không liên quan); `make test-huge` chạy riêng |
| **P1-4** | 1 | `TestTransportDifference`: net.Pipe vs TCP trên 4 kịch bản ⇒ **G6 sai một nửa**. Pipe tái tạo được short read (7/7) và nhiều-frame-một-Read (62/62); không tái tạo được Write bất đồng bộ (`tcp=true pipe=false`) và gom Write rời (pipe luôn 1; tcp 1 hoặc 11 — không xác định) |
| **P1-5** | 1 | `framelab -rawbuf 7` / `-rawbuf 12` (`make framelab-split`): Read thô cắt frame 1 giữa header / giữa payload, decoder vẫn 3 frame ✔; `nhỏ nhất 3` và `nhỏ nhất 2` byte/Read là phần đuôi ReadFull phải vá — `bench/p1-framelab-split-GOTIT-00663.txt` |
| **P1-1** | 1 | `cmd/netlab/wire.go` chuyển sang `frame.Decoder`/`frame.PutHeader`; `grep -n "func readFrame" cmd/netlab/*.go` → 0. G1 chạy lại: **1.42x** p50 (trước 1.41x), spike Nagle **44.00ms** (trước 44.03ms) — `bench/p1-netlab-on-frame-GOTIT-00663.txt`. Tỉ số sống sót qua việc đổi framer |
| **P1-2** | 1 | `TestDecodeHangsWithoutDeadline`: không deadline ⇒ Decode **không trả về sau 300ms**; `SetReadDeadline(50ms)` ⇒ trả `i/o timeout` sau 352ms tổng. Decoder cố ý không tự đặt deadline — giờ là quyết định có test ghi lại, phase 7 cài ở tầng connection |
| **P-env-1** | 0 | Hai phần. (a) `netlab -exp limits` qua `127.0.0.1` vs qua eth0 IP ⇒ `tcp_tw_reuse=2` che port exhaustion. (b) `scripts/pay-P0-1.sh` ⇒ **netem trên `lo` áp delay cho CẢ HAI chiều**: `delay 10ms` → RTT 20.157ms, `delay 20ms` → RTT 40.201ms. Đã sửa `Makefile` `rtt-up` thành `delay = RTT/2` + ép `ping` kiểm |
| **P0-1** | 0 | `scripts/pay-P0-1.sh` ⇒ G3 = **2.00x** (không phải >15x), và đúng 2.00x ở cả hai RTT ⇒ tỉ số là **đơn vị sai** cho pool; đơn vị đúng là **1 RTT phí mỗi connection dựng mới**. Dự đoán đăng ký trước khi đo: đúng |
