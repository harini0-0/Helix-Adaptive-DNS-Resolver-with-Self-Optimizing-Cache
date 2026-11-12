#!/usr/bin/env python3
"""Offline labeling pass for the resolver's telemetry log.

The resolver logs feature snapshots online, but the prediction target -- "will
this key be requested again within the horizon?" -- is a *future* fact that can
only be known after the fact. This script derives that label by looking ahead in
the event stream, writing a training-ready CSV with a `label` column.

It uses only the Python standard library (no pandas), so it runs anywhere.

Usage:
    python3 ml/label.py --in ml/data/telemetry.csv --out ml/data/labeled.csv \
        --horizon 60
"""
import argparse
import bisect
import csv
from collections import defaultdict

FEATURE_COLS = [
    "time_since_last_access_s",
    "access_count",
    "age_s",
    "freq_per_min",
    "ewma_interval_s",
    "burst_score",
    "hour",
    "dow",
]


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--in", dest="inp", default="ml/data/telemetry.csv")
    ap.add_argument("--out", dest="out", default="ml/data/labeled.csv")
    ap.add_argument("--horizon", type=float, default=60.0,
                    help="seconds; label=1 if the key recurs within this window")
    args = ap.parse_args()

    with open(args.inp, newline="") as f:
        rows = list(csv.DictReader(f))
    if not rows:
        raise SystemExit("no telemetry rows to label")

    horizon_ms = args.horizon * 1000.0

    # Per-key sorted access timestamps, so we can ask "does this key recur soon?"
    times_by_key: dict[str, list[int]] = defaultdict(list)
    for r in rows:
        times_by_key[r["key"]].append(int(r["ts_unix_ms"]))
    for ts in times_by_key.values():
        ts.sort()

    pos = 0
    neg = 0
    with open(args.out, "w", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(FEATURE_COLS + ["label"])
        for r in rows:
            ts = int(r["ts_unix_ms"])
            series = times_by_key[r["key"]]
            # Is there a later access strictly after ts and within the horizon?
            i = bisect.bisect_right(series, ts)
            label = 0
            if i < len(series) and series[i] - ts <= horizon_ms:
                label = 1
            pos += label
            neg += 1 - label
            writer.writerow([r[c] for c in FEATURE_COLS] + [label])

    total = pos + neg
    print(f"labeled rows : {total}")
    print(f"positives    : {pos} ({pos / total:.1%}) -- key recurs within {args.horizon:g}s")
    print(f"negatives    : {neg} ({neg / total:.1%})")
    print(f"distinct keys: {len(times_by_key)}")
    print(f"written to   : {args.out}")

    # Quick signal check: mean of a couple of features by class. If the classes
    # differ, the features carry predictive signal (not proof, but a sanity gate).
    summarize_by_class(args.out)


def summarize_by_class(path: str) -> None:
    sums = {0: defaultdict(float), 1: defaultdict(float)}
    counts = {0: 0, 1: 0}
    with open(path, newline="") as f:
        for r in csv.DictReader(f):
            y = int(r["label"])
            counts[y] += 1
            for c in ("access_count", "freq_per_min", "burst_score"):
                sums[y][c] += float(r[c])
    print("\nmean feature by class (label=0 vs label=1):")
    for c in ("access_count", "freq_per_min", "burst_score"):
        m0 = sums[0][c] / counts[0] if counts[0] else 0
        m1 = sums[1][c] / counts[1] if counts[1] else 0
        print(f"  {c:16s} 0={m0:10.3f}   1={m1:10.3f}")


if __name__ == "__main__":
    main()
