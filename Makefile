.PHONY: all fmt vet test bench check clean \
	envcap phase0 netlab netlab-rtt netlab-omission netlab-mem netlab-limits \
	netlab-server netlab-client \
	framelab fuzz-frame \
	httplab fuzz-http difffuzz difffuzz-chunk \
	proxylab proxybench upstream \
	smugglelab smugglelab-nodefense \
	poollab poollab-rtt poollab-idlerace poollab-nodefense \
	lblab lblab-even lblab-skew lblab-flap lblab-recover lblab-nodefense \
	chaoslab slowlab ratelab deadlinelab slowlab-nodefense ratelab-nodefense breakerlab-nodefense retrylab shedlab drainlab \
	tlslab tlslab-nodefense tlslab-rtt \
	perflab perflab-nodefense perf-bins idlelab l4l7lab pproflab bench-vs-nginx epolllab \
	h2-bins h2lab h2lab-flow h2lab-tcphol h2spec h2-nodefense \
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
	! go test ./internal/frame/ -run 'TestCapBeforeAlloc|TestCustomMax' -count=1 -tags nodefense1

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
# P-ops-1 (trả 2026-10-02): một recipe shell, trap EXIT dọn mọi tiến trình nền kể cả
# khi một bước giữa chừng lỗi (bản cũ: `-kill $$(cat pid)` ở cuối không chạy khi bước
# trước lỗi ⇒ upstream phase 3 còn sống tới phase 5). `go build` mỗi dòng một lệnh.
# PROXYLAB=false ⇒ giả lập bước lỗi để kiểm trap.
PROXYLAB ?= ./bin/proxylab
proxybench:
	go build -o bin/edgegate ./cmd/edgegate
	go build -o bin/upstream ./cmd/upstream
	go build -o bin/proxylab ./cmd/proxylab
	@set -e; UP=; PX=; \
	 trap 'kill $$PX $$UP 2>/dev/null; wait 2>/dev/null; true' EXIT; \
	 ./bin/upstream -addr :8081 & UP=$$!; sleep 0.5; \
	 ./bin/edgegate -config config/dev.json & PX=$$!; sleep 0.5; \
	 $(PROXYLAB) -mode overhead -n 2000; \
	 $(PROXYLAB) -mode keepalive -n 2000; \
	 $(PROXYLAB) -mode nagle -n 100 -chunkms 0 -label "[nodelay=true]"; \
	 kill $$PX; wait $$PX 2>/dev/null || true; \
	 ./bin/edgegate -config config/dev.json -nodelay=false & PX=$$!; sleep 0.5; \
	 $(PROXYLAB) -mode nagle -n 100 -chunkms 0 -label "[nodelay=false]"

# Tắt hai phòng tuyến (drain body khi upstream chết; framing thay io.Copy thô)
# ⇒ TestDrainOnUpstreamDown và TestRawCopyTrap PHẢI đỏ. Xanh là thất bại.
proxylab-nodefense:
	@echo "== bài phản chứng: lệnh này PHẢI đỏ =="
	! go test ./internal/proxy/ -run 'TestDrainOnUpstreamDown|TestRawCopyTrap' -count=1 -tags nodefense3

# ---------------------------------------------------------------------------
# Phase 4: smuggling. Bài phản chứng BẮT BUỘC ĐỎ.
# ---------------------------------------------------------------------------
# Bộ testdata/smuggle/*.txt qua HAI tầng: parser (httpx: status, body, phần dư
# = ranh giới) và proxy thật (proxy: status + đóng + không trả lời byte pipelined).
# TestSmugglingOracle in bảng "mình strict hơn net/http ở đâu" (G2 phase 4).
smugglelab:
	go test ./internal/httpx/ -run 'TestSmuggling' -v -count=1
	go test ./internal/proxy/ -run 'TestSmugglingE2E|TestXFF|TestAbsoluteForm|TestExpectContinue' -v -count=1

# Tắt 7 phòng tuyến (internal/httpx/defense_nodefense.go) -> bộ test PHẢI fail.
# Nếu vẫn xanh thì bộ test không chứng minh gì cả. XANH ở đây là một thất bại.
smugglelab-nodefense:
	@echo "== bài phản chứng: lệnh này PHẢI đỏ =="
	! go test ./internal/httpx/ -run 'TestSmuggling$$' -count=1 -tags nodefense4
	@echo "== và e2e cũng phải đỏ =="
	! go test ./internal/proxy/ -run TestSmugglingE2E -count=1 -tags nodefense4

