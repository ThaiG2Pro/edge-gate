# Phase 10 — (tùy chọn) HTTP/2 h2c: frame, HPACK, multiplexing, flow control

- **Thời lượng dự kiến:** 3-4 ngày · **thực tế:** ~2 giờ 20 phút làm việc (turn 1 2026-10-01 17:46-18:16, turn 2 2026-10-02 11:48-13:40, turn 3 13:43-13:55)
- **Bắt đầu:** 2026-10-01 17:46 · **Kết thúc:** 2026-10-02 13:55
- **Trạng thái:** ✅ xong — **2 giả thuyết sai/nửa sai** (G5 vế p99, G8 cả hai vế); G1-G4, G6, G7 đúng — giả thuyết và quyết định bên dưới viết **trước** file `.go` đầu tiên của phase; D6 a′ thêm 18:08, trước khi đo.
- **Commit:** `99a634f` (turn 1 `d4141b8`, turn 2 `bb02e5f`; commit nền `9a45ac3`)

> **Đường đi thô, kể cả ngõ cụt:** [`phase10-log.md`](phase10-log.md). File này là bản biên tập.
>
> **Quy tắc ghi nhật ký:** mọi con số, mọi kết luận đều phải kèm **lệnh shell sinh ra nó**
> và **output thật**. Luật đầy đủ: [`../skills/diary/SKILL.md`](../skills/diary/SKILL.md).

## Ràng buộc kế thừa

- **Không `net/http` trên data path, không kéo dependency.** Frame layer, HPACK (kể cả Huffman) tự viết trong
  `internal/h2`. `golang.org/x/net/http2` **không** được import. Bảng mã Huffman (RFC 7541 Appendix B — 257 mã,
  là *dữ liệu* của chuẩn, không phải thiết kế) chép từ `$GOROOT/src/vendor/golang.org/x/net/http2/hpack/tables.go`
  (BSD, ghi nguồn trong file) và **kiểm** bằng vector RFC 7541 Appendix C.4/C.6 — chép sai một bit là test đỏ.
- `net/http` chỉ được dùng trong `_test.go` và `cmd/*lab` làm **peer độc lập** (client `Protocols.SetUnencryptedHTTP2`,
  Go 1.24+) — giống `cmd/upstream`, `cmd/rpbaseline`. Cùng tác giả viết cả client lẫn server thì hai bên sai giống nhau;
  peer độc lập là bằng chứng interop.
- Phòng tuyến mới có tag riêng `nodefense10` = bản "không phòng tuyến" (P4-4); bài phản chứng phải **đỏ** khi tắt.
- Đo RTT/loss cần `tc netem` (sudo — người dùng tự chạy, và **phải** `sudo tc qdisc del dev lo root` sau). Máy chung
  6 core với session khác (phase 9: load nền 3-8) ⇒ chốt bằng tỉ số cùng lượt, xen kẽ, ≥ 3 lượt.

## Môi trường

Cùng máy phase 0-9 (`bench/env-GOTIT-00663.txt`).

```console
$ date; uptime
Thu Oct  1 17:47:44 +07 2026
 17:47:44 up  8:22,  1 user,  load average: 7.91, 4.29, 2.63
$ uname -srmo; go version; nproc; ulimit -n
Linux 6.6.87.2-microsoft-standard-WSL2 x86_64 GNU/Linux
go version go1.26.2 linux/amd64
6
1048576
$ which h2spec h2load nghttp curl; curl --version | head -1
/usr/bin/curl
curl 8.5.0 (x86_64-pc-linux-gnu) libcurl/8.5.0 OpenSSL/3.0.13 zlib/1.3 brotli/1.1.0 zstd/1.5.5 libidn2/2.3.7 libpsl/0.21.2 (+libidn2/2.3.7) libssh/0.10.6/openssl/zlib nghttp2/1.59.0 librtmp/2.3 OpenLDAP/2.6.10
```

Load nền 7.91 lúc bắt đầu (session khác). Không có `h2spec`/`h2load`/`nghttp`: `curl --http2-prior-knowledge`
(libnghttp2 1.59) là peer độc lập thứ hai; `h2spec` cài ở turn 2 (D10).

## Mục tiêu phase

Viết HTTP/2 cleartext (h2c, prior knowledge) phía client của EdgeGate — frame, HPACK, stream, flow control,
SETTINGS — rồi forward từng stream xuống upstream HTTP/1.1 qua pool cũ. Đo **vì sao** HTTP/2 hết HOL ở tầng HTTP mà
**vẫn còn** HOL ở tầng TCP, cái giá của flow control trên RTT thật, và cái giá mới mà multiplexing mở ra (một
connection = trăm công việc: Rapid Reset, CONTINUATION flood, downgrade smuggling h2→h1).

## Câu hỏi phải trả lời được (viết trước khi code)

1. Vì sao HTTP/2 nén header bằng HPACK (bảng tĩnh + động + Huffman) chứ không bằng deflate như SPDY? Request lặp lại
   tiết kiệm bao nhiêu byte? Cái giá: trạng thái nén **chung** giữa hai đầu ⇒ một lỗi decode giết **cả** connection.
2. Multiplexing giải head-of-line blocking ở tầng HTTP thế nào (một stream chậm không chặn stream khác trên cùng
   connection)? Vì sao HOL **vẫn còn** ở tầng TCP — đo được không, và nó tệ hơn HTTP/1.1 nhiều connection ở chỗ nào?
3. TCP đã có flow control, vì sao HTTP/2 cần thêm một tầng (stream + connection)? Window mặc định 65 535 byte đặt
   trần throughput **mỗi stream** là bao nhiêu trên RTT 40 ms?
4. Proxy h2→h1 (downgrade): ranh giới request ở h2 là framing (`END_STREAM`), ở h1 là CL/TE. Chỗ nào hai bên có thể
   bất đồng (H2.CL, H2.TE — họ hàng của smuggling phase 4), và phòng tuyến nào giữ I1?
5. Một connection h2 cho client mở bao nhiêu việc trên server? `MAX_CONCURRENT_STREAMS` có thật sự là trần khi
   client RST ngay sau HEADERS (Rapid Reset, CVE-2023-44487)? CONTINUATION không bao giờ có `END_HEADERS` thì sao?
6. Cùng proxy, cùng upstream h1: CPU/request và context switch/request của h2 so với h1 — framing + HPACK + giao
   việc giữa goroutine đọc và goroutine stream tốn bao nhiêu (phase 9: hand-off goroutine là thứ làm
   `httputil.ReverseProxy` chậm 3x)?
7. `h2spec` pass bao nhiêu phần trăm, và nhóm fail nói gì về chỗ mình hiểu sai spec?

## Giả thuyết đăng ký trước (viết 17:48-18:00, chưa có file `.go` nào của phase)

