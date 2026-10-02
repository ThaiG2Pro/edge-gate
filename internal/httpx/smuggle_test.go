package httpx

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http" // ORACLE — chỉ trong _test.go
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/thaivro/edgegate/internal/smugglecase"
)

const smuggleDir = "../../testdata/smuggle"

func loadCases(t testing.TB) []smugglecase.Case {
	t.Helper()
	cs, err := smugglecase.Load(smuggleDir)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

// outcome: kết quả một ca qua parser mình. status 0 = không lỗi, -1 = lỗi I/O.
type outcome struct {
	headStatus, bodyStatus int
	err                    error
	body, rest             []byte
	target, host           string
}

func runMine(c smugglecase.Case, lim Limits) outcome {
	br := bufio.NewReader(bytes.NewReader(c.Wire))
	var (
		body io.Reader
		o    outcome
		err  error
	)
	if c.Kind == "response" {
		var resp *Response
		resp, err = ReadResponse(br, lim, c.Method)
		if err == nil {
			body = resp.Body
		}
	} else {
		var req *Request
		req, err = ReadRequest(br, lim)
		if err == nil {
			body, o.target, o.host = req.Body, req.Target, req.Header.Get("Host")
		}
	}
	if err != nil {
		o.err = err
		if pe, ok := IsProtoError(err); ok {
			o.headStatus = pe.Status
		} else {
			o.headStatus = -1
		}
		return o
	}
	o.body, err = io.ReadAll(body)
	if err != nil {
		o.err = err
		if pe, ok := IsProtoError(err); ok {
			o.bodyStatus = pe.Status
		} else {
			o.bodyStatus = -1
		}
		return o
	}
	o.rest, _ = io.ReadAll(br)
	return o
}

// TestSmuggling — deliverable phase 4, tầng parser. Với mỗi ca: bị từ chối ở
// đúng chỗ (head/body) với đúng status; hoặc được nhận với ĐÚNG body, ĐÚNG
// phần dư (= điểm bắt đầu request kế — ranh giới), đúng Target/Host sau chuẩn
// hoá. `make smugglelab-nodefense` phải làm bài này đỏ.
func TestSmuggling(t *testing.T) {
	cs := loadCases(t)
	nReject, nOK, nResp := 0, 0, 0
	for _, c := range cs {
		c := c
		switch {
		case c.Kind == "response":
			nResp++
		case c.Reject():
			nReject++
		default:
			nOK++
		}
		t.Run(c.File, func(t *testing.T) {
			o := runMine(c, DefaultLimits())
			switch {
			case c.Expect == "ok":
				if o.err != nil {
					t.Fatalf("%s: muốn nhận, bị từ chối: %v", c.Name, o.err)
				}
				if string(o.body) != c.Body {
					t.Fatalf("%s: body %q, muốn %q", c.Name, o.body, c.Body)
				}
				if string(o.rest) != c.Rest {
					t.Fatalf("%s: phần dư %q, muốn %q — RANH GIỚI SAI", c.Name, o.rest, c.Rest)
				}
				if c.Kind == "request" {
					if o.target != c.Target {
						t.Fatalf("%s: target %q, muốn %q", c.Name, o.target, c.Target)
					}
					if o.host != c.Host {
						t.Fatalf("%s: host %q, muốn %q", c.Name, o.host, c.Host)
					}
				}
			case c.BodyReject():
				if o.headStatus != 0 {
					t.Fatalf("%s: muốn head nhận rồi body lỗi %d, nhưng head bị từ chối: %v", c.Name, c.Status(), o.err)
				}
				if o.bodyStatus != c.Status() {
					t.Fatalf("%s: body muốn lỗi %d, có status %d err=%v body=%q rest=%q", c.Name, c.Status(), o.bodyStatus, o.err, o.body, o.rest)
				}
			default:
				if o.headStatus != c.Status() {
					t.Fatalf("%s: muốn head bị từ chối %d, có status %d err=%v (body=%q rest=%q)\n  why: %s", c.Name, c.Status(), o.headStatus, o.err, o.body, o.rest, c.Why)
				}
			}
		})
	}
	t.Logf("G1: %d ca — %d request từ chối, %d request nhận, %d response", len(cs), nReject, nOK, nResp)
	if len(cs) < 40 || nReject < 30 || nOK < 5 || nResp < 5 {
		t.Fatalf("bộ ca dưới mức đăng ký (≥40 / ≥30 / ≥5 / ≥5)")
	}
}

// TestSmugglingOracle — G2: cùng bộ ca qua net/http (Go stdlib) làm oracle.
// Không đòi giống nhau. Đòi: KHÔNG có ca nào mình nhận mà oracle từ chối
// (hướng nguy hiểm — proxy forward thứ backend sẽ hiểu khác). Ca mình từ chối
// mà oracle nhận là chỗ mình strict hơn: in bảng để ghi vào diary.
func TestSmugglingOracle(t *testing.T) {
	cs := loadCases(t)
	agree, stricter, dangerous, nReject := 0, 0, 0, 0
	var stricterNames []string
	for _, c := range cs {
		if !c.Reject() {
			o := runMine(c, DefaultLimits())
			ob := runOracle(c)
			if ob.err != nil {
				dangerous++
				t.Errorf("NGUY HIỂM %s: mình nhận (body %q), net/http từ chối: %v", c.File, o.body, ob.err)
				continue
			}
			if !bytes.Equal(o.body, ob.body) || !bytes.Equal(o.rest, ob.rest) {
				dangerous++
				t.Errorf("LỆCH %s: body %q/%q, dư %q/%q", c.File, o.body, ob.body, o.rest, ob.rest)
			}
			continue
		}
		nReject++
		ob := runOracle(c)
		if ob.err != nil {
			agree++
			continue
		}
		stricter++
		stricterNames = append(stricterNames, fmt.Sprintf("%s (oracle body=%q dư=%q)", c.File, ob.body, ob.rest))
	}
	sort.Strings(stricterNames)
	t.Logf("G2: ca từ chối %d — oracle cùng từ chối %d (%.0f %%), mình strict hơn %d, hướng nguy hiểm %d",
		nReject, agree, 100*float64(agree)/float64(nReject), stricter, dangerous)
	t.Logf("G2: mình strict hơn net/http ở:\n  %s", strings.Join(stricterNames, "\n  "))
}

type oracleOut struct {
	err        error
	body, rest []byte
}

func runOracle(c smugglecase.Case) oracleOut {
	br := bufio.NewReader(bytes.NewReader(c.Wire))
	var body io.Reader
	if c.Kind == "response" {
		resp, err := http.ReadResponse(br, &http.Request{Method: c.Method})
		if err != nil {
			return oracleOut{err: err}
		}
		body = resp.Body
	} else {
		req, err := http.ReadRequest(br)
		if err != nil {
			return oracleOut{err: err}
		}
		// net/http không kiểm Host ở ReadRequest; server làm ở serve(). Mô
		// phỏng phần đó để oracle công bằng.
		if req.ProtoAtLeast(1, 1) && req.Host == "" {
			return oracleOut{err: errors.New("server: missing Host")}
		}
		body = req.Body
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return oracleOut{err: err}
	}
	rest, _ := io.ReadAll(br)
	return oracleOut{body: b, rest: rest}
}

// BenchmarkReadRequest — G4: chi phí kiểm tra thêm của phase 4 trên một head
// điển hình (6 header, không body). So với commit nền b3e08f0 bằng benchstat.
func BenchmarkReadRequest(b *testing.B) {
	wire := []byte("GET /api/v1/items?page=2 HTTP/1.1\r\nHost: shop.example.com\r\nUser-Agent: bench/1.0\r\nAccept: application/json\r\nAccept-Encoding: gzip\r\nConnection: keep-alive\r\nX-Request-Id: 7c1d0e0a-3f2b-4d1e-9a8c-0b1c2d3e4f50\r\n\r\n")
	lim := DefaultLimits()
	rd := bytes.NewReader(wire)
	br := bufio.NewReaderSize(rd, 4096)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rd.Reset(wire)
		br.Reset(rd)
		if _, err := ReadRequest(br, lim); err != nil {
			b.Fatal(err)
		}
	}
}

