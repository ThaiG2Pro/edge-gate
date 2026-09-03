# Phase 1 — log thô

Đây **không** phải bản biên tập. [`phase1.md`](phase1.md) là kết quả đã sắp lại cho người khác
đọc; file này là **đường đi thật**, theo thứ tự đã xảy ra, gồm cả code viết sai, dự đoán sai còn
nằm trong comment, và những bug tìm ra bằng cách đọc thay vì chạy.

Phase này ngắn (3 turn, cùng một ngày) nên log ngắn. Nhưng có một điểm bản biên tập không thể
hiện được: **bốn dự đoán sai đều sai trước lúc chạy lệnh đầu tiên chưa tới 5 phút**, và hai bug
thật thì được tìm ra **sau** khi mọi thứ đã xanh và fuzz đã chạy 1.58M lần.

## Phần 0 — Thứ mang từ phase 0 sang, trước khi viết dòng nào

- `cmd/netlab/wire.go` đã là một length-prefix framer chạy được, với cả hai bất biến
  (`ReadFull`, trần kiểm trước `make`). Quyết định: **không** copy nó. Viết `internal/frame`
  mới có magic/version như ROADMAP yêu cầu, để wire.go thành nợ P1-1 thay vì thành nguồn.
- Bài học "bufio giấu cái mình đo" ⇒ `Decoder` không có buffer riêng, test không bọc `bufio`.
- Bài học "phản chứng phải đỏ" ⇒ `checkLength` tách file có build tag ngay từ đầu, không phải
  viết xong rồi tìm cách tắt.
- Bài học "write-write-read = 44ms" ⇒ `Encode` bắt buộc một `Write`, có test đếm số lần Write.

Giả thuyết viết trước khi chạy (nằm trong comment đầu `cmd/framelab/main.go` bản đầu, nguyên văn):

```
// Con số quan trọng là tỉ số Read/frame ở phía server:
//   - dribble : Read/frame ≫ 1 — mỗi frame cần nhiều lần Read (short read).
//   - coalesce: Read/frame < 1 — một lần Read chứa nhiều frame.
```

Dòng thứ ba là G1, và nó sai. Ghi lại nguyên văn vì bản biên tập chỉ còn con số 1.67.

## 1. Turn 1 — viết `internal/frame` (≈ 15 phút)

Không có ma sát. Ba quyết định nhỏ:

- `NewDecoder(r, 0)` ⇒ dùng `DefaultMaxFrameSize`; **không có cách nào** tạo decoder không trần.
  Cân nhắc `max = 0` nghĩa là "không giới hạn" rồi bỏ ngay: đó là cách I2 bị tắt bằng một tham số.
- `io.EOF` sau header trọn đổi thành `io.ErrUnexpectedEOF`. Thực ra `ReadFull` đã làm điều đó
  cho `n > 0`; dòng đổi chỉ chạm tới trường hợp `n == 0`... mà với `n == 0` `ReadFull` trả `nil`
  chứ không EOF. Tức dòng đó **chưa bao giờ chạy**. Để lại vì nó làm ý định rõ ràng, nhưng ghi
  ở đây: một dòng phòng thủ không có test nào chạm tới.
- Test dùng TCP thật qua `127.0.0.1:0`, không `net.Pipe`. Lý do viết thành comment trong `pair()`
  và trở thành G6 — chấm ✅ theo tài liệu, **chưa đo** (P1-4).

```console
$ go test ./internal/frame/ -count=1 -race
ok  	github.com/thaivro/edgegate/internal/frame	1.660s
```

Xanh ngay lần đầu. Điều này không nói gì — ở phase 0, code xanh ngay cũng có bug thừa một roundtrip.

## 2. Turn 1 — phản chứng đỏ, và đỏ khác dự đoán

```console
$ go test ./internal/frame/ -run 'TestCapBeforeAlloc|TestCustomMax' -count=1 -tags nodefense
--- FAIL: TestCapBeforeAlloc (0.01s)
    frame_test.go:136: err = unexpected EOF, want ErrTooBig
    frame_test.go:139: decoder cấp phát 4294967376 byte cho một frame bị từ chối
```

Tôi mong dòng 136. Dòng 139 mới là bằng chứng. Và hai thứ bất ngờ cùng lúc:

