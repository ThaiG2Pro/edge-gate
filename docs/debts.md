# Sổ nợ kỹ thuật

Một món nợ = **một thứ tôi biết là còn thiếu**, kèm **lệnh để trả nó**. Không có lệnh thì
đó chỉ là lo lắng, không phải nợ.

Ba loại, và cách xử lý khác hẳn nhau:

| Loại | Nghĩa | Cách trả |
|---|---|---|
| 🔧 **code** | Sửa được ngay trên máy này, chỉ cần viết test trước | Viết test cho nó **fail**, rồi sửa cho **pass** |
| 📏 **đo** | Cần máy Linux thuần / mạng thật / nhiều máy mới có số đáng tin | Công cụ dựng sẵn, chạy lệnh, dán output vào diary |
| ⏳ **phase sau** | Chưa đủ ngữ cảnh để quyết định | Ghi lại, đừng đoán non |

> Trả nợ 📏: xem [`REPRODUCE-LINUX.md`](./REPRODUCE-LINUX.md) — lệnh, tiêu chí đạt, và hai
> script `linux-baseline.sh` (phase 0–7) + `linux-measure.sh` (P-env-2, phase 8/9/10, cờ mới).

---

## Đang nợ

### 📏 P0-6 · `netem` cho một mạng sạch một cách không thực tế

`netem delay 10ms` trần cho phân phối cực chặt: p50 20.43ms / p99 20.66ms. Không jitter, không
mất gói. Mà **jitter mới là thứ tạo ra tail latency**, và tail latency là toàn bộ lý do phase 6
(P2C+EWMA) và phase 7 (retry, circuit breaker) tồn tại. Đo trên một mạng không jitter thì P2C
sẽ trông vô dụng — và đó sẽ là một kết luận sai giống hệt "pool vô dụng trên loopback".

```bash
sudo tc qdisc add dev lo root netem delay 10ms 3ms distribution normal loss 0.1%
ping -c 20 -i 0.2 127.0.0.1 | tail -2      # phải thấy mdev lớn, và có gói mất
make lblab-skew
sudo tc qdisc del dev lo root
```

### 📏 P0-3 · `-role both`: client và server cùng process, cùng core

Hệ quả cụ thể, không phải lo xa: mọi số `RSS/conn` của phase 0 là của **một cặp** client+server
(19.40 KB), không phải chi phí một connection phía server. Và mọi số latency đều lẫn nhiễu
tranh CPU giữa generator và thứ bị đo.

```bash
# máy A (hoặc terminal A, đã ghim core):
taskset -c 0,1,2 go run ./cmd/netlab -role server -addr :9000 -workers 1 -bufio=false
# máy B (hoặc terminal B):
taskset -c 3,4,5 go run ./cmd/netlab -role client -addr <A>:9000 -all -tag "2-process"
```

Hai máy thật trả `P0-3` **và** `P0-6` trong một lần: RTT thật có jitter và mất gói sẵn, nên
không cần `netem` giả lập. `P0-1` đã trả bằng netem, nhưng netem cho một mạng sạch bất thường —
xem `P0-6`.

### 📏 P0-4 · `expLimits` dial tuần tự nên chưa loại được nghi phạm thứ hai

Đo được `1502 → 455 conn/s` khi `tw` tích luỹ, CPU 29.5%/6 core, 0 lỗi. Kết luận "trần ở không
gian ephemeral port" **chưa loại được** khả năng "trần ở chính vòng lặp một luồng của client".

```bash
# cần thêm -dialers N vào expLimits, rồi so conn/s ở N=1 và N=16.
# Nếu N=16 cũng chặn ở ~cùng conn/s => trần là port. Nếu tăng gần 16x => trần là client.
go run ./cmd/netlab -exp limits -duration 40s -dialers 1  -addr <eth0-ip>:9200
go run ./cmd/netlab -exp limits -duration 40s -dialers 16 -addr <eth0-ip>:9201
```

### 📏 P-env-2 · Generator và proxy dùng chung 6 core — công cụ xong 2026-10-03, số chốt cần Linux thuần

`make pinlab`: upstream core 2, proxy core 0-1 (`GOMAXPROCS=2`), generator core 3-5; `perflab -mode open` nay
nhận `-up`/`-spawn`, in CPU-giây proxy và **hai** kiểm tra generator: (1) rps đạt lệch > 2 % so với rate; (2) **lag**
`Start−Sched` (loadgen ghi mốc worker nhận việc) p99 ≥ ½ p99 latency ⇒ "p99 là của generator". Vi phạm ⇒ in rõ, exit 3,
dừng tiến trình con trước khi thoát (lượt đầu treo vì `os.Exit` bỏ qua defer — con giữ pipe của `grep`).

WSL2 (`bench/p-env-2-pinlab-wsl2.txt`, load 0.6, 3 lượt × 5k/10k rps): rps lệch −0.02…−0.05 % — **tiêu chí (1) đạt
mọi lượt**; nhưng lag p99 **1.2–10.5 ms** ở 5k, **16.6–42.2 ms** ở 10k, ≥ ½ p99 latency ở cả 3 lượt 10k ⇒ số p99 10k
trên WSL2 là của generator, không phải proxy. CPU proxy 69-90 µs/req ổn định hơn latency nhiều. Trên Linux thuần:

```bash
uptime                      # load < 1
make pinlab | tee bench/p-env-2-pinlab-linux.txt     # không dòng GENERATOR nào ⇒ số latency dùng được
```

### 📏 P4-2 · G4 đo trên WSL2 không phân giải được 5 %

ns/op ± 10-37 % (`bench/p4-bench-readrequest.txt`); chỉ allocs/op và CPU share tin được. Trả cùng
P-env-2/P3-5 trên Linux thuần: `taskset`, `-count 20`, benchstat hai commit `b3e08f0` vs `989b060`.

### 📏 P5-1 · G1/G2 đo lúc máy ồn (load 9 trên 6 core)

`bench/p5-poollab-rtt20.txt`: mẫu qua proxy +5-7 ms ngoài mô hình 3 RTT / 2 RTT, mẫu thẳng +0.5 ms.
Nghi 4 lần đánh thức tiến trình/request dưới tranh chấp CPU; chưa chứng minh. Trả cùng P-env-2:

```bash
uptime   # load < 1 rồi mới chạy
taskset -c 0,1 go run ./cmd/poollab -pool both -n 2000 | tee bench/p5-poollab-rtt0-quiet.txt
make poollab-rtt 2>&1 | tee bench/p5-poollab-rtt20-quiet.txt
```

### ⏳ P5-4b · Replay body ≤ 64 KiB cho PUT/DELETE khi connection reused chết

RFC 9110 §9.2.2 cho proxy retry method idempotent ⇒ PUT/DELETE có body replay được nếu buffer. P5-4 đo
0.09-0.13 % 502 với POST (không được replay) trên idle timeout đối nghịch 1-50 ms; PUT có body hiếm hơn
GET/POST. Làm khi có số đo PUT thật. Đăng ký D (đổi I2 có điều kiện) trước khi code. Đo lại trên Linux yên:

```bash
uptime   # load < 1
make poollab-idlerace 2>&1 | tee bench/p5-4-idlerace-linux.txt
```

### 📏 P6-1 · Bench LB closed-loop không biến capacity bỏ phí thành p99 — **trả một phần (phase 7)**

`-flap -recover`: P2C tau 30 s cho b2 hồi phục **0.0 %** tải nửa sau (least-conn 25.0 %), nhưng p99
nửa sau bằng nhau (4.43 vs 4.13 ms) vì 3 node còn lại dư sức và client tự gửi chậm lại. Cần open-loop
ở ~80-90 % capacity để thấy cái giá thành latency:

```bash
# thêm -rate R vào cmd/lblab: lịch gửi cố định, latency tính từ giờ HẸN, không từ lúc gửi
go run ./cmd/lblab -algos leastconn,p2c,p2c-slow -flap -recover -rate 1200 -n 20000 -conns 64
```

Phase 7 dựng xong công cụ: `internal/loadgen` (open-loop, latency từ giờ hẹn, `loadgen.Print`). Còn
lại: nối `cmd/lblab` vào nó và đo lại `-flap -recover` ở ~85 % capacity.

### 📏 P6-2b · Cơ chế chi tiết của lệch P2C lúc khởi động chưa chứng minh

