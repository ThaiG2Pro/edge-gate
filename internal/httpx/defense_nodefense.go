//go:build nodefense

package httpx

// Bài phản chứng phase 4: tắt cả bảy phòng tuyến. CHỈ để `make smugglelab-nodefense`
// chứng minh bộ test có răng. KHÔNG BAO GIỜ build binary thật với tag này.
const (
	rejectCLWithTE         = false
	rejectMultiCL          = false
	strictLineEnding       = false
	strictTE               = false
	strictCLSyntax         = false
	strictHeaderName       = false
	rejectForbiddenTrailer = false
)