1. `0.01s`. Tôi đã lo `make([]byte, 0xFFFFFFFF)` sẽ OOM máy 12 GB hoặc treo vài giây, đến mức
   cân nhắc dùng length nhỏ hơn (`max + 1`) cho an toàn. Nếu làm vậy thì bài phản chứng đã **không
   chứng minh được thứ tự check/make** — chỉ chứng minh trần có tác dụng. May là thử với
   `0xFFFFFFFF` trước. Hiểu ra sau: trang chưa chạm không tốn gì. (G5 sai.)
2. `4294967376 = 4294967295 + 81`. 81 byte lẻ là header, `Decoder`, `bytes.Reader`, lỗi wrap.
   Con số này khớp tới byte ⇒ `TotalAlloc` là thước đo đúng cho "đã cấp phát bao nhiêu".

## 3. Turn 1 — lab chạy lần đầu, G1 sai trong 1ms

```console
== coalesce
server Read  : 5 lần, 62 byte  (nhỏ nhất 5, lớn nhất 27 byte/Read)
Read/frame   : 1.67
```

Nhìn `5` và `27` là hiểu ngay: đó là hai payload. Decoder xin 10, được 10; xin 5, được 5. Kernel
không "đẩy" gói tin vào `Read`, nó **đáp** đúng yêu cầu. Coalescing đang xảy ra (62 byte đã nằm
trong socket buffer từ 1ms trước) và decoder không có cách nào biết — mà cũng không cần biết.

Sửa lab: một `Read` thô 4 KiB **trước** decoder, nối lại bằng
`io.MultiReader(bytes.NewReader(first[:n]), c)`. Suýt quên nối — bản nháp đầu đọc thô rồi đưa
thẳng `c` cho decoder, tức mất 62 byte đầu và decoder sẽ EOF. Bắt được vì nghĩ "decoder sẽ đọc gì
tiếp" trước khi chạy, không phải nhờ chạy.

Sửa comment đầu file để ghi lại dự đoán sai và con số 1.67 ngay tại chỗ dự đoán đã đứng.

```console
Read thô đầu : 1 byte
Read thô đầu : 62 byte
```

Đây là con số muốn thấy từ đầu — nhưng phải hỏi đúng câu mới thấy.

## 4. Turn 2 — chạy mạnh

```console
$ go test ./internal/frame/ -count=20 -race          # 240 passed
$ go test ... -fuzz FuzzFrameDecode -fuzztime 10s -tags nodefense
    frame_test.go:245: cấp phát 4294996320 byte cho input 10 byte
$ go run ./cmd/framelab -mode dribble -interval 0
Read thô đầu : 11 byte
```

Ba việc, một bất ngờ: **11 byte**. Tôi đã nghĩ NoDelay đủ để dribble-không-sleep vẫn là 1 byte/Read
(G7). Không: NoDelay chỉ chặn Nagle giữ gói; nó không làm server `Read` nhanh hơn client `Write`.
Trong lúc goroutine server chờ được lên lịch, client đã `Write` 11 lần và kernel gom 11 byte đó vào
socket buffer bên nhận. Coalescing có hai nguồn, socket option chỉ tắt một.

Fuzz 120s chạy nền suốt turn: 1580258 execs, 4 "new interesting" xuất hiện ở phút thứ 1 (phút đầu
không thêm gì — corpus seed đã phủ hết nhánh dễ), 0 crash.

Một chỗ ma sát công cụ, nhỏ: chạy fuzz nền bằng `(...) &` rồi `wait` ở lệnh sau — `wait` không
biết job của shell trước, trả ngay, file log còn dở. Phải chờ bằng `until grep -q "fuzz exit="`.
Cùng loại với `| tail` ở phase 0: **công cụ nói "xong" không có nghĩa là xong.**

## 5. Turn 2 — hai bug bằng mắt

Sau khi mọi thứ xanh, ngồi đọc lại `frame.go` từ trên xuống với câu hỏi "có đường nào chưa có
bài chạy nào đi qua không". Tìm được hai.

**`Append`:** `binary.BigEndian.PutUint32(hdr[6:10], uint32(len(f.Payload)))`. Trên amd64
`len` là `int` 64-bit; `uint32(1<<32 + 1) = 1`. Header hợp lệ, length = 1, payload 4 GiB đi
theo sau ⇒ decoder bên kia đọc 1 byte payload rồi coi byte thứ 2 là magic của frame kế ⇒
`ErrBadMagic`, connection đóng — ở **bên kia**, với một lỗi trông như "client gửi rác". Bug này
không thể tìm bằng fuzz hiện tại vì fuzz chỉ chạy `Decode`; round-trip check trong fuzz đi từ
byte → frame → byte, không bao giờ đi từ frame-4-GiB → byte.

