# Phase 6 — Load balancing + health (kèm trả nợ phase 5)

- **Thời lượng dự kiến:** 2-3 ngày · **thực tế:** _(turn 3 điền)_
- **Bắt đầu:** 2026-09-04 19:05 · **Kết thúc:** _______
- **Trạng thái:** 🟡 turn 2 xong 2026-09-29 (đo xong, G1-G7 đã chấm: **3 sai + 1 sai một vế**) — còn turn 3 (invariant, rút ra, nợ). Giả thuyết bên dưới đăng ký **trước** file `.go` đầu tiên của phase.
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

Turn 2, commit nền `d21bdb2`, máy im (không session nào khác trong repo; `load` cao nhất là ngay sau
`go test -race`):

```console
$ date; uptime
Tue Sep 29 17:41:32 +07 2026
 17:41:32 up  8:29,  1 user,  load average: 0.60, 1.10, 1.20
$ uptime   # lúc chạy lblab (đầu mỗi file bench/p6-lblab-*.txt có dòng uptime riêng)
 17:43:07 up  8:30,  1 user,  load average: 8.86, 4.12, 2.28     # ngay sau go test -race: bench even
 17:44:35 up  8:32,  1 user,  load average: 3.65, 3.63, 2.28     # flap
 17:52:31 up  8:40,  1 user,  load average: 1.33, 2.34, 2.21     # skew lặp
```

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
make lblab-flap            | tee bench/p6-lblab-flap2.txt     # G3: đọc cột share nửa sau, conns 32 và 4
make lblab-recover         | tee bench/p6-lblab-recover.txt   # chỗ P2C thua: b2 hồi phục, tau 30 s cho 0 %
make lblab-nodefense                                          # PHẢI đỏ (G6)
go test ./internal/lb -run TestConsistentHash -v              # G4 rehash
go test ./internal/proxy -run TestLBKillRevive -count=1 -v    # G5: kill giữa bài / revive
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

### 2026-09-29 17:41-17:58 — Turn 2: đo, chấm G1-G7, sửa bench hai lần

Chi tiết và output thô: [`phase6-log.md` §2](phase6-log.md). Mọi số dưới đây: **closed-loop,
coordinated omission chưa loại trừ; loopback RTT ≈ 0; 4 backend + proxy + client cùng tiến trình,
không ghim core**; service time là giả lập (fixture ngủ) nên RTT không đổi kết luận — không cần `rtt-up`.

**(1) Nền đều** (`bench/p6-lblab-even.txt`, load 8.86 vì vừa chạy `go test -race`):

```console
$ ./bin/lblab -algos rr,leastconn,p2c,chash -n 20000
algo           p50      p90      p99      max   5xx%    rps  share b0/b1/b2/b3 (%)       eject/refused ioErr
rr          4.78ms   7.57ms  11.52ms  62.72ms  0.00%   6284  25.0/25.0/25.0/25.0         0/0 0
leastconn   4.36ms   7.24ms  12.77ms  34.09ms  0.00%   6721  24.9/25.1/24.9/25.0         0/0 0
p2c          3.8ms   6.64ms  10.29ms  22.87ms  0.00%   7440  23.5/24.5/31.7/20.3         0/0 0
chash       3.36ms   5.65ms   8.12ms  13.26ms  0.00%   8487  26.9/26.1/21.7/25.3         0/0 0
```

**Đọc kết quả:** không thuật toán nào hỏng khi tải đều. P2C là con duy nhất **chia lệch** trên 4 node
giống hệt nhau (20.3-31.7 %, max/min 1.56x): điểm = EWMA × (inflight+1), EWMA mỗi node khởi từ
**mẫu đầu tiên** (có dial ≈ 1 ms) và với tau 1 s mỗi mẫu 0.5 ms sau chỉ nặng ~5·10⁻⁴ ⇒ vài trăm ms
đầu xếp hạng theo may rủi mẫu đầu. p50 4.78 ms với service 2 ms: 2.8 ms là tranh CPU của tiến trình chung.

