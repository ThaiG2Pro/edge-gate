# Phase 2 — HTTP/1.1 engine: request + response parser, chunked, keep-alive

- **Thời lượng dự kiến:** 2-3 ngày · **thực tế:** ~1 buổi (3 turn: code / run+fix / diary), 16:02 → 17:30
- **Bắt đầu:** 2026-09-03 · **Kết thúc:** 2026-09-03
- **Trạng thái:** ✅ xong phần engine. Diff-fuzz 300s ×2, bộ so có phản chứng đỏ, 3 lệch thật đã đóng.
  Còn 4 nợ mở (P2-1..P2-4), nợ P2-1 là nợ *về bằng chứng*, không phải về code
- **Commit:** `a2b0ab5` — git init 2026-09-03 sau khi phase 2 đã xong; commit này là cây làm việc phase 2 ở trạng thái cuối (đã trả P2-1, P2-4), không phải snapshot lúc đo.

> **Đường đi thô, kể cả ngõ cụt:** [`phase2-log.md`](phase2-log.md). File này là bản biên tập.
>
> **Quy tắc ghi nhật ký:** mọi con số, mọi kết luận đều phải kèm **lệnh shell sinh ra nó**
> và **output thật**. Luật đầy đủ: [`../skills/diary/SKILL.md`](../skills/diary/SKILL.md).

## Ràng buộc kế thừa

- **Tuyệt đối không dùng `net/http` trên data path.** Chỉ `net`. `net/http` xuất hiện đúng hai
  chỗ: fixture `cmd/upstream` (phase 3) và **oracle** của differential fuzzing trong file test.
- **I2 (kiểm trần trước cấp phát)** giờ có nghĩa cụ thể từ P1-6: một `length` bịa = **7 giây CPU
  kernel + 4 GiB RSS mỗi connection**. Ở HTTP, "length" xuất hiện ở ba chỗ: dòng header, khối
  header, **chunk-size**. Cả ba phải có trần kiểm trước khi gom byte; body theo `Content-Length`
  thì **stream**, không `make` theo CL.
- **I3 (mọi connection có deadline)** là việc của tầng connection, không của parser. Parser nhận
  `*bufio.Reader`; ai cầm `net.Conn` thì phải `SetReadDeadline` trước mỗi lần đọc head/body.
- **`bufio.Reader` ở parser là lựa chọn có chủ đích, không phải mặc định** (phase 0 G1, phase 1
  raw Read): HTTP/1.1 là giao thức theo dòng, không có prefix độ dài ⇒ phải nhìn trước để tìm
  CRLF ⇒ cần buffer. Hệ quả phải chấp nhận: byte của request sau có thể đã nằm trong buffer
  khi request trước chưa đọc body xong. **Body của request N phải đọc hết từ cùng một
  `bufio.Reader` trước khi parse request N+1** — bẫy #3 phase 3.

## Môi trường

Cùng máy phase 0/1 (`bench/env-GOTIT-00663.txt`). Phase này chỉ có số **đếm** (byte, execs, lần
Read) và một số thời gian có sai số cho phép ±100 ms (slowloris), nên ba bẫy đo mạng không áp
dụng; mọi test TCP chạy trên **loopback thật**, không `net.Pipe` (lý do đo ở phase 1 G6/P1-4).
Fuzz chạy trên cả 6 core; hai lần fuzz chạy chồng nhau thì execs/s giảm ~3x (ghi ở Bước 6).

```console
$ uname -srmo && go version && nproc && ulimit -n
Linux 6.6.87.2-microsoft-standard-WSL2 x86_64 GNU/Linux
go version go1.26.2 linux/amd64
6
1048576
```

## Mục tiêu phase

`internal/httpx` đọc được request **và response** HTTP/1.1 từ `*bufio.Reader`, cắt body đúng
ranh giới theo RFC 9112 §6.3, giải mã chunked mà không cấp phát theo chunk-size, giữ được
keep-alive (byte của request sau không bị nuốt), và có bằng chứng mạnh hơn "không panic":
**differential fuzzing với `net/http` — 0 chỗ lệch ranh giới body** khi cả hai bên nhận request.

## Câu hỏi phải trả lời được (viết trước khi code)

1. Thứ tự ưu tiên framing body (§6.3) — và vì sao request/response **không đối xứng** khi không
   có CL lẫn TE?
2. Chunk-size `FFFFFFFF` có làm decoder cấp phát 4 GiB không? Bằng chứng?
3. Header bomb 100 000 dòng `a: b` bị chặn **sau bao nhiêu byte đọc vào** — trần đếm hay trần byte
   nổ trước?
4. `net/http` khoan dung ở chỗ nào mà mình strict? Khoan dung theo hướng nào thì **nguy hiểm**
   (cả hai nhận nhưng khác ranh giới) và hướng nào thì vô hại (một bên từ chối)?
5. Slowloris 1 byte/50ms: ai phát hiện — parser hay tầng connection? Bao lâu?

## Giả thuyết đăng ký TRƯỚC khi đo

Khác phase 1 (chỉ 3/7 viết trước): **cả bảng này viết lúc 16:02 ngày 2026-09-03, trước khi tạo
file `.go` đầu tiên của phase**. Cột kết quả điền ở turn 2.

