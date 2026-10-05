# Bài 11 — Một connection rỗi tốn bao nhiêu RAM?

> Series [Mở nắp reverse proxy](README.md) · bài 11/16 · cần đọc trước: [bài 3](03-keep-alive.md)

Bài 3 khuyên bạn giữ connection lâu. Giờ bạn có app mobile với 10 000 người dùng. Mỗi điện thoại mở
một connection keep-alive tới proxy, gửi một request, rồi im lặng vài chục giây. Phần lớn thời gian,
cả 10 000 connection đều **rỗi**. Không có byte nào chạy qua.

Proxy tốn RAM cho chúng không, bao nhiêu, vào cái gì? Bài này đo ba lần, mỗi lần bỏ bớt một thứ.
(Máy đo chính là laptop WSL2, nên chỉ tỉ số là đáng tin. Chỗ nào có số Linux thuần thì in cả hai cột.)

## Thí nghiệm

### Lần 1: server Go viết theo cách ai cũng viết

Một goroutine cho mỗi connection, cấp `bufio.Reader` và `bufio.Writer` ngay lúc accept. Mở 10 000
connection, mỗi connection không gửi gì, rồi đọc bộ nhớ. Chạy hai lần, bật và tắt `bufio`:

```bash
make netlab-mem   # = go run ./cmd/netlab -exp mem -conns 10000 -bufio=true, rồi -bufio=false
```

| 10 000 connection rỗi | WSL2, có `bufio` | WSL2, không `bufio` | Linux thuần (CachyOS), có `bufio` | Linux thuần, không `bufio` |
|---|---|---|---|---|
| HeapAlloc / conn | 17.35 KB | 9.20 KB | 17.41 KB | 9.26 KB |
| StackSys / conn | 4.01 KB | 2.01 KB | 4.04 KB | 2.04 KB |
| RSS / conn | 19.40 KB | 9.09 KB | 23.41–23.61 KB | 13.13–13.14 KB |

(Nguồn: `bench/p0-mem-rtt0.txt` cho WSL2; `bench/baseline/thai-computer-20261004/p0-mem-10k.txt` và
`p0-mem-nobufio.txt` cho Linux, ba lượt mỗi file. Client và server chạy chung một process, nên mỗi
"conn" ở đây là một **cặp** hai đầu.)

Heap và stack gần như trùng khít trên hai máy. Đó là con số của cấu trúc dữ liệu, không phải của môi
trường. Nhật ký phase 0 trừ hai cột WSL2: **10.31 KB/conn là tiền trả riêng cho `bufio` phía server**
(`diary/phase0.md`). Stack còn kể thêm một chuyện: không có `bufio` thì 2.01 KB, đúng bằng stack khởi
tạo của goroutine. Có `bufio` thì 4.01 KB, vì đường gọi sâu hơn bắt stack phải nới lên một lần. Vậy thủ phạm chính không phải goroutine. Là
**buffer được cấp trước khi có byte nào để đọc**.

### Lần 2: EdgeGate trả `bufio` về pool khi connection rỗi

EdgeGate có hai bản build. `nodefense9` là bản cũ: cầm `bufio` 2 × 8 KiB suốt đời connection. Bản mặc
định trả cả hai buffer về `sync.Pool` ngay khi xong một response. Mở 10 000 connection vào proxy thật
(tiến trình riêng), mỗi connection một request rồi giữ im:

```bash
make idlelab   # xen kẽ: nodefense9, mặc định, nodefense9, mặc định
```

```console
edgegate-nodefense9  conns 10000 (mở trong 10.866s) · VmRSS 8960 → 295040 KiB · Δ 28.61 KiB/conn
edgegate             conns 10000 (mở trong 20.655s) · VmRSS 8704 →  88960 KiB · Δ 8.03 KiB/conn
edgegate-nodefense9  conns 10000 (mở trong 13.099s) · VmRSS 8960 → 279552 KiB · Δ 27.06 KiB/conn
edgegate             conns 10000 (mở trong 11.809s) · VmRSS 8960 → 102016 KiB · Δ 9.31 KiB/conn
```

