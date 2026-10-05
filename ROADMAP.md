# EdgeGate — Roadmap học network internals bằng Go

> Mục tiêu: không học "cấu hình nginx", mà học **invariant** và **failure mode** của một
> reverse proxy. Sau roadmap này, đọc `nginx.conf` hay Envoy xDS bằng trực giác của người đã
> tự viết ra nó.

**Nguyên tắc xuyên suốt:** mỗi phase kết thúc bằng **một load-test, một fuzz, hoặc một bài
phản chứng bắt buộc đỏ** chứng minh bạn hiểu — không phải bằng "code chạy được".

**Nguyên tắc thứ hai, riêng của project này:** vertical slice trước, đào sâu sau. Phase 3 phải
`curl` được xuyên proxy. Mọi phase sau đó chỉ làm **một** đường đi đã chạy trở nên đúng và
nhanh. Không ai được phép viết parser hoàn hảo trong 3 tháng mà chưa proxy nổi một request.

Môi trường: Go 1.26.2, Linux (WSL2). Không dùng `net/http` cho đường dữ liệu — chỉ `net`.

---

## Kiến trúc mục tiêu (bottom-up)

```
Observability      (phase 9)    <- pprof, metrics, access log
Resiliency         (phase 7)    <- timeout, rate limit, circuit breaker, shed
TLS termination    (phase 8)    <- SNI routing, ALPN, cert reload
Load balancing     (phase 6)    <- RR / least-conn / P2C+EWMA / consistent hash, health
Upstream pool      (phase 5)    <- keep-alive tới backend, reuse, sạch-connection
Proxy core         (phase 3-4)  <- forward, hop-by-hop, XFF, smuggling defense
HTTP/1.1 engine    (phase 2)    <- request/response parser, chunked, keep-alive
Framing            (phase 1)    <- length-prefix, ReadFull, trần frame
TCP socket         (phase 0)    <- fd, handshake, RTT, syscall, backlog
```

Hướng thiết kế: **goroutine-per-connection + parse-and-reserialize** = nginx/Envoy ở tầng L7.
Hướng còn lại (**event loop thủ công + splice zero-copy** = HAProxy ở tầng L4) để dành cho
phase 9 — và ở đó ta sẽ đo được **cái giá của chính lựa chọn L7** này.

---

## Bảng tổng quan

