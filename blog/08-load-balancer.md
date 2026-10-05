# Bài 8 — Load balancer "thông minh" có thông minh không?

> Series [Mở nắp reverse proxy](README.md) · bài 8/16 · cần đọc trước: [bài 1](01-benchmark-noi-doi.md)

Một trong bốn backend của bạn chậm hơn hẳn: ổ đĩa kém, hoặc nó đang chạy GC dài. Dashboard cho thấy
p99 bị kéo lên. Ai đó đề xuất đổi `upstream` của nginx từ round-robin sang `least_conn`: *"node chậm
giữ request lâu hơn, nên sẽ ít được chọn hơn."*

Lập luận đúng. Node chậm sẽ ít được chọn hơn thật. Nhưng p99 có giảm không?

## Thí nghiệm

4 backend chạy cùng tiến trình với proxy. Mỗi backend trả lời sau 2 ms, trừ `b0` chậm 10 lần (20 ms)
và `b1` trả 503 ngay lập tức cho 30 % request. 32 client, 20 000 request. Bốn thuật toán: round-robin,
least-conn, P2C+EWMA (giải thích ở dưới), consistent hash.

```bash
make lblab-skew
```

Lượt đầu tắt outlier ejection, để thấy riêng thuật toán:

```console
$ ./bin/lblab -algos rr,leastconn,p2c,chash -skew slow=10x,err=30% -n 20000 -outlier=false
algo           p50      p90      p99      max   5xx%    rps  share b0/b1/b2/b3 (%)
rr          2.94ms  20.76ms  22.01ms  70.34ms  7.88%   4429  25.0/25.0/25.0/25.0
leastconn   2.83ms   4.62ms  21.47ms  26.13ms 11.32%   8735  4.3/38.5/28.6/28.6
p2c         2.64ms   4.18ms   6.44ms  24.44ms 11.33%  11161  0.6/39.1/29.4/31.0
chash       2.88ms  20.68ms  21.61ms  25.37ms  8.26%   4692  23.6/27.3/22.8/26.3
```

(Số đo gốc: `bench/p6-lblab-skew.txt`, lặp thêm 2 lượt trong `bench/p6-lblab-skew-rep.txt`. WSL2,
closed-loop, nên chỉ tỉ số là đáng tin.)

Gom 3 lượt lại (dải min-max, từ `diary/phase6.md`):

| Thuật toán | phần tải của b0 (chậm) | p90 | p99 | p99 so với RR |
|---|---|---|---|---|
| round-robin | 25.0 % | 20.76-20.8 ms | 21.80-22.01 ms | 1 |
| least-conn | 4.1-4.3 % | 4.21-4.62 ms | 21.18-21.47 ms | **0.97-0.98** |
| P2C, tau 1 s | **0.6 %** | 3.84-4.31 ms | **5.89-6.48 ms** | **0.27-0.29** |
| consistent hash | 23.6-25.0 % | 20.68-20.77 ms | 21.61-21.69 ms | 0.98-0.99 |

Least-conn làm đúng điều nó hứa: node chậm từ 25 % tải xuống 4.3 %. p90 giảm từ 20.76 ms xuống
4.62 ms. **p99 thì gần như không nhúc nhích.** P2C đẩy node chậm xuống 0.6 %, và chỉ lúc đó p99 mới
rơi xuống 6.44 ms.

### Ngưỡng 1 %

p99 là latency của 1 % request chậm nhất. Node 20 ms còn nhận **hơn 1 %** tải thì 1 % chậm nhất đó
toàn là request của nó, và p99 bằng đúng latency của node chậm. Least-conn đưa node chậm từ 25 % xuống 4.3 %
mà p99 vẫn nằm yên, vì 4.3 % vẫn lớn hơn 1 %.

Trước khi đo, nhật ký phase 6 đoán least-conn sẽ giảm p99 "**≥ 2x**". Phần tải thì đoán đúng
(3-8 %). Phần p99 thì sai: tỉ số RR/least-conn chỉ 1.02-1.03x. Câu hỏi đúng không phải "thuật toán
có né node chậm không", mà là **"có đẩy node chậm xuống dưới 1 − percentile không"**.

### Least-conn dồn tải vào node lỗi nhanh

Nhìn cột `b1` trong output: least-conn đưa **38.5 %** tải vào node trả 503 nhanh, nhiều hơn mức 25 %
của round-robin. Node trả lỗi ngay thì không giữ request nào, inflight luôn thấp, nên least-conn tưởng
nó khoẻ nhất. Kết quả: 5xx client thấy là 11.32-11.97 %, so với 7.49-7.88 % của round-robin. "Thông
minh" ở đây làm tệ hơn.

