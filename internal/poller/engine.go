package poller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
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
	// the engine's locks (the poll lock included), so it may call PollNow. Calls
	// from concurrent polls are not serialized: the callback must do its
	// own locking.
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
	// AgentKind is the herdr agent kind (e.g. "codex") seen at subscribe
	// time, the D-13 fallback identity.
	AgentKind  string
	TerminalID string
	Events     []model.EventType
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
	// Terminal is true when the PR is already merged. It is never polled
	// again, so Interval and NextPollAt are zero (WR-04).
	Terminal bool
}

// Drain is a copy of one subscription's pending queue. UpToSeq is the value
// to Ack to clear everything in it, the drop marker included. DroppedSeq is
// the drop marker's sequence number (zero when there is none).
type Drain struct {
	Events     []store.PendingEvent
	Dropped    int
	DroppedSeq uint64
	UpToSeq    uint64
}

// PRView is a PR's current polled state, read at delivery time (DLV-07).
type PRView struct {
	Ref       model.PRRef
	HeadOID   string
	Mergeable string
	State     string
	Baselined bool
}

// Cancellation reasons. The delivery manager uses the pane ones (LIFE-02,
// LIFE-04); closed and expired are recorded when the final event of an
// ending subscription is acked (LIFE-01, LIFE-07, D-14, D-23).
const (
	ReasonPaneClosed       = "pane_closed"
	ReasonPaneGone         = "pane_gone"
	ReasonIdentityMismatch = "identity_mismatch"
	ReasonClosed           = "closed"
	ReasonExpired          = "expired"
)

// Cancellation records one subscription cancelled by the daemon (D-17). The
// most recent ones are kept in memory for Phase 4 status; they are never
// persisted.
type Cancellation struct {
	SubID, PR, HerdrSocket, PaneID, Reason string
	At                                     time.Time
}

// maxCancellations caps the in-memory cancellation ring (T-03-14).
const maxCancellations = 50

// UndeliverableAfter is D-16: every subscribed pane unable to receive for
// this long means the PR is polled at the slowest rate (LIFE-08, D-06).
const UndeliverableAfter = 30 * time.Minute

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

// ErrEnded means the subscription is ending (its closed or expired event is
// queued) and can no longer be kept alive (D-03).
var ErrEnded = errors.New("poller: subscription has ended; re-subscribe")

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
// (pollSem, a one-slot semaphore whose wait honours the caller's context,
// review CR-01); the network call holds no lock, so Subscribe and friends
// never wait on GitHub. A result whose record Gen changed during the call is
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
	mu sync.Mutex
	// pollSem serializes polls and subscribe probes. Acquire it with
	// lockPoll, which gives up when the caller's context ends.
	pollSem chan struct{}

	state   *store.State
	saver   Saver
	fetch   Fetcher
	cfg     config.Config
	log     *slog.Logger
	budget  *Budget
	stretch float64
	wake    chan struct{}
	// pendingWake is the delivery manager's signal, separate from wake,
	// which the scheduler consumes.
	pendingWake chan struct{}
	onCommit    func(CommitReport)
	now         func() time.Time

	// Scheduler and Status state (scheduler.go, status.go), guarded by mu.
	auth        AuthState
	authRetryAt time.Time
	quarPath    string
	quarAt      time.Time
	// cancels is the recent-cancellations ring, newest last (D-17).
	cancels []Cancellation
	// reach maps a pane that cannot receive to when it stopped being able
	// to (LIFE-08). In memory only; a missing entry means receivable.
	reach map[PaneRef]time.Time
	// running makes a second concurrent Run fail instead of racing.
	running atomic.Bool
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
		state:       o.State,
		saver:       o.Saver,
		fetch:       o.Fetcher,
		cfg:         o.Config,
		log:         o.Logger,
		stretch:     1,
		pollSem:     make(chan struct{}, 1),
		wake:        make(chan struct{}, 1),
		pendingWake: make(chan struct{}, 1),
		reach:       map[PaneRef]time.Time{},
		onCommit:    o.OnCommit,
		now:         o.Now,
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

// PendingWake returns a channel that fires after committed new pending
// events or drop markers, and after a re-subscribe re-filters a queue. It is
// distinct from Wake, which the scheduler consumes. A value means "look
// again", not a count: several commits may collapse into one value.
func (e *Engine) PendingWake() <-chan struct{} { return e.pendingWake }

