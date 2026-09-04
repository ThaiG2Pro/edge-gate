# Phase 4 — RFC compliance & request smuggling

- **Thời lượng dự kiến:** 2-3 ngày · **thực tế:** ~2.5 giờ (11:11 → 13:45, ba turn)
- **Bắt đầu:** 2026-09-04 11:11 · **Kết thúc:** 2026-09-04 13:45
- **Trạng thái:** ✅ xong 2026-09-04 13:45 — **3/7 giả thuyết sai** (G1 nửa sau 38 % vs ≥ 40 %, G2 46 % vs 60-75 %, G4 6.5 % vs < 5 %), cộng 3 lỗi vận hành (`pkill -f` lần hai, `&&`+`&`, hook `rtk` tóm tắt). 57 test xanh `-race`, 61/61 ca parser, 54/54 e2e, phản chứng đỏ 20/52, diff-fuzz 7.75 M exec 0 lệch. Giả thuyết bên dưới đăng ký **trước** file `.go` đầu tiên của phase.
- **Commit:** `8334b12` (turn 1 `dc3a824`, turn 2 `989b060`, turn 3 `8334b12`; commit nền `b3e08f0`)

> **Đường đi thô, kể cả ngõ cụt:** [`phase4-log.md`](phase4-log.md). File này là bản biên tập.
>
> **Quy tắc ghi nhật ký:** mọi con số, mọi kết luận đều phải kèm **lệnh shell sinh ra nó**
> và **output thật**. Luật đầy đủ: [`../skills/diary/SKILL.md`](../skills/diary/SKILL.md).

## Ràng buộc kế thừa

- **Tuyệt đối không dùng `net/http` trên data path.** `internal/httpx`, `internal/proxy`,
  `cmd/edgegate` chỉ `net`. `net/http` chỉ là fixture (`cmd/upstream`, `internal/fixture`) và
  **oracle** trong `_test.go`.
- **Bất biến số 1 của một proxy** (ROADMAP): *proxy và upstream phải luôn đồng ý về ranh giới
  của mỗi request.* Hệ quả thiết kế: **khi mơ hồ thì từ chối, không đoán.** Một proxy khoan dung
  là một proxy có lỗ hổng, vì nó khoan dung theo cách khác backend.
- Phase 2 đã dựng phần lớn hàng rào ở tầng parser (TE đúng một token, CL chữ số thuần, bare LF
  từ chối, obs-fold từ chối, tên header phải là token, CTL trong value từ chối, 3 trần header,
  hop-by-hop kể cả tên trong `Connection:`). Phase 4 **không viết lại** chúng; nó (a) đóng những
  lỗ còn mở có chủ đích (D4 phase 2: CL+TE ⇒ bỏ CL — chính là CL.TE), (b) thêm ranh giới tin cậy
  (XFF), (c) quyết `Host`/request-target, (d) kiểm **response** như kiểm request, và (e) **chứng
  minh** bằng bộ payload + phản chứng đỏ, thay cho "test xanh".
- Hai ca đã biết ở phase 2 mà oracle `net/http` khoan dung hơn mình (chunk-size `"5 "`, CL trùng
  giống nhau): hướng "mình từ chối, oracle nhận" là **vô hại** — request không tới backend.

## Môi trường

Cùng máy phase 0-3 (`bench/env-GOTIT-00663.txt`). Phase này chủ yếu đo **đúng/sai** (số ca), chỉ
một số đo thời gian (G4) nên ba bẫy đo mạng chỉ áp cho G4: closed-loop, cùng máy, loopback trần.

```console
$ uname -srmo && go version && nproc && ulimit -n
Linux 6.6.87.2-microsoft-standard-WSL2 x86_64 GNU/Linux
go version go1.26.2 linux/amd64
6
1048576
```

## Mục tiêu phase

Bộ `testdata/smuggle/*.txt` ≥ 20 ca (CL.TE, TE.CL, TE.TE, CL bẩn, bare LF, Host/target mơ hồ,
trailer mang framing, response splitting) — mỗi ca kèm status mong đợi — **xanh** ở tầng parser
(`internal/httpx`) và **xanh** e2e qua `cmd/edgegate` thật (proxy trả đúng status **và đóng
connection**, byte pipelined sau ca bị từ chối không bao giờ được trả lời). Bài phản chứng
`make smugglelab-nodefense` **phải đỏ**. XFF có ranh giới tin cậy. `Host` được quyết có ý thức.

## Câu hỏi phải trả lời được (viết trước khi code)

