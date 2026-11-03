package poller

import (
	"errors"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/store"
)

// fired reports whether ch holds a value, consuming it.
func fired(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func (s *memSaver) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

func TestPendingWake(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	fired(h.e.Wake()) // the new record pokes the scheduler; not under test
	if fired(h.e.PendingWake()) {
		t.Fatal("PendingWake fired on a plain create")
	}

	h.pollNow(prA) // silent baseline
	if fired(h.e.PendingWake()) {
		t.Error("PendingWake fired after a silent baseline")
	}

	h.f.addComment(prA, "IC_1", "bob", "User", h.at())
	h.pollNow(prA)
	if !fired(h.e.PendingWake()) {
		t.Error("PendingWake did not fire after new pending events")
	}
	if fired(h.e.PendingWake()) {
		t.Error("PendingWake fired twice for one commit")
	}
	if fired(h.e.Wake()) {
		t.Error("a commit with new events poked the scheduler's Wake")
	}

	h.clk.Advance(time.Minute)
	h.pollNow(prA)
	if fired(h.e.PendingWake()) {
		t.Error("PendingWake fired after a poll with nothing new")
	}

	// Refilter of an existing subscription.
	h.sub(prA, "w1:p1", model.EventComment)
	if !fired(h.e.PendingWake()) {
		t.Error("PendingWake did not fire on the Subscribe refilter path")
	}
	if fired(h.e.Wake()) {
		t.Error("refilter poked the scheduler's Wake")
	}

	// A gap-lost drop marker also pokes.
	if err := h.e.Ack(info.SubID, h.pending(info.SubID).UpToSeq); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 110; i++ {
		h.f.addComment(prA, "IC_g"+strconv.Itoa(i), "bob", "User", h.at())
	}
	h.pollNow(prA)
	d := h.pending(info.SubID)
	if d.Dropped == 0 {
		t.Fatalf("no drop marker after a persistent gap: %+v", d)
	}
	if !fired(h.e.PendingWake()) {
		t.Error("PendingWake did not fire after a gap-lost marker")
	}
}

func TestPendingWakeMarkerOnly(t *testing.T) {
	// A gap-lost marker with no new events still pokes. Mask the sub to a
	// type the gap does not produce, so appendPending adds nothing.
	h := newHarness(t)
	info := h.sub(prA, "w1:p1", model.EventConflict)
	h.pollNow(prA)
	fired(h.e.PendingWake())
	for i := 0; i < 110; i++ {
		h.f.addComment(prA, "IC_"+strconv.Itoa(i), "bob", "User", h.at())
	}
	h.pollNow(prA)
	d := h.pending(info.SubID)
	if len(d.Events) != 0 || d.Dropped == 0 {
		t.Fatalf("pending %d dropped %d, want 0 events and a marker", len(d.Events), d.Dropped)
	}
	if d.DroppedSeq == 0 || d.DroppedSeq != d.UpToSeq {
		t.Errorf("DroppedSeq %d UpToSeq %d", d.DroppedSeq, d.UpToSeq)
	}
	if !fired(h.e.PendingWake()) {
		t.Error("PendingWake did not fire for a marker-only commit")
	}
}

func TestPRView(t *testing.T) {
	h := newHarness(t)
	h.f.setHead(prA, "head-1", "bob")
	h.sub(prA, "w1:p1")
	h.pollNow(prA)
	v, ok := h.e.PRView(store.PRKey(prA))
	if !ok {
		t.Fatal("PRView(watched) not ok")
	}
	mem := h.record(prA).Memory
	want := PRView{Ref: prA, HeadOID: mem.HeadOID, Mergeable: mem.Mergeable, State: mem.State, Baselined: true}
	if v != want {
		t.Errorf("PRView = %+v, want %+v", v, want)
	}
	if v.HeadOID != "head-1" || v.State == "" {
		t.Errorf("PRView missing polled state: %+v", v)
	}
	if _, ok := h.e.PRView("acme/widgets#99"); ok {
		t.Error("PRView(unknown) ok")
	}
}

func TestAckMany(t *testing.T) {
	h := newHarness(t)
	a := h.sub(prA, "w1:p1")
	b := h.sub(prB, "w1:p2")
	h.pollNow(prA)
	h.pollNow(prB)
	h.f.addComment(prA, "IC_a", "bob", "User", h.at())
	h.f.addComment(prB, "IC_b", "bob", "User", h.at())
	h.pollNow(prA)
	h.pollNow(prB)
	da, db := h.pending(a.SubID), h.pending(b.SubID)
	if len(da.Events) != 1 || len(db.Events) != 1 {
		t.Fatalf("pending a %d b %d", len(da.Events), len(db.Events))
	}

	if err := h.e.AckMany(nil); err != nil {
		t.Fatalf("AckMany(empty): %v", err)
	}
	before := h.saver.count()
	err := h.e.AckMany(map[string]uint64{a.SubID: da.UpToSeq, b.SubID: db.UpToSeq, "gone": 7})
	if err != nil {
		t.Fatalf("AckMany: %v", err)
	}
	if n := h.saver.count() - before; n != 1 {
		t.Errorf("AckMany saved %d times, want 1", n)
	}
	if len(h.pending(a.SubID).Events) != 0 || len(h.pending(b.SubID).Events) != 0 {
		t.Error("AckMany left events pending")
	}

	t.Run("save failure", func(t *testing.T) {
		h.f.addComment(prA, "IC_a2", "bob", "User", h.at())
		h.pollNow(prA)
		d := h.pending(a.SubID)
		h.saver.setFail(func(*store.State) bool { return true })
		defer h.saver.setFail(nil)
		if err := h.e.AckMany(map[string]uint64{a.SubID: d.UpToSeq}); err == nil {
			t.Fatal("AckMany with failing save returned nil")
		}
		if got := h.pending(a.SubID); len(got.Events) != len(d.Events) {
			t.Errorf("failed AckMany changed state: %d -> %d events", len(d.Events), len(got.Events))
		}
	})
}

func TestAckManyDropMarker(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Config.Subscriptions.PendingCap = 3 })
	info := h.sub(prA, "w1:p1")
	h.pollNow(prA)
	for i := 0; i < 5; i++ {
		h.f.addComment(prA, "IC_"+strconv.Itoa(i), "bob", "User", h.at())
	}
	h.pollNow(prA)
	d := h.pending(info.SubID)
	if d.Dropped == 0 || d.DroppedSeq == 0 {
		t.Fatalf("no drop marker: %+v", d)
	}
	var maxEv uint64
	for _, p := range d.Events {
		maxEv = max(maxEv, p.Seq)
	}
	if maxEv >= d.DroppedSeq {
		t.Fatalf("event seq %d not below DroppedSeq %d", maxEv, d.DroppedSeq)
	}
	if err := h.e.AckMany(map[string]uint64{info.SubID: maxEv}); err != nil {
		t.Fatal(err)
	}
	if got := h.pending(info.SubID); got.Dropped == 0 || len(got.Events) != 0 {
		t.Fatalf("ack below DroppedSeq: %+v, want marker kept, events cleared", got)
	}
	if err := h.e.AckMany(map[string]uint64{info.SubID: d.DroppedSeq}); err != nil {
		t.Fatal(err)
	}
	if got := h.pending(info.SubID); got.Dropped != 0 || got.DroppedSeq != 0 {
		t.Fatalf("ack at DroppedSeq left marker: %+v", got)
	}
}

