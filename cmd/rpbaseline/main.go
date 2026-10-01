// rpbaseline: cột "Go httputil.ReverseProxy" của bảng phase 9 G8. net/http ở
// đây là ĐỐI TƯỢNG SO SÁNH, không phải data path của EdgeGate (như
// cmd/upstream). Transport pool 64 connection mỗi host (D8) — mặc định 2 thì
// so pool với không pool.
package main

import (
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:18092", "địa chỉ lắng nghe")
	upstream := flag.String("upstream", "127.0.0.1:18100", "backend")
	idle := flag.Int("max-idle", 64, "MaxIdleConnsPerHost")
	flag.Parse()

	target := &url.URL{Scheme: "http", Host: *upstream}
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.Transport = &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
		MaxIdleConns:        *idle,
		MaxIdleConnsPerHost: *idle,
		IdleConnTimeout:     30 * time.Second,
	}
	rp.ErrorLog = log.New(io.Discard, "", 0)
	srv := &http.Server{Addr: *listen, Handler: rp, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("rpbaseline: %s → %s, MaxIdleConnsPerHost %d", *listen, *upstream, *idle)
	log.Fatal(srv.ListenAndServe())
}
