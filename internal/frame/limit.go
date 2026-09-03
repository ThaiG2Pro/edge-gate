//go:build !nodefense

package frame

import "fmt"

// checkLength là phòng tuyến duy nhất của bất biến I2. Tách file để build tag
// `nodefense` thay nó bằng phiên bản luôn trả nil — lúc đó TestCapBeforeAlloc
// và fuzz PHẢI đỏ. Nếu vẫn xanh, bộ test không chứng minh gì.
func checkLength(n, max uint32) error {
	if n > max {
		return fmt.Errorf("%w: %d > %d", ErrTooBig, n, max)
	}
	return nil
}
