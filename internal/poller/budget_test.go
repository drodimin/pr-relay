package poller

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func newTestBudget() *Budget {
	return NewBudget(BudgetParams{CapPct: 0.10, LowThreshold: 0.20, MaxBatch: 20})
}

func TestBudgetDefaults(t *testing.T) {
	b := newTestBudget()
	if b.Limit != 5000 || b.Remaining != 5000 || b.CostPerPR != 0.25 || b.BatchSize != 20 {
		t.Fatalf("defaults wrong: %+v", b)
	}
}

func TestBudgetLedger(t *testing.T) {
	b := newTestBudget()
	b.Record(t0, 3)
	if got := b.Spent(t0.Add(59 * time.Minute)); got != 3 {
		t.Fatalf("Spent(+59m) = %d, want 3", got)
	}
	if got := b.Spent(t0.Add(61 * time.Minute)); got != 0 {
		t.Fatalf("Spent(+61m) = %d, want 0", got)
	}
	b.Record(t0.Add(30*time.Minute), 2)
	if got := b.Spent(t0.Add(45 * time.Minute)); got != 5 {
		t.Fatalf("Spent(+45m) = %d, want 5", got)
	}
	// Recording prunes entries older than 60 minutes.
	b.Record(t0.Add(95*time.Minute), 1)
	if len(b.Ledger) != 1 {
		t.Fatalf("ledger not pruned: %+v", b.Ledger)
	}
}

func TestBudgetObserve(t *testing.T) {
	b := newTestBudget()
	reset := t0.Add(30 * time.Minute)
	b.Observe(5000, 4200, reset, 5, 20)
	if b.Limit != 5000 || b.Remaining != 4200 || !b.ResetAt.Equal(reset) {
		t.Fatalf("Observe did not store limits: %+v", b)
	}
	if math.Abs(b.CostPerPR-0.25) > 1e-9 {
		t.Fatalf("CostPerPR = %v, want 0.25", b.CostPerPR)
	}
	b.Observe(5000, 4190, reset, 10, 20) // 0.5 per PR
	if want := 0.3*0.5 + 0.7*0.25; math.Abs(b.CostPerPR-want) > 1e-9 {
		t.Fatalf("CostPerPR EWMA = %v, want %v", b.CostPerPR, want)
	}
	// Zero PRs does not touch the EWMA; zero limit keeps the old limit.
	prev := b.CostPerPR
	b.Observe(0, 4100, reset, 1, 0)
	if b.CostPerPR != prev || b.Limit != 5000 || b.Remaining != 4100 {
		t.Fatalf("degenerate Observe wrong: %+v", b)
	}
}

func TestBudgetCapStretch(t *testing.T) {
	b := newTestBudget()
	if got := b.Stretch(t0, 400); got != 1.0 {
		t.Fatalf("Stretch(400/h) = %v, want 1.0", got)
	}
	if got := b.Stretch(t0, 1000); got != 2.0 {
		t.Fatalf("Stretch(1000/h) = %v, want 2.0", got)
	}
	// 20 PRs at 1m and 0.25 per poll = 300/h.
	bases := make([]time.Duration, 20)
	for i := range bases {
		bases[i] = time.Minute
	}
	if got := b.ProjectedPerHour(bases); math.Abs(got-300) > 1e-9 {
		t.Fatalf("ProjectedPerHour = %v, want 300", got)
	}
}

func TestBudgetLowStretchNeverStops(t *testing.T) {
	b := newTestBudget()
	b.Observe(5000, 500, t0.Add(time.Hour), 1, 1)
	if got := b.Stretch(t0, 0); got != 2.0 {
		t.Fatalf("lowStretch at 500/5000 = %v, want 2.0", got)
	}
	// Stretch is max(capStretch, lowStretch).
	if got := b.Stretch(t0, 2000); got != 4.0 {
		t.Fatalf("max(cap 4, low 2) = %v, want 4.0", got)
	}
	b.Observe(5000, 1, t0.Add(time.Hour), 1, 1)
	s := b.Stretch(t0, 0)
	if math.IsInf(s, 0) || math.IsNaN(s) || s < 100 {
		t.Fatalf("lowStretch at remaining 1 = %v, want large and finite", s)
	}
	b.Observe(5000, 0, t0.Add(time.Hour), 1, 1)
	s = b.Stretch(t0, 0)
	if math.IsInf(s, 0) || math.IsNaN(s) {
		t.Fatalf("lowStretch at remaining 0 = %v, want finite", s)
	}
	// Never a full pause: the stretched interval is capped.
	if got := Effective(16*time.Minute, s, 0, time.Hour); got != time.Hour {
		t.Fatalf("stretched interval = %v, want 1h cap", got)
	}
}

