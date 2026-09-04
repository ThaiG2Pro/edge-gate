# Phase 6 — Load balancing + health (kèm trả nợ phase 5)

- **Thời lượng dự kiến:** 2-3 ngày · **thực tế:** _(turn 3 điền)_
- **Bắt đầu:** 2026-09-04 19:05 · **Kết thúc:** _______
- **Trạng thái:** 🟡 turn 1 xong 22:13 (code + test xanh, chưa đo) — giả thuyết bên dưới đăng ký **trước** file `.go` đầu tiên của phase.
- **Commit:** _______ (commit nền `a0a5e1a`)

> **Đường đi thô, kể cả ngõ cụt:** [`phase6-log.md`](phase6-log.md). File này là bản biên tập.
>
> **Quy tắc ghi nhật ký:** mọi con số, mọi kết luận đều phải kèm **lệnh shell sinh ra nó**
> và **output thật**. Luật đầy đủ: [`../skills/diary/SKILL.md`](../skills/diary/SKILL.md).

## Ràng buộc kế thừa

- **Tuyệt đối không dùng `net/http` trên data path.** `internal/httpx`, `internal/proxy`,
  `internal/lb`, `cmd/edgegate`, `cmd/lblab` chỉ `net` (+ `syscall` cho probe phase 5). `net/http`
  chỉ là fixture (`cmd/upstream`, `internal/fixture`) và oracle trong `_test.go`. Health check chủ
  động (D6) cũng phải viết bằng `net` + `httpx.ReadResponse`, không `http.Get`.
- **Invariant sạch của phase 5 không được sứt:** pool giờ là `map[host]*pool`, mỗi pool giữ nguyên
  `get`/`put`/`discard` và `poolCheckClean`. Đổi backend **không** đổi đường `release`.
- **Đơn vị báo cáo là p99 và phần tải mỗi backend nhận**, không phải "thuật toán X thắng". ROADMAP đòi
  cột **"chỗ P2C thua"** — ghi cái thua bằng số như cái thắng.
- **Phase 0 bài 3 (closed-loop):** `cmd/lblab` là closed-loop (`-conns` goroutine). Khi backend
  chậm, chính client tự gửi ít hơn ⇒ p99 in ra là "p99 của tải mà proxy chịu nổi". Ghi rõ cạnh mọi số.
- Phase 5 chốt: pool tiết kiệm 958 µs/request ở RTT 0 (một dial ≈ 834 µs hôm máy ồn). Phase này
  **không** đo lại con số đó; chỉ cần `Dials == số backend` để chứng minh pool theo host chạy (G7).

## Môi trường

Cùng máy phase 0-5 (`bench/env-GOTIT-00663.txt`). Cả ba bẫy đo mạng áp: closed-loop (`cmd/lblab`),
loopback (RTT ≈ 0 ⇒ chênh lệch giữa thuật toán chỉ đến từ **service time giả lập** của fixture, không
từ mạng — nên phase này **không** cần `rtt-up`, ghi rõ vì sao ở Rút ra), generator chung 6 core
(P-env-2 chưa trả; 4 backend + proxy + client cùng tiến trình).

```console
$ uname -srmo && go version && nproc && ulimit -n
Linux 6.6.87.2-microsoft-standard-WSL2 x86_64 GNU/Linux
go version go1.26.2 linux/amd64
6
1048576
```

_(turn 2: dán `uptime` lúc đo — phase 5 load 9 trên 6 core làm số tuyệt đối lệch 2x.)_

## Mục tiêu phase

Bốn thuật toán chọn backend cài **cả bốn để so được**: round robin, least connections, P2C+EWMA
(decay theo **thời gian**), consistent hashing với vnode. Hai cơ chế sức khoẻ **song song**: active
health check (goroutine nền) và passive outlier ejection (đếm lỗi trên traffic thật, eject rồi cho về
từ từ). Bench quyết định: 4 backend **cố ý lệch** (1 chậm 10x, 1 trả 5xx 30 %), bảng p99 của 4
thuật toán, **và cột "chỗ P2C thua"** (tau quá dài phản ứng chậm hơn least-conn khi một backend đổi
tính giữa chừng). Trả nợ P5-2, P5-3, P5-5 trước khi thêm tầng mới lên pool.

## Câu hỏi phải trả lời được (viết trước khi code)

1. Least-conn **vẫn dồn** traffic vào node nào? Vì sao "inflight thấp" không đồng nghĩa "khoẻ"?
   Con số nào trong bench chứng minh điều đó (phần tải node lỗi nhận được so với 25 %)?
