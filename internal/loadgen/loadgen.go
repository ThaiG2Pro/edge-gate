// Package loadgen: generator OPEN-LOOP cho phase 7 (D10). Request i có giờ
// hẹn t0 + i/Rate; latency = giờ đọc xong response − giờ HẸN, không phải giờ
// gửi. Khi mọi worker đều bận, request vẫn chờ trong hàng và thời gian chờ đó
// được tính — đây là chỗ khác closed-loop (phase 0 bài 3): proxy chậm không
// làm generator tự gửi ít đi, nên quá tải lộ ra thành latency và lỗi.
//
// Chỉ net + httpx; mỗi worker một connection keep-alive raw, nối lười.
package loadgen

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/thaivro/edgegate/internal/httpx"
)

// Config của một lượt bắn.
type Config struct {
	Addr     string
	Rate     float64       // request/s
	Duration time.Duration // tổng thời gian phát lịch
	Workers  int           // connection tối đa; đủ lớn để không thành closed-loop (mặc định 256)
	Timeout  time.Duration // deadline mỗi request phía client (mặc định 15 s) — quá ⇒ Kind "timeout" (treo)
	// Request trả raw request cho request thứ i (mặc định GET /hello).
	Request func(i int) string
	// LocalIP: bind nguồn (vd 127.0.0.2) — loopback nhận mọi 127/8; rỗng = mặc định.
	// LocalIPFor ghi đè theo request (ratelab: nhiều IP nguồn).
	LocalIP    string
	LocalIPFor func(i int) string
	// RetryZeroByte: request trên connection DÙNG LẠI mà nhận 0 byte (EOF/RST)
	// ⇒ gửi lại đúng một lần trên connection mới. Phía client của D4 phase 5;
	// drainlab đo có/không (G8 b).
	RetryZeroByte bool
}

// Sample: một request.
type Sample struct {
	I       int
	Sched   time.Time
	Done    time.Time
	Status  int    // 0 nếu không có response
	Kind    string // "ok" (có status) | "dial" | "io-nohead" (đóng trước khi có head) | "io-body" (đứt giữa body) | "timeout"
	Close   bool   // response mang Connection: close
	Retried bool
	Reused  bool // lần gửi cuối đi trên connection dùng lại (false = vừa dial)
}

func (s Sample) Latency() time.Duration { return s.Done.Sub(s.Sched) }

type job struct {
	i     int
	sched time.Time
}

// Run bắn theo lịch rồi chờ mọi request xong (hoặc timeout).
func Run(cfg Config) []Sample {
	if cfg.Workers == 0 {
		cfg.Workers = 256
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.Request == nil {
		cfg.Request = func(int) string { return "GET /hello HTTP/1.1\r\nHost: loadgen\r\n\r\n" }
	}
	n := int(cfg.Rate * cfg.Duration.Seconds())
	jobs := make(chan job, n)
	out := make([]Sample, n)
	var wg sync.WaitGroup
	for w := 0; w < cfg.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wk := &worker{cfg: cfg}
			defer wk.close()
			for j := range jobs {
				out[j.i] = wk.do(j)
			}
		}()
	}
	t0 := time.Now()
	interval := time.Duration(float64(time.Second) / cfg.Rate)
	for i := 0; i < n; i++ {
		at := t0.Add(time.Duration(i) * interval)
		if d := time.Until(at); d > 0 {
			time.Sleep(d)
		}
		jobs <- job{i: i, sched: at}
	}
	close(jobs)
	wg.Wait()
	return out
}

type worker struct {
	cfg    Config
	c      net.Conn
	br     *bufio.Reader
	local  string
	reused bool
}

func (w *worker) close() {
	if w.c != nil {
		w.c.Close()
		w.c = nil
	}
}

func (w *worker) dial(local string) error {
	d := net.Dialer{Timeout: 5 * time.Second}
	if local != "" {
		d.LocalAddr = &net.TCPAddr{IP: net.ParseIP(local)}
	}
	c, err := d.Dial("tcp", w.cfg.Addr)
	if err != nil {
		return err
	}
	w.c, w.br, w.local, w.reused = c, bufio.NewReader(c), local, false
	return nil
}