| # | Giả thuyết | Tỉ số / giá trị kỳ vọng | Lệnh sẽ dùng để chấm |
|---|---|---|---|
| G1 | **HPACK** (client Go `net/http` h2c, 100 GET cùng header trên MỘT connection, đếm byte payload HEADERS server nhận): request #1 / trung vị #2-#100 **≥ 5x**; head h1 tương đương / HEADERS h2 #2+ **≥ 10x** | ≥ 5x; ≥ 10x | `go run ./cmd/h2lab -mode hpack` |
| G2 | **h2spec** (`h2spec -p <port> -h 127.0.0.1`, mọi nhóm: generic, http2, hpack): lần chạy **đầu tiên** (trước khi sửa gì theo h2spec) pass **≥ 85 %**; sau khi sửa **≥ 95 %** | ≥ 85 % → ≥ 95 % | `make h2spec` |
| G3 | **HOL tầng HTTP** (RTT 0; upstream `/slow` trả sau 500 ms, `/fast` ngay): trên MỘT connection gửi `/slow` rồi `/fast` sau 10 ms. h1 (keep-alive, tuần tự) latency `/fast` **≥ 490 ms**; h2 latency `/fast` **≤ 1.5x** `/fast` một mình (vài ms) ⇒ h1/h2 **≥ 50x** | ≥ 50x | `go run ./cmd/h2lab -mode hol` |
| G4 | **Trần flow control** (một stream, response 8 MiB, client h2 của h2lab tự đặt `SETTINGS_INITIAL_WINDOW_SIZE` + connection window, WINDOW_UPDATE ngay khi nhận): netem `delay 20ms` (RTT 40 ms): window 65 535 ⇒ throughput ≈ window/RTT ≈ **1.6 MB/s** (∈ [0.8, 2.4]); window 8 MiB ⇒ **≥ 8x** cao hơn. RTT 0: hai window chênh **≤ 2x** | ≈ 1.6 MB/s; ≥ 8x; RTT 0 ≤ 2x | `go run ./cmd/h2lab -mode flow` × {RTT 0, netem 20 ms} |
| G5 | **HOL tầng TCP** (netem `delay 10ms loss 2%`; 32 GET song song, response 64 KiB, lặp 20 vòng): h2 1 conn × 32 stream vs h1 32 conn: p50 latency mỗi request h2/h1 **≥ 1.5x**, p99 **≥ 1.5x**. `loss 0%` (cùng delay): p50 h2/h1 **∈ [0.67, 1.5]** — chênh là do mất gói, không do h2 | loss 2 %: ≥ 1.5x; loss 0: 0.67-1.5 | `go run ./cmd/h2lab -mode tcphol` × {loss 0, loss 2 %} |
| G6 | **Downgrade smuggling (I1)**: stream h2 `content-length: 0` + DATA chứa `GET /smuggled HTTP/1.1…`; và stream mang `transfer-encoding: chunked`. Phòng tuyến: RST_STREAM PROTOCOL_ERROR, upstream thấy **0** request `/smuggled`. `nodefense10`: upstream thấy **≥ 1** | 0 vs ≥ 1 | `go test ./internal/proxy -run TestH2Smuggle -v` × {mặc định, `-tags nodefense10`} |
| G7 | **Rapid Reset + CONTINUATION flood** (một connection): 5 000 cặp HEADERS+RST_STREAM, upstream giữ mỗi request 200 ms: phòng tuyến (slot stream chỉ trả khi goroutine stream **thoát**) ⇒ request upstream đồng thời tối đa **≤ 100** (= `MAX_CONCURRENT_STREAMS`); `nodefense10` (trả slot lúc RST) ⇒ **≥ 1 000** (≥ 10x). CONTINUATION 1 KiB lặp không `END_HEADERS`: phòng tuyến ⇒ connection đóng sau **≤ 64 KiB + 1 frame** đã đệm; `nodefense10` ⇒ đệm **≥ 4 MiB** | ≤ 100 vs ≥ 1 000; ≤ 80 KiB vs ≥ 4 MiB | `go test ./internal/h2 ./internal/proxy -run 'RapidReset|Continuation' -v` × hai build |
| G8 | **Giá CPU của h2** (proxy tiến trình con ghim core 0-1, upstream epoll core 2-3, client core 4-5; GET 1 KiB; closed-loop — *coordinated omission chưa loại trừ, chỉ đọc CPU/req*): h2 (8 conn × 8 stream) vs h1 (64 conn): CPU proxy/request h2/h1 **≥ 1.3x**; voluntary context switch/request h2/h1 **≥ 2x** (goroutine đọc ⇄ goroutine stream) | ≥ 1.3x; ≥ 2x | `go run ./cmd/h2lab -mode cpu` |

**Quyết định thiết kế đăng ký trước** (đổi sau phải ghi lý do):

| # | Quyết định | Lý do / hệ quả |
|---|---|---|
| D1 | **`internal/h2/hpack`** (package riêng): số nguyên prefix N bit, chuỗi literal (thường + Huffman), bảng tĩnh 61 mục, bảng động (kích thước = Σ(len name + len value + 32), đuổi từ cũ nhất), `Decoder` (trần kích thước bảng theo SETTINGS của ta; dynamic table size update vượt trần ⇒ lỗi; trần **tổng header list** kiểm **trong lúc** decode, không sau — I2), `Encoder` (indexed khi khớp cả cặp, literal-with-incremental-indexing khi khớp tên/không khớp, never-indexed cho `authorization`/`cookie` ngắn; Huffman khi ngắn hơn). Mọi lỗi decode = `COMPRESSION_ERROR` = lỗi **connection** | Câu 1. Trạng thái nén chung ⇒ không thể "bỏ qua một header hỏng" — bên kia đã cập nhật bảng |
| D2 | **`internal/h2` frame layer**: header 9 byte; đọc payload **chỉ sau** khi kiểm `Length ≤ SETTINGS_MAX_FRAME_SIZE` của ta (16 384) — lớn hơn ⇒ `FRAME_SIZE_ERROR` trước khi cấp phát (I2); padding (`PADDED`) kiểm `pad < len`; loại frame lạ bị bỏ qua (RFC 9113 §4.1, §5.5). Writer ghi qua `bufio.Writer` + Flush có chủ đích | I2 ở h2 là trần frame + trần header list, không phải trần dòng |
| D3 | **Mô hình goroutine**: mỗi connection **một** goroutine đọc (sở hữu Framer đọc, HPACK decoder, bảng stream, mọi chuyển trạng thái); mỗi stream **một** goroutine handler (sinh khi head xong); ghi frame bằng `wmu` (mutex — HPACK encoder phải mã hoá đúng thứ tự frame lên dây ⇒ encode + ghi HEADERS trong cùng khoá). Chờ flow control bằng `sync.Cond` trên `mu` riêng, **không** giữ `wmu` khi chờ. Không có goroutine ghi riêng: client không đọc ⇒ ghi chặn ⇒ goroutine đọc chặn khi phải ACK ⇒ TCP backpressure tự nhiên, không có hàng đợi ACK không trần (flood PING/SETTINGS, CVE-2019-9512/9515) | Câu 6: đây chính là hand-off phase 9 đổ lỗi cho ReverseProxy — giờ đo cái của mình |
| D4 | **Trạng thái stream** (RFC 9113 §5.1, rút gọn cho server, không push): idle → open → half-closed (remote) khi `END_STREAM` → closed. Stream id client lẻ, tăng ngặt; id chẵn / không tăng ⇒ `PROTOCOL_ERROR` connection. DATA/HEADERS trên stream đã đóng ⇒ `STREAM_CLOSED`. Frame trên stream 0 sai loại / SETTINGS trên stream ≠ 0 ⇒ `PROTOCOL_ERROR`. Header block phải liền: sau HEADERS không `END_HEADERS` chỉ được CONTINUATION cùng stream | Thứ h2spec sẽ chấm |
| D5 | **Flow control** (RFC 9113 §6.9): *nhận*: window stream + connection của ta (mặc định 65 535, cấu hình được); DATA vượt window ⇒ `FLOW_CONTROL_ERROR`; body request đệm trong stream **tối đa bằng window** (I2: bộ nhớ body mỗi stream có trần); gửi WINDOW_UPDATE khi handler **đọc** (không khi nhận) ⇒ upstream chậm đọc = client bị chặn đúng stream đó. *Gửi*: window theo SETTINGS của peer; đổi `INITIAL_WINDOW_SIZE` giữa chừng cộng chênh lệch vào mọi stream đang mở (có thể âm); WINDOW_UPDATE làm window > 2³¹−1 ⇒ `FLOW_CONTROL_ERROR`; increment 0 ⇒ `PROTOCOL_ERROR` | Câu 3 |
| D6 | **Phòng tuyến** (tắt bằng `nodefense10`): (a) `MAX_CONCURRENT_STREAMS` 100, slot chỉ trả khi goroutine stream **thoát** (không lúc RST) — vượt ⇒ RST `REFUSED_STREAM`; (a′, **thêm 18:08** khi viết proxy, trước khi đo) > 2×`MAX_CONCURRENT_STREAMS` RST_STREAM của client trong 1 s ⇒ GOAWAY `ENHANCE_YOUR_CALM` — proxy đóng upstream khi RST (`Stream.SetCancel`) nên slot về nhanh, (a) một mình không chặn tốc độ việc bơm sang upstream; (b) header block (HEADERS + CONTINUATION) cộng dồn có trần `Limits.MaxHeaderBytes` (64 KiB) — vượt ⇒ GOAWAY `ENHANCE_YOUR_CALM` (CVE-2024-27316); (c) h2→h1: header field chứa CR/LF/NUL, tên in hoa, header connection-specific (`connection`, `keep-alive`, `proxy-connection`, `transfer-encoding`, `upgrade`), `te` khác `trailers`, pseudo-header thiếu/lặp/lạ/đứng sau header thường, `content-length` ≠ tổng DATA ⇒ RST `PROTOCOL_ERROR` (RFC 9113 §8.1.1, §8.2) | Câu 4, 5. Ba bài phản chứng (G6, G7) |
| D7 | **Chỉ h2c prior knowledge**: `Config.H2C bool`; trên listener plaintext, byte đầu tới ⇒ `Peek(24)` so với preface `PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n`; khớp ⇒ connection sang h2 (đọc tiếp qua **cùng** `br` — byte đã nằm trong buffer). Không `Upgrade: h2c` (RFC 9113 bỏ cơ chế này — kiểm ở turn 3), không ALPN `h2` trên TLS (nợ) | Một listener, hai giao thức; không đoán theo port |
| D8 | **Forward stream → upstream h1** (`internal/proxy/h2.go`): pseudo-header → `httpx.Request` (`:method`, `:path` → Target, `:authority` → Host nếu không có `host`), cookie nhiều field nối bằng `; ` (RFC 9113 §8.2.3); body: có `content-length` ⇒ CL, không có và stream chưa `END_STREAM` ⇒ chunked sang upstream; XFF/vhost/LB/pool như h1 (`forwardedHeaders`, `balancerFor`, `Pick`, `poolFor`); retry đúng một lần khi conn reused chết với 0 byte response và không body (D4 phase 5). Response: hop-by-hop strip, tên thường hoá lowercase, `:status`, DATA theo flow control. Rate limit/shed (phase 7) **chưa** áp cho h2 (nợ) | Tái dùng pool/LB; h2 chỉ là phía client |
| D9 | **Peer kiểm tra**: (1) test thô bằng Framer + Encoder của chính `internal/h2` cho các ca biên (mình viết cả hai đầu — chỉ để tạo byte xấu); (2) `net/http` client h2c (`_test.go`) — interop thật; (3) `curl --http2-prior-knowledge` (nghttp2) trong lab; (4) `h2spec` (turn 2). HPACK kiểm bằng **toàn bộ** vector RFC 7541 Appendix C (C.2-C.6) | Peer độc lập > test tự viết |
| D10 | **`cmd/h2lab`**: mode `hpack` (G1), `hol` (G3), `flow` (G4 — client h2 tối giản của `internal/h2` để tự đặt window), `tcphol` (G5), `cpu` (G8, proxy là tiến trình con như phase 9 D6). `config/h2.json` cho `cmd/edgegate -h2c`. `h2spec`: `go install github.com/summerwind/h2spec/cmd/h2spec@latest` vào `~/go/bin` ở turn 2 (công cụ, không phải dependency của module) | Cùng khuôn đo phase 9 |