2. "Inflight" của một backend tăng ở đâu, giảm ở đâu? Vì sao **không** đo bằng số connection trong
   pool (P5-3: client nhận response *trước* khi proxy `put`)? Chọn sai thì least-conn lệch lúc nào?
3. EWMA decay theo **thời gian** khác decay theo **số request** ở chỗ nào, và vì sao decay theo số
   request làm một node bị điểm xấu **không bao giờ hồi phục**? Node mới (chưa có mẫu) cho điểm gì
   và cái giá của lựa chọn đó?
4. P2C thua least-conn ở đâu? Khi `tau` sai chiều nào thì thua, và thua **bao nhiêu** trong bench?
   Vì sao Envoy/Finagle vẫn chọn P2C dù thua ở kịch bản đó?
5. Thêm 1 node vào 4 node: `hash % N` di chuyển bao nhiêu phần trăm key, consistent hashing di
   chuyển bao nhiêu? Vì sao cần ~150 vnode/backend chứ không phải 1? Consistent hashing bỏ qua tải
   ⇒ node chậm nhận đúng 25 % — tức là thuật toán này **trả giá gì** để đổi lấy tính ổn định key?
6. Active và passive health bắt được loại chết nào của nhau mà mình không bắt được? Vì sao phải có
   **trần eject** (không eject quá 50 % backend) — điều gì xảy ra nếu không có khi upstream chậm
   đồng loạt vì chính proxy quá tải?
7. Trả nợ: con quá tuổi ở **đáy** stack LIFO vì sao không được `get` dọn (P5-2)? Vì sao
   `MaxIdleTime` của pool phải **ngắn hơn** idle timeout của upstream (P5-5) — bên nào đóng trước
   thì bên kia thấy gì?

## Giả thuyết đăng ký trước (viết 19:10, chưa có file `.go` nào của phase)

Kịch bản lệch chuẩn (`make lblab-skew`): 4 backend in-process, service time nền **2 ms**; backend
`b0` chậm 10x (20 ms); backend `b1` trả **503 nhanh** 30 % (không ngủ khi lỗi); `b2`, `b3` bình
thường. Client closed-loop 32 conn × keep-alive, 20 000 request GET /hello, header `X-Session`
ngẫu nhiên trong 1000 giá trị (khoá cho consistent hash — khoá theo IP thì loopback dồn hết một node).

