// Package tlsx: cert cho TLS termination của phase 8 — CertStore chọn cert
// theo SNI và hot-reload bằng atomic.Pointer (D1), và bộ sinh cert lab bằng
// crypto/x509 (D9) để test/lab không cần openssl và không có key nào trong git.
package tlsx

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"
)

// KeyType của cert sinh ra.
type KeyType string

const (
	ECDSA KeyType = "ecdsa" // P-256 (mặc định)
	RSA   KeyType = "rsa"   // 2048 bit — chỉ để so CPU handshake (G6)
)

// CA lab: tự ký, chỉ dùng để ký leaf cho test/lab.
type CA struct {
	Cert *x509.Certificate
	Key  crypto.Signer
	PEM  []byte
	Pool *x509.CertPool
}

func newKey(kt KeyType) (crypto.Signer, error) {
	if kt == RSA {
		return rsa.GenerateKey(rand.Reader, 2048)
	}
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// NewCA sinh một CA ECDSA tự ký hạn 24 h.
func NewCA() (*CA, error) {
	k, err := newKey(ECDSA)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "edgegate lab CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, k.Public(), k)
	if err != nil {
		return nil, err
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(c)
	return &CA{Cert: c, Key: k, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), Pool: pool}, nil
}

// Leaf: cert + key PEM cho names (DNS hoặc IP), ký bởi ca, serial tuỳ chọn
// (hot-reload test phân biệt cert cũ/mới bằng serial).
func (ca *CA) Leaf(names []string, kt KeyType, serial int64) (certPEM, keyPEM []byte, err error) {
	k, err := newKey(kt)
	if err != nil {
		return nil, nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: names[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else {
			tpl.DNSNames = append(tpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.Cert, k.Public(), ca.Key)
	if err != nil {
		return nil, nil, err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}), nil
}

// LeafTLS: như Leaf nhưng trả thẳng tls.Certificate (test/lab in-process).
func (ca *CA) LeafTLS(names []string, kt KeyType, serial int64) (tls.Certificate, error) {
	c, k, err := ca.Leaf(names, kt, serial)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(c, k)
}

// WriteFiles ghi cert/key PEM ra dir/<base>.crt|.key (key mode 0600).
func WriteFiles(dir, base string, certPEM, keyPEM []byte) (certFile, keyFile string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	certFile, keyFile = fmt.Sprintf("%s/%s.crt", dir, base), fmt.Sprintf("%s/%s.key", dir, base)
	if err := os.WriteFile(certFile, certPEM, 0o644); err != nil {
		return "", "", err
	}
	return certFile, keyFile, os.WriteFile(keyFile, keyPEM, 0o600)
}
