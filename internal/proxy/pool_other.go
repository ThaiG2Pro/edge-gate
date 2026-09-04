//go:build !linux

package proxy

import "net"

// probeIdle ngoài Linux: chưa cài ⇒ "không biết", trông vào retry (D4).
func probeIdle(net.Conn) (dead, known bool) { return false, false }
