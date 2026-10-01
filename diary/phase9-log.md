# Phase 9 — log thô

> Bản biên tập: [`phase9.md`](phase9.md). File này ghi theo thứ tự thời gian, kể cả ngõ cụt.

## §0 Mang từ phase 0-8 sang (đọc lại trước khi gõ)

1. Số tuyệt đối trên WSL2 nhiễu; chốt tỉ số cùng lượt, xen kẽ trước/sau (phase 4, 8).
2. RSS/conn phase 7: Slowloris 20.7 KiB/conn — trong đó `bufio` 2×8 KiB cấp ngay khi accept (ROADMAP phase 0 mục 4
   đã báo trước: "lý do phase 9 cần `sync.Pool`").
3. Máy đo và máy bị đo chung CPU ⇒ taskset (luật 3).
4. Không ghi giờ ước lượng; chỉ mốc từ `date`.

## §1 Turn 1 — 2026-10-01 14:08 → 14:29

- 14:08 đọc ROADMAP phase 9, Makefile (stub `perflab`/`epolllab`/`bench-vs-nginx` gọi `cmd/epolllab` và
  `scripts/bench-vs-nginx.sh` — **chưa tồn tại**). Công cụ: không `nginx`, `wrk`, `perf`, `strace`, `dot`; có
  `taskset`, `docker` (native WSL2, image `nginx:1.25-alpine` có sẵn; container `local-gateway` đang giữ cổng 80 —
  không đụng, bench dùng 18090).
- Đọc data path tìm chỗ cấp phát: `forward.go:copyBody` `make([]byte, 32<<10)` mỗi lần gọi (gọi cho cả body
  request lẫn response), `proxy.go:serveConn` 2 `bufio` 8 KiB lúc accept, `pool.go:dialNew` 2 `bufio`,
  `write.go:WriteHead` `make(0, 256)` ×2 mỗi request. `httpx/parse.go`: chuỗi header là bản copy (không
  tham chiếu vào `bufio`) ⇒ trả `bufio` sau request an toàn.
- 14:10-14:20 viết `phase9.md`: 6 câu hỏi, G1-G8, D1-D10. Chưa có file `.go`.
- (1) `bufpool.go` + `defense9*.go` (proxy, httpx) + `headpool.go`; nối vào `copyBody`, `pool.go`, `serveConn`
  (D2: `prefixReader` trong `connState`). Suite xanh ở cả hai build ngay lần đầu.
- (2) `splice.go` (D5) + `Config.SpliceBody` + `SpliceStats`. `phase9_test.go`: 4 test xanh. `-tags nodefense9`:
  `TestSpliceBody` đỏ (0 response) — đúng; `TestIdleReleasesBufio` **xanh** — sai: `waitLive` chờ 2 s = IdleTimeout
  của `startProxyS` ⇒ connection đóng vì idle, `defer` trả `bufio` ⇒ đếm về 0. Sửa: IdleTimeout 10 s, chờ 500 ms ⇒
  `nodefense9` "cầm 40" — đỏ đúng.
- (3) `headpool_test.go` xanh ×3 (không chặn 65536, chặn 512).
- (4) `bench_test.go`: upstream/client tối giản (response dựng sẵn, đọc đúng số byte) để B/op là của proxy. Một
  lượt `-benchtime 2s`: `nodefense9` 68130 B/op 47 allocs 91 µs; sau 1605 B/op 43 allocs 36 µs; Large splice=true
  2888 MB/s vs false 1733 (nodefense9: hai cái như nhau ≈ 1590 — splice bị tắt). Không phải số đo.
- (5) `cmd/edgegate`: `-pprof`, `-splice`, `splice_body`. `internal/epollsrv` (epoll + netpoller) + test xanh
  dưới `-race`; đọc `len(l.conns)` từ goroutine test là data race tiềm ẩn (race detector không bắt lần đó) ⇒ đổi
  sang `nconn atomic.Int64`.
- (6) `cmd/epolllab`, `cmd/rpbaseline`, `cmd/perflab`, `config/bench.json`, `config/nginx-bench.conf`.
  Chạy thử l4l7 `-n 10`: pipe fd bản copy = 2 ⇒ stdio của con là pipe vì perflab chạy sau `| grep` ⇒ bỏ fd 0-2.
  Idle 2000 conn: edgegate 8.13 / nodefense9 28.54 / epoll 0.00 (!) / netpoller 7.94 KiB/conn.
- 14:24 đổi upstream của G8 từ `cmd/upstream` sang `epolllab -impl epoll` (log mỗi request + net/http ⇒ nút cổ
  chai) và thêm cột `direct`; ghi vào D9 trước khi đo.
- (7) `scripts/bench-vs-nginx.sh`, `scripts/pproflab.sh`; Makefile `perf-bins perflab perflab-nodefense idlelab
  l4l7lab pproflab epolllab bench-vs-nginx`. Chạy thử: bench-vs-nginx `RATES=2000 DUR=2s` (direct p50 660 µs ở
  2 000 rps — loadgen trên WSL2 tự tốn ≈ 0.6 ms; turn 2 phải đọc mọi cột **trừ** cột direct), pproflab 3000 rps
  ra hai profile (xoá — không phải số đo).
- (8) `-race` cả suite: `TestHeadPoolDropsOversize` đỏ ("không chặn 512") — `sync.Pool` dưới race detector cố ý
  bỏ ngẫu nhiên `Put`. Sửa: 50 lượt, lấy max. Xanh ×5 dưới `-race`. Suite xanh (`bench/p9-turn1-race.txt`).
