# Phase 5 — log thô

> Bản biên tập: [`phase5.md`](phase5.md). File này ghi theo thứ tự thời gian, kể cả ngõ cụt.

## §0 Mang từ phase 0-4 sang (đọc lại trước khi gõ)

1. **Đơn vị là RTT tiết kiệm/request** (phase 0 G2/G3). Tỉ số ở RTT 0 và RTT 20 ms nói ngược
   nhau về cùng một pool. ROADMAP viết "~1.1x / >15x" — cả hai đã bị phase 0 lật, đăng ký G1/G2
   theo số phase 0 (2.0-2.5x / 1.4-1.6x), không theo ROADMAP.
2. Bẫy #3 phase 3 (byte thừa trong `bufio.Reader`) giờ nằm trên connection **dùng chung**: không
   còn là bug, là rò dữ liệu chéo người dùng. Phản chứng G3 phải thấy client B đọc được đuôi của A.
3. Phòng tuyến chỉ tin khi **đã thấy nó đỏ** lúc tắt (`poolCheckClean`).
4. Bug nằm ở đường không test nào chạy. Đường đó lần này: (a) client bỏ đi **giữa body response**
   (phase 3 chỉ có test client bỏ đi giữa body request), (b) upstream đóng rỗi **giữa probe và
   write**, (c) `resp.Close` (body-tới-EOF) trên connection pool.
5. ns/op trên WSL2 không đo được 5 % (phase 4 G4). Phase này chỉ có số **ms** ở mức 100 µs-20 ms,
   đo được; nhưng vẫn ghi closed-loop + cùng máy.
6. Vận hành: `pkill -f` riêng dòng; `go build` riêng dòng trước `&`; `rtk proxy go test` khi cần
   output thô; `sleep` foreground bị chặn.

## §1 Turn 1 — 2026-09-04 15:40

- 15:40 viết `phase5.md`: 6 câu hỏi, G1-G7, D1-D9. Chưa có file `.go`.
- Trong lúc viết G7 đã tự sửa một suy luận sai ngay trong ô: tưởng TIME_WAIT nằm ở upstream "vì nó
  nhận `Connection: close`" — nhưng D5 bỏ header đó, giờ **proxy** đóng trước ⇒ TIME_WAIT ở proxy.
  Giữ nguyên vết sửa trong bảng.
- Trong lúc viết D9 cũng tự sửa: LIFO ⇒ đỉnh stack **trẻ nhất**, quét tuổi phải từ đáy. Chọn cách
  đơn giản: `get` pop đỉnh + kiểm tuổi; `put` khi đầy đóng con **đáy** (già nhất) thay vì con mới.
- Kế hoạch file:
  - `internal/proxy/pool.go`: `pool`, `pooledConn`, `get/put`, `Stats`
  - `internal/proxy/pool_linux.go` / `pool_other.go`: `probeIdle` (MSG_PEEK)
  - `internal/proxy/defense.go` (+nodefense): `poolCheckClean`
  - `internal/proxy/proxy.go`: `Config.Pool`, `Server.pool`, `Close` đóng idle, `PoolStats()`
  - `internal/proxy/forward.go`: bước 2-5 dùng pool, `release(clean)`, retry một lần
  - `internal/proxy/pool_test.go`: G3-G6 + EOF + Close
  - `internal/fixture`: `ListenAndServe(addr)` để `cmd/poollab` không import `net/http`
  - `cmd/poollab/main.go`: in-process upstream + proxy + client raw; `-pool`, `-n`, `-conns`
  - `cmd/edgegate/main.go`, `config/dev.json`: khối `pool`, flag `-pool`
  - `Makefile`: `poollab`, `poollab-rtt`, `poollab-nodefense`
- 15:47 code xong: `proxy/{pool,pool_linux,pool_other}.go`, `defense.go` (+`poolCheckClean`),
  `proxy.go` (Config.Pool, Server.pool, Close đóng idle, PoolStats), `forward.go` (bước 2-5 tách
  thành `exchange`, vòng retry ở `roundTrip`), `pool_test.go` (7 test), `fixture.ListenAndServe`,
  `cmd/poollab`, `cmd/edgegate` + `config/dev.json` khối `pool`, Makefile. Build/vet xanh lần đầu.
