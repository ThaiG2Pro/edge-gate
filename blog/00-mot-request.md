# Bài 0 — Một request đi qua proxy gồm những gì?

> Series [Mở nắp reverse proxy](README.md) · bài 0/16

Bạn đặt nginx trước API. Không đổi một dòng code nào của service. Rồi biểu đồ latency nhích lên
vài trăm micro giây. Không ai phàn nàn, vì vài trăm micro giây là quá nhỏ. Nhưng chúng không tự
nhiên mà có. Trong khoảng đó, có một chương trình đã nhận byte của bạn, đọc hiểu chúng, quyết định
gửi đi đâu, rồi viết ra một request **mới**.

Bài này đếm xem "thêm một proxy vào giữa" thật ra là thêm những việc gì. Các bài sau của series
đều sống trong khoảng giữa đó.

Bài này chỉ cần Go:

```bash
git clone https://github.com/ThaiG2Pro/edge-gate.git && cd edge-gate
go build -o bin/upstream ./cmd/upstream
go build -o bin/edgegate ./cmd/edgegate
bin/upstream -addr 127.0.0.1:8081 &
bin/edgegate -listen 127.0.0.1:8080 -upstream 127.0.0.1:8081 &
```

## Thí nghiệm

### Proxy không chuyển tiếp byte

Upstream mẫu có một đường `/eof` cố tình trả response kiểu cũ: không `Content-Length`, không
`Transfer-Encoding`, body kéo dài tới khi upstream đóng connection. Gọi nó qua proxy, một lần bằng
HTTP/1.1 và một lần bằng HTTP/1.0:

```console
$ curl -sS -i --max-time 5 http://localhost:8080/eof
HTTP/1.1 200 OK
Content-Type: text/plain
Transfer-Encoding: chunked

body until eof

$ curl -sS -i --max-time 5 --http1.0 http://localhost:8080/eof
HTTP/1.1 200 OK
Connection: close
Content-Type: text/plain

body until eof
```

(Output gốc: `bench/p3-proxylab-curl.txt`. Bản tự động chạy bốn `curl` một lượt: `make upstream` ở một
terminal, `make proxylab` ở terminal kia.)

Upstream không hề gửi `Transfer-Encoding: chunked`. Proxy tự thêm nó. Với client HTTP/1.1, proxy
mã hoá lại body thành chunked để client biết body kết thúc ở đâu mà connection vẫn sống. Với
client HTTP/1.0, vốn không hiểu chunked, proxy chép tới hết rồi đóng connection.

Cùng một response từ upstream, client nhận hai thứ khác nhau. Nếu proxy chỉ "chuyển byte" thì
chuyện này không thể xảy ra.

### Vài trăm micro giây đó là bao nhiêu

`make proxybench` tự dựng upstream và proxy, rồi gửi 2000 request `GET /hello` theo kiểu
closed-loop trên một connection: một lần gọi thẳng upstream, một lần đi qua proxy. Nó dùng lại
cổng 8080 và 8081, nên tắt hai tiến trình nền ở trên trước:

```bash
kill %1 %2
make proxybench
```

| GET /hello, closed-loop, 1 conn, n=2000 | WSL2 | Linux thuần (CachyOS), 3 lượt |
|---|---|---|
| gọi thẳng upstream, p50 | 139 µs | 41–49 µs |
| qua proxy, p50 | 473 µs | 91–96 µs |
| tỉ số p50 | **3.39x** (lượt 2: 3.20x) | **1.92–2.29x** |
| chênh tuyệt đối p50 | 333 µs | 45–54 µs |
| proxy tới upstream | dial mới mỗi request (bản phase 3) | pool bật |

(WSL2: `bench/p3-proxybench-GOTIT-00663.txt`. Linux: `bench/baseline/thai-computer-20261004/p3-proxybench.txt`.
Máy đo chính là laptop chạy WSL2, nên số tuyệt đối không mang sang máy khác được. Chỉ tỉ số là đáng tin.)

