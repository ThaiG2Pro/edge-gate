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

### 🔧 P4-1 · Ba lần duyệt `Connection:` và hai trong ba cấp phát

`hasConnectionToken` (`bytes.Split([]byte(v))`), `StripHopByHop` (`strings.Split`), `checkConnectionTokens`
(`strings.Cut`, không alloc) — cùng một header duyệt ba lần. Gộp thành một lần duyệt trả về
(tokens, close, keepAlive, bad). Test trước: `BenchmarkReadRequest` phải giảm allocs/op dưới 26.

```bash
go test ./internal/httpx -run '^$' -bench 'ReadRequest$' -benchmem -count 6
```

### 📏 P4-2 · G4 đo trên WSL2 không phân giải được 5 %

ns/op ± 10-37 % (`bench/p4-bench-readrequest.txt`); chỉ allocs/op và CPU share tin được. Trả cùng
P-env-2/P3-5 trên Linux thuần: `taskset`, `-count 20`, benchstat hai commit `b3e08f0` vs `989b060`.

### ⏳ P4-3 · `Forwarded` / `X-Forwarded-Proto/Host/Port` chưa sinh, `X-Real-IP` từ peer tin chưa kiểm

`forwardedHeaders` chỉ lo XFF + X-Real-IP. Khi có TLS (phase 8) `X-Forwarded-Proto` mới có nghĩa; khi
có rate limit (phase 7) cần hàm "IP client thật" = phần tử phải nhất của XFF **không** nằm trong
`trusted_proxies`. Peer tin gửi `X-Real-IP: not-an-ip` hiện được forward nguyên văn.

### 🔧 P4-4 · Tag `nodefense` chung cho phase 1/3/4 — **trả một phần 2026-09-04 (phase 5)**

`make smugglelab-nodefense` e2e lật 21 ca, trong đó `95-resp-ok-eof` đỏ vì `rawCopyResponse=true`
(phase 3) chứ không vì phòng tuyến phase 4. Tách `nodefense4` hoặc ghi chú trong target.
Phase 5 đã tách `nodefensepool` cho `poolCheckClean` (`internal/proxy/defense_pool*.go`) sau khi
phản chứng G3 đỏ sai chỗ vì bẫy #2. Còn nợ: phase 1/3/4 vẫn chung `nodefense`.

```bash
go test ./internal/proxy -run 'TestSmugglingE2E/95' -tags nodefense -v   # đỏ vì bẫy #2, không vì phase 4
```

### 🔧 P4-5 · Ca còn thiếu trong `testdata/smuggle`

`Expect: 100-continue` qua proxy (proxy phải trả 100 hay forward?), chunk-ext dài quá `MaxLineBytes`
(⇒ 431 giữa body), `GET` có `Content-Length: 5` + body qua proxy (forward hay từ chối?), header bomb
qua proxy thật (431 + close), request-line có SP thừa cuối. Mỗi ca một file, `expect` đăng ký trước.

### 📏 P4-6 · Oracle thứ hai cho `TestSmugglingOracle`

Chỉ so với Go. "Hướng an toàn" mới đúng với backend Go. Dựng nginx và h2o (docker) nhận cùng 61 payload
qua `nc`, ghi status, so ba cột. Đặc biệt các ca 21 (CL trùng), 30/31 (bare LF), 50 (`Connection: Host`).

### 📏 P5-1 · G1/G2 đo lúc máy ồn (load 9 trên 6 core)

`bench/p5-poollab-rtt20.txt`: mẫu qua proxy +5-7 ms ngoài mô hình 3 RTT / 2 RTT, mẫu thẳng +0.5 ms.
Nghi 4 lần đánh thức tiến trình/request dưới tranh chấp CPU; chưa chứng minh. Trả cùng P-env-2:

```bash
uptime   # load < 1 rồi mới chạy
taskset -c 0,1 go run ./cmd/poollab -pool both -n 2000 | tee bench/p5-poollab-rtt0-quiet.txt
make poollab-rtt 2>&1 | tee bench/p5-poollab-rtt20-quiet.txt
```

### 🔧 P5-4 · Body vào connection chết giữa probe và `Write` ⇒ 502

D4 (c) không retry request có body vì body đã stream (I2). `TestIdleClosedUpstream/noprobe-POST-body-502`
cho mẫu 25/50 khi tắt probe; với probe là 0/50 nhưng cửa sổ probe→Write vẫn mở. Đo tần suất thật:
upstream đóng rỗi ngẫu nhiên 1-50 ms sau response, 10k POST, đếm 502. Nếu > 0.1 %: buffer body
≤ 64 KiB để replay đúng một lần — đổi I2 có điều kiện, đăng ký D trước khi code.

### 📏 P6-1 · Bench LB closed-loop không biến capacity bỏ phí thành p99

`-flap -recover`: P2C tau 30 s cho b2 hồi phục **0.0 %** tải nửa sau (least-conn 25.0 %), nhưng p99
nửa sau bằng nhau (4.43 vs 4.13 ms) vì 3 node còn lại dư sức và client tự gửi chậm lại. Cần open-loop
ở ~80-90 % capacity để thấy cái giá thành latency:

```bash
# thêm -rate R vào cmd/lblab: lịch gửi cố định, latency tính từ giờ HẸN, không từ lúc gửi
go run ./cmd/lblab -algos leastconn,p2c,p2c-slow -flap -recover -rate 1200 -n 20000 -conns 64
```

### 🔧 P6-2 · P2C chia lệch 20.3-31.7 % trên 4 node giống hệt nhau

`bench/p6-lblab-even.txt`: max/min 1.56x (least-conn 1.01x). EWMA khởi từ mẫu đầu (có dial ≈ 1 ms),
với tau 1 s mỗi mẫu sau nặng ~5·10⁻⁴ ⇒ thứ hạng vài trăm ms đầu là may rủi. Test fail trước: `make lblab`
đòi max/min share P2C ≤ 1.2. Sửa thử: không cho mẫu có dial vào EWMA, hoặc khởi tạo bằng trung vị cụm.

### 🔧 P6-3 · Node chưa có mẫu được điểm 0 kể cả khi đang có request

`ewma.go:score` trả 0 khi `n == 0` ⇒ mọi request trong lúc chờ mẫu đầu đều đổ vào node mới/hồi phục.
Finagle `PeakEwma.get`: `if (lcost == 0.0 && pending != 0) Penalty + pending`. Test fail trước: 4 backend
dưới tải 32 conn, thêm backend thứ 5 (hoặc revive), đếm inflight đỉnh của nó trước `Done` đầu tiên —
đòi ≤ 1.

### 📏 P6-4 · `consecutive_5xx` chậm ở rps thấp

Kỳ vọng ~586 request tới chuỗi 5 lỗi đầu với lỗi 30 % ⇒ 0.17 s ở 3 400 rps nhưng ~1 phút ở 10 rps.
Đo: `lblab -skew err=30% -conns 1` (≈ vài trăm rps chia 4) với `-n 2000`, đọc share b1 và 5xx; nếu
outlier không kịp, thêm detector `success_rate` (Envoy) — đăng ký D trước.

### 📏 P6-5 · Passive outlier cắt cửa sổ dial lỗi của active — chưa đo

`TestLBKillRevive` tắt outlier ⇒ 111-120 dial lỗi trong 500 ms trước khi active đánh dấu b3. Dự đoán
"bật outlier ⇒ ~5" chưa có số. Thêm sub-test cùng kịch bản với `Outlier{Consecutive: 5}`, log `Fails`:

```bash
go test ./internal/proxy -run 'TestLBKillRevive' -count=3 -v
```

### 🔧 P-ops-1 · `make proxybench` để sót tiến trình; `&&` + `&`

`pgrep -a -x upstream` lúc 16:00 phase 5 thấy `./bin/upstream -addr :8081` pid 146978 từ phase 3.
Target `-kill $$(cat /tmp/upstream.pid)` không chạy khi bước trước lỗi. Sửa: `trap`/`|| true` + `pkill -x
upstream` cuối target. Luật vận hành (mắc 3 lần): `go build` **một dòng riêng**, rồi mới `./bin/x &`.

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

## Đã trả

### ✅ P5-2 · Con quá `MaxIdleTime` ở đáy stack — trả 2026-09-04 (phase 6 turn 1)

Sổ nợ tả sai một nửa: `get` dọn cả stack khi **đỉnh** quá tuổi; lỗ thật là đỉnh luôn tươi (một client
keep-alive) còn đáy già mãi. `pool.put` giờ quét từ đáy bỏ mọi con quá tuổi. `TestPoolExpiredAtBottom`:
đỏ trên code cũ `Idle 5, DropExpired 0` (`bench/p6-debts-failfirst.txt`), xanh `Idle 1, DropExpired 4`.

### ✅ P5-3 · Pool theo host; inflight theo request — trả 2026-09-04 (phase 6 turn 1)

`Server.pools map[addr]*pool` (`proxy.go:poolFor`), `MaxIdle` per host; inflight tăng ở `lb.Pick`, giảm ở
`lb.Done` sau `exchange` (sau cả `put` — D2 ghi "trước", sai vài µs, diary phase 6 câu 2).
`TestLBPoolPerHost`: Dials/Reuses/Idle 4/396/4, `Inflight 0` cả 4 backend.

### ✅ P5-5 · `MaxIdleTime` phải ngắn hơn upstream — trả 2026-09-04 (phase 6 turn 1)

Mặc định 60 → 30 s. `TestMaxIdleTimeShorterThanUpstream`: upstream idle 100 ms; pool 50 ms ⇒
`DropExpired 19, DeadOnProbe 0`; pool 60 s ⇒ `DropExpired 0, DeadOnProbe 19`.

### ✅ P2-3 · CL+TE ⇒ từ chối — trả 2026-09-04 (phase 4 D1)

`body.go:framing`: có TE mà cũng có CL ⇒ `ErrAmbiguousFraming` (400), không bỏ CL nữa. Phản chứng
`rejectCLWithTE=false` (nodefense) ưu tiên CL ⇒ ca 01/02/03/10/12/90 đỏ (`bench/p4-smugglelab-nodefense.txt`).
Test cũ `TestReadRequestBodyFraming/"CL+TE…"` đổi kỳ vọng. Diff-fuzz 7.75 M exec sau đổi: 0 lệch.

