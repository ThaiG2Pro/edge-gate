// epolllab: server của thí nghiệm phase 9 G6/G7 (và upstream cố định cho G8).
// Cùng giao thức tối giản (internal/epollsrv), hai mô hình I/O:
//
//	-impl epoll      vòng epoll tự viết, -loops luồng OS, SO_REUSEPORT
//	-impl netpoller  goroutine-per-connection, buffer 4 KiB mỗi conn
//
// Chạy như tiến trình riêng để perflab đo RSS/CPU của RIÊNG nó.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/ThaiG2Pro/edge-gate/internal/epollsrv"
)

func main() {
	impl := flag.String("impl", "epoll", "epoll | netpoller")
	addr := flag.String("addr", "127.0.0.1:18100", "địa chỉ lắng nghe")
	loops := flag.Int("loops", runtime.GOMAXPROCS(0), "số loop epoll (mỗi loop một luồng OS)")
	body := flag.Int("body", 1024, "byte body response")
	flag.Parse()

	resp := epollsrv.Response(*body)
	var closeFn func()
	var reqs func() int64
	switch *impl {
	case "epoll":
		s, err := epollsrv.ServeEpoll(*addr, *loops, resp)
		if err != nil {
			log.Fatal(err)
		}
		closeFn, reqs = s.Close, s.Requests.Load
		fmt.Printf("epolllab: epoll %d loop trên %s, GOMAXPROCS %d\n", *loops, s.Addr(), runtime.GOMAXPROCS(0))
	case "netpoller":
		s, err := epollsrv.ServeNetpoller(*addr, resp)
		if err != nil {
			log.Fatal(err)
		}
		closeFn, reqs = s.Close, s.Requests.Load
		fmt.Printf("epolllab: netpoller trên %s, GOMAXPROCS %d\n", s.Addr(), runtime.GOMAXPROCS(0))
	default:
		log.Fatalf("impl lạ %q", *impl)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	closeFn()
	fmt.Printf("epolllab: %d request\n", reqs())
}
