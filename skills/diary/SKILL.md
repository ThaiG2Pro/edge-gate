---
name: diary
description: Quy tắc ghi nhật ký phase cho repo edgegate (diary/phase*.md). Dùng khi ghi, cập nhật hoặc chốt một file nhật ký phase, khi ghi lại kết quả benchmark/load-test/fuzz, hoặc khi đánh dấu một phase là xong.
---

# Skill: Ghi nhật ký phase của EdgeGate

Áp dụng mỗi khi làm việc trong repo này và có bất kỳ thứ gì cần ghi vào `diary/phase*.md`.

## Nguyên tắc gốc

> **Mọi con số và mọi kết luận phải kèm lệnh shell sinh ra nó, và output thật dán nguyên văn.**

Lý do: nhật ký này không phải báo cáo thành tích, nó là **bản ghi thí nghiệm**. Sáu tháng sau
đọc lại phải **chạy lại được**; trên máy khác phải **biết vì sao số khác**. Một con số không có
lệnh đi kèm là một con số không kiểm chứng được — tức là vô giá trị.

Hệ quả trực tiếp:

1. **Ghi trong lúc làm, không phải sau khi xong.** Giả thuyết sai lúc còn nóng mới là thứ đáng giá.
   Viết lại sau khi xong sẽ chỉ còn kết quả đẹp, mất sạch quá trình.
2. **Dán output nguyên văn, không tóm tắt.** "nhanh hơn nhiều" là vô nghĩa; `p99 41ms vs 1.2ms` thì không.
3. **Chốt bằng TỈ SỐ, không phải số tuyệt đối.** Số tuyệt đối đổi theo máy, theo `GOMAXPROCS`,
   theo việc load generator có chạy cùng máy hay không. Tỉ số thì bền và mang ý nghĩa thiết kế.
4. **Ghi cả cái sai, ghi trước cái đúng.** Bảng "giả thuyết sai" là cột giá trị nhất của cả file.
5. **Không tin số đo trước khi so với tỉ số lý thuyết kỳ vọng.** Thấy 1.05x ở chỗ đáng lẽ 20x
   thì nghi **bench sai**, đừng nghi máy lạ.

## Luật riêng của đo mạng: BA cái bẫy phải khai báo trong mọi mục số đo

Đây là chỗ đo-mạng khác đo-đĩa, và là chỗ 90% benchmark proxy trên mạng là rác.

### 1. Coordinated omission — cái bẫy lớn nhất

`ab`, `wrk`, và `hey` chạy **closed-loop**: N connection, mỗi cái gửi request tiếp theo *sau khi*
nhận xong response trước. Khi proxy chậm đi, chúng **tự động gửi ít hơn**. Nghĩa là con số p99
chúng in ra là "p99 của cái tải mà proxy chịu nổi", **không** phải p99 dưới tải bạn muốn đo.
Một stall 1 giây làm mất hàng nghìn sample đáng lẽ phải chậm — chính những sample tạo nên tail.

**Luật:** mọi số latency trong nhật ký này phải đến từ **open-loop, có rate cố định**
(`vegeta attack -rate=5000`, hoặc `wrk2 -R5000`). Nếu buộc phải dùng closed-loop, **ghi rõ**
`(closed-loop, coordinated omission chưa loại trừ)` ngay cạnh con số.

### 2. Loopback không phải mạng

Trên `127.0.0.1`, RTT ~30µs và không có packet loss. Mọi kết luận về **connection pooling**,
**TLS handshake**, **P2C**, và **retry** đều bị bóp méo hoặc mất hẳn: pool tiết kiệm 1 RTT, mà
1 RTT ở đây gần bằng 0 ⇒ pool trông như vô dụng.

**Luật:** thí nghiệm nào mà kết luận phụ thuộc RTT thì phải chạy **hai lần** — loopback trần, và
loopback đã bơm delay — rồi ghi **cả hai** cột:

```bash
sudo tc qdisc add dev lo root netem delay 20ms   # bơm 20ms mỗi chiều
sudo tc qdisc del dev lo root                    # nhớ tháo, nếu không mọi bench sau đều sai
```

### 3. Máy đo và máy bị đo dùng chung CPU

