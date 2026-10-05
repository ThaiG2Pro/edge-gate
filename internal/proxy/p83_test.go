package proxy

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ThaiG2Pro/edge-gate/internal/fixture"
	"github.com/ThaiG2Pro/edge-gate/internal/tlsx"
)

// P8-3 — health check active với upstream TLS: probe phải bắt tay TLS rồi mới
// GET Path. Trước sửa: HTTP thường vào cổng TLS ⇒ fail ⇒ backend unhealthy ⇒ 503.
func TestHealthTLSUpstream(t *testing.T) {
	ca, _ := tlsx.NewCA()
	leaf, err := ca.LeafTLS([]string{"127.0.0.1"}, tlsx.ECDSA, 9)
	if err != nil {
		t.Fatal(err)
	}
	us := httptest.NewUnstartedServer(fixture.Handler())
	us.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	us.StartTLS()
	defer us.Close()
	s, p := startProxyS(t, us.Listener.Addr().String(), func(c *Config) {
		c.UpstreamTLS = &UpstreamTLSConfig{RootCAs: ca.Pool}
		c.LB.Health.Disabled = false
		c.LB.Health.Interval, c.LB.Health.Timeout = 30*time.Millisecond, 500*time.Millisecond
		c.LB.Health.Path, c.LB.Health.Fall, c.LB.Health.Rise = "/hello", 2, 1
	})
	time.Sleep(300 * time.Millisecond) // ≈ 10 probe
	b := s.LBStats().Backends[0]
	if !b.Healthy {
		t.Fatalf("probe TLS: backend unhealthy sau 300 ms (Path /hello): %+v", b)
	}
	rc := dialRaw(t, p)
	if st := getN(t, rc, 3, "/hello", ""); st[200] != 3 {
		t.Fatalf("qua proxy: %v", st)
	}
}
