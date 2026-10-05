# Bài 7 — Retry có an toàn không?

> Series [Mở nắp reverse proxy](README.md) · bài 7/16 · cần đọc trước: [bài 3](03-keep-alive.md)

Proxy của bạn giữ một connection keep-alive tới upstream. Connection đó nằm rỗi trong pool vài
chục giây. Upstream thấy nó rỗi lâu quá, đóng. Đúng lúc đó một request mới tới, proxy lấy connection
ấy ra và ghi request vào. `Write` trả về `nil`. Một lát sau `Read` trả về EOF.

Request đó đã tới upstream chưa? Proxy nên gửi lại, hay trả 502? Nếu đó là `GET /products` thì gửi
lại vô hại. Nếu đó là `POST /payments` thì gửi lại có thể trừ tiền hai lần. Bài này đo hai câu hỏi: **request nào** được tự retry, và **retry bao nhiêu** thì vừa.

## Thí nghiệm 1: connection chết trong pool

Test này dựng một upstream đóng connection ngay sau mỗi response, rồi tắt probe của pool (bài 3) để
proxy chắc chắn lấy phải connection đã chết. Mỗi kiểu request chạy 50 lần:

```bash
go test ./internal/proxy -run TestIdleClosedUpstream -v
```

```console
=== RUN   TestIdleClosedUpstream/noprobe-GET-retry
    pool_test.go:230: 50/50; {Dials:51 Reuses:50 Puts:51 Retries:50 DropDirty:50 ...}
=== RUN   TestIdleClosedUpstream/noprobe-PUT-body-retry
    pool_test.go:230: 50/50; {Dials:51 Reuses:50 Puts:51 Retries:50 DropDirty:50 ...}
=== RUN   TestIdleClosedUpstream/noprobe-POST-body-502
    pool_test.go:289: 25 × 502 xen kẽ 25 × 200; {Dials:26 Reuses:25 Puts:26 Retries:0 DropDirty:25 ...}
```

(Chạy lại ngày 2026-10-05 trên WSL2. Cùng số với `bench/p5-pooltests.txt` cho GET và POST, và với
`docs/debts.md` mục P5-4b cho PUT. Test đếm request, không đo thời gian, nên số không đổi theo máy.)

- **GET**: 50/50 thành công, `Retries:50`. Proxy gửi lại trên connection mới, client không biết gì.
- **PUT có body**: cũng 50/50, `Retries:50`. Proxy đã đọc sẵn body vào RAM nên gửi lại được.
- **POST có body**: 25 lần 502, xen kẽ 25 lần 200. Không retry lần nào. 502 làm connection bị bỏ,
  request kế tiếp phải dial mới nên thành công, rồi connection mới đó lại chết, cứ thế xen kẽ.

POST bị 502 là quyết định có chủ đích. RFC 9110 §9.2.2 viết thẳng: *"A proxy MUST NOT
automatically retry non-idempotent requests."*

Chuyện này xảy ra thường không? Phase 5 đo bằng một upstream đóng connection rỗi sau 1-50 ms ngẫu
nhiên, 32 worker gửi POST 1 KiB, 10 000 request, ba lượt:

```console
probe=true  · status map[200:9987 502:13] · 502 = 13/10000 = 0.130 %
probe=false · status map[200:9629 502:371] · 502 = 371/10000 = 3.710 %
```

(Số đo gốc: `bench/p5-4-idlerace.txt`, WSL2. Ba lượt cho 0.09-0.13 % khi bật probe, 3.62-3.71 % khi tắt.)

Có probe thì vẫn lọt 9-13 POST trên 10 000: cửa sổ giữa lúc probe thấy connection còn sống và lúc
`Write`. Proxy không đóng được cửa sổ đó. Client, bên biết POST của mình lặp lại được không, tự quyết.

## Thí nghiệm 2: retry nhân tải lên bao nhiêu?

Retry đúng luật rồi. Nhưng đúng luật chưa chắc đã an toàn cho **tải**. Kịch bản: 4 backend, mỗi
backend đóng 50 % connection keep-alive trước khi trả byte nào. 1000 request/giây, open-loop
(bài 1), 10 giây. Chạy hai lần: có retry budget 10 %, rồi tắt budget (retry mù):

```bash
make retrylab
```

```console
$ ./bin/chaoslab -scenario retry -duration 10s -rate 1000 -drop 0.5
retry          n=10000 200:74.7% 502:25.3%
               200: p50 3.78ms p90 5.62ms p99 19.91ms ... · goodput 746/s
               proxy: retry cho 1200 / từ chối 2526
               upstream nhận 11200 lần gửi cho 10000 request client ⇒ khuếch đại 1.120x
$ ./bin/chaoslab-nd -scenario retry -duration 10s -rate 1000 -drop 0.5      # retry mù
retry          n=10000 200:100.0%
               200: p50 4.06ms p90 6.27ms p99 22.55ms ... · goodput 999/s
               proxy: retry cho 4942 / từ chối 0
               upstream nhận 14942 lần gửi cho 10000 request client ⇒ khuếch đại 1.494x
```

