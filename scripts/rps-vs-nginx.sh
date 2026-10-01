#!/usr/bin/env bash
# closed-loop trần rps của ba proxy (cùng cách ghim core như bench-vs-nginx.sh)
set -euo pipefail
cd /home/thaivro/test-project/edge-gate
pids=()
cleanup() { for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done; docker rm -f edgegate-bench-nginx >/dev/null 2>&1 || true; wait 2>/dev/null || true; }
trap cleanup EXIT
date; uptime
taskset -c 2-3 bin/epolllab -impl epoll -loops 2 -addr 127.0.0.1:18100 >/dev/null & pids+=($!)
taskset -c 0-1 env GOMAXPROCS=2 bin/edgegate -config config/bench.json 2>/dev/null & pids+=($!); EG=$!
taskset -c 0-1 env GOMAXPROCS=2 bin/rpbaseline -listen 127.0.0.1:18092 -upstream 127.0.0.1:18100 2>/dev/null & pids+=($!); RP=$!
docker run -d --rm --name edgegate-bench-nginx --network host --cpuset-cpus 0-1 -v "$PWD/config/nginx-bench.conf:/etc/nginx/nginx.conf:ro" nginx:1.25-alpine >/dev/null
sleep 2
for r in 1 2 3; do
  for t in edgegate:18091 nginx:18090 rp:18092 direct:18100; do
    n=${t%%:*}; p=${t##*:}
    taskset -c 4-5 bin/perflab -mode rps -conns ${CONNS:-64} -duration 8s -label "$n" -addr 127.0.0.1:$p
  done
done
