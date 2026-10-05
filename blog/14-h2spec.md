# Bài 14 — Qua hết bài kiểm tra chuẩn vẫn bị đánh sập

> Series [Mở nắp reverse proxy](README.md) · bài 14/16 · cần đọc trước: [bài 13](13-http2.md)

Bạn vừa viết xong server HTTP/2. Bạn chạy `h2spec`, bộ kiểm tra tuân thủ mà ai làm HTTP/2 cũng
dùng. Kết quả: 145/145, xanh hết. Bạn gắn nó vào CI và yên tâm.

Rồi bạn đọc về **Rapid Reset** (CVE-2023-44487). Kẻ tấn công không gửi frame nào sai luật. Nó mở một stream, huỷ ngay bằng
`RST_STREAM`, rồi lặp lại hàng nghìn lần trên một connection. Mọi frame đều đúng chuẩn. Server
vẫn sập.

Bài này đo xem bộ test chuẩn bắt được gì, và cái gì lọt qua nó.

## Thí nghiệm

Cần `h2spec` trong `~/go/bin` (`go install github.com/summerwind/h2spec/cmd/h2spec@latest`).
Chạy ở cấu hình mặc định (h1 và h2c chung một port), rồi ở chế độ chỉ h2c:

```bash
make h2spec
H2CFG=config/h2-only.json make h2spec
```

```console
$ make h2spec
    3.5. HTTP/2 Connection Preface
      × 2: Sends invalid connection preface
           Expected: Connection closed
             Actual: Error: http2: failed reading the frame payload: unexpected EOF, note that the frame header looked like an HTTP/1.1 header
145 tests, 144 passed, 0 skipped, 1 failed
$ H2CFG=config/h2-only.json make h2spec
145 tests, 145 passed, 0 skipped, 0 failed
```

(Chạy trên bản clone mới, 2026-10-05. Bản lưu trong repo: `bench/p10-h2spec-4.txt`, 144/145.)

Ca hỏng duy nhất là cố ý. h2spec gửi `INVALID CONNECTION PREFACE\r\n\r\n`. Trên port chung, đó
là một request HTTP/1.1 sai cú pháp, nên proxy trả `400` rồi đóng. Đúng với h1. Tách port thì hết.

Giờ phần thú vị. Repo có một bản build tắt **mọi** phòng thủ HTTP/2, bằng build tag `nodefense10`.
`make h2-bins` dựng sẵn nó ở `bin/edgegate-nodefense10`. Chạy đúng lệnh h2spec đó vào bản này:

```console
$ ~/go/bin/h2spec -h 127.0.0.1 -p 18093 -o 5      # trên bin/edgegate-nodefense10
      × 2: Sends invalid connection preface
        × 1: Sends a HEADERS frame that contains the header field name in uppercase letters
          × 1: Sends a HEADERS frame that contains the connection-specific header field
          × 2: Sends a HEADERS frame that contains the TE header field with any value other than "trailers"
          × 1: Sends a HEADERS frame with the "content-length" header field which does not equal the DATA frame payload length
          × 2: Sends a HEADERS frame with the "content-length" header field which does not equal the sum of the multiple DATA frames payload length
145 tests, 139 passed, 0 skipped, 6 failed
```

(`bench/p10-h2spec-nodefense10.txt`)

Bản không phòng thủ vẫn qua **139/145**. Năm ca nó hỏng thêm đều là luật về header field. Không ca
nào nói tới Rapid Reset hay CONTINUATION flood, vì h2spec **không có** ca nào cho hai thứ đó.

Vậy thử tấn công thật. Bốn test dưới đây chạy trên cả hai bản build:

```bash
go test ./internal/h2 ./internal/proxy -count=1 -v \
  -run 'TestRapidReset|TestContinuationFlood|TestH2Smuggle|TestH2RapidResetProxy'
make h2-nodefense      # 8 test tấn công, -tags nodefense10, PHẢI đỏ
```

```console
== tags=mặc định
    h2_test.go:439: streams=100 refused=4900 resets=100 peak_handlers=100
    h2_test.go:460: header block đệm tối đa = 65542 byte
    phase10_test.go:208: upstream thấy: []
    phase10_test.go:266: upstream nhận 143 request; h2 streams=202 refused=98 resets=201
== tags=nodefense10
    h2_test.go:439: streams=5000 refused=0 resets=5000 peak_handlers=5000
--- FAIL: TestRapidReset (0.07s)
    h2_test.go:460: header block đệm tối đa = 4194310 byte
--- FAIL: TestContinuationFlood (0.11s)
    phase10_test.go:211: SMUGGLED: upstream thấy ["POST /" "GET /smuggled"]
--- FAIL: TestH2Smuggle (0.91s)
    phase10_test.go:266: upstream nhận 4872 request; h2 streams=5000 refused=0 resets=5000
--- FAIL: TestH2RapidResetProxy (1.56s)
```

