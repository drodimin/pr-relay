package deliver

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/drodimin/pr-relay/internal/compose"
	"github.com/drodimin/pr-relay/internal/herdr"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/poller"
	"github.com/drodimin/pr-relay/internal/store"
)

// link is the state of a herdr session's events connection (satisfied by
// *herdr.Endpoint).
type link interface {
	Up() bool
	// UpSince is when the link last came up (zero while down).
	UpSince() time.Time
	// DownSince is when the link went down (zero while up).
	DownSince() time.Time
}

var _ link = (*herdr.Endpoint)(nil)

// workerConfig tunes one worker. Zero fields take the defaults.
type workerConfig struct {
	// QuietPeriod is how long the agent must stay idle or done, with no
	// status change, before a prompt (D-09).
	QuietPeriod time.Duration
	// RestoreGrace is how long the herdr link must be up before a missing
	// pane counts as gone (A2, Pitfall 6).
	RestoreGrace time.Duration
	// WaitTimeout bounds each agent.wait while working or blocked
	// (Pitfall 5).
	WaitTimeout time.Duration
	// Recheck is how often a pane that cannot receive is looked at again
	// when no signal arrives.
	Recheck time.Duration
	// PromptWait is the prompt's wait for the agent to start working.
	PromptWait time.Duration
	// MaxBytes is the message flood guard (D-12).
	MaxBytes int
}

const (
	defaultQuietPeriod  = 10 * time.Second
	defaultRestoreGrace = 10 * time.Second
	defaultWaitTimeout  = 5 * time.Minute
	defaultRecheck      = 60 * time.Second
	defaultPromptWait   = 5 * time.Second
)

func (c workerConfig) withDefaults() workerConfig {
	if c.QuietPeriod <= 0 {
		c.QuietPeriod = defaultQuietPeriod
	}
	if c.RestoreGrace <= 0 {
		c.RestoreGrace = defaultRestoreGrace
	}
	if c.WaitTimeout <= 0 {
		c.WaitTimeout = defaultWaitTimeout
	}
	if c.Recheck <= 0 {
		c.Recheck = defaultRecheck
	}
	if c.PromptWait <= 0 {
		c.PromptWait = defaultPromptWait
	}
	if c.MaxBytes <= 0 {
		c.MaxBytes = compose.DefaultMaxBytes
	}
	return c
}

// untilFree is the only until list the worker waits with (D-03).
var untilFree = []string{herdr.StatusIdle, herdr.StatusDone}

// worker delivers the pending updates of one (socket, pane). It is not safe
// for concurrent use except poke.
type worker struct {
	pane poller.PaneRef
	cl   *herdr.Client
	link link
	eng  Engine
	// sig wakes the worker early (status event, reconnect, new pending
	// events). Capacity 1, coalescing.
	sig chan struct{}
	// report receives reachability changes (LIFE-08).
	report func(poller.PaneRef, time.Time)
	cfg    workerConfig
	now    func() time.Time
	log    *slog.Logger

	// cannotSince is the earliest time of the current cannot-receive run
	// (zero while receivable).
	cannotSince time.Time

	// unconfirmed is the last prompt whose working-wait timed out: herdr
	// accepted the text but never saw the agent start (CR-01). Its acks
	// apply only once a later agent.get shows a completed turn.
	unconfirmed *unconfirmedPrompt
	// unconfirmedRuns counts consecutive unconfirmed prompts (backoff).
	unconfirmedRuns int
}

// unconfirmedPrompt is a prompt that may or may not have reached the agent.
type unconfirmedPrompt struct {
	acks map[string]uint64
	// completion is the agent's completion_seq before the prompt (nil when
	// the agent had not completed a turn).
	completion *uint64
}

// maxUnconfirmedBackoff caps the re-send backoff of unconfirmed prompts.
const maxUnconfirmedBackoff = 16 * time.Minute

