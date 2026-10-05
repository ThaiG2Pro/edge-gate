# Bài 12 — Vì sao `httputil.ReverseProxy` tốn CPU gấp 3?

> Series [Mở nắp reverse proxy](README.md) · bài 12/16 · cần đọc trước: [bài 11](11-ram-connection.md)

Bạn cần một reverse proxy nhỏ trong Go. Thư viện chuẩn có sẵn: `httputil.NewSingleHostReverseProxy`,
gắn vào `http.Server`, xong. Cả chương trình `cmd/rpbaseline` của repo này chỉ có chừng ấy, cộng một
dòng chỉnh pool:

```go
rp := httputil.NewSingleHostReverseProxy(target)
rp.Transport = &http.Transport{
	MaxIdleConns:        *idle, // 64. Mặc định 2 thì so pool với không pool
	MaxIdleConnsPerHost: *idle,
	// ...
}
srv := &http.Server{Addr: *listen, Handler: rp /* ... */}
```

Mười dòng, viết bằng Go, chạy trên cùng runtime với EdgeGate. Trước khi đo, nhật ký phase 9 đoán:
nginx (C) nhanh hơn EdgeGate ít nhất 1.5 lần, còn EdgeGate và ReverseProxy (Go với Go) ngang nhau, tỉ số
trong khoảng 0.8–1.25. Cả hai dự đoán đều sai, và sai theo cùng một hướng.

**Cảnh báo về số đo:** mọi số trong bài này đo trên laptop WSL2, proxy ghim **2 core**. Chỉ tỉ số giữa
các cột trong cùng một lượt là đáng tin. Bài này không kết luận gì về nginx trên máy thật.

## Thí nghiệm

Ba proxy, cùng một upstream (`epolllab -impl epoll`, ghim core 2-3, response 1 KiB cố định). Mỗi proxy
ghim core 0-1: EdgeGate và ReverseProxy với `GOMAXPROCS=2`, nginx 1.25 với 2 worker và `keepalive 64` tới
upstream. Client ghim core 4-5, 64 connection keep-alive closed-loop, 6 giây mỗi lượt. CPU của proxy đọc
từ `/proc/<pid>/stat` trước và sau mỗi lượt. Cần Docker (image `nginx:1.25-alpine`):

```bash
make perf-bins
./scripts/cpu-vs-nginx.sh
```

```console
edgegate  closed-loop 64 conn 6.002s: 176265 response, 29366 rps, lỗi 0 · CPU proxy 8770 ms = 49.7 µs/req
nginx     closed-loop 64 conn 6.003s: 203056 response, 33828 rps, lỗi 0 · CPU proxy 10490 ms = 51.6 µs/req
rp        closed-loop 64 conn 6.009s: 57460 response, 9563 rps, lỗi 0 · CPU proxy 10690 ms = 186.0 µs/req
edgegate  closed-loop 64 conn 6.005s: 172545 response, 28736 rps, lỗi 0 · CPU proxy 10700 ms = 62.0 µs/req
nginx     closed-loop 64 conn 6.002s: 196684 response, 32769 rps, lỗi 0 · CPU proxy 9630 ms = 48.9 µs/req
rp        closed-loop 64 conn 6.007s: 56048 response, 9331 rps, lỗi 0 · CPU proxy 11150 ms = 198.9 µs/req
edgegate  closed-loop 64 conn 6.003s: 179099 response, 29835 rps, lỗi 0 · CPU proxy 9540 ms = 53.2 µs/req
nginx     closed-loop 64 conn 6.002s: 183615 response, 30591 rps, lỗi 0 · CPU proxy 10240 ms = 55.7 µs/req
rp        closed-loop 64 conn 6.005s: 58411 response, 9727 rps, lỗi 0 · CPU proxy 11010 ms = 188.4 µs/req
```

(Nguồn: `bench/p9-cpu-vs-nginx.txt`, WSL2, load nền khoảng 5.)