| # | Giả thuyết | Kỳ vọng | Kết quả | Đúng? |
|---|---|---|---|---|
| G1 | Differential fuzz 300s lần chạy **đầu** sẽ tìm được ≥ 1 chỗ hai bên cùng nhận request nhưng lệch số byte body | ≥ 1 crash | **0** lệch / 7 712 575 execs — trong khi bảng đối chiếu **viết tay** tìm ra 2 lệch thật trong 5 phút (chunk-size `" 3"`, `"3 ;x"`); bản lenient không seed thêm 90s/2.9M execs vẫn 0 | ❌ |
| G2 | Header bomb 100 000 × `a: b\r\n` (500 KB) bị chặn bởi **`MaxHeaderCount`** (100) trước `MaxHeaderBytes` (64 KB); parser chỉ đọc vào ≤ 4 KiB + 101 dòng | 431, byte tiêu thụ < 8 KiB | 431, tiêu thụ **4096** byte (đúng một lần fill bufio) | ✅ |
| G3 | Chunk-size `FFFFFFFF` không làm `TotalAlloc` tăng: chunked reader stream theo `len(p)`, không `make` theo size | ΔTotalAlloc < 64 KiB | Δ **4240** byte; điểm đối chứng (decoder ngây thơ `make(size)`) Δ **4294967328** byte | ✅ |
| G4 | `net/http` **nhận** bare LF (textproto tolerant), mình từ chối ⇒ fuzz thấy lớp lệch "một bên từ chối" — vô hại, không tính là crash | Go nhận `GET / HTTP/1.1\nHost: x\n\n` | Go nhận (`err=<nil>`). Cũng nhận: obs-fold, `HTTP/2.0`, `Transfer-Encoding :` (thành header thường), và **bỏ qua TE trên HTTP/1.0** — đúng lớp D3 đã tránh | ✅ |
| G5 | 3 request pipelined trong **một** Write ⇒ parser trả đúng 3 request, sau request thứ 3 `br.Buffered() == 0`, body request 2 không lẫn vào request 3 | 3/3, leftover 0 | test 3/3 buffer 0; httplab 4/4 từ 281 byte, buffer 0 | ✅ |
| G6 | Slowloris 1 byte/50ms, `HeaderTimeout` 300ms ⇒ tầng connection đóng ở ~300ms sau **byte đầu**, nhận được ≈ 6-7 byte; parser không biết gì về thời gian | 300-400ms, 6-7 byte | **300ms / 301ms**, **6 byte**, err là `net.Error` Timeout; trả 408 | ✅ |
| G7 | Response `204` kèm `Content-Length: 5` ⇒ body 0 byte và **5 byte kia còn nguyên trong buffer** (sẽ bị hiểu là đầu response sau — cái bẫy phase 3) | body 0, Buffered 5 | body 0, dư `"hello"` (5 byte) | ✅ |

Dự đoán tổng: **≥ 2/7 sai**. Phase 0 sai 5/8, phase 1 sai 4/7 — nếu phase này sai 0/7 thì nghi
bộ đo trước khi nghi mình giỏi lên.

**Kết quả: 1/7 sai.** Thấp hơn dự đoán, và lý do không phải "giỏi lên": nhìn lại, G2/G3/G5/G6/G7
là dự đoán về **code tôi sắp viết và sắp test** — đó là *spec*, không phải giả thuyết; chúng
đúng vì tôi viết code cho chúng đúng. Chỉ G1 và G4 là dự đoán về thứ **bên ngoài** (fuzzer,
`net/http`), và 1/2 sai. Lần sau: chỉ tính là giả thuyết khi đối tượng không nằm trong tay mình.
Các giả thuyết sai *ngoài bảng* (sáu hàng, xem "Giả thuyết sai") mới là chỗ học được.

## Quyết định thiết kế (ghi trước, sẽ đối chiếu sau khi fuzz)

| # | Quyết định | Lý do | Rủi ro với oracle |
|---|---|---|---|
| D1 | Bare LF ⇒ 400 (strict) | Nguồn smuggling là strict/lenient lệch nhau; chọn strict và **ghi lại** (ROADMAP phase 4) | Go nhận, mình từ chối ⇒ vô hại |
| D2 | `Transfer-Encoding` phải là **đúng một dòng, đúng một token `chunked`** (không phân biệt hoa/thường); mọi thứ khác ⇒ 501 | Khớp `net/http` ≥ 1.15; đơn giản hơn bảng coding | thấp |
| D3 | HTTP/1.0 mà có TE ⇒ 400 | Go **bỏ qua** TE trên 1.0 rồi dùng CL — nếu mình honor TE thì cả hai nhận mà khác ranh giới = đúng lớp lỗi phải tránh | đã tránh bằng cách từ chối |
| D4 | CL + TE cùng có ⇒ phase 2 **dùng chunked, xoá CL khỏi header** (không forward CL). Phase 4 đổi thành từ chối | ROADMAP phase 2 nói vậy; xoá CL để không tự tạo CL.TE khi reserialize | Go làm y vậy ⇒ khớp |
| D5 | Nhiều `Content-Length` giống nhau ⇒ nhận; khác nhau, có dấu, có space, hex ⇒ 400 | RFC 9110 §8.6; Go nhận trùng-giống | Go trim space rồi nhận ⇒ một bên từ chối, vô hại |
| D6 | Body theo CL: **stream** qua reader giới hạn; EOF sớm ⇒ `io.ErrUnexpectedEOF`, không phải `io.EOF` | proxy phải biết body bị cắt cụt, không được coi là "xong" | — |
| D7 | Không có CL, không TE: **request ⇒ 0 byte; response ⇒ đọc tới EOF, Close=true** | RFC 9112 §6.3 bước 7-8; bất đối xứng | — |
| D8 | HEAD / 1xx / 204 / 304 ⇒ response không body **kể cả có CL/TE**; header giữ nguyên để forward | §6.3 bước 1-2 | — |

