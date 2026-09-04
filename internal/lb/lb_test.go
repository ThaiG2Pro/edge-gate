package lb

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func newTest(t *testing.T, algo string, addrs ...string) *Balancer {
	t.Helper()
	bl, err := New(addrs, Config{Algo: algo, Tau: 100 * time.Millisecond,
		Health: HealthConfig{Disabled: true}, Outlier: OutlierConfig{Disabled: true}, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	return bl
}

func addrs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("10.0.0.%d:80", i+1)
	}
	return out
}

func counts(bl *Balancer, n int, key func(i int) string) map[string]int {
	m := map[string]int{}
	for i := 0; i < n; i++ {
		b := bl.Pick(key(i))
		if b == nil {
			m["<nil>"]++
			continue
		}
		m[b.Addr]++
		bl.Done(b, time.Millisecond, false)
	}
	return m
}

func TestRoundRobinEvenAndSkipsUnhealthy(t *testing.T) {
	bl := newTest(t, "rr", addrs(4)...)
	m := counts(bl, 400, func(int) string { return "" })
	for _, a := range addrs(4) {
		if m[a] != 100 {
			t.Fatalf("rr lệch: %v", m)
		}
	}
	bl.backends[2].healthy.Store(false)
	m = counts(bl, 300, func(int) string { return "" })
	if m[addrs(4)[2]] != 0 || m["<nil>"] != 0 {
		t.Fatalf("rr vẫn chọn node unhealthy: %v", m)
	}
	for i, a := range addrs(4) {
		if i != 2 && m[a] != 100 {
			t.Fatalf("rr sau khi bỏ 1 node phải chia 100/100/100: %v", m)
		}
	}
}

func TestLeastConnPicksMinInflight(t *testing.T) {
	bl := newTest(t, "leastconn", addrs(3)...)
	held := map[string]*Backend{}
	// Cầm 2 request không Done ⇒ hai node có inflight 1, node thứ ba phải được chọn.
	for i := 0; i < 2; i++ {
		b := bl.Pick("")
		held[b.Addr] = b
	}
	third := bl.Pick("")
	if _, dup := held[third.Addr]; dup {
		t.Fatalf("least-conn chọn node đang có inflight 1: %s", third.Addr)
	}
	// Cả ba inflight 1: Done một con ⇒ con đó phải được chọn kế tiếp.
	for _, b := range held {
		bl.Done(b, time.Millisecond, false)
		if got := bl.Pick(""); got != b {
			t.Fatalf("least-conn sau Done(%s) chọn %s", b.Addr, got.Addr)
		}
		break
	}
}

func TestP2CColdStartSpreads(t *testing.T) {
	bl := newTest(t, "p2c", addrs(4)...)
	m := counts(bl, 400, func(int) string { return "" })
	for _, a := range addrs(4) {
		if m[a] < 40 {
			t.Fatalf("p2c cold-start dồn: %v", m)
		}
	}
}