func attrs(r slog.Record) map[string]string {
	out := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		out[a.Key] = a.Value.String()
		return true
	})
	return out
}

func TestCancel(t *testing.T) {
	h := newHarness(t)
	a := h.sub(prA, "w1:p1")
	b := h.sub(prA, "w1:p2")
	h.pollNow(prA)
	h.f.addComment(prA, "IC_1", "bob", "User", h.at())
	h.pollNow(prA)

	if err := h.e.Cancel(a.SubID, ReasonPaneClosed); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := h.e.Pending(a.SubID); !errors.Is(err, ErrUnknownSub) {
		t.Errorf("cancelled sub still present: %v", err)
	}
	if _, ok := h.e.PRView(store.PRKey(prA)); !ok {
		t.Error("PR record removed while another sub remains")
	}
	recs := h.logs.named("subscription cancelled")
	if len(recs) != 1 {
		t.Fatalf("%d cancel log records, want 1", len(recs))
	}
	got := attrs(recs[0])
	want := map[string]string{"sub": a.SubID, "pr": store.PRKey(prA), "pane": "w1:p1", "socket": sock, "reason": ReasonPaneClosed}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("log %s = %q, want %q", k, got[k], v)
		}
	}
	cs := h.e.RecentCancellations()
	wantC := Cancellation{SubID: a.SubID, PR: store.PRKey(prA), HerdrSocket: sock, PaneID: "w1:p1", Reason: ReasonPaneClosed, At: h.clk.Now()}
	if len(cs) != 1 || cs[0] != wantC {
		t.Fatalf("RecentCancellations = %+v, want [%+v]", cs, wantC)
	}
	if n := h.e.Status().Cancellations; n != 1 {
		t.Errorf("Status.Cancellations = %d, want 1", n)
	}

	if err := h.e.Cancel(b.SubID, ReasonPaneGone); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.e.PRView(store.PRKey(prA)); ok {
		t.Error("PR record kept after its last sub was cancelled")
	}

	if err := h.e.Cancel("nope", ReasonIdentityMismatch); !errors.Is(err, ErrUnknownSub) {
		t.Errorf("Cancel(unknown) = %v", err)
	}
	if n := len(h.logs.named("subscription cancelled")); n != 2 {
		t.Errorf("%d cancel log records after unknown cancel, want 2", n)
	}

	// The ring keeps only the newest 50.
	for i := 0; i < 60; i++ {
		info := h.sub(prB, "w2:p"+strconv.Itoa(i))
		if err := h.e.Cancel(info.SubID, ReasonPaneGone); err != nil {
			t.Fatal(err)
		}
	}
	cs = h.e.RecentCancellations()
	if len(cs) != 50 {
		t.Fatalf("ring len %d, want 50", len(cs))
	}
	if cs[49].PaneID != "w2:p59" || cs[0].PaneID != "w2:p10" {
		t.Errorf("ring order: first %s last %s", cs[0].PaneID, cs[49].PaneID)
	}
	cs[0].Reason = "mutated"
	if h.e.RecentCancellations()[0].Reason == "mutated" {
		t.Error("RecentCancellations returned the live slice")
	}
}

func TestSubscribeAgentKind(t *testing.T) {
	h := newHarness(t)
	info, err := h.e.Subscribe(SubRequest{PR: prA, HerdrSocket: sock, PaneID: "w1:p1", AgentKind: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	kind := func() string {
		for _, s := range h.e.List() {
			if s.ID == info.SubID {
				return s.AgentKind
			}
		}
		return "<missing>"
	}
	if k := kind(); k != "codex" {
		t.Errorf("AgentKind on create = %q", k)
	}
	if _, err := h.e.Subscribe(SubRequest{PR: prA, HerdrSocket: sock, PaneID: "w1:p1", AgentKind: "claude"}); err != nil {
		t.Fatal(err)
	}
	if k := kind(); k != "claude" {
		t.Errorf("AgentKind on refilter = %q", k)
	}
}
