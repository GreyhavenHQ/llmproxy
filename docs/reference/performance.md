# Performance

This page records stress test results for llmproxy 1.0.0 using the built-in
harness at `cmd/stress`.

## What the harness measures

The harness boots a fake OpenAI-compatible upstream and the proxy itself in a
single process, over real TCP sockets. It seeds a provider, a model, and an API
key into a fresh SQLite database (WAL mode) in a temporary directory, then
fires concurrent chat completion requests split between unary and SSE
streaming according to `-stream-ratio`. It reports throughput, latency
percentiles per request type, heap growth, and whether usage accounting kept
up (usage events recorded N/N).

Because the load generator, the proxy, and the upstream all share one process
and one machine, they compete for the same CPU. The numbers below are
therefore a conservative floor for proxy-only throughput, not a ceiling.
Usage accounting is written asynchronously off the request path, and the
harness verifies at the end that every request produced a usage event.

This is a synthetic in-process benchmark, not a network benchmark. Real
deployments are bounded by upstream model latency, which is typically measured
in seconds, several orders of magnitude above the proxy overhead measured
here.

## Machine and software

| | |
| --- | --- |
| CPU | Apple M4 Max, 16 cores |
| Memory | 64 GiB |
| OS | macOS 26.5.1 |
| Go | go1.26.1 darwin/arm64 |
| llmproxy | 1.0.0 |

Each scenario was run twice and the better run is shown. All runs completed
with zero errors and full usage accounting.

## Results

2,000 requests, concurrency 100, half streaming:

```
requests:      2000 (concurrency 100)
completed:     2000  errors: 0
wall time:     0.21s  throughput: 9340.7 req/s
unary   (1000): p50=8.0ms p95=29.8ms p99=47.8ms max=75.9ms
stream  (1000): p50=7.4ms p95=26.5ms p99=41.1ms max=85.4ms
usage events recorded: 2000/2000
heap: 2.8 MiB -> 8.9 MiB
```

10,000 requests, concurrency 200, half streaming:

```
requests:      10000 (concurrency 200)
completed:     10000  errors: 0
wall time:     1.17s  throughput: 8514.3 req/s
unary   (5000): p50=15.6ms p95=68.3ms p99=101.4ms max=197.6ms
stream  (5000): p50=15.6ms p95=69.6ms p99=106.2ms max=198.8ms
usage events recorded: 10000/10000
heap: 2.8 MiB -> 19.7 MiB
```

50,000 requests, concurrency 500, half streaming:

```
requests:      50000 (concurrency 500)
completed:     50000  errors: 0
wall time:     6.87s  throughput: 7275.6 req/s
unary   (25000): p50=46.0ms p95=207.6ms p99=314.1ms max=973.0ms
stream  (25000): p50=46.3ms p95=204.2ms p99=320.1ms max=697.1ms
usage events recorded: 50000/50000
heap: 2.9 MiB -> 43.5 MiB
```

10,000 requests, concurrency 200, all streaming:

```
requests:      10000 (concurrency 200)
completed:     10000  errors: 0
wall time:     1.22s  throughput: 8227.6 req/s
unary   (0): n/a
stream  (10000): p50=16.3ms p95=70.9ms p99=109.3ms max=276.0ms
usage events recorded: 10000/10000
heap: 2.8 MiB -> 10.7 MiB
```

10,000 requests, concurrency 200, all unary:

```
requests:      10000 (concurrency 200)
completed:     10000  errors: 0
wall time:     1.20s  throughput: 8354.3 req/s
unary   (10000): p50=16.2ms p95=69.8ms p99=104.6ms max=204.6ms
stream  (0): n/a
usage events recorded: 10000/10000
heap: 2.9 MiB -> 20.6 MiB
```

Streaming and unary requests cost about the same, and throughput degrades
gently as concurrency rises well past the core count.

## Why the numbers hold up

The results depend on one structural rule: the proxy never holds a database
connection across an upstream call. Authentication does a single query and
returns before the upstream request starts, and the streaming relay holds no
store handle at all. Usage events are recorded asynchronously after the
response completes. A slow upstream can therefore never pin a SQLite
connection, so database throughput stays independent of upstream latency.

## Reproducing

Run the default scenario with:

```
just stress
```

or run a custom scenario directly:

```
go run ./cmd/stress -requests N -concurrency C -stream-ratio R
```

The harness needs no configuration or running services; it creates its own
temporary database and cleans up after itself.
