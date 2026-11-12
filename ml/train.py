#!/usr/bin/env python3
"""Train the cache-retention model and export it for in-process Go inference.

Pipeline:
  1. Load the labeled dataset (features + look-ahead label).
  2. Split TEMPORALLY (early rows train, late rows test) -- a random split would
     leak the future into the past and inflate the score.
  3. Evaluate three predictors on the held-out tail:
       - recency-only   (what plain LRU already encodes: keep the recently-used)
       - logistic regression
       - XGBoost
     so the model's value is measured against LRU, not assumed.
  4. Export XGBoost to a minimal portable JSON the Go resolver walks directly
     (no dependency on XGBoost's internal format on the serving side), plus a
     parity file so a Go test can prove its inference matches XGBoost's.

Run: .venv/bin/python ml/train.py
"""
import csv
import json
import os

import numpy as np
import xgboost as xgb
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import average_precision_score, roc_auc_score
from sklearn.preprocessing import StandardScaler

FEATURES = [
    "time_since_last_access_s",
    "access_count",
    "age_s",
    "freq_per_min",
    "ewma_interval_s",
    "burst_score",
    "hour",
    "dow",
]
DATA = "ml/data/labeled.csv"
OUT_DIR = "ml/model"
TRAIN_FRAC = 0.70


def load():
    X, y = [], []
    with open(DATA, newline="") as f:
        for r in csv.DictReader(f):
            X.append([float(r[c]) for c in FEATURES])
            y.append(int(r["label"]))
    return np.asarray(X, dtype=np.float64), np.asarray(y, dtype=np.int32)


def metrics(name, y_true, score):
    auc = roc_auc_score(y_true, score)
    ap = average_precision_score(y_true, score)
    print(f"  {name:20s} ROC-AUC={auc:.4f}  PR-AUC={ap:.4f}")
    return auc


def flatten_tree(dump: dict) -> dict:
    """Flatten one XGBoost JSON tree into contiguous index-aligned nodes."""
    nodes = []

    def visit(node) -> int:
        idx = len(nodes)
        nodes.append(None)  # reserve slot to keep pre-order indices stable
        if "leaf" in node:
            nodes[idx] = {"leaf": True, "value": float(node["leaf"])}
            return idx
        children = {c["nodeid"]: c for c in node["children"]}
        feat = int(node["split"][1:])  # "f3" -> 3
        rec = {
            "leaf": False,
            "feature": feat,
            "threshold": float(node["split_condition"]),
        }
        nodes[idx] = rec
        rec["yes"] = visit(children[node["yes"]])   # taken when value < threshold
        rec["no"] = visit(children[node["no"]])     # taken when value >= threshold
        rec["missing"] = rec["yes"] if node["missing"] == node["yes"] else rec["no"]
        return idx

    visit(dump)
    return {"nodes": nodes}


def export(booster: xgb.Booster, path: str):
    dumps = booster.get_dump(dump_format="json")
    trees = [flatten_tree(json.loads(d)) for d in dumps]
    model = {"features": FEATURES, "base_margin": 0.0, "trees": trees}
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        json.dump(model, f)
    print(f"  exported {len(trees)} trees -> {path}")


def write_parity(booster: xgb.Booster, X: np.ndarray, path: str, n: int = 200):
    """Save sample feature rows with XGBoost's raw margin, for a Go parity test."""
    sample = X[:n]
    dm = xgb.DMatrix(sample, feature_names=[f"f{i}" for i in range(len(FEATURES))])
    margin = booster.predict(dm, output_margin=True)  # base_score=0.5 -> == sum(leaves)
    with open(path, "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(FEATURES + ["margin"])
        for row, m in zip(sample, margin):
            w.writerow([f"{v:.6f}" for v in row] + [f"{m:.6f}"])
    print(f"  wrote {len(sample)} parity rows -> {path}")


def main():
    X, y = load()
    n = len(y)
    cut = int(n * TRAIN_FRAC)
    Xtr, Xte = X[:cut], X[cut:]
    ytr, yte = y[:cut], y[cut:]
    print(f"rows={n}  train={len(ytr)}  test={len(yte)}  "
          f"test positives={yte.mean():.1%}")

    print("\nheld-out test performance (higher AUC = better):")

    # Baseline 1: recency only. LRU keeps the recently used, so -recency is its
    # implicit retention score. First-access sentinel (-1) -> treat as stale.
    recency = Xte[:, FEATURES.index("time_since_last_access_s")].copy()
    recency[recency < 0] = 1e9
    metrics("recency-only (LRU)", yte, -recency)

    # Baseline 2: logistic regression on standardized features.
    scaler = StandardScaler().fit(Xtr)
    lr = LogisticRegression(max_iter=1000)
    lr.fit(scaler.transform(Xtr), ytr)
    metrics("logistic regression", yte, lr.predict_proba(scaler.transform(Xte))[:, 1])

    # Model: XGBoost. base_score=0.5 so raw margin == sum of leaf values, which
    # is what the Go walker computes (and all we need to RANK keys for eviction).
    clf = xgb.XGBClassifier(
        n_estimators=60, max_depth=6, learning_rate=0.3,
        subsample=0.9, colsample_bytree=0.9,
        objective="binary:logistic", base_score=0.5,
        eval_metric="auc", n_jobs=0,
    )
    clf.fit(Xtr, ytr)
    booster = clf.get_booster()
    metrics("xgboost", yte, clf.predict_proba(Xte)[:, 1])

    print()
    export(booster, os.path.join(OUT_DIR, "model.json"))
    write_parity(booster, X, os.path.join(OUT_DIR, "parity.csv"))


if __name__ == "__main__":
    main()
