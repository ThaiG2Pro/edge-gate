package proxy

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThaiG2Pro/edge-gate/internal/fixture"
	"github.com/ThaiG2Pro/edge-gate/internal/httpx"
	"github.com/ThaiG2Pro/edge-gate/internal/lb"
	"github.com/ThaiG2Pro/edge-gate/internal/tlsx"
)

type tlsLab struct {
	s      *Server
	addr   string
	ca     *tlsx.CA
	store  *tlsx.CertStore
	simA   *fixture.Sim
	simB   *fixture.Sim
	serial atomic.Int64
}

// startTLSProxy: listener TLS, vhost 0 = a.test → sim A, vhost 1 = b.test → sim B.
func startTLSProxy(t *testing.T, mut func(*Config)) *tlsLab {
	t.Helper()
	ca, err := tlsx.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	l := &tlsLab{ca: ca, simA: fixture.NewSim("A", 0, 0), simB: fixture.NewSim("B", 0, 0)}
	upA, stopA, _ := fixture.ListenAndServeSim("127.0.0.1:0", l.simA)
	upB, stopB, _ := fixture.ListenAndServeSim("127.0.0.1:0", l.simB)
	t.Cleanup(stopA)
	t.Cleanup(stopB)
	l.serial.Store(1000)
	l.store, err = tlsx.NewCertStore(l.entries(t))
	if err != nil {
		t.Fatal(err)
	}
	noHealth := lb.Config{Health: lb.HealthConfig{Disabled: true}}
	l.s, l.addr = startProxyS(t, "", func(c *Config) {
		c.TLS = &TLSConfig{Store: l.store}
		c.VHosts = []VHost{{Names: []string{"a.test"}, Upstreams: []string{upA}, LB: noHealth},
			{Names: []string{"b.test"}, Upstreams: []string{upB}, LB: noHealth}}
		if mut != nil {
			mut(c)
		}
	})
	return l
}

// entries: cert mới cho a.test / b.test với serial tăng dần (hot-reload phân biệt được).
func (l *tlsLab) entries(t *testing.T) []tlsx.Entry {
	t.Helper()
	var out []tlsx.Entry
	for _, n := range []string{"a.test", "b.test"} {
		c, err := l.ca.LeafTLS([]string{n}, tlsx.ECDSA, l.serial.Add(1))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, tlsx.Entry{Names: []string{n}, Cert: &c})
	}
	return out
}

func (l *tlsLab) dial(sni string, alpn ...string) (*tls.Conn, error) {
	return tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", l.addr,
		&tls.Config{ServerName: sni, RootCAs: l.ca.Pool, NextProtos: alpn})
}

// get: một GET trên c, trả status và X-Sim (nhóm upstream đã phục vụ).
func tlsGet(c net.Conn, br *bufio.Reader, host string) (int, string, error) {
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(c, "GET /hello HTTP/1.1\r\nHost: "+host+"\r\n\r\n"); err != nil {
		return 0, "", err
	}
	resp, err := httpx.ReadResponse(br, httpx.DefaultLimits(), "GET")
	if err != nil {
		return 0, "", err
	}
	io.Copy(io.Discard, resp.Body)
	return resp.Status, resp.Header.Get("X-Sim"), nil
}

// G1 — SNI chọn cert + nhóm upstream; SNI lạ / không SNI ⇒ handshake hỏng.
func TestTLSSNIRouting(t *testing.T) {
	l := startTLSProxy(t, nil)
	conns := map[string]*tls.Conn{}
	brs := map[string]*bufio.Reader{}
	for _, n := range []string{"a.test", "b.test"} {
		c, err := l.dial(n)
		if err != nil {
			t.Fatalf("handshake %s: %v", n, err)
		}
		defer c.Close()
		if got := c.ConnectionState().PeerCertificates[0].DNSNames[0]; got != n {
			t.Fatalf("SNI %s nhận cert %s", n, got)
		}
		conns[n], brs[n] = c, bufio.NewReader(c)
	}
	wrong := 0
	for i := 0; i < 1000; i++ {
		n, want := "a.test", "A"
		if i%2 == 1 {
			n, want = "b.test", "B"
		}
		st, sim, err := tlsGet(conns[n], brs[n], n)
		if err != nil || st != 200 || sim != want {
			wrong++
		}
	}
	t.Logf("1000 request xen kẽ SNI a/b: lệch %d; sim A %d, B %d", wrong, l.simA.Served(), l.simB.Served())
	if wrong != 0 || l.simA.Served() != 500 || l.simB.Served() != 500 {
		t.Fatal("SNI routing lệch")
	}
	_, errC := l.dial("c.test")
	raw, _ := tls.Dial("tcp", l.addr, &tls.Config{InsecureSkipVerify: true}) // không SNI
	if raw != nil {
		raw.Close()
	}
	_, errNo := tls.Dial("tcp", l.addr, &tls.Config{InsecureSkipVerify: true})
	t.Logf("SNI lạ: %v; không SNI: %v; handshake hỏng phía proxy %d", errC, errNo, l.s.ResilienceStats().HandshakeFail)
	if errC == nil || errNo == nil || l.simA.Served()+l.simB.Served() != 1000 {
		t.Fatal("SNI lạ / không SNI phải hỏng handshake, không request nào tới upstream")
	}
	// P8-2: alert phải là unrecognized_name (RFC 6066 §3), không phải internal_error.
	if !strings.Contains(errC.Error(), "unrecognized name") {
		t.Fatalf("SNI lạ: muốn alert unrecognized_name, được %v", errC)
	}
}

