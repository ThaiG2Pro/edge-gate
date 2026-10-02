//go:build nodefense10

package h2

const (
	holdSlotUntilExit = false
	capResetRate      = false
	capHeaderBlock    = false
	validateDowngrade = false
	coalesceCtl       = false
	collapseSettings  = false
)