Load generator ăn CPU thật. Trên 6 core, `vegeta` ở 50k rps có thể ăn 3 core ⇒ proxy chỉ còn 3,
và cả hai đều chậm. Số đo phản ánh **cuộc tranh chấp CPU**, không phản ánh proxy.

**Luật:** ghi `nproc`, `GOMAXPROCS` của proxy, và **ghim core**:

```bash
taskset -c 0,1,2 ./edgegate &        # proxy: core 0-2
taskset -c 3,4,5 vegeta attack ...   # generator: core 3-5
```

### Bốn thứ hay bị nhầm là "proxy chậm" nhưng là giới hạn hệ điều hành

Kiểm **trước** khi kết luận, và dán output vào nhật ký:

```console
$ ulimit -n                                     # 1024 ⇒ test 10k conn chết vì fd, không vì code
$ sysctl net.ipv4.ip_local_port_range           # ~28k ephemeral port ⇒ trần rps khi không pool
$ ss -s | head -3                              # đếm TIME_WAIT: cạn port là do đây
$ sysctl net.core.somaxconn net.ipv4.tcp_max_syn_backlog
```

Cái thứ năm, không nhìn bằng sysctl được: **Nagle + delayed ACK**. Ghi response thành hai lần
`Write` (header rồi body) trên connection chưa `SetNoDelay(true)` tạo ra spike đúng **40ms** —
một con số đặc trưng, thấy nó là biết ngay bệnh gì.

## Cấu trúc bắt buộc của `diary/phaseN.md`