| Phase | Nội dung | Thời lượng | Deliverable chứng minh hiểu |
|---|---|---|---|
| 0 | ✅ Nền tảng vật lý mạng: syscall, RTT, handshake, coordinated omission, giới hạn OS | 0.5 ngày | ✅ [`diary/phase0.md`](diary/phase0.md) — **5/8 giả thuyết sai**, 4 lần bộ đo sai rồi tự sửa |
| 1 | ✅ Framing: custom binary protocol, `io.ReadFull`, trần frame | 0.5 ngày | ✅ [`diary/phase1.md`](diary/phase1.md) — **4/7 giả thuyết sai**, phản chứng I2 đỏ đúng chỗ (4294967376 byte), fuzz 1.58M execs sạch |
| 2 | ✅ HTTP/1.1 engine: request + **response** parser, chunked, keep-alive | 2-3 ngày | [`diary/phase2.md`](diary/phase2.md) — diff-fuzz 300s **0 lệch, nhưng** bảng đối chiếu tay tìm ra 2 lệch thật fuzzer không thấy (G1 ❌); phản chứng bộ so đỏ đúng chỗ; header bomb chặn sau 4096 B |
| 3 | ✅ **Vertical slice**: `curl` xuyên proxy tới upstream và về | 1-2 ngày | ✅ [`diary/phase3.md`](diary/phase3.md) — `make proxylab` 4 curl đúng, phản chứng bẫy #2/#3 đỏ; **G4 sai hai lần** (flag `-nodelay=false` là no-op vì Go mặc định NODELAY; Nagle là **sàn 44 ms**, không phải +40 ms); overhead L7 3.39x, keep-alive client chỉ 1.44x vì D1 |
| 4 | ✅ **RFC compliance & smuggling**: hop-by-hop, CL vs TE, XFF trust, trần | 2-3 ngày | ✅ [`diary/phase4.md`](diary/phase4.md) — 61 ca `testdata/smuggle` xanh hai tầng, `make smugglelab-nodefense` đỏ 20/52; **3/7 giả thuyết sai**: `net/http` chỉ cùng từ chối 46 % (Go nhận CL.TE, bare LF, trailer mang CL — đúng RFC, và đó là việc proxy phải chặn thay); kiểm tra thêm 6.5 % CPU, ns/op trên WSL2 không đo được 5 % |
| 5 | ✅ **Upstream connection pool**: keep-alive tới backend | 2 ngày | ✅ [`diary/phase5.md`](diary/phase5.md) — `make poollab`: RTT 0 **5.34x / 0.96 ms**, RTT 20 ms **1.37x / 17.9 ms = 0.87 RTT** — cặp số ngược với "~1.1x / >15x" viết ở đây, đúng như phase 0 dự báo: tỉ số tụt 4x, khoản tiết kiệm tăng 19x; **G1 sai** (dial hôm đó 834 µs, không 300), G2 sát biên; phản chứng `poollab-nodefense` đỏ 20/20; probe `MSG_PEEK` bắt 50/50 FIN lặng lẽ |
| 6 | ✅ **Load balancing**: RR / least-conn / **P2C+EWMA** / consistent hash + health | 2-3 ngày | ✅ [`diary/phase6.md`](diary/phase6.md) — **4/7 giả thuyết sai**: least-conn cắt node chậm 25 → 4.3 % mà p99 **không đổi** (0.98x; p99 chỉ giảm khi node xấu < 1 % tải), P2C → 0.6 % ⇒ p99 tốt **3.4x**; least-conn dồn node 503 nhanh 39 % (5xx 11.3 % vs RR 7.5 %); **chỗ P2C thua** không phải node xấu đi (inflight che EWMA cũ) mà node **hồi phục**: tau 30 s cho nó **0.0 %** vs least-conn 25 %; ring 150 vnode đổi 21.6 % key vs modulo 80.1 %; phản chứng decay theo request đỏ 0/200 |
| 7 | ✅ **Resiliency**: timeout, Slowloris, token bucket, circuit breaker, shed, drain | 2-3 ngày | ✅ [`diary/phase7.md`](diary/phase7.md) — `make chaoslab` 60 s ×2 **4/4 invariant** (goroutine 1=1, fd 7=7, 0 treo); **6/9 giả thuyết sai một vế**: Slowloris **không** giết Go (probe 100 % kể cả tắt HeaderTimeout, giá 20.7 KiB/conn) — **trần connection** mới giết (100 % treo); shed 2x capacity p99 11.6 s → **33 ms** (352x), goodput như nhau; retry budget 1.49x → 1.12x; half-open 8 → **1** request; drain đóng rỗi ngay mất ~99 % request kế ⇒ drain lười **0** mất; bug phase 3: `Close` không đóng upstream đang dùng |
| 8 | ✅ **TLS termination + SNI routing** | 1-2 ngày | ✅ [`diary/phase8.md`](diary/phase8.md) — 2 vhost 2 cert, 0/1000 lệch; SNI a + `Host: b` ⇒ **421** (phản chứng: 200 từ B dưới cert A); reload 96 lần dưới 64 conn **0 lỗi**; **2/8 giả thuyết sai một vế**: RTT 20 ms — TLS 1.3 +1 RTT, **resumed 1.3 cũng +1** (resumption cắt 0 RTT ở 1.3, Go không nhận 0-RTT trên TCP), 1.2 +2 → resumed +1; pool tới upstream TLS tiết kiệm **2.03-2.14 RTT** vs 0.97-1.17 (≈ 2x); mật mã chỉ 20-30 % CPU handshake, ML-KEM mặc định +0.25 ms |
| 9 | ✅ **Performance**: `sync.Pool`, splice trade-off, pprof, epoll vs netpoller | 2-3 ngày | ✅ [`diary/phase9.md`](diary/phase9.md) — **3/8 giả thuyết sai**: pool cắt B/op 43x mà ns/op **2.4x** (không ≤ 15 %) vì giá là **tần suất GC** (heap sống nhỏ ⇒ GC mỗi ~46 request), CPU/req 169 → 116 µs; connection rỗi 28 → **8-9 KiB**; **giá L7 với body 10 MiB = mất splice** (L4 1.76-2.19x, EdgeGate L7 ≈ L4 copy) và **lấy lại được** — splice body sau parse head 0.92-1.01x L4; epoll tự viết **55x** ít RAM/conn rỗi, rps ngang (netpoller là epoll); EdgeGate ≈ nginx (~50 µs CPU/req, WSL2 — chưa chạy Linux thuần), `ReverseProxy` 3.1x chậm hơn (0.68 context switch/req) |
| 10 | ✅ (tùy chọn) HTTP/2 h2c: HPACK, stream multiplexing | 3-4 ngày | ✅ [`diary/phase10.md`](diary/phase10.md) — **h2spec 143 → 145/145** qua TLS/ALPN (P10-5) và `h2c_only` (P10-6); 144/145 mặc định chung port (ca còn lại cố ý); **2/8 giả thuyết sai/nửa sai**: HOL tầng TCP (loss 2 %) làm h2 tệ **2.7x ở trung vị, 1.1x ở đuôi** — dời giá mất gói sang mọi stream; giá hand-off goroutine phụ thuộc độ bận (CPU h2/h1 1.23x khi bận, ~2x khi thưa). HPACK 318 → 12 byte; window 64 KiB @RTT 40 ms = 1.5 MB/s; Rapid Reset 100 vs 5 000 handler, CONTINUATION 64 KiB vs 4 MiB, H2.CL smuggling chặn (CRLF/TE bị lớp phase 4 chặn sẵn) |

Tổng ~5-7 tuần với 2-3h/ngày.
**Bắt buộc: phase 0-6.** Phase 4 là phase ăn điểm phỏng vấn nhiều nhất. Phase 7 là phần nâng
bạn từ "viết được proxy" lên "vận hành được proxy".

---

## Phase 0 — Nền tảng vật lý mạng (0.5 ngày)

Chưa code proxy. Hiểu 5 sự thật quyết định **mọi** thiết kế về sau:

1. **Một syscall tốn bao nhiêu.** `read`/`write` ~1-2µs. Ghi header rồi ghi body thành 2 lần
   `Write` = 2 syscall + nguy cơ 2 packet ⇒ đây là toàn bộ lý do `bufio.Writer` tồn tại trong
   một proxy, và lý do **phải `SetNoDelay(true)`**.
2. **RTT là đơn vị tiền tệ của mạng.** TCP handshake = 1 RTT trước khi gửi được byte đầu.
   TLS 1.3 = thêm 1 RTT. Trong datacenter RTT ~0.5ms, xuyên vùng ~50ms. **Connection pool
   không phải tối ưu, nó là cách không trả 1-2 RTT mỗi request.** Trên loopback RTT ~30µs nên
   sự thật này **vô hình** — đó là cái bẫy số 1 của cả roadmap.

   **Đã đo (phase 0), và nó lật ngược cách báo cáo:** tỉ số `dial/reuse` = 36.69x ở RTT ~0 nhưng
   **2.00x** ở RTT 20ms *và* 2.00x ở RTT 40ms. Tỉ số **tiệm cận 2.0 rồi dừng** (dial trả 2 RTT,
   reuse trả 1 RTT), nên nó **không** đo lợi ích của pool. Lượng phí phạm tuyệt đối thì tăng
   tuyến tính: 0.33ms → 20.47ms → 40.47ms. Đơn vị đúng là **1 RTT phí cho mỗi connection dựng
   mới**. Báo cáo "pool nhanh hơn 36x" là sai; đúng là "pool bỏ được 1 RTT/request".
