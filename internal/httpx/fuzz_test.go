package httpx

import (
	"bufio"
	"bytes"
	"io"
	"net/http" // CHỈ ở đây, chỉ làm oracle. Không có trong data path (xem ROADMAP).
	"runtime"
	"runtime/metrics"
	"strings"
	"testing"
)

var fuzzSeeds = []string{
	"GET / HTTP/1.1\r\nHost: h\r\n\r\n",
	"POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 5\r\n\r\nhello",
	"POST / HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nwiki\r\n5;x=y\r\npedia\r\n0\r\nT: v\r\n\r\n",
	"POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 3\r\nTransfer-Encoding: chunked\r\n\r\n1\r\nz\r\n0\r\n\r\n",
	"POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 2\r\nContent-Length: 2\r\n\r\nok",
	"POST / HTTP/1.0\r\nTransfer-Encoding: chunked\r\n\r\n1\r\nz\r\n0\r\n\r\n",
	"GET / HTTP/1.1\nHost: h\n\n",
	"GET / HTTP/1.1\r\nHost: h\r\n\r\nGET /2 HTTP/1.1\r\nHost: h\r\n\r\n",
	"POST / HTTP/1.1\r\nHost: h\r\nContent-Length: 5\r\n\r\nhel",
	"POST / HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\n\r\nFFFFFFFF\r\nabc",
	"POST / HTTP/1.1\r\nHost: h\r\nTransfer-Encoding : chunked\r\nContent-Length: 4\r\n\r\nabcd",
	"HEAD / HTTP/1.1\r\nHost: h\r\n\r\n",
}

// allocSample: fuzz chạy tuần tự trong một worker process, nên một sample dùng chung là đủ.
var allocSample = []metrics.Sample{{Name: "/gc/heap/allocs:bytes"}}

// allocBound: trần cấp phát cho một input — tuyến tính theo input cộng hai bufio.Reader
// và slack cho header map.
func allocBound(in []byte) uint64 { return uint64(16*len(in)) + 1<<16 }

// FuzzReadRequest: không panic, và các bất biến nội bộ giữ khi nhận request.
//
// P2-4: đo cấp phát hai tầng. Tầng 1 dùng runtime/metrics.Read (~0.5 µs); tầng 2 chỉ
// chạy khi tầng 1 vượt trần, đo lại bằng ReadMemStats (~80 µs, stop-the-world) lấy MIN
// của 3 lần. Cả hai bộ đếm đều là toàn tiến trình: goroutine của fuzz engine cấp phát
// trong cửa sổ đo sẽ cho dương tính giả (đã gặp: 506 KB cho input 44 byte, không tái
// hiện). Min của 3 lần lọc được nhiễu đó; một lần ReadMemStats mỗi input thì không lọc
// được mà lại làm fuzz chậm 2.7x.
func FuzzReadRequest(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}
	lim := DefaultLimits()
	f.Fuzz(func(t *testing.T, in []byte) {
		metrics.Read(allocSample)
		before := allocSample[0].Value.Uint64()
		fuzzReadRequestBody(t, in, lim)
		metrics.Read(allocSample)
		if allocSample[0].Value.Uint64()-before <= allocBound(in) {
			return
		}
		// Tầng 2: xác nhận.
		var ms runtime.MemStats
		min := ^uint64(0)
		for i := 0; i < 3; i++ {
			runtime.ReadMemStats(&ms)
			b := ms.TotalAlloc
			fuzzReadRequestBody(t, in, lim)
			runtime.ReadMemStats(&ms)
			if d := ms.TotalAlloc - b; d < min {
				min = d
			}
		}
		if min > allocBound(in) {
			t.Fatalf("cấp phát %d byte cho input %d byte (trần %d)", min, len(in), allocBound(in))
		}
	})
}

