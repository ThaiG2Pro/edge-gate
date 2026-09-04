//go:build nodefense

package proxy

// Tắt hai phòng tuyến phase 3: bẫy #3 (drain) và bẫy #2 (io.Copy thô).
// `make proxylab-nodefense` PHẢI đỏ.
const (
	drainOnUpstreamError = false
	rawCopyResponse      = true
)
