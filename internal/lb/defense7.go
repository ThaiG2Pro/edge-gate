//go:build !nodefense7

package lb

// breakerHalfOpen (phase 7 D5): hết hạn eject ⇒ half-open, cho ĐÚNG MỘT
// request thử. `-tags nodefense7` ⇒ hành vi phase 6: hết hạn là dùng lại cho
// mọi Pick — "mở lại hết" đúng lúc backend vừa hồi (G5 phải đỏ).
const breakerHalfOpen = true
