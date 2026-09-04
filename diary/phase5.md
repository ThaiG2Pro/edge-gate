# Phase 5 — Upstream connection pool

- **Thời lượng dự kiến:** 2 ngày · **thực tế:** _______
- **Bắt đầu:** 2026-09-04 15:40 · **Kết thúc:** _______
- **Trạng thái:** 🔧 đang làm — turn 1 (code). Giả thuyết bên dưới đăng ký **trước** file `.go` đầu tiên của phase.
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

_(turn 2)_

## Giả thuyết sai

_(turn 2)_

## Số đo

_(turn 2)_

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