3. **Coordinated omission.** `ab`/`wrk`/`hey` chạy closed-loop: proxy chậm đi thì chúng gửi ít
   đi, nên tail latency **tự bốc hơi**. Mọi con số p99 từ closed-loop dưới tải cao đều là số
   nói dối. Phải dùng open-loop rate-fixed (`vegeta -rate`, `wrk2 -R`).
4. **Giá của một connection rỗi.** Goroutine stack khởi tạo 2KB (`runtime._StackMin`), nhưng
   chi phí thật là `bufio.Reader` + `bufio.Writer` (4KB mỗi cái theo mặc định). 10k connection
   rỗi ⇒ ~100MB **nếu buffer cấp ngay khi accept**. Đây là lý do phase 9 cần `sync.Pool`, và
   lý do nginx dùng buffer nhỏ hơn nhiều.
5. **Trần thật nằm ở OS, không ở code.** `ulimit -n` (fd), `ip_local_port_range` (~28k ephemeral
   port), `TIME_WAIT` 60s, `somaxconn` (accept backlog). Test 10k connection thất bại vì `ulimit`
   1024 thì không nói được gì về proxy.

**Bài tập:** viết `cmd/netlab` đo 5 nhóm: (a) chi phí syscall `Write` 1 lần vs 2 lần,
(b) `Dial` mới vs reuse connection, ở RTT 0 và RTT 20ms, (c) rps trần khi không pool → xem có
đụng ephemeral port exhaustion không, (d) RSS mỗi connection rỗi ở 1k/10k conn,
(e) cùng một tải, đo bằng closed-loop và open-loop, **so hai cái p99**.

**Bẫy đã biết trước, phải để lại dấu vết trong nhật ký:** (b) trên loopback sẽ ra tỉ số ~1.1x
và nhìn như "pool vô dụng". Đó là bench sai môi trường, không phải kết luận. Chạy lại với
`tc qdisc add dev lo root netem delay 20ms`.

## Phase 1 — Framing: TCP không có ranh giới tin nhắn (1 ngày)

- `net.Listen("tcp", ...)`, goroutine mỗi connection. **Không** `net/http`.
- Custom binary protocol: `[magic uint32][version uint8][type uint8][length uint32][payload]`.
- **`conn.Read` trả về ÍT hơn số byte bạn cần là chuyện bình thường, không phải lỗi.**
  Dùng `io.ReadFull`. Đây là bug số 1 của mọi người viết socket lần đầu.
- Ngược lại: một lần `Read` có thể trả về **nhiều frame dính nhau** (packet coalescing do Nagle
  hoặc do sender ghi liên tiếp). Parser phải là vòng lặp trên buffer, không phải "1 read = 1 frame".

**Invariant:** `length` phải có **trần** (`MaxFrameSize`) và trần đó phải được kiểm **trước khi
`make([]byte, length)`**. Đọc `length = 0xFFFFFFFF` rồi cấp phát 4GB là một dòng code giết
process — và là lỗ hổng thật, không phải giả thuyết.

**Test quyết định:** hai bài, cả hai phải xanh:
- **Sender nhỏ giọt:** gửi 1 byte mỗi 10ms. Parser phải ghép đúng, không lỗi.
- **Sender dính gói:** ghi 3 frame trong **một** lần `Write`. Parser phải tách đúng 3.

**Fuzz:** `FuzzFrameDecode` — mọi byte string không được panic, không được cấp phát quá trần.

## Phase 2 — HTTP/1.1 engine (2-3 ngày)

Phase dài, và là nền của mọi thứ sau. Ba nửa người ta hay bỏ, đừng bỏ:

1. **Response parser, không chỉ request parser.** Proxy phải đọc response của upstream. Bỏ nửa
   này là lý do 90% proxy tự viết bị **treo vĩnh viễn** (xem phase 3).
2. **Chunked decoder.** Backend `net/http` gửi chunked mỗi khi handler không set `Content-Length`.
3. **Quy tắc framing body** — thứ tự ưu tiên, phải đúng y RFC 9112 §6:
   - Response cho `HEAD`, hoặc status `1xx`/`204`/`304` ⇒ **không có body**, kể cả khi có `Content-Length`.
   - `Transfer-Encoding: chunked` ⇒ chunked, và **`Content-Length` bị bỏ qua** (phase 4 sẽ đổi
     thành **từ chối**, vì bỏ qua chính là lỗ hổng smuggling).
   - `Content-Length: n` ⇒ đúng n byte.
   - Không có gì ⇒ **request: body rỗng; response: đọc tới EOF**. Hai bên **không** đối xứng.
     Nhầm chỗ này = treo.

Cấu trúc dữ liệu, quyết ngay từ dòng đầu:

```go
type Header map[string][]string   // KHÔNG map[string]string
```

Field name HTTP không phân biệt hoa/thường **và** lặp lại được (`Set-Cookie`, `Via`,
`X-Forwarded-For`). Dùng `map[string]string` là sai kiến trúc, và tới phase 4 sẽ phải viết lại
toàn bộ. Canonical hoá bằng `textproto.CanonicalMIMEHeaderKey` (lưu ý `"TE"` → `"Te"`).

**Deliverable — differential fuzzing, mạnh hơn "không panic" rất nhiều:**

```go
func FuzzAgainstNetHTTP(f *testing.F) {
    f.Fuzz(func(t *testing.T, data []byte) {
        mine, errMine := httpx.ReadRequest(bufio.NewReader(bytes.NewReader(data)), lim)
        theirs, errTheirs := http.ReadRequest(bufio.NewReader(bytes.NewReader(data)))
        // Không đòi hai bên giống nhau về mọi thứ. Chỉ đòi MỘT điều:
        // nếu CẢ HAI nhận request, chúng phải đồng ý về SỐ BYTE của body.
        // Lệch một byte = proxy và backend nhìn thấy hai request khác nhau = smuggling.
        if errMine == nil && errTheirs == nil {
            requireSameBodyLength(t, mine, theirs)
        }
    })
}
```

