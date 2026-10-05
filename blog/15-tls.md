# Bài 15 — TLS trước proxy tốn bao nhiêu?

> Series [Mở nắp reverse proxy](README.md) · bài 15/16 · cần đọc trước: [bài 3](03-keep-alive.md)

Bạn bật HTTPS ở proxy. Một file cert, một file key, vài dòng config. Hai câu hỏi thường bị bỏ qua:
mỗi connection mới trả thêm mấy RTT, và proxy có thêm lỗ hổng nào mà lúc plaintext không có? Bài
này đo cả hai, rồi thêm câu thứ ba: proxy nói TLS **với upstream** thì pool đáng giá bao nhiêu.

## Thí nghiệm

### 1. Một connection, hai lời khai về tên miền

Client nói TLS với proxy thì khai tên server **hai lần**. Lần đầu là SNI trong ClientHello, dùng để
chọn cert. Lần sau là header `Host:` trong request, dùng để chọn nội dung. Hai lời khai có thể khác
nhau. `make tlslab` dựng hai vhost `a.test` và `b.test` trên một proxy, rồi thử bằng `curl`:

```console
$ make tlslab
== G1: SNI a.test → A, b.test → B ==
200 "x-upstream":["A"]
200 "x-upstream":["B"]
== G1: SNI lạ c.test ⇒ handshake hỏng ==
curl: (35) OpenSSL/3.0.13: error:0A000438:SSL routines::tlsv1 alert internal error
== G2: SNI a.test + Host: b.test ⇒ 421 ==
421
```

(`bench/p8-tlslab.txt`. Sau lượt này alert đã đổi sang `unrecognized_name`, `diary/phase8.md`.)

Bắt tay với `a.test`, rồi xin `Host: b.test`, thì nhận **421 Misdirected Request**. Bản build tắt
phòng thủ (`nodefense8`) chọn vhost theo `Host` và bỏ qua SNI:

```console
$ make tlslab-nodefense      # PHẢI đỏ
    phase8_test.go:146: SNI a.test, Host b.test: 200 (X-Sim "B"), B phục vụ 1
    phase8_test.go:148: domain fronting phải 421 và B nhận 0
--- FAIL: TestTLSDomainFronting (0.01s)
    phase8_test.go:263: ClientHello nhỏ giọt: proxy đóng sau 2.001s (gửi 19 byte)
    phase8_test.go:265: muốn đóng sau ≈ HandshakeTimeout 300 ms
--- FAIL: TestTLSHandshakeTimeout (2.00s)
```

(`bench/p8-turn1-nodefense.txt`)

**200 từ B, dưới cert của A.** Đó là domain fronting. Mọi hộp ở giữa (CDN, firewall, log) nhìn
SNI và cert, tin rằng client đang nói chuyện với A, trong khi nội dung đến từ B. Test thứ hai là
Slowloris ở tầng TLS: gửi ClientHello từng byte một. Có phòng thủ thì proxy đóng sau **302 ms**;
không có thì giữ tới **2.001 s**, tức hết `IdleTimeout` của test (`diary/phase8.md`).

### 2. Handshake tốn mấy RTT?

Cần `sudo` để bật `tc netem` trên `lo` (xem [bài 3](03-keep-alive.md): netem trên `lo` áp delay
hai chiều, nên đặt 10 ms ra RTT 20 ms):

```console
$ make rtt-up RTT_TARGET_MS=20      # nhớ make rtt-down sau khi đo
$ go run ./cmd/tlslab -mode rtt -n 30
RTT đo (TCP connect trơn, median 21): 20.353ms

biến thể                    p50    ÷ RTT   (tới byte đầu response trên connection MỚI; median 30 lần)
plaintext              61.827ms     3.04
ecdsa-1.3-full         83.593ms     4.11
ecdsa-1.3-resumed      83.733ms     4.11
ecdsa-1.2-full        107.569ms     5.29
ecdsa-1.2-resumed      83.819ms     4.12
```

(`bench/p8-tlslab-rtt20.txt`, lượt 1; lượt 2 ra 3.02 / 4.10 / 4.20 / 5.38 / 4.18. Số chạy trên
WSL2; chỉ tỉ số và số RTT là đáng tin.)

