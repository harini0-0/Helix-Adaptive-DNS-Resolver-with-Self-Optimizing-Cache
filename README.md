# Adaptive DNS Resolver with Self-Optimizing Cache

A concurrent DNS resolver in Go with a TTL-aware LRU cache, Prometheus metrics,
and (later) an ML-driven adaptive eviction layer.

## Status
Stages 1–6 complete: concurrent UDP resolver, hand-rolled TTL-aware LRU cache
(race-detector clean under concurrent load), bounded worker pool, in-process
Prometheus metrics, a reproducible cache-on vs cache-off benchmark, a one-command
Dockerized Prometheus + Grafana stack, and a telemetry pipeline that logs
engineered features per query and derives a training-ready, labeled dataset.

See [BENCHMARKS.md](BENCHMARKS.md) for measured results (~9.8× throughput, ~53×
lower median latency at a 90% hit ratio against a controlled 20 ms upstream) and
[TELEMETRY.md](TELEMETRY.md) for the feature/label pipeline.

Still to build: the XGBoost adaptive-eviction / prefetch layer and its 4-way
benchmark (Stages 7–8).

## Observability stack

`docker compose up --build` brings up four services, all preconfigured:

| Service | Purpose | URL |
|---|---|---|
| `stubupstream` | controlled upstream (fixed 20 ms RTT) | internal |
| `resolver` | the DNS resolver (scratch image, ~14 MB) | udp://localhost:8053 |
| `prometheus` | scrapes `resolver:9153/metrics` every 2 s | http://localhost:9090 |
| `grafana` | preprovisioned dashboard + datasource | http://localhost:3000 |

Drive traffic from the host to see the dashboard move:

```sh
go build -o bin/loadtest ./cmd/loadtest
./bin/loadtest -addr 127.0.0.1:8053 -duration 60s -concurrency 50 -hot 20 -phot 0.9
```

![Grafana dashboard](docs/grafana-dashboard.png)

The resolver image is `FROM scratch` with a static binary, so it has no shell;
the container health check re-execs the binary in `-health-check` mode to probe
its own `/metrics` endpoint. Prometheus and Grafana configs are baked into their
images (no host bind-mounts), so the stack is fully self-contained.

## Benchmark

```sh
go build -o bin/stubupstream ./cmd/stubupstream   # controlled upstream
go build -o bin/resolver     ./cmd/resolver        # the resolver
go build -o bin/loadtest      ./cmd/loadtest        # concurrent query client
```

Full reproduction steps are in [BENCHMARKS.md](BENCHMARKS.md).
