#!/usr/bin/env bash
# linux-measure.sh — chạy các phép đo 📏 CÒN NỢ (phase 0–10) trên máy Linux thuần,
# ghim core, có netem/docker/strace khi cần. Bổ sung cho linux-baseline.sh (phase
# 0–7 timing): script này lo P-env-2, phase 8/9/10, và các cờ mới (dialers,
# heal-weight, overload). Số chốt CHỈ đáng tin khi `uptime` load < 1 và KHÔNG phải
# WSL2 (loopback ảo hoá, xem docs/debts.md P0).
#
#   ./scripts/linux-measure.sh                 # mọi phần không cần sudo/docker
#   SUDO=1 ./scripts/linux-measure.sh          # thêm phần netem (P0-6, P10-8 loss)
#   DOCKER=1 ./scripts/linux-measure.sh        # thêm oracle (P4-6) + bench-vs-nginx (P9-4)
#   ONLY=penv2,p7 ./scripts/linux-measure.sh   # chỉ vài phần
#   ETH0=10.0.0.5 ./scripts/linux-measure.sh   # P0-4 cần IP eth0 (không loopback)
#   REPEAT=5 ./scripts/linux-measure.sh
#
# Kết quả: bench/linux/<host>-<ngày>/ — một .txt mỗi phép đo + env.txt + README.txt.
# Mỗi phần in TIÊU CHÍ ĐẠT để bạn biết số nào đóng được nợ.
set -uo pipefail
cd "$(dirname "$0")/.."

REPEAT=${REPEAT:-3}
SUDO=${SUDO:-0}
DOCKER=${DOCKER:-0}
ETH0=${ETH0:-}
ONLY=${ONLY:-penv2,p0,p3,p4,p5,p6,p7,p8,p9,p10}
OUT="bench/linux/$(hostname)-$(date +%Y%m%d-%H%M%S)"
BIN=$(mktemp -d)
mkdir -p "$OUT"

want() { [[ ",$ONLY," == *",$1,"* ]]; }

NCPU=$(nproc)
if [ "$NCPU" -lt 4 ]; then
  echo "!! cần ≥ 4 core (có $NCPU). Số ghim core sẽ vô nghĩa. Thoát." >&2
  exit 1
fi
# Ba nhóm core: upstream = core cao nhất; proxy = 0..k; generator = phần giữa.
UP_CPU=$((NCPU-1))
PX_CPU="0-1"
GEN_CPU="2-$((NCPU-2))"
pin() { taskset -c "$1" "${@:2}"; }

cleanup() {
  [ "$SUDO" = 1 ] && sudo tc qdisc del dev lo root 2>/dev/null || true
  docker rm -f edgegate-oracle-nginx edgegate-oracle-h2o edgegate-bench-nginx 2>/dev/null || true
  pkill -f "$BIN/" 2>/dev/null || true
  rm -rf "$BIN"
}
trap cleanup EXIT INT TERM

{
  echo "host     : $(hostname)"
  echo "ngày     : $(date -Is)"
  echo "kernel   : $(uname -r)"
  echo "nproc    : $NCPU (upstream=$UP_CPU proxy=$PX_CPU generator=$GEN_CPU)"
  echo "uptime   : $(uptime)"
  echo "ulimit -n: $(ulimit -n)"
  echo "go       : $(go version)"
  echo "WSL2?    : $(uname -r | grep -qi microsoft && echo CÓ-số-không-chốt-được || echo không)"
  echo "SUDO=$SUDO DOCKER=$DOCKER ETH0=${ETH0:-<chưa đặt>} REPEAT=$REPEAT ONLY=$ONLY"
} | tee "$OUT/env.txt"

if uptime | grep -qE 'load average: [1-9]'; then
  echo; echo "!! CẢNH BÁO: load average ≥ 1 — số latency sẽ nhiễu. Chờ máy yên rồi chạy lại."
fi

echo; echo "==> build vào $BIN"
for c in netlab upstream edgegate epolllab proxylab poollab lblab slowlab chaoslab tlslab h2lab perflab smuggleoracle; do
  go build -o "$BIN/$c" "./cmd/$c" || { echo "build $c lỗi"; exit 1; }
done
go build -tags nodefense7 -o "$BIN/chaoslab-nodefense7" ./cmd/chaoslab

# run <tên> <lệnh...> : REPEAT lần, nối vào một file (median của N, không một lần).
run() {
  local name=$1; shift
  echo; echo "==> $name ×$REPEAT  ($(date +%T))"
  : > "$OUT/$name.txt"
  for i in $(seq 1 "$REPEAT"); do
    echo "### lần $i — $(date +%T)" >> "$OUT/$name.txt"
    "$@" >> "$OUT/$name.txt" 2>&1 || echo "!! lần $i thoát mã $? — cũng là kết quả" >> "$OUT/$name.txt"
  done
  tail -4 "$OUT/$name.txt"
}
crit() { echo "    TIÊU CHÍ: $*"; }