- 15:49 `go test -race -run 'TestPool|TestDirty|TestIdleClosed'`: **7 pass / 1 fail**, và phản chứng
  `-tags nodefense` đỏ. Nhưng đọc kỹ thì có **hai lỗi kỳ vọng và một lỗi phản chứng**:
  1. `TestIdleClosedUpstream/noprobe-POST-body-502` ra **25/50** 502 xen kẽ 200, không phải 50/50
     như G4 đăng ký. Không phải bug proxy: 502 ⇒ connection chết bị bỏ ⇒ pool rỗng ⇒ request kế
     **dial mới** ⇒ 200 ⇒ put ⇒ upstream FIN ⇒ request kế nữa 502. Kỳ vọng "50/50" quên rằng
     chính hành vi đúng (bỏ connection lỗi) làm mẫu xen kẽ. Sửa test: đòi đúng chuỗi `5252…`,
     `dropDirty = 25`, `retries = 0`, và **502 giữ connection client** (body đã drain).
     → **G4 sai một nhánh** (ghi vào bảng giả thuyết sai turn 2).
  2. Phản chứng `TestDirtyConnNotPooled -tags nodefense` đỏ với "unexpected EOF" — **đỏ sai chỗ**:
     (a) tag `nodefense` chung bật `rawCopyResponse` ⇒ proxy io.Copy thô, head của B kẹt trong
     `bufio.Writer` chưa Flush tới UpstreamBodyTimeout ⇒ B thấy EOF; (b) probe MSG_PEEK thấy byte
     thừa của A ⇒ **bắt được connection bẩn trước cả kiểm sạch** (`DeadOnProbe`). Cả hai đều che
     mất phòng tuyến đang muốn lật. Sửa: **tách tag `nodefensepool`** (`defense_pool.go`,
     `defense_pool_off.go` — trả một phần P4-4), và test G3 chạy với `Pool.Probe=false` để chỉ còn
     đúng một phòng tuyến. Chạy lại: đỏ đúng chỗ — `B: status 502 body "502 Bad Gateway: upstream
     trả response không hợp lệ hoặc đóng sớm"` (proxy đọc `AAAA…` của A làm status-line của B).
     Ghi nhận thêm: probe D3 hoá ra là **phòng tuyến thứ hai** cho D2, không chỉ cho FIN.
  3. `go test ./... -race` toàn bộ: **`TestRawCopyTrap` đỏ ở build thường**. Upstream giả của phase
     3 phục vụ **một** request rồi ngủ 5 s giữ connection. Phase 3 dial mới nên vô hại; phase 5 pool
     dùng lại đúng connection đó ⇒ request 2 chờ một upstream không bao giờ đọc ⇒ 504 sau 2 s.
     "Giữ connection nhưng không phục vụ request kế" không phải HTTP server — sửa fixture thành
     vòng lặp phục vụ, vẫn không bao giờ tự đóng. Bẫy #2 vẫn đỏ với `-tags nodefense` (3.00 s).
- 15:52 `go test ./... -count=1 -race`: frame/httpx/proxy **ok**. `-tags nodefensepool
  TestDirtyConnNotPooled` **FAIL** (đúng). `-tags nodefense TestDrainOnUpstreamDown|TestRawCopyTrap`
  **FAIL** (đúng).
- 15:50 `go run ./cmd/poollab -n 500` lần đầu (số turn 2 mới tính, đây là smoke): p50 off 1.225 ms
  / on 246 µs = 4.99x, tiết kiệm 979 µs. Ba lỗi dụng cụ đo, sửa ngay:
  1. "RTT = p50 net.Dial" = 401 µs — **đúng cái bẫy phase 0 G2**: trên loopback dial ≈ 96 % là dựng
     socket, không phải RTT. Đổi sang `ping`… lần chạy kế `ping 127.0.0.1` cho **2.778 ms** trong
     khi một dial trọn vẹn 312 µs — ping trên WSL2 không phải RTT TCP. Nguồn cuối: **srtt của kernel
     trên chính connection TCP tới upstream** (`ss -tin`, trường `rtt:srtt/rttvar`) = 153 µs; in cả
     ba cột (srtt, ping, dial) để turn 2 so.
  2. TIME_WAIT đếm số tuyệt đối ⇒ pool-on cũng thấy 541 vì TIME_WAIT sống 60 s gộp mẫu trước. Đổi
     sang **Δ trong mẫu**: off +520 phía proxy / +0 phía upstream; on +0 / +0. G7 sơ bộ đúng hướng
     (TIME_WAIT ở proxy = bên đóng trước).
  3. Trong lúc smoke lại mắc lỗi `go build … && ./bin/x &` (cả chuỗi vào nền) — lần thứ hai phase
     này ghi rồi vẫn mắc. Dọn tiến trình phát hiện **`bin/upstream :8081` sót từ phase 3** (pid
     146978) — `make proxybench` không dọn sạch. Ghi nợ vận hành.
- 15:54 binary: `bin/edgegate` + `bin/upstream` (build riêng dòng), 3 GET + POST + chunked qua curl
  (mỗi curl một connection client mới): upstream log thấy **đúng một** peer `127.0.0.1:42300` cho
  cả 6 request — pool dùng chung giữa các client, đúng nghĩa. Log khởi động in
  `pool=true max_idle=64 max_idle_time=60000ms`.
