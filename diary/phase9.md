# Phase 9 — Performance: sync.Pool, splice, pprof, epoll vs netpoller

- **Thời lượng dự kiến:** 2-3 ngày · **thực tế:** (điền khi xong)
- **Bắt đầu:** 2026-10-01 14:08 · **Kết thúc:** —
- **Trạng thái:** 🟡 turn 1 — giả thuyết và quyết định bên dưới viết **trước** file `.go` đầu tiên của phase.
- **Commit:** — (commit nền `4f89247`)

> **Đường đi thô, kể cả ngõ cụt:** [`phase9-log.md`](phase9-log.md). File này là bản biên tập.
>
> **Quy tắc ghi nhật ký:** mọi con số, mọi kết luận đều phải kèm **lệnh shell sinh ra nó**
> và **output thật**. Luật đầy đủ: [`../skills/diary/SKILL.md`](../skills/diary/SKILL.md).

## Ràng buộc kế thừa

- **Không `net/http` trên data path.** `httputil.ReverseProxy` chỉ sống trong `cmd/rpbaseline` (cột so sánh, như
  `cmd/upstream`); `net/http/pprof` chỉ trên listener admin riêng của `cmd/edgegate` (`-pprof`), tắt mặc định.
- **Không kéo dependency.** epoll viết bằng `syscall` của stdlib (`EpollCreate1`, `EpollCtl`, `EpollWait`,
  `SetsockoptInt` đều có trên linux/amd64) — không `golang.org/x/sys`.
- **Tối ưu không được phá bất biến.** Buffer trả về pool phải không còn ai giữ (I4 phase 5: byte request kế tiếp
  có thể đang nằm trong `bufio`); splice body phải giữ đúng ranh giới CL (I1) và deadline (I3).
- **Máy đo và máy bị đo chung 6 core** (luật 3 của skill diary): mọi bench throughput ghim core bằng `taskset`
  (proxy / upstream / loadgen tách nhau) và ghi rõ; latency chỉ lấy từ open-loop (`internal/loadgen`).
- **Số tuyệt đối trên WSL2 nhiễu** (phase 4: ns/op không đo được 5 %; phase 8: load 3-10 từ session khác) ⇒ chốt
  bằng tỉ số cùng lượt, chạy xen kẽ before/after, ≥ 3 lượt.
- Phòng tuyến/tối ưu mới có tag riêng `nodefense9` = bản "trước" (P4-4).

## Môi trường

Cùng máy phase 0-8 (`bench/env-GOTIT-00663.txt`).

```console
$ date; uptime
Thu Oct  1 14:08:26 +07 2026
 14:08:39 up  4:43,  1 user,  load average: 1.85, 1.55, 1.51
$ go version; nproc; free -m | head -2
go version go1.26.2 linux/amd64
6
               total        used        free      shared  buff/cache   available
Mem:           11962        9343         194          58        2691        2619
$ which nginx wrk perf strace dot taskset docker
/usr/bin/taskset
/usr/bin/docker            # nginx không cài; image nginx:1.25-alpine đã có sẵn trong docker (native WSL2, --network host được)
```

Không có `perf`, `strace`, `dot`, `wrk`: flamegraph đọc bằng `go tool pprof -top/-tree` (text), splice kiểm bằng
pipe fd trong `/proc/self/fd` (D5), không bằng strace.

## Mục tiêu phase

Biết **CPU và bộ nhớ của EdgeGate đi đâu**, và **cái giá của lựa chọn L7** (parse-and-reserialize mất splice).
Bốn thí nghiệm: `sync.Pool` (allocs trước/sau, và vì sao nó gần như không đổi ns/op), L4 vs L7 trên body 10 MB,
epoll tự viết vs netpoller (hai câu hỏi khác nhau: RSS mỗi conn rỗi và throughput), bảng ba cột
EdgeGate / nginx / `httputil.ReverseProxy`.

## Câu hỏi phải trả lời được (viết trước khi code)

