package httpx

import (
	"strings"
	"testing"
)

func TestHeaderCanonical(t *testing.T) {
	h := Header{}
	h.Add("content-TYPE", "a")
	h.Add("Content-Type", "b")
	h.Add("TE", "trailers")
	if got := h.Values("CONTENT-type"); len(got) != 2 || got[0] != "a" {
		t.Fatalf("%v", got)
	}
	if !h.Has("te") || h.Get("Te") != "trailers" {
		t.Fatalf("TE canonical là Te: %v", h)
	}
	if h.Count() != 3 {
		t.Fatalf("Count %d", h.Count())
	}
	h.Set("Content-Type", "c")
	if got := h.Values("Content-Type"); len(got) != 1 || got[0] != "c" {
		t.Fatalf("Set phải ghi đè: %v", got)
	}
	c := h.Clone()
	c.Add("Content-Type", "d")
	if h.Count() != 2 {
		t.Fatal("Clone không sâu")
	}
}

func TestStripHopByHop(t *testing.T) {
	h := Header{}
	for _, kv := range [][2]string{
		{"Connection", "X-Custom, keep-alive"}, {"Keep-Alive", "timeout=5"}, {"TE", "trailers"},
		{"Trailer", "X"}, {"Transfer-Encoding", "chunked"}, {"Upgrade", "h2c"},
		{"Proxy-Connection", "keep-alive"}, {"Proxy-Authorization", "Basic x"}, {"Proxy-Authenticate", "Basic"},
		{"X-Custom", "smuggled"}, // liệt kê trong Connection ⇒ cũng phải mất
		{"Host", "h"}, {"X-Keep", "yes"},
	} {
		h.Add(kv[0], kv[1])
	}
	h.StripHopByHop()
	if h.Count() != 2 || h.Get("Host") != "h" || h.Get("X-Keep") != "yes" {
		t.Fatalf("còn lại: %v", h)
	}
}

func TestHeaderWriteSortedAndSafe(t *testing.T) {
	h := Header{}
	h.Add("Zeta", "1")
	h.Add("Alpha", "2")
	h.Add("Alpha", "3")
	var sb strings.Builder
	if err := h.Write(&sb); err != nil {
		t.Fatal(err)
	}
	if sb.String() != "Alpha: 2\r\nAlpha: 3\r\nZeta: 1\r\n" {
		t.Fatalf("%q", sb.String())
	}
	for _, bad := range []string{"a\r\nX: y", "a\nb", "a\x00"} {
		h := Header{"X": {bad}}
		if err := h.Write(&sb); err != ErrHeaderInjection {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