| CPU / request (3 lượt) | EdgeGate | nginx | ReverseProxy |
|---|---|---|---|
| µs/req | 49.7–62.0 | 48.9–55.7 | 186–199 |
| tỉ số so với EdgeGate | 1 | 0.79–1.05 | 3.2–3.7 |

(Tỉ số lấy từ bảng "Số đo" của `diary/phase9.md`.)

ReverseProxy tốn CPU mỗi request gấp **3.2–3.7 lần** EdgeGate, và đạt ít hơn **3.07–3.08 lần** rps
(9 331–9 727 so với 28 736–29 835). EdgeGate và nginx nằm trong cùng một dải nhiễu.

Đo bằng open-loop (bài 1) thì khoảng cách lộ ra ở latency. Cùng 5 000 rps, `make bench-vs-nginx`:

| p99 @ 5 000 rps | EdgeGate | nginx | ReverseProxy | direct (không proxy) |
|---|---|---|---|---|
| lượt 1 (load 3.2) | 3.35 ms | 5.63 ms | 15.87 ms | 1.68 ms |
| lượt 2 (load 6.5–7.6) | 7.98 ms | 4.18 ms | 115.57 ms | 3.57 ms |
| lượt 3 (load 6.4) | 6.03 ms | 7.75 ms | 125.54 ms | 1.56 ms |

(Nguồn: `bench/p9-bench-vs-nginx.txt` cho lượt 1, `bench/p9-bench-vs-nginx-2.txt` cho lượt 2 và 3,
WSL2.)

ReverseProxy tệ hơn EdgeGate 4.7–21 lần ở p99. Ở 10 000 rps nó sập hẳn: p50 **422.51 ms** trong khi
`direct` là 310 µs (`bench/p9-bench-vs-nginx.txt`). EdgeGate so với nginx thì lượt hơn lượt kém, không
nhất quán.

### Hai nghi phạm sai trước khi tìm ra nghi phạm đúng

Trước khi tin con số "gấp 3", nhật ký thô (`diary/phase9-log.md`) loại hai nghi phạm:

- **Upstream nghẽn.** Lần đo đầu dùng `cmd/upstream` (viết bằng `net/http`, log mỗi request): direct 11926,
  edgegate 9257, rp 4700 rps. Upstream thành nút cổ chai của cả ba cột. Đổi sang upstream epoll thì
  lượt đầu của `scripts/rps-vs-nginx.sh` cho direct 38288, edgegate 25683, rp 2262 rps
  (`bench/p9-rps-vs-nginx.txt`). rp vẫn tụt xa.
- **Pool quá nhỏ.** `MaxIdleConnsPerHost` 64 so với 256: 6798 so với 5822 rps. Nới pool không giúp gì.

Rồi tới manh mối kỳ lạ nhất: profile 6 giây của ReverseProxy lúc nó đang chậm cho **110 % CPU trên 2
core**. Nó không hết CPU. Nó đang **chờ** cái gì đó.

### Đếm số lần luồng OS đi ngủ

Linux đếm cho mỗi luồng `voluntary_ctxt_switches`: số lần luồng tự nhường CPU vì không có việc. Cộng
cho mọi luồng của tiến trình proxy, trước và sau một lượt 4 giây, chia cho số request (repo không có
script cho phép đo này, chỉ có output):

```console
edgegate  closed-loop 64 conn 4.002s: 71174 response, 17785 rps, lỗi 0 · ctxsw tự nguyện 10499 = .14/req
rp        closed-loop 64 conn 4.008s: 29055 response, 7249 rps, lỗi 0 · ctxsw tự nguyện 19858 = .68/req
edgegate  closed-loop 64 conn 4.003s: 110661 response, 27642 rps, lỗi 0 · ctxsw tự nguyện 6130 = .05/req
rp        closed-loop 64 conn 4.008s: 29093 response, 7259 rps, lỗi 0 · ctxsw tự nguyện 20254 = .69/req
```

(Nguồn: `bench/p9-rp-ctxsw.txt`, WSL2.)

