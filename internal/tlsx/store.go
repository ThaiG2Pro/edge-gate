package tlsx

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// Entry: một vhost — các tên nó phục vụ và cặp cert/key. Hoặc file (CertFile/
// KeyFile, đọc lại được khi Reload) hoặc cert dựng sẵn (Cert, test/lab).
type Entry struct {
	Names    []string
	CertFile string
	KeyFile  string
	Cert     *tls.Certificate
	Default  bool // dùng cho SNI lạ / không SNI (D4). Tối đa một entry
}

// certSet bất biến sau khi dựng: reader lấy con trỏ, writer thay cả bộ (D1).
type certSet struct {
	byName map[string]*tls.Certificate // tên thường (lowercase) → cert; "*.x" cho wildcard
	def    *tls.Certificate
	vhost  map[*tls.Certificate]int // cert → chỉ số entry (proxy dùng để biết vhost)
	serial map[int]string           // chỉ số entry → serial (log, test)
}

// CertStore: chọn cert theo SNI, hot-reload không khoá phía đọc.
type CertStore struct {
	cur     atomic.Pointer[certSet]
	mu      sync.Mutex // chỉ để Reload tuần tự
	entries []Entry
	loads   atomic.Int64
}

// NewCertStore nạp entries; lỗi ⇒ không có store.
func NewCertStore(entries []Entry) (*CertStore, error) {
	s := &CertStore{entries: entries}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Reload đọc lại mọi file và dựng bộ MỚI; một entry hỏng ⇒ giữ nguyên bộ cũ,
// trả lỗi (D1). Connection đã handshake giữ cert nó thoả thuận — không bị ảnh hưởng.
func (s *CertStore) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(s.entries)
}

// Replace thay danh sách entry rồi nạp (test/lab hot-reload không qua file).
func (s *CertStore) Replace(entries []Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(entries); err != nil {
		return err
	}
	s.entries = entries
	return nil
}

func (s *CertStore) load(entries []Entry) error {
	set := &certSet{byName: map[string]*tls.Certificate{}, vhost: map[*tls.Certificate]int{}, serial: map[int]string{}}
	for i, e := range entries {
		var c *tls.Certificate
		if e.Cert != nil {
			cc := *e.Cert
			c = &cc
		} else {
			cc, err := tls.LoadX509KeyPair(e.CertFile, e.KeyFile)
			if err != nil {
				return fmt.Errorf("tlsx: vhost %v: %w", e.Names, err)
			}
			c = &cc
		}
		if c.Leaf == nil && len(c.Certificate) > 0 {
			leaf, err := x509.ParseCertificate(c.Certificate[0])
			if err != nil {
				return fmt.Errorf("tlsx: vhost %v: %w", e.Names, err)
			}
			c.Leaf = leaf
		}
		if len(e.Names) == 0 {
			return fmt.Errorf("tlsx: entry %d không có tên", i)
		}
		for _, n := range e.Names {
			n = strings.ToLower(n)
			if _, dup := set.byName[n]; dup {
				return fmt.Errorf("tlsx: tên %q ở hai vhost", n)
			}
			set.byName[n] = c
		}
		if e.Default {
			if set.def != nil {
				return errors.New("tlsx: hơn một vhost mặc định")
			}
			set.def = c
		}
		set.vhost[c] = i
		set.serial[i] = c.Leaf.SerialNumber.String()
	}
	s.cur.Store(set)
	s.loads.Add(1)
	return nil
}

// ErrUnknownName: SNI không khớp vhost nào và không có vhost mặc định (D4).
var ErrUnknownName = errors.New("tlsx: không có vhost cho SNI này")

// lookup: đúng tên → wildcard một nhãn → mặc định.
func (set *certSet) lookup(name string) *tls.Certificate {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if c := set.byName[name]; c != nil {
		return c
	}
	if i := strings.IndexByte(name, '.'); i > 0 {
		if c := set.byName["*"+name[i:]]; c != nil {
			return c
		}
	}
	return set.def
}

// GetCertificate cho tls.Config.
func (s *CertStore) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if c := s.cur.Load().lookup(hello.ServerName); c != nil {
		return c, nil
	}
	return nil, ErrUnknownName
}

// VHostOf: chỉ số entry (vhost) ứng với tên — cùng luật với GetCertificate,
// để proxy gắn connection vào ĐÚNG vhost của cert vừa thoả thuận. -1 = không có.
func (s *CertStore) VHostOf(serverName string) int {
	set := s.cur.Load()
	if c := set.lookup(serverName); c != nil {
		return set.vhost[c]
	}
	return -1
}

// Serials: serial hiện tại của từng entry (log reload, test).
func (s *CertStore) Serials() []string {
	set := s.cur.Load()
	out := make([]string, len(set.serial))
	for i, v := range set.serial {
		out[i] = v
	}
	return out
}

// Loads: số lần nạp thành công (kể cả lần đầu).
func (s *CertStore) Loads() int64 { return s.loads.Load() }

// ServerConfig: tls.Config cho listener — GetCertificate từ store, ALPN
// http/1.1 (D5), thêm "h2" khi h2 (P10-5), TLS ≥ 1.2.
//
// h2 ⇒ cipher TLS 1.2 chỉ còn ECDHE + AEAD (RFC 9113 §9.2.2 cấm blocklist
// Appendix A: không ECDHE, CBC, 3DES…). Giới hạn ở tầng cipher chứ không bắt
// tay rồi GOAWAY INADEQUATE_SECURITY: client CBC-only thấy handshake hỏng, rõ
// hơn một connection h2 chết. TLS 1.3 không bị ảnh hưởng (Go không cho chọn).
//
// SNI lạ (P8-2, phase 8 turn 3): GetConfigForClient trả một config KHÔNG có cert
// nào ⇒ crypto/tls đi nhánh errNoCertificates ⇒ alert unrecognized_name(112)
// đúng RFC 6066 §3. Trả lỗi từ GetCertificate thì Go luôn gửi internal_error
// (handshake_server_tls13.go: pickCertificate) — client thấy "internal error"
// cho một chuyện không phải lỗi nội bộ.
func (s *CertStore) ServerConfig(h2 bool) *tls.Config {
	protos := []string{"http/1.1"}
	var suites []uint16
	if h2 {
		protos = []string{"h2", "http/1.1"}
		suites = H2CipherSuites()
	}
	return &tls.Config{
		GetCertificate: s.GetCertificate,
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			if s.cur.Load().lookup(hello.ServerName) == nil {
				return &tls.Config{NextProtos: protos, MinVersion: tls.VersionTLS12, CipherSuites: suites}, nil
			}
			return nil, nil // nil ⇒ dùng config gốc
		},
		NextProtos:   protos,
		MinVersion:   tls.VersionTLS12,
		CipherSuites: suites,
	}
}

// H2CipherSuites: cipher TLS 1.2 được phép với h2 (RFC 9113 §9.2.2) — lọc từ
// tls.CipherSuites() (đã bỏ nhóm insecure) lấy ECDHE + AEAD (GCM/CHACHA20).
func H2CipherSuites() []uint16 {
	var out []uint16
	for _, cs := range tls.CipherSuites() {
		n := cs.Name
		if strings.HasPrefix(n, "TLS_ECDHE_") && (strings.Contains(n, "_GCM_") || strings.Contains(n, "CHACHA20")) {
			out = append(out, cs.ID)
		}
	}
	return out
}
