package proxy

// Phase 8: TLS termination + SNI routing. Listener TLS (D7: accept raw, bọc
// tls.Server trong goroutine của connection), handshake tường minh có deadline
// riêng (D6), vhost theo SNI và Host phải khớp (D2/D3 — 421), upstream TLS +
// probe FIN trên conn TCP bên dưới (D8).

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/thaivro/edgegate/internal/httpx"
	"github.com/thaivro/edgegate/internal/lb"
	"github.com/thaivro/edgegate/internal/tlsx"
)

// VHost (D2): một server_name — tên, nhóm upstream, LB riêng. Chỉ số trong
// Config.VHosts trùng chỉ số Entry trong TLS.Store (tlsx.CertStore.VHostOf).
type VHost struct {
	Names     []string
	Upstreams []string
	LB        lb.Config
}

// TLSConfig: listener TLS. Store chọn cert theo SNI (tlsx, D1).
type TLSConfig struct {
	Store *tlsx.CertStore
}

// UpstreamTLSConfig (D8): proxy nói TLS tới upstream. ServerName rỗng ⇒ host
// của addr. RootCAs nil ⇒ CA hệ thống.
type UpstreamTLSConfig struct {
	ServerName string
	RootCAs    *x509.CertPool
}

type vhostRT struct {
	names []string // lowercase; "*.x" = wildcard một nhãn
	lb    *lb.Balancer
}

func (v *vhostRT) matches(host string) bool {
	for _, n := range v.names {
		if n == host {
			return true
		}
		if strings.HasPrefix(n, "*.") {
			if i := strings.IndexByte(host, '.'); i > 0 && host[i:] == n[1:] {
				return true
			}
		}
	}
	return false
}

func (s *Server) initVHosts() {
	for _, vh := range s.cfg.VHosts {
		if vh.LB.Logf == nil {
			vh.LB.Logf = s.cfg.Logf
		}
		bl, err := lb.New(vh.Upstreams, vh.LB)
		if err != nil {
			panic(fmt.Sprintf("proxy: vhost %v: %v", vh.Names, err))
		}
		rt := &vhostRT{lb: bl}
		for _, n := range vh.Names {
			rt.names = append(rt.names, strings.ToLower(n))
		}
		s.vhosts = append(s.vhosts, rt)
		bl.Start()
	}
	if s.cfg.TLS != nil {
		s.tlsCfg = s.cfg.TLS.Store.ServerConfig()
	}
	if u := s.cfg.UpstreamTLS; u != nil {
		s.upTLSCache = tls.NewLRUClientSessionCache(256)
	}
}

// hostOf: Host không port, lowercase.
func hostOf(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// balancerFor (D2/D3): balancer phục vụ request này, hoặc status lỗi.
//   - Connection TLS: vhost = vhost của SNI. Host phải thuộc vhost đó, lệch ⇒
//     421 (domain fronting). nodefense8: bỏ SNI, chọn theo Host.
//   - Plaintext: vhost theo Host; không khớp ⇒ vhost mặc định (Upstreams) nếu
//     có, không thì 421.
func (s *Server) balancerFor(st *connState, req *httpx.Request) (*lb.Balancer, int) {
	host := hostOf(req.Header.Get("Host"))
	if st.tls != nil && sniPinsVHost && len(s.vhosts) > 0 {
		i := s.cfg.TLS.Store.VHostOf(st.tls.ConnectionState().ServerName)
		if i < 0 || i >= len(s.vhosts) {
			return nil, 421
		}
		if !s.vhosts[i].matches(host) {
			s.res.misdirected.Add(1)
			return nil, 421
		}
		return s.vhosts[i].lb, 0
	}
	for _, v := range s.vhosts {
		if v.matches(host) {
			return v.lb, 0
		}
	}
	if s.lb != nil {
		return s.lb, 0
	}
	s.res.misdirected.Add(1)
	return nil, 421
}

// handshake (D6): bọc raw bằng tls.Server và bắt tay NGAY, trong deadline
// HandshakeTimeout — chỗ đọc socket thứ 8 của I3. Hỏng ⇒ false (không có gì
// để trả lời HTTP). nodefense8: không bắt tay ở đây ⇒ handshake lười ở lần
// Peek đầu, dưới deadline IdleTimeout.
func (s *Server) handshake(raw net.Conn, st *connState) (*tls.Conn, bool) {
	tc := tls.Server(raw, s.tlsCfg)
	st.tls = tc
	if !explicitHandshake {
		return tc, true
	}
	raw.SetDeadline(time.Now().Add(s.cfg.HandshakeTimeout))
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.HandshakeTimeout)
	defer cancel()
	if err := tc.HandshakeContext(ctx); err != nil {
		s.res.handshakeFail.Add(1)
		return nil, false
	}
	raw.SetDeadline(time.Time{})
	return tc, true
}

// dialUpstream: TCP, rồi TLS nếu cấu hình (D8). Trả conn để nói HTTP và conn
// TCP bên dưới (probe FIN của phase 5 cần fd thật).
func (s *Server) dialUpstream(addr string) (c, raw net.Conn, err error) {
	raw, err = net.DialTimeout("tcp", addr, s.cfg.DialTimeout)
	if err != nil {
		return nil, nil, err
	}
	s.setNoDelay(raw)
	u := s.cfg.UpstreamTLS
	if u == nil {
		return raw, raw, nil
	}
	name := u.ServerName
	if name == "" {
		name, _, _ = net.SplitHostPort(addr)
	}
	tc := tls.Client(raw, &tls.Config{ServerName: name, RootCAs: u.RootCAs, NextProtos: []string{"http/1.1"},
		ClientSessionCache: s.upTLSCache, MinVersion: tls.VersionTLS12})
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.DialTimeout)
	defer cancel()
	if err := tc.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, nil, err
	}
	return tc, raw, nil
}
