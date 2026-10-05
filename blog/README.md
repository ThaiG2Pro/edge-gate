# Mở nắp reverse proxy — series blog

> Cho dev ngày nào cũng đặt nginx trước service, nhưng chưa bao giờ biết request của mình đi qua
> những gì trong mấy trăm micro giây ở giữa. Không cần đọc mã nguồn nginx hay Envoy: mỗi bài lấy
> **một câu hỏi** bạn từng gặp, trả lời bằng **một thí nghiệm chạy được trong 5 phút**, rồi mở ra
> **vài chục dòng code** của một proxy tự viết từ socket TCP trần ([EdgeGate](../README.md)) làm
> đúng việc đó.

Series anh em: [Mở nắp database](https://github.com/ThaiG2Pro/mini-kv-db/tree/master/blog) (minidb).

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
Linux thuần thì in cả hai cột. Bài 1 và bài 4 kể hai lần chính repo này đã phải rút lại một con số.

## Mục lục

| # | Bài | Câu hỏi của bạn | Thí nghiệm | Cần | Phase |
|---|---|---|---|---|---|
| 0 | [Một request đi qua proxy gồm những gì?](00-mot-request.md) | "Thêm nginx vào giữa" thật ra là thêm mấy socket, mấy lần parse? | Overhead L7 p50 3.39× khi dial mới mỗi request (WSL2); có pool thì thêm 51–101 µs | Go | 3, 5 |
| 1 | [Benchmark của bạn đang nói dối](01-benchmark-noi-doi.md) | Sao `wrk` báo p99 2 ms mà người dùng chờ vài giây? | Closed vs open-loop cùng hệ thống quá tải: p99 lệch 370× (WSL2) / 276–476× (Linux); con số 1787× bị rút vì generator có race | Go | 0, 7 |
| 2 | [Bí ẩn 40 mili giây](02-40ms.md) | Sao request nhỏ nào cũng chậm đúng ~40 ms? | Nagle + delayed ACK: sàn 44 ms (WSL2) / 41 ms (Linux), p50 ≈ p99 | Go | 0, 3 |
| 3 | [Keep-alive tiết kiệm bao nhiêu?](03-keep-alive.md) | Connection pool nhanh hơn "36 lần" hay "2 lần"? | Dial vs reuse ở RTT 0: 15.8–36.7× (WSL2), 2.5× (Linux); ở RTT 20 và 40 ms: **2.00×** | Go, sudo | 0, 5 |
| 4 | [Hết port mà không báo lỗi? (tôi đã sai)](04-het-port.md) | Không dùng pool thì chuyện gì xảy ra khi hết ephemeral port? | WSL2 0 lỗi tới tw 32k; Linux 101 lỗi `EADDRNOTAVAIL` ở tw 88k. Chưa so ở cùng mức | Go | 0, 5 |
| 5 | [Hai server đọc một request ra hai cách](05-smuggling.md) | Request smuggling là gì, sao nginx đứng trước vẫn bị? | 59 ca vào nginx và h2o: 46 cặp origin nhận thứ EdgeGate từ chối; `net/http` cùng từ chối 46% | Go, Docker | 4 |
| 6 | [Fuzz 7.75 triệu lần không thấy, bảng tay thấy](06-fuzz.md) | Differential fuzzing chứng minh được gì? | Fuzz phẳng 390 s bỏ sót 2 lệch thật; fuzz có cấu trúc tìm ra trong 0.10 s | Go | 2, 4 |
| 7 | [Retry có an toàn không?](07-retry.md) | Proxy được tự retry request nào, retry bao nhiêu thì vừa? | PUT replay 50/50, POST 502; budget nhân tải 1.12× nhưng 25% 502, retry mù 1.49× và 0% | Go | 5, 7 |
| 8 | [Load balancer "thông minh" có thông minh không?](08-load-balancer.md) | Least-conn, P2C, EWMA: cái nào thật sự giảm p99? | least-conn p99 0.98× RR; P2C bỏ đói node hồi phục 0% vs 25% | Go | 6 |
| 9 | [Xếp hàng hay từ chối?](09-shed.md) | Quá tải thì cho request chờ hay trả 503 ngay? | Shed ở 2× tải: p99 11.6 s → 24–33 ms, goodput gần như không đổi | Go | 7 |
| 10 | [Slowloris không giết được Go](10-slowloris.md) | Kẻ tấn công giữ 500 connection mở thì sao? | Probe sống 100%; trần 256 connection + HeaderTimeout còn 78.4%, tắt HeaderTimeout thì 0% | Go | 7 |
| 11 | [Một connection rỗi tốn bao nhiêu RAM?](11-ram-connection.md) | Mười nghìn client giữ keep-alive tốn bao nhiêu? | 28 → 8–9 KiB/conn; epoll ít RAM 55× nhưng rps 1.01×; `sync.Pool` 43× B/op, 2.4× ns/op | Go | 0, 9 |
| 12 | [Vì sao `httputil.ReverseProxy` tốn CPU gấp 3?](12-reverseproxy.md) | Thư viện chuẩn chậm ở đâu? | Context switch 0.68/req vs 0.05–0.14; CPU/req 1 : 3.2–3.7 (WSL2) | Go, Docker | 9 |
| 13 | [HTTP/2 có nhanh hơn HTTP/1.1 không?](13-http2.md) | Multiplex một connection thì mất gói ra sao? | Loss 2%: h2 p50 2.65–2.93× tệ hơn, p99 chỉ 1.06–1.13× | Go, sudo | 10 |
| 14 | [Qua hết bài kiểm tra chuẩn vẫn bị đánh sập](14-h2spec.md) | h2spec 145/145 có nghĩa là an toàn? | Bản tắt phòng thủ vẫn qua 139/145; Rapid Reset 100 vs 5000 handler | Go, h2spec | 10 |
| 15 | [TLS trước proxy tốn bao nhiêu?](15-tls.md) | Domain fronting, resumption, pool tới upstream TLS | SNI ≠ Host ⇒ 421; pool tới upstream TLS tiết kiệm 2.03 RTT; reload cert 96 lần, 0 lỗi | Go | 8 |
| 16 | [Bản đồ mang theo](16-ban-do-mang-theo.md) | Tổng kết: một request đi qua những đâu, hỏng ở đâu | — | — | — |

Số đo trong các bài lấy từ `bench/`, `bench/baseline/thai-computer-20261004/` và `diary/`, nơi có lệnh và
output gốc. Số nào chỉ có trong `docs/debts.md` mà chưa có output thô thì bài ghi rõ "(chưa có output thô
trong repo)".