1. EdgeGate tiêu CPU vào đâu cho một GET nhỏ keep-alive: syscall, parse, ghi lại head, malloc/GC, scheduler?
   Profile nói gì, và nó có khớp với "tối ưu GC" mà người ta hay làm đầu tiên không?
2. `sync.Pool` giảm được gì (B/op, allocs/op, ns/op) và **không** giảm được gì? Bẫy "pool giữ buffer to bất
   thường" xảy ra ở đâu trong code này, và chặn bằng cách nào?
3. Một connection keep-alive **rỗi** tốn bao nhiêu RSS ở EdgeGate, chia ra cho cái gì (stack goroutine, `bufio`,
   `net.Conn`)? Trả `bufio` về pool lúc rỗi cắt được bao nhiêu? nginx làm gì ở cùng chỗ đó?
4. **Giá của L7:** body 10 MB qua `io.Copy` TCP→TCP (splice) vs qua parser — tỉ số throughput và CPU/GB bao
   nhiêu? Bao nhiêu trong đó là do mất splice, bao nhiêu do flush-mỗi-lần-đọc? Proxy L7 có lấy lại splice cho body
   sau khi parse head được không, và khi nào thì không (TLS, chunked, body nhỏ)?
5. Netpoller của Go là epoll — vậy một vòng epoll tự viết (đa luồng, `SO_REUSEPORT`, một loop mỗi core) hơn kém
   goroutine-per-conn ở (a) RSS mỗi conn rỗi ở 10k conn, (b) throughput ở concurrency cao? Vì sao hai câu trả lời
   ngược nhau?
6. Cùng backend, cùng máy, cùng open-loop rate: EdgeGate đứng đâu giữa nginx và `httputil.ReverseProxy` ở
   p50/p99/p99.9 và rps trần? Khoảng cách tới nginx đến từ đâu?

## Giả thuyết đăng ký trước (viết 14:10-14:20, chưa có file `.go` nào của phase)

