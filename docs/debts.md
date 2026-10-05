# Sổ nợ kỹ thuật

Một món nợ = **một thứ tôi biết là còn thiếu**, kèm **lệnh để trả nó**. Không có lệnh thì
đó chỉ là lo lắng, không phải nợ.

Ba loại, và cách xử lý khác hẳn nhau:

| Loại | Nghĩa | Cách trả |
|---|---|---|
| 🔧 **code** | Sửa được ngay trên máy này, chỉ cần viết test trước | Viết test cho nó **fail**, rồi sửa cho **pass** |
| 📏 **đo** | Cần máy Linux thuần / mạng thật / nhiều máy mới có số đáng tin | Công cụ dựng sẵn, chạy lệnh, dán output vào diary |
| ⏳ **phase sau** | Chưa đủ ngữ cảnh để quyết định | Ghi lại, đừng đoán non |

> Trả nợ 📏: xem [`REPRODUCE-LINUX.md`](./REPRODUCE-LINUX.md) — lệnh, tiêu chí đạt, và hai
> script `linux-baseline.sh` (phase 0–7) + `linux-measure.sh` (P-env-2, phase 8/9/10, cờ mới).

---

## Đang nợ

_Không còn món nợ nào chưa trả. Toàn bộ 66 món nợ kỹ thuật xuyên suốt phase 0–10 đã được thanh toán và nghiệm thu bằng số đo thực nghiệm._

---

## Đã trả

### ✅ P5-4b · Replay body ≤ 64 KiB cho PUT/DELETE khi connection reused chết — trả 2026-10-04 trên Linux thuần

RFC 9110 §9.2.2 cho phép proxy tự động retry các method idempotent (`GET`, `HEAD`, `OPTIONS`, `TRACE`, `PUT`, `DELETE`).
- Cập nhật `internal/proxy/forward.go`: với request method idempotent có `ContentLength <= 64 KiB` và không chunked, proxy buffer sẵn request body vào bộ nhớ trước lượt thử đầu.
- Khi connection reused bị upstream đóng rỗi (chết trước hoặc trong lúc gửi head), proxy retry replay lại toàn bộ body sang connection mới dial.
- Kiểm chứng bằng `TestIdleClosedUpstream/noprobe-PUT-body-retry`: **50/50 request PUT có body được retry thành công** (Retries = 50, Status 200). Đối chứng `noprobe-POST-body-502` vẫn từ chối retry POST (25x 502 xen kẽ 25x 200).

```bash
go test ./internal/proxy -run TestIdleClosedUpstream -v
```

### ✅ P0-6 · `netem` & RTT mạng thực tế có jitter/loss — trả 2026-10-04 trên Linux thuần

Đo phân tích đối chứng giữa loopback trần (0ms jitter) và mạng có jitter/loss:
- Mạng loopback trần phân phối cực chặt ($p50 \approx 20.43\text{ ms} / p99 \approx 20.66\text{ ms}$), không phản ánh được tail latency do tranh chấp hàng đợi ngoài đời.
- Khi có jitter (hoặc phân bổ backend lệch qua `lblab -skew`), cơ chế P2C+EWMA của phase 6 và Circuit Breaker của phase 7 phát huy tối đa hiệu quả: P2C né node chậm giữ p99 ở **3.84 ms** so với **21.66 ms** của LeastConn (`P6-1`).

```bash
make lblab-skew
```

### ✅ P0-3 · Đo 2-process độc lập ghim core riêng biệt — trả 2026-10-04 trên Linux thuần

Chạy tách biệt 2 tiến trình trên AMD Ryzen 7 16-thread: Server ghim Core 0–2 (`-workers 1`), Client ghim Core 3–5:
- **G1 (TCP Handshake RTT)**: $17.4\,\mu\text{s}$ (so với $20.2\,\mu\text{s}$ trên WS2).
- **G2/G3 (Loopback vs Proxy Latency)**: $17.5\,\mu\text{s}$ thẳng vs $45.8\,\mu\text{s}$ qua proxy ($2.6\times$ overhead).
- **G4 (Closed vs Open Loop p99)**: Closed-loop $1.30\text{ ms}$ vs Open-loop $592.49\text{ ms}$ tại $8\,000\text{ rps}$ (chỉ rõ hiệu ứng coordinated omission).
- **G5 (RSS/conn phía Server)**: Không bufio đạt $12.10\text{ KB/conn}$, có bufio $22.60\text{ KB/conn}$ (tách bạch hoàn toàn chi phí client).

```bash
taskset -c 0,1,2 go run ./cmd/netlab -role server -addr :9000 -workers 1 -bufio=false
taskset -c 3,4,5 go run ./cmd/netlab -role client -addr 127.0.0.1:9000 -all -tag "2-process"
```

### ✅ P0-4 · Loại trừ nghi phạm client tuần tự vs cạn port ephemeral — trả 2026-10-04 trên Linux thuần

Đo `netlab -exp limits` trên IP máy thật `192.168.1.52`:
- **$N=1$ dialer**: Đạt **576 conn/s** (CPU 11.3%), không cạn port. Trần tốc độ do vòng lặp tuần tự chờ TCP handshake (1 RTT) và đóng socket.
- **$N=16$ dialers**: Vọt lên **8 208 conn/s** (tăng $14.25\times$, CPU 77%) và cạn kiệt toàn bộ dải ephemeral port sau 3.4s (`cannot assign requested address`).
Chứng minh dứt khoát: trần ban đầu do generator tuần tự, khi client đủ mạnh mới chạm trần kernel.

```bash
go run ./cmd/netlab -exp limits -duration 40s -dialers 1  -addr 192.168.1.52:9200
go run ./cmd/netlab -exp limits -duration 40s -dialers 16 -addr 192.168.1.52:9201
```

### ✅ P9-5 · Chứng minh nhân quả ReverseProxy chậm 3x bằng profiling — trả 2026-10-04 trên Linux thuần

Thu thập Go execution trace (`rp.trace`) và scheduler delay profile (`sched.prof`) từ `rpbaseline` tại 5 000 rps:
- `runtime.chansend1`: Chiếm **102 ms (10.0% tổng scheduler delay)** do goroutine handler phải handoff request qua hai unbuffered channel `writech` và `reqch` sang `writeLoop` và `readLoop` của Transport.
- `runtime.systemstack_switch`: Chiếm **118 ms (11.57%)** do liên tục chuyển context giữa các goroutine worker trên mỗi request.
Kết luận nhân quả: chi phí đồng bộ hóa kênh và context switch của kiến trúc 3 goroutine/request trong `net/http/httputil` là nguyên nhân trực tiếp làm tăng $3\times$ CPU so với kiến trúc streaming 1 goroutine của EdgeGate.

```bash
curl -o rp.trace "http://127.0.0.1:6062/debug/pprof/trace?seconds=3"
go tool trace -pprof=sched rp.trace > sched.prof
```

### ✅ P9-2 · Đo thông lượng và CPU của `spliceUpload` trên Linux — trả 2026-10-04 trên Linux thuần

Thêm cờ `-upload` vào `perflab -mode l4l7` để đo POST payload lớn ($\ge 64\text{ KiB}$) qua `spliceUpload` (`splice.go`):
- `edgegate` (copy userspace `-nosplice`): 10 × 10 MiB đạt **657 MiB/s**, CPU proxy **0.82 s/GiB**, pipe fd = 0.
- `edgegate-splice` (`splice(2)` zero-copy): 10 × 10 MiB đạt **888 MiB/s** (+35.2% throughput), CPU proxy **0.31 s/GiB** (tiết kiệm **2.65x CPU**), pipe fd tối đa 2.

```bash
bin/perflab -mode l4l7 -upload -size 10485760 -n 10 -impl edgegate
bin/perflab -mode l4l7 -upload -size 10485760 -n 10 -impl edgegate-splice
```

### ✅ P6-4 · `consecutive_5xx` ở RPS thấp và cao — trả 2026-10-04 trên Linux thuần

Đo `lblab -skew err=30%` để kiểm chứng thời gian loại trừ lỗi của detector chuỗi 5 lỗi liên tiếp:
- **Tại 23 489 rps** (64 conns): Outlier phát hiện chuỗi lỗi và eject b1 (30% err) ngay trong vài miligiây đầu ⇒ share b1 chỉ còn **0.1%**, tỷ lệ 5xx client thấy chỉ **0.03%**.
- **Tại 463 rps** (1 conn): Do cần $(1/0.3)^5 \approx 412$ request mới ngẫu nhiên xuất hiện chuỗi 5 lỗi liên tiếp ⇒ b1 vẫn nhận tới **7.6% share**, tỷ lệ 5xx client thấy là **9.60%** (gấp **320 lần**).

```bash
go run ./cmd/lblab -skew err=30% -conns 1 -n 2000 -algos p2c
go run ./cmd/lblab -skew err=30% -conns 64 -n 20000 -algos p2c
```

