#!/usr/bin/env bash
# Phase 9 G8 phụ: CPU / request của ba proxy ở cùng closed-loop 64 conn —
# ít nhạy với load nền hơn rps. CPU = utime+stime (/proc/<pid>/stat, mọi luồng;
# nginx = tổng các tiến trình nginx của container, thấy được từ pid namespace host).
set -euo pipefail
cd "$(dirname "$0")/.."
pids=()
cleanup() { for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done; docker rm -f edgegate-bench-nginx >/dev/null 2>&1 || true; wait 2>/dev/null || true; }
trap cleanup EXIT
cpu() { local s=0; for p in "$@"; do read -r ut st < <(awk '{print $14, $15}' /proc/$p/stat); s=$((s+ut+st)); done; echo $s; }
date; uptime
taskset -c 2-3 bin/epolllab -impl epoll -loops 2 -addr 127.0.0.1:18100 >/dev/null & pids+=($!)
taskset -c 0-1 env GOMAXPROCS=2 bin/edgegate -config config/bench.json 2>/dev/null & pids+=($!); EG=$!
taskset -c 0-1 env GOMAXPROCS=2 bin/rpbaseline -listen 127.0.0.1:18092 2>/dev/null & pids+=($!); RP=$!
docker run -d --rm --name edgegate-bench-nginx --network host --cpuset-cpus 0-1 -v "$PWD/config/nginx-bench.conf:/etc/nginx/nginx.conf:ro" nginx:1.25-alpine >/dev/null
sleep 2
NG=$(pgrep -x nginx | tr '\n' ' ')
for r in 1 2 3; do
  for t in edgegate:18091 nginx:18090 rp:18092; do
    n=${t%%:*}; p=${t##*:}
    case $n in edgegate) P=$EG;; rp) P=$RP;; nginx) P=$NG;; esac
    c0=$(cpu $P)
    out=$(taskset -c 4-5 bin/perflab -mode rps -conns 64 -duration 6s -label "$n" -addr 127.0.0.1:$p)
    c1=$(cpu $P)
    resp=$(echo "$out" | grep -o '[0-9]* response' | cut -d' ' -f1)
    echo "$out · CPU proxy $(( (c1-c0) ))0 ms = $(echo "scale=1; ($c1-$c0)*10000/$resp" | bc) µs/req"
  done
done