**Câu hỏi phải trả lời được:** vì sao keep-alive tiết kiệm được đúng 1 RTT chứ không hơn?
Pipelining khác keep-alive ở đâu, và vì sao thực tế không ai bật pipelining?

## Phase 3 — Vertical slice: chạy thông mạch (1-2 ngày)

Mục tiêu duy nhất: `curl http://localhost:8080/hello` trả về đúng dữ liệu từ upstream, và
`curl -X POST -d @file` cũng đúng. Xấu, thiếu tính năng, **một** backend hardcode. Nhưng chạy.

Đường đi: accept → parse request → chọn upstream (hardcode) → `Dial` → serialize request →
parse response → serialize về client.

**Ba cái bẫy sẽ giết bạn trong buổi đầu, biết trước để khỏi mất một ngày:**

1. **`io.Copy(upstream, clientConn)` treo vĩnh viễn.** Client giữ keep-alive nên không bao giờ
   EOF. Phải `io.CopyN(upstream, br, contentLength)`.
2. **`io.Copy(clientConn, upstreamConn)` cũng treo vĩnh viễn**, cùng lý do ở phía upstream.
   Phải **parse response** rồi framing body theo phase 2. Đây là lý do phase 2 buộc phải có
   response parser — không đẩy sang sau được.
3. **Đọc body xong mới được dùng lại connection.** Nếu bỏ sót byte body của request trước,
   những byte đó sẽ bị hiểu là dòng đầu của request sau. Trên connection client thì đó là bug;
   ở phase 5, trên connection **pool dùng chung**, đó là lỗ hổng: response của người này trả
   cho người khác.

**Lối tắt hợp pháp cho phase này:** gửi `Connection: close` sang upstream, `Dial` mới mỗi
request. Nhờ đó body-tới-EOF luôn đúng và chưa cần pool. Phase 5 sẽ tháo lối tắt này — và
lúc đó bẫy #3 mới thành lỗ hổng thật.

## Phase 4 — RFC compliance & request smuggling (2-3 ngày) — phase ăn điểm nhất

Đây là phase biến "đồ chơi" thành thứ kể được trong phỏng vấn.

### Bất biến số 1 của một proxy

> **Proxy và upstream phải luôn đồng ý về ranh giới của mỗi request.**

Mọi lỗ hổng smuggling là một cách phá bất biến này. Hệ quả trực tiếp về thiết kế:
**khi mơ hồ thì từ chối, không đoán.** Một proxy "khoan dung" là một proxy có lỗ hổng, vì nó
khoan dung theo cách khác với backend.

### Danh sách phòng tuyến

- **CL + TE cùng có** ⇒ `400`, đóng connection. (RFC 9112 §6.1 cho phép bỏ qua CL; **đừng** —
  bỏ qua là CL.TE.)
- **`Transfer-Encoding` có value lạ** (`chunked, chunked`, `xchunked`, `chunked ` có space,
  `Transfer-Encoding : chunked` có space trước dấu hai chấm) ⇒ `400`/`501`.
- **Nhiều `Content-Length`**, hoặc CL không phải chữ số thuần (`+5`, `0x5`, ` 5`, `5 `) ⇒ `400`.
- **Bare LF thay vì CRLF** ⇒ chọn strict, và **ghi lại lựa chọn**. Nguồn gốc smuggling là proxy
  strict + backend lenient (hoặc ngược lại).
- **Hop-by-hop strip** — 7 field của RFC 9110 §7.6.1: `Connection`, `Proxy-Connection`,
  `Keep-Alive`, `TE`, `Trailer`, `Transfer-Encoding`, `Upgrade`; cộng `Proxy-Authenticate` /
  `Proxy-Authorization` (§11.7, cơ chế khác nhưng cũng không forward); **cộng mọi field được
  liệt kê trong value của `Connection:`**. Bỏ sót cái cuối là bug kinh điển.
- **Trần:** `MaxLineBytes` 8KB, `MaxHeaderBytes` **64KB** (8KB làm vỡ traffic thật: JWT +
  cookie enterprise vượt 8KB rất dễ; nginx thực tế cho 32KB, Go stdlib 1MB), `MaxHeaderCount`
  100 (chống "1 triệu header 1 byte" — lọt qua trần tổng byte nhưng làm nổ map).
- **`X-Forwarded-For` phải APPEND, không overwrite** — và phải có **trust boundary**: XFF do
  client tự gửi là dữ liệu **không tin được**. Quyết định: chỉ tin XFF khi peer nằm trong
  `trusted_proxies`, ngược lại thay bằng `X-Real-IP` = peer thật. Bỏ qua chỗ này ⇒ rate limiter
  của phase 7 bị bypass bằng một header giả.
- **`Host`:** giữ nguyên hay đổi sang upstream? Hai lựa chọn hợp lệ, hệ quả khác nhau về virtual
  hosting. Ghi lựa chọn vào nhật ký, đừng để nó là tình cờ.
- **Response cũng phải kiểm.** Response có cả CL và TE là response splitting. Header value chứa
  CR/LF là header injection. Không tin upstream hơn tin client.

**Test quyết định:** bộ payload `testdata/smuggle/*.txt` (20+ ca, lấy từ RFC + PortSwigger),
mỗi ca kèm status mong đợi. **Và bài phản chứng bắt buộc đỏ:**
`make smugglelab-nodefense` phải **fail** — nếu tắt phòng tuyến mà bộ test vẫn xanh thì bộ test
không chứng minh gì cả.

## Phase 5 — Upstream connection pool (2 ngày)

- `map[upstreamAddr]chan *pooledConn`, hoặc slice + mutex. `MaxIdlePerHost`, `MaxIdleTime`.
- **Invariant sống còn:** một connection chỉ được trả về pool khi nó **sạch** — body của
  response trước đã đọc hết, không còn byte thừa trong `bufio.Reader`. Nếu không: request kế
  tiếp (**của người dùng khác**) đọc phải phần đuôi của response cũ. Đây là lớp lỗ hổng thật đã
  có CVE ở nhiều proxy. Quy tắc thực thi: khi có bất kỳ nghi ngờ nào (lỗi parse, timeout giữa
  body, client bỏ đi giữa dòng) thì **đóng connection, không trả về pool**. Đóng một connection
  là rẻ; trả về một connection bẩn là rò rỉ dữ liệu.