(Số đo gốc: `bench/p7-retrylab.txt`, WSL2.)

| 4 backend đóng 50 % connection dùng lại | WSL2 | Linux thuần (CachyOS), 3 lượt |
|---|---|---|
| budget 10 %: khuếch đại tải lên upstream | **1.120x** | 1.115-1.120x |
| budget 10 %: client nhận 502 | **25.3 %** | 24.9-25.4 % |
| retry mù: khuếch đại | **1.494x** | 1.496-1.506x |
| retry mù: client nhận 502 | **0 %** | 0 % |

(Linux: `bench/baseline/thai-computer-20261004/p7-retrylab*.txt`. Máy đo chính là laptop WSL2, nên
chỉ tỉ số là đáng tin. Ở đây hai máy cho cùng tỉ số.)

Budget cắt khuếch đại từ 1.494x xuống 1.120x. Cái giá là một phần tư client nhận 502.

### Tôi đã đoán sai vế 502

Trước khi đo, nhật ký phase 7 đăng ký: không budget thì khoảng 25 % client nhận 502, có budget thì
khoảng 45 %. Thực tế ngược hẳn: không budget là **0 %**, có budget là **25.3 %**. Lý do: lần retry
luôn dial một connection **mới**, và kịch bản chỉ đóng connection **dùng lại**. Retry nào cũng thành
công. Nên ở kịch bản này, budget đổi 25 % lỗi lấy 0.37x tải. Một trao đổi tệ, vì retry ở đây rẻ.

Budget có giá trị khi retry **không** cứu được gì, ví dụ backend quá tải thật. Sổ nợ ghi một lượt
đo kịch bản đó (`chaoslab -scenario overload`, backend chỉ chạy 2 request song song): có budget
thì gateway phải shed 19 420 request (48.5 % 503), retry mù thì 24 742 (61.9 % 503). Số này chỉ có
trong `docs/debts.md` mục P7-6 **(chưa có output thô trong repo)**.

## Bên trong: bốn điều kiện để được gửi lại

`Write` trả `nil` khi byte đã vào send buffer của kernel. Upstream đóng rồi thì RST tới sau, lúc
`Read`. Nên tiêu chí không thể là "ghi được chưa". Tiêu chí là **đã nhận được byte response nào
chưa**. 0 byte nghĩa là upstream chưa cho thấy side-effect nào. Từ 1 byte trở lên nghĩa là upstream
đã xử lý, gửi lại là gửi hai lần.

Proxy chỉ retry khi cả bốn điều đúng:

1. connection lấy từ **pool**. Connection vừa dial mà chết thì upstream chết thật;
2. lỗi I/O khi **0 byte response**, và không phải timeout. Upstream chậm có thể đang xử lý;
3. method **idempotent** theo RFC 9110: GET, HEAD, OPTIONS, TRACE, PUT, DELETE;
4. không có body, hoặc body ≤ 64 KiB, không chunked, đã đọc sẵn vào RAM.

Điều kiện 1, 3, 4 nằm trong một hàm (`internal/proxy/forward.go`, rút gọn):

```go
func canRetry(pc *pooledConn, req *httpx.Request, hasBufferedBody bool) bool {
	return pc.reused && (req.ContentLength == 0 || hasBufferedBody) && !req.Chunked && idempotent(req.Method)
}

// RFC 9110 §9.2.2: "A proxy MUST NOT automatically retry non-idempotent requests."
func idempotent(method string) bool {
	switch method {
	case "GET", "HEAD", "OPTIONS", "TRACE", "PUT", "DELETE":
		return true
	}
	return false
}
```

Điều kiện 2 kiểm ngay chỗ đọc response: `pc.in.n == 0 && !isTimeout(err) && canRetry(...)`, với
`pc.in` là bộ đếm byte bọc quanh connection upstream. Body ≤ 64 KiB được `io.ReadFull` vào RAM trước
lượt gửi đầu, ngay trong `roundTrip`.

Đọc kỹ RFC còn lòi ra một lỗi cũ: bản đầu chỉ xét body, nên **POST không body** vẫn được retry.
Test `noprobe-POST-nobody-502` viết trước, đỏ với `Retries:50`, rồi mới thêm `idempotent(method)`
(`docs/debts.md`, P5-4).

### Budget: trần là một tỉ lệ

Retry budget mượn từ Finagle. Một cửa sổ trượt 10 giây chia thành 10 xô. Mỗi request gốc gửi vào một
token, mỗi retry rút ra một (`internal/limit/limit.go`, rút gọn):