func newWorker(pane poller.PaneRef, cl *herdr.Client, l link, eng Engine, cfg workerConfig, log *slog.Logger) *worker {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &worker{
		pane:   pane,
		cl:     cl,
		link:   l,
		eng:    eng,
		sig:    make(chan struct{}, 1),
		report: eng.SetPaneReachability,
		cfg:    cfg.withDefaults(),
		now:    time.Now,
		log:    log.With("pane", pane.PaneID, "socket", pane.HerdrSocket),
	}
}

// poke wakes the worker without blocking; pokes coalesce.
func (w *worker) poke() {
	select {
	case w.sig <- struct{}{}:
	default:
	}
}

// run delivers until the pane has nothing pending, every subscription of
// the pane is cancelled, or ctx is done.
func (w *worker) run(ctx context.Context) {
	for ctx.Err() == nil {
		subs := w.paneSubs()
		if !w.anyPending(subs) {
			w.markReceivable()
			return
		}
		info, err := w.cl.AgentGet(ctx, w.pane.PaneID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			switch {
			case herdr.IsUnreachable(err):
				w.markCannot(w.downSince())
				w.sleep(ctx, w.cfg.Recheck)
			case herdr.Code(err) == herdr.CodeAgentNotFound:
				if w.noAgent(ctx, subs) {
					return
				}
			default:
				w.log.Warn("agent.get failed", "err", err)
				w.markCannot(w.now())
				w.sleep(ctx, w.cfg.Recheck)
			}
			continue
		}

		// The settled gate runs before identify: the identity check can
		// cancel subscriptions, and a mid-relaunch snapshot must never do
		// that (WR-02).
		if !settled(info) {
			w.markCannot(w.now())
			w.sleep(ctx, w.cfg.Recheck)
			continue
		}
		matching, held, failed := w.identify(subs, info)
		if failed || (len(matching) == 0 && held) {
			// A failed cancel (store save error) would otherwise be retried
			// at once, forever (WR-03): hold the pane for Recheck instead.
			w.markCannot(w.now())
			w.sleep(ctx, w.cfg.Recheck)
			continue
		}
		if len(matching) == 0 {
			continue // every sub was cancelled: the loop exits
		}
		if w.confirmUnconfirmed(info, matching) {
			continue // re-read pending
		}

		switch info.Status {
		case herdr.StatusWorking:
			w.markReceivable()
			w.waitFree(ctx)
		case herdr.StatusBlocked:
			w.markCannot(w.now())
			w.waitFree(ctx)
		case herdr.StatusIdle, herdr.StatusDone:
			w.markReceivable()
			w.quietThenDeliver(ctx, info)
		default:
			// A status this client does not know: never type into it.
			w.markCannot(w.now())
			w.sleep(ctx, w.cfg.Recheck)
		}
	}
}

// settled reports whether herdr considers the agent's snapshot stable
// enough to type into or to run the identity check on: no launch pending, a
// known status, and readiness not explicitly false. An absent
// interactive_ready key (herdr 0.9.5 on long-running agents) counts as
// ready (WR-01).
func settled(info herdr.AgentInfo) bool {
	switch {
	case info.LaunchPending:
		return false
	case info.Status == herdr.StatusUnknown || info.Status == "":
		return false
	case info.InteractiveReady != nil && !*info.InteractiveReady:
		return false
	}
	return true
}

// quietThenDeliver waits the quiet period on a timer (never agent.wait,
// which returns at once while idle) and delivers if the agent is still
// free, unchanged and the same agent.
func (w *worker) quietThenDeliver(ctx context.Context, before herdr.AgentInfo) {
	snap := snapshotOf(before)
	if timedOut := w.sleep(ctx, w.cfg.QuietPeriod); !timedOut {
		return // a signal restarts the window; ctx ends run
	}
	info, err := w.cl.AgentGet(ctx, w.pane.PaneID)
	if err != nil || snapshotOf(info) != snap || !settled(info) {
		return // the loop re-gates
	}
	matching, _, failed := w.identify(w.paneSubs(), info)
	if failed || len(matching) == 0 {
		return // the loop re-gates and backs off on a failed cancel
	}
	w.deliver(ctx, matching, info)
}

