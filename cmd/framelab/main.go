// framelab: cho THẤY hai hiện tượng mà package frame tồn tại để xử lý, trên
// TCP thật (loopback), và in con số đo được thay vì chỉ "ok".
//
// Hai con số quan trọng ở phía server:
//   - "Read thô đầu tiên": server gọi conn.Read MỘT lần với buffer 4 KiB trước
//     khi giao cho decoder. dribble → 1 byte; coalesce → cả 62 byte (3 frame).
//     Đây là hiện tượng nhìn bằng mắt: TCP đưa cho bạn bao nhiêu byte là tuỳ nó.
//   - Read/frame của decoder: dribble ≫ 1 (mỗi frame cần nhiều Read). Nhưng
//     coalesce KHÔNG cho < 1 — decoder dùng ReadFull với buffer đúng cỡ, nên
//     số lần Read do decoder xin quyết định, không do gói tin. Coalescing trở
//     nên vô hình với decoder đúng; đó chính là điều muốn đạt. Lần chạy đầu
//     của lab này đã dự đoán "< 1" và đo được 1.67 — comment này sửa sau đó.
//
// Nếu Read thô đầu tiên trả 62 byte ở cả hai mode thì bộ đo đang bị bufio hay
// net.Pipe che (phase 0, G1), không phải vì TCP tử tế.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/thaivro/edgegate/internal/frame"
)

// countingReader đếm số lần Read và tổng byte — chỉ để đo, không đổi hành vi.
type countingReader struct {
	r     io.Reader
	reads atomic.Int64
	bytes atomic.Int64
	min   atomic.Int64 // Read nhỏ nhất, để thấy "1 byte" thật sự xuất hiện
	max   atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.reads.Add(1)
		c.bytes.Add(int64(n))
		if m := c.min.Load(); m == 0 || int64(n) < m {
			c.min.Store(int64(n))
		}
		if int64(n) > c.max.Load() {
			c.max.Store(int64(n))
		}
	}
	return n, err
}

var frames = []frame.Frame{
	{Type: frame.TypeData, Payload: []byte("alpha")},
	{Type: frame.TypePing},
	{Type: frame.TypeData, Payload: []byte("gamma-with-a-longer-payload")},
}

func main() {
	mode := flag.String("mode", "dribble", "dribble | coalesce | oversize")
	interval := flag.Duration("interval", 10*time.Millisecond, "dribble: khoảng cách giữa hai byte")
	rawbuf := flag.Int("rawbuf", 4096, "cỡ buffer của Read thô đầu tiên; đặt 7 hoặc 12 để cắt frame 1 giữa header/payload (P1-5)")
	flag.Parse()
	log.SetFlags(0)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()

	// Một struct, một channel. Bản đầu dùng thêm channel firstRead riêng và
	// quên gửi vào nó ở các đường lỗi -> main treo ở <-firstRead nếu Accept
	// hay Read đầu thất bại. Mọi đường ra của goroutine giờ đi qua đúng một chỗ.
	type result struct {
		first int // byte của Read thô đầu tiên; -1 nếu chưa tới đó
		got   []frame.Frame
		err   error
		cr    *countingReader
	}
	done := make(chan result, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			done <- result{first: -1, err: err}
			return
		}
		defer c.Close()
		c.SetReadDeadline(time.Now().Add(30 * time.Second))
		// Một Read thô trước decoder, để THẤY TCP giao bao nhiêu byte một lần.
		// Phần đọc được phải nối lại trước conn, không thì mất byte.
		first := make([]byte, *rawbuf)
		n, err := c.Read(first)
		if err != nil {
			done <- result{first: -1, err: err}
			return
		}
		cr := &countingReader{r: io.MultiReader(bytes.NewReader(first[:n]), c)}
		d := frame.NewDecoder(cr, 1<<16) // trần 64 KiB, đủ cho lab và đủ nhỏ để oversize dễ chạm
		var got []frame.Frame
		for {
			f, err := d.Decode()
			if err != nil {
				if err == io.EOF {
					err = nil
				}
				done <- result{n, got, err, cr}
				return
			}
			got = append(got, f)
		}
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		log.Fatal(err)
	}
	tcp := conn.(*net.TCPConn)
	// Không tắt Nagle là dribble bị gom lại thành vài gói lớn và không còn là
	// dribble nữa — phase 0 G7 đo được chính cơ chế gom này.
	tcp.SetNoDelay(true)

	var wire []byte
	for _, f := range frames {
		wire = frame.Append(wire, f)
	}
	fmt.Printf("mode=%s  gửi %d frame = %d byte  (header %d byte/frame)\n", *mode, len(frames), len(wire), frame.HeaderSize)

	start := time.Now()
	var writes int
	switch *mode {
	case "dribble":
		for i := range wire {
			if _, err := conn.Write(wire[i : i+1]); err != nil {
				log.Fatal(err)
			}
			writes++
			time.Sleep(*interval)
		}
	case "coalesce":
		if _, err := conn.Write(wire); err != nil {
			log.Fatal(err)
		}
		writes = 1
	case "oversize":
		// Header hợp lệ về magic/version nhưng length = 0xFFFFFFFF, không kèm
		// payload. Server phải từ chối bằng ErrTooBig, KHÔNG được cấp phát.
		bad := frame.Append(nil, frame.Frame{Type: frame.TypeData})
		bad[6], bad[7], bad[8], bad[9] = 0xFF, 0xFF, 0xFF, 0xFF
		if _, err := conn.Write(bad); err != nil {
			log.Fatal(err)
		}
		writes = 1
	default:
		fmt.Fprintf(os.Stderr, "mode lạ: %s\n", *mode)
		os.Exit(2)
	}
	conn.Close()
	r := <-done
	elapsed := time.Since(start)

	fmt.Printf("client Write : %d lần\n", writes)
	if r.first < 0 {
		log.Fatalf("server lỗi trước khi nhận byte nào: %v", r.err)
	}
	fmt.Printf("Read thô đầu : %d byte / buffer %d  (TCP giao bao nhiêu là tuỳ nó — đây là lý do phải ReadFull)\n", r.first, *rawbuf)
	if r.cr != nil {
		reads := r.cr.reads.Load()
		fmt.Printf("server Read  : %d lần, %d byte  (nhỏ nhất %d, lớn nhất %d byte/Read)\n",
			reads, r.cr.bytes.Load(), r.cr.min.Load(), r.cr.max.Load())
		if len(r.got) > 0 {
			fmt.Printf("Read/frame   : %.2f\n", float64(reads)/float64(len(r.got)))
		}
	}
	fmt.Printf("server nhận  : %d frame trong %v\n", len(r.got), elapsed.Round(time.Millisecond))
	for i, f := range r.got {
		fmt.Printf("  [%d] type=%d len=%d %q\n", i, f.Type, len(f.Payload), f.Payload)
	}

	switch *mode {
	case "oversize":
		if errors.Is(r.err, frame.ErrTooBig) {
			fmt.Printf("server từ chối: %v  ✔ (không cấp phát)\n", r.err)
			return
		}
		fmt.Printf("SAI: mong ErrTooBig, nhận err=%v\n", r.err)
		os.Exit(1)
	default:
		if r.err != nil {
			fmt.Printf("SAI: server lỗi %v\n", r.err)
			os.Exit(1)
		}
		if len(r.got) != len(frames) {
			fmt.Printf("SAI: nhận %d frame, gửi %d\n", len(r.got), len(frames))
			os.Exit(1)
		}
		fmt.Println("✔ tách đúng ranh giới")
	}
}