- Nợ thấy trong lúc làm (ghi `docs/debts.md` ở turn 3): (a) splice: `ReadFrom` không tách lỗi đọc upstream với
  lỗi ghi client ⇒ client bỏ đi giữa body splice bị tính là lỗi upstream (outlier); (b) body request (upload) không
  splice; (c) `copyBody` lấy buffer 32 KiB cho cả body request rỗng (GET).

## §2 Turn 2 — 2026-10-01 14:33 → 15:08

- 14:33 load 1.5. `make perflab` (`bench/p9-perflab.txt`): B/op 68129 → 1590, ns/op ≈ 2.4x nhanh hơn — G1 vế
  ns/op sai xa. Nghi GC: `GOGC=off` bản trước **chậm hơn** (220 µs — heap phình, máy còn 2.6 GB, page fault);
  gctrace 433 vs 32 GC / 20 000 request; GOGC 100/400/1600 ⇒ 87 / 53 / 48 µs (`bench/p9-perflab-gogc.txt`).
- 14:37 `make idlelab` (10 000 conn ×2 xen kẽ): 28.6 / 27.1 vs 8.0 / 9.3 KiB/conn.
- 14:39 `make l4l7lab` ×3: đúng cả năm vế; edgegate ≈ l4copy (không đăng ký).
- 14:39-14:45 `bench-vs-nginx.sh` lượt 1 (load 3.2): EdgeGate và nginx đều không đơn điệu (15k trượt, 20k qua);
  ReverseProxy sập từ 5-10k (p50 422 ms @ 10k). Viết `scripts/rps-vs-nginx.sh` (closed-loop): lượt 1 edgegate
  25683 / nginx 25475 / rp 2262 / direct 38288; lượt 2-3 edgegate, nginx **và direct** tụt ~2.4x cùng lúc (load
  4.5) — nhiễu máy, không phải proxy.
- Đào ReverseProxy: curl header bình thường; ss: 64 ESTAB + 252 TIME-WAIT tới upstream; profile 6 s: 110 % CPU trên
  2 core ⇒ không nghẽn CPU. Ngõ cụt 1: `go build … && taskset … &` — `&` áp cho cả chuỗi ⇒ chạy binary cũ ("flag
  provided but not defined: -pprof"). Ngõ cụt 2: `pkill -f "bin/epolllab"` khớp dòng lệnh của chính shell ⇒ shell
  chết (exit 144) ×2, server mồ côi giữ 18100 ⇒ lần kế "address already in use" và đo nhầm server cũ. Từ đây
  `pkill -x`.
- 1 connection tuần tự: direct 1295 rps (770 µs/req!), edgegate 374, rp 352 — ở concurrency 1 ba cái gần nhau,
  máy chậm như nhau ⇒ sàn là đánh thức vCPU WSL2.
- Upstream net/http (`cmd/upstream`): direct 11926, edgegate 9257, rp 4700 — upstream thành nút cổ chai, rp vẫn
  ≈ 0.5x. `MaxIdleConnsPerHost` 64 vs 256: 6798 vs 5822 — không phải pool.
- Đếm `voluntary_ctxt_switches` mọi luồng (`bench/p9-rp-ctxsw.txt`): rp 0.68-0.69 / request, edgegate 0.05-0.14.
- 14:51-15:00 `bench-vs-nginx.sh` lượt 2-3 (load 6.4-7.6). `top`: pid 26494 `MainThr…` 85 % suốt 303 phút CPU —
  của session khác, không đụng.
- 15:01 `pproflab.sh 8000`: hai profile; nhóm bằng `-focus`/`-ignore` (`bench/p9-pprof-groups.txt`). `httpx` cum
  ban đầu ra 8.9 s vì gồm `bufio.fill` → syscall đọc; trừ syscall/malloc/runtime còn 1.56 / 1.34 s.
- 15:03 `make epolllab`: 0.14 vs 7.7 KiB/conn; rps 3 cặp nhiễu (0.78 / 1.55 / 1.00) ⇒ thêm 3 cặp: 1.30 / 0.92 / 1.03.
- 15:06 `scripts/cpu-vs-nginx.sh` (CPU nginx = tổng pid `nginx` của container thấy từ host): ~50 µs/req cho cả
  EdgeGate và nginx, rp ~190.
- G2 ×3 (`bench/p9-g2.txt`). 15:08 viết diary.

## §3 Turn 3 — 2026-10-01 15:34 → 15:40

- Đọc nguồn Go 1.26.2 trước khi viết Rút ra: `net/tcpsock_posix.go:47-51`, `net/splice_linux.go:19-45`,
  `internal/poll/splice_linux.go` (pipe pool), `sync/pool.go:103-106` (race bỏ 1/4 Put), `runtime/mgcpacer.go:58-60`
  (heap minimum 4 MiB; `go env GOEXPERIMENT` rỗng ⇒ không phải 512 KiB), `net/http/transport.go:1994-1995,
  2882-2887`. Không đọc gì về nginx ⇒ không viết gì về nginx ngoài số đo.
- Trả P9-3: `exchange` chỉ gọi `copyBody` khi request có body. `bench/p9-p93.txt`: bản trước 68 129 → 35 361 B/op;
  ns/op lượt đó nhiễu (bản sau 56 / 116 µs — load) ⇒ không dùng số ns/op của lượt này.
- 15:36 invariant (`bench/p9-invariants.txt`, load 3.15): `-race` xanh; nodefense9 đỏ đúng hai test; grep
  `net/http` chỉ còn fixture + `cmd/edgegate/pprof.go`.
- Nợ P9-1, 2, 4-7 vào `docs/debts.md`; P9-3 sang Đã trả. ROADMAP hàng 9 + 4 hàng bảng con số; README hàng 9 ✅.
