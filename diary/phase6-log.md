# Phase 6 — log thô

> Bản biên tập: [`phase6.md`](phase6.md). File này ghi theo thứ tự thời gian, kể cả ngõ cụt.

## §0 Mang từ phase 0-5 sang (đọc lại trước khi gõ)

1. **Không mang hằng số ngày khác sang** (phase 5 G1/G2 sai vì thế). Phase này service time là
   **giả lập** (fixture ngủ), nên tỉ số kỳ vọng suy từ mô hình, không từ số đo cũ.
2. Closed-loop: `cmd/lblab` là closed-loop. Backend chậm làm client gửi ít hơn ⇒ p99 dịu hơn thật.
   Ghi cạnh mọi số.
3. Phòng tuyến chỉ tin khi **đã thấy nó đỏ** lúc tắt — phase này là decay theo thời gian (`nodefenselb`).
4. Bug nằm ở đường không test nào chạy. Lần này: (a) mọi backend bị eject cùng lúc, (b) dial lỗi khi
   request **có body** (phase 5 nói "không retry có body" — nhưng dial lỗi thì chưa gửi gì),
   (c) node hồi phục sau eject rồi lỗi tiếp (backoff mũ), (d) consistent hash khi node đích unhealthy.
5. P5-3 phải trả **trước** khi cài least-conn, nếu không inflight đếm sai ngay từ đầu.
6. Vận hành: `go build` một dòng riêng rồi mới `&`; `pkill -x`; `rtk proxy go test` khi cần output thô.

## §1 Turn 1 — 2026-09-04 19:05 → 22:13

> Chỉ hai mốc 19:05 và 22:13 là đọc từ `date`. Các bước (1)-(7) ghi theo thứ tự làm, không có giờ —
> bản nháp đầu gán giờ ước lượng (19:20…20:00) rồi `date` cuối turn báo 22:13; đã bỏ số bịa.

- 19:05 đọc lại `pool.go`/`forward.go`/`proxy.go`, sổ nợ P5-2/P5-3/P5-5, ROADMAP phase 6.
- 19:10 viết `phase6.md`: 7 câu hỏi, G1-G7, D1-D12. Chưa có file `.go`.
- Trong lúc viết G2 tự lật một kỳ vọng: tưởng outlier ejection sẽ "cứu" least-conn khỏi node lỗi
  30 %; tính `0.3^5 = 0.24 %` mới thấy `consecutive_5xx` gần như không bao giờ kích với lỗi rải
  đều 30 %. Envoy có thêm `success_rate` outlier chính vì thế. Đăng ký G2 theo hướng "outlier
  KHÔNG bắt được", để turn 2 chấm.
- Kế hoạch file: xem Deliverable trong `phase6.md`.
- (1) trả nợ trong `pool.go`: `MaxIdleTime` 60 → 30 s (P5-5); `put` quét từ đáy bỏ mọi con quá
  tuổi (P5-2). **Sổ nợ P5-2 mô tả sai một nửa:** `get` có vòng `continue` nên khi ĐỈNH quá tuổi
  nó dọn hết cả stack; lỗ thật là đỉnh luôn tươi (một client keep-alive dùng đi dùng lại) trong
  khi 4 con dưới già mãi tới lần đầy. Test `TestPoolExpiredAtBottom` dựng đúng kịch bản đó: 5 idle
  rồi 30 request cách 10 ms trên một client ⇒ đòi `Idle 1, DropExpired 4`.
- (2) `internal/lb` xong: backend, ewma (decay theo thời gian, `score()` cũng decay), 4 picker,
  health (goroutine mỗi backend, fall/rise), outlier (consecutive, backoff mũ, trần 50 %), balancer.
  Tag `nodefenselb` lật `ewmaDecayByTime`.
- (3) test `lb` chạy lần đầu: **2/9 đỏ, cả hai là bug thật, không phải test sai.**

```console
$ go test ./internal/lb -count=1 -race -v
    lb_test.go:59: rr sau khi bỏ 1 node phải chia 100/100/100: map[10.0.0.1:80:75 10.0.0.2:80:75 10.0.0.4:80:150]
--- FAIL: TestRoundRobinEvenAndSkipsUnhealthy
    lb_test.go:148: 4→5 node, 100000 key: ring 150 vnode đổi chủ 17.5 % (lý thuyết 20), hash%N đổi 79.9 % (lý thuyết 80); tải 4 node [33830 26042 9470 30658] (max/min 3.57)
    lb_test.go:157: 150 vnode mà tải lệch max/min 3.57
--- FAIL: TestConsistentHashRehash
```

  (a) RR xoay trên danh sách gốc rồi "bỏ qua node unhealthy" ⇒ node đứng **sau** node chết nhận
  gấp đôi (150 vs 75). Sửa: xoay trên tập available. (b) Ring 150 vnode lệch **3.57x**. Nghi hash:
  chạy chương trình rời so 5 hàm hash × {1, 150, 1000} vnode:

```console
$ go run ./scratchpad/hashq
fnv64fold   vnode=150   load=[33830 26042 9470 30658] max/min=3.57
fnv64fold   vnode=1000  load=[11821 33725 23592 30862] max/min=2.85
fnv32       vnode=150   load=[25018 25844 27017 22121] max/min=1.22
fnv64+fmix  vnode=150   load=[23089 24575 25355 26981] max/min=1.17
fnv64+fmix  vnode=1000  load=[24484 24538 25190 25788] max/min=1.05
crc32       vnode=150   load=[26021 20068 23278 30633] max/min=1.53
```

  **Đọc kết quả:** FNV-1a không có avalanche — vnode `addr#0`, `addr#1`, … của cùng backend rơi
  gần nhau trên ring; 1000 vnode vẫn 2.85x. Vnode chỉ làm mịn khi hash rải đều. Thêm `fmix64`
  (bước trộn cuối của MurmurHash3) ⇒ 1.17x. Đây là một mảnh của G4 đã sai trước khi đo: "150 vnode
  ⇒ lệch ≤ ±10 %" chỉ đúng với hash tốt; ghi vào Giả thuyết sai turn 2.
- (4) nối `proxy`: `pools map[addr]*pool` (P5-3), `pooledConn.p` để release về đúng pool,
  `roundTrip` Pick → get → exchange → Done; dial lỗi ⇒ Done(failed) + Pick lại một lần (D9);
  không backend ⇒ 503 (D8); `exchange` trả thêm `upFail` (5xx hoặc lỗi transport; client bỏ đi
  giữa body KHÔNG tính). `forwardedHeaders` trả IP client sau ranh giới tin cậy làm khoá chash.
- (5) `TestNoGoroutineLeak` đỏ (4 → 7): goroutine health khởi động trong `Serve`, mà test chạy
  `go s.Serve(ln)` nên mốc `before` chụp trước khi nó sinh. Chuyển `lb.Start()` sang `New` (Close
  vẫn dừng). Xanh.
- (6) test proxy phase 6 + trả nợ: 7/7 xanh lần đầu. Chứng "fail trước" bằng cách tạm lật hai sửa
  trong `pool.go` (`bench/p6-debts-failfirst.txt`):

```console
--- FAIL: TestPoolExpiredAtBottom (2.43s)
    lb_test.go:261: đáy stack không được dọn: {Dials:5 Reuses:30 Puts:35 ... DropExpired:0 ... Idle:5} (kỳ vọng Idle 1, DropExpired 4)
--- FAIL: TestMaxIdleTimeShorterThanUpstream (0.00s)
    lb_test.go:273: MaxIdleTime mặc định 1m0s ≥ 60 s = nginx keepalive_timeout upstream ⇒ pool là bên thấy FIN
```

  Và số P5-5 đúng như đăng ký G7: `MaxIdleTime 50 ms ⇒ DropExpired 19, DeadOnProbe 0`;
  `60 s ⇒ DeadOnProbe 19, DropExpired 0`. Cùng upstream, cùng nhịp request — chỉ đổi ai đóng trước.
- (7) `cmd/lblab`, `config/lb.json`, khối `lb` trong `cmd/edgegate`, Makefile `lblab*`.
  Smoke n=2000 chạy được (không đọc số — đó là việc turn 2). `make lblab-nodefense` đỏ đúng dòng
  ("A không hồi phục: 0/200"). `go test ./... -race` xanh. Không package data-path nào import `net/http`.

## §2 Turn 2 — 2026-09-29 17:41 → 17:58

> Mốc đọc từ `date`/`uptime` trong đầu các file `bench/p6-*.txt`. Output đầy đủ nằm ở đó, không chép lại hết.

- 17:41 `go test ./... -race` xanh ở `d21bdb2`, load 0.60. Build `bin/lblab`.
- 17:43 `bench/p6-lblab-even.txt` (load 8.86 — dư âm của `-race`). 4 thuật toán ≈ nhau; P2C chia
  lệch 20.3-31.7 % trên 4 node giống nhau ⇒ nợ P6-2.