# ---------------------------------------------------------------------------
if want penv2; then
  echo; echo "########## P-env-2: generator/proxy ghim core riêng ##########"
  crit "KHÔNG dòng 'GENERATOR KHÔNG THEO NỔI' nào ⇒ số latency pinlab dùng được"
  make -s pinlab 2>&1 | tee "$OUT/p-env-2-pinlab.txt" | grep -vE '^\s{15}' | tail -20 || true
fi

if want p0; then
  echo; echo "########## P0-4: trần là port hay client? (dialers 1 vs 16) ##########"
  if [ -n "$ETH0" ]; then
    crit "N=16 ~ N=1 conn/s ⇒ trần PORT; N=16 tăng ~16x ⇒ trần CLIENT tuần tự"
    run p0-4-dialers-1  pin "$GEN_CPU" "$BIN/netlab" -exp limits -duration 40s -dialers 1  -addr "$ETH0:9200"
    run p0-4-dialers-16 pin "$GEN_CPU" "$BIN/netlab" -exp limits -duration 40s -dialers 16 -addr "$ETH0:9201"
  else
    echo "    (bỏ P0-4: đặt ETH0=<ip eth0> — loopback không chạm trần port)"
  fi
  if want p0 && [ "$SUDO" = 1 ]; then
    echo; echo "########## P0-6: netem có jitter + loss (mạng thực tế hơn) ##########"
    sudo tc qdisc add dev lo root netem delay 10ms 3ms distribution normal loss 0.1%
    ping -c 20 -i 0.2 127.0.0.1 | tail -2 | tee "$OUT/p0-6-ping.txt"
    crit "ping phải thấy mdev LỚN và có gói mất; lblab-skew: P2C vẫn hơn RR ở tail"
    make -s lblab-skew 2>&1 | tee "$OUT/p0-6-lblab-skew.txt" | tail -8 || true
    sudo tc qdisc del dev lo root
  fi
fi

if want p3; then
  echo; echo "########## P3-5: G2/G3 có ổn định giữa các lần không ##########"
  pin "$UP_CPU" "$BIN/upstream" -addr :8081 >/dev/null 2>&1 &
  pin "$PX_CPU" "$BIN/edgegate" -config config/dev.json >/dev/null 2>&1 &
  sleep 1
  crit "p50 overhead 5 lần dao động < ±0.1x ⇒ số G2/G3 chốt được"
  for i in 1 2 3 4 5; do pin "$GEN_CPU" "$BIN/proxylab" -mode overhead -n 2000 | grep 'G2 p50'; done | tee "$OUT/p3-5-overhead.txt"
  pkill -f "$BIN/edgegate" 2>/dev/null; pkill -f "$BIN/upstream" 2>/dev/null; sleep 0.3
fi

if want p4 && [ "$DOCKER" = 1 ]; then
  echo; echo "########## P4-6: oracle thứ hai/ba (nginx + h2o) ##########"
  crit "ca 21 nginx cũng 400; bare LF (30/31) + Connection: Host (50) cả hai origin NHẬN ⇒ strict đúng"
  make -s oracle-ext 2>&1 | tee "$OUT/p4-6-oracle-ext.txt" | tail -6 || true
fi

if want p5; then
  echo; echo "########## P5-1: pool có/không, máy yên ##########"
  crit "pool giảm p50 rõ (loopback ít, netem 20 ms nhiều); so TỈ SỐ với bench/p5-*.txt"
  run p5-1-poollab pin "$GEN_CPU" "$BIN/poollab" -pool both -n 2000
  echo; echo "########## P5-4b: idle-race POST (502) — số PUT thật cần đo ở đây ##########"
  crit "ghi % 502 của probe-on; PUT/DELETE replay là món P5-4b, cần số này trước khi code"
  run p5-4b-idlerace pin "$GEN_CPU" "$BIN/poollab" -idlerace -n 10000
fi

if want p6; then
  echo; echo "########## P6-2b: lệch P2C lúc khởi động ##########"
  crit "lblab-even: p2c max/min share ≤ 1.2x trên backend giống hệt"
  run p6-2b-lblab-even pin "$GEN_CPU" "$BIN/lblab" -algos rr,leastconn,p2c,chash -n 20000 -max-share-ratio 1.2
  echo; echo "########## P6-5: outlier vs active health, dial lỗi ##########"
  crit "TestLBKillRevive xanh 3/3 ⇒ cửa sổ dial lỗi không bị passive cắt oan"
  go test ./internal/proxy -run 'TestLBKillRevive' -count=3 -v 2>&1 | tee "$OUT/p6-5-killrevive.txt" | tail -4
fi

