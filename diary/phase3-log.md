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

## §2 Turn 2 — run + fix + measure (23:40 → 23:55)

- 23:40 Môi trường: `Linux 6.6.87.2-microsoft-standard-WSL2 x86_64`, `go1.26.2`, 6 core,
  `ulimit -n` 1048576. Cùng máy GOTIT-00663.
- 23:43 `make proxylab` nguyên bản: **exit 0, 4 curl đúng byte**, không treo. Nhưng lệnh của tôi
  treo 2 phút: recipe `go run … & echo $! > pid` rồi `kill $(cat pid)` giết **`go run`**, không
  giết binary con `/tmp/go-build…/exe/edgegate` ⇒ tiến trình mồ côi giữ cổng 8080 và giữ stdout
  của pipe. `make` xanh mà hệ thống bẩn. Sửa recipe: `go build -o bin/edgegate` rồi chạy binary
  trực tiếp. Chạy lại: exit 0, log `edgegate: đóng`, `pgrep` không còn gì. Output:
  `bench/p3-make-proxylab-GOTIT-00663.txt`.
- 23:45 Viết `cmd/proxylab` (client thô `net` + httpx, không net/http) đo G2/G3/G4; thêm
  `?n=&ms=` cho `/chunked` của fixture. `pkill -f 'bin/upstream'` trong cùng dòng lệnh **tự giết
  shell** (dòng lệnh chứa chuỗi khớp) ⇒ exit 144, không có output. Chuyển sang script file +
  `pkill -x` (khớp tên tiến trình).
- 23:47 **G2 = 3.39x** (p50 473 µs qua proxy / 139 µs thẳng, n=2000), trong khoảng 3-5x; chênh
  tuyệt đối 333 µs ≈ dial upstream (~300 µs phase 0) + parse 2 chiều. p99 chỉ 2.63x — đuôi của
  đường thẳng đã dày sẵn (scheduler), proxy không thêm đuôi tương ứng.
  **G3 = 1.44x** (657 / 456 µs), đúng 1.2-1.6x: D1 dial upstream mới cho cả hai mẫu nên keep-alive
  client chỉ tiết kiệm được đúng một dial loopback.
- 23:48 **G4 sai lần 1:** `-nodelay=false` ⇒ p50 473 µs vs 511 µs với `true` — **không khác gì**.
  Nghi 5 chunk `ms=0` gộp vào một Read ⇒ thêm `-chunkms` 2/5/10 ms: vẫn không khác (14.3/29.7/54.2
  vs 13.4/27.8/55.0 ms). Vậy không phải do gộp. Đọc `setNoDelay`: `if *cfg.NoDelay { SetNoDelay(true) }`
  — khi false thì **không gọi gì**, mà Go đã `setNoDelay(fd, true)` trong `newTCPConn`
  (`net/tcpsock.go:290`). Flag là no-op; phase 0 netlab gọi `SetNoDelay(false)` tường minh nên
  mới thấy 44 ms. Sửa: `tc.SetNoDelay(*s.cfg.NoDelay)`.
- 23:49 **G4 lần 2, Nagle thật sự bật:** ms=0 ⇒ p50 **44.004 ms** (vs 430 µs), ms=2 ⇒ 43.97 ms
  (vs 14.4), ms=5 ⇒ 44.00 ms (vs 29.1), ms=10 ⇒ 56.2 ms (vs 54.4). Không phải "+40 ms hằng số"
  như đăng ký: là **sàn ≈ 44 ms** — chunk terminator `0\r\n\r\n` (write nhỏ thứ hai, sau
  head+chunk) bị Nagle giữ tới khi delayed-ACK của segment trước về (~40 ms); response nào tự nó
  đã dài hơn 44 ms thì gần như không thêm. Cùng cơ chế phase 0 G3, khác hình dạng.
- 23:50 G1 lập lại 3 lần `-tags nodefense`: **3.01 / 3.00 / 3.00 s** với `UpstreamBodyTimeout=3s`
  ⇒ treo = timeout ± 10 ms, đúng ± 50 ms đã đăng ký. `go test ./internal/proxy -race -v`: 14/14
  (đếm lại: 14, không phải 13). G6 thêm `t.Logf`: trước 4 → sau 6 / 4 / 5 qua 3 lần.
  Toàn repo `-race` xanh; `make proxylab-nodefense` đỏ đúng.
- Turn 3 còn: bảng Invariant, Rút ra, nợ P3-k (`docs/debts.md`), ROADMAP/README, `Commit:`.
