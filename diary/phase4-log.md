# Phase 4 — log thô

> Bản biên tập: [`phase4.md`](phase4.md). File này ghi theo thứ tự thời gian, kể cả ngõ cụt.

## §0 Mang từ phase 3 sang (đọc lại trước khi gõ)

1. Giả thuyết đăng ký **trước** file `.go` đầu tiên — G1-G7 + D1-D11 trong `phase4.md`, 11:11.
2. Một flag có tên, có log, có test dựng quanh vẫn có thể là **no-op** (G4 phase 3). Với phòng
   tuyến: chỉ tin khi **đã thấy nó đỏ** lúc tắt. `make smugglelab-nodefense` không phải hình thức.
3. Bug nằm ở đường không test nào chạy — lần này đường đó là **response** (D10) và
   **trailer** (D8): hai chỗ phase 2-3 chỉ đọc mà chưa kiểm.
4. Phase 2 có hai lệch với oracle theo hướng vô hại (`"5 "`, CL trùng). Phase này sẽ thêm lệch
   cố ý (D2, D5, D6, D7). Phải **liệt kê** chúng (G2), không để chúng trôi thành "khác biệt lạ".
5. Test chạy trong `internal/httpx` nên `testdata/smuggle` ở gốc repo đọc bằng `../../testdata/smuggle`.

## §1 Turn 1 — 2026-09-04 11:11

- 11:11 viết `phase4.md`: 6 câu hỏi, G1-G7, D1-D11. Chưa có file `.go`.
- Kế hoạch file:
  - `internal/httpx/defense.go` + `defense_nodefense.go` (7 hằng phòng tuyến, tag `nodefense`)
  - `internal/httpx/{parse,body,chunked,request,errors}.go`: D1, D2, D3 (hằng), D4, D5, D6, D7, D8
  - `internal/smugglecase/` : loader `testdata/smuggle/*.txt` (dùng chung httpx + proxy test)
  - `testdata/smuggle/*.txt` ≥ 40 ca
  - `internal/httpx/smuggle_test.go`: `TestSmuggling`, `TestSmugglingOracle`, `BenchmarkReadRequest`
  - `internal/proxy/{proxy,forward}.go`: `TrustedProxies` (D9), `forwardedHeaders`
  - `internal/proxy/smuggle_test.go`: `TestSmugglingE2E`, `TestXFFUntrustedReplaced`, `TestAbsoluteFormRewritten`
  - `cmd/edgegate/main.go`, `config/dev.json`: `trusted_proxies`
  - `Makefile`: `smugglelab` chạy cả hai package
- 11:25 code xong: `httpx/{defense,defense_nodefense}.go` (7 hằng), sửa `parse/body/chunked/request/errors.go`,
  `internal/smugglecase`, 61 file `testdata/smuggle`, `httpx/smuggle_test.go` (TestSmuggling,
  TestSmugglingOracle, BenchmarkReadRequest), `proxy/{proxy,forward}.go` (TrustedProxies,
  forwardedHeaders), `proxy/smuggle_test.go` (E2E, XFF untrusted, absolute-form, panic CIDR),
  `cmd/edgegate`, `config/dev.json`, Makefile. `go build`/`go vet` xanh lần đầu.
- 11:40 `go test ./... -race` lần 1: **259 pass / 10 fail**, hai nguyên nhân, đều là **kỳ vọng sai**:
  1. Ca 10 (`TE: chunked` + `TE: identity` + CL) và 12 (`TE: xchunked` + CL) đăng ký 501, ra **400**:
     có CL ⇒ D1 (CL+TE) bắt **trước** kiểm TE. Thứ tự phòng tuyến quyết định status. Giữ code
     (400 bảo thủ hơn 501), sửa `expect` + `why` của hai ca.
  2. 4 ca response bẩn: proxy trả 502 **không** `Connection: close`. Đúng theo D6 phase 3
     (`keep = drained && !req.Close`): body request đã đọc hết, chưa gửi gì cho client ⇒ giữ được.
     Test đòi close là đòi thừa; D10 đăng ký "đóng cả hai" cũng thừa — chỉ upstream bị đóng
     (`defer uc.Close()`). Sửa test + D10, ghi lý do.