(Nguồn: `bench/p9-idlelab.txt`, WSL2.)

**27.1–28.6 xuống 8.0–9.3 KiB/conn**, tức 3.0–3.6 lần. 10 000 connection rỗi từ khoảng 280 MiB còn
90–100 MiB. Không đổi giao thức, không đổi mô hình goroutine, chỉ đổi **lúc nào** cầm buffer. 8 KiB còn lại chủ yếu là stack goroutine (`diary/phase9.md`). Trên Linux thuần, nợ P9-6 trong
`docs/debts.md` ghi 10.24 KiB/conn cho cùng phép đo (chưa có output thô trong repo).

### Lần 3: bỏ luôn goroutine

Muốn xuống dưới 8 KiB thì phải bỏ thứ giữ 8 KiB đó: goroutine đang chặn trong `Read`. `cmd/epolllab` có
hai server cùng một giao thức tối giản. Bản `netpoller` là Go thông thường: một goroutine và một buffer
4 KiB cho mỗi connection. Bản `epoll` gọi thẳng `epoll_wait`, không goroutine cho connection nào:

```bash
make epolllab   # RSS ở 10 000 conn rỗi, rồi rps với 256 conn closed-loop
```

```console
epoll      conns 10000 (mở trong 3.626s) · VmRSS 4608 →  6016 KiB · Δ 0.14 KiB/conn
netpoller  conns 10000 (mở trong 5.009s) · VmRSS 4224 → 81408 KiB · Δ 7.72 KiB/conn
epoll      conns 10000 (mở trong 5.567s) · VmRSS 4480 →  5888 KiB · Δ 0.14 KiB/conn
netpoller  conns 10000 (mở trong 5.274s) · VmRSS 4608 → 81152 KiB · Δ 7.65 KiB/conn
```

**0.14 so với 7.65–7.72 KiB/conn: 55 lần.** Một connection rỗi trên vòng epoll gần như miễn phí.

Vậy thì epoll nhanh hơn hẳn chứ? Cùng file, phần đo throughput (6 cặp xen kẽ, 256 connection):

```console
epoll      closed-loop 256 conn 10.003s: 911213 response, 91092 rps, lỗi 0 · CPU server 18.06 s = 19.8 µs/req
netpoller  closed-loop 256 conn 10.004s: 713689 response, 71344 rps, lỗi 0 · CPU server 15.28 s = 21.4 µs/req
epoll      closed-loop 256 conn 10.004s: 698778 response, 69851 rps, lỗi 0 · CPU server 15.84 s = 22.7 µs/req
netpoller  closed-loop 256 conn 10.004s: 1081831 response, 108140 rps, lỗi 0 · CPU server 18.50 s = 17.1 µs/req
... (thêm 4 cặp)
```

(Nguồn: `bench/p9-epolllab.txt`, WSL2. Closed-loop nên chỉ đọc rps, không đọc latency, xem bài 1.)

Tỉ số netpoller/epoll từng cặp nhảy từ 0.78 tới 1.55, lệch **cả hai chiều**. Trung vị là **1.01**. CPU
mỗi request của netpoller bằng 0.93 lần epoll (`diary/phase9.md`). Ít RAM hơn 55 lần, nhưng không nhanh
hơn chút nào.

### Phụ: `sync.Pool` giảm cấp phát 43 lần, nhanh hơn bao nhiêu?

Cùng phase đó, pool còn thay một buffer 32 KiB mà `copyBody` cấp cho mỗi response. Benchmark một
connection keep-alive, response 1 KiB, `make perflab` (3 lượt, xen kẽ hai bản):