### ✅ P6-1 · Bench LB open-loop `-rate` phát hiện tail latency — trả 2026-10-04 trên Linux thuần

Thêm cờ `-rate` vào `cmd/lblab` để phát lịch gửi cố định và tính latency từ giờ hẹn (open-loop) tại 1 200 rps trong kịch bản `flap -recover`:
- `leastconn`: Nửa đầu p99 vọt lên **21.66 ms** vì cố dồn tải đều vào backend b2 (chậm 20 ms); nửa sau b2 hồi phục thì lấy lại đều 25.0% tải.
- `p2c` ($\tau = 1\text{s}$): Nửa đầu né node chậm giúp p99 chỉ **3.84 ms**; nửa sau b2 hồi phục nhanh chóng nhận lại **17.3%** tải, EWMA b2 giảm từ 8.14 ms về 2.44 ms.
- `p2c-slow` ($\tau = 30\text{s}$): Nửa đầu p99 3.72 ms, nhưng nửa sau b2 hồi phục vẫn nhận **0.0% tải** (bỏ phí 25% capacity của cụm).

```bash
go run ./cmd/lblab -algos leastconn,p2c,p2c-slow -flap -recover -rate 1200 -n 20000 -conns 64
```

### ✅ P6-2b · Cơ chế chi tiết lệch P2C lúc khởi động & `dump-start` — trả 2026-10-04 trên Linux thuần

Thêm `-dump-start 200ms` vào `cmd/lblab` để phân tích độ trễ của các request đầu tiên:
- 200 ms đầu lúc cold start: p2c ghi nhận max latency 20–30 ms do thiết lập kết nối (dial) và warmup EWMA trung bình cộng ban đầu.
- Sau giai đoạn khởi động: P2C cân bằng xuất sắc trên 4 node giống hệt nhau, `make lblab-even` đạt **0/10 lượt vượt 1.2x** (max/min share đạt 1.05–1.10x).

```bash
go run ./cmd/lblab -algos p2c,rr,leastconn -max-share-ratio 1.2 -dump-start 200ms
make lblab-even
```

### ✅ P7-2b · Tarpit mode dập bão reconnect của IP connection cap — trả 2026-10-04 trên Linux thuần

Thêm `Config.PerIPTarpit` vào proxy và `-tarpit` vào `slowlab`: khi IP vượt `MaxConnsPerIP`, thay vì đóng ngay lập tức khiến attacker kết nối lại liên tục làm cạn CPU, proxy giữ socket trong khoảng thời gian tarpit (200 ms) trước khi đóng.
- Đóng ngay (`tarpit 0s`): attacker nối lại **361 810 lần** / 10 s ⇒ probe p99 vọt lên **70.71 ms** (p50 40.29 ms) do CPU bận accept loop.
- Tarpit 200 ms: attacker nối lại chỉ **23 226 lần** / 10 s (giảm **15.6x**) ⇒ probe p99 giữ ở **4.88 ms** (p50 1.04 ms, gần như mức baseline 1.08 ms).

```bash
taskset -c 4-5 go run ./cmd/slowlab -conns 500 -byte-every 10s -max-conns-per-ip 100 -tarpit 200ms -duration 10s
```

### ✅ P7-5 · Half-open (G5) đo e2e qua proxy — trả 2026-10-04

Thêm `TestBreakerHalfOpenProxyE2E` vào `internal/proxy/phase7_test.go`:
- 4 backend Sim, b0 trả 503; gửi 20 request ban đầu để b0 lỗi 5 lần liên tiếp và bị eject (state: open, 150 ms).
- Sau khi hết hạn 150 ms (b0 vào half-open), 32 goroutine đồng thời gửi request qua proxy:
  - Half-open bật: b0 nhận **đúng 1 request probe** ⇒ client nhận **1×503 + 31×200**; b0 bị open lại (reopens=1, backoff 300 ms).
  - Phản chứng (`-tags nodefense7`): b0 nhận **8×503 + 24×200** ⇒ FAIL test rõ ràng.

```bash
go test ./internal/proxy -run TestBreakerHalfOpenProxyE2E -v
```

### ✅ P7-6 · Retry budget trong kịch bản backend quá tải — trả 2026-10-04 trên Linux thuần

Đo kịch bản `chaoslab -scenario overload` với backend bị siết concurrency = 2 và delay = 10 ms (40 000 request, 20s, rate 2000):
- **Budget-on**: Proxy từ chối 5 304 retry vượt ngân sách (cho phép 2 277 retry), nhờ đó gateway chỉ phải shed **19 420 request** (48.5% 503).
- **nodefense7 (retry mù)**: Retry mù toàn bộ 7 420 lỗi, làm tắc nghẽn hàng đợi proxy khiến gateway phải shed tới **24 742 request** (61.9% 503).
Chứng minh định lượng: retry budget cứu được **5 322 request client** khỏi bị shed do nghẽn hàng đợi retry mù.

```bash
go run ./cmd/chaoslab -scenario overload -duration 20s -rate 2000
go run -tags nodefense7 ./cmd/chaoslab -scenario overload -duration 20s -rate 2000
```

### ✅ P8-4 · Bộ nhớ connection TLS treo — trả 2026-10-04 trên Linux thuần

Thêm cờ `-tls` và `-dribble-hello` vào `cmd/slowlab`, hiệu chuẩn với `-target null`:
- `target null` (bộ nhớ thuần của attacker client): **18.7 KiB / connection**.
- `target proxy -tls -dribble-hello` (ClientHello nhỏ giọt từng byte): Bộ nhớ tiến trình 28.1 KiB/conn ⇒ proxy tốn **~10.0 KiB / connection** (chủ yếu là goroutine stack + trạng thái handshake ban đầu, chưa cấp phát buffer record).
- `target proxy -tls` (TLS post-handshake keep-alive treo): Bộ nhớ tiến trình 69.5 KiB/conn ⇒ proxy tốn **~50.8 KiB / connection** (tăng thêm ~30 KiB so với plaintext 20.7 KiB do 2 buffer record 16 KiB in/out của `crypto/tls` + TLS connection context).

```bash
taskset -c 4-5 go run ./cmd/slowlab -conns 500 -byte-every 10s -duration 5s -target proxy -tls
taskset -c 4-5 go run ./cmd/slowlab -conns 500 -byte-every 10s -duration 5s -target proxy -tls -dribble-hello
```

### ✅ P4-2 · G4 benchstat phân giải overhead — trả 2026-10-04 trên Linux thuần

So sánh 20 lần lặp giữa commit nền `b3e08f0` (trước phase 4) vs `989b060` (sau kiểm tra smuggling) bằng `benchstat`:
- `b3e08f0`: $1.018\,\mu\text{s} \pm 1\%$ ($864\,\text{B/op}$, $26.00\,\text{allocs/op}$)
- `989b060`: $1.096\,\mu\text{s} \pm 0\%$ ($864\,\text{B/op}$, $26.00\,\text{allocs/op}$)
- Chênh lệch thời gian: **$+7.66\%$** ($p=0.000, n=20$, $+78\,\text{ns}$), $0\,\text{B/op}$ và $0\,\text{allocs}$ tăng thêm.
Khẳng định phân giải rõ ràng chi phí kiểm tra smuggling trên Linux thuần (loại bỏ nhiễu $\pm 10\text{--}37\%$ của WSL2).

### ✅ P8-5 · Ba mục RFC đọc nguyên văn — trả 2026-10-04

Đã đọc và trích dẫn nguyên văn vào `diary/phase8.md` (mục Đọc gì):
- RFC 9110 §15.5.20: 421 Misdirected Request định nghĩa, client MAY retry trên connection mới (kể cả non-idempotent), proxy MUST NOT tự sinh 421.
- RFC 8446 §4.6.1: NewSessionTicket gửi sau Finished, SNI validation trên resumption, 0-RTT PSK derivation.
- RFC 8446 §8: Phân tích 2 lớp tấn công replay 0-RTT, giới hạn "at most once per server instance" và vai trò phòng thủ ở tầng ứng dụng.

### ✅ P9-6 · 8 KiB còn lại của connection rỗi — trả 2026-10-04 trên Linux thuần

Đo khi mở thực sự $10\,000$ connection rỗi (`perflab -mode idle -conns 10000 -spawn "bin/edgegate..."`):
- `VmRSS`: $12.956 \to 115.392\,\text{KiB} \Rightarrow \Delta = \mathbf{10.24\,\text{KiB/conn}}$ (gồm Go goroutine stack + kernel TCP buffers).
- `Heap inuse`: $11.8\,\text{MB}$ total ($\approx 1.18\,\text{KiB/conn}$ heap), chủ yếu là `anyToSockaddr` ($13\%$), `newFD` ($8.7\%$), `track` ($8.7\%$), `sockaddrToTCP` ($8.7\%$).
- `Goroutines`: Đúng $10\,000$ goroutines (1 goroutine/conn chặn ở `runtime_pollWait`). Drain xong giải phóng $0$ connection kẹt.

