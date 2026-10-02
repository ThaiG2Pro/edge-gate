# Phase 10 — log thô

> Bản biên tập: [`phase10.md`](phase10.md). File này ghi theo thứ tự thời gian, kể cả ngõ cụt.

## §0 Mang từ phase 0-9 sang (đọc lại trước khi gõ)

1. Số tuyệt đối trên WSL2 nhiễu (phase 9: load nền 3-8 từ session khác) ⇒ chốt tỉ số cùng lượt, xen kẽ.
2. Phase 9: hand-off goroutine (ReverseProxy 0.68 ctxsw/req) là thứ đắt nhất của một proxy Go ⇒ D3 của h2 cố tình
   có hand-off (goroutine đọc ⇄ goroutine stream) — G8 đo nó.
3. Phản chứng phải đỏ — và phải kiểm rằng nó đỏ **vì đúng lý do** (phase 9: hai phản chứng hỏng vì IdleTimeout và
   `sync.Pool` dưới `-race`).
4. Không ghi giờ ước lượng; chỉ mốc từ `date`.

## §1 Turn 1 — 2026-10-01 17:46 → 18:16 (commit 2026-10-02 11:48)

- 17:46 đọc ROADMAP phase 10, `proxy.go:serveConn`, `forward.go`, `httpx` (Request/Header/WriteHead). Công cụ: có
  `curl` 8.5.0 với libnghttp2 1.59 (`--http2-prior-knowledge`); **không** `h2spec`, `h2load`, `nghttp`.
- Go 1.26 `net/http` có `Protocols.SetUnencryptedHTTP2` ⇒ client h2c độc lập dùng được trong `_test.go`/lab mà không
  kéo `golang.org/x/net`.
- 17:48-17:50 viết `phase10.md`: 7 câu hỏi, G1-G8, D1-D10. Chưa có file `.go`.
- (1) **HPACK.** Bảng Huffman chép từ `$GOROOT/src/vendor/golang.org/x/net/http2/hpack/tables.go` (chỉ phần dữ liệu,
  ghi nguồn + license). Bảng tĩnh viết tay rồi **diff** với RFC 7541 Appendix A tải từ rfc-editor.org:
  `STATIC_TABLE_MATCH` (61/61).
  - Vector Appendix C: lần đầu nhờ agent tải RFC — agent trả **tóm tắt** (WebFetch chỉ có ~39 KB đầu nguyên văn,
    Appendix nằm sau). Bỏ, `curl -o rfc7541.txt` rồi trích bằng `testdata/gen_vectors.py` ⇒ `rfc_vectors_test.go`
    (16 vector, kèm "Table size" RFC in).
  - Kết quả lần đầu: decode 16/16 đúng ngay; encoder lệch ở **C.6.2**: `got 4803333037… want 4883640eff…` — "307" dài
    3 byte thường, 3 byte Huffman; ta chọn Huffman chỉ khi **ngắn hơn**, RFC chọn khi **bằng**. Sửa `<` → `<=` ⇒
    C.4 và C.6 khớp từng byte (Huffman, chỉ số động, đuổi mục bảng 256 đều đúng).
  - `FuzzDecode` 30 s: 419 345 exec, 140 interesting, không panic, round-trip decode→encode→decode giữ list.
- (2) **Frame + conn.** `frame.go` (trần Length kiểm trước payload), `request.go` (pseudo-header + D6 c),
  `conn.go` (D3-D6), `client.go` (RawClient — chỉ để tạo byte xấu). Bổ sung trước khi chạy test: `BodyTimeout` (body
  stream không có deadline socket vì connection còn phục vụ stream khác — I3) và chờ window có `WriteTimeout`
  (client không bao giờ WINDOW_UPDATE).
  - Đọc RFC 9113 §8.2.1-8.2.2 (bản tải về) để viết luật field: tên không chứa 0x00-0x20, A-Z, 0x7f-0xff; value không
    NUL/CR/LF, không khoảng trắng hai đầu; `connection`/`proxy-connection`/`keep-alive`/`transfer-encoding`/`upgrade`
    ⇒ malformed; `te` chỉ `trailers`.
  - `h2_test.go` 15 test xanh dưới `-race` lần đầu. `-tags nodefense10`: 5 đỏ — 4 đúng dự kiến (malformed, CL,
    Rapid Reset 5000 handler đồng thời, CONTINUATION đệm 4 194 310 byte), **1 sai**: `TestRefusedKeepsHPACKInSync`
    đỏ vì test, không vì phòng tuyến — `expect(1)` bỏ qua frame không khớp nên nuốt HEADERS của stream 3 khi nó tới
    trước. Sửa: chờ cả hai bất kể thứ tự (và decode để bảng động client không lệch). Xanh 3/3 dưới nodefense10.
