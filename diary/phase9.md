# Phase 9 — Performance: sync.Pool, splice, pprof, epoll vs netpoller

- **Thời lượng dự kiến:** 2-3 ngày · **thực tế:** ~80 phút làm việc (turn 1 14:08-14:29, turn 2 14:33-15:08, turn 3 15:34-15:40)
- **Bắt đầu:** 2026-10-01 14:08 · **Kết thúc:** 2026-10-01 15:40
- **Trạng thái:** ✅ xong — **3/8 giả thuyết sai** (G1 vế ns/op, G5 vế GC, G8 cả bốn vế); G2-G4, G6, G7 đúng — giả thuyết và quyết định bên dưới viết **trước** file `.go` đầu tiên của phase.
- **Commit:** `ee6751e` (turn 1 `c569eb4`, turn 2 `7762a46`; commit nền `4f89247`)

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
| D11 (P9-7, 2026-10-02) | **Splice body mặc định BẬT**: `Config.SpliceBody` → `Config.NoSplice` (`"no_splice_body"`, CLI `-nosplice`); perflab `edgegate` trần nay truyền `-nosplice`, `edgegate-splice` không truyền gì. Điều kiện trong nợ đã đủ: P9-1 trả; `chaoslab -body 262144` (mỗi request thứ 4 là `/large` 256 KiB qua splice) 60 s **PASS** (`bench/p9-7-chaoslab-body.txt`). Lượt đầu đỏ invariant fd: dư 16 fd = 8 pipe — Go giữ pipe của splice(2) trong `sync.Pool` (`internal/poll` splicePipePool), đóng bằng finalizer ⇒ chaoslab nay `runtime.GC()` trước khi đếm và in riêng số `pipe:`; sau GC fd về đúng nền 7/7 | Không phải rò; chaoslab đếm đúng thứ nó định đếm |

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
./scripts/rps-vs-nginx.sh   # G8 phụ: trần rps closed-loop ba proxy + direct
./scripts/cpu-vs-nginx.sh   # G8 phụ: CPU / request ba proxy (ít nhạy load nền)
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

### 2026-10-01 14:33-15:08 — Turn 2: đo, chấm G1-G8

Output thô: [`phase9-log.md` §2](phase9-log.md). Commit nền `c569eb4`. **Load 1.5 → 7.6** suốt turn (một tiến trình
nền `MainThr…` của session khác ăn ~85 % một core suốt 5 giờ, cộng indexer) — `uptime` đầu mỗi file bench. Mọi
server ghim core bằng `taskset`, nhưng core ghim **vẫn chia** với tiến trình nền: mọi số tuyệt đối dưới đây nhiễu
1.5-2.5x giữa lượt (thấy ở cột `direct`), kết luận chỉ lấy từ tỉ số **cùng lượt**.

**(1) G1 — `sync.Pool`** (`bench/p9-perflab.txt`, 3 lượt xen kẽ × 2, core 0-3):

```console
$ make perflab
== lượt 1 tags=nodefense9
BenchmarkProxyKeepAlive/body=1024-4   110908 ns/op   68129 B/op   47 allocs/op
BenchmarkProxyKeepAlive/body=1024-4    94153 ns/op   68129 B/op   47 allocs/op
== lượt 1 tags=mặc định
BenchmarkProxyKeepAlive/body=1024-4    48090 ns/op    1590 B/op   43 allocs/op
BenchmarkProxyKeepAlive/body=1024-4    41958 ns/op    1593 B/op   43 allocs/op
… lượt 2: 95002 / 164715 vs 50342 / 34362 · lượt 3: 88404 / 101930 vs 36058 / 38466
```

B/op **68 129 → 1 590 (43x)** — đúng hướng, mạnh hơn kỳ vọng ≥ 4x vì `copyBody` cấp 32 KiB cả cho body request
rỗng (2×32 KiB, không phải 1×). allocs/op 47 → 43 (Δ **4**, đăng ký 1-3). ns/op **88-165 µs → 34-50 µs ≈ 2.4x**
— đăng ký "≤ 15 %". **Sai.** Vì sao (`bench/p9-perflab-gogc.txt`):

```console
$ GOGC=$g go test ./internal/proxy -bench ProxyKeepAlive -tags nodefense9   # bản TRƯỚC
GOGC=100   88170 / 86345 ns/op   GC/20000 req: 419
GOGC=400   52987 / 54090 ns/op   GC/20000 req: 94
GOGC=1600  50376 / 46565 ns/op   GC/20000 req: 25
GOGC=off  220085 / 222345 ns/op  (heap phình không giới hạn — máy còn ~2.6 GB trống)
$ GODEBUG=gctrace=1 … bản SAU (mặc định): GC/20000 req: 32
```

