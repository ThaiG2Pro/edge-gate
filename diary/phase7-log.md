# Phase 7 — log thô

> Bản biên tập: [`phase7.md`](phase7.md). File này ghi theo thứ tự thời gian, kể cả ngõ cụt.

## §0 Mang từ phase 0-6 sang (đọc lại trước khi gõ)

1. Closed-loop giấu quá tải (phase 0 bài 3, P6-1): shedding **bắt buộc** open-loop.
2. p99 chỉ nói về 1 % chậm nhất (phase 6 G1): ghi tỉ lệ từng status cạnh latency.
3. Phòng tuyến chỉ tin khi đã thấy nó đỏ lúc tắt. Tag riêng `nodefense7` (P4-4).
4. Bộ đếm nào cũng phải giảm trên mọi đường ra (I7): token, slot shed, budget retry — ba chỗ mới.
5. Đừng gán giờ ước lượng vào log (phase 6 turn 1 đã phải bỏ số bịa); chỉ ghi mốc đọc từ `date`.
6. Vận hành: `go build` một dòng riêng rồi mới `&`; `pkill -x`.

## §1 Turn 1 — 2026-10-01 09:48 → 10:14

- 09:48 đọc ROADMAP phase 7, `proxy.go`, `forward.go`, `respond.go`, `cmd/edgegate`, `lb/outlier.go`.
  Phát hiện: 5 timeout ROADMAP đòi **đã có** từ phase 3 (6 cái, kể cả `UpstreamBodyTimeout`), nhưng
  chỉ `UpstreamHeaderTimeout` có test (`TestUpstreamSlowIs504`). ⇒ D1: phase không thêm timeout, thêm test.
- Trong lúc viết G2 tự lật ROADMAP: proxy goroutine-per-connection không có worker pool để Slowloris làm
  cạn ⇒ đoán probe **vẫn sống** khi tắt HeaderTimeout. Cái làm Go chết là một trần connection (G3) — và
  shedding theo connection chính là trần đó ⇒ D7 shed theo request.
- Trong lúc viết D5: outlier phase 6 hết hạn eject ⇒ node available ngay cho **mọi** Pick — đó chính là
  "mở lại hết" ROADMAP cảnh báo. Breaker = thêm half-open vào outlier, không phải cơ chế thứ hai.
- 09:51-09:53 viết `phase7.md`: 8 câu hỏi, G1-G9, D1-D12. Chưa có file `.go`.
- (1) `internal/limit`: `KeyedLimiter` (map + LRU, một mutex), `RetryBudget` (10 xô × 1 s). Test chạy
  lần đầu xanh; **phản chứng bộ nhớ đỏ đúng chỗ** (đo sớm, chấm ở turn 2):

```console
$ go test ./internal/limit -count=1 -v
    limit_test.go:24: 50/s burst 10, 100 lần/s × 5 s: cho 259
    limit_test.go:85: 1e6 khoá: giữ 10000 khoá, đuổi 990000, heap tăng 1.4 MiB (148 B/khoá giữ)
    limit_test.go:111: 10 000 request trong 10 s, mỗi cái muốn retry: cho 1100 (11.0 %)
$ go test ./internal/limit -count=1 -v -tags nodefense7 -run TestLimiterMemory
    limit_test.go:88: heap tăng 144.8 MiB > 5 MiB — map per-IP không trần là rò rỉ attacker điều khiển được
--- FAIL: TestLimiterMemory (5.58s)
```

- (2) `fixture.Sim` (D11): `SetConcurrency`, `SetHang` (chờ `r.Context().Done()` ⇒ không rò khi proxy
  đóng), `SetDropReused` (đếm request/connection qua `ConnContext`, Hijack + Close trước byte nào),
  `SimServer.Kill/Revive` cùng port.
- (3) `internal/loadgen` (D10): lịch `t0 + i/rate`, latency từ giờ hẹn, worker nối lười, `RetryZeroByte`.
- (4) proxy: `resilience.go` (admit = rate limit → shed; budget; `Listen` reuseport; `Drain`),
  `connState.idle` cho Drain, `MaxConns` giành chỗ trước Accept. Ngõ cụt nhỏ: (a) `syscall` không có
  `SO_REUSEPORT` ⇒ hằng 15 từ `asm-generic/socket.h`; (b) bản đầu `Close()` "nhả một chỗ" connSem để
  gỡ Serve — sai: lấy mất chỗ `untrack` cần trả ⇒ `untrack` có thể chặn mãi; bỏ, untrack tự nhả.
  Suite cũ `-race` xanh sau khi gắn (retry budget sàn 100/10 s đủ cho mọi test phase 5-6).