- 17:43 `bench/p6-lblab-skew.txt`. Chấm G1 ngay: share LC b0 4.3 % đúng, **p99 không đổi** (22.01 vs
  21.47). Mất vài phút mới thấy: p99 = 1 % chậm nhất; node chậm còn > 1 % tải là nó quyết định p99.
  P2C 0.6 % ⇒ 6.44 ms. G2 vế 2 lật: outlier bật b1 còn 0.8 %. Tính lại kỳ vọng số request tới chuỗi 5
  lỗi: 586 ⇒ 0.17 s ở 3 400 rps.
- 17:44 `bench/p6-lblab-flap.txt`: p99 nửa sau 21.14 / 21.47 / 21.46 — **bench mù**, cùng bệnh G1.
  Sửa `cmd/lblab`: chụp `LBStats()` trong `onFlap` (đọc sau `wg.Wait` ⇒ không race), in share nửa sau
  + p90 nửa sau + EWMA b2 lúc flap→cuối.
- 17:45 `bench/p6-lblab-flap2.txt` conns 32 và 4. p2c-slow ≈ LC, không thua. Nghi: điểm có thừa số
  `(inflight+1)` ⇒ node xấu đi bị phạt ngay qua inflight, EWMA cũ không quan trọng. Suy ra chiều ngược
  (node **tốt lên**) mới là chỗ EWMA cũ không có gì bù ⇒ thêm cờ `-recover`.
- 17:47-17:50 `bench/p6-lblab-flap-rep.txt`: 3 lần × conns 32/4, số ổn định (bảng ở `phase6.md`).
- 17:50-17:52 `bench/p6-lblab-recover.txt`: p2c-slow b2 **0.0 %** nửa sau (conns 4, 2/2 lần); LC 25.0 %.
  p99 không lộ vì 3 node còn lại dư capacity.
- 17:52 `bench/p6-lblab-skew-rep.txt`: lặp skew 2 lần nữa + `-n 100000` outlier bật (3 eject, b1 0.7 %).
- 17:53 `bench/p6-tests-g4-g7.txt`: G4/G6/G7 xanh, `make lblab-nodefense` đỏ đúng dòng. Đọc
  `TestLBDeadBackend`: nó không phải kịch bản G5 (chết từ đầu, 50 ms/fall 2). Viết `TestLBKillRevive`
  (kill giữa bài, pool đang giữ connection, revive cùng port). 3/3 xanh: 500-503 ms / 298-300 ms, 0 lỗi
  client, **111-120 dial lỗi** — "+1 dial lỗi" trong G5 sai.
- 17:55 `go vet ./... && go test ./... -count=1 -race` xanh. `grep '"net/http"'` trên `internal/lb`,
  `internal/proxy/*.go` (trừ test), `cmd/lblab`: rỗng.
- Makefile: `lblab-flap` thêm dòng `-conns 4`; target mới `lblab-recover`.

## §3 Turn 3 — 2026-10-01 09:38 → 09:46

- 09:38 người dùng gõ lại "làm tiếp phase 6 turn 2" — turn 2 đã commit `6a38ca4`; hỏi lại, chọn turn 3.
- 09:43 chạy lại các lệnh của bảng invariant (`bench/p6-invariants.txt`, load 3.93 lúc mới boot): tất cả
  xanh; `poollab-nodefense` và `lblab-nodefense` đỏ đúng dòng.
- Song song đọc Finagle `PeakEwma.scala` / `Balancers.scala` (nhánh `develop`) cho câu 4: tau mặc định
  10 s; mẫu cao hơn ⇒ `cost.reset()` (nhảy lên tức thì); `get()` decay khi đọc; phạt `Penalty` khi
  `cost == 0 ∧ pending != 0`. Cái cuối thành nợ P6-3.
- Đọc lại `forward.go:roundTrip` để viết câu 2: `Done` chạy **sau** `put` (defer trong `exchange`),
  ngược với D2 và comment `balancer.go:Done`. Sửa comment, ghi vào câu 2; chênh vài µs, không đo được.
- Đếm lại G: turn 2 ghi "3 sai + 1 sai một vế" lẫn lộn; chuẩn: G3 sai hẳn, G1/G2/G5 sai một vế ⇒ 4/7.
- `docs/debts.md`: P6-1..P6-5 vào Đang nợ; P5-2/P5-3/P5-5 sang Đã trả. ROADMAP/README hàng 6 ✅.
