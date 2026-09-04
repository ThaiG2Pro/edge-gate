# Phase 5 — Upstream connection pool

- **Thời lượng dự kiến:** 2 ngày · **thực tế:** _______
- **Bắt đầu:** 2026-09-04 15:40 · **Kết thúc:** _______
- **Trạng thái:** 🔧 turn 2 (đo) xong phần RTT 0 — **G1 sai, G4 sai một nhánh**, G3/G5/G6/G7 đúng; **G2 chờ `make poollab-rtt`** (cần sudo). 25 test proxy xanh `-race`, phản chứng đỏ 20/20. Giả thuyết bên dưới đăng ký **trước** file `.go` đầu tiên của phase.
- **Commit:** _______ (commit nền `30a1ab0`)

> **Đường đi thô, kể cả ngõ cụt:** [`phase5-log.md`](phase5-log.md). File này là bản biên tập.
>
> **Quy tắc ghi nhật ký:** mọi con số, mọi kết luận đều phải kèm **lệnh shell sinh ra nó**
> và **output thật**. Luật đầy đủ: [`../skills/diary/SKILL.md`](../skills/diary/SKILL.md).

## Ràng buộc kế thừa

- **Tuyệt đối không dùng `net/http` trên data path.** `internal/httpx`, `internal/proxy`,
  `cmd/edgegate`, `cmd/poollab` chỉ `net` (+ `syscall` cho một probe). `net/http` chỉ là fixture
  (`cmd/upstream`, `internal/fixture`) và oracle trong `_test.go`.
- **Lối tắt D1 phase 3 bị tháo:** proxy không gửi `Connection: close` sang upstream nữa. Từ giờ
  connection upstream sống qua nhiều request **của nhiều client khác nhau** — bẫy #3 phase 3
  ("byte thừa trong `bufio.Reader`") chuyển từ *bug* thành *lỗ hổng rò dữ liệu chéo người dùng*.
- **Đơn vị báo cáo là RTT tiết kiệm/request, không phải tỉ số** (phase 0 G2/G3: RTT 0 cho 36.69x,
  RTT 20 ms cho 2.00x — cùng một pool, tỉ số nói ngược nhau; khoản tiết kiệm tuyệt đối 0.86 ms →
  ~20 ms mới là sự thật).
- Phase 3 đã đo: p50 qua proxy 473 µs vs thẳng upstream 139 µs; một `Dial` loopback ≈ 300 µs. Pool
  phải trả lại phần lớn khoản 300 µs này ở RTT 0.

## Môi trường

Cùng máy phase 0-4 (`bench/env-GOTIT-00663.txt`). Phase này có số **thời gian** nên cả ba bẫy đo
mạng áp đủ: closed-loop (`cmd/poollab`, coordinated omission chưa loại trừ), loopback (chạy **hai**
lần: RTT 0 và RTT 20 ms qua `make rtt-up`, RTT phải **đo bằng ping**, không suy từ tham số netem),
generator chung 6 core với proxy và upstream (P-env-2 chưa trả).

```console
$ uname -srmo && go version && nproc && ulimit -n
Linux 6.6.87.2-microsoft-standard-WSL2 x86_64 GNU/Linux
go version go1.26.2 linux/amd64
6
1048576
```

⚠️ **Máy ồn hơn phase 3 hẳn** lúc đo turn 2 (16:06): `load average: 9.21, 7.15, 4.81` trên 6 core, một
tiến trình `python` ngoài dự án ăn 400 % CPU. Hệ quả đo được ngay: cùng `bin/upstream`, p50 "thẳng
upstream keep-alive" là 120 / 189 / 205 / **453** µs trong bốn lần chạy cách nhau vài chục giây (phase
3: 139 µs). Vì thế mọi **số tuyệt đối** ở phase này chỉ tin được đến ±2x, và bảng số đo chốt bằng
**khoản tiết kiệm tuyệt đối off − on** (ổn định 958 / 988 / 657 µs qua ba dụng cụ) chứ không bằng
p50 của từng mẫu.

## Mục tiêu phase

Connection pool tới upstream: `MaxIdlePerHost`, `MaxIdleTime`, phát hiện upstream đóng connection
rỗi lặng lẽ, retry **đúng một lần** khi chưa gửi được byte nào. **Invariant sống còn:** một
connection chỉ về pool khi **sạch**. `make poollab` (RTT 0) và `make poollab-rtt` (RTT 20 ms) báo
cáo bằng **ms và RTT tiết kiệm / request**. Bài phản chứng `make poollab-nodefense` **phải đỏ**:
tắt kiểm "sạch" ⇒ client B đọc được đuôi response của client A.