func (w *worker) do(j job) Sample {
	s := Sample{I: j.i, Sched: j.sched}
	local := w.cfg.LocalIP
	if w.cfg.LocalIPFor != nil {
		local = w.cfg.LocalIPFor(j.i)
	}
	if w.c != nil && w.local != local {
		w.close()
	}
	deadline := time.Now().Add(w.cfg.Timeout)
	for attempt := 0; ; attempt++ {
		if w.c == nil {
			if err := w.dial(local); err != nil {
				s.Kind, s.Done = "dial", time.Now()
				return s
			}
		}
		reused := w.reused
		s.Reused = reused
		w.c.SetDeadline(deadline)
		status, closeResp, got, err := roundTrip(w.c, w.br, w.cfg.Request(j.i))
		s.Done = time.Now()
		if err == nil {
			s.Status, s.Kind, s.Close = status, "ok", closeResp
			w.reused = true
			if closeResp {
				w.close()
			}
			return s
		}
		w.close()
		if errors.Is(err, os.ErrDeadlineExceeded) {
			s.Kind = "timeout"
			return s
		}
		if w.cfg.RetryZeroByte && reused && got == 0 && attempt == 0 {
			s.Retried = true
			continue
		}
		s.Kind = "io-nohead"
		if got > 0 {
			s.Kind = "io-body" // đã có head: lỗi rõ ràng (body cụt), không phải "không trả lời"
		}
		return s
	}
}

func roundTrip(c net.Conn, br *bufio.Reader, req string) (status int, closeResp bool, got int, err error) {
	if _, err = io.WriteString(c, req); err != nil {
		return 0, false, 0, err
	}
	if _, err = br.Peek(1); err != nil {
		return 0, false, 0, err // 0 byte response: chưa thấy gì từ server
	}
	resp, err := httpx.ReadResponse(br, httpx.DefaultLimits(), "GET")
	if err != nil {
		return 0, false, 1, err
	}
	if _, err = io.Copy(io.Discard, resp.Body); err != nil {
		return 0, false, 1, err
	}
	return resp.Status, resp.Close, 1, nil
}

// Summary: đếm theo status/kind + percentile latency (từ giờ hẹn).
type Summary struct {
	N        int
	ByStatus map[int]int
	ByKind   map[string]int
	Retried  int
	// Percentile của MỌI request có response, và riêng của 200.
	All, OK Percentiles
}

type Percentiles struct {
	N                        int
	P50, P90, P99, P999, Max time.Duration
}

func Summarize(ss []Sample) Summary {
	sum := Summary{N: len(ss), ByStatus: map[int]int{}, ByKind: map[string]int{}}
	var all, ok []time.Duration
	for _, s := range ss {
		sum.ByKind[s.Kind]++
		if s.Retried {
			sum.Retried++
		}
		if s.Kind != "ok" {
			continue
		}
		sum.ByStatus[s.Status]++
		all = append(all, s.Latency())
		if s.Status == 200 {
			ok = append(ok, s.Latency())
		}
	}
	sum.All, sum.OK = pcts(all), pcts(ok)
	return sum
}

func pcts(ds []time.Duration) Percentiles {
	if len(ds) == 0 {
		return Percentiles{}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	at := func(p float64) time.Duration { return ds[int(float64(len(ds)-1)*p)] }
	return Percentiles{N: len(ds), P50: at(.5), P90: at(.9), P99: at(.99), P999: at(.999), Max: ds[len(ds)-1]}
}

// Print in một dòng tóm tắt: số request, tỉ lệ từng status/kind, percentile
// (từ giờ hẹn) của request có response và riêng của 200, goodput.
func Print(w io.Writer, name string, sum Summary, dur time.Duration) {
	keys := make([]int, 0, len(sum.ByStatus))
	for k := range sum.ByStatus {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	pct := func(n int) float64 { return float64(n) / float64(max(sum.N, 1)) * 100 }
	fmt.Fprintf(w, "%-14s n=%d", name, sum.N)
	for _, k := range keys {
		fmt.Fprintf(w, " %d:%.1f%%", k, pct(sum.ByStatus[k]))
	}
	for _, k := range []string{"dial", "io-nohead", "io-body", "timeout"} {
		if v := sum.ByKind[k]; v > 0 {
			fmt.Fprintf(w, " %s:%d", k, v)
		}
	}
	if sum.Retried > 0 {
		fmt.Fprintf(w, " retried:%d", sum.Retried)
	}
	r := func(d time.Duration) string { return d.Round(10 * time.Microsecond).String() }
	fmt.Fprintf(w, "\n%-14s 200: p50 %s p90 %s p99 %s p99.9 %s max %s · goodput %.0f/s\n", "",
		r(sum.OK.P50), r(sum.OK.P90), r(sum.OK.P99), r(sum.OK.P999), r(sum.OK.Max), float64(sum.ByStatus[200])/dur.Seconds())
}