Chi phí không nằm ở `malloc` mà ở **tần suất GC = tốc độ cấp phát / heap sống**. Proxy có heap sống rất nhỏ (vài
MiB) ⇒ 68 KiB mỗi request ⇒ một chu kỳ GC mỗi ~46 request. Nới GOGC 16x đưa bản trước về 48 µs — sát bản pool
(36-43 µs); phần chênh còn lại ≈ 15 % đúng là cỡ đăng ký cho "cấp phát thuần". GOGC=off **chậm hơn** (220 µs): bộ
nhớ mới mỗi request = page fault, không phải tái dùng.

**(2) G2 — bẫy pool giữ buffer to** (`bench/p9-g2.txt`): ×3 "không chặn **65536**, chặn (> 16384 bỏ) **512**". Đúng.

**(3) G3 — RSS mỗi connection rỗi, 10 000 conn** (`bench/p9-idlelab.txt`, xen kẽ ×2):

```console
$ make idlelab
edgegate-nodefense9  conns 10000 · VmRSS 8960 → 295040 KiB · Δ 28.61 KiB/conn
edgegate             conns 10000 · VmRSS 8704 →  88960 KiB · Δ  8.03 KiB/conn
edgegate-nodefense9  conns 10000 · VmRSS 8960 → 279552 KiB · Δ 27.06 KiB/conn
edgegate             conns 10000 · VmRSS 8960 → 102016 KiB · Δ  9.31 KiB/conn
```

Trước 27.1-28.6 (đăng ký 20-30), sau 8.0-9.3 (≤ 12), **3.0-3.6x** (≥ 2x). Đúng. 10 000 connection rỗi: 280 MiB →
90-100 MiB.

**(4) G4 — giá của L7, body 10 MiB × 100** (`bench/p9-l4l7lab.txt`, 3 lượt; proxy core 0):

```console
$ make l4l7lab                     # lượt 1 (lượt 2, 3 cùng hình dạng)
direct           100 × 10 MiB trong 354ms · 2826 MiB/s
l4splice         100 × 10 MiB trong 375ms · 2665 MiB/s · CPU proxy 0.30 s = 0.31 s/GiB · pipe fd tối đa 6
l4copy           100 × 10 MiB trong 882ms · 1133 MiB/s · CPU proxy 0.82 s = 0.84 s/GiB · pipe fd tối đa 0
edgegate         100 × 10 MiB trong 822ms · 1217 MiB/s · CPU proxy 0.76 s = 0.78 s/GiB · pipe fd tối đa 0
edgegate-splice  100 × 10 MiB trong 372ms · 2692 MiB/s · CPU proxy 0.30 s = 0.31 s/GiB · pipe fd tối đa 2
```

| Tỉ số (lượt 1 / 2 / 3) | Đăng ký | Đo |
|---|---|---|
| CPU/GiB l4splice / l4copy | ≤ 0.6x | 0.37 / 0.42 / 0.48 ✅ |
| throughput l4splice / l4copy | ≥ 1.2x | 2.35 / 2.11 / 1.84 ✅ |
| throughput l4splice / edgegate | ≥ 1.7x | 2.19 / 2.02 / 1.76 ✅ |
| CPU/GiB edgegate / l4splice | ≥ 2x | 2.5 / 2.3 / 2.0 ✅ (sát biên lượt 3) |
| throughput edgegate-splice / l4splice | trong 1.2x | 1.01 / 0.92 / 1.01 ✅ |

Đúng cả năm vế. Điều **không** đăng ký: edgegate L7 ≈ l4copy (1217-1482 vs 1133-1416 MiB/s, CPU/GiB 0.65-0.78 vs
0.67-0.84) — với body to, **toàn bộ** giá của L7 là mất splice; parse head và flush-mỗi-lần-đọc không thấy được.
Pipe fd khẳng định splice có chạy (6 = hai chiều × cache pipe; 2 = một chiều body response; 0 ở hai bản copy).
l4splice đạt 92-95 % `direct` ⇒ trần ở đây là client/upstream, tỉ số splice là **cận dưới**.

**(5) G5 — pprof 30 s @ 8 000 rps open-loop** (`bench/p9-pproflab.txt`, `bench/p9-pprof-*.{prof,txt}`,
`bench/p9-pprof-groups.txt`; proxy core 0-1):

