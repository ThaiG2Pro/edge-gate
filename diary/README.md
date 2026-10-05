# Nhật ký EdgeGate — tổng quan

Mục lục 11 cuốn nhật ký phase + trạng thái hiện tại. Mỗi `phaseN.md` là kết luận (giả thuyết nào
sai, số đo nói gì); `phaseN-log.md` là nhật ký thao tác theo giờ. Cập nhật 2026-10-05.

## Trạng thái một dòng

**Mười phase xong. 66/66 nợ đã trả, 0 còn mở.** Các nợ `📏` cuối cùng được đo trên Linux thuần
(CachyOS, 16 thread) ngày 2026-10-04.

- Sổ nợ: [`../docs/debts.md`](../docs/debts.md)
- Tái hiện số đo thật: [`../docs/REPRODUCE-LINUX.md`](../docs/REPRODUCE-LINUX.md)
- Roadmap + bảng tổng quan: [`../ROADMAP.md`](../ROADMAP.md)

## Mục lục phase

| Phase | Kết luận ngắn | Nhật ký |
|---|---|---|
| 0 Nền tảng mạng | 5/8 giả thuyết sai; dial/reuse là 1 RTT/conn, không phải tỉ số; port exhaustion = thông lượng tụt | [phase0](phase0.md) · [log](phase0-log.md) |
| 1 Framing | 4/7 sai; phản chứng trần frame đỏ (4.29 tỉ byte); fuzz 1.58M execs sạch | [phase1](phase1.md) · [log](phase1-log.md) |
| 2 HTTP/1.1 engine | diff-fuzz 0 lệch nhưng bảng tay tìm 2 lệch thật; header bomb chặn sau 4096 B | [phase2](phase2.md) · [log](phase2-log.md) |
| 3 Vertical slice | 1/6 sai (G4 sai hai lần); Nagle là sàn 44 ms; overhead L7 3.39x | [phase3](phase3.md) · [log](phase3-log.md) |
| 4 RFC & smuggling | 3/7 sai; `net/http` chỉ cùng từ chối 46 %; oracle nginx+h2o xác nhận strict đúng (P4-6) | [phase4](phase4.md) · [log](phase4-log.md) |
| 5 Upstream pool | RTT 0: 5.34x; RTT 20 ms: 1.37x / 0.87 RTT (tỉ số ngược ROADMAP); probe MSG_PEEK 50/50 FIN | [phase5](phase5.md) · [log](phase5-log.md) |
| 6 Load balancing | 4/7 sai; p99 chỉ giảm khi node chậm < 1 % tải; P2C thua khi node hồi phục (tau 30 s cứu) | [phase6](phase6.md) · [log](phase6-log.md) |
| 7 Resiliency | 6/9 sai một vế; Slowloris không giết Go, trần conn mới giết; shed p99 352x; drain lười 0 mất | [phase7](phase7.md) · [log](phase7-log.md) |
| 8 TLS + SNI | 2/8 sai; 421 chặn domain fronting; resumption cắt 0 RTT ở TLS 1.3; pool TLS tiết kiệm 2 RTT | [phase8](phase8.md) · [log](phase8-log.md) |
| 9 Performance & epoll | 3/8 sai; pool ns/op 2.4x (giá là tần suất GC); conn rỗi 8-9 KiB; splice body lấy lại giá L7 | [phase9](phase9.md) · [log](phase9-log.md) |
| 10 HTTP/2 h2c | h2spec 145/145 qua TLS/h2c_only, 144/145 chung port (cố ý); HOL TCP h2 2.7x tệ ở trung vị | [phase10](phase10.md) · [log](phase10-log.md) |

## Phiên trả nợ 2026-10-02 / 10-03

Chín nợ trả + công cụ đo:

| Nợ | Nội dung |
|---|---|
| P2-5 | FuzzReadRequest đỏ 1/3 là `%q` chép cả trường bẩn vào Reason (không phải sync.Pool); `clip()` 32 byte |
| P4-3 | Sinh `X-Forwarded-Proto/Host/Port`; clientIP = phần tử phải nhất XFF ngoài trusted; Forwarded không sinh |
| P10-5 | ALPN `h2` trên TLS (cipher TLS 1.2 chỉ ECDHE+AEAD); `make h2spec-tls` 145/145 |
| P10-6 | `h2c_only`: listener plaintext chỉ h2c; `make h2spec` config h2-only 145/145 |
| P3-2 | Tunnel 101 (WebSocket): đặt lại Upgrade cho upstream, chép hai chiều; fixture `/ws` |
| P8-3 | Health probe bắt tay TLS khi `UpstreamTLS` (`lb.HealthConfig.Dial` hook) |
| P7-3 | Chốt test trần eject + half-open (`TestBreakerUnderEjectCap`) |
| P9-7 | Splice body mặc định BẬT; chaoslab body 256 KiB PASS; cờ `-nosplice` |
| P6-5 | Passive outlier cắt cửa sổ dial lỗi: active-only 116-122 fails vs outlier-on 8 (đo bằng test, valid WSL2) |
| P4-6 | Oracle thứ hai/ba nginx+h2o (`make oracle-ext`): bare LF + Connection:Host cả hai origin nhận ⇒ strict đúng |
| P-env-2 | Công cụ xong (`make pinlab`, kiểm lag generator); số chốt cần Linux thuần |

Công cụ tái hiện đã dựng: `scripts/linux-measure.sh`, cờ `netlab -dialers`, `chaoslab -heal-weight`,
`chaoslab -scenario overload`.

## Phiên trả nợ 2026-10-04 (Linux thuần)

13 nợ đo trên CachyOS + các nợ code cuối (P5-4b replay body ≤ 64 KiB, P7-2b tarpit per-IP,
P9-2 splice upload, P0-3/P0-4/P0-6, P9-5). Chi tiết từng món trong [`../docs/debts.md`](../docs/debts.md).

## Phiên polish 2026-10-05

- `TestLBKillRevive/active-only` đỏ 3/3 sau 37cd9d9: kịch bản đổi sang 32 client ⇒ 25 % request
  trúng b3 lúc chết cần retry D9, vượt retry budget mặc định 10 % ⇒ 502 đúng thiết kế
  (502 = `RetryDenied` = 1160). Test nới budget vì nó đo health, không đo budget; ngưỡng
  cập nhật theo tải mới. Xanh 8/8 lượt `-count=8`, full suite xanh dưới `-race`.
- Thêm LICENSE (MIT), CI GitHub Actions (gofmt, vet, build, test -race), README tiếng Anh.
