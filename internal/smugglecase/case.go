// Package smugglecase đọc bộ payload testdata/smuggle/*.txt — deliverable của
// phase 4. Dùng chung cho test parser (internal/httpx) và test e2e
// (internal/proxy) để hai tầng chấm CÙNG một bộ ca. Không import net/http.
//
// Định dạng một file:
//
//	# name: CL.TE cơ bản
//	# expect: 400
//	# source: PortSwigger, RFC 9112 §6.1
//	# why: CL nói 6, TE nói 0 — hai ranh giới
//	---
//	POST / HTTP/1.1\r\n
//	Host: h\r\n
//	...
//
// Mỗi dòng payload là một chuỗi Go đã escape (viết "\r\n" bằng tay); loader
// unescape rồi NỐI, không thêm gì. Nhờ vậy bare LF, CR lạc, NUL, obs-fold đều
// viết được và diff đọc được.
//
// Trường:
//
//	kind:   request (mặc định) | response
//	method: method của request tương ứng khi kind=response (mặc định GET)
//	expect: <status>       — head bị từ chối với *ProtoError status đó
//	        body-<status>  — head nhận, ĐỌC BODY lỗi với status đó
//	        ok             — nhận; kèm body: / rest: / target: / host: để so
//	body, rest: chuỗi escape, so với body đọc được và phần dư sau body
//	target, host: (ok) Target sau chuẩn hoá và Host sau chuẩn hoá
package smugglecase

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Case struct {
	File   string
	Name   string
	Kind   string // "request" | "response"
	Method string
	Expect string // "400" | "body-400" | "ok" | ...
	Source string
	Why    string
	Body   string
	Rest   string
	Target string
	Host   string
	Wire   []byte
}

// Status trả status mong đợi của một ca từ chối (head hoặc body), 0 nếu ok.
func (c Case) Status() int {
	s := strings.TrimPrefix(c.Expect, "body-")
	n, _ := strconv.Atoi(s)
	return n
}

// BodyReject: head nhận nhưng body phải lỗi.
func (c Case) BodyReject() bool { return strings.HasPrefix(c.Expect, "body-") }

// Reject: bị từ chối ở head hoặc body.
func (c Case) Reject() bool { return c.Expect != "ok" }

// Load đọc mọi *.txt trong dir, sắp theo tên file.
func Load(dir string) ([]Case, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.txt"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("smugglecase: không có *.txt trong %s", dir)
	}
	var out []Case
	for _, f := range files {
		c, err := parseFile(f)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func parseFile(path string) (Case, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Case{}, err
	}
	c := Case{File: filepath.Base(path), Kind: "request", Method: "GET"}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	i := 0
	for ; i < len(lines); i++ {
		l := lines[i]
		if l == "---" {
			i++
			break
		}
		if !strings.HasPrefix(l, "#") {
			return Case{}, fmt.Errorf("%s:%d: trước '---' mọi dòng phải bắt đầu '#'", path, i+1)
		}
		l = strings.TrimSpace(strings.TrimPrefix(l, "#"))
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "name":
			c.Name = v
		case "kind":
			c.Kind = v
		case "method":
			c.Method = v
		case "expect":
			c.Expect = v
		case "source":
			c.Source = v
		case "why":
			c.Why = v
		case "body":
			if c.Body, err = unescape(v); err != nil {
				return Case{}, fmt.Errorf("%s: body: %v", path, err)
			}
		case "rest":
			if c.Rest, err = unescape(v); err != nil {
				return Case{}, fmt.Errorf("%s: rest: %v", path, err)
			}
		case "target":
			c.Target = v
		case "host":
			c.Host = v
		}
	}
	if c.Expect == "" {
		return Case{}, fmt.Errorf("%s: thiếu '# expect:'", path)
	}
	if c.Name == "" {
		c.Name = strings.TrimSuffix(c.File, ".txt")
	}
	var wire strings.Builder
	for ; i < len(lines); i++ {
		if lines[i] == "" && i == len(lines)-1 {
			break // newline cuối file
		}
		s, err := unescape(lines[i])
		if err != nil {
			return Case{}, fmt.Errorf("%s:%d: %v", path, i+1, err)
		}
		wire.WriteString(s)
	}
	c.Wire = []byte(wire.String())
	if len(c.Wire) == 0 {
		return Case{}, fmt.Errorf("%s: payload rỗng", path)
	}
	return c, nil
}

// unescape hiểu dòng như nội dung một chuỗi Go trong dấu nháy kép; dấu " viết
// thẳng được (loader tự escape).
func unescape(s string) (string, error) {
	s = strings.ReplaceAll(s, `"`, `\"`)
	return strconv.Unquote(`"` + s + `"`)
}