- (3) **Proxy.** `isH2Preface` so **từng phần** (request h1 ngắn bắt đầu bằng 'P' không được chờ đủ 24 byte);
  `serveH2` đọc qua cùng `br`; `serveH2Stream`/`h2exchange` dùng lại `forwardedHeaders`, `balancerFor`, `Pick`,
  `poolFor`, retry D4. `Stream.SetCancel`: RST ⇒ đóng connection upstream.
  - Hệ quả thấy ngay khi viết: SetCancel làm slot về nhanh ⇒ D6 a một mình không chặn tốc độ việc bơm sang upstream ⇒
    **thêm D6 a′** (18:08, trước khi đo): > 2×MAX_CONCURRENT_STREAMS RST/1 s ⇒ GOAWAY ENHANCE_YOUR_CALM.
  - `phase10_test.go` xanh. `-tags nodefense10`:
    - H2.CL đỏ đúng: upstream thấy `["POST /" "GET /smuggled"]`.
    - H2.CRLF **xanh** ở cả hai build: `httpx.appendWire` (phase 4) từ chối CR/LF/NUL ⇒ lớp h1 chặn. H2.TE cũng xanh:
      `StripHopByHop` (phase 4) xoá TE. Không phải lỗi test — là **phòng tuyến nhiều lớp**; ghi lại, đổi nhãn ca.
    - Rapid Reset qua proxy **xanh nhầm** (0 request upstream ở cả hai build): HEADERS+RST liền nhau ⇒ RST tới trước
      khi proxy đụng upstream. Sửa: lô 100 HEADERS, chờ 5 ms, RST cả lô ⇒ 193 vs 877. Chạy 5 lần: nodefense10 thấy
      502-2353 — có lần < 400 (ngưỡng) ⇒ phản chứng không ổn định. Chờ 20 ms ⇒ mặc định 136-263, nodefense10
      4672-4974 (5/5).
- (4) `cmd/edgegate -h2c`, `config/h2.json` (port 18093), `cmd/h2lab` 5 mode, Makefile `h2-bins`, `h2lab`,
  `h2lab-flow`, `h2lab-tcphol`, `h2spec`, `h2-nodefense`. Smoke chạy đủ 5 mode, tiến trình con dừng sạch (`ss`,
  `pgrep -x` rỗng). **Số smoke không phải số đo** (n nhỏ, không taskset client, load nền) — turn 2 đo lại; một số
  smoke đáng ngờ để turn 2 điều tra: `tcphol` RTT 0 h2/h1 p50 ≈ 3.4x (G5 dự đoán 0.67-1.5 khi không mất gói).
- 18:16 suite đầy đủ `-race` xanh; `make h2-nodefense` đỏ đúng 6 test; `nodefense9` vẫn đỏ đúng 2 test phase 9.

### Nợ dự kiến (chốt ở turn 3)

- ALPN `h2` trên TLS (D7: chỉ h2c).
- Rate limit / shed phase 7 chưa áp cho stream h2 (D8).
- Drain (phase 7 D8) chưa gửi GOAWAY NO_ERROR cho connection h2 — Close đóng cứng.
- Trailer request/response h2 bị bỏ (như P3-1).
- Upload body h2 → upstream luôn chép qua bufio (không splice) — h2 không splice được về bản chất (framing).

## §2 Turn 2 — 2026-10-02 11:48 → 13:40

- 11:48 suite vẫn xanh trên đĩa (phiên mới). 11:50 cài h2spec: `GOBIN=$HOME/go/bin go install
  github.com/summerwind/h2spec/cmd/h2spec@latest` (`GOBIN` mặc định trỏ vào thư mục mise — đặt tường minh); `--version`
  in `2.0.0`, module cache là `v2.2.1+incompatible`. `go.mod` không đổi.
