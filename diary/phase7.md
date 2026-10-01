# Phase 7 — Resiliency: timeout, Slowloris, rate limit, circuit breaker, retry budget, shed, drain

- **Thời lượng dự kiến:** 2-3 ngày · **thực tế:** ~1 giờ làm việc, một buổi (turn 1 09:48-10:14, turn 2 10:19-10:40, turn 3 10:41-10:45)
- **Bắt đầu:** 2026-10-01 09:48 · **Kết thúc:** 2026-10-01 10:45
- **Trạng thái:** ✅ xong — **6/9 giả thuyết sai một vế** (G1 dial, G2 số conn, G3 b, G6 502, G7 504, G8 b/c), 0 sai hẳn; G4/G5/G9 đúng. Thêm D8′ drain lười (đăng ký giữa turn 2, trước khi đo) — giả thuyết và quyết định bên dưới viết **trước** file `.go` đầu tiên của phase.
- **Commit:** `07ee110` (turn 1 `0ed7a23`, turn 2 `c925df4`; commit nền `40e8cbb`)

> **Đường đi thô, kể cả ngõ cụt:** [`phase7-log.md`](phase7-log.md). File này là bản biên tập.
>
> **Quy tắc ghi nhật ký:** mọi con số, mọi kết luận đều phải kèm **lệnh shell sinh ra nó**
> và **output thật**. Luật đầy đủ: [`../skills/diary/SKILL.md`](../skills/diary/SKILL.md).

## Ràng buộc kế thừa

- **Không `net/http` trên data path.** `internal/proxy`, `internal/lb`, `internal/limit` (mới),
  `cmd/edgegate`, các `cmd/*lab` mới chỉ `net` (+ `syscall`). `net/http` chỉ là fixture và oracle test.
- **Invariant phase 4-6 không được sứt:** I1 (ranh giới), I4 (pool sạch), I6 (IP qua ranh giới tin cậy),
  I7 (counter giảm trên **mọi** đường ra). Phase này thêm ba bộ đếm mới (token, slot shedding, budget
  retry) — mỗi cái là một chỗ I7 có thể vỡ.
- **Phase 0 bài 3 + nợ P6-1:** load shedding chỉ đo được bằng **open-loop**. Closed-loop dưới 2x tải tự
  giảm tải về 1x (client chờ) ⇒ không bao giờ thấy quá tải. Phase này dựng generator open-loop có lịch
  cố định, latency tính từ **giờ hẹn** (không phải giờ gửi) — loại coordinated omission.
- **Phase 6 bài 1:** p99 chỉ nói về 1 % chậm nhất. Mọi bảng phase này ghi kèm **tỉ lệ từng status**
  (200/429/502/503/504/treo), không chỉ latency.
- **Phòng tuyến chỉ tin khi đã thấy nó đỏ lúc tắt** — mỗi cơ chế mới có tag phản chứng riêng
  (`nodefense7` cho cả phase, mỗi cơ chế một hằng số), theo khuôn P4-4.

## Môi trường

Cùng máy phase 0-6 (`bench/env-GOTIT-00663.txt`).

```console
$ date; uptime
Thu Oct  1 09:51:02 +07 2026
 09:51:02 up 26 min,  1 user,  load average: 2.99, 5.03, 2.96
$ uname -srmo && go version && nproc && ulimit -n
Linux 6.6.87.2-microsoft-standard-WSL2 x86_64 GNU/Linux
go version go1.26.2 linux/amd64
6
1048576
$ sysctl net.core.somaxconn net.ipv4.ip_local_port_range
net.core.somaxconn = 4096
net.ipv4.ip_local_port_range = 32768	60999
```

## Mục tiêu phase

Mỗi cơ chế ROADMAP đòi (5 timeout, Slowloris, token bucket per-IP, circuit breaker 3 trạng thái,
retry budget, shedding, graceful drain) có **một bài đo chứng minh nó làm việc** và **một bài phản
chứng đỏ khi tắt**. Test quyết định: `make chaoslab` — backend kill/chậm/flap/treo ngẫu nhiên 60 s,
bốn invariant (không panic, goroutine về nền, connection về nền, mọi request đúng một response
hoặc một lỗi rõ — không treo).

## Câu hỏi phải trả lời được (viết trước khi code)

1. Đủ bao nhiêu deadline thì "mọi connection đều có deadline" (I3)? Liệt kê từng chỗ đọc/ghi socket
   ở **cả hai** connection, mỗi chỗ deadline nào canh, hết hạn thì client thấy gì (408 / 504 / 502 /
   đóng im lặng). Chỗ nào trước phase này **không** có test?
2. Slowloris giết server kiểu nào? Vì sao Apache prefork chết với 500 connection còn một server Go
   goroutine-per-connection **có thể không**? Nếu không chết thì cái giá thật là gì (byte/connection,
   fd)? Và khi nào Go **cũng** chết — chính cơ chế giới hạn nào của phase này mở lại cửa cho Slowloris?
3. Token bucket per-IP: IP nào? Vì sao phải là IP **sau** ranh giới tin cậy của phase 4, và bypass
   bằng `X-Forwarded-For` trông ra sao bằng số? Map per-IP không dọn rò bao nhiêu byte mỗi key, và
   trần LRU đổi lại cái gì (ai được lợi khi một key bị đuổi)?
4. Circuit breaker khác outlier ejection phase 6 ở đâu? Vì sao half-open phải cho **đúng một** request
   — đo được bao nhiêu request rơi vào node vừa hết hạn eject khi không có half-open?
5. Retry khuếch đại sự cố thế nào, bằng số? Retry của phase 5/6 đã "an toàn" (chỉ khi 0 byte, chỉ
   một lần) — vậy budget còn chặn được gì? Vì sao trần là **tỉ lệ** (10 %) chứ không phải số lần?
6. Load shedding: khi upstream chậm, **ai chịu**? Vì sao hàng đợi không trần làm p99 của **mọi**
   request tệ đi chứ không chỉ request thừa? Shed ở mức connection hay request — chọn sai thì sao
   (liên quan câu 2)?
7. Graceful drain: "ngừng accept, `Connection: close`, chờ N giây" — request nào vẫn mất? Có cửa sổ
   nào mà server **không thể** tránh được dù làm đúng, và ai phải vá nó?
8. Bốn invariant của chaoslab: mỗi cái đo bằng lệnh gì, và cái nào dễ vỡ nhất ở proxy Go?

## Giả thuyết đăng ký trước (viết 09:51-09:53, chưa có file `.go` nào của phase)

Kịch bản chung: 4 backend `fixture.Sim` in-process, proxy in-process, generator open-loop mới
(`internal/loadgen`) — mỗi request có **giờ hẹn** `t0 + i/rate`, latency = giờ nhận xong − giờ hẹn.