## Câu hỏi phải trả lời được (viết trước khi code)

1. "Sạch" là gì, **đủ** điều kiện nào? Liệt kê được từng điều kiện và vì sao thiếu một điều kiện
   là rò dữ liệu chéo người dùng (không phải chỉ "lỗi").
2. Upstream đóng connection rỗi thì proxy biết bằng cách nào **trước** khi ghi request vào đó?
   Vì sao `Read` của Go chưa báo gì dù FIN đã nằm trong kernel? Cái gì rẻ hơn goroutine đọc nền?
3. Khi nào được retry, khi nào **tuyệt đối không**? Vì sao "ghi thành công" (`Write` trả nil)
   không có nghĩa là "upstream đã nhận"? Request có body thì mất gì?
4. Pool tiết kiệm **bao nhiêu RTT mỗi request** — và vì sao con số này ở loopback (RTT ≈ 30 µs)
   lại **lớn hơn 1 RTT rất nhiều** trong khi ở RTT 20 ms lại **đúng ≈ 1 RTT**?
5. Không pool thì cạn cái gì trước: CPU, fd, hay ephemeral port / `TIME_WAIT`? Ở **phía nào**
   (proxy hay upstream)? Vì sao?
6. Cái giá của pool: connection rỗi giữ tài nguyên gì ở hai đầu, `MaxIdlePerHost` và `MaxIdleTime`
   chọn theo gì, và vì sao LIFO tốt hơn FIFO cho pool?

## Giả thuyết đăng ký trước (viết 15:40, chưa có file `.go` nào của phase)

| # | Giả thuyết | Tỉ số / giá trị kỳ vọng | Lệnh sẽ dùng để chấm |
|---|---|---|---|
| G1 | **RTT 0**, closed-loop 1 conn, GET /hello n=2000: p50 pool-off / p50 pool-on = **2.0-2.5x** (ROADMAP nói ~1.1x — phase 0 G2 đã lật: dial loopback ≈ 300 µs không phải RTT mà là dựng socket); tiết kiệm tuyệt đối **250-350 µs/request** ≈ **8-12 RTT loopback** | 2.0-2.5x; 250-350 µs; 8-12 RTT | `make poollab` → `bench/p5-poollab-rtt0.txt` |
| G2 | **RTT 20 ms** (netem, đo bằng ping): pool-off ≈ 3 RTT/request (client→proxy 1 + handshake 1 + request 1), pool-on ≈ 2 RTT ⇒ tỉ số **1.4-1.6x** (ROADMAP nói >15x — phase 0 G3 đã lật: 2.00x), tiết kiệm **19-21 ms** = **1.0 RTT/request** (±10 %) | 1.4-1.6x; 19-21 ms; 0.9-1.1 RTT | `make poollab-rtt` → `bench/p5-poollab-rtt20.txt` |
| G3 | **Phản chứng bẩn:** client A nhận head response CL=1 MiB rồi bỏ đi giữa body; client B gửi GET /hello ngay sau. Có kiểm sạch: B thấy 200 `hello` **N/N**. `-tags nodefense` (connection bẩn về pool): B thấy **≠ 200 hoặc body ≠ hello ở ≥ 90 %** lần (đuôi body của A thành status-line của B) | N/N xanh; ≥ 0.9 đỏ khi tắt | `go test ./internal/proxy -run TestDirtyConnNotPooled -count 20`; `make poollab-nodefense` đỏ |
| G4 | **Upstream đóng rỗi lặng lẽ:** upstream raw đóng sau 100 ms rỗi, client chờ 200 ms rồi gửi. Probe `recv(MSG_PEEK\|MSG_DONTWAIT)` trước khi dùng lại bắt được **50/50** (kể cả POST có body ⇒ 0 lỗi 502); tắt probe (`Pool.Probe=false`): GET không body được cứu bằng retry **50/50** với `retries=50`, POST có body ⇒ 502 **50/50** (không retry vì body đã tiêu) | 50/50; 50/50 + retries=50; 50/50 lỗi 502 | `go test ./internal/proxy -run 'TestIdleClosed' -v` |
| G5 | `MaxIdlePerHost=4`, 16 client song song × 50 request: sau khi xong, idle trong pool **= 4** (không hơn), `dropFull > 0`, số dial ≤ 16 + dropFull; goroutine về nền (pool không có goroutine) | idle == 4; goroutine Δ ≤ 2 | `go test ./internal/proxy -run 'TestPoolMaxIdle\|TestNoGoroutineLeak' -v` |
| G6 | **Invariant sạch trên đường bình thường:** 1 client, 200 request xoay vòng /hello, POST /echo (CL), POST /echo (chunked), /chunked, /nobody (204), HEAD /hello ⇒ **đúng 1 dial**, 199 reuse. Thêm 10 GET /eof (body tới EOF ⇒ `resp.Close`) ⇒ **thêm đúng 10 dial**, không request nào sai byte | dials = 1 rồi = 11; reuse = 199 rồi 209 | `go test ./internal/proxy -run TestPoolReuseMixed -v` |
| G7 | **TIME_WAIT nằm ở upstream, không ở proxy:** 2000 request pool-off ⇒ `ss -tan state time-wait` với local port của upstream ≈ **≥ 1800** (bên đóng trước = upstream, vì nó nhận `Connection: close`... **không** — D5 bỏ header đó; phase 3 upstream đóng vì header, giờ **proxy** đóng ⇒ TIME_WAIT ở **proxy**); pool-on ⇒ **≤ 5** | off ≥ 1800 ở phía proxy; on ≤ 5 | `cmd/poollab` in `ss -tan state time-wait \| grep <upstream port> \| wc -l` sau mỗi mẫu |