| # | Giả thuyết | Tỉ số / giá trị kỳ vọng | Lệnh sẽ dùng để chấm |
|---|---|---|---|
| G1 | **`sync.Pool` cho buffer** (`BenchmarkProxyKeepAlive`: GET, response 1 KiB CL, một conn keep-alive, in-process): trước (`nodefense9`) B/op **≥ 33 KiB** (copyBody cấp 32 KiB mỗi response); sau **≤ 8 KiB** ⇒ B/op giảm **≥ 4x**; allocs/op giảm chỉ **1-3**; ns/op giảm **≤ 15 %** — alloc không phải nút cổ chai, syscall mới là | B/op ≥ 4x; Δallocs 1-3; ns/op ≤ 15 % | `go test ./internal/proxy -run ^$ -bench ProxyKeepAlive -benchmem -count 6` × {mặc định, `-tags nodefense9`} |
| G2 | **Bẫy pool giữ buffer to:** head 60 KiB (dưới `MaxHeaderBytes`) đi qua một lần ⇒ buffer head tăng cap; không chặn ⇒ request nhỏ sau đó vẫn nhận lại buffer cap ≥ 60 KiB (pool phình theo request tệ nhất); chặn (`Put` bỏ cap > 16 KiB) ⇒ cap ≤ 16 KiB | không chặn: cap ≥ 60 KiB; chặn: ≤ 16 KiB | `go test ./internal/httpx -run TestHeadPoolDropsOversize -v` (pool head sống ở `httpx` — sửa tên lệnh 14:27, trước khi đo) |
| G3 | **RSS mỗi conn keep-alive rỗi** (10 000 conn vào `bin/edgegate`, mỗi conn 1 request rồi giữ; ΔVmRSS / 10 000): trước (`nodefense9`, `bufio` 2×8 KiB giữ suốt đời conn) **20-30 KiB**; sau (trả `bufio` về pool khi rỗi, chờ byte đầu bằng `Read` 1 byte) **≤ 12 KiB** ⇒ **≥ 2x**; phần còn lại chủ yếu là stack goroutine | trước 20-30 KiB; sau ≤ 12 KiB; ≥ 2x | `make idlelab` (= `perflab -mode idle` × hai build) |
| G4 | **Giá của L7 — body 10 MB, RTT 0** (proxy ghim core 0, client+upstream core 2-3; CPU của proxy = rusage tiến trình con): L4 splice vs L4 copy userspace: CPU/GB splice **≤ 0.6x**, throughput **≥ 1.2x**. EdgeGate L7 vs L4 splice: throughput **≤ 0.6x** (L4 ≥ 1.7x), CPU/GB **≥ 2x**. EdgeGate + `SpliceBody` (D5) **trong 1.2x** của L4 splice. Splice thật sự chạy ⇔ thấy pipe fd của proxy trong `/proc/<pid>/fd` lúc copy | ≥ 1.7x / ≥ 2x; splice-body ≤ 1.2x | `make l4l7lab` |
| G5 | **pprof EdgeGate, GET nhỏ keep-alive, open-loop** (60 s, rate ≈ 70 % trần): flat trong syscall (`internal/runtime/syscall.Syscall6` + `runtime/internal` tương đương) **≥ 40 %**; `httpx` parse + ghi head **≤ 10 %**; malloc + GC (`mallocgc`, `gcBgMarkWorker`, `scanobject`) trước pool **≤ 15 %**, pool cắt **≤ 5 điểm %** | syscall ≥ 40 %; httpx ≤ 10 %; GC ≤ 15 % | `go tool pprof -top -sample_index=cpu bench/p9-*.prof` |
| G6 | **epoll vs netpoller — (a) RSS mỗi conn rỗi, 10 000 conn** (cùng giao thức tối giản, server tiến trình riêng): netpoller (goroutine + buffer đọc 4 KiB mỗi conn) **6-10 KiB/conn**; epoll (trạng thái conn vài trăm byte, buffer dùng chung mỗi loop) **≤ 1 KiB/conn** ⇒ epoll **≥ 6x** nhỏ hơn | netpoller 6-10 KiB; epoll ≤ 1 KiB; ≥ 6x | `make epolllab` |
| G7 | **epoll vs netpoller — (b) throughput** (server ghim core 0-2: epoll 3 loop `SO_REUSEPORT` / netpoller `GOMAXPROCS=3`; client core 3-5, 256 conn keep-alive closed-loop — *coordinated omission chưa loại trừ, chỉ đọc rps*): netpoller/epoll **∈ [0.8, 1.25]** — netpoller **là** epoll, chênh chỉ là scheduler | 0.8-1.25 | `make epolllab` |
| G8 | **Bảng ba cột** (cùng `cmd/upstream`, proxy ghim core 0-1, upstream 2-3, loadgen 4-5; open-loop, 64 worker keep-alive, GET 1 KiB): rps trần (p99 < 10 ms, 0 lỗi) nginx **≥ 1.5x** EdgeGate; EdgeGate / ReverseProxy **∈ [0.8, 1.25]**; ở 50 % trần của EdgeGate, p99 nginx tốt hơn EdgeGate **≥ 1.5x**, EdgeGate vs ReverseProxy p99 **∈ [0.7, 1.4]** | ≥ 1.5x; 0.8-1.25; ≥ 1.5x; 0.7-1.4 | `make bench-vs-nginx` |

**Quyết định thiết kế đăng ký trước** (đổi sau phải ghi lý do):

