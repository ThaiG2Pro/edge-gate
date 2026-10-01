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

## §2 Turn 2 — 2026-10-01 11:08 → 11:36

- 11:08 `sudo -n true` ⇒ cần mật khẩu ⇒ phần netem phải nhờ người dùng. `go doc crypto/tls.Config.CurvePreferences`:
  "From Go 1.24, the default includes the X25519MLKEM768 hybrid post-quantum key exchange" — nghi phạm của tín hiệu
  turn 1 (1.3 chậm hơn 1.2). Thêm `-kex` + cột curve vào tlslab.
- 11:09 G1-G5/G8 ×3 + nodefense (`bench/p8-tests.txt`): xanh / đỏ đúng dòng.
- 11:10-11:11 handshake 4 lượt wall-time (`-handshake.txt`): load 7.5-10.2, cùng biến thể lệch 40 % ⇒ `ps`: codegraph,
  pytest + next-server của project khác. Thêm cột CPU/hs (`getrusage`).
- 11:12 4 lượt CPU (`-cpu.txt`): vẫn trôi, và **resumed đắt hơn full** ở vài dòng — vì lượt resumed GET mỗi lần để
  lấy ticket mới. Bỏ GET (ticket cũ dùng lại được: 1000/1000 resumed). 11:13-11:15 3 lượt × 2 kex n = 1000 (`-cpu2.txt`).
  Chỉ chấm tỉ số cùng lượt.
- 11:15 microbench từng phép (`taskset -c 5`): X25519 222-230 µs, ML-KEM-768 240-260 µs, ECDSA s+v 221-241 µs, RSA s+v
  1.33-1.63 ms ⇒ mật mã ≈ 20-30 % CPU handshake. Chuyển vào `internal/tlsx/kex_bench_test.go` để reproduce được.
- 11:16 `make tlslab` (curl) — đúng hết; G8 RTT 0 ×3; mode rtt ở RTT 0 (vô nghĩa khi chia RTT: CPU áp đảo — chỉ là mốc).
- 11:17 hỏi người dùng về netem (ảnh hưởng mọi traffic loopback của máy, cả session khác). Người dùng tự bật
  `delay 10ms`. 11:21-11:22 ping 20.1-22.1 ms; rtt ×2, upstream ×2 (`-rtt20.txt`). Báo tháo ngay; người dùng tháo,
  11:35 kiểm lại: `noqueue`, ping 0.037 ms.
- 11:35 G4 dưới tải: 96 reload / 64 conn / 10 s, 98 151 request, 0 lỗi.
- G7 đọc lần đầu: "plaintext 3 RTT, sai" — rồi thấy mọi ô +1 đúng bằng nhau ⇒ chặng proxy → upstream qua `lo` cũng
  bị delay; hiệu số khớp đăng ký từng ô.