Sửa: `Encode` trả `ErrPayloadTooBig`; `Append` panic. Tranh cãi với chính mình 2 phút về panic:
`Append` không trả lỗi và đổi chữ ký sẽ kéo theo toàn bộ test. Kết luận: payload > 4 GiB vào
`Append` là lỗi lập trình cùng hạng slice vượt biên, và **cắt cụt im lặng tệ hơn panic rất nhiều**
— hành vi cũ chính là cắt cụt im lặng. Dữ liệu từ ngoài phải đi qua `Encode`.

Test cho nó cần payload 4 GiB + 1 byte thật. Nhờ §2 biết là được: `make([]byte, MaxPayload+1)` —
0.03s. G5 sai hoá ra hữu ích ngay.

**`framelab`:** goroutine server có hai kênh ra, `done` và `firstRead`. Đường lỗi của `Accept` và
của `Read` đầu gửi `done` nhưng không gửi `firstRead`; `main` đọc `<-firstRead` trước `<-done`...
không, `main` đọc `r := <-done` trước rồi mới `<-firstRead` — vẫn treo, vì `firstRead` trống và
không ai gửi nữa. Ba mode đều xanh vì cả ba đều đi qua đường thành công. Sửa: gộp `first` vào
`result`, một channel, `first = -1` trên đường lỗi.

Đây là bug **của bộ đo**, không của thứ bị đo — cùng họ với bốn lần bộ đo sai ở phase 0, chỉ khác
là lần này chưa kịp làm sai số nào.

## 6. Turn 3 — viết diary, và một chỗ phải thành thật

Khi điền bảng "giả thuyết đăng ký trước", chỉ G1, G2, G6 là thật sự được viết ra **trước** lệnh
đo đầu tiên (nằm trong comment). G3, G5, G7 là những kỳ vọng tôi **có** trong đầu trước khi chạy
lệnh tương ứng ở turn 2 — nhưng không viết xuống. G4 cũng vậy. Ghi rõ trong bảng thay vì để
người đọc tưởng cả 7 đều được đăng ký cùng lúc. Phase 0 làm việc này tốt hơn (bảng đăng ký viết
trước khi build); phase 1 vì ngắn nên đã lười. Lần sau: bảng đăng ký viết **trước turn 1**, kể cả
khi phase chỉ có số đếm.

## 7. Trả P1-1 và P1-2 (cùng ngày)

**P1-1.** Định thay `readFrame` bằng `frame.Decoder` và xong trong 5 phút. Vướng một chỗ:
`writeResponse` biến thể hai-lần-Write ghi header rồi ghi `respPad[:size]` **không copy**. Nếu
dùng `frame.Append` để có header thì phải đưa payload vào ⇒ copy ⇒ biến thể 2-Write gánh thêm
một chi phí mà trước đó nó không có, còn biến thể 1-Write vốn đã copy. Tỉ số G1 vẫn ra đúng
hướng, nhưng đó là **đổi thứ bị đo mà không nói**. Thêm `frame.PutHeader(dst, type, length)`
để ghi header rời — `Append` giờ gọi nó. Chạy lại: 1.42x / 44.00ms, khớp phase 0.

**P1-2.** Câu hỏi đầu: viết test đỏ có chủ đích (như nợ ghi) hay test xanh chứng minh treo?
Test đỏ-skip-mặc-định là test không ai chạy. Đổi sang: chờ 300ms thấy chưa trả về (nửa a), rồi
đặt deadline và thấy trả về timeout (nửa b). Xanh, chạy mỗi lần, và ghi lại quyết định thay vì
ghi lại một lỗi giả.

**Số vô lý:** cả bộ `-race` 36s. Phản xạ phase 0: nghi bộ đo. Chạy `-v` sắp theo thời gian:
`TestPayloadOverUint32 5.84s`. Trước đó test này 0.03s và tôi đã viết vào phase1.md "trang chưa
chạm không tốn gì" như một sự thật. Nó là sự thật **không có `-race`**, hoặc là sự thật của một
lần chạy. Race detector map shadow memory cho vùng đó — tốn hay không phụ thuộc runtime, không
chỉ kernel. G5 sai lần thứ hai, theo hướng ngược lại. Ghi vào P1-3, không xoá câu cũ trong
phase1.md mà thêm mục nhật ký nói nó sai ở đâu.

## 8. Trả P1-3, P1-4, P1-5 — và phải sửa lời mình vừa viết ở §7

