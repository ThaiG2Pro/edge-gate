# Phase 3 — Vertical slice: `curl` xuyên proxy tới upstream và về

- **Thời lượng dự kiến:** 1-2 ngày · **thực tế:** _______
- **Bắt đầu:** 2026-09-03 22:30 · **Kết thúc:** _______
- **Trạng thái:** 🔨 turn 2 (run+fix+measure) xong 23:55 — G1 G2 G3 G5 G6 đúng, **G4 sai** (no-op flag rồi sàn 44 ms, không phải +40 ms). `make proxylab` sửa mồ côi. 14 test xanh `-race`. Turn 3: diary (invariant, rút ra, nợ). Giả thuyết bên dưới đăng ký **trước** file `.go` đầu tiên của phase.
- **Commit:** `_______` (điền khi chốt phase; commit nền là `ba4ed2f`)

> **Đường đi thô, kể cả ngõ cụt:** [`phase3-log.md`](phase3-log.md). File này là bản biên tập.
>
> **Quy tắc ghi nhật ký:** mọi con số, mọi kết luận đều phải kèm **lệnh shell sinh ra nó**
> và **output thật**. Luật đầy đủ: [`../skills/diary/SKILL.md`](../skills/diary/SKILL.md).

## Ràng buộc kế thừa

- **Tuyệt đối không dùng `net/http` trên data path.** `cmd/upstream` là fixture, được phép dùng
  `net/http` vì nó **là backend**, không phải proxy. `internal/proxy` và `cmd/edgegate` chỉ `net`.
- **I3 (mọi connection có deadline)** giờ là việc của phase này: parser nhận `*bufio.Reader`,
  còn `net.Conn` nằm ở đây. Mỗi lần đọc head/body ở **cả hai** connection (client và upstream)
  đều phải có `SetReadDeadline` trước.
- **`SetNoDelay(true)` tường minh** trên mọi socket (phase 0 G3: 44 ms là hằng số, không phải đuôi).
- **Bẫy #3 của ROADMAP:** body của request N phải đọc hết từ cùng `bufio.Reader` trước khi
  `ReadRequest` lần N+1. Trên connection client phase này là bug; phase 5 nó thành lỗ hổng.

## Môi trường

Cùng máy phase 0-2 (`bench/env-GOTIT-00663.txt`). Phase này có số **thời gian** (latency qua
proxy), nên ba bẫy đo mạng áp dụng đầy đủ: mọi số latency ghi rõ closed/open-loop, RTT (loopback
trần trừ khi ghi khác), và generator chạy chung 6 core với proxy (P-env-2 chưa trả).

```console
$ uname -srmo && go version && nproc && ulimit -n
Linux 6.6.87.2-microsoft-standard-WSL2 x86_64 GNU/Linux
go version go1.26.2 linux/amd64
6
1048576
```

## Mục tiêu phase

`make proxylab`: `curl` GET, POST có body, và response **chunked** đi xuyên `cmd/edgegate` tới
`cmd/upstream` và về đúng byte, **không treo**, giữ được keep-alive phía client qua nhiều
request trên một connection. Một backend hardcode trong `config/dev.json`. Xấu nhưng chạy.

## Câu hỏi phải trả lời được (viết trước khi code)

1. Vì sao `io.Copy(upstream, clientConn)` treo, và **treo bao lâu** khi có I3? Chỗ nào trong
   code là "một dòng" quyết định không treo?
2. Response từ upstream không có CL lẫn TE (body tới EOF): proxy trả cho client thế nào để
   client **biết body kết thúc ở đâu** mà không phải đóng connection? HTTP/1.0 client thì sao?
3. Upstream chết **sau khi** proxy đã đọc một phần body request: connection client còn dùng lại
   được không? Điều kiện chính xác là gì?
4. Lối tắt `Connection: close` sang upstream mua được gì, và **trả giá bao nhiêu RTT/request**
   (số này là mốc để phase 5 so)?
5. Header nào **không** được forward, và tại sao `StripHopByHop` xoá `Transfer-Encoding` rồi
   proxy vẫn phải đặt lại nó?

