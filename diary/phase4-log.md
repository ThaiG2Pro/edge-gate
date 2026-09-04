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
