# EdgeGate

[![ci](https://github.com/ThaiG2Pro/edge-gate/actions/workflows/ci.yml/badge.svg)](https://github.com/ThaiG2Pro/edge-gate/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/go-1.26-00ADD8)
![deps](https://img.shields.io/badge/dependencies-0-brightgreen)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](./LICENSE)

An L7 reverse proxy and load balancer written in Go **from raw TCP sockets**.
The data path uses only `net`. No `net/http`, no third-party modules.

This is not a production proxy. It is a **lab notebook**. Each phase states hypotheses
up front, then confirms or refutes them with a reproducible measurement, a fuzzer, or a
counter-proof test that must go red when the defense is compiled out.

> 🇻🇳 The lab notes (`diary/`, `docs/`, code comments) are written in Vietnamese.
> Vietnamese README: [`README.vi.md`](./README.vi.md).

## Highlights

- **HTTP/1.1 and HTTP/2 implemented by hand.** Includes the parser, chunked encoding, HPACK,
  and flow control. h2spec passes **145/145** over TLS+ALPN and in h2c-only mode.
- **Request smuggling defense, tested against real servers.** 66 attack payloads in
  `testdata/smuggle/`. Go's `net/http` agrees with EdgeGate's rejections only 46% of the time.
  nginx and h2o were used as extra oracles.
- **Key defenses have a counter-proof.** 11 `*-nodefense` targets in the `Makefile` compile
  a defense out with a build tag and assert that the matching test goes red.
- **Edge features with numbers attached.** Connection pool, RR / least-conn / P2C+EWMA /
  consistent hashing, active health checks with passive outlier ejection, rate limiting,
  load shedding, retry budget, graceful drain, TLS with SNI routing, `splice(2)`, and an epoll server.
- **Measured, not claimed.** 168 tests and 5 fuzzers. 66 tracked technical debts are all
  closed, each with the command that closed it, in [`docs/debts.md`](./docs/debts.md).

## Quick start

```bash
go build -o bin/upstream ./cmd/upstream
go build -o bin/edgegate ./cmd/edgegate

bin/upstream -addr 127.0.0.1:8081 &
bin/edgegate -listen 127.0.0.1:8080 -upstream 127.0.0.1:8081 &

curl -i http://127.0.0.1:8080/hello
# HTTP/1.1 200 OK ... hello from upstream
```

A CL.TE smuggling attempt is rejected before it reaches the upstream:

```bash
printf 'POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n' \
  | nc 127.0.0.1 8080
# HTTP/1.1 400 Bad Request
```

More configs live in `config/`: load balancing, resilience, TLS, and HTTP/2.

## Findings that surprised me

These are measured results. Each links to the diary entry with the exact command and raw output.

| Finding | Number | Source |
|---|---|---|
| Closed-loop load generators hide overload. Same system, two tools, different p99. | **1787×** gap | [phase 0](./diary/phase0.md) |
| Nagle + delayed ACK is a constant floor, not a tail effect. | **44 ms** at p50 and p99 | [phase 0](./diary/phase0.md) |
| Ephemeral port exhaustion shows no errors. Throughput just drops. | **1502 → 455** conn/s | [phase 0](./diary/phase0.md) |
| P2C+EWMA only helps when the slow node takes under 1% of traffic. It loses when a node recovers. | 3.4× vs 0.98× | [phase 6](./diary/phase6.md) |
| Load shedding under overload. | p99 **352×** better | [phase 7](./diary/phase7.md) |
| epoll vs goroutine-per-connection: same throughput, far less memory. | **55×** less RAM | [phase 9](./diary/phase9.md) |

The wrong hypotheses are recorded too. Phase 0 got 5 of 8 wrong.

## Layout

```
internal/
  frame/      length-prefixed framing over TCP
  httpx/      HTTP/1.1 request/response parser
  proxy/      forwarding, pool, retry, smuggling defense, resilience
  lb/         load-balancing algorithms, health checks, outlier ejection
  limit/      token bucket, retry budget, concurrency limits
  h2/         HTTP/2 frames, HPACK, flow control
  tlsx/       certificate store and SNI selection
  epollsrv/   hand-written epoll event loop (phase 9 comparison)
cmd/
  edgegate/   the proxy
  upstream/   a test backend
  *lab/       one measurement harness per phase (netlab, proxylab, lblab, chaoslab, ...)
```

## Phases

| # | Topic | Result |
|---|---|---|
| 0 | TCP physics: RTT, Nagle, port exhaustion | 5/8 hypotheses wrong |
| 1 | Framing | 4/7 wrong; fuzzed 1.58M execs clean |
| 2 | HTTP/1.1 engine | differential fuzz: 0 diffs; manual table found 2 real diffs |
| 3 | Vertical slice: first end-to-end `curl` | L7 overhead 3.39× on WSL2 |
| 4 | RFC 9112 compliance and smuggling | 61 cases; counter-proof red 20/52 |
| 5 | Upstream connection pool | dirty-connection counter-proof red 20/20 |
| 6 | Load balancing and health | 4/7 wrong |
| 7 | Resiliency | Slowloris did not kill Go; the connection cap did |
| 8 | TLS + SNI | 421 blocks domain fronting; reload with 0 errors |
| 9 | Performance, splice, epoll | on par with nginx; `httputil.ReverseProxy` 3.1× slower (WSL2) |
| 10 | HTTP/2 | h2spec 145/145; Rapid Reset and CONTINUATION flood defended |

Full write-ups: [`diary/`](./diary/). Roadmap and architecture: [`ROADMAP.md`](./ROADMAP.md).

## Running the tests

```bash
make test        # go test ./... -race
make phase0      # run the phase 0 experiments, results in bench/
```

Latency numbers from WSL2 are only indicative. [`docs/REPRODUCE-LINUX.md`](./docs/REPRODUCE-LINUX.md)
lists the commands and pass criteria for bare-metal Linux.

## License

[MIT](./LICENSE)