(`bench/p10-g6g7.txt`, đã cắt bớt)

| Một connection, một kẻ tấn công | có phòng thủ | `nodefense10` |
|---|---|---|
| h2spec | **145/145** (tách port) | **139/145** |
| Rapid Reset: handler chạy đồng thời | **100** | **5000** |
| Rapid Reset qua proxy: request tới upstream | **143** | **4872** |
| CONTINUATION flood: header block phải đệm | **65 542 B** | **4 194 310 B** |
| H2.CL: upstream thấy request lậu `/smuggled` | không | **có** |

Ở dòng h2spec, hai bản build chỉ khác nhau 6 ca trên 145. Ở các dòng tấn công, chúng khác nhau
hàng chục lần. Đây đều là số đếm, không phải thời gian. (Máy đo là laptop WSL2: với số thời gian
trong series thì chỉ tỉ số là đáng tin, còn số đếm ở đây không phụ thuộc máy nhiều như vậy.)

## Bên trong: h2spec kiểm ngữ pháp, không kiểm tài nguyên

h2spec hỏi: *"gửi frame sai thế này, server có trả đúng mã lỗi không?"* Câu hỏi đó về **cú pháp**.
Cả ba cuộc tấn công dưới đây chỉ dùng frame đúng cú pháp. Thứ chúng nhắm vào là **tài nguyên**:
số việc đang chạy, số byte đang đệm, và ranh giới giữa hai request.

Toàn bộ phòng thủ HTTP/2 của EdgeGate là sáu hằng số (`internal/h2/defense10.go`). Build tag
`nodefense10` đặt tất cả thành `false`:

```go
const (
	holdSlotUntilExit = true // slot MAX_CONCURRENT_STREAMS chỉ trả khi handler THOÁT
	capResetRate      = true // > 2×MAX_CONCURRENT_STREAMS RST trong 1 s ⇒ GOAWAY
	capHeaderBlock    = true // HEADERS + CONTINUATION cộng dồn có trần
	validateDowngrade = true // ký tự cấm, header connection-specific, CL ≠ tổng DATA
	coalesceCtl       = true
	collapseSettings  = true
)
```

### Rapid Reset: trần phải đặt lên việc, không đặt lên frame

`MAX_CONCURRENT_STREAMS = 100` nghe như một trần. Nhưng nó chỉ đếm stream **đang mở**. Client gửi
HEADERS, server khởi động handler, client gửi RST_STREAM. Stream đóng rồi, slot trống, client mở
stream kế. Handler cũ vẫn đang chạy. Lặp 5000 lần thì có 5000 handler cùng chạy, trên một
connection mà "trần" là 100.

Sửa ở hai chỗ (`internal/h2/conn.go`, bỏ bớt phần phụ):

```go
func (c *Conn) onRST(f *Frame) error {
	// ...
	if capResetRate {
		now := time.Now()
		if now.Sub(c.rstWindow) > time.Second {
			c.rstWindow, c.rstCount = now, 0
		}
		c.rstCount++
		if c.rstCount > 2*int(c.cfg.MaxConcurrentStreams) {
			return ConnError{ErrEnhanceYourCalm, "RST_STREAM trong 1 s (Rapid Reset)"}
		}
	}
	s.peerReset = true
	s.markReset(errStreamReset)
	if !holdSlotUntilExit && !s.exited {
		c.active-- // nodefense10: slot về ngay, handler vẫn chạy
		s.slotFreed = true
	}
	// ...
}
```

Chỗ thứ nhất: slot chỉ được trả trong `exitStream`, tức là khi goroutine của stream **thoát
thật**, không phải lúc nhận RST. Chỉ riêng việc đó đã đưa số handler đỉnh từ 5000 về 100.

