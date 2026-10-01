# Phase 8 — TLS termination + SNI routing

- **Thời lượng dự kiến:** 1-2 ngày · **thực tế:** _(turn 3 điền)_
- **Bắt đầu:** 2026-10-01 10:52 · **Kết thúc:** _______
- **Trạng thái:** 🟡 turn 2 xong 11:36 (đo xong, G1-G8 đã chấm: **2/8 sai một vế**, 0 sai hẳn) — còn turn 3 — giả thuyết và quyết định bên dưới viết **trước** file `.go` đầu tiên của phase.
- **Commit:** _______ (commit nền `46cd0f3`)

> **Đường đi thô, kể cả ngõ cụt:** [`phase8-log.md`](phase8-log.md). File này là bản biên tập.
>
> **Quy tắc ghi nhật ký:** mọi con số, mọi kết luận đều phải kèm **lệnh shell sinh ra nó**
> và **output thật**. Luật đầy đủ: [`../skills/diary/SKILL.md`](../skills/diary/SKILL.md).

## Ràng buộc kế thừa

- **Không `net/http` trên data path.** TLS dùng `crypto/tls` (stdlib, không phải `net/http`) — đúng ROADMAP.
  Cert cho test/lab sinh bằng `crypto/x509` (không `openssl`, không file key commit vào repo).
- **I3 phải phủ cả handshake.** Phase 7 có 7 deadline; TLS thêm chỗ đọc socket thứ 8 — ClientHello.
  `tls.Conn` handshake **lười** ở lần `Read` đầu, tức là đang chạy dưới deadline `IdleTimeout` (60 s) của
  `Peek` — một Slowloris kiểu TLS được 60 s chứ không phải `HeaderTimeout`.
- **Phase 4 (I1, routing):** quyết định "request này thuộc vhost nào" phải có **một** nguồn. Với TLS có hai
  nguồn (SNI ở handshake, `Host` ở request) — lệch nhau là domain fronting.
- **Phase 5 (pool sạch, probe):** probe `MSG_PEEK` cần fd TCP thật; `tls.Conn` không có ⇒ hiện tại probe
  **lặng lẽ tắt** (`known=false`) cho connection upstream TLS.
- **Đo RTT phải đo, không suy từ netem** (phase 0, phase 5): netem trên `lo` tác dụng **hai chiều**.
- Phòng tuyến mới có tag riêng `nodefense8` (P4-4).

## Môi trường

Cùng máy phase 0-7 (`bench/env-GOTIT-00663.txt`).

```console
$ date; uptime
Thu Oct  1 10:52:26 +07 2026
 10:52:26 up  1:27,  1 user,  load average: 2.71, 3.18, 3.80
$ uname -srmo && go version && nproc && ulimit -n
Linux 6.6.87.2-microsoft-standard-WSL2 x86_64 GNU/Linux
go version go1.26.2 linux/amd64
6
1048576
```

## Mục tiêu phase

Proxy kết thúc TLS: chọn cert theo SNI cho từng vhost (`server_name` của nginx), route theo vhost tới nhóm
upstream riêng, ALPN `http/1.1`, cert hot-reload không đứt connection. Đo handshake tốn bao nhiêu RTT, session
resumption cắt được bao nhiêu (và **cắt cái gì** — RTT hay CPU), và pool tới upstream TLS tiết kiệm bao nhiêu RTT.

## Câu hỏi phải trả lời được (viết trước khi code)

1. SNI chọn cert, `Host` chọn vhost — hai thứ lệch nhau thì proxy phải làm gì, và vì sao "route theo `Host`"
   là một lỗ (domain fronting)? Mã nào là đúng (RFC 9110 §15.5.20 `421 Misdirected Request`)?
2. Client không gửi SNI (IP literal, client cũ) hoặc gửi SNI lạ: trả cert mặc định hay từ chối? Mỗi lựa chọn
   lộ ra cái gì?
3. Handshake TLS 1.3 tốn bao nhiêu RTT, TLS 1.2 bao nhiêu? Session resumption cắt **RTT** hay cắt **CPU**, ở
   bản nào? Vì sao không có 0-RTT thì TLS 1.3 resumption không nhanh hơn về RTT?
4. Handshake tốn bao nhiêu CPU, phía nào (server ký, client verify)? ECDSA P-256 vs RSA-2048 khác bao nhiêu?
5. Hot-reload: vì sao `atomic.Pointer` đủ mà không cần khoá? Connection đang mở dùng cert nào sau reload? Reload
   hỏng (key không khớp cert) thì sao?
