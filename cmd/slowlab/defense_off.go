//go:build nodefense7

package main

// nodefense7: proxy bỏ qua HeaderTimeout (internal/proxy/defense7_off.go).
func headerTimeoutApplied() bool { return false }