| # | Quyết định | Lý do / hệ quả |
|---|---|---|
| D1 | `internal/proxy/bufpool.go`: ba pool — `copyBuf` (32 KiB, `*[]byte`), `bufio.Reader` 8 KiB, `bufio.Writer` 8 KiB — và một pool head (`[]byte` cap khởi đầu 512) trong `httpx` cho `WriteHead`. `Put` **bỏ** buffer cap > `maxPooledHead` (16 KiB) (G2). Pool `*[]byte` chứ không `[]byte` (tránh alloc khi đổi slice sang interface) | Bốn chỗ cấp phát mỗi request/conn nhìn thấy trong code: `forward.go:copyBody` 32 KiB, `proxy.go:serveConn` 2×8 KiB, `pool.go:dialNew` 2×8 KiB, `write.go:WriteHead` 256 B×2 |
| D2 | **Trả `bufio` khi rỗi** (`serveConn`): hết một request, nếu `br.Buffered() == 0` ⇒ `bw` (đã Flush) và `br` về pool; chờ request kế bằng `c.Read(st.first[:1])` dưới `IdleTimeout`; có byte ⇒ lấy `br` mới, `Reset` trên `prefixReader{first, c}` (trả byte đã đọc trước rồi đọc tiếp `c`, không alloc). `Buffered() > 0` (pipelining) ⇒ **giữ** `br` | Byte request kế tiếp nằm trong `br` thì trả pool = mất byte (I1). Cặp Dekker drain phase 7 giữ nguyên: `idle=true` → đọc `closeIdle` → `Read` |
| D3 | Pool upstream: `bufio` của `pooledConn` lấy từ pool khi dial, trả khi `close`; **không** trả lúc rỗi trong pool (≤ `MaxIdle` = 64 conn ⇒ ≤ 1 MiB, không đáng phức tạp) | Bộ nhớ rỗi phía upstream bị chặn bởi `MaxIdle`, phía client thì không |
| D4 | `nodefense9` = bản **trước**: `get` luôn `new`, `Put` là no-op, `serveConn` giữ `bufio` suốt đời, `SpliceBody` bị bỏ qua. Một build tag cho cả G1, G3, G4 | So trước/sau trên **cùng commit**, xen kẽ lượt chạy |
| D5 | **`Config.SpliceBody`** (mặc định tắt, bật trong lab): response có CL ≥ 64 KiB, client và upstream đều `*net.TCPConn` (không TLS): Flush head, chép phần body đã nằm trong `ubr`, rồi `clientTCP.ReadFrom(&io.LimitedReader{upTCP, còn lại})` ⇒ Go dùng `splice(2)`. Ranh giới vẫn là CL (I1); sạch ⇔ chép đủ CL và `ubr.Buffered() == 0`; deadline đặt trên conn như cũ (I3). Kiểm splice có chạy: đếm pipe fd của tiến trình (`/proc/self/fd` → `pipe:`) trong lúc copy | Câu hỏi 4: L7 mất splice ở **head**, không bắt buộc mất ở **body**. Chunked/TLS/body nhỏ không splice được — ghi rõ. Request body (upload) không làm ⇒ nợ |
| D6 | `cmd/perflab` (một binary, bốn mode, mọi server là **tiến trình con** để RSS/CPU đo riêng qua `/proc/<pid>/status` (VmRSS) + `/proc/<pid>/stat` (utime+stime, tick 100 Hz — không `wait4`: cần đo giữa chừng, trước khi con thoát)): `idle` (G3, G6a: spawn server, mở N conn, 1 request mỗi conn, chờ ổn định, ΔVmRSS/N), `l4l7` (G4: spawn proxy theo `-impl direct|l4splice|l4copy|edgegate|edgegate-splice`, upstream = `epolllab -impl netpoller -body 10 MiB` tiến trình riêng, client tuần tự K lần, MiB/s + CPU proxy/GiB + pipe fd tối đa — bỏ fd 0-2), `rps` (G7: closed-loop N conn, có nhãn "closed-loop"), `open` (G8: `internal/loadgen`) | Đo RSS/CPU của server lẫn với client là phép đo sai (luật 3) |
| D7 | `internal/epollsrv` (linux): `Serve(addr, loops)` — mỗi loop: `LockOSThread`, socket riêng `SO_REUSEPORT`, `EpollCreate1`, accept non-blocking + edge-triggered `EPOLLIN|EPOLLRDHUP`; trạng thái conn `{fd, in []byte (nil khi rỗi), out []byte}` trong `map[int32]*conn`; buffer đọc 64 KiB **dùng chung mỗi loop**; giao thức tối giản: đọc tới `\r\n\r\n`, trả response cố định 1 KiB, keep-alive, nhận pipelining và head cắt ngang nhiều lần đọc. `EAGAIN` khi ghi ⇒ giữ `out`, đăng ký `EPOLLOUT` | Điều kiện công bằng 1 của ROADMAP (đa luồng). Cùng giao thức với bản netpoller (`cmd/epolllab -impl netpoller`: goroutine + `make([]byte, 4096)` mỗi conn — cách viết Go thông thường) |
| D8 | `cmd/rpbaseline`: `httputil.ReverseProxy` với `Transport{MaxIdleConnsPerHost: 64}` (mặc định 2 sẽ dial liên tục — so như vậy là so pool vs không pool). nginx: `docker run --network host nginx:1.25-alpine` với `config/nginx-bench.conf` (`worker_processes 2`, `upstream { keepalive 64; }`, `proxy_http_version 1.1`, `proxy_set_header Connection ""`, `access_log off`), port 18090 — **không** đụng container `local-gateway` cổng 80 đang chạy | So ReverseProxy không pool hay nginx không keepalive upstream là so cấu hình, không so proxy |
| D9 | Ghim core cho G8: `taskset -c 0-1` proxy (`GOMAXPROCS=2`, nginx 2 worker, `--cpuset-cpus 0-1`), `-c 2-3` upstream, `-c 4-5` loadgen. EdgeGate cấu hình tối giản (`config/bench.json`: 1 upstream, RR, health/outlier/rate limit/shed tắt) — so cái proxy làm, không so tính năng. **Upstream = `epolllab -impl epoll -loops 2`** (response 1 KiB cố định), không phải `cmd/upstream` (sửa 14:24, trước khi đo: `cmd/upstream` log mỗi request + net/http ⇒ upstream thành nút cổ chai của cả ba cột). Thêm cột **direct** (loadgen → upstream, không proxy) = trần của chính loadgen. Ba proxy chạy cùng lúc, mỗi rate bắn xoay vòng thứ tự | nginx bench cũng không có health check chủ động/rate limit |
| D10 | `cmd/edgegate -pprof 127.0.0.1:6061` (listener admin riêng, `net/http/pprof`); profile 30 s lấy bằng `go tool pprof -proto` lưu `bench/p9-*.prof` (vài chục KB, commit được) | Data path vẫn không `net/http`; profile phải có thể mở lại |

