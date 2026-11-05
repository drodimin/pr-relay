package poller

import (
	"context"
	"errors"
	"fmt"

	"github.com/drodimin/pr-relay/internal/diff"
	"github.com/drodimin/pr-relay/internal/ghquery"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/store"
)

// Verify error classes beyond the errClass values (not_found, forbidden,
// unauthorized, no_token, secondary_limit, rate_limited, transient, error).
const (
	// ClassTimeout means the probe did not finish before the caller's
	// deadline.
	ClassTimeout = "timeout"
	// ClassPRClosed means the PR is merged or closed, or the subscription
	// is ending because it was.
	ClassPRClosed = "pr_closed"
)

// errPRClosed is the cause of a pr_closed VerifyError.
var errPRClosed = errors.New("pull request is not open")

// VerifyError is a failed subscribe-time verification (D-08, SUB-07).
// Nothing was persisted. Class is one of the errClass values, ClassTimeout
// or ClassPRClosed.
type VerifyError struct {
	Class string
	Err   error
}

func (e *VerifyError) Error() string {
	return "poller: verify PR: " + e.Class + ": " + fmt.Sprint(e.Err)
}

func (e *VerifyError) Unwrap() error { return e.Err }

// verifyErr classifies a probe failure. A deadline is a timeout whatever
// the transport wrapped it in.
func verifyErr(ctx context.Context, err error) *VerifyError {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &VerifyError{Class: ClassTimeout, Err: err}
	}
	return &VerifyError{Class: errClass(err), Err: err}
}

// probed is a successful probe: an OPEN snapshot and the batch's viewer.
type probed struct {
	snap   model.Snapshot
	viewer string
}

// SubscribeVerified subscribes like Subscribe, but only once GitHub proved
// the PR exists, is readable and is OPEN (D-08, SUB-07). ctx bounds the
// probe; the daemon gives it about five seconds.
//
// A PR already watched, baselined, OPEN and error-free is joined without a
// GitHub call. Otherwise one forced, budget-gated batch fetches the PR; its
// snapshot becomes the silent baseline of a new record, with LastPollAt and
// RateAnchor set to now, so the first scheduled poll is about one minimum
// interval later and finds nothing new (SUB-08). A gate refusal fails at once
// with rate_limited or secondary_limit and never waits.
//
// Every failure is a *VerifyError and leaves state, on disk and in memory,
// untouched. A merged or closed PR fails with pr_closed. Re-subscribing an
// expired-ending subscription revives it (see subscribeIn).
func (e *Engine) SubscribeVerified(ctx context.Context, req SubRequest) (SubInfo, error) {
	if !req.PR.Valid() {
		return SubInfo{}, fmt.Errorf("poller: invalid pull request reference %q", req.PR.String())
	}
	mask, err := normalizeMask(req.Events)
	if err != nil {
		return SubInfo{}, err
	}
	if info, done, err := e.subscribeVerifiedCommit(req, mask, nil); done {
		return info, err
	}

	// Probes run one at a time, like polls. Re-check under pollMu: a
	// concurrent caller may have just baselined the PR, and this one can
	// then join without spending a request.
	e.pollMu.Lock()
	defer e.pollMu.Unlock()
	if info, done, err := e.subscribeVerifiedCommit(req, mask, nil); done {
		return info, err
	}
	p, verr := e.probeLocked(ctx, req.PR)
	if verr != nil {
		return SubInfo{}, verr
	}
	info, _, err := e.subscribeVerifiedCommit(req, mask, p)
	return info, err
}

