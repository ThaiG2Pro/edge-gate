# Phase 3 — log thô

> Bản biên tập: [`phase3.md`](phase3.md). File này ghi theo thứ tự thời gian, kể cả ngõ cụt.

## §0 Mang từ phase 2 sang (đọc lại trước khi gõ)

1. Giả thuyết đăng ký **trước** file `.go` đầu tiên — bảng G1-G6 + D1-D7 trong `phase3.md`, 22:30.
2. Bug nằm ở đường không test nào chạy. Sau khi xanh, **đọc code** đường lỗi (upstream chết
   giữa body, client cắt cụt, response 1xx).
3. Buffering che thứ mình đo: `bufio.Writer` phía client phải `Flush` sau **mỗi** chunk, nếu
   không response streaming bị gom và G4 (Nagle) không đo được.
4. Số tuyệt đối đổi 2.5-6x giữa phiên (P0-7). Chỉ tin **hướng** và **bậc** của tỉ số.
5. Generator và proxy chung 6 core (P-env-2). Ghi `(closed-loop, cùng máy)` cạnh mọi số latency.

## §1 Turn 1 — 2026-09-03 22:30

- 22:30 viết `phase3.md`: câu hỏi, G1-G6, D1-D7. Chưa có file `.go`.
- Kế hoạch file: `internal/proxy/{proxy.go,forward.go,respond.go}`, `cmd/edgegate/main.go`,
  `cmd/upstream/main.go` (net/http, fixture), `config/dev.json`, `internal/proxy/proxy_test.go`.
- 22:40 code xong: `internal/fixture` (net/http, backend giả — chỗ duy nhất ngoài `_test.go`),
  `internal/proxy/{proxy,forward,respond,defense,defense_nodefense}.go`, `cmd/edgegate`,
  `cmd/upstream`, `config/dev.json`, 13 test. `go vet` + `go build` xanh lần đầu.
- 22:45 `go test -race` lần 1: **12/13**. Đỏ: `TestHTTP10ClientGetsEOFBodyAndClose` —
  `400 Bad Request: missing required Host header` từ **upstream**. Nguyên nhân: client HTTP/1.0
  không gửi `Host`; proxy nâng request lên `HTTP/1.1` (D1 gửi `Connection: close` kiểu 1.1) mà
  không thêm `Host` ⇒ net/http từ chối. Bug ở đường không test nào phase 2 chạy (parser cho
  phép 1.0 thiếu Host — đúng RFC — nhưng forward thì không). Sửa: thiếu `Host` ⇒ điền
  `cfg.Upstream` (giống nginx `proxy_set_header Host`). Liên quan P-arch-1: đây là trường hợp
  đầu tiên proxy **buộc phải** sinh Host, dù D2 nói "nguyên văn".
- 22:50 `go test -race` lần 2: 13/13. `make proxylab-nodefense`: **chỉ đỏ một nửa**.
  `TestRawCopyTrap` đỏ đúng, kẹt **3.00 s = UpstreamBodyTimeout** (ủng hộ G1: treo = deadline,
  không vô hạn). `TestDrainOnUpstreamDown` **vẫn xanh** khi tắt drain: body 1000 chữ `Z` không
  drain ghép với request 2 thành `ZZZ…ZGET /hello HTTP/1.1` — method là token dài nhưng HỢP LỆ
  ⇒ vẫn dial ⇒ vẫn 502. Phản chứng không đỏ ⇒ bài test không chứng minh gì (luật SKILL §3).
  Sửa TEST (không sửa code): body `"Z " × 500` ⇒ request 2 parse ra version `Z Z …` ⇒ 505/400.
  Bài học cũ lặp lại: một test xanh chưa bao giờ đỏ chưa phải bằng chứng.
- 22:55 `make proxylab-nodefense` đỏ **cả hai** bài (`TestDrainOnUpstreamDown` 0.00s,
  `TestRawCopyTrap` 3.00s). `go test ./... -race`: frame 2.244s, httpx 1.339s, proxy 3.629s — xanh.
- 22:39 e2e bằng binary thật (`bin/upstream :8081` + `bin/edgegate config/dev.json`), 8 lệnh
  curl, output nguyên văn: `bench/p3-proxylab-curl.txt`. Đúng byte cả 8: GET (CL giữ nguyên),
  POST CL, chunked response (wall 0.11 s = 5×20 ms sleep, không treo), `/eof` → client HTTP/1.1
  thấy `Transfer-Encoding: chunked` (D3), POST body chunked từ curl, `--http1.0 /eof` →
  `Connection: close` không TE (D3 nhánh 1.0), HEAD không body, 2 URL trên 1 connection
  `num_connects=1,0` (keep-alive). Chưa chạy target `make proxylab` nguyên bản (dùng `sleep` +
  `&`); turn 2 chạy đúng target và ghi output.
- Câu hỏi để turn 2: G2/G3/G4 chưa đo. G1 có số sơ bộ (3.00 s = timeout) từ bài phản chứng,
  chưa lập lại. G5, G6 đã có test xanh + phản chứng đỏ, chưa ghi vào bảng số đo.