| Nhóm (CPU / tổng) | Đăng ký | trước (`nodefense9`) 40.66 s | sau 27.79 s |
|---|---|---|---|
| `Syscall6` (flat) | ≥ 40 % | 13.70 s = **33.7 %** ❌ | 15.30 s = 55.1 % ✅ |
| `httpx` (trừ syscall/malloc/runtime) | ≤ 10 % | 1.56 s = 3.8 % ✅ | 1.34 s = 4.8 % ✅ |
| GC (mark/sweep/assist) + `mallocgc` | ≤ 15 %, pool cắt ≤ 5 điểm | 7.85 + 7.84 = **38.6 %** ❌ | 0.26 + 1.38 = 5.9 % — cắt **33 điểm** ❌ |
| `findRunnable` + `futex` | — | 2.57 + 2.11 | 3.62 + 1.74 |

CPU mỗi request **169 → 116 µs** (−32 %); p99 ở 8 000 rps **348 ms → 10.7 ms** — bản trước đang quá tải ở mức mà
bản sau còn chịu được. Cùng nguyên nhân G1: GC, không phải malloc. Parse + ghi lại head (thứ "L7" đắt về lý thuyết)
chỉ **4-5 %**; syscall là hơn nửa.

**(6) G6 — RSS mỗi conn rỗi, 10 000 conn, epoll vs netpoller** (`bench/p9-epolllab.txt`):

```console
epoll      conns 10000 · VmRSS 4608 →  6016 KiB · Δ 0.14 KiB/conn · luồng OS 10 → 10
netpoller  conns 10000 · VmRSS 4224 → 81408 KiB · Δ 7.72 KiB/conn · luồng OS 7 → 9
epoll      conns 10000 · VmRSS 4480 →  5888 KiB · Δ 0.14 KiB/conn
netpoller  conns 10000 · VmRSS 4608 → 81152 KiB · Δ 7.65 KiB/conn
```

epoll 0.14 (≤ 1), netpoller 7.65-7.72 (6-10), **55x** (≥ 6x). Đúng. 0.14 KiB = map entry + `conn{}` 48 B; 7.7 KiB =
buffer 4 KiB + stack goroutine. (0.00 ở 2 000 conn của turn 1 = 280 KiB nằm gọn trong trang heap đã thường trú.)

**(7) G7 — throughput epoll vs netpoller, 256 conn closed-loop** (*coordinated omission chưa loại trừ — chỉ đọc
rps*), 6 cặp xen kẽ:

```console
epoll 91092 / netpoller 71344 · epoll 69851 / netpoller 108140 · epoll 107054 / netpoller 106776
netpoller 108099 / epoll 83094 · netpoller 97889 / epoll 106552 · netpoller 106935 / epoll 103994
CPU server µs/req: epoll 19.8 22.7 18.4 19.2 18.7 18.5 (TB 19.6) · netpoller 21.4 17.1 16.7 17.6 19.2 17.8 (TB 18.3)
```

netpoller/epoll từng cặp 0.78 / 1.55 / 1.00 / 1.30 / 0.92 / 1.03 — trung vị **1.01**, lượt tốt nhất 108.1k vs
107.1k; CPU/req netpoller **0.93x** epoll. Đúng (4/6 cặp trong dải; 2 cặp ngoài dải lệch **cả hai chiều** = nhiễu,
không phải xu hướng). Vòng epoll tự viết **không** rẻ hơn per request: netpoller chính là epoll, và scheduler của Go
gần như không tốn gì ở đây. Cái epoll thắng là bộ nhớ (G6), không phải tốc độ.

**(8) G8 — EdgeGate / nginx / ReverseProxy** (`bench/p9-bench-vs-nginx.txt` + `-2.txt` — 3 lượt open-loop;
`bench/p9-rps-vs-nginx.txt`; `bench/p9-cpu-vs-nginx.txt`). Open-loop 5 000 rps, p99:

| lượt (load) | EdgeGate | nginx | ReverseProxy | direct |
|---|---|---|---|---|
| 1 (3.2) | 3.35 ms | 5.63 ms | 15.87 ms | 1.68 ms |
| 2 (6.5-7.6) | 7.98 ms | 4.18 ms | 115.57 ms | 3.57 ms |
| 3 (6.4) | 6.03 ms | 7.75 ms | 125.54 ms | 1.56 ms |