| Mục | Bắt buộc có gì |
|---|---|
| Header | thời lượng, ngày bắt đầu/kết thúc, trạng thái, **commit hash** lúc chốt phase |
| **Môi trường** | output của `uname -srmo && go version && nproc && ulimit -n` — không có thì mọi bench vô nghĩa |
| Mục tiêu phase | 1-2 câu |
| Câu hỏi phải trả lời được | danh sách câu hỏi tự chấm điểm, viết **trước** khi làm |
| **Giả thuyết đăng ký trước** | G1..Gn kèm **tỉ số kỳ vọng**, viết khi code đã xong mà **chưa chạy lệnh đo nào** |
| Deliverable | test/bench/fuzz cụ thể chứng minh đã hiểu, không phải "code chạy được" |
| **Reproduce toàn bộ phase** | khối bash copy-paste chạy lại được từ đầu, kèm cả lệnh `tc`/`taskset` |
| **Nhật ký theo ngày** | mỗi mục: ` ```console ` block (lệnh + output thật) → *Đọc kết quả* → *Đang nghĩ gì* |
| **Giả thuyết sai** | bảng 4 cột: tôi tưởng là / thực tế là / **lệnh + output đã lật tẩy** / đã sửa thế nào |
| **Số đo** | kèm **lệnh, ngày, commit, máy, closed/open-loop, RTT**; kết lại bằng bảng **tỉ số** |
| **Invariant + lệnh kiểm chứng** | bảng: invariant / cài ở `file:hàm` / lệnh kiểm chứng / kết quả |
| Đọc gì | RFC + tài liệu đã đọc *trong* phase này, kèm số mục (`RFC 9112 §6.3`) |
| **Rút ra** | viết như thể đang giải thích cho người khác — không phải gạch đầu dòng từ khoá |
| Nợ kỹ thuật | checkbox, ID dạng `PN-k`, ghi vào `docs/debts.md` |

Mẫu trống: `diary/phase0.md`.

## Khuôn một mục nhật ký

````markdown
### YYYY-MM-DD — <việc chính của buổi>

```console
$ taskset -c 3,4,5 vegeta attack -rate=5000 -duration=30s -targets=t.txt | vegeta report
Latencies  [min, mean, p50, p90, p95, p99, max]  0.3ms, 1.1ms, 0.9ms, 1.8ms, 2.4ms, 41ms, 210ms
Success    [ratio]  100.00%
```

**Đọc kết quả:** p50/p99 = 46x. Khoảng cách p95→p99 nhảy 17x là **cliff**, không phải
đuôi mượt ⇒ nghi một cơ chế bật/tắt (GC pause, hoặc pool cạn rồi phải Dial mới), không phải
"tải cao nên chậm dần".

**Đang nghĩ gì:** <nghi vấn còn lại, hướng tiếp theo>
````

Nếu output dài, cắt bớt **phần không liên quan** và ghi rõ `...`, nhưng **không bao giờ**
sửa hay làm tròn con số.

## Khi số đo trông vô lý

1. Viết ra **tỉ số kỳ vọng theo lý thuyết** trước.
2. Nếu đo lệch xa → **giả định bench sai**, không phải máy lạ.
3. Truy ra "cái tôi tưởng đang đo" ≠ "cái máy thật sự làm". Ba thủ phạm kinh điển của đo mạng:
   closed-loop giấu mất tail; loopback không có RTT nên pooling/TLS mất ý nghĩa; generator và
   proxy giành CPU.
4. Sửa bench, **giữ lại cả output sai lẫn output đúng** trong nhật ký, và điền một dòng vào
   bảng giả thuyết sai.

## Luật riêng cho phase về bảo mật giao thức (phase 2, 4)

Một parser "chạy đúng" không phải bằng chứng. Bằng chứng là:

1. **Fuzz không panic** — dán số exec thật: `go test -fuzz FuzzX -fuzztime 120s`. Bắt buộc kèm
   `-fuzzminimizetime 1s`.
2. **Differential fuzzing** — cùng một byte string, so kết luận của parser mình với `net/http`.
   Hai bên **không đồng ý về ranh giới body** = một lỗ hổng smuggling. Đây là bằng chứng mạnh
   hơn "không panic" rất nhiều.
3. **Bài phản chứng phải ĐỎ.** Với mỗi phòng tuyến, viết một test cố tình tắt nó và **bắt buộc
   test đó fail**. Một bài test chưa bao giờ đỏ thì chưa phải bằng chứng.

## Chống các thói quen xấu

| Đừng | Hãy |
|---|---|
| "nhanh hơn đáng kể" | `p99 41ms → 1.2ms = 34x` |
| "đã test, chạy ổn" | dán lệnh `go test ... -v` và output |
| Ghi mean latency | ghi p50/p90/p99/p99.9 — proxy chỉ có tail mới có nghĩa |
| Ghi số từ `ab`/`wrk` như thể nó đúng | ghi rõ closed-loop, hoặc đổi sang `vegeta`/`wrk2` |
| "pool giúp tăng tốc" | `pool on/off, RTT 20ms: 22.1ms → 1.3ms = 17x; RTT 0: 1.1x` |
| Viết nhật ký sau khi phase xong | viết ngay lúc bug còn nóng |
| Giấu benchmark đã đo sai | ghi vào bảng giả thuyết sai — đó là phần đáng giá nhất |
| Ghi số mà không ghi máy/ngày/commit/loop-mode | bốn thứ đó là điều kiện để số có nghĩa |

## Checklist trước khi đánh dấu phase ✅

- [ ] Mục **Môi trường** có output thật, gồm `ulimit -n` và `nproc`
- [ ] Khối **Reproduce** chạy lại được từ máy sạch (đã thử chạy lại ít nhất 1 lần)
- [ ] Mọi bảng số ghi đủ: lệnh + ngày + commit + máy + **closed/open-loop** + **RTT**
- [ ] Mọi `tc qdisc add` đều có `tc qdisc del` tương ứng đã chạy
- [ ] Có ít nhất một dòng trong bảng **giả thuyết sai** (nếu không có, khả năng cao là chưa thật
      sự thử gì khó, hoặc đã quên ghi)
- [ ] Mọi **câu hỏi phải trả lời được** ở đầu file đã được trả lời ở mục **Rút ra**
- [ ] Mọi **giả thuyết đăng ký trước** (G1..Gn) đều đã chấm đúng/sai
- [ ] Bảng **invariant** chỉ đúng `file:hàm` và lệnh kiểm chứng
- [ ] Phase có phòng tuyến bảo mật: **bài phản chứng đã được xác nhận là ĐỎ khi tắt phòng tuyến**
- [ ] Nợ mới đã vào `docs/debts.md` với ID `PN-k`, mỗi món kèm **lệnh để trả**
- [ ] Cập nhật cột trạng thái của phase trong `ROADMAP.md`