// confirmUnconfirmed acks the last unconfirmed prompt once the agent's
// completion_seq advanced past its pre-prompt value, i.e. the agent finished
// a turn after the text was typed. It reports whether it acked.
//
// state_change_seq movement alone and a working status alone are NOT
// confirmation: both also move while an agent relaunches, which is exactly
// when typed text is dropped (SC3). A user-started turn completing while
// our text was dropped could confirm falsely (T-03-43, accepted).
func (w *worker) confirmUnconfirmed(info herdr.AgentInfo, subs []store.Subscription) bool {
	u := w.unconfirmed
	if u == nil || info.CompletionSeq == nil {
		return false
	}
	if u.completion != nil && *info.CompletionSeq <= *u.completion {
		return false
	}
	present := make(map[string]bool, len(subs))
	for _, s := range subs {
		present[s.ID] = true
	}
	acks := make(map[string]uint64, len(u.acks))
	for id, seq := range u.acks {
		if present[id] {
			acks[id] = seq
		}
	}
	w.log.Info("delivered (confirmed by completed turn)", "subs", len(acks), "attempts", w.unconfirmedRuns)
	w.unconfirmed, w.unconfirmedRuns = nil, 0
	if len(acks) > 0 {
		w.ack(acks)
	}
	return true
}

// unconfirmedBackoff is Recheck doubled per consecutive unconfirmed prompt,
// capped at maxUnconfirmedBackoff.
func (w *worker) unconfirmedBackoff() time.Duration {
	d := w.cfg.Recheck
	for i := 1; i < w.unconfirmedRuns && d < maxUnconfirmedBackoff; i++ {
		d *= 2
	}
	return min(d, maxUnconfirmedBackoff)
}

// waitFree waits, bounded, for a working or blocked agent to become idle or
// done. Every result (success, timeout, agent_not_running) re-runs
// agent.get in the caller's loop.
func (w *worker) waitFree(ctx context.Context) {
	_, err := w.cl.AgentWait(ctx, w.pane.PaneID, untilFree, w.cfg.WaitTimeout)
	if err == nil || ctx.Err() != nil {
		return
	}
	switch herdr.Code(err) {
	case herdr.CodeTimeout, herdr.CodeAgentNotRunning, herdr.CodeAgentNotFound:
		return
	}
	if herdr.IsUnreachable(err) {
		return // agent.get reports it
	}
	// Anything else could repeat at once: back off instead of spinning.
	w.log.Warn("agent.wait failed", "err", err)
	w.sleep(ctx, w.cfg.Recheck)
}

// noAgent handles agent_not_found: a gone pane (after the restore grace)
// cancels every subscription of the pane and returns true; a bare shell or
// a pane not yet restored is held.
func (w *worker) noAgent(ctx context.Context, subs []store.Subscription) bool {
	_, err := w.cl.PaneGet(ctx, w.pane.PaneID)
	switch {
	case ctx.Err() != nil:
		return true
	case err == nil:
		// The pane exists without an agent (D-14): keep queued.
		w.markCannot(w.now())
		w.sleep(ctx, w.cfg.Recheck)
		return false
	case herdr.IsUnreachable(err):
		w.markCannot(w.downSince())
		w.sleep(ctx, w.cfg.Recheck)
		return false
	case herdr.Code(err) == herdr.CodePaneNotFound:
		wait := w.cfg.Recheck
		if w.link.Up() {
			upFor := w.now().Sub(w.link.UpSince())
			if upFor >= w.cfg.RestoreGrace {
				failed := false
				for _, s := range subs {
					if w.cancel(s, poller.ReasonPaneGone) != nil {
						failed = true
					}
				}
				if failed {
					// Exiting with subs left would make the Manager respawn
					// the worker at once (WR-03): hold and retry instead.
					w.markCannot(w.now())
					w.sleep(ctx, w.cfg.Recheck)
					return false
				}
				w.markReceivable()
				return true
			}
			wait = w.cfg.RestoreGrace - upFor
		}
		w.markCannot(w.now())
		w.sleep(ctx, wait)
		return false
	default:
		w.log.Warn("pane.get failed", "err", err)
		w.markCannot(w.now())
		w.sleep(ctx, w.cfg.Recheck)
		return false
	}
}