- Upstream đóng connection rỗi **lặng lẽ**: connection lấy từ pool có thể đã chết mà `Read`
  chưa báo. Cần retry **đúng một lần** khi lỗi xảy ra *trước khi* gửi được byte nào — và
  **không** retry khi đã gửi body (không idempotent).

**Bench quyết định — hai con số, và chúng rất khác nhau:**

```bash
make poollab                 # RTT 0 (loopback trần)
make poollab-rtt             # RTT 20ms qua tc netem
```

Kỳ vọng: RTT 0 cho ~1.1x (**pool trông vô dụng**), RTT 20ms cho >15x. Chính cặp số này là câu
trả lời cho "vì sao connection pooling quan trọng" — và là bằng chứng bạn hiểu **coordinated
omission và loopback** chứ không chỉ copy kết luận từ blog.

## Phase 6 — Load balancing + health (2-3 ngày)

Bốn thuật toán, cài cả bốn để **so được**:

1. **Round Robin** — baseline.
2. **Least Connections** — và hiểu nó **vẫn dồn traffic vào node chậm**: node chậm trả response
   chậm ⇒ inflight cao ⇒ bị tránh; nhưng node **lỗi nhanh** thì inflight thấp ⇒ bị dồn vào.
3. **P2C + EWMA** (Power of Two Random Choices) — cái Finagle/Envoy dùng thật. Chọn ngẫu nhiên
   2 node, so `ewmaLatency × (inflight+1)`, lấy node tốt hơn.
   **Bẫy phải cài đúng:** EWMA phải **decay theo thời gian**, không theo số request:
   ```go
   decay := math.Exp(-elapsed.Seconds() / tau.Seconds())
   e.value = e.value*decay + sample*(1-decay)
   ```
   Nếu chỉ update khi có request thì node bị điểm xấu sẽ **không bao giờ nhận request nữa ⇒
   không bao giờ hồi phục**. Và phải xử lý cold-start (chưa có sample) — cho điểm 0 để node mới
   được thử.
4. **Consistent hashing + virtual node** (~150 vnode/backend), **không** `hash(ip) % N`. Câu hỏi
   tự chấm: modulo thì thêm 1 node vào 4 node làm rehash bao nhiêu phần trăm key? (~80%, không
   phải 20%.) Đó là toàn bộ lý do consistent hashing tồn tại.

Sức khoẻ backend, **hai cơ chế song song**, không thay thế nhau:
- **Active health check** (goroutine nền, probe định kỳ) — phát hiện node chết khi không có traffic.
- **Passive outlier ejection** — đếm lỗi trên traffic thật, eject tạm rồi cho về từ từ. Phát
  hiện nhanh hơn active, và bắt được cả node "sống nhưng trả 5xx".

**Bench quyết định:** 4 backend, **cố ý lệch** (1 node chậm 10x, 1 node trả 5xx 30%). Bảng p99
của 4 thuật toán. **Và cột "chỗ P2C thua"** — P2C với `tau` sai (quá dài) phản ứng chậm hơn
least-conn; ghi lại con số đó, đừng chỉ ghi cái thắng.

## Phase 7 — Resiliency (2-3 ngày)

- **Timeout, đủ 5 loại, thiếu một là một lỗ hổng:** `HeaderTimeout` (chống Slowloris),
  `BodyTimeout`, `IdleTimeout` (keep-alive rỗi), `UpstreamDialTimeout`,
  `UpstreamResponseTimeout`. Không có deadline = connection sống mãi = Slowloris.
- **Slowloris thật:** viết `cmd/slowlab` mở 500 connection, mỗi cái gửi 1 byte header mỗi 10s.
  Đo: proxy còn phục vụ được request bình thường không? Trước khi có `HeaderTimeout` thì không.
- **Rate limiter — token bucket per-IP**, không phải per-connection. Và **phải dùng IP đã qua
  trust boundary của phase 4**, nếu không thì bypass bằng `X-Forwarded-For: 1.2.3.4`.
  Bẫy bộ nhớ: map per-IP không bao giờ dọn = rò rỉ có chủ đích được ⇒ cần LRU/TTL có trần.
- **Circuit breaker** 3 trạng thái (closed → open → half-open). Bẫy: half-open phải cho **đúng
  một** request thử, không phải "mở lại hết" — mở lại hết đúng lúc backend vừa hồi là cách giết
  nó lần hai.
- **Retry budget**, không phải "retry 3 lần". Retry mù khuếch đại sự cố: backend quá tải nhận
  gấp 3 tải. Giới hạn retry ≤ 10% tổng request (cách Finagle làm).
- **Backpressure và load shedding:** khi upstream chậm, ai chịu? Bounded queue + trả `503`
  ngay, **không** buffer vô hạn. Đo: dưới tải 2x capacity, p99 của request **được nhận** phải
  vẫn tốt — đó là ý nghĩa của shedding.
- **Graceful drain** trên `SIGTERM`: ngừng accept, `Connection: close` cho request đang chạy,
  chờ tối đa N giây rồi mới thoát. Đo: rolling restart mất 0 request.

**Test quyết định:** `make chaoslab` — backend bị kill/chậm/flap ngẫu nhiên trong 60s, kiểm
invariant: (a) proxy không panic, (b) không rò goroutine (`runtime.NumGoroutine()` về mức nền),
(c) không rò connection (`ss -tan | grep -c ESTAB` về mức nền), (d) mọi request đều nhận được
**một** response hoặc **một** lỗi rõ ràng — không treo.

Rò goroutine là bệnh đặc trưng của proxy Go. Chạy `-race` và kiểm `NumGoroutine` là hai lệnh
phát hiện nó.

## Phase 8 — TLS termination + SNI routing (1-2 ngày)