- 11:51 h2spec lần đầu 143/145. Đọc source h2spec (`http2/3_5_http2_connection_preface.go`: gửi
  `INVALID CONNECTION PREFACE\r\n\r\n`, chờ đóng; `http2/5_1_stream_states.go`: HEADERS, **client** RST, DATA ⇒ chờ
  STREAM_CLOSED) và RFC 9113 §5.1 "closed" (tải lại — scratchpad phiên trước đã mất).
- Sửa 5.1/8 bằng `peerReset`. `TestDataAfterRST` viết cả ca ngược (ta RST rồi DATA đang bay) ⇒ đỏ: stream đã rời map
  ⇒ RST STREAM_CLOSED lần hai. Sửa: `localReset` FIFO 256. 11:53 h2spec 144/145 ×3.
- h2spec trên `nodefense10`: 139/145 — 5 ca thêm, cả 5 của `validateDowngrade`; 0 ca về Rapid Reset/CONTINUATION.
- **Lỗi thao tác:** dọn tiến trình bằng `pkill -f -x "./bin/edgegate-nodefense10 -config config/h2.json"` — trái luật
  "chỉ `pkill -x`" (phase 9: `pkill -f` giết shell). Lần này khớp đúng chuỗi nên shell sống; từ đó chỉ `pkill -x`
  (comm cắt 15 ký tự: `edgegate-nodefe`).
- 11:54 G1 ×3 tất định. Load nhảy 13.15 — `ps`: `MainThread` pid 128249 179 % CPU (session khác). Không đụng; đo xen
  kẽ, đọc tỉ số.
- 11:54 G3 ×3. 11:56 G8 ×3 ⇒ ctxsw h2 **không** cao hơn ⇒ thêm cờ `-h2conns/-h2streams/-h1conns`, đo 1×1 ⇒ h2 1.7x
  ctxsw, ~2x CPU. Chấm G8 theo cấu hình đăng ký (sai), 1×1 ghi là phụ.
- 11:58 G4/G5 RTT 0: tcphol h2/h1 2.82x không mất gói ⇒ nghi cài đặt. Thêm `-ref` (server `net/http` h1+h2c trực tiếp)
  ⇒ 1.89-2.97x ⇒ không phải cài đặt; là "một socket".
- 13:31 người dùng bật `netem delay 20ms` (ping 40.3 ms) ⇒ G4. 13:33 đổi `delay 10ms` ⇒ G5 đối chứng. 13:33 đổi
  `delay 10ms loss 2%` ⇒ G5. 13:36 người dùng tháo; kiểm: `qdisc noqueue`, ping 0.047 ms, không tiến trình sót.
- 13:37 G6/G7 hai build. Suite `-race` xanh, `make h2-nodefense` đỏ 6.

## §3 Turn 3 — 2026-10-02 13:43 → 13:55

- Đọc RFC 9113 §5.2, §10.5, §3.1 (bản tải sáng nay), mô tả CVE-2023-44487 / CVE-2024-27316 qua API cveawg.mitre.org.
  §10.5 chỉ ra một lỗ mình chưa phòng: WINDOW_UPDATE nhỏ giọt ⇒ P10-3.
- Trả nợ drain (P10-1): `TestH2Drain` viết trước, chạy trên code cũ ⇒ `Drain 3.001s, forced=2` (đỏ). Cài
  `Conn.Shutdown` (GOAWAY NO_ERROR), `connState.h2`, Dekker với `closeIdle` ⇒ `202ms, forced=0` ×3 `-race`.
  Suite `-race` xanh; `nodefense10` đỏ 6, `nodefense9` đỏ 2; h2spec vẫn 144/145.
- Khi viết sổ nợ thấy P10-2 không phải "thiếu tính năng" mà là **đường vòng**: rate limit phase 7 chỉ chạy trên h1 ⇒
  client nói h2c trên cùng port là thoát. Ghi ưu tiên cao nhất.

## §4 Sau turn 3 — trả P10-2 (2026-10-02 13:52 → 13:55)

- Test viết trước, chạy trên code cũ: `TestH2RateLimit` ⇒ `h2: map[200:6] … h1 sau đó: 200`, `RateLimited:0`
  (sáu request h2 không chạm bucket); `TestH2Shed` ⇒ `/hello` 200 trong lúc `/slow` giữ slot duy nhất, `Inflight:0`.
- Tách `admitDecision` khỏi `admit`; h2 gọi nó sau head, trước `Pick`. Sau sửa ×3 `-race`: `map[200:2 429:4]`, h1 429,
  `/hello` 503. Suite `-race` xanh; `nodefense7` đỏ đúng 3 test như commit trước (so bằng worktree của HEAD).

