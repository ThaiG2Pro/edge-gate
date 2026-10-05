# Bài 3 — Keep-alive tiết kiệm bao nhiêu?

> Series [Mở nắp reverse proxy](README.md) · bài 3/16 · cần đọc trước: [bài 0](00-mot-request.md)

Bạn đọc một bài blog: "dùng connection pool, request nhanh hơn 36 lần". Có benchmark hẳn hoi. Bạn bật
pool cho service, đẩy lên staging ở một region khác, đo lại. Chỉ nhanh hơn khoảng 2 lần. Bạn bắt đầu
nghi: blog kia nói quá, hay pool của mình cấu hình sai?

Không cái nào sai. Cả hai con số đều thật, đo cùng một thứ, ở hai khoảng cách mạng khác nhau. Cái sai
là **đơn vị**. Bài này chỉ ra vì sao tỉ số "nhanh hơn bao nhiêu lần" là cách tóm tắt sai cho pool, và
đơn vị đúng là gì.

Phần RTT 0 chỉ cần Go. Phần có RTT cần `sudo` để bơm độ trễ vào loopback bằng `tc netem`:

```bash
git clone https://github.com/ThaiG2Pro/edge-gate.git && cd edge-gate
go run ./cmd/netlab -exp rtt -n 2000 -bufio=false   # RTT 0
make netlab-rtt                                     # RTT 20 ms và 40 ms, cần sudo
```

## Thí nghiệm

### Một connection, hai cách dùng

Client gửi request nhỏ rồi chờ response. Cách một: giữ **một** connection, dùng lại cho mọi request.
Cách hai: **dial mới** cho mỗi request. Trên loopback, không có độ trễ mạng nào:

```console
$ go run ./cmd/netlab -exp rtt -n 2000 -bufio=false -tag rtt0
biến thể                           loop            n      mean       p50       p90       p99       max    err
reuse 1 connection                 -            2000    32.7µs    23.5µs    46.6µs   188.9µs    1.09ms      0
dial mới mỗi request               -            2000   986.0µs   861.7µs    1.36ms    3.84ms   30.76ms      0
  [G2/G3] p50 dial / p50 reuse                            36.69x
```

(WSL2, `bench/p0-rtt-rtt0.txt`.)

Đây là con số 36 lần. Tôi đã đăng ký trước rằng nó sẽ chỉ khoảng 1.0–1.3 lần: loopback không có RTT,
handshake tốn 1 RTT, vậy dial gần như miễn phí. Sai hẳn một bậc. RTT không phải chi phí duy nhất của
một lần dial. Còn tạo socket, `connect`, kernel cấp port, phía server `accept` và tạo goroutine, rồi
cả hai đầu bị lên lịch lại.

Con số 36 lần cũng không đứng yên. Cùng lệnh, cùng máy, những lần chạy sau:

| dial / reuse, p50, RTT 0 | WSL2 | Linux thuần (CachyOS) |
|---|---|---|
| lần đầu (phase 0) | 36.69x | |
| 5 lần chạy sau đó, n=500 | 15.77–18.24x | |
| 3 lượt, n=2000 | | **2.45–2.57x** |

(WSL2: `bench/p0-rtt-rtt0.txt`, `bench/p0-7-rtt-5runs.txt`. Linux: `bench/baseline/thai-computer-20261004/p0-rtt-rtt0.txt`.)

Trên Linux thuần, dial loopback chỉ đắt hơn reuse 2.45–2.57 lần. Lớp mạng ảo của WSL2 làm dial đắt
bất thường. Đây cũng là một dự đoán phase 0 đã ghi trước khi có máy Linux, và nó đúng chiều.

Vậy blog "36 lần" kia đo cái gì? Nó đo **chi phí dựng socket trên một máy cụ thể**. Không phải mạng.

### Bơm RTT vào

Giờ thêm độ trễ mạng bằng `tc netem` trên loopback, và **đo** RTT bằng `ping` thay vì tin tham số:

```console
$ bash ./scripts/pay-P0-1.sh
=== netem delay 10ms  (n=300 request mỗi biến thể) ===
>>> ĐẶT delay=10ms mỗi chiều  =>  RTT ĐO ĐƯỢC = 20.157 ms
reuse 1 connection                 -             300   20.45ms   20.43ms   20.55ms   20.66ms   26.75ms      0
dial mới mỗi request               -             300   41.01ms   40.90ms   41.15ms   43.90ms   49.49ms      0
  [G2/G3] p50 dial / p50 reuse                             2.00x

=== netem delay 20ms  (n=150 request mỗi biến thể) ===
>>> ĐẶT delay=20ms mỗi chiều  =>  RTT ĐO ĐƯỢC = 40.201 ms
reuse 1 connection                 -             150   40.57ms   40.49ms   40.65ms   42.20ms   46.03ms      0
dial mới mỗi request               -             150   81.24ms   80.96ms   81.39ms   85.79ms   90.73ms      0
  [G2/G3] p50 dial / p50 reuse                             2.00x
```