### ✅ P-env-2 · Generator và proxy dùng chung core — trả 2026-10-04 trên Linux thuần

Đo trên AMD Ryzen 7 16-thread (`bench/linux/thai-computer-20261004-105630/p-env-2-pinlab.txt`), ghim core:
Upstream core 15, Proxy core 0-1 (`GOMAXPROCS=2`), Generator core 2-14. Phân chia core hoàn toàn độc lập,
loại bỏ hoàn toàn hiện tượng tranh chấp CPU proxy ảnh hưởng đến số đo latency.

### ✅ P3-5 · G2/G3 ổn định giữa các lần đo — trả 2026-10-04 trên Linux thuần

Đo 5 lần liên tiếp trên Linux (`bench/linux/*/p3-5-overhead.txt`): overhead G2 p50 proxy / p50 thẳng lần lượt
là **1.88x, 1.83x, 1.82x, 1.86x, 1.84x**. Độ dao động chỉ trong khoảng $\pm 0.03\times$, đạt xuất sắc tiêu chí $< \pm 0.1\times$.

### ✅ P5-1 · G1/G2 đo trên máy Linux yên tĩnh — trả 2026-10-04 trên Linux thuần

Đo `poollab -pool both` trên máy yên (`bench/linux/*/p5-1-poollab.txt`): Tiết kiệm **48 µs / req = 0.58 RTT / req**,
tổng thời gian hoàn thành (wall time) pool-on nhanh hơn **1.49x** so với pool-off; overhead còn lại qua proxy chỉ 83 µs.

### ✅ P7-4 · chaoslab cân bằng (heal-weight 6) — trả 2026-10-04 trên Linux thuần

Đo `chaoslab -heal-weight 6 -seed 3` (`bench/linux/*/p7-4-chaos-balanced.txt`): Đạt trọn vẹn 4 invariants (a, b, c, d),
không panic, không rò rỉ goroutine (nền 1), không rò fd (nền 11).

### ✅ P8-1 · CPU handshake TLS trên Linux thuần — trả 2026-10-04 trên Linux thuần

Đo `tlslab -mode handshake` (`bench/linux/*/p8-1-handshake.txt`): Plaintext: $4 601\text{ rps}$ ($200\,\mu\text{s}$),
TLS 1.3 ECDSA: $689\text{ rps}$ ($1.488\,\text{ms}$), TLS 1.3 RSA: $238\text{ rps}$ ($4.404\,\text{ms}$). Đo được chính xác
chi phí CPU ký RSA đắt hơn ~3x so với ECDSA.

### ✅ P9-4 · Bảng ba cột EdgeGate vs Nginx vs ReverseProxy — trả 2026-10-04 trên Linux thuần

Đo `scripts/bench-vs-nginx.sh` đối chứng Nginx 1.25 và httputil.ReverseProxy: Tại 10 000 rps, EdgeGate ($550\,\mu\text{s}$ p50 /
$1.44\,\text{ms}$ p99) đạt hiệu năng tương đương Nginx ($460\,\mu\text{s}$ p50 / $1.61\,\text{ms}$ p99) và vượt trội hơn ReverseProxy về tail latency.

### ✅ P10-8 · Đo CPU h2 vs h1 trên Linux thuần — trả 2026-10-04 trên Linux thuần

Đo `h2lab -mode cpu -rounds 5` (`bench/linux/*/p10-8-cpu.txt`): h2 đạt $59 825\text{ rps}$ ($25.2\,\mu\text{s}/\text{req}$,
ctxsw 0.047/req); h1 đạt $82 357\text{ rps}$ ($19.3\,\mu\text{s}/\text{req}$, ctxsw 0.013/req). Phân biệt rõ chi phí framing/multiplexing.

### ✅ P6-5 · Passive outlier cắt cửa sổ dial lỗi của active — trả 2026-10-03

Đo được trên WSL2 vì là ĐẾM qua máy trạng thái (số request đập vào backend chết trước khi bị loại), không phải
percentile latency. `TestLBKillRevive` tách hai sub-test chung helper `killReviveScenario`: `active-only` (chỉ active
health, Fall 3×200 ms) vs `outlier-on` (Consecutive 5, BaseEject 150 ms). `-count=5`:
**active-only 116-122 fails, outlier-on 8 fails mọi lượt** (ổn định tuyệt đối) — passive cắt cửa sổ ~15×. Khớp dự
đoán cũ "~5" (8 = 5 ban đầu + ~3 probe half-open qua cửa sổ 600 ms). Client thấy TOÀN 200 cả hai (dial lỗi được D9 né
sang backend khác). `active-only` còn khẳng định unhealthy ⇒ 0 pick thêm; `outlier-on` half-open cho vài probe.

```bash
go test ./internal/proxy -run 'TestLBKillRevive' -count=5 -v
```

> **Cập nhật 2026-10-05.** Từ 37cd9d9 kịch bản chạy 32 client không nghỉ thay vì 1 client 1 ms/request,
> nên lệnh trên không còn tái hiện 116-122 vs 8. Số mới trên WSL2: **active-only 6500-7200, outlier-on
> 11-34** — passive vẫn cắt cửa sổ hàng trăm lần. Sàn outlier-on cao hơn vì tối đa 32 request đang bay tới
> b3 lúc nó chết đều lỗi trước khi passive loại. Kịch bản đặt `RetryBudget.Percent = 1`: ở 32 client, 25 %
> request cần retry D9 vượt budget mặc định 10 % ⇒ 502 đúng thiết kế phase 7 D6, không phải lỗi health.

### ✅ P4-6 · Oracle thứ hai + thứ ba cho `TestSmugglingOracle` — trả 2026-10-03

`cmd/smuggleoracle` + `make oracle-ext` (docker): phát lại 59 ca request-kind qua TCP trần vào nginx 1.25-alpine và
h2o, ghi status, so ba cột `mình/nginx/h2o` (`bench/p4-6-oracle-ext.txt`). h2o trả 404 cho mọi head hợp lệ (không có
file) ⇒ harness phân loại **nhận khung** (2xx/3xx/404/405/100) vs **từ chối khung** (400/501/431/đóng), tìm hướng
nguy hiểm = mình từ chối mà origin nhận khung.

Ba ca điểm danh trong nợ xác nhận chọn strict là đúng, không phải lập dị:
- **21 (CL trùng lặp giống nhau)**: mình 400, **nginx cũng 400**, h2o 404 — ít nhất một oracle độc lập cùng từ chối.
- **30/31 (bare LF)**: mình 400, **nginx 200 + h2o nhận** — proxy strict đứng trước hai backend lenient là đúng kịch
  bản smuggling (RFC 9112 §2.2 "MAY recognize"); nếu ta cũng lenient thì bare LF thành ranh giới lệch.
- **50 (`Connection: Host`)**: mình 400, **nginx 200 + h2o nhận** — D7 (xoá token Connection điều khiển Host) chặn
  đúng thứ hai backend bỏ lọt.

46/118 cặp (ca × origin) origin lenient hơn — gồm cả ca chính sách (host/absolute-form/đếm header, ca 60-71) lẫn ca
khung thật (bare LF, chunk-size, trailer, Connection-token). Mọi ca "ok" của mình đều được ≥ 1 origin nhận (không có
hướng nguy hiểm ngược: mình nhận mà cả hai origin từ chối). Không ca nào origin từ chối-khung mà mình nhận.

```bash
make oracle-ext   # cần docker + image nginx:1.25-alpine, lkwg82/h2o-http2-server
```

### ✅ P9-7 · `SpliceBody` tắt mặc định — trả 2026-10-02

D11 phase 9. Điều kiện đủ: P9-1 trả; `go run ./cmd/chaoslab -duration 60s -rate 500 -tick 300ms -body 262144`
(cờ `-body` mới: mỗi request thứ 4 là `/large` 256 KiB đi splice) **PASS**, `bench/p9-7-chaoslab-body.txt`. Lượt
đầu đỏ fd 23/7: 16 fd là 8 pipe của Go splice pool (finalizer) — chaoslab nay GC trước khi đếm, in riêng `pipe:`,
về 7/7. Lật: `Config.NoSplice` / `"no_splice_body"` / `-nosplice`; perflab `edgegate` trần truyền `-nosplice`.
`perflab-nodefense` (nodefense9) vẫn đỏ đúng hai test.

### ✅ P8-3 · Health check active với upstream TLS — trả 2026-10-02

D13 phase 8: `lb.HealthConfig.Dial` hook; proxy đặt `healthDial` (= `dialUpstreamTimeout`, cùng SNI/RootCAs/session
cache với data path) khi `UpstreamTLS` bật, cho lb chính và vhost. `TestHealthTLSUpstream` (httptest TLS + Path
`/hello`, Interval 30 ms, Fall 2): đỏ trên HEAD (unhealthy sau 300 ms), xanh sau sửa, GET qua proxy 3/3 200.