Không có TLS thì chưa phải "nginx thu nhỏ" — ~40% cấu hình nginx thực tế là TLS.

- `crypto/tls` + `tls.Config.GetConfigForClient`: dựa `ClientHelloInfo.ServerName` (SNI) chọn
  cert theo từng vhost — đúng cái `server_name` của nginx làm.
- ALPN (`NextProtos: []string{"http/1.1"}`) — chỗ móc cho phase 10 (h2).
- **Cert hot-reload không đứt connection:** `GetCertificate` đọc từ một `atomic.Pointer`,
  fsnotify/`SIGHUP` chỉ swap con trỏ. Connection đang chạy không bị ảnh hưởng.
- Đo: TLS handshake tốn bao nhiêu RTT, và **session resumption** cắt được bao nhiêu. Đây là
  chỗ phase 5 (pool) được nhân đôi ý nghĩa: pool tới upstream TLS tiết kiệm 2 RTT, không phải 1.

Phase này khá độc lập — dịch lên trước phase 6 được nếu bạn muốn có HTTPS sớm.

## Phase 9 — Performance & epoll (2-3 ngày)

- **`sync.Pool` cho buffer.** Đo bằng `go test -bench . -benchmem`, ghi `allocs/op` **trước và
  sau**. Bẫy: `sync.Pool` giữ object có capacity lớn bất thường ⇒ rò bộ nhớ; phải drop buffer
  quá to khi `Put`.
- **`sync.Pool` KHÔNG phải zero-copy.** Nó là buffer reuse. Zero-copy thật trên Linux là
  `splice(2)`, và Go `io.Copy` **đã tự dùng splice** cho TCP→TCP (`(*TCPConn).ReadFrom`).
  **Và đây là đánh đổi trung tâm của cả project:** một khi bạn parse-and-reserialize HTTP,
  bạn **mất splice**. Đo cái giá đó: cùng payload 10MB, `io.Copy` thuần (L4) vs qua parser (L7).
  Tỉ số đó chính là câu trả lời cho "vì sao L4 LB nhanh hơn L7 LB" — và nói được nó giá trị hơn
  cả tuần tối ưu GC.
- **pprof:** `go tool pprof -http=:6060 cpu.prof`, flamegraph trước/sau. Ghi con số thật, không
  ghi con số mong muốn.
- **Thí nghiệm epoll vs netpoller.** Roadmap gốc đặt mục tiêu "hiểu epoll" nhưng deliverable
  toàn `net.Listen` + goroutine ⇒ **không chạm epoll một dòng nào**, vì Go runtime netpoller đã
  bọc kín nó (`runtime/netpoll_epoll.go`). Muốn thật thì phải tự làm:
  ```go
  fd, _ := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_NONBLOCK, 0)
  ep, _ := unix.EpollCreate1(0)
  unix.EpollCtl(ep, unix.EPOLL_CTL_ADD, fd, &unix.EpollEvent{Events: unix.EPOLLIN, Fd: int32(fd)})
  n, _ := unix.EpollWait(ep, events, -1)
  ```
  **Hai điều kiện để so sánh công bằng, thiếu là số vô nghĩa:**
  1. Bản epoll phải **đa luồng** (`SO_REUSEPORT`, một loop mỗi core). So single-thread epoll với
     goroutine đa core là so 1 core với 6 core.
  2. Phải nói rõ **đang đo gì**: (a) RSS mỗi connection rỗi ở 10k conn — đo bằng `ps`/`/proc`,
     không suy từ 2KB stack; (b) throughput ở concurrency cao. Hai câu hỏi khác nhau, và
     netpoller thường thắng (b) mà thua (a).

**Bench cuối:** cùng backend, cùng máy, cùng open-loop rate → bảng p50/p99/p99.9 + rps của
**EdgeGate / nginx / Go `httputil.ReverseProxy`**. Ba cột. Đây là thứ đưa vào CV.

## Phase 10 — (tùy chọn) HTTP/2 h2c (3-4 ngày)

Frame layer, HPACK (bảng tĩnh + động), stream multiplexing, flow control (`WINDOW_UPDATE`),
`SETTINGS`. Chạy `h2spec` và ghi tỉ lệ pass. Ở đây mới hiểu vì sao HTTP/2 giải quyết được
head-of-line blocking ở tầng HTTP mà **vẫn còn** ở tầng TCP — và đó là lý do QUIC tồn tại.

---

## Bất biến toàn cục (mọi phase phải giữ)

| # | Bất biến | Vỡ thì sao |
|---|---|---|
| I1 | Proxy và upstream luôn đồng ý về ranh giới mỗi request | Request smuggling |
| I2 | Mọi byte đọc từ socket đều có trần, kiểm **trước** khi cấp phát | OOM bằng một header |
| I3 | Mọi connection đều có deadline | Slowloris |
| I4 | Connection chỉ về pool khi **sạch**; nghi ngờ thì đóng | Response của người này trả cho người khác |
| I5 | Hop-by-hop bị strip đúng một lần, kể cả field liệt kê trong `Connection:` | Header smuggling |
| I6 | Client IP dùng cho rate limit/log phải qua trust boundary | Bypass rate limiter bằng header giả |
| I7 | Mọi counter (inflight, token) giảm trên **mọi** đường ra (`defer`) | Node bị loại khỏi LB vĩnh viễn |
| I8 | Không rò goroutine, không rò connection sau khi request kết thúc | Chết chậm sau vài giờ |

Mỗi phase phải chỉ ra invariant nào nó thêm/giữ, cài ở `file:hàm` nào, và **lệnh nào chứng minh**.

---

## Tài liệu (đọc đúng lúc, đừng đọc trước)

- **RFC 9110** (HTTP Semantics) §7.6.1 hop-by-hop, §11.7 proxy auth — đọc trước phase 4.
- **RFC 9112** (HTTP/1.1 Syntax) §6 message body, §7 transfer codings — đọc trước phase 2.
  Hai RFC này thay thế RFC 7230-7235; đừng đọc bản cũ.