**(2) Lệch tĩnh** — b0 chậm 10x, b1 503 nhanh 30 %. Outlier tắt, 3 lần (`bench/p6-lblab-skew.txt`,
`bench/p6-lblab-skew-rep.txt`), lần 1:

```console
$ ./bin/lblab -algos rr,leastconn,p2c,chash -skew slow=10x,err=30% -n 20000 -outlier=false
algo           p50      p90      p99      max   5xx%    rps  share b0/b1/b2/b3 (%)       eject/refused ioErr
rr          2.94ms  20.76ms  22.01ms  70.34ms  7.88%   4429  25.0/25.0/25.0/25.0         0/0 0
leastconn   2.83ms   4.62ms  21.47ms  26.13ms 11.32%   8735  4.3/38.5/28.6/28.6          0/0 0
p2c         2.64ms   4.18ms   6.44ms  24.44ms 11.33%  11161  0.6/39.1/29.4/31.0          0/0 0
chash       2.88ms  20.68ms  21.61ms  25.37ms  8.26%   4692  23.6/27.3/22.8/26.3         0/0 0
```

**Đọc kết quả:** least-conn đưa b0 đúng 4.3 % như G1 đoán, **mà p99 không nhúc nhích** (22.01 → 21.47).
p99 là latency của 1 % chậm nhất; node 20 ms còn nhận **> 1 %** tải thì 1 % chậm nhất toàn là của nó.
p90 mới thấy least-conn: 20.76 → 4.62 ms = 4.5x. P2C đẩy b0 xuống **0.6 % < 1 %** ⇒ p99 6.44 ms,
RR/P2C = 3.4x. Ngưỡng không phải "giảm tải node chậm" mà là "giảm **dưới 1 − percentile**".
Least-conn dồn b1 (503 nhanh) 38.5 % ⇒ 5xx 11.32 % > RR 7.88 % — câu ROADMAP đúng bằng số.

Outlier bật (mặc định: 5 lỗi liên tiếp, eject 2 s × 2ⁿ⁻¹):

```console
$ ./bin/lblab -algos rr,leastconn,p2c,chash -skew slow=10x,err=30% -n 20000
algo           p50      p90      p99      max   5xx%    rps  share b0/b1/b2/b3 (%)       eject/refused ioErr
rr          2.96ms   20.9ms  22.92ms  62.45ms  4.83%   4075  27.9/16.2/27.9/27.9         1/0 0
leastconn   2.96ms   4.49ms  21.52ms  24.84ms  0.24%   7572  6.7/0.8/46.2/46.2           2/0 0
p2c         3.06ms   4.72ms  20.31ms  26.24ms  0.07%   9163  1.2/0.1/46.6/52.1           2/0 0
chash       3.19ms  21.07ms  22.72ms  37.53ms  0.42%   3659  32.0/1.2/34.6/32.1          2/0 0
$ ./bin/lblab -algos rr,leastconn -skew slow=10x,err=30% -n 100000   # dài 5x để thấy trạng thái bền
rr           3.1ms  20.91ms  21.84ms  69.61ms  1.65%   3783  31.5/5.5/31.5/31.5          4/0 0
leastconn   3.01ms   5.32ms  21.78ms  34.95ms  0.19%   7270  7.0/0.7/46.2/46.2           3/0 0
```

**Đọc kết quả:** G2 vế 2 sai. Outlier "5 lỗi liên tiếp" **bắt được** node lỗi 30 %: b1 còn 0.7-0.8 %.
Tôi tính `0.3⁵ = 0.24 %` rồi đọc là "hiếm" — đó là xác suất **mỗi request**. Kỳ vọng số request tới
chuỗi 5 lỗi đầu tiên là `(1 − p⁵) / ((1 − p)·p⁵) = 586`; least-conn gửi b1 ~3 400 rps ⇒ **0.17 s**. Sau
đó backoff mũ (2 s, 4 s, 8 s) giữ b1 ra gần hết bài: 3 lần eject phủ 14 s trong bài 13.7 s. Hiếm theo
request ≠ hiếm theo thời gian khi rps cao. Mặt trái: eject b1 đẩy P2C chọn cặp trong 3 node ⇒ b0 lên
1.2 % > 1 % ⇒ p99 P2C 6.44 → 20.31 ms. Bỏ một node lỗi làm lộ node chậm.