// pokePending is non-blocking, so it is safe to call while holding e.mu.
func (e *Engine) pokePending() {
	select {
	case e.pendingWake <- struct{}{}:
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
	if err := e.save(next); err != nil {
		return err
	}
	e.state = next
	return nil
}

// save persists next. A store.ErrNotDurable error means state.json already
// holds next (only the directory fsync failed), so it is logged and treated
// as success: the caller must swap next in, or memory would fall behind disk
// and, for example, re-deliver events an Ack already removed (WR-05).
func (e *Engine) save(next *store.State) error {
	err := e.saver.Save(next)
	if errors.Is(err, store.ErrNotDurable) {
		e.log.Warn("state saved but may not be durable", "error", err)
		return nil
	}
	return err
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

	e.mu.Lock()
	now := e.now()
	var id string
	created := false
	newRecord := false
	err = e.mutateLocked(func(next *store.State) error {
		if next.PRs[key] == nil {
			next.NextGen++
			next.PRs[key] = &store.PRRecord{
				Ref:        req.PR,
				Gen:        next.NextGen,
				CreatedAt:  now,
				RateAnchor: now,
			}
			newRecord = true
		}
		var err error
		id, created, err = e.subscribeIn(next, req, mask, now)
		return err
	})
	if err != nil {
		e.mu.Unlock()
		return SubInfo{}, err
	}
	refiltered := !created
	info := e.subInfoLocked(req, id, created, now)
	e.mu.Unlock()
	if newRecord {
		e.poke()
	}
	if refiltered {
		e.pokePending()
	}
	return info, nil
}

// subInfoLocked assembles the SubInfo of subscription id after a subscribe.
// Ending subscriptions are not co-subscribers. Callers hold e.mu.
func (e *Engine) subInfoLocked(req SubRequest, id string, created bool, now time.Time) SubInfo {
	key := store.PRKey(req.PR)
	info := SubInfo{SubID: id, Created: created}
	for _, p := range e.panesLocked(key) {
		if p.HerdrSocket == req.HerdrSocket && p.PaneID == req.PaneID {
			continue
		}
		info.CoSubscribers = append(info.CoSubscribers, p)
	}
	rec := e.state.PRs[key]
	info.Interval, info.NextPollAt = e.scheduleLocked(rec, now)
	info.Terminal = rec != nil && rec.Terminal
	return info
}

// subscribeIn creates or updates the subscription of (HerdrSocket, PaneID,
// PR) in next, whose PR record must already exist. An existing subscription
// has its mask replaced, its pending queue re-filtered and its clock reset.
// An expired-ending one is revived: its ending marker and check-in are
// cleared and its queued expired and check-in events dropped (D-02, D-14).
// A closed-ending one cannot be revived and returns a pr_closed
// *VerifyError. Callers hold e.mu.
func (e *Engine) subscribeIn(next *store.State, req SubRequest, mask []model.EventType, now time.Time) (id string, created bool, err error) {
	key := store.PRKey(req.PR)
	id = store.SubID(req.HerdrSocket, req.PaneID, key)
	if sub := next.Subs[id]; sub != nil {
		switch sub.Ending {
		case ReasonClosed:
			return id, false, &VerifyError{Class: ClassPRClosed, Err: errPRClosed}
		case ReasonExpired:
			sub.Ending, sub.EndSeq = "", 0
			sub.CheckinAt = time.Time{}
			kept := sub.Pending[:0:0]
			for _, p := range sub.Pending {
				if p.Event.Type == model.EventExpired || p.Event.Type == model.EventCheckin {
					continue
				}
				kept = append(kept, p)
			}
			sub.Pending = kept
		}
		sub.Events = mask
		sub.AgentSession = req.AgentSession
		sub.AgentKind = req.AgentKind
		sub.TerminalID = req.TerminalID
		sub.UpdatedAt = now
		sub.ClockAt = now
		sub.Pending = refilter(sub.Pending, mask)
		return id, false, nil
	}
	next.Subs[id] = &store.Subscription{
		ID:           id,
		PR:           key,
		HerdrSocket:  req.HerdrSocket,
		PaneID:       req.PaneID,
		AgentSession: req.AgentSession,
		AgentKind:    req.AgentKind,
		TerminalID:   req.TerminalID,
		Events:       mask,
		CreatedAt:    now,
		UpdatedAt:    now,
		ClockAt:      now,
	}
	return id, true, nil
}

// Unsubscribe removes a subscription. The PR record goes with the last one.
func (e *Engine) Unsubscribe(subID string) error {
	return e.mutate(func(next *store.State) error { return unsubscribeIn(next, subID) })
}

// unsubscribeIn removes subID (and its pending queue) from next, and the PR
// record with its last subscription.
func unsubscribeIn(next *store.State, subID string) error {
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
}

// Cancel removes a subscription on the daemon's own initiative (pane closed,
// pane gone, identity mismatch; LIFE-02, LIFE-04). Like Unsubscribe it drops
// the pending queue and, with the last subscription, the PR record. Each
// cancellation is logged with sub, PR, pane, socket and reason; nothing is
// sent to the pane (D-17). The newest 50 are kept for RecentCancellations.
func (e *Engine) Cancel(subID, reason string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	sub := e.state.Subs[subID]
	if sub == nil {
		return ErrUnknownSub
	}
	c := Cancellation{SubID: subID, PR: sub.PR, HerdrSocket: sub.HerdrSocket, PaneID: sub.PaneID, Reason: reason}
	if err := e.mutateLocked(func(next *store.State) error { return unsubscribeIn(next, subID) }); err != nil {
		return err
	}
	c.At = e.now()
	e.log.Info("subscription cancelled",
		"sub", c.SubID, "pr", c.PR, "pane", c.PaneID, "socket", c.HerdrSocket, "reason", c.Reason)
	e.recordCancelLocked(c)
	return nil
}

// recordCancelLocked appends c to the cancellation ring, keeping the newest
// maxCancellations. Callers hold e.mu.
func (e *Engine) recordCancelLocked(c Cancellation) {
	e.cancels = append(e.cancels, c)
	if over := len(e.cancels) - maxCancellations; over > 0 {
		e.cancels = append([]Cancellation(nil), e.cancels[over:]...)
	}
}

// RecentCancellations returns a copy of the newest cancellations, oldest
// first.
func (e *Engine) RecentCancellations() []Cancellation {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Cancellation(nil), e.cancels...)
}