// G2 — domain fronting: SNI a.test + Host b.test ⇒ 421, B nhận 0, connection
// giữ được. nodefense8 (chọn vhost theo Host) ⇒ B phục vụ dưới cert của A ⇒ ĐỎ.
func TestTLSDomainFronting(t *testing.T) {
	l := startTLSProxy(t, nil)
	c, err := l.dial("a.test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	br := bufio.NewReader(c)
	st, sim, err := tlsGet(c, br, "b.test")
	t.Logf("SNI a.test, Host b.test: %d (X-Sim %q), B phục vụ %d", st, sim, l.simB.Served())
	if err != nil || st != 421 || l.simB.Served() != 0 {
		t.Fatalf("domain fronting phải 421 và B nhận 0")
	}
	if st, sim, err := tlsGet(c, br, "A.TEST:443"); err != nil || st != 200 || sim != "A" {
		t.Fatalf("request kế đúng vhost (Host hoa + port) trên cùng connection: %d %q %v", st, sim, err)
	}
}

// G3 — ALPN: [h2 http/1.1] ⇒ http/1.1; chỉ [h2] ⇒ hỏng; không ALPN ⇒ "".
func TestTLSALPN(t *testing.T) {
	l := startTLSProxy(t, nil)
	c, err := l.dial("a.test", "h2", "http/1.1")
	if err != nil || c.ConnectionState().NegotiatedProtocol != "http/1.1" {
		t.Fatalf("[h2 http/1.1]: %v", err)
	}
	c.Close()
	_, err = l.dial("a.test", "h2")
	t.Logf("chỉ h2: %v", err)
	if err == nil {
		t.Fatal("client chỉ nói h2 phải hỏng handshake (no_application_protocol)")
	}
	c, err = l.dial("a.test")
	if err != nil || c.ConnectionState().NegotiatedProtocol != "" {
		t.Fatalf("không ALPN: %v", err)
	}
	c.Close()
}

// G4 — hot-reload dưới tải: 32 connection keep-alive bắn liên tục, 20 lần
// Replace; 0 lỗi; connection cũ giữ serial cũ, connection mới thấy serial mới.
// Reload hỏng (file key không khớp) ⇒ lỗi, serial không đổi.
func TestTLSHotReload(t *testing.T) {
	l := startTLSProxy(t, nil)
	first := l.store.Serials()[0]
	stop := make(chan struct{})
	var errs, ok atomic.Int64
	var wg, dialed sync.WaitGroup
	var oldSerial atomic.Value
	for i := 0; i < 32; i++ {
		wg.Add(1)
		dialed.Add(1)
		go func() {
			defer wg.Done()
			c, err := l.dial("a.test")
			dialed.Done()
			if err != nil {
				errs.Add(1)
				return
			}
			defer c.Close()
			// Mọi connection dial xong TRƯỚC lần reload đầu (barrier dialed) ⇒ đều
			// phải mang serial đầu. Bản đầu không có barrier: dưới -race có con dial
			// sau reload đầu và ghi đè "serial cũ".
			if s := c.ConnectionState().PeerCertificates[0].SerialNumber.String(); s != first {
				oldSerial.Store("lệch:" + s)
			} else if oldSerial.Load() == nil {
				oldSerial.Store(s)
			}
			br := bufio.NewReader(c)
			for {
				select {
				case <-stop:
					return
				default:
				}
				if st, _, err := tlsGet(c, br, "a.test"); err != nil || st != 200 {
					errs.Add(1)
					return
				}
				ok.Add(1)
			}
		}()
	}
	dialed.Wait()
	time.Sleep(50 * time.Millisecond)
	for i := 0; i < 20; i++ {
		if err := l.store.Replace(l.entries(t)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	last := l.store.Serials()[0]
	c, err := l.dial("a.test")
	if err != nil {
		t.Fatal(err)
	}
	newSerial := c.ConnectionState().PeerCertificates[0].SerialNumber.String()
	c.Close()
	t.Logf("20 reload dưới 32 connection: %d request ok, %d lỗi; serial đầu %s, connection cũ %s, mới nhất %s, connection mới thấy %s",
		ok.Load(), errs.Load(), first, oldSerial.Load(), last, newSerial)
	if errs.Load() != 0 || oldSerial.Load() != first || newSerial != last || first == last {
		t.Fatal("reload đứt connection hoặc serial sai")
	}
	// Reload hỏng: file key không khớp cert.
	dir := t.TempDir()
	cp, _, _ := l.ca.Leaf([]string{"a.test"}, tlsx.ECDSA, 1)
	_, kp, _ := l.ca.Leaf([]string{"a.test"}, tlsx.ECDSA, 2)
	cf, kf, _ := tlsx.WriteFiles(dir, "bad", cp, kp)
	err = l.store.Replace([]tlsx.Entry{{Names: []string{"a.test"}, CertFile: cf, KeyFile: kf}, {Names: []string{"b.test"}, CertFile: cf, KeyFile: kf}})
	if err == nil || l.store.Serials()[0] != last {
		t.Fatalf("reload hỏng phải lỗi và giữ serial %s: err=%v serial=%s", last, err, l.store.Serials()[0])
	}
}

// G5 — handshake có deadline riêng: ClientHello nhỏ giọt 1 byte / 100 ms.
// HandshakeTimeout 300 ms ⇒ đóng sau ≈ 300 ms. nodefense8 ⇒ đóng sau
// IdleTimeout (2 s ở startProxyS) ⇒ ĐỎ.
func TestTLSHandshakeTimeout(t *testing.T) {
	l := startTLSProxy(t, func(c *Config) { c.HandshakeTimeout = 300 * time.Millisecond })
	c, err := net.Dial("tcp", l.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	closed := make(chan time.Duration, 1)
	t0 := time.Now()
	go func() {
		io.Copy(io.Discard, c)
		closed <- time.Since(t0)
	}()
	hdr := []byte{0x16, 0x03, 0x01, 0x02, 0x00} // record handshake, 512 byte sẽ không bao giờ tới
	for i := 0; ; i++ {
		select {
		case el := <-closed:
			t.Logf("ClientHello nhỏ giọt: proxy đóng sau %s (gửi %d byte)", el.Round(time.Millisecond), i)
			if el < 250*time.Millisecond || el > 450*time.Millisecond {
				t.Fatalf("muốn đóng sau ≈ HandshakeTimeout 300 ms")
			}
			return
		case <-time.After(100 * time.Millisecond):
		}
		if time.Since(t0) > 3*time.Second {
			t.Fatal("proxy không đóng handshake nhỏ giọt sau 3 s")
		}
		c.Write([]byte{hdr[i%len(hdr)]})
	}
}

// G8 (đơn vị) — upstream TLS: probe FIN đọc fd TCP bên dưới tls.Conn.
// (a) keep-alive đều ⇒ 1 dial, DeadOnProbe 0 (NewSessionTicket đã đọc cùng
// response đầu). (b) upstream đóng connection rỗi (IdleTimeout 50 ms ⇒ close_notify
// + FIN), request cách 100 ms ⇒ probe bắt hết, Retries 0.
func TestTLSUpstreamProbe(t *testing.T) {
	ca, _ := tlsx.NewCA()
	leafC, err := ca.LeafTLS([]string{"127.0.0.1"}, tlsx.ECDSA, 7)
	if err != nil {
		t.Fatal(err)
	}
	us := httptest.NewUnstartedServer(fixture.Handler())
	us.TLS = &tls.Config{Certificates: []tls.Certificate{leafC}, SessionTicketsDisabled: true}
	us.Config.IdleTimeout = 200 * time.Millisecond
	us.StartTLS()
	defer us.Close()
	s, p := startProxyS(t, us.Listener.Addr().String(), func(c *Config) {
		c.UpstreamTLS = &UpstreamTLSConfig{RootCAs: ca.Pool}
		c.LB.Health.Disabled = true
	})
	rc := dialRaw(t, p)
	if st := getN(t, rc, 20, "/hello", ""); st[200] != 20 {
		t.Fatalf("keep-alive: %v", st)
	}
	a := s.PoolStats()
	for i := 0; i < 20; i++ {
		time.Sleep(300 * time.Millisecond)
		if st := getN(t, rc, 1, "/hello", ""); st[200] != 1 {
			t.Fatalf("sau upstream đóng rỗi: %v", st)
		}
	}
	b := s.PoolStats()
	t.Logf("(a) 20 request liền: %+v", a)
	t.Logf("(b) +20 request cách 100 ms, upstream idle 50 ms: %+v", b)
	if a.Dials != 1 || a.DeadOnProbe != 0 {
		t.Fatal("(a) keep-alive TLS phải 1 dial, probe không báo sai")
	}
	if b.DeadOnProbe-a.DeadOnProbe < 19 || b.Retries != 0 {
		t.Fatal("(b) probe trên fd TCP phải bắt FIN/close_notify trước khi ghi")
	}
}
