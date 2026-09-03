// Package fixture là backend giả cho phase 3+. ĐÂY là chỗ DUY NHẤT ngoài file
// _test.go được dùng net/http trong repo: fixture là UPSTREAM, không phải data
// path của proxy. cmd/edgegate và internal/proxy không import package này.
package fixture

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Handler trả bộ route dùng cho `make proxylab` và test của internal/proxy.
//
//	/hello          GET  → body nhỏ, net/http tự đặt Content-Length
//	/echo           POST → trả nguyên body, Content-Length tường minh
//	/chunked        GET  → 5 chunk cách nhau 20 ms, KHÔNG có CL ⇒ Transfer-Encoding: chunked
//	/eof            GET  → Hijack: response KHÔNG CL, KHÔNG TE, body tới EOF rồi đóng (RFC 9112 §6.3 bước 8)
//	/nobody         GET  → 204, không body
//	/headers        GET  → liệt kê header nhận được, mỗi dòng "Name: value" (kiểm hop-by-hop, XFF)
//	/large?n=N      GET  → N byte xác định (kiểm đúng byte)
//	/slow?ms=N      GET  → ngủ N ms rồi 200 (kiểm 504)
//	/status?code=N  GET  → status N, body "status N"
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "hello from upstream\n")
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.Write(b)
	})
	// /chunked?n=5&ms=20 — n chunk, ngủ ms giữa mỗi chunk. ms=0 dùng cho G4
	// (Nagle): chunk phải dồn sát nhau thì delayed-ACK 40 ms mới lộ.
	mux.HandleFunc("/chunked", func(w http.ResponseWriter, r *http.Request) {
		n, ms := 5, 20
		if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 {
			n = v
		}
		if v, err := strconv.Atoi(r.URL.Query().Get("ms")); err == nil && v >= 0 {
			ms = v
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fl, _ := w.(http.Flusher)
		for i := 0; i < n; i++ {
			fmt.Fprintf(w, "chunk %d\n", i)
			if fl != nil {
				fl.Flush()
			}
			if ms > 0 {
				time.Sleep(time.Duration(ms) * time.Millisecond)
			}
		}
	})
	mux.HandleFunc("/eof", func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", 500)
			return
		}
		c, _, err := hj.Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\nbody until eof\n")
	})
	mux.HandleFunc("/nobody", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/headers", func(w http.ResponseWriter, r *http.Request) {
		names := make([]string, 0, len(r.Header))
		for k := range r.Header {
			names = append(names, k)
		}
		sort.Strings(names)
		var sb strings.Builder
		fmt.Fprintf(&sb, "Host: %s\n", r.Host)
		for _, k := range names {
			for _, v := range r.Header[k] {
				fmt.Fprintf(&sb, "%s: %s\n", k, v)
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, sb.String())
	})
	mux.HandleFunc("/large", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n <= 0 {
			n = 1 << 20
		}
		w.Header().Set("Content-Length", strconv.Itoa(n))
		w.Write(Pattern(n))
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		time.Sleep(time.Duration(ms) * time.Millisecond)
		io.WriteString(w, "slow done\n")
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		code, _ := strconv.Atoi(r.URL.Query().Get("code"))
		if code < 100 || code > 599 {
			code = 200
		}
		w.WriteHeader(code)
		fmt.Fprintf(w, "status %d\n", code)
	})
	return mux
}

// Pattern sinh n byte xác định: byte i = 'a' + i%26. Hai đầu so được từng byte.
func Pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a' + byte(i%26)
	}
	return b
}