Dòng cuối của bảng quan trọng hơn các con số. Hai cột **không đo cùng một proxy**. Cột WSL2 đo bản
phase 3, khi proxy còn dial một connection mới tới upstream cho mỗi request. Cột Linux đo bản cuối,
khi pool đã bật. Dòng log đầu file Linux ghi rõ điều đó:

```text
edgegate: :8080 → [127.0.0.1:8081] lb=rr (nodelay=true, trusted_proxies=[], pool=true max_idle=64 ...)
```

Nhãn `(D1: dial mới)` trong bảng output của bench là nhãn cũ, viết từ phase 3, chưa sửa. Đọc nhãn
mà không đọc log thì sẽ so hai thứ khác nhau.

Vậy 333 µs của WSL2 gồm gì? Phase 0 đo riêng chi phí dựng một socket loopback trên cùng máy, ra
khoảng 320 µs (`diary/phase0.md`). Tức là gần hết khoản chênh là **một lần dial**. Phần còn lại mới
là việc của proxy. Muốn thấy riêng phần đó thì phải bật pool ở cả hai bên:

```bash
make poollab
```

| GET /hello, n=2000, pool bật | WSL2 | Linux thuần (CachyOS), 3 lượt |
|---|---|---|
| gọi thẳng upstream, p50 | 120 µs | 38–39 µs |
| qua proxy, p50 | 221 µs | 90–117 µs |
| overhead còn lại (qua proxy − thẳng) | **101 µs** | **51–78 µs** |