**P1-4 trước**, vì nó có thể lật G6 — và lật thật, một nửa. Viết 4 kịch bản cho cả hai
transport. Kỳ vọng lúc viết: Pipe sẽ cho 62 ở bài short-read (vì "giao nguyên khối"). Đo: **7**.
Đọc lại godoc `net.Pipe`: "Reads on one end are matched with writes on the other, copying data
directly between the two; there is no internal buffering" — tức Read buffer nhỏ thì lấy ít, phần
còn lại Write vẫn chờ. Tôi đã đọc câu này ở turn 1 và hiểu thành "một Write = một Read". Bài
async (`pipe=false`) mới là chỗ khác biệt thật.

Bất ngờ phụ: bài "62 Write rời không sleep" TCP cho **1**, trong khi framelab cùng ngày cho
**11**. Không xác định — nên test không assert phía TCP. Đây là lý do G7 không nên là assert.

**P1-3.** Ba skip: 32-bit (`^uint(0)>>1 < 4 GiB` — và `make([]byte, MaxPayload+1)` với hằng
sẽ **không compile** trên 32-bit, phải qua biến), `overcommit_memory=2`, và `-race`. Viết lời
skip "~6s vì shadow memory" — rồi nghĩ: chưa đo 3 lần dưới race bao giờ. Thêm `FRAME_HUGE=1`
để ép, đo: 0.02/0.01/0.01s. **Lời skip sai ngay khi vừa viết.** Cả package thì 4.49/3.76/0.00s.
Bisect 4 cặp: đều 0.00s. Dừng, mở P1-6, sửa lời ở §7 của phase1.md bằng một mục mới thay vì
xoá — vì §7 là ví dụ tốt của "gán nguyên nhân hợp lý nhất rồi ghi như sự thật", đúng thứ phase 0
đã cảnh báo, và tôi vẫn làm.

**P1-5.** `-rawbuf 7` và `-rawbuf 12`. Mười dòng. `nhỏ nhất 3 / 2 byte` — phần đuôi ReadFull
phải vá — là con số đẹp nhất của lab: nó là bằng chứng bằng mắt rằng `MultiReader` cạn giữa
header và `ReadFull` tiếp tục đúng chỗ.

## 9. Trả P1-6 — bốn vòng đo, mỗi vòng đổi câu hỏi

**Vòng 1:** `gctrace` + `cpuprofile` + `time`, 4 lần. Không tái hiện (0.01s ×4). Cùng lệnh 30 phút
trước tái hiện 2/3. Bài P0-7 lần thứ ba trong ngày. Bỏ profile — nó đổi timing, và thứ cần biết
trước hết không phải "ở hàm nào" mà là "**user hay sys**".

**Vòng 2:** in thời gian `make` ngay trong test, chạy binary đã build sẵn 8 lần với `/usr/bin/time`.
2/8 chậm: **sys=3.59 / 3.32s, user=0.26**. Thời gian ở kernel. Race runtime chạy ở user ⇒ chẩn
đoán "shadow memory" sai ngay tại đây, chưa cần gì thêm. Rồi chạy binary **không race** 8 lần cả
package: **7.73s, 7.34s** (2/8). `-race` hoàn toàn vô can — tôi đã gỡ skip đúng lý do sai.

**Vòng 3:** kernel làm gì 7s cho 4 GiB *không ai chạm*? Chỉ có một việc tỉ lệ với 4 GiB: page fault.
Tức là **có ai chạm** — runtime zero span. Đăng ký dự đoán: lần chậm RSS ≈ 4 GB. `maxrss` 8 lần:
2 lần chậm = **4206848 / 4206592 KB**, 6 lần nhanh = 12 MB. Trúng 8/8. Viết chương trình tái hiện
"alloc → thả → `FreeOSMemory` → alloc lại": **1ms, không tái hiện**. Hụt — nhưng hụt có ích: Go biết
trang đã scavenge là zero. Vậy trang phải **bẩn**: free rồi nhưng chưa trả kernel.

**Vòng 4:** 64 MB rác chạm-rồi-thả + `GC()` không scavenge, rồi `make(4 GiB)`: **7.178s, RSS
4.27 GB**. Cùng rác + `FreeOSMemory()`: 3ms. Xác định, lặp lại được (`cmd/needzerolab`: 7.138s
lần hai). Fix trong test: `FreeOSMemory()` trước `make` ⇒ 8/8 nhanh, `-race` cả bộ 2.35s ×3.

