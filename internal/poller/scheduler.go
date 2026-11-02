package poller

import (
	"context"
	"errors"
	"hash/fnv"
	"sort"
	"time"

	"github.com/drodimin/pr-relay/internal/ghclient"
	"github.com/drodimin/pr-relay/internal/ghquery"
)

const (
	// maxSleep caps every scheduler sleep. Due times are wall-clock, so after
	// a laptop sleep overdue PRs go out within 30s even if a monotonic timer
	// lagged (pitfall 11).
	maxSleep = 30 * time.Second
	// authRetry is how often a missing or rejected token is retried (DMN-03).
	authRetry = 5 * time.Minute
)

// errRunning is returned by a second concurrent Run.
var errRunning = errors.New("poller: Run is already running")

// Run is the scheduler loop. It returns nil when ctx is cancelled.
//
// Each iteration recomputes wall-clock time, derives every watched PR's due
// time from its persisted LastPollAt and RateAnchor (the cadence curve,
// stretched by the budget and shortened by a stable per-PR jitter), and once
// any PR is due polls it together with every PR due within its coalesce
// window, in chunks of the budget's batch size, one batch at a time (DMN-02).
// Terminal PRs and PRs without subscriptions are never polled. The budget's
// gate holds batches for the own-spend cap and GitHub's limits (D-05..D-07);
// a missing or rejected token is retried every 5 minutes (DMN-03).
func (e *Engine) Run(ctx context.Context) error {
	if !e.running.CompareAndSwap(false, true) {
		return errRunning
	}
	defer e.running.Store(false)
	for ctx.Err() == nil {
		// Round(0) strips the monotonic reading, so comparisons with
		// persisted times are wall-clock (pitfall 11).
		now := e.now().Round(0)
		e.mu.Lock()
		keys, wait := e.planLocked(now)
		e.mu.Unlock()
		if len(keys) == 0 {
			if !e.sleep(ctx, wait) {
				return nil
			}
			continue
		}
		pause := e.runBatches(ctx, keys)
		if ctx.Err() != nil {
			return nil
		}
		if pause > 0 && !e.sleep(ctx, pause) {
			return nil
		}
	}
	return nil
}

type duePR struct {
	key      string
	due      time.Time
	interval time.Duration
}

// planLocked picks the PR keys to poll now, or how long to wait when none
// are. It also refreshes e.stretch. Callers hold e.mu.
func (e *Engine) planLocked(now time.Time) ([]string, time.Duration) {
	if !e.authRetryAt.IsZero() && now.Before(e.authRetryAt) {
		return nil, e.authRetryAt.Sub(now)
	}
	p := e.cfg.Poll
	minI, maxI, maxS := p.MinInterval.Duration, p.MaxInterval.Duration, p.MaxStretchedInterval.Duration

	// GitHub refills the primary budget at resetAt. Without a new response
	// the last observed Remaining (possibly 0 after an exhausted limit) would
	// keep every interval stretched to the low-budget ceiling.
	if b := e.budget; !b.ResetAt.IsZero() && !now.Before(b.ResetAt) && b.Remaining < b.Limit {
		b.Remaining = b.Limit
	}

	subscribed := map[string]bool{}
	for _, s := range e.state.Subs {
		subscribed[s.PR] = true
	}
	type cand struct {
		key  string
		base time.Duration
	}
	var cands []cand
	var bases []time.Duration
	for k, rec := range e.state.PRs {
		if rec.Terminal || !subscribed[k] {
			continue
		}
		var quiet time.Duration
		if !rec.LastPollAt.IsZero() {
			quiet = rec.LastPollAt.Sub(rec.RateAnchor)
		}
		base := BaseInterval(quiet, minI, maxI)
		cands = append(cands, cand{k, base})
		bases = append(bases, base)
	}
	e.stretch = e.budget.Stretch(now, e.budget.ProjectedPerHour(bases))
	if len(cands) == 0 {
		return nil, maxSleep
	}

	list := make([]duePR, 0, len(cands))
	var earliest time.Time
	for i, c := range cands {
		rec := e.state.PRs[c.key]
		j := jitterFor(c.key)
		d := duePR{key: c.key, interval: Effective(c.base, e.stretch, j, maxS)}
		if next := NextDue(rec.LastPollAt, rec.RateAnchor, minI, maxI, maxS, e.stretch, j); !next.IsZero() {
			d.due = next
		} else {
			d.due = now
		}
		if i == 0 || d.due.Before(earliest) {
			earliest = d.due
		}
		list = append(list, d)
	}
	if earliest.After(now) {
		return nil, earliest.Sub(now)
	}
	if g := e.budget.Gate(now, false); g > 0 {
		return nil, g
	}

	var sel []duePR
	for _, d := range list {
		if !d.due.After(now.Add(CoalesceWindow(d.interval))) {
			sel = append(sel, d)
		}
	}
	sort.Slice(sel, func(i, j int) bool {
		if !sel[i].due.Equal(sel[j].due) {
			return sel[i].due.Before(sel[j].due)
		}
		return sel[i].key < sel[j].key
	})
	keys := make([]string, len(sel))
	for i, d := range sel {
		keys[i] = d.key
	}
	return keys, 0
}

