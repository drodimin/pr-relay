package poller

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/store"
)

// liveSub returns a copy of the live subscription id, or ok=false when it
// no longer exists.
func (h *harness) liveSub(id string) (store.Subscription, bool) {
	h.t.Helper()
	h.e.mu.Lock()
	defer h.e.mu.Unlock()
	s := h.e.state.Subs[id]
	if s == nil {
		return store.Subscription{}, false
	}
	return copySub(s), true
}

// setSub mutates the live subscription id in place (test-only shortcut).
func (h *harness) setSub(id string, fn func(s *store.Subscription)) {
	h.t.Helper()
	h.e.mu.Lock()
	defer h.e.mu.Unlock()
	s := h.e.state.Subs[id]
	if s == nil {
		h.t.Fatalf("no sub %s", id)
	}
	fn(s)
}

// planned reports whether planLocked, run a day from now, would poll ref.
func (h *harness) planned(ref model.PRRef) bool {
	h.t.Helper()
	h.e.mu.Lock()
	defer h.e.mu.Unlock()
	keys, _ := h.e.planLocked(h.clk.Now().Add(48 * time.Hour))
	return slices.Contains(keys, store.PRKey(ref))
}

func closePR(pr map[string]any) { pr["state"] = "CLOSED" }

func TestMaskBypass(t *testing.T) {
	mask := []model.EventType{model.EventCIFailed}
	for _, typ := range []model.EventType{model.EventClosed, model.EventCheckin, model.EventExpired} {
		if !maskAllows(mask, typ) {
			t.Errorf("mask [ci.failed] rejects %s", typ)
		}
	}
	p := []store.PendingEvent{
		{Seq: 1, Event: model.Event{Type: model.EventComment, Key: "c"}},
		{Seq: 2, Event: model.Event{Type: model.EventClosed, Key: "closed"}},
	}
	got := refilter(p, mask)
	if len(got) != 1 || got[0].Event.Type != model.EventClosed {
		t.Fatalf("refilter = %+v, want only closed", got)
	}

	h := newHarness(t)
	info := h.sub(prA, "w1:p1", model.EventCIFailed)
	h.pollNow(prA)
	h.f.mutate(prA, closePR)
	h.clk.Advance(time.Minute)
	h.pollNow(prA)
	d := h.pending(info.SubID)
	if len(d.Events) != 1 || d.Events[0].Event.Type != model.EventClosed {
		t.Fatalf("pending = %+v, want one closed despite the mask", d.Events)
	}
}

