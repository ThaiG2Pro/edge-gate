package tlsx

import (
	"crypto/tls"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func mustCA(t *testing.T) *CA {
	t.Helper()
	ca, err := NewCA()
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func leaf(t *testing.T, ca *CA, serial int64, names ...string) *tls.Certificate {
	t.Helper()
	c, err := ca.LeafTLS(names, ECDSA, serial)
	if err != nil {
		t.Fatal(err)
	}
	return &c
}

// D1/D4: đúng tên, hoa thường, wildcard một nhãn, SNI lạ ⇒ lỗi khi không có mặc định.
func TestLookup(t *testing.T) {
	ca := mustCA(t)
	s, err := NewCertStore([]Entry{
		{Names: []string{"a.test"}, Cert: leaf(t, ca, 10, "a.test")},
		{Names: []string{"*.b.test"}, Cert: leaf(t, ca, 20, "*.b.test")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int{"a.test": 0, "A.TEST": 0, "a.test.": 0, "x.b.test": 1, "b.test": -1, "y.x.b.test": -1, "c.test": -1, "": -1} {
		if got := s.VHostOf(name); got != want {
			t.Errorf("VHostOf(%q) = %d, muốn %d", name, got, want)
		}
	}
	if _, err := s.GetCertificate(&tls.ClientHelloInfo{ServerName: "c.test"}); !errors.Is(err, ErrUnknownName) {
		t.Fatalf("SNI lạ phải lỗi: %v", err)
	}
	// Có mặc định ⇒ SNI lạ và không SNI rơi vào nó.
	s2, _ := NewCertStore([]Entry{{Names: []string{"a.test"}, Cert: leaf(t, ca, 10, "a.test"), Default: true}})
	if s2.VHostOf("c.test") != 0 || s2.VHostOf("") != 0 {
		t.Fatal("vhost mặc định không nhận SNI lạ")
	}
}

// D1: reload hỏng (key không khớp cert) ⇒ lỗi, bộ cũ giữ nguyên; reload tốt ⇒ serial mới.
func TestReloadKeepsOldOnError(t *testing.T) {
	ca := mustCA(t)
	dir := t.TempDir()
	c1, k1, _ := ca.Leaf([]string{"a.test"}, ECDSA, 100)
	cf, kf, err := WriteFiles(dir, "a", c1, k1)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewCertStore([]Entry{{Names: []string{"a.test"}, CertFile: cf, KeyFile: kf}})
	if err != nil {
		t.Fatal(err)
	}
	// Ghi cert mới nhưng key CŨ của cert khác ⇒ không khớp.
	c2, _, _ := ca.Leaf([]string{"a.test"}, ECDSA, 200)
	_, k3, _ := ca.Leaf([]string{"a.test"}, ECDSA, 300)
	os.WriteFile(cf, c2, 0o644)
	os.WriteFile(kf, k3, 0o600)
	if err := s.Reload(); err == nil {
		t.Fatal("key không khớp mà Reload không lỗi")
	} else {
		t.Logf("reload hỏng: %v", err)
	}
	if got := s.Serials()[0]; got != "100" {
		t.Fatalf("reload hỏng phải giữ serial cũ 100, đang %s", got)
	}
	c4, k4, _ := ca.Leaf([]string{"a.test"}, ECDSA, 400)
	os.WriteFile(filepath.Join(dir, "a.crt"), c4, 0o644)
	os.WriteFile(filepath.Join(dir, "a.key"), k4, 0o600)
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := s.Serials()[0]; got != "400" || s.Loads() != 2 {
		t.Fatalf("reload tốt: serial %s loads %d", got, s.Loads())
	}
}
