# Adaptive DNS Resolver with Self-Optimizing Cache

A concurrent DNS resolver in Go with a TTL-aware LRU cache, Prometheus metrics,
and (later) an ML-driven adaptive eviction layer.

## Status
Stages 1–8a complete: concurrent UDP resolver, hand-rolled TTL-aware LRU cache
(race-detector clean under concurrent load), bounded worker pool, in-process
Prometheus metrics, a reproducible cache-on vs cache-off benchmark, a one-command
Dockerized Prometheus + Grafana stack, a telemetry pipeline that logs engineered
features per query, an XGBoost retention model served in-process in Go for
sampled adaptive eviction + admission (deterministic LRU fallback), and a
three-way benchmark showing where the model beats LRU.

Key measured results:
- Caching vs none: **~9.8× throughput, ~53× lower median latency** at a 90% hit
  ratio (controlled 20 ms upstream).
- Adaptive vs LRU on a recency-hostile workload: LRU **0% hits**, adaptive
  **~44%**, **~1.76× throughput, ~2× lower median latency**. On recency-friendly
  traffic the model ties LRU — stated honestly.

See [BENCHMARKS.md](BENCHMARKS.md) for both benchmarks and their limits,
[TELEMETRY.md](TELEMETRY.md) for the feature/label pipeline, and
[MODEL.md](MODEL.md) for training, Go inference parity, and the eviction/admission
policy.

Still to build (Stage 8b): predictive prefetching (the 4th config), and an
LFU baseline to isolate the model's value over plain frequency.

## Run it yourself

Everything runs locally — no cloud account, no hosted services. Clone and go.

**Prerequisites**
- Go 1.25+ (builds the resolver, cache, worker pool, load client)
- Docker + Docker Compose (only for the Prometheus + Grafana dashboard stack)
- Python 3.11+ (only for the optional XGBoost training step)

**1. Build and test the core (Go only)**
```sh
make build        # resolver, stub upstream, load client
make test         # unit tests under the race detector (-race), must be clean
```

**2. See the live observability dashboard (Docker)**
```sh
docker compose up --build        # resolver + stub upstream + Prometheus + Grafana
# in another terminal, drive traffic so the panels move:
./bin/loadtest -addr 127.0.0.1:8053 -duration 60s -concurrency 50 -hot 20 -phot 0.9
# open Grafana at http://localhost:3000  (Prometheus at http://localhost:9090)
docker compose down              # stop everything when done
```

**3. Reproduce the cache-on vs cache-off benchmark (Go only)**
```sh
# full steps in BENCHMARKS.md; produces the ~9.8x throughput / ~53x latency result
```

**4. Train the model and run the adaptive-vs-LRU benchmark (Go + Python)**
```sh
make venv              # one-time: create .venv, install xgboost/sklearn/numpy
make telemetry         # generate a labeled training dataset under load
make train             # train + evaluate + export ml/model/model.json (parity-tested in Go)
make bench-adaptive    # no-cache / LRU / adaptive on the recency-hostile workload
```

A recruiter can stop after step 1 or 2 and still see a complete, running system;
steps 3–4 reproduce the numbers in the docs. The repository is the deliverable —
no deployment required.

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
