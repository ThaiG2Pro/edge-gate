// edgegate: reverse proxy L7 từ socket trần. Phase 3-5: một backend, phòng
// tuyến smuggling + XFF trust, connection pool tới upstream.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/thaivro/edgegate/internal/proxy"
)

type fileConfig struct {
	Listen                  string `json:"listen"`
	Upstream                string `json:"upstream"`
	DialTimeoutMs           int    `json:"dial_timeout_ms"`
	UpstreamHeaderTimeoutMs int    `json:"upstream_header_timeout_ms"`
	UpstreamBodyTimeoutMs   int    `json:"upstream_body_timeout_ms"`
	// TrustedProxies: CIDR của các proxy đứng trước mà ta tin X-Forwarded-For
	// (phase 4 D9). Rỗng = không tin ai: XFF luôn là IP peer.
	TrustedProxies []string `json:"trusted_proxies"`
	// Pool (phase 5): connection pool tới upstream.
	Pool struct {
		Disabled      bool `json:"disabled"`
		MaxIdle       int  `json:"max_idle"`
		MaxIdleTimeMs int  `json:"max_idle_time_ms"`
	} `json:"pool"`
}

func main() {
	path := flag.String("config", "config/dev.json", "file cấu hình JSON")
	listen := flag.String("listen", "", "ghi đè listen")
	upstream := flag.String("upstream", "", "ghi đè upstream")
	nodelay := flag.Bool("nodelay", true, "SetNoDelay(true) trên mọi socket; false CHỈ để đo G4")
	pool := flag.Bool("pool", true, "pool connection tới upstream; false = dial mỗi request (phase 3-4)")
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
	if fc.Listen == "" || fc.Upstream == "" {
		log.Fatal("cần listen và upstream")
	}
	if !*pool {
		fc.Pool.Disabled = true
	}

	srv := proxy.New(proxy.Config{
		Listen:                fc.Listen,
		Upstream:              fc.Upstream,
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
	})

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Print("edgegate: đóng")
		srv.Close()
	}()

	log.Printf("edgegate: %s → %s (nodelay=%v, trusted_proxies=%v, pool=%v max_idle=%d max_idle_time=%dms)",
		fc.Listen, fc.Upstream, *nodelay, fc.TrustedProxies, !fc.Pool.Disabled, fc.Pool.MaxIdle, fc.Pool.MaxIdleTimeMs)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