// G6 / D11 — node A bị một mẫu 500 ms rồi hồi phục thật (mọi mẫu sau 1 ms).
// EWMA decay theo THỜI GIAN ⇒ điểm của A tự hạ khi rỗi ⇒ được chọn lại trong
// ~tau·ln(500) ≈ 0.6 s với tau 100 ms. `-tags nodefenselb` (decay theo số
// request) ⇒ A không bao giờ có mẫu mới ⇒ 0/200 ⇒ test ĐỎ (make lblab-nodefense).
func TestP2CRecovers(t *testing.T) {
	bl := newTest(t, "p2c", addrs(3)...)
	now := time.Now()
	a := bl.backends[0]
	a.ewma.observe(500*time.Millisecond, now)
	bl.backends[1].ewma.observe(time.Millisecond, now)
	bl.backends[2].ewma.observe(time.Millisecond, now)
	gotA := 0
	for i := 0; i < 200; i++ {
		b := bl.Pick("")
		if b == a {
			gotA++
		}
		bl.Done(b, time.Millisecond, false)
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("A được chọn lại %d/200 lần trong 2 s (tau 100 ms)", gotA)
	if gotA < 15 {
		t.Fatalf("A không hồi phục: %d/200 — EWMA không decay theo thời gian", gotA)
	}
}

// G4 — thêm 1 node vào 4: modulo đổi chủ ~80 % key, ring 150 vnode ~20 %.
func TestConsistentHashRehash(t *testing.T) {
	const keys = 100_000
	four := newTest(t, "chash", addrs(4)...).picker.(*consistentHash)
	five := newTest(t, "chash", addrs(5)...).picker.(*consistentHash)
	movedRing, movedMod := 0, 0
	load4 := make([]int, 4)
	for i := 0; i < keys; i++ {
		k := fmt.Sprintf("session-%d", i)
		o4, o5 := four.owner(k), five.owner(k)
		load4[o4]++
		if o4 != o5 {
			movedRing++
		}
		if Hash32(k)%4 != Hash32(k)%5 {
			movedMod++
		}
		if four.owner(k) != o4 {
			t.Fatalf("owner không ổn định cho %q", k)
		}
	}
	ring, mod := float64(movedRing)/keys, float64(movedMod)/keys
	mn, mx := load4[0], load4[0]
	for _, v := range load4 {
		mn, mx = min(mn, v), max(mx, v)
	}
	t.Logf("4→5 node, %d key: ring 150 vnode đổi chủ %.1f %% (lý thuyết 20), hash%%N đổi %.1f %% (lý thuyết 80); tải 4 node %v (max/min %.2f)",
		keys, ring*100, mod*100, load4, float64(mx)/float64(mn))
	if ring < 0.15 || ring > 0.25 {
		t.Fatalf("ring đổi chủ %.3f, ngoài [0.15, 0.25]", ring)
	}
	if mod < 0.7 {
		t.Fatalf("modulo đổi chủ %.3f — kỳ vọng ≈ 0.8; nếu thấp thì hash hoặc phép so sai", mod)
	}
	if float64(mx)/float64(mn) > 1.25 {
		t.Fatalf("150 vnode mà tải lệch max/min %.2f", float64(mx)/float64(mn))
	}
	// 1 vnode: lệch tải phải TỆ hơn hẳn — đó là lý do vnode tồn tại.
	one := newConsistentHash(four.ringBackends(t, addrs(4)), 1)
	load1 := make([]int, 4)
	for i := 0; i < keys; i++ {
		load1[one.owner(fmt.Sprintf("session-%d", i))]++
	}
	mn1, mx1 := load1[0], load1[0]
	for _, v := range load1 {
		mn1, mx1 = min(mn1, v), max(mx1, v)
	}
	t.Logf("1 vnode: tải %v (max/min %.2f)", load1, float64(mx1)/float64(mn1))
	if float64(mx1)/float64(mn1) <= float64(mx)/float64(mn) {
		t.Fatalf("1 vnode (%.2f) không lệch hơn 150 vnode (%.2f)?", float64(mx1)/float64(mn1), float64(mx)/float64(mn))
	}
}

// ringBackends dựng lại slice Backend cùng addr/idx để test 1 vnode.
func (c *consistentHash) ringBackends(t *testing.T, addrs []string) []*Backend {
	t.Helper()
	out := make([]*Backend, len(addrs))
	for i, a := range addrs {
		out[i] = &Backend{Addr: a, idx: i}
		out[i].healthy.Store(true)
	}
	return out
}

func TestConsistentHashSkipsUnavailable(t *testing.T) {
	bl := newTest(t, "chash", addrs(4)...)
	ch := bl.picker.(*consistentHash)
	key := "user-42"
	owner := bl.backends[ch.owner(key)]
	if got := bl.Pick(key); got != owner {
		t.Fatalf("Pick(%q) = %s, owner %s", key, got.Addr, owner.Addr)
	}
	owner.healthy.Store(false)
	alt := bl.Pick(key)
	if alt == nil || alt == owner {
		t.Fatalf("owner unhealthy mà Pick trả %v", alt)
	}
	for i := 0; i < 50; i++ {
		if bl.Pick(key) != alt {
			t.Fatalf("key không ổn định khi owner unhealthy")
		}
	}
	// Key khác vẫn về chủ cũ của nó: bỏ một node không xáo trộn phần còn lại.
	other := "user-7"
	if o := bl.backends[ch.owner(other)]; o != owner && bl.Pick(other) != o {
		t.Fatalf("key không liên quan bị đổi chủ")
	}
}

// D7 — eject sau Consecutive lỗi, backoff mũ, trần MaxEjectPercent.
func TestOutlierEjectBackoffAndCap(t *testing.T) {
	bl, err := New(addrs(2), Config{Algo: "rr", Health: HealthConfig{Disabled: true},
		Outlier: OutlierConfig{Consecutive: 3, BaseEject: 50 * time.Millisecond, MaxEject: 200 * time.Millisecond, MaxEjectPercent: 50},
		Logf:    t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	a, b := bl.backends[0], bl.backends[1]
	fail := func(x *Backend, n int) {
		for i := 0; i < n; i++ {
			x.inflight.Add(1)
			bl.Done(x, time.Millisecond, true)
		}
	}
	fail(a, 2)
	if !a.available(time.Now()) {
		t.Fatal("2 lỗi đã eject (Consecutive=3)")
	}
	fail(a, 1)
	if a.available(time.Now()) {
		t.Fatal("3 lỗi liên tiếp chưa eject")
	}
	for i := 0; i < 10; i++ {
		if got := bl.Pick(""); got != b {
			t.Fatalf("A bị eject mà Pick trả %v", got)
		}
		bl.Done(b, time.Millisecond, false)
	}
	// B lỗi 3 ⇒ vượt trần 50 % của 2 node ⇒ KHÔNG eject.
	fail(b, 3)
	if !b.available(time.Now()) {
		t.Fatal("eject cả hai node: vượt trần MaxEjectPercent mà vẫn eject")
	}
	if bl.Stats().EjectRefused != 1 {
		t.Fatalf("EjectRefused = %d", bl.Stats().EjectRefused)
	}
	time.Sleep(60 * time.Millisecond)
	if !a.available(time.Now()) {
		t.Fatal("hết BaseEject mà A chưa về")
	}
	// Lần eject thứ hai: 100 ms (backoff mũ).
	fail(a, 3)
	left := time.Until(time.Unix(0, a.ejectedUntil.Load()))
	if left < 80*time.Millisecond || left > 100*time.Millisecond {
		t.Fatalf("eject lần 2 còn %s, kỳ vọng ≈ 100 ms", left)
	}
	// 4xx/2xx reset đếm: 2 lỗi + 1 ok + 2 lỗi ≠ 3 liên tiếp.
	time.Sleep(110 * time.Millisecond)
	fail(a, 2)
	a.inflight.Add(1)
	bl.Done(a, time.Millisecond, false)
	fail(a, 2)
	if !a.available(time.Now()) {
		t.Fatal("một response tốt phải reset đếm lỗi liên tiếp")
	}
	if st := bl.Stats(); st.Backends[0].Ejections != 2 || st.Backends[0].Fails != 10 {
		t.Fatalf("stats: %+v", st.Backends[0])
	}
}

// D6 — fall/rise trên recordProbe (không socket) rồi probe thật (socket).
func TestHealthFallRiseAndProbe(t *testing.T) {
	bl, err := New(addrs(1), Config{Algo: "rr",
		Health: HealthConfig{Fall: 3, Rise: 2, Timeout: 300 * time.Millisecond, Path: "/h"}, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	a := bl.backends[0]
	bl.recordProbe(a, false)
	bl.recordProbe(a, false)
	if !a.healthy.Load() {
		t.Fatal("2 lỗi đã unhealthy (fall=3)")
	}
	bl.recordProbe(a, false)
	if a.healthy.Load() {
		t.Fatal("3 lỗi chưa unhealthy")
	}
	if bl.Pick("") != nil || bl.Stats().NoAvailable != 1 {
		t.Fatal("backend duy nhất unhealthy mà Pick vẫn trả")
	}
	bl.recordProbe(a, true)
	if a.healthy.Load() {
		t.Fatal("1 ok đã healthy (rise=2)")
	}
	bl.recordProbe(a, true)
	if !a.healthy.Load() {
		t.Fatal("2 ok chưa healthy")
	}

	// Probe thật. Không ai nghe ⇒ false.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	if bl.probe(&Backend{Addr: dead}) {
		t.Fatal("probe port đã đóng trả true")
	}
	serve := func(status int) string {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					br := bufio.NewReader(c)
					line, _ := br.ReadString('\n')
					for {
						l, err := br.ReadString('\n')
						if err != nil || l == "\r\n" {
							break
						}
					}
					if len(line) < 6 || line[:6] != "GET /h" {
						status = 404
					}
					fmt.Fprintf(c, "HTTP/1.1 %d X\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", status)
				}()
			}
		}()
		return ln.Addr().String()
	}
	if !bl.probe(&Backend{Addr: serve(200)}) {
		t.Fatal("probe 200 trả false")
	}
	if bl.probe(&Backend{Addr: serve(503)}) {
		t.Fatal("probe 503 trả true — node 'sống nhưng 5xx' phải unhealthy")
	}
	// Start/Close: goroutine health chạy và dừng sạch; node chết bị đánh dấu.
	bl2, _ := New([]string{dead}, Config{Algo: "rr",
		Health: HealthConfig{Interval: 20 * time.Millisecond, Timeout: 100 * time.Millisecond, Fall: 2}, Logf: t.Logf})
	bl2.Start()
	deadline := time.Now().Add(2 * time.Second)
	for bl2.backends[0].healthy.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	bl2.Close()
	if bl2.backends[0].healthy.Load() {
		t.Fatal("active health không đánh dấu node chết trong 2 s")
	}
	_ = io.Discard
}

func TestNewRejectsBadConfig(t *testing.T) {
	if _, err := New(nil, Config{}); err == nil {
		t.Fatal("không upstream mà New không lỗi")
	}
	if _, err := New(addrs(1), Config{Algo: "magic"}); err == nil {
		t.Fatal("algo lạ mà New không lỗi")
	}
}