**Quyết định thiết kế đăng ký trước** (đổi sau phải ghi lý do):

| # | Quyết định | Lý do / hệ quả |
|---|---|---|
| D1 | Pool = **stack LIFO** `[]*pooledConn` + mutex, một upstream (phase 6 mới thành map theo host). `MaxIdlePerHost` 64, `MaxIdleTime` 60 s (nginx `keepalive_timeout` upstream 60 s; Go `IdleConnTimeout` 90 s) | LIFO: connection vừa dùng là connection **chắc còn sống nhất** và cache/cwnd còn ấm; connection ở đáy stack tự già quá `MaxIdleTime` rồi bị bỏ — FIFO thì xoay đều nên **không con nào được nghỉ hưu**, pool phình bằng peak |
| D2 | **Sạch** ⇔ (a) response head parse OK, (b) `resp.Body` đã trả `io.EOF` (CL đủ / chunk 0 + trailer / NoBody), (c) `ubr.Buffered() == 0` — không byte thừa sau body, (d) `!resp.Close` (upstream không đòi đóng, không body-tới-EOF), (e) không lỗi/timeout nào ở cả hai chiều (kể cả client bỏ đi giữa body response). **Thiếu một ⇒ `Close`**, không có "cố cứu" | Một hàm `clean()` duy nhất; mọi nhánh lỗi đi qua `release(false)`. Đóng một connection rẻ (~300 µs dial lại); trả connection bẩn = response của người này cho người khác |
| D3 | **Probe sống** trước khi dùng lại: `SyscallConn().Read` → `recv(fd, 1, MSG_PEEK\|MSG_DONTWAIT)`. `EAGAIN` ⇒ rỗi thật; `0` ⇒ FIN; lỗi ⇒ RST; **có byte** ⇒ upstream gửi thứ ta không hỏi ⇒ bẩn. Linux only (`pool_linux.go`), OS khác probe trả "không biết" | Một syscall ~1 µs thay một goroutine đọc nền/idle conn (net/http `readLoop`). FIN đã nằm trong receive queue của kernel; Go chưa báo chỉ vì **chưa ai gọi `Read`**. Không đủ 100 % (upstream có thể đóng giữa probe và write) ⇒ cần D4 |
| D4 | **Retry đúng một lần** ⇔ (a) connection **lấy từ pool** (không retry connection vừa dial), (b) lỗi là I/O (write lỗi, hoặc `ReadResponse` lỗi khi **0 byte response** đã tới), (c) request **không body** (`ContentLength == 0 && !Chunked`). Lần hai luôn dial mới. Có body ⇒ **502**, giữ client (body đã drain khỏi `br`) | `Write` trả nil chỉ nghĩa "vào send buffer kernel"; RST tới sau. Body đã stream từ client sang connection chết ⇒ không replay được (không buffer body: I2). 0 byte response = chưa có side-effect nào **quan sát được**; ≥ 1 byte = upstream đã xử lý ⇒ không idempotent nữa |
| D5 | **Bỏ `Connection: close` sang upstream** (tháo D1 phase 3). `Connection: close` của client **không lan** sang upstream (hop-by-hop đúng nghĩa). Upstream nói `Connection: close` hoặc HTTP/1.0 ⇒ dùng xong đóng, không pool | Vòng đời hai connection độc lập — đó là toàn bộ ý nghĩa của pool |
| D6 | Đếm được: `dials, reuses, puts, retries, dropDirty, dropFull, dropExpired, deadOnProbe`; `Server.PoolStats()`. `cmd/poollab` chạy **in-process** upstream fixture + proxy + client raw, in p50/p99 pool-off/on, **ms tiết kiệm/request**, **RTT tiết kiệm/request** với RTT = p50 của `net.Dial` (handshake = 1 RTT) đo tại chỗ | Không đo được thì không biết pool có chạy; báo cáo theo đơn vị phase 0 đã chứng minh là đúng |
| D7 | `-tags nodefense` lật `poolCheckClean=false`: connection về pool ngay sau khi có head response, bỏ (b)(c)(e). Tag vẫn dùng chung (P4-4 chưa trả) | Phản chứng G3 phải đỏ; đúng khuôn phase 1/3/4 |
| D8 | `SetDeadline(time.Time{})` trước khi `put`; mỗi lần dùng lại đặt deadline mới. `Server.Close()` đóng cả idle trong pool | Deadline cũ còn treo trên connection rỗi ⇒ request kế tiếp lỗi timeout ma |
| D9 | Không có goroutine dọn pool định kỳ: `MaxIdleTime` kiểm **lúc get** (bỏ con quá tuổi ở đỉnh stack cho tới khi gặp con còn hạn — LIFO nên đỉnh trẻ nhất, gặp một con trẻ là dừng... **sai chiều**: đỉnh trẻ nhất ⇒ phải quét từ **đáy**; đơn giản: get pop đỉnh, nếu quá tuổi thì đóng và pop tiếp; đáy quá tuổi được dọn lúc `put` khi đầy) | Pool không sinh goroutine ⇒ G5 đếm goroutine sạch; giá: connection quá tuổi ở đáy có thể sống tới lần `put` đầy kế tiếp (chấp nhận, ghi nợ nếu đo thấy fd rỗi cao) |