## Deliverable

- [x] `internal/httpx`: `ReadRequest`, `ReadResponse`, chunked reader/writer, `WriteHead`, `Header` có test (turn 1)
- [x] `FuzzReadRequest` 120s sạch (1 331 499 execs); `FuzzAgainstNetHTTP` 300s: **0 lệch** — nhưng xem G1: 0 lệch là điều kiện cần, không phải bằng chứng
- [x] `cmd/httplab`: parse file pipelined / slowloris / response — in số, ✔/SAI (turn 1, ba mode ✔ lần đầu)
- [x] Bài phản chứng I2 cho chunk-size (ΔTotalAlloc 4240 vs 4294967328)
- [x] Fuzz có cấu trúc `FuzzChunkLineAgainstNetHTTP` + 17 seed từ bảng đối chiếu tay (P2-1)
- [x] Bài phản chứng cho **bộ so** diff-fuzz: bản lenient + 2 seed viết tay ⇒ ĐỎ đúng lý do (`bench/p2-difffuzz-counterproof.txt`)
- [x] Trả P-code-1 (`header_test.go`: canonical, `Te`, hop-by-hop kể cả field liệt kê trong `Connection`, Write sắp xếp + chặn injection)

## Reproduce toàn bộ phase

```bash
# 1. unit + race (≈ 4 s)
make test
# 2. ba lab, in số và ✔/SAI (≈ 6 s; slowloris cố ý mất 0.3 s)
make httplab
# 3. I2 cho chunk-size: hai điểm đo, cần ~4 GiB địa chỉ ảo (≈ 0.1 s)
make test-chunkalloc
# 4. fuzz không panic (120 s) và diff-fuzz với net/http (300 s)
make fuzz-http
make difffuzz
make difffuzz-chunk        # P2-1: fuzz có cấu trúc, 120 s
# 5. phản chứng bộ so: PHẢI đỏ. Tạm khoan dung whitespace trong chunk-size rồi chạy seed corpus
cp internal/httpx/chunked.go /tmp/chunked.go.bak
sed -i 's|\tif len(line) == 0 \|\| len(line) > 16 {|\tline = trimOWS(line)\n\tif len(line) == 0 \|\| len(line) > 16 {|' internal/httpx/chunked.go
! go test ./internal/httpx -run FuzzAgainstNetHTTP -count=1     # đỏ, 2 seed, nhãn "NGUY HIỂM"
cp /tmp/chunked.go.bak internal/httpx/chunked.go
```

Đã chạy lại bước 1–3 sau khi chốt (17:20): `ok internal/httpx 1.307s`; Δ 4240 / 4294967552 B
(số thứ hai lệch +224 B so với lần đo trước — nhiễu cấp phát của runtime, không phải của reader);
ba mode ✔, slowloris 301 ms. Bước 4–5 là các lần chạy ghi ở Bước 4–6 bên dưới và `bench/p2-*`.

## Nhật ký

### 2026-09-03 16:02 — Turn 1: đăng ký xong, bắt đầu code

Xong ~16:15. Ba điều xảy ra trước khi mọi thứ xanh (chi tiết + output: log §1):

1. **Bẫy quên-drain-body im lặng hơn dự đoán.** `hello` + `GET /2 …` ⇒ method `helloGET` —
   token hợp lệ, parser nhận không lỗi. Không có error rate để mà thấy. Test giữ lại đúng
   hành vi này thay vì cái 400 tôi tưởng.
2. **`-race` bắt data race trong `pair()` của test** (biến `err` dùng chung Accept/Dial). Sửa.
3. `go test ./... -race` xanh; fuzz smoke 2×20s sạch (513k / 594k execs); `make httplab` ba
   mode ✔ lần đầu — slowloris cắt ở 301ms, nhận 6 byte.

Bảng G chưa điền: đó là việc của turn 2 (chạy đúng lệnh, dán nguyên văn).

### 2026-09-03 16:20 — Turn 2: run và fix bug

**Bước 1 — bảng đối chiếu oracle viết tay, trước khi tin fuzz.** Một test tạm liệt kê 18 input
khả nghi và in cạnh nhau `net/http` vs mình (output đầy đủ: log §2). Ba phát hiện:

| Input | `net/http` | Mình (turn 1) | Loại |
|---|---|---|---|
| chunk-size `" 3"` | head OK, body **lỗi** `invalid byte in chunk length` | head OK, body `"abc"` | **NGUY HIỂM**: cả hai nhận head, lệch body |
| chunk-size `"3 ;x"` | như trên | `"abc"` | **NGUY HIỂM** |
| chunk-size `"3  "` (trailing) | body `"abc"` | `"abc"` | khớp — nhưng cho thấy Go trim *đuôi* mà không trim *đầu* |
| `Content-Length: 5,5` | từ chối | nhận 5 | mình khoan dung hơn, vô ích |
| target `/\tx` | từ chối (net/url) | nhận | mình khoan dung hơn, vô ích |
| HTTP/1.0 + TE chunked | nhận, **bỏ qua TE**, body 0 | 400 (D3) | đã tránh đúng lớp nguy hiểm |
| bare LF, obs-fold, `HTTP/2.0`, `Transfer-Encoding :` | nhận | từ chối | vô hại (G4) |

