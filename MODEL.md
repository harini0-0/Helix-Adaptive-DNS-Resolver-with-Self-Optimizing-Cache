# Adaptive-cache model (Stage 7)

Stage 7 trains a gradient-boosted model to predict cache retention and wires it
into the resolver as a **sampled adaptive eviction** policy, with deterministic
LRU as the guaranteed fallback. Nothing about correctness depends on the model.

## Training (`ml/train.py`)

- **Input:** `ml/data/labeled.csv` (features + look-ahead label from Stage 6).
- **Temporal split:** the earliest 70% of rows train, the latest 30% test. A
  random split would leak future accesses into the training set and inflate the
  score — the classic time-series evaluation mistake.
- **Three predictors, same test set**, so the model is measured against LRU:
  1. `recency-only` — score = negative time-since-last-access. This is exactly
     what plain LRU encodes, used here as the baseline to beat.
  2. `logistic regression` — a linear reference.
  3. `xgboost` — 60 trees, depth 6.
- **Metrics:** ROC-AUC and PR-AUC (threshold-independent, right for ranking and
  for class imbalance).

### Result on the current synthetic workload (honest)

```
recency-only (LRU)   ROC-AUC=0.9961  PR-AUC=0.9985
logistic regression  ROC-AUC=0.9950  PR-AUC=0.9976
xgboost              ROC-AUC=0.9961  PR-AUC=0.9986
```

**XGBoost ties LRU; it does not beat it.** This is the expected, honest outcome:
the synthetic workload is a fixed hot set plus a unique long tail, so "will this
key recur?" is almost perfectly determined by recency alone, and there is no
residual pattern for the model to exploit. Reporting this — rather than hiding it
— is the point of measuring against a baseline.

**What would make the model win** is a workload with structure recency cannot
see: keys that go cold and then reliably spike (time-of-day/periodic revival),
so that "least recently used" is the *wrong* thing to evict. Building that
workload and running the four-way comparison is Stage 8.

## Serving the model in Go (no Python on the hot path)

`ml/train.py` exports a **minimal portable JSON** (`ml/model/model.json`, ~8 KB)
that the Go resolver walks directly — the serving side has zero dependency on
XGBoost's internal format.

- [`internal/predictor`](internal/predictor/predictor.go) loads the trees and
  computes a key's retention score in O(depth) by summing leaf values. Only the
  raw margin is needed, because eviction only has to *rank* candidates and the
  logistic transform is monotonic.
- A **parity test** ([`predictor_test.go`](internal/predictor/predictor_test.go))
  loads sample rows with XGBoost's own raw margin (`ml/model/parity.csv`) and
  asserts the Go walker reproduces them — verified to within `1.7e-6`. This is
  the guarantee that in-process Go inference equals the trained model.

## Adaptive eviction (`internal/cache`)

When the cache is full, instead of always dropping the LRU tail, it:

1. samples the `-evict-sample` (default 8) least-recently-used entries,
2. scores each with the model (probability it recurs soon),
3. evicts the lowest-scoring one.

If the scorer has no data for any sampled key, it falls back to strict LRU. This
is the same bounded, sampled approach Redis uses for approximate LRU/LFU — O(k)
per eviction, never O(n) — and it keeps a model failure from ever degrading
correctness.

## Running it

```sh
make venv            # one-time: create .venv, install xgboost/sklearn/numpy
make telemetry       # generate ml/data/labeled.csv under load
make train           # train, evaluate, export ml/model/model.json + parity.csv

# resolver with adaptive eviction (requires telemetry for live features)
./bin/resolver -telemetry-file /tmp/tel.csv -model-file ml/model/model.json \
  -evict-sample 8 -cache-size 10000
```

## Known limitation

Adaptive eviction currently requires `-telemetry-file` because the eviction
scorer reads live features from the telemetry tracker, which today is owned by
the CSV recorder. Decoupling the feature tracker from file logging (so the model
can run without writing a CSV) is a clean follow-up.
