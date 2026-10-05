# Bài 5 — Hai server đọc một request ra hai cách

> Series [Mở nắp reverse proxy](README.md) · bài 5/16 · cần đọc trước: [bài 0](00-mot-request.md)

Bạn có nginx đứng trước một service. Một client gửi request có **cả** `Content-Length` lẫn
`Transfer-Encoding: chunked`. Hai header này cùng nói "body dài bao nhiêu", và chúng nói khác nhau:

```text
POST / HTTP/1.1\r\n
Host: h\r\n
Content-Length: 6\r\n
Transfer-Encoding: chunked\r\n
\r\n
0\r\n
\r\n
G
```

(Nguồn: `testdata/smuggle/01-clte-basic.txt`.)

Bên nào tin `Content-Length: 6` thì đọc đúng 6 byte `0\r\n\r\nG` làm body. Bên nào tin chunked thì
đọc tới chunk `0` là hết, và chữ `G` còn sót lại trên connection. Với bên đó, `G` là byte đầu tiên
của **request kế tiếp**. Nếu front và back chọn hai cách khác nhau, kẻ tấn công vừa nhét được một
mẩu request vào giữa luồng của người khác. Đó là request smuggling. Bài này đếm xem các server
thật đọc những request mơ hồ như thế ra sao.

## Thí nghiệm

### Proxy trả lời thế nào

Không cần Docker:

```bash
make smugglelab              # bộ ca ở tầng parser + e2e qua proxy thật
make smugglelab-nodefense    # bài phản chứng: lệnh này PHẢI đỏ
```

Thử bằng tay với binary thật: gửi ca CL.TE ở trên, rồi ngay sau đó, trên cùng connection, một
`GET /hello`. Proxy chỉ được trả **một** response:

```console
### CL.TE + GET pipelined — chỉ được 1 response:
HTTP/1.1 400 Bad Request
Connection: close
Content-Length: 67
Content-Type: text/plain; charset=utf-8

400 Bad Request: Content-Length and Transfer-Encoding both present
```

(Nguồn: `bench/p4-e2e-binary.txt`.)

Một response 400, rồi đóng. Cái `GET` đi sau không bao giờ được trả lời.

### Go `net/http` đọc cùng bộ ca ra sao

Mỗi file trong `testdata/smuggle/` là một ca, kèm status mong đợi, nguồn và lý do.
`TestSmugglingOracle` đưa từng ca cho `net/http.ReadRequest` của Go và so với EdgeGate:

```bash
go test ./internal/httpx/ -run TestSmugglingOracle -v
```

```console
G2: ca từ chối 52 — oracle cùng từ chối 24 (46 %), mình strict hơn 28, hướng nguy hiểm 0
G2: mình strict hơn net/http ở:
      01-clte-basic.txt (oracle body="" dư="G")
      02-tecl-basic.txt (oracle body="GPOST / HTTP/1.1\r\nContent-Type: ...x=1" dư="")
      30-bare-lf-request-line.txt (oracle body="hello" dư="")
      45-trailer-content-length.txt (oracle body="hello" dư="")
      50-connection-host.txt (oracle body="" dư="")
      ...
```

(Nguồn: `bench/p4-oracle.txt`, trích. Đo trên máy WSL2, nhưng bài này đếm ca đúng/sai chứ không đo
thời gian, nên môi trường không làm lệch số.)

Trong 52 ca EdgeGate từ chối, Go chỉ cùng từ chối 24, tức 46%. Nhìn dòng đầu: với ca CL.TE, Go
**nhận** request, đọc body rỗng, và để lại `dư="G"`. Go làm vậy là đúng RFC 9112 §6.1, cho phép bỏ
`Content-Length` khi có `Transfer-Encoding`. Một mình Go thì không sao. Đặt Go sau một front tin
`Content-Length`, chữ `G` thành request lậu.

Trước khi đo, tôi đăng ký giả thuyết Go sẽ cùng từ chối 60–75%. Sai. Go khoan dung ở nhiều chỗ hơn
tôi nghĩ, và đúng ở những chỗ RFC cho phép: bare LF, `Content-Length` trùng, trailer mang
`Content-Length`, `Connection: Host`.