// fuzzReadRequestBody là thân fuzz thuần: parse, kiểm bất biến, round-trip.
func fuzzReadRequestBody(t *testing.T, in []byte, lim Limits) {
	br := bufio.NewReader(bytes.NewReader(in))
	req, err := ReadRequest(br, lim)
	if err != nil {
		if req != nil {
			t.Fatal("err != nil nhưng req != nil")
		}
		return
	}
	if req.HeaderBytes > len(in) {
		t.Fatalf("HeaderBytes %d > input %d", req.HeaderBytes, len(in))
	}
	if req.Chunked != (req.ContentLength == -1) {
		t.Fatalf("Chunked=%v ContentLength=%d", req.Chunked, req.ContentLength)
	}
	if req.Chunked && req.Header.Has("Content-Length") {
		t.Fatal("D4: CL còn lại cạnh TE")
	}
	body, berr := io.ReadAll(req.Body)
	if berr != nil && berr != io.ErrUnexpectedEOF {
		if _, ok := IsProtoError(berr); !ok {
			t.Fatalf("lỗi body lạ: %v", berr)
		}
	}
	if berr == nil && !req.Chunked && int64(len(body)) != req.ContentLength {
		t.Fatalf("body %d byte, CL %d", len(body), req.ContentLength)
	}
	rest, _ := io.ReadAll(br)
	if req.HeaderBytes+len(body)+len(rest) > len(in) {
		t.Fatalf("head %d + body %d + dư %d > input %d", req.HeaderBytes, len(body), len(rest), len(in))
	}

	// Round-trip: WriteHead rồi parse lại phải cho cùng head.
	var sb strings.Builder
	if err := req.WriteHead(&sb); err != nil {
		t.Fatalf("WriteHead: %v", err)
	}
	again, err := ReadRequest(bufio.NewReader(strings.NewReader(sb.String())), lim)
	if err != nil {
		t.Fatalf("parse lại thất bại: %v\n%q", err, sb.String())
	}
	if again.Method != req.Method || again.Target != req.Target || again.Proto != req.Proto ||
		again.ContentLength != req.ContentLength || again.Chunked != req.Chunked || again.Close != req.Close {
		t.Fatalf("round-trip lệch: %+v vs %+v", again, req)
	}
}

// FuzzAgainstNetHTTP — deliverable của phase 2. Không đòi hai parser giống nhau
// về mọi thứ. Chỉ đòi: nếu CẢ HAI nhận request, chúng phải đồng ý về (a) đọc
// body thành công hay không, (b) BYTE của body, (c) byte còn lại sau body —
// tức là điểm bắt đầu của request kế tiếp. Lệch một byte ở (b) hoặc (c) =
// proxy và backend nhìn thấy hai request khác nhau = smuggling.
//
// Một bên từ chối, bên kia nhận: KHÔNG phải lỗi ở đây (đó là D1..D5 — strict
// hơn oracle là chủ đích). Nhưng ghi lại lớp đó ở turn 2 để biết mình strict
// ở đâu.
func FuzzAgainstNetHTTP(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}
	lim := DefaultLimits()
	f.Fuzz(func(t *testing.T, in []byte) {
		brMine := bufio.NewReader(bytes.NewReader(in))
		mine, errMine := ReadRequest(brMine, lim)
		brTheirs := bufio.NewReader(bytes.NewReader(in))
		theirs, errTheirs := http.ReadRequest(brTheirs)
		if errMine != nil || errTheirs != nil {
			return
		}
		bodyMine, eMine := io.ReadAll(mine.Body)
		bodyTheirs, eTheirs := io.ReadAll(theirs.Body)
		// Bất đối xứng lúc đọc body chỉ NGUY HIỂM theo một hướng: mình đọc
		// được, oracle từ chối ⇒ proxy forward một body mà backend sẽ hiểu khác.
		// Hướng ngược (mình từ chối, oracle nhận) là D1..D5 cố ý — proxy đóng
		// connection, không có gì tới backend. Turn 2: fuzz 300s tìm ra
		// chunk-size "5 " (Go trim đuôi, mình không) — đúng hướng vô hại, và
		// bản đầu của điều kiện này coi nó là lỗi.
		if eMine == nil && eTheirs != nil {
			t.Fatalf("NGUY HIỂM: mình đọc body xong (%d byte), net/http lỗi %v\ninput=%q", len(bodyMine), eTheirs, in)
		}
		if eMine != nil {
			return
		}
		if !bytes.Equal(bodyMine, bodyTheirs) {
			t.Fatalf("LỆCH BODY: mình %d byte %q, net/http %d byte %q\ninput=%q",
				len(bodyMine), bodyMine, len(bodyTheirs), bodyTheirs, in)
		}
		restMine, _ := io.ReadAll(brMine)
		restTheirs, _ := io.ReadAll(brTheirs)
		if !bytes.Equal(restMine, restTheirs) {
			t.Fatalf("LỆCH RANH GIỚI: sau body mình còn %q, net/http còn %q\ninput=%q", restMine, restTheirs, in)
		}
	})
}

