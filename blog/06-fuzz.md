# Bài 6 — Fuzz 7.75 triệu lần không thấy, bảng tay thấy

> Series [Mở nắp reverse proxy](README.md) · bài 6/16 · cần đọc trước: [bài 5](05-smuggling.md)

Bài 5 kết luận rằng proxy và backend phải đồng ý về ranh giới của từng request. Làm sao kiểm điều
đó trên mọi input, chứ không chỉ trên vài chục ca viết tay? Câu trả lời quen thuộc là differential
fuzzing: sinh input ngẫu nhiên, đưa cho cả parser của mình lẫn `net/http` của Go, và báo động khi
hai bên đọc ra khác nhau.

Cuối phase 4, tôi chạy nó 300 giây:

```console
PASS
ok  	github.com/thaivro/edgegate/internal/httpx	300.489s
fuzz: elapsed: 5m0s, execs: 7753072 (0/sec), new interesting: 40 (total: 767)
exit=0
```

(Nguồn: `bench/p4-difffuzz-300s.txt`. Máy WSL2. Số exec phụ thuộc máy, nên chỉ so được giữa các
lần chạy trên cùng máy.)

7 753 072 lần thử, 0 lệch. Xong chưa?

Chưa chắc. Một ngày trước đó, ở phase 2, tôi đã thấy đúng cảnh này: 7 712 575 lần, 0 lệch,
trong khi parser của tôi **có** hai lệch thật. Bài này kể vì sao fuzzer không thấy, và cái gì đã
thấy.

## Thí nghiệm

### Fuzz kiểm cái gì

`FuzzAgainstNetHTTP` không đòi hai parser giống nhau mọi thứ. Một bên từ chối còn bên kia nhận thì
không sao: request không tới backend. Nó chỉ đỏ ở hướng nguy hiểm (`internal/httpx/fuzz_test.go`):

```go
if errMine != nil || errTheirs != nil {
	return
}
bodyMine, eMine := io.ReadAll(mine.Body)
bodyTheirs, eTheirs := io.ReadAll(theirs.Body)
// Chỉ NGUY HIỂM theo một hướng: mình đọc được, oracle từ chối.
if eMine == nil && eTheirs != nil {
	t.Fatalf("NGUY HIỂM: mình đọc body xong (%d byte), net/http lỗi %v\ninput=%q", len(bodyMine), eTheirs, in)
}
// ...
if !bytes.Equal(restMine, restTheirs) {
	t.Fatalf("LỆCH RANH GIỚI: sau body mình còn %q, net/http còn %q\ninput=%q", restMine, restTheirs, in)
}
```

Hai bên cùng nhận head thì phải cùng đọc body, cùng số byte, và cùng phần còn lại. Phần còn lại là
chỗ request kế tiếp bắt đầu. Lệch ở đó chính là smuggling.

### Lần 1: fuzz phẳng, 390 giây, 0 lệch

```bash
make difffuzz   # FuzzAgainstNetHTTP, 300 s
```

Phase 2, bản parser đầu tiên:

```console
fuzz: elapsed: 5m0s, execs: 7712575 (29431/sec), new interesting: 253 (total: 461)
PASS
ok  	github.com/thaivro/edgegate/internal/httpx	300.704s
```

(Nguồn: `bench/p2-difffuzz-300s.txt`, WSL2.)

Thêm 90 giây nữa, bỏ hai seed viết tay: 2 914 539 execs, vẫn `PASS` (`bench/p2-difffuzz-lenient-noseed-90s.txt`).
Tổng 390 giây, 0 lệch.

### Bảng đối chiếu viết tay

Trước khi tin fuzz, tôi viết một test tạm: liệt kê 18 input khả nghi, in cạnh nhau `net/http` đọc
ra sao và parser của mình đọc ra sao. Theo nhật ký, việc này mất khoảng 5 phút. Ba dòng đáng chú ý:

| Input | `net/http` | Mình (bản đầu) | Loại |
|---|---|---|---|
| chunk-size `" 3"` | head OK, body **lỗi** `invalid byte in chunk length` | head OK, body `"abc"` | **NGUY HIỂM** |
| chunk-size `"3 ;x"` | như trên | `"abc"` | **NGUY HIỂM** |
| chunk-size `"3  "` | body `"abc"` | `"abc"` | khớp |

