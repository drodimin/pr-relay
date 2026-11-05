package poller

import (
	"context"
	"log/slog"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/drodimin/pr-relay/internal/config"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/store"
)

// lifeTCheckin and lifeTExpire are the default thresholds (D-15).
const (
	lifeTCheckin = 24 * time.Hour
	lifeTExpire  = 7 * 24 * time.Hour
)

// lifeTSaves returns how many Saves the harness saver has accepted.
func lifeTSaves(h *harness) int {
	h.saver.mu.Lock()
	defer h.saver.mu.Unlock()
	return h.saver.saves
}

// lifeTTick runs LifecycleTick at t0+d and fails the test on error.
func lifeTTick(h *harness, d time.Duration) {
	h.t.Helper()
	if err := h.e.LifecycleTick(t0.Add(d)); err != nil {
		h.t.Fatalf("LifecycleTick(t0+%v): %v", d, err)
	}
}

// lifeTOfType returns the pending events of sub id with type typ.
func lifeTOfType(h *harness, id string, typ model.EventType) []store.PendingEvent {
	h.t.Helper()
	var out []store.PendingEvent
	for _, p := range h.pending(id).Events {
		if p.Event.Type == typ {
			out = append(out, p)
		}
	}
	return out
}

func TestLifecycleCheckin(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	saves := lifeTSaves(h)

	lifeTTick(h, 23*time.Hour+59*time.Minute)
	if n := len(h.pending(info.SubID).Events); n != 0 {
		t.Fatalf("pending before 24h = %d, want 0", n)
	}
	if got := lifeTSaves(h); got != saves {
		t.Fatalf("saves = %d, want %d (nothing due must not save)", got, saves)
	}

	lifeTTick(h, lifeTCheckin)
	cs := lifeTOfType(h, info.SubID, model.EventCheckin)
	if len(cs) != 1 {
		t.Fatalf("check-ins at 24h = %+v, want one", cs)
	}
	at := t0.Add(lifeTCheckin)
	if want := "checkin:" + strconv.FormatInt(at.Unix(), 10); cs[0].Event.Key != want {
		t.Errorf("Key = %q, want %q", cs[0].Event.Key, want)
	}
	if cs[0].Event.IdleSecs != 86400 {
		t.Errorf("IdleSecs = %d, want 86400", cs[0].Event.IdleSecs)
	}
	if !cs[0].DetectedAt.Equal(at) {
		t.Errorf("DetectedAt = %v, want %v", cs[0].DetectedAt, at)
	}
	sub, _ := h.liveSub(info.SubID)
	if !sub.CheckinAt.Equal(at) {
		t.Errorf("CheckinAt = %v, want %v", sub.CheckinAt, at)
	}
	if len(h.logs.named("check-in queued")) != 1 {
		t.Error("no \"check-in queued\" log")
	}
	select {
	case <-h.e.PendingWake():
	default:
		t.Error("check-in did not poke the delivery manager")
	}

	lifeTTick(h, 30*time.Hour)
	if n := len(h.pending(info.SubID).Events); n != 1 {
		t.Fatalf("pending after 30h tick = %d, want 1", n)
	}
}

func TestLifecycleRepeat(t *testing.T) {
	t.Run("unacked is replaced", func(t *testing.T) {
		h := newHarness(t)
		info := h.sub(prA, "w1:p1")
		lifeTTick(h, lifeTCheckin)
		first := lifeTOfType(h, info.SubID, model.EventCheckin)[0]

		lifeTTick(h, 2*lifeTCheckin)
		cs := lifeTOfType(h, info.SubID, model.EventCheckin)
		if len(cs) != 1 {
			t.Fatalf("check-ins at 48h = %+v, want exactly one (no stacking)", cs)
		}
		if cs[0].Seq <= first.Seq {
			t.Errorf("replacement Seq %d, want > %d", cs[0].Seq, first.Seq)
		}
		sub, _ := h.liveSub(info.SubID)
		if want := t0.Add(2 * lifeTCheckin); !sub.CheckinAt.Equal(want) {
			t.Errorf("CheckinAt = %v, want %v", sub.CheckinAt, want)
		}
	})
	t.Run("acked still repeats", func(t *testing.T) {
		h := newHarness(t)
		info := h.sub(prA, "w1:p1")
		lifeTTick(h, lifeTCheckin)
		first := lifeTOfType(h, info.SubID, model.EventCheckin)[0]
		if err := h.e.Ack(info.SubID, first.Seq); err != nil {
			t.Fatal(err)
		}
		lifeTTick(h, 2*lifeTCheckin)
		if cs := lifeTOfType(h, info.SubID, model.EventCheckin); len(cs) != 1 {
			t.Fatalf("check-ins at 48h after ack = %+v, want one (D-13)", cs)
		}
	})
}

