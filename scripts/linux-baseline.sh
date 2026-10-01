#!/usr/bin/env bash
# Chạy lại mọi phép đo PHỤ THUỘC THỜI GIAN của phase 0–7 trên một máy Linux thuần.
# Trên WSL2 các số này nhiễu: dial p50 đổi 2.5x cách nhau 30 phút (P0-7), ns/op ±10–37 % (phase 4),
# loopback là network stack ảo hoá. Test đúng/sai (deadline, phản chứng) không cần chạy lại ở đây.
#
#   ./scripts/linux-baseline.sh                  # bộ mặc định, RTT 0
#   RTT=1 ./scripts/linux-baseline.sh            # thêm vòng netem RTT 20 ms (cần sudo)
#   REPEAT=5 ONLY=p5,p7 ./scripts/linux-baseline.sh
#
# Kết quả: bench/baseline/<host>-<ngày>/  gồm env.txt và một file .txt mỗi phép đo.
# Chỉ so TỈ SỐ với số WSL2 trong bench/*.txt; số tuyệt đối giữa hai máy không so được (phase 0).
set -euo pipefail
cd "$(dirname "$0")/.."

REPEAT=${REPEAT:-3}
RTT=${RTT:-0}
ONLY=${ONLY:-p0,p3,p4,p5,p6,p7}
OUT="bench/baseline/$(hostname)-$(date +%Y%m%d)"
BIN=$(mktemp -d)
mkdir -p "$OUT"

cleanup() {
  [ "$RTT" = 1 ] && sudo tc qdisc del dev lo root 2>/dev/null || true
  pkill -f "$BIN/" 2>/dev/null || true
  rm -rf "$BIN"
}
trap cleanup EXIT INT TERM

want() { [[ ",$ONLY," == *",$1,"* ]]; }

ulimit -n 65536 2>/dev/null || echo "⚠️  không nâng được ulimit -n — 10k conn của phase 0 sẽ hỏng"

# --- 1. Môi trường: không có phần này thì mọi con số vô nghĩa ---
./scripts/envcap.sh >/dev/null
{
  echo "# ngày: $(date -Is)"
  echo "# host: $(hostname)"
  echo "# commit: $(git rev-parse --short HEAD)$(git diff --quiet || echo ' (+ thay đổi chưa commit)')"
  echo; cat "bench/env-$(hostname).txt"
  echo; echo '$ lscpu | grep -E "Model name|^CPU\(s\)|Thread|MHz"'; lscpu | grep -E "Model name|^CPU\(s\)|Thread|MHz" || true
  echo; echo '# governor (muốn "performance"; "powersave" làm p99 nhảy theo xung nhịp)'
  cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor 2>/dev/null || echo "(không có cpufreq)"
  echo; echo '# turbo (1 = tắt turbo ⇒ số ổn định hơn)'
  cat /sys/devices/system/cpu/intel_pstate/no_turbo 2>/dev/null || echo "(không có intel_pstate)"
  echo; echo '$ uptime'; uptime
  echo; echo '# 5 tiến trình ăn CPU nhất lúc bắt đầu: có gì > 20 % thì tắt đi rồi chạy lại'
  ps -eo pcpu,comm --sort=-pcpu | head -6
} | tee "$OUT/env.txt"
if grep -qi microsoft /proc/version; then
  echo "⚠️  đây vẫn là WSL2 — số ra không trả được nợ 'Linux thuần'. Ctrl-C trong 5 s để dừng."
  sleep 5
fi
gov=$(cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor 2>/dev/null || echo none)
[ "$gov" = powersave ] && echo "⚠️  governor=powersave. Nên: sudo cpupower frequency-set -g performance"

# --- 2. Build một lần, chạy binary: tránh tính giờ biên dịch và tiến trình con mồ côi của `go run` ---
echo "==> build vào $BIN"
for c in netlab upstream edgegate proxylab poollab lblab slowlab ratelab shedlab drainlab chaoslab; do
  go build -o "$BIN/$c" "./cmd/$c"
done
go build -tags nodefense7 -o "$BIN/chaoslab-nodefense7" ./cmd/chaoslab
go build -tags nodefense7 -o "$BIN/slowlab-nodefense7" ./cmd/slowlab

# Generator và thứ bị đo tranh CPU: chừa core cuối cho OS (giống phase0-run.sh)
pin=""
ncpu=$(nproc)
if command -v taskset >/dev/null && [ "$ncpu" -ge 4 ]; then pin="taskset -c 0-$((ncpu-2))"; fi

# run <tên> <lệnh...> : chạy REPEAT lần, nối vào một file. Median của N lần, không phải một lần (P0-7).
run() {
  local name=$1; shift
  echo; echo "==> $name ×$REPEAT"
  : > "$OUT/$name.txt"
  for i in $(seq 1 "$REPEAT"); do
    echo "### lần $i — $(date +%T)" | tee -a "$OUT/$name.txt"
    # shellcheck disable=SC2086
    $pin "$@" 2>&1 | tee -a "$OUT/$name.txt" || echo "!! lần $i thoát mã $? — cũng là kết quả" | tee -a "$OUT/$name.txt"
  done
}