1. Vì sao "CL + TE ⇒ ưu tiên TE, bỏ CL" (RFC 9112 §6.1 cho phép, phase 2 D4 đã làm) vẫn là lỗ
   hổng dù đúng RFC? Ranh giới nào bị phá, ở phía nào?
2. Với mỗi phòng tuyến, **phản chứng** nào chứng minh test có răng? Nếu tắt phòng tuyến mà bộ
   test vẫn xanh thì kết luận gì?
3. Backend `net/http` (Go 1.26) đồng ý với mình ở bao nhiêu ca? Ở những ca mình strict hơn,
   hướng lệch là an toàn hay nguy hiểm — và vì sao chỉ có **một** hướng nguy hiểm?
4. `X-Forwarded-For` do client tự gửi thì tin được gì? Khi nào append, khi nào thay, và cái gì
   quyết định ranh giới đó? Bỏ qua thì phase 7 hỏng ở đâu?
5. `Host` giữ nguyên hay đổi sang upstream — chọn gì, vì sao, và absolute-form / `*` /
   authority-form (CONNECT) xử lý thế nào để không có hai cách hiểu về "request này gửi tới ai"?
6. Kiểm response có gì khác kiểm request? Vì sao "không tin upstream hơn tin client" không phải
   là hoang tưởng?

## Giả thuyết đăng ký trước (viết 11:11, chưa có file `.go` nào của phase)

| # | Giả thuyết | Tỉ số / giá trị kỳ vọng | Lệnh sẽ dùng để chấm |
|---|---|---|---|
| G1 | Bộ ≥ 40 ca (≥ 30 ca từ chối + ≥ 5 ca nhận + ≥ 5 ca response): có phòng tuyến **N/N** đúng; `-tags nodefense` (tắt 7 phòng tuyến trong `httpx/defense_nodefense.go`) làm **≥ 40 %** số ca từ chối đổi kết quả | ≥ 0.40 số ca từ chối đỏ khi tắt | `make smugglelab` xanh; `make smugglelab-nodefense` đỏ, đếm dòng `--- FAIL` |
| G2 | Oracle `net/http.ReadRequest` trên cùng bộ ca: **cùng từ chối** với mình ở **60-75 %** ca từ chối; phần còn lại là chỗ mình strict hơn (CL trùng giống nhau, `Connection: Host`, absolute-form lệch Host, CONNECT, trailer mang CL…). **0** ca mình nhận mà oracle từ chối | 0.60-0.75; và 0 ca ở hướng nguy hiểm | `go test ./internal/httpx -run TestSmugglingOracle -v` in bảng |
| G3 | Diff-fuzz `FuzzAgainstNetHTTP` 300 s sau khi đổi D4⇒từ chối và CL trùng⇒từ chối: **0** "NGUY HIỂM"/"LỆCH" — hai đổi này chỉ đi hướng vô hại | 0 lệch, ≥ 300 k exec | `make difffuzz` → `bench/p4-difffuzz-300s.txt` |
| G4 | Kiểm tra thêm ở `ReadRequest` (cú pháp Host, dạng request-target, token `Connection`, trailer, CIDR trust) làm `BenchmarkReadRequest` (head 6 header, không body) chậm **< 5 %** ns/op so với commit nền `b3e08f0` | ≤ 1.05x | `go test ./internal/httpx -run '^$' -bench ReadRequest -count 6` ở hai commit, `benchstat` |
| G5 | E2E qua proxy thật: **100 %** ca từ chối ⇒ status đúng, `Connection: close`, và **đúng 1** response trên wire dù client pipeline thêm `GET /hello` ngay sau payload | 1 response / connection cho mọi ca từ chối | `go test ./internal/proxy -run TestSmugglingE2E -v` |
| G6 | XFF trust: peer **không** trong `trusted_proxies` gửi `X-Forwarded-For: 1.2.3.4` ⇒ upstream thấy giá trị giả **0/N** lần, `X-Real-IP` = peer; peer trong list ⇒ thấy `1.2.3.4, <peer>` **N/N** | 0/N và N/N | `TestXFFUntrustedReplaced`, `TestHopByHopAndXFF` (trusted) |
| G7 | Response splitting từ upstream (CL+TE, CL trùng khác nhau, header có CTL, TE trên HTTP/1.0) ⇒ client nhận **đúng một** response 502 và **0 byte** body của upstream lọt ra | 502 × 4 ca, 0 byte lọt | `TestSmugglingE2E` nhóm `kind: response` |

**Quyết định thiết kế đăng ký trước** (đổi sau phải ghi lý do):

