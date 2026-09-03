# Phase 3 — Vertical slice: `curl` xuyên proxy tới upstream và về

- **Thời lượng dự kiến:** 1-2 ngày · **thực tế:** _______
- **Bắt đầu:** 2026-09-03 22:30 · **Kết thúc:** _______
- **Trạng thái:** 🔨 turn 1 (code) xong 22:55 — 13 test xanh `-race`, phản chứng `nodefense` đỏ cả hai bài, 8 curl e2e đúng. Turn 2: đo G1-G6, chạy `make proxylab` nguyên bản. Giả thuyết bên dưới đăng ký **trước** file `.go` đầu tiên của phase.
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
_______ (điền ở turn 2)
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

- `make proxylab` xanh: 3 lệnh `curl` đúng byte, không treo.
- `go test ./internal/proxy -race`: GET / POST / chunked / keep-alive 2 request / drain khi
  upstream chết / goroutine không leak.
- Bài phản chứng cho bẫy #2 (G1) và bẫy #3 (G5) **đỏ** khi tắt phòng tuyến.

## Reproduce toàn bộ phase

```bash
_______ (điền ở turn 2-3)
```

## Nhật ký

_(turn 2)_

## Giả thuyết sai

| Tôi tưởng là | Thực tế là | Lệnh + output đã lật tẩy | Đã sửa thế nào |
|---|---|---|---|
| _______ | | | |

## Số đo

_(turn 2)_

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
