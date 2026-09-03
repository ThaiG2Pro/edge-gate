#!/usr/bin/env bash
# Chụp lại môi trường của MỘT máy vào bench/env-<host>.txt
#
# Vì sao cần: số đo mạng không so được giữa hai máy nếu không biết nhân, số core,
# ulimit -n, port range và mức nền TIME_WAIT. Chạy script này TRƯỚC khi đo, ở mỗi
# máy mới. File nó sinh ra là thứ dán vào mục "Môi trường" của diary/phaseN.md.
set -u
out="bench/env-$(hostname).txt"
mkdir -p bench
{
  echo "# chụp lúc: $(date -Is)"
  echo "# host: $(hostname)"
  echo
  echo '$ uname -srmo'; uname -srmo
  echo; echo '$ go version'; go version
  echo; echo '$ nproc'; nproc
  echo; echo '$ ulimit -n'; ulimit -n
  echo; echo '$ grep "model name" /proc/cpuinfo | head -1'; grep 'model name' /proc/cpuinfo | head -1
  echo; echo '$ free -m | head -2'; free -m | head -2
  echo; echo '$ sysctl net.ipv4.ip_local_port_range net.core.somaxconn net.ipv4.tcp_max_syn_backlog net.ipv4.tcp_tw_reuse'
  sysctl net.ipv4.ip_local_port_range net.core.somaxconn net.ipv4.tcp_max_syn_backlog net.ipv4.tcp_tw_reuse 2>&1
  echo; echo '$ ss -s | head -3'; ss -s | head -3
  echo; echo '$ tc qdisc show dev lo'; tc qdisc show dev lo 2>&1
  echo; echo '$ grep -qi microsoft /proc/version && echo WSL2 || echo "Linux thuần"'
  grep -qi microsoft /proc/version && echo WSL2 || echo "Linux thuần"
} > "$out" 2>&1
echo "đã ghi $out"
echo
echo "--- kiểm tra ba cái bẫy trước khi tin bất kỳ số nào ---"
[ "$(ulimit -n)" -lt 65536 ] && echo "❌ ulimit -n = $(ulimit -n) < 65536. Chạy: ulimit -n 65536" || echo "✅ ulimit -n = $(ulimit -n)"
tc qdisc show dev lo | grep -q netem \
  && echo "⚠️  lo ĐANG có netem: mọi số là số CÓ RTT nhân tạo. Nhớ ghi vào diary." \
  || echo "✅ lo không có netem: RTT ~0. Kết luận nào phụ thuộc RTT thì CHƯA đo được."
echo "ℹ️  nproc = $(nproc). Generator và thứ bị đo phải ở core khác nhau (taskset)."
