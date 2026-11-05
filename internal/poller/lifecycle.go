package poller

import (
	"sort"
	"strconv"
	"time"

	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/store"
)

// lifeAction is what a lifecycle pass does to one subscription.
type lifeAction int

const (
	lifeNone lifeAction = iota
	lifeCheckin
	lifeExpire
)

// lifeDue decides what is due for sub at now. Ending subscriptions are on
// their way out and get nothing. Expiry wins over a check-in. A check-in is
// due once the sub has been idle for checkin and the previous check-in (if
// any) is at least checkin old, so check-ins repeat but never stack (D-13).
func lifeDue(sub *store.Subscription, now time.Time, checkin, expire time.Duration) lifeAction {
	if sub.Ending != "" {
		return lifeNone
	}
	idle := now.Sub(sub.ClockAt)
	if expire > 0 && idle >= expire {
		return lifeExpire
	}
	if checkin > 0 && idle >= checkin && now.Sub(laterOf(sub.ClockAt, sub.CheckinAt)) >= checkin {
		return lifeCheckin
	}
	return lifeNone
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// withoutCheckins returns p without pending check-ins, as a new slice (nil
// when nothing remains).
func withoutCheckins(p []store.PendingEvent) []store.PendingEvent {
	var out []store.PendingEvent
	for _, e := range p {
		if e.Event.Type != model.EventCheckin {
			out = append(out, e)
		}
	}
	return out
}

// lifeQueued records one lifecycle event for logging after the save.
type lifeQueued struct {
	action lifeAction
	sub    store.Subscription
}

// LifecycleTick runs one check-in/expiry pass at now (D-11, D-13, D-14,
// D-15). A sub idle for Lifecycle.CheckinAfter gets one check-in event,
// replacing any pending one, and CheckinAt = now. A sub idle for
// Lifecycle.ExpireAfter has its pending check-ins dropped and an expired
// event queued; it becomes ending (Ending = expired, EndSeq = that event), so
// it stops counting for polling at once and is deleted when the expired line
// is acked. Lifecycle events carry IdleSecs equal to the configured threshold.
// They are appended directly, so the mask and the pending cap's drop marker
// do not apply. All changes go through one mutate; when nothing is due no
// Save happens.
func (e *Engine) LifecycleTick(now time.Time) error {
	checkin := e.cfg.Lifecycle.CheckinAfter.Duration
	expire := e.cfg.Lifecycle.ExpireAfter.Duration

	e.mu.Lock()
	due := false
	for _, s := range e.state.Subs {
		if lifeDue(s, now, checkin, expire) != lifeNone {
			due = true
			break
		}
	}
	if !due {
		e.mu.Unlock()
		return nil
	}
	var queued []lifeQueued
	err := e.mutateLocked(func(next *store.State) error {
		queued = queued[:0]
		ids := make([]string, 0, len(next.Subs))
		for id := range next.Subs {
			ids = append(ids, id)
		}
		// Sorted, so Seq numbering is deterministic across subs.
		sort.Strings(ids)
		for _, id := range ids {
			sub := next.Subs[id]
			act := lifeDue(sub, now, checkin, expire)
			if act == lifeNone {
				continue
			}
			sub.Pending = withoutCheckins(sub.Pending)
			next.Seq++
			unix := strconv.FormatInt(now.Unix(), 10)
			ev := model.Event{Type: model.EventCheckin, Key: "checkin:" + unix, IdleSecs: int64(checkin / time.Second)}
			if act == lifeExpire {
				ev = model.Event{Type: model.EventExpired, Key: "expired:" + unix, IdleSecs: int64(expire / time.Second)}
			}
			sub.Pending = append(sub.Pending, store.PendingEvent{Seq: next.Seq, Event: ev, DetectedAt: now})
			if act == lifeExpire {
				sub.Ending, sub.EndSeq = ReasonExpired, next.Seq
			} else {
				sub.CheckinAt = now
			}
			queued = append(queued, lifeQueued{action: act, sub: *sub})
		}
		return nil
	})
	e.mu.Unlock()
	if err != nil {
		return err
	}
	for _, q := range queued {
		msg := "check-in queued"
		if q.action == lifeExpire {
			msg = "subscription expiring"
		}
		e.log.Info(msg, "sub", q.sub.ID, "pr", q.sub.PR, "pane", q.sub.PaneID, "socket", q.sub.HerdrSocket)
	}
	if len(queued) > 0 {
		e.pokePending()
	}
	return nil
}

// LifecycleDue returns when s's next check-in is due and when it expires
// without activity, for ls (D-11, D-13). Both are zero for an ending
// subscription.
func (e *Engine) LifecycleDue(s store.Subscription) (checkinDue, expiresAt time.Time) {
	if s.Ending != "" {
		return time.Time{}, time.Time{}
	}
	checkinDue = laterOf(s.ClockAt, s.CheckinAt).Add(e.cfg.Lifecycle.CheckinAfter.Duration)
	expiresAt = s.ClockAt.Add(e.cfg.Lifecycle.ExpireAfter.Duration)
	return checkinDue, expiresAt
}
