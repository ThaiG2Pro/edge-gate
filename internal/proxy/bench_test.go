package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/thaivro/edgegate/internal/httpx"
)

// Phase 9 G1: allocs/op, B/op của PROXY. Client và upstream tối giản, gần như
// không cấp phát (request/response dựng sẵn, đọc độ dài đã biết) — để số đo
// là của proxy, không phải của net/http hai đầu.

// fixedUpstream: mỗi head request (tới \r\n\r\n, không body) ⇒ ghi resp.
func fixedUpstream(b *testing.B, resp []byte) string {
	b.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReaderSize(c, 16<<10)
				for {
					for {
						line, err := br.ReadSlice('\n')
						if err != nil {
							return
						}
						if len(line) <= 2 {
							break
						}
					}
					if _, err := c.Write(resp); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func benchResponse(n int) []byte {
	head := "HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nContent-Length: " + strconv.Itoa(n) + "\r\n\r\n"
	return append([]byte(head), bytes.Repeat([]byte{'x'}, n)...)
}

func startBenchProxy(b *testing.B, up string, mut func(*Config)) string {
	b.Helper()
	lim := httpx.DefaultLimits()
	cfg := Config{Listen: "127.0.0.1:0", Upstream: up, Limits: lim, DialTimeout: time.Second,
		UpstreamHeaderTimeout: 5 * time.Second, UpstreamBodyTimeout: 5 * time.Second,
		Logf: func(string, ...any) {}}
	cfg.LB.Health.Disabled = true
	if mut != nil {
		mut(&cfg)
	}
	s := New(cfg)
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		b.Fatal(err)
	}
	go s.Serve(ln)
	b.Cleanup(func() { s.Close() })
	return ln.Addr().String()
}

// proxyHeadLen: độ dài head response proxy ghi lại cho benchResponse(n) —
// đo một lần để client đọc đúng số byte mà không parse.
func proxyRespLen(b *testing.B, addr string, req []byte) int {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	c.Write(req)
	resp, err := httpx.ReadResponse(bufio.NewReader(c), httpx.DefaultLimits(), "GET")
	if err != nil {
		b.Fatal(err)
	}
	var w bytes.Buffer
	resp.WriteHead(&w)
	n, _ := io.Copy(io.Discard, resp.Body)
	return w.Len() + int(n)
}

func benchKeepAlive(b *testing.B, size int, mut func(*Config)) {
	up := fixedUpstream(b, benchResponse(size))
	p := startBenchProxy(b, up, mut)
	req := []byte("GET /x HTTP/1.1\r\nHost: bench\r\nUser-Agent: bench\r\nAccept: */*\r\n\r\n")
	n := proxyRespLen(b, p, req)
	c, err := net.Dial("tcp", p)
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	buf := make([]byte, n)
	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Write(req); err != nil {
			b.Fatal(err)
		}
		if _, err := io.ReadFull(c, buf); err != nil {
			b.Fatal(err)
		}
	}
}

// G1: GET nhỏ keep-alive — một connection client, một connection upstream.
func BenchmarkProxyKeepAlive(b *testing.B) {
	for _, size := range []int{1 << 10} {
		b.Run(fmt.Sprintf("body=%d", size), func(b *testing.B) { benchKeepAlive(b, size, nil) })
	}
}

// G4 đơn vị: body 1 MiB — copy userspace vs splice (D5).
func BenchmarkProxyLarge(b *testing.B) {
	for _, splice := range []bool{false, true} {
		b.Run(fmt.Sprintf("splice=%v", splice), func(b *testing.B) {
			benchKeepAlive(b, 1<<20, func(c *Config) { c.NoSplice = !splice })
		})
	}
}
