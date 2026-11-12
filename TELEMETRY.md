# Telemetry pipeline (Stage 6)

This stage turns the live query stream into a **training-ready dataset** for the
adaptive-cache model (Stage 7). It is the bridge from the deterministic resolver
to the ML layer — and it is built so that correctness and performance never
depend on it: telemetry is opt-in (`-telemetry-file`) and runs off the hot path.

## Design

```
query ──► handler ──► Recorder.Record(key, hit)
                          │  (compute features, O(1), under a mutex)
                          ▼
                   buffered channel ──► single writer goroutine ──► CSV
                          │
                          └─ full? drop + count (never block the handler)
```

Two deliberate choices, both defensible in an interview:

1. **Features are computed online; the label is derived offline.** The prediction
   target is "will this key be requested again within the horizon?" — a *future*
   fact that cannot be known when the row is written. So the resolver logs only
   what it knows at access time (a feature snapshot of the key's past), and
   [`ml/label.py`](ml/label.py) adds the label by looking ahead in the event log.
2. **The writer is decoupled from query handling.** Handlers hand a formatted row
   to a buffered channel drained by one background goroutine, so disk I/O never
   stalls a DNS response. If the buffer fills under extreme load, rows are dropped
   and counted rather than applying backpressure to resolution — the same
   "protect the hot path" principle as the bounded worker pool. (For a complete
   dataset at benchmark rates the buffer is sized so drops stay at zero.)

## Row schema (`ml/data/telemetry.csv`)

One row per cache lookup. Features describe the key's history **up to, not
including, this access** — exactly the information a predictor would have at
decision time.

| Column | Meaning |
|---|---|
| `ts_unix_ms` | event time (used for offline labeling, not a feature) |
| `key` | `domain|type` cache key (not a feature; used for grouping) |
| `is_hit` | 1 if served from cache, 0 if it was a miss |
| `time_since_last_access_s` | recency; `-1` on the key's first-ever access |
| `access_count` | prior accesses of this key |
| `age_s` | seconds since the key was first seen |
| `freq_per_min` | prior accesses per minute over the key's lifetime |
| `ewma_interval_s` | smoothed inter-arrival gap (α=0.3) |
| `burst_score` | decaying access-rate accumulator over ~`burst-window` seconds |
| `hour` | hour of day [0,23] |
| `dow` | day of week [0=Sun,6=Sat] |

The `burst_score` is an O(1) exponentially-decaying counter: on each access it is
multiplied by `exp(-Δt/τ)` and incremented, so a key being hammered scores high
and a key that goes quiet decays back down — no sliding-window buffer needed.

## Generating the dataset

```sh
make telemetry          # runs scripts/gen_telemetry.sh
# or, tuned:
DURATION=30s HORIZON=60 ./scripts/gen_telemetry.sh
```

This produces `ml/data/telemetry.csv` (raw) and `ml/data/labeled.csv`
(features + `label`). Both are gitignored — regenerate rather than commit them.

## What a sample run looked like

~97k rows, zero drops, horizon 60 s:

```
positives    : 82397 (85.0%)  -- key recurs within 60s
negatives    : 14540 (15.0%)
mean feature by class (label=0 vs label=1):
  access_count     0=     9.615   1=  2059.823
  freq_per_min     0=    28.877   1= 12789.871
  burst_score      0=     4.148   1=  1170.853
```

The class-conditional feature means differ by orders of magnitude, which is the
signal the model will exploit: keys that recur are the ones already being
accessed often and in bursts.

## Honest caveat (say this in an interview)

This separation is **clean because the workload is synthetic** — a fixed hot set
plus a unique long tail makes "recurs soon" almost perfectly predictable from
access count alone. Real recursive-DNS traffic is burstier and noisier, the
class balance shifts, and the model's job is genuinely harder. These numbers
validate that the *pipeline* produces a trainable dataset with real signal; they
are not a claim that cache retention is an easy prediction problem in production.
The point of Stage 7 is to measure whether the model beats plain LRU **on this
same workload**, and to report it honestly either way.
