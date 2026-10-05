# Bài 16 — Bản đồ mang theo

> Series [Mở nắp reverse proxy](README.md) · bài 16/16 · cần đọc trước: [bài 0](00-mot-request.md)

Mười sáu bài trước mở từng khúc của một reverse proxy ra một lần. Bài cuối này ghép chúng lại: đi
theo **một** request từ lúc kernel trao cho proxy một socket mới tới lúc byte cuối của response
về tới client, và ở mỗi chặng ghi lại thứ có thể hỏng ở đó, bài nào giải thích nó, và một con số
đã đo.

Không có con số mới trong bài này. Mỗi số là số của bài được trỏ tới, và bài đó ghi file output
gốc trong `bench/`. Máy đo chính là laptop WSL2, nên với số thời gian thì chỉ tỉ số là đáng tin;
số nào đo trên Linux thuần (CachyOS) thì ghi rõ.

## Hành trình của một request

```http
GET /api/orders/42 HTTP/1.1
Host: shop.example
```

```text
 ① accept(): kernel trao một socket                                    bài 0, 11
    giành slot connection TRƯỚC khi Accept; đủ trần per-IP thì đóng ngay
    proxy L7 không chuyển tiếp byte: 2 socket, 4 lần parse
      ⚠ overhead L7 p50 3.39× (WSL2, dial mới mỗi request) so với gọi thẳng bài 0
      ⚠ mỗi connection rỗi tốn RAM: 28 → 8-9 KiB khi trả bufio về pool bài 11

 ② (HTTPS) TLS handshake, có deadline riêng                           bài 15
    SNI chọn cert và GẮN connection với một vhost
      ⚠ TLS 1.3 thêm 1 RTT; resumption ở 1.3 vẫn +1 RTT (không có 0-RTT)
      ⚠ ClientHello nhỏ giọt: không có deadline riêng thì giữ tới IdleTimeout

 ③ Trần connection                                                      bài 10
    500 connection Slowloris không giết được proxy viết bằng Go
      ⚠ thứ giết nó là TRẦN connection: MaxConns 256, tắt HeaderTimeout ⇒ probe hỏng 100%

 ④ Đọc request head, HeaderTimeout tính từ byte đầu                    bài 10
      ⚠ probe vẫn sống 100% kể cả khi tắt HeaderTimeout: slot mới là thứ hữu hạn

 ⑤ Parse + phòng tuyến smuggling                                         bài 5, 6
    CL + TE cùng lúc, bare LF, chunk-size lạ ⇒ 400
      ⚠ 46 cặp (ca × origin) mà nginx 1.25 / h2o NHẬN thứ EdgeGate từ chối
      ⚠ fuzz phẳng 390 s bỏ sót 2 lệch thật; fuzz có cấu trúc thấy sau 0.10 s
    (h2c) frame → stream → request                                      bài 13, 14
      ⚠ Rapid Reset: 100 vs 5000 handler; h2spec vẫn 139/145 khi tắt phòng thủ

 ⑥ Rate limit → shed                                                    bài 9
    hàng đợi có trần + queue timeout, đầy thì 503 ngay
      ⚠ 2× tải mà không shed: p99 11.6 s. Có shed: 33 ms, 55% nhận 503

 ⑦ Chọn vhost, rồi chọn backend                                          bài 15, 8
      ⚠ TLS: SNI ≠ Host ⇒ 421, không thì domain fronting
      ⚠ least-conn p99 0.98× round-robin; P2C cho node vừa hồi phục 0% tải

 ⑧ Lấy connection từ pool, probe FIN trước khi dùng                     bài 3, 4, 15
      ⚠ reuse chỉ nhanh hơn dial 2.00× ở RTT 20 ms (36.69× ở RTT 0)
      ⚠ không pool trên Linux thuần: 101 lỗi EADDRNOTAVAIL (WSL2: 0 lỗi)
      ⚠ upstream TLS: pool tiết kiệm 2.03 RTT, gấp đôi plaintext

 ⑨ Viết lại request, ghi lên upstream                                     bài 2, 12
    serialize lại, gom head và body vào ít lần ghi
      ⚠ hai Write nhỏ + Nagle + delayed ACK: sàn 44 ms (WSL2) / 41 ms (Linux)

 ⑩ Upstream đóng connection keep-alive đúng lúc đó? Retry?             bài 7
    chỉ khi: connection dùng lại, 0 byte response, method idempotent, body đã đệm
      ⚠ PUT replay 50/50, POST trả 502
      ⚠ budget 10%: nhân tải 1.12× nhưng 25.3% nhận 502; retry mù 1.49×

 ⑪ Đọc response head từ upstream, parse lần nữa                           bài 0, 12
      ⚠ httputil.ReverseProxy: CPU/req 3.2-3.7× EdgeGate, 0.68 ctxsw/req

 ⑫ Chép body upstream → client                                            bài 12
    stream trong cùng một goroutine; body lớn đi bằng splice (diary/phase9.md)

 ⑬ Ghi response về client                                                  bài 2, 13
      ⚠ (h2) mất gói 2%: p50 2.65× tệ hơn h1, vì mọi stream chung một TCP

 ⑭ Trả connection upstream về pool, chờ request kế trong IdleTimeout     bài 3, 4, 7
      ⚠ có pool: TIME_WAIT phía proxy +1964 → +0
      ⚠ connection rỗi có thể bị upstream đóng bất cứ lúc nào ⇒ quay lại ⑩
```