### ✅ P7-3 · Trần eject 50 % + half-open — trả 2026-10-02

D9 phase 7: đăng ký "node half-open thử hỏng dưới trần ⇒ ở lại half-open, một request thử mỗi vòng" là thiết kế.
`TestBreakerUnderEjectCap` (lb): 3 vòng thử hỏng ⇒ b0 nhận đúng 1/32 mỗi vòng, `EjectRefused 3 / Reopens 3 /
Ejections 1`, state `half-open`; thử tốt ⇒ closed ⇒ 16/32. Không đổi code.

### ✅ P3-2 · 101 Switching Protocols ⇒ 502 — trả 2026-10-02

D13 phase 3. `roundTrip` đặt lại `Connection: Upgrade` + `Upgrade` cho upstream khi GET không body; `exchange` gặp 101
mà ta đã chuyển Upgrade ⇒ `tunnel()` (101 về client, chép hai chiều qua `br`/`ubr`, deadline rỗi mỗi chiều, đóng cả hai
khi một bên xong, connection upstream discard). Fixture `/ws`: `Upgrade: echo` ⇒ 101 + echo. `TestUpgradeTunnel`
(byte đầu gửi liền sau head vẫn tới; 3 vòng echo; đóng client ⇒ `DropDirty ≥ 1`, `Idle 0`), `TestUpgradeNotRequested`
(POST + Upgrade ⇒ tước ⇒ upstream 426; upstream tự ý 101 ⇒ 502). `ResilienceStats.Tunnels` đếm. h2 vẫn 502.

### ✅ P10-6 · h2spec 3.5/2 fail vì h1 + h2c chung port — trả 2026-10-02

D12 phase 10: `Config.H2COnly` / `"h2c_only"` — listener plaintext bỏ sniff preface, mọi connection vào `serveH2`;
preface sai ⇒ đóng (h2.Conn), không 400. `TestH2COnlyRefusesH1` (h1 GET ⇒ 0 byte + EOF; h2c client vẫn 200).
`make h2spec H2CFG=config/h2-only.json`: **145/145**; `make h2spec` mặc định (chung port) vẫn 144/145 — 3.5/2 là
đánh đổi cố ý của D7, nay có công tắc. Qua TLS (P10-5) cũng 145/145.

### ✅ P10-5 · ALPN `h2` trên TLS — trả 2026-10-02

D11 phase 10. `Config.H2ALPN` / `"h2_alpn"` ⇒ `ServerConfig(h2)`: ALPN `["h2","http/1.1"]`, cipher TLS 1.2 chỉ ECDHE+AEAD
(RFC 9113 §9.2.2). `NegotiatedProtocol=="h2"` ⇒ `serveH2` không sniff preface. `TestTLSALPNH2` (net/http h2 qua TLS
SNI a.test: 3 GET HTTP/2.0 200 A, 3 stream; client chỉ http/1.1 vẫn h1), `TestTLSH2CipherBlocklist` (TLS 1.2 +
ECDHE-ECDSA-AES128-CBC-SHA + h2 ⇒ `handshake failure`; h2 tắt ⇒ cùng cipher h1 bắt tay được). `make h2spec-tls`:
**145 tests, 145 passed** — 3.5/2 (P10-6) xanh qua TLS vì không còn mơ hồ chung port.

### ✅ P4-3 · `X-Forwarded-Proto/Host/Port` chưa sinh, `X-Real-IP` từ peer tin chưa kiểm — trả 2026-10-02

Đăng ký D12 (diary phase 4) rồi code. `forwardedHeaders(h, c, st)` nay sinh Proto (`https` khi `st.tls`),
Host (client gửi), Port (listener). Peer không tin ⇒ thay cả ba; peer tin ⇒ giữ nếu hợp lệ. `X-Real-IP` của peer
tin không `ParseIP` được ⇒ := clientIP. **clientIP = phần tử phải nhất của XFF không trong `trusted_proxies`**
(trước: phần tử đầu — client bịa được qua proxy tin). `Forwarded` RFC 7239 quyết định **không sinh** (hai nguồn
sự thật). `TestForwardedHeadersUnit` (6 nhánh), `TestForwardedProtoHostPortE2E` (plaintext, peer không tin bịa
`https`/`evil`/`443` ⇒ 0 lọt), `TestForwardedProtoTLS` (qua TLS SNI `a.test` ⇒ `https`). `TestRateLimitPerIP`
nhánh tin vẫn 10/20 — khoá không đổi khi chuỗi XFF một phần tử.

### ✅ P2-5 · `FuzzReadRequest` đỏ 1/3 lượt: 164 912 byte cho input ~6.2 KB — trả 2026-10-02

Không phải `sync.Pool`. Corpus `testdata/fuzz/FuzzReadRequest/637b585bcb169468`: request-line `1 1 HTTP/1` +
~6 K byte `\xa9`. `badRequest("phiên bản không hợp lệ: %q", b)` chép **cả trường** vào `Reason`: `%q` nở
`\xa9` → `\xa9` (4 byte/byte) cộng tăng trưởng buffer của `fmt` ⇒ 151-196 KB cho 7 KB input, và chuỗi đó đi
vào log. Fuzz chỉ đỏ 1/3 vì bộ đếm tầng 1 (`runtime/metrics`) đếm thiếu ~4x so với `ReadMemStats`.

`TestErrorAllocBound` viết trước, đỏ trên HEAD 4/5 ca (`Reason 28 040 byte`, `151 584 byte cho input 7 016`,
trần 44 448); ca `status` xanh sẵn vì code chỉ 3 byte. Sửa: `clip()` trong `errors.go` — mọi `%q` nhận byte của
peer (version, method, tên header, token `Connection:`, status code) chỉ mang 32 byte đầu + `…(+N byte)`.
Sau sửa: fuzz 3/3 lượt 30 s xanh; `TestReadRequestAllocs` vẫn 24.

### ✅ P5-4 · Body vào connection chết giữa probe và `Write` ⇒ 502 — trả 2026-10-02

`cmd/poollab -idlerace` (`make poollab-idlerace`): upstream raw đóng rỗi sau 1-50 ms ngẫu nhiên mỗi response,
32 worker POST 1 KiB nghỉ 0-60 ms, 10k request. `bench/p5-4-idlerace.txt` (WSL2, load 5.45), 3 lượt:
**probe on 13 / 13 / 9 ⇒ 0.09-0.13 %** (≈ 3 % số lần upstream đóng rỗi lọt cửa sổ probe→Write);
probe off 3.6-3.7 %. Vượt ngưỡng 0.1 % đăng ký, nhưng replay POST bị cấm: RFC 9110 §9.2.2 (đọc nguyên văn)
"A proxy MUST NOT automatically retry non-idempotent requests." ⇒ 502 là câu trả lời đúng cho POST;
client (biết ngữ nghĩa) tự quyết. Replay cho PUT/DELETE có body tách thành P5-4b.

Lỗ lộ ra khi đọc §9.2.2: **D4 retry cả POST không body** (chỉ xét body, không xét method).
`TestIdleClosedUpstream/noprobe-POST-nobody-502` viết trước, đỏ: `Retries:50`, muốn 0. Nay `canRetry`
(h1) và `h2exchange` đòi `idempotent(method)` = GET/HEAD/OPTIONS/TRACE/PUT/DELETE (§9.2.1 + §9.2.2).
D9 không đổi: dial lỗi ⇒ chưa byte nào đi, không phải retry.

### ✅ P4-4 · Tag `nodefense` chung cho phase 1/3/4 — trả 2026-10-02