6. Handshake là chỗ đọc socket thứ mấy cần deadline? Slowloris ở tầng TLS trông ra sao và tốn proxy bao nhiêu?
7. Pool tới upstream **TLS** tiết kiệm bao nhiêu RTT so với upstream thường (ROADMAP: "2, không phải 1")?
   Probe FIN của phase 5 còn chạy được trên `tls.Conn` không, và TLS 1.3 gửi gì **sau** handshake có thể làm
   probe báo sai?

## Giả thuyết đăng ký trước (viết 10:53-10:54, chưa có file `.go` nào của phase)

Kịch bản chung: proxy in-process, listener TLS, hai vhost `a.test` (nhóm upstream A) và `b.test` (nhóm B),
cert sinh bằng `crypto/x509` từ một CA lab; client là `crypto/tls` với `RootCAs` = CA đó (verify thật, không
`InsecureSkipVerify`).

| # | Giả thuyết | Tỉ số / giá trị kỳ vọng | Lệnh sẽ dùng để chấm |
|---|---|---|---|
| G1 | **SNI routing:** 1 000 request xen kẽ SNI `a.test` / `b.test` ⇒ cert đúng tên + upstream đúng nhóm, **0** lệch. SNI lạ `c.test` ⇒ handshake **thất bại** (client thấy alert), upstream nhận **0**. Không SNI (nối bằng IP) ⇒ vhost mặc định nếu cấu hình, không thì thất bại | 0/1000 lệch; c.test 0 request tới upstream | `go test ./internal/proxy -run TestTLSSNIRouting -v` |
| G2 | **Domain fronting:** SNI `a.test` + `Host: b.test` ⇒ **421**, nhóm B nhận **0**, connection giữ được (request kế đúng vhost vẫn 200). Phản chứng `nodefense8` (route theo `Host`, bỏ qua SNI) ⇒ request tới **B** dưới cert của A ⇒ đỏ | 421; B nhận 0 vs ≥ 1 | `TestTLSDomainFronting`; `make tlslab-nodefense` đỏ |
| G3 | **ALPN:** client `[h2, http/1.1]` ⇒ chọn `http/1.1`; client chỉ `[h2]` ⇒ handshake **thất bại** (`no_application_protocol`); client không ALPN ⇒ OK, `NegotiatedProtocol == ""` | 3/3 | `TestTLSALPN` |
| G4 | **Hot-reload không đứt:** 32 connection TLS keep-alive bắn liên tục, 20 lần reload trong 2 s (xen kẽ hai bộ cert khác serial) ⇒ **0** lỗi; connection mở **trước** reload giữ serial cũ (cert đã thoả thuận), connection **mới** thấy serial mới ngay handshake kế. Reload hỏng (key không khớp) ⇒ `Reload` trả lỗi, serial **không đổi**, 0 lỗi client | 0 lỗi / 20 reload; reload hỏng giữ cũ | `TestTLSHotReload`; `tlslab -reload 20` |
| G5 | **Handshake có deadline riêng (I3 chỗ thứ 8):** client gửi ClientHello 1 byte/100 ms. `HandshakeTimeout` 300 ms ⇒ proxy đóng sau **300 ± 50 ms**. Phản chứng `nodefense8` (handshake chạy dưới deadline `Peek` = `IdleTimeout` 2 s trong test) ⇒ đóng sau **≈ 2 s** ⇒ đỏ | 300 ms vs ≈ IdleTimeout | `TestTLSHandshakeTimeout` |
| G6 | **CPU handshake ở RTT 0** (client đo từ `Dial` tới handshake xong, tuần tự 500 lần): TLS 1.3 ECDSA P-256 full **0.5-3 ms**; **resumed (PSK) nhanh hơn ≥ 1.5x** (bỏ ký cert + verify chuỗi, vẫn ECDHE); RSA-2048 full **≥ 3x** ECDSA full (ký RSA đắt). rps "mỗi request một connection mới" TLS / plaintext **≤ 0.5** | resumed ≥ 1.5x; RSA/ECDSA ≥ 3x; TLS/plain ≤ 0.5 | `go run ./cmd/tlslab -mode handshake` |
| G7 | **RTT của handshake** (netem, RTT đo bằng ping ≈ 20 ms): thời gian tới byte đầu response trên connection **mới**: plaintext **≈ 2 RTT** (TCP + HTTP); TLS 1.3 full **≈ 3 RTT**; TLS 1.3 resumed **≈ 3 RTT** — resumption **không** cắt RTT ở 1.3 (Go không có 0-RTT phía server); TLS 1.2 full **≈ 4 RTT**, TLS 1.2 resumed **≈ 3 RTT**. Mỗi ô ± 0.3 RTT | 2 / 3 / 3 / 4 / 3 RTT | `make rtt-up RTT_TARGET_MS=20 && go run ./cmd/tlslab -mode rtt; make rtt-down` |
| G8 | **Pool tới upstream TLS:** khoản tiết kiệm mỗi request (không pool − pool) ở RTT 20 ms ≈ **2 RTT** (TCP + TLS 1.3, dải 1.7-2.3) — so với **0.87 RTT** của upstream thường ở phase 5 ⇒ tỉ số ≈ **2x**. Ở RTT 0 tiết kiệm **≥ 3x** của plaintext (CPU handshake). Probe trên upstream TLS (đọc fd TCP bên dưới): keep-alive đều đặn ⇒ `DeadOnProbe` **0** (NewSessionTicket của TLS 1.3 đã được đọc cùng response đầu, không nằm lại làm probe báo "bẩn"); upstream đóng connection rỗi (close_notify + FIN) ⇒ probe bắt **50/50** | 1.7-2.3 RTT; ≥ 3x ở RTT 0; DeadOnProbe 0 / 50 | `go run ./cmd/tlslab -mode upstream`; `TestTLSUpstreamProbe` |

