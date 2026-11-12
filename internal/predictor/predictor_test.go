package predictor

import (
	"encoding/csv"
	"math"
	"os"
	"strconv"
	"testing"
)

const (
	modelPath  = "../../ml/model/model.json"
	parityPath = "../../ml/model/parity.csv"
)

// TestParityWithXGBoost loads sample rows with XGBoost's own raw margin (from
// ml/train.py) and asserts the Go tree walker reproduces each one. This is the
// correctness guarantee that in-process Go inference == the trained model.
func TestParityWithXGBoost(t *testing.T) {
	if _, err := os.Stat(modelPath); err != nil {
		t.Skipf("no exported model (run `make train`): %v", err)
	}
	m, err := Load(modelPath)
	if err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(parityPath)
	if err != nil {
		t.Skipf("no parity file (run `make train`): %v", err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 {
		t.Fatal("parity file has no data rows")
	}

	header := rows[0]
	marginCol := len(header) - 1
	nFeat := len(FeatureOrder)
	if marginCol != nFeat {
		t.Fatalf("parity header has %d feature cols, want %d", marginCol, nFeat)
	}

	const tol = 1e-4
	maxDiff := 0.0
	for i, row := range rows[1:] {
		feats := make([]float64, nFeat)
		for j := 0; j < nFeat; j++ {
			feats[j] = mustFloat(t, row[j])
		}
		want := mustFloat(t, row[marginCol])
		got := m.Score(feats)
		diff := math.Abs(got - want)
		if diff > maxDiff {
			maxDiff = diff
		}
		if diff > tol {
			t.Errorf("row %d: Go score=%.6f, XGBoost margin=%.6f, diff=%.6f > tol",
				i, got, want, diff)
		}
	}
	t.Logf("parity over %d rows: max abs diff = %.2e", len(rows)-1, maxDiff)
}

func mustFloat(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}
