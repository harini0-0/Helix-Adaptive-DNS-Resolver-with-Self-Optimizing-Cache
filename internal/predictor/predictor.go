// Package predictor runs in-process inference for the cache-retention model
// exported by ml/train.py. It walks a gradient-boosted tree ensemble to produce
// a retention score for a key: higher means more likely to be requested again,
// so the adaptive cache evicts the lowest-scoring candidates first.
//
// Only the raw margin (sum of leaf values) is computed, because eviction only
// needs to RANK candidates and the logistic transform is monotonic. If the model
// file is missing or malformed, callers fall back to deterministic LRU, so
// correctness never depends on the model.
package predictor

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// FeatureOrder is the feature vector layout the model expects. It must match
// the FEATURES list in ml/train.py and the telemetry feature snapshot.
var FeatureOrder = []string{
	"time_since_last_access_s",
	"access_count",
	"age_s",
	"freq_per_min",
	"ewma_interval_s",
	"burst_score",
	"hour",
	"dow",
}

type node struct {
	Leaf      bool    `json:"leaf"`
	Value     float64 `json:"value"`     // leaf output
	Feature   int     `json:"feature"`   // split feature index
	Threshold float64 `json:"threshold"` // go "yes" when value < threshold
	Yes       int     `json:"yes"`
	No        int     `json:"no"`
	Missing   int     `json:"missing"`
}

type tree struct {
	Nodes []node `json:"nodes"`
}

// Model is a loaded tree ensemble, safe for concurrent reads.
type Model struct {
	Features   []string `json:"features"`
	BaseMargin float64  `json:"base_margin"`
	Trees      []tree   `json:"trees"`
}

// Load reads a model exported by ml/train.py.
func Load(path string) (*Model, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Model
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse model: %w", err)
	}
	if len(m.Trees) == 0 {
		return nil, fmt.Errorf("model has no trees")
	}
	if len(m.Features) != len(FeatureOrder) {
		return nil, fmt.Errorf("model expects %d features, resolver provides %d",
			len(m.Features), len(FeatureOrder))
	}
	return &m, nil
}

// Score returns the raw margin (sum of leaf values plus base) for a feature
// vector laid out per FeatureOrder. Higher = more likely to recur = keep.
func (m *Model) Score(features []float64) float64 {
	sum := m.BaseMargin
	for i := range m.Trees {
		sum += walk(m.Trees[i].Nodes, features)
	}
	return sum
}

func walk(nodes []node, features []float64) float64 {
	i := 0
	for {
		n := &nodes[i]
		if n.Leaf {
			return n.Value
		}
		v := features[n.Feature]
		switch {
		case math.IsNaN(v):
			i = n.Missing
		case v < n.Threshold:
			i = n.Yes
		default:
			i = n.No
		}
	}
}