| # | Quyết định | Lý do / hệ quả |
|---|---|---|
| D1 | **CL + TE cùng có ⇒ từ chối** (request 400, response ⇒ proxy 502), đóng connection. Thay D4 phase 2 (trả nợ P2-3) | RFC 9112 §6.1 *cho phép* bỏ CL, nhưng "cho phép" ≠ "an toàn": backend nào ưu tiên CL là CL.TE. Mơ hồ ⇒ từ chối |
| D2 | **Nhiều dòng `Content-Length`, kể cả giống nhau ⇒ 400**. Strict hơn `net/http` (Go nhận trùng giống nhau) | RFC 9110 §8.6 cho phép về cú pháp; nhưng "2 dòng giống nhau" là tín hiệu có ai đó đã chèn header. Hướng lệch vô hại |
| D3 | **Bare LF ⇒ 400** (giữ D1 phase 2, ghi lại lựa chọn theo yêu cầu ROADMAP). `nodefense` chấp nhận LF để phản chứng | Nguồn smuggling là strict + lenient lệch nhau; mình chọn strict và **biết** mình strict |
| D4 | **`Host` giữ nguyên** khi forward (nginx `$host`), **quyết P-arch-1**. Chỉ sinh `Host = upstream` khi client HTTP/1.0 không gửi. Cú pháp Host kiểm theo `uri-host [":" port]` (bộ ký tự như `httpguts.ValidHostHeader`); sai ⇒ 400 | Reverse proxy đứng trước virtual host: đổi Host là phá routing của upstream. Kiểm cú pháp vì Host là thứ upstream dùng để **chọn ai trả lời** |
| D5 | **Request-target:** origin-form forward nguyên văn; **absolute-form** `http://auth/path` ⇒ viết lại origin-form và `Host := auth`; có `Host` khác `auth` ⇒ **400**; scheme khác `http` ⇒ 400; `*` chỉ với `OPTIONS`, khác ⇒ 400 | RFC 9112 §3.2.2: authority trong target thắng Host. Hai nguồn nói hai địa chỉ = hai cách hiểu "gửi tới ai" ⇒ từ chối |
| D6 | **`CONNECT` ⇒ 501**, đóng | Không tunnel (cùng lớp với P3-2). Forward CONNECT sang origin server là vô nghĩa và mở tunnel qua backend |
| D7 | `Connection:` liệt kê `Host` hoặc `Content-Length` ⇒ **400** | RFC 9110 §7.6.1: chúng không phải hop-by-hop. Xoá theo lệnh client rồi tự sinh lại = client điều khiển được Host proxy gửi đi |
| D8 | **Trailer** chứa `Content-Length`, `Transfer-Encoding`, `Host`, `Trailer`, `Connection`, `Content-Type`… (RFC 9110 §6.5.1) ⇒ 400 (request) / đóng (response) | Trailer là chỗ "sau ranh giới"; cho framing field vào đó là cách phá ranh giới sau khi đã đồng ý |
| D9 | **XFF trust boundary:** `trusted_proxies` (CIDR) trong config. Peer **không** tin ⇒ `X-Forwarded-For := peer` (thay), `X-Real-IP := peer`, xoá `Forwarded`. Peer tin ⇒ **append** peer vào XFF, giữ `X-Real-IP` nếu có | Header do client gửi là dữ liệu không tin được. Phase 7 rate-limit theo IP: không có ranh giới này thì một header giả bypass |
| D10 | **Response kiểm bằng cùng parser**: CL+TE, CL bẩn, CTL, TE/1.0, trailer cấm ⇒ `ReadResponse` lỗi ⇒ proxy **502**, đóng connection **upstream**; connection client giữ theo D6 phase 3 (body request đã đọc hết, chưa gửi gì). *Sửa turn 1 11:45: bản đăng ký ghi "đóng cả hai" — thừa, xem log.* Lỗi **giữa** body (trailer cấm) ⇒ chỉ đóng, không 502 thứ hai | Upstream có thể bị chiếm hoặc chính nó là proxy khoan dung. Không tin upstream hơn client |
| D11 | Mọi request bị từ chối ở tầng parse ⇒ **đóng connection**, không đọc tiếp | Parser đã lệch, byte sau đó vô nghĩa; pipeline sau payload bẩn là chính kịch bản smuggling |

## Deliverable

- `testdata/smuggle/*.txt`: ≥ 40 ca, mỗi ca có `expect`, `source` (RFC §/PortSwigger), `why`.
- `make smugglelab` xanh: `TestSmuggling` (parser + ranh giới body/phần dư) và `TestSmugglingE2E`
  (proxy thật, 1 response/connection).