## Deliverable

- `internal/h2/hpack` + test vector RFC 7541 Appendix C + fuzz decoder không panic.
- `internal/h2`: frame, server connection (stream, flow control, SETTINGS, PING, GOAWAY, RST), `defense10*.go`, test thô
  (preface, frame quá cỡ, id chẵn, window overflow, CONTINUATION flood, rapid reset).
- `internal/proxy/h2.go`: h2c trên listener plaintext, forward xuống pool h1; `phase10_test.go` (interop `net/http`,
  multiplex, body upload, smuggling h2→h1, rapid reset).
- `cmd/h2lab`, `cmd/edgegate -h2c`, `config/h2.json`; Makefile `h2lab`, `h2spec`, `h2-nodefense`.

## Reproduce toàn bộ phase

```bash
git checkout <commit phase 10>
go test ./... -count=1 -race
go test ./internal/h2/... ./internal/proxy -count=1 -tags nodefense10 -run 'Smuggle|RapidReset|Continuation'  # phải ĐỎ
GOBIN=$HOME/go/bin go install github.com/summerwind/h2spec/cmd/h2spec@latest   # G2 — công cụ, không vào go.mod
make h2lab          # G1, G3, G8
make h2spec         # G2
make h2lab-flow     # G4 vế RTT 0
make h2lab-tcphol   # G5 phụ: RTT 0
./bin/h2lab -mode tcphol -n 20 -par 32 -ref                                  # G5 phụ: đối chứng server net/http
taskset -c 4-5 ./bin/h2lab -mode cpu -rounds 3 -h2conns 1 -h2streams 1 -h1conns 1   # G8 phụ: tải thấp
sudo tc qdisc add dev lo root netem delay 20ms && make h2lab-flow;  sudo tc qdisc del dev lo root   # G4
sudo tc qdisc add dev lo root netem delay 10ms && make h2lab-tcphol; sudo tc qdisc del dev lo root   # G5 đối chứng
sudo tc qdisc add dev lo root netem delay 10ms loss 2% && make h2lab-tcphol; sudo tc qdisc del dev lo root  # G5
```

## Nhật ký

### 2026-10-01 17:46-18:16 — Turn 1: HPACK, frame, stream/flow control, h2c trong proxy, phản chứng; chưa đo

Chi tiết: [`phase10-log.md` §1](phase10-log.md). Số smoke trong log **không** phải số đo — turn 2 đo với tham số của
Reproduce.

1. **HPACK đúng theo RFC, kiểm bằng byte của RFC**: bảng tĩnh diff với Appendix A (61/61); 16 vector Appendix C trích
   máy từ `rfc7541.txt` — decode 16/16 + "Table size" khớp; encoder sinh **đúng từng byte** C.4 và C.6 (sau khi sửa
   luật "Huffman khi bằng độ dài" — C.6.2 `"307"`).

   ```console
   $ go test ./internal/h2/hpack -count=1
   ok  	github.com/thaivro/edgegate/internal/h2/hpack	0.006s
   $ go test ./internal/h2/hpack -run ^$ -fuzz FuzzDecode -fuzztime 30s -fuzzminimizetime 1s
   fuzz: elapsed: 30s, execs: 419345 (0/sec), new interesting: 124 (total: 140)
   PASS
   ```

2. **Interop với hai peer độc lập** (`net/http` h2c, curl/libnghttp2): GET, response 300 KB (> window 65 535 ⇒ cần
   WINDOW_UPDATE của client), POST 1 MiB (> window của ta), 50 stream song song trên một connection.

   ```console
   $ go test ./internal/h2/ -count=1 -race -v
       h2_test.go:149: streams=53 resets_sent=0 max_active=12
   --- PASS: TestInteropNetHTTP (0.11s)
   --- PASS: TestCurlInterop (0.05s)
   --- PASS: TestGoAwayCases (0.01s)        # 10 ca connection error: FRAME_SIZE, id chẵn, window tràn, HPACK hỏng…
   --- PASS: TestMalformedStreamReset (0.00s)
   --- PASS: TestContentLengthMismatch (0.00s)
   --- PASS: TestFlowControlStreamWindow (0.20s)
   --- PASS: TestWindowTimeout (0.20s)
   --- PASS: TestBodyTimeout (0.20s)
   --- PASS: TestRefusedKeepsHPACKInSync (0.00s)
       h2_test.go:425: streams=100 refused=4900 resets=100 peak_handlers=100
   --- PASS: TestRapidReset (0.16s)
       h2_test.go:446: header block đệm tối đa = 65542 byte
   --- PASS: TestContinuationFlood (0.10s)
   --- PASS: TestPing (0.00s)
   --- PASS: TestServeConnExits (0.00s)
   ok  	github.com/thaivro/edgegate/internal/h2	2.082s
   ```