## §5 P10-3 — 2026-10-02 13:58 → 14:00: nợ đóng bằng số đo

- Kế hoạch ghi trong sổ nợ ("chờ window ≥ 1 KiB") có lỗ trước khi chạy: client hợp lệ với window nhỏ
  (`TestFlowControlStreamWindow` dùng INITIAL 0 + update 7) sẽ deadlock; hạ ngưỡng theo window client công bố thì
  attacker đặt 0 là né. Đổi hướng: đếm frame bị ép nhỏ ⇒ GOAWAY (khuôn D6 a′). Viết test "phải GOAWAY" trước.
- Chạy trên code cũ: `DATA frame = 984, byte = 20000`, không GOAWAY. Đỏ — nhưng 984, không phải 20 000 như sổ nợ tính.
  Thử client flush mỗi 1/10/100 update: 1 208 / 878 / 937 frame.
- Vì sao: D3 ghi đồng bộ; `WriteData` lấy hết credit đang dồn mỗi frame ⇒ update đến trong lúc ghi gộp vào frame sau.
  Số frame ≤ số update ⇒ không khuếch đại. Phòng tuyến cho rủi ro không tồn tại chỉ còn lại giá (chặn nhầm client
  window nhỏ) ⇒ không thêm. Viết lại test thành chốt bất biến "frame ≤ update + 64" (×2 nhịp flush, ×3 `-race`:
  2 822-3 350 frame). Suite `-race` xanh, `make h2-nodefense` đỏ 6.

## §6 P10-4 — 2026-10-02 14:01 → 14:19: đo trước, có khuếch đại thật ⇒ sửa cấu trúc

- Bài học P10-3 áp ngay: thêm `Stats.Flushes` + `h2lab -mode flood` đo TRƯỚC. Có khuếch đại thật (khác P10-3): ACK
  PING/RST không gộp được bằng window ⇒ 100 PING trong một lần ghi = 100 Flush; SETTINGS lặp INITIAL_WINDOW_SIZE ×
  mọi stream = 1.8-2.8 ms CPU mỗi frame (`bench/p10-flood-before.txt`).
- Sửa (a) gộp Flush cho phản hồi của goroutine đọc, (b) SETTINGS cộng dồn. Hai lỗi của chính mình trên đường:
  - Lần đầu kiểm "còn frame" bằng `Buffered() ≥ 9`: payload chưa tới thì ACK vẫn kẹt trong bufio ⇒ đổi sang trọn
    frame (`frameBuffered`).
  - Rồi `Peek(9)` khi có < 9 byte ⇒ **chặn đọc socket** ⇒ suite proxy đỏ. Sửa: kiểm `Buffered()` trước Peek.
- `TestSettingsCollapse` đỏ lần đầu vì test giữ 100 stream = hết MAX_CONCURRENT_STREAMS ⇒ stream đo ngữ nghĩa bị
  REFUSED ("0 byte") — giữ 99.
- `TestIdleClosedUpstream` đỏ 2 lần trong full suite ⇒ không đổ lỗi, đo: riêng 10/10 xanh cả hai bản, full suite
  3 + 3 lần sạch ⇒ chập chờn có sẵn (sleep 20 ms chờ FIN) ⇒ nợ P10-9.
- Sau: PING 111 Flush / 100 000, SETTINGS 51-106 µs/frame; `make h2-nodefense` đỏ 8; h2spec 144/145.

## §7 P10-9 — 2026-10-02 15:05 → 15:13: test chờ thời gian → chờ sự kiện

- Tái hiện trước: 6 × `yes` một mình ⇒ 0/20 đỏ (load chưa kịp lên, và không giống full suite). Thêm 5 package test
  `-race` chạy song song ⇒ 1/30 đỏ (`probe-POST-body` 46/50, `DropDirty:4`). Tải phải giống tải thật mới tái hiện.
- Sửa: upstream báo sau `Close`, test chờ tín hiệu + 2 ms. Cùng bài tải: 0/30, rồi 0/60 ở load 7.06; full suite 3/3
  sạch. Bài học lặp lại lần thứ ba của sleep này (3 ms phase 5 → 20 ms phase 8 → sự kiện): nới số chỉ dời điểm gãy.
