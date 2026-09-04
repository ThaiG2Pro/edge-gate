// edgegate: reverse proxy L7 từ socket trần. Phase 3-4: một backend, không pool,
// phòng tuyến smuggling + XFF trust.
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
}

func main() {
	path := flag.String("config", "config/dev.json", "file cấu hình JSON")
	listen := flag.String("listen", "", "ghi đè listen")
	upstream := flag.String("upstream", "", "ghi đè upstream")
	nodelay := flag.Bool("nodelay", true, "SetNoDelay(true) trên mọi socket; false CHỈ để đo G4")
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

	srv := proxy.New(proxy.Config{
		Listen:                fc.Listen,
		Upstream:              fc.Upstream,
		DialTimeout:           time.Duration(fc.DialTimeoutMs) * time.Millisecond,
		UpstreamHeaderTimeout: time.Duration(fc.UpstreamHeaderTimeoutMs) * time.Millisecond,
		UpstreamBodyTimeout:   time.Duration(fc.UpstreamBodyTimeoutMs) * time.Millisecond,
		NoDelay:               nodelay,
		TrustedProxies:        fc.TrustedProxies,
	})

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Print("edgegate: đóng")
		srv.Close()
	}()

	log.Printf("edgegate: %s → %s (nodelay=%v, trusted_proxies=%v)", fc.Listen, fc.Upstream, *nodelay, fc.TrustedProxies)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
