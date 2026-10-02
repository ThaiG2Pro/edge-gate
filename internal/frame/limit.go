//go:build !nodefense && !nodefense1

package frame

import "fmt"

// checkLength là phòng tuyến duy nhất của bất biến I2. Tách file để build tag
// `nodefense1` (hoặc `nodefense` = mọi phase 1/3/4) thay nó bằng phiên bản luôn trả nil — lúc đó TestCapBeforeAlloc
// và fuzz PHẢI đỏ. Nếu vẫn xanh, bộ test không chứng minh gì.
func checkLength(n, max uint32) error {
	if n > max {
		return fmt.Errorf("%w: %d > %d", ErrTooBig, n, max)
	}
	return nil
}
