# EdgeGate

L7 reverse proxy + load balancer viết từ socket trần bằng Go. **Không dùng `net/http`** cho
đường dữ liệu — chỉ `net`.

Đây không phải một project để dùng. Đây là một **bản ghi thí nghiệm**: mục tiêu là hiểu
invariant và failure mode của tầng edge, và chứng minh sự hiểu đó bằng số đo chạy lại được.

- Lộ trình + kiến trúc: [`ROADMAP.md`](./ROADMAP.md)
- Luật ghi nhật ký: [`skills/diary/SKILL.md`](./skills/diary/SKILL.md)
- Nhật ký từng phase: [`diary/`](./diary/)
- Sổ nợ kỹ thuật: [`docs/debts.md`](./docs/debts.md)

## Ba nguyên tắc

1. **Mỗi phase kết thúc bằng một load-test, một fuzz, hoặc một bài phản chứng bắt buộc đỏ** —
   không phải bằng "code chạy được".
2. **Vertical slice trước, đào sâu sau.** Phase 3 phải `curl` được xuyên proxy. Không ai được
   viết parser hoàn hảo trong 3 tháng mà chưa proxy nổi một request.
3. **Mọi con số phải kèm lệnh sinh ra nó và output thật.** Số nào không có trong `diary/` thì
   không được viết vào CV.

## Bắt đầu

```bash
ulimit -n 65536        # BẮT BUỘC, và ghi con số này vào diary
make envcap            # chụp môi trường máy này -> bench/env-<host>.txt. Làm TRƯỚC TIÊN
make phase0            # chạy trọn phase 0, mỗi thí nghiệm một file trong bench/
```

Sang máy khác thì hai lệnh trên là toàn bộ những gì cần chạy lại — file output tự mang theo
khối môi trường của nó nên hai máy so được cạnh nhau. Muốn có RTT **thật** thay vì `tc netem`:

```bash
make netlab-server                      # máy A
make netlab-client ADDR=<ip-máy-A>:9000 # máy B
```

Trước khi tin bất kỳ con số p99 nào trong repo này, đọc mục **"Luật riêng của đo mạng"** trong
[`skills/diary/SKILL.md`](./skills/diary/SKILL.md). Ba cái bẫy — coordinated omission, loopback
không có RTT, và generator giành CPU với proxy — làm sai lệch phần lớn benchmark proxy trên
mạng, và hai trong ba cái được dựng sẵn thành bẫy cho phase 0.

## Trạng thái

