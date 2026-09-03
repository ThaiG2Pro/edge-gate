# Phase 2 — log thô

> Bản biên tập: [`phase2.md`](phase2.md). File này ghi theo thứ tự thời gian, kể cả ngõ cụt.

## §0 Mang từ phase 1 sang (đọc lại trước khi gõ)

1. Đăng ký giả thuyết **trước turn 1** — lần này làm thật: bảng G1-G7 + D1-D8 trong `phase2.md`
   có timestamp trước file `.go` đầu tiên.
2. Đo hai điểm, kiểm quan hệ. Một con số hợp lý không phải bằng chứng.
3. Biến đầu tiên nhìn thấy trong lệnh tái hiện không phải nguyên nhân (đã đổ oan `-race` hai lần).
4. Đo user vs sys trước khi profile.
5. Bug nằm ở đường không test nào chạy: fuzz phase 1 mù với encoder; 3 mode lab xanh mù với
   đường lỗi. Sau khi mọi thứ xanh, **đọc code**.
6. Buffering che thứ mình đo. `bufio.Reader` trong parser là quyết định có chủ đích (xem
   "Ràng buộc kế thừa").
7. I3 thuộc tầng connection; `TestDribble` phase 1 chính là slowloris.
8. `-fuzzminimizetime 1s` bắt buộc.
9. I2 = 7s CPU + 4 GiB RSS / connection, không phải "địa chỉ ảo vô hại".

## §1 Turn 1 — 2026-09-03 16:02

Thứ tự viết: `parse.go` (readLine, request-line, headers) → `body.go` (framing §6.3) →
`chunked.go` → `request.go` / `response.go` → `write.go` → test → fuzz → `cmd/httplab`.

Viết xong lúc ~16:10. `go build ./... && go vet ./internal/httpx` sạch ngay lần đầu. Chạy test lần đầu:

```console
$ go test ./internal/httpx -count=1
--- FAIL: TestBodyNotDrainedBreaksNextRequest
    limits_test.go:90: muốn 400 vì 'helloGET' thành method, có <nil>
--- FAIL: TestReadRequestRejects/...magic_EDGG
    request_test.go:165: muốn *ProtoError http 400: , có unexpected EOF
```

**Phát hiện 1 — bẫy "quên drain body" im lặng hơn tôi nghĩ.** Tôi dự đoán `helloGET /2 HTTP/1.1`
sẽ bị từ chối 400. Sai: `helloGET` là token hợp lệ (§5.6.2) ⇒ parser **nhận** một request có
method `helloGET`, không lỗi, không log. Nghĩa là ở phase 3/5, bug drain body không tự lộ qua
error rate; nó lộ qua upstream nhận method lạ — hoặc, nếu body kết thúc bằng thứ vô hại, không
lộ gì cả. Sửa test để khẳng định điều này thay vì 400. (EDGG: chỉ là kỳ vọng sai — không CRLF
⇒ `ErrUnexpectedEOF`, chuyển sang `TestReadRequestEOF`.)

**Phát hiện 2 — `-race` bắt data race trong chính test helper.** `pair()` dùng chung biến `err`
giữa goroutine `Accept` và `Dial` ở goroutine chính:

```
WARNING: DATA RACE
Write at 0x00c000030420 by goroutine 10: httpx.pair.func1() httpx_test.go:46
Previous write at 0x00c000030420 by goroutine 9: httpx.pair() httpx_test.go:51
```

Phase 1 `pair` không dính vì tôi viết khác. Bài học nhỏ: helper test cũng là code; chạy `-race`
từ lần đầu, không đợi turn 2.

Sau hai sửa:

```console
$ go test ./... -count=1 -race
ok  	github.com/thaivro/edgegate/internal/frame	2.247s
ok  	github.com/thaivro/edgegate/internal/httpx	1.316s
$ go test ./internal/httpx -run '^$' -fuzz FuzzReadRequest -fuzztime 20s -fuzzminimizetime 1s
fuzz: elapsed: 21s, execs: 513633 (17215/sec), new interesting: 37 (total: 49)   ok
$ go test ./internal/httpx -run '^$' -fuzz FuzzAgainstNetHTTP -fuzztime 20s -fuzzminimizetime 1s
fuzz: elapsed: 20s, execs: 594208 (26569/sec), new interesting: 196 (total: 208)  ok
```