(WSL2, `bench/p0-rtt-netem10ms-GOTIT-00663.txt` và `bench/p0-rtt-netem20ms-GOTIT-00663.txt`.
Máy đo chính là laptop chạy WSL2, nên chỉ tỉ số là đáng tin. `make netlab-rtt` chạy đúng script này.)

Tôi đã đăng ký trước: ở RTT 20 ms, tỉ số phải **trên 15 lần**. Đo được **2.00 lần**. Và đúng 2.00 lần ở
cả RTT 20 lẫn RTT 40. Một tỉ số trùng khít ở hai điểm khác nhau không phải trùng hợp. Nó là một giới hạn.

Trong lúc đó, khoản tiết kiệm tuyệt đối thì **tăng**: 40.90 − 20.43 = **20.47 ms** ở RTT 20, và 40.47 ms
ở RTT 40. Tỉ số tụt từ 36 xuống 2, trong khi pool tiết kiệm nhiều hơn hẳn. Ai báo cáo bằng tỉ số sẽ kết
luận ngược với sự thật.

### Bẫy phụ: netem trên `lo` áp delay hai lần

Nhìn kỹ dòng `>>>`: đặt `delay 10ms`, đo được RTT **20.157 ms**. Đặt `delay 20ms`, ra **40.201 ms**. Trên
`lo`, cả gói đi lẫn gói về đều qua qdisc của `lo`, nên netem áp delay cho **cả hai chiều**. Muốn RTT 20 ms
thì phải đặt `delay 10ms`. `Makefile` của repo từng đặt `delay 20ms` và gọi đó là "RTT 20 ms". Nếu không
kiểm bằng `ping`, mọi nhãn RTT từ phase 5 trở đi đã sai gấp đôi, và không tỉ số nào báo cho tôi biết.

### Qua proxy: pool tới upstream

Cùng câu hỏi, nhưng giờ là connection từ proxy tới upstream. `cmd/poollab` chạy upstream, proxy và
client trong một tiến trình, đo pool tắt rồi pool bật:

```bash
make poollab       # RTT 0
make poollab-rtt   # RTT 20 ms, cần sudo; tự bật rồi tự tháo netem
```

| GET /hello qua proxy, p50 | WSL2, RTT 0 | WSL2, RTT 20 ms | Linux thuần, RTT 0, 3 lượt |
|---|---|---|---|
| pool tắt (dial mỗi request) | 1.178 ms | 65.943 ms | 168 µs |
| pool bật | 221 µs | 48.08 ms | 90–117 µs |
| tỉ số tắt / bật | **5.34x** | **1.37x** | 1.43–1.87x |
| tiết kiệm mỗi request | **958 µs** | **17.863 ms** | 51–78 µs |
| một `net.Dial` bằng bao nhiêu RTT | 19.9 RTT | 1.0 RTT | 3.1–4.9 RTT |

(WSL2: `bench/p5-poollab-rtt0.txt`, `bench/p5-poollab-rtt20.txt`. Linux: `bench/baseline/thai-computer-20261004/p5-poollab-rtt0.txt`.
Bản Linux chưa có lượt chạy với netem.)

Cùng một hình dạng với thí nghiệm trên: tỉ số tụt 5.34 → 1.37 khi RTT tăng, còn khoản tiết kiệm tăng
958 µs → 17.9 ms. ROADMAP của dự án từng viết dự đoán ngược cả hai chiều: "khoảng 1.1 lần" ở RTT 0, "trên
15 lần" ở RTT 20 ms.

Ở RTT 20 ms, pool tiết kiệm 17.86 ms. Chia cho RTT đo bằng `ping` (20.45 ms) ra **0.87 RTT** mỗi request
(`diary/phase5.md`). Output tự chia bằng RTT đo qua dial nên in 0.83. Tôi đăng ký trước 0.9–1.1 RTT. Lệch
thấp một chút, cùng chiều với mọi số khác: mỗi mẫu qua proxy cộng thêm vài mili giây không có trong mô
hình. Nghi phạm là máy ồn, chưa chứng minh được.

## Bên trong: dial trả 2 RTT, reuse trả 1

Mô hình đơn giản giải thích được mọi con số ở trên:

```text
reuse  = xử lý          + 1 RTT     ← request đi, response về
dial   = dựng socket    + 2 RTT     ← SYN / SYN-ACK, RỒI request + response
tỉ số  = (dựng socket + 2·RTT) / (xử lý + RTT)
```

