// netlab đo 5 sự thật vật lý của mạng trên chính cái máy đang chạy nó.
// Phase 0 của EdgeGate. Chưa có proxy nào ở đây.
//
// Ba chế độ, và chế độ thứ hai/ba là đường sang máy khác:
//
//	netlab -all                          # client+server cùng process (loopback)
//	netlab -role server -addr :9000      # máy A
//	netlab -role client -addr A:9000 -all # máy B  <- RTT thật, không cần tc netem
//
// Mọi tham số thí nghiệm được gửi TRONG request, nên server không cần biết
// mình đang phục vụ thí nghiệm nào.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

var (
	nowFunc   = time.Now
	sinceFunc = time.Since
)

func main() {
	var (
		role     = flag.String("role", "both", "both | server | client")
		addr     = flag.String("addr", "", "server: địa chỉ listen (mặc định 127.0.0.1:0). client: địa chỉ server")
		exp      = flag.String("exp", "", "syscall | rtt | omission | mem | limits | nagle")
		all      = flag.Bool("all", false, "chạy tất cả thí nghiệm")
		n        = flag.Int("n", 3000, "số request mỗi biến thể (syscall/rtt/nagle)")
		respSize = flag.Int("resp", 1024, "kích thước response (byte)")
		conns    = flag.Int("conns", 1000, "số connection rỗi cho -exp mem")
		useBufio = flag.Bool("bufio", true, "server cấp bufio.Reader+Writer khi accept (biến của G5)")
		rate     = flag.Float64("rate", 1200, "open-loop rate cho -exp omission")
		duration = flag.Duration("duration", 10*time.Second, "thời lượng omission/limits")
		svc      = flag.Duration("svc", time.Millisecond, "thời gian phục vụ nhân tạo mỗi request (omission)")
		workers  = flag.Int("workers", 1, "số request server xử lý đồng thời = capacity")
		dialers  = flag.Int("dialers", 1, "P0-4: số goroutine dial song song cho -exp limits (so N=1 vs N=16 để tách trần port/client)")
		tag      = flag.String("tag", "", "nhãn ghi vào output, ví dụ 'rtt20ms' hoặc 'wsl2'")
	)
	flag.Parse()

	printEnv(*tag, *role)

	switch *role {
	case "server":
		a := *addr
		if a == "" {
			a = ":9000"
		}
		s, err := newServer(a, *workers, *useBufio)
		if err != nil {
			fmt.Fprintf(os.Stderr, "listen: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("server sẵn sàng trên %s (workers=%d bufio=%v)\n", s.addr(), *workers, *useBufio)
		fmt.Println("chạy client ở máy khác:  netlab -role client -addr <ip>:9000 -all")
		select {}

	case "client":
		if *addr == "" {
			fmt.Fprintln(os.Stderr, "-role client cần -addr")
			os.Exit(2)
		}
		runExperiments(*addr, *exp, *all, opts{*n, *respSize, *conns, *useBufio, *rate, *duration, *svc, *workers, *dialers})

	case "both":
		a := *addr
		if a == "" {
			a = "127.0.0.1:0"
		}
		s, err := newServer(a, *workers, *useBufio)
		if err != nil {
			fmt.Fprintf(os.Stderr, "listen: %v\n", err)
			os.Exit(1)
		}
		defer s.close()
		fmt.Printf("server nội bộ: %s (workers=%d bufio=%v)\n", s.addr(), *workers, *useBufio)
		warn("chế độ -role both: client và server CÙNG process, cùng core, và loopback KHÔNG có RTT.")
		warn("Mọi số ở đây chỉ dùng để so TỈ SỐ với nhau. Xem G2/G3 để biết vì sao.")
		runExperiments(s.addr(), *exp, *all, opts{*n, *respSize, *conns, *useBufio, *rate, *duration, *svc, *workers, *dialers})

	default:
		fmt.Fprintf(os.Stderr, "-role không hợp lệ: %s\n", *role)
		os.Exit(2)
	}
}

type opts struct {
	n        int
	respSize int
	conns    int
	useBufio bool
	rate     float64
	duration time.Duration
	svc      time.Duration
	workers  int
	dialers  int
}

func runExperiments(addr, exp string, all bool, o opts) {
	want := func(name string) bool { return all || exp == name }

	if want("syscall") {
		expSyscall(addr, o.n, o.respSize)
	}
	if want("rtt") {
		expRTT(addr, o.n, o.respSize)
	}
	if want("nagle") {
		expNagle(addr, o.n/3)
	}
	if want("omission") {
		capacity := float64(o.workers) / o.svc.Seconds()
		expOmission(addr, o.rate, o.duration, o.svc, capacity)
	}
	if want("mem") {
		if all {
			// G5 đo RSS, và RSS chỉ có nghĩa trong process SẠCH: các thí nghiệm
			// trước đã phình rồi trả bộ nhớ về OS, làm mốc nền sai (đã ra
			// -7.99 KB/conn một lần vì đúng lý do này). Nên `-all` tự fork.
			forkMem(o)
		} else {
			expMem(addr, o.conns, o.useBufio)
		}
	}
	if want("limits") {
		expLimits(addr, o.duration, o.dialers)
	}
	if !all && exp == "" {
		fmt.Println("\nkhông chọn thí nghiệm nào. Dùng -all hoặc -exp <tên>.")
	}
}

// forkMem chạy lại chính binary này với `-exp mem` trong một process mới, để
// phép đo RSS có mốc nền sạch. Không phải chi tiết cài đặt: nó là điều kiện để
// con số RSS/conn có nghĩa.
func forkMem(o opts) {
	self, err := os.Executable()
	if err != nil {
		warn("không fork được để đo G5: %v. Chạy tay: netlab -exp mem", err)
		return
	}
	for _, b := range []string{"true", "false"} {
		fmt.Printf("\n[fork process sạch để đo G5, bufio=%s]\n", b)
		cmd := exec.Command(self, "-exp", "mem",
			"-conns", fmt.Sprint(o.conns), "-bufio="+b, "-workers", fmt.Sprint(o.workers))
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			warn("fork G5 (bufio=%s) lỗi: %v", b, err)
		}
	}
}

// printEnv in khối môi trường vào ĐẦU mọi lần chạy.
//
// Đây không phải trang trí. Số đo mạng không có nghĩa nếu thiếu: nhân, số core,
// ulimit -n, port range, và mức nền TIME_WAIT. Khi chuyển sang máy khác, chính
// khối này là thứ cho phép so hai lần chạy — không có nó thì hai file output chỉ
// là hai đống số không so được với nhau.
func printEnv(tag, role string) {
	fmt.Println("=== netlab · môi trường ===")
	if tag != "" {
		fmt.Printf("tag              : %s\n", tag)
	}
	fmt.Printf("thời điểm        : %s\n", time.Now().Format(time.RFC3339))
	fmt.Printf("role             : %s\n", role)
	fmt.Printf("go               : %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Printf("NumCPU / GOMAXPROCS: %d / %d\n", runtime.NumCPU(), runtime.GOMAXPROCS(0))
	fmt.Printf("kernel           : %s\n", firstLine("uname", "-srmo"))
	fmt.Printf("ulimit -n        : %d\n", fdLimit())
	fmt.Printf("port range       : %s\n", portRange())
	fmt.Printf("somaxconn        : %s\n", sysctlFile("net/core/somaxconn"))
	fmt.Printf("tcp_max_syn_backlog: %s\n", sysctlFile("net/ipv4/tcp_max_syn_backlog"))
	fmt.Printf("sockstat         : %s\n", sockstat())
	fmt.Printf("qdisc lo         : %s\n", firstLine("tc", "qdisc", "show", "dev", "lo"))
	fmt.Printf("cpu              : %s\n", cpuModel())
	if isWSL() {
		warn("WSL2: loopback không phải NIC thật và tc netem có thể hành xử khác Linux thuần.")
		warn("Số tuyệt đối ở đây KHÔNG mang sang máy khác được. Chỉ tỉ số mang được.")
	}
	fmt.Println("===========================")
}

func firstLine(cmd string, args ...string) string {
	out, err := exec.Command(cmd, args...).Output()
	if err != nil {
		return "n/a (" + err.Error() + ")"
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}

func sysctlFile(p string) string {
	b, err := os.ReadFile("/proc/sys/" + p)
	if err != nil {
		return "n/a"
	}
	return strings.TrimSpace(string(b))
}

func cpuModel() string {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "n/a"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "model name") {
			if i := strings.Index(line, ":"); i >= 0 {
				return strings.TrimSpace(line[i+1:])
			}
		}
	}
	return "n/a"
}

func isWSL() bool {
	b, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(b)), "microsoft")
}