- **PortSwigger — HTTP Request Smuggling** (bản web, có lab): đọc **trong** phase 4, không trước.
- **Beej's Guide to Network Programming**: nếu phase 0-1 thấy lạ.
- **`net/http` source** (`transfer.go`, `readRequest`, `transport.go`): đọc **sau** khi tự viết
  phase 2 và phase 5 xong — lúc đó mới thấy vì sao nó phức tạp thế. Đọc trước thì chỉ copy.
- **nginx docs** `proxy_set_header` / `upstream` / `large_client_header_buffers`: đọc trước
  phase 4 và 6, để biết mình đang tái tạo cái gì.
- **"The Tail at Scale"** (Dean & Barroso, 8 trang): đọc trước phase 6. Nguồn của P2C, hedged
  request, và toàn bộ triết lý "p99 mới là con số thật".
- **Envoy architecture overview** (outlier detection, circuit breaking): đọc trước phase 7.

## Sau roadmap

Viết thêm một **L4 load balancer** (TCP passthrough, `splice`, không parse gì) trong ~2 ngày.
Lúc đó mới hiểu đánh đổi thật sự: **L7 cho bạn routing/rewrite/observability và lấy đi
zero-copy; L4 cho bạn thông lượng và lấy đi mọi thứ khác.** Đây là câu hỏi đầu tiên khi thiết
kế tầng edge cho hệ thống lớn.

## Nhật ký — quy tắc ghi

Mỗi phase có một file trong `diary/`. Ghi **trong lúc làm**, không phải sau khi xong.
Quy tắc đầy đủ + checklist chốt phase: [`skills/diary/SKILL.md`](./skills/diary/SKILL.md).

Nguyên tắc bất di bất dịch: **mọi con số và mọi kết luận phải kèm lệnh shell sinh ra nó
và output thật, dán nguyên văn.** Sáu tháng sau mở lại phải chạy lại được, trên máy khác
phải biết vì sao số khác.

Riêng project này, mọi con số latency còn phải ghi thêm **ba** thứ, thiếu một là số vô nghĩa:

- **closed-loop hay open-loop** — closed-loop giấu mất tail (coordinated omission).
- **RTT** — loopback ~30µs làm mọi kết luận về pool/TLS/retry biến mất.
- **CPU của generator và của proxy** — chung core thì đang đo cuộc tranh chấp CPU.

## Bảng "đã đo, và nó nói gì" (điền dần, KHÔNG điền trước)

Mọi ô đang trống. Điền **sau** khi có lệnh + output trong diary. Đây cũng là nguồn duy nhất
cho các con số đưa vào CV — số nào không có ở đây thì không được viết vào CV.