func TestLifecycleKeepResets(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	lifeTTick(h, lifeTCheckin)
	h.clk.Advance(30 * time.Hour)
	if err := h.e.Keep(info.SubID); err != nil {
		t.Fatal(err)
	}
	if n := len(h.pending(info.SubID).Events); n != 0 {
		t.Fatalf("pending after keep = %d, want 0", n)
	}
	lifeTTick(h, 53*time.Hour)
	if n := len(h.pending(info.SubID).Events); n != 0 {
		t.Fatalf("pending 23h after keep = %d, want 0", n)
	}
	lifeTTick(h, 54*time.Hour)
	if cs := lifeTOfType(h, info.SubID, model.EventCheckin); len(cs) != 1 {
		t.Fatalf("check-ins 24h after keep = %+v, want one", cs)
	}
}

func TestLifecycleAckDoesNotReset(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	lifeTTick(h, lifeTCheckin)
	c := lifeTOfType(h, info.SubID, model.EventCheckin)[0]
	h.clk.Advance(25 * time.Hour)
	if err := h.e.Ack(info.SubID, c.Seq); err != nil {
		t.Fatal(err)
	}
	sub, _ := h.liveSub(info.SubID)
	if !sub.ClockAt.Equal(t0) {
		t.Fatalf("ClockAt = %v after check-in ack, want %v (D-11)", sub.ClockAt, t0)
	}
}

func TestExpiry(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	lifeTTick(h, 6*lifeTCheckin)
	if n := len(lifeTOfType(h, info.SubID, model.EventCheckin)); n != 1 {
		t.Fatalf("check-ins at 6d = %d, want 1", n)
	}
	if !h.planned(prA) {
		t.Fatal("PR not planned before expiry")
	}

	lifeTTick(h, lifeTExpire)
	d := h.pending(info.SubID)
	if len(d.Events) != 1 || d.Events[0].Event.Type != model.EventExpired {
		t.Fatalf("pending at 7d = %+v, want only expired", d.Events)
	}
	ex := d.Events[0]
	if ex.Event.IdleSecs != 604800 {
		t.Errorf("IdleSecs = %d, want 604800", ex.Event.IdleSecs)
	}
	if want := "expired:" + strconv.FormatInt(t0.Add(lifeTExpire).Unix(), 10); ex.Event.Key != want {
		t.Errorf("Key = %q, want %q", ex.Event.Key, want)
	}
	sub, _ := h.liveSub(info.SubID)
	if sub.Ending != ReasonExpired || sub.EndSeq != ex.Seq {
		t.Fatalf("Ending %q EndSeq %d, want expired %d", sub.Ending, sub.EndSeq, ex.Seq)
	}
	if n := h.e.Status().Subs; n != 0 {
		t.Errorf("Status().Subs = %d, want 0", n)
	}
	if h.planned(prA) {
		t.Error("PR still planned with only an expiring sub")
	}
	if len(h.logs.named("subscription expiring")) != 1 {
		t.Error("no \"subscription expiring\" log")
	}

	saves := lifeTSaves(h)
	lifeTTick(h, lifeTExpire+lifeTCheckin)
	lifeTTick(h, 3*lifeTExpire)
	if n := len(h.pending(info.SubID).Events); n != 1 {
		t.Fatalf("pending after ticks on ending sub = %d, want 1", n)
	}
	if got := lifeTSaves(h); got != saves {
		t.Errorf("ticks on an ending sub saved %d times", got-saves)
	}

	if err := h.e.Ack(info.SubID, ex.Seq); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.liveSub(info.SubID); ok {
		t.Fatal("ack of expired line kept the sub")
	}
	cs := h.e.RecentCancellations()
	if len(cs) != 1 || cs[0].Reason != ReasonExpired || cs[0].SubID != info.SubID {
		t.Fatalf("cancellations = %+v, want one expired", cs)
	}
}