Toàn bộ series là mười bốn chặng này. Và mọi con số ở trên chỉ đáng tin nếu bạn đo nó đúng cách:
closed-loop không bao giờ thấy hàng đợi, nên cùng một hệ thống quá tải cho p99 lệch **370×**
(WSL2) / **276–476×** (Linux thuần) giữa closed-loop và open-loop ([bài 1](01-benchmark-noi-doi.md)).
Chặng ⑥ của bài 9 chỉ nhìn thấy được bằng open-loop.

## Hai câu hỏi dùng được cho mọi proxy

Khi gặp một proxy mới (nginx, Envoy, HAProxy, Caddy, một service mesh), hỏi hai câu này trước khi
chạy benchmark nào.

**1. Ranh giới của một request do ai quyết?**

Proxy L7 đứng giữa hai bên nói cùng một giao thức, và mỗi bên tự đọc ranh giới. Chỗ nào hai bên có
thể đọc khác nhau là chỗ có lỗ.

| Ranh giới | Hai lời khai | Lệch thì | Bài |
|---|---|---|---|
| Hết request h1 | `Content-Length` vs `Transfer-Encoding` | request lậu thứ hai (smuggling) | [5](05-smuggling.md) |
| Hết request khi h2 → h1 | `END_STREAM` vs `content-length` | H2.CL: upstream thấy `/smuggled` | [14](14-h2spec.md) |
| Request này cho ai | SNI vs `Host` | domain fronting, phải 421 | [15](15-tls.md) |
| Request này có được gửi lại | method + đã nhận byte nào chưa | POST chạy hai lần | [7](07-retry.md) |

**2. Trần đặt lên cái gì?**

Mọi cuộc tấn công và mọi lần quá tải trong series đều thắng bằng cách tiêu một tài nguyên mà trần
không đếm.

| Trần đếm | Thứ lọt qua | Bài |
|---|---|---|
| tổng connection | một IP giữ hết slot (Slowloris) | [10](10-slowloris.md) |
| stream đang mở | stream đã huỷ mà handler vẫn chạy (Rapid Reset: 5000 vs 100) | [14](14-h2spec.md) |
| (không có trần hàng đợi) | p99 11.6 s thay vì 503 sau 33 ms | [9](09-shed.md) |
| số lần retry mỗi request | tổng tải retry lên upstream (1.49× vs 1.12×) | [7](07-retry.md) |
| (không pool: mỗi request một port) | Linux thuần: 101 lỗi `EADDRNOTAVAIL` | [4](04-het-port.md) |

## Checklist cho dev

