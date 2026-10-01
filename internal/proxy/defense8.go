//go:build !nodefense8

package proxy

// Phòng tuyến phase 8 (D12). `-tags nodefense8` tắt và `make tlslab-nodefense`
// PHẢI đỏ đúng dòng.
const (
	// sniPinsVHost (D3): connection TLS gắn với vhost của SNI; Host lệch ⇒ 421.
	// false ⇒ chọn vhost theo Host — domain fronting (cert của A, nội dung của B).
	sniPinsVHost = true
	// explicitHandshake (D6): bắt tay ngay sau Accept trong HandshakeTimeout.
	// false ⇒ handshake lười ở Peek đầu, dưới deadline IdleTimeout.
	explicitHandshake = true
)
