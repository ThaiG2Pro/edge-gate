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