// FuzzChunkLineAgainstNetHTTP — fuzz CÓ CẤU TRÚC (P2-1). Head cố định hợp lệ,
// fuzzer chỉ đột biến dòng chunk-size (size + ext) và data. Không gian tìm
// kiếm nhỏ hơn FuzzAgainstNetHTTP hàng nghìn lần, và mọi input đều tới được
// chỗ hai parser phân xử chunk-size — chính chỗ 3 lệch thật của phase 2 nằm.
//
// Cùng điều kiện đỏ với FuzzAgainstNetHTTP: chỉ hướng "mình đọc xong, net/http
// lỗi", hoặc cả hai đọc xong mà khác byte/khác phần dư.
func FuzzChunkLineAgainstNetHTTP(f *testing.F) {
	for _, s := range [][2]string{{"3", "abc"}, {"3;x=y", "abc"}, {"0003", "abc"}, {"3 ", "abc"}, {"A", "0123456789"}, {"0", ""}} {
		f.Add([]byte(s[0]), []byte(s[1]))
	}
	lim := DefaultLimits()
	f.Fuzz(func(t *testing.T, sizeLine, data []byte) {
		if len(sizeLine) > 64 || len(data) > 256 {
			return
		}
		var wire bytes.Buffer
		wire.WriteString("POST / HTTP/1.1\r\nHost: h\r\nTransfer-Encoding: chunked\r\n\r\n")
		wire.Write(sizeLine)
		wire.WriteString("\r\n")
		wire.Write(data)
		wire.WriteString("\r\n0\r\n\r\nNEXT")
		in := wire.Bytes()

		brMine := bufio.NewReader(bytes.NewReader(in))
		mine, errMine := ReadRequest(brMine, lim)
		brTheirs := bufio.NewReader(bytes.NewReader(in))
		theirs, errTheirs := http.ReadRequest(brTheirs)
		if errMine != nil || errTheirs != nil {
			return
		}
		bodyMine, eMine := io.ReadAll(mine.Body)
		bodyTheirs, eTheirs := io.ReadAll(theirs.Body)
		if eMine == nil && eTheirs != nil {
			t.Fatalf("NGUY HIỂM: chunk-size %q: mình đọc %d byte, net/http lỗi %v", sizeLine, len(bodyMine), eTheirs)
		}
		if eMine != nil {
			return
		}
		if !bytes.Equal(bodyMine, bodyTheirs) {
			t.Fatalf("LỆCH BODY: chunk-size %q: mình %q, net/http %q", sizeLine, bodyMine, bodyTheirs)
		}
		restMine, _ := io.ReadAll(brMine)
		restTheirs, _ := io.ReadAll(brTheirs)
		if !bytes.Equal(restMine, restTheirs) {
			t.Fatalf("LỆCH RANH GIỚI: chunk-size %q: dư %q vs %q", sizeLine, restMine, restTheirs)
		}
	})
}
