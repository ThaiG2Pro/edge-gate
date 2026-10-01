//go:build nodefense7

package proxy

// nodefense7: Slowloris không bị cắt, rate limit đọc XFF thô.
const (
	headerTimeoutOn  = false
	limitByTrustedIP = false
)
