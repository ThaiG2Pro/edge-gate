# Phase 8 — log thô

> Bản biên tập: [`phase8.md`](phase8.md). File này ghi theo thứ tự thời gian, kể cả ngõ cụt.

## §0 Mang từ phase 0-7 sang (đọc lại trước khi gõ)

1. Mọi chỗ đọc socket cần deadline (I3) — TLS thêm ClientHello (phase 7 có 7 chỗ).
2. Routing phải có **một** nguồn sự thật (phase 4): SNI vs `Host`.
3. Probe phase 5 chỉ chạy trên fd TCP thật — `tls.Conn` giấu fd.
4. RTT phải đo (ping/TCP connect), không suy từ tham số netem; netem lo = hai chiều; nhớ `rtt-down`.
5. Không ghi giờ ước lượng; chỉ mốc từ `date`.

## §1 Turn 1 — 2026-10-01 10:52 → 11:07

- 10:52 đọc ROADMAP phase 8, Makefile `tlslab` (gọi `scripts/gen-certs.sh` — **không tồn tại**; `config/tls.json`
  cũng không), `pool_linux.go:probeIdle` (ép `syscall.Conn` ⇒ `tls.Conn` không có ⇒ probe tắt lặng lẽ),
  `proxy.go:setNoDelay` (ép `*net.TCPConn` ⇒ tls.Conn bị bỏ qua).
- Trong lúc viết G7 tự lật một kỳ vọng ROADMAP: "session resumption cắt được bao nhiêu" — ở TLS 1.3 không
  có 0-RTT thì resumption vẫn 1 RTT như full; nó cắt **CPU** (bỏ ký cert/verify chuỗi), không cắt RTT. Chỉ
  TLS 1.2 resumption cắt 1 RTT. Đăng ký G6 (CPU) và G7 (RTT) riêng.
- 10:53-10:54 viết `phase8.md`: 7 câu hỏi, G1-G8, D1-D12. Chưa có file `.go`.
- (1) `internal/tlsx`: `gen.go` (CA + leaf ECDSA/RSA, `WriteFiles`), `store.go` (`certSet` bất biến sau dựng,
  `atomic.Pointer`; lookup đúng tên → wildcard một nhãn → mặc định; `VHostOf` cùng luật). Test xanh lần đầu;
  reload hỏng: "tls: private key does not match public key", serial giữ 100.
- (2) proxy: `tls.go` (VHost, `balancerFor` + 421, `handshake` có deadline, `dialUpstream` TLS), `defense8*.go`;
  pool `dial` trả (c, raw), `pooledConn.raw` cho probe; `connState.tls`. Suite cũ xanh ngay sau nối.
- (3) `phase8_test.go` 6 test: **xanh hết lần đầu**. G1: lệch 0/1000, A 500 B 500; SNI lạ / không SNI: "remote
  error: tls: internal error" (không phải unrecognized_name — xem phase8.md mục 2). G8 đơn vị: (a)
  `Dials:1 Reuses:19 DeadOnProbe:0`; (b) `DeadOnProbe:20 Retries:0`.
- (4) `-tags nodefense8`: DomainFronting `200 (X-Sim "B")`, HandshakeTimeout đóng sau 2.001 s. Đỏ đúng dòng.
- (5) `fixture.ListenAndServeTLS`, `cmd/tlslab` (4 mode), `cmd/gencert`, `cmd/upstream -name`, `config/tls.json`,
  Makefile `tlslab` (gencert → upstream A/B → edgegate → curl ×6 → SIGHUP → tlslab handshake), `tlslab-nodefense`,
  `tlslab-rtt`. `cmd/edgegate`: viết lại thân `main` (bản sửa từng mảnh đầu làm hỏng cấu trúc), `lbCfg`, nhiều
  server, SIGHUP reload, SIGTERM drain song song mọi listener.
- (6) `-race` toàn bộ: hai đỏ. `TestTLSHotReload` — test thiếu barrier (con dial sau reload đầu ghi đè "serial
  cũ" = 1003); thêm `dialed` WaitGroup. `TestLBKillRevive` — `map[200:573 502:1]`: khe D4 → dial refused → 502
  (phase8.md mục 3); sửa điều kiện D9 `!repicked && attempt == 0` → `!repicked`. 3/3 xanh dưới race.
- (7) `-race` toàn bộ lần hai: `TestIdleClosedUpstream/probe-*` đỏ (DeadOnProbe 41/50; POST 43/50). Chạy riêng
  5/5 xanh, worktree commit nền `46cd0f3` 5/5 xanh ⇒ timing của test (3 ms chờ FIN) dưới load 6.8; nới 20 ms.
  Lần ba: xanh hết.