Rate cao nhất còn p99 < 10 ms: EdgeGate 20k / 5k / 10k; nginx 20k / 5k / 5k; ReverseProxy 2k / < 5k / < 5k (cả
ba proxy không đơn điệu theo rate ở lượt 1 — 15k trượt, 20k qua — vì load nền). Closed-loop 64 conn + CPU (lượt
load ~5):

```console
$ ./scripts/cpu-vs-nginx.sh
edgegate  29366 rps · 49.7 µs/req   |  28736 · 62.0  |  29835 · 53.2
nginx     33828 rps · 51.6 µs/req   |  32769 · 48.9  |  30591 · 55.7
rp         9563 rps · 186.0 µs/req  |   9331 · 198.9 |   9727 · 188.4
```

| Vế | Đăng ký | Đo | |
|---|---|---|---|
| rps nginx / EdgeGate | ≥ 1.5x | 1.15 / 1.14 / 1.03 (closed-loop); trần open-loop bằng hoặc thấp hơn | ❌ |
| EdgeGate / ReverseProxy | 0.8-1.25 | rps **3.07-3.08x**, CPU/req **3.2-3.7x** ít hơn | ❌ |
| p99 @ 5k nginx tốt hơn EdgeGate | ≥ 1.5x | 0.60x / 1.91x / 0.78x — không nhất quán | ❌ |
| p99 @ 5k EdgeGate vs ReverseProxy | 0.7-1.4 | ReverseProxy tệ hơn **4.7-21x** | ❌ |

**Sai cả bốn vế, theo cùng một hướng**: EdgeGate không đứng giữa, mà **ngang nginx** (CPU/req EdgeGate / nginx 0.96 / 1.27 / 0.96 —
trong cùng nhiễu), và **cách xa ReverseProxy**. Hai quan sát giải thích (chưa chứng minh):

- **ReverseProxy:** không nghẽn CPU khi chậm (profile 6 s: 110 % trên 2 core lúc 3.4k rps) mà nghẽn **chờ**: đếm
  context switch tự nguyện của tiến trình (`bench/p9-rp-ctxsw.txt`) — ReverseProxy **0.68-0.69 / request**,
  EdgeGate **0.05-0.14**. Transport của net/http chuyển mỗi request qua goroutine `readLoop`/`writeLoop` riêng của
  connection upstream; EdgeGate làm trọn một request trong một goroutine. Mỗi lần goroutine nhường mà P rỗi, luồng
  OS ngủ futex — và trên máy này đánh thức một vCPU rỗi tốn cỡ trăm µs (dưới).
- **nginx ≈ EdgeGate:** cả hai ~50 µs CPU/request — nhiều lần một proxy C trên máy thật. `Syscall6` = 55 % CPU của
  EdgeGate; nếu syscall trên WSL2 đắt cỡ đó cho cả nginx (không đo được: không có `perf`), thì hai proxy làm **cùng
  số syscall** mỗi request và phần runtime/ngôn ngữ chỉ còn là phần nhỏ. Không suy kết quả này ra máy thật.

**Phát hiện phụ — sàn latency của máy:** `direct` 1 connection tuần tự chỉ **1 295 rps = 770 µs/request** trên
loopback; open-loop `direct` p50 640 µs ở 2 000 rps nhưng 310-350 µs ở 10 000-30 000 rps — latency **giảm** khi tải
tăng: vCPU rỗi của WSL2 ngủ sâu, đánh thức tốn hàng trăm µs. Ở concurrency thấp, mọi p50 trong file này đo cái giá
đánh thức, không đo phần mềm.

**Bẫy trong lúc đo:** `pkill -f epolllab` khớp chính dòng lệnh của shell đang chạy nó ⇒ giết shell (exit 144) giữa
thí nghiệm, để lại server mồ côi giữ cổng 18100 (lần đo kế "bind: address already in use" rồi đo nhầm server cũ).
Từ đó dùng `pkill -x <tên>`.

## Giả thuyết sai