## Deliverable

- `internal/proxy/pool.go` (+ `pool_linux.go`, `pool_other.go`): pool LIFO, probe, stats.
- `internal/proxy/forward.go`: `roundTrip` dùng pool, `release(clean)`, retry một lần.
- `internal/proxy/pool_test.go`: G3 (`TestDirtyConnNotPooled`), G4 (`TestIdleClosedUpstream*`),
  G5 (`TestPoolMaxIdle`), G6 (`TestPoolReuseMixed`), `TestEOFResponseNeverPooled`,
  `TestPoolClosedWithServer`.
- `cmd/poollab`: `make poollab` / `make poollab-rtt` → hai file `bench/p5-poollab-rtt*.txt`.
- `make poollab-nodefense` đỏ.
- `cmd/edgegate` + `config/dev.json`: khối `pool`.

## Reproduce toàn bộ phase

```bash
git checkout <commit phase 5>
go test ./... -count=1 -race
make poollab            | tee bench/p5-poollab-rtt0.txt
make poollab-rtt        | tee bench/p5-poollab-rtt20.txt     # sudo tc; nhớ rtt-down (target tự tháo)
make poollab-nodefense                                        # PHẢI đỏ
go test ./internal/proxy -run TestDirtyConnNotPooled -count 20 -v
```

## Nhật ký

### 2026-09-04 15:40-15:55 — Turn 1: code, và ba chỗ sai lộ ngay khi test chạy lần đầu

Chi tiết trong [`phase5-log.md` §1](phase5-log.md). Tóm: (1) G4 nhánh "POST không probe" đăng ký
50/50 lỗi 502, thực tế **25/50 xen kẽ** — vì 502 làm connection chết bị bỏ nên request kế dial mới;
(2) phản chứng đỏ **sai chỗ** với tag `nodefense` chung (io.Copy thô che, probe MSG_PEEK bắt trước)
⇒ tách `nodefensepool` + tắt probe trong test G3; (3) `TestRawCopyTrap` đỏ ở build thường vì upstream
giả phase 3 chỉ phục vụ một request rồi giữ connection — với pool, đó không phải HTTP server.

### 2026-09-04 16:00 — G1, G7: pool ở RTT 0, và con số nào bền khi máy ồn