Chỗ thứ hai là bài học riêng của một proxy. Khi nhận RST, proxy huỷ luôn request đang gửi lên
upstream. Huỷ là đúng, vì nó giải phóng tài nguyên. Nhưng huỷ nhanh thì slot cũng về nhanh, và
client vẫn bơm được hàng nghìn request sang upstream rồi bỏ. Vì thế cần thêm trần **tốc độ**: quá
2 × `MAX_CONCURRENT_STREAMS` lần RST trong một giây thì đóng connection bằng GOAWAY
`ENHANCE_YOUR_CALM`. Upstream nhận 143 request thay vì 4872.

### CONTINUATION flood: đệm cũng là tài nguyên

Header của một request có thể dài, nên HTTP/2 cho phép cắt nó thành HEADERS rồi nhiều frame
CONTINUATION, cho tới frame mang cờ `END_HEADERS`. Kẻ tấn công không bao giờ gửi cờ đó. Server
không thể giải mã nửa chừng, nên nó cứ đệm. Không có trần thì đệm 4 MiB và hơn nữa:

```go
// Không thể RST riêng stream: block chưa decode xong thì bảng HPACK của hai
// bên đã lệch ⇒ chỉ còn cách đóng connection.
func (c *Conn) checkHBCap() error {
	if capHeaderBlock && len(c.hb) > int(c.cfg.MaxHeaderListSize) {
		return ConnError{ErrEnhanceYourCalm, "header block > trần (CONTINUATION flood?)"}
	}
	return nil
}
```

Trần là `MaxHeaderBytes`, 64 KiB. Connection bị đóng ngay khi block đang ghép vượt nó, ở 65 542 byte.

### H2.CL: ranh giới request phải dựng lại

Đây là họ hàng của smuggling ở [bài 5](05-smuggling.md). Ở HTTP/2, request kết thúc khi gặp cờ
`END_STREAM`. Ở HTTP/1.1 phía upstream, request kết thúc theo `Content-Length`. Client khai
`content-length: 0` rồi gửi DATA chứa `GET /smuggled HTTP/1.1...`. Proxy chép header sang upstream
và chép body theo frame. Upstream đọc 0 byte body, rồi coi phần còn lại là request thứ hai.

```go
	s.recvd += int64(len(data))
	if validateDowngrade && s.declCL >= 0 && s.recvd > s.declCL {
		// byte vượt content-length KHÔNG BAO GIỜ vào body: với proxy
		// h2→h1 đó chính là request thứ hai trên connection upstream.
		c.mu.Unlock()
		c.refundCtl(n)
		return malformed(f.Stream, "DATA vượt content-length")
	}
	s.body = append(s.body, data...)
```

Kiểm **trước** khi byte vào buffer, không phải sau. Hai biến thể khác của downgrade (CR/LF trong
giá trị header, header `transfer-encoding`) bị chặn cả dưới `nodefense10`, vì serializer h1 của
phase 4 tự từ chối CR/LF/NUL và tự xoá header hop-by-hop. Đó là phòng thủ nhiều lớp đúng nghĩa.
Ranh giới body là chỗ duy nhất serializer h1 không tự kiểm được, vì nó không biết frame là gì
(`diary/phase10.md`).

## Mang về dùng

1. **Pass spec không có nghĩa là chịu được tấn công.** Bộ test tuân thủ đo xem bạn nói đúng ngôn
   ngữ chưa. Nó không đo việc một người nói đúng ngôn ngữ có giết được bạn không. Với mỗi trần tài
   nguyên, viết một test tấn công riêng, và chạy nó trên một bản build **tắt** phòng thủ: test đó
   phải đỏ. Test không bao giờ đỏ thì không chứng minh được gì.
2. **Trần phải đếm việc đang chạy, không đếm frame.** `MAX_CONCURRENT_STREAMS` chỉ là trần thật
   khi slot được giữ tới lúc handler thoát. Với proxy, thêm cả trần tốc độ huỷ, vì việc huỷ làm slot
   về nhanh. Mọi buffer có thể lớn dần theo input (header block, body) phải có trần trong lúc ghép,
   không chờ ghép xong mới kiểm.
3. **Proxy đổi giao thức thì phải tự dựng lại ranh giới request.** h2 → h1 là chỗ `content-length`
   và framing có thể nói hai điều khác nhau. Kiểm `content-length` bằng tổng DATA ngay khi nhận.

---

Số đo gốc: `bench/p10-h2spec-*.txt`, `bench/p10-g6g7.txt`, [`diary/phase10.md`](../diary/phase10.md) (cả lúc h2spec bắt được một bug thật, 143 → 144).

**Bài tiếp theo:** [Bài 15: TLS trước proxy tốn bao nhiêu?](15-tls.md)