ReverseProxy: **0.68–0.69** lần luồng ngủ cho mỗi request. EdgeGate: **0.05–0.14**. Gần như mỗi request
đi qua ReverseProxy là một lần một luồng OS ngủ rồi phải được đánh thức.

## Bên trong: một request, ba goroutine

Mở `net/http/transport.go` của Go 1.26.2. Khi Transport dial một connection tới upstream, nó sinh hai
goroutine riêng cho connection đó (dòng 1994-1995): `go pconn.readLoop()` và `go pconn.writeLoop()`.
Khi handler gửi một request, nó không tự ghi lên socket. Nó đẩy request qua hai channel (dòng 2882-2887):
`pc.writech <- writeRequest{...}` cho goroutine ghi, rồi `pc.reqch <- requestAndChan{...}` cho goroutine
đọc, và chờ response trên một channel thứ ba.

```text
ReverseProxy (net/http Transport)
  goroutine handler ──writech──► goroutine writeLoop ──► socket upstream
         ▲                                                      │
         └────────resc──────── goroutine readLoop ◄─────────────┘

EdgeGate
  goroutine của connection: ghi head ─► chép body ─► Flush ─► đọc head response ─► chép về client
```

Mỗi mũi tên channel là một lần goroutine này dừng, goroutine kia chạy. Nếu lúc đó P (bộ xử lý logic của
scheduler Go) không còn goroutine nào khác để chạy, luồng OS đi ngủ trên futex. Request sau phải đánh
thức nó.

EdgeGate làm trọn một request trong goroutine của connection client. Không channel, không hand-off. Đây
là xương sống của `exchange` (`internal/proxy/forward.go`, bỏ hết nhánh lỗi):

```go
uc, ubr, ubw := pc.c, pc.br, pc.bw // connection upstream lấy từ pool
// --- 3. Head + body sang upstream
writeErr := up.WriteHead(ubw)
if hasBody {
	readErr, writeErr = copyBodyT(ubw, req.Body, req.Chunked, req.Trailer)
}
if writeErr == nil {
	writeErr = ubw.Flush()
}
// ...
// --- 4. Head response từ upstream (cùng goroutine, đọc thẳng ubr)
for resp == nil {
	uc.SetReadDeadline(time.Now().Add(s.cfg.UpstreamHeaderTimeout))
	r, err := httpx.ReadResponse(ubr, lim, req.Method)
	// ...
}
// --- 5. Head + body về client
```

Ghi rồi đọc, tuần tự, trên cùng một stack. Thiết kế của Transport có lý do: nó phải phục vụ mọi kiểu
client, kể cả client đọc response trong lúc vẫn đang gửi body, hoặc huỷ request giữa chừng. EdgeGate chỉ
là proxy, nên được phép đơn giản hơn.

### Vì sao một lần ngủ lại đắt thế trên máy này

Cùng phase, một phát hiện phụ: `direct` (client nói thẳng với upstream, không proxy) trên **1 connection
tuần tự** chỉ đạt 1 295 rps, tức **770 µs mỗi request** trên loopback. Ở concurrency 1, edgegate 374 và
rp 352 rps, gần nhau (`diary/phase9-log.md`). Open-loop `direct` còn cho latency **giảm** khi tải tăng:
p50 640 µs ở 2 000 rps nhưng 310–350 µs ở 10 000–30 000 rps (`diary/phase9.md`).

vCPU rỗi của WSL2 ngủ sâu, và đánh thức nó tốn hàng trăm micro giây. Một kiến trúc làm luồng OS ngủ 0.68
lần mỗi request sẽ trả cái giá đó gần như mỗi request. Trên máy thật, cái giá đánh thức có thể nhỏ hơn
nhiều, nên tỉ số 3 lần có thể không giữ nguyên.

Ở đây số mới chỉ cho thấy **tương quan**, chưa phải nhân quả. Nợ P9-5 trong `docs/debts.md` ghi đã chạy
`go tool trace` trên Linux thuần, và `runtime.chansend1` cùng `runtime.systemstack_switch` nằm trong
những dòng lớn nhất của scheduler delay (chưa có output thô trong repo). Bảng ba cột chạy lại trên Linux
thuần (nợ P9-4) cũng chỉ có ở `docs/debts.md` (chưa có output thô trong repo).