- Chưa đo G1/G2/G7 chính thức, chưa có `bench/p5-*` — việc của turn 2. Lưu ý cho turn 2: p50
  pool-off 1.15-1.23 ms **cao hơn hẳn** 473 µs phase 3 (cùng loopback) — cần hiểu trước khi chấm G1
  (in-process ba vai? `ss` exec chen giữa? GC?).

## §2 Turn 2 — 2026-09-04 16:00

- 16:00 `sudo -n true` ⇒ "a password is required": `make poollab-rtt` (netem) phải do người dùng gõ
  `! make poollab-rtt`. Làm mọi thứ không cần sudo trước.
- 16:01 `make poollab` (n=2000) → `bench/p5-poollab-rtt0.txt`: 5.34x, 958 µs, 22.8 RTT. **G1 sai cả ba
  cột.** Dial đo tại chỗ 834 µs, không phải 300 µs. TIME_WAIT Δ +1964 proxy / +0 upstream; pool-on +0.
- 16:03 nghi số pool-off 1.18 ms (phase 3: 473 µs). Kiểm chéo: (a) upstream ngoài tiến trình
  (`bin/upstream :18081`): 2.88x / 988 µs; (b) `bin/proxylab -mode overhead` đo `bin/edgegate
  -pool=false` rồi `-pool=true`: 1.624 → 0.967 ms nhưng p50 thẳng nhảy 205 → 453 µs giữa hai lần.
  `uptime`: load 9.21 trên 6 core, `python` lạ 400 % CPU. ⇒ máy ồn, không phải bench sai. Cột bền:
  off − on = 958 / 988 / 657 µs.
- 16:04 srtt kernel = 550 µs với rttvar 0.49 ms ở hai tiến trình (42 µs in-process): ACK hoãn cõng
  response ⇒ srtt gồm thời gian xử lý. Ba nguồn RTT đều bẩn ở loopback; giữ cả ba cột, chấm "theo
  RTT" ở RTT 20 ms.
- 16:06 test G3-G6 `-race -v`: **TestPoolReuseMixed đỏ một lần** (`Puts:199 Idle:0`) — client nhận
  response trước khi proxy put. Race test-vs-proxy, sửa `waitStats` ở 5 chỗ đọc stats. `-count=5
  -race` xanh. Regenerate `bench/p5-pooltests.txt` (9 PASS).
- 16:07 G3 `-count=20`: xanh 20/20; `-tags nodefensepool` đỏ **20/20**, cùng một thông điệp
  `B: status 502 … connection bẩn về pool` → `bench/p5-poollab-nodefense.txt`.
- 16:08 `go test ./... -race` ok ×3 package, proxy 25 PASS → `bench/p5-tests-turn2.txt`.
- 16:12 ghi phase5.md: Nhật ký (3 mục), Giả thuyết sai (7 hàng), Số đo (G2 để ⏳). Commit turn 2.
  Còn: `make poollab-rtt` (người dùng chạy) → điền G2, rồi turn 3.
- 16:20 người dùng chạy `! make poollab-rtt 2>&1 | tee bench/p5-poollab-rtt20.txt` (sudo). ping 20.448 ms.
  off 65.943 / on 48.08 / thẳng 20.974 ms ⇒ 1.37x, 17.86 ms, 0.87 RTT. Dial = 20.757 ms = 1.0 RTT.
  **G2 sai sát biên cả ba cột, cùng hướng thấp**: mẫu qua proxy +5-7 ms ngoài mô hình 3/2 RTT, mẫu
  thẳng +0.5 ms. Nghi máy ồn (4 lần đánh thức/request). Không sửa code. Cặp số ROADMAP: tỉ số
  5.34x → 1.37x, tiết kiệm 0.96 → 17.9 ms. `rtt-down` xong, `qdisc noqueue`.
- 16:22 chốt turn 2: G1 ❌, G2 ⚠️, G4 ⚠️ (1/4), G3/G5/G6/G7 ✅. Commit.

## §3 Turn 3 — 2026-09-04 18:51

- Invariant 9 hàng, Rút ra 6 câu + mục về cách đo, Nợ P5-1..P5-5 + P-ops-1, P4-4 trả một phần.
- `docs/debts.md`: thêm 6 món, ghi chú P4-4. README/ROADMAP hàng 5 ✅ với cặp số ngược ROADMAP.
- Lúc viết P5-2 mới thấy D9 có lỗ: `get` chỉ kiểm tuổi đỉnh, con chết ở đáy sống tới khi đầy. Ghi
  nợ có test fail-trước thay vì sửa vội ở turn diary.
- Lúc viết P5-5 mới thấy `MaxIdleTime` 60 s bằng đúng nginx mặc định — vi phạm chính nguyên tắc "mình
  đóng trước" vừa rút ra ở câu 6. Rút ra rồi mới thấy cấu hình sai: đúng thứ tự cần có.