func TestBudgetPrimaryGate(t *testing.T) {
	for _, forced := range []bool{false, true} {
		b := newTestBudget()
		reset := t0.Add(10*time.Minute + 1234567*time.Nanosecond)
		b.OnPrimaryExhausted(reset)
		if b.Remaining != 0 || !b.ResetAt.Equal(reset) {
			t.Fatalf("OnPrimaryExhausted state: %+v", b)
		}
		w := b.Gate(t0, forced)
		if w < reset.Sub(t0) || w > reset.Sub(t0)+5*time.Second {
			t.Fatalf("forced=%v: Gate = %v, want in [%v, +5s]", forced, w, reset.Sub(t0))
		}
		if w2 := b.Gate(t0, forced); w2 != w {
			t.Fatalf("jitter not deterministic: %v vs %v", w, w2)
		}
		// After the reset passes the gate opens.
		if w := b.Gate(reset.Add(time.Second), forced); w != 0 {
			t.Fatalf("forced=%v: Gate after reset = %v, want 0", forced, w)
		}
	}
}

func TestBudgetForcedBypassesCap(t *testing.T) {
	b := newTestBudget()
	b.Record(t0, 300)
	b.Record(t0.Add(10*time.Minute), 250) // cap is 500
	now := t0.Add(20 * time.Minute)
	if w := b.Gate(now, false); w != 40*time.Minute {
		t.Fatalf("cap gate = %v, want 40m (oldest ages out)", w)
	}
	if w := b.Gate(now, true); w != 0 {
		t.Fatalf("forced cap gate = %v, want 0", w)
	}
	// The forced poll is still recorded in the same ledger (D-15).
	b.Record(now, 1)
	if got := b.Spent(now); got != 551 {
		t.Fatalf("Spent after forced = %d, want 551", got)
	}
	// Under the cap the gate is open.
	if w := b.Gate(t0.Add(65*time.Minute), false); w != 0 {
		t.Fatalf("Gate under cap = %v, want 0", w)
	}
}

func TestSecondaryBackoffPure(t *testing.T) {
	b := newTestBudget()
	b.OnSecondary(t0, 30*time.Second)
	if !b.SecondaryUntil.Equal(t0.Add(30*time.Second)) || b.BatchSize != 10 || b.SecondaryStreak != 1 {
		t.Fatalf("first hit: %+v", b)
	}
	for _, forced := range []bool{false, true} {
		if w := b.Gate(t0.Add(10*time.Second), forced); w != 20*time.Second {
			t.Fatalf("forced=%v: Gate during secondary = %v, want 20s", forced, w)
		}
	}
	t2 := t0.Add(31 * time.Second)
	b.OnSecondary(t2, 0)
	if !b.SecondaryUntil.Equal(t2.Add(60*time.Second)) || b.BatchSize != 5 {
		t.Fatalf("second hit: until %v batch %d", b.SecondaryUntil.Sub(t2), b.BatchSize)
	}
	// Further hits double up to 15m.
	want := []time.Duration{2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	now := t2
	for i, w := range want {
		now = now.Add(time.Hour)
		b.OnSecondary(now, 0)
		if got := b.SecondaryUntil.Sub(now); got != w {
			t.Fatalf("hit %d: wait %v, want %v", i+3, got, w)
		}
	}
	if b.BatchSize != 1 {
		t.Fatalf("batch size floor = %d, want 1", b.BatchSize)
	}
	// A Retry-After beyond the cap is honoured exactly.
	now = now.Add(time.Hour)
	b.OnSecondary(now, 20*time.Minute)
	if got := b.SecondaryUntil.Sub(now); got != 20*time.Minute {
		t.Fatalf("Retry-After 20m: wait %v", got)
	}
}

func TestBatchRestore(t *testing.T) {
	b := newTestBudget()
	b.OnSecondary(t0, 0)
	b.OnSecondary(t0.Add(time.Minute), 0)
	if b.BatchSize != 5 || b.SecondaryStreak != 2 {
		t.Fatalf("setup: %+v", b)
	}
	b.OnClean()
	if b.SecondaryStreak != 0 {
		t.Fatalf("streak not reset on first clean batch: %d", b.SecondaryStreak)
	}
	// A new hit after a clean batch starts the backoff over.
	b.OnSecondary(t0.Add(time.Hour), 0)
	if got := b.SecondaryUntil.Sub(t0.Add(time.Hour)); got != 60*time.Second {
		t.Fatalf("backoff not restarted: %v", got)
	}
	if b.BatchSize != 2 {
		t.Fatalf("batch after third hit = %d, want 2", b.BatchSize)
	}
	sizes := []int{}
	for i := 0; i < 20; i++ {
		b.OnClean()
		sizes = append(sizes, b.BatchSize)
	}
	// Every 5 consecutive clean batches double the size toward MaxBatch.
	if sizes[3] != 2 || sizes[4] != 4 || sizes[9] != 8 || sizes[14] != 16 || sizes[19] != 20 {
		t.Fatalf("restore sequence wrong: %v", sizes)
	}
}