3. **Proxy h2c → h1**: cùng port với h1 (request h1 19 byte bắt đầu bằng `P` vẫn được trả trong < 1 s), stream dùng lại
   pool upstream (`Dials:25 Reuses:31` cho 56 stream).

   ```console
   $ go test ./internal/proxy -count=1 -race -run 'H2' -v
       phase10_test.go:115: h2 conns=1 streams=55 pool={Dials:25 Reuses:31 Puts:56 …}
   --- PASS: TestH2CProxyInterop (0.18s)
   --- PASS: TestH2Smuggle (0.90s)
   --- PASS: TestH2RapidResetProxy (1.03s)
   ```

4. **Phản chứng đỏ đúng 6 test** (`make h2-nodefense`, 18:16):

   ```console
   $ go test ./internal/h2 ./internal/proxy -count=1 -tags nodefense10 -run '…' -v
       h2_test.go:425: streams=5000 refused=0 resets=5000 peak_handlers=5000
   --- FAIL: TestRapidReset
       h2_test.go:446: header block đệm tối đa = 4194310 byte
   --- FAIL: TestContinuationFlood
   --- FAIL: TestMalformedStreamReset        # không có RST_STREAM nào
   --- FAIL: TestContentLengthMismatch
       phase10_test.go:209: SMUGGLED: upstream thấy ["POST /" "GET /smuggled"]
   --- FAIL: TestH2Smuggle
   --- FAIL: TestH2RapidResetProxy           # upstream nhận 4672-4974 request (mặc định: 136-263), 5 lần mỗi build
   ```

   **Đọc kết quả:** ba phản chứng đầu tiên viết ra thì hỏng, cùng kiểu với phase 9:
   - `TestRefusedKeepsHPACKInSync` đỏ vì test nuốt frame không khớp.
   - Rapid Reset qua proxy xanh nhầm vì RST tới trước khi proxy chạm tới upstream (0 request ở cả hai build).
   - Bản chờ 5 ms đôi khi xuống dưới ngưỡng.

   Ca CRLF và TE **xanh dưới nodefense10** là đúng chứ không phải test hỏng: `httpx.appendWire` (từ chối CR/LF/NUL) và
   `StripHopByHop` của phase 4 là lớp thứ hai. Downgrade h2→h1 chỉ lọt được qua ranh giới body (H2.CL), vì đó là chỗ
   duy nhất mà serializer h1 không tự kiểm.

**Đang nghĩ gì:** smoke `tcphol` ở RTT 0 cho h2/h1 p50 ≈ 3.4x, trong khi G5 dự đoán 0.67-1.5x khi không mất gói. Nếu
turn 2 đo ra vẫn vậy thì chi phí đến từ connection h2 của chính mình, không phải từ TCP: mọi DATA đi qua một `wmu` +
Flush mỗi frame, và window mặc định là 65 535. Phải tách hai thứ này ra trước khi chấm G5.

### 2026-10-02 11:48-13:40 — Turn 2: đo G1-G8; h2spec 143 → 144/145; sửa một bug stream state

Chi tiết + ngõ cụt: [`phase10-log.md` §2](phase10-log.md). Máy: load nền 1.6 lúc bắt đầu, nhảy 13.15 lúc 11:54
(tiến trình ngoài `MainThread` pid 128249, 179 % CPU, session khác — không đụng), 0.8-2.5 lúc đo netem. Raw: `bench/p10-*.txt`.

**G2 — h2spec 2.0.0** (`edgegate -config config/h2.json`, upstream `epolllab`):

```console
$ ~/go/bin/h2spec -h 127.0.0.1 -p 18093 -o 5        # lần ĐẦU, chưa sửa gì (bench/p10-h2spec-1.txt)
      × 2: Sends invalid connection preface
           Expected: Connection closed
             Actual: Error: http2: failed reading the frame payload: unexpected EOF, note that the frame header looked like an HTTP/1.1 header
      × 8: closed: Sends a DATA frame after sending RST_STREAM frame
           Expected: GOAWAY Frame (Error Code: STREAM_CLOSED)
                     RST_STREAM Frame (Error Code: STREAM_CLOSED)
             Actual: WINDOW_UPDATE Frame (length:4, flags:0x00, stream_id:0)
145 tests, 143 passed, 0 skipped, 2 failed
$ ~/go/bin/h2spec -h 127.0.0.1 -p 18093 -o 5        # sau khi sửa 5.1/8 (bench/p10-h2spec-2.txt; ×3 lần như nhau)
145 tests, 144 passed, 0 skipped, 1 failed
$ # cùng lệnh trên bin/edgegate-nodefense10 (bench/p10-h2spec-nodefense10.txt)
        × 1: Sends a HEADERS frame that contains the header field name in uppercase letters
          × 1: Sends a HEADERS frame that contains the connection-specific header field
          × 2: Sends a HEADERS frame that contains the TE header field with any value other than "trailers"
          × 1: Sends a HEADERS frame with the "content-length" header field which does not equal the DATA frame payload length
          × 2: Sends a HEADERS frame with the "content-length" header field which does not equal the sum of the multiple DATA frames payload length
145 tests, 139 passed, 0 skipped, 6 failed
```

**Đọc kết quả:** 98.6 % → 99.3 %. Hai ca fail lần đầu, hai loại khác nhau:
- **5.1/8 là bug hiểu sai spec.** RFC 9113 §5.1 (closed) chỉ cho "minimally process and then discard" frame tới sau RST
  **do mình gửi**. Code dùng một cờ `reset` cho cả hai hướng, nên DATA sau RST **của client** bị bỏ qua lặng lẽ. Sửa:
  cờ `peerReset` ⇒ STREAM_CLOSED. Viết test cho ca ngược lại thì lộ thêm bug thứ hai: TA RST, handler thoát, stream rời
  map ⇒ DATA đang bay bị RST lần nữa. Sửa: nhớ id mình đã RST, FIFO trần 256 (`TestDataAfterRST`).
- **3.5/2 là hệ quả cố ý của D7.** h2spec gửi `INVALID CONNECTION PREFACE\r\n\r\n`; trên port dùng chung, chuỗi đó là một
  request h1 sai cú pháp ⇒ trả `400` rồi đóng là đúng cho h1. Không sửa.

h2spec không có ca nào về Rapid Reset hay CONTINUATION flood. Bản `nodefense10` chỉ fail thêm 5 ca, cả 5 thuộc
`validateDowngrade`. **Pass spec ≠ chịu được tấn công.**

**G1 — HPACK** (`bench/p10-hpack.txt`, ×3 lượt như nhau, tất định):

```console
$ ./bin/h2lab -mode hpack -n 100
  HEADERS #1        = 318 byte
  HEADERS #2..#100    trung vị = 12 byte (min 12, max 12)
  head h1 tương đương = 508 byte
  tỉ số #1 / trung vị #2+   = 26.5x
  tỉ số h1 / trung vị h2 #2+ = 42.3x
```

**G3 — HOL tầng HTTP** (RTT loopback, `bench/p10-hol.txt`):

```console
$ ./bin/h2lab -mode hol -n 20     # ×3 lượt
  /fast một mình (h2)  p50 0.59 | 0.59 | 0.41 ms
  /fast sau /slow, h2  p50 0.84 | 0.71 | 0.66 ms
  /fast sau /slow, h1  p50 490.63 | 490.56 | 490.52 ms
  h2 / một mình = 1.44x | 1.20x | 1.60x ; h1 / h2 = 581.3x | 694.8x | 746.6x
```

