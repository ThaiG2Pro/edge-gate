//go:build nodefense

package frame

// Bài phản chứng: tắt phòng tuyến. Chỉ tồn tại để `make framelab-nodefense`
// chứng minh bộ test có răng. KHÔNG BAO GIỜ build binary thật với tag này.
func checkLength(n, max uint32) error { return nil }
