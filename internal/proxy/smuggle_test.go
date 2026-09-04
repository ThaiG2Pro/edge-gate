package proxy

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http" // oracle client — chỉ trong _test.go
	"strings"
	"testing"
	"time"

	"github.com/thaivro/edgegate/internal/httpx"
	"github.com/thaivro/edgegate/internal/smugglecase"
)

const smuggleDir = "../../testdata/smuggle"

// rawUpstream: backend giả bằng net thuần, trả ĐÚNG payload cho mọi request
// (để bơm response bẩn — net/http không cho ghi CL+TE hay NUL trong header).
func rawUpstream(t *testing.T, payload []byte) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(3 * time.Second))
				br := bufio.NewReader(c)
				if req, err := httpx.ReadRequest(br, httpx.DefaultLimits()); err == nil {
					io.Copy(io.Discard, req.Body)
				}
				c.Write(payload)
			}()
		}
	}()
	return ln.Addr().String()
}

// TestSmugglingE2E — G5/G7: cùng bộ ca, nhưng qua proxy THẬT. Với ca request
// bị từ chối: client gửi payload + pipeline thêm một GET hợp lệ; proxy phải trả
// đúng status, `Connection: close`, rồi ĐÓNG — GET pipelined không bao giờ
// được trả lời (D11). Với ca response: upstream giả trả payload; proxy phải
// trả 502 (head bẩn) hoặc đóng giữa body (trailer bẩn), 0 byte body upstream lọt.
func TestSmugglingE2E(t *testing.T) {
	cs, err := smugglecase.Load(smuggleDir)
	if err != nil {
		t.Fatal(err)
	}
	fix := startFixture(t)
	pReq := startProxy(t, fix, nil)
	nReq, nResp := 0, 0
	for _, c := range cs {
		c := c
		if c.Kind == "request" && !c.Reject() {
			continue // ca nhận: ranh giới đã kiểm ở httpx; e2e ở TestKeepAliveSequential
		}
		t.Run(c.File, func(t *testing.T) {
			if c.Kind == "response" {
				nResp++
				e2eResponse(t, c)
				return
			}
			nReq++
			e2eRequestRejected(t, pReq, c)
		})
	}
	t.Logf("G5: %d ca request từ chối, G7: %d ca response qua proxy thật", nReq, nResp)
}

func e2eRequestRejected(t *testing.T, proxyAddr string, c smugglecase.Case) {
	rc := dialRaw(t, proxyAddr)
	rc.c.SetDeadline(time.Now().Add(3 * time.Second))
	wire := append(append([]byte{}, c.Wire...), "GET /hello HTTP/1.1\r\nHost: h\r\n\r\n"...)
	if _, err := rc.c.Write(wire); err != nil {
		t.Fatal(err)
	}
	resp, err := httpx.ReadResponse(rc.br, httpx.DefaultLimits(), "GET")
	if err != nil {
		t.Fatalf("%s: đọc response: %v", c.Name, err)
	}
	if resp.Status != c.Status() {
		t.Fatalf("%s: status %d, muốn %d", c.Name, resp.Status, c.Status())
	}
	if !resp.Close {
		t.Fatalf("%s: thiếu Connection: close", c.Name)
	}
	io.Copy(io.Discard, resp.Body)
	// Sau response lỗi phải là EOF — không có response thứ hai cho GET pipelined.
	if _, err := rc.br.ReadByte(); err != io.EOF {
		t.Fatalf("%s: sau response lỗi còn byte/lỗi khác: %v — GET pipelined đã được trả lời?", c.Name, err)
	}
}