| Hiện tượng | Tỉ số | Lệnh | Phase | Nói lên điều gì |
|---|---|---|---|---|
| Dial mới vs reuse, **RTT 0** | **36.69x** (p50) | `netlab -exp rtt -n 2000 -bufio=false` | 0 | Pool tiết kiệm chi phí *dựng socket*, tách biệt với chi phí *RTT*; ở RTT 0 chỉ thấy vế đầu |
| Dial mới vs reuse, **RTT 20.157ms** | **2.00x** (tiết kiệm **20.47ms**) | `scripts/pay-P0-1.sh` | 0 | Tỉ số **tụt** khi RTT tăng, tiết kiệm tuyệt đối **tăng 62x**. Xem hàng dưới |
| Dial mới vs reuse, **RTT 40.201ms** | **2.00x** (tiết kiệm **40.47ms**) | ↑ | 0 | Trùng khít 2.00x ở RTT gấp đôi ⇒ **hằng số tiệm cận**: dial trả 2 RTT, reuse trả 1. Tỉ số **không** đo lợi ích của pool |
| RTT đo được / `netem delay` đặt trên `lo` | **2.00x** | `ping -c 5 127.0.0.1` trước/sau `tc` | 0 | `lo` áp qdisc cho cả hai chiều. Muốn RTT X phải đặt `delay X/2` — nếu không, mọi nhãn RTT sai 2x |
| `dial p50` cùng lệnh, cách nhau 30 phút | **2.5x** (861.7 → 341.3µs) | `bench/p0-rtt-rtt0.txt` vs `bench/p0-rtt-G8-dial-rate.txt` | 0 | Số tuyệt đối không ổn định **ngay trên cùng máy**. Bằng chứng cho chính nguyên tắc "kết luận là tỉ số" |
| p99 closed-loop vs open-loop | **1787x** (1 conn) · 33x (50 conn) | `netlab -exp omission -rate 1200 -svc 1ms -workers 1` | 0 | coordinated omission lớn cỡ nào — cùng hệ thống, p99 báo về sai 3 bậc độ lớn |
| Nagle `write-write-read` vs `NoDelay` | **638x** (p99), spike **44.03ms** | `netlab -exp nagle -n 150 -bufio=false` | 0 | Hai tính năng đúng ghép thành deadlock 40ms |
| 2 `Write` vs 1 `Write` (không bufio) | 1.41x (p50) · 1.07x (p99) | `netlab -exp syscall -n 5000 -bufio=false` | 0 | Syscall là thật nhưng **không quyết định tail latency** |
| RSS/conn 10k conn: có vs không `bufio` | 2.13x (hiệu **10.31 KB/conn**) | `netlab -exp mem -conns 10000 -bufio=true/false` | 0 | Chi phí thật của connection rỗi là **buffer**, không phải goroutine (`StackSys` = 2.01 KB) |
| conn/s không pool: `tw` thấp vs cao | 3.3x (1502 → 455, CPU 29.5%) | `netlab -exp limits -addr <eth0-ip>:911x` | 0 | ~~Trần OS không báo bằng lỗi~~ — **bác bỏ trên Linux thuần (P0-4, 2026-10-04):** 1089 → 386 conn/s rồi `EADDRNOTAVAIL` (101 lỗi). "Im lặng" là đặc thù WSL2 |
| Read thô đầu: 3 frame/1 Write vs 1 byte/10ms | **62x** (62 vs 1 byte) | `framelab -mode coalesce` / `-mode dribble` | 1 | Ranh giới `Write` của bên gửi không tồn tại ở bên nhận; `ReadFull` là bắt buộc |
| Byte cấp phát cho `length=0xFFFFFFFF`: tắt I2 vs có I2 | **> 65536x** (4294967376 vs < 65536) | `make framelab-nodefense` vs `TestCapBeforeAlloc` | 1 | Một dòng đặt sai thứ tự check/`make` = 4 GiB mỗi connection |
| Read/frame khi coalesce: dự đoán vs đo | < 1 vs **1.67** | `framelab -mode coalesce` | 1 | Số lần Read do decoder xin, không do gói tin — decoder đúng không "thấy" coalescing |
| `make(4 GiB)`: heap sạch vs sau 64 MB trang bẩn | **~1800x** (4ms → 7.138s), RSS 7 MB → **4.27 GB** | `go run ./cmd/needzerolab` | 1 | "Cấp phát chưa chạm không tốn gì" chỉ đúng trên heap sạch. I2 bảo vệ 7s CPU + 4 GiB RSS thật mỗi connection |
| Tìm lệch chunk-size `" 3"` vs `net/http`: fuzz phẳng vs fuzz có cấu trúc | 390 s không thấy vs **0.10 s** (>3900x) | `make difffuzz-chunk` trên bản khoan dung whitespace | 2 | Lệch differential không sinh edge mới ⇒ coverage không dẫn đường; thu nhỏ không gian, đừng chạy lâu hơn (P2-1) |
| `FuzzReadRequest`: `ReadMemStats` ×2 mỗi input vs `runtime/metrics.Read` | 80 µs vs **0.5 µs** một lần đo; fuzz 14 717 → **32 828 execs/s** | benchmark tạm + `make fuzz-http` | 2 | Bộ đếm cấp phát nào cũng toàn tiến trình ⇒ có dương tính giả; kiểm rẻ trước, xác nhận đắt (min-of-3) sau (P2-4) |
| Open-loop generator của `netlab`: semaphore đếm slot + `i % len(pool)` | panic ở 100k rps; **có race âm thầm** trong số G4 gốc | `netlab -exp omission -rate 100000` | 0 | Số đo của một bộ đo có race không phải số đo. Free-list phải chứa chính tài nguyên, không chỉ đếm nó (P0-2) |
| Pool on/off, RTT 20ms | | | 5 | |
| p99 4 thuật toán LB, backend lệch | | | 6 | |
| Slowloris: trước/sau `HeaderTimeout` | | | 7 | |
| B/op · ns/op trước/sau `sync.Pool` (keep-alive 1 KiB) | **43x** B/op (68 129 → 1 590) · **≈ 2.4x** ns/op; GC 419-433 → 32 / 20k req | `make perflab`; `GOGC=… go test -bench ProxyKeepAlive -tags nodefense9` | 9 | Giá của cấp phát là **tần suất GC** (heap sống vài MiB, mốc 4 MiB), không phải malloc: GOGC 16x xoá gần hết chênh |
| L4 `io.Copy` (splice) vs L7 parse, 10 MiB | **1.76-2.19x** throughput, **2.0-2.5x** CPU/GiB; L7 + splice body **0.92-1.01x** L4 | `make l4l7lab` | 9 | **giá phải trả của L7** ở body lớn = byte lên userspace; parse head chìm hẳn (L7 ≈ L4 copy). Parse xong head thì body CL splice được |
| RSS/conn rỗi 10k conn: netpoller vs epoll · EdgeGate trước/sau trả `bufio` | **55x** (7.7 vs 0.14 KiB) · **3.0-3.6x** (28 → 8-9 KiB); rps netpoller/epoll trung vị **1.01** | `make epolllab`; `make idlelab` | 9 | Goroutine-per-conn trả bằng **không gian** (stack + buffer trước khi có byte), không bằng thời gian |
| EdgeGate vs nginx vs `ReverseProxy` (2 core, WSL2 ồn) | CPU/req **1 : 0.79-1.05 : 3.2-3.7**; rps nginx/EdgeGate 1.03-1.15x | `./scripts/cpu-vs-nginx.sh`; `make bench-vs-nginx` | 9 | Kiến trúc thắng ngôn ngữ: ReverseProxy 0.68 context switch/req vs 0.05-0.14. P9-4 đã đo trên Linux thuần (xem `docs/debts.md`) nhưng output thô chưa có trong repo |
| HEADERS h2 #1 vs #2+ (client `net/http`, 5 header thật) · head h1 vs HEADERS h2 #2+ | **26.5x** (318 → 12 byte) · **42.3x** | `./bin/h2lab -mode hpack -n 100` | 10 | Request lặp lại tốn ~1 byte mỗi header: bảng động biến header thành chỉ số — và biến lỗi decode thành lỗi **connection** |
| Một stream, window 65 535 vs 8 MiB, RTT 40 ms | **1.51-1.57 MB/s** (lý thuyết 1.63) · **13.5-14.4x** | netem `delay 20ms` + `./bin/h2lab -mode flow` | 10 | Trần mỗi stream = window / RTT; RTT 0 che mất (1.5-1.8x) |
| h2 1 conn × 32 stream vs h1 32 conn, `delay 10ms loss 2%` | p50 **2.65-2.93x** · p99 **1.06-1.13x** (loss 0: p50 1.14-1.16x) | netem + `./bin/h2lab -mode tcphol -n 20 -par 32` | 10 | HOL tầng TCP **dời giá** mất gói từ vài request (đuôi h1) sang **mọi** stream (trung vị h2) — lý do QUIC tồn tại |
| h2spec 2.0.0: lần đầu → sau sửa → TLS/h2c-only (P10-5/6) · bản `nodefense10` | **143 → 144 → 145/145** · 139/145 | `make h2spec`, `make h2spec-tls` | 10 | Pass spec ≠ chịu tấn công: h2spec không có ca Rapid Reset / CONTINUATION flood nào (5 ca nodefense là luật field) |
