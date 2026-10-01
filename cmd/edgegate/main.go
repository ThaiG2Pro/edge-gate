// edgegate: reverse proxy L7 từ socket trần. Phase 3-7: phòng tuyến smuggling
// + XFF trust, connection pool theo backend, load balancing + health, rate
// limit / shedding / retry budget / graceful drain (SIGTERM).
package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/thaivro/edgegate/internal/lb"
	"github.com/thaivro/edgegate/internal/proxy"
)

type fileConfig struct {
	Listen   string `json:"listen"`
	Upstream string `json:"upstream"` // một backend (phase 3-5); bỏ qua nếu có upstreams
	// Upstreams (phase 6): nhiều backend. Rỗng ⇒ [upstream].
	Upstreams               []string `json:"upstreams"`
	DialTimeoutMs           int      `json:"dial_timeout_ms"`
	UpstreamHeaderTimeoutMs int      `json:"upstream_header_timeout_ms"`
	UpstreamBodyTimeoutMs   int      `json:"upstream_body_timeout_ms"`
	// TrustedProxies: CIDR của các proxy đứng trước mà ta tin X-Forwarded-For
	// (phase 4 D9). Rỗng = không tin ai: XFF luôn là IP peer.
	TrustedProxies []string `json:"trusted_proxies"`
	// Pool (phase 5): connection pool tới upstream.
	Pool struct {
		Disabled      bool `json:"disabled"`
		MaxIdle       int  `json:"max_idle"`
		MaxIdleTimeMs int  `json:"max_idle_time_ms"`
	} `json:"pool"`
	// LB (phase 6): algo rr|leastconn|p2c|chash; health + outlier.
	LB struct {
		Algo       string `json:"algo"`
		TauMs      int    `json:"tau_ms"`
		HashHeader string `json:"hash_header"`
		VNodes     int    `json:"vnodes"`
		Health     struct {
			Disabled   bool   `json:"disabled"`
			IntervalMs int    `json:"interval_ms"`
			TimeoutMs  int    `json:"timeout_ms"`
			Path       string `json:"path"`
			Fall       int    `json:"fall"`
			Rise       int    `json:"rise"`
		} `json:"health"`
		Outlier struct {
			Disabled        bool `json:"disabled"`
			Consecutive     int  `json:"consecutive_5xx"`
			BaseEjectMs     int  `json:"base_eject_ms"`
			MaxEjectMs      int  `json:"max_eject_ms"`
			MaxEjectPercent int  `json:"max_eject_percent"`
		} `json:"outlier"`
	} `json:"lb"`
	// Phase 7. Thiếu khối nào thì cơ chế đó tắt (rate_limit, shed) hoặc mặc định
	// (retry_budget 10 %, drain 30 s).
	RateLimit struct {
		Rate    float64 `json:"rate"`
		Burst   float64 `json:"burst"`
		MaxKeys int     `json:"max_keys"`
	} `json:"rate_limit"`
	Shed struct {
		MaxInflight    int `json:"max_inflight"`
		MaxQueue       int `json:"max_queue"`
		QueueTimeoutMs int `json:"queue_timeout_ms"`
	} `json:"shed"`
	RetryBudgetPercent float64 `json:"retry_budget_percent"`
	DrainTimeoutMs     int     `json:"drain_timeout_ms"`
	ReusePort          bool    `json:"reuse_port"`
}

