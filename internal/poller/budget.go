package poller

import (
	"math"
	"time"
)

const (
	// defaultLimit is the GraphQL hourly point limit assumed until observed.
	defaultLimit = 5000
	// initialCostPerPR seeds the per-PR cost EWMA before any batch returns.
	initialCostPerPR = 0.25
	// costAlpha is the EWMA weight of the newest per-PR cost sample.
	costAlpha = 0.3
	// ledgerWindow is how long a recorded spend counts toward the cap.
	ledgerWindow = 60 * time.Minute
	// primaryJitterMax bounds the extra wait after a primary reset.
	primaryJitterMax = 5 * time.Second

	secondaryDefault      = 60 * time.Second
	secondaryCap          = 15 * time.Minute
	cleanBatchesToRestore = 5
)

// BudgetParams are the tunable budget knobs.
type BudgetParams struct {
	// CapPct is the share of the hourly limit pr-relay may spend (D-05).
	CapPct float64
	// LowThreshold is the share of the limit below which every interval
	// stretches in proportion (D-06).
	LowThreshold float64
	// MaxBatch is the largest number of PRs per batch query.
	MaxBatch int
}

type spend struct {
	At   time.Time
	Cost int
}

// Budget tracks pr-relay's own spend and GitHub's reported rate-limit state.
// Every method takes the current time explicitly; Budget never reads a clock.
type Budget struct {
	P BudgetParams
	// Ledger holds own spend per request in the last 60 minutes, forced
	// polls included (D-15).
	Ledger           []spend
	Limit, Remaining int
	ResetAt          time.Time
	// CostPerPR is an EWMA of query cost divided by PRs in the batch.
	CostPerPR       float64
	SecondaryUntil  time.Time
	SecondaryStreak int
	BatchSize       int

	lastSecondaryWait time.Duration
	cleanRun          int
}

// NewBudget returns a Budget assuming the default 5000-point limit until a
// response reports otherwise.
func NewBudget(p BudgetParams) *Budget {
	if p.MaxBatch < 1 {
		p.MaxBatch = 1
	}
	return &Budget{
		P:         p,
		Limit:     defaultLimit,
		Remaining: defaultLimit,
		CostPerPR: initialCostPerPR,
		BatchSize: p.MaxBatch,
	}
}

// Record adds a request's cost to the ledger and prunes expired entries.
func (b *Budget) Record(now time.Time, cost int) {
	b.prune(now)
	b.Ledger = append(b.Ledger, spend{At: now, Cost: cost})
}

// Spent returns own spend within the 60 minutes before now.
func (b *Budget) Spent(now time.Time) int {
	total := 0
	for _, s := range b.Ledger {
		if live(s, now) {
			total += s.Cost
		}
	}
	return total
}

// Observe stores the rate-limit state from a response and folds the batch's
// per-PR cost into the EWMA. A non-positive limit keeps the previous limit; a
// batch with no PRs leaves the EWMA unchanged.
func (b *Budget) Observe(limit, remaining int, resetAt time.Time, cost, prs int) {
	if limit > 0 {
		b.Limit = limit
	}
	b.Remaining = max(remaining, 0)
	b.ResetAt = resetAt
	if prs > 0 {
		sample := float64(cost) / float64(prs)
		b.CostPerPR = costAlpha*sample + (1-costAlpha)*b.CostPerPR
	}
}

// ProjectedPerHour estimates own hourly spend if every PR polled at its base
// interval: Σ 3600/seconds(base) × CostPerPR.
func (b *Budget) ProjectedPerHour(baseIntervals []time.Duration) float64 {
	total := 0.0
	for _, d := range baseIntervals {
		if d <= 0 {
			continue
		}
		total += 3600 / d.Seconds() * b.CostPerPR
	}
	return total
}