Hai dòng đầu là đúng lớp lỗi diff-fuzz sinh ra để bắt — và nó đã chạy 300s, 7.7M execs, **không
bắt được** (G1 ❌). Sửa: chunk-size **không whitespace ở bất kỳ đâu** (strict hơn cả Go), CL không
danh sách phẩy, target chỉ VCHAR. Ba test thêm/đổi kỳ vọng.

**Bước 2 — bộ so có bắt được không?** Phản chứng: quay lại bản lenient, thêm 2 seed viết tay,
chạy seed corpus:

```console
$ go test ./internal/httpx -run FuzzAgainstNetHTTP -count=1     # bản TẠM lenient
--- FAIL: FuzzAgainstNetHTTP/chunk-leading-space
    fuzz_test.go:116: đọc body: mình err=<nil>, net/http err=invalid byte in chunk length
```

Lần **đầu** chạy phản chứng này nó **XANH** — vì tôi để seed ở `testdata/fuzz/` gốc repo, còn Go
tìm ở `internal/httpx/testdata/fuzz/`. 12 seed `f.Add` chạy, 2 seed file không. Bài phản chứng
xanh vì lý do sai — phase 1 đã dạy "đỏ phải đỏ đúng lý do", giờ thêm chiều ngược lại. `-v` lộ ra
ngay (chỉ `seed#0..#11`). Chuyển thư mục ⇒ đỏ đúng chỗ ⇒ bộ so đúng, **fuzzer không tới được**.

**Bước 3 — vì sao fuzzer không tới?** Thêm một điểm đo: bản lenient, *bỏ* 2 seed tay, fuzz 90s:
2 914 539 execs, 0 lệch. Giả thuyết lúc đó (nợ P2-1): coverage-guided chỉ thấy coverage của
**package mình**, nhánh lỗi bên `net/http` vô hình. **Sai — xem mục "Trả P2-1" bên dưới.**

**Bước 4 — chạy đủ:**

```console
$ go test ./... -count=1 -race                  # 20 lần cho httpx: bench/p2-race20.txt, ok 31.3s
$ make httplab                                  # bench/p2-httplab-GOTIT-00663.txt — 3 mode ✔
$ go test ./internal/httpx/ -run '^$' -fuzz FuzzReadRequest -fuzztime 120s -fuzzminimizetime 1s
fuzz: elapsed: 2m0s, execs: 1331499 (14717/sec), new interesting: 214 (total: 263)   ok
$ go test ./internal/httpx/ -run '^$' -fuzz FuzzAgainstNetHTTP -fuzztime 300s -fuzzminimizetime 1s   # bản strict
```

**Bước 5 — fuzz strict 300s lần 1: ĐỎ, và đỏ đúng chỗ thú vị.** Sau khi siết chunk-size, chạy
lại 300s trên bản strict — fuzzer **tìm được** một lệch:

```
input="0 * HTTP/1.1\r\nHost:\r\nTrAnsfer-EnCoding:Chunked\r\n\r\n5 \r\n00000\r\n0\r\n\r\n"
fuzz_test.go:116: đọc body: mình err=http 400: bad chunk, net/http err=<nil>
```

Chunk-size `"5 "` — space **đuôi**. Go trim đuôi nên nhận; mình strict nên từ chối. Hai điều:

1. **Vì sao lần này fuzzer thấy?** Lệch này lộ ra ở nhánh lỗi **của mình** (`ErrBadChunk` từ một
   input mà head hợp lệ) — coverage mới trong package đang đo. Lệch `" 3"` trước đó chỉ lộ ở nhánh
   lỗi **của Go** — vô hình. Cùng một fuzzer, cùng 300s: thấy cái nằm trong coverage của mình,
   mù cái nằm ngoài. Đây là điểm đo thứ hai cho giả thuyết P2-1 (vẫn là giả thuyết — chưa
   instrument `net/http` để chứng minh).
2. **Bộ so sai một nửa.** Điều kiện `(eMine==nil) != (eTheirs==nil)` coi mọi bất đối xứng lúc đọc
   body là lỗi. Nhưng hướng "mình từ chối, Go nhận" là **D1–D5 cố ý**: proxy đóng connection,
   backend không nhận gì. Chỉ hướng "mình đọc xong, Go lỗi" mới nguy hiểm. Sửa bộ so cho đúng
   nguyên tắc; giữ input làm seed hồi quy `chunk-trailing-space-fuzz-found`. Phản chứng chạy lại
   trên bản lenient: vẫn đỏ 2/2 với nhãn `NGUY HIỂM` — bộ so vẫn bắt hướng nguy hiểm.

Không đổi quyết định strict: nhận `"5 "` để "khớp Go" là khớp một khoan dung, và backend khác
Go có thể không khoan dung y vậy.

**Bước 6 — fuzz strict 300s lần 2 (bộ so đã sửa):**