Plaintext ra 3 RTT chứ không phải 2 (TCP + HTTP), vì chặng proxy → upstream cũng đi qua `lo` có
netem: một proxy luôn là **hai** chặng mạng. Vậy đọc phần **chênh** so với plaintext (`diary/phase8.md`):

| Trên connection mới | thêm so với plaintext |
|---|---|
| TLS 1.3 full | +1 RTT |
| TLS 1.3 resumed | **+1 RTT** (4.11 vs 4.11: resumption không cắt được RTT nào) |
| TLS 1.2 full | +2 RTT |
| TLS 1.2 resumed | +1 RTT |

Ở TLS 1.3, session resumption **không** nhanh hơn về RTT. Lý do ở phần dưới.
### 3. Pool tới upstream TLS đáng giá gấp đôi

Vẫn RTT 20 ms, đo chặng proxy → upstream, pool bật và tắt (`make tlslab-rtt` chạy cả hai mode):

```console
$ go run ./cmd/tlslab -mode upstream -n 50
upstream       pool          p50       mean    dials
plain          false    61.321ms   61.447ms       50
plain          true     41.078ms   41.585ms        1
tls            false    83.026ms   83.414ms       50
tls            true     41.086ms   42.028ms        1
tiết kiệm của pool, upstream plain: 19.863ms / request = 0.97 RTT
tiết kiệm của pool, upstream tls  : 41.386ms / request = 2.03 RTT
tỉ số tiết kiệm tls / plain: 2.08x
```

(`bench/p8-tlslab-rtt20.txt`, lượt 1; lượt 2: 1.17 RTT và 2.14 RTT, tỉ số 1.82x.)

Upstream thường: pool tiết kiệm 0.97 RTT (bắt tay TCP). Upstream TLS: tiết kiệm **2.03 RTT** (TCP cộng
TLS 1.3). Khi pool đã bật, upstream TLS tốn đúng bằng upstream thường: 41.086 ms so với 41.078 ms.
Giá của TLS chỉ còn ở lần dial. Ở RTT 0, tỉ số tiết kiệm TLS / plain còn lên **3.91-4.61x**, vì lúc
đó handshake là CPU chứ không phải mạng (`bench/p8-tlslab-upstream0.txt`).

### 4. Đổi cert dưới tải

```console
$ go run ./cmd/tlslab -mode reload -conns 64 -duration 10s -every 100ms
reload: 96 lần trong 10s dưới 64 connection: 98151 request ok, 0 lỗi, 1993 handshake mới; serial cuối 196
```

(`bench/p8-tlslab-reload.txt`) 96 lần thay cert trong 10 giây, 98 151 request, **0 lỗi**.

**Còn CPU handshake?** `go run ./cmd/tlslab -mode handshake` đo được, nhưng máy ồn (load 3-10) làm cùng biến thể lệch tới
2x giữa các lượt. Chỉ đọc tỉ số **trong cùng lượt** (`diary/phase8.md`,
`bench/p8-tlslab-handshake*.txt`, WSL2): resumed rẻ hơn full **1.29-1.50x** CPU; RSA-2048 đắt hơn
ECDSA chỉ **0.96-2.53x** vì đo cả hai phía trong một tiến trình; mỗi request một connection TLS chỉ
được **0.20-0.29** rps của plaintext. Bản đo tách tiến trình trên Linux thuần (CachyOS) ghi trong
`docs/debts.md` P8-1 (chưa có output thô trong repo).

## Bên trong: TLS gắn connection với một tên

### SNI chọn vhost, Host chỉ được xác nhận

Plaintext không có SNI, nên `Host` là nguồn duy nhất. Có TLS thì có hai nguồn, và chỉ một được
thắng. EdgeGate gắn connection với vhost của SNI một lần lúc handshake (`internal/proxy/tls.go`):

```go
func (s *Server) balancerFor(st *connState, req *httpx.Request) (*lb.Balancer, int) {
	host := hostOf(req.Header.Get("Host"))
	if st.tls != nil && sniPinsVHost && len(s.vhosts) > 0 {
		i := s.cfg.TLS.Store.VHostOf(st.tls.ConnectionState().ServerName)
		if i < 0 || i >= len(s.vhosts) {
			return nil, 421
		}
		if !s.vhosts[i].matches(host) {
			s.res.misdirected.Add(1)
			return nil, 421 // domain fronting
		}
		return s.vhosts[i].lb, 0
	}
	// ... plaintext: chọn theo Host
}
```

