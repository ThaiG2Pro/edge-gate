//go:build !nodefenselb

package lb

// ewmaDecayByTime: EWMA decay theo thời gian (D3). `-tags nodefenselb` lật về
// decay theo số request để TestP2CRecovers ĐỎ — phòng tuyến chỉ tin khi đã
// thấy nó đỏ lúc tắt (D11).
const ewmaDecayByTime = true