- `make smugglelab-nodefense` **đỏ** với ≥ 40 % ca từ chối lật.
- `TestSmugglingOracle`: bảng đồng ý/lệch với `net/http` (G2), 0 ca hướng nguy hiểm.
- XFF trust (G6), response splitting (G7), P-arch-1 đóng (D4/D5), P2-3 đóng (D1).

## Reproduce toàn bộ phase

```bash
# 1. bộ ca smuggling, hai tầng (không cần server nào chạy)
make smugglelab                      # TestSmuggling (61 ca, parser) + TestSmugglingE2E (proxy thật) + XFF + absolute-form
make smugglelab-nodefense            # PHẢI đỏ: 20/52 ca từ chối lật ở parser, 21 ở e2e
rtk proxy go test ./internal/httpx/ -run TestSmugglingOracle -v   # G2: bảng "mình strict hơn net/http ở đâu"

# 2. toàn bộ test + diff-fuzz sau D1/D2
go test ./... -race -count=1
go test ./internal/httpx/ -run '^$' -fuzz FuzzAgainstNetHTTP -fuzztime 300s -fuzzminimizetime 1s   # G3

# 3. G4: chi phí kiểm tra thêm. So allocs/op và CPU share, KHÔNG so ns/op trên WSL2
git worktree add /tmp/base b3e08f0 && cp internal/httpx/smuggle_test.go /tmp/base/internal/httpx/bench_base_test.go  # (giữ lại chỉ BenchmarkReadRequest)
(cd /tmp/base && go test -c -o /tmp/base.test ./internal/httpx/) && go test -c -o /tmp/head.test ./internal/httpx/
for i in 1 2 3 4 5; do for t in base head; do taskset -c 2 /tmp/$t.test -test.run '^$' -test.bench 'ReadRequest$' -test.count 2 -test.benchmem >> /tmp/$t.txt; done; done
go run golang.org/x/perf/cmd/benchstat@latest /tmp/base.txt /tmp/head.txt
taskset -c 2 /tmp/head.test -test.run '^$' -test.bench 'ReadRequest$' -test.benchtime 3s -test.cpuprofile /tmp/cpu.out
go tool pprof -top -cum -focus='normalizeTarget|validHost|checkConnectionTokens' /tmp/head.test /tmp/cpu.out

# 4. e2e binary thật: make proxylab + 3 payload thô qua nc (script: xem bench/p4-e2e-binary.txt)
```

## Nhật ký

Chi tiết theo giờ ở `diary/phase4-log.md`. Tóm tắt turn 2:

1. G4 đo trước khi máy bận. Alloc lộ 2 cấp phát mới (`bytes.Split` trong `checkConnectionTokens`),
   sửa về 26 allocs/op như nền. ns/op nhiễu ± 10-37 % trên WSL2, không đo được hiệu ứng ~5 %; đổi
   thước sang **CPU share theo pprof**: hàm phase 4 chiếm **6.5 %** của `ReadRequest`.
2. Hook `rtk` tóm tắt output `go test -v` trong script ⇒ ba file bench rỗng; chạy lại bằng `rtk proxy`.
3. `pkill -f` với pattern nằm trong dòng lệnh: **tự sát lần hai** (phase 3 đã ghi). Fuzz phải chạy lại.
4. `go build … && ./bin/x &` đưa cả chuỗi vào nền ⇒ `nc` gõ vào cổng chưa mở. Tách build ra.
5. Mọi bài đúng/sai xanh ngay: 61/61 ca parser, 54/54 ca e2e, phản chứng đỏ 20/52. Hai giả thuyết
   **số** (G1 nửa sau 38 % vs ≥ 40 %, G2 46 % vs 60-75 %) sai; G4 sai sát (6.5 % vs < 5 %).

## Giả thuyết sai