| # | Giả thuyết | Tỉ số / giá trị kỳ vọng | Lệnh sẽ dùng để chấm |
|---|---|---|---|
| G1 | **RR vs least-conn với node chậm 10x:** RR đưa `b0` đúng **25 %** tải ⇒ p99 ≈ service time của `b0` (≥ 20 ms), p50 ≈ 2-3 ms. Least-conn cân inflight ⇒ tải `b0` ∝ 1/service time ≈ (1/10)/(3+1/10) ≈ **3 %** (dải 3-8 % vì closed-loop), p99 giảm **≥ 2x** so với RR | `b0` RR 25 % → LC 3-8 %; p99 RR/LC ≥ 2 | `make lblab-skew` → `bench/p6-lblab-skew.txt` |
| G2 | **Least-conn dồn vào node lỗi nhanh** (câu ROADMAP): tắt outlier (`-outlier=false`), `b1` trả 503 nhanh 30 % ⇒ inflight thấp ⇒ least-conn cho `b1` **> 30 %** tải (nhiều hơn RR 25 %) ⇒ tỉ lệ 5xx client thấy **> 7.5 %** (RR: 30 % × 25 % = 7.5 %). Bật outlier: `b1` bị eject sau 5 lỗi liên tiếp… nhưng 30 % lỗi **hiếm khi** cho 5 lỗi liên tiếp (0.3⁵ = 0.24 %) ⇒ outlier "consecutive 5xx" **gần như không bắt được** node lỗi 30 %; tải `b1` vẫn > 25 % | LC `b1` > 30 %, 5xx > 7.5 %; outlier bật: eject ≤ 2 lần/20k | `make lblab-skew` (hai lần: `-outlier=false` và mặc định) |
| G3 | **P2C+EWMA tau=1 s ≈ least-conn** trên lệch tĩnh: p99 trong ±30 % của least-conn, tải `b0` 3-10 %. **Chỗ P2C thua** (`-flap`): `b2` đang nhanh đổi thành chậm 10x ở giữa bài; p99 nửa sau của P2C **tau=30 s** cao hơn least-conn **≥ 1.5x** (EWMA cũ kéo điểm, phải chờ ~tau để tin mẫu mới); P2C tau=1 s trong ±30 % least-conn | P2C₁ₛ/LC p99 ∈ [0.7, 1.3]; P2C₃₀ₛ/LC ≥ 1.5 ở nửa sau | `go run ./cmd/lblab -algos leastconn,p2c,p2c-slow -flap` |
| G4 | **Consistent hash bỏ qua tải:** tải mỗi backend **25 % ± 3** bất kể lệch ⇒ p99 ≈ RR (±20 %). **Rehash:** 4 → 5 node với 100 000 key: `hash % N` đổi **≥ 75 %** key (lý thuyết 80 %), ring 150 vnode đổi **17-23 %** (lý thuyết 20 %); độ lệch tải giữa backend với 150 vnode ≤ ±10 %, với 1 vnode ≥ ±30 % | share 25 ± 3; modulo ≥ 0.75; ring 0.17-0.23 | `go test ./internal/lb -run TestConsistentHash -v` |
| G5 | **Active health:** giết `b3` (đóng listener) giữa bài với `interval=200 ms, fall=3`: proxy ngừng chọn `b3` trong **≤ 600 ms + 1 lần dial lỗi**; trong khoảng đó request rơi vào `b3` được **chọn lại backend khác** (dial lỗi = chưa gửi byte nào) ⇒ client thấy **0** lỗi 502/503; sau khi `b3` sống lại, `rise=2` ⇒ nhận tải lại sau ≤ 400 ms | 0 lỗi client; ngừng chọn ≤ 600 ms | `go test ./internal/proxy -run TestLBDeadBackend -v` |
| G6 | **Phản chứng decay:** `-tags nodefenselb` đổi EWMA sang decay theo **số request**: node bị một mẫu 500 ms rồi hồi phục **không bao giờ** được P2C chọn lại trong 2 s (0/200 lần) — bản thường chọn lại sau ≤ 3·tau (≥ 30/200) | 0/200 đỏ; ≥ 30/200 xanh | `make lblab-nodefense` đỏ; `go test ./internal/lb -run TestP2CRecovers` |
| G7 | **Pool theo host (P5-3):** 1 client keep-alive, 400 request RR qua 4 backend ⇒ `Dials == 4`, `Reuses == 396`, `Idle` tổng == 4. **P5-2:** 5 idle quá tuổi, 1 request ⇒ `Idle == 1` (trước sửa: 4 con chết nằm lại). **P5-5:** upstream idle 100 ms, `MaxIdleTime` 50 ms, 20 request cách 150 ms ⇒ `DeadOnProbe == 0`, `DropExpired == 19`; `MaxIdleTime` 60 s cùng kịch bản ⇒ `DeadOnProbe == 19` | 4/396/4; 1; 0/19 vs 19 | `go test ./internal/proxy -run 'TestLBPoolPerHost\|TestPoolExpiredAtBottom\|TestMaxIdleTimeShorter' -v` |

**Quyết định thiết kế đăng ký trước** (đổi sau phải ghi lý do):