**Đo**
- [ ] Load test open-loop, latency tính từ giờ hẹn gửi, không từ giờ gửi thật (bài 1)
- [ ] Độ trễ là một hằng số tròn (40 ms, 200 ms) thì nghi Nagle và delayed ACK trước (bài 2)
- [ ] Báo lợi ích của pool bằng RTT tiết kiệm, không bằng tỉ số đo ở RTT 0 (bài 3)
- [ ] Ghi rõ môi trường đo; kết luận đúng trên WSL2 có thể sai trên Linux (bài 4)

**Phòng thủ**
- [ ] Proxy chặt hơn origin nó che chắn: từ chối CL + TE, bare LF, chunk-size lạ (bài 5)
- [ ] Mỗi phòng tuyến có một bản build tắt nó, và test tấn công phải đỏ trên bản đó (bài 5, 14)
- [ ] Trần theo IP và theo request, không chỉ trần tổng connection (bài 10)
- [ ] h2: slot stream giữ tới khi handler thoát, trần tốc độ RST, trần header block (bài 14)
- [ ] TLS: `Host` phải khớp SNI; handshake có deadline riêng (bài 15)

**Quá tải và lỗi**
- [ ] Hàng đợi có trần và có timeout; đầy thì 503 nhanh (bài 9)
- [ ] Retry chỉ khi idempotent và upstream chưa trả byte nào; có budget, chọn con số có chủ đích (bài 7)
- [ ] LB: kiểm xem node chậm nhận bao nhiêu % tải, và node vừa hồi phục có được nhận lại không (bài 8)

**Hiệu năng**
- [ ] Keep-alive cả hai phía; với upstream TLS thì gấp đôi quan trọng (bài 3, 15)
- [ ] Đếm context switch khi profile, không chỉ đếm CPU (bài 12)
- [ ] Đo cái mình định claim: giảm cấp phát 43× không có nghĩa là nhanh hơn 43× (bài 11)
- [ ] HTTP/2 trên mạng mất gói dời giá từ vài request sang mọi request (bài 13)

## Đi tiếp từ đây

- **Tự chạy lại mọi thí nghiệm.** Mỗi bài có lệnh `make` hoặc `go run ./cmd/<lab>` sinh ra số của
  nó. Chạy `make envcap` trước để chụp môi trường máy bạn. Số tuyệt đối sẽ khác; tỉ số thì nên giống.
  Nếu không giống, đó là một câu hỏi đáng đào tiếp, như [bài 4](04-het-port.md) đã đào.
- **Đọc code EdgeGate theo đường đi ở trên:** `internal/proxy/proxy.go` (`Serve`, `serveConn`) →
  `internal/httpx` (parse, `defense.go`) → `internal/limit` → `internal/lb` →
  `internal/proxy/forward.go` (`roundTrip`, `canRetry`) → `internal/h2` → `internal/tlsx`. Nhật ký
  từng phase ở `diary/` ghi cả những giả thuyết sai trên đường đi.
- **Đọc thêm**, theo thứ tự:
  - RFC 9112 (HTTP/1.1 message syntax): §6 về ranh giới body là nửa series này.
  - RFC 9113 §10.5 (HTTP/2 DoS) và RFC 8446 §8 (0-RTT và replay).
  - Các bài của James Kettle về request smuggling (PortSwigger Research): đọc cùng bài 5.
- **Một hướng khác hẳn:** mọi thứ ở đây chạy trên TCP. HTTP/3 chạy trên QUIC, nên mất một gói chỉ
  chặn một stream. Đó là câu trả lời cho [bài 13](13-http2.md), và cũng là một proxy hoàn toàn khác.

---

Cảm ơn bạn đã đọc tới đây. Nếu một con số không khớp với máy của bạn, hoặc có chỗ giải thích sai,
hãy mở issue. Series này theo cùng luật với nhật ký của EdgeGate: **cái sai được ghi lại, không bị
xoá đi**. Bài 4 tồn tại vì đúng luật đó.

**Về mục lục:** [README](README.md)