**Phát hiện 3 (chưa kết luận):** 20s differential fuzz **không** tìm được lệch nào. G1 dự đoán
≥ 1 ở 300s. 20s chưa đủ để nói G1 sai — nhưng "interesting" của diff-fuzz tăng nhanh gấp 4
(196 vs 37) vì oracle mở thêm nhánh. Để turn 2.

Số đo sớm từ test (chưa phải kết quả G — turn 2 chạy lại và dán nguyên văn):

```
limits_test.go:19: bomb 600027 byte: err=http 431: header too large, tiêu thụ 4096 byte
limits_test.go:32: big 120327 byte:  err=http 431: header too large, tiêu thụ 68281 byte
chunked_test.go:109: ngây thơ: make(4294967295) trong 4.152ms, đọc 3 byte, ΔTotalAlloc = 4294967328 byte
limits_test.go:138: slowloris: deadline cắt sau 200ms, nhận 10 byte
```

`make httplab` cả ba mode ✔ ở lần chạy đầu: parse 4/4 request, buffer còn 0; slowloris cắt ở
301ms nhận 6 byte (G6 dự đoán 6-7); response 5/5 gồm 204+CL và tới-EOF. Cột `buffered-sau`
(221 → 175 → 135 → 54 → 0) cho thấy cả 5 response nằm trong một Read và parser cắt dần.

Chưa làm ở turn 1: G-table chưa điền; fuzz dài (120s / 300s); phản chứng kiểu `nodefense` cho
httpx (phase 4 mới có `smugglelab-nodefense`); đọc lại code sau khi xanh (bài học §0.5).

## §2 Turn 2 — 2026-09-03 16:20 → 

Kế hoạch trước khi chạy: (1) difffuzz 300s nền; (2) G2/G3/G7 bằng test `-v`; (3) G4 bằng một
test tạm in `net/http` vs mình cạnh nhau; (4) race ×20; (5) FuzzReadRequest 120s; (6) đọc code.

### §2.1 Bảng đối chiếu oracle (test tạm `zz_g4_test.go`, đã xoá) — output nguyên văn, cắt cột

```
"GET / HTTP/1.1\nHost: x\n\n"                              net/http: err=<nil>            | mình: 400 bare LF
"...Content-Length: +5..."                                 net/http: bad Content-Length   | mình: 400
"...Content-Length:  5 ..."                                net/http: body="hello"         | mình: body="hello"
"...chunked\r\n\r\n 3\r\nabc\r\n0\r\n\r\n"                 net/http: body=""(invalid byte in chunk length) | mình: body="abc"   ← !!!
"...chunked\r\n\r\n3 ;x\r\nabc\r\n0\r\n\r\n"               net/http: body=""(invalid byte in chunk length) | mình: body="abc"   ← !!!
"...chunked\r\n\r\n0003\r\nabc..."                         net/http: "abc"                | mình: "abc"
"POST / HTTP/1.0 ... Transfer-Encoding: chunked ..."       net/http: err=<nil> body=""    | mình: 400 TE trên HTTP/1.0
"...Content-Length: 5\r\nContent-Length: 5..."             net/http: "hello"              | mình: "hello"
"GET / HTTP/1.1\r\nHost: x\r\n continued\r\n\r\n"          net/http: err=<nil>            | mình: 400 obs-fold
"...Transfer-Encoding : chunked..."                        net/http: err=<nil> body=""    | mình: 400 tên header
"GET / HTTP/2.0..."                                        net/http: err=<nil>            | mình: 505
--- vòng 2 ---
"...chunked\r\n\r\n3  \r\nabc..."                          go: "abc"                      | mình: "abc"        (Go trim ĐUÔI, không trim ĐẦU)
"...0\r\n continued\r\n\r\n"  (obs-fold trong trailer)      go: "abc"(malformed MIME header) | mình: "abc"(400 obs-fold)   khớp: cả hai lỗi
"Content-Length: 5, 5"                                     go: bad Content-Length         | mình: 400
"Content-Length: 5,5"                                      go: bad Content-Length         | mình: body="hello"   ← khoan dung vô ích
"POST /\tx HTTP/1.1"                                       go: invalid control character  | mình: nhận          ← khoan dung vô ích
```

