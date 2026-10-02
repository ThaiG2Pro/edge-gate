//go:build !linux

package proxy

import "net"

// probeIdle ngoài Linux: chưa cài ⇒ "không biết", trông vào retry (D4).
func probeIdle(net.Conn) (dead, known bool) { return false, false }

// peerGone: không phải Linux ⇒ không biết.
func peerGone(c net.Conn) (gone, known bool) { return false, false }
