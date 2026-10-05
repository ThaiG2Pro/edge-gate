package proxy

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ThaiG2Pro/edge-gate/internal/httpx"
)

// P3-2 — D13: client xin Upgrade (GET, không body) ⇒ Connection: Upgrade +
// Upgrade: X đi tiếp sang upstream; upstream 101 ⇒ 101 về client rồi tunnel
// hai chiều tới khi một bên đóng. Connection upstream không về pool.
func TestUpgradeTunnel(t *testing.T) {
	s, addr := startProxyS(t, startFixture(t), nil)
	rc := dialRaw(t, addr)
	rc.c.SetDeadline(time.Now().Add(3 * time.Second))
	// Byte đầu của tunnel gửi NGAY sau head (nằm trong br của proxy trước 101).
	io.WriteString(rc.c, "GET /ws HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\nping-0\n")
	resp, err := httpx.ReadResponse(rc.br, httpx.DefaultLimits(), "GET")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 101 || !strings.EqualFold(resp.Header.Get("Upgrade"), "echo") || !strings.EqualFold(resp.Header.Get("Connection"), "Upgrade") {
		t.Fatalf("muốn 101 + Upgrade: echo, được %d %v", resp.Status, resp.Header)
	}
	for i := 0; i < 3; i++ {
		if i > 0 {
			io.WriteString(rc.c, "ping-"+string(rune('0'+i))+"\n")
		}
		line, err := rc.br.ReadString('\n')
		if err != nil || line != "ping-"+string(rune('0'+i))+"\n" {
			t.Fatalf("echo %d: %q %v", i, line, err)
		}
	}
	rc.c.Close()
	// Upstream đóng theo ⇒ tunnel kết thúc, pool không nhận lại connection.
	deadline := time.Now().Add(2 * time.Second)
	for {
		ps := s.PoolStats()
		if ps.Idle == 0 && ps.DropDirty >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connection tunnel phải bị discard, không về pool: %+v", ps)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// P3-2 — upstream trả 101 khi client KHÔNG xin Upgrade ⇒ 502 như D7 phase 3.
// Request có body + Upgrade ⇒ Upgrade bị tước (RFC 9110 §7.8: không đổi giao
// thức khi body chưa nhận) ⇒ upstream /ws trả 426, không tunnel.
func TestUpgradeNotRequested(t *testing.T) {
	_, addr := startProxyS(t, startFixture(t), nil)
	req, _ := http.NewRequest("POST", "http://"+addr+"/ws", strings.NewReader("body"))
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "echo")
	resp, err := oracleClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 426 {
		t.Fatalf("POST + Upgrade: muốn 426 từ upstream (Upgrade bị tước), được %d", resp.StatusCode)
	}
	// Upstream tự ý 101 (client không xin): raw upstream.
	up := rawUpstream(t, []byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n"))
	_, addr2 := startProxyS(t, up, nil)
	rc := dialRaw(t, addr2)
	r2, _ := rc.do(t, "GET", "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n")
	if r2.Status != 502 {
		t.Fatalf("101 tự ý: muốn 502, được %d", r2.Status)
	}
}