**G4 — trần flow control** (`bench/p10-flow-rtt0.txt`, `bench/p10-flow-rtt40.txt`):

```console
$ ./bin/h2lab -mode flow -rounds 3                          # RTT 0
flow: lượt 1 window    65535: 8388608 byte trong 26ms = 319.78 MB/s
flow: lượt 1 window  8388608: 8388608 byte trong 15ms = 569.94 MB/s
  ... lượt 2: 383.64 / 575.54 MB/s; lượt 3: 413.60 / 630.95 MB/s
$ tc qdisc show dev lo; ping -c 3 -q 127.0.0.1 | tail -1     # người dùng bật netem delay 20ms
qdisc netem 8001: root refcnt 2 limit 1000 delay 20ms
rtt min/avg/max/mdev = 40.075/40.307/40.662/0.254 ms
$ ./bin/h2lab -mode flow -rounds 3
flow: lượt 1 window    65535: 8388608 byte trong 5.562s = 1.51 MB/s
flow: lượt 1 window  8388608: 8388608 byte trong 411ms = 20.43 MB/s
flow: lượt 2 window    65535: 8388608 byte trong 5.327s = 1.57 MB/s
flow: lượt 2 window  8388608: 8388608 byte trong 371ms = 22.61 MB/s
flow: lượt 3 window    65535: 8388608 byte trong 5.371s = 1.56 MB/s
flow: lượt 3 window  8388608: 8388608 byte trong 381ms = 22.02 MB/s
```

**Đọc kết quả:** 65 535 B / 40.3 ms = 1.63 MB/s lý thuyết; đo 1.51-1.57 (93-96 % trần). Đúng một window mỗi RTT —
đây là cách window 64 KiB "mặc định hợp lý" của RFC trở thành trần 1.6 MB/s trên đường xuyên lục địa.

**G5 — HOL tầng TCP** (32 GET 64 KiB song song × 20 vòng; `bench/p10-tcphol-*.txt`):

```console
$ ./bin/h2lab -mode tcphol -n 20 -par 32       # netem delay 10ms (RTT 20.2 ms), loss 0 — ×3
  h2 1 conn × 32 stream: p50 48.7 | 47.7 | 47.6 ms   p99 79.9 | 81.1 | 81.1
  h1 32 conn            : p50 42.0 | 41.8 | 41.9 ms   p99 44.9 | 44.1 | 45.0
  h2/h1: p50 1.16x | 1.14x | 1.14x   p99 1.78x | 1.84x | 1.80x
$ ./bin/h2lab -mode tcphol -n 20 -par 32       # netem delay 10ms loss 2% (ping: 5 % mất trên 20 gói) — ×3
  h2 1 conn × 32 stream: p50 112.2 | 115.9 | 124.0 ms   p99 332.2 | 309.5 | 328.4   max 1246.1 | 349.4 | 343.6
  h1 32 conn            : p50 42.3 | 42.2 | 42.3 ms     p99 293.3 | 293.2 | 299.7   max 525.4 | 568.3 | 535.9
  h2/h1: p50 2.65x | 2.74x | 2.93x   p99 1.13x | 1.06x | 1.10x
```

**Đọc kết quả:** mất gói làm h2 tệ ở **trung vị** (2.7x), không ở đuôi (1.1x). Lý do: với h1, một gói mất chỉ chặn
request trên connection đó — 2 % gói ⇒ vài phần trăm request dính RTO, đúng bằng p99 của h1 (≈ 293 ms ≈ RTO tối
thiểu 200 ms + RTT). Với h2, một gói mất chặn **mọi** stream đang bay trên connection (byte sau chỗ mất nằm trong
kernel chờ retransmit, không stream nào đọc được) ⇒ gần như request nào cũng dính ⇒ trung vị dịch lên, đuôi giữ nguyên
cỡ (vẫn là một RTO). HOL ở tầng TCP **phân bố lại** cái giá của mất gói từ vài request sang tất cả. Đây chính là lý
do QUIC tách stream xuống tầng transport.

Phụ (RTT 0, không netem): h2/h1 p50 2.82x. Đối chứng cùng bài trên server `net/http` của Go (`-ref`, không qua
EdgeGate) cũng 1.89-2.97x ⇒ chậm là của "32 stream dồn vào một socket" (một goroutine đọc ở client, ghi tuần tự), không
phải của cài đặt h2 này. Ở RTT 0, CPU là đường ống hẹp; RTT 20 ms che đi gần hết (1.15x).

**G8 — CPU/request** (proxy tiến trình con `taskset 0-1`, `GOMAXPROCS=2`; client `taskset 4-5`; closed-loop 10 s —
*coordinated omission chưa loại trừ, chỉ đọc CPU/req*; `bench/p10-cpu.txt`, `bench/p10-cpu-1x1.txt`):

```console
$ taskset -c 4-5 ./bin/h2lab -mode cpu -rounds 3 -dur 10s          # h2 8 conn × 8 stream vs h1 64 conn
  lượt 1 h2: 192105 req (19202 rps), CPU proxy 13.01 s = 67.7 µs/req, ctxsw 0.136 /req
  lượt 1 h1: 213059 req (21298 rps), CPU proxy 11.21 s = 52.6 µs/req, ctxsw 0.163 /req
  lượt 2 h2: 233867 req (23385 rps), CPU proxy 14.77 s = 63.2 µs/req, ctxsw 0.132 /req
  lượt 2 h1: 212769 req (21272 rps), CPU proxy 13.73 s = 64.5 µs/req, ctxsw 0.151 /req
  lượt 3 h2: 183408 req (18336 rps), CPU proxy 12.63 s = 68.9 µs/req, ctxsw 0.182 /req
  lượt 3 h1: 211127 req (21111 rps), CPU proxy 11.82 s = 56.0 µs/req, ctxsw 0.177 /req
$ taskset -c 4-5 ./bin/h2lab -mode cpu -rounds 3 -dur 10s -h2conns 1 -h2streams 1 -h1conns 1   # tải thấp
  lượt 1 h2: 18381 req (1838 rps), CPU proxy 6.94 s = 377.6 µs/req, ctxsw 8.260 /req
  lượt 1 h1: 33077 req (3308 rps), CPU proxy 5.35 s = 161.7 µs/req, ctxsw 4.720 /req
  lượt 2 h2: 30366 req (3037 rps), CPU proxy 7.66 s = 252.3 µs/req, ctxsw 7.484 /req
  lượt 2 h1: 35732 req (3573 rps), CPU proxy 5.07 s = 141.9 µs/req, ctxsw 4.505 /req
  lượt 3 h2: 32520 req (3252 rps), CPU proxy 7.90 s = 242.9 µs/req, ctxsw 7.398 /req
  lượt 3 h1: 40811 req (4081 rps), CPU proxy 5.21 s = 127.7 µs/req, ctxsw 4.386 /req
```

**Đọc kết quả:** ở cấu hình đã đăng ký, h2/h1 CPU 1.29 | 0.98 | 1.23x (trung vị 1.23, ngưỡng ≥ 1.3), ctxsw
0.83 | 0.87 | 1.03x (ngưỡng ≥ 2x) ⇒ sai cả hai vế. Ở tải thấp (1 stream vs 1 conn): CPU 2.34 | 1.78 | 1.90x, ctxsw
1.75 | 1.66 | 1.69x. Giả thuyết đã nhầm đơn vị: `voluntary_ctxt_switches` đếm lần **luồng OS** ngủ, không đếm lần
chuyển goroutine. Proxy bận liên tục ⇒ goroutine đọc giao việc cho goroutine stream mà luồng không ngủ ⇒ hand-off gần
như miễn phí. Tải thưa ⇒ mỗi hand-off đánh thức một luồng (trên WSL2: đánh thức vCPU, phase 9 — sàn 770 µs/req) ⇒ h2
tốn gần 2x. Giá của kiến trúc hand-off phụ thuộc **độ bận**, không phải hằng số mỗi request.

