#!/usr/bin/env bash
# Chạy TRỌN phase 0 và ghi mỗi thí nghiệm ra một file trong bench/.
#
# Dùng lại được ở máy khác không sửa gì: tên file có hostname và tag, nên hai máy
# không ghi đè nhau và có thể so cạnh nhau. Đây là cách "chừa đường sang máy khác":
# không phải chép số vào diary rồi mất dấu, mà là mỗi lần chạy để lại một file
# tự mang theo khối môi trường của nó.
#
#   ./scripts/phase0-run.sh              # loopback trần (RTT ~0)
#   ./scripts/phase0-run.sh rtt20ms      # sau khi `make rtt-up`
#   ./scripts/phase0-run.sh lan -role client -addr 192.168.1.50:9000
set -u
tag="${1:-rtt0}"; shift || true
host=$(hostname)
mkdir -p bench
out="bench/p0-${host}-${tag}"

ulimit -n 65536 2>/dev/null || echo "⚠️  không nâng được ulimit -n"

# Generator và thứ bị đo tranh CPU là cái bẫy thứ ba. Ở -role both không tách
# được (cùng process), nên ít nhất phải chừa core cho hệ điều hành.
ncpu=$(nproc)
pin=""
if command -v taskset >/dev/null && [ "$ncpu" -ge 4 ]; then
  pin="taskset -c 0-$((ncpu-2))"
  echo "ghim core: $pin (chừa core $((ncpu-1)) cho OS)"
fi

run() { # run <tên> <args...>
  local name=$1; shift
  echo "==> $name"
  # shellcheck disable=SC2086
  $pin go run ./cmd/netlab -tag "$tag" "$@" > "${out}-${name}.txt" 2>&1
  echo "    -> ${out}-${name}.txt"
}

run syscall  -exp syscall  -n 5000 "$@"
run rtt      -exp rtt      -n 2000 "$@"
run nagle    -exp nagle    -n 150  "$@"
run omission -exp omission -rate 1200 -duration 10s -svc 1ms -workers 1 "$@"
run mem-1k   -exp mem      -conns 1000 -bufio=true  "$@"
run mem-10k  -exp mem      -conns 10000 -bufio=true "$@"
run mem-nobufio -exp mem   -conns 10000 -bufio=false "$@"
run limits   -exp limits   -duration 20s "$@"

echo
echo "xong. $(ls -1 ${out}-*.txt | wc -l) file trong bench/"
echo "Bước tiếp: dán output vào diary/phase0.md và CHẤM từng giả thuyết G1..G7."
echo "Đừng viết nhận định trước khi chấm — chấm xong mới được viết mục 'Rút ra'."
