package main

// P5-4: đo tần suất "body vào connection chết giữa probe và Write ⇒ 502".
//
// Upstream raw (net + httpx, không net/http): sau MỖI response chọn idle
// timeout ngẫu nhiên 1-50 ms; hết hạn mà chưa có byte request kế ⇒ đóng (FIN)
// — giống keepalive_timeout thật nhưng cố ý ngắn và lệch nhau để cửa sổ
// probe→Write bị trúng nhiều nhất có thể. Client: `conns` worker keep-alive,
// POST body 1 KiB, nghỉ ngẫu nhiên 0-60 ms giữa hai request (để request kế
// rơi vào mọi pha của idle timeout upstream). Đếm 502 qua proxy probe on/off.

import (
	"bufio"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ThaiG2Pro/edge-gate/internal/httpx"
	"github.com/ThaiG2Pro/edge-gate/internal/proxy"
)

func idleRaceUpstream() (string, *atomic.Int64) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	var idleCloses atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					req, err := httpx.ReadRequest(br, lim)
					if err != nil {
						return
					}
					io.Copy(io.Discard, req.Body)
					io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
					d := time.Millisecond + rand.N(49*time.Millisecond)
					c.SetReadDeadline(time.Now().Add(d))
					if _, err := br.Peek(1); err != nil {
						idleCloses.Add(1)
						return // FIN khi rỗi
					}
					c.SetReadDeadline(time.Time{})
				}
			}()
		}
	}()
	return ln.Addr().String(), &idleCloses
}

func runIdleRace(n, conns int) {
	body := strings.Repeat("x", 1024)
	req := fmt.Sprintf("POST /up HTTP/1.1\r\nHost: h\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	fmt.Printf("P5-4 idlerace: upstream đóng rỗi sau 1-50 ms ngẫu nhiên · POST 1 KiB · n=%d · conns=%d · nghỉ 0-60 ms\n", n, conns)
	for _, probe := range []bool{true, false} {
		up, idleCloses := idleRaceUpstream()
		p := probe
		srv := proxy.New(proxy.Config{Listen: "127.0.0.1:0", Upstream: up,
			Pool: proxy.PoolConfig{Probe: &p}, Logf: func(string, ...any) {}})
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fatal(err)
		}
		go srv.Serve(ln)
		var mu sync.Mutex
		status := map[int]int{}
		var errs int
		var next atomic.Int64
		var wg sync.WaitGroup
		t0 := time.Now()
		for w := 0; w < conns; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var c net.Conn
				var br *bufio.Reader
				for next.Add(1) <= int64(n) {
					if c == nil {
						var derr error
						if c, derr = net.Dial("tcp", ln.Addr().String()); derr != nil {
							mu.Lock()
							errs++
							mu.Unlock()
							continue
						}
						br = bufio.NewReader(c)
					}
					c.SetDeadline(time.Now().Add(5 * time.Second))
					st := 0
					if _, err := io.WriteString(c, req); err == nil {
						if resp, err := httpx.ReadResponse(br, lim, "POST"); err == nil {
							io.Copy(io.Discard, resp.Body)
							st = resp.Status
							if resp.Close {
								c.Close()
								c = nil
							}
						}
					}
					mu.Lock()
					if st == 0 {
						errs++
						c.Close()
						c = nil
					} else {
						status[st]++
					}
					mu.Unlock()
					time.Sleep(rand.N(60 * time.Millisecond))
				}
				if c != nil {
					c.Close()
				}
			}()
		}
		wg.Wait()
		st := srv.PoolStats()
		srv.Close()
		ln.Close()
		total := errs
		for _, v := range status {
			total += v
		}
		fmt.Printf("\n  probe=%v · %s · upstream đóng rỗi %d lần\n", probe, time.Since(t0).Round(time.Millisecond), idleCloses.Load())
		fmt.Printf("    status %v · lỗi client %d · 502 = %d/%d = %.3f %%\n", status, errs, status[502], total, 100*float64(status[502])/float64(total))
		fmt.Printf("    pool %+v\n", st)
	}
}
