.PHONY: all fmt vet test bench check clean \
	envcap phase0 netlab netlab-rtt netlab-omission netlab-mem netlab-limits \
	netlab-server netlab-client \
	framelab fuzz-frame \
	httplab fuzz-http difffuzz difffuzz-chunk \
	proxylab proxybench upstream \
	smugglelab smugglelab-nodefense \
	poollab poollab-rtt poollab-nodefense \
	lblab lblab-skew lblab-flap lblab-nodefense \
	chaoslab slowlab ratelab \
	tlslab \
	perflab bench-vs-nginx epolllab \
	rtt-up rtt-down

all: fmt vet test

fmt:
	gofmt -w .

vet:
	go vet ./...

test:
	go test ./... -count=1 -race

bench:
	go test ./... -run '^$$' -bench . -benchmem

# ---------------------------------------------------------------------------
# Bơm/tháo RTT. MỌI thí nghiệm nào kết luận phụ thuộc RTT phải chạy CẢ HAI lần.
# Quên `rtt-down` là làm sai mọi bench sau đó.
# ---------------------------------------------------------------------------
# delay 10ms, KHÔNG phải 20ms. Trên `lo` cả gói đi và gói hồi đều qua qdisc của
# `lo`, nên netem áp delay HAI LẦN cho một round-trip: `delay 10ms` -> RTT 20.157ms,
# `delay 20ms` -> RTT 40.201ms (đo được, xem diary/phase0.md). Target này từng đặt
# 20ms và nếu không kiểm bằng ping thì mọi diary từ phase 5 đã sai nhãn 2x.
RTT_TARGET_MS ?= 20
rtt-up:
	sudo tc qdisc add dev lo root netem delay $$(( $(RTT_TARGET_MS) / 2 ))ms
	@echo "== RTT phải ĐO, không được suy từ tham số netem =="
	ping -c 5 -i 0.2 127.0.0.1 | tail -1
	@echo "== RTT trung bình ở trên phải ≈ $(RTT_TARGET_MS)ms. Ghi con số ĐO ĐƯỢC vào diary. =="

rtt-down:
	sudo tc qdisc del dev lo root
	tc qdisc show dev lo

# ---------------------------------------------------------------------------
# Phase 0: 5 sự thật vật lý (syscall, RTT, coordinated omission, RSS/conn, trần OS)
# ---------------------------------------------------------------------------
# Chụp môi trường TRƯỚC mọi thí nghiệm. Không có file này thì số đo không so được
# giữa hai máy, và không so được với chính máy này ba tháng sau.
envcap:
	./scripts/envcap.sh

# Chạy trọn phase 0, mỗi thí nghiệm một file trong bench/<host>-<tag>-*.txt
phase0:
	./scripts/phase0-run.sh rtt0

# -bufio=false là BẮT BUỘC ở đây, không phải tuỳ chọn: với bufio=true thì
# bufio.Writer gộp 2 lần Write thành 1 syscall, tỉ số G1 ra 1.02x thay vì 1.41x,
# và spike Nagle 44ms bị che sạch (12.4µs). Xem diary/phase0.md, mục G1.
netlab:
	@echo "== ulimit -n = $$(ulimit -n) — ghi con số này vào diary =="
	go run ./cmd/netlab -all -bufio=false

# Cùng lệnh trên, nhưng có RTT 20ms. Tỉ số giữa hai lần chạy MỚI là kết luận.
# Đây là món nợ P0-1: chưa chạy được vì thiếu sudo.
# ĐÃ TRẢ (nợ P0-1). Giữ lại để chạy lại được: 2.00x ở RTT 20.157ms và 40.201ms.
netlab-rtt:
	./scripts/pay-P0-1.sh

# Cùng một rate danh nghĩa, đo bằng closed-loop và open-loop -> coordinated omission
# -rate 885 = 1.2 x capacity ĐO ĐƯỢC (737 rps, P0-2). Bản cũ 1200 là 1.63x mà tưởng 1.2x.
netlab-omission:
	go run ./cmd/netlab -exp omission -rate 885 -duration 10s -svc 1ms -workers 1 -bufio=false

# Hai lần chạy, HIỆU của chúng là giá riêng của bufio (đo được 10.31 KB/conn)
netlab-mem:
	go run ./cmd/netlab -exp mem -conns 10000 -bufio=true
	go run ./cmd/netlab -exp mem -conns 10000 -bufio=false