- (5) breaker D5 trên outlier: `available()` loại half-open khi đã có người giữ lượt; `Pick` giành
  lượt bằng CAS; `Done` nhận ra request thử vì chỉ nó bắt đầu ≥ `probeAt`. **Đổi D5 một chi tiết:**
  closed sau thử tốt **không** reset backoff (giữ luật phase 6: yên 2×MaxEject mới về Base) — node
  chập chờn không được thử lại ở Base mãi. `TestBreakerHalfOpen` lần đầu đỏ ở "reopens 0": test đo
  latency từ **trước** `Pick` ⇒ request thử trông như request cũ, không ai giải half-open. Proxy đo từ
  sau `Pick` nên không mắc, nhưng đây là chỗ cơ chế mong manh — ghi. Sau sửa test: 3/3 xanh;
  `-tags nodefense7`: b0 nhận **8/32** (đỏ đúng dòng), bản thường **1/32**.
- (6) test proxy phase 7 (`phase7_test.go`) chạy lần đầu: **2 đỏ**.
  (a) `TestRetryBudget` — test sai: quên rằng connection reused bị bỏ ⇒ request kế dial **mới** ⇒ sim
  không đóng connection mới ⇒ chuỗi 200/502 xen kẽ (11/9), không phải 2/18. Thêm: `Percent 1e-9` làm
  trần = 1 + ε ⇒ cho retry thứ hai; thêm `Percent < 0 ⇒ 0 %`.
  (b) `TestDrainTimeoutForces` — **bug thật**: `Drain(200 ms)` mất 4.95 s vì `Close` không đóng connection
  upstream đang dùng. Sửa bằng `connState.up` (xem phase7.md mục 1).
- (7) phản chứng `-tags nodefense7` cho 3 test proxy: header-408 treo 3.0 s; spoof `map[200:20]`;
  retry mù `map[200:20]`, retry 19. Đỏ đúng dòng.
- (8) lab: `slowlab` (bản đầu pha hold vẫn nối lại khi bị đóng ⇒ đếm sai "proxy còn giữ" — sửa: pha
  hold không nối lại), `ratelab` (mỗi IP một generator — `LocalIPFor` theo request làm worker đổi
  connection mỗi request), `shedlab` (bản đầu chia goodput cho **lịch** 3 s trong khi noshed mất 6.8 s mới
  xong — sửa chia thời gian thật), `drainlab`, `chaoslab` (fd nền lệch 2 tuỳ lượt vì netpoller mở epoll +
  eventfd ở lần dùng mạng đầu — sửa: listen tạm trước khi đo nền).
- Chạy thử (không phải số đo): shedlab 3 s — noshed p99 200 **3.80 s** tăng theo giây (1.37 / 2.60 /
  3.82 s), shed 54.4 % 503 (p50 660 µs), p99 200 **26.9 ms** phẳng; drainlab 4 restart — noretry mất 181
  `io-nohead`, retry 0; chaoslab 8 s PASS nhưng chaos thiên về kill (10 kill / 5 revive / 1 heal) ⇒ cuối bài
  cả 4 backend unhealthy, 28.7 % 503 — turn 2 cân lại tỉ lệ hành động; retry 3 s: budget 1.133x / 502
  24.4 %, mù 1.487x / 502 0 % — G6 đoán "không budget 502 ~25 %" sai hướng: retry D4 luôn dial mới, connection
  mới không bị sim đóng ⇒ retry mù cứu hết.
- (9) `-race` toàn bộ: `TestDeadline/slow-reader` đỏ — dưới race fixture sinh 64 MiB chậm ⇒
  `UpstreamHeaderTimeout` 300 ms fire trước (504), test đo nhầm deadline; rồi lần hai đỏ vì vòng chờ bắt
  đầu trước khi proxy accept (`ConnsActive` = 0 sẵn). Sửa test: nới deadline phía upstream cho subtest này,
  đồng hồ bắt đầu khi byte đầu của head tới client. 3/3 xanh dưới race, 301-303 ms.
- (10) `cmd/edgegate`: khối `rate_limit`, `shed`, `retry_budget_percent`, `drain_timeout_ms`, `reuse_port`;
  `config/resilience.json`. SIGTERM ⇒ Drain; **bug**: `main` thoát khi `Serve` trả về lúc Drain đóng
  listener — sửa: chờ goroutine signal. Binary thật: curl `/slow?ms=1500` + SIGTERM ⇒ 200 sau 1.50 s.
- `go test ./... -race` xanh; `go vet -tags nodefense7 ./...` sạch; `make breakerlab-nodefense`,
  `make ratelab-nodefense` đỏ đúng dòng (`bench/p7-turn1-nodefense.txt`).

## §2 Turn 2 — 2026-10-01 10:19 → 10:40

- 10:19 commit nền `0ed7a23`, load **5.43** trên 6 core: `ps` thấy indexer codegraph (~95 % một core) và
  chroma-mcp (~35 %) — không phải của bài đo, không tắt. Ghi `uptime` đầu mỗi file bench; chấm bằng tỉ số.
- 10:20 G1 `bench/p7-deadline.txt` (×3): 6/7 deadline fire trong 301-309 ms; **dial 602-610 ms** = 2×
  DialTimeout — một backend ⇒ D9 chọn lại CHÍNH backend chết. Vế dial của G1 sai.
