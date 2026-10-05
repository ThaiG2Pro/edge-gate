# Mở nắp reverse proxy — series blog (bản ý tưởng)

> Cho dev ngày nào cũng đặt nginx trước service, nhưng chưa bao giờ biết request của mình đi qua
> những gì trong mấy trăm micro giây ở giữa. Không cần đọc mã nguồn nginx hay Envoy: mỗi bài lấy
> **một câu hỏi** bạn từng gặp, trả lời bằng **một thí nghiệm chạy được trong 5 phút**, rồi mở ra
> **vài chục dòng code** của một proxy tự viết từ socket TCP trần ([EdgeGate](../README.md)) làm
> đúng việc đó.

Series anh em: [Mở nắp database](https://github.com/ThaiG2Pro/mini-kv-db/tree/master/blog) (minidb).

**Trạng thái:** chưa có bài nào được viết. File này là danh sách ý tưởng, mỗi ý đã có sẵn số đo và
file output trong repo. Khi viết xong bài nào thì đổi dòng của nó trong mục lục thành link.

## Cách đọc (giống series database)

Mỗi bài có bốn phần, luôn theo thứ tự này:

1. **Câu hỏi:** một thứ bạn đã thấy nhưng chưa hiểu, kiểu *"sao request nào cũng chậm đúng 40 ms?"*.
2. **Thí nghiệm:** lệnh copy-paste, chạy trên hệ thống thật (nginx, h2o, Go `net/http`, kernel Linux)
   khi có thể, kèm số đo thật và file output gốc trong `bench/`.
3. **Bên trong:** cơ chế, vẽ bằng một hình và kể bằng code EdgeGate (ngắn, bỏ bớt phần phụ).
4. **Mang về dùng:** một hai quy tắc làm khác đi được ngay từ ngày mai.

Muốn chạy thí nghiệm thì cần Go 1.26. Một số bài cần thêm Docker (nginx, h2o), `sudo` cho `tc netem`
(giả lập RTT), hoặc `h2spec`. Cột "Cần" trong mục lục ghi rõ.

```bash
git clone https://github.com/ThaiG2Pro/edge-gate.git && cd edge-gate
make envcap   # chụp môi trường máy bạn, để so số với số trong bài
```

**Luật số đo của series:** máy đo chính là laptop WSL2, nên chỉ tỉ số là đáng tin. Bài nào có số
Linux thuần thì in cả hai cột. Bài 4 tồn tại chính vì một kết luận đúng trên WSL2 nhưng sai trên Linux.

## Mục lục (ý tưởng)

| # | Bài | Câu hỏi của bạn | Thí nghiệm chính | Cần | Phase |
|---|---|---|---|---|---|
| 0 | Một request đi qua proxy gồm những gì? | "Thêm nginx vào giữa" thật ra là thêm mấy socket, mấy lần parse? | Đếm syscall và socket cho một `curl`; overhead L7 2.29× (Linux) | Go | 3 |
| 1 | Benchmark của bạn đang nói dối | Sao `wrk` báo p99 2 ms mà người dùng chờ vài giây? | Closed-loop vs open-loop cùng một hệ thống quá tải: p99 lệch **1787×** (WSL2) / **276×** (Linux) | Go | 0, 7 |
| 2 | Bí ẩn 40 mili giây | Sao request nhỏ nào cũng chậm đúng ~40 ms? | Nagle + delayed ACK: sàn **44 ms** (WSL2) / **41 ms** (Linux), p50 ≈ p99 | Go | 0, 3 |
| 3 | Keep-alive tiết kiệm bao nhiêu? | Connection pool nhanh hơn "36 lần" hay "2 lần"? | Dial vs reuse: 36.69× ở RTT 0, **2.00×** ở RTT 20 và 40 ms; pool 5.34× vs 1.37× | Go, sudo | 0, 5 |
| 4 | Hết port mà không báo lỗi? (tôi đã sai) | Không dùng pool thì chuyện gì xảy ra khi hết ephemeral port? | WSL2: 1502 → 455 conn/s, **0 lỗi**. Linux: 1089 → 386 rồi **101 lỗi `EADDRNOTAVAIL`** | Go | 0, 5 |
| 5 | Hai server đọc một request ra hai cách | Request smuggling là gì, sao nginx đứng trước vẫn bị? | 59 ca phát vào nginx 1.25 và h2o: **46 cặp** origin nhận thứ EdgeGate từ chối; `net/http` chỉ cùng từ chối 46% | Go, Docker | 4 |
| 6 | Fuzz 7.75 triệu lần không thấy, bảng tay thấy | Differential fuzzing chứng minh được gì? | Fuzz phẳng 390 s bỏ sót 2 lệch thật; fuzz có cấu trúc tìm ra trong **0.10 s** | Go | 2 |
| 7 | Retry có an toàn không? | Proxy được tự retry request nào, và retry bao nhiêu thì vừa? | PUT replay 50/50, POST 502; retry budget: nhân tải **1.12×** nhưng 502 25.3%, retry mù **1.49×** và 0% | Go | 5, 7 |
| 8 | Load balancer "thông minh" có thông minh không? | Least-conn, P2C, EWMA: cái nào thật sự giảm p99? | least-conn p99 **0.98×** RR; P2C 3.4× tốt hơn nhưng bỏ đói node hồi phục **0%** vs 25% | Go | 6 |
| 9 | Xếp hàng hay từ chối? | Quá tải thì nên cho request chờ hay trả 503 ngay? | Shed ở 2× tải: p99 **11.6 s → 33 ms**, goodput 1471 vs 1441/s, 55% nhận 503 | Go | 7 |
| 10 | Slowloris không giết được Go | Kẻ tấn công giữ 500 connection mở thì sao? | Probe 100% sống kể cả tắt HeaderTimeout; **trần connection** mới giết; tarpit | Go | 7 |
| 11 | Một connection rỗi tốn bao nhiêu RAM? | Mười nghìn client giữ keep-alive tốn bao nhiêu? | 28 → 8–9 KiB/conn; epoll tự viết ít RAM **55×** nhưng rps **1.01×**; `sync.Pool` 43× B/op nhưng chỉ 2.4× ns/op | Go | 9 |
| 12 | Vì sao `httputil.ReverseProxy` tốn CPU gấp 3? | Thư viện chuẩn chậm ở đâu? | Context switch tự nguyện **0.68**/req vs 0.05–0.14; CPU/req 1 : 3.2–3.7 | Go | 9 |
| 13 | HTTP/2 có nhanh hơn HTTP/1.1 không? | Multiplex trên một connection thì mất gói ra sao? | Loss 2%: h2 p50 **2.65×** tệ hơn nhưng p99 chỉ 1.13×; window 64 KiB ở RTT 40 ms trần 1.5 MB/s | Go, sudo | 10 |
| 14 | Qua hết bài kiểm tra chuẩn vẫn bị đánh sập | h2spec 145/145 có nghĩa là an toàn? | Bản tắt phòng thủ vẫn qua **139/145**; Rapid Reset 100 vs **5000** handler | Go, h2spec | 10 |
| 15 | TLS trước proxy tốn bao nhiêu? | Domain fronting, session resumption, pool tới upstream TLS | SNI ≠ Host ⇒ **421**; pool tới upstream TLS tiết kiệm **2.03 RTT**; reload cert 96 lần, 0 lỗi | Go | 8 |
| 16 | Bản đồ mang theo | Tổng kết: một request đi qua những đâu, hỏng ở đâu | — | — | — |

## Ý tưởng từng bài

Mỗi mục: mở bài bằng tình huống nào, thí nghiệm nào, code nào để mở, bài học mang về, và việc phải làm
trước khi viết.

### 0. Một request đi qua proxy gồm những gì?
- **Mở bài:** bạn thêm nginx trước API, latency tăng vài trăm micro giây. Vài trăm micro giây đó làm gì?
- **Thí nghiệm:** `make proxylab`, quickstart trong README + `strace -c -f`. Overhead L7 p50: 3.39× (WSL2,
  `bench/p3-proxybench-*.txt`), **2.29×** (Linux, `bench/baseline/thai-computer-20261004/p3-proxybench.txt`).
  Chênh tuyệt đối ≈ 1 lần dial loopback.
- **Bên trong:** `internal/proxy/forward.go` `roundTrip`: accept → `ReadRequest` → chọn backend → lấy
  connection → serialize lại → đọc response → serialize lại. Vẽ hình 2 socket, 4 lần parse.
- **Mang về:** proxy L7 không "chuyển tiếp byte", nó **đọc hiểu rồi viết lại**. Mọi tính năng phía sau
  (smuggling, retry, LB) đều sống ở khoảng giữa đó.

### 1. Benchmark của bạn đang nói dối
- **Mở bài:** load test báo p99 2 ms, production thì người dùng than chậm.
- **Thí nghiệm:** `go run ./cmd/netlab -exp omission`. WSL2: closed-loop 1 conn p99 1.87 ms vs open-loop
  3.35 s = **1787×** (`bench/p0-omission-rtt0.txt`). Linux: 2.19 vs 604.86 ms = **276×**
  (`bench/baseline/thai-computer-20261004/p0-omission.txt`).
- **Bên trong:** `internal/loadgen`: latency tính từ giờ **hẹn** `t0 + i/rate`, không từ giờ gửi.
  Closed-loop chỉ gửi khi nhận xong, nên không bao giờ thấy hàng đợi.
- **Mang về:** hỏi tool load test của bạn là open hay closed loop. `wrk` là closed-loop; `wrk2` và
  HdrHistogram có chế độ sửa coordinated omission.

### 2. Bí ẩn 40 mili giây
- **Mở bài:** request nhỏ nào cũng chậm đúng ~40 ms, không phải ở đuôi mà ở trung vị.
- **Thí nghiệm:** `go run ./cmd/netlab -exp nagle`. Spike **44.03 ms** p50 (WSL2, `bench/p0-nagle-rtt0.txt`),
  **41 ms** (Linux). Một `Write` vs hai `Write`, `NoDelay` bật vs tắt.
- **Bên trong:** Nagle giữ gói nhỏ thứ hai chờ ACK; bên kia trì hoãn ACK 40 ms. Go bật `TCP_NODELAY`
  sẵn: chuyện "flag `-nodelay=false` là no-op" ở phase 3 (giả thuyết sai hai lần).
- **Mang về:** gom header và body thành một lần ghi (`bufio`, `writev`); nghi ngờ ngay khi thấy độ trễ
  là một hằng số tròn.

### 3. Keep-alive tiết kiệm bao nhiêu?
- **Mở bài:** blog nói connection pool nhanh hơn 36 lần. Thật không?
- **Thí nghiệm:** `make rtt-up` + `go run ./cmd/netlab -exp rtt`. Dial/reuse 36.69× ở RTT 0, nhưng
  **2.00×** ở cả RTT 20 ms lẫn 40 ms (`bench/p0-rtt-netem*.txt`). Pool qua proxy: 5.34× ở RTT 0,
  1.37× = 0.87 RTT ở RTT 20 ms (`bench/p5-poollab-rtt*.txt`), ngược hẳn dự đoán trong ROADMAP.
- **Bên trong:** dial trả 2 RTT, reuse trả 1, nên tỉ số tiến về 2 khi RTT tăng. Bẫy phụ: `netem` trên `lo`
  áp delay **hai chiều**, đặt 10 ms ra RTT 20 ms.
- **Mang về:** đừng báo cáo lợi ích của pool bằng tỉ số. Đơn vị đúng là "1 RTT cho mỗi connection mới".

### 4. Hết port mà không báo lỗi? (tôi đã sai)
- **Mở bài:** viết như một lời đính chính. README của chính repo từng ghi "trần OS không báo bằng lỗi".
- **Thí nghiệm:** `go run ./cmd/netlab -exp limits`. WSL2: 1502 → 738 → 601 → 455 conn/s, 0 lỗi
  (`bench/p0-limits-eth0*.txt`). Linux: 1089 → 386 conn/s rồi **101 lỗi `cannot assign requested address`**
  (`bench/baseline/thai-computer-20261004/p0-limits.txt`). Thêm pool: TIME_WAIT phía proxy +1964 → +0.
- **Bên trong:** ai chủ động đóng thì giữ TIME_WAIT; `tcp_tw_reuse` và lớp mạng ảo của WSL2 che mất trần.
- **Mang về:** môi trường đo cũng là một biến. Kết luận nào chỉ đo trên một môi trường thì ghi rõ môi trường đó.
- **Trước khi viết:** tìm hiểu cơ chế nào của WSL2 làm im lỗi (chưa giải thích được trong repo).

### 5. Hai server đọc một request ra hai cách
- **Mở bài:** request có cả `Content-Length` và `Transfer-Encoding`. Front tin cái này, back tin cái kia,
  và request thứ hai lọt qua mà không ai kiểm.
- **Thí nghiệm:** `make oracle-ext` (Docker). 59 ca phát thẳng vào nginx 1.25 và h2o: **46 cặp** (ca × origin)
  mà origin **nhận** thứ EdgeGate từ chối (nginx 17, h2o 29), ví dụ bare LF (`bench/p4-6-oracle-ext.txt`).
  `net/http` chỉ cùng từ chối 24/52 = **46%** (`bench/p4-oracle.txt`). Quickstart: CL.TE ⇒ 400.
- **Bên trong:** `internal/httpx/defense.go`, 7 phòng tuyến; `testdata/smuggle/` 66 ca. Phản chứng
  `make smugglelab-nodefense` phải đỏ.
- **Mang về:** origin "khoan dung đúng RFC" không có nghĩa là an toàn khi đứng sau một proxy khác. Proxy
  phải chặt hơn thứ nó che chắn.

### 6. Fuzz 7.75 triệu lần không thấy, bảng tay thấy
- **Mở bài:** chạy differential fuzz với `net/http` 300 giây, 7.75 triệu lần, 0 lệch. Xong chưa?
- **Thí nghiệm:** `make difffuzz` rồi `make difffuzz-chunk`. Bảng đối chiếu tay tìm 2 lệch thật ở
  chunk-size (`" 3"`, `"3 ;x"`); fuzz phẳng 390 s không ra; fuzz có cấu trúc ra sau **0.10 s**
  (`bench/p2-structfuzz-lenient-60s.txt`).
- **Bên trong:** lệch differential không sinh nhánh code mới, nên coverage không dẫn fuzzer tới đó.
  Thu nhỏ không gian input thay vì chạy lâu hơn.
- **Mang về:** "0 disagreements" chỉ nói "không khác oracle", không nói "đúng". Luôn kèm một bảng tay.

### 7. Retry có an toàn không?
- **Mở bài:** upstream đóng connection keep-alive đúng lúc proxy gửi request. Retry hay trả 502?
- **Thí nghiệm:** `go test ./internal/proxy -run TestIdleClosedUpstream -v`: PUT có body replay **50/50**,
  POST 25 × 502. `make retrylab`: budget 10% nhân tải lên upstream **1.12×** nhưng 25.3% client nhận 502;
  retry mù **1.49×** và 0% 502 (`bench/p7-retrylab.txt`).
- **Bên trong:** `canRetry` trong `forward.go`: chỉ retry khi connection là đồ dùng lại, 0 byte response,
  method idempotent (RFC 9110 §9.2.2), body ≤ 64 KiB đã buffer.
- **Câu chuyện kèm:** test kill-revive đỏ trên main vì 32 client đẩy nhu cầu retry lên 25% > budget 10%:
  502 = `RetryDenied` = 1160. Code đúng, test sai.
- **Mang về:** retry budget đổi lỗi lấy sự sống của upstream. Chọn con số đó có chủ đích, đừng để mặc định.

### 8. Load balancer "thông minh" có thông minh không?
- **Mở bài:** chuyển từ round-robin sang least-conn để "né node chậm". p99 có giảm không?
- **Thí nghiệm:** `make lblab-skew`, `make lblab-recover`. least-conn cắt tải node chậm 25 → 4.3% nhưng p99
  **0.98×** RR; P2C+EWMA tải node chậm 0.6% ⇒ p99 ~3.4× tốt hơn (`bench/p6-lblab-skew*.txt`). P2C tau 30 s cho
  node vừa hồi phục **0.0%** tải vs least-conn 25% (`bench/p6-lblab-recover.txt`).
- **Phần phụ: health check chậm 600 ms.** `TestLBKillRevive`: chỉ active health thì hàng nghìn request đập
  vào backend chết; thêm passive outlier còn hàng chục.
- **Bên trong:** `internal/lb/`: P2C, EWMA decay theo **thời gian** (phản chứng decay theo request đỏ 0/200).
- **Mang về:** p99 chỉ giảm khi node chậm nhận dưới ~1% tải. Thuật toán nhớ lâu thì quên chậm.

### 9. Xếp hàng hay từ chối?
- **Mở bài:** traffic gấp đôi capacity. Hàng đợi vô hạn hay 503?
- **Thí nghiệm:** `make shedlab` (33 s). Không shed: p99 **11.63 s**, 100% 200. Shed: p99 **33.08 ms**, 54.9% nhận 503
  với p99 8.58 ms, goodput 1471 vs 1441/s (`bench/p7-shedlab.txt`). Lượt chạy lại 2026-10-05: 11.57 s → 23.84 ms,
  53.4% nhận 503, goodput 1475 vs 1488/s. In cả hai lượt.
- **Bên trong:** bounded inflight + hàng đợi có trần + queue timeout. Đo bằng open-loop (bài 1), nếu không thì
  không thấy gì.
- **Mang về:** quá tải thì goodput không đổi, chỉ đổi **ai** phải chờ. 503 nhanh cứu được client; 11 giây thì không.

### 10. Slowloris không giết được Go
- **Mở bài:** kẻ tấn công mở 500 connection, mỗi 10 giây gửi một byte header.
- **Thí nghiệm:** `make slowlab`, `make slowlab-nodefense`. Probe sống 100% kể cả khi tắt HeaderTimeout
  (`bench/p7-slowlab*.txt`). Thứ giết proxy là **trần connection** (MaxConns 256 ⇒ probe hỏng 100%).
  Trần per-IP thì đẩy attacker sang reconnect liên tục.
- **Bên trong:** goroutine rẻ nên giữ 500 connection chậm không đáng kể; tài nguyên hữu hạn thật là slot.
- **Mang về:** đặt trần theo request và theo IP, đừng chỉ đặt trần tổng connection.
- **Trước khi viết:** số tarpit 15.6× trên Linux chưa có output thô trong repo (`docs/debts.md` P7-2b).

### 11. Một connection rỗi tốn bao nhiêu RAM?
- **Mở bài:** 10 nghìn client mobile giữ keep-alive. Tốn bao nhiêu RAM?
- **Thí nghiệm:** `make idlelab`, `make epolllab`, `make perflab`. 28 → **8–9 KiB/conn** sau khi trả `bufio`
  về pool lúc rỗi (`bench/p9-idlelab.txt`). Epoll tự viết 0.14 KiB/conn (**55×** ít hơn) nhưng rps **1.01×**
  (`bench/p9-epolllab.txt`). `sync.Pool`: B/op **43×** nhưng ns/op chỉ **2.4×**, vì giá thật là tần suất GC
  (`bench/p9-perflab*.txt`).
- **Bên trong:** netpoller của Go **là** epoll. Goroutine-per-connection trả bằng không gian, không bằng thời gian.
- **Mang về:** đo cái mình định claim. Giảm cấp phát 43 lần không có nghĩa là nhanh hơn 43 lần.

### 12. Vì sao `httputil.ReverseProxy` tốn CPU gấp 3?
- **Mở bài:** ReverseProxy của thư viện chuẩn là 10 dòng code. Giá của 10 dòng đó là gì?
- **Thí nghiệm:** `./scripts/cpu-vs-nginx.sh`. CPU/req EdgeGate : nginx : ReverseProxy = **1 : 0.79–1.05 : 3.2–3.7**
  (`bench/p9-cpu-vs-nginx.txt`); context switch tự nguyện 0.68/req vs 0.05–0.14 (`bench/p9-rp-ctxsw.txt`).
- **Bên trong:** ReverseProxy đi qua Transport: 3 goroutine mỗi request, handoff qua channel. EdgeGate stream
  trong một goroutine.
- **Mang về:** kiến trúc thắng ngôn ngữ. Khi profile, đếm context switch chứ không chỉ đếm CPU.
- **Trước khi viết:** số WSL2, 2 core. Bảng ba cột Linux thuần (P9-4) và trace (P9-5) chưa có output thô trong repo.

### 13. HTTP/2 có nhanh hơn HTTP/1.1 không?
- **Mở bài:** chuyển sang HTTP/2 để "multiplex cho nhanh". Trên mạng mất gói thì sao?
- **Thí nghiệm:** `netem delay 10ms loss 2%` + `./bin/h2lab -mode tcphol`. h2 một connection 32 stream vs h1
  32 connection: p50 **2.65×** tệ hơn, p99 chỉ 1.13× (`bench/p10-tcphol-d10-loss2.txt`). Window 65 535 ở RTT 40 ms:
  **1.51–1.57 MB/s** (lý thuyết 1.63), nâng lên 8 MiB nhanh 13.5–14.4× (`bench/p10-flow-rtt40.txt`).
- **Bên trong:** `internal/h2/`: một gói mất chặn **mọi** stream trên connection (HOL ở tầng TCP).
- **Mang về:** HTTP/2 dời giá mất gói từ vài request sang tất cả. Đó là lý do QUIC tồn tại.

### 14. Qua hết bài kiểm tra chuẩn vẫn bị đánh sập
- **Mở bài:** server HTTP/2 của bạn qua h2spec 145/145. An toàn chưa?
- **Thí nghiệm:** `make h2spec` (144/145 chung port, 145/145 tách port), `make h2-nodefense`: bản **tắt** phòng thủ
  vẫn qua **139/145** (`bench/p10-h2spec-nodefense10.txt`). Rapid Reset (CVE-2023-44487): 100 vs **5000** handler;
  CONTINUATION flood: 65 KiB vs 4 MiB bộ nhớ.
- **Bên trong:** h2spec kiểm luật frame, không kiểm tài nguyên. Phòng thủ là trần, không phải cú pháp.
- **Mang về:** pass spec ≠ chịu tấn công. Viết test tấn công riêng cho mỗi trần tài nguyên.

### 15. TLS trước proxy tốn bao nhiêu?
- **Mở bài:** bật HTTPS ở proxy. Thêm mấy RTT, và có lỗ hổng gì mới?
- **Thí nghiệm:** `make tlslab`, `make tlslab-rtt`. SNI là a, `Host:` là b ⇒ **421** (bản tắt phòng thủ trả 200 từ b:
  domain fronting). TLS 1.3 thêm 1 RTT; resumption 1.3 **vẫn** +1 (Go không có 0-RTT trên TCP). Pool tới upstream TLS
  tiết kiệm **2.03 RTT** (`bench/p8-tlslab-rtt20.txt`). Reload cert 96 lần dưới tải, 98 151 request, 0 lỗi
  (`bench/p8-tlslab-reload.txt`).
- **Bên trong:** `internal/tlsx/`: `atomic.Pointer[CertStore]`, reload hỏng thì giữ bản cũ.
- **Mang về:** kiểm Host khớp SNI. Pool tới upstream TLS quan trọng gấp đôi pool plaintext.

### 16. Bản đồ mang theo
Tổng kết một hình: request đi từ accept tới response, mỗi chặng ghi bài nào giải thích nó và thứ gì có thể
hỏng ở đó. Kèm bảng "đã đo, và nó nói gì" từ `ROADMAP.md`.

## Thứ tự viết đề xuất

1. **Bài 4** (port, tôi đã sai) và **bài 1** (benchmark nói dối): số có sẵn cả WSL2 lẫn Linux, câu chuyện mạnh nhất.
2. **Bài 5** (smuggling) và **bài 9** (shed): demo trực quan, chạy dưới 1 phút.
3. **Bài 7** (retry): có câu chuyện mới nhất (test đỏ vì budget).
4. Các bài còn lại theo thứ tự số.
5. **Bài 10, 12** viết sau cùng: chờ đẩy output Linux thuần vào `bench/baseline/`.

## Quy ước khi viết

- Mỗi số trong bài phải trỏ tới một file trong `bench/` hoặc lệnh tái tạo được. Số không có file thì không vào bài.
- In cả hai lượt chạy nếu có, như series database.
- Biểu đồ đặt cạnh bài, sinh từ file bench, kèm CSV.
- Lệnh trong bài phải chạy được từ một bản clone mới.