// TestExpiryKeepsRealEvents: pending real events stay queued ahead of the
// expired line, only check-ins are dropped.
func TestExpiryKeepsRealEvents(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	h.setSub(info.SubID, func(s *store.Subscription) {
		s.Pending = []store.PendingEvent{{Seq: 1, Event: model.Event{Type: model.EventComment, Key: "c1"}}}
	})
	h.e.mu.Lock()
	h.e.state.Seq = 1
	h.e.mu.Unlock()
	lifeTTick(h, lifeTExpire)
	d := h.pending(info.SubID)
	if len(d.Events) != 2 || d.Events[0].Event.Key != "c1" || d.Events[1].Event.Type != model.EventExpired {
		t.Fatalf("pending = %+v, want comment then expired", d.Events)
	}
	if d.Events[1].Seq != 2 {
		t.Errorf("expired Seq = %d, want 2 (global sequence)", d.Events[1].Seq)
	}
}

func TestExpiryPaneGone(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	lifeTTick(h, lifeTExpire)
	if sub, _ := h.liveSub(info.SubID); sub.Ending != ReasonExpired {
		t.Fatalf("Ending = %q, want expired", sub.Ending)
	}
	if err := h.e.Cancel(info.SubID, ReasonPaneGone); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.liveSub(info.SubID); ok {
		t.Fatal("pane_gone kept the ending sub")
	}
	h.e.mu.Lock()
	_, rec := h.e.state.PRs[store.PRKey(prA)]
	h.e.mu.Unlock()
	if rec {
		t.Error("PR record kept after its last sub was cancelled")
	}
}

func TestLifecycleDue(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	sub, _ := h.liveSub(info.SubID)
	c, x := h.e.LifecycleDue(sub)
	if !c.Equal(t0.Add(lifeTCheckin)) || !x.Equal(t0.Add(lifeTExpire)) {
		t.Fatalf("fresh due = %v / %v", c, x)
	}

	lifeTTick(h, 30*time.Hour)
	sub, _ = h.liveSub(info.SubID)
	c, x = h.e.LifecycleDue(sub)
	if want := t0.Add(30*time.Hour + lifeTCheckin); !c.Equal(want) {
		t.Errorf("checkinDue after check-in = %v, want %v", c, want)
	}
	if !x.Equal(t0.Add(lifeTExpire)) {
		t.Errorf("expiresAt = %v, want %v", x, t0.Add(lifeTExpire))
	}

	sub.Ending = ReasonClosed
	c, x = h.e.LifecycleDue(sub)
	if !c.IsZero() || !x.IsZero() {
		t.Errorf("ending due = %v / %v, want zero", c, x)
	}
}

// TestInactiveSlowestTier: after 24h without activity the PR is already at
// the slowest tier through the cadence curve (LIFE-06, open question 3).
func TestInactiveSlowestTier(t *testing.T) {
	h := newHarness(t)
	h.sub(prA, "w1:p1")
	h.pollNow(prA)
	h.clk.Advance(lifeTCheckin)
	rec := h.record(prA)
	h.e.mu.Lock()
	interval, _ := h.e.scheduleLocked(&rec, h.clk.Now())
	h.e.mu.Unlock()
	if want := h.e.cfg.Poll.MaxInterval.Duration; interval != want {
		t.Fatalf("interval after 24h idle = %v, want %v", interval, want)
	}
}

// lifeTRun starts RunLifecycle on s.e in the bubble and returns a stop func
// that cancels it and checks it returned nil promptly.
func lifeTRun(t *testing.T, s *sched, grace, every time.Duration) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.e.RunLifecycle(ctx, grace, every) }()
	synctest.Wait()
	return func() {
		t.Helper()
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("RunLifecycle returned %v", err)
			}
		default:
			t.Error("RunLifecycle did not return after cancel")
		}
	}
}