(WSL2: `bench/p5-poollab-rtt0.txt`. Linux: `bench/baseline/thai-computer-20261004/p5-poollab-rtt0.txt`.
Output in dòng này là `overhead còn lại p50 on − p50 thẳng`, kèm ghi chú "parse 2 chiều + goroutine,
KHÔNG còn dial".)

Đây là giá của một hop L7 khi không còn dial: vài chục tới cả trăm micro giây trên loopback, tuỳ máy.
Nó nhỏ. Nhưng nó là chỗ chứa mọi tính năng của proxy.

## Bên trong: hai connection, hai lần đọc, hai lần viết

Một request đi qua EdgeGate chạm vào **hai** connection TCP độc lập:

```text
 client ──── conn A ────► EdgeGate ──── conn B ────► upstream
        ◄─── response ───          ◄─── response ───

 trên conn A: đọc request (parse 1)          viết response (viết lại 2)
 trên conn B: viết request (viết lại 1)      đọc response (parse 2)
```

Hai connection này có vòng đời riêng. Client có thể giữ conn A keep-alive trong khi conn B về pool
cho client khác dùng. Client gửi `Connection: close` thì chỉ conn A đóng. Vì vậy proxy không được
chép header nguyên xi.

Vòng lặp chính nằm trong `internal/proxy/proxy.go` (`serveConn`). Mỗi vòng chờ byte đầu tiên, đặt
deadline, rồi gọi `httpx.ReadRequest` để biến byte của client thành một struct. Đó là parse 1.

Rồi `roundTrip` trong `internal/proxy/forward.go` dựng một request **mới** cho upstream. Nó không sửa
request cũ, nó viết lại từ đầu (bỏ bớt phần phụ):

```go
up := &httpx.Request{Method: req.Method, Target: req.Target, Proto: "HTTP/1.1", Header: req.Header.Clone()}
up.Header.StripHopByHop()                    // Connection, Keep-Alive, TE... chỉ mô tả conn A
if req.Chunked {
	up.Header.Set("Transfer-Encoding", "chunked") // chặng B do proxy tự quyết framing
}
clientIP := s.forwardedHeaders(up.Header, c, st) // X-Forwarded-For
// ...
ok, keep, release := s.admit(c, bw, req, clientIP) // rate limit, shed (bài 9)
// ...
be = bl.Pick(key)                            // chọn backend (bài 8)
pc, err = p.get()                            // lấy conn B từ pool hoặc dial (bài 3)
```

Phần trao đổi với upstream nằm trong `exchange`, cùng file. Đây là chỗ response được đọc rồi viết lại:

```go
writeErr := up.WriteHead(ubw)                // viết lại 1: head sang upstream
// ... chép body request qua reader có ranh giới, không io.Copy thô
r, err := httpx.ReadResponse(ubr, lim, req.Method) // parse 2
// ...
out := &httpx.Response{Proto: "HTTP/1.1", Status: resp.Status, Reason: resp.Reason, Header: resp.Header.Clone()}
out.Header.StripHopByHop()
switch {
case httpx.NoBody(req.Method, resp.Status): // HEAD / 204 / 304: không body
case resp.Chunked:
	out.Header.Set("Transfer-Encoding", "chunked")
	mode = modeChunked
case resp.ContentLength >= 0:
	mode = modeCopy
default: // body tới EOF
	if req.Proto == "HTTP/1.1" {
		out.Header.Set("Transfer-Encoding", "chunked") // đây là dòng curl ở trên thấy
		mode = modeChunked
	} else {
		mode = modeCopy
		closeClient = true
	}
}
// ...
out.WriteHead(bw)                            // viết lại 2: head về client
```

Nhánh `default` chính là thí nghiệm `/eof` ở đầu bài. Proxy biết body kết thúc ở đâu vì nó đã parse
response. Nó chọn lại framing cho chặng của client, vì framing là chuyện của từng chặng.

`StripHopByHop` (`internal/httpx/header.go`) cũng không chỉ xoá một danh sách cố định. Nó đọc giá trị
của `Connection:` và xoá luôn mọi header được liệt kê ở đó. Bỏ sót bước này là để attacker gửi header
xuyên qua proxy.

### Mọi bài sau đều nằm ở khoảng giữa

Nhìn lại hai đoạn code. Mỗi dòng là một chỗ có thể hỏng, và mỗi chỗ là một bài của series:

- Parse 1 đọc sai ranh giới body thì request thứ hai lọt qua mà không ai kiểm. Đó là smuggling
  ([bài 5](05-smuggling.md)).
- `p.get()` trả connection đã dùng: nhanh hơn bao nhiêu ([bài 3](03-keep-alive.md)), và nếu nó vừa bị
  upstream đóng thì có được retry không ([bài 7](07-retry.md)).
- `bl.Pick` chọn backend nào khi một node chậm ([bài 8](08-load-balancer.md)).
- `s.admit` quyết cho request chờ hay trả 503 ngay ([bài 9](09-shed.md)).
- Hai lần viết lại head đi qua socket TCP. Viết hai lần nhỏ liên tiếp thì có thể mất 40 ms
  ([bài 2](02-40ms.md)).

Một proxy L4 chỉ chép byte thì không có chỗ nào để hỏng như vậy, và cũng không làm được việc nào ở trên.

## Mang về dùng

1. **Proxy L7 không chuyển tiếp byte. Nó đọc hiểu rồi viết lại.** Header client gửi chưa chắc là
   header upstream nhận, và framing upstream trả chưa chắc là framing client thấy. Khi debug qua
   proxy, hãy bắt gói ở **cả hai** chặng, đừng giả định chúng giống nhau.
2. **Đo overhead của proxy thì so cùng cấu hình.** Pool bật hay tắt đổi con số gấp mấy lần, như
   bảng đầu bài. Ghi rõ cấu hình cạnh con số, và đọc log của chính proxy thay vì tin nhãn trong
   output bench.
3. **Phần lớn chi phí của một hop là dial, không phải parse.** Khi pool tắt, gần hết khoản chênh
   là một lần dựng socket. Bật pool tới upstream trước khi đi tối ưu parser.

---

Số đo gốc: [`bench/p3-proxylab-curl.txt`](../bench/p3-proxylab-curl.txt),
[`bench/p3-proxybench-GOTIT-00663.txt`](../bench/p3-proxybench-GOTIT-00663.txt),
[`bench/p5-poollab-rtt0.txt`](../bench/p5-poollab-rtt0.txt),
[`bench/baseline/thai-computer-20261004/`](../bench/baseline/thai-computer-20261004/) (Linux thuần) ·
nhật ký: [`diary/phase3.md`](../diary/phase3.md), [`diary/phase0.md`](../diary/phase0.md) (chi phí dựng socket).

**Bài tiếp theo:** [Bài 1: Benchmark của bạn đang nói dối](01-benchmark-noi-doi.md)