**Quyết định thiết kế đăng ký trước** (đổi sau phải ghi lý do):

| # | Quyết định | Lý do / hệ quả |
|---|---|---|
| D1 | Package mới `internal/tlsx`: `CertStore` = `atomic.Pointer[certSet]`, `certSet{byName map[string]*tls.Certificate; def *tls.Certificate}`; `GetCertificate(hello)`: khớp đúng tên → wildcard `*.x` một nhãn → `def` (nếu có) → lỗi. `Load(entries)` parse **hết** rồi mới swap — một file hỏng thì giữ nguyên bộ cũ, trả lỗi | `atomic.Pointer` đủ vì `certSet` bất biến sau khi tạo: reader lấy con trỏ, writer thay cả bộ. Handshake đã xong giữ `tls.Certificate` nó thoả thuận — connection cũ không bị ảnh hưởng |
| D2 | `Config.VHosts []VHost{Names, CertFile, KeyFile, Upstreams, LB}` — **mỗi vhost một `lb.Balancer`**; pool vẫn theo addr (dùng chung). Config cũ (`Upstreams`) = vhost mặc định không tên. Listener TLS: vhost chọn **một lần** theo SNI lúc handshake, lưu ở `connState`. Listener thường: vhost chọn theo `Host` mỗi request | Một connection TLS gắn với một cert ⇒ một vhost. Plaintext không có SNI ⇒ `Host` là nguồn duy nhất |
| D3 | **`Host` phải thuộc vhost của SNI**, so không phân biệt hoa thường, bỏ port; lệch ⇒ **421** (RFC 9110 §15.5.20), drain body, giữ connection. Phản chứng `nodefense8`: chọn vhost theo `Host`, bỏ qua SNI | Domain fronting: cert của A, nội dung của B — client/CDN tin cert A. 421 là mã RFC dành đúng cho "connection này không phục vụ authority đó" |
| D4 | SNI lạ / không SNI: dùng `DefaultVHost` nếu cấu hình, không thì `GetCertificate` trả lỗi ⇒ handshake hỏng. Mặc định: **không** default | Trả cert mặc định cho SNI lạ = lộ tên vhost mặc định cho mọi scanner, và client nhận cert sai tên (verify hỏng muộn hơn, khó hiểu hơn) |
| D5 | ALPN `NextProtos = ["http/1.1"]`; `MinVersion` TLS 1.2; session ticket mặc định của Go (bật) | Móc cho phase 10. TLS 1.2 vẫn cho để đo G7 |
| D6 | **`Limits.HandshakeTimeout`** (mặc định 10 s): `serveConn` gọi `tlsConn.HandshakeContext` với deadline riêng **trước** vòng keep-alive; hỏng ⇒ đóng (không có gì để trả lời HTTP). Phản chứng `nodefense8`: bỏ bước này, handshake lười dưới deadline `Peek` | I3 chỗ thứ 8. Handshake tường minh còn cho biết SNI **trước** request đầu (cần cho D2) |
| D7 | Listener TLS: `Config.TLS{CertStore}`; `Serve` bọc `tls.NewListener`? — **không**: accept raw, bọc `tls.Server(c, cfg)` trong goroutine của connection (handshake không chặn vòng Accept). `setNoDelay` gọi trên conn TCP **bên dưới** | `tls.NewListener` không handshake trong Accept, nhưng bọc thủ công giữ được conn TCP để NoDelay + MaxConns + track như cũ |
| D8 | **Upstream TLS** (`Config.UpstreamTLS{Enabled, ServerName, RootCAs}`): pool dial `tls.Client` + `HandshakeContext` trong `DialTimeout`; `pooledConn.raw` = conn TCP bên dưới ⇒ `probeIdle(raw)` | Probe phải nhìn fd thật. Byte trên fd lúc rỗi (close_notify, NewSessionTicket chưa đọc) ⇒ "bẩn" ⇒ bỏ — đúng cho close_notify, có thể **sai** cho ticket (G8 kiểm) |
| D9 | `internal/tlsx/gen.go`: `NewCA()`, `Leaf(ca, names, keyType, serial)` — ECDSA P-256 mặc định, RSA-2048 cho G6; `WritePEM`. `cmd/gencert -out bin/certs` cho `make tlslab` + `config/tls.json`. Không commit key | Cert lab sinh mỗi lần, không có file bí mật trong git |
| D10 | `cmd/edgegate`: khối `tls` (`listen`, `vhosts[{names, cert, key, upstreams, lb}]`, `default_vhost`, `handshake_timeout_ms`) + `SIGHUP` ⇒ `CertStore.Reload()` (đọc lại file), log serial mới hoặc lỗi | fsnotify không có trong stdlib và repo không kéo dependency; SIGHUP là cách nginx (`nginx -s reload`) |
| D11 | `cmd/tlslab` in-process, ba mode: `handshake` (G6: full/resumed × ECDSA/RSA × 1.2/1.3, + plaintext; tuần tự, p50/p99 handshake), `rtt` (G7: thời gian tới byte đầu trên connection mới, chia cho RTT **đo** bằng một TCP connect trơn ngay trước), `upstream` (G8: pool on/off × upstream plain/TLS), `reload` (G4 dưới tải) | Cùng tiến trình ⇒ CPU client và server lẫn nhau ở G6: ghi rõ đó là **tổng** hai phía |
| D12 | Phản chứng: `defense8.go` (`!nodefense8`) / `_off`: `sniPinsVHost` (D3), `explicitHandshake` (D6). `make tlslab-nodefense` đỏ đúng dòng | Khuôn P4-4 |