# ---------------------------------------------------------------------------
# Phase 5: connection pool. Hai con số, và chúng rất khác nhau — báo cáo bằng
# ms và RTT TIẾT KIỆM / REQUEST, không phải tỉ số (phase 0 G2/G3).
# cmd/poollab chạy in-process: upstream fixture + proxy (pool off rồi on) + client raw.
# ---------------------------------------------------------------------------
poollab:
	go run ./cmd/poollab -pool both -n 2000

# P5-4: upstream đóng rỗi ngẫu nhiên 1-50 ms; POST 1 KiB; đếm 502 probe on/off.
poollab-idlerace:
	go run ./cmd/poollab -idlerace -n 10000 -conns 32

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

# P6-2: 4 backend giống hệt ⇒ mọi algo (trừ chash: khoá ngẫu nhiên) phải chia ≤ 1.2x, 10 lượt liền.
# p2c chạy ĐẦU TIÊN trong tiến trình: lúc đó mới có outlier khởi động (max 37-94 ms) — chạy sau rr/leastconn
# thì tiến trình đã ấm và ngay bản lỗi cũng xanh (lần đầu viết target mắc đúng bẫy này).
lblab-even:
	go build -o bin/lblab ./cmd/lblab
	@fails=0; for i in 1 2 3 4 5 6 7 8 9 10; do \
	   out=$$(./bin/lblab -algos p2c,rr,leastconn -n 20000 -max-share-ratio 1.2) || fails=$$((fails+1)); \
	   echo "$$out" | grep -E '^(p2c|FAIL)'; done; \
	 echo "lblab-even: $$fails/10 lượt vượt 1.2x"; test $$fails -eq 0

# G1/G2/G4: b0 chậm 10x, b1 trả 503 nhanh 30 %. Chạy hai lần: outlier tắt (least-conn dồn vào node
# lỗi nhanh lộ rõ) rồi mặc định. Cột share là phần tải mỗi backend nhận; đừng chỉ đọc p99.
lblab-skew:
	go run ./cmd/lblab -algos rr,leastconn,p2c,chash -skew slow=10x,err=30% -n 20000 -outlier=false
	go run ./cmd/lblab -algos rr,leastconn,p2c,chash -skew slow=10x,err=30% -n 20000

# G3 — chỗ P2C thua: b2 đổi nhanh→chậm 10x ở giữa bài; p2c-slow (tau 30 s) phản ứng chậm hơn least-conn.
# Turn 2: p99 không tách được thuật toán khi b2 còn > 1 % tải ⇒ đọc cột "share nửa sau". conns 4 để
# inflight ít thông tin, EWMA có tiếng nói. -recover (b2 chậm→nhanh) mới là chỗ P2C tau dài thua thật.
lblab-flap:
	go run ./cmd/lblab -algos leastconn,p2c,p2c-slow -flap -n 20000
	go run ./cmd/lblab -algos leastconn,p2c,p2c-slow -flap -n 20000 -conns 4

lblab-recover:
	go run ./cmd/lblab -algos leastconn,p2c,p2c-slow -flap -recover -n 20000
	go run ./cmd/lblab -algos leastconn,p2c,p2c-slow -flap -recover -n 20000 -conns 4

# Phản chứng G6: EWMA decay theo số request thay vì thời gian ⇒ node hồi phục không bao giờ được chọn lại.
lblab-nodefense:
	! go test ./internal/lb/ -run 'TestP2CRecovers' -count=1 -tags nodefenselb

# ---------------------------------------------------------------------------
# Phase 7: resiliency
# ---------------------------------------------------------------------------
# G1: bảy deadline, mỗi cái fire đúng status / đúng thời điểm.
deadlinelab:
	go test ./internal/proxy -run 'TestDeadline' -count=1 -v

