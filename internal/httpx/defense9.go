//go:build !nodefense9

package httpx

// poolHead (phase 9 D1): WriteHead lấy buffer head từ pool. false (nodefense9)
// ⇒ make mỗi lần.
const poolHead = true
