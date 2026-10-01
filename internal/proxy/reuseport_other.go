//go:build !linux

package proxy

// Ngoài Linux drainlab không chạy (D9); setsockopt option 0 sẽ trả lỗi rõ ràng.
const soReusePort = 0