# G6 KHÔNG chạy được qua 127.0.0.1: tcp_tw_reuse=2 nghĩa là "chỉ bật cho loopback",
# kernel tái dùng TIME_WAIT ngay và port exhaustion biến mất. Phải dùng IP của một
# interface thật. Xem diary/phase0.md, mục G6.
netlab-limits:
	@ip=$$(ip -4 -o addr show scope global | awk 'NR==1{print $$4}' | cut -d/ -f1); \
	echo "== dùng IP không-loopback: $$ip (tcp_tw_reuse=$$(cat /proc/sys/net/ipv4/tcp_tw_reuse)) =="; \
	go run ./cmd/netlab -exp limits -duration 40s -bufio=false -addr $$ip:9200

# Đường sang máy khác. Trả luôn cả P0-1 (RTT thật) và P0-3 (không chung CPU).
netlab-server:
	go run ./cmd/netlab -role server -addr :9000 -workers 1 -bufio=false

netlab-client:
	@test -n "$(ADDR)" || (echo "cần ADDR=<ip>:9000" && exit 2)
	ping -c 5 $(firstword $(subst :, ,$(ADDR))) | tail -2
	go run ./cmd/netlab -role client -addr $(ADDR) -all -bufio=false -tag "lan"

# ---------------------------------------------------------------------------
# Phase 1: framing — TCP không có ranh giới tin nhắn
# ---------------------------------------------------------------------------
framelab:
	go run ./cmd/framelab -mode dribble    # 1 byte mỗi 10ms
	go run ./cmd/framelab -mode coalesce   # 3 frame trong 1 lần Write
	go run ./cmd/framelab -mode oversize   # length = 0xFFFFFFFF, phải bị từ chối TRƯỚC make()

# P1-5: frame 1 (15 byte) bị cắt giữa header (7) hoặc giữa payload (12) bởi Read thô
framelab-split:
	go run ./cmd/framelab -mode coalesce -rawbuf 7
	go run ./cmd/framelab -mode coalesce -rawbuf 12

# P1-3/P1-6: bài 4 GiB ảo. Từng chậm 5-8s ngẫu nhiên — không phải vì -race mà vì
# span đè lên trang bẩn ⇒ runtime zero cả 4 GiB. Test giờ FreeOSMemory trước make.
test-huge:
	go test ./internal/frame/ -run TestPayloadOverUint32 -count=1 -v

# P1-6: cho THẤY make(4 GiB) = 4ms trên heap sạch và ~7s + RSS 4 GiB sau 64 MB rác bẩn.
needzerolab:
	go run ./cmd/needzerolab

# Tắt phòng tuyến (checkLength luôn trả nil) -> bộ test PHẢI đỏ, và đỏ đúng
# chỗ: TestCapBeforeAlloc báo decoder cấp phát ~4 GiB. Nếu XANH là thất bại.
framelab-nodefense:
	@echo "== bài phản chứng: lệnh này PHẢI đỏ =="
	! go test ./internal/frame/ -run 'TestCapBeforeAlloc|TestCustomMax' -count=1 -tags nodefense

fuzz-frame:
	go test ./internal/frame/ -run '^$$' -fuzz FuzzFrameDecode -fuzztime 120s -fuzzminimizetime 1s

# ---------------------------------------------------------------------------
# Phase 2: HTTP/1.1 engine
# ---------------------------------------------------------------------------
httplab:
	go run ./cmd/httplab -mode parse -file testdata/requests/basic.txt
	go run ./cmd/httplab -mode slowloris -interval 50ms -header-timeout 300ms
	go run ./cmd/httplab -mode response

# Bài I2 của phase 2: chunk-size FFFFFFFF không làm reader cấp phát; điểm đo
# đối chứng (decoder ngây thơ) cấp phát đúng 4 GiB. Cần ~4 GiB RAM ảo.
test-chunkalloc:
	go test ./internal/httpx/ -run TestChunkSizeDoesNotAllocate -v -count=1

fuzz-http:
	go test ./internal/httpx/ -run '^$$' -fuzz FuzzReadRequest -fuzztime 120s -fuzzminimizetime 1s

# Bằng chứng MẠNH HƠN "không panic": cùng byte string, parser mình và net/http
# phải đồng ý về SỐ BYTE của body. Lệch một byte = một lỗ hổng smuggling.
difffuzz:
	go test ./internal/httpx/ -run '^$$' -fuzz FuzzAgainstNetHTTP -fuzztime 300s -fuzzminimizetime 1s

# P2-1: fuzz CÓ CẤU TRÚC — head cố định, chỉ đột biến dòng chunk-size. Trên bản
# khoan dung whitespace nó tìm ra lệch " 3" trong 0.10s; fuzz phẳng 390s không.
difffuzz-chunk:
	go test ./internal/httpx/ -run '^$$' -fuzz FuzzChunkLineAgainstNetHTTP -fuzztime 120s -fuzzminimizetime 1s

