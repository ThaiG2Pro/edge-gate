//go:build !nodefense9

package proxy

// Tối ưu phase 9 (D4). `-tags nodefense9` = bản TRƯỚC, để so trước/sau trên
// cùng commit.
const (
	// poolBuffers (D1): copyBuf, bufio lấy/trả qua sync.Pool. false ⇒ mỗi
	// response một make 32 KiB, mỗi connection bufio mới.
	poolBuffers = true
	// releaseIdleBufio (D2): connection rỗi trả bufio về pool, chờ byte đầu
	// bằng Read 1 byte. false ⇒ giữ 2×8 KiB suốt đời connection.
	releaseIdleBufio = true
	// spliceBodyOn (D5): Config.SpliceBody có tác dụng. false ⇒ luôn copy qua
	// userspace.
	spliceBodyOn = true
)