```console
$ make poollab        # bench/p5-poollab-rtt0.txt — in-process: fixture + proxy + client, 1 tiến trình
RTT = 42µs (srtt kernel, ss -tin, rttvar 0.008 ms) · ping = 642µs (ping avg, 5 gói) · chi phí một net.Dial (p50/20) = 834µs
  một net.Dial = 19.9 RTT — phần vượt 1 RTT là dựng socket/accept/goroutine, không phải mạng (phase 0 G2)
thẳng upstream, keep-alive                     2000     120µs     482µs  11.868ms    1.153s     0
  [qua proxy, pool-off (dial mỗi request)] Δ TIME_WAIT: phía proxy (dport=36557) +1964 · phía upstream (sport=36557) +0
  [qua proxy, pool-on] Δ TIME_WAIT: phía proxy (dport=36557) +0 · phía upstream (sport=36557) +0
qua proxy, pool-off (dial mỗi request)         2000   1.178ms   8.506ms  22.235ms    6.291s     0
qua proxy, pool-on                             2000     221µs   1.644ms  13.705ms    2.028s     0
  pool-off stats: {Dials:2020 Reuses:0 ...}   pool-on  stats: {Dials:1 Reuses:2019 Puts:2020 ... Idle:1}
  G1/G2 tỉ số        p50 off / p50 on   = 5.34x
  G1/G2 tiết kiệm     p50 off − p50 on   = 958µs / request
  G1/G2 theo RTT      tiết kiệm / RTT     = 22.80 RTT / request
  overhead còn lại    p50 on − p50 thẳng = 101µs (= parse 2 chiều + goroutine, KHÔNG còn dial)
```

**Đọc kết quả:** G1 đăng ký 2.0-2.5x / 250-350 µs / 8-12 RTT. Thực tế **5.34x / 958 µs / 22.8 RTT**.
Sai cả ba cột, cùng một nguyên nhân: tôi lấy "một dial ≈ 300 µs" của phase 3 làm hằng số, nhưng hôm
nay một `net.Dial` đo tại chỗ là **834 µs** (phase 0 lần đầu cũng đo 861 µs — con số 300 µs của
phase 3/phase 0 G8 là lúc máy rỗi). Khoản tiết kiệm 958 µs ≈ đúng **một dial + một lần đóng**, nghĩa
là pool trả lại đúng cái nó hứa; cái sai là hằng số tôi mang theo. Dials 2020 vs **1** và Reuses 2019
xác nhận pool chạy thật (D6), không chỉ có flag. G7: TIME_WAIT **+1964 ở phía proxy, +0 ở upstream**
— bên đóng trước là proxy (D5 bỏ `Connection: close` nên upstream không đóng nữa); pool-on **+0**.

**Đang nghĩ gì:** pool-off p50 1.18 ms cao gấp 2.5 lần số 473 µs của phase 3 — bench sai hay máy khác?
Kiểm chéo bằng hai cách trước khi chấm.

```console
$ go run ./cmd/poollab -upstream 127.0.0.1:18081 -n 2000   # bench/p5-poollab-rtt0-extupstream.txt — upstream NGOÀI tiến trình
RTT = 550µs (srtt kernel, ss -tin, rttvar 0.492 ms) · ping = 77µs (ping avg, 5 gói) · chi phí một net.Dial (p50/20) = 746µs
thẳng upstream, keep-alive                     2000     189µs     612µs   3.747ms     732ms     0
qua proxy, pool-off (dial mỗi request)         2000   1.515ms   5.791ms  13.727ms     5.19s     0
qua proxy, pool-on                             2000     526µs   2.206ms   11.67ms    2.222s     0
  G1/G2 tỉ số        p50 off / p50 on   = 2.88x
  G1/G2 tiết kiệm     p50 off − p50 on   = 988µs / request
  overhead còn lại    p50 on − p50 thẳng = 337µs

$ # bench/p5-proxylab-pool-onoff.txt — dụng cụ PHASE 3 đo bin/edgegate -pool=false rồi -pool=true
thẳng upstream, 1 conn keep-alive              2000     205µs     839µs    5.06ms     927ms     0   (lần 1)
qua proxy [-pool=false]                        2000   1.624ms   4.146ms  11.119ms    4.652s     0
thẳng upstream, 1 conn keep-alive              2000     453µs     917µs   3.717ms   1.137s     0   (lần 2, 10 s sau)
qua proxy [-pool=true]                         2000     967µs   2.539ms   8.045ms   2.733s     0
$ uptime
 16:06:16 up  4:05,  1 user,  load average: 9.21, 7.15, 4.81
```

**Đọc kết quả:** ba dụng cụ, ba mức nền khác nhau (p50 thẳng 120 → 189 → 205 → 453 µs), nhưng khoản
tiết kiệm off − on gần như **không đổi: 958 / 988 / 657 µs** (lần cuối lệch vì nền nhảy 2.2x giữa
hai mẫu). Tỉ số thì chạy từ 5.34x xuống 2.88x rồi ~1.7x tuỳ nền — **đúng bài phase 0: tỉ số là đơn
vị sai, khoản tuyệt đối mới bền.** Máy ồn không phải bench sai; nó là lý do phải chốt bằng cột nào.