- 11:45 đếm bộ ca: **61** (47 request từ chối, 7 nhận, 7 response) — trên mức đăng ký 40/30/5/5.
- 11:50 `go test ./... -race` lần 2: **xanh toàn bộ** (httpx 1.4 s, proxy 5.7 s, frame 2.3 s). `make smugglelab` exit 0.
- 11:52 phản chứng `make smugglelab-nodefense`: **đỏ cả hai package** (exit target 0 vì `!`).
  Parser: **20/52** ca từ chối lật (01 02 03 10 11 12 13 14 15 20 21 22 30 31 45 46 47 90 91 94);
  e2e: 21 ca. 20/52 = **38 %** — G1 đăng ký ≥ 40 %: **sát ranh, có vẻ sai**. Chấm chính thức ở
  turn 2; không nới lỏng bản nodefense để "cho đủ 40 %" — hằng số nodefense mô phỏng proxy khoan
  dung điển hình, không phải công cụ nắn số.
- 11:53 `TestSmugglingOracle` chạy lần đầu (để biết test chạy được, chưa phải đo): oracle cùng
  từ chối **24/52 = 46 %**, mình strict hơn **28**, hướng nguy hiểm **0**. G2 đăng ký 60-75 % —
  **trông sai**, và sai theo hướng đáng chú ý: `net/http` **nhận** CL.TE cơ bản (01: body="" dư="G"
  — Go ưu tiên TE, đúng RFC 9112 §6.1), TE.CL (02), bare LF (30, 31), trailer mang CL (45),
  `Connection: Host` (50), response CL+TE (90). Nghĩa là backend Go **khoan dung đúng chỗ RFC cho
  phép** — và mỗi chỗ đó là một ca proxy phải chặn thay nó. Ghi vào Rút ra turn 3.
- 11:55 `gofmt -l .` sạch, `go vet ./...` sạch. 20 file đổi/mới. Commit turn 1.

## §2 Turn 2 — 2026-09-04 12:28

- 12:28 **G4 trước tiên** (cần máy yên): worktree `b3e08f0` + bản sao `BenchmarkReadRequest`.
  `-count 6` thô: nền 1.968 µs ± 19 %, HEAD 2.232 µs ± 14 %, **864 → 904 B/op, 26 → 28 allocs/op**
  (`bench/p4-bench-readrequest.txt`). Alloc là tín hiệu sạch nhất (± 0 %): 2 alloc mới.
- 12:29 `pprof -sample_index=alloc_objects`: `bytes.genSplit` + `slicebytetostring` trong
  `checkConnectionTokens` — `bytes.Split([]byte(v), ",")` cho một kiểm tra. Sửa sang `strings.Cut`
  vòng lặp, không cấp phát ⇒ **26 allocs/op, 864 B/op** như nền.
- 12:30 ns/op vẫn nhiễu: benchstat nền vs HEAD-sau-sửa `+15.3 % (p=0.026)` với ± 37 %. Đo lại tử tế:
  build binary test hai bên, `taskset -c 2`, xen kẽ nền/HEAD 5 vòng × count 2 (n=10): nền 2.012 µs
  ± 10 %, HEAD 2.526 µs ± 37 %, `+25.5 % (p=0.001)`. Nhưng mẫu HEAD có 3455 và 3984 ns — đuôi WSL2,
  không phải code. Hỏi CPU profile thay vì hỏi ns/op: `pprof -focus normalizeTarget|validHost|checkConnectionTokens`
  ⇒ **6.9 % tổng mẫu** của `ReadRequest` (3.4 % `normalizeTarget`, 3.4 % `checkConnectionTokens`).
