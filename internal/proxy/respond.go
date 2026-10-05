package proxy

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/ThaiG2Pro/edge-gate/internal/httpx"
)

var reasons = map[int]string{
	400: "Bad Request",
	408: "Request Timeout",
	413: "Content Too Large",
	429: "Too Many Requests",
	421: "Misdirected Request",
	431: "Request Header Fields Too Large",
	500: "Internal Server Error",
	501: "Not Implemented",
	502: "Bad Gateway",
	503: "Service Unavailable",
	504: "Gateway Timeout",
	505: "HTTP Version Not Supported",
}

func reasonPhrase(status int) string {
	if r, ok := reasons[status]; ok {
		return r
	}
	return "Error"
}

// writeError trả một response lỗi do CHÍNH PROXY sinh ra (không phải từ
// upstream). keep=false ⇒ thêm "Connection: close"; caller chịu trách nhiệm
// đóng. detail đi vào body, không vào header — header có thể bị injection,
// body có Content-Length thì không.
func (s *Server) writeError(c net.Conn, bw *bufio.Writer, status int, detail string, keep bool) {
	s.writeErrorH(c, bw, status, detail, keep)
}

// writeErrorH: writeError kèm header thêm (cặp name, value) — 429/503 mang
// Retry-After (phase 7). Đang drain ⇒ luôn Connection: close (D8).
func (s *Server) writeErrorH(c net.Conn, bw *bufio.Writer, status int, detail string, keep bool, kv ...string) {
	if s.draining.Load() {
		keep = false
	}
	body := fmt.Sprintf("%d %s: %s\n", status, reasonPhrase(status), detail)
	h := httpx.Header{}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if !keep {
		h.Set("Connection", "close")
	}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	resp := &httpx.Response{Proto: "HTTP/1.1", Status: status, Reason: reasonPhrase(status), Header: h}
	c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := resp.WriteHead(bw); err != nil {
		return
	}
	bw.WriteString(body)
	bw.Flush()
}
