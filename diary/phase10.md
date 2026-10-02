# Phase 10 — (tùy chọn) HTTP/2 h2c: frame, HPACK, multiplexing, flow control

- **Thời lượng dự kiến:** 3-4 ngày · **thực tế:** đang làm (turn 1 2026-10-01 17:46-18:16)
- **Bắt đầu:** 2026-10-01 17:46 · **Kết thúc:** —
- **Trạng thái:** 🚧 turn 1 xong (code + test + phản chứng), chưa đo — giả thuyết và quyết định bên dưới viết **trước** file `.go` đầu tiên của phase; D6 a′ thêm 18:08, trước khi đo.
- **Commit:** — (commit nền `9a45ac3`)

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
make h2lab          # G1, G3, G8
make h2spec         # G2 (cần ~/go/bin/h2spec)
sudo tc qdisc add dev lo root netem delay 20ms && make h2lab-flow;  sudo tc qdisc del dev lo root   # G4
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

## Giả thuyết sai

| Tôi tưởng là | Thực tế là | Lệnh + output đã lật tẩy | Đã sửa thế nào |
|---|---|---|---|

## Số đo

*(turn 2)*

## Invariant + lệnh kiểm chứng

*(turn 3)*

## Đọc gì

*(turn 3 — chỉ nguồn đã thật sự đọc)*

## Rút ra

*(turn 3)*

## Nợ kỹ thuật

*(turn 3)*
