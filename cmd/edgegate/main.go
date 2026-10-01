// edgegate: reverse proxy L7 từ socket trần. Phase 3-8 (TLS + SNI, SIGHUP reload): phòng tuyến smuggling
// + XFF trust, connection pool theo backend, load balancing + health, rate
// limit / shedding / retry budget / graceful drain (SIGTERM).
package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/thaivro/edgegate/internal/lb"
	"github.com/thaivro/edgegate/internal/proxy"
	"github.com/thaivro/edgegate/internal/tlsx"
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
	DrainIdleGraceMs   *int    `json:"drain_idle_grace_ms"` // nil ⇒ 1000 (D8′); 0 ⇒ đóng connection rỗi ngay
	ReusePort          bool    `json:"reuse_port"`
	SpliceBody         bool    `json:"splice_body"` // phase 9 D5
	// Phase 8: listener TLS thứ hai, vhost theo SNI (D2/D10). Mỗi vhost dùng
	// thuật toán/health/outlier của khối lb ở trên. SIGHUP ⇒ đọc lại cert.
	TLS *struct {
		Listen             string `json:"listen"`
		HandshakeTimeoutMs int    `json:"handshake_timeout_ms"`
		DefaultVHost       string `json:"default_vhost"` // tên trong vhosts; rỗng = SNI lạ bị từ chối (D4)
		VHosts             []struct {
			Names     []string `json:"names"`
			Cert      string   `json:"cert"`
			Key       string   `json:"key"`
			Upstreams []string `json:"upstreams"`
		} `json:"vhosts"`
	} `json:"tls"`
}