// identify applies D-13 to every sub: mismatches are cancelled, matches are
// returned, and held reports whether any sub is waiting for its session to
// be identified. failed reports whether any mismatch cancel failed (the sub
// still exists).
func (w *worker) identify(subs []store.Subscription, info herdr.AgentInfo) (matching []store.Subscription, held, failed bool) {
	for _, s := range subs {
		switch checkIdentity(s, info) {
		case identMatch:
			matching = append(matching, s)
		case identMismatch:
			if w.cancel(s, poller.ReasonIdentityMismatch) != nil {
				failed = true
			}
		case identUnidentified:
			held = true
		}
	}
	return matching, held, failed
}

// cancel cancels s. It returns nil on success or when the sub is already
// gone, and otherwise logs and returns the error (the sub still exists).
func (w *worker) cancel(s store.Subscription, reason string) error {
	// The engine logs the cancellation (D-17); nothing is typed.
	err := w.eng.Cancel(s.ID, reason)
	if err == nil || errors.Is(err, poller.ErrUnknownSub) {
		return nil
	}
	w.log.Error("cancel failed", "sub", s.ID, "reason", reason, "err", err)
	return err
}

// deliver composes every pending update of subs into one message, prompts
// once and acks only what the message carried (D-04, D-05). before is the
// agent snapshot the prompt is typed on.
func (w *worker) deliver(ctx context.Context, subs []store.Subscription, before herdr.AgentInfo) {
	var groups []compose.Group
	unrenderable := 0
	for _, s := range subs {
		d, err := w.eng.Pending(s.ID)
		if errors.Is(err, poller.ErrUnknownSub) {
			continue
		}
		if err != nil {
			w.log.Error("pending failed", "sub", s.ID, "err", err)
			continue
		}
		if len(d.Events) == 0 && d.Dropped == 0 {
			continue
		}
		g := compose.Group{SubID: s.ID, Events: d.Events, Dropped: d.Dropped, DroppedSeq: d.DroppedSeq, UpToSeq: d.UpToSeq}
		if v, ok := w.eng.PRView(s.PR); ok {
			g.Ref = v.Ref
			g.View = compose.View{HeadOID: v.HeadOID, Mergeable: v.Mergeable, State: v.State, Baselined: v.Baselined}
		} else {
			ref := refFromKey(s.PR)
			if !ref.Valid() {
				// Never prompted, never acked (IN-04).
				w.log.Error("pending update has no renderable PR, kept queued", "sub", s.ID, "pr", s.PR)
				unrenderable++
				continue
			}
			g.Ref = ref
		}
		groups = append(groups, g)
	}
	if len(groups) == 0 {
		if unrenderable > 0 {
			// Nothing else to send: wait instead of re-gating every quiet
			// period.
			w.sleep(ctx, w.cfg.Recheck)
		}
		return
	}
	res := compose.Message(groups, compose.Limits{MaxBytes: w.cfg.MaxBytes})
	if res.Text == "" {
		// Everything was superseded: ack silently, type nothing.
		w.ack(res.Acks)
		return
	}
	_, err := w.cl.AgentPrompt(ctx, herdr.PromptParams{
		Target: w.pane.PaneID,
		Text:   res.Text,
		Wait:   &herdr.WaitOpts{Until: []string{herdr.StatusWorking}, TimeoutMs: w.cfg.PromptWait.Milliseconds()},
	})
	switch {
	case err == nil:
		w.unconfirmed, w.unconfirmedRuns = nil, 0
	case herdr.Code(err) == herdr.CodeTimeout:
		// herdr accepted the text but never saw the agent start working.
		// A just-relaunched agent can report idle and drop it (SC3,
		// CR-01), so this is not delivery: keep the updates queued and
		// re-send with backoff unless a completed turn confirms it. A
		// duplicate is accepted, a loss is not (D-05).
		var completion *uint64
		if before.CompletionSeq != nil {
			c := *before.CompletionSeq
			completion = &c
		}
		w.unconfirmed = &unconfirmedPrompt{acks: res.Acks, completion: completion}
		w.unconfirmedRuns++
		w.log.Warn("prompt not confirmed (working not observed), kept queued",
			"lines", res.Lines, "subs", len(groups), "attempt", w.unconfirmedRuns)
		// Poke-interruptible: an agent that starts working is re-gated
		// at once.
		w.sleep(ctx, w.unconfirmedBackoff())
		return
	default:
		if ctx.Err() == nil {
			w.log.Info("prompt not delivered, kept queued", "code", herdr.Code(err),
				"unreachable", herdr.IsUnreachable(err), "err", err)
		}
		return
	}
	w.log.Info("delivered", "lines", res.Lines, "held", res.Held, "subs", len(groups))
	w.log.Debug("delivered text", "text", res.Text)
	w.ack(res.Acks)
}