### nginx và h2o

Go là một backend. Còn hai server mà bạn hay đặt sau proxy thì sao? Lệnh này cần Docker:

```bash
make oracle-ext   # nginx:1.25-alpine và h2o, phát 59 ca request qua TCP trần
```

`cmd/smuggleoracle` gửi từng ca thẳng vào mỗi origin và ghi status. h2o trả 404 cho mọi head hợp lệ
(không có file nào để phục vụ), nên 404 ở đây nghĩa là "đã đọc được khung request". Trích bảng:

```text
ca                                 mình       nginx      h2o
--------------------------------------------------------------------
01-clte-basic.txt                  400        400        404
21-cl-dup-same.txt                 400        400        404
30-bare-lf-request-line.txt        400        200        404
31-bare-lf-header.txt              400        200        404
41-chunk-size-trailing-space.txt   body-400   200        404
45-trailer-content-length.txt      body-400   200        404
50-connection-host.txt             400        200        404
80-ok-cl.txt                       ok         200        404

59 ca request
46 cặp (ca × origin) origin lenient hơn — proxy ta đứng trước nó phải chặn:
```

(Nguồn: `bench/p4-6-oracle-ext.txt`, trích. File không ghi máy đo.)

46 cặp (ca × origin) mà origin **nhận** thứ EdgeGate từ chối: nginx 17, h2o 29. Bare LF lọt qua cả
nginx lẫn h2o. `Connection: Host` cũng vậy. h2o nhận cả CL.TE cơ bản. Chiều ngược lại thì không
có: không ca nào EdgeGate nhận mà origin từ chối khung.

Đây là lý do nginx đứng trước vẫn chưa đủ: nếu proxy phía trước khoan dung **khác** origin phía
sau, hai bên sẽ thấy hai ranh giới.

## Bên trong: khi mơ hồ thì từ chối, không đoán

Bất biến số một của một proxy L7, như bài 0 đã nói: proxy và upstream phải luôn đồng ý về ranh giới
của mỗi request. Proxy không chọn được cách upstream đọc. Thứ duy nhất nó kiểm soát được là không
chuyển đi bất cứ thứ gì có thể đọc ra hai cách.

Quyết định framing nằm trong `internal/httpx/body.go`:

```go
func framing(proto string, h Header, isResponse, noBody bool) (int64, bool, error) {
	// ...
	if te := h.Values("Transfer-Encoding"); len(te) > 0 {
		if proto == "HTTP/1.0" {
			return 0, false, badRequest("Transfer-Encoding trên HTTP/1.0")
		}
		if h.Has("Content-Length") {
			if rejectCLWithTE {
				return 0, false, ErrAmbiguousFraming
			}
			// nodefense: ưu tiên CL, bỏ TE, đúng cách front-end trong CL.TE đọc.
			h.Del("Transfer-Encoding")
			n, err := parseContentLength(h.Values("Content-Length"))
			return n, false, err
		}
		if !teIsChunked(te) {
			return 0, false, ErrUnsupportedTE
		}
		return -1, true, nil
	}
	// ...
}
```

`rejectCLWithTE` là một trong bảy hằng của `internal/httpx/defense.go`. Mỗi hằng là một phòng
tuyến, và bản "tắt" của nó mô phỏng đúng cách một proxy khoan dung điển hình cư xử:

```go
const (
	rejectCLWithTE = true         // CL + TE ⇒ từ chối. false ⇒ ưu tiên CL
	rejectMultiCL = true          // nhiều dòng CL, kể cả giống nhau ⇒ từ chối
	strictLineEnding = true       // dòng phải kết thúc CRLF. false ⇒ nhận LF trần
	strictTE = true               // TE phải đúng một token "chunked"
	strictCLSyntax = true         // CL phải là chữ số thuần. false ⇒ nhận "+5", " 5"
	strictHeaderName = true       // "Transfer-Encoding : chunked" không thành TE thật
	rejectForbiddenTrailer = true // trailer mang CL/TE/Host ⇒ từ chối
)
```