```console
== lượt 1 tags=nodefense9
BenchmarkProxyKeepAlive/body=1024-4   22317   110908 ns/op   68129 B/op   47 allocs/op
BenchmarkProxyKeepAlive/body=1024-4   26401    94153 ns/op   68129 B/op   47 allocs/op
== lượt 1 tags=mặc định
BenchmarkProxyKeepAlive/body=1024-4   52701    48090 ns/op    1590 B/op   43 allocs/op
BenchmarkProxyKeepAlive/body=1024-4   48189    41958 ns/op    1593 B/op   43 allocs/op
```

(Nguồn: `bench/p9-perflab.txt`, WSL2. Lượt 2 và 3 cùng hình dạng.)

B/op giảm **43 lần** (68 129 → 1 590). ns/op giảm **khoảng 2.4 lần** (88–165 µs → 34–50 µs qua ba lượt).
Trước khi đo, nhật ký đăng ký dự đoán "ns/op giảm ≤ 15 %", với lý do "cấp phát không phải nút cổ chai,
syscall mới là". **Sai.** Nhưng 2.4 lần cũng không phải 43 lần. Vậy giá thật nằm ở đâu?

```console
$ GOGC=$g go test ./internal/proxy -run '^$' -bench ProxyKeepAlive -benchmem -tags nodefense9   # bản cũ
== GOGC=100   88170 ns/op  86345 ns/op    GC/20000 req: 419
== GOGC=400   52987 ns/op  54090 ns/op    GC/20000 req: 94
== GOGC=1600  50376 ns/op  46565 ns/op    GC/20000 req: 25
== GOGC=off  220085 ns/op 222345 ns/op
```

(Nguồn: `bench/p9-perflab-gogc.txt`. Số GC đếm bằng `GODEBUG=gctrace=1`; bản mới: 32 GC / 20 000 request,
ghi trong `diary/phase9.md`.)

Bản cũ, không đổi một dòng code, chỉ nới `GOGC` lên 16 lần là từ 87 µs về 48 µs, sát bản có pool
(`diary/phase9.md`). Tắt hẳn GC thì **chậm hơn** (220 µs): không GC thì mỗi lần cấp phát là trang nhớ
mới, tức page fault.

## Bên trong: chờ đọc thì cầm gì trong tay?

Khi một goroutine gọi `conn.Read` mà chưa có byte, nó không chặn một luồng OS. Runtime đăng ký fd vào
một epoll của chính nó (`runtime/netpoll_epoll.go`) rồi cất goroutine đi. Có byte thì epoll báo, runtime
đánh thức đúng goroutine đó. Hai server ở lần 3 vì vậy làm **cùng số syscall** mỗi request. Đó là lý do
rps hoà.

Chỗ khác nhau duy nhất là **trạng thái chờ nằm ở đâu**. Goroutine chờ trong `Read` thì phải có stack và
buffer **trước** khi có byte. Bản netpoller (`internal/epollsrv/netpoller.go`) gọi `make([]byte, 4096)`
ở đầu mỗi goroutine và giữ nó suốt đời connection. Vòng epoll (`internal/epollsrv/epoll_linux.go`) chỉ
giữ hai slice cho mỗi connection, cả hai `nil` khi rỗi, và một buffer đọc dùng chung cả loop:

```go
type conn struct {
	in, out []byte // head dở, response chưa ghi được. nil khi rỗi
}

type loop struct {
	conns map[int32]*conn
	buf   []byte // buffer đọc DÙNG CHUNG cả loop (64 KiB)
	// ...
}
// readAll: syscall.Read(int(fd), l.buf) chỉ khi epoll báo có byte.
```

0.14 KiB/conn chính là map entry cộng `conn{}` 48 byte. Cái giá: bạn tự viết state machine, tự xử lý
head bị cắt ngang nhiều lần đọc, tự đăng ký `EPOLLOUT` khi gặp `EAGAIN`. Những việc goroutine làm hộ.

### EdgeGate: vẫn goroutine, nhưng không cầm buffer lúc rỗi