### Bật outlier ejection, và tôi đã tính sai

Outlier ejection loại một node sau 5 lỗi liên tiếp. Trước khi đo, tôi tính: 30 % lỗi thì xác suất 5
lỗi liên tiếp là 0.3⁵ = 0.24 %, "hiếm", nên outlier gần như không bắt được node này.

Sai. 0.24 % là xác suất **mỗi request**. Kỳ vọng số request tới chuỗi 5 lỗi đầu tiên là 586.
Least-conn gửi cho `b1` khoảng 3 400 rps, nên chuỗi đó tới sau khoảng **0.17 giây**. Hiếm theo request
không có nghĩa là hiếm theo thời gian khi rps cao. Bật outlier thì `b1` chỉ còn 0.1-1.2 % tải.

Nhưng loại `b1` lại làm lộ `b0`. P2C giờ chọn cặp trong 3 node, `b0` lên 1.2 % và p99 của P2C nhảy
từ 6.44 lên **20.31 ms** (WSL2). Trên Linux thuần (CachyOS), cùng lệnh, `b0` ở lại 0.9 % và p99 giữ
được 4.10-4.27 ms, so với 21.22-21.25 ms của round-robin
(`bench/baseline/thai-computer-20261004/p6-lblab-skew.txt`, 3 lượt). Cùng một cơ chế: 0.9 % dưới
ngưỡng, 1.2 % trên ngưỡng.

## Chỗ P2C thua

P2C nhớ quá khứ qua EWMA. Nhớ thì cũng phải quên. Kịch bản: `b2` chậm 10 lần từ đầu, rồi **hồi
phục** ở giữa bài. `p2c-slow` là P2C với tau 30 giây thay vì 1 giây.

```bash
make lblab-recover
```

```console
$ ./bin/lblab -algos leastconn,p2c,p2c-slow -flap -recover -n 20000 -conns 4
algo       p99 nửa đầu  p99 nửa sau  share nửa sau b0/b1/b2/b3 EWMA b2 lúc flap→cuối
leastconn      21.27ms       4.13ms  25.1/25.0/25.0/24.9      20.5ms→2.79ms
p2c             4.42ms       8.26ms  28.8/24.0/17.8/29.4      13.81ms→3.01ms
p2c-slow        4.07ms       4.43ms  33.4/29.3/0.0/37.3       16.81ms→14.22ms
```

(Số đo gốc: `bench/p6-lblab-recover.txt`, WSL2, 2 lượt, đã bỏ bớt cột. Linux thuần:
`bench/baseline/thai-computer-20261004/p6-lblab-recover.txt`, 3 lượt, `p2c-slow` cũng cho `b2` 0.0 %.)

Least-conn trả `b2` về **25.0 %** ngay sau khi nó hồi phục. Inflight không có trí nhớ. `p2c-slow`
cho `b2` **0.0 %** suốt nửa sau: node không được chọn thì không có mẫu mới, EWMA cũ chỉ hạ theo
`exp(−t/30 s)`. Nhật ký phase 6 ước tính `b2` bị bỏ đói khoảng 30·ln 6 ≈ 54 giây.

p99 không lộ cái thua này (4.43 vs 4.13 ms), vì 3 node còn lại dư sức gánh. Cái giá là **capacity
bị bỏ phí**: một phần tư cụm ngồi chơi. Nó chỉ thành p99 khi cụm gần bão hoà.

Tôi cũng đoán sai chỗ này. Giả thuyết ban đầu: P2C tau dài thua khi một node **xấu đi**. Đo chiều đó
(`make lblab-flap`) thì P2C tau 30 s ngang least-conn: node chậm giữ request lâu, inflight lên ngay,
điểm lên ngay, không cần chờ EWMA. Nó chỉ thua khi node **tốt lên**: không có inflight nào kéo điểm
xuống.

## Phần phụ: health check chậm 600 ms

Active health check hỏi mỗi 200 ms, 3 lần lỗi liên tiếp mới đánh dấu node chết. Trong khoảng đó
request vẫn đập vào node chết. `TestLBKillRevive` giết một trong 4 backend giữa bài và đếm:

```bash
go test ./internal/proxy -run 'TestLBKillRevive' -count=5 -v
```

| Kịch bản client | chỉ active health | thêm passive outlier (5 lỗi liên tiếp) |
|---|---|---|
| 1 client, ~1 ms/request (phase 6) | 116-121 dial lỗi | **8** |
| 32 client không nghỉ (từ commit `37cd9d9`) | 6500-7200 | **11-34** |