**G6, G7** (`bench/p10-g6g7.txt`, 13:37): số khớp turn 1 — mặc định: upstream thấy `[]` ở cả 3 ca smuggle, peak handler
100, header block 65 542 B, upstream nhận 143 request; `nodefense10`: `["POST /" "GET /smuggled"]`, 5 000, 4 194 310 B,
4 872.

**Đang nghĩ gì:** turn 3 cần đọc:
- RFC 9113 §5.2 (vì sao flow control tồn tại).
- RFC 9113 §10.5 (DoS).
- Bài CVE-2023-44487 / CVE-2024-27316, nếu tải được nguyên văn.

Nợ cũng phải chốt lại; các món dự kiến ghi ở log §1, cộng thêm ca 3.5/2 của h2spec.

## Giả thuyết sai

| Tôi tưởng là | Thực tế là | Lệnh + output đã lật tẩy | Đã sửa thế nào |
|---|---|---|---|
| G5 vế p99: mất gói 2 % làm h2 (1 conn) tệ hơn h1 (32 conn) ≥ 1.5x **cả ở đuôi** | Đuôi gần bằng nhau (1.06-1.13x); cái tệ nằm ở **trung vị** (2.65-2.93x) | `h2lab -mode tcphol` dưới `delay 10ms loss 2%`: p99 h2 309-332 ms vs h1 293-300 ms; p50 112-124 vs 42 ms | Hiểu lại HOL tầng TCP: không làm đuôi dài hơn (đuôi vẫn là một RTO), mà kéo **mọi** stream vào cùng một lần chờ RTO ⇒ dời giá từ vài request sang tất cả |
| G8: h2 tốn CPU ≥ 1.3x và context switch ≥ 2x so với h1 vì hand-off goroutine đọc ⇄ goroutine stream | Ở 8×8 vs 64 conn: CPU 1.23x (trung vị), ctxsw 0.83-1.03x. Ở 1×1: CPU 1.8-2.3x, ctxsw 1.7x | `h2lab -mode cpu` mặc định và `-h2conns 1 -h2streams 1 -h1conns 1` | `voluntary_ctxt_switches` đếm luồng OS ngủ, không đếm chuyển goroutine; bận ⇒ hand-off không làm luồng ngủ. Đo thêm ở tải thấp; kết luận: giá hand-off tỉ lệ với độ **thưa** của tải |
| (sổ nợ, sau turn 3) P10-3: "client cho window từng 1 byte ⇒ 1 MiB = 1 048 576 DATA frame + syscall" — viết từ RFC 9113 §10.5, chưa đo | 20 000 update 1 byte ⇒ 878-1 208 frame; số frame ≤ số update — không khuếch đại | `TestTinyWindowUpdates` viết dạng "phải GOAWAY", chạy trên code cũ: `DATA frame = 984, byte = 20000`; flush mỗi update: 1 208 | Đóng nợ bằng số đo; giữ test làm chốt bất biến; không thêm phòng tuyến có giá (chặn nhầm client window nhỏ) cho một rủi ro không có |
| (lỗi spec, không phải giả thuyết) "Frame tới sau RST đều bỏ qua" | Chỉ frame tới sau RST **của mình** được bỏ qua; sau RST của peer ⇒ STREAM_CLOSED | h2spec 5.1/8 `Actual: WINDOW_UPDATE Frame` | Cờ `peerReset`; nhớ id mình RST (FIFO 256); `TestDataAfterRST` |

## Số đo

2026-10-02, commit `d4141b8` + sửa turn 2, máy WSL2 6 core (`bench/env-GOTIT-00663.txt`), Go 1.26.2. Latency G3/G5:
lặp tuần tự theo vòng (không phải open-loop có rate — mỗi vòng chờ vòng trước), coi như closed-loop. G8 closed-loop,
chỉ đọc CPU/req. RTT: loopback ~0.05 ms, hoặc netem như ghi.

| # | Đại lượng | Đo được | Kỳ vọng | Chấm |
|---|---|---|---|---|
| G1 | HEADERS #1 / trung vị #2-#100; head h1 / HEADERS h2 #2+ | 26.5x; 42.3x | ≥ 5x; ≥ 10x | ✅ |
| G2 | h2spec lần đầu → sau sửa | 98.6 % (143/145) → 99.3 % (144/145) | ≥ 85 % → ≥ 95 % | ✅ |
| G3 | `/fast` sau `/slow` trên một conn: h1 p50; h2 / một mình; h1 / h2 | 490.5 ms; 1.20-1.60x (trung vị 1.44); 581-747x | ≥ 490 ms; ≤ 1.5x; ≥ 50x | ✅ (vế h2: 1/3 lượt vượt 1.5x) |
| G4 | RTT 40 ms window 65 535; 8 MiB / 65 535; RTT 0 cùng tỉ số | 1.51-1.57 MB/s; 13.5-14.4x; 1.50-1.78x | ∈ [0.8, 2.4]; ≥ 8x; ≤ 2x | ✅ |
| G5 | loss 2 %: h2/h1 p50; p99; loss 0: p50 | 2.65-2.93x; 1.06-1.13x; 1.14-1.16x | ≥ 1.5x; ≥ 1.5x; ∈ [0.67, 1.5] | ½ — p99 ❌ |
| G6 | `/smuggled` tới upstream: mặc định / nodefense10 | 0 / 1 | 0 / ≥ 1 | ✅ |
| G7 | handler đồng thời; header block đệm (mặc định / nodefense10) | 100 / 5 000; 65 542 B / 4 194 310 B | ≤ 100 / ≥ 1 000; ≤ 80 KiB / ≥ 4 MiB | ✅ |
| G8 | h2/h1 CPU/req; ctxsw/req (8×8 vs 64) | 0.98-1.29x (trung vị 1.23); 0.83-1.03x | ≥ 1.3x; ≥ 2x | ❌ |

Phụ, không chấm: tcphol RTT 0 h2/h1 p50 2.82x (EdgeGate) vs 1.89-2.97x (server `net/http`, `-ref`); G8 tải thấp 1×1:
CPU 1.78-2.34x, ctxsw 1.66-1.75x; Rapid Reset qua proxy: upstream nhận 143 vs 4 872 request.

## Invariant + lệnh kiểm chứng

Chạy lại 2026-10-02 13:50, output `bench/p10-invariants.txt` (25 test PASS ở bản mặc định; `make h2-nodefense` đỏ đúng 6);
số lab lấy từ turn 2.

