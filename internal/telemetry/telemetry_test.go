package telemetry

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTrackerFirstAccessHasEmptyHistory(t *testing.T) {
	tr := NewTracker(10*time.Second, 0.3)
	now := time.Now()
	f := tr.Observe("a|1", now)
	if f.AccessCount != 0 {
		t.Fatalf("first access count = %v, want 0", f.AccessCount)
	}
	if f.TimeSinceLastAccess != -1 {
		t.Fatalf("first access recency = %v, want -1 sentinel", f.TimeSinceLastAccess)
	}
}

func TestTrackerAccumulatesHistory(t *testing.T) {
	tr := NewTracker(10*time.Second, 0.3)
	base := time.Now()
	tr.Observe("a|1", base)
	tr.Observe("a|1", base.Add(2*time.Second))
	f := tr.Observe("a|1", base.Add(4*time.Second))

	if f.AccessCount != 2 {
		t.Fatalf("access count = %v, want 2", f.AccessCount)
	}
	if f.TimeSinceLastAccess != 2 {
		t.Fatalf("recency = %v, want 2s", f.TimeSinceLastAccess)
	}
	if f.BurstScore <= 0 {
		t.Fatalf("burst score = %v, want > 0 after repeated hits", f.BurstScore)
	}
}

func TestBurstDecaysWhenIdle(t *testing.T) {
	tr := NewTracker(1*time.Second, 0.3) // short tau so idle decay is visible
	base := time.Now()
	tr.Observe("a|1", base)
	tr.Observe("a|1", base.Add(100*time.Millisecond)) // tight burst
	hot := tr.Observe("a|1", base.Add(200*time.Millisecond)).BurstScore
	// Long idle gap, then access again: burst should be far lower than the hot run.
	cold := tr.Observe("a|1", base.Add(30*time.Second)).BurstScore
	if cold >= hot {
		t.Fatalf("burst did not decay while idle: hot=%v cold=%v", hot, cold)
	}
}

func TestRecorderWritesHeaderAndRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.csv")
	r, err := NewRecorder(path, 10*time.Second, 0.3, 1024)
	if err != nil {
		t.Fatal(err)
	}
	r.Record("example.com|1", false)
	r.Record("example.com|1", true)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) != 3 { // header + 2 rows
		t.Fatalf("got %d lines, want 3: %v", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], "ts_unix_ms,key,is_hit,") {
		t.Fatalf("unexpected header: %q", lines[0])
	}
	cols := strings.Split(lines[1], ",")
	if len(cols) != 11 {
		t.Fatalf("row has %d columns, want 11: %q", len(cols), lines[1])
	}
	if cols[1] != "example.com|1" {
		t.Fatalf("key column = %q, want example.com|1", cols[1])
	}
}

// TestRecorderConcurrentRecordIsRaceFree drives Record from many goroutines so
// the race detector can vet the tracker map and the writer channel together.
func TestRecorderConcurrentRecordIsRaceFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.csv")
	r, err := NewRecorder(path, 10*time.Second, 0.3, 4096)
	if err != nil {
		t.Fatal(err)
	}

	const goroutines, each = 16, 1000
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				key := "k" + string(rune('A'+(g+i)%26)) + "|1"
				r.Record(key, i%2 == 0)
			}
		}(g)
	}
	wg.Wait()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}