## Deliverable

- `internal/proxy/bufpool.go`, `defense9*.go`; `serveConn` trả `bufio` lúc rỗi; `SpliceBody`; `bench_test.go`
  (`BenchmarkProxyKeepAlive`, `BenchmarkProxyLarge`), `phase9_test.go` (pool cap, idle release giữ byte pipelining,
  splice body đúng ranh giới + pipe fd).
- `internal/httpx`: pool head trong `WriteHead`.
- `internal/epollsrv` + test (head cắt ngang, pipelining, 1000 conn).
- `cmd/perflab`, `cmd/epolllab`, `cmd/rpbaseline`; `config/bench.json`, `config/nginx-bench.conf`;
  `scripts/bench-vs-nginx.sh`; Makefile `perflab`, `idlelab`, `l4l7lab`, `epolllab`, `bench-vs-nginx`, `pproflab`.
- `cmd/edgegate -pprof`.

## Reproduce toàn bộ phase

```bash
git checkout <commit phase 9>
go test ./... -count=1 -race
make perflab        # G1: bench trước/sau, xen kẽ
make idlelab        # G3
make l4l7lab        # G4
make pproflab       # G5: profile trước/sau
make epolllab       # G6, G7
make bench-vs-nginx # G8 (cần docker, image nginx:1.25-alpine)
```

## Nhật ký

### 2026-10-01 14:08-14:29 — Turn 1: pool buffer, trả bufio lúc rỗi, splice body, epoll tự viết, bộ đo; chưa đo

Chi tiết: [`phase9-log.md` §1](phase9-log.md). Số trong lần chạy thử (`-n 10`, 2 000 conn, 2 s) **không** phải số
đo — turn 2 đo với tham số của Reproduce.