**(3) Flap b2 nhanh→chậm — bench đo sai lần 1.** Bản đầu chỉ in p99 hai nửa:

```console
$ ./bin/lblab -algos leastconn,p2c,p2c-slow -flap -n 20000        # bench/p6-lblab-flap.txt
algo         p50 nửa đầu    p99 nửa đầu    p50 nửa sau    p99 nửa sau   (b2 đổi nhanh→chậm 10x ở request n/2)
leastconn         3.21ms         7.63ms         2.94ms        21.14ms
p2c               3.12ms         9.65ms         3.69ms        21.47ms
p2c-slow          3.18ms         7.04ms         3.15ms        21.46ms
```

Cả ba ≈ 21 ms — cùng bệnh ở (2): cả ba còn cho b2 > 1 % nửa sau. Sửa `cmd/lblab`: chụp `LBStats()` đúng
lúc flap ⇒ in **share nửa sau** và EWMA b2 lúc flap → cuối; thêm `-conns 4` (inflight ≈ 1/node, ít
thông tin ⇒ EWMA có tiếng nói). 3 lần mỗi mức (`bench/p6-lblab-flap-rep.txt`), lần 1:

```console
$ ./bin/lblab -algos leastconn,p2c,p2c-slow -flap -n 20000 -conns 32
algo       p50 nửa đầu  p99 nửa đầu  p50 nửa sau  p90 nửa sau  p99 nửa sau  share nửa sau b0/b1/b2/b3 EWMA b2 lúc flap→cuối
leastconn       3.45ms       7.51ms       3.03ms       4.64ms      21.52ms  31.8/31.8/4.8/31.7       11.54ms→18.08ms
p2c              3.2ms       7.34ms       3.39ms       5.94ms      21.47ms  32.6/31.1/2.2/34.1       3.06ms→16.48ms
p2c-slow        4.13ms      10.77ms       3.28ms       6.01ms      22.48ms  33.7/31.9/6.1/28.4       3.4ms→4.24ms
$ ./bin/lblab -algos leastconn,p2c,p2c-slow -flap -n 20000 -conns 4
leastconn       2.79ms       4.08ms       2.82ms       3.68ms      21.26ms  31.9/31.8/4.4/31.9       2.8ms→20.73ms
p2c             2.76ms       3.94ms       2.76ms       3.29ms       4.15ms  35.4/30.7/0.3/33.7       2.73ms→10.31ms
p2c-slow        2.76ms       4.04ms       2.78ms       3.42ms      21.07ms  32.0/36.7/4.0/27.2       3.73ms→8.07ms
```

**Đọc kết quả:** G3 sai cả hai vế. p2c-slow (tau 30 s) **không thua** least-conn khi node xấu đi:
share b2 4.0-4.4 % vs 4.3-4.5 % (conns 4), p99 tỉ số 0.99-1.02. EWMA b2 của nó gần như đứng yên (3.73 →
8.07 ms trong khi thật là 20 ms), nhưng điểm = EWMA × **(inflight+1)**: node chậm giữ request lâu ⇒
inflight cao ⇒ điểm lên **ngay**, không chờ tau. P2C tau dài suy biến thành least-conn-theo-cặp, không
tệ hơn. P2C tau 1 s thì **thắng** 5x: b2 0.3 % < 1 % ⇒ p99 4.15 vs 21.26 ms. Least-conn nhận b2 đúng
"một connection" (1/20 ms = 50 rps / 1 100 rps ≈ 4.5 %): cứ b2 rảnh là nó hoà inflight 0 và được chọn.

**(4) Chỗ P2C thua thật — chiều ngược, b2 chậm→nhanh** (`-recover`, thêm cờ; `bench/p6-lblab-recover.txt`, 2 lần):