| # | Quyết định | Lý do / hệ quả |
|---|---|---|
| D1 | Package mới `internal/lb`: `Backend{Addr, inflight, ewma, health}`, interface `Picker{Pick(key) *Backend}`; 4 picker: `RoundRobin`, `LeastConn`, `P2C`, `ConsistentHash`. `Balancer` bọc picker + danh sách backend + health, cung cấp `Pick(key)` và `Done(b, latency, failed)` | Thuật toán là hàm thuần trên slice backend "đang dùng được" ⇒ test không cần socket. Proxy chỉ gọi hai hàm |
| D2 | **Inflight tăng ngay khi `Pick`, giảm khi `exchange` trả về** (trước `put`/`discard`) — tức là theo **request**, không theo connection trong pool. Latency báo cho EWMA = từ `Pick` tới khi `exchange` xong (cả body) | P5-3: connection về pool **sau** khi client đã nhận response; đếm theo pool thì inflight thừa đúng lúc request kế đến. Đếm theo request là cái least-conn muốn: "backend đang gánh bao nhiêu request" |
| D3 | **EWMA decay theo thời gian**: `decay = exp(-elapsed/tau)`, `v = v·decay + sample·(1-decay)`, cập nhật khi có mẫu **và** khi đọc điểm (`score()` áp decay từ lần cập nhật cuối, không ghi). Cold-start: chưa mẫu ⇒ điểm **0** (được thử ngay). `tau` mặc định **1 s**; `p2c-slow` trong lblab dùng 30 s | Bẫy ROADMAP: decay theo request ⇒ node điểm xấu không bao giờ được chọn ⇒ không bao giờ có mẫu mới ⇒ không hồi phục. Điểm 0 cho node mới = "lạc quan": giá là node vừa hồi phục bị dồn 2 request cùng lúc trước khi có mẫu — chấp nhận, ghi nếu G3 lộ |
| D4 | P2C: chọn 2 backend **khác nhau** ngẫu nhiên trong tập đang dùng được, điểm = `ewma × (inflight + 1)`, lấy nhỏ hơn; < 2 backend ⇒ trả con còn lại | Công thức Finagle. `+1` để node inflight 0 nhưng ewma cao vẫn có điểm > 0 |
| D5 | Consistent hashing: ring `[]struct{hash uint32; idx int}` sắp xếp, **150 vnode/backend**, hash FNV-1a 64 → 32 bit của `addr + "#" + i`; lookup nhị phân, node không dùng được ⇒ đi tiếp trên ring. Khoá: header `LB.HashHeader` nếu có, không thì IP client **sau** ranh giới tin cậy (phase 4 D9) | `hash % N` di chuyển ~80 % key khi thêm node (G4). Khoá theo XFF chưa tin là để attacker chọn backend |
| D6 | **Active health**: goroutine nền mỗi `Interval` (2 s mặc định) dial TCP + nếu có `Path` thì gửi `GET Path` raw và đòi 2xx-3xx; `fall=3` lỗi liên tiếp ⇒ unhealthy, `rise=2` ⇒ healthy. Ban đầu **healthy** (chưa probe cũng cho dùng). Dừng khi `Server.Close` | Node chết khi **không có traffic** chỉ active bắt được. Không `net/http`: viết request bằng `httpx.Request.WriteHead` + `httpx.ReadResponse` |
| D7 | **Passive outlier ejection** (Envoy `consecutive_5xx`): lỗi transport hoặc status 5xx liên tiếp ≥ 5 ⇒ eject `30 s × 2^(n-1)` (trần 5 phút), 2xx-4xx reset đếm. **Trần eject 50 %** số backend: vượt trần thì **không eject** con tiếp | Bắt "sống nhưng trả 5xx" và bắt nhanh hơn active. Không trần ⇒ upstream chậm đồng loạt (do proxy quá tải) bị eject hết ⇒ 503 toàn tập — tự làm mình chết |
| D8 | `available(b) = healthy(active) ∧ now ≥ ejectedUntil`. Không còn backend nào ⇒ **503** (không 502), giữ client (chưa đụng body ⇒ drain) | 502 = upstream hỏng; 503 = không có ai để hỏi. Khác mã để dashboard phân biệt |
| D9 | **Dial lỗi ⇒ `Done(failed)` rồi `Pick` lại đúng một lần** (backend khác nếu picker cho), bất kể request có body — dial lỗi nghĩa là chưa gửi byte nào. D4 phase 5 (retry sau khi đã ghi) **giữ nguyên trên cùng backend** với `dialNew` | nginx `proxy_next_upstream error`. Không lẫn hai loại retry: một là "chưa gửi", một là "gửi rồi nhưng 0 byte về" |
| D10 | Pool: `Server.pools map[string]*pool` + mutex, tạo lười; `Pool.MaxIdle` là **per host**. `Close` đóng mọi pool. **P5-2:** `put` quét từ đáy bỏ mọi con quá tuổi (dừng ở con đầu còn hạn). **P5-5:** `MaxIdleTime` mặc định **30 s** (nginx upstream `keepalive_timeout` 60 s ⇒ ta đóng trước) | Không đổi `get`/`put`/`discard` ký hiệu ⇒ `poolCheckClean` và test phase 5 giữ nguyên |
| D11 | Phản chứng: file `defense_lb.go` (`!nodefenselb`) `ewmaDecayByTime = true`; tag `nodefenselb` ⇒ decay theo request (`decay` hằng 0.5 mỗi mẫu). `make lblab-nodefense` chạy `TestP2CRecovers` với tag, **phải đỏ** | Đúng khuôn phase 1/3/4/5: phòng tuyến chỉ tin khi đã thấy nó đỏ lúc tắt |
| D12 | `cmd/lblab` in-process: 4 fixture `fixture.Sim` (delay + tỉ lệ 5xx đổi được lúc chạy), proxy `LB.Algo` lần lượt, client raw keep-alive `-conns` goroutine; in **p50/p90/p99/max**, `5xx %`, **share mỗi backend**, ejections, và với `-flap` in p99 **nửa đầu / nửa sau** riêng | Cùng máy, cùng tiến trình ⇒ so **giữa thuật toán** là công bằng; số tuyệt đối không mang sang máy khác |