Hai dòng `!!!` là lệch **cả hai nhận head**. Trong lúc đó difffuzz đã chạy 1m42s / 2.2M execs
không kêu. Sửa code ngay (chunk-size không whitespace; CL không phẩy; target VCHAR); test bảng
chunked đổi kỳ vọng ba dòng. Lần sửa test đầu `python replace` không khớp vì gofmt đã canh cột —
dùng regex.

### §2.2 Difffuzz 300s (bản turn 1, lenient) — `bench/p2-difffuzz-300s.txt`

```
fuzz: elapsed: 5m0s, execs: 7712575 (29431/sec), new interesting: 253 (total: 461)
ok  	github.com/thaivro/edgegate/internal/httpx	300.704s
```

0 lệch trên bản **có** lệch. G1 ❌ — và sai theo hướng làm deliverable "0 lệch" mất giá trị.

### §2.3 Phản chứng bộ so — hai lần

Lần 1: tạm `sed` chèn `trimOWS` lại, thêm 2 seed vào `testdata/fuzz/FuzzAgainstNetHTTP/` (gốc
repo), `go test -run FuzzAgainstNetHTTP` ⇒ **`ok 0.014s`**. Xanh. Nghi bộ so sai ⇒ `-v`:

```
=== RUN   FuzzAgainstNetHTTP/seed#0 ... seed#11
--- PASS: FuzzAgainstNetHTTP (0.01s)
```

Chỉ 12 seed của `f.Add`. Hai seed file không chạy: Go tìm corpus ở **thư mục package**
`internal/httpx/testdata/fuzz/<Fuzz>/`, không phải gốc repo. `mv` rồi chạy lại:

```
--- FAIL: FuzzAgainstNetHTTP/chunk-bws-ext
    fuzz_test.go:116: đọc body: mình err=<nil>, net/http err=invalid byte in chunk length
--- FAIL: FuzzAgainstNetHTTP/chunk-leading-space
    fuzz_test.go:116: đọc body: mình err=<nil>, net/http err=invalid byte in chunk length
```

Đỏ đúng lý do. Khôi phục bản strict ⇒ 90 test xanh (kể cả 2 seed).

### §2.4 Fuzzer có tự tìm được không? — `bench/p2-difffuzz-lenient-noseed-90s.txt`

Bản lenient, bỏ 2 seed tay, 90s: `execs: 2914539 (31979/sec), new interesting: 83 (total: 544)`,
ok. Không. Giả thuyết coverage-một-phía ghi ở phase2.md; chưa kiểm ⇒ P2-1.

### §2.5 Race, FuzzReadRequest, httplab

```
$ go test ./internal/httpx -count=20 -race       → ok 31.332s (bench/p2-race20.txt)
$ FuzzReadRequest 120s                           → execs: 1331499 (14717/sec), interesting 214, ok
$ make httplab                                   → bench/p2-httplab-GOTIT-00663.txt, 3 mode ✔, slowloris 300ms / 6 byte
```

Lưu ý tốc độ: FuzzReadRequest 14.7k/s trong khi difffuzz 29k/s **dù difffuzz chạy thêm cả
net/http**. Nghi `runtime.ReadMemStats` ×2 mỗi lần (stop-the-world) — P2-4, chưa đo.

### §2.6 Đọc code sau khi xanh

- `chunkedReader.first` không dùng ⇒ xoá.
- `lengthReader`: hai nhánh EOF (`n>0` ⇒ Unexpected; `n==0` ⇒ nil rồi EOF lần sau) — đúng.
- `framing` với `noBody` giữ nguyên TE/CL trong header ⇒ P2-2.
- `Header.Del("Content-Length")` trong `framing` là mutation ngầm lên header của caller — có
  comment ở D4; chấp nhận vì mục tiêu là "không forward cặp CL+TE".

### §2.7 Fuzz strict 300s lần 1 — ĐỎ