Ở RTT 0, tỉ số chỉ là "dựng socket / xử lý", và nó tuỳ máy: 36.69 lần trên WSL2 lúc máy chậm, 2.45–2.57 lần
trên Linux thuần. Khi RTT lớn dần, hai hằng số kia chìm đi, tỉ số tiến về **đúng 2** và dừng ở đó, dù mạng
tệ tới đâu. Còn khoản tiết kiệm là `dựng socket + 1 RTT`, tăng tuyến tính theo RTT.

Tỉ số không đo lợi ích của pool. Nó chỉ nói "dial trả 2 RTT, reuse trả 1".

Pool của EdgeGate là một stack LIFO cho mỗi backend (`internal/proxy/pool.go`, bỏ bớt phần đếm):

```go
func (p *pool) get() (*pooledConn, error) {
	if !p.cfg.Disabled {
		for {
			pc := p.pop()                                // đỉnh stack = connection trẻ nhất
			if pc == nil {
				break
			}
			if time.Since(pc.idleSince) > p.cfg.MaxIdleTime {
				pc.close()                               // rỗi quá lâu: upstream có thể đã đóng
				continue
			}
			if *p.cfg.Probe {
				if dead, known := probeIdle(pc.raw); known && dead {
					pc.close()                           // upstream đã gửi FIN trong lúc rỗi
					continue
				}
			}
			pc.reused = true
			return pc, nil                               // tiết kiệm: một lần dial
		}
	}
	return p.dialNew()                                   // trả giá: dựng socket + 1 RTT
}
```

Lấy ra thì dễ. Trả về mới khó. Một connection chỉ được `put` lại khi nó **sạch**: body response đã đọc tới
EOF, không còn byte thừa trong buffer, upstream không đòi đóng. Đó là dòng `clean = ...` ở cuối `exchange`
trong [bài 0](00-mot-request.md). Trả về một connection còn sót đuôi body của client A là để client B đọc
được dữ liệu của A.

Pool còn tiết kiệm một thứ không hiện ra trong latency. Cùng file `p5-poollab-rtt0.txt`, đếm socket ở trạng
thái TIME_WAIT phía proxy sau 2000 request: pool tắt **+1964**, pool bật **+0**. Bên nào chủ động đóng thì
giữ TIME_WAIT, và không pool nghĩa là tiêu một ephemeral port cho mỗi request. Chuyện gì xảy ra khi hết
port là [bài 4](04-het-port.md).

## Mang về dùng

1. **Đừng báo cáo lợi ích của pool bằng tỉ số.** Báo bằng mili giây tiết kiệm mỗi request, hoặc gọn hơn:
   **1 RTT cho mỗi connection mới**, cộng chi phí dựng socket của máy đó. Tỉ số đo ở RTT 0 nói về máy,
   không nói về pool.
2. **Đo ở RTT của mạng thật, và đo RTT bằng `ping`.** Loopback không có RTT. `netem` trên `lo` áp delay
   hai chiều. Ghi con số RTT **đo được** cạnh mọi kết quả, không ghi tham số bạn đã đặt.
3. **Pool chỉ an toàn khi biết connection nào sạch.** Lấy ra phải kiểm tuổi và kiểm upstream còn sống.
   Trả về phải chắc body đã đọc hết. Thiếu một trong hai thì pool đổi vài mili giây lấy một lỗi khó tìm.

---

Số đo gốc: [`bench/p0-rtt-rtt0.txt`](../bench/p0-rtt-rtt0.txt), [`bench/p0-7-rtt-5runs.txt`](../bench/p0-7-rtt-5runs.txt),
[`bench/p0-rtt-netem10ms-GOTIT-00663.txt`](../bench/p0-rtt-netem10ms-GOTIT-00663.txt),
[`bench/p0-rtt-netem20ms-GOTIT-00663.txt`](../bench/p0-rtt-netem20ms-GOTIT-00663.txt),
[`bench/p5-poollab-rtt0.txt`](../bench/p5-poollab-rtt0.txt), [`bench/p5-poollab-rtt20.txt`](../bench/p5-poollab-rtt20.txt),
[`bench/baseline/thai-computer-20261004/`](../bench/baseline/thai-computer-20261004/) (Linux thuần) ·
nhật ký: [`diary/phase0.md`](../diary/phase0.md) (G2, G3, trả nợ P0-1), [`diary/phase5.md`](../diary/phase5.md) (G1, G2).

**Bài tiếp theo:** [Bài 4: Hết port mà không báo lỗi? (tôi đã sai)](04-het-port.md)
