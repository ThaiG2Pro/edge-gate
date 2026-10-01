package main

// Phase 9 D10: pprof trên listener ADMIN riêng. net/http ở đây không chạm data
// path — data path vẫn là internal/proxy; listener này chỉ phục vụ
// /debug/pprof/* và tắt mặc định.

import (
	"log"
	"net/http"
	_ "net/http/pprof"
)

func startPprof(addr string) {
	go func() {
		log.Printf("edgegate: pprof http://%s/debug/pprof/", addr)
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Printf("edgegate: pprof: %v", err)
		}
	}()
}
