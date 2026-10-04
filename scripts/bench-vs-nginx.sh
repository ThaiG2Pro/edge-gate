#!/usr/bin/env bash
# Phase 9 G8: EdgeGate / nginx / httputil.ReverseProxy (+ "direct" = upstream
# trần: trần của chính loadgen). Cùng upstream (epolllab epoll, 1 KiB), cùng
# open-loop rate, 64 worker keep-alive. Proxy core 0-1 (GOMAXPROCS=2 / nginx 2
# worker), upstream core 2-3, loadgen core 4-5. Ba proxy chạy cùng lúc (rỗi khi
# không bị bắn) để mỗi rate chạy xen kẽ ba cột.
set -euo pipefail
RATES=${RATES:-"2000 5000 10000 15000 20000 30000"}
DUR=${DUR:-10s}
cd "$(dirname "$0")/.."
pids=()
cleanup() {
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  docker rm -f edgegate-bench-nginx >/dev/null 2>&1 || true
  wait 2>/dev/null || true
}
trap cleanup EXIT

date; uptime
taskset -c 2-3 bin/epolllab -impl epoll -loops 2 -addr 127.0.0.1:18100 >/dev/null &
pids+=($!)
taskset -c 0-1 env GOMAXPROCS=2 bin/edgegate -config config/bench.json 2>/dev/null &
pids+=($!)
taskset -c 0-1 env GOMAXPROCS=2 bin/rpbaseline -listen 127.0.0.1:18092 -upstream 127.0.0.1:18100 2>/dev/null &
pids+=($!)
docker run -d --rm --name edgegate-bench-nginx --network host --cpuset-cpus 0-1 \
  -v "$PWD/config/nginx-bench.conf:/etc/nginx/nginx.conf:ro" nginx:1.25-alpine >/dev/null
sleep 2
docker exec edgegate-bench-nginx nginx -v 2>&1

NCPU=$(nproc)
GEN_CPUS="4-$((NCPU-1))"
[ "$NCPU" -le 5 ] && GEN_CPUS="4-5"

declare -A ADDR=([direct]=127.0.0.1:18100 [edgegate]=127.0.0.1:18091 [nginx]=127.0.0.1:18090 [rp]=127.0.0.1:18092)
for t in direct edgegate nginx rp; do   # khởi động: pool upstream, JIT của chính loadgen
  taskset -c "$GEN_CPUS" bin/perflab -mode open -addr "${ADDR[$t]}" -rate 2000 -duration 2s -label "warm-$t" >/dev/null 2>&1 || true
done
order=(edgegate nginx rp direct)
for r in $RATES; do
  for t in "${order[@]}"; do
    taskset -c "$GEN_CPUS" bin/perflab -mode open -addr "${ADDR[$t]}" -rate "$r" -duration "$DUR" -workers 256 -label "$t" || true
  done
  order=("${order[@]:1}" "${order[0]}")   # xoay thứ tự mỗi rate
done
