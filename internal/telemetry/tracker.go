// Package telemetry turns the live query stream into a feature-rich, append-only
// event log that later becomes training data for the adaptive-cache model. It
// computes per-key features online (recency, frequency, burst, time-of-day) and
// writes one row per query off the hot path. The prediction label ("was this key
// requested again within the horizon?") is a future fact, so it is NOT computed
// here; it is derived in an offline labeling pass (see ml/label.py).
package telemetry

import (
	"math"
	"sync"
	"time"
)

// Features is the per-query feature snapshot describing a key's history up to
// (but not counting) the current access. These are exactly the inputs available
// to a predictor at decision time.
type Features struct {
	TimeSinceLastAccess float64 // seconds since this key was last seen (-1 if first)
	AccessCount         float64 // prior accesses of this key
	AgeSeconds          float64 // seconds since this key was first seen
	FreqPerMin          float64 // prior accesses per minute over the key's lifetime
	EWMAIntervalSeconds float64 // smoothed inter-arrival gap
	BurstScore          float64 // decaying access rate over ~burstWindow seconds
	Hour                int     // hour of day [0,23]
	DayOfWeek           int     // 0=Sunday .. 6=Saturday
}

type keyStat struct {
	count     float64
	firstSeen time.Time
	lastSeen  time.Time
	ewma      float64 // smoothed inter-arrival interval, seconds
	burst     float64 // decaying access-rate accumulator
}

// Tracker maintains per-key access statistics. It is safe for concurrent use.
type Tracker struct {
	mu        sync.Mutex
	stats     map[string]*keyStat
	burstTau  float64 // burst decay time constant, seconds
	ewmaAlpha float64 // weight of the newest interval in the EWMA
}

// NewTracker creates a Tracker. burstWindow sets the timescale over which the
// burst score accumulates and decays; ewmaAlpha in (0,1] weights recent
// inter-arrival gaps.
func NewTracker(burstWindow time.Duration, ewmaAlpha float64) *Tracker {
	if burstWindow <= 0 {
		burstWindow = 10 * time.Second
	}
	if ewmaAlpha <= 0 || ewmaAlpha > 1 {
		ewmaAlpha = 0.3
	}
	return &Tracker{
		stats:     make(map[string]*keyStat),
		burstTau:  burstWindow.Seconds(),
		ewmaAlpha: ewmaAlpha,
	}
}

// Observe folds one access of key (at time now) into the tracker and returns the
// feature snapshot as of just before this access. The burst score is reported
// decayed to now and then incremented for this access.
func (t *Tracker) Observe(key string, now time.Time) Features {
	t.mu.Lock()
	defer t.mu.Unlock()

	s, ok := t.stats[key]
	if !ok {
		// First time we've ever seen this key.
		t.stats[key] = &keyStat{
			count:     1,
			firstSeen: now,
			lastSeen:  now,
			ewma:      0,
			burst:     1,
		}
		return Features{
			TimeSinceLastAccess: -1,
			AccessCount:         0,
			AgeSeconds:          0,
			FreqPerMin:          0,
			EWMAIntervalSeconds: 0,
			BurstScore:          0,
			Hour:                now.Hour(),
			DayOfWeek:           int(now.Weekday()),
		}
	}

	dt := now.Sub(s.lastSeen).Seconds()
	if dt < 0 {
		dt = 0 // clock went backwards; clamp
	}
	age := now.Sub(s.firstSeen).Seconds()
	decayedBurst := s.burst * math.Exp(-dt/t.burstTau)

	freqPerMin := 0.0
	if age > 0 {
		freqPerMin = s.count / (age / 60.0)
	}

	f := Features{
		TimeSinceLastAccess: dt,
		AccessCount:         s.count,
		AgeSeconds:          age,
		FreqPerMin:          freqPerMin,
		EWMAIntervalSeconds: s.ewma,
		BurstScore:          decayedBurst,
		Hour:                now.Hour(),
		DayOfWeek:           int(now.Weekday()),
	}

	// Fold in this access.
	if s.ewma == 0 {
		s.ewma = dt
	} else {
		s.ewma = t.ewmaAlpha*dt + (1-t.ewmaAlpha)*s.ewma
	}
	s.burst = decayedBurst + 1
	s.count++
	s.lastSeen = now

	return f
}

// Len reports the number of distinct keys currently tracked.
func (t *Tracker) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.stats)
}