## Giả thuyết đăng ký trước (viết 22:30, chưa có file `.go` nào của phase)

| # | Giả thuyết | Tỉ số / giá trị kỳ vọng | Lệnh sẽ dùng để chấm |
|---|---|---|---|
| G1 | Thay đường body-response bằng `io.Copy(client, upstreamConn)` thô (bẫy #2) khi upstream **giữ keep-alive** ⇒ treo, nhưng treo **đúng bằng** `UpstreamBodyTimeout` chứ không vô hạn, vì I3 | thời gian treo = timeout ± 50 ms | test có `-tags trap2` hoặc flag, đo `time` |
| G2 | Overhead L7 của vertical slice: p50 qua proxy / p50 gọi thẳng upstream, loopback, closed-loop 1 conn | **3-5x** (1 hop parse + Dial mới mỗi request; dial ≈ 300 µs phase 0, roundtrip thẳng ≈ 100 µs) — dưới 2x là bench sai | `cmd/proxylab -mode overhead` hoặc test bench, ghi `(closed-loop)` |
| G3 | Keep-alive phía client: 200 request trên **1** connection vs 200 connection mới, cùng upstream Dial mới mỗi request | chỉ **1.2-1.6x** — lối tắt phase 3 nuốt phần lớn lợi ích; đây là số nền cho phase 5 | như G2, hai cột |
| G4 | Tắt `SetNoDelay` ở proxy ⇒ response chunked nhiều chunk có p50 tăng một **hằng số ≈ 40 ms** (phase 0: 44.03 ms) | +40 ms ở p50, không phải ở đuôi | flag `-nodelay=false`, so p50/p99 |
| G5 | Bẫy #3: POST 1000 B mà upstream không dial được ⇒ proxy **drain** body rồi trả 502; request kế tiếp trên **cùng** connection vẫn parse đúng. Phản chứng: bỏ drain ⇒ request 2 thành 400 | drain: 2/2 request đúng; bỏ drain: request 2 = 400 | test `TestDrainOnUpstreamFail` + biến thể tắt drain |
| G6 | Sau 1000 request (nửa keep-alive, nửa `Connection: close`) goroutine về mức nền | `NumGoroutine` sau − trước ≤ 2 | test đếm goroutine, `-race` |

**Quyết định thiết kế đăng ký trước** (đổi sau phải ghi lý do):

| # | Quyết định | Lý do / hệ quả |
|---|---|---|
| D1 | `Connection: close` sang upstream, `Dial` mới mỗi request | lối tắt hợp pháp của ROADMAP: body-tới-EOF luôn đúng, chưa cần pool. Phase 5 tháo |
| D2 | `Host` forward **nguyên văn** | P-arch-1 còn mở, quyết ở phase 4 |
| D3 | Response body tới EOF (không CL/TE) ⇒ client HTTP/1.1 nhận **chunked** do proxy mã hoá lại; client HTTP/1.0 nhận tới EOF rồi proxy đóng | client phải biết ranh giới; HTTP/1.0 không có chunked |
| D4 | Trailer chunked từ upstream **bị bỏ** | `ChunkedWriter.Close` không ghi trailer; ghi nợ P3-k |
| D5 | `X-Forwarded-For`: **append** IP client, chưa có trust list | XFF trust là phase 4 |
| D6 | Lỗi upstream: dial/ghi lỗi ⇒ 502, đọc head quá deadline ⇒ 504. Connection client **giữ** nếu body request đã đọc hết (kể cả bằng drain), **đóng** nếu head response đã gửi | không được gửi hai response cho một request |
| D7 | Response 1xx (trừ 101) từ upstream: **bỏ qua**, đọc response kế; 101 ⇒ 502 và đóng | Upgrade/tunnel không thuộc phase 3 |

## Deliverable

- `make proxylab` xanh: 4 lệnh `curl` đúng byte, không treo, **không để lại tiến trình**.
- `go test ./internal/proxy -race`: GET / POST / chunked / keep-alive 2 request / drain khi
  upstream chết / goroutine không leak.
- Bài phản chứng cho bẫy #2 (G1) và bẫy #3 (G5) **đỏ** khi tắt phòng tuyến.

## Reproduce toàn bộ phase

```bash
# 1. test + phản chứng (không cần server nào chạy)
go test ./internal/proxy/ -race -count=1 -v          # 14 PASS; dòng "G6: goroutine trước/sau"
make proxylab-nodefense                              # PHẢI đỏ: TestDrainOnUpstreamDown + TestRawCopyTrap (3.00s)
go test ./internal/proxy/ -run TestRawCopyTrap -count=3 -v -tags nodefense   # G1: 3 lần ≈ 3.00s

# 2. e2e curl (terminal 1: make upstream; terminal 2:)
make proxylab                                        # 4 curl, exit 0, không còn bin/edgegate sau đó

# 3. số đo G2/G3/G4 (tự khởi động upstream + proxy, chạy proxy 2 lần cho G4)
make proxybench                                      # → bench/p3-proxybench-*.txt
```

## Nhật ký

Chi tiết theo giờ ở `diary/phase3-log.md`. Tóm tắt turn 2:

1. `make proxylab` xanh nhưng để mồ côi: `kill` pid của `go run` không giết binary con. Sửa
   recipe sang build rồi chạy binary. Bài học: một target "xanh" còn phải trả hệ thống về sạch.
2. `pkill -f` với pattern nằm trong chính dòng lệnh đang chạy ⇒ tự giết shell (exit 144). Dùng
   `pkill -x` + script file.
3. G2, G3 đúng khoảng đăng ký ngay lần đo đầu (3.39x, 1.44x).
4. G4 hai lần sai: flag `-nodelay=false` là no-op vì Go mặc định `TCP_NODELAY=1`; sau khi sửa,
   Nagle không cộng 40 ms mà tạo **sàn 44 ms** do chunk terminator là write nhỏ thứ hai.
5. G1 lập lại 3 lần: 3.01/3.00/3.00 s = `UpstreamBodyTimeout`. G6: goroutine 4 → 4..6.

## Giả thuyết sai

| Tôi tưởng là | Thực tế là | Lệnh + output đã lật tẩy | Đã sửa thế nào |
|---|---|---|---|
| `-nodelay=false` (không gọi `SetNoDelay(true)`) là "bật Nagle" | Go đã `setNoDelay(fd, true)` trong `newTCPConn`; không gọi gì = vẫn NODELAY. Flag là no-op | `bin/proxylab -mode nagle -chunkms 0/2/5/10` với hai giá trị flag: p50 473 vs 511 µs, 13.4 vs 14.3 ms, … không khác (`bench/p3-proxybench-*.txt`, khối "G4 lần 1") | `setNoDelay` gọi `tc.SetNoDelay(*cfg.NoDelay)` tường minh cả hai chiều |
| Nagle làm p50 response chunked **+≈40 ms hằng số** (G4) | Nagle tạo **sàn ≈ 44 ms**: ms=0 ⇒ 44.0 (+43.6), ms=2 ⇒ 44.0 (+29.5), ms=5 ⇒ 44.0 (+14.9), ms=10 ⇒ 56.2 (+1.8). Write nhỏ thứ hai (chunk terminator) chờ delayed-ACK của write đầu; response tự dài > 44 ms thì hầu như không đội | khối "G4 lần 2" cùng file | Không sửa code (NODELAY mặc định đã đúng). Sửa cách phát biểu G4 ở bảng số đo; ghi vào Rút ra |
| `make proxylab` xanh = xong | Xanh nhưng `kill $(cat pid)` giết `go run`, binary con sống tiếp giữ :8080 (`pgrep`: `/tmp/go-build…/exe/edgegate`) | `make proxylab \| tee` treo 2 phút; `pgrep -af edgegate` | Recipe build `bin/edgegate` rồi chạy binary; kiểm `pgrep` sau kill |
| Cùng `pkill -f 'bin/upstream'` trong một dòng lệnh dài là vô hại | Dòng lệnh của shell chứa chuỗi đó ⇒ pkill giết chính shell, exit 144, không có output | lần chạy bench đầu: `Exit code 144`, không có file kết quả | Script file + `pkill -x <tên tiến trình>` |
| Phase có 13 test | 14 (đếm nhầm ở turn 1) | `go test -v \| grep -c PASS` | Sửa số trong diary |

## Số đo

Tất cả **closed-loop, 1 connection, cùng máy 6 core** (generator + proxy + upstream chung CPU,
P-env-2 chưa trả), loopback trần, `SetNoDelay(true)` trừ khi ghi khác. Con số là **overhead
tương đối** của một hop L7, không phải throughput hay tail dưới tải. Nguồn:
`bench/p3-proxybench-GOTIT-00663.txt`, `bench/p3-tests-turn2.txt`.

| # | Đăng ký | Đo được | Kết luận |
|---|---|---|---|
| G1 | treo = `UpstreamBodyTimeout` ± 50 ms | 3.01 / 3.00 / 3.00 s với timeout 3 s (3 lần, `-tags nodefense`) | **Đúng.** I3 biến treo vô hạn thành treo có hạn |
| G2 | p50 proxy / p50 thẳng = 3-5x | **3.39x** (473 µs / 139 µs, n=2000); p99 2.63x (825 / 314 µs); chênh tuyệt đối p50 333 µs | **Đúng.** Chênh ≈ 1 dial loopback (~300 µs phase 0) + parse 2 chiều |
| G3 | mỗi-conn / keep-alive = 1.2-1.6x | **1.44x** p50 (657 / 456 µs); wall 1.42x (1.353 / 0.950 s cho 2000 req). Lần chạy 2 (`make proxybench`): 1.63x; G2 lần 2: 3.20x | **Đúng**, nhưng dao động giữa hai lần chạy ±0.2x — cùng máy, 6 core chia ba tiến trình. D1 nuốt phần lớn lợi ích; số nền cho phase 5 |
| G4 | Nagle ⇒ +≈40 ms hằng số ở p50 | lần 1: **0 ms** (flag no-op). lần 2: sàn **44.0 ms** ở ms=0/2/5, +1.8 ms ở ms=10 (NODELAY: 0.43 / 14.4 / 29.1 / 54.4 ms) | **Sai hai lần**, cơ chế đúng: delayed-ACK 40 ms giữ write nhỏ thứ hai; hình dạng là sàn, không phải cộng |
| G5 | drain: 2/2 đúng; bỏ drain: request 2 ≠ 502 | `TestDrainOnUpstreamDown` PASS; `-tags nodefense` FAIL 0.00 s (sau khi sửa body test, xem turn 1) | **Đúng** (sau khi làm phản chứng đỏ được) |
| G6 | goroutine sau − trước ≤ 2 sau 1000 req | trước 4 → sau 6 / 4 / 5 (3 lần `-race`, 200 req × nửa keep-alive) | **Đúng.** Test dùng 200 req thay 1000 — ghi lại; không đổi kết luận |

Bảng phụ G4 (p50, n=100 mỗi ô, `GET /chunked?n=5&ms=X` qua proxy):

| ms giữa chunk | NODELAY (mặc định) | Nagle (`-nodelay=false`) | chênh |
|---|---|---|---|
| 0 | 430 µs | 44.004 ms | +43.6 ms |
| 2 | 14.42 ms | 43.97 ms | +29.5 ms |
| 5 | 29.13 ms | 44.00 ms | +14.9 ms |
| 10 | 54.40 ms | 56.17 ms | +1.8 ms |

## Invariant + lệnh kiểm chứng

_(turn 3)_

## Đọc gì

- RFC 9112 §6.3 (framing body), §9.3 (persistence), §9.6 (tear-down)
- RFC 9110 §7.6.1 (hop-by-hop `Connection`), §15.6.3 (502), §15.6.5 (504)
- RFC 7239 / de-facto `X-Forwarded-For`

## Rút ra

_(turn 3)_

## Nợ kỹ thuật

_(turn 3; ID `P3-k`, ghi vào `docs/debts.md`)_