### Chính EdgeGate cũng trả giá này

Phase 10 thêm HTTP/2 cho EdgeGate, và với h2 thì không tránh được hand-off: một goroutine đọc frame
của connection, mỗi stream một goroutine handler. Nhật ký đoán h2 tốn CPU gấp ít nhất 1.3 lần h1 và context
switch gấp ít nhất 2 lần. Đo bằng `h2lab -mode cpu` (sau `make h2-bins`) ở hai mức tải:

```console
$ taskset -c 4-5 ./bin/h2lab -mode cpu -rounds 3 -dur 10s          # h2 8 conn × 8 stream vs h1 64 conn
  lượt 1 h2: 192105 req (19202 rps), CPU proxy 13.01 s = 67.7 µs/req, ctxsw 0.136 /req
  lượt 1 h1: 213059 req (21298 rps), CPU proxy 11.21 s = 52.6 µs/req, ctxsw 0.163 /req
$ taskset -c 4-5 ./bin/h2lab -mode cpu -rounds 3 -dur 10s -h2conns 1 -h2streams 1 -h1conns 1
  lượt 1 h2: 18381 req (1838 rps), CPU proxy 6.94 s = 377.6 µs/req, ctxsw 8.260 /req
  lượt 1 h1: 33077 req (3308 rps), CPU proxy 5.35 s = 161.7 µs/req, ctxsw 4.720 /req
```

(Nguồn: `bench/p10-cpu.txt`, `bench/p10-cpu-1x1.txt`, WSL2, mỗi file ba lượt.)

Khi proxy bận liên tục, h2/h1 CPU chỉ 0.98–1.29 lần, context switch 0.83–1.03 lần: dự đoán sai. Khi tải
thưa (một stream), CPU 1.78–2.34 lần, context switch 1.66–1.75 lần (`diary/phase10.md`). Lý do:
`voluntary_ctxt_switches` đếm lần **luồng OS** ngủ, không đếm lần chuyển goroutine. Lúc lúc nào cũng có
việc, hand-off chỉ chuyển goroutine trên cùng một luồng đang thức, gần như miễn phí. Lúc tải thưa, mỗi
hand-off đánh thức một luồng đang ngủ.

## Mang về dùng

1. **Kiến trúc thắng ngôn ngữ.** Dự đoán "C nhanh hơn Go, Go bằng Go" sai cả bốn vế. Khoảng cách lớn nhất
   trong bài nằm giữa hai chương trình Go, và nó đến từ số goroutine mà một request phải đi qua.
2. **Khi profile, đếm context switch chứ không chỉ đếm CPU.** Một proxy chậm mà CPU chưa đầy là một proxy
   đang chờ. `voluntary_ctxt_switches` chia cho số request là một con số rẻ để lấy, và nó chỉ ngay ra chỗ
   chờ đó.
3. **Giá của hand-off phụ thuộc độ bận, không phải hằng số.** Cùng một kiến trúc có thể gần như miễn phí ở
   tải cao và đắt gấp đôi ở tải thưa. Đo ở cả hai mức trước khi kết luận.

---

Số đo gốc: `bench/p9-cpu-vs-nginx.txt`, `bench/p9-rp-ctxsw.txt`, `bench/p9-bench-vs-nginx*.txt`,
`bench/p10-cpu*.txt` · code: [`cmd/rpbaseline/main.go`](../cmd/rpbaseline/main.go),
[`internal/proxy/forward.go`](../internal/proxy/forward.go) (`exchange`) · nhật ký:
[`diary/phase9.md`](../diary/phase9.md) (G8), [`diary/phase10.md`](../diary/phase10.md) (G8).

**Bài tiếp theo:** [Bài 13: HTTP/2 có nhanh hơn HTTP/1.1 không?](13-http2.md)