# G2 (a) + G3: Slowloris 500 conn × 1 byte/10 s từ 127.0.0.2, probe 50 rps từ 127.0.0.1.
slowlab:
	go run ./cmd/slowlab -conns 500 -byte-every 10s -target null -duration 5s -hold 1s   # hiệu chuẩn bộ nhớ attacker
	go run ./cmd/slowlab -conns 500 -byte-every 10s
	go run ./cmd/slowlab -conns 500 -byte-every 10s -max-conns 256
	go run ./cmd/slowlab -conns 500 -byte-every 10s -max-inflight 64

# G2 (b): tắt HeaderTimeout — probe vẫn sống (không worker pool), connection sống mãi.
# G3 (a): + MaxConns 256 ⇒ chết. Không có '!' : đây là đo, không phải phản chứng.
slowlab-nodefense:
	go run -tags nodefense7 ./cmd/slowlab -conns 500 -byte-every 10s
	go run -tags nodefense7 ./cmd/slowlab -conns 500 -byte-every 10s -max-conns 256

# G4: 20 IP × 100 rps, bucket 50/s burst 10; spoof XFF từ peer không tin PHẢI bị chặn.
ratelab:
	go run ./cmd/ratelab -ips 20 -rate 50 -burst 10 -per-ip 100 -duration 5s
	go test ./internal/limit -run TestLimiterMemory -count=1 -v

# Phản chứng G4: khoá = XFF thô ⇒ spoof lọt; map không trần ⇒ heap phình. PHẢI đỏ.
ratelab-nodefense:
	! go run -tags nodefense7 ./cmd/ratelab -ips 20 -rate 50 -burst 10 -per-ip 100 -duration 5s
	! go test ./internal/limit -run TestLimiterMemory -count=1 -tags nodefense7

# Phản chứng G5: không half-open ⇒ cả loạt request rơi vào node vừa hết hạn eject. PHẢI đỏ.
breakerlab-nodefense:
	! go test ./internal/lb -run TestBreakerHalfOpen -count=1 -tags nodefense7

# G6: khuếch đại retry. Thường (budget 10 %) rồi retry mù (nodefense7).
retrylab:
	go run ./cmd/chaoslab -scenario retry -duration 10s -rate 1000 -drop 0.5
	go run -tags nodefense7 ./cmd/chaoslab -scenario retry -duration 10s -rate 1000 -drop 0.5

# G7: 2x capacity open-loop, không shed vs shed.
shedlab:
	go run ./cmd/shedlab -x 2 -duration 10s

# G8: 20 rolling restart qua SO_REUSEPORT, không retry vs client retry khi 0 byte.
drainlab:
	go run ./cmd/drainlab -restarts 20 -every 500ms
	go run ./cmd/drainlab -restarts 20 -every 500ms -grace 1s   # D8′ drain lười

# G9: test quyết định. Thoát mã 1 nếu invariant vỡ.
chaoslab:
	go run ./cmd/chaoslab -duration 60s -rate 500 -tick 300ms
	@echo "== ESTAB còn lại trên máy (chỉ để đối chiếu; chaoslab đã đếm fd của chính nó) =="
	-ss -tan | grep -c ESTAB