| Tôi tưởng là | Thực tế là | Lệnh + output đã lật tẩy | Đã sửa thế nào |
|---|---|---|---|
| G2: `net/http` cùng từ chối 60-75 % ca | **46 %** (24/52). Go **nhận** CL.TE cơ bản (ưu tiên TE, đúng RFC 9112 §6.1), TE.CL, bare LF, trailer mang CL, `Connection: Host`, response CL+TE, TE trên response 1.0 | `rtk proxy go test ./internal/httpx -run TestSmugglingOracle -v` → `G2: ca từ chối 52 — oracle cùng từ chối 24 (46 %), mình strict hơn 28, hướng nguy hiểm 0` (`bench/p4-oracle.txt`) | Không sửa code: hướng lệch là hướng an toàn. Sửa cách nghĩ: backend Go khoan dung **đúng những chỗ RFC cho phép**, và mỗi chỗ đó là một ca proxy phải chặn thay nó |
| G1 nửa sau: tắt 7 phòng tuyến lật ≥ 40 % ca từ chối | **38 %** (20/52) ở parser. 32 ca còn lại bị chặn bởi hàng rào phase 2 không có công tắc (CTL, obs-fold, tên header không token, chunk-size, Host/target, CONNECT…) | `make smugglelab-nodefense` → 20 dòng `--- FAIL` (`bench/p4-smugglelab-nodefense.txt`) | Không nới bản nodefense để đủ số. Ghi: bộ ca đo **cả** hàng rào phase 2; phần "lật được" chỉ là phần phase 4 thêm |
| G4: kiểm tra thêm < 5 % | **≈ 6.5 %** CPU share (`pprof -focus`, 3 s, taskset). ns/op không dùng được: ± 10-37 % | `bench/p4-bench-readrequest.txt`: benchstat `+25.5 % (p=0.001)` nhưng mẫu HEAD 3455/3984 ns; pprof: `ReadRequest 6.50 %` trong focus | Bỏ 2 alloc (`bytes.Split` → `strings.Cut`), bỏ 3 lần tra map Host. Đổi thước đo sang allocs/op + CPU share |
| Ca 10/12 (TE.TE kèm CL) ⇒ 501 | **400**: có CL ⇒ D1 bắt trước kiểm TE | `go test -race` lần 1 turn 1: `status 400, muốn 501` | Sửa `expect`; thứ tự phòng tuyến quyết định status, ghi vào `why` |
| Response bẩn ⇒ 502 **và** đóng connection client | Client giữ được (D6 phase 3: body request đã đọc hết, chưa gửi gì); chỉ upstream bị đóng | turn 1 lần 1: `502 phải kèm Connection: close` đỏ 4 ca | Sửa test + D10 |
| `pkill -f <pattern>` trong cùng dòng với lệnh mới là vô hại nếu pattern khác | Dòng lệnh chứa pattern ⇒ pkill giết chính shell ⇒ exit 144, lệnh mới chết theo. **Lần hai** (phase 3 §2 23:45 đã ghi) | task `bict1p1tm` exit 144 | Quy tắc: `pkill` luôn ở lệnh riêng; kiểm `pgrep -c -x` trước |
| `go build … && ./bin/x … &` chạy build đồng bộ rồi mới nền | `&` áp cho cả danh sách `&&` ⇒ build cũng vào nền, `sleep 0.5` hết trước khi build xong | e2e lần 1: 3 mục `nc` rỗng | Build ở dòng riêng |
| Output `go test -v` trong `{ …; } > file` là thô | Hook `rtk` viết lại `go test` ⇒ `-json` + tóm tắt, mất `--- FAIL`/`t.Logf` | `bench/p4-oracle.txt` lần 1: `Go test: 1 passed in 1 packages` | `rtk proxy go test` cho mọi lệnh cần output thô |

## Số đo

Phase này đo **đúng/sai theo ca**; chỉ G4 là thời gian (closed-loop vô nghĩa ở đây — là micro-bench
một hàm, `taskset -c 2`, WSL2, cùng máy phase 0-3). Nguồn: `bench/p4-*.txt`, HEAD `dc3a824`+turn 2,
2026-09-04 12:28-12:45.