**Ba nguồn "RTT" và không nguồn nào vô tội ở loopback:** srtt kernel 42 µs (in-process) nhưng 550 µs
(hai tiến trình) — trên loopback ACK bị hoãn và cõng theo response nên srtt gồm cả thời gian xử lý
của upstream; ping 642 µs rồi 77 µs; dial 746-834 µs là chi phí dựng socket. Ở RTT 20 ms cả ba sẽ hội
tụ về ~20 ms và cột "theo RTT" mới có nghĩa. Ở RTT 0, kết luận đúng là: **một dial loopback ≈ 10-20
RTT**, nên pool ở loopback tiết kiệm không phải "1 RTT" mà là "một lần dựng socket ở cả hai đầu".

### 2026-09-04 16:06 — G3, G4, G5, G6: invariant sạch, probe, retry, MaxIdle

```console
$ go test ./internal/proxy -run 'TestPool|TestDirtyConnNotPooled|TestIdleClosedUpstream|TestNoGoroutineLeak|TestRawCopyTrap' -count=1 -race -v   # bench/p5-pooltests.txt
    pool_test.go:83: G6 sau 200 request 6 kiểu framing: {Dials:1 Reuses:199 Puts:200 ... DropDirty:0 ... Idle:1}
    pool_test.go:96: G6 sau 10 GET /eof: {Dials:10 Reuses:200 Puts:200 ... DropDirty:10 ... Idle:0}
    pool_test.go:166: G3: B thấy 200 hello; {Dials:2 Reuses:0 Puts:1 Retries:0 DropDirty:1 ... Idle:1}
    pool_test.go:206: 50/50; {Dials:51 Reuses:0 Puts:51 Retries:0 ... DeadOnProbe:50 Idle:1}          probe-GET
    pool_test.go:206: 50/50; {Dials:51 Reuses:0 Puts:51 Retries:0 ... DeadOnProbe:50 Idle:1}          probe-POST-body
    pool_test.go:206: 50/50; {Dials:51 Reuses:50 Puts:51 Retries:50 DropDirty:50 ... DeadOnProbe:0}   noprobe-GET-retry
    pool_test.go:252: 25 × 502 xen kẽ 25 × 200; {Dials:26 Reuses:25 Puts:26 Retries:0 DropDirty:25 ...} noprobe-POST-body-502
    pool_test.go:298: G5: {Dials:108 Reuses:692 Puts:800 Retries:0 DropDirty:0 DropFull:104 ... Idle:4}
    proxy_test.go:415: G6: goroutine trước 4, sau 6 (chờ 21ms)
ok  	github.com/thaivro/edgegate/internal/proxy	3.248s

$ go test ./internal/proxy -run TestDirtyConnNotPooled -count=20 -race      # bench/p5-poollab-nodefense.txt
ok  	github.com/thaivro/edgegate/internal/proxy	1.674s
$ go test ./internal/proxy -run TestDirtyConnNotPooled -count=20 -tags nodefensepool -v | grep -E '^--- |B: status' | sort | uniq -c
     20 --- FAIL: TestDirtyConnNotPooled (0.03s)
     20     pool_test.go:160: B: status 502 body "502 Bad Gateway: upstream trả response không hợp lệ hoặc đóng sớm\n" — connection bẩn về pool
```

**Đọc kết quả:** G3 **20/20 xanh, 20/20 đỏ khi tắt** — và đỏ đúng chỗ: B nhận 502 vì proxy đọc
`AAAA…` (đuôi body 900 KiB của A) làm status-line của B. Không phải "B thấy body của A" nguyên văn vì
parser của chính mình từ chối status-line rác — nghĩa là ở proxy này, connection bẩn lộ ra dưới dạng
**502 hàng loạt cho người khác**, còn ở một proxy `io.Copy` thô nó sẽ là rò dữ liệu thật. G4 ba nhánh
đúng như đăng ký (probe bắt 50/50 kể cả POST có body; tắt probe thì GET được cứu bằng retry 50/50);
nhánh thứ tư sai như đã ghi turn 1. G5 idle **= 4** đúng bằng MaxIdle, dropFull 104, dials 108 ≤
16 + 104; goroutine 4 → 6 (pool không sinh goroutine). G6 **đúng 1 dial** cho 200 request 6 kiểu
framing, /eof thêm đúng 10 dial và **0 put** — `resp.Close` chặn đúng.