## Deliverable

- `internal/lb/`: `backend.go`, `rr.go`, `leastconn.go`, `p2c.go` (+`ewma.go`), `chash.go`,
  `health.go`, `outlier.go`, `balancer.go`, `defense_lb.go` (+`_off`), test cho từng thuật toán,
  `TestConsistentHashRehash` (G4), `TestP2CRecovers` (G6), `TestOutlierMaxEject` (D7).
- `internal/proxy`: `pools` theo host (P5-3), `roundTrip` chọn backend + 503 + re-pick khi dial
  lỗi (D8/D9), `LBStats()`; test `TestLBRoundRobin`, `TestLBDeadBackend` (G5), `TestLBPoolPerHost`
  (G7), `TestPoolExpiredAtBottom` (P5-2), `TestMaxIdleTimeShorterThanUpstream` (P5-5).
- `internal/fixture.Sim`: delay + err rate đổi lúc chạy, `ListenAndServeSim`.
- `cmd/lblab`: `make lblab`, `make lblab-skew`, `-flap` → `bench/p6-lblab-*.txt`.
- `make lblab-nodefense` đỏ.
- `cmd/edgegate` + `config/lb.json`: `upstreams`, khối `lb` (`algo`, `hash_header`, `health`, `outlier`).

## Reproduce toàn bộ phase

```bash
git checkout <commit phase 6>
go test ./... -count=1 -race
make lblab                 | tee bench/p6-lblab-even.txt      # 4 backend đều: RR/LC/P2C/chash phải ≈ nhau
make lblab-skew            | tee bench/p6-lblab-skew.txt      # G1, G2, G4
go run ./cmd/lblab -algos leastconn,p2c,p2c-slow -flap -n 20000 | tee bench/p6-lblab-flap.txt   # G3
make lblab-nodefense                                          # PHẢI đỏ (G6)
go test ./internal/lb -run TestConsistentHash -v              # G4 rehash
```

## Nhật ký

### 2026-09-04 19:05-22:13 — Turn 1: trả nợ P5-2/P5-3/P5-5 rồi dựng `internal/lb`

Chi tiết và output thô: [`phase6-log.md` §1](phase6-log.md). Ba chỗ lộ ngay khi test chạy lần đầu,
**trước** khi đo gì:

1. **RR "bỏ qua node unhealthy" cho node kế nhận gấp đôi** (75/75/150). Xoay phải trên tập
   available, không trên danh sách gốc.
2. **Ring 150 vnode lệch tải 3.57x** — FNV-1a không có avalanche, vnode cùng backend dồn cục; 1000
   vnode vẫn 2.85x. Thêm `fmix64` ⇒ 1.17x. Vnode không cứu được hash xấu.
3. **Sổ nợ P5-2 tả sai một nửa:** `get` đã dọn cả stack khi đỉnh quá tuổi; lỗ thật là đỉnh luôn tươi
   còn đáy già mãi. Test dựng lại đúng kịch bản; đỏ trên code cũ (`Idle 5`), xanh sau sửa (`Idle 1`).

Fail-trước cho P5-2/P5-5: `bench/p6-debts-failfirst.txt`. `make lblab-nodefense` đỏ đúng dòng.
`go test ./... -race` xanh (9 test `lb`, 32 test `proxy`).

## Giả thuyết sai

_(turn 2/3)_

## Số đo

_(turn 2)_

## Invariant + lệnh kiểm chứng

_(turn 3)_

## Đọc gì

- ROADMAP phase 6; Finagle `PeakEwma` (Aperture) — công thức decay theo thời gian; Envoy
  `outlier_detection` (`consecutive_5xx`, `base_ejection_time`, `max_ejection_percent`);
  Karger et al. consistent hashing; nginx `upstream` (`max_fails`, `fail_timeout`, `keepalive_timeout`).

## Rút ra

_(turn 3)_

## Nợ kỹ thuật

_(turn 3)_
