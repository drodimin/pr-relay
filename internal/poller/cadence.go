package poller

import (
	"math"
	"time"
)

// steps is the quiet-PR ramp relative to a 1-minute base (D-10). A PR quiet
// for less than upTo polls every mult × min. Beyond the last step it polls at
// max. Thresholds are scaled by min/1m, so the curve keeps its shape at any
// configured base.
var steps = []struct {
	upTo time.Duration
	mult float64
}{
	{10 * time.Minute, 1},
	{20 * time.Minute, 1.5},
	{35 * time.Minute, 2},
	{55 * time.Minute, 3},
	{85 * time.Minute, 4},
	{120 * time.Minute, 6},
	{165 * time.Minute, 8},
	{240 * time.Minute, 11},
}

// maxJitter bounds how much jitter may shorten an interval.
const maxJitter = 0.1

// coalesceCap is the longest a near-due PR may be pulled forward into a batch.
const coalesceCap = 15 * time.Second

// BaseInterval returns the unstretched poll interval for a PR that has been
// quiet for quiet, clamped to [min, max]. A negative quiet (clock skew) is
// treated as active.
func BaseInterval(quiet, min, max time.Duration) time.Duration {
	// quiet < upTo × min/1m, compared as quiet×1m < upTo×min so equal
	// thresholds stay exact (no division rounding) and nothing overflows int64.
	for _, s := range steps {
		if float64(quiet)*float64(time.Minute) < float64(s.upTo)*float64(min) {
			return clamp(time.Duration(math.Round(float64(min)*s.mult)), min, max)
		}
	}
	return max
}

// Effective applies the budget stretch and jitter to base. Stretch below 1 is
// ignored. A stretched interval is capped at max(maxStretched, base), so polling
// never fully pauses (D-06). Jitter is clamped to [-10%, 0]: it only shortens
// an interval, never raising it above the ceiling (D-11).
func Effective(base time.Duration, stretch, jitter float64, maxStretched time.Duration) time.Duration {
	j := math.Max(math.Min(jitter, 0), -maxJitter)
	s := math.Max(stretch, 1)
	d := float64(base) * s
	if s > 1 {
		d = math.Min(d, float64(max(maxStretched, base)))
	}
	return time.Duration(math.Round(d * (1 + j)))
}

// NextDue returns when a PR last polled at lastPoll, with activity anchor
// anchor, is next due. A zero lastPoll means the PR has never been polled and
// is due now, signalled by the zero time.
func NextDue(lastPoll, anchor time.Time, min, max, maxStretched time.Duration, stretch, jitter float64) time.Time {
	if lastPoll.IsZero() {
		return time.Time{}
	}
	base := BaseInterval(lastPoll.Sub(anchor), min, max)
	return lastPoll.Add(Effective(base, stretch, jitter, maxStretched))
}

// CoalesceWindow returns how far ahead of its due time a PR with the given
// interval may be pulled into the current batch: min(15s, interval/4).
func CoalesceWindow(interval time.Duration) time.Duration {
	return min(coalesceCap, interval/4)
}

func clamp(d, lo, hi time.Duration) time.Duration {
	return max(lo, min(d, hi))
}
