//go:build nodefense7

package limit

// nodefense7: map per-IP không trần (rò theo số IP attacker gửi), retry mù.
const (
	limiterCapped = false
	retryBudgetOn = false
)