(Dòng 1: `bench/p6-5-killrevive-fails.txt`. Dòng 2: `docs/debts.md` mục P6-5, ghi chú cập nhật
2026-10-05, WSL2. Đây là số **đếm** qua máy trạng thái, không phải latency, nên ít phụ thuộc máy hơn.)

Client không thấy lỗi nào trong cả hai cột: dial lỗi nghĩa là chưa gửi byte nào, proxy chọn backend
khác (bài 7). Nhưng mỗi lần dial lỗi là một lượt dial bỏ đi, và lượt chọn lại đó đi qua retry budget.
Bài 7 kể chuyện budget chặn chính những lượt này khi test chuyển sang 32 client.

Active và passive không thay nhau được. Active bắt node chết khi **không** có traffic. Passive bắt
nhanh khi **có**.

## Bên trong: P2C và EWMA decay theo thời gian

P2C (power of two choices) bốc ngẫu nhiên **hai** backend, chấm điểm, lấy con điểm thấp hơn. Điểm
là `ewma × (inflight + 1)` (`internal/lb/picker.go`, rút gọn):

```go
func (p2c) Pick(all []*Backend, now time.Time, _ string) *Backend {
	// ... avail = các backend còn available(now); 0 hoặc 1 con thì trả luôn
	i := rand.IntN(len(avail))
	j := rand.IntN(len(avail) - 1)
	if j >= i {
		j++
	}
	a, b := avail[i], avail[j]
	if a.score(now) <= b.score(now) {
		return a
	}
	return b
}
```

Phần quan trọng là EWMA **decay theo thời gian**, kể cả khi không có mẫu mới (`internal/lb/ewma.go`):

```go
func (e *ewma) score(now time.Time) float64 {
	// ...
	el := now.Sub(e.last)
	return e.v * math.Exp(-el.Seconds()/e.tau.Seconds())
}
```

Node bị chấm điểm xấu rồi không được chọn nữa thì điểm vẫn tự hạ theo đồng hồ, tới lúc nào đó lại
được thử, và có mẫu mới. Nếu decay theo **số request** thì không có đường về đó: không được chọn thì
không có request, không có request thì điểm đứng yên mãi.

Phản chứng chạy được: `make lblab-nodefense` chạy test với tag `nodefenselb`, đổi sang decay theo request.
`TestP2CRecovers` chọn lại node hồi phục **45/200** lần ở bản thường, **0/200** ở bản phản chứng
(`bench/p6-tests-g4-g7.txt`).

Tau là núm vặn giữa hai cái thua. Tau ngắn thì né node chậm nhanh, nhưng dễ nhảy theo nhiễu. Tau dài
thì ổn định, nhưng tin lại node đã hồi phục rất chậm, như bảng `-recover` ở trên.

## Mang về dùng

1. **p99 chỉ giảm khi node chậm nhận dưới 1 % tải.** Khi so thuật toán LB, đọc cột phần tải của từng
   backend, đừng chỉ đọc p99. Least-conn giảm p90 rõ rệt nhưng thường không chạm được p99.
2. **Least-conn thưởng cho node lỗi nhanh.** Dùng least-conn thì phải bật passive outlier ejection,
   nếu không node trả 503 ngay sẽ nhận nhiều tải nhất.
3. **Thuật toán nhớ lâu thì quên chậm.** EWMA tau dài né node chậm tốt, nhưng bỏ đói node vừa hồi
   phục hàng chục giây. Chọn tau theo thời gian bạn chấp nhận được để tin lại một node.

---

Số đo gốc: [`bench/p6-lblab-skew.txt`](../bench/p6-lblab-skew.txt),
[`bench/p6-lblab-skew-rep.txt`](../bench/p6-lblab-skew-rep.txt),
[`bench/p6-lblab-recover.txt`](../bench/p6-lblab-recover.txt),
[`bench/p6-5-killrevive-fails.txt`](../bench/p6-5-killrevive-fails.txt),
[`bench/baseline/thai-computer-20261004/`](../bench/baseline/thai-computer-20261004/) (`p6-lblab-*.txt`) ·
nhật ký: [`diary/phase6.md`](../diary/phase6.md) (turn 2, Giả thuyết sai) ·
sổ nợ: [`docs/debts.md`](../docs/debts.md) (P6-5).

**Bài tiếp theo:** [Bài 9: Xếp hàng hay từ chối?](09-shed.md)