**Cái tôi đã làm sai, lần thứ hai cùng một kiểu:** §7 gán "shadow memory" không đo. §8 sửa thành
"chưa biết". Nhưng khi viết lời skip P1-3 tôi vẫn giữ `-race` trong điều kiện skip — tức vẫn ngầm
tin `-race` có vai trò, chỉ vì lần đầu thấy nó trong lệnh chậm. Vòng 2 chứng minh không. Một biến
xuất hiện trong lệnh tái hiện đầu tiên **không** phải nguyên nhân; nó chỉ là biến đầu tiên mình
nhìn thấy.

**Thứ đáng giá nhất không phải nợ P1-6.** Nó là G5 và Rút ra §6 phải viết lại: "4 GiB địa chỉ ảo
vô hại" đúng trên heap sạch của một test, sai trên process server thật — nơi luôn có rác bẩn. Với
64 MB rác, `length=0xFFFFFFFF` = 7 giây kernel + 4 GiB RSS **mỗi connection**, trước byte payload
đầu tiên. I2 tôi đã cài đúng; hiểu về nó thì mới đúng từ hôm nay.

## Tổng kết đường đi

| # | Phát hiện | Tìm ra ở đâu | Đang định làm gì lúc đó |
|---|---|---|---|
| 1 | Decoder đúng không "thấy" coalescing (Read/frame 1.67, không < 1) | §3 | Chạy lab lần đầu cho xanh |
| 2 | `make` 4 GiB = 0.01s, không tốn RSS | §2 | Kiểm bài phản chứng có đỏ không |
| 3 | Phản chứng đỏ vì `unexpected EOF`, không vì `ErrTooBig` — lý do dự đoán là lý do yếu | §2 | ↑ |
| 4 | NoDelay chỉ tắt một nửa coalescing (11 byte) | §4 | Thử dribble không sleep cho vui |
| 5 | `Append` cắt cụt `uint32(len)` — I2 hở chiều ghi | §5 | Đọc lại code sau khi mọi thứ xanh |
| 6 | Lab treo vĩnh viễn trên đường lỗi | §5 | ↑ |
| 7 | `TestDribble` là slowloris; Decoder không có deadline | Viết Rút ra | Viết diary |
| 8 | Tỉ số G1 sống qua việc thay framer (1.41x → 1.42x, 44.03 → 44.00ms) | §7 | Trả P1-1 |
| 9 | "4 GiB không tốn gì" sai: 0.03s → 5.84s cả package dưới `-race` | §7 | Chạy lại cả bộ sau P1-2 |
| 10 | G6 sai một nửa: Pipe tái tạo được short read và nhiều-frame-một-Read | §8 | Trả P1-4 |
| 11 | Chẩn đoán "shadow memory" ở §7 sai: một mình `-race` = 0.01s | §8 | Viết lời skip cho P1-3 |
| 12 | `-race` vô can hẳn: không race cũng 7.73s; thời gian ở **sys** | §9 vòng 2 | Đo user/sys thay vì profile |
| 13 | Chậm ⇔ RSS 4.2 GB (8/8); cơ chế `needzero` trên trang bẩn, khuếch đại 64x | §9 vòng 3-4 | Đăng ký dự đoán rồi đo |
| 14 | G5 sai theo hướng nguy hiểm: I2 bảo vệ 7s CPU + 4 GiB RSS thật, không phải địa chỉ ảo | §9 | Viết lại Rút ra §6 |

Mục 1-4 tìm ra **trong 20 phút đầu**, mỗi cái ngay lần chạy đầu của lệnh tương ứng. Mục 5-6 tìm ra
**sau** khi đã có 240 test pass và 1.58M fuzz execs — bằng đọc, không bằng chạy. Mục 7 tìm ra khi
đang viết câu giải thích cho người khác.

## 4/7 giả thuyết sai — và cả bốn sai cùng một kiểu

G1, G5, G6, G7 (và chẩn đoán §7) đều là tôi gán cho một con số quan sát được (số lần `Read`, thời gian `make`, byte
trong `Read` đầu) một nguyên nhân **duy nhất** (gói tin, kích cỡ, Nagle) trong khi nó có hai
(gói tin + decoder xin gì; kích cỡ + trang đã chạm chưa; Nagle + scheduler). Phase 0 dạy "một
con số hợp lý không phải bằng chứng"; phase 1 thêm: **một con số có nhiều hơn một cách để ra
được giá trị đó**, và dự đoán chỉ đúng khi mình đã kể hết các cách.
