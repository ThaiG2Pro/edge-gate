# Phase 8 — TLS termination + SNI routing

- **Thời lượng dự kiến:** 1-2 ngày · **thực tế:** _(turn 3 điền)_
- **Bắt đầu:** 2026-10-01 10:52 · **Kết thúc:** _______
- **Trạng thái:** 🟡 turn 1 xong 11:07 (code + test xanh + phản chứng đỏ, **chưa đo**) — giả thuyết và quyết định bên dưới viết **trước** file `.go` đầu tiên của phase.
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

## Giả thuyết sai

_(turn 2/3)_

## Số đo

_(turn 2)_

## Invariant + lệnh kiểm chứng

_(turn 3)_

## Đọc gì

- ROADMAP phase 8; RFC 8446 (TLS 1.3) §2 (handshake 1-RTT, PSK, 0-RTT), §4.6.1 (NewSessionTicket);
  RFC 9110 §15.5.20 (421); RFC 7301 (ALPN); RFC 6066 §3 (SNI). _(turn 3: ghi những gì đã kiểm từ nguồn)_

## Rút ra

_(turn 3)_

## Nợ kỹ thuật

_(turn 3)_
