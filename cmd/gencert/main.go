// gencert — sinh CA lab + cert cho từng tên (phase 8 D9). Không openssl, không
// key nào trong git: `make tlslab` gọi lệnh này ghi vào bin/certs/.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ThaiG2Pro/edge-gate/internal/tlsx"
)

func main() {
	out := flag.String("out", "bin/certs", "thư mục ghi")
	names := flag.String("names", "a.test,b.test", "mỗi tên một cert, phẩy")
	kt := flag.String("key", "ecdsa", "ecdsa | rsa")
	serial := flag.Int64("serial", 1, "serial của cert đầu (tăng dần theo tên); đổi để thử SIGHUP reload")
	flag.Parse()
	ca, err := tlsx.NewCA()
	if err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*out+"/ca.crt", ca.PEM, 0o644); err != nil {
		fatal(err)
	}
	for i, n := range strings.Split(*names, ",") {
		c, k, err := ca.Leaf([]string{n}, tlsx.KeyType(*kt), *serial+int64(i))
		if err != nil {
			fatal(err)
		}
		cf, kf, err := tlsx.WriteFiles(*out, n, c, k)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("%s %s (serial %d)\n", cf, kf, *serial+int64(i))
	}
	fmt.Printf("%s/ca.crt\n", *out)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gencert:", err)
	os.Exit(1)
}