func e2eResponse(t *testing.T, c smugglecase.Case) {
	up := rawUpstream(t, c.Wire)
	p := startProxy(t, up, nil)
	rc := dialRaw(t, p)
	rc.c.SetDeadline(time.Now().Add(3 * time.Second))
	method := c.Method
	if method == "" {
		method = "GET"
	}
	if _, err := io.WriteString(rc.c, method+" /x HTTP/1.1\r\nHost: h\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := httpx.ReadResponse(rc.br, httpx.DefaultLimits(), method)
	if err != nil {
		t.Fatalf("%s: đọc response từ proxy: %v", c.Name, err)
	}
	body, berr := io.ReadAll(resp.Body)
	switch {
	case c.Expect == "ok":
		if resp.Status != 200 || berr != nil || string(body) != c.Body {
			t.Fatalf("%s: %d body=%q err=%v, muốn 200 %q", c.Name, resp.Status, body, berr, c.Body)
		}
	case c.BodyReject():
		// Head sạch đã đi (200); lỗi giữa body ⇒ proxy chỉ được ĐÓNG. Client
		// thấy body cắt cụt (ErrUnexpectedEOF hoặc lỗi chunk), không thấy 502 thứ hai.
		if resp.Status != 200 {
			t.Fatalf("%s: status %d, muốn 200 rồi đóng giữa body", c.Name, resp.Status)
		}
		if berr == nil {
			t.Fatalf("%s: body đọc trọn %q — proxy đã nuốt trailer bẩn thay vì đóng", c.Name, body)
		}
	default:
		if resp.Status != 502 {
			t.Fatalf("%s: status %d, muốn 502 (upstream trả response mơ hồ)", c.Name, resp.Status)
		}
		if strings.Contains(string(body), "EVL") || strings.Contains(string(body), "hello") {
			t.Fatalf("%s: byte body của upstream lọt ra client: %q", c.Name, body)
		}
		// Connection CLIENT được giữ (D6 phase 3: body request đã đọc hết, chưa
		// gửi gì) — chỉ connection UPSTREAM bị đóng. Turn 1 từng đòi
		// Connection: close ở đây; đó là đòi thừa, xem phase4-log.
		if resp.Close {
			t.Logf("%s: 502 kèm Connection: close (không sai, nhưng không cần)", c.Name)
		}
	}
}

// TestXFFUntrustedReplaced — G6 nửa "không tin": peer (127.0.0.1) không có
// trong TrustedProxies ⇒ XFF giả bị THAY bằng peer, X-Real-IP = peer, Forwarded mất.
func TestXFFUntrustedReplaced(t *testing.T) {
	p := startProxy(t, startFixture(t), nil) // TrustedProxies rỗng
	req, _ := http.NewRequest("GET", "http://"+p+"/headers", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Real-IP", "1.2.3.4")
	req.Header.Set("Forwarded", "for=1.2.3.4")
	resp, err := oracleClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	got := string(b)
	if strings.Contains(got, "1.2.3.4") {
		t.Fatalf("giá trị giả lọt sang upstream:\n%s", got)
	}
	for _, want := range []string{"X-Forwarded-For: 127.0.0.1\n", "X-Real-Ip: 127.0.0.1\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("thiếu %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Forwarded:") {
		t.Errorf("Forwarded của peer không tin phải bị xoá:\n%s", got)
	}
}

// TestAbsoluteFormRewritten — D5: absolute-form khớp Host ⇒ upstream thấy
// origin-form + Host giữ nguyên (D4).
func TestAbsoluteFormRewritten(t *testing.T) {
	p := startProxy(t, startFixture(t), nil)
	rc := dialRaw(t, p)
	resp, body := rc.do(t, "GET", "GET http://vhost.example/headers?x=1 HTTP/1.1\r\nHost: vhost.example\r\n\r\n")
	if resp.Status != 200 || !strings.Contains(string(body), "Host: vhost.example\n") {
		t.Fatalf("%d\n%s", resp.Status, body)
	}
	// Lệch Host ⇒ 400 và đóng.
	rc2 := dialRaw(t, p)
	resp, _ = rc2.do(t, "GET", "GET http://a.example/headers HTTP/1.1\r\nHost: b.example\r\n\r\n")
	if resp.Status != 400 || !resp.Close {
		t.Fatalf("%d close=%v", resp.Status, resp.Close)
	}
}

// TestBadTrustedProxiesPanics: cấu hình tin cậy sai là lỗi khởi động.
func TestBadTrustedProxiesPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("muốn panic")
		}
	}()
	New(Config{Listen: ":0", Upstream: "x", TrustedProxies: []string{"not-a-cidr"}})
}

var _ = errors.New