// lifeTEnding reports the Ending marker of sub id ("<gone>" when deleted).
func lifeTEnding(s *sched, id string) string {
	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	if sub := s.e.state.Subs[id]; sub != nil {
		return sub.Ending
	}
	return "<gone>"
}

func TestRunLifecycleGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSched(t, schedOpt{})
		info := s.sub(prA, "w1:p1")
		s.e.mu.Lock()
		s.e.state.Subs[info.SubID].ClockAt = time.Now().Add(-8 * 24 * time.Hour)
		s.e.mu.Unlock()

		stop := lifeTRun(t, s, 10*time.Minute, time.Minute)
		s.advance(10*time.Minute - time.Second)
		if got := lifeTEnding(s, info.SubID); got != "" {
			t.Fatalf("Ending = %q before the grace ended, want live", got)
		}
		if d, _ := s.e.Pending(info.SubID); len(d.Events) != 0 {
			t.Fatalf("pending during grace = %+v", d.Events)
		}
		s.advance(time.Second)
		if got := lifeTEnding(s, info.SubID); got != ReasonExpired {
			t.Fatalf("Ending = %q right after grace, want expired", got)
		}
		stop()
	})
}

// TestRunLifecycleTicks: after the grace the pass repeats every interval.
func TestRunLifecycleTicks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSched(t, schedOpt{})
		info := s.sub(prA, "w1:p1")
		stop := lifeTRun(t, s, 0, time.Minute)
		s.advance(lifeTCheckin - time.Minute)
		if d, _ := s.e.Pending(info.SubID); len(d.Events) != 0 {
			t.Fatalf("pending before 24h = %+v", d.Events)
		}
		s.advance(time.Minute)
		d, _ := s.e.Pending(info.SubID)
		if len(d.Events) != 1 || d.Events[0].Event.Type != model.EventCheckin {
			t.Fatalf("pending at 24h = %+v, want one check-in", d.Events)
		}
		stop()
	})
}

// TestRunLifecycleKeepsWake: the lifecycle loop must not consume the
// scheduler's wake channel, or a new PR could wait up to maxSleep.
func TestRunLifecycleKeepsWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSched(t, schedOpt{})
		stop := lifeTRun(t, s, 0, time.Minute)
		s.e.poke()
		s.advance(2 * time.Minute)
		select {
		case <-s.e.Wake():
		default:
			t.Error("RunLifecycle consumed the scheduler wake")
		}
		stop()
	})
}

// TestRunLifecycleSaveError: a failed save is logged and the loop goes on
// (the daemon never exits on its own, D-18).
func TestRunLifecycleSaveError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		saver := &memSaver{}
		logs := &logCapture{}
		f := newFakeGH(t)
		e, err := New(Options{Saver: saver, State: store.NewState(), Fetcher: newTestClient(t, f),
			Config: config.Defaults(), Logger: slog.New(logs)})
		if err != nil {
			t.Fatal(err)
		}
		s := &sched{t: t, f: f, e: e}
		info := s.sub(prA, "w1:p1")
		saver.setFail(func(*store.State) bool { return true })
		stop := lifeTRun(t, s, 0, time.Minute)
		s.advance(lifeTCheckin)
		if len(logs.named("lifecycle tick failed")) == 0 {
			t.Fatal("no \"lifecycle tick failed\" log")
		}
		saver.setFail(nil)
		s.advance(time.Minute)
		if d, _ := e.Pending(info.SubID); len(d.Events) != 1 {
			t.Fatalf("pending after save recovered = %+v, want one check-in", d.Events)
		}
		stop()
	})
}

func TestRunLifecycleSecondRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSched(t, schedOpt{})
		stop := lifeTRun(t, s, time.Hour, time.Minute)
		if err := s.e.RunLifecycle(context.Background(), 0, time.Minute); err == nil {
			t.Error("second concurrent RunLifecycle returned nil")
		}
		stop()
		// Once the first run has returned a new one may start.
		stop = lifeTRun(t, s, time.Hour, time.Minute)
		stop()
	})
}