| # | Đoán | Đo | Vì sao sai |
|---|---|---|---|
| G1 | ns/op giảm ≤ 15 % (alloc không phải nút cổ chai) | **2.4x** | Nhầm "giá cấp phát" với "giá GC". Với heap sống vài MiB, 68 KiB/request = một GC mỗi ~46 request; nới GOGC 16x thì chênh còn ~15 % |
| G1 | Δallocs 1-3 | 4 | Không đếm `copyBody` cho body request rỗng |
| G5 | GC + malloc ≤ 15 %, pool cắt ≤ 5 điểm | 38.6 % → 5.9 % | Cùng gốc G1 |
| G5 | syscall ≥ 40 % (bản trước) | 33.7 % | GC chiếm chỗ; bản sau 55.1 % |
| G8 | nginx ≥ 1.5x EdgeGate; EdgeGate ≈ ReverseProxy | nginx 1.03-1.15x; EdgeGate 3.1x rps, 3.2-3.7x ít CPU hơn ReverseProxy | Đoán theo "C > Go, Go ≈ Go". Thực tế: (a) giá syscall WSL2 san phẳng nginx/EdgeGate; (b) ReverseProxy trả giá handoff goroutine × giá đánh thức vCPU — một chi phí của **kiến trúc** (Transport), không phải của ngôn ngữ |

## Số đo

| Thứ đo | Trước / A | Sau / B | Tỉ số |
|---|---|---|---|
| G1 B/op keep-alive 1 KiB | 68 129 | 1 590 | 43x |
| G1 ns/op keep-alive | 88-165 µs | 34-50 µs | ≈ 2.4x |
| G1 GC / 20 000 request | 419-433 | 32 | 13x |
| G3 RSS / conn rỗi (10k) | 27.1-28.6 KiB | 8.0-9.3 KiB | 3.0-3.6x |
| G4 throughput 10 MiB: l4splice / edgegate | 2611-2894 MiB/s | 1217-1482 MiB/s | 1.76-2.19x |
| G4 edgegate-splice / l4splice | 2636-2692 | 2611-2894 | 0.92-1.01 |
| G5 CPU / request @ 8k rps | 169 µs | 116 µs | 1.46x |
| G5 p99 @ 8k rps | 348 ms | 10.7 ms | 33x |
| G6 RSS / conn rỗi: netpoller / epoll | 7.65-7.72 KiB | 0.14 KiB | 55x |
| G7 rps netpoller / epoll | — | — | trung vị 1.01 |
| G8 CPU / request EdgeGate / nginx / ReverseProxy | 49.7-62.0 | 48.9-55.7 / 186-199 µs | 1 : 0.79-1.05 : 3.2-3.7 |

Code đổi trong turn: `cmd/rpbaseline -pprof` (để profile cột so sánh); thêm `scripts/rps-vs-nginx.sh`,
`scripts/cpu-vs-nginx.sh`. Không đổi data path.


**Chấm G1-G8:** G2, G3, G4, G6, G7 ✅; G1 ❌ vế ns/op (+ Δallocs lệch 1); G5 ❌ vế GC/syscall bản trước; G8 ❌ cả
bốn vế.

## Invariant + lệnh kiểm chứng

Chạy lại 2026-10-01 15:36 (load 3.15), output `bench/p9-invariants.txt`; số lab lấy từ turn 2.

| Invariant | Cài ở | Kiểm chứng | Kết quả |
|---|---|---|---|
| **Buffer về pool chỉ khi không còn byte của ai** — `br.Buffered() > 0` (pipelining) ⇒ giữ (I1) | `proxy.go:serveConn` nhánh `pipelined` / `releaseIdleBufio` | `TestIdlePipeliningAndPrefix` | 200, 201 trong một lần ghi; byte đầu tách 50 ms ⇒ 202 |
| **Connection rỗi không cầm `bufio`** | `proxy.go:serveConn` (Read 1 byte vào `connState.pre`, `bufpool.go:prefixReader`) | `TestIdleReleasesBufio`; `make perflab-nodefense` PHẢI đỏ; `make idlelab` | 0 bufio / 20 conn; nodefense **40**; 28 → 8-9 KiB/conn |
| **Trả pool đúng một lần** (hai người cầm chung một buffer = rò dữ liệu giữa request) | `pool.go:pooledConn.close` (nil sau khi trả); `serveConn` defer chỉ trả cái đang cầm | `go test ./... -race` (pool + race detector) | xanh |
| **Pool không phình theo request tệ nhất** | `httpx/headpool.go:putHead` (bỏ cap > 16 KiB) | `TestHeadPoolDropsOversize` (×3, `-race` ×5) | không chặn 65536, chặn 512 |
| **Splice giữ ranh giới CL (I1) và sạch mới về pool (I4)** | `splice.go:spliceBody` — phần trong `ubr` đi trước, `LimitedReader{N: còn lại}`; `forward.go:exchange` `clean = bodyDone && ubr.Buffered()==0` | `TestSpliceBody`, `TestSpliceBodyShortUpstream`; nodefense PHẢI đỏ | đúng từng byte 1 MiB + 100 000; `Dials:1 Reuses:3`; upstream đứt ⇒ client bị đóng, `DropDirty:1`; nodefense 0 splice |
| **Splice thật sự chạy** (không suy từ code) | Go `net/splice_linux.go:spliceFrom` | `make l4l7lab` cột pipe fd | 6 / 2 ở bản splice, 0 ở bản copy |
| **epoll tự viết đúng giao thức** (ET đọc tới EAGAIN, EAGAIN ghi ⇒ EPOLLOUT, head dở có trần) | `epollsrv/epoll_linux.go:readAll/handle/write/flush` | `go test ./internal/epollsrv -race` | head cắt giữa `\r\n\r`/`\n`, pipelining 3, response 4 MiB, 1000 conn × 5 — chia 333/319/348 cho 3 loop |
| **I3, I7, I8 giữ nguyên** | không đổi deadline/counter/Close | `go test ./... -race` (TestDeadline, TestNoGoroutineLeak, chaos test phase 7) | xanh |
| **Data path không `net/http`** | — | `grep -rln '"net/http"' internal cmd/edgegate cmd/perflab cmd/epolllab \| grep -v _test.go` | `internal/fixture` (upstream giả, được phép) + `cmd/edgegate/pprof.go` (listener admin D10, tắt mặc định) |