EdgeGate không bỏ goroutine. Nó chỉ hoãn việc cầm `bufio` tới khi có byte đầu tiên của request kế tiếp
(`internal/proxy/proxy.go`, `serveConn`, bỏ bớt phần phụ):

```go
for {
	c.SetReadDeadline(time.Now().Add(lim.IdleTimeout))
	pipelined := br != nil && br.Buffered() > 0
	switch {
	case pipelined:
		// Byte request kế đã nằm trong br. KHÔNG trả br: trả là mất byte.
	case releaseIdleBufio:
		if br != nil {
			putReader(br)
			putWriter(bw) // đã Flush ở cuối response
			br, bw = nil, nil
		}
		if n, _ := c.Read(st.pre.first[:]); n == 0 { // chờ đúng 1 byte
			return
		}
		st.pre.has = true
		br, bw = getReader(&st.pre), getWriter(c)
	// ...
	}
	// ... đọc head, forward, ghi response
}
```

Connection rỗi chờ bằng một `Read` vào mảng 1 byte nằm sẵn trong `connState`. Có byte thì lấy `bufio`
từ pool, đọc qua `prefixReader` (`internal/proxy/bufpool.go`): nó trả byte đã đọc trước, rồi đọc tiếp từ
conn. Không cấp phát gì mỗi request.

Nhánh `pipelined` là luật không được phá: hai request trong một lần ghi thì byte của request thứ hai đã
nằm trong `br`, trả `br` là **mất request**. `TestIdlePipeliningAndPrefix` khoá luật này.

### Vì sao 43 lần cấp phát chỉ thành 2.4 lần tốc độ

Giá của một lần cấp phát 32 KiB là vài micro giây để xoá trắng vùng nhớ. Giá thật nằm ở **GC**. Heap
sống của proxy chỉ vài MiB, và mốc GC tối thiểu của Go là 4 MiB (`runtime/mgcpacer.go`). Bản cũ cấp
68 KiB mỗi request (hai buffer 32 KiB, một cho cả body request **rỗng**), nên cứ khoảng 46 request lại
kích một chu kỳ GC: 419–433 GC cho 20 000 request, so với 32 sau khi có pool.

Pool không làm cấp phát rẻ hơn. Nó làm GC **thưa** hơn. Nới GOGC cũng làm GC thưa hơn, nên cho cùng kết
quả. Phần chênh còn lại sau khi nới GOGC, khoảng 15 %, mới là "giá cấp phát" mà dự đoán ban đầu nói tới.

## Mang về dùng

1. **Đừng cấp buffer cho connection trước khi nó có byte để đọc.** Với keep-alive dài và nhiều client
   rỗi, buffer cấp lúc accept là phần lớn RAM. Trả về pool khi rỗi, trừ khi còn byte pipelining trong đó.
2. **Đo đúng cái mình định claim.** Giảm cấp phát 43 lần không có nghĩa là nhanh hơn 43 lần. Epoll tự
   viết ít RAM hơn 55 lần nhưng rps 1.01 lần. Đọc `B/op` để biết RAM, đọc số GC để biết thời gian.
3. **Goroutine-per-connection trả giá bằng không gian, không bằng thời gian.** Khoảng 8 KiB mỗi connection
   rỗi là giá của mô hình đó. Muốn connection rỗi gần như miễn phí thì phải đổi mô hình, không phải tối ưu.

---

Số đo gốc: `bench/p9-{idlelab,epolllab,perflab,perflab-gogc}.txt`, `bench/p0-mem-rtt0.txt`,
`bench/baseline/thai-computer-20261004/p0-mem-*.txt` · nhật ký: [`diary/phase9.md`](../diary/phase9.md) (G1, G3, G6, G7), [`diary/phase0.md`](../diary/phase0.md) (G5).

**Bài tiếp theo:** [Bài 12: Vì sao `httputil.ReverseProxy` tốn CPU gấp 3?](12-reverseproxy.md)