func main() {
	path := flag.String("config", "config/dev.json", "file cấu hình JSON")
	listen := flag.String("listen", "", "ghi đè listen")
	upstream := flag.String("upstream", "", "ghi đè upstream")
	nodelay := flag.Bool("nodelay", true, "SetNoDelay(true) trên mọi socket; false CHỈ để đo G4")
	pool := flag.Bool("pool", true, "pool connection tới upstream; false = dial mỗi request (phase 3-4)")
	algo := flag.String("algo", "", "ghi đè lb.algo: rr|leastconn|p2c|chash")
	flag.Parse()

	var fc fileConfig
	if b, err := os.ReadFile(*path); err != nil {
		log.Fatalf("đọc %s: %v", *path, err)
	} else if err := json.Unmarshal(b, &fc); err != nil {
		log.Fatalf("parse %s: %v", *path, err)
	}
	if *listen != "" {
		fc.Listen = *listen
	}
	if *upstream != "" {
		fc.Upstream = *upstream
	}
	if len(fc.Upstreams) == 0 && fc.Upstream != "" {
		fc.Upstreams = []string{fc.Upstream}
	}
	if fc.Listen == "" || len(fc.Upstreams) == 0 {
		log.Fatal("cần listen và upstream/upstreams")
	}
	if *algo != "" {
		fc.LB.Algo = *algo
	}
	if !*pool {
		fc.Pool.Disabled = true
	}

	srv := proxy.New(proxy.Config{
		Listen:                fc.Listen,
		Upstreams:             fc.Upstreams,
		DialTimeout:           time.Duration(fc.DialTimeoutMs) * time.Millisecond,
		UpstreamHeaderTimeout: time.Duration(fc.UpstreamHeaderTimeoutMs) * time.Millisecond,
		UpstreamBodyTimeout:   time.Duration(fc.UpstreamBodyTimeoutMs) * time.Millisecond,
		NoDelay:               nodelay,
		TrustedProxies:        fc.TrustedProxies,
		Pool: proxy.PoolConfig{
			Disabled:    fc.Pool.Disabled,
			MaxIdle:     fc.Pool.MaxIdle,
			MaxIdleTime: time.Duration(fc.Pool.MaxIdleTimeMs) * time.Millisecond,
		},
		LB: lb.Config{
			Algo: fc.LB.Algo, Tau: ms(fc.LB.TauMs), HashHeader: fc.LB.HashHeader, VNodes: fc.LB.VNodes,
			Health: lb.HealthConfig{Disabled: fc.LB.Health.Disabled, Interval: ms(fc.LB.Health.IntervalMs),
				Timeout: ms(fc.LB.Health.TimeoutMs), Path: fc.LB.Health.Path, Fall: fc.LB.Health.Fall, Rise: fc.LB.Health.Rise},
			Outlier: lb.OutlierConfig{Disabled: fc.LB.Outlier.Disabled, Consecutive: fc.LB.Outlier.Consecutive,
				BaseEject: ms(fc.LB.Outlier.BaseEjectMs), MaxEject: ms(fc.LB.Outlier.MaxEjectMs), MaxEjectPercent: fc.LB.Outlier.MaxEjectPercent},
		},
		RateLimit:   proxy.RateLimitConfig{Rate: fc.RateLimit.Rate, Burst: fc.RateLimit.Burst, MaxKeys: fc.RateLimit.MaxKeys},
		Shed:        proxy.ShedConfig{MaxInflight: fc.Shed.MaxInflight, MaxQueue: fc.Shed.MaxQueue, QueueTimeout: ms(fc.Shed.QueueTimeoutMs)},
		RetryBudget: proxy.RetryBudgetConfig{Percent: fc.RetryBudgetPercent / 100},
		ReusePort:   fc.ReusePort,
	})
	drainTO := ms(fc.DrainTimeoutMs)
	if drainTO == 0 {
		drainTO = 30 * time.Second
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		// D8: SIGTERM (rolling restart) ⇒ drain; SIGINT (Ctrl-C) ⇒ đóng ngay.
		if s := <-sig; s == syscall.SIGTERM {
			log.Printf("edgegate: SIGTERM ⇒ drain tối đa %s", drainTO)
			forced := srv.Drain(drainTO)
			log.Printf("edgegate: drain xong, %d connection bị đóng cưỡng bức", forced)
			return
		}
		log.Print("edgegate: đóng")
		srv.Close()
	}()

	log.Printf("edgegate: %s → %v lb=%s (nodelay=%v, trusted_proxies=%v, pool=%v max_idle=%d max_idle_time=%dms, health=%v outlier=%v)",
		fc.Listen, fc.Upstreams, orDefault(fc.LB.Algo, "rr"), *nodelay, fc.TrustedProxies, !fc.Pool.Disabled, fc.Pool.MaxIdle, fc.Pool.MaxIdleTimeMs,
		!fc.LB.Health.Disabled, !fc.LB.Outlier.Disabled)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
	// Serve trả về NGAY khi Drain đóng listener — thoát ở đây là giết các
	// request đang drain. Chờ goroutine signal làm xong.
	<-stopped
}

func ms(v int) time.Duration { return time.Duration(v) * time.Millisecond }

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