## Đọc gì

Đọc trực tiếp mã nguồn Go 1.26.2 (`$(go env GOROOT)/src`) ở turn 3:

- `net/tcpsock_posix.go:47-51` — `(*TCPConn).readFrom` thử `spliceFrom` trước, rồi `sendFile`.
- `net/splice_linux.go:19-45` — `spliceFrom` gỡ `*io.LimitedReader` lấy `N` làm `remain`, nhận nguồn
  `*TCPConn` / `tcpConnWithoutWriteTo` / unix stream; kiểu khác ⇒ `handled=false` (về copy thường). Đây là lý do
  D5 bọc đúng `&io.LimitedReader{R: upTCP}` và bản `l4copy` (giấu kiểu) ra 0 pipe.
- `internal/poll/splice_linux.go:21-25, 184-220` — `maxSpliceSize = 1 << 20`; pipe lấy từ `splicePipePool`
  (`sync.Pool`) ⇒ pipe fd còn sống sau copy (nguồn của số "6" ở l4splice).
- `sync/pool.go:103-106` — dưới race detector, `Put` "Randomly drop x on floor" khi `runtime_randn(4) == 0` (1/4).
  Nguồn của test đỏ ở turn 1.
- `runtime/mgcpacer.go:58-60` — `defaultHeapMinimum` = 4 MiB (trừ khi bật GOEXPERIMENT `HeapMinimum512KiB`;
  `go env GOEXPERIMENT` rỗng) ⇒ heap sống nhỏ hơn thì mốc GC kế ≈ 4 MiB: với 68 KiB/request là ~60 request/chu kỳ,
  khớp bậc với 419-433 GC / 20 000 request (46/chu kỳ) đo được.
- `runtime/netpoll_epoll.go` — netpoller trên Linux là một `epfd` epoll của runtime (câu 5).
- `net/http/transport.go:1994-1995` (`go pconn.readLoop()`, `go pconn.writeLoop()`) và `:2882-2887`
  (`pc.writech <- writeRequest{…}`, `pc.reqch <- requestAndChan{…}`) — mỗi request đi qua hai channel sang hai
  goroutine khác (câu 6).

Không đọc (và không trích): tài liệu/mã nginx (buffer client, `reuseport`, số syscall mỗi request) — nợ P9-6, P9-4.

## Rút ra

**1. CPU của EdgeGate đi vào syscall, không vào parse.** Profile 30 s ở 8 000 rps (bản sau): `Syscall6` **55 %**,
scheduler (`findRunnable` + `futex`) **≤ 19 %** (hai nhóm có thể chồng nhau), GC + malloc **6 %**, `httpx` (parse + ghi lại head) **4.8 %**. Thứ
làm proxy "L7" — đọc từng dòng header, canonical hoá, ghi lại — rẻ đến mức gần như không thấy. Nhưng câu trả lời
"đừng tối ưu GC" là **sai** cho bản trước: ở đó GC + malloc là **38.6 %**, lớn hơn cả syscall (33.7 %), và CPU mỗi
request 169 µs vs 116 µs. Không phải vì GC của Go chậm, mà vì một chỗ cấp phát 32 KiB mỗi lần gọi (hai lần mỗi
request — một cho body request **rỗng**, P9-3). Bài học: profile trước, và đọc profile theo **nhóm** (`-focus` /
`-ignore`) — `httpx` cum ban đầu ra 8.9 s vì gồm cả syscall đọc qua `bufio.fill`; trừ ra còn 1.3-1.6 s.