func (w *worker) ack(acks map[string]uint64) {
	if err := w.eng.AckMany(acks); err != nil {
		// Re-delivery is accepted (D-05, at-least-once).
		w.log.Error("ack failed", "subs", len(acks), "err", err)
	}
}

// paneSubs returns the subscriptions of this worker's (socket, pane).
func (w *worker) paneSubs() []store.Subscription {
	sock := filepath.Clean(w.pane.HerdrSocket)
	var out []store.Subscription
	for _, s := range w.eng.List() {
		if s.PaneID == w.pane.PaneID && filepath.Clean(s.HerdrSocket) == sock {
			out = append(out, s)
		}
	}
	return out
}

// anyPending reports whether any sub has pending events or a drop marker.
func (w *worker) anyPending(subs []store.Subscription) bool {
	for _, s := range subs {
		d, err := w.eng.Pending(s.ID)
		if err == nil && (len(d.Events) > 0 || d.Dropped > 0) {
			return true
		}
	}
	return false
}

// sleep waits for d, a signal or ctx. timedOut is true only when d elapsed.
func (w *worker) sleep(ctx context.Context, d time.Duration) (timedOut bool) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-w.sig:
		return false
	case <-t.C:
		return true
	}
}

func (w *worker) downSince() time.Time {
	if t := w.link.DownSince(); !t.IsZero() {
		return t
	}
	return w.now()
}

// markCannot records that the pane cannot receive, keeping the earliest
// since of the current run, and reports only on change (LIFE-08).
func (w *worker) markCannot(since time.Time) {
	if !w.cannotSince.IsZero() && !since.Before(w.cannotSince) {
		return
	}
	w.cannotSince = since
	w.report(w.pane, since)
}

// markReceivable records that the pane can receive and reports on change.
func (w *worker) markReceivable() {
	if w.cannotSince.IsZero() {
		return
	}
	w.cannotSince = time.Time{}
	w.report(w.pane, time.Time{})
}

// gateSnapshot is what must not change during the quiet period. idle and
// done are one class (D-08).
type gateSnapshot struct {
	free          bool
	stateSeq      uint64
	hasCompletion bool
	completion    uint64
}

func snapshotOf(info herdr.AgentInfo) gateSnapshot {
	s := gateSnapshot{
		free:     info.Status == herdr.StatusIdle || info.Status == herdr.StatusDone,
		stateSeq: info.StateChangeSeq,
	}
	if info.CompletionSeq != nil {
		s.hasCompletion, s.completion = true, *info.CompletionSeq
	}
	return s
}

// refFromKey parses a PR key ("owner/repo#n") for rendering when the PR has
// no polled view.
func refFromKey(key string) model.PRRef {
	slash := strings.IndexByte(key, '/')
	hash := strings.LastIndexByte(key, '#')
	if slash <= 0 || hash <= slash {
		return model.PRRef{}
	}
	n, _ := strconv.Atoi(key[hash+1:])
	return model.PRRef{Owner: key[:slash], Repo: key[slash+1 : hash], Number: n}
}