## Deliverable

- `internal/tlsx`: `CertStore` (+ reload), `gen.go`; test.
- `internal/proxy`: listener TLS, vhost theo SNI/Host, 421, `HandshakeTimeout`, upstream TLS + probe trên raw;
  `TestTLSSNIRouting`, `TestTLSDomainFronting`, `TestTLSALPN`, `TestTLSHotReload`, `TestTLSHandshakeTimeout`,
  `TestTLSUpstreamProbe`; `defense8*.go`.
- `cmd/gencert`, `cmd/tlslab`; `config/tls.json`; Makefile `tlslab`, `tlslab-nodefense`, `tlslab-rtt`.
- `cmd/edgegate`: khối `tls`, SIGHUP reload.

## Reproduce toàn bộ phase

```bash
git checkout <commit phase 8>
go test ./... -count=1 -race
make tlslab                | tee bench/p8-tlslab.txt          # G1-G4 qua binary + curl, G6
make tlslab-nodefense                                         # PHẢI đỏ (G2, G5)
make rtt-up RTT_TARGET_MS=20                                  # đo RTT bằng ping, ghi số ĐO
go run ./cmd/tlslab -mode rtt      | tee bench/p8-tlslab-rtt20.txt       # G7
go run ./cmd/tlslab -mode upstream | tee bench/p8-tlslab-upstream20.txt  # G8
make rtt-down                                                 # NHỚ tháo
go run ./cmd/tlslab -mode upstream | tee bench/p8-tlslab-upstream0.txt   # G8 ở RTT 0
for k in default x25519; do go run ./cmd/tlslab -mode handshake -n 1000 -kex $k; done   # G6: so cùng lượt
go run ./cmd/tlslab -mode reload -conns 64 -duration 10s                  # G4 dưới tải
```

## Nhật ký

### 2026-10-01 10:52-11:07 — Turn 1: TLS listener, vhost theo SNI, reload, upstream TLS; chưa đo

Chi tiết: [`phase8-log.md` §1](phase8-log.md). Số trong lần chạy thử (`-n 30`, `make tlslab` một lượt) **không**
phải số đo — turn 2 đo với tham số của Reproduce.

1. **Sáu test TLS xanh ngay lần đầu** (G1-G5, G8 mức đơn vị); hai phản chứng `nodefense8` đỏ đúng dòng
   (`bench/p8-turn1-nodefense.txt`): domain fronting ⇒ `200 (X-Sim "B")` dưới cert của A; ClientHello nhỏ giọt
   ⇒ proxy đóng sau **2.001 s** (= IdleTimeout) thay vì 0.301 s.