| # | Giả thuyết | Tỉ số / giá trị kỳ vọng | Lệnh sẽ dùng để chấm |
|---|---|---|---|
| G1 | **Bảy deadline, mỗi cái một test đỏ-được:** Header ⇒ **408** + đóng; body client ⇒ **408**; idle keep-alive ⇒ đóng im lặng, không byte nào; dial (tới `10.255.255.1`) ⇒ **502** sau `DialTimeout` ± 50 ms; upstream head ⇒ **504** (có từ phase 3); upstream body ⇒ đóng giữa body, không response thứ hai; **client đọc chậm** (không đọc 8 MiB response) ⇒ goroutine proxy thoát trong `BodyTimeout` + 1 s, connection upstream **không** về pool. Mỗi cái: thời điểm fire trong ±100 ms của cấu hình | 7/7; sai số ≤ 100 ms | `go test ./internal/proxy -run TestDeadline -v` |
| G2 | **Slowloris không làm Go chết, mà làm Go béo.** `cmd/slowlab` 500 conn từ `127.0.0.2`, mỗi conn 1 byte header / 10 s; probe 50 rps GET từ `127.0.0.1` trong 30 s. (a) HeaderTimeout 10 s: probe **100 %** 200, p99 probe trong **1.5x** baseline (không slowloris). (b) **Tắt HeaderTimeout** (`nodefense7`): probe **vẫn ~100 %** 200 — **ngược ROADMAP** ("trước khi có HeaderTimeout thì không") — vì không có worker pool để cạn. Giá: mỗi conn treo ≈ 8 KiB `br` + 8 KiB `bw` + ~8 KiB stack ≈ **24 KiB** ⇒ 500 conn ≈ **12 MiB** heap+stack; (a) giữ conn ≤ 10 s, (b) giữ mãi | (a) 100 %, p99 ≤ 1.5x; (b) ≥ 99 %, mem/conn 16-32 KiB, conn còn sống sau 30 s: (a) ≤ 500 lần reconnect, (b) 500 conn gốc | `make slowlab`, `make slowlab-nodefense` → `bench/p7-slowlab*.txt` |
| G3 | **Giới hạn connection mở lại cửa Slowloris; shed theo request thì không.** Bật `MaxConns 256` (trần connection toàn cục): (a) tắt HeaderTimeout ⇒ probe **≥ 90 %** thất bại (accept bị chặn / RST); (b) HeaderTimeout 10 s **cũng không cứu** khi attacker reconnect ngay: ≥ 50 % probe thất bại. Shed bằng **trần request đang chạy** (`MaxInflight`, D7) thay vì trần connection: conn slowloris không bao giờ xong head ⇒ không chiếm slot ⇒ probe 100 % | MaxConns: ≥ 90 % / ≥ 50 % lỗi; MaxInflight: 100 % | `go run ./cmd/slowlab -max-conns 256 [-no-header-timeout]` |
| G4 | **Rate limit per-IP sau ranh giới tin cậy.** Token bucket 50/s burst 10. `cmd/ratelab`: 20 IP (`127.0.1.1`-`.20`) mỗi IP 100 rps open-loop trong 5 s ⇒ mỗi IP được **260 ± 5 %** (50×5+10), còn lại **429**. Spoof: một peer không tin, mỗi request XFF ngẫu nhiên ⇒ cả peer được 260 ± 5 %. Phản chứng (`nodefense7`: khoá = XFF thô) ⇒ spoof được **≥ 95 %** request. Bộ nhớ: 1 000 000 IP khác nhau qua peer tin (XFF) ⇒ map giữ ≤ `MaxKeys` 10 000, heap tăng **≤ 5 MiB**; tắt trần ⇒ heap tăng ≈ **100-200 B/key** ⇒ ≥ 100 MiB | 260 ± 13; spoof 260 ± 13 vs ≥ 95 %; heap ≤ 5 MiB vs ≥ 100 MiB | `make ratelab`; `go test ./internal/limit -run TestLimiterMemory -v` |
| G5 | **Half-open đúng một request.** Backend b0 trả 5xx ⇒ eject 200 ms; 32 conn closed-loop qua RR 4 backend; b0 vẫn hỏng lúc hết hạn eject. Có half-open: trong 50 ms đầu sau hết hạn, b0 nhận **đúng 1** request rồi open lại (backoff ×2). Không half-open (`nodefense7` = hành vi phase 6): b0 nhận **≥ 8** (¼ của 32 conn đang chờ) cùng lúc, cả 8 thành 5xx client thấy. b0 hồi phục thật: half-open → closed sau **1** request tốt | 1 vs ≥ 8; 5xx client thấy mỗi lần hết hạn: 1 vs ≥ 8 | `go test ./internal/lb -run TestBreakerHalfOpen -v`; `make breakerlab-nodefense` đỏ |
| G6 | **Retry budget 10 % (+ sàn 10/s).** Cụm 4 backend đều "quá tải": đóng connection reused trước khi trả byte nào với xác suất 50 %. Không budget: số lần gửi sang upstream / số request client **≈ 1.5** (mỗi request lỗi retry đúng một lần, phase 5 D4). Có budget: **≤ 1.12** (10 % + sàn). Giá: tỉ lệ 502 client thấy tăng từ ≈ 25 % (retry cứu một nửa) lên ≈ 45 % | 1.5 ± 0.1 vs ≤ 1.12; 502: ~25 % vs ~45 % | `go run ./cmd/chaoslab -scenario retry` (hoặc test `TestRetryBudget`) |
| G7 | **Shedding giữ p99 của request được nhận.** 4 backend, mỗi cái xử lý tối đa 4 request song song × 10 ms ⇒ capacity ≈ **1 600 rps**. Open-loop **3 200 rps** (2x) trong 10 s. Không shed: hàng đợi lớn dần ⇒ p99 (tính từ giờ hẹn) **≥ 1 s** và tăng theo thời gian chạy, phần lớn kết thúc bằng 504 sau 5 s. Shed (`MaxInflight 16`, `MaxQueue 16`, `QueueTimeout 50 ms`): **45-55 %** nhận 503 trong < 2 ms, p99 của request **được nhận ≤ 50 ms** — tức ≥ **20x** tốt hơn; goodput (200/s) trong ±15 % capacity | p99 nhận: ≥ 1 s vs ≤ 50 ms; 503 45-55 %; goodput 1 360-1 840 rps | `make shedlab` → `bench/p7-shedlab.txt` |
| G8 | **Drain: in-flight không mất, idle-close mất.** Rolling restart: hai instance cùng port (`SO_REUSEPORT`), 32 client keep-alive closed-loop (reconnect khi thấy `Connection: close`/EOF), cũ `Drain(5 s)`, lặp 20 lần. (a) Request đang chạy khi drain: **0** mất, mọi response mang `Connection: close`. (b) **Cửa sổ idle-close**: client ghi request đúng lúc proxy đóng connection rỗi ⇒ EOF không byte nào ⇒ mất — server không tránh được; dự đoán **≥ 1** mất / 20 lần nếu client không retry, **0** nếu client retry request idempotent khi 0 byte (đúng D4 phase 5, phía client). (c) Connection nằm trong **backlog** của listener cũ lúc nó đóng ⇒ RST ⇒ dial thất bại ≥ 1 / 20 lần | (a) 0; (b) ≥ 1 vs 0; (c) ≥ 1 | `make drainlab` |
| G9 | **chaoslab 60 s** (500 rps open-loop, mỗi 300 ms một hành động ngẫu nhiên: kill/revive/slow 10x/err 50 %/**treo** — accept rồi không trả gì): (a) 0 panic; (b) `NumGoroutine` về mức trước tải **± 2** trong 2 s sau khi tải dừng; (c) fd mở của tiến trình về mức nền (sau `Close` proxy) **± 0**; (d) **0** request treo quá deadline client (= `UpstreamHeaderTimeout` + `DialTimeout` + 2 s); mọi request có đúng một status. Dự đoán vỡ: **không** cái nào — nếu vỡ thì (b) trước (goroutine đọc upstream treo không ai đóng) | 0 / ±2 / ±0 / 0 | `make chaoslab` → `bench/p7-chaoslab.txt` |

**Quyết định thiết kế đăng ký trước** (đổi sau phải ghi lý do):

| # | Quyết định | Lý do / hệ quả |
|---|---|---|
| D1 | **Không thêm timeout mới cho 5 loại ROADMAP — đã có từ phase 3** (`Limits.HeaderTimeout/BodyTimeout/IdleTimeout`, `DialTimeout`, `UpstreamHeaderTimeout/BodyTimeout`). Việc của phase là **test từng cái** (G1) + cái thứ 7 (client đọc chậm, `SetWriteDeadline` ở `exchange`) và làm chúng tắt được bằng `nodefense7` cho slowlab | Phase 3 viết deadline theo I3 nhưng chỉ 504 có test. Một deadline chưa từng fire trong test là một deadline chưa có bằng chứng |
| D2 | `cmd/slowlab`: N conn từ `-src 127.0.0.2` (bind `LocalAddr`, loopback nhận mọi 127/8), mỗi conn gửi `GET / HTTP/1.1\r\n` rồi mỗi `-byte-every` một byte `X-a: b\r\n`; reconnect ngay khi bị đóng (đếm). Probe open-loop từ `127.0.0.1`. Đo heap/stack proxy bằng `runtime.ReadMemStats` (proxy in-process) trước/sau | Hai IP để tách attacker và probe ⇒ đúng điều kiện cho per-IP sau này. In-process để đọc được MemStats; ghi rõ đó là bộ nhớ của **cả** tiến trình |
| D3 | `Config.MaxConns` (0 = không trần) **chỉ để chứng minh G3** — trần connection toàn cục bằng semaphore quanh `Accept` (vượt ⇒ đóng ngay). Không bật mặc định | Ghi lại vì sao proxy Go không nên có trần connection như worker pool: trần đó chính là thứ Slowloris cần |
| D4 | Package mới `internal/limit`: `TokenBucket` (float tokens, refill theo `time.Since`, không goroutine) + `KeyedLimiter{Rate, Burst, MaxKeys, TTL}` = map + LRU (`container/list`) dưới **một** mutex; đầy ⇒ đuổi LRU. Khoá = `clientIP` trả về từ `forwardedHeaders` (phase 4 D9) — **không bao giờ** đọc XFF ở đây. Vượt ⇒ **429** + `Retry-After: 1`, body drain, giữ connection | I6. Một mutex: đo ns/op trước khi shard (đừng tối ưu non). Key bị đuổi được bucket mới đầy — lợi cho chính key đó; chấp nhận, ghi ở câu 3 |
| D5 | **Circuit breaker = nâng outlier phase 6 thành 3 trạng thái**, không phải cơ chế thứ hai. closed ⇒ (5 lỗi liên tiếp) ⇒ open (eject như cũ, backoff mũ) ⇒ hết hạn ⇒ **half-open**: `available()` trả true cho **đúng một** `Pick` (CAS cờ `probing`), mọi Pick khác coi như unavailable. Done(ok) ⇒ closed (reset backoff); Done(fail) ⇒ open với backoff kế tiếp | Hai cơ chế cùng đếm lỗi trên một backend sẽ cãi nhau. Phase 6 "hết hạn eject ⇒ dùng lại ngay" chính là "mở lại hết" ROADMAP cảnh báo |
| D6 | **Retry budget** kiểu Finagle trong `internal/limit`: cửa sổ trượt 10 s (10 xô 1 s), `deposit` mỗi request, `withdraw` mỗi retry; cho phép khi `retries < 0.1 × requests + 10 × window_s`. Áp cho **cả** retry D4 (cùng backend) **và** re-pick D9 (dial lỗi). Hết budget ⇒ trả lỗi gốc (502) | Finagle `RetryBudget(ttl 10s, minRetriesPerSec 10, percentCanRetry 0.2)` — ta chọn 10 % theo ROADMAP. Sàn để lưu lượng thấp vẫn retry được |
| D7 | **Shedding theo request, không theo connection**: semaphore `MaxInflight` (0 = tắt) giành sau khi đọc xong head, **sau** rate limit (request bị 429 không tốn slot), **trước** `Pick`. Hàng đợi: đếm atomic số người chờ; ≥ `MaxQueue` ⇒ 503 ngay; chờ quá `QueueTimeout` ⇒ 503. Trả slot bằng `defer` (I7). 503 shed mang `Retry-After: 1` | Conn Slowloris không bao giờ xong head ⇒ không bao giờ giành slot (G3). Body của request bị shed: drain như D8 phase 6 để giữ connection |
| D8 | **`Server.Drain(timeout)`**: `draining=true`; đóng listener; mọi connection đang **rỗi** (chờ ở `Peek` của vòng keep-alive) bị đánh thức bằng `SetReadDeadline(now)` và đóng không trả gì; connection **đang có request**: response mang `Connection: close` rồi đóng; chờ `wg` tối đa `timeout` rồi `Close()` cưỡng bức. `cmd/edgegate`: SIGTERM ⇒ `Drain(30 s)`, SIGINT ⇒ `Close()` | Phân biệt "rỗi" và "đang chạy" cần một trạng thái per-connection (atomic) — đặt ở `serveConn` quanh `Peek` |
| D9 | `SO_REUSEPORT` qua `net.ListenConfig.Control` (`syscall.SetsockoptInt`) bật bằng `Config.ReusePort` — **chỉ** để drainlab chạy hai instance cùng port | Rolling restart thật không có cách khác trên một máy (trừ truyền fd). Hệ quả backlog RST đăng ký ở G8 (c) |
| D10 | `internal/loadgen`: open-loop, `rate`, `duration`, `conns` worker tối đa (đủ lớn để không thành closed-loop — nếu không còn worker rảnh lúc tới giờ hẹn thì request **vẫn** tính latency từ giờ hẹn và ghi `late`), mỗi worker một connection keep-alive raw (`net` + `httpx`); trả mỗi mẫu `{scheduled, done, status, err}`. `cmd/shedlab`, `cmd/chaoslab`, `cmd/ratelab` dùng chung | Trả một phần P6-1 (công cụ có, chưa đo lại phase 6). Không mượn `vegeta`: không cài được ở đây và cần đọc status từng request |
| D11 | `fixture.Sim` thêm: `SetConcurrency(n)` (semaphore — vượt thì xếp hàng trong backend, mô phỏng capacity G7), `SetHang(bool)` (giữ connection, không trả gì), `SetDropReused(p)` (đóng connection keep-alive ở request thứ ≥ 2 với xác suất p, trước khi ghi byte nào — G6). Kill/revive = đóng/mở lại listener cùng port như `TestLBKillRevive` | Một fixture cho cả phase, vẫn là chỗ duy nhất có `net/http` |
| D12 | Phản chứng: file `defense7.go` (`!nodefense7`) / `defense7_off.go` — mỗi phòng tuyến một hằng: `headerTimeoutOn`, `limitByTrustedIP`, `limiterCapped`, `breakerHalfOpen`, `retryBudgetOn`; `make <lab>-nodefense` đỏ đúng dòng. Shedding tắt bằng cấu hình (`MaxInflight 0`), không cần tag | Đúng khuôn phase 1/3/4/5/6; tag riêng cho phase (P4-4) |

## Deliverable

- `internal/proxy`: test 7 deadline (G1), `MaxConns` (D3), rate limit (D4), shedding (D7), retry budget
  (D6), `Drain` (D8), `ReusePort` (D9), `defense7*.go`.
- `internal/limit`: `TokenBucket`, `KeyedLimiter` (LRU + trần), `RetryBudget`; test đơn vị + bộ nhớ.
- `internal/lb`: breaker 3 trạng thái trên outlier (D5) + `TestBreakerHalfOpen`.
- `internal/loadgen` (D10); `internal/fixture.Sim` mở rộng (D11).
- `cmd/slowlab`, `cmd/ratelab`, `cmd/shedlab`, `cmd/drainlab`, `cmd/chaoslab`; Makefile `*lab` + `*-nodefense`.
- `cmd/edgegate` + config: khối `limits` (`rate`, `burst`, `max_keys`), `shed` (`max_inflight`, `max_queue`,
  `queue_timeout_ms`), `retry_budget_percent`, SIGTERM ⇒ drain.

## Reproduce toàn bộ phase

```bash
git checkout <commit phase 7>
go test ./... -count=1 -race
go test ./internal/proxy -run TestDeadline -v                 # G1
make slowlab               | tee bench/p7-slowlab.txt         # G2 (a), G3
make slowlab-nodefense     | tee bench/p7-slowlab-nodefense.txt   # G2 (b) — probe vẫn sống, conn sống mãi
make ratelab               | tee bench/p7-ratelab.txt         # G4
make ratelab-nodefense                                        # PHẢI đỏ: spoof XFF lọt
go test ./internal/limit -run TestLimiterMemory -v            # G4 bộ nhớ
make breakerlab-nodefense                                     # PHẢI đỏ (G5)
make shedlab               | tee bench/p7-shedlab.txt         # G7
make drainlab              | tee bench/p7-drainlab.txt        # G8 + D8′ (-grace 1s)
make chaoslab              | tee bench/p7-chaoslab.txt        # G9
```

## Nhật ký

### 2026-10-01 09:48-10:14 — Turn 1: dựng đủ cơ chế + lab, chưa đo

Chi tiết và output thô: [`phase7-log.md` §1](phase7-log.md). Số trong các lần chạy thử (`-duration 3s`,
`-restarts 4`) **không** phải số đo — chỉ để biết lab chạy; turn 2 đo lại với tham số của Reproduce.

Năm chỗ lộ ra ngay khi test chạy lần đầu, **trước** khi đo:

1. **Bug có từ phase 3: `Server.Close` không đóng connection upstream đang dùng.** `TestDrainTimeoutForces`
   (upstream treo, `Drain(200 ms)`) mất **4.95 s** — handler đang chờ head upstream giữ `wg.Wait` tới
   `UpstreamHeaderTimeout` 5 s. Close "cưỡng bức" chỉ cưỡng bức được phía client. Sửa: `connState.up`
   giữ connection upstream của request hiện tại, `Close` đóng cả hai ⇒ **0.25 s**.
2. **`cmd/edgegate` sẽ thoát giữa drain:** `Serve` trả về ngay khi Drain đóng listener ⇒ `main` return ⇒
   tiến trình chết với request đang chạy. Sửa: `main` chờ goroutine signal. Kiểm bằng binary thật:
   `curl /slow?ms=1500`, SIGTERM giữa chừng ⇒ curl nhận **200 sau 1.50 s**, log "drain xong, 0 cưỡng bức".
3. **Nhận diện request thử của half-open mong manh:** dựa vào "chỉ request thử bắt đầu ≥ `probeAt`".
   Bản đầu của chính test đo latency từ **trước** `Pick` ⇒ request thử trông như request cũ ⇒ không ai
   giải half-open (đỏ "reopens 0"). Proxy đo từ sau `Pick` nên đúng — nhưng một caller khác của `lb` có
   thể mắc. Ghi vào câu 4 turn 3.
4. **Đổi D5:** closed sau thử tốt **không** reset backoff (giữ luật phase 6) — node chập chờn không được
   thử lại ở Base mãi.
5. **Drain có thể mất nhiều request hơn G8 (b) đoán rất nhiều:** lần chạy thử 4 restart mất **181**
   request `io-nohead` / 187 connection rỗi bị đóng — gần như **mỗi** connection rỗi bị đóng làm mất
   đúng request kế của nó, vì client không đọc khi rỗi nên không thấy FIN (đúng bài phase 5, phía client).
   Client retry khi 0 byte ⇒ 0 mất. Chưa phải số đo; turn 2 đo 20 restart và cân nhắc biến thể "drain
   lười" (để connection rỗi nhận request kế kèm `Connection: close` thay vì đóng ngay).

Phản chứng `nodefense7` đã đỏ đúng dòng (`bench/p7-turn1-nodefense.txt` + phase7-log §1): head dở treo
tới deadline client 3 s thay vì 408; spoof XFF **500/500** lọt; map không trần heap **+144.7 MiB**; half-open
tắt ⇒ b0 nhận **8/32**; retry mù ⇒ 19/19 retry. `go test ./... -race` xanh; không package data-path/lab
nào import `net/http`.

### 2026-10-01 10:19-10:40 — Turn 2: đo, chấm G1-G9, thêm D8′ drain lười

Output thô và thứ tự làm: [`phase7-log.md` §2](phase7-log.md). Commit nền `0ed7a23`. Load **2.3-10.1** trên
6 core suốt bài: indexer codegraph (~95 % một core) + chroma-mcp (~35 %) chạy nền, không phải của bài đo —
`uptime` ở đầu mỗi file bench. Mọi số: **open-loop, latency từ giờ hẹn; loopback; proxy + backend +
generator cùng tiến trình, không ghim core.**

**(1) G1 — bảy deadline ×3** (`bench/p7-deadline.txt`):

```console
$ go test ./internal/proxy -run TestDeadline -count=3 -v      # (gộp theo dòng, sort | uniq -c)
      3 head dở: "HTTP/1.1 408 Request Timeout" sau 301-303ms (EOF)
      3 body dở: "HTTP/1.1 408 Request Timeout" sau 302-306ms (EOF)
      3 không gửi gì: 0 byte sau 301ms (EOF)
      3 dial không route: 502 sau 602-610ms (DialTimeout 300ms)
      3 upstream không trả head: 504 sau 301-306ms
      3 upstream dừng giữa body: 43 byte, "HTTP/1.1 200 OK" sau 302-309ms (EOF)
      3 client không đọc 64 MiB: goroutine proxy thoát sau 302-307ms; pool {... DropDirty:1 ... Idle:0}
```

**Đọc kết quả:** 6/7 fire trong +1..+9 ms của 300 ms. Dial là **2×**: chỉ một backend, D9 đổi backend
"nếu picker cho" — picker cho lại chính nó.

**(2) G2/G3 — Slowloris** (`bench/p7-slowlab.txt`, `bench/p7-slowlab-nodefense.txt`, `bench/p7-slowlab-calib.txt`):

```console
$ ./bin/slowlab -conns 500 -byte-every 10s
baseline       n=250 200:100.0%      200: p50 1.59ms p90 2.53ms p99 3.79ms ...
attack:        proxy giữ 500 connection (trước 0); bộ nhớ tiến trình +19.3 MiB ⇒ 39.4 KiB / connection (gồm cả phía attacker)
probe/attack   n=1500 200:100.0%     200: p50 1.59ms p90 2.17ms p99 4.71ms p99.9 9.82ms ...
               attacker: reconnect 1500, dial lỗi 0; proxy giữ 500 connection lúc hết probe
hold 12s:      attacker ngừng gửi, giữ socket ⇒ proxy còn giữ 0 connection
$ ./bin/slowlab-nd -conns 500 -byte-every 10s      # -tags nodefense7: KHÔNG HeaderTimeout
probe/attack   n=1500 200:100.0%     200: p50 1.42ms p90 2.12ms p99 3.72ms ...
               attacker: reconnect 0, dial lỗi 0; proxy giữ 500 connection lúc hết probe
hold 12s:      attacker ngừng gửi, giữ socket ⇒ proxy còn giữ 500 connection
$ ./bin/slowlab -conns 500 -byte-every 10s -max-conns 256
probe/attack   n=1500 200:78.4% timeout:324      200: p50 5.45ms p90 4.01393s p99 6.11091s ...
$ ./bin/slowlab-nd -conns 500 -byte-every 10s -max-conns 256
probe/attack   n=1500 timeout:1500
$ ./bin/slowlab -conns 500 -byte-every 10s -max-inflight 64
probe/attack   n=1500 200:100.0%     200: p50 1.48ms p90 2.21ms p99 5.14ms ...
$ ./bin/slowlab -conns 500 -target null -duration 5s -hold 1s     # ×3: chỉ attacker
attack:        ... ⇒ 18.6 / 18.8 / 18.7 KiB / connection
$ ./bin/slowlab -conns 500 -duration 5s -hold 1s                  # ×3: attacker + proxy
attack:        ... ⇒ 39.4 / 39.5 / 39.3 KiB / connection
```

**Đọc kết quả:** ROADMAP nói "trước khi có HeaderTimeout thì không [phục vụ được]" — **sai với Go**: tắt
HeaderTimeout, 500 connection Slowloris, probe vẫn **100 %**, p99 3.72 ms. Không có worker pool thì không có
gì để cạn. Cái giá là bộ nhớ: **20.6-20.9 KiB/connection** phía proxy (8 KiB `br` + 8 KiB `bw` + stack) ⇒
500 conn ≈ 10 MiB, 100 000 conn ≈ 2 GiB. HeaderTimeout không giảm con số đó khi attacker nối lại (1 500
reconnect, vẫn 500 conn); nó chỉ đảm bảo connection **im lặng** chết (hold: 0 vs 500). Go chết khi có
**trần connection**: MaxConns 256 + không HeaderTimeout ⇒ **100 %** probe treo; có HeaderTimeout vẫn 21.6 %
treo. Shed theo request (MaxInflight 64) ⇒ 100 %: conn Slowloris không bao giờ xong head nên không giành slot.

**(3) G4 — rate limit** (`bench/p7-ratelab.txt`):

```console
$ ./bin/ratelab -ips 20 -rate 50 -burst 10 -per-ip 100 -duration 5s
ip-that        mỗi IP 200: min 253 max 258 (kỳ vọng 260 ± 5 %); khác 200/429: 0
spoof-xff      peer 127.0.0.3, XFF ngẫu nhiên mỗi request: 200 = 259 / 500 (kỳ vọng 260 ± 5 %), 429 = 241
$ ./bin/ratelab-nd ...      # -tags nodefense7: khoá = XFF thô
spoof-xff      peer 127.0.0.3, XFF ngẫu nhiên mỗi request: 200 = 500 / 500 (kỳ vọng 260 ± 5 %), 429 = 0
proxy          rate-limited 4840, khoá đang nhớ 520
$ go test ./internal/limit -run TestLimiterMemory -count=3 -v
    limit_test.go:85: 1e6 khoá: giữ 10000 khoá, đuổi 990000, heap tăng 1.4 / 1.3 / 1.4 MiB
$ go test ./internal/limit -run TestLimiterMemory -count=1 -v -tags nodefense7
    limit_test.go:85: 1e6 khoá: giữ 1000000 khoá, đuổi 0, heap tăng 144.8 MiB (152 B/khoá giữ)
```

**(4) G5 — half-open** (`bench/p7-breaker.txt`): 32 Pick cùng lúc lúc hết hạn eject ⇒ b0 nhận **1** (5/5 lần);
`nodefense7` ⇒ **8** (3/3). Chỉ ở mức `lb` — chưa có bài proxy-level (nợ P7-5).

**(5) G6 — khuếch đại retry** (`bench/p7-retrylab.txt`; 4 backend đóng 50 % connection dùng lại trước byte nào):

```console
$ ./bin/chaoslab -scenario retry -duration 10s -rate 1000 -drop 0.5
retry          n=10000 200:74.7% 502:25.3%
               proxy: retry cho 1200 / từ chối 2526
               upstream nhận 11200 lần gửi cho 10000 request client ⇒ khuếch đại 1.120x; ... 502 = 25.3 %
$ ./bin/chaoslab-nd -scenario retry -duration 10s -rate 1000 -drop 0.5      # retry mù
retry          n=10000 200:100.0%
               upstream nhận 14942 lần gửi cho 10000 request client ⇒ khuếch đại 1.494x; ... 502 = 0.0 %
```

**Đọc kết quả:** khuếch đại đúng đoán (1.12x vs 1.49x). 1 200 retry > trần lý thuyết 1 100 vì cửa sổ trượt:
xô của giây đầu rơi khỏi cửa sổ ở t = 10 s cùng với retry của nó. Phần 502 ngược đoán — xem Giả thuyết sai.
Ở kịch bản này retry **rẻ và luôn thành công**, nên budget chỉ đổi 25 % lỗi lấy 0.37x tải; budget có giá
trị khi retry **không** thành công (backend quá tải thật) — kịch bản đó chưa đo (nợ P7-6).

**(6) G7 — shedding 2x capacity** (`bench/p7-shedlab.txt`):

```console
$ ./bin/shedlab -x 2 -duration 10s
shedlab: 4 backend × conc 4 × 10ms ⇒ capacity 1600 rps; tải 3200 rps (2.0x) trong 10s; shed inflight=16 queue=16 queue-timeout=50ms; generator 256 worker
noshed         n=32000 200:100.0%
               200: p50 5.80686s p90 10.53345s p99 11.63283s p99.9 11.73658s max 11.74865s · goodput 1471/s
               bắn xong lịch sau 21.747s (lịch 10s)
               p99 200 theo giây: s0:1.169s s1:2.307s s2:3.448s s3:4.586s s4:5.796s s5:6.965s s6:8.174s s7:9.364s s8:10.522s s9:11.737s
shed           n=32000 200:45.1% 503:54.9%
               200: p50 21.57ms p90 24.1ms p99 33.08ms p99.9 48.7ms max 62.59ms · goodput 1441/s
               503: p50 710µs p99 8.58ms
               p99 200 theo giây: s0:37ms s1:32ms s2:25ms s3:24ms s4:24ms s5:26ms s6:49ms s7:38ms s8:28ms s9:27ms
```

**Đọc kết quả:** không shed, p99 tăng **≈ 1.15 s mỗi giây chạy** — đúng tốc độ hàng đợi phình (thừa 1 600
rps ⇒ thêm 1 s chờ mỗi giây). Shed: p99 của request **được nhận** 33 ms, phẳng theo thời gian — **352x**.
Goodput gần như bằng nhau (1 441 vs 1 471/s): shed không phục vụ ít hơn, nó chỉ thôi bắt **mọi** người chờ.

**(7) G8 — rolling restart, rồi D8′** (`bench/p7-drainlab.txt`, `-grace.txt`, `-closeidle.txt`): 20 restart / lượt,
2 000 rps, 64 worker.

| Drain | Lượt | Mất `io-nohead` (không retry) | Có client retry | Backlog RST | Drain lâu nhất |
|---|---|---|---|---|---|
| D8 đóng rỗi ngay | 4 | **795 / 991 / 548 / 943** | 0 (retried 766-1 029) | 0 | 8-175 ms |
| D8′ grace 1 s | 2 | **4 / 4** | 0 (retried 5-6) | 0 | 34-39 ms |
| D8′ + sửa `closeIdle` | 3 | **0 / 0 / 0** | 0 (retried 0) | 0 | 37-86 ms |

**Đọc kết quả:** đóng connection rỗi ngay làm mất gần như **mỗi** request kế của nó — client không biết vì
không đọc khi rỗi (đúng bài FIN phase 5, giờ ở phía client). Drain lười để request kế tới, trả nó kèm
`Connection: close` ⇒ client tự đóng có báo trước. Bốn cái còn mất là khe race của chính tôi (response ghi
trước khi `draining` bật) — tách cờ `closeIdle` ⇒ 0 trong 5 × 22 000. `cmd/edgegate` mặc định grace 1 s.

**(8) G9 — chaoslab 60 s × 2 seed** (`bench/p7-chaoslab.txt`):

```console
$ ./bin/chaoslab -duration 60s -rate 500 -tick 300ms -seed 1
chaos          n=30000 200:57.6% 502:5.2% 503:36.2% 504:1.0%
               200 hành động chaos: map[err50:37 hang:31 heal:26 kill:32 revive:37 slow:37]
               127.0.0.1:32945 picks 10579 fails 4134 ejections 6 probes 586 reopens 582 state closed healthy false
               proxy: retry cho 1291 / từ chối 31, shed 0+339, no-available 8903, eject-refused 1173
invariant (c'): connection client còn mở ở proxy 500 ms sau tải: 0 (ngay khi tải xong: 0)
invariant (b): goroutine sau Close proxy + kill backend: 1 (nền trước mọi thứ 1, ±2)
invariant (c): fd sau Close proxy + kill backend: 7 (nền trước mọi thứ 7, ±0)
invariant (d): treo quá 7s: 0; đóng không trả gì (io-nohead): 0; body cụt (io-body): 0; dial lỗi: 0
PASS
$ ./bin/chaoslab -duration 60s -rate 500 -tick 300ms -seed 2
chaos          n=30000 200:66.6% 502:1.8% 503:26.0% 504:5.7%
               proxy: retry cho 521 / từ chối 0, shed 0+1028, no-available 5287, eject-refused 886
invariant (b) 1 / 1 · (c) 7 / 7 · (d) treo 0, io-nohead 0, io-body 0, dial 0 · PASS
```

**Đọc kết quả:** bốn invariant giữ cả hai seed. Status mix không phải "đẹp" (26-36 % 503 vì 4/6 hành động
là hại, chỉ `heal`/`revive` chữa) — nhưng mỗi request có **đúng một** status, không cái nào treo. Một
tương tác lạ: node `32945` có `probes 586 / reopens 582` mà `ejections 6` — thử hỏng, `tryEject` bị **trần
50 %** từ chối ⇒ node ở lại half-open ⇒ thử lại ⇒ hỏng ⇒ … Trần eject + half-open biến thành "một request
thử mỗi lượt" cho node xấu khi cụm đang xấu — có lẽ là điều đúng, nhưng chưa có test nào nói thế (nợ P7-3).

**Đang nghĩ gì:** câu 2 đổi đáp án (Go không chết vì Slowloris; chết vì trần connection). Câu 7 đổi đáp
án: cửa sổ idle-close không hẹp — nó là **mọi** connection rỗi, và server vá được bằng drain lười, không
cần chờ client retry.


## Giả thuyết sai

| # | Tôi tưởng là | Thực tế là | Lệnh + output đã lật tẩy | Đã sửa thế nào |
|---|---|---|---|---|
| G1 vế dial | Dial không route ⇒ 502 sau `DialTimeout` ± 50 ms | **602-610 ms** = 2× — một backend, D9 "chọn backend khác" chọn lại **chính** backend vừa dial lỗi | `go test ./internal/proxy -run TestDeadline -count=3 -v` → `bench/p7-deadline.txt` | Chưa sửa — nợ P7-1 (re-pick loại backend vừa lỗi) |
| G2 vế số conn | HeaderTimeout làm attacker phải giữ ít connection hơn | Attacker nối lại mỗi 10 s (**1 500** reconnect / 30 s) ⇒ proxy vẫn giữ **500** suốt bài. HeaderTimeout giới hạn **tuổi** một connection, không giới hạn **số** connection | `bench/p7-slowlab.txt` dòng "reconnect 1500 … proxy giữ 500" | Chưa sửa — nợ P7-2 (trần connection per-IP) |
| G2 bộ nhớ | 39.4 KiB/conn là của proxy | Gộp attacker cùng tiến trình. Hiệu chuẩn `-target null`: attacker 18.6-18.8 KiB ⇒ proxy **20.6-20.9 KiB** | `bench/p7-slowlab-calib.txt` | Thêm `-target null` vào slowlab |
| G3 vế (b) | MaxConns 256 + HeaderTimeout + attacker nối lại ⇒ ≥ 50 % probe hỏng | **21.6 %** treo (324/1 500), p99 6.1 s — mỗi 10 s cả loạt attacker bị cắt cùng lúc, probe chen vào backlog được 78 % | `bench/p7-slowlab.txt` lượt `-max-conns 256` | Không sửa: kết luận giữ (trần connection mở lại cửa); số nhỏ hơn đoán |
| G6 vế 502 | Không budget ⇒ ~25 % 502, có budget ⇒ ~45 % | **Ngược**: không budget **0 %** (retry D4 luôn dial mới, connection mới không bị đóng ⇒ cứu hết), có budget **25.3 %** | `bench/p7-retrylab.txt` | Không sửa: budget đúng thiết kế; ghi "budget đổi lỗi lấy tải" vào Rút ra |
| G7 vế 504 | Không shed ⇒ phần lớn thành 504 sau 5 s | **100 % 200**, 0 cái 504: hàng đợi nằm ở **generator** (256 worker), không ở proxy ⇒ phía proxy mỗi request chỉ chờ ~160 ms ⇒ deadline upstream không bao giờ fire | `bench/p7-shedlab.txt` | Không sửa: chính là bài học "quá tải nằm ở đâu" |
| G8 (b) | Cửa sổ idle-close hẹp ⇒ ≥ 1 mất / 20 restart | **548-1 031** mất / 20 restart ≈ **99 %** số connection rỗi bị đóng: client không đọc khi rỗi ⇒ không thấy FIN ⇒ request kế **luôn** rơi | `bench/p7-drainlab.txt`, `bench/p7-drainlab-grace.txt` | **D8′ drain lười** (grace 1 s) ⇒ 4 ⇒ sửa khe race `closeIdle` ⇒ **0** (5 × 22 000) |
| G8 (c) | Backlog listener cũ bị RST ⇒ ≥ 1 / 20 restart | **0** trong 10 lượt × 20 restart (đếm riêng `io-nohead` trên connection vừa dial) dù `tcp_migrate_req = 0` | `bench/p7-drainlab-grace.txt` dòng "backlog RST: 0" | Không sửa; ghi điều kiện (instance mới bind **trước** khi cũ đóng; accept loop rút queue nhanh ở 2 000 rps) |
| D8 (bug) | Đóng connection khi về rỗi mà thấy `draining` là an toàn | Response ghi **trước** khi `draining` bật không mang `Connection: close` ⇒ đóng nó là mất request kế: còn **4** mất ở drain lười | `bench/p7-drainlab-grace.txt` (4/4) → `bench/p7-drainlab-closeidle.txt` (0) | Tách cờ `closeIdle` (bật khi Drain quét) khỏi `draining` |

## Số đo

2026-10-01, commit `0ed7a23` + sửa turn 2 (D8′, `closeIdle`, `-target null`), máy `bench/env-GOTIT-00663.txt`
(6 core WSL2), **open-loop (latency từ giờ hẹn), loopback RTT ≈ 0, mọi thành phần cùng tiến trình, không ghim
core, load nền 2.3-10.1** (codegraph + chroma-mcp). Tỉ số là kết luận; số tuyệt đối không mang đi.

| # | Đại lượng | Đo | Tỉ số / kết luận | Kỳ vọng |
|---|---|---|---|---|
| G1 | 7 deadline (300 ms) | 6 cái 301-309 ms; dial 602-610 ms | 6/7 ±10 ms; dial **2×** | 7/7 ±100 ms |
| G2 | Probe dưới 500 conn Slowloris, có / không HeaderTimeout | 100 % / 100 % 200; p99 4.71 / 3.72 ms (nền 3.79 / 7.3) | p99 **1.24x** nền; **Go không chết** | 100 %, ≤ 1.5x; ≥ 99 % |
| G2 | Bộ nhớ proxy / connection treo | 39.3-39.5 − 18.6-18.8 KiB | **20.6-20.9 KiB** | 16-32 KiB |
| G2 | Proxy giữ sau 12 s attacker im, có / không HeaderTimeout | 0 / 500 | timeout giết conn im | 0 / 500 |
| G3 | Probe hỏng: MaxConns 256 không / có HeaderTimeout; MaxInflight 64 | **100 %** / 21.6 % / 0 % | trần conn = cửa Slowloris; shed theo request miễn nhiễm | ≥ 90 / ≥ 50 / 0 |
| G4 | 200 mỗi IP; spoof XFF thường / nodefense | 253-258; 259 / **500** of 500 | ±3 %; spoof bị chặn | 260 ± 13; 260 vs ≥ 95 % |
| G4 | Heap 1e6 khoá, trần 10k / không trần | 1.3-1.4 / **144.8 MiB** | **~100x**; 152 B/khoá | ≤ 5 / ≥ 100 MiB |
| G5 | Request vào node vừa hết hạn eject (32 song song) | **1** / 8 (nodefense) | 8x | 1 / ≥ 8 |
| G6 | Khuếch đại; 502: budget / retry mù | **1.120x** / 1.494x; 25.3 % / 0 % | budget đổi 25 % lỗi lấy 0.37x tải | ≤ 1.12 / 1.5 |
| G7 | p99 của 200: không shed / shed (2x capacity) | 11.63 s / **33.08 ms** | **352x**; 503 54.9 % (p50 0.71 ms); goodput 1 441 vs 1 471/s | ≥ 20x; 45-55 % |
| G8 | Mất / 20 restart: đóng rỗi ngay / drain lười + closeIdle | 548-1 031 / **0** (3 lượt) | ∞ ; backlog RST 0 / 10 lượt | ≥ 1 vs 0 |
| G9 | 60 s chaos ×2: goroutine / fd / treo / io-nohead | 1=1 / 7=7 / 0 / 0 | **4/4 invariant** | ±2 / ±0 / 0 |

**Chấm G1-G9:** G4 ✅, G5 ✅ (mức `lb`), G9 ✅; G1, G2, G3, G6, G7, G8 ❌ **một vế** (bảng Giả thuyết sai).
Không giả thuyết nào sai hẳn — nhưng ba vế sai (G2 số conn, G7 504, G8 b) là ba câu trả lời đổi hẳn.

## Invariant + lệnh kiểm chứng

Chạy lại 2026-10-01 10:42 (load 4.00), output `bench/p7-invariants.txt`; số lab lấy từ turn 2.

| Invariant | Cài ở | Kiểm chứng | Kết quả |
|---|---|---|---|
| **I3 — mọi chỗ đọc/ghi socket có deadline**, cả hai connection (7 chỗ) | `proxy.go:serveConn` (Idle, Header), `forward.go:exchange` (body client đọc + ghi, upstream head/body), `proxy.go:poolFor` (`DialTimeout`) | `make deadlinelab`; phản chứng `-tags nodefense7 -run TestDeadline/header` | 7/7 fire 301-310 ms (dial = 2×, P7-1); nodefense treo tới deadline client 3 s |
| **Close/Drain cưỡng bức đóng CẢ upstream đang dùng** (vá bug phase 3) | `proxy.go:Close` qua `connState.up`; `forward.go:roundTrip` đặt/xoá `st.up` quanh `exchange` | `TestDrainTimeoutForces` | `Drain(200 ms)` với upstream treo: 0.25 s (trước sửa 4.95 s) |
| **Shed theo request**: slot giành sau khi xong head ⇒ connection chưa xong head không chiếm gì | `resilience.go:admit` gọi từ `forward.go:roundTrip` sau `ReadRequest` | `slowlab -max-inflight 64` vs `-max-conns 256` | probe 100 % vs 78.4 % (nodefense: 0 %) |
| **I7 — slot shed, hàng chờ, token trả trên mọi đường ra** | `admit` trả `release` ⇒ `defer release()` trong `roundTrip`; `queued.Add(-1)` ở cả hai nhánh `select` | `TestShedQueue` | 2×200 + 8×503 rồi `Inflight 0, Queued 0` |
| **I6 — khoá rate limit = IP sau ranh giới tin cậy** | `resilience.go:rateKey` (`clientIP` từ `forward.go:forwardedHeaders`); `internal/limit` không đọc header | `TestRateLimitPerIP`; `make ratelab-nodefense` PHẢI đỏ | spoof 5/20 qua (bucket chung); nodefense 500/500 |
| **Bộ nhớ limiter có trần** bất kể số IP attacker gửi | `limit.go:KeyedLimiter.Allow` (LRU, `MaxKeys`) | `TestLimiterMemory`; nodefense PHẢI đỏ | 1e6 khoá: +1.4 MiB vs +144.7 MiB |
| **Half-open cho đúng một request thử** | `balancer.go:Pick` (CAS `probing`), `backend.go:available`, `outlier.go:resolveProbe` | `TestBreakerHalfOpen`; `make breakerlab-nodefense` PHẢI đỏ | 1/32 vs 8/32 |
| **Retry ≤ budget** (D4 cùng backend **và** D9 đổi backend) | `resilience.go:allowRetry` ở hai nhánh retry của `forward.go:roundTrip`; `limit.go:RetryBudget` | `TestRetryBudget` (limit + proxy); nodefense PHẢI đỏ | 1 100 / 10 000 = 11 %; proxy 11×200 / 9×502, nodefense 20×200 |
| **Drain không mất request**: in-flight xong kèm `Connection: close`; rỗi được grace; khe "response trước khi draining bật" đóng bằng `closeIdle` | `resilience.go:Drain`; `proxy.go:serveConn` (cặp `idle` / `closeIdle`) | `TestDrainInflightAndIdle`, `TestDrainLazyIdle`; `drainlab -grace 1s` | 0 mất / 3 lượt × 22 000 (đóng ngay: 548-1 031) |
| **I8 — không rò goroutine / connection** dưới chaos | toàn bộ đường trên + `fixture.Sim` hang chờ `ctx.Done()` | `TestNoGoroutineLeak`; `make chaoslab` | 6 → 6; chaos 60 s ×2: goroutine 1=1, fd 7=7, 0 treo |
| **Data path không `net/http`** | `internal/{proxy,lb,limit,loadgen}`, `cmd/edgegate` | `grep -rln '"net/http"' … \| grep -v _test.go` | rỗng |

## Đọc gì

- ROADMAP phase 7; Envoy outlier detection (`max_ejection_percent`); Slowloris (RSnake 2009);
  `SO_REUSEPORT` (`socket(7)`).
- **Finagle `RetryBudget`**, đọc nguồn turn 3 (`finagle-core/.../service/RetryBudget.scala`, nhánh `develop`):
  `DefaultTtl = 10.seconds`, `DefaultMinRetriesPerSec = 10`, `apply()` dùng `percentCanRetry` **0.2** — D6 ghi
  đúng tham số; ROADMAP "≤ 10 % (cách Finagle làm)" là **chặt hơn** mặc định Finagle (20 %). Cài đặt của họ
  là token bucket "leaky" số nguyên: mỗi `deposit()` +1000, mỗi retry −1000/percent (5000 ở 0.2), dự trữ
  `minRetriesPerSec × ttl × withdrawal` — cùng ngữ nghĩa với cửa sổ 10 xô của ta, khác cách tính. Docs
  Clients guide: "share a single instance of `RetryBudget` between both `RetryFilter` and `RequeueFilter`
  to prevent retry storms" — đúng chỗ ta làm: một budget cho cả D4 và D9.
- **Linux `tcp_migrate_req`** (docs.kernel.org ip-sysctl): "When a listener is closed, in-flight request
  sockets during the handshake and established sockets in the accept queue are aborted"; mặc định **0**;
  bật thì con được chuyển sang listener khác cùng nhóm `SO_REUSEPORT`. Máy này 0 ⇒ G8 (c) **đúng là có
  thể** xảy ra; đo 0/10 lượt vì accept loop rút queue nhanh hơn 2 000 rps — không phải vì kernel cứu.

## Rút ra

**1. "Mọi connection đều có deadline" là bảy deadline, và một trong số đó đã nằm sai chỗ từ phase 3.**
Phía client: rỗi chờ request kế (`IdleTimeout` ⇒ đóng im lặng, không byte nào — không có request thì
không có gì để trả lời), head (`HeaderTimeout` tính từ **byte đầu**, không phải từ mỗi byte ⇒ 408), body
request (`BodyTimeout` ⇒ 408), và **ghi** response cho client đọc chậm (`BodyTimeout` trên `SetWriteDeadline`
⇒ đóng, connection upstream dở body bị bỏ chứ không về pool). Phía upstream: dial (`DialTimeout` ⇒ 502),
head (`UpstreamHeaderTimeout` ⇒ 504), body (`UpstreamBodyTimeout` ⇒ đóng giữa body — head đã đi, không còn
response thứ hai để gửi). Cả bảy fire trong 1-10 ms của cấu hình. Trước phase này chỉ 504 có test; một
deadline chưa từng fire trong test là một deadline chưa có bằng chứng. Hai thứ lộ ra: dial fire **hai lần**
khi chỉ có một backend (D9 "chọn lại" chọn đúng nó, P7-1), và `Close()` "cưỡng bức" chỉ cưỡng bức được phía
client — handler đang chờ upstream treo giữ `Close` thêm tới 5 s, bug có từ phase 3 mà không test nào chạm
tới vì không test nào đóng server **trong lúc** upstream treo.

**2. Slowloris không giết Go — một trần connection mới giết Go.** Slowloris giết Apache prefork vì mỗi
connection giữ một **worker** trong pool có hạn (`MaxClients`); 500 connection gửi một byte mỗi 10 s là đủ
chiếm hết, và request thật xếp hàng ngoài. Proxy Go goroutine-per-connection không có pool đó: tắt hẳn
HeaderTimeout, 500 connection Slowloris, probe vẫn **100 %**, p99 3.72 ms. ROADMAP nói "trước khi có
HeaderTimeout thì không" — sai với kiến trúc này. Cái giá thật là bộ nhớ và fd: **20.7 KiB mỗi connection**
treo (8 KiB `br` + 8 KiB `bw` cấp phát ngay ở `serveConn` + stack), nghĩa là 100 000 connection ≈ 2 GiB, và
mỗi cái một fd. Và HeaderTimeout **không** giảm số đó khi attacker chịu nối lại: 1 500 lần reconnect trong
30 s, proxy vẫn giữ 500 suốt bài. Nó chỉ đảm bảo connection **im lặng** chết (0 vs 500 sau 12 s). Go chết khi
ta thêm thứ giống worker pool: `MaxConns 256` ⇒ **100 %** probe treo khi không có HeaderTimeout, 21.6 % ngay
cả khi có. Nên câu trả lời là: phòng Slowloris bằng **không** đặt trần connection toàn cục, shed theo
request, và giới hạn **số connection mỗi IP** (chưa có, P7-2) — HeaderTimeout là điều kiện cần, không đủ.

**3. Rate limit: IP nào, và trần bộ nhớ đổi lấy gì.** IP sau ranh giới tin cậy của phase 4, không bao giờ
`X-Forwarded-For` thô. Bằng số: một peer không tin gửi XFF ngẫu nhiên mỗi request được **259/500** — đúng
một bucket 50/s burst 10 trong 5 s; đổi khoá sang XFF thô (`nodefense7`) thì **500/500** và limiter nhớ 520
khoá — mỗi request giả một "người dùng" mới đầy token. Đặt limiter ở đâu cũng vô nghĩa nếu khoá do client
chọn. Map per-IP không dọn là rò rỉ attacker điều khiển được: **152 B mỗi khoá** (đo; thành phần — string,
entry, phần tử list, slot map — là suy luận, không profile), 1 000 000 IP giả qua một proxy tin ⇒ **+144.7 MiB**; trần LRU 10 000 ⇒ +1.4 MiB. Trần đổi
lấy một điều: khoá bị đuổi quay lại với bucket **đầy**. Ai lợi? Chỉ chính khoá đó — attacker xoay vòng đủ IP
để đuổi một nạn nhân thì làm nạn nhân **được** thêm burst, không bị mất. Chấp nhận được; cái không chấp nhận
được là attacker xoay vòng được IP **qua ranh giới tin cậy** — đó là cấu hình `trusted_proxies`, không phải
limiter.

**4. Circuit breaker khác outlier ở một trạng thái, và trạng thái đó là toàn bộ điểm.** Outlier phase 6
có hai trạng thái: dùng được / bị eject tới `ejectedUntil`. Hết hạn là dùng lại ngay cho **mọi** Pick — và 32
request đang chờ thì ¼ số đó (**8**) rơi vào node vừa hết hạn mà vẫn hỏng, cả 8 thành 5xx client thấy, mỗi
lần hết hạn. Đó là "mở lại hết đúng lúc backend vừa hồi". Half-open chen giữa: hết hạn ⇒ chỉ **một** Pick
giành được lượt thử (CAS), mọi Pick khác coi node như vẫn đóng; thử tốt ⇒ closed, thử hỏng ⇒ open lại với
backoff kế tiếp, không chờ đủ 5 lỗi. Đo: **1/32**. Hai điều phase này học được mà chưa đăng ký: (a) nhận ra
"đâu là request thử" mà không đổi API `Pick/Done` dựa vào thời điểm bắt đầu ≥ `probeAt` — đúng với proxy,
nhưng chính test của tôi đã mắc khi đo latency từ trước `Pick`; caller khác của `lb` sẽ mắc tương tự; (b)
khi trần eject 50 % từ chối mở lại, node xấu ở lại half-open và nhận đúng một request mỗi lượt (582 lần thử
trong chaoslab) — có lẽ đúng ý, nhưng là hành vi nảy ra từ hai cơ chế, không phải thiết kế (P7-3).

**5. Retry khuếch đại thế nào, và budget đổi gì lấy gì.** Retry của phase 5/6 đã an toàn theo nghĩa
**đúng đắn** (chỉ khi 0 byte response, chỉ một lần, lần hai dial mới). Nhưng an toàn đúng đắn không phải
an toàn **tải**: cụm đóng 50 % connection dùng lại ⇒ upstream nhận **1.494x** request; retry "3 lần" mù thì
2x, 3x, 4x — đúng lúc cụm đang yếu. Budget 10 % + sàn 10/s cắt về **1.120x**. Trần là tỉ lệ vì cụm phục vụ
1 000 rps chịu được 100 retry/s mà cụm 100 000 rps cũng chịu được 10 000 — một con số tuyệt đối thì hoặc vô
dụng ở tải cao hoặc chặn hết ở tải thấp; sàn để client vừa khởi động vẫn retry được. Finagle mặc định 20 %
và khuyên dùng **một** budget cho mọi loại retry — ta làm thế (D4 + D9). Cái tôi đoán sai: ở kịch bản đo,
retry luôn thành công (connection mới không bị đóng), nên budget **đổi 25 % lỗi 502 lấy 0.37x tải** — một
trao đổi tệ khi retry rẻ. Budget có giá trị khi retry **không** cứu được gì (backend quá tải thật) — kịch
bản đó chưa đo (P7-6). Bài học: budget là bảo hiểm cho sự cố, không phải tối ưu cho ngày thường.

**6. Load shedding: ai chịu quá tải, và vì sao hàng không trần làm hại cả request "đáng được phục vụ".**
2x capacity, không shed: p99 tăng **1.15 s mỗi giây chạy**, tới 11.6 s; có shed: 55 % nhận 503 trong 0.7 ms,
request **được nhận** p99 **33 ms** phẳng suốt bài — 352x. Goodput như nhau (1 441 vs 1 471/s): shed không phục
vụ ít hơn, nó chỉ chọn **ai** chờ. Không shed thì hàng đợi là FIFO không trần, và request thứ N phải chờ mọi
request trước nó — kể cả những cái client đã bỏ cuộc. Mỗi giây quá tải cộng thêm một giây chờ cho **mọi
người** về sau. Ai chịu? Tôi đoán upstream (504 sau 5 s) — sai: hàng nằm ở **client** (generator 256 worker),
phía proxy mỗi request chỉ thấy cỡ
~160 ms (ước tính, không đo: ≤ 256 request trong proxy, 16 chạy, 240 chờ ở backend / 1 600 rps), nên không deadline nào fire, và một dashboard đo latency ở proxy sẽ
**không thấy gì bất thường** trong khi người dùng chờ 11 s. Đây là lý do shedding phải đo bằng open-loop từ
phía client. Và shed **theo request**, không theo connection: shed theo connection chính là trần Slowloris
cần (câu 2).

**7. Graceful drain: request nào vẫn mất, và ai vá được.** Request đang chạy lúc drain: **0** mất —
response mang `Connection: close`, client biết mà đóng. Cái mất là ở connection **rỗi**: đóng nó ngay (bản
đầu của tôi) mất **548-1 031** request / 20 restart — gần như **mỗi** connection rỗi bị đóng
làm rơi đúng request kế của nó. Tôi đã đăng ký đây là "cửa sổ hẹp, ≥ 1": sai bậc. Không hẹp, vì client
**không đọc khi rỗi** nên không thấy FIN; lần ghi kế tiếp đi vào connection đã đóng — đúng bài FIN-trong-kernel
phase 5, lần này ở phía client. Client có thể vá (retry idempotent khi 0 byte ⇒ 0 mất), nhưng server vá
**tốt hơn**: drain lười — không đóng connection rỗi, để request kế của nó tới và trả lời kèm `Connection:
close` (grace 1 s) ⇒ client được báo trước khi connection chết. Còn 4/22 000 mất là khe race của chính tôi
(response ghi trước khi cờ drain bật không mang close, rồi bị đóng lúc về rỗi) — tách cờ `closeIdle` ⇒ **0**
trong 5 lượt. Cái server **không** vá được: connection nằm trong accept queue của listener cũ lúc nó đóng —
kernel abort chúng (`tcp_migrate_req = 0` mặc định). Đo 0/10 lượt vì accept loop rút queue nhanh; dưới tải
lớn hơn tốc độ accept thì nó là thật, và cách vá là `tcp_migrate_req = 1` hoặc truyền fd listener sang tiến
trình mới (không đóng listener).

**8. Bốn invariant của chaoslab và cái nào dễ vỡ nhất.** (a) không panic: tới được dòng cuối; (b) goroutine
về nền: `runtime.NumGoroutine` sau `Close` proxy so với trước mọi thứ (1 = 1); (c) connection về nền: đếm
`/proc/self/fd` (7 = 7) — đo fd của chính tiến trình thay vì `ss | grep ESTAB` cả máy, vì máy có session
khác; và nền phải đo **sau** khi netpoller đã mở epoll + eventfd, không thì lệch 2 tuỳ lượt; (d) không treo:
0 request quá deadline client = tổng chuỗi deadline của proxy + dư. Dự đoán (b) dễ vỡ nhất — goroutine đọc
upstream treo mà không ai đóng. Nó không vỡ trong chaoslab, nhưng **đúng là** chỗ đó: `TestDrainTimeoutForces`
lộ chính handler đó giữ `Close` 4.95 s. Chaos không bắt được vì nó dừng proxy **sau** khi tải xong, lúc không
còn handler nào chờ upstream; bug chỉ lộ khi đóng **giữa** lúc treo. Bài học cho phase sau: invariant "về nền"
phải được kiểm cả lúc dừng **trong** sự cố, không chỉ sau nó.

## Nợ kỹ thuật

Chi tiết + lệnh trả trong `docs/debts.md`. P6-1 trả **một phần**: `internal/loadgen` open-loop đã có, chưa
đo lại `lblab -recover` với nó.


- [ ] **P7-1** 🔧 D9 "chọn backend khác" chọn lại chính backend vừa dial lỗi khi picker cho ⇒ 2× DialTimeout
  (G1). Loại backend vừa lỗi khỏi lượt chọn lại; chỉ một backend ⇒ 502 ngay.
- [ ] **P7-2** 🔧 HeaderTimeout không giới hạn **số** connection một IP giữ (500 Slowloris từ một IP).
  Thêm `MaxConnsPerIP` (khoá = peer, không XFF); slowlab đòi attacker bị giới hạn, probe từ IP khác 100 %.
- [ ] **P7-3** ⏳ Trần eject 50 % + half-open: node xấu ở lại half-open, mỗi lượt một request thử khi bị
  từ chối mở lại (582 reopen). Hành vi chưa đăng ký, chưa có test.
- [ ] **P7-4** 📏 chaoslab 4/6 hành động là hại ⇒ 26-36 % 503; đo thêm một tỉ lệ cân (heal ≥ hại) để status
  mix có nghĩa, và một seed có backend "treo" kéo dài để thử deadline client.
- [ ] **P7-5** 📏 G5 chỉ đo ở mức `lb`; thiếu bài qua proxy (32 conn closed-loop, đếm 5xx client thấy mỗi
  lần hết hạn eject).
- [ ] **P7-6** 📏 Retry budget chưa đo ở kịch bản nó **có** giá trị: retry không thành công (backend quá tải
  thật, dial mới cũng hỏng) — khi đó retry mù là 2x tải lên cụm đang chết.