```go
func (r *RetryBudget) TryWithdraw(now time.Time) bool {
	// ...
	r.advance(now) // xoá các xô đã trượt khỏi cửa sổ
	var reqs, rets int64
	for i := range r.reqs {
		reqs += r.reqs[i]
		rets += r.rets[i]
	}
	limit := r.Percent*float64(reqs) + r.MinPerSec*r.Window.Seconds()
	if float64(rets) >= limit {
		r.denied++
		return false
	}
	r.rets[r.slot%10]++
	r.allowed++
	return true
}
```

Trần là **tỉ lệ** vì cụm 1 000 rps chịu được 100 retry/s, cụm 100 000 rps chịu được 10 000. Số tuyệt
đối thì hoặc vô dụng ở tải cao, hoặc chặn hết ở tải thấp. `MinPerSec·Window` là sàn (10/s).

Với 10 000 request trong 10 giây, trần lý thuyết là 10 % × 10 000 + 10 × 10 = 1 100 retry. Đo được
1 200. Phần dư đến từ cửa sổ trượt: ở giây thứ 10, xô của giây đầu rơi khỏi cửa sổ cùng với các retry
của nó, nên budget có thêm chỗ.

Budget áp cho **mọi** loại gửi lại: retry cùng backend và chọn backend khác khi dial lỗi. Finagle
cũng khuyên một budget chung cho mọi loại retry.

### Câu chuyện kèm: test đỏ vì budget làm đúng việc

Sau đợt trả hết nợ kỹ thuật, `TestLBKillRevive/active-only` đỏ 3/3 lượt trên `main`. Test này giết
một backend giữa bài để đo health check (bài 8). Commit `37cd9d9` vừa đổi nó từ 1 client sang
**32 client** chạy không nghỉ.

Test đòi client thấy toàn 200. Nó đọc được 502, và số 502 bằng đúng `RetryDenied`: **1160**. Bốn
backend round-robin, một con chết, nên khoảng 25 % request trúng con chết và cần chọn backend khác.
25 % vượt budget mặc định 10 %. Phần vượt thành 502, đúng như thiết kế phase 7.

Code đúng. Test sai: nó đo health nhưng ngầm phụ thuộc vào budget. Bản sửa nới budget cho riêng
kịch bản này:

```go
// 32 client, rr 4 node ⇒ ~25 % request trúng b3 lúc chết ⇒ cần retry D9
// cho 25 % > budget mặc định 10 % ⇒ phần vượt thành 502 đúng thiết kế
// (đo: 502 = RetryDenied = 1160). Kịch bản này đo HEALTH, không đo budget.
c.RetryBudget = RetryBudgetConfig{Percent: 1}
```

(`internal/proxy/lb_test.go`. Sau sửa: xanh 8/8 lượt, theo `diary/README.md`, "Phiên polish 2026-10-05".)

Budget đã làm đúng việc của nó **trong một test không định kiểm nó**. Bật budget trên production thì
đây là thứ bạn sẽ thấy: một phần tư cụm chết, một phần client nhận 502, thay vì cả cụm gánh thêm tải.

## Mang về dùng

1. **Đừng tự retry POST ở proxy.** Retry chỉ an toàn khi method idempotent, connection là đồ dùng
   lại, và chưa có byte response nào về. POST thì để client tự quyết.
2. **Đặt retry budget theo tỉ lệ, và chọn con số có chủ đích.** Budget đổi lỗi lấy sự sống của
   upstream. 10 % nghĩa là: khi hơn 10 % request cần retry, phần dư nhận lỗi. Nếu cụm của bạn chịu
   được nhiều hơn, nâng lên. Đừng để mặc định mà không biết nó là bao nhiêu.
3. **Một budget chung cho mọi loại gửi lại.** Retry cùng backend, chọn backend khác, retry ở tầng
   ứng dụng: mỗi lớp một budget riêng thì nhân lên nhau, đúng lúc cụm đang yếu.

---

Số đo gốc: [`bench/p7-retrylab.txt`](../bench/p7-retrylab.txt),
[`bench/baseline/thai-computer-20261004/`](../bench/baseline/thai-computer-20261004/) (`p7-retrylab*.txt`),
[`bench/p5-4-idlerace.txt`](../bench/p5-4-idlerace.txt), [`bench/p5-pooltests.txt`](../bench/p5-pooltests.txt) ·
nhật ký: [`diary/phase5.md`](../diary/phase5.md) (D4, câu 3), [`diary/phase7.md`](../diary/phase7.md) (G6, câu 5) ·
sổ nợ: [`docs/debts.md`](../docs/debts.md) (P5-4, P5-4b, P6-5, P7-6).

**Bài tiếp theo:** [Bài 8: Load balancer "thông minh" có thông minh không?](08-load-balancer.md)
