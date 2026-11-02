package poller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/drodimin/pr-relay/internal/config"
	"github.com/drodimin/pr-relay/internal/diff"
	"github.com/drodimin/pr-relay/internal/ghclient"
	"github.com/drodimin/pr-relay/internal/ghquery"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/store"
)

// Fetcher fetches one batch of PRs. *ghclient.Client implements it.
type Fetcher interface {
	Batch(ctx context.Context, refs []model.PRRef, w ghquery.Window) (ghclient.BatchResult, error)
}

// Saver persists a whole State atomically. *store.Store implements it.
type Saver interface {
	Save(*store.State) error
}

// Options configures New. Saver, State and Fetcher are required. Logger
// defaults to slog.Default and Now to time.Now.
type Options struct {
	Saver   Saver
	State   *store.State
	Fetcher Fetcher
	Config  config.Config
	Logger  *slog.Logger
	// OnCommit, when set, is called after every committed poll, outside
	// the engine's locks.
	OnCommit func(CommitReport)
	// Now is the clock. Tests inject a fake one.
	Now func() time.Time
}

// SubRequest is one subscribe call. An empty Events mask means every type.
type SubRequest struct {
	PR           model.PRRef
	HerdrSocket  string
	PaneID       string
	AgentSession string
	TerminalID   string
	Events       []model.EventType
}

// PaneRef names one subscribed herdr pane.
type PaneRef struct {
	HerdrSocket string
	PaneID      string
}

// SubInfo is what Subscribe returns (D-16).
type SubInfo struct {
	SubID string
	// Created is false when an existing subscription was updated (D-18).
	Created bool
	// CoSubscribers are the other panes subscribed to the same PR.
	CoSubscribers []PaneRef
	Interval      time.Duration
	NextPollAt    time.Time
}

// Drain is a copy of one subscription's pending queue. UpToSeq is the value
// to Ack to clear everything in it, the drop marker included.
type Drain struct {
	Events  []store.PendingEvent
	Dropped int
	UpToSeq uint64
}

// PollResult is the outcome for one PR of a poll.
type PollResult struct {
	Ref model.PRRef
	// NewEvents counts events delivered at PR level (after ExcludeSelf,
	// before subscriber masks).
	NewEvents int
	Err       error
	// Cost is the GraphQL cost of the whole batch, gap re-fetch included.
	Cost int
}

// CommitReport describes one committed poll, for the scheduler and logs.
type CommitReport struct {
	At                     time.Time
	Forced                 bool
	Cost, Remaining, Limit int
	ResetAt                time.Time
	PRs                    []PRReport
}

// PRReport is the per-PR part of a CommitReport.
type PRReport struct {
	Key        string
	Ref        model.PRRef
	NewEvents  int
	Activity   bool
	Interval   time.Duration
	NextPollAt time.Time
	Err        string
}

// ErrNotWatched means no PR record exists for the ref.
var ErrNotWatched = errors.New("poller: pull request is not watched")

// ErrUnknownSub means no subscription has the given ID.
var ErrUnknownSub = errors.New("poller: unknown subscription")

// errStale marks a result dropped because its PR record was removed or
// recreated while the batch was in flight (pitfall 8).
var errStale = errors.New("poller: pull request record changed during poll")

// RateLimitedError means GitHub's limits (or, for routine polls, the own
// spend cap) hold the poll until Until.
type RateLimitedError struct {
	Until time.Time
}

func (e *RateLimitedError) Error() string {
	return "poller: rate limited until " + e.Until.UTC().Format(time.RFC3339)
}

// Engine owns the subscription registry and the per-poll commit pipeline.
//
// Every change goes through clone, mutate, Save, swap: the live state is
// cloned, the clone is changed and saved, and only a successful Save makes it
// the live state. A poll's new Memory and the pending events derived from it
// therefore reach disk in one rename, and a failed Save leaves the engine
// exactly where it was, so the next poll re-derives the same events once
// (D-03, EVT-09). The only in-memory-only fields are LastPollAt and
// LastError after a batch-level failure or an unchanged poll (pitfall 6).
//
// Polling iterates PR records, never subscriptions: a PR is fetched once per
// poll however many panes watch it (DMN-02). Polls run one at a time
// (pollMu); the network call holds no lock, so Subscribe and friends never
// wait on GitHub. A result whose record Gen changed during the call is
// dropped (pitfall 8).
//
// Activity (D-12, assumption A6) is PR-level and ignores subscriber masks:
// any event surviving ExcludeSelf, or any raw commits event including the
// user's own pushes, moves RateAnchor and LastActivity to the poll time.
//
// Subscribe returns once the subscription is persisted and before any fetch
// (RESEARCH open question 1). A new PR record is due immediately and the
// scheduler is woken; Phase 4's SUB-07 calls PollNow with a timeout when the
// caller wants the first result synchronously.
type Engine struct {
	mu     sync.Mutex
	pollMu sync.Mutex

	state    *store.State
	saver    Saver
	fetch    Fetcher
	cfg      config.Config
	log      *slog.Logger
	budget   *Budget
	stretch  float64
	wake     chan struct{}
	onCommit func(CommitReport)
	now      func() time.Time
}