2. **SNI lạ ⇒ alert `internal_error`, không phải `unrecognized_name`** (RFC 6066 §3): `GetCertificate` trả lỗi
   thì `crypto/tls` luôn gửi `internal_error`. Client thấy "remote error: tls: internal error" — đúng hành vi
   (từ chối), sai thông điệp. Ghi vào câu 2.
3. **Khe có từ phase 5-6 lộ dưới `-race`:** connection reused chết ⇒ retry D4 `dialNew` **cùng** backend ⇒
   backend vừa chết ⇒ refused ⇒ **502**, dù chưa byte nào đi đâu (D9 chỉ chọn lại ở lần thử đầu).
   `TestLBKillRevive` bắt được 1/574. Sửa: dial lỗi ở lần nào cũng được D9 chọn lại một lần (còn budget).
4. **Test phase 5 phụ thuộc thời gian:** `TestIdleClosedUpstream` chờ FIN bằng `Sleep(3ms)` — cả suite chạy
   song song dưới `-race` ở load ~7 thì hụt (DeadOnProbe 41/50). Chạy riêng 5/5 xanh cả ở commit nền ⇒ không
   phải code mới. Nới 20 ms.
5. **Tín hiệu cho G6 (chưa phải số đo):** `make tlslab` n = 500 — TLS 1.3 full ECDSA p50 **3.55 ms** chậm hơn
   TLS 1.2 full **1.34 ms**; 1.3 resumed 2.00 ms vs 1.2 resumed 0.64 ms. G6 không đoán 1.3 chậm hơn 1.2; nghi
   key exchange mặc định của Go 1.26 cho 1.3 (lai hậu lượng tử) — turn 2 kiểm bằng `CurvePreferences`.

`make tlslab` qua binary thật + curl: a.test → `X-Upstream: A`, b.test → B, SNI a.test + `Host: b.test` ⇒ **421**,
curl `--http2` ⇒ `http_version=1.1`, SIGHUP ⇒ log `serial [1 2]` → `[100 101]`. `go test ./... -race` xanh.

### 2026-10-01 11:08-11:36 — Turn 2: đo, chấm G1-G8

Output thô: [`phase8-log.md` §2](phase8-log.md). Commit nền `4444954`. Load **3.1-10.2** (indexer codegraph +
pytest + next-server của project khác) — `uptime` đầu mỗi file bench. Mọi số: **loopback, proxy + upstream +
client cùng tiến trình** (thời gian handshake = CPU hai phía), không ghim core trừ microbench.

**(1) G1-G5, G8 đơn vị ×3** (`bench/p8-tests.txt`) — đúng cả ba lượt:

```console
$ go test ./internal/proxy -run TestTLS -count=3 -v
    phase8_test.go:119: 1000 request xen kẽ SNI a/b: lệch 0; sim A 500, B 500
    phase8_test.go:129: SNI lạ: remote error: tls: internal error; không SNI: remote error: tls: internal error; handshake hỏng phía proxy 3
    phase8_test.go:146: SNI a.test, Host b.test: 421 (X-Sim ""), B phục vụ 0
    phase8_test.go:164: chỉ h2: remote error: tls: no application protocol
    phase8_test.go:237: 20 reload dưới 32 connection: 3677 / 1964 / 3503 request ok, 0 lỗi; serial đầu 1001, connection cũ 1001, mới nhất 1041, connection mới thấy 1041
    phase8_test.go:273: ClientHello nhỏ giọt: proxy đóng sau 302ms (gửi 2 byte)
    phase8_test.go:318: (a) 20 request liền: {Dials:1 Reuses:19 ... DeadOnProbe:0 ...}
    phase8_test.go:319: (b) +20 request cách 100 ms, upstream idle 50 ms: {Dials:21 ... Retries:0 ... DeadOnProbe:20 ...}
$ go test ./internal/proxy -run 'TestTLSDomainFronting|TestTLSHandshakeTimeout' -count=1 -v -tags nodefense8
    phase8_test.go:146: SNI a.test, Host b.test: 200 (X-Sim "B"), B phục vụ 1
    phase8_test.go:273: ClientHello nhỏ giọt: proxy đóng sau 2.001s (gửi 19 byte)
$ ./bin/tlslab -mode reload -conns 64 -duration 10s -every 100ms
reload: 96 lần trong 10s dưới 64 connection: 98151 request ok, 0 lỗi, 1993 handshake mới; serial cuối 196
```

