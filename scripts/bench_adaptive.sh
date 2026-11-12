#!/bin/bash
# Four-way comparison on a recency-hostile "revival" workload: no-cache, LRU, and
# the ML-driven adaptive cache (prefetching config is Stage 8b, pending).
#
# A recurring key set is requested round-robin (no bursts) interleaved with
# unique noise, so popular keys return only after more distinct keys than the
# cache holds -- the case where recency (LRU) fails and frequency wins.
#
# Regenerates revival telemetry, retrains the model on it, then benchmarks each
# config against the same controlled upstream. Requires `make venv` first.
set -e
cd "$(dirname "$0")/.."

go build -o bin/resolver ./cmd/resolver
go build -o bin/stubupstream ./cmd/stubupstream
go build -o bin/loadtest ./cmd/loadtest
mkdir -p ml/data

WL="-workload revival -recurring ${RECURRING:-200} -noise ${NOISE:-0.4}"
CACHE=${CACHE:-256}
SAMPLE=${SAMPLE:-32}

STUB=""; RES=""
cleanup() { kill $STUB $RES 2>/dev/null || true; wait 2>/dev/null || true; }
trap cleanup EXIT

./bin/stubupstream -listen 127.0.0.1:8060 -delay 5ms -ttl 300 >/tmp/bench_stub.log 2>&1 &
STUB=$!
sleep 0.5

echo "## generate revival telemetry + retrain"
./bin/resolver -listen 127.0.0.1:8061 -upstream 127.0.0.1:8060 -metrics-addr 127.0.0.1:9161 \
  -workers 16 -queue 256 -cache-size $CACHE -telemetry-file ml/data/telemetry.csv >/tmp/bench_resA.log 2>&1 &
RES=$!
sleep 0.8
./bin/loadtest -addr 127.0.0.1:8061 -duration 30s -concurrency 40 $WL >/dev/null
kill -TERM $RES 2>/dev/null || true; sleep 1; RES=""
python3 -I ml/label.py --in ml/data/telemetry.csv --out ml/data/labeled.csv --horizon 60
.venv/bin/python ml/train.py

run_bench () {
  local name="$1"; shift
  ./bin/resolver -listen 127.0.0.1:8062 -upstream 127.0.0.1:8060 -metrics-addr 127.0.0.1:9162 \
    -workers 16 -queue 256 -cache-size $CACHE "$@" >/tmp/bench_res_$name.log 2>&1 &
  RES=$!
  sleep 0.9
  echo "---------- $name ----------"
  ./bin/loadtest -addr 127.0.0.1:8062 -duration 25s -concurrency 40 $WL -metrics-url http://127.0.0.1:9162/metrics
  kill -TERM $RES 2>/dev/null || true; wait $RES 2>/dev/null || true; RES=""; sleep 0.5
}

echo ""
echo "## benchmark"
run_bench nocache  -no-cache
run_bench lru
run_bench adaptive -telemetry-file /tmp/bench_tel.csv -model-file ml/model/model.json -evict-sample $SAMPLE