P6-2 sửa được (A/B 0/10 vs 6/10) và lệch chỉ có khi tiến trình lạnh, nhưng mô phỏng sự kiện rời rạc với outlier ngẫu nhiên
độc lập trong 3 mẫu đầu KHÔNG tái hiện (bản cũ 1.09x). Nghi: outlier khởi động **tương quan** (cả loạt request đầu của
một node chậm cùng lúc), không độc lập. Lệnh trả: ghi latency từng request 200 ms đầu theo backend trong lblab, so bản cũ.

```bash
git stash; make lblab-even; git stash pop   # bản cũ: phải thấy FAIL; thêm dump latency 200 ms đầu rồi so
```

### 📏 P6-4 · `consecutive_5xx` chậm ở rps thấp

Kỳ vọng ~586 request tới chuỗi 5 lỗi đầu với lỗi 30 % ⇒ 0.17 s ở 3 400 rps nhưng ~1 phút ở 10 rps.
Đo: `lblab -skew err=30% -conns 1` (≈ vài trăm rps chia 4) với `-n 2000`, đọc share b1 và 5xx; nếu
outlier không kịp, thêm detector `success_rate` (Envoy) — đăng ký D trước.

### 📏 P7-2b · Trần theo IP đổi RAM lấy CPU: đóng ngay ⇒ attacker nối lại liên tục

slowlab 500 conn, `-max-conns-per-ip 100`: proxy giữ 100 thay vì 500, nhưng attacker bị đóng ngay nên nối lại
**828 913** lần / 30 s (không trần: 1 500) ⇒ p99 probe **4.71 → 88.88 ms** (attacker + proxy + probe cùng tiến trình,
không ghim core — một phần là CPU của chính attacker). Hướng: tarpit (giữ connection bị từ chối không đọc, đóng sau
vài trăm ms — tốn fd thay CPU) hoặc trần ở kernel (`iptables connlimit`, SYN). Đo trên Linux thuần, attacker tách máy:

```bash
taskset -c 4-5 go run ./cmd/slowlab -conns 500 -byte-every 10s -max-conns-per-ip 100   # × {đóng ngay, tarpit 200 ms}
```

### 📏 P7-4 · chaoslab thiên về hại ⇒ status mix không có nghĩa

4/6 hành động là hại (kill/slow/err50/hang), 2/6 chữa ⇒ 26-36 % 503 vì không còn backend. Invariant vẫn
đúng, nhưng tỉ lệ status không so được giữa các lần. Thêm `-heal-weight`, đo một lượt cân (chữa ≥ hại),
và một seed giữ backend "treo" ≥ 5 s để chạm deadline client.

```bash
go run ./cmd/chaoslab -duration 60s -rate 500 -heal-weight 3 -seed 3
```

### 📏 P7-5 · Half-open (G5) chỉ đo ở mức `lb`

`TestBreakerHalfOpen` gọi `Pick/Done` trực tiếp. Thiếu bài qua proxy: 32 connection closed-loop, b0 trả
5xx, đếm 5xx client thấy mỗi lần hết hạn eject — đòi 1 (nodefense7: ≈ 8).

### 📏 P7-6 · Retry budget chưa đo ở kịch bản nó có giá trị

`bench/p7-retrylab.txt`: retry luôn thành công (connection mới không bị đóng) ⇒ budget chỉ đổi 25 % lỗi
lấy 0.37x tải. Kịch bản cần đo: backend quá tải thật (`SetConcurrency` thấp + mọi request mới cũng lỗi),
retry mù đẩy tải lên ~2x đúng lúc cụm đang chết; budget phải giữ goodput cao hơn.

```bash
go run ./cmd/chaoslab -scenario overload -duration 20s -rate 2000      # (scenario chưa có)
```

### 📏 P8-1 · CPU handshake (G6) đo trên máy ồn, hai phía cùng tiến trình

`bench/p8-tlslab-handshake-cpu2.txt`: cùng biến thể lệch tới 2x giữa lượt (load 3-10, session khác). Chỉ tỉ số
cùng lượt là chấm được. Cần: máy yên, proxy và client hai tiến trình ghim core riêng để CPU **server** (cái proxy
thật trả tiền) đo riêng — RSA lúc đó phải đắt hơn rõ (+~1.2 ms ký).

```bash
uptime   # < 1
taskset -c 4,5 go run ./cmd/tlslab -mode handshake -n 2000 -kex x25519
```

### 📏 P8-4 · Bộ nhớ connection TLS treo

Phase 7: 20.7 KiB / connection plaintext treo. TLS thêm buffer record (tới 16 KiB) + trạng thái handshake. Thêm
`-tls` vào slowlab (ClientHello nhỏ giọt và connection rỗi sau handshake), hiệu chuẩn `-target null` như phase 7.

### 📖 P8-5 · Ba mục RFC chưa đọc được nguyên văn

RFC 9110 §15.5.20 (421 — hành vi retry của client), RFC 8446 §4.6.1 (thời điểm NewSessionTicket) và §8
(anti-replay 0-RTT): công cụ fetch cắt trang ở turn 3. Đọc và trích vào `diary/phase8.md` Đọc gì.

### ⏳ P9-2 · Body request (upload) không splice

D5 chỉ làm chiều response. Upload lớn CL qua proxy vẫn đi `copyBody` userspace (32 KiB). Cùng điều kiện (CL, hai
phía TCP trần, `br.Buffered()` chép trước). Đáng làm khi có workload upload thật; đo bằng `perflab -mode l4l7`
chiều ngược.

### 📏 P9-4 · Bảng ba cột (G8) đo trên WSL2 ồn

Load 1.5-7.6 suốt turn 2 (tiến trình nền của session khác ăn ~85 % một core); cùng cột lệch 2-3x giữa lượt;
open-loop không đơn điệu theo rate. Kết luận "EdgeGate ≈ nginx, ~50 µs CPU/req" có thể là đặc thù WSL2 (syscall
đắt, đánh thức vCPU đắt — `direct` 1 conn 770 µs/req). Chạy lại trên Linux thuần, `uptime` < 1, và đo **số syscall
mỗi request** của nginx vs EdgeGate (`strace -c -f` hoặc `perf trace -s`) để kiểm giả thuyết "syscall san phẳng".

```bash
./scripts/linux-baseline.sh && make bench-vs-nginx && ./scripts/cpu-vs-nginx.sh
strace -c -f -p $(pgrep -x edgegate) & sleep 5; kill %1    # cần strace
```

### 📏 P9-5 · Vì sao ReverseProxy chậm 3x — mới là tương quan

Đo được: 0.68 context switch tự nguyện / request vs 0.05-0.14 của EdgeGate; Transport đẩy request qua `writech`/
`reqch` sang `writeLoop`/`readLoop` (`net/http/transport.go:1994-1995, 2882-2887`). Chưa chứng minh nhân quả.
Cần `go tool trace` (độ trễ goroutine runnable → running) hoặc block profile của `rpbaseline` dưới cùng tải.

```bash
curl -o rp.trace "http://127.0.0.1:6062/debug/pprof/trace?seconds=3"; go tool trace -pprof=sched rp.trace > sched.prof
```

### 📏 P9-6 · 8 KiB còn lại của connection rỗi chưa chia nhỏ; nginx chưa đối chiếu

Sau D2: 8.0-9.3 KiB/conn. Đoán là stack goroutine (`serveConn` sâu khi xử lý request, chỉ co lại khi GC quét) +
`net.Conn`/`connState`/map — chưa tách bằng số. ROADMAP nói "nginx dùng buffer nhỏ hơn nhiều" — chưa đọc tài liệu
nginx (`client_header_buffer_size`, giải phóng buffer khi keep-alive) và chưa đo RSS/conn của nginx cùng kịch bản.

```bash
./bin/perflab -mode idle -conns 10000 -spawn "bin/edgegate -config config/bench.json -pprof 127.0.0.1:6061" \
  & sleep 25; curl -s 127.0.0.1:6061/debug/pprof/goroutine?debug=1 | head; go tool pprof -top http://127.0.0.1:6061/debug/pprof/heap
```

### 📏 P10-8 · G5/G8 đo trên WSL2 loopback + netem

