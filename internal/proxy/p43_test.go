package proxy

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaivro/edgegate/internal/httpx"
)

// stubConn: net.Conn chỉ có địa chỉ — forwardedHeaders không đọc/ghi byte.
type stubConn struct {
	net.Conn
	remote, local net.Addr
}

func (c stubConn) RemoteAddr() net.Addr { return c.remote }
func (c stubConn) LocalAddr() net.Addr  { return c.local }

func tcpAddr(s string) net.Addr { a, _ := net.ResolveTCPAddr("tcp", s); return a }

// P4-3 (đơn vị) — D12: X-Forwarded-Proto/Host/Port sinh theo connection;
// X-Real-IP của peer tin phải là IP; clientIP = phần tử PHẢI nhất của XFF
// không nằm trong trusted_proxies (không phải phần tử đầu — client tự thêm được).
func TestForwardedHeadersUnit(t *testing.T) {
	s := &Server{cfg: Config{TrustedProxies: []string{"10.0.0.0/8"}}}
	s.cfg.withDefaults()
	mk := func(peer string, tlsOn bool, hdr ...string) (httpx.Header, string) {
		h := httpx.Header{}
		h.Set("Host", "app.example")
		for i := 0; i+1 < len(hdr); i += 2 {
			h.Add(hdr[i], hdr[i+1])
		}
		st := &connState{}
		if tlsOn {
			st.tls = &tls.Conn{}
		}
		ip := s.forwardedHeaders(h, stubConn{remote: tcpAddr(peer), local: tcpAddr("192.0.2.1:8080")}, st)
		return h, ip
	}
	// Peer KHÔNG tin: mọi X-Forwarded-* bị thay, X-Real-IP = peer.
	h, ip := mk("203.0.113.9:4000", false,
		"X-Forwarded-For", "1.2.3.4", "X-Forwarded-Proto", "https", "X-Forwarded-Host", "evil", "X-Forwarded-Port", "443", "X-Real-IP", "1.2.3.4")
	want := map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "http", "X-Forwarded-Host": "app.example", "X-Forwarded-Port": "8080", "X-Real-Ip": "203.0.113.9"}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("không tin: %s = %q, muốn %q", k, got, v)
		}
	}
	if ip != "203.0.113.9" {
		t.Errorf("không tin: clientIP %q", ip)
	}
	// TLS ⇒ Proto https.
	if h, _ := mk("203.0.113.9:4000", true); h.Get("X-Forwarded-Proto") != "https" {
		t.Errorf("TLS: Proto = %q", h.Get("X-Forwarded-Proto"))
	}
	// Peer tin, không gửi gì: sinh đủ bộ, X-Real-IP = clientIP = peer.
	h, ip = mk("10.0.0.5:4000", false)
	for k, v := range map[string]string{"X-Forwarded-For": "10.0.0.5", "X-Forwarded-Proto": "http", "X-Forwarded-Host": "app.example", "X-Forwarded-Port": "8080", "X-Real-Ip": "10.0.0.5"} {
		if got := h.Get(k); got != v {
			t.Errorf("tin, trống: %s = %q, muốn %q", k, got, v)
		}
	}
	// Peer tin, gửi hợp lệ: giữ nguyên Proto/Host/Port/X-Real-IP; XFF append.
	h, ip = mk("10.0.0.5:4000", false,
		"X-Forwarded-For", "9.9.9.9, 1.2.3.4, 10.0.0.7", "X-Forwarded-Proto", "https", "X-Forwarded-Host", "front.example", "X-Forwarded-Port", "443", "X-Real-IP", "1.2.3.4", "Forwarded", "for=1.2.3.4")
	for k, v := range map[string]string{"X-Forwarded-For": "9.9.9.9, 1.2.3.4, 10.0.0.7, 10.0.0.5", "X-Forwarded-Proto": "https", "X-Forwarded-Host": "front.example", "X-Forwarded-Port": "443", "X-Real-Ip": "1.2.3.4", "Forwarded": "for=1.2.3.4"} {
		if got := h.Get(k); got != v {
			t.Errorf("tin, hợp lệ: %s = %q, muốn %q", k, got, v)
		}
	}
	// clientIP: 10.0.0.7 là proxy tin ⇒ bỏ; 1.2.3.4 là phần tử phải nhất không tin ⇒ chọn. 9.9.9.9 do client bịa.
	if ip != "1.2.3.4" {
		t.Errorf("clientIP = %q, muốn 1.2.3.4 (phải nhất không tin)", ip)
	}
	// Peer tin, gửi bẩn: X-Real-IP không phải IP, Proto lạ, Port không phải số ⇒ thay.
	h, ip = mk("10.0.0.5:4000", false,
		"X-Forwarded-For", "1.2.3.4", "X-Real-IP", "not-an-ip", "X-Forwarded-Proto", "gopher", "X-Forwarded-Port", "4x4", "X-Forwarded-Host", "bad host")
	for k, v := range map[string]string{"X-Real-Ip": "1.2.3.4", "X-Forwarded-Proto": "http", "X-Forwarded-Port": "8080", "X-Forwarded-Host": "app.example"} {
		if got := h.Get(k); got != v {
			t.Errorf("tin, bẩn: %s = %q, muốn %q", k, got, v)
		}
	}
	// Toàn chuỗi XFF đều là proxy tin ⇒ clientIP = phần tử đầu (xa nhất).
	if _, ip = mk("10.0.0.5:4000", false, "X-Forwarded-For", "10.0.0.1, 10.0.0.2"); ip != "10.0.0.1" {
		t.Errorf("toàn tin: clientIP = %q", ip)
	}
}

// P4-3 (e2e, plaintext) — peer không tin bịa X-Forwarded-Proto/Host/Port ⇒ upstream
// thấy giá trị proxy sinh.
func TestForwardedProtoHostPortE2E(t *testing.T) {
	p := startProxy(t, startFixture(t), nil)
	req, _ := http.NewRequest("GET", "http://"+p+"/headers", nil)
	req.Host = "app.example"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "evil")
	req.Header.Set("X-Forwarded-Port", "443")
	resp, err := oracleClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	got := string(b)
	_, port, _ := net.SplitHostPort(p)
	for _, want := range []string{"X-Forwarded-Proto: http\n", "X-Forwarded-Host: app.example\n", "X-Forwarded-Port: " + port + "\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("thiếu %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "evil") || strings.Contains(got, "https") {
		t.Errorf("giá trị bịa lọt:\n%s", got)
	}
}

// P4-3 (e2e, TLS) — qua listener TLS upstream phải thấy X-Forwarded-Proto: https.
func TestForwardedProtoTLS(t *testing.T) {
	l := startTLSProxy(t, nil)
	c, err := l.dial("a.test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	io.WriteString(c, "GET /headers HTTP/1.1\r\nHost: a.test\r\n\r\n")
	br := bufio.NewReader(c)
	resp, err := httpx.ReadResponse(br, httpx.DefaultLimits(), "GET")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	got := string(b)
	_, port, _ := net.SplitHostPort(l.addr)
	for _, want := range []string{"X-Forwarded-Proto: https\n", "X-Forwarded-Host: a.test\n", "X-Forwarded-Port: " + port + "\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("thiếu %q:\n%s", want, got)
		}
	}
}
