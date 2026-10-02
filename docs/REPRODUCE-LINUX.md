# Tái hiện số đo thật trên Linux thuần

23 món nợ `📏` còn lại không trả được trên WSL2: loopback là network stack ảo hoá, ns/op dao
động ±10–37 %, dial p50 đổi 2.5× sau 30 phút (P0-7). Chúng cần **một máy Linux thuần, ≥ 4 core,
`uptime` load < 1**. Một số cần thêm `sudo` (netem) hoặc `docker` (nginx/h2o).

Tài liệu này + hai script dưới là tất cả những gì cần. Mỗi món ghi rõ **lệnh**, **nhìn gì**, và
**tiêu chí đạt** (số nào mới đóng được nợ).

## 0. Chuẩn bị (một lần)

```bash
# Go 1.26+, docker (tuỳ chọn), taskset (util-linux), strace (tuỳ chọn, cho P9-4)
go version
sudo tc qdisc show dev lo          # kiểm netem dùng được (P0-6, P10-8)
docker pull nginx:1.25-alpine      # P4-6, P9-4
docker pull lkwg82/h2o-http2-server # P4-6
uptime                             # PHẢI < 1 trước mọi phép đo latency
uname -r | grep -qi microsoft && echo "ĐANG Ở WSL2 — số không chốt được"
```

Quy ước ghim core (script tự tính theo `nproc`): **upstream** = core cao nhất, **proxy** =
`0-1` (GOMAXPROCS=2), **generator** = các core giữa. Tách ba nhóm để số latency phản ánh proxy,
không phản ánh tranh chấp CPU (P-env-2).

## 1. Chạy nhanh: hai script

```bash
# A. Timing phase 0–7 (đã có sẵn): syscall, rtt, pool, lblab, slowlab, retry, chaos…
REPEAT=5 ./scripts/linux-baseline.sh
RTT=1 ./scripts/linux-baseline.sh            # thêm vòng netem RTT 20 ms (cần sudo)

# B. Nợ còn lại: P-env-2, phase 8/9/10, và cờ mới (dialers, heal-weight, overload)
./scripts/linux-measure.sh                    # phần không cần sudo/docker
SUDO=1 DOCKER=1 ETH0=<ip-eth0> ./scripts/linux-measure.sh   # đầy đủ
ONLY=penv2,p7,p8 ./scripts/linux-measure.sh   # chỉ vài phần
```

Kết quả vào `bench/baseline/<host>-<ngày>/` và `bench/linux/<host>-<ngày>/`, mỗi phép đo một
`.txt` (`REPEAT` lần — lấy **median**, bỏ lần đầu nếu lệch vì JIT/cache). So **tỉ số** với số
WSL2 trong `bench/*.txt`; số tuyệt đối giữa hai máy không so trực tiếp.

## 2. Bảng nợ → lệnh → tiêu chí

Phần (`ONLY=`) trong `linux-measure.sh` trừ khi ghi `baseline` (ở `linux-baseline.sh`) hoặc `tay`.