**2. `sync.Pool` cắt tần suất GC, không cắt giá cấp phát.** B/op **68 129 → 1 590 (43x)**, allocs/op chỉ 47 → 43,
ns/op **≈ 2.4x**. Đoán "≤ 15 %" sai vì nhầm hai chi phí: cấp phát 32 KiB (zero 32 KiB, vài µs) vs **GC** — heap sống
của proxy chỉ vài MiB, mốc GC tối thiểu 4 MiB (`mgcpacer.go`), nên 68 KiB/request kích một chu kỳ GC mỗi ~46
request (đo: 419-433 GC / 20 000 request vs 32 sau pool). Nới GOGC 16x đưa bản trước từ 87 µs về 48 µs — gần bản
pool; phần chênh còn lại ~15 % mới là "giá cấp phát" mà G1 đoán. GOGC=off còn **tệ hơn** (220 µs): không GC ⇒ mọi
cấp phát là trang mới ⇒ page fault. Pool không chạm syscall (thứ chiếm hơn nửa CPU). Bẫy "pool giữ buffer to"
có thật ở đúng một chỗ: buffer head lớn lên theo header (tới `MaxHeaderBytes` 64 KiB); không chặn ⇒ request nhỏ sau
đó nhận lại 64 KiB; `putHead` bỏ cap > 16 KiB. Bẫy thứ hai không ai báo trước: `sync.Pool` dưới `-race` cố ý vứt
1/4 số `Put` (`sync/pool.go:103`) — test dựa vào "Put rồi Get lại được" là test hên xui.

**3. Connection rỗi: 28 KiB → 8-9 KiB, vì buffer chứ không vì goroutine.** 10 000 connection keep-alive rỗi: bản
trước **27.1-28.6 KiB/conn** (280 MiB tổng), bản trả `bufio` lúc rỗi **8.0-9.3 KiB/conn** (90-100 MiB) — **3.0-3.6x**.
Cơ chế: hết response, `Buffered() == 0` ⇒ trả cả hai `bufio`; chờ byte đầu của request kế bằng một `Read` 1 byte
vào trường của `connState`; có byte ⇒ lấy `bufio` mới, đọc qua `prefixReader` trả byte đó trước. Pipelining là
ngoại lệ bắt buộc: byte request kế đã nằm trong `br` thì trả `br` là **mất request** (I1). 8 KiB còn lại chủ yếu là
stack goroutine — đó là giá của mô hình goroutine-per-connection, không thể trả về pool (câu 5). nginx làm gì ở cùng
chỗ: chưa đọc nguồn — không kết luận (P9-6).

**4. Giá của L7 với body lớn = giá mất splice, và lấy lại được.** Body 10 MiB × 100, proxy một core:
L4 `io.Copy` TCP→TCP (splice) **2611-2894 MiB/s, 0.29-0.32 s CPU/GiB**; L4 copy userspace **1133-1416 MiB/s,
0.67-0.84 s/GiB**; EdgeGate L7 **1217-1482 MiB/s, 0.65-0.78 s/GiB**. L4 nhanh hơn L7 **1.76-2.19x**, rẻ CPU hơn
**2.0-2.5x** — nhưng EdgeGate L7 **ngang L4 copy**: parse head, ghi lại head, flush mỗi lần đọc đều chìm trong 10 MiB
copy. Toàn bộ khoảng cách là byte đi lên userspace rồi xuống lại. Và nó **không** bắt buộc với L7: parse xong head,
ranh giới body là CL đã biết, phần còn lại đúng là việc của L4 — `SpliceBody` (D5) chép phần body đã nằm trong
`ubr` qua `bw`, rồi `ReadFrom(&io.LimitedReader{upTCP, còn lại})` ⇒ Go splice (`net/splice_linux.go`). EdgeGate +
splice **0.92-1.01x** L4 splice. Câu "L4 LB nhanh hơn L7 LB" vì vậy đúng ở **head** (L7 phải đọc từng byte head;
với GET nhỏ thì đó là cả request — câu 1 nói nó rẻ) và **không** đúng ở body CL. Không splice được: TLS (byte phải
giải/mã), chunked (phải đọc khung), body tới-EOF, body nhỏ (đã nằm cả trong `ubr`), và chiều upload — code splice viết sẵn 2026-10-03 (P9-2), số đo chờ Linux.