// New returns an Engine over o.State. The engine owns o.State from now on.
func New(o Options) (*Engine, error) {
	switch {
	case o.Saver == nil:
		return nil, errors.New("poller: New: nil Saver")
	case o.State == nil:
		return nil, errors.New("poller: New: nil State")
	case o.Fetcher == nil:
		return nil, errors.New("poller: New: nil Fetcher")
	}
	if o.State.PRs == nil {
		o.State.PRs = map[string]*store.PRRecord{}
	}
	if o.State.Subs == nil {
		o.State.Subs = map[string]*store.Subscription{}
	}
	e := &Engine{
		state:    o.State,
		saver:    o.Saver,
		fetch:    o.Fetcher,
		cfg:      o.Config,
		log:      o.Logger,
		stretch:  1,
		wake:     make(chan struct{}, 1),
		onCommit: o.OnCommit,
		now:      o.Now,
	}
	if e.log == nil {
		e.log = slog.Default()
	}
	if e.now == nil {
		e.now = time.Now
	}
	e.budget = NewBudget(BudgetParams{
		CapPct:       o.Config.Poll.BudgetPct,
		LowThreshold: o.Config.Poll.LowBudgetThreshold,
		MaxBatch:     o.Config.Poll.BatchSize,
	})
	return e, nil
}

// Wake returns a channel that receives a value whenever something became
// due outside the normal cadence (a new PR, ResetRate).
func (e *Engine) Wake() <-chan struct{} { return e.wake }

func (e *Engine) poke() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// mutate applies fn to a clone of the live state, saves the clone and only
// then swaps it in. Any error leaves the live state untouched. Callers must
// not hold e.mu.
func (e *Engine) mutate(fn func(next *store.State) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.mutateLocked(fn)
}

func (e *Engine) mutateLocked(fn func(next *store.State) error) error {
	next := e.state.Clone()
	if err := fn(next); err != nil {
		return err
	}
	if err := e.saver.Save(next); err != nil {
		return err
	}
	e.state = next
	return nil
}