// Stretch returns the factor every interval is multiplied by:
// max(capStretch, lowStretch). capStretch keeps projected spend within
// CapPct of the limit (D-05). lowStretch grows as Remaining falls below
// LowThreshold of the limit (D-06); it is always finite, and Effective caps
// stretched intervals, so polling never stops.
func (b *Budget) Stretch(now time.Time, projected float64) float64 {
	capStretch := 1.0
	if c := b.capPoints(); c > 0 {
		capStretch = math.Max(1, projected/c)
	}
	lowStretch := 1.0
	low := b.P.LowThreshold * float64(b.Limit)
	if float64(b.Remaining) < low {
		lowStretch = math.Max(1, low/float64(max(b.Remaining, 1)))
	}
	return math.Max(capStretch, lowStretch)
}

// Gate returns how long to wait before sending a batch, 0 meaning send now.
// Order: an active secondary limit and an exhausted primary limit hold every
// batch, forced or not, because GitHub enforces them. The own-spend cap holds
// only non-forced batches (D-15); forced spend is still recorded.
func (b *Budget) Gate(now time.Time, forced bool) time.Duration {
	if now.Before(b.SecondaryUntil) {
		return b.SecondaryUntil.Sub(now)
	}
	if b.Remaining == 0 && now.Before(b.ResetAt) {
		return b.ResetAt.Sub(now) + resetJitter(b.ResetAt)
	}
	if forced {
		return 0
	}
	c := b.capPoints()
	if c <= 0 {
		return 0
	}
	spent := b.Spent(now)
	if float64(spent) < c {
		return 0
	}
	// Wait until enough of the oldest entries age out to drop below the cap.
	for _, s := range b.Ledger {
		if !live(s, now) {
			continue
		}
		spent -= s.Cost
		if float64(spent) < c {
			return s.At.Add(ledgerWindow).Sub(now)
		}
	}
	return 0
}

// OnSecondary handles a secondary rate-limit hit. The first hit of a streak
// waits Retry-After exactly (60s when absent). Each consecutive hit doubles
// the previous wait up to 15m, but a longer Retry-After is always honoured.
// Every hit halves the batch size, never below 1 (D-07).
func (b *Budget) OnSecondary(now time.Time, retryAfter time.Duration) {
	wait := retryAfter
	if wait <= 0 {
		wait = secondaryDefault
	}
	if b.SecondaryStreak > 0 {
		wait = max(wait, min(2*b.lastSecondaryWait, secondaryCap))
	}
	b.SecondaryStreak++
	b.lastSecondaryWait = wait
	b.SecondaryUntil = now.Add(wait)
	b.BatchSize = max(1, b.BatchSize/2)
	b.cleanRun = 0
}

// OnPrimaryExhausted records that the primary limit is spent until resetAt.
func (b *Budget) OnPrimaryExhausted(resetAt time.Time) {
	b.Remaining = 0
	b.ResetAt = resetAt
}

// OnClean records a batch that hit no rate limit. The first clean batch ends
// the secondary streak; every 5 consecutive clean batches double the batch
// size toward MaxBatch.
func (b *Budget) OnClean() {
	b.SecondaryStreak = 0
	b.lastSecondaryWait = 0
	if b.BatchSize >= b.P.MaxBatch {
		b.cleanRun = 0
		return
	}
	b.cleanRun++
	if b.cleanRun >= cleanBatchesToRestore {
		b.BatchSize = min(b.P.MaxBatch, b.BatchSize*2)
		b.cleanRun = 0
	}
}

func (b *Budget) capPoints() float64 {
	return b.P.CapPct * float64(b.Limit)
}

func (b *Budget) prune(now time.Time) {
	kept := b.Ledger[:0]
	for _, s := range b.Ledger {
		if live(s, now) {
			kept = append(kept, s)
		}
	}
	b.Ledger = kept
}

func live(s spend, now time.Time) bool {
	return now.Sub(s.At) < ledgerWindow
}

// resetJitter spreads retries after a reset by up to 5s, derived from the
// reset time's nanoseconds so the result is deterministic for a given state.
// GitHub reports resetAt at whole seconds, so the full UnixNano is mixed with
// a multiplicative hash rather than reading Nanosecond() alone.
func resetJitter(resetAt time.Time) time.Duration {
	h := uint64(resetAt.UnixNano()) * 0x9E3779B97F4A7C15
	return time.Duration(h % uint64(primaryJitterMax))
}
