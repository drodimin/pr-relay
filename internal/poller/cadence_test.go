package poller

import (
	"testing"
	"time"
)

const (
	tMin = time.Minute
	tMax = 16 * time.Minute
)

func TestCadenceTable(t *testing.T) {
	cases := []struct {
		quiet, want time.Duration
	}{
		{0, time.Minute},
		{9*time.Minute + 59*time.Second, time.Minute},
		{10 * time.Minute, 90 * time.Second},
		{20 * time.Minute, 2 * time.Minute},
		{35 * time.Minute, 3 * time.Minute},
		{55 * time.Minute, 4 * time.Minute},
		{85 * time.Minute, 6 * time.Minute},
		{120 * time.Minute, 8 * time.Minute},
		{165 * time.Minute, 11 * time.Minute},
		{240 * time.Minute, 16 * time.Minute},
		{24 * time.Hour, 16 * time.Minute},
		{-time.Minute, time.Minute}, // clock skew: treat as active
	}
	for _, c := range cases {
		if got := BaseInterval(c.quiet, tMin, tMax); got != c.want {
			t.Errorf("BaseInterval(%v) = %v, want %v", c.quiet, got, c.want)
		}
	}
}

// TestQuietRampPure simulates a PR that never sees activity: each poll's next
// due time is derived only from persisted LastPollAt and RateAnchor, so a
// restart at any point resumes the same position on the curve.
func TestQuietRampPure(t *testing.T) {
	anchor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := anchor.Add(4 * time.Hour)
	last := anchor
	polls := 0
	for last.Before(end) {
		polls++
		last = last.Add(BaseInterval(last.Sub(anchor), tMin, tMax))
	}
	if polls < 55 || polls > 59 {
		t.Fatalf("polls in first 4h = %d, want 55..59", polls)
	}
	if got := BaseInterval(last.Sub(anchor), tMin, tMax); got != tMax {
		t.Fatalf("interval after 4h = %v, want %v", got, tMax)
	}
	// Restart stability: recomputing from the persisted pair gives the same due time.
	a := NextDue(last, anchor, tMin, tMax, time.Hour, 1, 0)
	b := NextDue(last, anchor, tMin, tMax, time.Hour, 1, 0)
	if !a.Equal(b) || !a.Equal(last.Add(tMax)) {
		t.Fatalf("NextDue not stable: %v %v", a, b)
	}
}

func TestCadenceScaled(t *testing.T) {
	min, max := 100*time.Millisecond, 200*time.Millisecond
	for q := time.Duration(0); q < 10*time.Second; q += 10 * time.Millisecond {
		got := BaseInterval(q, min, max)
		if got < min || got > max {
			t.Fatalf("BaseInterval(%v) = %v outside [%v,%v]", q, got, min, max)
		}
	}
	// Thresholds scale with min: 10m becomes 1s at min=100ms.
	if got := BaseInterval(999*time.Millisecond, min, max); got != min {
		t.Errorf("scaled step 1 = %v, want %v", got, min)
	}
	if got := BaseInterval(time.Second, min, max); got != 150*time.Millisecond {
		t.Errorf("scaled step 2 = %v, want 150ms", got)
	}
	if got := BaseInterval(24*time.Second, min, max); got != max {
		t.Errorf("scaled ceiling = %v, want %v", got, max)
	}
}

func TestEffective(t *testing.T) {
	cases := []struct {
		base         time.Duration
		stretch, jit float64
		maxStretched time.Duration
		want         time.Duration
	}{
		{16 * time.Minute, 1.0, -0.1, time.Hour, 14*time.Minute + 24*time.Second},
		{16 * time.Minute, 1.0, 0.5, time.Hour, 16 * time.Minute},
		{16 * time.Minute, 3.0, 0, time.Hour, 48 * time.Minute},
		{16 * time.Minute, 10, 0, time.Hour, time.Hour},
		{time.Minute, 1, 0, time.Hour, time.Minute},
		{time.Minute, 0.2, 0, time.Hour, time.Minute},             // stretch < 1 is ignored
		{16 * time.Minute, 1, -0.9, time.Hour, 864 * time.Second}, // jitter clamped to -10%
		{2 * time.Hour, 2, 0, time.Hour, 2 * time.Hour},           // cap never below base
	}
	for _, c := range cases {
		if got := Effective(c.base, c.stretch, c.jit, c.maxStretched); got != c.want {
			t.Errorf("Effective(%v,%v,%v,%v) = %v, want %v", c.base, c.stretch, c.jit, c.maxStretched, got, c.want)
		}
	}
	// Without stretch, Effective never exceeds the 16m ceiling.
	for j := -0.1; j <= 0.1; j += 0.01 {
		if got := Effective(tMax, 1, j, time.Hour); got > tMax {
			t.Fatalf("jitter %v raised interval to %v", j, got)
		}
	}
}

func TestNextDue(t *testing.T) {
	if got := NextDue(time.Time{}, time.Time{}, tMin, tMax, time.Hour, 1, 0); !got.IsZero() {
		t.Fatalf("zero lastPoll: got %v, want zero (due now)", got)
	}
	anchor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	last := anchor.Add(30 * time.Minute)
	if got, want := NextDue(last, anchor, tMin, tMax, time.Hour, 1, 0), last.Add(2*time.Minute); !got.Equal(want) {
		t.Fatalf("NextDue = %v, want %v", got, want)
	}
	if got, want := NextDue(last, anchor, tMin, tMax, time.Hour, 2, 0), last.Add(4*time.Minute); !got.Equal(want) {
		t.Fatalf("stretched NextDue = %v, want %v", got, want)
	}
}

func TestCoalesceWindow(t *testing.T) {
	cases := map[time.Duration]time.Duration{
		time.Minute:            15 * time.Second,
		16 * time.Minute:       15 * time.Second,
		100 * time.Millisecond: 25 * time.Millisecond,
	}
	for in, want := range cases {
		if got := CoalesceWindow(in); got != want {
			t.Errorf("CoalesceWindow(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestNextDueBase(t *testing.T) {
	if got := NextDueBase(time.Time{}, tMax, time.Hour, 1, 0); !got.IsZero() {
		t.Fatalf("zero lastPoll: got %v, want zero (due now)", got)
	}
	last := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got, want := NextDueBase(last, tMax, time.Hour, 1, 0), last.Add(16*time.Minute); !got.Equal(want) {
		t.Fatalf("NextDueBase = %v, want %v", got, want)
	}
	for _, c := range []struct{ stretch, jit float64 }{{1, -0.1}, {3, 0}, {10, -0.05}} {
		got := NextDueBase(last, tMax, time.Hour, c.stretch, c.jit)
		if want := last.Add(Effective(tMax, c.stretch, c.jit, time.Hour)); !got.Equal(want) {
			t.Errorf("NextDueBase(stretch %v, jitter %v) = %v, want %v", c.stretch, c.jit, got, want)
		}
	}
}