// probeLocked fetches ref once, through the budget gate as a forced batch,
// and returns its snapshot when it is OPEN. The caller holds pollMu.
func (e *Engine) probeLocked(ctx context.Context, ref model.PRRef) (*probed, *VerifyError) {
	e.mu.Lock()
	now := e.now()
	if w := e.budget.Gate(now, true); w > 0 {
		class := "rate_limited"
		if now.Before(e.budget.SecondaryUntil) {
			class = "secondary_limit"
		}
		e.mu.Unlock()
		return nil, &VerifyError{Class: class, Err: &RateLimitedError{Until: now.Add(w)}}
	}
	e.mu.Unlock()

	res, err := e.fetch.Batch(ctx, []model.PRRef{ref}, ghquery.NormalWindow)
	if err != nil {
		if ctx.Err() == nil {
			e.applyRateLimit(now, err)
		}
		e.log.Warn("subscribe probe failed", "pr", ref.String(), "error", errClass(err))
		return nil, verifyErr(ctx, err)
	}
	e.mu.Lock()
	e.budget.Record(now, res.RateLimit.Cost)
	e.budget.Observe(res.RateLimit.Limit, res.RateLimit.Remaining, res.ResetAt, res.RateLimit.Cost, 1)
	e.budget.OnClean()
	e.mu.Unlock()

	it := itemFor(res.Items, 0, ref)
	if it.Err != nil {
		return nil, verifyErr(ctx, it.Err)
	}
	if it.Snapshot.State != "OPEN" {
		return nil, &VerifyError{Class: ClassPRClosed, Err: fmt.Errorf("%w: %s is %s", errPRClosed, ref.String(), it.Snapshot.State)}
	}
	return &probed{snap: it.Snapshot, viewer: res.Viewer}, nil
}

// subscribeVerifiedCommit persists the subscription when it can. With p nil
// it only joins a baselined, OPEN, error-free record and reports done=false
// when a probe is needed. With p set it creates the record from the probe
// when it is still absent; a record that appeared meanwhile keeps its own
// Memory (adopting the probe baseline only if it had none).
func (e *Engine) subscribeVerifiedCommit(req SubRequest, mask []model.EventType, p *probed) (SubInfo, bool, error) {
	key := store.PRKey(req.PR)
	e.mu.Lock()
	rec := e.state.PRs[key]
	if rec != nil && rec.Memory.Baselined && rec.Memory.State != "OPEN" {
		e.mu.Unlock()
		return SubInfo{}, true, &VerifyError{Class: ClassPRClosed, Err: fmt.Errorf("%w: %s is %s", errPRClosed, req.PR.String(), rec.Memory.State)}
	}
	if p == nil && (rec == nil || !rec.Memory.Baselined || rec.LastError != "") {
		e.mu.Unlock()
		return SubInfo{}, false, nil
	}

	now := e.now()
	var id string
	created, newRecord := false, false
	err := e.mutateLocked(func(next *store.State) error {
		r := next.PRs[key]
		switch {
		case r == nil:
			next.NextGen++
			next.PRs[key] = &store.PRRecord{
				Ref:        req.PR,
				Gen:        next.NextGen,
				CreatedAt:  now,
				Memory:     diff.Step(model.Memory{}, p.snap).Memory,
				LastPollAt: now,
				RateAnchor: now,
			}
			newRecord = true
		case !r.Memory.Baselined && p != nil:
			r.Memory = diff.Step(model.Memory{}, p.snap).Memory
			r.LastPollAt = now
			r.LastError = ""
		}
		if p != nil && p.viewer != "" && next.Viewer == "" {
			next.Viewer = p.viewer
		}
		var err error
		id, created, err = e.subscribeIn(next, req, mask, now)
		return err
	})
	if err != nil {
		e.mu.Unlock()
		return SubInfo{}, true, err
	}
	info := e.subInfoLocked(req, id, created, now)
	e.mu.Unlock()
	if newRecord {
		// Not due yet, but the scheduler must learn the new next-due time.
		e.poke()
	}
	if !created {
		e.pokePending()
	}
	e.log.Info("subscribed", "sub", id, "pr", key, "pane", req.PaneID, "created", created, "new_pr", newRecord)
	return info, true, nil
}