func TestClosedRemoval(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	h.pollNow(prA)
	h.f.mutate(prA, closePR)
	h.clk.Advance(time.Minute)
	h.pollNow(prA)

	sub, ok := h.liveSub(info.SubID)
	if !ok {
		t.Fatal("sub gone before ack")
	}
	var closedSeq uint64
	for _, p := range sub.Pending {
		if p.Event.Type == model.EventClosed {
			closedSeq = p.Seq
		}
	}
	if closedSeq == 0 {
		t.Fatalf("no closed event pending: %+v", sub.Pending)
	}
	if sub.Ending != "closed" || sub.EndSeq != closedSeq {
		t.Fatalf("Ending %q EndSeq %d, want closed %d", sub.Ending, sub.EndSeq, closedSeq)
	}

	if err := h.e.AckMany(map[string]uint64{info.SubID: closedSeq - 1}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.liveSub(info.SubID); !ok {
		t.Fatal("ack below EndSeq removed the sub")
	}
	if n := len(h.e.RecentCancellations()); n != 0 {
		t.Fatalf("cancellations = %d before the final ack", n)
	}

	if err := h.e.AckMany(map[string]uint64{info.SubID: closedSeq}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.liveSub(info.SubID); ok {
		t.Fatal("ack of EndSeq kept the sub")
	}
	cs := h.e.RecentCancellations()
	if len(cs) != 1 {
		t.Fatalf("cancellations = %+v, want one", cs)
	}
	c := cs[0]
	if c.Reason != ReasonClosed || c.SubID != info.SubID || c.PR != store.PRKey(prA) ||
		c.PaneID != "w1:p1" || c.HerdrSocket != sock || !c.At.Equal(h.clk.Now()) {
		t.Fatalf("cancellation = %+v", c)
	}
	if len(h.logs.named("subscription ended")) != 1 {
		t.Error("removal not logged as subscription ended")
	}

	// A reopen is not tracked: no sub remains and the PR is not polled.
	h.f.mutate(prA, func(pr map[string]any) { pr["state"] = "OPEN" })
	if h.planned(prA) {
		t.Fatal("PR with no remaining sub still planned")
	}
	if n := len(h.e.List()); n != 0 {
		t.Fatalf("List = %d subs, want 0", n)
	}
}

func TestClosedRemovalAck(t *testing.T) {
	// Engine.Ack (single sub) ends the sub too, with reason expired when the
	// final event was an expiry.
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	h.pollNow(prA)
	h.setSub(info.SubID, func(s *store.Subscription) {
		h.e.state.Seq++
		s.Pending = append(s.Pending, store.PendingEvent{Seq: h.e.state.Seq, Event: model.Event{Type: model.EventExpired, Key: "expired:1"}})
		s.Ending, s.EndSeq = "expired", h.e.state.Seq
	})
	sub, _ := h.liveSub(info.SubID)
	h.clk.Advance(time.Minute)
	if err := h.e.Ack(info.SubID, sub.EndSeq); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.liveSub(info.SubID); ok {
		t.Fatal("ack of expired EndSeq kept the sub")
	}
	cs := h.e.RecentCancellations()
	if len(cs) != 1 || cs[0].Reason != ReasonExpired {
		t.Fatalf("cancellations = %+v, want one expired", cs)
	}
}

func TestEndingExcluded(t *testing.T) {
	h := newHarness(t)
	a := h.sub(prA, "w1:p1")
	b := h.sub(prA, "w1:p2")
	h.pollNow(prA)
	h.setSub(a.SubID, func(s *store.Subscription) { s.Ending, s.EndSeq = "closed", 999 })

	if !h.planned(prA) {
		t.Fatal("PR with a live sub not planned")
	}
	co := h.e.CoSubscribers(prA)
	if len(co) != 1 || co[0].PaneID != "w1:p2" {
		t.Fatalf("CoSubscribers = %+v, want only w1:p2", co)
	}
	if got := h.e.Status().Subs; got != 1 {
		t.Fatalf("Status.Subs = %d, want 1", got)
	}

	h.f.addComment(prA, "IC_1", "bob", "User", h.at())
	h.pollNow(prA)
	if d := h.pending(a.SubID); len(d.Events) != 0 {
		t.Errorf("ending sub received %+v", d.Events)
	}
	if d := h.pending(b.SubID); len(d.Events) != 1 || d.Events[0].Event.Type != model.EventComment {
		t.Errorf("live sub pending = %+v, want one comment", d.Events)
	}

	if err := h.e.Unsubscribe(b.SubID); err != nil {
		t.Fatal(err)
	}
	if h.planned(prA) {
		t.Fatal("PR with only an ending sub still planned")
	}
}

func TestAckClockReset(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	h.pollNow(prA)
	h.f.addComment(prA, "IC_1", "bob", "User", h.at())
	h.pollNow(prA)
	d := h.pending(info.SubID)
	if len(d.Events) != 1 {
		t.Fatalf("pending = %+v", d.Events)
	}

	h.clk.Advance(time.Hour)
	T := h.clk.Now()
	if err := h.e.AckMany(map[string]uint64{info.SubID: d.UpToSeq}); err != nil {
		t.Fatal(err)
	}
	sub, _ := h.liveSub(info.SubID)
	if !sub.ClockAt.Equal(T) || !sub.LastEventAt.Equal(T) || sub.LastEventType != model.EventComment {
		t.Fatalf("ClockAt %v LastEventAt %v LastEventType %q, want %v/%v/comment",
			sub.ClockAt, sub.LastEventAt, sub.LastEventType, T, T)
	}

	// Acking only a check-in leaves the clock alone.
	h.setSub(info.SubID, func(s *store.Subscription) {
		h.e.state.Seq++
		s.Pending = append(s.Pending, store.PendingEvent{Seq: h.e.state.Seq, Event: model.Event{Type: model.EventCheckin, Key: "checkin:1"}})
	})
	d = h.pending(info.SubID)
	h.clk.Advance(time.Hour)
	if err := h.e.Ack(info.SubID, d.UpToSeq); err != nil {
		t.Fatal(err)
	}
	sub, _ = h.liveSub(info.SubID)
	if !sub.ClockAt.Equal(T) || !sub.LastEventAt.Equal(T) || sub.LastEventType != model.EventComment {
		t.Fatalf("check-in ack moved the clock: ClockAt %v LastEventAt %v type %q", sub.ClockAt, sub.LastEventAt, sub.LastEventType)
	}
	if len(sub.Pending) != 0 {
		t.Fatalf("check-in not acked: %+v", sub.Pending)
	}
}

func TestKeepLifecycle(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	h.pollNow(prA)
	h.setSub(info.SubID, func(s *store.Subscription) {
		h.e.state.Seq++
		s.Pending = append(s.Pending, store.PendingEvent{Seq: h.e.state.Seq, Event: model.Event{Type: model.EventComment, Key: "comment:1"}})
		h.e.state.Seq++
		s.Pending = append(s.Pending, store.PendingEvent{Seq: h.e.state.Seq, Event: model.Event{Type: model.EventCheckin, Key: "checkin:1"}})
		s.CheckinAt = h.clk.Now()
	})
	h.clk.Advance(time.Hour)
	if err := h.e.Keep(info.SubID); err != nil {
		t.Fatalf("Keep: %v", err)
	}
	sub, _ := h.liveSub(info.SubID)
	if !sub.ClockAt.Equal(h.clk.Now()) || !sub.CheckinAt.IsZero() {
		t.Fatalf("ClockAt %v CheckinAt %v", sub.ClockAt, sub.CheckinAt)
	}
	if len(sub.Pending) != 1 || sub.Pending[0].Event.Type != model.EventComment {
		t.Fatalf("pending after keep = %+v, want only the comment", sub.Pending)
	}

	h.setSub(info.SubID, func(s *store.Subscription) { s.Ending, s.EndSeq = "expired", 999 })
	before, _ := h.liveSub(info.SubID)
	saves := h.saver.count()
	h.clk.Advance(time.Hour)
	if err := h.e.Keep(info.SubID); !errors.Is(err, ErrEnded) {
		t.Fatalf("Keep(ending) = %v, want ErrEnded", err)
	}
	after, _ := h.liveSub(info.SubID)
	if !reflect.DeepEqual(before, after) || h.saver.count() != saves {
		t.Fatal("Keep on an ending sub changed state")
	}
}
