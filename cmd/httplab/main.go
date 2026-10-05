// httplab: cho THẤY ba việc mà internal/httpx tồn tại để làm, trên TCP thật
// (loopback), in con số thay vì chỉ "ok".
//
//	-mode parse    : client gửi CẢ FILE request trong MỘT Write (pipelined);
//	                 server parse vòng keep-alive, in framing từng request và
//	                 số byte còn trong buffer sau request cuối (phải là 0).
//	-mode slowloris: client nhỏ giọt 1 byte/-interval; server đặt HeaderTimeout
//	                 (-header-timeout) SAU byte đầu. Parser không biết gì về
//	                 thời gian — tầng connection (ở đây) mới là I3.
//	-mode response : server đóng vai upstream gửi 5 response trên một connection,
//	                 gồm hai bẫy: 204 kèm Content-Length và response không CL
//	                 không TE (tới EOF). Client parse bằng ReadResponse.
//
// Dự đoán (G5, G6, G7) ghi ở diary/phase2.md TRƯỚC khi chạy.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/ThaiG2Pro/edge-gate/internal/httpx"
)

func main() {
	mode := flag.String("mode", "parse", "parse | slowloris | response")
	file := flag.String("file", "testdata/requests/basic.txt", "file request thô (CRLF) cho -mode parse")
	interval := flag.Duration("interval", 50*time.Millisecond, "slowloris: khoảng cách giữa hai byte")
	headerTimeout := flag.Duration("header-timeout", 300*time.Millisecond, "slowloris: HeaderTimeout của tầng connection")
	flag.Parse()

	var ok bool
	switch *mode {
	case "parse":
		ok = runParse(*file)
	case "slowloris":
		ok = runSlowloris(*interval, *headerTimeout)
	case "response":
		ok = runResponse()
	default:
		fmt.Fprintln(os.Stderr, "mode không hợp lệ")
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}

func pair() (client, server net.Conn) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer ln.Close()
	ch := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); ch <- c }()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		panic(err)
	}
	server = <-ch
	for _, c := range []net.Conn{client, server} {
		c.(*net.TCPConn).SetNoDelay(true)
	}
	return
}

func framingString(cl int64, chunked bool) string {
	switch {
	case chunked:
		return "chunked"
	case cl < 0:
		return "tới-EOF"
	default:
		return fmt.Sprintf("CL:%d", cl)
	}
}

// ---- parse -----------------------------------------------------------------

func runParse(file string) bool {
	wire, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return false
	}
	if !strings.Contains(string(wire), "\r\n") {
		fmt.Fprintln(os.Stderr, "file không có CRLF — parser strict (D1) sẽ từ chối; đây là cố ý")
	}
	client, server := pair()
	defer client.Close()
	defer server.Close()

	// Client: MỘT Write cho cả file (pipelined). Sau đó half-close để server
	// thấy EOF sạch sau request cuối.
	go func() {
		client.Write(wire)
		client.(*net.TCPConn).CloseWrite()
	}()

	lim := httpx.DefaultLimits()
	br := bufio.NewReader(server)
	n, okAll := 0, true
	for {
		server.SetReadDeadline(time.Now().Add(lim.HeaderTimeout))
		req, err := httpx.ReadRequest(br, lim)
		if err == io.EOF {
			break
		}
		if err != nil {
			pe, isProto := httpx.IsProtoError(err)
			if isProto {
				fmt.Printf("#%d  từ chối: %d %s\n", n+1, pe.Status, pe.Reason)
			} else {
				fmt.Printf("#%d  lỗi I/O: %v\n", n+1, err)
			}
			okAll = false
			break
		}
		n++
		server.SetReadDeadline(time.Now().Add(lim.BodyTimeout))
		body, berr := io.ReadAll(req.Body)
		fmt.Printf("#%d  %-4s %-8s %s  head=%3d B  framing=%-8s body=%d B  close=%v",
			n, req.Method, req.Target, req.Proto, req.HeaderBytes, framingString(req.ContentLength, req.Chunked), len(body), req.Close)
		if tr := req.Trailer(); tr != nil {
			fmt.Printf("  trailer=%d", tr.Count())
		}
		if berr != nil {
			fmt.Printf("  BODY LỖI: %v", berr)
			okAll = false
		}
		fmt.Println()
		if req.Close {
			break
		}
	}
	left := br.Buffered()
	fmt.Printf("\n%d request từ %d byte, sau request cuối buffer còn %d byte\n", n, len(wire), left)
	if left != 0 {
		fmt.Println("SAI: còn byte thừa — ranh giới body sai ở đâu đó")
		return false
	}
	if okAll {
		fmt.Println("✔ ranh giới đúng, không byte nào lẫn giữa các request")
	}
	return okAll
}

// ---- slowloris ---------------------------------------------------------------

