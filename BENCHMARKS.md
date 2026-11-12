# Benchmarks

This document records the measured effect of the TTL-aware LRU cache on the
resolver's throughput and tail latency. It is deliberately reproducible: load is
driven against a **controlled stub upstream** with a fixed simulated round-trip
time, not a public resolver whose latency drifts, so the cache-on vs cache-off
comparison isolates the cache and can be re-run to the same numbers.

## What is being compared

| Run | Resolver flag | Every query path |
|---|---|---|
| **Cache ON** | (default) | Hit → served from memory; miss → forwarded to upstream, then cached |
| **Cache OFF** | `-no-cache` | Every query forwarded to upstream (baseline) |

Both runs use the identical workload, upstream, and hardware. The only variable
is whether the cache is active.

## Methodology

- **Hardware:** Apple M4 (10 cores), macOS 15.6, Go 1.26.5.
- **Topology:** `loadtest` → `resolver` → `stubupstream`, all on loopback.
- **Upstream:** `stubupstream` answers every A query with one fixed answer record
  after a **fixed 20 ms delay** (stands in for real upstream RTT) and a 60 s TTL.
- **Workload:** 50 concurrent senders for 8 s. Each query targets a **hot set of
  20 repeated domains with probability 0.9**, otherwise a freshly generated
  **unique** domain (forces a miss). This yields a steady ~90% hit ratio, which
  is a realistic shape for recursive DNS traffic (a small set of popular names
  dominates, with a long unique tail).
- **Resolver config:** 16 workers, queue 256, cache size 10 000.
- **Measurement:** `loadtest` records per-query wall-clock latency and computes
  percentiles (nearest-rank); hit ratio is read from the resolver's own
  Prometheus counters (`dns_cache_hits_total` / `dns_cache_misses_total`) over
  the run.

Reproduce with:

```sh
go build -o bin/resolver ./cmd/resolver
go build -o bin/stubupstream ./cmd/stubupstream
go build -o bin/loadtest ./cmd/loadtest

./bin/stubupstream -listen 127.0.0.1:8054 -delay 20ms -ttl 60 &

# Cache ON
./bin/resolver -listen 127.0.0.1:8053 -upstream 127.0.0.1:8054 \
  -metrics-addr 127.0.0.1:9153 -workers 16 -queue 256 &
./bin/loadtest -addr 127.0.0.1:8053 -duration 8s -concurrency 50 \
  -hot 20 -phot 0.9 -metrics-url http://127.0.0.1:9153/metrics

# Cache OFF (baseline)
./bin/resolver -listen 127.0.0.1:8053 -upstream 127.0.0.1:8054 \
  -no-cache -workers 16 -queue 256 &
./bin/loadtest -addr 127.0.0.1:8053 -duration 8s -concurrency 50 -hot 20 -phot 0.9
```

## Results

Two consecutive runs, consistent to within noise.

| Metric | Cache OFF (baseline) | Cache ON | Improvement |
|---|---|---|---|
| Throughput | ~741 qps | ~7 240 qps | **~9.8× higher** |
| Latency p50 | ~65 ms | ~1.2 ms | **~53× lower** |
| Latency p95 | ~85 ms | ~21.6 ms | ~3.9× lower |
| Latency p99 | ~87 ms | ~40 ms | ~2.2× lower |
| Cache hit ratio | — | 89.9% | — |

Raw output (run 1):

```
RUN A: CACHE ON              RUN B: CACHE OFF
throughput  7253 qps         throughput  741 qps
latency p50 1.231ms          latency p50 64.914ms
latency p95 21.616ms         latency p95 85.426ms
latency p99 39.751ms         latency p99 86.486ms
hit ratio   89.9%
```

## How to read these numbers honestly

- **Why is the cache-off p50 (~65 ms) higher than the 20 ms upstream delay?**
  This is **worker-pool saturation**, not a measurement error. With the cache
  off, all 50 in-flight senders must reach the upstream, but only 16 workers
  forward at a time and each forward blocks ~20 ms. Queries queue behind the
  pool, so observed latency is the 20 ms upstream RTT *plus* queue wait. This is
  the correct, expected behavior of a bounded pool under offered load that
  exceeds its service rate, and it is exactly why caching helps: a hit never
  occupies the upstream path at all.
- **Why is the cache-on p99 (~40 ms) still well above the p50?** The ~10% of
  queries that are unique still miss and pay the full saturated upstream path.
  The tail is dominated by misses; the median is dominated by hits. That gap is
  the real, honest signature of a cache under a mixed workload.
- **What this does NOT prove.** Single node, loopback, synthetic traffic, and a
  *constant* upstream latency with no jitter or packet loss. Real upstream RTT
  varies, real traffic is burstier, and loopback removes NIC and network-stack
  cost. These numbers quantify the cache's effect under controlled conditions;
  they are not a claim about internet-facing production performance.
- **The hit ratio is an input, not an achievement.** 89.9% follows directly from
  the `-phot 0.9` workload. The honest claim is "at a ~90% hit ratio, the cache
  delivered ~9.8× throughput and ~53× lower median latency," not "the cache
  achieves 90% hit ratio."

## Defensible one-line claim

> On an Apple M4, a TTL-aware LRU cache raised a forwarding DNS resolver's
> throughput ~9.8× (741 → 7 240 qps) and cut median latency ~53× (65 → 1.2 ms)
> at a 90% hit ratio, measured against a controlled 20 ms upstream.