```
input="0 * HTTP/1.1\r\nHost:\r\nTrAnsfer-EnCoding:Chunked\r\n\r\n5 \r\n00000\r\n0\r\n\r\n"
fuzz_test.go:116: đọc body: mình err=http 400: bad chunk, net/http err=<nil>
Failing input written to testdata/fuzz/FuzzAgainstNetHTTP/3b2d4c17d449463f
```

`"5 "` trailing space: Go nhận (trim đuôi — §2.1 vòng 2 đã thấy `"3  "` được nhận), mình strict.
Hướng vô hại. Bộ so coi là lỗi ⇒ bộ so sai nửa: đổi thành chỉ đỏ khi `eMine==nil && eTheirs!=nil`
(nhãn `NGUY HIỂM`) hoặc byte khác nhau. Đổi tên crash file thành seed `chunk-trailing-space-fuzz-found`.
Phản chứng lenient chạy lại: đỏ 2/2 `NGUY HIỂM`. `go test ./... -race`: ok (frame 2.400s, httpx 1.528s).

Điểm đo thứ hai cho P2-1: lệch lộ ở nhánh lỗi của MÌNH thì fuzzer thấy; lộ ở nhánh lỗi của Go thì
không. Cùng fuzzer, cùng 300s.

### §2.8 Fuzz strict 300s lần 2 (bộ so đã sửa)

```
fuzz: elapsed: 5m0s, execs: 3330212 (9059/sec), new interesting: 105 (total: 652)
ok  	github.com/thaivro/edgegate/internal/httpx	301.048s
fuzz exit=0
```

Sạch. Corpus tay: `chunk-leading-space`, `chunk-bws-ext`, `chunk-trailing-space-fuzz-found`.
Turn 2 kết ~17:05.

## Thứ tự phát hiện (turn 2)

| # | Lúc | Phát hiện | Cách |
|---|---|---|---|
| 1 | 16:25 | `" 3"`, `"3 ;x"`: Go từ chối, mình nhận — lệch nguy hiểm | bảng đối chiếu tay |
| 2 | 16:25 | Go bỏ qua TE trên 1.0 (D3 đúng); `5,5` và tab target mình khoan dung vô ích | bảng tay |
| 3 | 16:32 | difffuzz 300s lenient: 0 lệch dù có lệch | fuzz |
| 4 | 16:40 | phản chứng xanh — seed sai thư mục | `-v` đếm seed |
| 5 | 16:45 | lenient không seed 90s: fuzzer tự không tìm được | fuzz |
| 6 | 16:58 | fuzz strict tìm `"5 "` — lệch trong coverage mình thì thấy; bộ so sai nửa | fuzz |
| 7 | 17:05 | strict + bộ so mới 300s: sạch | fuzz |

## §3 Turn 3 — 2026-09-03 17:15 → 17:30: biên tập

Việc: thêm Môi trường (output thật), khối Reproduce (chạy lại bước 1–3 để kiểm), bảng Invariant
với `file:hàm`, mục Đọc gì, viết lại Rút ra thành văn trả lời 5 câu hỏi + câu hỏi ROADMAP về
keep-alive/pipelining (dựa trên mô hình `reuse = 1 RTT, dial = 2 RTT` đo ở phase 0 §G2/G3).

Chạy lại Reproduce bước 1–3:

```
$ make test
ok  	github.com/thaivro/edgegate/internal/frame	2.214s
ok  	github.com/thaivro/edgegate/internal/httpx	1.307s
$ make test-chunkalloc
chunked_test.go:88:  stream: đọc 3 byte, err=unexpected EOF, ΔTotalAlloc = 4240 byte
chunked_test.go:110: ngây thơ: make(4294967295) trong 3.635ms, đọc 3 byte, ΔTotalAlloc = 4294967552 byte
$ make httplab
✔ ranh giới đúng, không byte nào lẫn giữa các request
✔ cắt ở 301ms (dự đoán ≈ 300ms), trả 408 và đóng
✔ 5 response, 5 ranh giới đúng; response cuối đọc tới EOF và Close=true
```

Điểm đo ngây thơ lệch +224 B so với lần trước (4294967328 → 4294967552): nhiễu cấp phát runtime
giữa hai lần `ReadMemStats`, không phải của reader; điểm đo stream y nguyên 4240.

Không sửa code ở turn 3. Không sửa số. Bảng "Thứ tự phát hiện" ở §2 giữ nguyên.