**Đang nghĩ gì:** G6 lần chạy đầu turn 2 **đỏ một lần**: `Puts:199 Idle:0` — client đã nhận xong
response mà proxy chưa `put` (put nằm sau `Flush`). Đó là race giữa test và proxy, không phải bug pool;
sửa test bằng `waitStats`. Nhưng nó nhắc một điều thật: "response đã về client" và "connection upstream
đã về pool" là **hai thời điểm khác nhau**, và phase 6 (least-conn đếm inflight) phải chọn một.

## Giả thuyết sai

| Tôi tưởng là | Thực tế là | Lệnh + output đã lật tẩy | Đã sửa thế nào |
|---|---|---|---|
| G1: pool ở RTT 0 cho 2.0-2.5x, tiết kiệm 250-350 µs ≈ 8-12 RTT | **5.34x, 958 µs, 22.8 RTT** (in-process); 2.88x / 988 µs (upstream ngoài) | `make poollab` → `bench/p5-poollab-rtt0.txt`: `chi phí một net.Dial (p50/20) = 834µs`, `tiết kiệm = 958µs` | Không sửa code. Sửa cách đăng ký: hằng "dial ≈ 300 µs" là số lúc máy rỗi (phase 3); phase 0 lần đầu đã đo 861 µs. Lần sau đăng ký **theo số đo tại chỗ cùng ngày** (`poollab` giờ in chi phí dial trước khi đo) |
| G4 nhánh 4: tắt probe, POST có body vào connection chết ⇒ **50/50** lỗi 502 | **25/50** xen kẽ 200/502: 502 ⇒ connection bị bỏ ⇒ pool rỗng ⇒ request kế dial mới ⇒ 200 ⇒ put ⇒ FIN ⇒ 502 | `TestIdleClosedUpstream/noprobe-POST-body-502` lần 1: `25/50 đúng (muốn 502 "")`; output `req 1: 200 "4", req 3: 200 "4", …` | Test đòi đúng chuỗi `5252…`, `dropDirty = 25`, `retries = 0`, 502 giữ connection client. Bài học: kỳ vọng phải mô phỏng **cả hành vi đúng của phòng tuyến** (bỏ connection lỗi), không chỉ lỗi |
| Phản chứng G3 với tag `nodefense` chung sẽ đỏ vì connection bẩn | Đỏ, nhưng vì **io.Copy thô** (head của B kẹt trong bufio tới deadline ⇒ EOF) và vì **probe MSG_PEEK** thấy byte thừa của A trước cả kiểm sạch | `go test -run TestDirtyConnNotPooled -tags nodefense`: `B đọc head: unexpected EOF`; stats `DeadOnProbe` | Tách tag `nodefensepool`; test G3 chạy `Pool.Probe=false`. Chạy lại: `B: status 502 … connection bẩn về pool` — đỏ đúng phòng tuyến. Trả một phần P4-4 |
| Upstream giả của `TestRawCopyTrap` (một request rồi giữ connection 5 s) vẫn dùng được | Với pool, request 2 đi đúng vào connection đó ⇒ 504 sau 2 s. "Giữ connection mà không phục vụ" không phải HTTP server | `go test ./... -race`: `--- FAIL: TestRawCopyTrap … request 2 không được trả lời trong 500 ms` ở build **thường** | Fixture phục vụ vòng lặp keep-alive, không tự đóng. Bẫy #2 vẫn đỏ với `-tags nodefense` (3.00 s) |
| "RTT = p50 `net.Dial`" đủ dùng làm mẫu số | Dial loopback 401-834 µs trong khi RTT ≈ 30-80 µs — chính bẫy phase 0 G2. `ping` WSL2: 2.78 ms rồi 642 µs rồi 77 µs. srtt kernel: 42 µs in-process, **550 µs** hai tiến trình (ACK hoãn cõng response) | Ba lần chạy `poollab` với ba dòng `RTT = …` khác nhau trong `phase5-log.md` §1-§2 | In cả ba cột; cột "theo RTT" chỉ có nghĩa ở RTT 20 ms. Ở RTT 0 kết luận là "một dial ≈ 10-20 RTT" |
| Đọc `PoolStats` ngay sau khi client nhận response là đủ | `put` nằm **sau** Flush cho client ⇒ có lúc `Puts:199 Idle:0` | `--- FAIL: TestPoolReuseMixed … có {Dials:1 Reuses:199 Puts:199 … Idle:0}` (lần 1 turn 2; lần 2 xanh) | `waitStats` chờ điều kiện, 5 lần `-count=5 -race` xanh |
| TIME_WAIT đếm số tuyệt đối sau mỗi mẫu | TIME_WAIT sống 60 s ⇒ mẫu pool-on "thấy" 541 của mẫu trước | `poollab` lần 1: `pool-on TIME_WAIT phía proxy: 541` | Đếm **Δ trong mẫu**: off +1964 / on +0 |