func normalizeMask(events []model.EventType) ([]model.EventType, error) {
	if len(events) == 0 {
		return nil, nil
	}
	known := map[model.EventType]bool{}
	for _, t := range model.AllEventTypes() {
		known[t] = true
	}
	seen := map[model.EventType]bool{}
	var out []model.EventType
	for _, t := range events {
		if !known[t] {
			return nil, fmt.Errorf("poller: unknown event type %q", t)
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// Subscribe creates or updates the subscription of (HerdrSocket, PaneID,
// PR). A new subscription to an unwatched PR creates its record with an
// un-baselined Memory, due now (SUB-08, D-13). A new subscription to a
// watched PR joins its Memory and cadence without a fetch and without moving
// RateAnchor (D-13, D-17). An existing subscription has its mask replaced,
// its pending queue re-filtered and its clock reset; the baseline is kept
// (SUB-04, D-18).
func (e *Engine) Subscribe(req SubRequest) (SubInfo, error) {
	if !req.PR.Valid() {
		return SubInfo{}, fmt.Errorf("poller: invalid pull request reference %q", req.PR.String())
	}
	mask, err := normalizeMask(req.Events)
	if err != nil {
		return SubInfo{}, err
	}
	key := store.PRKey(req.PR)
	id := store.SubID(req.HerdrSocket, req.PaneID, key)

	e.mu.Lock()
	now := e.now()
	created := false
	newRecord := false
	err = e.mutateLocked(func(next *store.State) error {
		rec := next.PRs[key]
		if rec == nil {
			next.NextGen++
			rec = &store.PRRecord{
				Ref:        req.PR,
				Gen:        next.NextGen,
				CreatedAt:  now,
				RateAnchor: now,
			}
			next.PRs[key] = rec
			newRecord = true
		}
		if sub := next.Subs[id]; sub != nil {
			sub.Events = mask
			sub.AgentSession = req.AgentSession
			sub.TerminalID = req.TerminalID
			sub.UpdatedAt = now
			sub.ClockAt = now
			sub.Pending = refilter(sub.Pending, mask)
			return nil
		}
		created = true
		next.Subs[id] = &store.Subscription{
			ID:           id,
			PR:           key,
			HerdrSocket:  req.HerdrSocket,
			PaneID:       req.PaneID,
			AgentSession: req.AgentSession,
			TerminalID:   req.TerminalID,
			Events:       mask,
			CreatedAt:    now,
			UpdatedAt:    now,
			ClockAt:      now,
		}
		return nil
	})
	if err != nil {
		e.mu.Unlock()
		return SubInfo{}, err
	}
	info := SubInfo{SubID: id, Created: created}
	for _, p := range e.panesLocked(key) {
		if p.HerdrSocket == req.HerdrSocket && p.PaneID == req.PaneID {
			continue
		}
		info.CoSubscribers = append(info.CoSubscribers, p)
	}
	info.Interval, info.NextPollAt = e.scheduleLocked(e.state.PRs[key], now)
	e.mu.Unlock()
	if newRecord {
		e.poke()
	}
	return info, nil
}

// Unsubscribe removes a subscription. The PR record goes with the last one.
func (e *Engine) Unsubscribe(subID string) error {
	return e.mutate(func(next *store.State) error {
		sub := next.Subs[subID]
		if sub == nil {
			return ErrUnknownSub
		}
		delete(next.Subs, subID)
		for _, s := range next.Subs {
			if s.PR == sub.PR {
				return nil
			}
		}
		delete(next.PRs, sub.PR)
		return nil
	})
}

// Keep resets only the subscription's ClockAt (D-14).
func (e *Engine) Keep(subID string) error {
	return e.mutate(func(next *store.State) error {
		sub := next.Subs[subID]
		if sub == nil {
			return ErrUnknownSub
		}
		sub.ClockAt = e.now()
		return nil
	})
}

// ResetRate moves the PR's RateAnchor to now and nothing else (D-15).
func (e *Engine) ResetRate(ref model.PRRef) error {
	err := e.resetAnchor(ref)
	if err == nil {
		e.poke()
	}
	return err
}

func (e *Engine) resetAnchor(ref model.PRRef) error {
	key := store.PRKey(ref)
	return e.mutate(func(next *store.State) error {
		rec := next.PRs[key]
		if rec == nil {
			return ErrNotWatched
		}
		rec.RateAnchor = e.now()
		return nil
	})
}

func copyPending(p []store.PendingEvent) []store.PendingEvent {
	if p == nil {
		return nil
	}
	out := make([]store.PendingEvent, len(p))
	for i, x := range p {
		if x.Event.FailedNames != nil {
			x.Event.FailedNames = append([]string(nil), x.Event.FailedNames...)
		}
		if x.Event.Authors != nil {
			x.Event.Authors = append([]model.Actor(nil), x.Event.Authors...)
		}
		out[i] = x
	}
	return out
}

func copySub(s *store.Subscription) store.Subscription {
	c := *s
	if s.Events != nil {
		c.Events = append([]model.EventType(nil), s.Events...)
	}
	c.Pending = copyPending(s.Pending)
	return c
}

// Pending returns a copy of the subscription's pending queue.
func (e *Engine) Pending(subID string) (Drain, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	sub := e.state.Subs[subID]
	if sub == nil {
		return Drain{}, ErrUnknownSub
	}
	d := Drain{Events: copyPending(sub.Pending), Dropped: sub.Dropped, UpToSeq: sub.DroppedSeq}
	for _, p := range sub.Pending {
		d.UpToSeq = max(d.UpToSeq, p.Seq)
	}
	return d, nil
}

// Ack drops pending events with Seq <= upToSeq and clears the drop marker
// when upToSeq covers it.
func (e *Engine) Ack(subID string, upToSeq uint64) error {
	return e.mutate(func(next *store.State) error {
		sub := next.Subs[subID]
		if sub == nil {
			return ErrUnknownSub
		}
		var kept []store.PendingEvent
		for _, p := range sub.Pending {
			if p.Seq > upToSeq {
				kept = append(kept, p)
			}
		}
		sub.Pending = kept
		if sub.DroppedSeq != 0 && sub.DroppedSeq <= upToSeq {
			sub.Dropped, sub.DroppedSeq = 0, 0
		}
		return nil
	})
}

// panesLocked lists every pane subscribed to key, sorted.
func (e *Engine) panesLocked(key string) []PaneRef {
	var out []PaneRef
	for _, s := range e.state.Subs {
		if s.PR == key {
			out = append(out, PaneRef{HerdrSocket: s.HerdrSocket, PaneID: s.PaneID})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].HerdrSocket != out[j].HerdrSocket {
			return out[i].HerdrSocket < out[j].HerdrSocket
		}
		return out[i].PaneID < out[j].PaneID
	})
	return out
}

// CoSubscribers lists every pane subscribed to ref.
func (e *Engine) CoSubscribers(ref model.PRRef) []PaneRef {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.panesLocked(store.PRKey(ref))
}

// Schedule returns the PR's current interval and next poll time. A PR never
// polled is due now. An unwatched PR returns zero values.
func (e *Engine) Schedule(ref model.PRRef) (time.Duration, time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.scheduleLocked(e.state.PRs[store.PRKey(ref)], e.now())
}

func (e *Engine) scheduleLocked(rec *store.PRRecord, now time.Time) (time.Duration, time.Time) {
	if rec == nil {
		return 0, time.Time{}
	}
	p := e.cfg.Poll
	base := BaseInterval(now.Sub(rec.RateAnchor), p.MinInterval.Duration, p.MaxInterval.Duration)
	interval := Effective(base, e.stretch, 0, p.MaxStretchedInterval.Duration)
	if rec.LastPollAt.IsZero() {
		return interval, now
	}
	next := NextDue(rec.LastPollAt, rec.RateAnchor, p.MinInterval.Duration, p.MaxInterval.Duration,
		p.MaxStretchedInterval.Duration, e.stretch, 0)
	return interval, next
}

// List returns copies of every subscription, sorted by ID.
func (e *Engine) List() []store.Subscription {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]store.Subscription, 0, len(e.state.Subs))
	for _, s := range e.state.Subs {
		out = append(out, copySub(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// PollNow polls one watched PR immediately (D-15). It sets RateAnchor to
// now, bypasses the own-spend cap (the spend is still recorded) but never
// GitHub's own limits, which come back as *RateLimitedError.
func (e *Engine) PollNow(ctx context.Context, ref model.PRRef) (PollResult, error) {
	if !ref.Valid() {
		return PollResult{Ref: ref}, ErrNotWatched
	}
	if err := e.resetAnchor(ref); err != nil {
		return PollResult{Ref: ref}, err
	}
	key := store.PRKey(ref)
	res, err := e.pollRefs(ctx, []string{key}, true)
	if err != nil {
		return PollResult{Ref: ref}, err
	}
	for _, r := range res {
		if store.PRKey(r.Ref) == key {
			return r, nil
		}
	}
	return PollResult{Ref: ref}, ErrNotWatched
}

type target struct {
	key string
	ref model.PRRef
	gen uint64
	mem model.Memory
}

type stepped struct {
	target
	item    ghquery.AliasResult
	res     diff.Result
	gapLost bool
}

// errClass is the LastError value for a per-PR or batch failure.
func errClass(err error) string {
	var se *ghclient.SecondaryLimitError
	var pe *ghclient.PrimaryExhaustedError
	var te *ghclient.TransientError
	switch {
	case errors.Is(err, ghquery.ErrNotFound):
		return "not_found"
	case errors.Is(err, ghquery.ErrForbidden), errors.Is(err, ghclient.ErrForbidden):
		return "forbidden"
	case errors.Is(err, ghclient.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, ghclient.ErrNoToken):
		return "no_token"
	case errors.As(err, &se):
		return "secondary_limit"
	case errors.As(err, &pe):
		return "rate_limited"
	case errors.As(err, &te):
		return "transient"
	}
	return "error"
}

// pollRefs fetches the PR records named by keys in one batch, steps each
// through diff, re-fetches gapped PRs with the wide window and commits the
// result in one Save. Polls run one at a time. keys must fit one batch
// (ghquery.MaxBatch); unknown keys are skipped.
func (e *Engine) pollRefs(ctx context.Context, keys []string, forced bool) ([]PollResult, error) {
	e.pollMu.Lock()
	defer e.pollMu.Unlock()
	if len(keys) > ghquery.MaxBatch {
		return nil, fmt.Errorf("poller: %d pull requests exceed one batch of %d", len(keys), ghquery.MaxBatch)
	}

	e.mu.Lock()
	now := e.now()
	var targets []target
	seen := map[string]bool{}
	for _, k := range keys {
		rec := e.state.PRs[k]
		if rec == nil || seen[k] {
			continue
		}
		seen[k] = true
		// Memory is never mutated in place (copy-on-write), so it can be
		// read after the lock is released.
		targets = append(targets, target{key: k, ref: rec.Ref, gen: rec.Gen, mem: rec.Memory})
	}
	if len(targets) == 0 {
		e.mu.Unlock()
		return nil, nil
	}
	if w := e.budget.Gate(now, forced); w > 0 {
		e.mu.Unlock()
		return nil, &RateLimitedError{Until: now.Add(w)}
	}
	e.mu.Unlock()

	refs := make([]model.PRRef, len(targets))
	for i, t := range targets {
		refs[i] = t.ref
	}
	start := e.now()
	res, err := e.fetch.Batch(ctx, refs, ghquery.NormalWindow)
	if err != nil {
		e.batchFailed(ctx, targets, now, err)
		return nil, err
	}
	cost := res.RateLimit.Cost
	e.mu.Lock()
	e.budget.Record(now, res.RateLimit.Cost)
	e.budget.Observe(res.RateLimit.Limit, res.RateLimit.Remaining, res.ResetAt, res.RateLimit.Cost, len(refs))
	e.budget.OnClean()
	e.mu.Unlock()

	steps := make([]stepped, len(targets))
	var gapped []int
	for i, t := range targets {
		steps[i] = stepped{target: t, item: itemFor(res.Items, i, t.ref)}
		if steps[i].item.Err == nil {
			steps[i].res = diff.Step(t.mem, steps[i].item.Snapshot)
			if steps[i].res.Gap {
				gapped = append(gapped, i)
			}
		}
	}
	nodeCount := res.RateLimit.NodeCount
	if len(gapped) > 0 {
		wref := make([]model.PRRef, len(gapped))
		for j, i := range gapped {
			wref[j] = steps[i].ref
		}
		wres, werr := e.fetch.Batch(ctx, wref, ghquery.WideWindow)
		if werr != nil {
			if ctx.Err() != nil {
				return nil, werr
			}
			e.log.Warn("gap re-fetch failed", "prs", len(wref), "error", errClass(werr))
			for _, i := range gapped {
				steps[i].gapLost = true
			}
		} else {
			cost += wres.RateLimit.Cost
			nodeCount += wres.RateLimit.NodeCount
			res.RateLimit.Remaining, res.RateLimit.Limit, res.ResetAt = wres.RateLimit.Remaining, wres.RateLimit.Limit, wres.ResetAt
			e.mu.Lock()
			e.budget.Record(now, wres.RateLimit.Cost)
			e.budget.Observe(wres.RateLimit.Limit, wres.RateLimit.Remaining, wres.ResetAt, wres.RateLimit.Cost, len(wref))
			e.mu.Unlock()
			for j, i := range gapped {
				it := itemFor(wres.Items, j, steps[i].ref)
				if it.Err != nil {
					steps[i].gapLost = true
					continue
				}
				steps[i].item = it
				steps[i].res = diff.Step(steps[i].mem, it.Snapshot)
				steps[i].gapLost = steps[i].res.Gap
			}
		}
	}

	results, report, err := e.commit(steps, res.Viewer, now, forced, cost)
	if err != nil {
		e.log.Error("poll commit failed", "prs", len(targets), "error", err)
		return nil, err
	}
	report.Remaining, report.Limit, report.ResetAt = res.RateLimit.Remaining, res.RateLimit.Limit, res.ResetAt

	e.mu.Lock()
	stretch, batchSize := e.stretch, e.budget.BatchSize
	e.mu.Unlock()
	e.log.Info("poll",
		"prs", len(targets),
		"cost", cost,
		"remaining", res.RateLimit.Remaining,
		"limit", res.RateLimit.Limit,
		"reset_at", res.ResetAt,
		"node_count", nodeCount,
		"stretch", stretch,
		"batch_size", batchSize,
		"duration_ms", e.now().Sub(start).Milliseconds(),
	)
	if e.onCommit != nil {
		e.onCommit(report)
	}
	return results, nil
}

// itemFor returns items[i] when it is the result for ref.
func itemFor(items []ghquery.AliasResult, i int, ref model.PRRef) ghquery.AliasResult {
	if i < len(items) && store.PRKey(items[i].Ref) == store.PRKey(ref) {
		return items[i]
	}
	return ghquery.AliasResult{Ref: ref, Err: fmt.Errorf("poller: no result for %s", ref.String())}
}

// batchFailed records a batch-level failure: budget backoff plus in-memory
// LastPollAt/LastError (no Save, pitfall 6).
func (e *Engine) batchFailed(ctx context.Context, targets []target, now time.Time, err error) {
	if ctx.Err() != nil {
		return
	}
	var se *ghclient.SecondaryLimitError
	var pe *ghclient.PrimaryExhaustedError
	class := errClass(err)
	e.mu.Lock()
	switch {
	case errors.As(err, &se):
		e.budget.OnSecondary(now, se.RetryAfter)
	case errors.As(err, &pe):
		e.budget.OnPrimaryExhausted(pe.ResetAt)
	}
	for _, t := range targets {
		if rec := e.state.PRs[t.key]; rec != nil && rec.Gen == t.gen {
			rec.LastPollAt = now
			rec.LastError = class
		}
	}
	e.mu.Unlock()
	e.log.Warn("poll failed", "prs", len(targets), "error", class)
}

// commit applies stepped results to a clone of the live state and saves it.
// When nothing durable changed, the Save is skipped and only LastPollAt is
// updated in memory.
func (e *Engine) commit(steps []stepped, viewer string, now time.Time, forced bool, cost int) ([]PollResult, CommitReport, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	next := e.state.Clone()
	changed := false
	if viewer != "" && viewer != next.Viewer {
		next.Viewer = viewer
		changed = true
	}
	self := next.Viewer
	limit := e.cfg.Subscriptions.PendingCap

	subsByKey := map[string][]*store.Subscription{}
	for _, s := range next.Subs {
		subsByKey[s.PR] = append(subsByKey[s.PR], s)
	}
	for _, list := range subsByKey {
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	}

	results := make([]PollResult, 0, len(steps))
	report := CommitReport{At: now, Forced: forced, Cost: cost}
	type touched struct {
		key string
		gen uint64
	}
	var polled []touched
	for _, st := range steps {
		pr := PollResult{Ref: st.ref, Cost: cost}
		rep := PRReport{Key: st.key, Ref: st.ref}
		rec := next.PRs[st.key]
		if rec == nil || rec.Gen != st.gen {
			pr.Err = errStale
			results = append(results, pr)
			continue
		}
		polled = append(polled, touched{st.key, st.gen})
		rec.LastPollAt = now
		if st.item.Err != nil {
			class := errClass(st.item.Err)
			if rec.LastError != class {
				rec.LastError = class
				changed = true
			}
			pr.Err = st.item.Err
			rep.Err = class
		} else {
			if rec.LastError != "" {
				rec.LastError = ""
				changed = true
			}
			if !reflect.DeepEqual(rec.Memory, st.res.Memory) {
				rec.Memory = st.res.Memory
				changed = true
			}
			delivered, activity := classify(st.res.Events, self)
			if activity {
				rec.RateAnchor = now
				rec.LastActivity = now
				changed = true
			}
			for _, sub := range subsByKey[st.key] {
				if appendPending(sub, delivered, &next.Seq, limit, now) > 0 {
					changed = true
				}
				if st.gapLost {
					sub.Dropped++
					next.Seq++
					sub.DroppedSeq = next.Seq
					changed = true
				}
			}
			if st.item.Snapshot.State == "MERGED" && !rec.Terminal {
				rec.Terminal = true
				changed = true
			}
			pr.NewEvents = len(delivered)
			rep.NewEvents = len(delivered)
			rep.Activity = activity
		}
		rep.Interval, rep.NextPollAt = e.scheduleLocked(rec, now)
		results = append(results, pr)
		report.PRs = append(report.PRs, rep)
	}

	if changed {
		if err := e.saver.Save(next); err != nil {
			return nil, CommitReport{}, err
		}
		e.state = next
	} else {
		for _, p := range polled {
			if rec := e.state.PRs[p.key]; rec != nil && rec.Gen == p.gen {
				rec.LastPollAt = now
			}
		}
	}
	return results, report, nil
}