## Kết: 1/7 giả thuyết sai — và vì sao con số đó nói ít hơn phase 0/1

Phase 0 sai 5/8, phase 1 sai 4/7, phase 2 sai 1/7. Không phải vì đo giỏi hơn: 5 trong 7 "giả
thuyết" là về hành vi của code tôi sắp viết — chúng là spec được đặt tên G. Hai giả thuyết thật
(về fuzzer và về `net/http`) sai 1/2. Bảng giả thuyết sai *ngoài* G có 6 hàng; đó mới là tỉ lệ
thật của phase này. Lần sau: G chỉ dành cho thứ không nằm trong tay mình.

## §4 Trả P2-1 — 17:35 → 17:55

1. Kiểm giả thuyết trước: `rtk proxy go test -a -n -fuzz` (lệnh `go test` thường bị hook rtk tóm
   tắt mất output — mất một vòng vì thế). 207 compile, 180 có `-d=libfuzzer`, `net/http` có.
   **Giả thuyết sai.** Lần thứ ba trong hai phase tôi gán nguyên nhân cho biến đầu tiên nghĩ tới.
2. Lời giải còn lại: lệch differential không sinh edge mới ⇒ không có tín hiệu coverage.
3. `FuzzChunkLineAgainstNetHTTP` (head cố định, đột biến sizeLine+data). Bản lenient: đỏ sau 0.10 s,
   minimize về `" 3"`. Bản strict 120 s: xem `bench/p2-structfuzz-strict-120s.txt` — lần chạy đầu bị
   rtk tóm tắt thành một dòng "Go test: 1 passed", phải chạy lại qua `rtk proxy`.
4. 17 seed `hand-*` từ bảng §2.1. `go test -v`: 32 PASS cho FuzzAgainstNetHTTP, 6 cho ChunkLine.
5. `make difffuzz-chunk`; debts P2-1 → Đã trả; ROADMAP thêm hàng "390 s vs 0.10 s".

## §5 Turn trả nợ nhanh — 21:50 → 22:20 (P2-4, P0-5, P0-7, P0-2)

1. **P2-4.** `-cpuprofile` bị từ chối kèm `-fuzz`. Benchmark tạm: ReadMemStats 70–114 µs,
   thân fuzz 9.3–9.7 µs, `metrics.Read` 0.48–0.53 µs. Thay thẳng ⇒ 39.7k/s nhưng đỏ giả sau 2 s
   (506 232 B / 44 B, không tái hiện 3/3). Sửa hai tầng, min-of-3 ReadMemStats khi vượt trần.
   60 s: **32 828/s**, 0 đỏ giả. Phản chứng trần 1 B: đỏ 12/12 seed. Corpus giả bị xoá.
2. **P0-5.** Không cần code: `grep readFrame cmd/netlab` chỉ còn comment của P1-1. Đóng.
3. **P0-7.** 5 lần rtt: dial p50 125.6–154.4 µs, median 138.8; tỉ số 15.8–18.2x. Phase 0: 861.7 µs
   / 36.69x. Tỉ số **không** bất biến — sửa câu ở phase0.md, thêm Errata.
4. **P0-2.** `-rate 100000` ⇒ **panic** `bufio.(*Reader).Read` slice bounds 8246 > 8192. Stack qua
   `expOmission.func2.2` → `roundtrip` → `frame.Decode`. Đọc code: `sem` đếm slot, conn chọn
   `i % len(pool)` ⇒ hai goroutine chung một bufio.Reader. Sửa free-list `chan *clientConn`.
   `go run -race` 5 s: 0 DATA RACE. Đo lại: capacity **737 rps**, p50 1.34 ms. Makefile 1200 → 885.
   G4 mới: 377x / 8.82x. Bug này đã có mặt trong số G4 gốc của phase 0 (hàng đợi > 427 ms ⇒ va).
5. Sổ: `docs/debts.md` 4 món → Đã trả; `phase0.md` Errata 3 mục + 3 checkbox; `phase2.md` mục
   22:10 + 3 hàng Số đo; ROADMAP hàng ReadMemStats; `bench/p0-2-*`, `bench/p0-7-*`, `bench/p2-fuzz-*`.
