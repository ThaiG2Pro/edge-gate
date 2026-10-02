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