## Số đo

Tất cả: 2026-09-04, commit `f46d851` (+ sửa test waitStats), máy `bench/env-GOTIT-00663.txt`, **closed-loop
1 conn, coordinated omission chưa loại trừ**, cùng máy 6 core **load 5-9**, loopback. File:
`bench/p5-poollab-rtt0.txt`, `p5-poollab-rtt0-extupstream.txt`, `p5-proxylab-pool-onoff.txt`,
`p5-pooltests.txt`, `p5-poollab-nodefense.txt`, `p5-tests-turn2.txt`.

| # | Đăng ký | Thực tế | Đúng/Sai | Ghi chú |
|---|---|---|---|---|
| G1 | RTT 0: 2.0-2.5x; 250-350 µs; 8-12 RTT | **5.34x; 958 µs; 22.8 RTT** (srtt 42 µs). Upstream ngoài: 2.88x; 988 µs. Dụng cụ phase 3: 657 µs | ❌ **Sai** | Hằng "dial 300 µs" sai ngày; dial đo tại chỗ 746-834 µs. Khoản tuyệt đối bền, tỉ số không |
| G2 | RTT 20 ms: 1.4-1.6x; 19-21 ms; 1.0 RTT | _chờ `make poollab-rtt` (sudo)_ | ⏳ | `bench/p5-poollab-rtt20.txt` |
| G3 | N/N xanh; ≥ 90 % đỏ khi tắt kiểm sạch | **20/20** xanh; **20/20** đỏ (`-tags nodefensepool`, probe tắt) | ✅ | Đỏ dưới dạng 502 cho B — parser mình từ chối rác; proxy io.Copy thô sẽ rò thật |
| G4 | probe: 50/50 GET, 50/50 POST; tắt probe: GET 50/50 retries=50; POST 502 50/50 | 50/50, 50/50 (DeadOnProbe 50); 50/50 retries **50**; POST **25/50** xen kẽ | ⚠️ **3/4 đúng, 1 sai** | Nhánh 4 sai vì hành vi đúng (bỏ connection lỗi) làm mẫu xen kẽ |
| G5 | idle == 4; goroutine Δ ≤ 2 | idle **4**, dropFull 104, dials 108 ≤ 16+104; goroutine 4 → 6 | ✅ | pool không có goroutine riêng |
| G6 | dials = 1 rồi 11; reuse 199 rồi 209 | dials **1** rồi **10**, reuse **199** rồi 200, dropDirty 10, idle 0 | ✅ | Đăng ký "11 dial" sai số học: /eof lần đầu **reuse** connection rỗi rồi bỏ ⇒ 1 + 9 dial. Reuse 200 không 209 vì /eof lần 2-10 không có gì để reuse |
| G7 | off ≥ 1800 TIME_WAIT phía proxy; on ≤ 5 | off **+1964** proxy / +0 upstream; on **+0** / +0 | ✅ | Bên đóng trước = proxy (D5 bỏ `Connection: close`) |

**Bảng tỉ số / khoản tiết kiệm (RTT 0):**

| Dụng cụ | p50 thẳng | p50 off | p50 on | off/on | **off − on** | on − thẳng |
|---|---|---|---|---|---|---|
| `poollab` in-process | 120 µs | 1.178 ms | 221 µs | 5.34x | **958 µs** | 101 µs |
| `poollab` upstream ngoài | 189 µs | 1.515 ms | 526 µs | 2.88x | **988 µs** | 337 µs |
| `proxylab` (phase 3), hai lần chạy | 205 / 453 µs | 1.624 ms | 967 µs | ~1.7x | **657 µs** | 514 µs |
| phase 3 tham chiếu (máy rỗi) | 139 µs | 473 µs | — | — | (dial ≈ 333 µs) | — |

## Invariant + lệnh kiểm chứng

_(turn 3)_

## Đọc gì

- RFC 9112 §9.3 (persistence), §9.6 (tear-down: client MUST NOT retry non-idempotent).
- RFC 9110 §9.2.2 (idempotent methods), §15.6.3 (502).
- `man 2 recv` — `MSG_PEEK`, `MSG_DONTWAIT`; `man 7 tcp` — TIME_WAIT thuộc bên đóng trước.
- Go `net/http/transport.go`: `persistConn.readLoop`, `nothingWrittenError`, `errServerClosedIdle`,
  `shouldRetryRequest` — để đối chiếu D3/D4, không copy.

## Rút ra

_(turn 3)_

## Nợ kỹ thuật

_(turn 3)_