if want p7; then
  echo; echo "########## P7-2b: trần theo IP (đóng ngay) ##########"
  crit "ghi reconnect/s của attacker khi -max-conns-per-ip 100; so RAM vs CPU"
  run p7-2b-slowlab pin "$GEN_CPU" "$BIN/slowlab" -conns 500 -byte-every 10s -max-conns-per-ip 100
  echo; echo "########## P7-4: chaoslab cân lại (heal-weight) ##########"
  crit "với -heal-weight 6, tỉ lệ 200 KHÔNG còn nghiêng 5xx vô nghĩa ⇒ status mix đọc được"
  run p7-4-chaos-balanced pin "$GEN_CPU" "$BIN/chaoslab" -duration 60s -rate 500 -heal-weight 6 -seed 3
  echo; echo "########## P7-6: retry budget ở overload (rate > capacity) ##########"
  crit "budget-on: goodput CAO hơn + khuếch đại THẤP hơn so với -tags nodefense7 (retry mù)"
  run p7-6-overload-budget pin "$GEN_CPU" "$BIN/chaoslab"            -scenario overload -duration 20s -rate 2000 -drop 0.5
  run p7-6-overload-blind  pin "$GEN_CPU" "$BIN/chaoslab-nodefense7" -scenario overload -duration 20s -rate 2000 -drop 0.5
fi

if want p8; then
  echo; echo "########## P8-1: CPU handshake TLS, ghim core ##########"
  crit "ms/handshake ổn định giữa các lần; so ECDSA vs RSA vs kex"
  run p8-1-handshake pin "$GEN_CPU" "$BIN/tlslab" -mode handshake -n 2000 -kex x25519
fi

if want p9; then
  if [ "$DOCKER" = 1 ]; then
    echo; echo "########## P9-4: bảng ba cột EdgeGate/nginx/ReverseProxy ##########"
    crit "EdgeGate ≈ nginx; ReverseProxy chậm ~3x — số chốt trên Linux thuần"
    ./scripts/bench-vs-nginx.sh 2>&1 | tee "$OUT/p9-4-bench-vs-nginx.txt" | tail -12 || true
  fi
  echo; echo "########## P9-6: RAM/goroutine connection rỗi, so nginx ##########"
  crit "heap pprof: 8 KiB/conn còn lại ở đâu; goroutine mỗi conn không rò"
  pin "$PX_CPU" "$BIN/edgegate" -config config/bench.json -pprof 127.0.0.1:6061 >/dev/null 2>&1 &
  pin "$UP_CPU" "$BIN/epolllab" -impl epoll -loops 1 -addr 127.0.0.1:18100 >/dev/null 2>&1 &
  sleep 1
  pin "$GEN_CPU" "$BIN/perflab" -mode idle -conns 10000 -addr 127.0.0.1:18091 >/dev/null 2>&1 &
  sleep 20
  { echo "== goroutine =="; curl -s 127.0.0.1:6061/debug/pprof/goroutine?debug=1 | head -1;
    echo "== heap top =="; go tool pprof -top -inuse_space http://127.0.0.1:6061/debug/pprof/heap 2>/dev/null | head -15; } \
    | tee "$OUT/p9-6-pprof.txt" || true
  pkill -f "$BIN/edgegate" 2>/dev/null; pkill -f "$BIN/epolllab" 2>/dev/null; pkill -f "$BIN/perflab" 2>/dev/null; sleep 0.3
fi

if want p10; then
  echo; echo "########## P10-8: HOL blocking + CPU h2, ghim core ##########"
  cp "$BIN/edgegate" bin/edgegate 2>/dev/null; cp "$BIN/epolllab" bin/epolllab 2>/dev/null
  if [ "$SUDO" = 1 ]; then
    for loss in 0 1 2 5; do
      [ "$loss" != 0 ] && sudo tc qdisc add dev lo root netem delay 10ms loss ${loss}%
      crit "tcphol loss=${loss}%: h2 (một TCP) tụt so với h1 (nhiều TCP) khi loss tăng"
      run "p10-8-tcphol-loss${loss}" "$BIN/h2lab" -mode tcphol -n 100 -par 32
      [ "$loss" != 0 ] && sudo tc qdisc del dev lo root
    done
  else
    echo "    (bỏ phần loss: cần SUDO=1; chạy phần CPU không cần sudo)"
  fi
  crit "cpu h2: 8×8 stream vs 64 conn — CPU/req không tệ hơn nhiều"
  run p10-8-cpu pin "$GEN_CPU" "$BIN/h2lab" -mode cpu -rounds 5
fi

# ---------------------------------------------------------------------------
cat > "$OUT/README.txt" <<EOF
Đo trên $(hostname), $(date -Is), kernel $(uname -r), $NCPU core.
Mỗi .txt là $REPEAT lần của một phép đo — lấy MEDIAN, bỏ lần đầu nếu lệch (JIT/cache).
So TỈ SỐ với số WSL2 trong bench/*.txt; số tuyệt đối giữa hai máy không so trực tiếp.
Đọc tiêu chí từng nợ trong docs/REPRODUCE-LINUX.md.
EOF
echo; echo "==> xong: $(ls -1 "$OUT" | wc -l) file trong $OUT"
echo "    đọc docs/REPRODUCE-LINUX.md để biết mỗi file đóng nợ nào."
