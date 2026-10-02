package proxy

import (
	"io"
	"net/http"
	"testing"
)

// P10-6 — Config.H2COnly: listener plaintext chỉ nhận h2c prior knowledge.
// Byte đầu không phải preface ⇒ h2.Conn từ chối (đóng), KHÔNG rơi về h1 400
// (D7 chung port). h2spec 3.5/2 đòi đúng điều này. Client h2c vẫn chạy.
func TestH2COnlyRefusesH1(t *testing.T) {
	_, addr := startProxyS(t, startFixture(t), func(c *Config) { c.H2C, c.H2COnly = true, true })
	rc := dialRaw(t, addr)
	if _, err := io.WriteString(rc.c, "GET /hello HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := io.ReadFull(rc.br, buf)
	if err == nil || n > 0 {
		t.Fatalf("h2c_only: request h1 phải bị đóng không trả lời, nhận %d byte %q (err %v)", n, buf[:n], err)
	}
	resp, err := h2cClient().Get("http://" + addr + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.ProtoMajor != 2 || resp.StatusCode != http.StatusOK {
		t.Fatalf("h2c: %s %d", resp.Proto, resp.StatusCode)
	}
}