```console
$ go test ./internal/httpx/ -run '^$' -fuzz FuzzAgainstNetHTTP -fuzztime 300s -fuzzminimizetime 1s
fuzz: elapsed: 5m0s, execs: 3330212 (9059/sec), new interesting: 105 (total: 652)
ok  	github.com/thaivro/edgegate/internal/httpx	301.048s
```

0 lệch nguy hiểm, không crash file mới. Execs/s thấp hơn lần 1 (9k vs 29k) vì đầu phiên chạy
song song với `go test -race`; con số cần so là *execs*, và 3.3M vẫn gấp đôi số lần 90s. Đọc
đúng ý nghĩa: đây là "không lệch trong những gì fuzzer nhìn thấy + 3 seed tay" — xem Rút ra §1.

Turn 2 kết lúc ~17:05. Phase 2 còn: turn 3 (biên tập diary, đối chiếu ROADMAP câu hỏi keep-alive
1 RTT / pipelining) và các nợ P2-1..P2-4.

**Đọc lại code sau khi xanh** (bài học §0.5): bỏ field `first` không dùng trong `chunkedReader`;
xác nhận `lengthReader` phân biệt EOF/ErrUnexpectedEOF ở cả hai nhánh; ghi câu hỏi cho phase 3:
response 204/304 kèm `Transfer-Encoding: chunked` được forward **nguyên head** — client đúng
chuẩn biết không có body, nhưng client lười thì chờ chunk; quyết ở phase 3/4 (P2-2).

### 2026-09-03 17:35 — Trả P2-1: giả thuyết "oracle không được instrument" sai

**Kiểm giả thuyết trước khi sửa gì** (bài P1-6: đừng đổ cho biến đầu tiên nhìn thấy). Build khô:

```console
$ rtk proxy go test -a -n -run '^$' -fuzz FuzzAgainstNetHTTP ./internal/httpx > dry.txt
$ grep -c '/compile ' dry.txt; grep '/compile ' dry.txt | grep -c 'd=libfuzzer'
207
180
== net/http:       libfuzzer 1 / compile 1
== bufio:          libfuzzer 1 / compile 1
== net/textproto:  libfuzzer 1 / compile 1
== không instrument: runtime sync/atomic sync syscall time reflect context testing internal/fuzz
   regexp os/exec runtime/pprof encoding/json go/* ... (27 package — toàn runtime và fuzz engine)
```

`net/http` **có** được instrument. Fuzzer không mù oracle. Vậy vì sao 390 s không tìm ra `" 3"`?
Lời giải còn lại: một lệch differential không phải một **edge** mới — nó là một **quan hệ** giữa hai
đường đều đã được cover từ sớm (`trimOWS` của mình qua header value; nhánh `invalid byte in chunk
length` của Go qua bất kỳ chunk-line rác nào). Input `" 3"` không thêm coverage cho ai ⇒ không
được giữ trong corpus ⇒ chỉ tới đó bằng may mắn của đột biến ngẫu nhiên trong không gian toàn bộ
request. `"5 "` thì ngược lại: nó mở nhánh `ErrBadChunk`-sau-head-hợp-lệ **mới** của mình ⇒ có
tín hiệu ⇒ tìm ra.

**Sửa bằng cách thu nhỏ không gian, không phải chạy lâu hơn.** `FuzzChunkLineAgainstNetHTTP`: head
cố định hợp lệ, fuzzer chỉ đột biến `(sizeLine, data)`. Thử trên bản lenient (tạm `trimOWS`):

```console
$ go test ./internal/httpx -run '^$' -fuzz FuzzChunkLineAgainstNetHTTP -fuzztime 60s -fuzzminimizetime 1s
fuzz: elapsed: 0s, gathering baseline coverage: 6/6 completed, now fuzzing with 6 workers
fuzz: elapsed: 0s, minimizing
--- FAIL: FuzzChunkLineAgainstNetHTTP (0.10s)
        fuzz_test.go:173: NGUY HIỂM: chunk-size " 3": mình đọc 3 byte, net/http lỗi invalid byte in chunk length
```

**0.10 s**, và minimize về đúng `" 3"`. Cùng property, cùng oracle, cùng fuzzer — khác nhau chỉ ở
chỗ mọi input đều rơi vào vùng phân xử chunk-size. Bản strict 120 s: 2 296 048 execs  0 lệch.

Việc (c): 17 hàng bảng đối chiếu tay thành seed `hand-*`; `go test` giờ chạy 32 seed cho
`FuzzAgainstNetHTTP` + 6 cho `FuzzChunkLine…`, `-race` xanh. Target `make difffuzz-chunk`.

### 2026-09-03 22:10 — Trả P2-4: giả thuyết đúng, nhưng thuốc đầu tiên sai

Không profile được (`cannot use -cpuprofile flag with -fuzz flag`). Viết benchmark tạm trong
package, xoá sau: `ReadMemStats` 70–114 µs, thân fuzz 9.3–9.7 µs, cả cụm 220–242 µs. Vậy 2 lần
ReadMemStats = ~94% thời gian mỗi input. `runtime/metrics.Read("/gc/heap/allocs:bytes")` 0.5 µs.

