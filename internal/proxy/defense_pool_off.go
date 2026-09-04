//go:build nodefensepool

package proxy

// Tắt kiểm sạch phase 5: connection về pool ngay khi có head response.
const poolCheckClean = false