```console
$ ./bin/lblab -algos leastconn,p2c,p2c-slow -flap -recover -n 20000 -conns 4
algo       p50 nửa đầu  p99 nửa đầu  p50 nửa sau  p90 nửa sau  p99 nửa sau  share nửa sau b0/b1/b2/b3 EWMA b2 lúc flap→cuối   (b2 đổi chậm 10x→nhanh ở request n/2)
leastconn       2.83ms      21.27ms       2.76ms       3.31ms       4.13ms  25.1/25.0/25.0/24.9      20.5ms→2.79ms
p2c             2.81ms       4.42ms       3.33ms       4.79ms       8.26ms  28.8/24.0/17.8/29.4      13.81ms→3.01ms
p2c-slow        2.91ms       4.07ms       2.82ms       3.45ms       4.43ms  33.4/29.3/0.0/37.3       16.81ms→14.22ms
$ ./bin/lblab -algos leastconn,p2c,p2c-slow -flap -recover -n 20000 -conns 32
leastconn       3.11ms      22.44ms       3.27ms       4.75ms       6.14ms  25.0/25.0/24.9/25.0      22.88ms→9.8ms
p2c             3.02ms       8.63ms       2.95ms       4.55ms       5.69ms  33.6/29.6/5.9/30.9       21.06ms→9.77ms
p2c-slow        3.05ms       9.12ms       3.16ms       4.87ms       6.62ms  31.7/33.2/3.1/32.0       20.93ms→20.33ms
```

**Đọc kết quả:** đây là cột "chỗ P2C thua". Least-conn trả b2 về **25.0 %** ngay request đầu sau hồi
phục (inflight không có trí nhớ). p2c-slow cho b2 **0.0 %** suốt ~7 s nửa sau (cả 2 lần, conns 4): khi
b2 không được chọn thì inflight 0, điểm = EWMA cũ × 1, và EWMA chỉ hạ theo `exp(−t/30 s)`. Node khác
điểm ≈ 2.8 ms ⇒ b2 bị bỏ đói tới khi `16.8·e^(−t/30) < 2.8` ⇒ **t ≈ 30·ln 6 ≈ 54 s**. p2c tau 1 s cũng
thua, nhẹ hơn: 16.5-17.8 % (conns 4), 5.9-8.2 % (conns 32). **p99 không lộ cái thua** (4.43 vs 4.13 ms):
3 node còn lại dư sức ở closed-loop nên mất 25 % capacity chỉ là ba node kia gánh thêm 1/3. Cái giá là
**capacity bị bỏ phí ~tau·ln(tỉ số latency)**, thành p99 chỉ khi cụm gần bão hoà — bench closed-loop
không tạo được bão hoà (nợ P6-1).

**(5) G4-G7 bằng test** (`bench/p6-tests-g4-g7.txt`):

```console
$ go test ./internal/lb -run TestConsistentHash -count=1 -v
    lb_test.go:148: 4→5 node, 100000 key: ring 150 vnode đổi chủ 21.6 % (lý thuyết 20), hash%N đổi 80.1 % (lý thuyết 80); tải 4 node [23089 24575 25355 26981] (max/min 1.17)
    lb_test.go:169: 1 vnode: tải [21577 15524 53431 9468] (max/min 5.64)
--- PASS: TestConsistentHashRehash (0.04s)
$ go test ./internal/lb -run TestP2CRecovers -count=1 -v
    lb_test.go:116: A được chọn lại 45/200 lần trong 2 s (tau 100 ms)
--- PASS: TestP2CRecovers (2.12s)
$ make lblab-nodefense
--- FAIL: TestP2CRecovers (2.11s)
    lb_test.go:116: A được chọn lại 0/200 lần trong 2 s (tau 100 ms)
    lb_test.go:118: A không hồi phục: 0/200 — EWMA không decay theo thời gian
$ go test ./internal/proxy -run 'TestLBPoolPerHost|TestPoolExpiredAtBottom|TestMaxIdleTimeShorter' -count=1 -v
--- PASS: TestLBPoolPerHost (0.06s)
--- PASS: TestPoolExpiredAtBottom (0.41s)
    lb_test.go:307: 50 ms: {Dials:20 Reuses:0 Puts:20 ... DropExpired:19 DeadOnProbe:0 Idle:1}
        60 s: {Dials:20 Reuses:0 Puts:20 ... DropExpired:0 DeadOnProbe:19 Idle:1}
--- PASS: TestMaxIdleTimeShorterThanUpstream (5.75s)
```