### ✅ P-arch-1 · `Host` giữ hay đổi — trả 2026-09-04 (phase 4 D4/D5/D6/D7)

**Giữ nguyên** (nginx `$host`). Điều kiện để "giữ" an toàn: Host chỉ có một nguồn — absolute-form
⇒ authority thắng và viết về origin-form, lệch ⇒ 400 (`normalizeTarget`); `Connection: Host` ⇒ 400
(`checkConnectionTokens`); CONNECT ⇒ 501; cú pháp `validHost`. Ngoại lệ sinh Host: client HTTP/1.0
không gửi (từ phase 3). Bằng chứng `TestAbsoluteFormRewritten`, ca 60-71, 83-86.

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
| **P2-3** | 4 | `ErrAmbiguousFraming` khi CL+TE; nodefense ưu tiên CL ⇒ 6 ca đỏ; diff-fuzz 300 s 0 lệch |
| **P-arch-1** | 4 | Host giữ nguyên; `normalizeTarget` + `checkConnectionTokens` bảo đảm một nguồn authority; `TestAbsoluteFormRewritten` |
| **P2-2** | 3 | `TestHEADAnd204HaveNoBody`: HEAD/204 qua proxy không mang `Transfer-Encoding`; hệ quả của `StripHopByHop` + chỉ đặt lại TE khi proxy tự chunked |
| **P1-6** | 1 | `cmd/needzerolab` tái hiện **xác định**: heap sạch `make(4 GiB)` = 7ms / RSS 7 MB; sau **64 MB rác bẩn + GC** = **7.138s / RSS 4.27 GB**; sau `FreeOSMemory` = 3ms. Cơ chế: span đè lên trang free-còn-bẩn ⇒ runtime zero CẢ span ⇒ ~1M page fault (sys 7.7s, user 0.3s). Không liên quan `-race` (không race cũng 7.73s, 2/8). Dự đoán "lần chậm RSS 4 GB" trúng 8/8. Fix test: `FreeOSMemory()` trước `make` ⇒ 8/8 nhanh; gỡ skip-dưới-race; `make test` -race 2.35s ×3 |
| **P1-3** | 1 | `TestPayloadOverUint32` skip khi int 32-bit, khi `vm.overcommit_memory=2`, ~~và dưới `-race`~~ (skip race đã gỡ khi P1-6 chỉ ra `-race` không liên quan); `make test-huge` chạy riêng |
| **P1-4** | 1 | `TestTransportDifference`: net.Pipe vs TCP trên 4 kịch bản ⇒ **G6 sai một nửa**. Pipe tái tạo được short read (7/7) và nhiều-frame-một-Read (62/62); không tái tạo được Write bất đồng bộ (`tcp=true pipe=false`) và gom Write rời (pipe luôn 1; tcp 1 hoặc 11 — không xác định) |
| **P1-5** | 1 | `framelab -rawbuf 7` / `-rawbuf 12` (`make framelab-split`): Read thô cắt frame 1 giữa header / giữa payload, decoder vẫn 3 frame ✔; `nhỏ nhất 3` và `nhỏ nhất 2` byte/Read là phần đuôi ReadFull phải vá — `bench/p1-framelab-split-GOTIT-00663.txt` |
| **P1-1** | 1 | `cmd/netlab/wire.go` chuyển sang `frame.Decoder`/`frame.PutHeader`; `grep -n "func readFrame" cmd/netlab/*.go` → 0. G1 chạy lại: **1.42x** p50 (trước 1.41x), spike Nagle **44.00ms** (trước 44.03ms) — `bench/p1-netlab-on-frame-GOTIT-00663.txt`. Tỉ số sống sót qua việc đổi framer |
| **P1-2** | 1 | `TestDecodeHangsWithoutDeadline`: không deadline ⇒ Decode **không trả về sau 300ms**; `SetReadDeadline(50ms)` ⇒ trả `i/o timeout` sau 352ms tổng. Decoder cố ý không tự đặt deadline — giờ là quyết định có test ghi lại, phase 7 cài ở tầng connection |
| **P-env-1** | 0 | Hai phần. (a) `netlab -exp limits` qua `127.0.0.1` vs qua eth0 IP ⇒ `tcp_tw_reuse=2` che port exhaustion. (b) `scripts/pay-P0-1.sh` ⇒ **netem trên `lo` áp delay cho CẢ HAI chiều**: `delay 10ms` → RTT 20.157ms, `delay 20ms` → RTT 40.201ms. Đã sửa `Makefile` `rtt-up` thành `delay = RTT/2` + ép `ping` kiểm |
| **P0-1** | 0 | `scripts/pay-P0-1.sh` ⇒ G3 = **2.00x** (không phải >15x), và đúng 2.00x ở cả hai RTT ⇒ tỉ số là **đơn vị sai** cho pool; đơn vị đúng là **1 RTT phí mỗi connection dựng mới**. Dự đoán đăng ký trước khi đo: đúng |