Thay thẳng bằng `metrics.Read` ⇒ fuzz **39.7k/s** nhưng đỏ sau 2 s: "cấp phát 506 232 byte cho
input 44 byte". Chạy lại input đó 3 lần: xanh. Bộ đếm là toàn tiến trình, goroutine của fuzz
engine cấp phát trúng cửa sổ đo. `ReadMemStats` cũ cũng toàn tiến trình — nó chỉ *ít* dính hơn
vì stop-the-world. Thuốc: hai tầng — kiểm rẻ, vượt trần mới đo lại 3 lần bằng ReadMemStats lấy
min. 60 s: 1 874 459 execs, 32.8k/s, không đỏ giả. Phản chứng trần 1 byte: đỏ cả 12 seed.

Bài học ghép vào hàng 7 bảng giả thuyết sai: lần này giả thuyết đúng, và điều đó **không làm cho
việc đo trước thành thừa** — nếu không đo, tôi không biết `metrics.Read` rẻ tới mức nào để dám
dùng nó làm tầng 1, và không bắt được dương tính giả để biết cần tầng 2.

## Giả thuyết sai — kể cả ngoài bảng G

| # | Tôi nghĩ | Thực tế | Sửa gì |
|---|---|---|---|
| 1 | Quên drain body ⇒ request sau bị 400 | `helloGET` là token hợp lệ ⇒ request sau **parse thành công**, method `helloGET`, không lỗi, không log | Test khẳng định hành vi thật; ghi vào bẫy #3 phase 3: bug này **không lộ qua error rate** |
| 2 | G1: diff-fuzz 300s sẽ tìm được lệch | 0 lệch / 7.7M — lệch thật tìm bằng tay | Bảng đối chiếu tay → seed; nợ P2-1 |
| 3 | Phản chứng bộ so xanh ⇒ bộ so sai? | Xanh vì seed nằm sai thư mục | Luôn `-v` để đếm seed đã chạy; seed phải ở `<pkg>/testdata/fuzz/<Fuzz>/` |
| 4 | Trim BWS trước `;` trong chunk-size là đúng RFC nên an toàn | Đúng RFC nhưng **lệch oracle theo hướng nguy hiểm** | Strict hơn RFC: không whitespace |
| 5 | Helper test không cần `-race` từ turn 1 | `pair()` có data race thật | Chạy `-race` từ lần test đầu |
| 6 | Mọi bất đối xứng lúc đọc body đều là lệch nguy hiểm | Chỉ hướng "mình nhận, oracle từ chối" nguy hiểm; hướng ngược là strict cố ý | Bộ so chỉ đỏ theo hướng nguy hiểm; phản chứng vẫn đỏ |
| 7 | Fuzzer không tìm ra `" 3"` vì `net/http` không được instrument | `-n` build khô: `net/http` **có** `-d=libfuzzer` (180/207 package). Lệch differential là quan hệ giữa hai đường đã cover, không phải edge mới ⇒ không có tín hiệu | Fuzz có cấu trúc thu nhỏ không gian: tìm ra trong 0.10 s. Lại một lần đổ cho biến đầu tiên nghĩ tới (P1-6) |

## Số đo

| Phép đo | Lệnh | Kết quả |
|---|---|---|
| Header bomb 600 027 B | `go test ./internal/httpx -run TestHeaderBomb -v` | 431 sau **4096 B** tiêu thụ |
| Dòng to 120 327 B (60 × 2 KB) | như trên | 431 sau 68 281 B (trần byte, ngay sau dòng vượt 64 KB) |
| Chunk-size FFFFFFFF, reader thật | `make test-chunkalloc` | Δ 4240 B |
| Chunk-size FFFFFFFF, decoder ngây thơ | như trên | Δ 4 294 967 328 B, `make` 4–21 ms trên heap đã scavenge |
| Slowloris 1 B/50 ms, HeaderTimeout 300 ms | `make httplab` | cắt 300–301 ms, 6 B |
| Pipelined 4 request / 281 B / 1 Write | `make httplab` | 4/4, buffer dư 0 |
| 5 response / 1 Write, gồm 204+CL và tới-EOF | `make httplab` | 5/5, `buffered-sau` 221→175→135→54→0 |
| FuzzReadRequest 120 s | `make fuzz-http` | 1 331 499 execs, 0 crash |
| FuzzAgainstNetHTTP 300 s (turn 1 lenient) | `make difffuzz` | 7 712 575 execs, **0 lệch** dù có lệch thật |
| FuzzAgainstNetHTTP 90 s lenient, không seed | — | 2 914 539 execs, 0 lệch |
| FuzzAgainstNetHTTP 300 s strict, bộ so cũ | `bench/p2-difffuzz-300s-strict.txt` (lần 1) | **tìm ra `"5 "`** — lệch nằm trong coverage của mình |
| FuzzAgainstNetHTTP 300 s strict, bộ so mới | `bench/p2-difffuzz-300s-strict.txt` | 3 330 212 execs, 0 lệch nguy hiểm |
| Phản chứng bộ so (lenient + 2 seed) | `bench/p2-difffuzz-counterproof.txt` | ĐỎ, 2/2, đúng lý do |
| Fuzz có cấu trúc, bản lenient | `bench/p2-structfuzz-lenient-60s.txt` | tìm `" 3"` sau **0.10 s** (fuzz phẳng: 390 s không) |
| Fuzz có cấu trúc, bản strict 120 s | `make difffuzz-chunk` → `bench/p2-structfuzz-strict-120s.txt` | 2 296 048 execs  0 lệch |
| Package được instrument khi `-fuzz` | `rtk proxy go test -a -n -fuzz ...` | 180/207, gồm `net/http` |
| `-race` ×20 httpx | `bench/p2-race20.txt` | ok 31.3 s |
| Giá một `ReadMemStats` / `metrics.Read` / thân fuzz | benchmark tạm (đã xoá), 3 lần | **80 µs** / **0.5 µs** / 9.5 µs |
| FuzzReadRequest 60 s sau P2-4 | `bench/p2-fuzz-readrequest-metrics-60s.txt` | 1 874 459 execs, **32 828/s** (trước 14 717/s) |
| Phản chứng trần cấp phát = 1 B | `bench/p2-fuzz-allocbound-counterproof.txt` | đỏ, 11 024 B cho seed 52 B |