| Phase | | Trạng thái |
|---|---|---|
| 0 | Nền tảng vật lý mạng | ✅ [phase0.md](./diary/phase0.md) — **5/8 giả thuyết sai** |
| 1 | Framing | ✅ [phase1.md](./diary/phase1.md) — **4/7 giả thuyết sai** |
| 2 | HTTP/1.1 engine | ✅ [phase2.md](./diary/phase2.md) — diff-fuzz 0 lệch, bảng tay tìm 2 lệch thật |
| 3 | Vertical slice | ✅ [phase3.md](./diary/phase3.md) — **1/6 giả thuyết sai** (G4 sai hai lần), overhead L7 3.39x |
| 4 | RFC compliance & smuggling | ✅ [phase4.md](./diary/phase4.md) — **3/7 giả thuyết sai**; 61 ca, phản chứng đỏ 20/52, Go làm oracle chỉ cùng từ chối 46 % |
| 5 | Upstream connection pool | ✅ [`diary/phase5.md`](diary/phase5.md) — pool LIFO + probe `MSG_PEEK` + retry một lần; phản chứng connection bẩn đỏ 20/20; RTT 0: 5.34x / 0.96 ms, RTT 20 ms: 1.37x / 17.9 ms — tỉ số ngược ROADMAP, khoản tiết kiệm mới đúng |
| 6 | Load balancing + health | ✅ [`diary/phase6.md`](diary/phase6.md) — **4/7 giả thuyết sai**; p99 chỉ giảm khi node chậm < 1 % tải (least-conn 4.3 % ⇒ 0.98x, P2C 0.6 % ⇒ 3.4x); P2C thua khi node **hồi phục** (tau 30 s: 0.0 % vs least-conn 25 %); phản chứng EWMA decay theo request đỏ 0/200 |
| 7 | Resiliency | ✅ [`diary/phase7.md`](diary/phase7.md) — **6/9 giả thuyết sai một vế**; Slowloris không giết Go, trần connection mới giết; shed p99 352x; drain lười 0 mất; chaoslab 4/4 invariant |
| 8 | TLS + SNI | ✅ [`diary/phase8.md`](diary/phase8.md) — **2/8 giả thuyết sai một vế**; 421 chặn domain fronting; resumption cắt 0 RTT ở TLS 1.3; pool upstream TLS tiết kiệm 2 RTT; reload 0 lỗi |
| 9 | Performance & epoll | ✅ [`diary/phase9.md`](diary/phase9.md) — **3/8 giả thuyết sai**; pool: GC chứ không malloc (ns/op 2.4x); conn rỗi 28 → 8-9 KiB; giá L7 ở body = mất splice, lấy lại bằng splice body; epoll 55x ít RAM, rps ngang; EdgeGate ≈ nginx, ReverseProxy 3.1x chậm hơn (WSL2) |
| 10 | HTTP/2 h2c (tùy chọn) | ⬜ |

## Ba con số của phase 0

Đo trên WSL2 / i5-1235U / 6 core, 2026-09-03. Chi tiết và lệnh: [`diary/phase0.md`](./diary/phase0.md).

- **2.00x** — tỉ số `dial`/`reuse` ở RTT 20ms, *và* ở RTT 40ms. Trùng khít ở cả hai điểm vì đó
  là hằng số tiệm cận (dial trả 2 RTT, reuse trả 1), nên **tỉ số không đo lợi ích của connection
  pool**. Ở RTT ~0 tỉ số là 36.69x — báo cáo theo tỉ số sẽ kết luận ngược hoàn toàn sự thật.
  Đơn vị đúng: **1 RTT phí cho mỗi connection dựng mới** (0.33ms → 20.47ms → 40.47ms).
- **1787x** — chênh lệch p99 giữa closed-loop và open-loop trên **cùng một hệ thống quá tải**.
  Chọn sai dụng cụ đo thì p99 báo về sai 3 bậc độ lớn.
- **44.03ms** — spike Nagle + delayed ACK, và nó là *hằng số* (p50 44.03, p99 47.99), không
  phải hiện tượng ở đuôi. Lý do mọi socket phải `SetNoDelay(true)` tường minh.
- **1502 → 455 conn/s, 0 lỗi, CPU 29.5%** — trần ephemeral port không báo bằng exception, nó
  báo bằng thông lượng tụt dần. Đây là lý do thật của connection pool.

Và một con số về chính bộ đo: `dial p50` đo được **861.7µs** rồi **341.3µs** — cùng lệnh, cùng
máy, cách nhau 30 phút. Số tuyệt đối đổi 2.5x; tỉ số thì sống sót. Đó là lý do nguyên tắc 3.

Ba cái bẫy trong `SKILL.md` đều nổ thật: `bufio` che chi phí syscall *và* che spike 44ms;
`tcp_tw_reuse=2` che port exhaustion trên loopback; loopback che RTT.

## Đã có gì (trước khi phase 0 chạy)

`internal/httpx/`: `Header` (`map[string][]string`, canonical hoá, `StripHopByHop`), `Limits`,
`ProtoError`. Thuộc phase 2, **chưa có test nào** ⇒ chưa phải bằng chứng của gì cả.
Xem nợ `P-code-1` trong [`docs/debts.md`](./docs/debts.md).