func main() {
	path := flag.String("config", "config/dev.json", "file cấu hình JSON")
	listen := flag.String("listen", "", "ghi đè listen")
	upstream := flag.String("upstream", "", "ghi đè upstream")
	nodelay := flag.Bool("nodelay", true, "SetNoDelay(true) trên mọi socket; false CHỈ để đo G4")
	pool := flag.Bool("pool", true, "pool connection tới upstream; false = dial mỗi request (phase 3-4)")
	algo := flag.String("algo", "", "ghi đè lb.algo: rr|leastconn|p2c|chash")
	pprofAddr := flag.String("pprof", "", "listener admin net/http/pprof (phase 9 D10), vd 127.0.0.1:6061; rỗng = tắt")
	splice := flag.Bool("splice", false, "bật splice_body (phase 9 D5) bất kể config")
	flag.Parse()
	if *pprofAddr != "" {
		startPprof(*pprofAddr)
	}

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

	base := proxy.Config{
		Listen:                fc.Listen,
		Upstreams:             fc.Upstreams,
		DialTimeout:           ms(fc.DialTimeoutMs),
		UpstreamHeaderTimeout: ms(fc.UpstreamHeaderTimeoutMs),
		UpstreamBodyTimeout:   ms(fc.UpstreamBodyTimeoutMs),
		NoDelay:               nodelay,
		TrustedProxies:        fc.TrustedProxies,
		Pool: proxy.PoolConfig{
			Disabled:    fc.Pool.Disabled,
			MaxIdle:     fc.Pool.MaxIdle,
			MaxIdleTime: ms(fc.Pool.MaxIdleTimeMs),
		},
		LB:          lbCfg(fc),
		RateLimit:   proxy.RateLimitConfig{Rate: fc.RateLimit.Rate, Burst: fc.RateLimit.Burst, MaxKeys: fc.RateLimit.MaxKeys},
		Shed:        proxy.ShedConfig{MaxInflight: fc.Shed.MaxInflight, MaxQueue: fc.Shed.MaxQueue, QueueTimeout: ms(fc.Shed.QueueTimeoutMs)},
		RetryBudget: proxy.RetryBudgetConfig{Percent: fc.RetryBudgetPercent / 100},
		ReusePort:   fc.ReusePort,
		SpliceBody:  fc.SpliceBody || *splice,
		DrainIdleGrace: func() time.Duration {
			if fc.DrainIdleGraceMs == nil {
				return time.Second // phase 7 turn 2: đóng ngay mất ~99 % request kế của connection rỗi (G8 b)
			}
			return ms(*fc.DrainIdleGraceMs)
		}(),
	}
	servers := []*proxy.Server{proxy.New(base)}

	// Phase 8: listener TLS thứ hai (D10). Cùng mọi cấu hình của base, khác
	// listener + vhost theo SNI. Không có vhost mặc định ⇒ SNI lạ bị từ chối (D4).
	var store *tlsx.CertStore
	if t := fc.TLS; t != nil {
		var entries []tlsx.Entry
		var vhosts []proxy.VHost
		for _, v := range t.VHosts {
			entries = append(entries, tlsx.Entry{Names: v.Names, CertFile: v.Cert, KeyFile: v.Key, Default: t.DefaultVHost != "" && slices.Contains(v.Names, t.DefaultVHost)})
			vhosts = append(vhosts, proxy.VHost{Names: v.Names, Upstreams: v.Upstreams, LB: lbCfg(fc)})
		}
		var err error
		if store, err = tlsx.NewCertStore(entries); err != nil {
			log.Fatal(err)
		}
		tc := base
		tc.Listen, tc.Upstreams, tc.Upstream = t.Listen, nil, ""
		tc.TLS = &proxy.TLSConfig{Store: store}
		tc.VHosts = vhosts
		tc.HandshakeTimeout = ms(t.HandshakeTimeoutMs)
		servers = append(servers, proxy.New(tc))
		log.Printf("edgegate: TLS %s, %d vhost, serial %v", t.Listen, len(vhosts), store.Serials())
	}
	drainTO := ms(fc.DrainTimeoutMs)
	if drainTO == 0 {
		drainTO = 30 * time.Second
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for s := range sig {
			switch s {
			case syscall.SIGHUP:
				// D10: đọc lại cert; hỏng ⇒ giữ bộ cũ (tlsx D1). Connection đang mở không bị ảnh hưởng.
				if store == nil {
					log.Print("edgegate: SIGHUP nhưng không có khối tls")
					continue
				}
				if err := store.Reload(); err != nil {
					log.Printf("edgegate: SIGHUP reload HỎNG, giữ cert cũ %v: %v", store.Serials(), err)
				} else {
					log.Printf("edgegate: SIGHUP reload xong, serial %v", store.Serials())
				}
				continue
			case syscall.SIGTERM:
				// Phase 7 D8: rolling restart ⇒ drain mọi listener song song.
				log.Printf("edgegate: SIGTERM ⇒ drain tối đa %s", drainTO)
				var wg sync.WaitGroup
				var forced atomic.Int64
				for _, srv := range servers {
					wg.Add(1)
					go func() { defer wg.Done(); forced.Add(int64(srv.Drain(drainTO))) }()
				}
				wg.Wait()
				log.Printf("edgegate: drain xong, %d connection bị đóng cưỡng bức", forced.Load())
			default:
				log.Print("edgegate: đóng")
				for _, srv := range servers {
					srv.Close()
				}
			}
			return
		}
	}()

	log.Printf("edgegate: %s → %v lb=%s (nodelay=%v, trusted_proxies=%v, pool=%v max_idle=%d max_idle_time=%dms, health=%v outlier=%v)",
		fc.Listen, fc.Upstreams, orDefault(fc.LB.Algo, "rr"), *nodelay, fc.TrustedProxies, !fc.Pool.Disabled, fc.Pool.MaxIdle, fc.Pool.MaxIdleTimeMs,
		!fc.LB.Health.Disabled, !fc.LB.Outlier.Disabled)
	errc := make(chan error, len(servers))
	for _, srv := range servers {
		go func() { errc <- srv.ListenAndServe() }()
	}
	for range servers {
		if err := <-errc; err != nil {
			log.Fatal(err)
		}
	}
	// Serve trả về NGAY khi Drain đóng listener — thoát ở đây là giết các
	// request đang drain. Chờ goroutine signal làm xong.
	<-stopped
}

func lbCfg(fc fileConfig) lb.Config {
	return lb.Config{
		Algo: fc.LB.Algo, Tau: ms(fc.LB.TauMs), HashHeader: fc.LB.HashHeader, VNodes: fc.LB.VNodes,
		Health: lb.HealthConfig{Disabled: fc.LB.Health.Disabled, Interval: ms(fc.LB.Health.IntervalMs),
			Timeout: ms(fc.LB.Health.TimeoutMs), Path: fc.LB.Health.Path, Fall: fc.LB.Health.Fall, Rise: fc.LB.Health.Rise},
		Outlier: lb.OutlierConfig{Disabled: fc.LB.Outlier.Disabled, Consecutive: fc.LB.Outlier.Consecutive,
			BaseEject: ms(fc.LB.Outlier.BaseEjectMs), MaxEject: ms(fc.LB.Outlier.MaxEjectMs), MaxEjectPercent: fc.LB.Outlier.MaxEjectPercent},
	}
}

func ms(v int) time.Duration { return time.Duration(v) * time.Millisecond }

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