## Invariant + lệnh kiểm chứng

| Invariant | Cài ở | Kiểm chứng | Kết quả |
|---|---|---|---|
| **I1** proxy và backend đồng ý ranh giới body | `body.go:framing` (thứ tự §6.3), `body.go:lengthReader`, `chunked.go:chunkedReader.Read` | `make difffuzz` + 3 seed tay; phản chứng lenient ở Reproduce §5 | 3.33M execs 0 lệch nguy hiểm; phản chứng đỏ 2/2 |
| **I2** kiểm trần trước khi gom byte | `parse.go:readLine` (từng dòng, trong lúc gom), `parse.go:readHeaders` (tổng byte + số dòng, trước `Add`), `chunked.go:parseChunkSize` + `Read` (không `make` theo size) | `go test -run 'TestHeaderBomb\|TestChunkSizeDoesNotAllocate' -v` | bomb 600 KB: 431 sau 4096 B; chunk FFFFFFFF: Δ 4240 B vs ngây thơ 4.29 GB |
| **I3** mọi connection có deadline | **không** trong parser (cố ý) — `cmd/httplab/main.go:runSlowloris`, `limits_test.go:TestSlowlorisNeedsConnectionDeadline` | `make httplab` (mode slowloris) | cắt 300–301 ms sau byte đầu, 6 B, trả 408 |
| **I4** header ghi ra không tạo header mới ở phía nhận | `header.go:appendWire` (`safeWire`) | `go test -run 'TestHeaderWriteSortedAndSafe\|TestWriteHeadRoundTrip'` | `ErrHeaderInjection` cho CR/LF/NUL |
| Body cắt cụt không bao giờ là `io.EOF` (D6) | `body.go:lengthReader.Read`, `chunked.go:Read` | `go test -run 'TestReadRequestEOF\|TestChunkedReject'` | `io.ErrUnexpectedEOF` ở mọi vị trí cắt |

## Đọc gì trong phase này