`TestLBDeadBackend` (turn 1) chỉ phủ backend **chết từ đầu** với interval 50 ms, fall 2 — không phải kịch
bản G5 đăng ký. Viết thêm `TestLBKillRevive`: 3 fixture + b3 sống, client keep-alive ~1 ms/request, giết
b3 **giữa bài** (`http.Server.Close` ⇒ pool đang giữ connection tới nó), interval 200 ms / fall 3 / rise 2,
outlier tắt, rồi mở lại cùng port. 3 lần:

```console
$ go test ./internal/proxy -run TestLBKillRevive -count=1 -v
    lb_test.go:407: kill→unhealthy 503ms (fall 3 × 200 ms); b3 picks lúc kill 54, lúc unhealthy 165 (fails 111), 300 ms sau 165
    lb_test.go:409: revive→healthy 298ms (rise 2), healthy→pick đầu 6ms; client: map[200:990]
    lb_test.go:407: kill→unhealthy 501ms ...; b3 picks lúc kill 53, lúc unhealthy 173 (fails 120), 300 ms sau 173
    lb_test.go:409: revive→healthy 299ms (rise 2), healthy→pick đầu 6ms; client: map[200:980]
    lb_test.go:407: kill→unhealthy 500ms ...; b3 picks lúc kill 54, lúc unhealthy 169 (fails 115), 300 ms sau 169
    lb_test.go:409: revive→healthy 300ms (rise 2), healthy→pick đầu 7ms; client: map[200:994]
```

**Đọc kết quả:** 500 ms chứ không 600: probe đầu tiên sau kill rơi ở pha ngẫu nhiên trong interval ⇒
fall 3 cần `(3−1)·200 + pha` ms. Client 0 lỗi / 2 964 request — D9 (dial lỗi ⇒ Pick lại) và probe MSG_PEEK
phase 5 cùng làm việc. Nhưng "+ 1 lần dial lỗi" là sai: **111-120 dial lỗi** trong cửa sổ 500 ms, mỗi
lần là một RTT dial bỏ đi rồi chọn lại. RR cứ 4 request lại trúng b3 một lần tới khi active health kịp.
Passive outlier (tắt trong test này) sẽ cắt ở 5 lỗi ≈ 20 ms — active và passive không thay nhau được:
active bắt node chết khi **không** có traffic, passive bắt nhanh khi **có**.

**Đang nghĩ gì:** câu hỏi 4 đổi đáp án: P2C tau dài không thua khi node **xấu đi** (inflight che), mà
thua khi node **tốt lên** (không có inflight nào kéo điểm xuống). Bất đối xứng "né nhanh, tin lại chậm" là
cái giá của EWMA dài — turn 3 đọc lại Finagle `PeakEwma` (chưa kiểm tau mặc định của nó, không ghi số
từ trí nhớ) để trả lời vì sao họ vẫn chọn. Cần một bench open-loop gần bão hoà để biến capacity bỏ
phí thành p99 — để nợ.

## Giả thuyết sai

