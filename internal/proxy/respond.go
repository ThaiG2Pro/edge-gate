package proxy

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/thaivro/edgegate/internal/httpx"
)

var reasons = map[int]string{
	400: "Bad Request",
	408: "Request Timeout",
	413: "Content Too Large",
	431: "Request Header Fields Too Large",
	500: "Internal Server Error",
	501: "Not Implemented",
	502: "Bad Gateway",
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
	body := fmt.Sprintf("%d %s: %s\n", status, reasonPhrase(status), detail)
	h := httpx.Header{}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if !keep {
		h.Set("Connection", "close")
	}
	resp := &httpx.Response{Proto: "HTTP/1.1", Status: status, Reason: reasonPhrase(status), Header: h}
	c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := resp.WriteHead(bw); err != nil {
		return
	}
	bw.WriteString(body)
	bw.Flush()
}