// P4-1: duyệt `Connection:` không cấp phát — request có `Connection: keep-alive`
// tốn ≤ 24 alloc (bản cũ 26: bytes.Split([]byte(v)) trong hasConnectionToken).
func TestReadRequestAllocs(t *testing.T) {
	wire := []byte("GET /api/v1/items?page=2 HTTP/1.1\r\nHost: shop.example.com\r\nUser-Agent: bench/1.0\r\nAccept: application/json\r\nAccept-Encoding: gzip\r\nConnection: keep-alive\r\nX-Request-Id: 7c1d0e0a-3f2b-4d1e-9a8c-0b1c2d3e4f50\r\n\r\n")
	lim := DefaultLimits()
	rd := bytes.NewReader(wire)
	br := bufio.NewReaderSize(rd, 4096)
	n := testing.AllocsPerRun(200, func() {
		rd.Reset(wire)
		br.Reset(rd)
		if _, err := ReadRequest(br, lim); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("%.0f alloc / ReadRequest", n)
	if n > 24 {
		t.Fatalf("%.0f alloc > 24", n)
	}
}

// P2-5 (2026-10-02): badRequest("%q", <cả trường bẩn>) cấp phát ~4 byte/byte
// input cộng tăng trưởng của fmt — input 7.4 KB cho 196 KB, vượt allocBound.
// Fuzz chỉ bắt được 1/3 lượt vì tầng 1 (runtime/metrics) đếm thiếu ~4x. Đo
// tất định ở đây, mọi chỗ %q nhận byte của peer, bằng ReadMemStats min-of-3.
func TestErrorAllocBound(t *testing.T) {
	junk := strings.Repeat("\xa9", 7000)
	cases := map[string]string{
		"version": "GET / HTTP/1" + junk + "\r\n\r\n",
		"method":  "G" + junk + " / HTTP/1.1\r\nHost: h\r\n\r\n",
		"name":    "GET / HTTP/1.1\r\nHost: h\r\nX" + junk + ": v\r\n\r\n",
		"conn":    "GET / HTTP/1.1\r\nHost: h\r\nConnection: a" + junk + "\r\n\r\n",
		"status":  "HTTP/1.1 2" + junk + " OK\r\n\r\n",
	}
	lim := DefaultLimits()
	for name, in := range cases {
		run := func() error {
			br := bufio.NewReader(strings.NewReader(in))
			var err error
			if name == "status" {
				_, err = ReadResponse(br, lim, "GET")
			} else {
				_, err = ReadRequest(br, lim)
			}
			return err
		}
		if err := run(); err == nil {
			t.Fatalf("%s: muốn lỗi", name)
		} else if e, ok := IsProtoError(err); !ok || len(e.Reason) > 256 {
			t.Errorf("%s: Reason %d byte — không được chép nguyên trường bẩn vào lỗi", name, len(e.Reason))
		}
		var ms runtime.MemStats
		min := ^uint64(0)
		for i := 0; i < 3; i++ {
			runtime.ReadMemStats(&ms)
			b := ms.TotalAlloc
			run()
			runtime.ReadMemStats(&ms)
			if d := ms.TotalAlloc - b; d < min {
				min = d
			}
		}
		// Trần chặt hơn allocBound của fuzz: lỗi sớm thì chỉ còn bufio + readLine.
		if bound := uint64(4*len(in)) + 1<<14; min > bound {
			t.Errorf("%s: cấp phát %d byte cho input %d byte (trần %d)", name, min, len(in), bound)
		}
	}
}