(Nguồn: `diary/phase2.md`, turn 2 bước 1.)

Parser của tôi bỏ khoảng trắng quanh chunk-size. Bỏ khoảng trắng trước `;` là đúng RFC, nên tôi
tưởng là an toàn. Go thì bỏ khoảng trắng ở đuôi mà không bỏ ở đầu. Cả hai cùng nhận head, rồi đọc body ra hai kết quả. Đúng loại lỗi mà fuzz
được viết ra để bắt, và nó đã chạy 7 712 575 lần mà không bắt được.

### Bộ so có hỏng không?

Câu hỏi đầu tiên: có khi nào bộ so sai, chứ không phải fuzzer? Phản chứng: giữ bản parser khoan
dung, thêm hai input từ bảng tay làm seed, chạy lại:

```console
--- FAIL: FuzzAgainstNetHTTP/chunk-bws-ext (0.00s)
    fuzz_test.go:122: NGUY HIỂM: mình đọc body xong (3 byte), net/http lỗi invalid byte in chunk length
--- FAIL: FuzzAgainstNetHTTP/chunk-leading-space (0.00s)
    fuzz_test.go:122: NGUY HIỂM: mình đọc body xong (3 byte), net/http lỗi invalid byte in chunk length
```

(Nguồn: `bench/p2-difffuzz-counterproof.txt`, trích.)

Đỏ 2/2, đúng lý do. Lần chạy phản chứng đầu tiên thật ra lại **xanh**, vì tôi để file seed ở
`testdata/fuzz/` gốc repo, trong khi Go tìm ở `internal/httpx/testdata/fuzz/`. Bài phản chứng xanh
vì lý do sai. Thêm `-v` thì thấy ngay seed không chạy. Chuyển thư mục thì đỏ. Vậy bộ so đúng. Chỉ là
fuzzer không tới được đó.

### Lần 2: fuzz có cấu trúc, 0.10 giây

`FuzzChunkLineAgainstNetHTTP` giữ head cố định và hợp lệ. Fuzzer chỉ được đột biến dòng chunk-size
và phần data:

```go
f.Fuzz(func(t *testing.T, sizeLine, data []byte) {
	// ...
	wire.WriteString("POST / HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\n\r\n")
	wire.Write(sizeLine)
	wire.WriteString("\r\n")
	wire.Write(data)
	wire.WriteString("\r\n0\r\n\r\nNEXT")
	// ... cùng điều kiện đỏ với FuzzAgainstNetHTTP
```

Chạy trên bản parser khoan dung (bật tạm lại phần bỏ khoảng trắng):

```console
fuzz: elapsed: 0s, gathering baseline coverage: 6/6 completed, now fuzzing with 6 workers
fuzz: minimizing 43-byte failing input file
--- FAIL: FuzzChunkLineAgainstNetHTTP (0.10s)
        fuzz_test.go:173: NGUY HIỂM: chunk-size " 3": mình đọc 3 byte, net/http lỗi invalid byte in chunk length
```

(Nguồn: `bench/p2-structfuzz-lenient-60s.txt`, WSL2.)

0.10 giây, và tự thu nhỏ về đúng `" 3"`. Cùng property, cùng oracle, cùng fuzzer. Chỉ khác ở chỗ
mọi input đều rơi vào vùng mà hai parser phân xử chunk-size.

Nếu bạn chạy `make difffuzz-chunk` hôm nay, bạn sẽ thấy 0 lệch. Lý do: bản parser khoan dung chỉ là
bản sửa tạm lúc đo, không nằm trong repo. Bản strict, 120 giây: 2 296 048 execs, `PASS`
(`bench/p2-structfuzz-strict-120s.txt`).

## Bên trong: lệch không phải là một nhánh code mới

Giả thuyết đầu tiên của tôi: Go chỉ instrument coverage cho package đang test, nên nhánh lỗi bên
trong `net/http` là vô hình với fuzzer. Nghe hợp lý. Kiểm bằng một lần build khô:

```console
$ go test -a -n -run '^$' -fuzz FuzzAgainstNetHTTP ./internal/httpx > dry.txt
$ grep -c '/compile ' dry.txt; grep '/compile ' dry.txt | grep -c 'd=libfuzzer'
207
180
== net/http:       libfuzzer 1 / compile 1
```

(Nguồn: `diary/phase2.md`, mục "Trả P2-1". Nhật ký chạy lệnh này qua wrapper `rtk proxy` để giữ
output thô.)

180 trên 207 package được instrument, có cả `net/http`. Giả thuyết sai.

Lời giải còn lại: fuzzer của Go được dẫn đường bằng coverage. Nó giữ lại input nào mở ra một nhánh
code mới. Nhưng một lệch differential không phải một nhánh mới. Nó là một **quan hệ** giữa hai
nhánh đều đã được chạy từ sớm: hàm bỏ khoảng trắng của tôi đã chạy qua header value, nhánh
`invalid byte in chunk length` của Go đã chạy qua bất kỳ chunk-line rác nào. Input `" 3"` không
thêm coverage cho bên nào, nên không được giữ lại. Fuzzer chỉ tới đó nếu đột biến ngẫu nhiên may
mắn trúng, trong không gian của cả một request.

Có một ca đối chứng. Sau khi siết chunk-size, fuzz 300 giây trên bản strict **tìm ra** một lệch
khác: chunk-size `"5 "`, Go nhận, tôi từ chối (`diary/phase2.md`, bước 5). Lệch này mở một nhánh lỗi
mới trong chính package của tôi, nên fuzzer có tín hiệu để bám. Nó thuộc hướng vô hại, và chính nó
làm lộ lỗi thứ hai: bản đầu của bộ so coi mọi bất đối xứng là lỗi. Bộ so được sửa để chỉ đỏ ở
hướng "mình nhận, oracle từ chối".

Bản sửa cho chunk-size thì strict hơn cả Go: không cho khoảng trắng ở bất kỳ đâu
(`internal/httpx/chunked.go`):

```go
func parseChunkSize(line []byte) (uint64, error) {
	if i := bytes.IndexByte(line, ';'); i >= 0 {
		line = line[:i]
	}
	if len(line) == 0 || len(line) > 16 {
		return 0, ErrBadChunk
	}
	for _, c := range line {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return 0, ErrBadChunk
		}
	}
	// ...
}
```

Mười bảy dòng của bảng tay được giữ lại làm seed `hand-*`, nên từ đó mỗi lần `go test` đều chạy lại
chúng.

## Mang về dùng

1. **"0 lệch" chỉ nói "không khác oracle trong những gì fuzzer đã nhìn thấy".** Nó không nói
   "đúng". Luôn chạy kèm một bảng đối chiếu tay cho những input bạn đã biết là nguy hiểm, và biến
   bảng đó thành seed.
2. **Fuzz không tới thì thu nhỏ không gian, đừng chạy lâu hơn.** 390 giây fuzz phẳng không ra.
   Cố định phần head, chỉ đột biến đúng chỗ hai parser phân xử, thì ra trong 0.10 giây.
3. **Bài phản chứng phải đỏ đúng lý do, và xanh cũng phải xanh đúng lý do.** Chạy với `-v` để đếm
   số seed thật sự đã chạy, trước khi kết luận bất cứ điều gì từ màu của test.

---

Số đo gốc: [`bench/p2-difffuzz-300s.txt`](../bench/p2-difffuzz-300s.txt),
[`bench/p2-difffuzz-lenient-noseed-90s.txt`](../bench/p2-difffuzz-lenient-noseed-90s.txt),
[`bench/p2-difffuzz-counterproof.txt`](../bench/p2-difffuzz-counterproof.txt),
[`bench/p2-structfuzz-*.txt`](../bench/), [`bench/p4-difffuzz-300s.txt`](../bench/p4-difffuzz-300s.txt).
Nhật ký: [`diary/phase2.md`](../diary/phase2.md), [`diary/phase4.md`](../diary/phase4.md).

**Bài tiếp theo:** [Bài 7: Retry có an toàn không?](07-retry.md)
