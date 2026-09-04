//go:build nodefenselb

package lb

// nodefenselb: EWMA chỉ đổi khi có mẫu ⇒ node điểm xấu không bao giờ hồi phục.
const ewmaDecayByTime = false