| Invariant | Cài ở | Kiểm chứng | Kết quả |
|---|---|---|---|
| **I1 qua downgrade h2→h1**: byte vượt `content-length` không bao giờ thành request thứ hai trên upstream | `h2/conn.go:onData` (recvd > declCL ⇒ RST trước khi vào buffer); `proxy/h2.go:h2copyRequestBody` (LimitReader + đòi EOF — lớp hai) | `TestH2Smuggle`, `TestContentLengthMismatch`; `make h2-nodefense` PHẢI đỏ | mặc định upstream thấy `[]`; nodefense10 `["POST /" "GET /smuggled"]` |
| **I1/I5 qua downgrade**: CR/LF/NUL trong field và header connection-specific không tới serializer h1 | `h2/request.go:buildRequest` (§8.2.1-8.2.2); lớp hai phase 4: `httpx.appendWire`, `StripHopByHop` | `TestMalformedStreamReset` (8 ca), `TestH2Smuggle` ca CRLF/TE | RST PROTOCOL_ERROR; conn sống (request thứ 9 ⇒ 200); CRLF/TE chặn cả dưới nodefense10 nhờ phase 4 |
| **I2**: frame có trần trước payload; header block có trần trong lúc ghép; header list có trần trong lúc decode; body mỗi stream ≤ window | `frame.go:ReadFrame` (Length > MaxRead), `conn.go:checkHBCap`, `hpack/codec.go:emit` + `readString`, `conn.go:onData` (FLOW_CONTROL_ERROR) | `TestGoAwayCases`, `TestContinuationFlood`, `TestDecoderListLimit` | FRAME_SIZE_ERROR không đọc payload; đệm 65 542 B vs nodefense **4 194 310 B** |
| **Bảng HPACK hai đầu không lệch**: block luôn được decode, kể cả stream bị từ chối (RFC 9113 §10.5.1 "MUST be processed") | `conn.go:endHeaderBlock` (decode TRƯỚC kiểm concurrency) | `TestRefusedKeepsHPACKInSync` | request dùng mục động của block bị REFUSED vẫn 200 |
| **Việc đồng thời mỗi connection có trần thật** (Rapid Reset) | `conn.go:exitStream` (slot trả khi handler thoát), `onRST` (trần tốc độ RST, D6 a′) | `TestRapidReset`, `TestH2RapidResetProxy` | handler đỉnh 100 vs **5 000**; upstream 143 vs **4 872** request |
| **I3**: mọi chờ đợi có deadline — preface/header block (HeaderTimeout), conn rỗi (IdleTimeout), body stream (BodyTimeout), chờ window (WriteTimeout) | `conn.go:setReadDeadline`, `bodyTimer`, `WriteData` AfterFunc | `TestBodyTimeout`, `TestWindowTimeout` | `ErrBodyTimeout`, `ErrWindowTimeout` sau 200 ms |
| **Trạng thái stream đúng §5.1** (frame sau RST của peer ⇒ STREAM_CLOSED; sau RST của mình ⇒ bỏ qua) | `conn.go:onData` (`peerReset`, `localReset` FIFO 256) | `TestDataAfterRST`; `make h2spec` | h2spec 144/145 (`bench/p10-h2spec-3.txt` sau turn 3) |
| **I7/I8**: slot, window connection, goroutine trả trên mọi đường ra | `conn.go:exitStream` (active--, refund body chưa đọc), `shutdown` (markReset mọi stream + `wg.Wait`) | `TestServeConnExits`, `go test ./... -race` | ServeConn thoát < 2 s sau khi client đóng; suite xanh |
| **Drain không cắt stream h2 đang chạy** (phase 7 D8 mở rộng) | `conn.go:Shutdown` (GOAWAY NO_ERROR), `resilience.go:Drain`, `h2.go:serveH2` (Dekker với `closeIdle`) | `TestH2Drain` (trả P10-1) | trước: `Drain 3.001s, forced=2`; sau: `202ms, forced=0`, `/slow` 200 |
| **Data path không `net/http`, không dependency** | — | `grep -rln '"net/http"' internal cmd/edgegate \| grep -v _test.go`; `cat go.mod` | `internal/fixture`, `cmd/edgegate/pprof.go` (như phase 9); `go.mod` không có `require` |

## Đọc gì

Chỉ nguồn đã thật sự mở trong phase này:

- **RFC 7541** (bản `curl https://www.rfc-editor.org/rfc/rfc7541.txt`, 2026-10-01): §4.1 (kích thước mục = name +
  value + 32), §4.2 / §6.3 (size update chỉ ở đầu block), §5.1 (số nguyên prefix N bit, ví dụ C.1.2 = 1337), §5.2 (ba
  luật lỗi padding Huffman), Appendix A (diff máy 61/61), Appendix B (bảng mã — dữ liệu chép từ `$GOROOT/src/vendor/
  golang.org/x/net/http2/hpack/tables.go`, kiểm bằng C.4/C.6), Appendix C (16 vector trích máy).