**(2) Binary thật + curl** (`bench/p8-tlslab.txt`): a.test → `"x-upstream":["A"]`, b.test → `["B"]`; c.test ⇒
`curl: (35) … tlsv1 alert internal error`; SNI a.test + `Host: b.test` ⇒ **421**; `--http2` ⇒ `http_version=1.1`;
gencert CA mới + SIGHUP ⇒ log `serial [1 2]` → `[100 101]`, curl với **CA mới** ⇒ 200 (cert thật sự đã đổi).

**(3) G6 — CPU handshake** (`bench/p8-tlslab-handshake-cpu2.txt`, n = 1000, 3 lượt × 2 key exchange; cột CPU/hs =
user+sys của cả tiến trình chia n). Lượt 3, `-kex default` và `-kex x25519`:

```console
handshake (tuần tự)        p50       p90       p99      mean     CPU/hs   resumed / n
ecdsa-1.3-full         1.541ms   2.594ms   3.772ms   1.754ms     2.39ms   0 / 1000  X25519MLKEM768
ecdsa-1.3-resumed      1.155ms   1.809ms   2.596ms   1.267ms    1.842ms   1000 / 1000  X25519MLKEM768
ecdsa-1.2-full         1.274ms   2.288ms   2.882ms   1.489ms    1.882ms   0 / 1000  X25519
ecdsa-1.2-resumed        517µs     708µs   1.137ms     544µs      987µs   1000 / 1000  X25519
rsa-1.3-full           2.612ms   3.634ms   5.625ms   2.857ms    3.449ms   0 / 1000  X25519MLKEM768
rsa-1.3-resumed         1.25ms   2.149ms   3.037ms   1.416ms    2.074ms   1000 / 1000  X25519MLKEM768
---
ecdsa-1.3-full         1.251ms   2.262ms   3.437ms   1.469ms     1.99ms   0 / 1000  X25519
ecdsa-1.3-resumed        890µs   1.373ms   2.031ms     968µs    1.509ms   1000 / 1000  X25519
ecdsa-1.2-full         1.646ms   2.868ms   3.968ms   1.838ms    2.216ms   0 / 1000  X25519
rsa-1.3-full           3.046ms   7.472ms   11.12ms   3.957ms    4.406ms   0 / 1000  X25519
$ taskset -c 5 go test ./internal/tlsx -run ^$ -bench . -benchtime 2000x -count 3   # microbench từng phép, hai phía
BenchmarkX25519              229974 / 221869 / 228254 ns/op
BenchmarkMLKEM768            239830 / 249691 / 259832 ns/op
BenchmarkECDSAP256SignVerify 220517 / 230289 / 241117 ns/op
BenchmarkRSA2048SignVerify   1527119 / 1626056 / 1332189 ns/op
```

**Đọc kết quả:** cùng biến thể lệch tới 2x giữa các lượt (máy ồn), nên chỉ chấm **tỉ số trong một lượt**.
Mật mã của một handshake TLS 1.3 ECDSA là X25519 (~0.23 ms) + ký/verify (~0.23 ms) ≈ 0.45 ms trên tổng 2-3 ms
⇒ phần lớn CPU handshake **không phải mật mã** (x509, accept/dial, cấp phát, goroutine). Đó là vì sao resumption
chỉ 1.3-1.5x và RSA chỉ 1-2.5x. Mặc định của Go 1.26 thêm ML-KEM (~0.25 ms) vào mọi handshake 1.3 — 1.3 full
đắt hơn 1.2 full 1.27-2.0x; ép X25519 thì 0.9-1.26x. rps "mỗi request một connection": TLS ECDSA / plaintext =
207/713, 160/785, 96/438, 217/833 = **0.20-0.29** (`bench/p8-tlslab-handshake.txt`).

**(4) G8 ở RTT 0** (`bench/p8-tlslab-upstream0.txt`, 3 lượt × 500): tiết kiệm của pool upstream thường 314-716 µs,
upstream TLS 1.45-2.98 ms ⇒ **3.91-4.61x**; pool bật thì TLS và plain như nhau (p50 256-285 µs vs 273-285 µs).

**(5) G7 + G8 ở RTT 20 ms** — netem do người dùng bật (`sudo` cần mật khẩu), tháo ngay sau đo
(`bench/p8-tlslab-rtt20.txt`):