| # | Tôi tưởng là | Thực tế là | Lệnh + output đã lật tẩy | Đã sửa thế nào |
|---|---|---|---|---|
| G1 | Least-conn đẩy b0 xuống 3-8 % ⇒ p99 giảm **≥ 2x** so với RR | Share đúng (4.1-4.3 %), p99 RR/LC = **1.02-1.03x** (22.01/21.47, 21.80/21.18, 21.97/21.43 ms). p99 chỉ giảm khi node chậm nhận **< 1 %**; P2C 0.6 % ⇒ RR/P2C = 3.4-3.7x | `lblab -skew slow=10x,err=30% -outlier=false` ×3 → `bench/p6-lblab-skew*.txt` | Đọc p90 (RR/LC 4.5x) cạnh p99; ghi ngưỡng "share < 1 − percentile" vào Rút ra |
| G2 vế 2 | 30 % lỗi hiếm cho 5 lỗi liên tiếp (0.24 %) ⇒ outlier gần như không bắt, b1 vẫn > 25 % | Bắt trong ~586 request ≈ **0.17 s**; b1 còn 0.7-0.8 %, 5xx 11.3 % → 0.19-0.24 %. Xác suất mỗi request ≠ tần suất theo thời gian | `lblab -skew ...` mặc định; `-n 100000`: LC `7.0/0.7/46.2/46.2`, 3 eject | Không sửa code: ejection đúng. Sửa cách tính (kỳ vọng thời gian chờ chuỗi 5) |
| G3 | P2C tau 1 s ≈ LC (±30 %); P2C tau 30 s thua LC ≥ 1.5x ở nửa sau khi b2 xấu đi | P2C 1 s **thắng** 3.4-5x (b0/b2 < 1 %). P2C 30 s ≈ LC (0.99-1.09x): inflight kéo điểm lên ngay. Thua thật ở chiều **hồi phục**: b2 0.0 % vs LC 25.0 % | `lblab -flap` (p99 ≈ 21 ms cả ba — bench mù) → sửa bench in share nửa sau → `-flap -conns 4` ×3, `-flap -recover` ×2 | Thêm cột share nửa sau + EWMA b2, cờ `-recover`, `make lblab-recover` |
| G5 vế 2 | Ngừng chọn b3 trong ≤ 600 ms "+ 1 lần dial lỗi" | 500-503 ms ✓, client 0 lỗi ✓, nhưng **111-120 dial lỗi** trong cửa sổ đó (mỗi lượt RR tới b3) | `go test ./internal/proxy -run TestLBKillRevive -v` ×3 | Test mới `TestLBKillRevive` (G5 đúng kịch bản). Ghi: cần passive outlier cạnh active |
| bench | p99 hai nửa đủ để thấy thuật toán nào phản ứng chậm | Khi mọi thuật toán còn cho node chậm > 1 % thì p99 = latency node đó cho cả ba | `bench/p6-lblab-flap.txt`: 21.14 / 21.47 / 21.46 ms | Đo **share**, không chỉ p99 — đúng ràng buộc tự viết ở đầu file |
| even | Tải đều thì 4 thuật toán chia ≈ 25 % | P2C 20.3-31.7 % (1.56x): EWMA khởi từ mẫu đầu (có dial) và tau 1 s làm mẫu sau nặng ~5·10⁻⁴ | `bench/p6-lblab-even.txt` | Chưa sửa — nợ P6-2 |

## Số đo

2026-09-29, commit `d21bdb2` + sửa bench turn 2, máy `bench/env-GOTIT-00663.txt` (6 core WSL2),
**closed-loop 32 conn (hoặc 4), coordinated omission chưa loại trừ, loopback RTT ≈ 0, không ghim
core**, service time giả lập 2 ms / 20 ms. Số tuyệt đối không mang đi; tỉ số là kết luận.

**Lệch tĩnh** (b0 chậm 10x, b1 503 nhanh 30 %, 3 lần, outlier tắt — dải min-max):

| Thuật toán | share b0 (chậm) | share b1 (lỗi nhanh) | 5xx client | p90 | p99 | p99 / p99 RR |
|---|---|---|---|---|---|---|
| RR | 25.0 % | 25.0 % | 7.49-7.88 % | 20.76-20.8 ms | 21.80-22.01 ms | 1 |
| least-conn | 4.1-4.3 % | **38.5-39.3 %** | **11.32-11.97 %** | 4.21-4.62 ms | 21.18-21.47 ms | **0.97-0.98** |
| P2C tau 1 s | **0.6 %** | 39.1-47.1 % | 11.33-13.98 % | 3.84-4.31 ms | **5.89-6.48 ms** | **0.27-0.29** |
| chash | 23.6-25.0 % | 25.3-27.3 % | 7.56-8.26 % | 20.68-20.77 ms | 21.61-21.69 ms | 0.98-0.99 |