- **RFC 9113** (bản tải 2026-10-01 và 2026-10-02):
  - §3.1: token "h2c" cho Upgrade "never widely deployed and is deprecated" ⇒ D7 chỉ prior knowledge là đúng hướng.
  - §5.1 "closed": phân biệt RST do mình gửi (bỏ qua frame tới sau) và RST do peer gửi — đọc sau khi h2spec fail 5.1/8.
  - §5.2.1-5.2.3: "Flow control is specific to a connection … single hop"; "a proxy … might have a slow upstream
    connection and a fast downstream one"; "If an endpoint cannot ensure that its peer always has available
    flow-control window space that is greater than the peer's bandwidth * delay product … receive throughput will be
    limited".
  - §8.2.1-8.2.2: luật ký tự field, header connection-specific, `te`. Có câu nói thẳng: "Failure to validate fields
    can be exploited for request smuggling attacks … when messages are forwarded using HTTP/1.1".
  - §10.5 (danh sách DoS: WINDOW_UPDATE nhỏ giọt, PING/SETTINGS phải trả lời, request sai sinh RST); §10.5.1 ("The
    field block MUST be processed to ensure a consistent connection state").
- **CVE-2023-44487** và **CVE-2024-27316**: mô tả nguyên văn từ `https://cveawg.mitre.org/api/cve/<id>`.
  - CVE-2023-44487: "request cancellation can reset many streams quickly, as exploited in the wild in August through
    October 2023".
  - CVE-2024-27316: "incoming headers exceeding the limit are temporarily buffered in nghttp2 in order to generate an
    informative HTTP 413 response. If a client does not stop sending headers, this leads to memory exhaustion". Bài
    học trong câu đó: đệm *để trả lỗi cho đẹp* cũng là đệm không trần.
- **Source h2spec** (`~/go/pkg/mod/github.com/summerwind/h2spec@v2.2.1+incompatible`):
  - `http2/3_5_http2_connection_preface.go`.
  - `http2/5_1_stream_states.go`.

  Đọc để hiểu đúng hai ca fail, không phải để chỉnh code cho khớp test.
- **Go**: `go doc net/http.Protocols` (`SetUnencryptedHTTP2`, Go 1.24+). **Không** đọc `net/http/h2_bundle.go`
  hay `x/net/http2` server — để dành cho sau (như ROADMAP dặn với `net/http` ở phase 2/5).

## Rút ra

**1. HPACK đổi một rủi ro lấy một rủi ro khác.**
- Deflate nén header chung ngữ cảnh với dữ liệu do attacker điều khiển. Độ dài sau nén vì thế làm lộ cookie từng byte
  (CRIME).
- HPACK bỏ hẳn so khớp chuỗi con: một field hoặc khớp nguyên vẹn để thành chỉ số, hoặc không khớp gì.
- Kết quả trên request thật: 318 byte còn 12 byte từ request thứ hai trở đi. Ít hơn 42 lần so với head h1 tương đương.
- Cái giá: bảng động là trạng thái **chung** của hai đầu.
  - Decode hỏng một block thì mình không còn biết bên kia đã thêm gì vào bảng. Vì vậy mọi lỗi HPACK là lỗi
    connection.
  - Một block phải được decode ngay cả khi stream đó bị từ chối. `TestRefusedKeepsHPACKInSync` chứng minh: bỏ qua
    block của stream REFUSED thì request kế tiếp đọc sai chỉ số.

**2. Multiplexing hết HOL ở tầng HTTP, nhưng HOL vẫn còn ở tầng TCP.**
- Ở tầng HTTP: `/fast` sau `/slow` trên cùng một connection h1 phải chờ 490 ms; trên h2 chỉ mất 0.7 ms (gấp 581-747 lần).
- Ở tầng TCP: byte của mọi stream nằm chung một hàng đợi kernel có thứ tự. Mất một gói thì không stream nào đọc tiếp
  được cho tới khi gói đó được gửi lại.
- Số đo dưới loss 2 % cho thấy HOL này **không** làm đuôi dài thêm (p99 h2/h1 chỉ 1.1x). Nó làm **trung vị** tệ đi
  2.7x.
  - Với h1 trên 32 connection, mất gói chỉ trúng vài request. Chúng chịu một RTO, và đó chính là đuôi của h1.
  - Với h2, cùng lần mất gói đó bắt cả 32 stream chịu chung một RTO.
- HOL tầng TCP không tạo ra đuôi mới mà dời giá của mất gói từ vài request sang tất cả. Muốn mỗi stream mất gói độc
  lập thì stream phải nằm dưới tầng transport. Đó là QUIC.
- Phụ: ở RTT 0 một connection h2 cũng chậm hơn 32 connection h1 khoảng 2.8 lần.
  - Server `net/http` của Go đo cùng bài cũng chậm 1.9-3 lần, nên đây là giá của việc dồn mọi thứ vào một socket
    (một luồng đọc, ghi tuần tự). Nó không phải lỗi cài đặt.
  - RTT 20 ms che phần này đi gần hết, còn 1.15 lần.

**3. Flow control thứ hai tồn tại vì TCP chỉ có một window cho cả connection.**
- Proxy có upstream chậm phải chặn được *một* stream mà không chặn cả connection (RFC 9113 §5.2.2 lấy đúng ví dụ
  proxy).
- Ta gửi WINDOW_UPDATE khi handler **đọc**, không khi nhận. Nhờ vậy upstream chậm đọc thì chỉ client của stream đó
  bị chặn, và body đệm mỗi stream không vượt quá window.
- Cái giá đo được: window mặc định 65 535 byte là trần một window mỗi RTT. Ở RTT 40 ms đo được 1.51-1.57 MB/s, sát
  lý thuyết 1.63 MB/s; window 8 MiB nhanh hơn 14 lần. Ở RTT 0 không thấy (1.5-1.8 lần).
- Client và server h2 "mặc định đúng spec" mà không nới window thì là đường ống 1.6 MB/s trên mọi đường xuyên lục địa.

**4. Downgrade h2→h1 phải tự dựng lại ranh giới.**
- Ranh giới request ở h2 là framing (`END_STREAM`). Ở h1 là CL hoặc TE.
- Proxy ghi `content-length` của client sang upstream nhưng lại chép body theo frame. Như vậy CL 0 cộng với DATA
  `GET /smuggled…` thành request thứ hai trên upstream. G6 cho đúng chuỗi đó dưới nodefense10.
- Phòng tuyến là kiểm CL = tổng DATA ngay khi nhận, trước khi byte vào buffer (RFC 9113 §8.1.1 coi lệch là
  malformed).
- Hai vector còn lại (CRLF trong value, `transfer-encoding`) đã bị chặn sẵn ở phase 4: serializer h1 từ chối
  CR/LF/NUL, `StripHopByHop` xoá TE. Đó là phòng tuyến nhiều lớp đúng nghĩa: lớp h2 tắt mà lớp h1 vẫn giữ.
- Ranh giới body là chỗ duy nhất serializer h1 *không thể* tự kiểm, vì nó không biết frame.

**5. Một connection h2 là cả trăm việc, nên trần phải đặt lên *việc*, không đặt lên *frame*.**
- `MAX_CONCURRENT_STREAMS` chỉ là trần thật nếu slot được giữ tới khi công việc xong. Trả slot lúc nhận RST thì 5 000
  handler chạy cùng lúc thay vì 100 (CVE-2023-44487).
- Giữ slot thôi chưa đủ cho proxy. Proxy huỷ upstream khi nhận RST (đúng, vì giải phóng tài nguyên), nhưng việc huỷ
  làm slot về nhanh. Client vẫn bơm được 4 872 request sang upstream rồi huỷ. Phải có thêm trần **tốc độ** RST (143
  request).
- CONTINUATION không có END_HEADERS cần trần trên block đang ghép. Không có trần thì đệm 4 MiB và hơn nữa.
  - Không thể RST riêng stream đó: block chưa decode xong thì bảng HPACK đã lệch, nên chỉ còn cách đóng connection.
  - CVE-2024-27316 nhắc thêm một điều: đệm *để trả lỗi cho đẹp* cũng là đệm không trần.

**6. Giá CPU của h2 không phải hằng số mỗi request.**
- Giả thuyết dự đoán h2 tốn ≥ 1.3 lần CPU và ≥ 2 lần context switch, vì có hand-off giữa goroutine đọc và goroutine
  stream. Thực tế khi proxy bận liên tục: CPU 1.23 lần, context switch 0.83-1.03 lần.
- `voluntary_ctxt_switches` đếm lần luồng OS phải ngủ. Khi lúc nào cũng có việc, hand-off chỉ chuyển goroutine trong
  cùng luồng và gần như miễn phí.
- Khi tải thưa (một stream), mỗi hand-off đánh thức một luồng đang ngủ, nên h2 tốn khoảng 2 lần CPU và 1.7 lần context
  switch.
- Phase 9 thấy hand-off là thứ làm `ReverseProxy` chậm. Phase 10 thêm điều kiện: nó đắt khi tải **thưa**, không đắt
  khi tải bận.

**7. h2spec đạt 143 → 144/145 nhưng không chứng minh được khả năng chịu tấn công.**
- Hai ca fail lần đầu thuộc hai loại khác nhau:
  - 5.1/8 là hiểu sai spec: frame sau RST của peer khác frame sau RST của mình. Viết test cho ca ngược lại còn lộ thêm
    một bug thứ hai.
  - 3.5/2 là đánh đổi cố ý. Port dùng chung h1 + h2c nên preface sai là một request h1 hỏng, trả 400 là đúng.
- Bản nodefense10 vẫn pass 139/145. Năm ca nó fail đều là luật field; không ca nào là Rapid Reset hay CONTINUATION
  flood.
- Bộ test tuân thủ đo xem mình nói đúng ngôn ngữ chưa. Nó không đo việc người nói đúng ngôn ngữ có giết được mình
  không. Bằng chứng cho vế sau chỉ có thể là phản chứng đỏ.

## Nợ kỹ thuật

Chi tiết + lệnh trả trong [`../docs/debts.md`](../docs/debts.md).

- [x] **P10-1** 🔧 Drain không biết connection h2 — **trả turn 3** (GOAWAY NO_ERROR; `TestH2Drain` 3.0 s/forced 2 → 202 ms/forced 0)
- [x] **P10-2** 🔧 Rate limit / shed phase 7 không áp cho stream h2 — đường vòng qua h2c — **trả 2026-10-02** sau turn 3 (`admitDecision` dùng chung; `TestH2RateLimit` 6 × 200 → 2 × 200 + 4 × 429; `TestH2Shed`)
- [x] **P10-3** 🔧 WINDOW_UPDATE nhỏ giọt — **đóng 2026-10-02 bằng số đo**: không khuếch đại (20 000 update ⇒ 878-1 208 DATA frame; ghi đồng bộ + mỗi frame lấy hết credit), không thêm phòng tuyến
- [x] **P10-4** 🔧 PING / SETTINGS / malformed khuếch đại — **trả 2026-10-02**: gộp Flush phản hồi điều khiển (100 001 → 111 Flush / 100 000 PING), SETTINGS áp một lần (1.8-2.8 ms → 51-106 µs / frame); không cần trần tốc độ
- [ ] **P10-9** 🔧 `TestIdleClosedUpstream` chập chờn khi cả suite chạy dưới tải (sleep cố định 20 ms chờ FIN)
- [ ] **P10-5** ⏳ ALPN `h2` trên TLS
- [ ] **P10-6** ⏳ h2spec 3.5/2 (listener `h2c_only` nếu cần 145/145)
- [ ] **P10-7** ⏳ Trailer h2 bị bỏ (chưa proxy được gRPC)
- [ ] **P10-8** 📏 G5/G8 trên Linux thuần, `tc` chỉ chiều client↔proxy, nhiều mẫu hơn