# ---------------------------------------------------------------------------
# Phase 3: vertical slice — curl xuyên proxy
# ---------------------------------------------------------------------------
# Terminal 1: make upstream · Terminal 2: make proxylab
upstream:
	go run ./cmd/upstream -addr :8081

# 3 curl phải đúng byte, KHÔNG treo. Không thấy "chunk 4" hoặc curl đứng > 1s ⇒ bẫy #2.
# Build trước rồi chạy binary: `go run … &` sinh tiến trình con, kill pid của
# `go run` KHÔNG giết edgegate ⇒ mồ côi giữ cổng 8080 (phát hiện turn 2 phase 3).
proxylab:
	go build -o bin/edgegate ./cmd/edgegate
	./bin/edgegate -config config/dev.json & echo $$! > /tmp/edgegate.pid; sleep 1
	curl -sS -i --max-time 5 http://localhost:8080/hello
	curl -sS -i --max-time 5 -X POST -d 'xin chao' http://localhost:8080/echo
	curl -sS -i --max-time 5 http://localhost:8080/chunked      # response chunked: KHÔNG được treo
	curl -sS -i --max-time 5 http://localhost:8080/eof          # body tới EOF ⇒ client thấy chunked (D3)
	-kill $$(cat /tmp/edgegate.pid); rm -f /tmp/edgegate.pid

# Số đo G2/G3/G4: tự chạy upstream + proxy (2 lần: -nodelay=true / false).
# Closed-loop, 1 conn, cùng máy — chỉ đo OVERHEAD TƯƠNG ĐỐI của hop L7.
proxybench:
	go build -o bin/edgegate ./cmd/edgegate && go build -o bin/upstream ./cmd/upstream && go build -o bin/proxylab ./cmd/proxylab
	./bin/upstream -addr :8081 & echo $$! > /tmp/upstream.pid; sleep 0.5
	./bin/edgegate -config config/dev.json & echo $$! > /tmp/edgegate.pid; sleep 0.5
	./bin/proxylab -mode overhead -n 2000
	./bin/proxylab -mode keepalive -n 2000
	./bin/proxylab -mode nagle -n 100 -chunkms 0 -label "[nodelay=true]"
	-kill $$(cat /tmp/edgegate.pid); sleep 0.5
	./bin/edgegate -config config/dev.json -nodelay=false & echo $$! > /tmp/edgegate.pid; sleep 0.5
	./bin/proxylab -mode nagle -n 100 -chunkms 0 -label "[nodelay=false]"
	-kill $$(cat /tmp/edgegate.pid) $$(cat /tmp/upstream.pid); rm -f /tmp/edgegate.pid /tmp/upstream.pid

# Tắt hai phòng tuyến (drain body khi upstream chết; framing thay io.Copy thô)
# ⇒ TestDrainOnUpstreamDown và TestRawCopyTrap PHẢI đỏ. Xanh là thất bại.
proxylab-nodefense:
	@echo "== bài phản chứng: lệnh này PHẢI đỏ =="
	! go test ./internal/proxy/ -run 'TestDrainOnUpstreamDown|TestRawCopyTrap' -count=1 -tags nodefense

# ---------------------------------------------------------------------------
# Phase 4: smuggling. Bài phản chứng BẮT BUỘC ĐỎ.
# ---------------------------------------------------------------------------
# Bộ testdata/smuggle/*.txt qua HAI tầng: parser (httpx: status, body, phần dư
# = ranh giới) và proxy thật (proxy: status + đóng + không trả lời byte pipelined).
# TestSmugglingOracle in bảng "mình strict hơn net/http ở đâu" (G2 phase 4).
smugglelab:
	go test ./internal/httpx/ -run 'TestSmuggling' -v -count=1
	go test ./internal/proxy/ -run 'TestSmugglingE2E|TestXFF|TestAbsoluteForm' -v -count=1

# Tắt 7 phòng tuyến (internal/httpx/defense_nodefense.go) -> bộ test PHẢI fail.
# Nếu vẫn xanh thì bộ test không chứng minh gì cả. XANH ở đây là một thất bại.
smugglelab-nodefense:
	@echo "== bài phản chứng: lệnh này PHẢI đỏ =="
	! go test ./internal/httpx/ -run 'TestSmuggling$$' -count=1 -tags nodefense
	@echo "== và e2e cũng phải đỏ =="
	! go test ./internal/proxy/ -run TestSmugglingE2E -count=1 -tags nodefense

