# Adaptive DNS Resolver with Self-Optimizing Cache

A concurrent DNS resolver in Go with a TTL-aware LRU cache, Prometheus metrics,
and (later) an ML-driven adaptive eviction layer.

## Status
Stages 1–5 complete: concurrent UDP resolver, hand-rolled TTL-aware LRU cache
(race-detector clean under concurrent load), bounded worker pool, in-process
Prometheus metrics, and a reproducible cache-on vs cache-off benchmark.

See [BENCHMARKS.md](BENCHMARKS.md) for measured results (~9.8× throughput, ~53×
lower median latency at a 90% hit ratio against a controlled 20 ms upstream).

Still to build: Docker + Grafana stack (Stage 4 polish), telemetry feature
logging, and the XGBoost adaptive-eviction / prefetch layer (Stages 6–8).

## Benchmark

```sh
go build -o bin/stubupstream ./cmd/stubupstream   # controlled upstream
go build -o bin/resolver     ./cmd/resolver        # the resolver
go build -o bin/loadtest      ./cmd/loadtest        # concurrent query client
```

Full reproduction steps are in [BENCHMARKS.md](BENCHMARKS.md).