- 12:31 `normalizeTarget` tra map `Host` 3 lần (`Has`/`Get`/`Get`, mỗi lần `CanonicalMIMEHeaderKey`) —
  truyền thẳng `hosts` đã có. Profile lại: **6.5 %** (`normalizeTarget` 3.4 → 1.5 %, `checkConnectionTokens`
  lên 5.0 % — nhiễu profile). **G4 (< 5 %) sai**, sát: chi phí thật ≈ 6.5 % CPU của `ReadRequest`, không
  phải 15-25 % như ns/op gợi ý. Bài học: trên WSL2 với hiệu ứng ~5 %, so ns/op là so nhiễu; so
  allocs/op và share CPU thì được.
- 12:32 thả `make difffuzz` 300 s nền; song song `make smugglelab`, `smugglelab-nodefense`, oracle.
- 12:33 **Hook `rtk` tóm tắt output** của `go test -v` bên trong script (`Go test: 1 passed…`) ⇒ file oracle
  và danh sách ca lật rỗng. Fuzz nền cũng đang chạy qua `rtk go test -json`. Phải dùng `rtk proxy go test`
  cho mọi lệnh cần output thô. Chạy lại oracle + nodefense: `bench/p4-oracle.txt`, `bench/p4-smugglelab-nodefense.txt`.
- 12:35 `go test ./... -race` xanh (httpx 2.6 s, proxy 9.9 s dưới tải fuzz): **28 + 18 + 11 test**
  (`bench/p4-tests-turn2.txt`). `make smugglelab` exit 0 (`bench/p4-smugglelab.txt`).
- 12:36 e2e binary thật lần 1: 3 mục `nc` **rỗng**. `go build … && ./bin/edgegate … &` đưa CẢ chuỗi `&&`
  vào nền ⇒ `sleep 0.5` chạy trước khi build xong. Tách build ra trước. Lần 2 (`bench/p4-e2e-binary.txt`):
  CL.TE + GET pipelined ⇒ **đúng 1** response `400 … both present` + `Connection: close`; XFF giả ⇒
  upstream thấy `X-Forwarded-For: 127.0.0.1`, `X-Real-Ip: 127.0.0.1`; CONNECT ⇒ 501. `make proxylab`
  4 curl vẫn đúng với `trusted_proxies: []`. Không còn tiến trình.
- 12:36 **Tự sát bằng `pkill -f` lần hai.** `pkill -f "fuzz FuzzAgainstNetHTTP"` để giết fuzz `rtk` — pattern
  nằm trong chính dòng lệnh đang chạy (dòng đó cũng khởi động fuzz mới) ⇒ exit 144, cả fuzz cũ lẫn mới
  chết. Phase 3 đã ghi bài này (§2 23:45) và vẫn lặp. Quy tắc mới: **không bao giờ** đặt `pkill` và lệnh
  cần sống trong cùng một dòng; kiểm `pgrep -c -x <tên>` ở lệnh riêng trước.
- 12:37 fuzz khởi động lại (`rtk proxy`, lệnh riêng, máy chỉ chạy fuzz). Chờ 300 s.
- 12:43 fuzz xong: `execs: 7753072`, `new interesting: 40 (total: 767)`, PASS. **G3 đúng.**
- 12:45 điền phase4.md: Reproduce, Nhật ký, Giả thuyết sai (8 dòng), Số đo G1-G7. Chấm: G1 nửa
  đầu đúng / nửa sau sai (38 %), G2 sai (46 %), G3 đúng, G4 sai (6.5 %), G5 G6 G7 đúng ⇒ **3/7 sai**.
  Commit turn 2.

## §3 Turn 3 — 2026-09-04 13:33

- 13:33 điền Invariant (8 dòng), Rút ra (6 câu + 3 giả thuyết sai + vận hành), Nợ P4-1..P4-6;
  `docs/debts.md`: mở P4-1..6, đóng P2-3 + P-arch-1 (chuyển sang Đã trả + bảng); README hàng 4,
  ROADMAP hàng 4 ✅. Header: thực tế ~2.5 giờ, kết thúc 13:45.
- 13:45 commit turn 3, rồi commit ghi hash.
