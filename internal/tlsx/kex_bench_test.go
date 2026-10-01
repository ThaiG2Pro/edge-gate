// Microbench phase 8 G6: giá từng phép mật mã của một handshake, đo cả hai phía.
// go test ./internal/tlsx -run ^$ -bench . -benchtime 2000x -count 3
package tlsx

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"testing"
)

// Một lượt key exchange đủ HAI phía: client sinh khoá, server sinh khoá + tính bí mật chung, client tính.
func BenchmarkX25519(b *testing.B) {
	for b.Loop() {
		c, _ := ecdh.X25519().GenerateKey(rand.Reader)
		s, _ := ecdh.X25519().GenerateKey(rand.Reader)
		s.ECDH(c.PublicKey())
		c.ECDH(s.PublicKey())
	}
}

// ML-KEM-768: client sinh khoá decaps, server encapsulate, client decapsulate.
func BenchmarkMLKEM768(b *testing.B) {
	for b.Loop() {
		dk, _ := mlkem.GenerateKey768()
		_, ct := dk.EncapsulationKey().Encapsulate()
		dk.Decapsulate(ct)
	}
}

var msg = sha256.Sum256([]byte("transcript"))

func BenchmarkECDSAP256SignVerify(b *testing.B) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	for b.Loop() {
		sig, _ := ecdsa.SignASN1(rand.Reader, k, msg[:])
		ecdsa.VerifyASN1(&k.PublicKey, msg[:], sig)
	}
}

func BenchmarkRSA2048SignVerify(b *testing.B) {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	for b.Loop() {
		sig, _ := rsa.SignPSS(rand.Reader, k, 5, msg[:], nil)
		rsa.VerifyPSS(&k.PublicKey, 5, msg[:], sig, nil)
	}
}