- **RFC 9112** §2.2 (bare CR/LF — "MAY recognize a single LF"; ta chọn không), §3 request-line,
  §4 status-line (reason-phrase có thể rỗng), §5 field syntax + §5.2 obs-fold ("MUST either reject
  or replace"), **§6.1–6.3 message body length** (thứ tự 8 bước — chép thẳng vào `framing`),
  **§7.1 chunked** (`chunk-size [ chunk-ext ]`, BWS — chỗ đúng RFC mà vẫn phải strict hơn).
- **RFC 9110** §5.6.2 token (vì sao `helloGET` hợp lệ), §8.6 Content-Length (danh sách trùng),
  §7.6.1 hop-by-hop, §9.3.2 HEAD, §15.3.5 204 / §15.4.5 304.
- **`net/http` — chỉ hành vi quan sát qua oracle, chưa đọc source** (ROADMAP: đọc sau phase 5):
  trim đuôi chunk-size nhưng không trim đầu; bỏ qua TE trên HTTP/1.0; nhận bare LF, obs-fold,
  `HTTP/2.0`; từ chối `5,5`, `+5`, tab trong target.

## Rút ra

**Trả lời 5 câu hỏi đầu file.**

1. *Thứ tự framing và bất đối xứng.* §6.3 là một chuỗi `if` có thứ tự: (1) HEAD/1xx/204/304 ⇒
   không body dù header nói gì; (2) có TE ⇒ chunked, CL bị bỏ; (3) CL ⇒ đúng n byte; (4) không gì
   cả ⇒ **request 0 byte, response tới EOF**. Bất đối xứng ở bước 4 vì hai bên có nguồn "hết"
   khác nhau: request không thể chờ EOF (client giữ keep-alive nên EOF không tới — bẫy #1 phase 3),
   còn response *có* EOF của server để dựa vào (HTTP/1.0 cũ). Lab `response` cho thấy hệ quả: response
   cuối `Close=true` bắt buộc — connection đó không dùng lại được.
2. *Chunk-size FFFFFFFF.* Không. Reader chỉ nhớ "còn n byte" và cấp theo `len(p)`; Δ 4240 B. Bằng
   chứng là **hai điểm đo cùng input**: decoder ngây thơ `make(size)` cho Δ 4 294 967 328 B.
3. *Header bomb.* Trần **đếm** nổ trước: dòng 101 ⇒ 431 khi mới tiêu thụ 4096 B (một lần fill
   bufio) trong 600 KB. Biến thể 60 dòng × 2 KB thì trần byte nổ ở 68 281 B. Cả hai trần đều kiểm
   *trong lúc* đọc, không phải sau khi gom.
4. *Khoan dung của `net/http`.* Bảng ở Bước 1. Hướng **nguy hiểm** là mình nhận / oracle từ chối
   (`" 3"`, `"3 ;x"` — đã đóng). Hướng vô hại là mình từ chối / oracle nhận (bare LF, obs-fold,
   `"5 "`…). Hướng thứ ba đáng sợ nhất: **cả hai nhận nhưng khác nghĩa** — TE trên HTTP/1.0 (Go bỏ
   qua TE, dùng CL). D3 đã chặn bằng cách từ chối trước khi tới đó.
5. *Slowloris.* Parser không phát hiện được và **không nên** phát hiện — nó không có khái niệm
   thời gian. Tầng cầm `net.Conn` đặt `SetReadDeadline` sau byte đầu; cắt ở đúng HeaderTimeout
   (300 ms, sai số 1 ms), nhận 6 byte, trả 408.

**Câu hỏi của ROADMAP: vì sao keep-alive tiết kiệm đúng 1 RTT, không hơn? Pipelining khác gì,
vì sao không ai bật?** Phase 0 đã đo mô hình: `reuse = 1 RTT`, `dial = 2 RTT`, tỉ số tiệm cận
2.00x ở mọi RTT. Cái keep-alive bỏ đi là **handshake** — đúng 1 RTT — còn request/response vẫn
phải 1 RTT vì client phải nhận xong response N mới gửi N+1 (closed-loop trên một connection).
Pipelining bỏ nốt ràng buộc đó: gửi N+1 khi chưa có response N — chính là `TestKeepAlivePipelined`
(3 request trong một Write). Phase này cho thấy vì sao không ai bật: (a) response **phải trả theo
đúng thứ tự** trên cùng connection, nên một request chậm chặn mọi request sau (head-of-line
blocking — HTTP/2 sinh ra để giải cái này); (b) ranh giới body phải **tuyệt đối đúng** ở cả hai
đầu, vì byte của request N+1 đã nằm trong buffer khi đang xử lý N — bug drain body (`helloGET`)
từ "một request hỏng" thành "mọi request sau hỏng"; (c) proxy phải giữ thứ tự khi fan-out sang
nhiều upstream. Parser của mình *hỗ trợ* pipelining (buffer dư 0 sau 4 request) — đó là điều kiện
cần để keep-alive đúng, không phải để bật pipelining.

**Bài học không nằm trong câu hỏi nào.**

- *Differential fuzzing 0 lệch là điều kiện cần, không phải bằng chứng.* Bảng đối chiếu viết tay
  tìm được 2 lệch thật mà 10.6M execs không thấy; lệch thứ ba (`"5 "`), mở nhánh lỗi *mới của
  mình*, fuzzer tìm ra trong 300 s. Không phải vì oracle không được instrument (đã kiểm: có) — mà
  vì một lệch differential là **quan hệ giữa hai đường đã cover**, không sinh edge mới, nên coverage
  không dẫn đường tới nó. Thuốc là **thu nhỏ không gian** (fuzz có cấu trúc: 0.10 s) và **seed từ
  nghi ngờ của người** — không phải chạy lâu hơn.
- *Khoan dung đúng RFC vẫn có thể là lỗ hổng* nếu backend khoan dung khác. BWS trước `;` hợp lệ
  theo §7.1.1; `net/http` không nhận. Quy tắc phase 4 "mơ hồ thì từ chối" áp cả cho chỗ RFC cho
  phép: strict hơn cả RFC và cả oracle, vì hướng "mình từ chối" luôn vô hại.
- *Bẫy quên-drain-body im lặng.* Không phải 400, mà là request hợp lệ với method lạ. Phase 3
  không thể dựa vào error rate; cần test ranh giới rõ ràng (`Buffered()==0`) và phải **đóng
  upstream connection** khi body lỗi giữa chừng.
- *Xanh cũng phải xanh đúng lý do.* Phản chứng xanh vì seed không được load. `-v` và đếm seed.
- *Giả thuyết về code mình sắp viết là spec.* 5/7 "giả thuyết" đúng vì tôi viết code cho chúng
  đúng. Chỉ đếm những gì mình không kiểm soát.

## Nợ mở của phase

Chi tiết và lệnh để trả: [`../docs/debts.md`](../docs/debts.md).

- [x] **P2-1** — Diff-fuzz không tới được lệch chỉ lộ ở oracle. Trả 17:35: giả thuyết instrument
  **sai** (đã kiểm bằng build khô); `FuzzChunkLineAgainstNetHTTP` + 17 seed tay; `make difffuzz-chunk`.
- [ ] **P2-2** — 204/304 kèm TE forward nguyên head: giữ hay strip TE? Quyết phase 3/4.
- [ ] **P2-3** — D4 (CL+TE ⇒ chunked, xoá CL) đổi thành **từ chối** ở phase 4, kèm phản chứng
  `make smugglelab-nodefense` đỏ.
- [x] **P2-4** — `FuzzReadRequest` 14.7k execs/s. Trả 22:10: `-cpuprofile` **không dùng được với
  `-fuzz`**, đo bằng benchmark tạm: ReadMemStats 80 µs vs thân fuzz 9.5 µs. Đổi sang
  `runtime/metrics.Read` (0.5 µs) + xác nhận min-of-3 ⇒ **32.8k execs/s**.
