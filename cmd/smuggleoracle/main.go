// smuggleoracle (P4-6): oracle thứ hai + thứ ba cho bộ testdata/smuggle. Phát
// lại từng ca request-kind qua TCP trần vào nginx và h2o (docker), ghi status
// (hoặc "đóng"/"timeout"), so ba cột với cột "mình" (Expect của ca). Mục đích
// KHÔNG phải bắt nginx/h2o từ chối giống hệt — mà xem: ở ca nào mình strict hơn
// (an toàn), và có ca nào một backend lenient NHẬN theo cách tạo được ranh giới
// smuggling (nguy hiểm — ghi rõ để biết proxy ta đứng trước nó thì chặn gì).
//
// Không phải test đi vào `go test`: cần docker, chạy tay/CI qua `make oracle-ext`.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ThaiG2Pro/edge-gate/internal/smugglecase"
)

func main() {
	dir := flag.String("dir", "testdata/smuggle", "thư mục ca")
	nginx := flag.String("nginx", "127.0.0.1:18081", "origin nginx")
	h2o := flag.String("h2o", "127.0.0.1:18082", "origin h2o")
	flag.Parse()

	cases, err := smugglecase.Load(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "oracle:", err)
		os.Exit(1)
	}
	targets := []struct{ name, addr string }{{"nginx", *nginx}, {"h2o", *h2o}}
	// Mỗi origin phải trả lời được một GET hợp lệ — nếu không, cấu hình sai và
	// mọi cột "đóng" là giả.
	for _, t := range targets {
		if s := replay(t.addr, []byte("GET / HTTP/1.1\r\nHost: h\r\n\r\n")); !strings.HasPrefix(s, "2") && !strings.HasPrefix(s, "3") && !strings.HasPrefix(s, "4") {
			fmt.Fprintf(os.Stderr, "oracle: %s không trả lời GET hợp lệ (%q) — bỏ qua\n", t.name, s)
		}
	}

	fmt.Printf("%-34s %-10s %-10s %-10s\n", "ca", "mình", "nginx", "h2o")
	fmt.Println(strings.Repeat("-", 68))
	var dangerous []string
	n := 0
	for _, c := range cases {
		if c.Kind == "response" {
			continue
		}
		n++
		mine := c.Expect
		row := []string{}
		for _, t := range targets {
			row = append(row, replay(t.addr, c.Wire))
		}
		fmt.Printf("%-34s %-10s %-10s %-10s\n", c.File, mine, row[0], row[1])
		// Nguy hiểm: ca mình TỪ CHỐI mà một origin NHẬN KHUNG. "Nhận khung" =
		// origin phân tích xong head và định tuyến (2xx/3xx/404/405/100…), KHÁC
		// "từ chối khung" (400/501/414/431 hoặc đóng/timeout). h2o trả 404 cho
		// mọi head hợp lệ (không có file) ⇒ 404 là NHẬN, không phải từ chối.
		if c.Reject() {
			for i, t := range targets {
				if accepted(row[i]) {
					dangerous = append(dangerous, fmt.Sprintf("%s: %s nhận khung (%s) — mình %s", c.File, t.name, row[i], mine))
				}
			}
		}
	}
	fmt.Printf("\n%d ca request\n", n)
	sort.Strings(dangerous)
	if len(dangerous) == 0 {
		fmt.Println("không ca nào origin nhận mà mình từ chối theo hướng nguy hiểm")
		return
	}
	fmt.Printf("%d cặp (ca × origin) origin lenient hơn — proxy ta đứng trước nó phải chặn:\n", len(dangerous))
	for _, d := range dangerous {
		fmt.Println("  " + d)
	}
}

// accepted: status cho thấy origin ĐÃ phân tích head và định tuyến (nhận khung),
// phân biệt với từ chối khung (400/501/… hoặc đóng). Dùng để tìm hướng nguy
// hiểm: mình từ chối mà origin nhận.
func accepted(status string) bool {
	switch status {
	case "400", "408", "413", "414", "431", "501", "505", "đóng", "timeout", "lỗi", "dial-lỗi", "ghi-lỗi", "reset":
		return false
	}
	// Số 1xx/2xx/3xx/404/405… = nhận; chuỗi lạ = coi như không kết luận (bỏ).
	return len(status) == 3 && status[0] >= '1' && status[0] <= '4'
}

// replay mở TCP, ghi wire, đọc status line của response đầu. Đóng không byte ⇒
// "đóng"; quá hạn ⇒ "timeout".
func replay(addr string, wire []byte) string {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return "dial-lỗi"
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write(wire); err != nil {
		return "ghi-lỗi"
	}
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		if err == io.EOF && line == "" {
			return "đóng"
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return "timeout"
		}
		return "lỗi"
	}
	f := strings.Fields(line)
	if len(f) >= 2 {
		return f[1]
	}
	return strings.TrimSpace(line)
}