# ---------------------------------------------------------------------------
# Phase 5: connection pool. Hai con số, và chúng rất khác nhau — báo cáo bằng
# ms và RTT TIẾT KIỆM / REQUEST, không phải tỉ số (phase 0 G2/G3).
# cmd/poollab chạy in-process: upstream fixture + proxy (pool off rồi on) + client raw.
# ---------------------------------------------------------------------------
poollab:
	go run ./cmd/poollab -pool both -n 2000

poollab-rtt: rtt-up
	-go run ./cmd/poollab -pool both -n 200
	$(MAKE) rtt-down

# Tắt kiểm "sạch" (poolCheckClean=false): connection còn đuôi body của client A
# về pool, client B đọc được nó. Lệnh này PHẢI đỏ.
poollab-nodefense:
	@echo "== bài phản chứng: lệnh này PHẢI đỏ =="
	! go test ./internal/proxy/ -run 'TestDirtyConnNotPooled' -count=1 -tags nodefensepool

# ---------------------------------------------------------------------------
# Phase 6: load balancing
# ---------------------------------------------------------------------------
lblab:
	go run ./cmd/lblab -algos rr,leastconn,p2c,chash -n 20000

# G1/G2/G4: b0 chậm 10x, b1 trả 503 nhanh 30 %. Chạy hai lần: outlier tắt (least-conn dồn vào node
# lỗi nhanh lộ rõ) rồi mặc định. Cột share là phần tải mỗi backend nhận; đừng chỉ đọc p99.
lblab-skew:
	go run ./cmd/lblab -algos rr,leastconn,p2c,chash -skew slow=10x,err=30% -n 20000 -outlier=false
	go run ./cmd/lblab -algos rr,leastconn,p2c,chash -skew slow=10x,err=30% -n 20000

# G3 — chỗ P2C thua: b2 đổi nhanh→chậm 10x ở giữa bài; p2c-slow (tau 30 s) phản ứng chậm hơn least-conn.
lblab-flap:
	go run ./cmd/lblab -algos leastconn,p2c,p2c-slow -flap -n 20000

# Phản chứng G6: EWMA decay theo số request thay vì thời gian ⇒ node hồi phục không bao giờ được chọn lại.
lblab-nodefense:
	! go test ./internal/lb/ -run 'TestP2CRecovers' -count=1 -tags nodefenselb

# ---------------------------------------------------------------------------
# Phase 7: resiliency
# ---------------------------------------------------------------------------
slowlab:
	go run ./cmd/slowlab -conns 500 -byte-every 10s -probe

ratelab:
	go run ./cmd/ratelab -ips 100 -rate 50 -burst 10
	@echo "== và thử bypass bằng header giả: PHẢI bị chặn =="
	go run ./cmd/ratelab -spoof-xff

chaoslab:
	go run ./cmd/chaoslab -duration 60s -kill -slow -flap
	@echo "== invariant: goroutine và connection phải về mức nền =="
	ss -tan | grep -c ESTAB

# ---------------------------------------------------------------------------
# Phase 8: TLS + SNI
# ---------------------------------------------------------------------------
tlslab:
	./scripts/gen-certs.sh
	go run ./cmd/edgegate -config config/tls.json &
	sleep 1
	curl -sS -k --resolve a.test:8443:127.0.0.1 https://a.test:8443/ -w '\n%{ssl_verify_result}\n'
	curl -sS -k --resolve b.test:8443:127.0.0.1 https://b.test:8443/
	-pkill -f 'cmd/edgegate'

# ---------------------------------------------------------------------------
# Phase 9: performance
# ---------------------------------------------------------------------------
perflab:
	go test ./internal/... -run '^$$' -bench . -benchmem -count=5 | tee bench/perf-$$(date +%F).txt
	@echo "== ghi allocs/op TRƯỚC và SAU sync.Pool vào diary/phase9.md =="

# Giá phải trả của L7: io.Copy (splice) vs parse-and-reserialize, payload 10MB
epolllab:
	go run ./cmd/epolllab -mode netpoller -conns 10000 -report-rss
	go run ./cmd/epolllab -mode epoll     -conns 10000 -report-rss -reuseport

bench-vs-nginx:
	./scripts/bench-vs-nginx.sh    # 3 cột: EdgeGate / nginx / httputil.ReverseProxy

check: fmt vet test
	@echo "== nợ kỹ thuật chưa trả =="
	@grep -c '^### ' docs/debts.md

clean:
	rm -rf bin/ *.prof *.test
