#!/usr/bin/env bash
# Phase 9 G5: profile CPU 30 s của edgegate (trước = nodefense9 / sau) dưới
# cùng tải open-loop. Proxy core 0-1, upstream core 2-3, loadgen core 4-5.
# Profile + bản -top lưu bench/p9-pprof-<bin>.{prof,txt}.
set -euo pipefail
RATE=${1:-8000}
cd "$(dirname "$0")/.."
pids=()
cleanup() { for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done; wait 2>/dev/null || true; }
trap cleanup EXIT

taskset -c 2-3 bin/epolllab -impl epoll -loops 2 -addr 127.0.0.1:18100 >/dev/null &
pids+=($!)
sleep 0.5
for b in edgegate-nodefense9 edgegate; do
  taskset -c 0-1 env GOMAXPROCS=2 bin/$b -config config/bench.json -pprof 127.0.0.1:6061 2>/dev/null &
  px=$!
  sleep 1
  taskset -c 4-5 bin/perflab -mode open -addr 127.0.0.1:18091 -rate 1000 -duration 2s -label warm >/dev/null
  curl -s -o "bench/p9-pprof-$b.prof" "http://127.0.0.1:6061/debug/pprof/profile?seconds=30" &
  cp=$!
  taskset -c 4-5 bin/perflab -mode open -addr 127.0.0.1:18091 -rate "$RATE" -duration 32s -label "$b"
  wait "$cp"
  kill "$px"; wait "$px" 2>/dev/null || true
  go tool pprof -top -nodecount=35 "bench/p9-pprof-$b.prof" 2>/dev/null > "bench/p9-pprof-$b.txt"
  echo "== $b: bench/p9-pprof-$b.txt"
  head -45 "bench/p9-pprof-$b.txt"
done