# ---------------------------------------------------------------------------
# Phase 8: TLS + SNI
# ---------------------------------------------------------------------------
# G1-G4 bằng binary thật + curl (cert sinh bằng cmd/gencert, không openssl, không key trong git).
# Luật vận hành: build một dòng riêng rồi mới '&'; dọn bằng pkill -x.
tlslab:
	go run ./cmd/gencert -out bin/certs -names a.test,b.test -serial 1
	go build -o bin/edgegate ./cmd/edgegate
	go build -o bin/upstream ./cmd/upstream
	./bin/upstream -addr 127.0.0.1:8081 -name A > bin/upstream-a.log 2>&1 &
	./bin/upstream -addr 127.0.0.1:8082 -name B > bin/upstream-b.log 2>&1 &
	./bin/edgegate -config config/tls.json > bin/edgegate-tls.log 2>&1 &
	sleep 1
	@echo "== G1: SNI a.test → A, b.test → B =="
	curl -sS --cacert bin/certs/ca.crt --resolve a.test:8443:127.0.0.1 https://a.test:8443/hello -o /dev/null -w '%{http_code} %{header_json}\n' | grep -o '^[0-9]* \|"x-upstream":\["[AB]"\]' | tr -d '\n'; echo
	curl -sS --cacert bin/certs/ca.crt --resolve b.test:8443:127.0.0.1 https://b.test:8443/hello -o /dev/null -w '%{http_code} %{header_json}\n' | grep -o '^[0-9]* \|"x-upstream":\["[AB]"\]' | tr -d '\n'; echo
	@echo "== G1: SNI lạ c.test ⇒ handshake hỏng =="
	-curl -sS --cacert bin/certs/ca.crt --resolve c.test:8443:127.0.0.1 https://c.test:8443/hello
	@echo "== G2: SNI a.test + Host: b.test ⇒ 421 =="
	curl -sS --cacert bin/certs/ca.crt --resolve a.test:8443:127.0.0.1 -H 'Host: b.test' https://a.test:8443/hello -o /dev/null -w '%{http_code}\n'
	@echo "== G3: ALPN (curl đề nghị h2,http/1.1) =="
	curl -sS --cacert bin/certs/ca.crt --resolve a.test:8443:127.0.0.1 --http2 https://a.test:8443/hello -o /dev/null -w 'http_version=%{http_version}\n'
	@echo "== G4: cert mới + SIGHUP ⇒ log serial mới, curl vẫn chạy =="
	go run ./cmd/gencert -out bin/certs -names a.test,b.test -serial 100 > /dev/null
	-pkill -HUP -x edgegate
	sleep 0.5
	-curl -sS --cacert bin/certs/ca.crt --resolve a.test:8443:127.0.0.1 https://a.test:8443/hello -o /dev/null -w '%{http_code} (CA mới)\n'
	grep -E 'TLS|SIGHUP' bin/edgegate-tls.log
	-pkill -x edgegate
	-pkill -x upstream
	go run ./cmd/tlslab -mode handshake -n 500

# Phản chứng G2/G5: route theo Host bỏ qua SNI; handshake lười dưới IdleTimeout. PHẢI đỏ.
tlslab-nodefense:
	! go test ./internal/proxy -run 'TestTLSDomainFronting|TestTLSHandshakeTimeout' -count=1 -tags nodefense8

# G7/G8 cần RTT thật: make rtt-up RTT_TARGET_MS=20 trước, make rtt-down sau.
tlslab-rtt:
	go run ./cmd/tlslab -mode rtt -n 50
	go run ./cmd/tlslab -mode upstream -n 100

# ---------------------------------------------------------------------------
# Phase 9: performance
# ---------------------------------------------------------------------------
perf-bins:
	go build -o bin/edgegate ./cmd/edgegate
	go build -tags nodefense9 -o bin/edgegate-nodefense9 ./cmd/edgegate
	go build -o bin/epolllab ./cmd/epolllab
	go build -o bin/perflab ./cmd/perflab
	go build -o bin/rpbaseline ./cmd/rpbaseline

# G1: allocs/op, B/op, ns/op trước (nodefense9) / sau — XEN KẼ, 3 lượt, ghim core 0-3.
perflab:
	@for r in 1 2 3; do for tag in nodefense9 ""; do echo "== lượt $$r tags=$${tag:-mặc định}"; \
	  taskset -c 0-3 go test ./internal/proxy -run '^$$' -bench 'ProxyKeepAlive|ProxyLarge' -benchmem -count 2 -benchtime 2s -tags "$$tag" | grep Benchmark; done; done

# Phản chứng D4: bản TRƯỚC phải đỏ đúng hai test (idle cầm bufio, không splice).
perflab-nodefense:
	! go test ./internal/proxy -run 'TestIdleReleasesBufio|TestSpliceBody$$' -count=1 -v -tags nodefense9

# G3: RSS mỗi connection rỗi của edgegate, trước / sau (D2).
IDLE_CONNS ?= 10000
idlelab: perf-bins
	@for b in edgegate-nodefense9 edgegate edgegate-nodefense9 edgegate; do \
	  ./bin/perflab -mode idle -conns $(IDLE_CONNS) -label $$b -up "taskset -c 2 bin/epolllab -impl epoll -loops 1" \
	    -spawn "taskset -c 0-1 bin/$$b -config config/bench.json" 2>&1 | grep -v 'epolllab:\|edgegate:'; done

