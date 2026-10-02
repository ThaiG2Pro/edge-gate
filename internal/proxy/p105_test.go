package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// P10-5 — ALPN h2 trên TLS: Config.H2ALPN ⇒ listener công bố ["h2","http/1.1"];
// NegotiatedProtocol == "h2" ⇒ serveH2 ngay sau handshake (không sniff preface
// — ALPN đã quyết), SNI vẫn chọn vhost. Tắt (mặc định) ⇒ TestTLSALPN giữ nguyên.
func TestTLSALPNH2(t *testing.T) {
	l := startTLSProxy(t, func(c *Config) { c.H2ALPN = true })
	// net/http client chỉ nói h2 qua TLS tới l.addr với SNI a.test.
	var p http.Protocols
	p.SetHTTP2(true)
	tr := &http.Transport{
		Protocols:       &p,
		TLSClientConfig: &tls.Config{RootCAs: l.ca.Pool, ServerName: "a.test", NextProtos: []string{"h2"}},
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", l.addr)
		},
	}
	cl := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	defer tr.CloseIdleConnections()
	for i := 0; i < 3; i++ {
		resp, err := cl.Get("https://a.test/hello")
		if err != nil {
			t.Fatalf("GET %d: %v", i, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.ProtoMajor != 2 || resp.StatusCode != 200 || resp.Header.Get("X-Sim") != "A" {
			t.Fatalf("GET %d: %s %d X-Sim=%q — muốn HTTP/2.0 200 A", i, resp.Proto, resp.StatusCode, resp.Header.Get("X-Sim"))
		}
	}
	if got := l.s.H2Stats().Streams.Load(); got != 3 {
		t.Errorf("h2 streams = %d, muốn 3", got)
	}
	// Server ưu tiên (RFC 7301 §3.2): client chào [http/1.1 h2] vẫn được h2;
	// client chỉ biết http/1.1 vẫn chạy h1.
	c, err := l.dial("a.test", "http/1.1", "h2")
	if err != nil || c.ConnectionState().NegotiatedProtocol != "h2" {
		t.Fatalf("[http/1.1 h2]: %v %q", err, c.ConnectionState().NegotiatedProtocol)
	}
	c.Close()
	c, err = l.dial("a.test", "http/1.1")
	if err != nil || c.ConnectionState().NegotiatedProtocol != "http/1.1" {
		t.Fatalf("[http/1.1]: %v %q", err, c.ConnectionState().NegotiatedProtocol)
	}
	br := bufio.NewReader(c)
	if st, sim, err := tlsGet(c, br, "a.test"); err != nil || st != 200 || sim != "A" {
		t.Fatalf("h1 trên listener h2: %d %q %v", st, sim, err)
	}
	c.Close()
}

// P10-5 — RFC 9113 §9.2: h2 trên TLS 1.2 không được dùng cipher trong blocklist
// (Appendix A: không ECDHE, hay CBC). Client chỉ chào CBC + h2 ⇒ handshake hỏng
// (không có cipher chung), KHÔNG được bắt tay rồi mới GOAWAY INADEQUATE_SECURITY.
// Cùng cipher, không ALPN h2 ⇒ h1 vẫn chạy: hạn chế cipher chỉ khi h2 bật.
func TestTLSH2CipherBlocklist(t *testing.T) {
	cbc := []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA}
	l := startTLSProxy(t, func(c *Config) { c.H2ALPN = true })
	dial := func(alpn ...string) (*tls.Conn, error) {
		return tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", l.addr,
			&tls.Config{ServerName: "a.test", RootCAs: l.ca.Pool, NextProtos: alpn, MaxVersion: tls.VersionTLS12, CipherSuites: cbc})
	}
	c, err := dial("h2")
	if err == nil {
		st := c.ConnectionState()
		c.Close()
		t.Fatalf("TLS 1.2 + CBC + ALPN h2 phải hỏng handshake; được proto=%q cipher=%s", st.NegotiatedProtocol, tls.CipherSuiteName(st.CipherSuite))
	}
	t.Logf("CBC + h2: %v", err)
	// h2 tắt ⇒ cipher không bị hạn chế, h1 chạy (tương thích client cũ).
	l1 := startTLSProxy(t, nil)
	c, err = tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", l1.addr,
		&tls.Config{ServerName: "a.test", RootCAs: l1.ca.Pool, MaxVersion: tls.VersionTLS12, CipherSuites: cbc})
	if err != nil {
		t.Fatalf("h2 tắt, CBC h1 phải bắt tay được: %v", err)
	}
	c.Close()
}
