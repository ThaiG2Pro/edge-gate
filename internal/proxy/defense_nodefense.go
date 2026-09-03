//go:build nodefense

package proxy

// Bản tắt phòng tuyến, CHỈ để chứng minh bộ test đỏ đúng chỗ:
//
//	make proxylab-nodefense   # phải ĐỎ ở TestDrainOnUpstreamDown và TestRawCopyTrap
const (
	drainOnUpstreamError = false
	rawCopyResponse      = true
)