Outlier bật (1 lần 20k + 1 lần 100k): b1 còn **0.1-1.2 %**, 5xx least-conn 0.19-0.24 %, P2C 0.07 %;
p99 P2C lên 20.31 ms vì b1 bị eject ⇒ b0 lên 1.2 %.

**Chỗ P2C thua** (4 backend 2 ms, b2 đổi 10x ở request n/2; share b2 ở **nửa sau**):

| Kịch bản | conns | least-conn | P2C tau 1 s | P2C tau 30 s | Đọc |
|---|---|---|---|---|---|
| b2 **xấu đi** (flap), 3 lần | 4 | 4.3-4.5 % · p99 21.17-21.29 ms | **0.3 %** · p99 4.15-4.6 ms | 4.0-4.4 % · p99 21.07-21.68 ms | P2C 1 s thắng **4.6-5.1x** p99; tau 30 s = LC |
| b2 xấu đi, 3 lần | 32 | 4.8-6.8 % | 2.2-3.1 % | 5.5-8.2 % | inflight che EWMA cũ |
| b2 **hồi phục** (recover), 2 lần | 4 | **25.0 %** | 16.5-17.8 % | **0.0 %** | tau 30 s bỏ đói b2 ≈ 30·ln 6 ≈ 54 s |
| b2 hồi phục, 2 lần | 32 | 24.9 % | 5.9-8.2 % | 3.1-6.1 % | p99 nửa sau ≈ nhau (5.69-8.95 ms): dư capacity |

**Consistent hash + health + pool** (test, output ở Nhật ký turn 2):

| Đại lượng | Đo | Kỳ vọng |
|---|---|---|
| 4→5 node, 100k key: ring 150 vnode / `hash%N` đổi chủ | **21.6 % / 80.1 %** | 17-23 / ≥ 75 |
| Tải 150 vnode / 1 vnode (max/min) | 1.17x (±7.9 %) / 5.64x | ≤ ±10 % / ≥ ±30 % |
| Kill→unhealthy (200 ms × fall 3) / revive→healthy (rise 2) | **500-503 ms / 298-300 ms** | ≤ 600 / ≤ 400 |
| Dial lỗi trong cửa sổ kill / lỗi client | **111-120** / 0 trên 2 964 | 1 / 0 |
| P2C hồi phục (tau 100 ms, 2 s): thường / `nodefenselb` | 45/200 / **0/200 đỏ** | ≥ 30 / 0 |
| Pool theo host: Dials/Reuses/Idle | 4/396/4 | 4/396/4 |
| `MaxIdleTime` 50 ms vs 60 s: DropExpired / DeadOnProbe | 19/0 vs 0/19 | 19/0 vs 0/19 |

**Chấm G1-G7:** G1 ❌ (share ✓, p99 ✗), G2 ❌ vế 2 (vế 1 ✓), G3 ❌, G4 ✅, G5 ❌ vế 2 (thời gian ✓, số
dial lỗi ✗), G6 ✅, G7 ✅.

## Invariant + lệnh kiểm chứng

_(turn 3)_

## Đọc gì

- ROADMAP phase 6; Finagle `PeakEwma` (Aperture) — công thức decay theo thời gian; Envoy
  `outlier_detection` (`consecutive_5xx`, `base_ejection_time`, `max_ejection_percent`);
  Karger et al. consistent hashing; nginx `upstream` (`max_fails`, `fail_timeout`, `keepalive_timeout`).

## Rút ra

_(turn 3)_

## Nợ kỹ thuật

_(turn 3 chốt và chép vào `docs/debts.md`)_ Phát sinh turn 2:

- [ ] **P6-1** bench open-loop gần bão hoà để biến "capacity bỏ phí" của P2C tau dài thành p99 —
  closed-loop không tạo được. Trả: `lblab -rate` (hàng đợi theo lịch cố định, đo từ giờ hẹn).
- [ ] **P6-2** P2C chia lệch 20.3-31.7 % trên 4 node giống nhau: EWMA khởi từ mẫu đầu (có dial).
  Trả: bỏ mẫu có dial khỏi EWMA hoặc khởi tạo bằng trung vị cụm; đo lại `make lblab`.