G5: 640 mẫu mỗi cột, p99 = ~6 mẫu; netem trên `lo` mất gói **cả hai chiều** và cả chặng proxy→upstream. G8: load nền
tới 13 (session khác), closed-loop. Chạy lại trên Linux thuần, hai máy, `tc` chỉ ở chiều client↔proxy:

```bash
./bin/h2lab -mode tcphol -n 100 -par 32      # × {loss 0, 1 %, 2 %, 5 %}
taskset -c 4-5 ./bin/h2lab -mode cpu -rounds 5   # × {8×8 vs 64, 1×1 vs 1}
```

### 📏 P3-5 · G2/G3 dao động ±0.2x giữa hai lần chạy

3.39x / 3.20x và 1.44x / 1.63x cùng máy, 3 tiến trình chia 6 core. Trả cùng P-env-2:

```bash
taskset -c 0,1 ./bin/upstream -addr :8081 & taskset -c 2,3 ./bin/edgegate -config config/dev.json &
for i in 1 2 3 4 5; do taskset -c 4,5 ./bin/proxylab -mode overhead -n 2000 | grep 'G2 p50'; done
```

## Đã trả

### ✅ P6-5 · Passive outlier cắt cửa sổ dial lỗi của active — trả 2026-10-03

Đo được trên WSL2 vì là ĐẾM qua máy trạng thái (số request đập vào backend chết trước khi bị loại), không phải
percentile latency. `TestLBKillRevive` tách hai sub-test chung helper `killReviveScenario`: `active-only` (chỉ active
health, Fall 3×200 ms) vs `outlier-on` (Consecutive 5, BaseEject 150 ms). `-count=5`:
**active-only 116-122 fails, outlier-on 8 fails mọi lượt** (ổn định tuyệt đối) — passive cắt cửa sổ ~15×. Khớp dự
đoán cũ "~5" (8 = 5 ban đầu + ~3 probe half-open qua cửa sổ 600 ms). Client thấy TOÀN 200 cả hai (dial lỗi được D9 né
sang backend khác). `active-only` còn khẳng định unhealthy ⇒ 0 pick thêm; `outlier-on` half-open cho vài probe.

```bash
go test ./internal/proxy -run 'TestLBKillRevive' -count=5 -v
```

### ✅ P4-6 · Oracle thứ hai + thứ ba cho `TestSmugglingOracle` — trả 2026-10-03

`cmd/smuggleoracle` + `make oracle-ext` (docker): phát lại 59 ca request-kind qua TCP trần vào nginx 1.25-alpine và
h2o, ghi status, so ba cột `mình/nginx/h2o` (`bench/p4-6-oracle-ext.txt`). h2o trả 404 cho mọi head hợp lệ (không có
file) ⇒ harness phân loại **nhận khung** (2xx/3xx/404/405/100) vs **từ chối khung** (400/501/431/đóng), tìm hướng
nguy hiểm = mình từ chối mà origin nhận khung.

Ba ca điểm danh trong nợ xác nhận chọn strict là đúng, không phải lập dị:
- **21 (CL trùng lặp giống nhau)**: mình 400, **nginx cũng 400**, h2o 404 — ít nhất một oracle độc lập cùng từ chối.
- **30/31 (bare LF)**: mình 400, **nginx 200 + h2o nhận** — proxy strict đứng trước hai backend lenient là đúng kịch
  bản smuggling (RFC 9112 §2.2 "MAY recognize"); nếu ta cũng lenient thì bare LF thành ranh giới lệch.
- **50 (`Connection: Host`)**: mình 400, **nginx 200 + h2o nhận** — D7 (xoá token Connection điều khiển Host) chặn
  đúng thứ hai backend bỏ lọt.

46/118 cặp (ca × origin) origin lenient hơn — gồm cả ca chính sách (host/absolute-form/đếm header, ca 60-71) lẫn ca
khung thật (bare LF, chunk-size, trailer, Connection-token). Mọi ca "ok" của mình đều được ≥ 1 origin nhận (không có
hướng nguy hiểm ngược: mình nhận mà cả hai origin từ chối). Không ca nào origin từ chối-khung mà mình nhận.

```bash
make oracle-ext   # cần docker + image nginx:1.25-alpine, lkwg82/h2o-http2-server
```

### ✅ P9-7 · `SpliceBody` tắt mặc định — trả 2026-10-02

D11 phase 9. Điều kiện đủ: P9-1 trả; `go run ./cmd/chaoslab -duration 60s -rate 500 -tick 300ms -body 262144`
(cờ `-body` mới: mỗi request thứ 4 là `/large` 256 KiB đi splice) **PASS**, `bench/p9-7-chaoslab-body.txt`. Lượt
đầu đỏ fd 23/7: 16 fd là 8 pipe của Go splice pool (finalizer) — chaoslab nay GC trước khi đếm, in riêng `pipe:`,
về 7/7. Lật: `Config.NoSplice` / `"no_splice_body"` / `-nosplice`; perflab `edgegate` trần truyền `-nosplice`.
`perflab-nodefense` (nodefense9) vẫn đỏ đúng hai test.

### ✅ P8-3 · Health check active với upstream TLS — trả 2026-10-02

D13 phase 8: `lb.HealthConfig.Dial` hook; proxy đặt `healthDial` (= `dialUpstreamTimeout`, cùng SNI/RootCAs/session
cache với data path) khi `UpstreamTLS` bật, cho lb chính và vhost. `TestHealthTLSUpstream` (httptest TLS + Path
`/hello`, Interval 30 ms, Fall 2): đỏ trên HEAD (unhealthy sau 300 ms), xanh sau sửa, GET qua proxy 3/3 200.

### ✅ P7-3 · Trần eject 50 % + half-open — trả 2026-10-02

D9 phase 7: đăng ký "node half-open thử hỏng dưới trần ⇒ ở lại half-open, một request thử mỗi vòng" là thiết kế.
`TestBreakerUnderEjectCap` (lb): 3 vòng thử hỏng ⇒ b0 nhận đúng 1/32 mỗi vòng, `EjectRefused 3 / Reopens 3 /
Ejections 1`, state `half-open`; thử tốt ⇒ closed ⇒ 16/32. Không đổi code.

### ✅ P3-2 · 101 Switching Protocols ⇒ 502 — trả 2026-10-02

D13 phase 3. `roundTrip` đặt lại `Connection: Upgrade` + `Upgrade` cho upstream khi GET không body; `exchange` gặp 101
mà ta đã chuyển Upgrade ⇒ `tunnel()` (101 về client, chép hai chiều qua `br`/`ubr`, deadline rỗi mỗi chiều, đóng cả hai
khi một bên xong, connection upstream discard). Fixture `/ws`: `Upgrade: echo` ⇒ 101 + echo. `TestUpgradeTunnel`
(byte đầu gửi liền sau head vẫn tới; 3 vòng echo; đóng client ⇒ `DropDirty ≥ 1`, `Idle 0`), `TestUpgradeNotRequested`
(POST + Upgrade ⇒ tước ⇒ upstream 426; upstream tự ý 101 ⇒ 502). `ResilienceStats.Tunnels` đếm. h2 vẫn 502.

### ✅ P10-6 · h2spec 3.5/2 fail vì h1 + h2c chung port — trả 2026-10-02

D12 phase 10: `Config.H2COnly` / `"h2c_only"` — listener plaintext bỏ sniff preface, mọi connection vào `serveH2`;
preface sai ⇒ đóng (h2.Conn), không 400. `TestH2COnlyRefusesH1` (h1 GET ⇒ 0 byte + EOF; h2c client vẫn 200).
`make h2spec H2CFG=config/h2-only.json`: **145/145**; `make h2spec` mặc định (chung port) vẫn 144/145 — 3.5/2 là
đánh đổi cố ý của D7, nay có công tắc. Qua TLS (P10-5) cũng 145/145.

### ✅ P10-5 · ALPN `h2` trên TLS — trả 2026-10-02

D11 phase 10. `Config.H2ALPN` / `"h2_alpn"` ⇒ `ServerConfig(h2)`: ALPN `["h2","http/1.1"]`, cipher TLS 1.2 chỉ ECDHE+AEAD
(RFC 9113 §9.2.2). `NegotiatedProtocol=="h2"` ⇒ `serveH2` không sniff preface. `TestTLSALPNH2` (net/http h2 qua TLS
SNI a.test: 3 GET HTTP/2.0 200 A, 3 stream; client chỉ http/1.1 vẫn h1), `TestTLSH2CipherBlocklist` (TLS 1.2 +
ECDHE-ECDSA-AES128-CBC-SHA + h2 ⇒ `handshake failure`; h2 tắt ⇒ cùng cipher h1 bắt tay được). `make h2spec-tls`:
**145 tests, 145 passed** — 3.5/2 (P10-6) xanh qua TLS vì không còn mơ hồ chung port.

