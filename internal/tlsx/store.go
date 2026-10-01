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
// http/1.1 (D5, móc cho phase 10), TLS ≥ 1.2.
func (s *CertStore) ServerConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: s.GetCertificate,
		NextProtos:     []string{"http/1.1"},
		MinVersion:     tls.VersionTLS12,
	}
}
