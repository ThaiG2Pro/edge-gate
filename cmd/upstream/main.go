// upstream: backend giả cho phase 3. Dùng net/http vì nó LÀ backend — phần
// duy nhất của repo (ngoài _test.go) được phép. Proxy không import gì từ đây.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/thaivro/edgegate/internal/fixture"
)

func main() {
	addr := flag.String("addr", ":8081", "địa chỉ lắng nghe")
	flag.Parse()

	h := fixture.Handler()
	srv := &http.Server{
		Addr: *addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Printf("%s %s %s CL=%d TE=%v", r.RemoteAddr, r.Method, r.URL.Path, r.ContentLength, r.TransferEncoding)
			h.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("upstream (net/http fixture) lắng nghe %s", *addr)
	log.Fatal(srv.ListenAndServe())
}