- 10:20 G4 `bench/p7-ratelab.txt`: đúng cả bốn vế. 10:21 G5 `bench/p7-breaker.txt`: 1/32 ×5, nodefense 8/32 ×3.
- 10:21-10:26 G2/G3 slowlab (5 lượt). Bộ nhớ "39.4 KiB/conn" gộp attacker ⇒ thêm `-target null` (attacker
  nối vào listener chỉ accept rồi giữ) để hiệu chuẩn: attacker 18.6-18.8 KiB ⇒ proxy **20.6-20.9 KiB/conn**.
- 10:29 G6 `bench/p7-retrylab.txt`, G7 `bench/p7-shedlab.txt`.
- 10:30 G8 `bench/p7-drainlab.txt` ×2: noretry mất 795 / 991 `io-nohead` (≈ 99 % số connection rỗi bị
  đóng), retry 0; dial lỗi 0. **RST backlog (G8 c) không tách được khỏi idle-close**: kernel bắt tay xong
  rồi mới RST ⇒ client thấy dial OK rồi đọc lỗi = `io-nohead`. Retry chỉ cứu connection REUSED, mà lượt
  retry mất 0 ⇒ connection mới không mất lần nào ⇒ (c) không xảy ra trong 2 × 20 restart.
- **Biến thể D8′ "drain lười"** (đăng ký trước khi code, 10:31): `Config.DrainIdleGrace` — lúc drain,
  connection rỗi KHÔNG bị đóng ngay mà được chờ tối đa grace để gửi request kế, request đó nhận response
  kèm `Connection: close`; hết grace mới đóng phần còn rỗi. **G8′:** grace 1 s, cùng drainlab (2000 rps,
  64 worker ⇒ mỗi connection dùng lại mỗi ~32 ms ≪ 1 s): noretry `io-nohead` **≤ 1 %** của bản đóng ngay
  (≤ 10 / 20 restart); giá: drain lâu nhất tăng tới ≈ grace chỉ khi có connection rỗi không bao giờ gửi
  (ở drainlab: không có ⇒ drain lâu nhất vẫn < 200 ms).
- 10:31-10:35 D8′: `DrainIdleGrace` + `TestDrainLazyIdle` (xanh ×3 dưới race). drainlab grace 1 s ×2:
  **4 / 4** mất (từ 548 / 943) — G8′ (≤ 10) đúng, nhưng 4 ≠ 0: khe race của chính tôi — response ghi
  TRƯỚC khi `draining` bật (không mang `Connection: close`) rồi `serveConn` thấy `draining` lúc về rỗi
  ⇒ đóng ⇒ request kế mất. Tách cờ `closeIdle` (Drain bật lúc quét) khỏi `draining`; `serveConn` chỉ
  thoát sau roundTrip khi response đã mang close. Đo lại: **0** (3 lượt × 2 run). `cmd/edgegate` mặc định
  `drain_idle_grace_ms` 1000.
- 10:36-10:38 G9 chaoslab 60 s seed 1 + 2: PASS cả hai. Quan sát: node `probes 586 / reopens 582 /
  ejections 6` — thử hỏng nhưng `tryEject` bị trần 50 % từ chối ⇒ ở lại half-open ⇒ thử lại.
- 10:39 `go test ./... -race` xanh, `go vet -tags nodefense7 ./...` sạch. Makefile: `drainlab` thêm lượt
  `-grace 1s`, `slowlab` thêm lượt hiệu chuẩn `-target null`.
- 10:40-10:40 viết `phase7.md` turn 2 (nhật ký, giả thuyết sai, số đo). Rà số với output: sửa "8-180 ms" →
  8-175 ms, "~105x" → ~100x.

## §3 Turn 3 — 2026-10-01 10:41 → 10:45

- 10:41 song song: agent đọc Finagle `RetryBudget.scala` + docs.kernel.org `tcp_migrate_req`. D6 đúng tham
  số Finagle (ttl 10 s, 10/s, **0.2** — ROADMAP "10 %" chặt hơn mặc định của họ). `tcp_migrate_req = 0` ⇒
  accept queue của listener đóng bị abort ⇒ G8 (c) có thể xảy ra; 0/10 lượt vì queue rỗng lúc đóng.
- 10:42 chạy lại lệnh bảng invariant (`bench/p7-invariants.txt`, load 4.00): xanh hết; ba phản chứng
  proxy + `ratelab-nodefense` + `breakerlab-nodefense` đỏ đúng dòng; grep `net/http` rỗng.
- Viết Rút ra 8 câu. Rà lại câu chữ: bỏ "cách nginx làm" (chưa đọc nguồn nginx), đánh dấu "~160 ms phía
  proxy" là ước tính, thành phần 152 B/khoá là suy luận.
- `docs/debts.md`: P7-1..P7-6; P6-1 trả một phần (công cụ loadgen có, chưa đo lại lblab). ROADMAP/README hàng 7 ✅.
