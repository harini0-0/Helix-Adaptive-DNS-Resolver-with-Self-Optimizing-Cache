#!/bin/bash
# Generate a labeled training dataset from live resolver telemetry.
#
# Runs the resolver against the controlled stub upstream under synthetic load
# with feature logging enabled, then runs the offline labeling pass. Output:
#   ml/data/telemetry.csv  -- raw per-query feature rows
#   ml/data/labeled.csv    -- features + look-ahead label (training-ready)
#
# Uses ports 8055/8056/9155 to avoid colliding with the docker-compose stack.
set -e
cd "$(dirname "$0")/.."

go build -o bin/resolver ./cmd/resolver
go build -o bin/stubupstream ./cmd/stubupstream
go build -o bin/loadtest ./cmd/loadtest
mkdir -p ml/data

DURATION="${DURATION:-20s}"
CONCURRENCY="${CONCURRENCY:-40}"
HORIZON="${HORIZON:-60}"

cleanup() { kill "$STUB" "$RES" 2>/dev/null || true; wait 2>/dev/null || true; }
trap cleanup EXIT

./bin/stubupstream -listen 127.0.0.1:8056 -delay 20ms -ttl 60 >/tmp/stub_tel.log 2>&1 &
STUB=$!
sleep 0.5

./bin/resolver -listen 127.0.0.1:8055 -upstream 127.0.0.1:8056 \
  -metrics-addr 127.0.0.1:9155 -workers 16 -queue 256 -cache-size 10000 \
  -telemetry-file ml/data/telemetry.csv -burst-window 10s >/tmp/res_tel.log 2>&1 &
RES=$!
sleep 0.7

./bin/loadtest -addr 127.0.0.1:8055 -duration "$DURATION" -concurrency "$CONCURRENCY" \
  -hot 20 -phot 0.85 -metrics-url http://127.0.0.1:9155/metrics

# Graceful stop so the telemetry buffer flushes fully.
kill -TERM "$RES" 2>/dev/null || true
sleep 1
RES=""

echo "=== raw telemetry ==="
wc -l ml/data/telemetry.csv
echo "=== labeling (horizon=${HORIZON}s) ==="
python3 -I ml/label.py --in ml/data/telemetry.csv --out ml/data/labeled.csv --horizon "$HORIZON"