func runSlowloris(interval, headerTimeout time.Duration) bool {
	client, server := pair()
	defer client.Close()
	defer server.Close()
	head := "GET / HTTP/1.1\r\nHost: victim.example\r\nUser-Agent: slowloris\r\nAccept: */*\r\n\r\n"
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for i := 0; i < len(head); i++ {
			select {
			case <-stop:
				return
			case <-time.After(interval):
			}
			if _, err := client.Write([]byte{head[i]}); err != nil {
				return
			}
		}
	}()

	lim := httpx.DefaultLimits()
	lim.HeaderTimeout = headerTimeout
	cr := &counting{r: server}
	br := bufio.NewReader(cr)
	server.SetReadDeadline(time.Now().Add(lim.IdleTimeout))
	if _, err := br.Peek(1); err != nil {
		fmt.Println("không nhận được byte đầu:", err)
		return false
	}
	t0 := time.Now()
	server.SetReadDeadline(t0.Add(lim.HeaderTimeout))
	_, err := httpx.ReadRequest(br, lim)
	el := time.Since(t0)
	fmt.Printf("client nhỏ giọt 1 byte/%v, head %d byte ⇒ cần %v để gửi hết\n", interval, len(head), interval*time.Duration(len(head)))
	fmt.Printf("server HeaderTimeout=%v sau byte đầu: ReadRequest trả sau %v, nhận %d byte\n", headerTimeout, el.Round(time.Millisecond), cr.n)
	fmt.Printf("err = %v\n", err)
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		fmt.Println("SAI: không phải timeout — tầng connection không cắt được slowloris")
		return false
	}
	if el > 2*headerTimeout {
		fmt.Println("SAI: cắt quá trễ")
		return false
	}
	// 408 Request Timeout là câu trả lời đúng của tầng connection; parser
	// không có status này vì nó không biết thời gian.
	resp := &httpx.Response{Proto: "HTTP/1.1", Status: 408, Reason: "Request Timeout", Header: httpx.Header{}}
	resp.Header.Set("Content-Length", "0")
	resp.Header.Set("Connection", "close")
	server.SetWriteDeadline(time.Now().Add(time.Second))
	resp.WriteHead(server)
	fmt.Printf("✔ cắt ở %v (dự đoán ≈ %v), trả 408 và đóng\n", el.Round(time.Millisecond), headerTimeout)
	return true
}

type counting struct {
	r io.Reader
	n int
}

func (c *counting) Read(p []byte) (int, error) { k, err := c.r.Read(p); c.n += k; return k, err }

// ---- response ----------------------------------------------------------------

func runResponse() bool {
	client, server := pair()
	defer client.Close()
	defer server.Close()

	type step struct {
		method string
		wire   string
		body   string // body client PHẢI thấy
	}
	steps := []step{
		{"GET", "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello", "hello"},
		{"GET", "HTTP/1.1 204 No Content\r\nContent-Length: 5\r\n\r\n", ""}, // bẫy: CL bị bỏ qua, KHÔNG có 5 byte theo sau
		{"HEAD", "HTTP/1.1 200 OK\r\nContent-Length: 999\r\n\r\n", ""},
		{"GET", "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nwiki\r\n5\r\npedia\r\n0\r\nX-Sum: 9\r\n\r\n", "wikipedia"},
		{"GET", "HTTP/1.1 200 OK\r\nX-Note: no-length\r\n\r\nbody until close", "body until close"},
	}
	go func() {
		// Upstream: ghi cả 5 response trong MỘT Write rồi đóng — client phải
		// cắt đúng 5 ranh giới từ một cục byte, và response cuối cần EOF.
		var all strings.Builder
		for _, s := range steps {
			all.WriteString(s.wire)
		}
		server.Write([]byte(all.String()))
		server.(*net.TCPConn).CloseWrite()
	}()

	lim := httpx.DefaultLimits()
	br := bufio.NewReader(client)
	ok := true
	for i, s := range steps {
		client.SetReadDeadline(time.Now().Add(lim.HeaderTimeout))
		resp, err := httpx.ReadResponse(br, lim, s.method)
		if err != nil {
			fmt.Printf("#%d  %s ⇒ lỗi %v\n", i+1, s.method, err)
			return false
		}
		body, berr := io.ReadAll(resp.Body)
		fmt.Printf("#%d  %-4s ⇒ %d  framing=%-8s body=%d B  close=%v  buffered-sau=%d",
			i+1, s.method, resp.Status, framingString(resp.ContentLength, resp.Chunked), len(body), resp.Close, br.Buffered())
		if berr != nil {
			fmt.Printf("  BODY LỖI: %v", berr)
			ok = false
		}
		if string(body) != s.body {
			fmt.Printf("  SAI: muốn body %q", s.body)
			ok = false
		}
		fmt.Println()
		if resp.Close {
			if i != len(steps)-1 {
				fmt.Println("SAI: Close=true trước response cuối")
				ok = false
			}
			break
		}
	}
	if ok {
		fmt.Println("✔ 5 response, 5 ranh giới đúng; response cuối đọc tới EOF và Close=true")
	}
	return ok
}