// PRView returns the PR's current polled state. ok is false when key is not
// watched.
func (e *Engine) PRView(key string) (PRView, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rec := e.state.PRs[key]
	if rec == nil {
		return PRView{}, false
	}
	m := rec.Memory
	return PRView{Ref: rec.Ref, HeadOID: m.HeadOID, Mergeable: m.Mergeable, State: m.State, Baselined: m.Baselined}, true
}

// Keep resets the subscription's ClockAt, clears CheckinAt and drops any
// pending check-in, which keep answers (D-03). An ending subscription
// returns ErrEnded and is left unchanged.
func (e *Engine) Keep(subID string) error {
	return e.mutate(func(next *store.State) error {
		sub := next.Subs[subID]
		if sub == nil {
			return ErrUnknownSub
		}
		if sub.Ending != "" {
			return ErrEnded
		}
		sub.ClockAt = e.now()
		sub.CheckinAt = time.Time{}
		var kept []store.PendingEvent
		for _, p := range sub.Pending {
			if p.Event.Type != model.EventCheckin {
				kept = append(kept, p)
			}
		}
		sub.Pending = kept
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
	d := Drain{Events: copyPending(sub.Pending), Dropped: sub.Dropped, DroppedSeq: sub.DroppedSeq, UpToSeq: sub.DroppedSeq}
	for _, p := range sub.Pending {
		d.UpToSeq = max(d.UpToSeq, p.Seq)
	}
	return d, nil
}

// Ack drops pending events with Seq <= upToSeq and clears the drop marker
// when upToSeq covers it. When the ack covers an ending subscription's final
// event, the subscription is deleted and recorded as a cancellation.
func (e *Engine) Ack(subID string, upToSeq uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	var ended []Cancellation
	err := e.mutateLocked(func(next *store.State) error {
		sub := next.Subs[subID]
		if sub == nil {
			return ErrUnknownSub
		}
		ended = ackIn(next, sub, upToSeq, now, nil)
		return nil
	})
	if err != nil {
		return err
	}
	e.endedLocked(ended)
	return nil
}

// AckMany acks several subscriptions in one Save (DLV-04). Subscriptions
// that no longer exist (cancelled meanwhile) are skipped, not errors. An
// empty map saves nothing. A failed Save leaves the state unchanged.
// Subscriptions whose final event is acked are deleted in the same Save
// (LIFE-01).
func (e *Engine) AckMany(acks map[string]uint64) error {
	if len(acks) == 0 {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	var ended []Cancellation
	err := e.mutateLocked(func(next *store.State) error {
		ids := make([]string, 0, len(acks))
		for id := range acks {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			sub := next.Subs[id]
			if sub == nil {
				continue
			}
			ended = ackIn(next, sub, acks[id], now, ended)
		}
		return nil
	})
	if err != nil {
		return err
	}
	e.endedLocked(ended)
	return nil
}

// ackIn acks sub inside next and, when its final event is covered, removes
// it from next and appends its Cancellation to ended.
func ackIn(next *store.State, sub *store.Subscription, upToSeq uint64, now time.Time, ended []Cancellation) []Cancellation {
	if !ackSub(sub, upToSeq, now) {
		return ended
	}
	c := Cancellation{SubID: sub.ID, PR: sub.PR, HerdrSocket: sub.HerdrSocket, PaneID: sub.PaneID, Reason: sub.Ending}
	if err := unsubscribeIn(next, sub.ID); err != nil {
		return ended
	}
	return append(ended, c)
}

// endedLocked logs and records subscriptions removed by an ack, after the
// Save that removed them succeeded (D-23). Callers hold e.mu.
func (e *Engine) endedLocked(ended []Cancellation) {
	for _, c := range ended {
		c.At = e.now()
		e.log.Info("subscription ended",
			"sub", c.SubID, "pr", c.PR, "pane", c.PaneID, "socket", c.HerdrSocket, "reason", c.Reason)
		e.recordCancelLocked(c)
	}
}

// ackSub drops sub's pending events with Seq <= upToSeq and clears the drop
// marker when upToSeq covers it. If a real (non-lifecycle) event is among
// those acked, the inactivity clock restarts: ClockAt and LastEventAt become
// now and LastEventType that event's type (D-11). Check-in and expired acks
// never reset it. ended reports that the ack covered the final event of an
// ending subscription, which the caller must then delete.
func ackSub(sub *store.Subscription, upToSeq uint64, now time.Time) (ended bool) {
	var kept []store.PendingEvent
	var lastReal model.EventType
	for _, p := range sub.Pending {
		if p.Seq > upToSeq {
			kept = append(kept, p)
			continue
		}
		if !p.Event.Type.IsLifecycle() {
			lastReal = p.Event.Type
		}
	}
	sub.Pending = kept
	if lastReal != "" {
		sub.ClockAt = now
		sub.LastEventAt = now
		sub.LastEventType = lastReal
	}
	if sub.DroppedSeq != 0 && sub.DroppedSeq <= upToSeq {
		sub.Dropped, sub.DroppedSeq = 0, 0
	}
	return sub.EndSeq != 0 && sub.EndSeq <= upToSeq
}

// panesLocked lists every pane subscribed to key, sorted. Ending
// subscriptions are on their way out and are not listed, so they count for
// neither co-subscribers nor the undeliverable check (D-14, D-16).
func (e *Engine) panesLocked(key string) []PaneRef {
	var out []PaneRef
	for _, s := range e.state.Subs {
		if s.PR == key && s.Ending == "" {
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

// SetPaneReachability records whether a pane can receive deliveries. A
// non-zero cannotSince marks it unable to receive since then; the zero time
// marks it receivable. Working counts as receivable; blocked, no agent,
// unknown status and herdr unreachable do not. The caller keeps the earliest
// since across consecutive cannot-receive states. When a pane becomes
// receivable again the scheduler is woken, so the normal cadence resumes on
// its next loop (LIFE-08).
func (e *Engine) SetPaneReachability(p PaneRef, cannotSince time.Time) {
	e.mu.Lock()
	_, was := e.reach[p]
	if cannotSince.IsZero() {
		delete(e.reach, p)
	} else {
		e.reach[p] = cannotSince
	}
	e.mu.Unlock()
	if was && cannotSince.IsZero() {
		e.poke()
	}
}

// undeliverableLocked reports whether every pane subscribed to key has been
// unable to receive for at least UndeliverableAfter. A PR with no panes, or
// with one pane without an entry, is deliverable. Callers hold e.mu.
func (e *Engine) undeliverableLocked(key string, now time.Time) bool {
	panes := e.panesLocked(key)
	if len(panes) == 0 {
		return false
	}
	for _, p := range panes {
		since, ok := e.reach[p]
		if !ok || now.Sub(since) < UndeliverableAfter {
			return false
		}
	}
	return true
}

// CoSubscribers lists every pane subscribed to ref.
func (e *Engine) CoSubscribers(ref model.PRRef) []PaneRef {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.panesLocked(store.PRKey(ref))
}

// Schedule returns the PR's current interval and next poll time. A PR never
// polled is due now. An unwatched PR, or a Terminal (merged) one that is
// never polled again, returns zero values.
func (e *Engine) Schedule(ref model.PRRef) (time.Duration, time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.scheduleLocked(e.state.PRs[store.PRKey(ref)], e.now())
}

func (e *Engine) scheduleLocked(rec *store.PRRecord, now time.Time) (time.Duration, time.Time) {
	if rec == nil || rec.Terminal {
		return 0, time.Time{}
	}
	p := e.cfg.Poll
	if e.undeliverableLocked(store.PRKey(rec.Ref), now) {
		// LIFE-08: no pane can receive, so poll at the slowest rate.
		base := p.MaxInterval.Duration
		interval := Effective(base, e.stretch, 0, p.MaxStretchedInterval.Duration)
		if rec.LastPollAt.IsZero() {
			return interval, now
		}
		return interval, NextDueBase(rec.LastPollAt, base, p.MaxStretchedInterval.Duration, e.stretch, 0)
	}
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
	// gapErr is set when the wide re-fetch for this gapped PR failed as a
	// whole. Its normal-window result is then not committed: Memory stays
	// as it was, so the next poll re-derives the gap and can still recover
	// every item (review WR-02).
	gapErr error
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
//
// The wait for the poll lock is bounded by ctx (review CR-01). OnCommit
// runs after the lock is released (review WR-03), so a callback may call
// PollNow without deadlocking and does not stall other polls.
func (e *Engine) pollRefs(ctx context.Context, keys []string, forced bool) ([]PollResult, error) {
	if err := e.lockPoll(ctx); err != nil {
		return nil, err
	}
	results, report, err := e.pollRefsLocked(ctx, keys, forced)
	e.unlockPoll()
	if err == nil && report != nil && e.onCommit != nil {
		e.onCommit(*report)
	}
	return results, err
}

// lockPoll acquires the poll lock, or returns ctx.Err() if ctx ends first
// (review CR-01). A caller that got nil must call unlockPoll.
func (e *Engine) lockPoll(ctx context.Context) error {
	select {
	case e.pollSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// unlockPoll releases the poll lock taken by lockPoll.
func (e *Engine) unlockPoll() { <-e.pollSem }

// pollRefsLocked is pollRefs without the callback. The caller holds the
// poll lock.
// The report is nil when nothing was committed.
func (e *Engine) pollRefsLocked(ctx context.Context, keys []string, forced bool) ([]PollResult, *CommitReport, error) {
	if len(keys) > ghquery.MaxBatch {
		return nil, nil, fmt.Errorf("poller: %d pull requests exceed one batch of %d", len(keys), ghquery.MaxBatch)
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
		return nil, nil, nil
	}
	if w := e.budget.Gate(now, forced); w > 0 {
		e.mu.Unlock()
		return nil, nil, &RateLimitedError{Until: now.Add(w)}
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
		return nil, nil, err
	}
	cost := res.RateLimit.Cost
	e.mu.Lock()
	e.budget.Record(now, res.RateLimit.Cost)
	e.budget.Observe(res.RateLimit.Limit, res.RateLimit.Remaining, res.ResetAt, res.RateLimit.Cost, len(refs))
	e.mu.Unlock()

	steps := make([]stepped, len(targets))
	var gapped []int
	for i, t := range targets {
		steps[i] = stepped{target: t, item: itemFor(res.Items, i, t.ref)}
		if steps[i].item.Err == nil {
			steps[i].res = diff.Step(t.mem, steps[i].item.Snapshot)
			if !t.mem.Baselined {
				// The baseline is silent, but a PR that is already
				// merged or closed would then never notify (WR-04).
				if ev, ok := diff.BaselineClosed(steps[i].item.Snapshot); ok {
					steps[i].res.Events = append(steps[i].res.Events, ev)
				}
			}
			if steps[i].res.Gap {
				gapped = append(gapped, i)
			}
		}
	}
	nodeCount := res.RateLimit.NodeCount
	rateLimited := false
	if len(gapped) > 0 {
		wref := make([]model.PRRef, len(gapped))
		for j, i := range gapped {
			wref[j] = steps[i].ref
		}
		wres, werr := e.fetch.Batch(ctx, wref, ghquery.WideWindow)
		if werr != nil {
			if ctx.Err() != nil {
				return nil, nil, werr
			}
			// A rate limit on the re-fetch must back off exactly like one
			// on the first batch (D-07), or the next chunk hits it again.
			rateLimited = e.applyRateLimit(now, werr)
			e.log.Warn("gap re-fetch failed", "prs", len(wref), "error", errClass(werr))
			for _, i := range gapped {
				steps[i].gapErr = werr
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
					// Record the item error and keep Memory unchanged,
					// so a later poll can still close the gap.
					steps[i].item = it
					continue
				}
				steps[i].item = it
				steps[i].res = diff.Step(steps[i].mem, it.Snapshot)
				steps[i].gapLost = steps[i].res.Gap
			}
		}
	}
	// The batch counts as clean only once every fetch in it, the gap
	// re-fetch included, has come back without a rate limit.
	if !rateLimited {
		e.mu.Lock()
		e.budget.OnClean()
		e.mu.Unlock()
	}

	results, report, err := e.commit(steps, res.Viewer, now, forced, cost)
	if err != nil {
		e.log.Error("poll commit failed", "prs", len(targets), "error", err)
		return nil, nil, err
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
	return results, &report, nil
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
	class := errClass(err)
	e.applyRateLimit(now, err)
	e.mu.Lock()
	for _, t := range targets {
		if rec := e.state.PRs[t.key]; rec != nil && rec.Gen == t.gen {
			rec.LastPollAt = now
			rec.LastError = class
		}
	}
	e.mu.Unlock()
	e.log.Warn("poll failed", "prs", len(targets), "error", class)
}

// applyRateLimit feeds a secondary or primary rate-limit error into the
// budget's backoff and reports whether err was one. Callers must not hold
// e.mu.
func (e *Engine) applyRateLimit(now time.Time, err error) bool {
	var se *ghclient.SecondaryLimitError
	var pe *ghclient.PrimaryExhaustedError
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case errors.As(err, &se):
		e.budget.OnSecondary(now, se.RetryAfter)
		return true
	case errors.As(err, &pe):
		e.budget.OnPrimaryExhausted(pe.ResetAt)
		return true
	}
	return false
}

// markEnding sets sub's ending marker when its queue holds a closed event:
// the sub is deleted once that event is acked (LIFE-01, D-16).
func markEnding(sub *store.Subscription) {
	if sub.Ending != "" {
		return
	}
	for _, p := range sub.Pending {
		if p.Event.Type == model.EventClosed {
			sub.Ending, sub.EndSeq = ReasonClosed, p.Seq
			return
		}
	}
}

// commit applies stepped results to a clone of the live state and saves it.
// When nothing durable changed, the Save is skipped and only LastPollAt is
// updated in memory.
func (e *Engine) commit(steps []stepped, viewer string, now time.Time, forced bool, cost int) ([]PollResult, CommitReport, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	next := e.state.Clone()
	changed := false
	// added is set when a sub got new pending events or a drop marker, so
	// delivery is told after the save (PendingWake).
	added := false
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
	// deferred are gapped PRs whose wide re-fetch failed. Like batchFailed,
	// only their in-memory LastPollAt/LastError change; nothing durable is
	// committed for them.
	type deferredPR struct {
		touched
		class string
	}
	var deferred []deferredPR
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
		if st.gapErr != nil {
			class := errClass(st.gapErr)
			deferred = append(deferred, deferredPR{touched{st.key, st.gen}, class})
			pr.Err = st.gapErr
			rep.Err = class
		} else if st.item.Err != nil {
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
				if sub.Ending != "" {
					// An ending sub receives no new PR events (D-14).
					continue
				}
				before := sub.DroppedSeq
				if appendPending(sub, delivered, &next.Seq, limit, now) > 0 {
					changed, added = true, true
				}
				markEnding(sub)
				if sub.DroppedSeq != before {
					// Cap overflow set a new drop marker.
					changed, added = true, true
				}
				if st.gapLost {
					sub.Dropped++
					next.Seq++
					sub.DroppedSeq = next.Seq
					changed, added = true, true
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
		if err := e.save(next); err != nil {
			return nil, CommitReport{}, err
		}
		e.state = next
		if added {
			e.pokePending()
		}
	} else {
		for _, p := range polled {
			if rec := e.state.PRs[p.key]; rec != nil && rec.Gen == p.gen {
				rec.LastPollAt = now
			}
		}
	}
	for _, d := range deferred {
		if rec := e.state.PRs[d.key]; rec != nil && rec.Gen == d.gen {
			rec.LastError = d.class
		}
	}
	return results, report, nil
}