| # | Đăng ký | Đo được | Kết luận |
|---|---|---|---|
| G1 | ≥ 40 ca (≥ 30 từ chối / ≥ 5 nhận / ≥ 5 response), N/N đúng; nodefense lật ≥ 40 % ca từ chối | **61 ca** (47 request từ chối, 7 nhận, 7 response), **61/61** parser, **54/54** e2e; nodefense lật **20/52 = 38 %** parser (01 02 03 10-15 20 21 22 30 31 45 46 47 90 91 94), 21 e2e (thêm `95-resp-ok-eof` — do bẫy #2 phase 3 cũng bật dưới cùng tag) | Nửa đầu **đúng**, nửa sau **sai sát ranh**. Phản chứng **đỏ** ở cả hai tầng |
| G2 | oracle cùng từ chối 60-75 %; 0 ca hướng nguy hiểm | **24/52 = 46 %** cùng từ chối; mình strict hơn **28**; hướng nguy hiểm **0** | Tỉ lệ **sai**, phần an toàn **đúng**. Danh sách 28 ca ở `bench/p4-oracle.txt` |
| G3 | diff-fuzz 300 s: 0 lệch, ≥ 300 k exec | **7 753 072 exec**, 0 "NGUY HIỂM"/"LỆCH", 40 input mới (corpus 767), PASS 300.5 s (`bench/p4-difffuzz-300s.txt`) | **Đúng.** D1/D2 chỉ đi hướng vô hại: mọi input Go nhận mà mình cũng nhận vẫn cùng body và cùng phần dư |
| G4 | kiểm tra thêm < 5 % ns/op | allocs 26 → 28 → **26** (sau sửa); ns/op nhiễu ± 10-37 %, benchstat +25 % không tin được; **CPU share 6.9 % → 6.5 %** sau bỏ tra map | **Sai** (6.5 % > 5 %), và sai cả **thước đo**: ns/op trên WSL2 không phân giải được 5 % |
| G5 | 100 % ca từ chối: status đúng + close + đúng 1 response dù pipeline | **47/47** (`TestSmugglingE2E`); binary thật qua `nc`: CL.TE + GET pipelined ⇒ 1 response `400`, `Connection: close` | **Đúng** |
| G6 | XFF giả từ peer không tin: 0/N lọt, `X-Real-IP` = peer; peer tin: N/N append | `TestXFFUntrustedReplaced` PASS (0 lần `1.2.3.4`, `X-Forwarded-For: 127.0.0.1`, `X-Real-Ip: 127.0.0.1`, `Forwarded` mất); `TestHopByHopAndXFF` với `127.0.0.0/8` ⇒ `10.0.0.1, 127.0.0.1`; binary thật `nc`: cùng kết quả | **Đúng** |
| G7 | 4 ca response bẩn ⇒ 502, 0 byte lọt | 4 ca head bẩn (90 91 92 93) ⇒ **502**, body không chứa `EVL`/`hello`; ca 94 (trailer bẩn, head sạch) ⇒ 200 rồi **đóng giữa body**, client thấy lỗi đọc, không 502 thứ hai | **Đúng**; ca 94 là hệ quả "một response cho một request" phase 3 |

## Invariant + lệnh kiểm chứng

| Invariant | Cài ở | Kiểm chứng | Kết quả |
|---|---|---|---|
| **Bất biến số 1**: proxy và upstream đồng ý ranh giới mỗi request — mọi head mà hai framing có thể đọc khác nhau đều bị **từ chối**, không đoán | `body.go:framing` (CL+TE ⇒ `ErrAmbiguousFraming`; TE phải đúng một token), `body.go:parseContentLength` (một dòng, chữ số thuần), `parse.go:readLine` (CRLF), `parse.go:readHeaders` (tên là token tới sát `:`, không obs-fold, không CTL) | `make smugglelab` (61 ca: status, body, **phần dư**) + `TestSmugglingOracle` (0 ca mình nhận mà Go từ chối) + diff-fuzz | 61/61; 0 hướng nguy hiểm; 7.75 M exec 0 lệch |
| **Phòng tuyến có răng**: tắt phòng tuyến ⇒ bộ test đỏ | `httpx/defense.go` ↔ `defense_nodefense.go` (7 hằng, tag `nodefense`) | `make smugglelab-nodefense` PHẢI đỏ | đỏ cả hai package, 20/52 ca lật ở parser, 21 ở e2e |
| **Từ chối ⇒ đóng, không đọc tiếp** (D11): byte pipelined sau payload bẩn không bao giờ được trả lời | `proxy.go:replyReadError` luôn `keep=false` rồi `serveConn` return; `forward.go` lỗi body ⇒ 400/408 `keep=false` | `TestSmugglingE2E`: payload + `GET /hello` pipelined ⇒ đúng 1 response rồi EOF; binary thật qua `nc` | 47/47; `nc` thấy 1 response 400 |
| **"Gửi tới ai" chỉ có một cách hiểu** (D4/D5/D6/D7): Host đúng cú pháp; absolute-form ⇒ origin-form, authority thắng, lệch Host ⇒ 400; `*` chỉ OPTIONS; CONNECT ⇒ 501; `Connection:` không được xoá Host/CL | `request.go:normalizeTarget`, `request.go:validHost`, `parse.go:checkConnectionTokens` | ca 60-71, 83-86, `TestAbsoluteFormRewritten` | `http://vhost.example/headers` ⇒ upstream thấy `Host: vhost.example` + `/headers`; lệch ⇒ 400 close |
| **Trailer không phá ranh giới đã đồng ý** (D8) | `chunked.go:checkTrailer` (RFC 9110 §6.5.1) gọi trong `beginChunk` khi size 0 | ca 45-47 (request), 94 (response) | request: body lỗi 400; response: 200 rồi đóng giữa body, không 502 thứ hai |
| **Không tin upstream hơn client** (D10): response đi qua cùng parser | `response.go:ReadResponse` dùng cùng `readHeaders`/`framing`; `forward.go` lỗi head ⇒ 502 | ca 90-96 qua `rawUpstream` | 4 head bẩn ⇒ 502, 0 byte `EVL`/`hello` lọt |
| **Ranh giới tin cậy của XFF** (D9): peer ngoài `trusted_proxies` không nói được gì về "client thật" | `forward.go:forwardedHeaders`, `proxy.go:Config.isTrusted` (CIDR, sai ⇒ panic khởi động) | `TestXFFUntrustedReplaced`, `TestHopByHopAndXFF` (127.0.0.0/8), `TestBadTrustedProxiesPanics`, `nc` binary thật | 0 lần `1.2.3.4` lọt; tin ⇒ `10.0.0.1, 127.0.0.1`; `not-a-cidr` ⇒ panic |
| **Kiểm tra thêm không đổi hồ sơ cấp phát** | `parse.go:checkConnectionTokens` (`strings.Cut`), `request.go:normalizeTarget(req, h, hosts)` | `BenchmarkReadRequest -benchmem` hai commit | 26 allocs/op, 864 B/op ở cả nền và HEAD; CPU share 6.5 % |

## Đọc gì

- RFC 9112 §3.2 (request-target: origin/absolute/authority/asterisk-form), §3.2.2, §6.1 (TE), §6.3
  (framing, và câu "MUST ... close the connection after responding" khi CL+TE), §7.1.2 (trailer)
- RFC 9110 §5.6.2 (token), §6.5.1 (trailer không được mang framing/routing field), §7.6.1
  (Connection hop-by-hop), §7.2 (Host), §8.6 (Content-Length)
- RFC 7239 (Forwarded) — để biết mình **không** tin nó
- PortSwigger "HTTP request smuggling": CL.TE, TE.CL, TE.TE obfuscation list

## Rút ra

**Trả lời 6 câu hỏi đầu file.**

1. *Vì sao "CL + TE ⇒ ưu tiên TE" đúng RFC mà vẫn là lỗ hổng.* RFC 9112 §6.1 viết cho **một** bên
   nhận. Smuggling không nằm ở một bên; nó nằm ở **hai** bên đọc cùng một chuỗi byte. Nếu proxy ưu
   tiên TE và backend ưu tiên CL (hoặc không hiểu TE viết lạ), phần đuôi mà proxy coi là "hết body"
   backend coi là "request kế tiếp" — request đó mang credential của connection người khác nếu có
   pool (phase 5). Ranh giới bị phá ở phía **backend**, nhưng lỗi là của proxy, vì proxy là bên đã
   *chọn* một cách hiểu trong khi thấy rõ có hai. Bằng chứng ca 01: Go **nhận** nó (body="" dư="G")
   — backend Go hành xử đúng RFC, và chính vì thế proxy đứng trước Go phải chặn thay nó.
2. *Phản chứng nào chứng minh test có răng.* Bảy hằng trong `defense_nodefense.go`, mỗi hằng mô
   phỏng đúng một kiểu khoan dung thật (ưu tiên CL, substring "chunked", trim tên header, nhận LF,
   lấy CL đầu, ParseInt, nhận trailer bẩn). Tắt ⇒ 20/52 ca lật. Nếu tắt mà vẫn xanh thì hoặc bộ ca
   không chạm phòng tuyến, hoặc phòng tuyến là no-op (bài G4 phase 3). Con số 38 % còn nói một điều
   khác: 32 ca còn lại đỏ nhờ **phase 2**, không nhờ phase 4 — bộ ca đo cả hàng rào cũ.
3. *Go đồng ý bao nhiêu, và vì sao chỉ một hướng nguy hiểm.* 24/52. Ở 28 ca còn lại Go nhận mà
   mình từ chối. Hướng này vô hại vì request **không tới** backend — không có "hai cách hiểu" khi
   chỉ một bên đọc. Hướng ngược (mình nhận, Go từ chối) mới nguy hiểm: proxy forward một thứ backend
   sẽ đọc khác, hoặc trả 400 và đóng trong khi proxy tin connection còn sạch. Đo: 0 ca, và diff-fuzz
   7.75 M input không tìm được. Bài học đắt hơn con số: **backend Go khoan dung đúng chỗ RFC cho
   phép** — bare LF, CL trùng, trailer mang CL, `Connection: Host`, response CL+TE. Mỗi chỗ đó là
   việc của proxy.
4. *XFF tin được gì.* Không gì cả, trừ khi peer là proxy **mình** cấu hình. Append-không-overwrite
   chỉ là nửa bài: nó giữ chuỗi đúng dạng nhưng vẫn để client viết phần đầu chuỗi. Ranh giới là
   `trusted_proxies`: peer trong list ⇒ append (nó đã làm việc của mình ở hop trước); ngoài list ⇒
   **thay** bằng IP peer và đặt `X-Real-IP`. Không có ranh giới này, rate limiter phase 7 đếm theo
   XFF sẽ bị bypass bằng một header giả — G6 đo 0/N lọt là số cần giữ tới phase 7.
5. *Host giữ hay đổi.* **Giữ** (D4): reverse proxy đứng trước virtual host, đổi Host là đổi người
   trả lời. Nhưng "giữ" chỉ an toàn khi Host chỉ có **một** nguồn: absolute-form mang authority thứ
   hai ⇒ viết về origin-form và authority thắng, lệch ⇒ 400; `Connection: Host` là cách thứ ba để
   client điều khiển Host mà proxy gửi ⇒ 400; CONNECT là authority-form ⇒ 501. Ngoại lệ duy nhất
   sinh Host là client HTTP/1.0 không gửi — ghi từ phase 3. P-arch-1 đóng có ý thức, không tình cờ.
6. *Kiểm response khác gì kiểm request.* Cùng parser, khác hệ quả khi lỗi: lỗi ở **head** ⇒ còn trả
   được 502 (client chưa nhận gì); lỗi **giữa body** (trailer bẩn) ⇒ chỉ được đóng, vì đã có một
   response đi rồi (bất biến "một response cho một request" phase 3). Không hoang tưởng vì upstream
   có thể là một proxy khoan dung khác, hoặc một app ghi header từ input người dùng (ca 92: NUL
   trong Set-Cookie) — response splitting là smuggling theo chiều về.

**Về ba giả thuyết sai.** G1/G2 sai vì đoán **tỉ lệ** khi chưa hiểu phân bố: bộ ca là do mình viết,
nên tỉ lệ đo ra phản ánh mình chọn ca gì nhiều hơn phản ánh proxy hay Go. Lần sau đăng ký theo
**danh sách ca** ("Go sẽ nhận 01, 02, 30…"), không theo phần trăm. G4 sai theo cách có ích hơn:
5 % là hiệu ứng nhỏ hơn nhiễu của ns/op trên WSL2 (± 10-37 %), benchstat cho `+25 % p=0.001` mà vẫn
sai vì đuôi phân bố; allocs/op (± 0 %) và CPU share theo pprof mới phân giải được. Chọn thước trước
khi chọn số.

**Về vận hành.** `pkill -f` tự sát lần hai dù phase 3 đã ghi; `&&` với `&`; hook tóm tắt output.
Ba lỗi cùng một gốc: tin một lệnh làm đúng điều mình nghĩ mà không nhìn kết quả thô. Đây cũng là
gốc của smuggling.

## Nợ kỹ thuật

Chi tiết + lệnh trả trong `docs/debts.md`.

- [ ] **P4-1** 🔧 — `hasConnectionToken` / `StripHopByHop` (phase 2) vẫn `bytes.Split`/`strings.Split`
  cấp phát; `checkConnectionTokens` 5 % CPU share. Gộp ba lần duyệt `Connection:` thành một.
- [ ] **P4-2** 📏 — G4 đo trên WSL2 không phân giải 5 %; đo lại trên Linux thuần cùng P-env-2/P3-5.
- [ ] **P4-3** ⏳ — `Forwarded` (RFC 7239), `X-Forwarded-Proto/Host/Port` chưa sinh; `X-Real-IP` từ peer
  tin cậy chưa kiểm là IP. Cần khi phase 7 (rate limit) và phase 8 (TLS: Proto mới có nghĩa).
- [ ] **P4-4** 🔧 — Tag `nodefense` dùng chung với phase 1/3 ⇒ e2e lật thêm ca `95-resp-ok-eof` vì bẫy #2
  phase 3 bật cùng lúc. Tách tag theo phase, hoặc ghi rõ trong Makefile.
- [ ] **P4-5** 🔧 — Bộ ca chưa có: `Expect: 100-continue`, chunk-ext dài quá `MaxLineBytes`, `Content-Length`
  trên GET có body qua proxy, header bomb e2e, `HTTP/1.1` với SP thừa cuối request-line.
- [ ] **P4-6** 📏 — Oracle thứ hai (nginx/h2o qua docker) cho `TestSmugglingOracle`: một backend
  không đủ để nói "hướng an toàn".
- [x] **P2-3** — đóng: CL+TE ⇒ `ErrAmbiguousFraming` 400, phản chứng đỏ (ca 01-03, 10, 12, 90).
- [x] **P-arch-1** — đóng: Host giữ nguyên (D4) + một nguồn authority (D5/D6/D7).