| Nợ | Phần | Cần | Nhìn gì / Tiêu chí đạt |
|---|---|---|---|
| **P-env-2** generator chung core | `penv2` | — | `make pinlab`: KHÔNG dòng `GENERATOR KHÔNG THEO NỔI` ⇒ latency dùng được. WSL2 lag p99 16–42 ms ở 10k ⇒ đỏ |
| **P0-3** client+server cùng core | `baseline` p0 | 2 máy/2 terminal | netlab 2-process vs 1-process: p50 KHÁC ⇒ số 1-process có lẫn tranh chấp |
| **P0-4** trần port hay client | `p0` | `ETH0=` | netlab `-dialers 1` vs `16`: N=16 ≈ N=1 conn/s ⇒ trần **port**; tăng ~16× ⇒ trần **client** tuần tự |
| **P0-6** netem có jitter+loss | `p0` | `SUDO=1` | `ping` mdev LỚN + có gói mất; `lblab-skew`: P2C vẫn hơn RR ở tail (không phải mạng phẳng giả) |
| **P3-5** G2/G3 ổn định | `p3` | — | `proxylab -mode overhead` ×5: p50 dao động < ±0.1× ⇒ số G2/G3 chốt được |
| **P4-2** G4 không phân giải 5 % | `baseline` p4 | benchstat | `benchstat` 10 lần: CI hẹp lại < 5 % ⇒ phân giải được overhead phase 4 |
| **P4-6** oracle nginx+h2o | `p4` | `DOCKER=1` | ca 21 nginx cũng 400; bare LF (30/31) + `Connection: Host` (50) cả hai origin NHẬN ⇒ strict đúng. ✅ đã chạy một lần, đây là tái hiện |
| **P5-1** pool máy yên | `p5` | — | `poollab -pool both`: pool giảm p50 (loopback ít, netem 20 ms nhiều); so tỉ số với `bench/p5-*.txt` |
| **P5-4b** replay PUT/DELETE | `p5` | — | `poollab -idlerace`: ghi % 502 của PUT/DELETE thật. Cần số này TRƯỚC khi code replay (⏳, đăng ký D) |
| **P6-1** LB closed-loop che p99 | `tay` | — | cần cờ `-rate` open-loop trong lblab (CHƯA có — xem §3) |
| **P6-2b** lệch P2C khởi động | `p6` | — | `lblab -max-share-ratio 1.2`: p2c max/min ≤ 1.2× trên backend giống hệt |
| **P6-4** consecutive_5xx chậm rps thấp | `tay` | — | `lblab -skew err=…` ở rps thấp: đo bao lâu mới eject; so với rps cao |
| **P6-5** outlier cắt dial lỗi active | `p6` | — | `go test -run TestLBKillRevive -count=3`: xanh 3/3 ⇒ cửa sổ dial lỗi không bị cắt oan |
| **P7-2b** trần IP đóng ngay | `p7` | — | `slowlab -max-conns-per-ip 100`: ghi reconnect/s attacker; so RAM tiết kiệm vs CPU reconnect |
| **P7-4** chaoslab thiên về hại | `p7` | — | `chaoslab -heal-weight 6`: tỉ lệ 200 KHÔNG còn nghiêng 5xx ⇒ status mix đọc được |
| **P7-5** half-open G5 chỉ ở lb | `tay` | `SUDO=1` | đo half-open end-to-end qua proxy dưới netem, không chỉ unit `lb` |
| **P7-6** retry budget có giá trị | `p7` | — | `chaoslab -scenario overload -rate 2000` budget-on vs `-tags nodefense7`: budget-on goodput CAO hơn, khuếch đại THẤP hơn |
| **P8-1** CPU handshake máy yên | `p8` | — | `tlslab -mode handshake`: ms/handshake ổn định; ECDSA vs RSA vs kex |
| **P8-4** RAM connection TLS treo | `tay` | — | `perflab -mode idle` với TLS listener + pprof heap: byte/conn TLS không tăng vô hạn |
| **P9-4** bảng ba cột G8 | `p9` | `DOCKER=1` | `bench-vs-nginx.sh`: EdgeGate ≈ nginx; ReverseProxy ~3× chậm — số chốt |
| **P9-5** vì sao ReverseProxy 3× | `tay` | pprof trace | `go tool trace` ReverseProxy vs EdgeGate: tìm NGUYÊN NHÂN (sched/alloc), không chỉ tương quan |
| **P9-6** 8 KiB conn rỗi + nginx | `p9` | — | pprof heap `-inuse_space`: 8 KiB/conn còn lại ở đâu; goroutine không rò |
| **P10-8** G5/G8 netem | `p10` | `SUDO=1` | `h2lab -mode tcphol` loss 0/1/2/5 %: h2 (một TCP) tụt so h1 khi loss tăng; `-mode cpu` 8×8 vs 64 |
| **P3-5 / P4-6** docker | — | `DOCKER=1` | xem trên |

## 3. Món cần thêm cờ tooling trước (chưa dựng)

Ba món tham chiếu cờ chưa viết — là **thiết kế phép đo**, nên để chủ máy quyết sau khi có máy thật:

- **P6-1** `-rate R` open-loop trong `cmd/lblab` (latency từ giờ HẸN, loại coordinated omission).
  Hiện lblab closed-loop. `cmd/chaoslab` đã open-loop (dùng `internal/loadgen`) — mẫu để bê sang.
- **P6-4** đo thời gian-tới-eject ở rps thấp: thêm mốc thời gian vào log eject của lblab.
- **P7-5 / P8-4 / P9-5**: đo end-to-end / pprof, lệnh ở cột "tay" bảng trên.

Cờ ĐÃ dựng sẵn và chạy được ngay: `-dialers` (P0-4), `-heal-weight` (P7-4), `-scenario overload`
(P7-6), `-max-conns-per-ip` (P7-2b), `-max-share-ratio` (P6-2b).

## 4. Đọc kết quả

1. Mở `bench/linux/<host>-<ngày>/env.txt` — xác nhận KHÔNG phải WSL2, load < 1, đủ core.
2. Mỗi `.txt`: lấy median `REPEAT` lần. Lần đầu lệch (JIT/cache) thì bỏ.
3. So **tỉ số** với số WSL2 cùng tên trong `bench/`. Ví dụ P5-1: pool giảm p50 bao nhiêu lần.
4. Đạt tiêu chí cột phải bảng §2 ⇒ cập nhật `docs/debts.md` (📏 → ✅) và diary phase tương ứng,
   dán số vào, ghi tên máy + kernel + ngày.

Số không đạt cũng là kết quả: ghi lại, nói RÕ máy/điều kiện, đừng ép thành "đạt".
