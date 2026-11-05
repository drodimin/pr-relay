package poller

// Fan-out rules. Everything in this file is pure: no clock (now is an
// argument), no I/O, no locking.
//
//   - EVT-08: events that are the user's own activity (filter.ExcludeSelf)
//     are never delivered.
//   - D-12 / A6: activity is PR-level. It is any event that survives
//     ExcludeSelf, or any raw commits event, including the user's own pushes
//     (a push restarts CI, which is worth watching closely). Own comments and
//     reviews are not activity. Subscriber masks play no part: one PR has one
//     shared cadence, whatever each pane chose to hear about.
//   - D-18: re-subscribing replaces the mask and re-filters the pending queue
//     to it, keeping order.
//   - D-19: each subscription's pending queue is mask-filtered, deduplicated
//     by Event.Key, numbered from the global State.Seq, and capped (200 by
//     default). Overflow drops the oldest events and records a dropped
//     marker (Dropped count plus DroppedSeq). Pending events have no TTL.

import (
	"time"

	"github.com/drodimin/pr-relay/internal/config"
	"github.com/drodimin/pr-relay/internal/filter"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/store"
)

// maskAllows reports whether a subscription with mask wants events of type
// t. A nil or empty mask means every type. closed is always delivered,
// whatever the mask, because it ends the subscription (D-16); the synthetic
// lifecycle events (check-in, expired) bypass the mask too.
func maskAllows(mask []model.EventType, t model.EventType) bool {
	if t == model.EventClosed || t.IsLifecycle() {
		return true
	}
	if len(mask) == 0 {
		return true
	}
	for _, m := range mask {
		if m == t {
			return true
		}
	}
	return false
}

// classify splits one Step's raw events into the set delivered to
// subscribers (EVT-08) and whether the step counts as D-12 activity.
func classify(raw []model.Event, viewer string) (delivered []model.Event, activity bool) {
	delivered = filter.ExcludeSelf(raw, viewer)
	if len(delivered) > 0 {
		return delivered, true
	}
	for _, e := range raw {
		if e.Type == model.EventCommits {
			return delivered, true
		}
	}
	return delivered, false
}

// appendPending appends the events of evs allowed by sub's mask and not
// already pending (by Key), numbering each with ++*seq. If the queue then
// exceeds limit, the oldest events are dropped, Dropped grows by the number
// dropped and DroppedSeq is set once to ++*seq. A non-positive limit means
// the default cap. It returns how many events were appended.
func appendPending(sub *store.Subscription, evs []model.Event, seq *uint64, limit int, now time.Time) (added int) {
	if limit <= 0 {
		limit = config.DefaultPendingCap
	}
	have := make(map[string]bool, len(sub.Pending)+len(evs))
	for _, p := range sub.Pending {
		have[p.Event.Key] = true
	}
	for _, e := range evs {
		if !maskAllows(sub.Events, e.Type) || have[e.Key] {
			continue
		}
		have[e.Key] = true
		*seq++
		sub.Pending = append(sub.Pending, store.PendingEvent{Seq: *seq, Event: e, DetectedAt: now})
		added++
	}
	if over := len(sub.Pending) - limit; over > 0 {
		kept := make([]store.PendingEvent, limit)
		copy(kept, sub.Pending[over:])
		sub.Pending = kept
		sub.Dropped += over
		*seq++
		sub.DroppedSeq = *seq
	}
	return added
}

// refilter returns the events of p allowed by mask, in order, as a new
// slice (nil when none remain). p is not modified.
func refilter(p []store.PendingEvent, mask []model.EventType) []store.PendingEvent {
	var out []store.PendingEvent
	for _, e := range p {
		if maskAllows(mask, e.Event.Type) {
			out = append(out, e)
		}
	}
	return out
}