**5. epoll tự viết thắng bộ nhớ 55x, hoà tốc độ — vì netpoller chính là epoll.** Cùng giao thức tối giản, 10 000
connection rỗi: epoll (trạng thái conn ~48 B + map entry, buffer đọc 64 KiB dùng chung mỗi loop) **0.14 KiB/conn**;
netpoller (goroutine + buffer 4 KiB mỗi conn) **7.65-7.72 KiB/conn** — **55x**. Throughput 256 conn: netpoller/epoll
trung vị **1.01** (6 cặp), CPU/req netpoller **0.93x** epoll. Hai câu trả lời ngược nhau vì hai mô hình trả cùng một
số syscall mỗi request (`runtime/netpoll_epoll.go` là một epoll), chỉ khác **chỗ giữ trạng thái chờ**: goroutine
chờ trong `Read` thì phải có stack và buffer **trước** khi có byte; vòng epoll chỉ cấp buffer **khi** có byte, và một
buffer phục vụ mọi conn của loop. Chi phí của goroutine-per-conn là không gian, không phải thời gian. Muốn
"connection rỗi gần như miễn phí" ở Go thì phải đổi mô hình — không phải tối ưu.

**6. EdgeGate ngang nginx và cách xa ReverseProxy — trên máy này.** Cùng upstream, cùng ghim core (proxy 2 core):
CPU/request EdgeGate **49.7-62.0 µs**, nginx **48.9-55.7 µs**, `httputil.ReverseProxy` **186-199 µs**; rps closed-loop
nginx/EdgeGate **1.03-1.15x**, EdgeGate/ReverseProxy **3.07x**; p99 open-loop @ 5 000 rps ReverseProxy tệ hơn
**4.7-21x**, EdgeGate vs nginx không nhất quán (0.60 / 1.91 / 0.78x). Đoán "nginx ≥ 1.5x, EdgeGate ≈ ReverseProxy"
sai cả bốn vế, theo cùng một hướng — đoán theo ngôn ngữ ("C > Go, Go ≈ Go") thay vì theo **kiến trúc**:
ReverseProxy không nghẽn CPU khi chậm (110 % / 200 %) mà có **0.68 context switch / request** vs 0.05-0.14 — Transport
đẩy mỗi request qua hai channel sang `writeLoop`/`readLoop` (`transport.go:2882-2887`), mỗi lần nhường mà P rỗi thì
luồng OS ngủ, và đánh thức một vCPU rỗi trên WSL2 tốn hàng trăm µs (`direct` 1 conn: 770 µs/request). EdgeGate làm
trọn một request trong một goroutine. Khoảng cách tới nginx "đến từ đâu" — ở đây **không có** khoảng cách đo được;
giả thuyết (P9-4, chưa kiểm): syscall WSL2 đắt (55 % CPU EdgeGate) và hai proxy làm cùng số syscall mỗi request ⇒
runtime/ngôn ngữ chỉ còn phần nhỏ. **Không đưa "ngang nginx" vào CV** khi chưa chạy lại trên Linux thuần.

## Nợ kỹ thuật

Chi tiết + lệnh trả trong `docs/debts.md`.

- [ ] **P9-1** 🔧 Splice: lỗi ghi client tính là lỗi upstream (outlier oan).
- [~] **P9-2** (code + test xong 2026-10-03, số đo chờ Linux) ⏳ Body request (upload) không splice.
- [x] **P9-3** — trả turn 3: `copyBody` không gọi cho body request rỗng (`bench/p9-p93.txt`: bản trước 68 129 →
  35 361 B/op — khớp mức G1 đăng ký "≥ 33 KiB").
- [ ] **P9-4** 📏 G8 chạy lại trên Linux thuần + đếm syscall/request của nginx vs EdgeGate.
- [ ] **P9-5** 📏 ReverseProxy chậm: tương quan context switch — cần `go tool trace` để thành nhân quả.
- [ ] **P9-6** 📏 8 KiB/conn rỗi còn lại chưa chia; RSS/conn nginx chưa đo, tài liệu nginx chưa đọc.
- [x] **P9-7** ✅ (2026-10-02, D11) splice body mặc định bật — chaoslab body 256 KiB PASS.