// runBatches polls keys in chunks of the budget's batch size, re-checking
// the gate after each. It returns an extra pause for errors the cadence and
// the gate do not already account for.
func (e *Engine) runBatches(ctx context.Context, keys []string) time.Duration {
	for len(keys) > 0 {
		e.mu.Lock()
		n := min(max(e.budget.BatchSize, 1), ghquery.MaxBatch, len(keys))
		e.mu.Unlock()
		chunk := keys[:n]
		keys = keys[n:]
		_, err := e.pollRefs(ctx, chunk, false)
		if ctx.Err() != nil {
			return 0
		}
		if pause, stop := e.afterBatch(err); stop {
			return pause
		}
		e.mu.Lock()
		g := e.budget.Gate(e.now().Round(0), false)
		e.mu.Unlock()
		if g > 0 {
			return 0 // the next plan sleeps on the gate
		}
	}
	return 0
}

// afterBatch updates the auth state from a batch outcome and reports whether
// the remaining chunks should wait, with an extra pause when nothing else
// will delay the retry.
func (e *Engine) afterBatch(err error) (time.Duration, bool) {
	var rl *RateLimitedError
	var se *ghclient.SecondaryLimitError
	var pe *ghclient.PrimaryExhaustedError
	var te *ghclient.TransientError
	switch {
	case err == nil:
		e.setAuth(AuthOK, time.Time{})
		return 0, false
	case errors.Is(err, ghclient.ErrNoToken):
		e.authFailed(AuthNoToken)
		return 0, true
	case errors.Is(err, ghclient.ErrUnauthorized):
		e.authFailed(AuthUnauthorized)
		return 0, true
	case errors.As(err, &rl), errors.As(err, &se), errors.As(err, &pe):
		// The budget gate holds the next batch.
		return 0, true
	case errors.As(err, &te):
		// batchFailed moved LastPollAt, so the cadence delays the retry.
		return 0, true
	}
	// A commit (Save) failure or another error leaves the PRs due; pause so
	// the loop does not spin (T-02-37).
	e.log.Warn("scheduler pausing after poll error", "error", errClass(err), "pause", maxSleep)
	return maxSleep, true
}

func (e *Engine) authFailed(state string) {
	if inv, ok := e.fetch.(interface{ Invalidate() }); ok {
		inv.Invalidate()
	}
	e.setAuth(state, e.now().Round(0).Add(authRetry))
}

// setAuth records the auth state and the next auth retry time (zero when
// auth is fine).
func (e *Engine) setAuth(state string, retryAt time.Time) {
	src := ""
	if ts, ok := e.fetch.(interface{ TokenSource() string }); ok {
		src = ts.TokenSource()
	}
	e.mu.Lock()
	changed := e.auth.State != state
	if changed {
		e.auth.State = state
		e.auth.Since = e.now()
	}
	if state == AuthOK || src != "" {
		e.auth.Source = src
	}
	e.authRetryAt = retryAt
	e.mu.Unlock()
	if changed {
		if state == AuthOK {
			e.log.Info("github auth ok", "token_source", src)
		} else {
			e.log.Warn("github auth failed", "state", state, "retry_in", authRetry)
		}
	}
}

// sleep waits for d (capped at maxSleep), a wake or ctx. It reports false
// when ctx ended.
func (e *Engine) sleep(ctx context.Context, d time.Duration) bool {
	d = min(max(d, 0), maxSleep)
	if d == 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-e.wake:
	case <-t.C:
	}
	return true
}

// jitterFor returns the PR's stable jitter in [-0.1, 0], derived from a hash
// of its key so it never changes between polls or restarts (D-11).
func jitterFor(key string) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return -maxJitter * float64(h.Sum64()%1001) / 1000
}
