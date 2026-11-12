package telemetry

import (
	"bufio"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// csvHeader documents the row schema. Keep it in sync with Recorder.format and
// with ml/label.py, which consumes this file.
const csvHeader = "ts_unix_ms,key,is_hit,time_since_last_access_s,access_count,age_s,freq_per_min,ewma_interval_s,burst_score,hour,dow\n"

// Recorder owns the feature tracker and an append-only CSV writer. Rows are
// handed to a buffered channel and flushed by a single background goroutine, so
// query handlers never block on disk I/O. It is safe for concurrent use.
type Recorder struct {
	tracker *Tracker
	rows    chan string
	done    chan struct{}
	f       *os.File
	w       *bufio.Writer

	dropped atomic.Uint64
	closeMu sync.Mutex
	closed  bool
}

// NewRecorder opens (truncating) path and starts the writer goroutine. A
// generous buffer keeps the hot path non-blocking; if it ever fills, rows are
// dropped and counted rather than stalling query handling.
func NewRecorder(path string, burstWindow time.Duration, ewmaAlpha float64, bufferSize int) (*Recorder, error) {
	if bufferSize <= 0 {
		bufferSize = 8192
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := bufio.NewWriterSize(f, 64*1024)
	if _, err := w.WriteString(csvHeader); err != nil {
		f.Close()
		return nil, err
	}

	r := &Recorder{
		tracker: NewTracker(burstWindow, ewmaAlpha),
		rows:    make(chan string, bufferSize),
		done:    make(chan struct{}),
		f:       f,
		w:       w,
	}
	go r.writeLoop()
	return r, nil
}

// Record computes the feature snapshot for key at the current time and enqueues
// a CSV row. It never blocks: if the buffer is full the row is dropped.
func (r *Recorder) Record(key string, hit bool) {
	now := time.Now()
	f := r.tracker.Observe(key, now)
	row := r.format(now, key, hit, f)
	select {
	case r.rows <- row:
	default:
		r.dropped.Add(1)
	}
}

func (r *Recorder) format(now time.Time, key string, hit bool, f Features) string {
	var b []byte
	b = strconv.AppendInt(b, now.UnixMilli(), 10)
	b = append(b, ',')
	b = append(b, key...)
	b = append(b, ',')
	if hit {
		b = append(b, '1')
	} else {
		b = append(b, '0')
	}
	b = append(b, ',')
	b = appendFloat(b, f.TimeSinceLastAccess)
	b = append(b, ',')
	b = appendFloat(b, f.AccessCount)
	b = append(b, ',')
	b = appendFloat(b, f.AgeSeconds)
	b = append(b, ',')
	b = appendFloat(b, f.FreqPerMin)
	b = append(b, ',')
	b = appendFloat(b, f.EWMAIntervalSeconds)
	b = append(b, ',')
	b = appendFloat(b, f.BurstScore)
	b = append(b, ',')
	b = strconv.AppendInt(b, int64(f.Hour), 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, int64(f.DayOfWeek), 10)
	b = append(b, '\n')
	return string(b)
}

func appendFloat(b []byte, v float64) []byte {
	return strconv.AppendFloat(b, v, 'f', 4, 64)
}

func (r *Recorder) writeLoop() {
	defer close(r.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case row, ok := <-r.rows:
			if !ok {
				r.w.Flush()
				return
			}
			r.w.WriteString(row)
		case <-ticker.C:
			r.w.Flush()
		}
	}
}

// Dropped reports how many rows were dropped due to a full buffer.
func (r *Recorder) Dropped() uint64 { return r.dropped.Load() }

// TrackedKeys reports the number of distinct keys with feature state.
func (r *Recorder) TrackedKeys() int { return r.tracker.Len() }

// Features returns the current feature snapshot for key without recording an
// access, for scoring eviction candidates. ok is false if the key is unknown.
func (r *Recorder) Features(key string, now time.Time) (Features, bool) {
	return r.tracker.Peek(key, now)
}

// Close stops the writer, flushes remaining rows, and closes the file. Safe to
// call more than once.
func (r *Recorder) Close() error {
	r.closeMu.Lock()
	if r.closed {
		r.closeMu.Unlock()
		return nil
	}
	r.closed = true
	r.closeMu.Unlock()

	close(r.rows)
	<-r.done
	return r.f.Close()
}