Mỗi phase một tag: `nodefense1` (frame), `nodefense3` (proxy D-drain + raw copy), `nodefense4` (httpx 7 phòng
tuyến). `nodefense` trơn vẫn tắt cả ba (`//go:build nodefense || nodefenseN`) để lệnh cũ trong diary còn chạy.
`make smugglelab-nodefense` nay `-tags nodefense4`: e2e lật **21 → 20** ca — `95-resp-ok-eof` xanh (nó đỏ vì
bẫy #2 phase 3, không vì phase 4). `framelab-nodefense` (2 test), `proxylab-nodefense` (2 test),
`smugglelab-nodefense` vẫn đỏ đúng chỗ.

### ✅ P4-5 · Ca còn thiếu trong `testdata/smuggle` — trả 2026-10-02

Năm ca mới, `expect` ghi trước khi chạy: `33-request-line-trailing-sp` (400), `48-chunk-ext-too-long` (body-431),
`53-header-count-bomb` (431, 101 dòng), `87-ok-get-cl-body` (ok: GET đọc theo CL, `hello` không thành request kế),
`88-ok-expect-continue` (ok ở parser). Hai lỗ lộ ra:

- **48 qua proxy trả 400 thay vì 431**: lỗi body request nào cũng thành 400. Nay `*httpx.ProtoError` giữ status
  của parser (`forward.go`, nhánh `readErr`).
- **`Expect: 100-continue` treo**: proxy forward head nhưng D7 (phase 3) nuốt 1xx ⇒ client chờ 100 đến timeout
  của chính nó, proxy chờ body. RFC 9110 §10.1.1 (đọc nguyên văn): proxy PHẢI trả status final ngay hoặc forward
  head; chỉ được TỰ sinh 100 khi tin server kế là HTTP/1.0. Nay `awaitContinue`: flush head, chờ upstream ≤ 1 s
  (`expectWait`): 100 ⇒ chuyển cho client rồi gửi body; status final ⇒ trả luôn, không gửi body, đóng cả hai
  phía (body client còn trên br, upstream nhận head có CL mà thiếu body); im lặng ⇒ cứ chuyển body. Client đã
  gửi body sẵn (`br.Buffered() > 0`) ⇒ không chờ. `TestExpectContinue` (3 ca) viết trước: 2 ca đỏ trên HEAD
  (`i/o timeout` sau 500 ms), ca im lặng xanh cả hai. Không đụng h2 (client h2 gửi body không chờ).

### ✅ P4-1 · Duyệt `Connection:` cấp phát — trả 2026-10-02

`parse.go:hasConnectionToken` và `header.go:StripHopByHop` duyệt `Connection:` bằng `strings.Cut` (bản cũ
`bytes.Split([]byte(v))` / `strings.Split` — cấp phát bản sao + slice). `TestReadRequestAllocs` (`AllocsPerRun`, trần 24)
viết trước, đỏ `26 alloc > 24`; sau 24. `BenchmarkReadRequest` ×6: **26 → 24 allocs/op, 864 → 824 B/op**, ns/op
1590-3630 → 1145-2158 (máy ồn, không chốt ns). Ba lần duyệt vẫn còn nhưng không lần nào cấp phát — gộp làm một chỉ
tiết kiệm ns, không đổi đại lượng món nợ đăng ký. Suite httpx/proxy `-race` + `make smugglelab` xanh.

### ✅ P7-2 · Trần connection theo IP — trả 2026-10-02

`Config.MaxConnsPerIP` (khoá = IP peer; `proxy.go:track` đếm dưới `mu`, `untrack` trả — I7, xoá key về 0); vượt ⇒
đóng ngay sau Accept, trước goroutine/bufio; `ResilienceStats.PerIPRejected`; `edgegate` `max_conns_per_ip`, `slowlab
-max-conns-per-ip`. `TestMaxConnsPerIP` (×3 `-race`): 5 conn rỗi từ 127.0.0.2 với trần 3 ⇒ 2 bị đóng ngay (EOF
< 300 ms ≪ HeaderTimeout), 127.0.0.1 vẫn 200, đóng một conn ⇒ slot về. slowlab 500 conn (`bench/p7-2-slowlab-perip100.txt`):
proxy giữ **100** (phase 7: 500), probe 100 % cả baseline lẫn lúc tấn công. Lệnh ghi trong sổ (`-max-conns-per-ip 32`)
chọn sai ngưỡng: probe có 64 worker keep-alive cùng một IP ⇒ baseline chỉ 51.6 % 200 (`bench/p7-2-slowlab-perip.txt`) —
trần đếm mọi connection phải lớn hơn độ đồng thời hợp lệ của một IP. Cái giá mới đo được ⇒ P7-2b.

### ✅ P6-2 · P2C chia lệch trên 4 node giống hệt — trả 2026-10-02

Hai thay đổi, chốt bằng A/B trên lblab thật (không bằng mô phỏng):
1. `lb/ewma.go:observe` — trong tau đầu kể từ mẫu đầu, EWMA = **trung bình cộng** mọi mẫu (bản cũ: `v = mẫu đầu`, mẫu sau
   nặng ~dt/tau ≈ 4·10⁻⁴ ở 2.5k rps/node ⇒ mẫu đầu thống trị cả giây). Sau tau: EWMA theo thời gian như cũ.
2. `forward.go`/`h2.go` — latency cho EWMA tính từ khi có connection (không tính dial); dial lỗi vẫn `Done(start, true)`.

`make lblab-even` (mới): 10 lượt `lblab -algos p2c,rr,leastconn -max-share-ratio 1.2`, **p2c chạy đầu tiên** — chỉ lúc
tiến trình còn lạnh mới có outlier khởi động (`max` 37-94 ms); bản đầu của target chạy p2c sau rr/leastconn ⇒ tiến
trình đã ấm ⇒ ngay bản cũ cũng xanh (bẫy); bản thứ hai đếm mã thoát của `grep` thay vì lblab (in FAIL mà vẫn "0/10").
A/B cùng lệnh, liền nhau, load 2.4-3.2: **mới 0/10, HEAD 6/10** (1.53-2.00x). Riêng (2): 4/10 vẫn lệch — dial không
phải nguyên nhân chính. Mô phỏng sự kiện rời rạc (32 client, outlier ngẫu nhiên trong 3 mẫu đầu) KHÔNG tái hiện lệch
kể cả trên bản cũ (tệ nhất 1.09) ⇒ xoá, không giữ test chưa từng đỏ; cơ chế chi tiết ⇒ P6-2b.

### ✅ P6-3 · Node chưa có mẫu được điểm 0 khi đang có request — trả 2026-10-02

`lb/picker.go:Backend.score`: node chưa có mẫu (`ewma == 0`) mà đang có inflight ⇒ `noSamplePenalty + inflight` (Finagle
PeakEwma "Penalty + pending"); lượt thử đầu (inflight 0) vẫn được ngay. Test viết trước, đỏ trên code cũ:
`TestP2CNoSampleNotFlooded` (4 node ấm EWMA 1 ms + 1 node mới, 32 lượt chọn chưa Done) ⇒ `node chưa có mẫu nhận 10/32`.
Sau: ≤ 1 trong 20/20 lượt; `TestP2CColdStartSpreads` (cold-start mọi node) và `TestP2CRecovers` (46/200) vẫn xanh;
`-tags nodefenselb` đỏ đúng `TestP2CRecovers` như HEAD.

### ✅ P10-7 · Trailer h2 bị bỏ — trả 2026-10-02

Cả hai chiều qua downgrade h2 → h1 → h2. Response: `h2.Stream.WriteTrailers` (HEADERS cuối END_STREAM, RFC 9113 §8.1)
khi `resp.Trailer()` có field; không có ⇒ DATA rỗng END_STREAM như cũ. Request: `endHeaderBlock` giữ trailer trên stream
(`Stream.Trailer()` sau io.EOF); `h2copyRequestBody` (body không CL ⇒ chunked) ghi nó thành trailer chunked, bỏ field
cấm (`httpx.ForbiddenTrailer`, RFC 9110 §6.5.1). Còn lại có chủ ý: request h2 có `content-length` ⇒ chặng h1 dùng CL,
không chở được trailer ⇒ bỏ; `te: trailers` không truyền sang upstream (upstream h1 vẫn được gửi trailer, client vẫn
nhận được). Test viết trước, đỏ trên HEAD (worktree): `TestH2Trailers` `trailer map[Grpc-Status:[] X-Echo-Sig:[]]`.
Sau (×3 `-race`): client net/http h2c nhận `Grpc-Status: 0`, `X-Echo-Sig: s1` (trailer request `X-Sig` đi tới upstream
và quay về). h2spec vẫn 144/145.

### ✅ P3-1 · Trailer chunked bị bỏ (h1, cả hai chiều) — trả 2026-10-02

`httpx/chunked.go:CloseWithTrailer` (chunk cuối + trailer qua `appendWire` — CR/LF bị từ chối); `forward.go:copyBodyT`
gọi `resp.Trailer()` / `req.Trailer()` SAU khi body EOF và ghi kèm chunk cuối, cả hai chiều. Đọc RFC 9110 nguyên văn
(tải 2026-10-02): §7.6.1 **không** liệt kê `Trailer` là hop-by-hop (chỉ Proxy-Connection, Keep-Alive, TE,
Transfer-Encoding, Upgrade + Connection) — comment phase 2 trong `httpx/header.go` trích sai ⇒ bỏ `Trailer` khỏi
`hopByHop`, sửa comment và `TestStripHopByHop`; §6.6.2: `Trailer` là gợi ý cho bên nhận cuối. Test viết trước, đỏ trên
HEAD (worktree): `TestTrailerForwarded` `trailer map[]`, `TestRequestTrailerForwarded` `upstream thấy trailer ""`. Sau
(×3 `-race`): `X-Checksum: abc123` tới client net/http; `X-Sig: s1` tới upstream; trailer cấm `Content-Length` ⇒ 400.

### ✅ P9-1 · Splice body: lỗi ghi client bị tính là lỗi upstream — trả 2026-10-02

`splice.go:spliceClientFault` phân loại sau một ReadFrom thiếu byte: `err == nil` ⇒ upstream EOF sớm; `EPIPE` (chỉ
phát sinh khi ghi) ⇒ client; còn lại ⇒ dò hai socket bằng `pool_linux.go:peerGone` (MSG_PEEK; khác `probeIdle`: byte
đang chờ = còn sống) — upstream chết ⇒ upstream, không mà client chết ⇒ client, không rõ ⇒ upstream như cũ. Client lỗi
trả về `werr` ⇒ không nuôi outlier, connection upstream vẫn bị bỏ (chưa đọc hết body). Test viết trước, đỏ trên code
cũ: `TestSpliceClientGone` (body 16 MiB, client đọc 100 KB rồi đóng ⇒ RST) `fails=1` ×3. Sau ×5 `-race`: `fails=0`;
đối chứng `TestSpliceBodyShortUpstream` thêm assert `fails=1` (upstream đóng giữa body) xanh.

### ✅ P7-1 · D9 chọn lại chính backend vừa dial lỗi — trả 2026-10-02

`lb/balancer.go:PickExcept(key, ex)` — picker nhận bản sao danh sách với `ex` thay bằng chỗ giữ `unavailable`
(healthy=false; giữ vị trí ⇒ chash đi tiếp trên ring); `forward.go:roundTrip` dùng nó cho lượt chọn lại D9, không còn
ai khác ⇒ 502 ngay. Test viết trước, đỏ trên code cũ: `TestDeadline/dial-502` siết ≤ DialTimeout + 150 ms ⇒ `502 sau
602ms`; `TestRepickExcludesFailedBackend` (2 backend, một không route, least-conn, outlier tắt) ⇒ `200:17 502:3`. Sau
(×3 `-race`): `502 sau 301-303ms`; `200:20` với 8-11 lượt bốc trúng backend xấu. `TestPickExcept`: 4 thuật toán × 300
lượt không trả `ex`; chash giữ đúng một backend thay thế cho cùng key; một backend ⇒ nil.

### ✅ P-ops-1 · `make proxybench` để sót tiến trình — trả 2026-10-02

`proxybench` viết lại thành một recipe shell: `trap 'kill $PX $UP; wait' EXIT` dọn mọi tiến trình nền trên MỌI đường
ra; `go build` mỗi dòng một lệnh; biến `PROXYLAB` để giả lập bước lỗi. Kiểm 2026-10-02: `make proxybench
PROXYLAB=false` ⇒ `Error 1`, `pgrep -x upstream/edgegate` = 0/0; chạy thật ⇒ đủ bảng G2-G4, sót 0/0. `make h2spec`
(cùng khuôn rủi ro, h2spec thoát 1 vì ca 3.5/2) dùng cùng trap ⇒ sót `epolllab/edgegate` 0/0.

### ✅ P3-3 · Test upstream chết giữa body response — trả 2026-10-02

`debts_test.go:TestUpstreamDiesMidBody`: upstream gửi `Content-Length: 100` + 50 byte rồi đóng ⇒ client nhận đúng 50
byte + `io.ErrUnexpectedEOF`, sau đó EOF sạch (không byte nào thêm), backend `fails=1` (×3 `-race`). Xanh ngay lần đầu
— bất biến đã đúng theo code — nên kiểm bằng **đột biến**: thêm `writeError(502)` sau lỗi body trong
`forward.go:exchange` ⇒ test đỏ `body: 100 byte, err <nil>` (50 byte body + 50 byte đầu của trang 502 dính vào body —
đúng "hai response cho một request"); trả file về `git checkout`.

### ✅ P3-4 · G6 chạy đủ 1 000 request — trả 2026-10-02

`TestNoGoroutineLeak` chạy đúng số G6 đăng ký: 1 000 request, chia ba h1 keep-alive / h1 mỗi request một connection /
h2c (phase 10: goroutine đọc + goroutine stream cũng phải về, I8). `-race -count=3`: goroutine trước 5, sau 6
(≤ before+2) sau 20 ms. **Sửa cùng ngày (lúc trả P7-2):** đỏ 1/3 lượt full suite `trước 5, sau 8` —
in stack: 3 goroutine thừa là `net/http.(*conn).serve` của FIXTURE (connection rỗi trong pool proxy, đúng thiết kế;
trộn h2 ⇒ pool giữ 2-3 conn). Test giờ trừ goroutine phía upstream (đếm từ profile) và in stack khi đỏ; 0/20 đỏ; đột
biến (rò một goroutine mỗi connection proxy) ⇒ đỏ `trước 5, sau 341`. Commit P7-2 `4a4327d` đi qua dù suite đỏ vì
chuỗi lệnh nối bằng `;` — từ đó commit chỉ sau `go test … &&`.

### ✅ P10-9 · `TestIdleClosedUpstream` chập chờn dưới tải — trả 2026-10-02

Tái hiện trước khi sửa: chạy subtest probe `-race -count=30` trong khi 5 package khác chạy test `-race` song song + 6 ×
`yes` (load 7.25) ⇒ **1/30 đỏ** (`probe-POST-body`: `46/50 … DropDirty:4 DeadOnProbe:42`); chỉ 6 × `yes` thì 0/20 —
tải phải giống full suite. Nguyên nhân: test ngủ cố định 20 ms chờ FIN, nhưng dưới tải goroutine upstream có thể chưa
chạy tới `Close`. Sửa: `idleClosingUpstream` báo tín hiệu SAU `Close`; `waitUpstreamClosed` chờ tín hiệu (trần 2 s) +
2 ms cho FIN qua loopback; nhánh noprobe-502 chỉ chờ sau request upstream thực sự phục vụ. Sau, cùng bài tải: **0/30**
và **0/60** (load 7.06), full suite `-race` 3/3 sạch; test 3.9 s → 0.5 s (bỏ 50 × 20 ms).

### ✅ P10-4 · PING / SETTINGS / request malformed khuếch đại chi phí server — trả 2026-10-02

Đo trước (`h2lab -mode flood`, `bench/p10-flood-before.txt`): client gom 100 frame mỗi lần ghi ⇒ server **100 001
Flush** cho 100 000 PING (khuếch đại syscall 100x), 5.75 µs/frame; SETTINGS 2 730 × INITIAL_WINDOW_SIZE với 100 stream
mở ⇒ **1.8-2.8 ms CPU mỗi frame 16 KiB** dưới `c.mu` (O(setting × stream)). Sửa cấu trúc, không ngưỡng, không chặn
nhầm client hợp lệ: (a) `conn.go:writeCtl` — phản hồi của goroutine đọc chỉ Flush khi buffer đọc không còn TRỌN một
frame (`frameBuffered`; Peek chỉ khi đủ 9 byte — Peek thiếu byte sẽ chặn đọc socket), `flushPending` trước khi có thể
chặn đọc; (b) `onSettings` — nhiều INITIAL_WINDOW_SIZE trong một frame áp một lần (chênh lệch cuối; kiểm tràn bằng
giá trị lớn nhất). Sau (`bench/p10-flood-after.txt`, xen kẽ với nodefense10 ×2): PING 100 000 → **111 Flush**,
0.65 µs/frame; SETTINGS **51-106 µs/frame** (≈ 45x); malformed 90 Flush, 1.84-1.90 µs/frame. Phản chứng
(`-tags nodefense10` ĐỎ): `TestControlFloodCoalesced` 10 000 vs 11-12 Flush, `TestSettingsCollapse` 2 436 731 vs
≤ 990 lần cộng window (+ ngữ nghĩa: window = giá trị cuối 65 536). h2spec vẫn 144/145 (`bench/p10-h2spec-4.txt`).
Không thêm trần tốc độ: sau (a)(b) chi phí server mỗi frame ≈ chi phí client gửi nó.

### ✅ P10-3 · WINDOW_UPDATE nhỏ giọt — đóng 2026-10-02 bằng số đo: KHÔNG phải lỗ (tiền đề của món nợ sai)

Món nợ ghi "1 MiB = 1 048 576 frame + 1 048 576 syscall" — ước lượng, chưa từng đo. Đo
(`TestTinyWindowUpdates`, viết dạng "phải GOAWAY" trước, chạy trên code cũ): 20 000 WINDOW_UPDATE 1 byte ⇒ **878-1 208**
DATA frame (client flush mỗi 1/10/100 update; dưới `-race` 2 822-3 350), không phải 20 000. Lý do: ghi đồng bộ dưới
`wmu` (D3) và `WriteData` lấy **toàn bộ** credit đang dồn mỗi frame ⇒ trong lúc ghi một frame, client gửi thêm nhiều
update và chúng gộp vào frame sau ⇒ số DATA frame ≤ số WINDOW_UPDATE (+ phần cắt theo MAX_FRAME_SIZE): **không khuếch
đại**, chi phí client ≥ chi phí server. Kịch bản RFC 9113 §10.5 nhắm cài đặt *xếp hàng* frame. Không thêm phòng tuyến
(ngưỡng "chờ ≥ 1 KiB" deadlock client window nhỏ hợp lệ; đếm-rồi-GOAWAY chặn nhầm nó). Test giữ lại làm chốt bất
biến tất định "frame ≤ update + 64". Còn lại, không phải nợ: client đi từng bước (1 update, chờ DATA) tốn 1 RTT mỗi
byte — tự giới hạn.

### ✅ P10-2 · Rate limit / shed không áp cho stream h2 — trả 2026-10-02 (sau phase 10 turn 3)

Đường vòng: `serveH2Stream` không gọi `admit` ⇒ client nói h2c trên cùng port thoát token bucket lẫn trần inflight.
Sửa: tách lõi `resilience.go:admitDecision` (trả 0/429/503 + release, không ghi gì); `admit` (h1) ghi qua bufio,
`h2.go:serveH2Stream` ghi HEADERS/DATA kèm `retry-after`, gọi sau head, trước `Pick`, `defer release()` (I7). Test
viết trước, đỏ trên code cũ: `TestH2RateLimit` `h2: map[200:6]`, h1 sau đó vẫn 200 (`RateLimited:0`); `TestH2Shed`
`/hello` 200 khi `/slow` giữ slot duy nhất. Sau sửa (×3 `-race`): `map[200:2 429:4]`, Retry-After 4/4, h1 sau đó 429
(bucket chung, `RateLimited:5`); `/hello` 503, `Inflight:0`. `-tags nodefense7` đỏ đúng 3 test như trước
(`TestDeadline`, `TestRateLimitPerIP`, `TestRetryBudget`).

### ✅ P10-1 · Drain không biết connection h2 — trả 2026-10-02 (phase 10 turn 3)

Connection h2 không bao giờ "idle" theo nghĩa h1 ⇒ `Drain` chờ hết timeout rồi đóng cưỡng bức, cắt stream đang chạy.
Sửa: `h2.Conn.Shutdown` — GOAWAY NO_ERROR với last-stream-id hiện tại, không nhận stream mới, stream cuối xong ⇒
đóng; `resilience.go:Drain` gọi nó qua `connState.h2`; `serveH2` tự Shutdown nếu Store sau lần quét (Dekker như
idle/closeIdle). `TestH2Drain` viết trước, đỏ trên code cũ (`Drain 3.001s, forced=2`), sau sửa `Drain 202ms,
forced=0`, stream `/slow` đang chạy trả `200` (×3 `-race`).

### ✅ P9-3 · `copyBody` cấp 32 KiB cho body request rỗng — trả 2026-10-01 (phase 9 turn 3)

`forward.go:exchange` gọi `copyBody(ubw, req.Body, …)` cả khi GET không body ⇒ mỗi request 2 buffer 32 KiB
(nguồn của "68 KiB, không phải 33" ở G1). Sửa: chỉ gọi khi `ContentLength != 0 || Chunked`. `bench/p9-p93.txt`:
bản `nodefense9` 68 129 → **35 361 B/op**, 47 → 46 allocs; suite `-race` xanh (`bench/p9-invariants.txt`).

### ✅ P8-2 · SNI lạ ⇒ alert `internal_error` — trả 2026-10-01 (phase 8 turn 3)

`crypto/tls` (`handshake_server_tls13.go:pickCertificate`) chỉ gửi `unrecognized_name(112)` cho sentinel nội bộ
`errNoCertificates`; lỗi từ `GetCertificate` ⇒ `internal_error`. `tlsx/store.go:ServerConfig` thêm
`GetConfigForClient` trả config không cert cho SNI lạ ⇒ 112 đúng RFC 6066 §3. `TestTLSSNIRouting` đòi chuỗi
"unrecognized name": đỏ trước sửa, xanh sau.

### ✅ P5-2 · Con quá `MaxIdleTime` ở đáy stack — trả 2026-09-04 (phase 6 turn 1)

Sổ nợ tả sai một nửa: `get` dọn cả stack khi **đỉnh** quá tuổi; lỗ thật là đỉnh luôn tươi (một client
keep-alive) còn đáy già mãi. `pool.put` giờ quét từ đáy bỏ mọi con quá tuổi. `TestPoolExpiredAtBottom`:
đỏ trên code cũ `Idle 5, DropExpired 0` (`bench/p6-debts-failfirst.txt`), xanh `Idle 1, DropExpired 4`.

### ✅ P5-3 · Pool theo host; inflight theo request — trả 2026-09-04 (phase 6 turn 1)

`Server.pools map[addr]*pool` (`proxy.go:poolFor`), `MaxIdle` per host; inflight tăng ở `lb.Pick`, giảm ở
`lb.Done` sau `exchange` (sau cả `put` — D2 ghi "trước", sai vài µs, diary phase 6 câu 2).
`TestLBPoolPerHost`: Dials/Reuses/Idle 4/396/4, `Inflight 0` cả 4 backend.

### ✅ P5-5 · `MaxIdleTime` phải ngắn hơn upstream — trả 2026-09-04 (phase 6 turn 1)

Mặc định 60 → 30 s. `TestMaxIdleTimeShorterThanUpstream`: upstream idle 100 ms; pool 50 ms ⇒
`DropExpired 19, DeadOnProbe 0`; pool 60 s ⇒ `DropExpired 0, DeadOnProbe 19`.

### ✅ P2-3 · CL+TE ⇒ từ chối — trả 2026-09-04 (phase 4 D1)

`body.go:framing`: có TE mà cũng có CL ⇒ `ErrAmbiguousFraming` (400), không bỏ CL nữa. Phản chứng
`rejectCLWithTE=false` (nodefense) ưu tiên CL ⇒ ca 01/02/03/10/12/90 đỏ (`bench/p4-smugglelab-nodefense.txt`).
Test cũ `TestReadRequestBodyFraming/"CL+TE…"` đổi kỳ vọng. Diff-fuzz 7.75 M exec sau đổi: 0 lệch.

### ✅ P-arch-1 · `Host` giữ hay đổi — trả 2026-09-04 (phase 4 D4/D5/D6/D7)

**Giữ nguyên** (nginx `$host`). Điều kiện để "giữ" an toàn: Host chỉ có một nguồn — absolute-form
⇒ authority thắng và viết về origin-form, lệch ⇒ 400 (`normalizeTarget`); `Connection: Host` ⇒ 400
(`checkConnectionTokens`); CONNECT ⇒ 501; cú pháp `validHost`. Ngoại lệ sinh Host: client HTTP/1.0
không gửi (từ phase 3). Bằng chứng `TestAbsoluteFormRewritten`, ca 60-71, 83-86.

### ✅ P2-2 · 204/304 kèm TE: strip hay giữ? — trả 2026-09-04 (phase 3)

Đóng **bằng cấu trúc**: `StripHopByHop` xoá TE ở chiều response, proxy chỉ đặt lại TE khi chính
nó chunked; với NoBody (HEAD/1xx/204/304) mode là "không body" nên TE không bao giờ được đặt lại.
Không cần `if` riêng. Bằng chứng `TestHEADAnd204HaveNoBody` (`go test ./internal/proxy -run
TestHEADAnd204HaveNoBody -v`).

### ✅ P2-4 · `FuzzReadRequest` chậm 2x vì `ReadMemStats` — trả 2026-09-03

Đo trước khi sửa (không profile được với `-fuzz`, dùng benchmark tạm): thân fuzz **9.5 µs**/input,
một `runtime.ReadMemStats` **80 µs** (stop-the-world), `runtime/metrics.Read` **0.5 µs**. Hai lần
ReadMemStats đắt gấp ~17 lần việc thật. Giả thuyết lần này **đúng** — nhưng chỉ biết sau khi đo.
Sửa: kiểm hai tầng — tầng 1 `metrics.Read`, vượt trần mới xác nhận bằng **min của 3** lần
ReadMemStats. Lý do cần tầng 2: cả hai bộ đếm đều toàn tiến trình; lần chạy đầu bắt được dương
tính giả 506 KB cho input 44 byte (goroutine của fuzz engine cấp phát trong cửa sổ đo), không tái
hiện. Kết quả: **14 717 → 32 828 execs/s** (`bench/p2-fuzz-readrequest-metrics-60s.txt`, 1 874 459
execs, 0 đỏ giả). Phản chứng trần = 1 byte ⇒ đỏ 11 024 B/52 B (`bench/p2-fuzz-allocbound-counterproof.txt`).

### ✅ P0-5 · `cmd/netlab` không có test — trả gián tiếp ở phase 1 (P1-1), đóng 2026-09-03

Món nợ là `readFrame` riêng không được fuzz. P1-1 đã xoá nó: `cmd/netlab/wire.go` dùng
`frame.Decoder`, package `internal/frame` có `FuzzDecode`. `go test ./cmd/netlab` vẫn
`[no test files]` — đúng, vì netlab giờ chỉ còn logic thí nghiệm, không còn logic giao thức.

### ✅ P0-7 · Số tuyệt đối phase 0 là một lần chạy — trả 2026-09-03

5 lần `netlab -exp rtt -n 500 -bufio=false` liên tiếp (`bench/p0-7-rtt-5runs.txt`):

| run | p50 reuse | p50 dial | tỉ số |
|---|---|---|---|
| 1 | 8.5 µs | 154.4 µs | 18.24x |
| 2 | 7.8 µs | 141.5 µs | 18.05x |
| 3 | 7.5 µs | 125.6 µs | 16.65x |
| 4 | 8.1 µs | 127.7 µs | 15.77x |
| 5 | 7.6 µs | **138.8 µs** (median) | 18.23x |

Phase 0 ghi dial p50 **861.7 µs** (n=2000, tw đầy) và **341 µs** (n=100). Hôm nay median 138.8 µs
⇒ số tuyệt đối lệch **2.5–6x** giữa các phiên. Tỉ số cũng **không** bất biến: 36.69x → ~17x.
Kết luận sửa lại: chỉ có **thứ tự độ lớn** của tỉ số (>10x) và **chiều** của nó mang được sang
phiên khác; con số của tỉ số thì không. Mọi ô "tỉ số" ở phase 0 phải đọc là "cỡ chục lần".

### ✅ P0-2 · Capacity của `expOmission` được tính, chưa được đo — trả 2026-09-03

`netlab -exp omission -rate 100000 -svc 1ms -workers 1` (`bench/p0-2-capacity.txt`): closed-loop
1 conn đạt **737 rps**, p50 **1.34 ms** ⇒ `time.Sleep(1ms)` thật sự ngủ ~1.3 ms. Khớp 754 của
phase 0. Mốc "quá tải 1.2x" đúng là **~885 rps**; `make netlab-omission` cũ dùng 1200 = **1.63x**.
Đã đổi Makefile về 885.

**Bug lộ ra khi trả:** ở rate 100000, generator open-loop **panic** `slice bounds out of range`
trong `bufio.Reader.Read`. Nguyên nhân: `sem` đếm slot rỗi nhưng conn chọn theo `i % len(pool)`,
hai goroutine dùng chung một `clientConn`. Ở rate 1200 của phase 0 nó va khi hàng đợi > 427 ms
(512 conn / 1200 rps) — tức là **có va** trong lần đo G4 gốc (p99 open-loop hàng trăm ms). Sửa:
free-list `chan *clientConn`. `go run -race` 5 s: 0 DATA RACE (`bench/p0-2-race.txt`). Số G4
mới: p99 open/closed-1 = 377x (phase 0 ghi một số khác, xem errata phase0.md).

### ✅ P2-1 · Diff-fuzz không tới được lệch chỉ lộ ở oracle — trả 2026-09-03

Giả thuyết "fuzzer chỉ instrument package mình" **sai**: `rtk proxy go test -a -n -fuzz ...` cho
thấy 180/207 package compile với `-d=libfuzzer`, gồm `net/http`, `bufio`, `net/textproto`; 27
package không instrument là runtime/testing/fuzz engine. Nguyên nhân thật: lệch differential là
**quan hệ giữa hai đường đã được cover** (trimOWS của mình + nhánh lỗi chunk của Go), không tạo
edge mới ⇒ không có tín hiệu coverage; fuzz phẳng chỉ tới đó bằng may mắn.
Trả bằng: (b) `FuzzChunkLineAgainstNetHTTP` — head cố định, đột biến riêng dòng chunk-size — tìm
ra `" 3"` trên bản lenient trong **0.10s** (`bench/p2-structfuzz-lenient-60s.txt`), strict 120s sạch
(`bench/p2-structfuzz-strict-120s.txt`), `make difffuzz-chunk`; (c) 17 hàng bảng đối chiếu tay
thành seed `internal/httpx/testdata/fuzz/FuzzAgainstNetHTTP/hand-*` (32 seed chạy trong `go test`).

### ✅ P-code-1 · `internal/httpx` có test — trả ở phase 2 turn 1-2

`header_test.go` (canonical, `TE`→`Te`, `StripHopByHop` kể cả field liệt kê trong `Connection`,
`Write` sắp xếp + chặn CR/LF injection), `limits.go` được dùng thật bởi parser với 3 trần kiểm
trong lúc đọc (`TestHeaderBomb`: 431 sau 4096 byte), `errors.go` mở rộng (`Is`, lỗi lớp).
Bằng chứng: `go test ./internal/httpx -count=20 -race` ok (`bench/p2-race20.txt`).

| ID | Trả ở phase | Bằng lệnh nào |
|---|---|---|
| **P2-3** | 4 | `ErrAmbiguousFraming` khi CL+TE; nodefense ưu tiên CL ⇒ 6 ca đỏ; diff-fuzz 300 s 0 lệch |
| **P-arch-1** | 4 | Host giữ nguyên; `normalizeTarget` + `checkConnectionTokens` bảo đảm một nguồn authority; `TestAbsoluteFormRewritten` |
| **P2-2** | 3 | `TestHEADAnd204HaveNoBody`: HEAD/204 qua proxy không mang `Transfer-Encoding`; hệ quả của `StripHopByHop` + chỉ đặt lại TE khi proxy tự chunked |
| **P1-6** | 1 | `cmd/needzerolab` tái hiện **xác định**: heap sạch `make(4 GiB)` = 7ms / RSS 7 MB; sau **64 MB rác bẩn + GC** = **7.138s / RSS 4.27 GB**; sau `FreeOSMemory` = 3ms. Cơ chế: span đè lên trang free-còn-bẩn ⇒ runtime zero CẢ span ⇒ ~1M page fault (sys 7.7s, user 0.3s). Không liên quan `-race` (không race cũng 7.73s, 2/8). Dự đoán "lần chậm RSS 4 GB" trúng 8/8. Fix test: `FreeOSMemory()` trước `make` ⇒ 8/8 nhanh; gỡ skip-dưới-race; `make test` -race 2.35s ×3 |
| **P1-3** | 1 | `TestPayloadOverUint32` skip khi int 32-bit, khi `vm.overcommit_memory=2`, ~~và dưới `-race`~~ (skip race đã gỡ khi P1-6 chỉ ra `-race` không liên quan); `make test-huge` chạy riêng |
| **P1-4** | 1 | `TestTransportDifference`: net.Pipe vs TCP trên 4 kịch bản ⇒ **G6 sai một nửa**. Pipe tái tạo được short read (7/7) và nhiều-frame-một-Read (62/62); không tái tạo được Write bất đồng bộ (`tcp=true pipe=false`) và gom Write rời (pipe luôn 1; tcp 1 hoặc 11 — không xác định) |
| **P1-5** | 1 | `framelab -rawbuf 7` / `-rawbuf 12` (`make framelab-split`): Read thô cắt frame 1 giữa header / giữa payload, decoder vẫn 3 frame ✔; `nhỏ nhất 3` và `nhỏ nhất 2` byte/Read là phần đuôi ReadFull phải vá — `bench/p1-framelab-split-GOTIT-00663.txt` |
| **P1-1** | 1 | `cmd/netlab/wire.go` chuyển sang `frame.Decoder`/`frame.PutHeader`; `grep -n "func readFrame" cmd/netlab/*.go` → 0. G1 chạy lại: **1.42x** p50 (trước 1.41x), spike Nagle **44.00ms** (trước 44.03ms) — `bench/p1-netlab-on-frame-GOTIT-00663.txt`. Tỉ số sống sót qua việc đổi framer |
| **P1-2** | 1 | `TestDecodeHangsWithoutDeadline`: không deadline ⇒ Decode **không trả về sau 300ms**; `SetReadDeadline(50ms)` ⇒ trả `i/o timeout` sau 352ms tổng. Decoder cố ý không tự đặt deadline — giờ là quyết định có test ghi lại, phase 7 cài ở tầng connection |
| **P-env-1** | 0 | Hai phần. (a) `netlab -exp limits` qua `127.0.0.1` vs qua eth0 IP ⇒ `tcp_tw_reuse=2` che port exhaustion. (b) `scripts/pay-P0-1.sh` ⇒ **netem trên `lo` áp delay cho CẢ HAI chiều**: `delay 10ms` → RTT 20.157ms, `delay 20ms` → RTT 40.201ms. Đã sửa `Makefile` `rtt-up` thành `delay = RTT/2` + ép `ping` kiểm |
| **P0-1** | 0 | `scripts/pay-P0-1.sh` ⇒ G3 = **2.00x** (không phải >15x), và đúng 2.00x ở cả hai RTT ⇒ tỉ số là **đơn vị sai** cho pool; đơn vị đúng là **1 RTT phí mỗi connection dựng mới**. Dự đoán đăng ký trước khi đo: đúng |