```console
$ tc qdisc show dev lo
qdisc netem 8001: root refcnt 2 limit 1000 delay 10ms
$ ping -c 10 -i 0.2 127.0.0.1 | tail -1
rtt min/avg/max/mdev = 20.110/20.375/22.091/0.573 ms
$ ./bin/tlslab -mode rtt -n 30                 # ×2
RTT đo (TCP connect trơn, median 21): 20.353ms / 20.518ms
biến thể                    p50    ÷ RTT
plaintext              61.827ms     3.04   |  61.959ms   3.02
ecdsa-1.3-full         83.593ms     4.11   |  84.099ms   4.10
ecdsa-1.3-resumed      83.733ms     4.11   |  86.232ms   4.20
ecdsa-1.2-full        107.569ms     5.29   | 110.319ms   5.38
ecdsa-1.2-resumed      83.819ms     4.12   |  85.769ms   4.18
$ ./bin/tlslab -mode upstream -n 50            # ×2
tiết kiệm của pool, upstream plain: 19.863ms / request = 0.97 RTT   |  24.466ms = 1.17 RTT
tiết kiệm của pool, upstream tls  : 41.386ms / request = 2.03 RTT   |  44.576ms = 2.14 RTT
tỉ số tiết kiệm tls / plain: 2.08x   |  1.82x
$ tc qdisc show dev lo                         # sau khi tháo
qdisc noqueue 0: root refcnt 2
```

**Đọc kết quả:** đây là câu 3 bằng số. TLS 1.3 thêm **1 RTT**; resumption ở 1.3 thêm **cũng 1 RTT** — không cắt
được gì về RTT (4.11 vs 4.11). TLS 1.2 thêm 2, resumption 1.2 cắt về 1. "+1 RTT ở mọi ô" so với đăng ký là chặng
proxy → upstream (cũng qua `lo`). Pool tới upstream TLS tiết kiệm **2 RTT** (TCP + TLS 1.3) — đúng câu ROADMAP
"2, không phải 1"; với pool, upstream TLS tốn đúng bằng upstream thường (41.09 vs 41.08 ms).


## Giả thuyết sai

| # | Tôi tưởng là | Thực tế là | Lệnh + output đã lật tẩy | Đã sửa thế nào |
|---|---|---|---|---|
| G6 resumed | Resumed (PSK) nhanh hơn full ≥ 1.5x | CPU **1.29-1.50x**, wall p50 1.32-1.77x (cùng lượt). Resumption chỉ bỏ ký + verify cert (~230 µs mỗi cặp ECDSA), mà mật mã chỉ là 20-30 % CPU handshake 2-3 ms | `bench/p8-tlslab-handshake-cpu2.txt`; microbench ECDSA sign+verify 221-241 µs | Không sửa; ghi "resumption cắt cái gì" vào câu 3-4 |
| G6 RSA | RSA-2048 full ≥ 3x ECDSA full | CPU **0.96-2.53x** cùng lượt. RSA đắt ở **ký** (server) nhưng rẻ ở **verify** (client); ECDSA ngược lại — đo cả hai phía cùng tiến trình nên một phần bù trừ. Microbench: RSA sign+verify 1.33-1.63 ms vs ECDSA 0.22-0.24 ms ⇒ +1.2 ms trên nền ~2.5 ms | `internal/tlsx/kex_bench_test.go` (`taskset -c 5`) | Không sửa |
| G6 (chưa đăng ký) | TLS 1.3 không đắt hơn 1.2 | Mặc định Go 1.26 cho 1.3 là **X25519MLKEM768** (lai hậu lượng tử): 1.3 full / 1.2 full CPU **1.27-2.0x** cùng lượt; ép X25519 ⇒ 0.90-1.26x. ML-KEM-768 (keygen + encaps + decaps) **240-260 µs**, X25519 222-230 µs | `-kex default` vs `-kex x25519` ×3; `go doc crypto/tls.Config.CurvePreferences` | Thêm `-kex` vào tlslab; không đổi mặc định của proxy (giữ an toàn hậu lượng tử) |
| G7 tuyệt đối | Plaintext ≈ 2 RTT, TLS 1.3 ≈ 3 RTT … | **+1 RTT ở mọi ô**: 3.02-3.04 / 4.10-4.11 / 4.11-4.20 / 5.29-5.38 / 4.12-4.18 — chặng proxy → upstream cũng đi qua `lo` bị netem. Phần **chênh** so với plaintext khớp đúng đăng ký: +1 / +1 / +2 / +1 | `bench/p8-tlslab-rtt20.txt` | Không sửa lab; đọc theo hiệu số |
| bench G6 | Wall-time handshake đủ để so | Load 5-10 (session khác) làm cùng biến thể lệch 40 % giữa lượt; đổi sang CPU/handshake (`getrusage`) — vẫn trôi giữa lượt ⇒ chỉ so **tỉ số cùng lượt**. Thêm: lượt resumed GET mỗi lần để lấy ticket mới ⇒ CPU GET cộng vào ⇒ "resumed đắt hơn full" | `bench/p8-tlslab-handshake.txt` → `-cpu.txt` → `-cpu2.txt` | Cột CPU/hs; bỏ GET (ticket cũ dùng lại được: 1000/1000 resumed) |