Từ chối xong thì đóng connection. Trong `internal/proxy/proxy.go`, khi `httpx.ReadRequest` trả lỗi,
`serveConn` gọi `replyReadError` để ghi status rồi `return` khỏi vòng keep-alive, không đọc thêm.

### Phòng tuyến có răng không?

Một test xanh chưa chứng minh gì nếu tắt phòng tuyến mà nó vẫn xanh. `make smugglelab-nodefense`
build với tag tắt cả bảy hằng, và phải đỏ:

```console
--- FAIL: TestSmuggling/01-clte-basic.txt (0.00s)
smuggle_test.go:126: CL.TE cơ bản: CL=6, TE=chunked: muốn head bị từ chối 400, có status 0 err=<nil> (body="0\r\n\r\nG" rest="")
--- FAIL: TestSmuggling/02-tecl-basic.txt (0.00s)
smuggle_test.go:126: TE.CL cơ bản: TE=chunked, CL=4: muốn head bị từ chối 400, có status 0 err=<nil> (body="5c\r\n" rest="GPOST / HTTP/1.1\r\n...")
...
--- FAIL: TestSmugglingE2E/01-clte-basic.txt (0.01s)
smuggle_test.go:91: CL.TE cơ bản: CL=6, TE=chunked: status 404, muốn 400
```

(Nguồn: `bench/p4-smugglelab-nodefense.txt`, trích.)

Bản tắt đọc ca CL.TE như một front-end CL: body là `0\r\n\r\nG`, và qua proxy thật request tới
upstream, về 404 thay vì 400. Ở ca TE.CL, phần `rest` chính là cái `GPOST` lậu.

Giả thuyết thứ hai cũng sai. Tôi đoán tắt bảy hằng sẽ lật ít nhất 40% số ca từ chối. Đo được 20/52,
tức 38% (`diary/phase4.md`). 32 ca còn lại vẫn bị chặn bởi hàng rào phase 2 không có công tắc: ký
tự điều khiển, obs-fold, tên header không phải token, cú pháp Host. Tôi không nới bản tắt cho đủ số.

Một lưu ý khi bạn tự chạy: số ở trên đo lúc bộ ca có 61 file (`bench/p4-smugglelab.txt`). Thư mục
`testdata/smuggle/` giờ có 66 file, nên số ca bạn thấy hôm nay sẽ khác số trong bài.

## Mang về dùng

1. **Proxy phải chặt hơn thứ nó che chắn.** Origin "khoan dung đúng RFC" không có nghĩa là an toàn
   khi đứng sau một proxy khác. nginx và h2o ở trên nhận bare LF và `Connection: Host`; Go nhận
   CL.TE. Mỗi chỗ khoan dung đó là việc của proxy phải chặn thay.
2. **Thấy hai cách đọc thì từ chối và đóng connection.** Đừng chọn một cách hiểu, kể cả khi RFC
   cho phép chọn. Byte đi sau một head mơ hồ phải bị bỏ, không được phục vụ.
3. **Mỗi phòng tuyến cần một bài phản chứng đỏ.** Tắt nó đi, chạy lại bộ ca, và đếm xem cái gì lật.
   Nếu không có gì lật, hoặc bộ ca không chạm phòng tuyến, hoặc phòng tuyến không làm gì cả.

---

Số đo gốc: [`bench/p4-6-oracle-ext.txt`](../bench/p4-6-oracle-ext.txt),
[`bench/p4-oracle.txt`](../bench/p4-oracle.txt), [`bench/p4-smugglelab.txt`](../bench/p4-smugglelab.txt),
[`bench/p4-smugglelab-nodefense.txt`](../bench/p4-smugglelab-nodefense.txt). Nhật ký:
[`diary/phase4.md`](../diary/phase4.md). Bộ ca: [`testdata/smuggle/`](../testdata/smuggle/).

**Bài tiếp theo:** [Bài 6: Fuzz 7.75 triệu lần không thấy, bảng tay thấy](06-fuzz.md)
