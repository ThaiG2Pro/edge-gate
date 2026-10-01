//go:build !nodefense7

package limit

// Phòng tuyến phase 7 (D12). Tag nodefense7 lật cả hai — xem defense7_off.go.
const (
	limiterCapped = true // KeyedLimiter có trần MaxKeys (G4 bộ nhớ)
	retryBudgetOn = true // RetryBudget từ chối khi hết budget (G6)
)