suite() { # suite <hậu tố tag>: những phép đo có nghĩa khác nhau theo RTT
  local t=$1
  if want p0; then
    run "p0-syscall-$t"  "$BIN/netlab" -exp syscall  -n 5000 -bufio=false -tag "$t"
    run "p0-rtt-$t"      "$BIN/netlab" -exp rtt      -n 2000 -bufio=false -tag "$t"
    run "p0-nagle-$t"    "$BIN/netlab" -exp nagle    -n 150  -bufio=false -tag "$t"
  fi
  if want p5; then
    local n=2000; [ "$t" != rtt0 ] && n=200
    run "p5-poollab-$t"  "$BIN/poollab" -pool both -n "$n"
  fi
}

# --- 3. RTT 0 ---
suite rtt0

if want p0; then
  run p0-omission "$BIN/netlab" -exp omission -rate 885 -duration 10s -svc 1ms -workers 1 -bufio=false
  run p0-mem-10k  "$BIN/netlab" -exp mem -conns 10000 -bufio=true
  run p0-mem-nobufio "$BIN/netlab" -exp mem -conns 10000 -bufio=false
  # G6: tcp_tw_reuse=2 chỉ bật cho loopback ⇒ phải dùng IP interface thật để thấy port exhaustion
  ip=$(ip -4 -o addr show scope global | awk 'NR==1{print $4}' | cut -d/ -f1)
  [ -n "$ip" ] && run p0-limits "$BIN/netlab" -exp limits -duration 20s -bufio=false -addr "$ip:9200"
fi

if want p3; then
  echo; echo "==> p3-proxybench ×$REPEAT (upstream :8081, edgegate :8080)"
  : > "$OUT/p3-proxybench.txt"
  for i in $(seq 1 "$REPEAT"); do
    echo "### lần $i" >> "$OUT/p3-proxybench.txt"
    "$BIN/upstream" -addr :8081 & up=$!; sleep 0.5
    for nd in true false; do
      "$BIN/edgegate" -config config/dev.json -nodelay=$nd & eg=$!; sleep 0.5
      if [ $nd = true ]; then
        $pin "$BIN/proxylab" -mode overhead  -n 2000
        $pin "$BIN/proxylab" -mode keepalive -n 2000
      fi
      $pin "$BIN/proxylab" -mode nagle -n 100 -chunkms 0 -label "[nodelay=$nd]"
      kill $eg; wait $eg 2>/dev/null || true
    done 2>&1 | tee -a "$OUT/p3-proxybench.txt"
    kill $up; wait $up 2>/dev/null || true
  done
fi

if want p4; then
  # Hiệu ứng ~5 %: một lần ns/op là nhiễu. -count 10 rồi benchstat (go install golang.org/x/perf/cmd/benchstat@latest)
  echo; echo "==> p4-bench-readrequest"
  go test ./internal/httpx -run '^$' -bench ReadRequest -benchmem -count 10 | tee "$OUT/p4-bench-readrequest.txt"
  command -v benchstat >/dev/null && benchstat "$OUT/p4-bench-readrequest.txt" | tee "$OUT/p4-benchstat.txt"
fi

if want p6; then
  run p6-lblab-even    "$BIN/lblab" -algos rr,leastconn,p2c,chash -n 20000
  run p6-lblab-skew    "$BIN/lblab" -algos rr,leastconn,p2c,chash -skew slow=10x,err=30% -n 20000
  run p6-lblab-flap    "$BIN/lblab" -algos leastconn,p2c,p2c-slow -flap -n 20000 -conns 4
  run p6-lblab-recover "$BIN/lblab" -algos leastconn,p2c,p2c-slow -flap -recover -n 20000 -conns 4
fi

if want p7; then
  run p7-slowlab           "$BIN/slowlab" -conns 500 -byte-every 10s
  run p7-slowlab-maxinfl   "$BIN/slowlab" -conns 500 -byte-every 10s -max-inflight 64
  run p7-slowlab-nodefense "$BIN/slowlab-nodefense7" -conns 500 -byte-every 10s
  run p7-ratelab           "$BIN/ratelab" -ips 20 -rate 50 -burst 10 -per-ip 100 -duration 5s
  run p7-retrylab          "$BIN/chaoslab" -scenario retry -duration 10s -rate 1000 -drop 0.5
  run p7-retrylab-blind    "$BIN/chaoslab-nodefense7" -scenario retry -duration 10s -rate 1000 -drop 0.5
  run p7-shedlab           "$BIN/shedlab" -x 2 -duration 10s
  run p7-drainlab          "$BIN/drainlab" -restarts 20 -every 500ms
  run p7-chaoslab          "$BIN/chaoslab" -duration 60s -rate 500 -tick 300ms
fi

# --- 4. RTT 20 ms (tuỳ chọn): netem trên lo áp HAI chiều ⇒ delay 10ms = RTT ~20 ms. RTT phải ĐO ---
if [ "$RTT" = 1 ]; then
  echo; echo "==> netem delay 10ms trên lo"
  sudo tc qdisc add dev lo root netem delay 10ms
  ping -c 5 -i 0.2 127.0.0.1 | tail -1 | tee "$OUT/rtt20-ping.txt"
  REPEAT=1 suite rtt20
  sudo tc qdisc del dev lo root
  tc qdisc show dev lo | tee -a "$OUT/rtt20-ping.txt"   # phải thấy noqueue
fi

echo
echo "==> xong: $(ls -1 "$OUT" | wc -l) file trong $OUT"
echo "Bước tiếp: so TỈ SỐ với bench/*.txt (WSL2), ghi lệch vào bảng 'Giả thuyết sai' của diary phase tương ứng."
