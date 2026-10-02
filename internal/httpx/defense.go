//go:build !nodefense && !nodefense4

package httpx

// Bảy phòng tuyến của phase 4 gom thành hằng để `-tags nodefense` tắt được
// cả bảy trong một file (defense_nodefense.go) — lúc đó `make smugglelab-nodefense`
// (`-tags nodefense4`; P4-4: không kéo theo bẫy phase 3) PHẢI đỏ. Một phòng tuyến chưa bao giờ thấy đỏ thì chưa phải bằng chứng.
//
// Phiên bản "tắt" không phải là "không kiểm" chung chung: mỗi hằng false mô
// phỏng đúng cách một proxy khoan dung điển hình cư xử (ưu tiên CL khi có cả
// hai, nhận mọi TE có chữ "chunked", trim tên header, nhận bare LF…) — vì lỗ
// hổng smuggling nằm ở chỗ khoan dung *khác backend*, không ở chỗ không kiểm.
const (
	// D1: CL + TE cùng có ⇒ từ chối. false ⇒ ưu tiên CL (kiểu front-end CL.TE).
	rejectCLWithTE = true
	// D2: nhiều dòng Content-Length ⇒ từ chối, kể cả giống nhau. false ⇒ lấy dòng đầu.
	rejectMultiCL = true
	// D3: dòng phải kết thúc CRLF. false ⇒ chấp nhận LF trần.
	strictLineEnding = true
	// Transfer-Encoding phải là đúng một token "chunked". false ⇒ nhận mọi value
	// chứa "chunked" (TE.TE obfuscation lọt).
	strictTE = true
	// Content-Length phải là chữ số thuần. false ⇒ TrimSpace + ParseInt (nhận "+5", " 5").
	strictCLSyntax = true
	// Tên header phải là token tới sát ':'. false ⇒ trim OWS trước ':'
	// ("Transfer-Encoding : chunked" thành TE thật).
	strictHeaderName = true
	// D8: trailer mang framing/routing field ⇒ từ chối. false ⇒ nhận và bỏ qua.
	rejectForbiddenTrailer = true
)