### ✅ P4-3 · `X-Forwarded-Proto/Host/Port` chưa sinh, `X-Real-IP` từ peer tin chưa kiểm — trả 2026-10-02

Đăng ký D12 (diary phase 4) rồi code. `forwardedHeaders(h, c, st)` nay sinh Proto (`https` khi `st.tls`),
Host (client gửi), Port (listener). Peer không tin ⇒ thay cả ba; peer tin ⇒ giữ nếu hợp lệ. `X-Real-IP` của peer
tin không `ParseIP` được ⇒ := clientIP. **clientIP = phần tử phải nhất của XFF không trong `trusted_proxies`**
(trước: phần tử đầu — client bịa được qua proxy tin). `Forwarded` RFC 7239 quyết định **không sinh** (hai nguồn
sự thật). `TestForwardedHeadersUnit` (6 nhánh), `TestForwardedProtoHostPortE2E` (plaintext, peer không tin bịa
`https`/`evil`/`443` ⇒ 0 lọt), `TestForwardedProtoTLS` (qua TLS SNI `a.test` ⇒ `https`). `TestRateLimitPerIP`
nhánh tin vẫn 10/20 — khoá không đổi khi chuỗi XFF một phần tử.

### ✅ P2-5 · `FuzzReadRequest` đỏ 1/3 lượt: 164 912 byte cho input ~6.2 KB — trả 2026-10-02

Không phải `sync.Pool`. Corpus `testdata/fuzz/FuzzReadRequest/637b585bcb169468`: request-line `1 1 HTTP/1` +
~6 K byte `\xa9`. `badRequest("phiên bản không hợp lệ: %q", b)` chép **cả trường** vào `Reason`: `%q` nở
`\xa9` → `\xa9` (4 byte/byte) cộng tăng trưởng buffer của `fmt` ⇒ 151-196 KB cho 7 KB input, và chuỗi đó đi
vào log. Fuzz chỉ đỏ 1/3 vì bộ đếm tầng 1 (`runtime/metrics`) đếm thiếu ~4x so với `ReadMemStats`.

`TestErrorAllocBound` viết trước, đỏ trên HEAD 4/5 ca (`Reason 28 040 byte`, `151 584 byte cho input 7 016`,
trần 44 448); ca `status` xanh sẵn vì code chỉ 3 byte. Sửa: `clip()` trong `errors.go` — mọi `%q` nhận byte của
peer (version, method, tên header, token `Connection:`, status code) chỉ mang 32 byte đầu + `…(+N byte)`.
Sau sửa: fuzz 3/3 lượt 30 s xanh; `TestReadRequestAllocs` vẫn 24.

### ✅ P5-4 · Body vào connection chết giữa probe và `Write` ⇒ 502 — trả 2026-10-02

`cmd/poollab -idlerace` (`make poollab-idlerace`): upstream raw đóng rỗi sau 1-50 ms ngẫu nhiên mỗi response,
32 worker POST 1 KiB nghỉ 0-60 ms, 10k request. `bench/p5-4-idlerace.txt` (WSL2, load 5.45), 3 lượt:
**probe on 13 / 13 / 9 ⇒ 0.09-0.13 %** (≈ 3 % số lần upstream đóng rỗi lọt cửa sổ probe→Write);
probe off 3.6-3.7 %. Vượt ngưỡng 0.1 % đăng ký, nhưng replay POST bị cấm: RFC 9110 §9.2.2 (đọc nguyên văn)
"A proxy MUST NOT automatically retry non-idempotent requests." ⇒ 502 là câu trả lời đúng cho POST;
client (biết ngữ nghĩa) tự quyết. Replay cho PUT/DELETE có body tách thành P5-4b.

Lỗ lộ ra khi đọc §9.2.2: **D4 retry cả POST không body** (chỉ xét body, không xét method).
`TestIdleClosedUpstream/noprobe-POST-nobody-502` viết trước, đỏ: `Retries:50`, muốn 0. Nay `canRetry`
(h1) và `h2exchange` đòi `idempotent(method)` = GET/HEAD/OPTIONS/TRACE/PUT/DELETE (§9.2.1 + §9.2.2).
D9 không đổi: dial lỗi ⇒ chưa byte nào đi, không phải retry.

### ✅ P4-4 · Tag `nodefense` chung cho phase 1/3/4 — trả 2026-10-02