# G4: giá của L7 — body 10 MiB, proxy ghim core 0, upstream core 2, client core 3.
L4L7_N ?= 100
l4l7lab: perf-bins
	@for r in 1 2 3; do for i in direct l4splice l4copy edgegate edgegate-splice; do \
	  taskset -c 3 ./bin/perflab -mode l4l7 -impl $$i -n $(L4L7_N) -proxy-cpus 0 -up-cpus 2 2>&1 | grep -v 'epolllab:\|edgegate:'; done; done

# G5: profile CPU 30 s của edgegate dưới open-loop, trước / sau. Profile lưu bench/.
PPROF_RATE ?= 8000
pproflab: perf-bins
	./scripts/pproflab.sh $(PPROF_RATE)

# G6 (RSS mỗi conn rỗi) + G7 (rps closed-loop): epoll tự viết vs netpoller.
epolllab: perf-bins
	@for i in epoll netpoller epoll netpoller; do \
	  ./bin/perflab -mode idle -conns $(IDLE_CONNS) -label $$i -addr 127.0.0.1:18100 \
	    -spawn "taskset -c 0-2 bin/epolllab -impl $$i -loops 3" 2>&1 | grep -v 'epolllab:'; done
	@for i in epoll netpoller epoll netpoller epoll netpoller; do \
	  taskset -c 3-5 ./bin/perflab -mode rps -conns 256 -duration 10s -label $$i -addr 127.0.0.1:18100 \
	    -spawn "env GOMAXPROCS=3 taskset -c 0-2 bin/epolllab -impl $$i -loops 3" 2>&1 | grep -v 'epolllab:'; done

# G8: EdgeGate / nginx / httputil.ReverseProxy — cần docker + image nginx:1.25-alpine.
bench-vs-nginx: perf-bins
	./scripts/bench-vs-nginx.sh    # 3 cột: EdgeGate / nginx / httputil.ReverseProxy

# --- Phase 10: HTTP/2 h2c -------------------------------------------------
h2-bins:
	go build -o bin/edgegate ./cmd/edgegate
	go build -tags nodefense10 -o bin/edgegate-nodefense10 ./cmd/edgegate
	go build -o bin/epolllab ./cmd/epolllab
	go build -o bin/h2lab ./cmd/h2lab

# G1 (HPACK), G3 (HOL tầng HTTP), G8 (CPU/req h2 vs h1; client core 4-5).
h2lab: h2-bins
	./bin/h2lab -mode hpack -n 100
	./bin/h2lab -mode hol -n 20
	taskset -c 4-5 ./bin/h2lab -mode cpu -rounds 3 -dur 10s

# G4: chạy hai lần — RTT 0, và sau `sudo tc qdisc add dev lo root netem delay 20ms`.
h2lab-flow: h2-bins
	./bin/h2lab -mode flow -rounds 3

# G5: chạy dưới netem `delay 10ms` và `delay 10ms loss 2%` (sudo, người dùng tự bật/tháo).
h2lab-tcphol: h2-bins
	./bin/h2lab -mode tcphol -n 20 -par 32

# G2: h2spec (go install github.com/summerwind/h2spec/cmd/h2spec@latest) chống edgegate -h2c.
H2SPEC ?= $(HOME)/go/bin/h2spec
h2spec: h2-bins
	@UP=; PX=; trap 'kill $$PX $$UP 2>/dev/null; wait 2>/dev/null; true' EXIT; \
	 ./bin/epolllab -impl epoll -loops 1 -addr 127.0.0.1:18100 & UP=$$!; \
	 ./bin/edgegate -config config/h2.json >/dev/null 2>&1 & PX=$$!; sleep 1; \
	 $(H2SPEC) -h 127.0.0.1 -p 18093 -o 5 | tail -40

# Phản chứng D6 + P10-4: bản không phòng tuyến phải đỏ đúng 8 test.
h2-nodefense:
	! go test ./internal/h2 ./internal/proxy -count=1 -tags nodefense10 \
	  -run 'TestMalformedStreamReset|TestContentLengthMismatch|TestRapidReset|TestContinuationFlood|TestControlFloodCoalesced|TestSettingsCollapse|TestH2Smuggle|TestH2RapidResetProxy' -v

check: fmt vet test
	@echo "== nợ kỹ thuật chưa trả =="
	@grep -c '^### ' docs/debts.md

clean:
	rm -rf bin/ *.prof *.test