421 là mã RFC 9110 §15.5.20 dành đúng cho "connection này không phục vụ authority đó".

### Handshake là một lần đọc socket cần deadline riêng

Go bắt tay **lười**, ở lần `Read` đầu tiên. Không làm gì thì handshake chạy dưới deadline của lần
đọc request, tức `IdleTimeout`. EdgeGate gọi handshake tường minh ngay sau accept:

```go
func (s *Server) handshake(raw net.Conn, st *connState) (*tls.Conn, bool) {
	tc := tls.Server(raw, s.tlsCfg)
	st.tls = tc
	// ... (nodefense8 dừng ở đây: bắt tay lười)
	raw.SetDeadline(time.Now().Add(s.cfg.HandshakeTimeout))
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.HandshakeTimeout)
	defer cancel()
	if err := tc.HandshakeContext(ctx); err != nil {
		s.res.handshakeFail.Add(1)
		return nil, false
	}
	raw.SetDeadline(time.Time{})
	return tc, true
}
```

Lợi thêm: proxy biết SNI **trước** request đầu tiên, nên mới gắn được vhost như trên.

### Vì sao resumption ở TLS 1.3 không cắt RTT

TLS 1.2 full cần hai vòng: hello và cert, rồi key exchange và finished. Resumption 1.2 bỏ được một
vòng. TLS 1.3 đã gộp key exchange vào ClientHello, nên full chỉ còn **một** vòng, và một vòng là
tối thiểu khi client phải chờ ServerHello mới được gửi dữ liệu. Thứ duy nhất cắt được vòng đó là
0-RTT (early data), mà RFC 8446 nói rõ là đổi lấy việc mất chống replay. Go không nhận 0-RTT trên
TCP (`diary/phase8.md`). Vậy ở 1.3, resumption chỉ cắt **CPU** (bỏ ký và verify cert), không cắt RTT.

### Reload: đổi con trỏ, không sửa tại chỗ

`internal/tlsx/store.go` dựng một bộ cert **mới** hoàn chỉnh rồi mới đổi con trỏ:

```go
func (s *CertStore) load(entries []Entry) error {
	set := &certSet{byName: map[string]*tls.Certificate{}, /* ... */}
	for i, e := range entries {
		// ...
			cc, err := tls.LoadX509KeyPair(e.CertFile, e.KeyFile)
			if err != nil {
				return fmt.Errorf("tlsx: vhost %v: %w", e.Names, err) // bộ cũ giữ nguyên
			}
		// ... parse leaf, kiểm tên trùng, vhost mặc định
	}
	s.cur.Store(set) // atomic.Pointer: handshake kế tiếp thấy bộ mới
	s.loads.Add(1)
	return nil
}
```

Bộ cert không bao giờ bị sửa sau khi tạo, nên `GetCertificate` của mỗi handshake chỉ cần
`s.cur.Load()`, không cần khoá. File hỏng thì
`load` trả lỗi **trước** khi đổi, bộ cũ phục vụ tiếp. Connection đã bắt tay giữ cert nó đã thoả
thuận, nên không có gì để đứt.

## Mang về dùng

1. **Kiểm `Host` khớp SNI, lệch thì trả 421.** Proxy chọn cert theo SNI mà route theo `Host` là đang
   cho domain fronting đi qua. Đặt luôn một deadline riêng cho handshake.
2. **Pool tới upstream TLS quan trọng gấp đôi pool plaintext.** Mỗi connection mới tới upstream TLS
   1.3 tốn 2 RTT thay vì 1. Nếu bạn bật TLS giữa proxy và service (mTLS, service mesh), hãy kiểm
   keep-alive phía upstream trước tiên.
3. **Đừng trông vào session resumption để cắt RTT ở TLS 1.3.** Nó chỉ cắt CPU. Muốn ít RTT hơn thì
   giữ connection sống lâu hơn.

---

Số đo gốc: `bench/p8-tlslab*.txt`, `bench/p8-turn1-nodefense.txt`, [`diary/phase8.md`](../diary/phase8.md) (cả hai giả thuyết sai về CPU handshake).

**Bài tiếp theo:** [Bài 16: Bản đồ mang theo](16-ban-do-mang-theo.md)