## Số đo

2026-10-01, commit `4444954` + `-kex`/CPU trong tlslab, máy `bench/env-GOTIT-00663.txt` (6 core WSL2), **loopback;
proxy + upstream + client cùng tiến trình (handshake = CPU hai phía); không ghim core; load nền 3.1-10.2**.
RTT 20 ms: netem `delay 10ms` trên `lo` (ping 20.1-22.1 ms), đo bằng TCP connect trơn trong lab.

| # | Đại lượng | Đo | Kết luận | Kỳ vọng |
|---|---|---|---|---|
| G1 | 1000 request xen kẽ SNI; SNI lạ / không SNI | lệch 0; handshake hỏng (alert `internal_error`), upstream 0 | ✅ | 0; hỏng |
| G2 | SNI a + `Host: b` | **421**, B 0; nodefense **200 từ B** | ✅ | 421 vs ≥ 1 |
| G3 | ALPN [h2,1.1] / [h2] / không | http/1.1 / `no application protocol` / "" | ✅ | 3/3 |
| G4 | Reload dưới tải | 20 × 32 conn: 0 lỗi (×3); 96 × 64 conn, 98 151 request: **0 lỗi**; reload hỏng giữ serial | ✅ | 0 lỗi |
| G5 | ClientHello nhỏ giọt: HandshakeTimeout 300 ms / nodefense | **302 ms** (×3) / 2.001 s | ✅ | 300 ± 50 / ≈ 2 s |
| G6 | full ECDSA 1.3; resumed; RSA/ECDSA; TLS/plain rps | p50 1.25-2.89 ms; CPU **1.29-1.50x**; **0.96-2.53x**; **0.20-0.29** | ❌ hai vế | 0.5-3 ms; ≥ 1.5x; ≥ 3x; ≤ 0.5 |
| G6′ | Key exchange mặc định (ML-KEM lai) vs X25519 | ML-KEM-768 +240-260 µs; 1.3/1.2 full 1.27-2.0x vs 0.90-1.26x | chưa đăng ký | — |
| G7 | ÷ RTT: plain / 1.3 / 1.3 res / 1.2 / 1.2 res | 3.03 / 4.11 / 4.16 / 5.34 / 4.15 (trung bình 2 lượt) | hiệu số ✅ (+1/+1/+2/+1); tuyệt đối ❌ +1 | 2 / 3 / 3 / 4 / 3 |
| G8 | Tiết kiệm pool, RTT 20: plain / TLS; RTT 0 tỉ số; probe | **0.97-1.17 / 2.03-2.14 RTT** (1.82-2.08x); 3.91-4.61x; DeadOnProbe 0 / 20 | ✅ | 2 RTT, ≈ 2x; ≥ 3x; 0 / 50 |

**Chấm G1-G8:** G1, G2, G3, G4, G5, G8 ✅; G6, G7 ❌ **một vế** (bảng Giả thuyết sai).

## Invariant + lệnh kiểm chứng

_(turn 3)_

## Đọc gì

- ROADMAP phase 8; RFC 8446 (TLS 1.3) §2 (handshake 1-RTT, PSK, 0-RTT), §4.6.1 (NewSessionTicket);
  RFC 9110 §15.5.20 (421); RFC 7301 (ALPN); RFC 6066 §3 (SNI). _(turn 3: ghi những gì đã kiểm từ nguồn)_

## Rút ra

_(turn 3)_

## Nợ kỹ thuật

_(turn 3 chốt và chép vào `docs/debts.md`)_ Phát sinh turn 2:

- [ ] **P8-1** 📏 G6 đo trên máy ồn (load 3-10, cùng biến thể lệch 2x giữa lượt). Chạy lại lúc `uptime` < 1, ghim
  core (`taskset`), tách client/proxy hai tiến trình để CPU server đo riêng (`scripts/linux-baseline.sh`).
- [ ] **P8-2** 🔧 SNI lạ ⇒ alert `internal_error` thay vì `unrecognized_name` (RFC 6066 §3): `crypto/tls` không cho
  chọn alert từ `GetCertificate`. Thử `GetConfigForClient` trả lỗi có kiểu `tls.AlertError(112)`.
- [ ] **P8-3** ⏳ Health check active với upstream TLS: probe `GET Path` nói HTTP thường vào cổng TLS ⇒ fail. Cần
  probe TLS (hoặc chỉ TCP) khi `UpstreamTLS` bật — chưa có test.
