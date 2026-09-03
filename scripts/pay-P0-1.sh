#!/usr/bin/env bash
# Trả nợ P0-1: đo G3 = Dial mới vs reuse connection khi CÓ RTT.
#
# Cần sudo (tc netem). Chạy: ./scripts/pay-P0-1.sh
#
# Script này cũng trả luôn câu hỏi treo của nợ P-env-1: netem trên `lo` áp delay
# cho MẤY chiều? Trên loopback, cả gói đi và gói hồi đều đi qua qdisc của `lo`,
# nên `delay Xms` phải cho RTT ~2X. Nếu ping xác nhận điều đó thì nhãn "RTT 20ms"
# tương ứng với `delay 10ms`, KHÔNG phải `delay 20ms` — và mọi diary từ phase 5
# trở đi phải ghi đúng con số đã ĐO, không phải con số đã ĐẶT.
set -u

DEV=lo
OUT_DIR=bench
mkdir -p "$OUT_DIR"
host=$(hostname)

# Tháo qdisc trong MỌI đường ra, kể cả Ctrl-C hay lỗi giữa chừng. Quên tháo là
# làm sai mọi benchmark sau đó trên máy này, và sẽ không ai nhớ vì sao.
cleanup() {
  echo
  echo "--- dọn dẹp: tháo qdisc khỏi $DEV ---"
  sudo tc qdisc del dev "$DEV" root 2>/dev/null
  tc qdisc show dev "$DEV"
}
trap cleanup EXIT INT TERM

echo "⚠️  netem trên lo áp cho TOÀN BỘ traffic loopback của máy này (DB local, X11, ...)."
echo "    Script chạy ~2 phút rồi tự tháo."
echo
sudo -v || { echo "cần sudo"; exit 1; }

# ping_rtt in ra RTT trung bình (ms) đo được, không phải RTT mình tưởng.
ping_rtt() {
  ping -c 5 -i 0.2 127.0.0.1 2>/dev/null | tail -1 | awk -F'/' '{print $5}'
}

echo "=== mốc nền, chưa có netem ==="
tc qdisc show dev "$DEV"
base_rtt=$(ping_rtt)
echo "RTT nền: ${base_rtt} ms"

run_case() { # run_case <delay-đặt> <n>
  local delay=$1 n=$2
  echo
  echo "=================================================================="
  echo "=== netem delay ${delay}  (n=${n} request mỗi biến thể) ==="
  echo "=================================================================="
  sudo tc qdisc del dev "$DEV" root 2>/dev/null
  sudo tc qdisc add dev "$DEV" root netem delay "$delay" || { echo "tc add thất bại"; return 1; }
  tc qdisc show dev "$DEV"

  local rtt
  rtt=$(ping_rtt)
  echo
  echo ">>> ĐẶT delay=${delay} mỗi chiều  =>  RTT ĐO ĐƯỢC = ${rtt} ms"
  echo ">>> (nếu RTT ≈ 2 x delay thì netem áp cho CẢ HAI chiều trên lo — trả lời P-env-1)"
  echo

  local f="${OUT_DIR}/p0-rtt-netem${delay}-${host}.txt"
  ulimit -n 65536
  go run ./cmd/netlab -exp rtt -n "$n" -bufio=false \
     -tag "netem-delay-${delay}-RTT-DO-DUOC-${rtt}ms" 2>&1 | tee "$f"
  echo "-> $f"
}

# delay 10ms  => kỳ vọng RTT ~20ms. Đây là cấu hình khớp nhãn "RTT 20ms" của G3.
run_case 10ms 300

# delay 20ms  => kỳ vọng RTT ~40ms. Chạy để CHỨNG MINH quan hệ nhân đôi, không
# phải để tin nó.
run_case 20ms 150

echo
echo "=================================================================="
echo "Xong. Việc còn lại KHÔNG phải copy số, mà là chấm giả thuyết:"
echo
echo "  G3 đăng ký trước:  dial/reuse ở RTT 20ms  > 15x"
echo "  Dự đoán đã ghi ở P0-1 (mục nợ):"
echo "    - tỉ số sẽ TỤT xuống ~2x (không phải >15x)"
echo "    - tiết kiệm TUYỆT ĐỐI tăng từ 0.86ms lên ~20.9ms"
echo "  => nếu đúng thì kết luận là: với connection pool, TỈ SỐ LÀ ĐƠN VỊ SAI,"
echo "     đơn vị đúng là RTT/request. Phase 5 phải biết trước khi báo cáo."
echo
echo "Dán output vào diary/phase0.md, chấm G3, rồi cập nhật bảng tỉ số ROADMAP.md."
echo "=================================================================="