1. **Bốn chỗ cấp phát đi qua pool** (D1-D3): `copyBody` 32 KiB, `bufio` client 2×8 KiB, `bufio` upstream 2×8 KiB
   (trả khi `pooledConn.close`, gọi lần hai no-op), buffer head của `WriteHead` (bỏ cap > 16 KiB). Suite cũ xanh
   ngay cả hai build.
2. **Connection rỗi không cầm `bufio`** (D2): hết response, `Buffered() == 0` ⇒ trả cả hai; chờ request kế bằng
   `Read` 1 byte vào `connState.pre`; có byte ⇒ `bufio` mới đọc qua `prefixReader`. Pipelining (`Buffered() > 0`)
   giữ nguyên `br`. `TestIdlePipeliningAndPrefix` xanh (200, 201 trong một lần ghi; byte đầu tách 50 ms ⇒ 202).
3. **Splice body** (D5) chạy thật: `TestSpliceBody` 1 MiB + 100 000 byte đúng từng byte, 1 132 435 byte qua splice
   (phần còn lại đã nằm trong `ubr` cùng head — đi qua `bw` trước), upstream về pool (`Dials:1 Reuses:3`), body 1000
   byte không splice. Upstream đóng giữa body ⇒ client nhận 300 000 byte rồi bị đóng, `DropDirty:1`.
4. **Phản chứng `nodefense9` đỏ đúng hai test** (`bench/p9-turn1-nodefense.txt`): 20 connection rỗi cầm **40**
   `bufio` (muốn 0); splice 0 response.
5. **Bẫy test: `sync.Pool` dưới `-race` cố ý bỏ ngẫu nhiên một phần `Put`** — `TestHeadPoolDropsOversize` đọc
   một lần `Get` thì đỏ dưới `-race` ("không chặn 512"), xanh ở build thường. Sửa: 50 lượt, lấy cap lớn nhất.
   Tương tự: `TestIdleReleasesBufio` lần đầu **xanh cả ở `nodefense9`** vì chờ 2 s = `IdleTimeout` của test —
   connection đóng vì idle cũng trả `bufio`. Sửa: `IdleTimeout` 10 s, chờ ≤ 500 ms. Một phản chứng xanh là một
   phản chứng hỏng.
6. **`internal/epollsrv`**: epoll ET + `SO_REUSEPORT` + `LockOSThread`, buffer đọc 64 KiB dùng chung mỗi loop,
   `EAGAIN` ⇒ `EPOLLOUT`; bản netpoller cùng giao thức. Test cả hai: head cắt giữa `\r\n\r` | `\n`, 3 request
   pipelining, response 4 MiB (đụng `EAGAIN`), 1000 conn × 5 lượt — kernel chia 336 / 320 / 344 cho 3 loop.
7. **Tín hiệu (chưa phải số đo):** `BenchmarkProxyKeepAlive` một lượt: `nodefense9` 68 130 B/op, 91 µs/op; sau
   1 605 B/op, 36 µs/op — B/op đúng hướng G1 nhưng ns/op lệch xa "≤ 15 %" (và 68 KiB chứ không 33: `copyBody`
   được gọi **cả cho body request rỗng** ⇒ 2×32 KiB). L4/L7 `-n 10`: l4splice 2999 MiB/s, l4copy 1672,
   edgegate 1389, edgegate-splice 2432; pipe fd 8 / 2 / 2 / 4 — hai số "2" ở bản copy là stdio của tiến trình con
   (perflab chạy sau `|`), đã bỏ fd 0-2. Idle 2 000 conn: edgegate 8.13 vs `nodefense9` 28.54 KiB/conn; epoll
   **0.00** KiB/conn (RSS không đổi một trang — nghi heap Go đã có trang thường trú sẵn; 10 000 conn sẽ rõ),
   netpoller 7.94.

`go test ./... -count=1 -race` xanh (`bench/p9-turn1-race.txt`). `make bench-vs-nginx` chạy thử `RATES=2000
DUR=2s`: bốn cột 100 % 200, nginx 1.25.5 trong docker `--network host`, container tự gỡ khi xong.