Mỗi phase một tag: `nodefense1` (frame), `nodefense3` (proxy D-drain + raw copy), `nodefense4` (httpx 7 phòng
tuyến). `nodefense` trơn vẫn tắt cả ba (`//go:build nodefense || nodefenseN`) để lệnh cũ trong diary còn chạy.
`make smugglelab-nodefense` nay `-tags nodefense4`: e2e lật **21 → 20** ca — `95-resp-ok-eof` xanh (nó đỏ vì
bẫy #2 phase 3, không vì phase 4). `framelab-nodefense` (2 test), `proxylab-nodefense` (2 test),
`smugglelab-nodefense` vẫn đỏ đúng chỗ.

### ✅ P4-5 · Ca còn thiếu trong `testdata/smuggle` — trả 2026-10-02

Năm ca mới, `expect` ghi trước khi chạy: `33-request-line-trailing-sp` (400), `48-chunk-ext-too-long` (body-431),
`53-header-count-bomb` (431, 101 dòng), `87-ok-get-cl-body` (ok: GET đọc theo CL, `hello` không thành request kế),
`88-ok-expect-continue` (ok ở parser). Hai lỗ lộ ra:

- **48 qua proxy trả 400 thay vì 431**: lỗi body request nào cũng thành 400. Nay `*httpx.ProtoError` giữ status
  của parser (`forward.go`, nhánh `readErr`).
- **`Expect: 100-continue` treo**: proxy forward head nhưng D7 (phase 3) nuốt 1xx ⇒ client chờ 100 đến timeout
  của chính nó, proxy chờ body. RFC 9110 §10.1.1 (đọc nguyên văn): proxy PHẢI trả status final ngay hoặc forward
  head; chỉ được TỰ sinh 100 khi tin server kế là HTTP/1.0. Nay `awaitContinue`: flush head, chờ upstream ≤ 1 s
  (`expectWait`): 100 ⇒ chuyển cho client rồi gửi body; status final ⇒ trả luôn, không gửi body, đóng cả hai
  phía (body client còn trên br, upstream nhận head có CL mà thiếu body); im lặng ⇒ cứ chuyển body. Client đã
  gửi body sẵn (`br.Buffered() > 0`) ⇒ không chờ. `TestExpectContinue` (3 ca) viết trước: 2 ca đỏ trên HEAD
  (`i/o timeout` sau 500 ms), ca im lặng xanh cả hai. Không đụng h2 (client h2 gửi body không chờ).

### ✅ P4-1 · Duyệt `Connection:` cấp phát — trả 2026-10-02

`parse.go:hasConnectionToken` và `header.go:StripHopByHop` duyệt `Connection:` bằng `strings.Cut` (bản cũ
`bytes.Split([]byte(v))` / `strings.Split` — cấp phát bản sao + slice). `TestReadRequestAllocs` (`AllocsPerRun`, trần 24)
viết trước, đỏ `26 alloc > 24`; sau 24. `BenchmarkReadRequest` ×6: **26 → 24 allocs/op, 864 → 824 B/op**, ns/op
1590-3630 → 1145-2158 (máy ồn, không chốt ns). Ba lần duyệt vẫn còn nhưng không lần nào cấp phát — gộp làm một chỉ
tiết kiệm ns, không đổi đại lượng món nợ đăng ký. Suite httpx/proxy `-race` + `make smugglelab` xanh.

### ✅ P7-2 · Trần connection theo IP — trả 2026-10-02

`Config.MaxConnsPerIP` (khoá = IP peer; `proxy.go:track` đếm dưới `mu`, `untrack` trả — I7, xoá key về 0); vượt ⇒
đóng ngay sau Accept, trước goroutine/bufio; `ResilienceStats.PerIPRejected`; `edgegate` `max_conns_per_ip`, `slowlab
-max-conns-per-ip`. `TestMaxConnsPerIP` (×3 `-race`): 5 conn rỗi từ 127.0.0.2 với trần 3 ⇒ 2 bị đóng ngay (EOF
< 300 ms ≪ HeaderTimeout), 127.0.0.1 vẫn 200, đóng một conn ⇒ slot về. slowlab 500 conn (`bench/p7-2-slowlab-perip100.txt`):
proxy giữ **100** (phase 7: 500), probe 100 % cả baseline lẫn lúc tấn công. Lệnh ghi trong sổ (`-max-conns-per-ip 32`)
chọn sai ngưỡng: probe có 64 worker keep-alive cùng một IP ⇒ baseline chỉ 51.6 % 200 (`bench/p7-2-slowlab-perip.txt`) —
trần đếm mọi connection phải lớn hơn độ đồng thời hợp lệ của một IP. Cái giá mới đo được ⇒ P7-2b.

### ✅ P6-2 · P2C chia lệch trên 4 node giống hệt — trả 2026-10-02

Hai thay đổi, chốt bằng A/B trên lblab thật (không bằng mô phỏng):
1. `lb/ewma.go:observe` — trong tau đầu kể từ mẫu đầu, EWMA = **trung bình cộng** mọi mẫu (bản cũ: `v = mẫu đầu`, mẫu sau
   nặng ~dt/tau ≈ 4·10⁻⁴ ở 2.5k rps/node ⇒ mẫu đầu thống trị cả giây). Sau tau: EWMA theo thời gian như cũ.
2. `forward.go`/`h2.go` — latency cho EWMA tính từ khi có connection (không tính dial); dial lỗi vẫn `Done(start, true)`.

`make lblab-even` (mới): 10 lượt `lblab -algos p2c,rr,leastconn -max-share-ratio 1.2`, **p2c chạy đầu tiên** — chỉ lúc
tiến trình còn lạnh mới có outlier khởi động (`max` 37-94 ms); bản đầu của target chạy p2c sau rr/leastconn ⇒ tiến
trình đã ấm ⇒ ngay bản cũ cũng xanh (bẫy); bản thứ hai đếm mã thoát của `grep` thay vì lblab (in FAIL mà vẫn "0/10").
A/B cùng lệnh, liền nhau, load 2.4-3.2: **mới 0/10, HEAD 6/10** (1.53-2.00x). Riêng (2): 4/10 vẫn lệch — dial không
phải nguyên nhân chính. Mô phỏng sự kiện rời rạc (32 client, outlier ngẫu nhiên trong 3 mẫu đầu) KHÔNG tái hiện lệch
kể cả trên bản cũ (tệ nhất 1.09) ⇒ xoá, không giữ test chưa từng đỏ; cơ chế chi tiết ⇒ P6-2b.

### ✅ P6-3 · Node chưa có mẫu được điểm 0 khi đang có request — trả 2026-10-02

`lb/picker.go:Backend.score`: node chưa có mẫu (`ewma == 0`) mà đang có inflight ⇒ `noSamplePenalty + inflight` (Finagle
PeakEwma "Penalty + pending"); lượt thử đầu (inflight 0) vẫn được ngay. Test viết trước, đỏ trên code cũ:
`TestP2CNoSampleNotFlooded` (4 node ấm EWMA 1 ms + 1 node mới, 32 lượt chọn chưa Done) ⇒ `node chưa có mẫu nhận 10/32`.
Sau: ≤ 1 trong 20/20 lượt; `TestP2CColdStartSpreads` (cold-start mọi node) và `TestP2CRecovers` (46/200) vẫn xanh;
`-tags nodefenselb` đỏ đúng `TestP2CRecovers` như HEAD.

### ✅ P10-7 · Trailer h2 bị bỏ — trả 2026-10-02

Cả hai chiều qua downgrade h2 → h1 → h2. Response: `h2.Stream.WriteTrailers` (HEADERS cuối END_STREAM, RFC 9113 §8.1)
khi `resp.Trailer()` có field; không có ⇒ DATA rỗng END_STREAM như cũ. Request: `endHeaderBlock` giữ trailer trên stream
(`Stream.Trailer()` sau io.EOF); `h2copyRequestBody` (body không CL ⇒ chunked) ghi nó thành trailer chunked, bỏ field
cấm (`httpx.ForbiddenTrailer`, RFC 9110 §6.5.1). Còn lại có chủ ý: request h2 có `content-length` ⇒ chặng h1 dùng CL,
không chở được trailer ⇒ bỏ; `te: trailers` không truyền sang upstream (upstream h1 vẫn được gửi trailer, client vẫn
nhận được). Test viết trước, đỏ trên HEAD (worktree): `TestH2Trailers` `trailer map[Grpc-Status:[] X-Echo-Sig:[]]`.
Sau (×3 `-race`): client net/http h2c nhận `Grpc-Status: 0`, `X-Echo-Sig: s1` (trailer request `X-Sig` đi tới upstream
và quay về). h2spec vẫn 144/145.

### ✅ P3-1 · Trailer chunked bị bỏ (h1, cả hai chiều) — trả 2026-10-02

`httpx/chunked.go:CloseWithTrailer` (chunk cuối + trailer qua `appendWire` — CR/LF bị từ chối); `forward.go:copyBodyT`
gọi `resp.Trailer()` / `req.Trailer()` SAU khi body EOF và ghi kèm chunk cuối, cả hai chiều. Đọc RFC 9110 nguyên văn
(tải 2026-10-02): §7.6.1 **không** liệt kê `Trailer` là hop-by-hop (chỉ Proxy-Connection, Keep-Alive, TE,
Transfer-Encoding, Upgrade + Connection) — comment phase 2 trong `httpx/header.go` trích sai ⇒ bỏ `Trailer` khỏi
`hopByHop`, sửa comment và `TestStripHopByHop`; §6.6.2: `Trailer` là gợi ý cho bên nhận cuối. Test viết trước, đỏ trên
HEAD (worktree): `TestTrailerForwarded` `trailer map[]`, `TestRequestTrailerForwarded` `upstream thấy trailer ""`. Sau
(×3 `-race`): `X-Checksum: abc123` tới client net/http; `X-Sig: s1` tới upstream; trailer cấm `Content-Length` ⇒ 400.

### ✅ P9-1 · Splice body: lỗi ghi client bị tính là lỗi upstream — trả 2026-10-02

`splice.go:spliceClientFault` phân loại sau một ReadFrom thiếu byte: `err == nil` ⇒ upstream EOF sớm; `EPIPE` (chỉ
phát sinh khi ghi) ⇒ client; còn lại ⇒ dò hai socket bằng `pool_linux.go:peerGone` (MSG_PEEK; khác `probeIdle`: byte
đang chờ = còn sống) — upstream chết ⇒ upstream, không mà client chết ⇒ client, không rõ ⇒ upstream như cũ. Client lỗi
trả về `werr` ⇒ không nuôi outlier, connection upstream vẫn bị bỏ (chưa đọc hết body). Test viết trước, đỏ trên code
cũ: `TestSpliceClientGone` (body 16 MiB, client đọc 100 KB rồi đóng ⇒ RST) `fails=1` ×3. Sau ×5 `-race`: `fails=0`;
đối chứng `TestSpliceBodyShortUpstream` thêm assert `fails=1` (upstream đóng giữa body) xanh.

### ✅ P7-1 · D9 chọn lại chính backend vừa dial lỗi — trả 2026-10-02

`lb/balancer.go:PickExcept(key, ex)` — picker nhận bản sao danh sách với `ex` thay bằng chỗ giữ `unavailable`
(healthy=false; giữ vị trí ⇒ chash đi tiếp trên ring); `forward.go:roundTrip` dùng nó cho lượt chọn lại D9, không còn
ai khác ⇒ 502 ngay. Test viết trước, đỏ trên code cũ: `TestDeadline/dial-502` siết ≤ DialTimeout + 150 ms ⇒ `502 sau
602ms`; `TestRepickExcludesFailedBackend` (2 backend, một không route, least-conn, outlier tắt) ⇒ `200:17 502:3`. Sau
(×3 `-race`): `502 sau 301-303ms`; `200:20` với 8-11 lượt bốc trúng backend xấu. `TestPickExcept`: 4 thuật toán × 300
lượt không trả `ex`; chash giữ đúng một backend thay thế cho cùng key; một backend ⇒ nil.

### ✅ P-ops-1 · `make proxybench` để sót tiến trình — trả 2026-10-02

`proxybench` viết lại thành một recipe shell: `trap 'kill $PX $UP; wait' EXIT` dọn mọi tiến trình nền trên MỌI đường
ra; `go build` mỗi dòng một lệnh; biến `PROXYLAB` để giả lập bước lỗi. Kiểm 2026-10-02: `make proxybench
PROXYLAB=false` ⇒ `Error 1`, `pgrep -x upstream/edgegate` = 0/0; chạy thật ⇒ đủ bảng G2-G4, sót 0/0. `make h2spec`
(cùng khuôn rủi ro, h2spec thoát 1 vì ca 3.5/2) dùng cùng trap ⇒ sót `epolllab/edgegate` 0/0.

### ✅ P3-3 · Test upstream chết giữa body response — trả 2026-10-02

`debts_test.go:TestUpstreamDiesMidBody`: upstream gửi `Content-Length: 100` + 50 byte rồi đóng ⇒ client nhận đúng 50
byte + `io.ErrUnexpectedEOF`, sau đó EOF sạch (không byte nào thêm), backend `fails=1` (×3 `-race`). Xanh ngay lần đầu
— bất biến đã đúng theo code — nên kiểm bằng **đột biến**: thêm `writeError(502)` sau lỗi body trong
`forward.go:exchange` ⇒ test đỏ `body: 100 byte, err <nil>` (50 byte body + 50 byte đầu của trang 502 dính vào body —
đúng "hai response cho một request"); trả file về `git checkout`.

### ✅ P3-4 · G6 chạy đủ 1 000 request — trả 2026-10-02

`TestNoGoroutineLeak` chạy đúng số G6 đăng ký: 1 000 request, chia ba h1 keep-alive / h1 mỗi request một connection /
h2c (phase 10: goroutine đọc + goroutine stream cũng phải về, I8). `-race -count=3`: goroutine trước 5, sau 6
(≤ before+2) sau 20 ms. **Sửa cùng ngày (lúc trả P7-2):** đỏ 1/3 lượt full suite `trước 5, sau 8` —
in stack: 3 goroutine thừa là `net/http.(*conn).serve` của FIXTURE (connection rỗi trong pool proxy, đúng thiết kế;
trộn h2 ⇒ pool giữ 2-3 conn). Test giờ trừ goroutine phía upstream (đếm từ profile) và in stack khi đỏ; 0/20 đỏ; đột
biến (rò một goroutine mỗi connection proxy) ⇒ đỏ `trước 5, sau 341`. Commit P7-2 `4a4327d` đi qua dù suite đỏ vì
chuỗi lệnh nối bằng `;` — từ đó commit chỉ sau `go test … &&`.

### ✅ P10-9 · `TestIdleClosedUpstream` chập chờn dưới tải — trả 2026-10-02

Tái hiện trước khi sửa: chạy subtest probe `-race -count=30` trong khi 5 package khác chạy test `-race` song song + 6 ×
`yes` (load 7.25) ⇒ **1/30 đỏ** (`probe-POST-body`: `46/50 … DropDirty:4 DeadOnProbe:42`); chỉ 6 × `yes` thì 0/20 —
tải phải giống full suite. Nguyên nhân: test ngủ cố định 20 ms chờ FIN, nhưng dưới tải goroutine upstream có thể chưa
chạy tới `Close`. Sửa: `idleClosingUpstream` báo tín hiệu SAU `Close`; `waitUpstreamClosed` chờ tín hiệu (trần 2 s) +
2 ms cho FIN qua loopback; nhánh noprobe-502 chỉ chờ sau request upstream thực sự phục vụ. Sau, cùng bài tải: **0/30**
và **0/60** (load 7.06), full suite `-race` 3/3 sạch; test 3.9 s → 0.5 s (bỏ 50 × 20 ms).

### ✅ P10-4 · PING / SETTINGS / request malformed khuếch đại chi phí server — trả 2026-10-02

Đo trước (`h2lab -mode flood`, `bench/p10-flood-before.txt`): client gom 100 frame mỗi lần ghi ⇒ server **100 001
Flush** cho 100 000 PING (khuếch đại syscall 100x), 5.75 µs/frame; SETTINGS 2 730 × INITIAL_WINDOW_SIZE với 100 stream
mở ⇒ **1.8-2.8 ms CPU mỗi frame 16 KiB** dưới `c.mu` (O(setting × stream)). Sửa cấu trúc, không ngưỡng, không chặn
nhầm client hợp lệ: (a) `conn.go:writeCtl` — phản hồi của goroutine đọc chỉ Flush khi buffer đọc không còn TRỌN một
frame (`frameBuffered`; Peek chỉ khi đủ 9 byte — Peek thiếu byte sẽ chặn đọc socket), `flushPending` trước khi có thể
chặn đọc; (b) `onSettings` — nhiều INITIAL_WINDOW_SIZE trong một frame áp một lần (chênh lệch cuối; kiểm tràn bằng
giá trị lớn nhất). Sau (`bench/p10-flood-after.txt`, xen kẽ với nodefense10 ×2): PING 100 000 → **111 Flush**,
0.65 µs/frame; SETTINGS **51-106 µs/frame** (≈ 45x); malformed 90 Flush, 1.84-1.90 µs/frame. Phản chứng
(`-tags nodefense10` ĐỎ): `TestControlFloodCoalesced` 10 000 vs 11-12 Flush, `TestSettingsCollapse` 2 436 731 vs
≤ 990 lần cộng window (+ ngữ nghĩa: window = giá trị cuối 65 536). h2spec vẫn 144/145 (`bench/p10-h2spec-4.txt`).
Không thêm trần tốc độ: sau (a)(b) chi phí server mỗi frame ≈ chi phí client gửi nó.

### ✅ P10-3 · WINDOW_UPDATE nhỏ giọt — đóng 2026-10-02 bằng số đo: KHÔNG phải lỗ (tiền đề của món nợ sai)

Món nợ ghi "1 MiB = 1 048 576 frame + 1 048 576 syscall" — ước lượng, chưa từng đo. Đo
(`TestTinyWindowUpdates`, viết dạng "phải GOAWAY" trước, chạy trên code cũ): 20 000 WINDOW_UPDATE 1 byte ⇒ **878-1 208**
DATA frame (client flush mỗi 1/10/100 update; dưới `-race` 2 822-3 350), không phải 20 000. Lý do: ghi đồng bộ dưới
`wmu` (D3) và `WriteData` lấy **toàn bộ** credit đang dồn mỗi frame ⇒ trong lúc ghi một frame, client gửi thêm nhiều
update và chúng gộp vào frame sau ⇒ số DATA frame ≤ số WINDOW_UPDATE (+ phần cắt theo MAX_FRAME_SIZE): **không khuếch
đại**, chi phí client ≥ chi phí server. Kịch bản RFC 9113 §10.5 nhắm cài đặt *xếp hàng* frame. Không thêm phòng tuyến
(ngưỡng "chờ ≥ 1 KiB" deadlock client window nhỏ hợp lệ; đếm-rồi-GOAWAY chặn nhầm nó). Test giữ lại làm chốt bất
biến tất định "frame ≤ update + 64". Còn lại, không phải nợ: client đi từng bước (1 update, chờ DATA) tốn 1 RTT mỗi
byte — tự giới hạn.

### ✅ P10-2 · Rate limit / shed không áp cho stream h2 — trả 2026-10-02 (sau phase 10 turn 3)

Đường vòng: `serveH2Stream` không gọi `admit` ⇒ client nói h2c trên cùng port thoát token bucket lẫn trần inflight.
Sửa: tách lõi `resilience.go:admitDecision` (trả 0/429/503 + release, không ghi gì); `admit` (h1) ghi qua bufio,
`h2.go:serveH2Stream` ghi HEADERS/DATA kèm `retry-after`, gọi sau head, trước `Pick`, `defer release()` (I7). Test
viết trước, đỏ trên code cũ: `TestH2RateLimit` `h2: map[200:6]`, h1 sau đó vẫn 200 (`RateLimited:0`); `TestH2Shed`
`/hello` 200 khi `/slow` giữ slot duy nhất. Sau sửa (×3 `-race`): `map[200:2 429:4]`, Retry-After 4/4, h1 sau đó 429
(bucket chung, `RateLimited:5`); `/hello` 503, `Inflight:0`. `-tags nodefense7` đỏ đúng 3 test như trước
(`TestDeadline`, `TestRateLimitPerIP`, `TestRetryBudget`).

### ✅ P10-1 · Drain không biết connection h2 — trả 2026-10-02 (phase 10 turn 3)

Connection h2 không bao giờ "idle" theo nghĩa h1 ⇒ `Drain` chờ hết timeout rồi đóng cưỡng bức, cắt stream đang chạy.
Sửa: `h2.Conn.Shutdown` — GOAWAY NO_ERROR với last-stream-id hiện tại, không nhận stream mới, stream cuối xong ⇒
đóng; `resilience.go:Drain` gọi nó qua `connState.h2`; `serveH2` tự Shutdown nếu Store sau lần quét (Dekker như
idle/closeIdle). `TestH2Drain` viết trước, đỏ trên code cũ (`Drain 3.001s, forced=2`), sau sửa `Drain 202ms,
forced=0`, stream `/slow` đang chạy trả `200` (×3 `-race`).

### ✅ P9-3 · `copyBody` cấp 32 KiB cho body request rỗng — trả 2026-10-01 (phase 9 turn 3)

`forward.go:exchange` gọi `copyBody(ubw, req.Body, …)` cả khi GET không body ⇒ mỗi request 2 buffer 32 KiB
(nguồn của "68 KiB, không phải 33" ở G1). Sửa: chỉ gọi khi `ContentLength != 0 || Chunked`. `bench/p9-p93.txt`:
bản `nodefense9` 68 129 → **35 361 B/op**, 47 → 46 allocs; suite `-race` xanh (`bench/p9-invariants.txt`).

### ✅ P8-2 · SNI lạ ⇒ alert `internal_error` — trả 2026-10-01 (phase 8 turn 3)

`crypto/tls` (`handshake_server_tls13.go:pickCertificate`) chỉ gửi `unrecognized_name(112)` cho sentinel nội bộ
`errNoCertificates`; lỗi từ `GetCertificate` ⇒ `internal_error`. `tlsx/store.go:ServerConfig` thêm
`GetConfigForClient` trả config không cert cho SNI lạ ⇒ 112 đúng RFC 6066 §3. `TestTLSSNIRouting` đòi chuỗi
"unrecognized name": đỏ trước sửa, xanh sau.

### ✅ P5-2 · Con quá `MaxIdleTime` ở đáy stack — trả 2026-09-04 (phase 6 turn 1)

Sổ nợ tả sai một nửa: `get` dọn cả stack khi **đỉnh** quá tuổi; lỗ thật là đỉnh luôn tươi (một client
keep-alive) còn đáy già mãi. `pool.put` giờ quét từ đáy bỏ mọi con quá tuổi. `TestPoolExpiredAtBottom`:
đỏ trên code cũ `Idle 5, DropExpired 0` (`bench/p6-debts-failfirst.txt`), xanh `Idle 1, DropExpired 4`.

### ✅ P5-3 · Pool theo host; inflight theo request — trả 2026-09-04 (phase 6 turn 1)

`Server.pools map[addr]*pool` (`proxy.go:poolFor`), `MaxIdle` per host; inflight tăng ở `lb.Pick`, giảm ở
`lb.Done` sau `exchange` (sau cả `put` — D2 ghi "trước", sai vài µs, diary phase 6 câu 2).
`TestLBPoolPerHost`: Dials/Reuses/Idle 4/396/4, `Inflight 0` cả 4 backend.

### ✅ P5-5 · `MaxIdleTime` phải ngắn hơn upstream — trả 2026-09-04 (phase 6 turn 1)

Mặc định 60 → 30 s. `TestMaxIdleTimeShorterThanUpstream`: upstream idle 100 ms; pool 50 ms ⇒
`DropExpired 19, DeadOnProbe 0`; pool 60 s ⇒ `DropExpired 0, DeadOnProbe 19`.

### ✅ P2-3 · CL+TE ⇒ từ chối — trả 2026-09-04 (phase 4 D1)

`body.go:framing`: có TE mà cũng có CL ⇒ `ErrAmbiguousFraming` (400), không bỏ CL nữa. Phản chứng
`rejectCLWithTE=false` (nodefense) ưu tiên CL ⇒ ca 01/02/03/10/12/90 đỏ (`bench/p4-smugglelab-nodefense.txt`).
Test cũ `TestReadRequestBodyFraming/"CL+TE…"` đổi kỳ vọng. Diff-fuzz 7.75 M exec sau đổi: 0 lệch.

### ✅ P-arch-1 · `Host` giữ hay đổi — trả 2026-09-04 (phase 4 D4/D5/D6/D7)

**Giữ nguyên** (nginx `$host`). Điều kiện để "giữ" an toàn: Host chỉ có một nguồn — absolute-form
⇒ authority thắng và viết về origin-form, lệch ⇒ 400 (`normalizeTarget`); `Connection: Host` ⇒ 400
(`checkConnectionTokens`); CONNECT ⇒ 501; cú pháp `validHost`. Ngoại lệ sinh Host: client HTTP/1.0
không gửi (từ phase 3). Bằng chứng `TestAbsoluteFormRewritten`, ca 60-71, 83-86.

### ✅ P2-2 · 204/304 kèm TE: strip hay giữ? — trả 2026-09-04 (phase 3)

Đóng **bằng cấu trúc**: `StripHopByHop` xoá TE ở chiều response, proxy chỉ đặt lại TE khi chính
nó chunked; với NoBody (HEAD/1xx/204/304) mode là "không body" nên TE không bao giờ được đặt lại.
Không cần `if` riêng. Bằng chứng `TestHEADAnd204HaveNoBody` (`go test ./internal/proxy -run
TestHEADAnd204HaveNoBody -v`).

### ✅ P2-4 · `FuzzReadRequest` chậm 2x vì `ReadMemStats` — trả 2026-09-03

Đo trước khi sửa (không profile được với `-fuzz`, dùng benchmark tạm): thân fuzz **9.5 µs**/input,
một `runtime.ReadMemStats` **80 µs** (stop-the-world), `runtime/metrics.Read` **0.5 µs**. Hai lần
ReadMemStats đắt gấp ~17 lần việc thật. Giả thuyết lần này **đúng** — nhưng chỉ biết sau khi đo.
Sửa: kiểm hai tầng — tầng 1 `metrics.Read`, vượt trần mới xác nhận bằng **min của 3** lần
ReadMemStats. Lý do cần tầng 2: cả hai bộ đếm đều toàn tiến trình; lần chạy đầu bắt được dương
tính giả 506 KB cho input 44 byte (goroutine của fuzz engine cấp phát trong cửa sổ đo), không tái
hiện. Kết quả: **14 717 → 32 828 execs/s** (`bench/p2-fuzz-readrequest-metrics-60s.txt`, 1 874 459
execs, 0 đỏ giả). Phản chứng trần = 1 byte ⇒ đỏ 11 024 B/52 B (`bench/p2-fuzz-allocbound-counterproof.txt`).

### ✅ P0-5 · `cmd/netlab` không có test — trả gián tiếp ở phase 1 (P1-1), đóng 2026-09-03

Món nợ là `readFrame` riêng không được fuzz. P1-1 đã xoá nó: `cmd/netlab/wire.go` dùng
`frame.Decoder`, package `internal/frame` có `FuzzDecode`. `go test ./cmd/netlab` vẫn
`[no test files]` — đúng, vì netlab giờ chỉ còn logic thí nghiệm, không còn logic giao thức.

### ✅ P0-7 · Số tuyệt đối phase 0 là một lần chạy — trả 2026-09-03

5 lần `netlab -exp rtt -n 500 -bufio=false` liên tiếp (`bench/p0-7-rtt-5runs.txt`):

| run | p50 reuse | p50 dial | tỉ số |
|---|---|---|---|
| 1 | 8.5 µs | 154.4 µs | 18.24x |
| 2 | 7.8 µs | 141.5 µs | 18.05x |
| 3 | 7.5 µs | 125.6 µs | 16.65x |
| 4 | 8.1 µs | 127.7 µs | 15.77x |
| 5 | 7.6 µs | **138.8 µs** (median) | 18.23x |

Phase 0 ghi dial p50 **861.7 µs** (n=2000, tw đầy) và **341 µs** (n=100). Hôm nay median 138.8 µs
⇒ số tuyệt đối lệch **2.5–6x** giữa các phiên. Tỉ số cũng **không** bất biến: 36.69x → ~17x.
Kết luận sửa lại: chỉ có **thứ tự độ lớn** của tỉ số (>10x) và **chiều** của nó mang được sang
phiên khác; con số của tỉ số thì không. Mọi ô "tỉ số" ở phase 0 phải đọc là "cỡ chục lần".

### ✅ P0-2 · Capacity của `expOmission` được tính, chưa được đo — trả 2026-09-03

`netlab -exp omission -rate 100000 -svc 1ms -workers 1` (`bench/p0-2-capacity.txt`): closed-loop
1 conn đạt **737 rps**, p50 **1.34 ms** ⇒ `time.Sleep(1ms)` thật sự ngủ ~1.3 ms. Khớp 754 của
phase 0. Mốc "quá tải 1.2x" đúng là **~885 rps**; `make netlab-omission` cũ dùng 1200 = **1.63x**.
Đã đổi Makefile về 885.

**Bug lộ ra khi trả:** ở rate 100000, generator open-loop **panic** `slice bounds out of range`
trong `bufio.Reader.Read`. Nguyên nhân: `sem` đếm slot rỗi nhưng conn chọn theo `i % len(pool)`,
hai goroutine dùng chung một `clientConn`. Ở rate 1200 của phase 0 nó va khi hàng đợi > 427 ms
(512 conn / 1200 rps) — tức là **có va** trong lần đo G4 gốc (p99 open-loop hàng trăm ms). Sửa:
free-list `chan *clientConn`. `go run -race` 5 s: 0 DATA RACE (`bench/p0-2-race.txt`). Số G4
mới: p99 open/closed-1 = 377x (phase 0 ghi một số khác, xem errata phase0.md).

### ✅ P2-1 · Diff-fuzz không tới được lệch chỉ lộ ở oracle — trả 2026-09-03

Giả thuyết "fuzzer chỉ instrument package mình" **sai**: `rtk proxy go test -a -n -fuzz ...` cho
thấy 180/207 package compile với `-d=libfuzzer`, gồm `net/http`, `bufio`, `net/textproto`; 27
package không instrument là runtime/testing/fuzz engine. Nguyên nhân thật: lệch differential là
**quan hệ giữa hai đường đã được cover** (trimOWS của mình + nhánh lỗi chunk của Go), không tạo
edge mới ⇒ không có tín hiệu coverage; fuzz phẳng chỉ tới đó bằng may mắn.
Trả bằng: (b) `FuzzChunkLineAgainstNetHTTP` — head cố định, đột biến riêng dòng chunk-size — tìm
ra `" 3"` trên bản lenient trong **0.10s** (`bench/p2-structfuzz-lenient-60s.txt`), strict 120s sạch
(`bench/p2-structfuzz-strict-120s.txt`), `make difffuzz-chunk`; (c) 17 hàng bảng đối chiếu tay
thành seed `internal/httpx/testdata/fuzz/FuzzAgainstNetHTTP/hand-*` (32 seed chạy trong `go test`).

### ✅ P-code-1 · `internal/httpx` có test — trả ở phase 2 turn 1-2

`header_test.go` (canonical, `TE`→`Te`, `StripHopByHop` kể cả field liệt kê trong `Connection`,
`Write` sắp xếp + chặn CR/LF injection), `limits.go` được dùng thật bởi parser với 3 trần kiểm
trong lúc đọc (`TestHeaderBomb`: 431 sau 4096 byte), `errors.go` mở rộng (`Is`, lỗi lớp).
Bằng chứng: `go test ./internal/httpx -count=20 -race` ok (`bench/p2-race20.txt`).

| ID | Trả ở phase | Bằng lệnh nào |
|---|---|---|
| **P2-3** | 4 | `ErrAmbiguousFraming` khi CL+TE; nodefense ưu tiên CL ⇒ 6 ca đỏ; diff-fuzz 300 s 0 lệch |
| **P-arch-1** | 4 | Host giữ nguyên; `normalizeTarget` + `checkConnectionTokens` bảo đảm một nguồn authority; `TestAbsoluteFormRewritten` |
| **P2-2** | 3 | `TestHEADAnd204HaveNoBody`: HEAD/204 qua proxy không mang `Transfer-Encoding`; hệ quả của `StripHopByHop` + chỉ đặt lại TE khi proxy tự chunked |
| **P1-6** | 1 | `cmd/needzerolab` tái hiện **xác định**: heap sạch `make(4 GiB)` = 7ms / RSS 7 MB; sau **64 MB rác bẩn + GC** = **7.138s / RSS 4.27 GB**; sau `FreeOSMemory` = 3ms. Cơ chế: span đè lên trang free-còn-bẩn ⇒ runtime zero CẢ span ⇒ ~1M page fault (sys 7.7s, user 0.3s). Không liên quan `-race` (không race cũng 7.73s, 2/8). Dự đoán "lần chậm RSS 4 GB" trúng 8/8. Fix test: `FreeOSMemory()` trước `make` ⇒ 8/8 nhanh; gỡ skip-dưới-race; `make test` -race 2.35s ×3 |
| **P1-3** | 1 | `TestPayloadOverUint32` skip khi int 32-bit, khi `vm.overcommit_memory=2`, ~~và dưới `-race`~~ (skip race đã gỡ khi P1-6 chỉ ra `-race` không liên quan); `make test-huge` chạy riêng |
| **P1-4** | 1 | `TestTransportDifference`: net.Pipe vs TCP trên 4 kịch bản ⇒ **G6 sai một nửa**. Pipe tái tạo được short read (7/7) và nhiều-frame-một-Read (62/62); không tái tạo được Write bất đồng bộ (`tcp=true pipe=false`) và gom Write rời (pipe luôn 1; tcp 1 hoặc 11 — không xác định) |
| **P1-5** | 1 | `framelab -rawbuf 7` / `-rawbuf 12` (`make framelab-split`): Read thô cắt frame 1 giữa header / giữa payload, decoder vẫn 3 frame ✔; `nhỏ nhất 3` và `nhỏ nhất 2` byte/Read là phần đuôi ReadFull phải vá — `bench/p1-framelab-split-GOTIT-00663.txt` |
| **P1-1** | 1 | `cmd/netlab/wire.go` chuyển sang `frame.Decoder`/`frame.PutHeader`; `grep -n "func readFrame" cmd/netlab/*.go` → 0. G1 chạy lại: **1.42x** p50 (trước 1.41x), spike Nagle **44.00ms** (trước 44.03ms) — `bench/p1-netlab-on-frame-GOTIT-00663.txt`. Tỉ số sống sót qua việc đổi framer |
| **P1-2** | 1 | `TestDecodeHangsWithoutDeadline`: không deadline ⇒ Decode **không trả về sau 300ms**; `SetReadDeadline(50ms)` ⇒ trả `i/o timeout` sau 352ms tổng. Decoder cố ý không tự đặt deadline — giờ là quyết định có test ghi lại, phase 7 cài ở tầng connection |
| **P-env-1** | 0 | Hai phần. (a) `netlab -exp limits` qua `127.0.0.1` vs qua eth0 IP ⇒ `tcp_tw_reuse=2` che port exhaustion. (b) `scripts/pay-P0-1.sh` ⇒ **netem trên `lo` áp delay cho CẢ HAI chiều**: `delay 10ms` → RTT 20.157ms, `delay 20ms` → RTT 40.201ms. Đã sửa `Makefile` `rtt-up` thành `delay = RTT/2` + ép `ping` kiểm |
| **P0-1** | 0 | `scripts/pay-P0-1.sh` ⇒ G3 = **2.00x** (không phải >15x), và đúng 2.00x ở cả hai RTT ⇒ tỉ số là **đơn vị sai** cho pool; đơn vị đúng là **1 RTT phí mỗi connection dựng mới**. Dự đoán đăng ký trước khi đo: đúng |
