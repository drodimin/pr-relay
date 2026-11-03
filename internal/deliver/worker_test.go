package deliver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/drodimin/pr-relay/internal/compose"
	"github.com/drodimin/pr-relay/internal/herdr"
	"github.com/drodimin/pr-relay/internal/herdr/herdrtest"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/poller"
	"github.com/drodimin/pr-relay/internal/store"
)

const (
	testSock = "/fake/h.sock"
	testPane = "w1:p1"
)

var testRef = poller.PaneRef{HerdrSocket: testSock, PaneID: testPane}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// fakeLink is a scripted events-endpoint state.
type fakeLink struct {
	mu        sync.Mutex
	up        bool
	upSince   time.Time
	downSince time.Time
}

func (l *fakeLink) Up() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.up
}

func (l *fakeLink) UpSince() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.up {
		return time.Time{}
	}
	return l.upSince
}

func (l *fakeLink) DownSince() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.up {
		return time.Time{}
	}
	return l.downSince
}

func (l *fakeLink) setUp(at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.up, l.upSince = true, at
}

func (l *fakeLink) setDown(at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.up, l.downSince = false, at
}

// harness runs one worker against the fake herdr and fake engine inside a
// synctest bubble. Every helper must be called from inside the bubble.
type harness struct {
	t      *testing.T
	srv    *herdrtest.Server
	eng    *fakeEngine
	link   *fakeLink
	logs   *lockedBuf
	w      *worker
	cancel context.CancelFunc
	done   chan struct{}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	srv := herdrtest.New(t)
	logs := &lockedBuf{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cl, err := herdr.NewClient(herdr.Options{Socket: testSock, Dial: srv.Dialer(), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	link := &fakeLink{}
	link.setUp(time.Now())
	eng := newFakeEngine()
	cfg := workerConfig{QuietPeriod: 10 * time.Second, RestoreGrace: 10 * time.Second}
	w := newWorker(testRef, cl, link, eng, cfg, log)
	return &harness{t: t, srv: srv, eng: eng, link: link, logs: logs, w: w}
}

func (h *harness) start() {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan struct{})
	go func() {
		defer close(h.done)
		h.w.run(ctx)
	}()
	h.t.Cleanup(h.stop)
	synctest.Wait()
}

func (h *harness) stop() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	<-h.done
	h.cancel = nil
}

func (h *harness) running() bool {
	select {
	case <-h.done:
		return false
	default:
		return true
	}
}

// advance moves fake time forward by d and lets every goroutine settle.
func (h *harness) advance(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

func prN(n int) model.PRRef { return model.PRRef{Owner: "acme", Repo: "widgets", Number: n} }

// sub adds a subscription on pane for pr, with a current view (head "head1",
// OPEN, baselined).
func (h *harness) sub(pane, session, kind string, pr model.PRRef) store.Subscription {
	key := store.PRKey(pr)
	s := store.Subscription{ID: store.SubID(testSock, pane, key), PR: key, HerdrSocket: testSock, PaneID: pane,
		AgentSession: session, AgentKind: kind}
	h.eng.addSub(s)
	h.eng.setView(key, poller.PRView{Ref: pr, HeadOID: "head1", State: "OPEN", Baselined: true})
	return s
}

func (h *harness) pane(ps herdrtest.PaneState) {
	if ps.ID == "" {
		ps.ID = testPane
	}
	h.srv.SetPane(ps)
}

func comments(from, n int) []model.Event {
	out := make([]model.Event, n)
	for i := range out {
		out[i] = model.Event{Type: model.EventComment, Key: fmt.Sprintf("c%d", from+i), CommentKind: "comment",
			Actor: model.Actor{Login: fmt.Sprintf("user%d", from+i), Type: "User"},
			URL:   fmt.Sprintf("https://github.com/acme/widgets/pull/1#issuecomment-%d", from+i)}
	}
	return out
}

func (h *harness) prompts() []herdrtest.Prompt { return h.srv.Prompts() }

func (h *harness) wantPrompts(n int) []herdrtest.Prompt {
	h.t.Helper()
	ps := h.prompts()
	if len(ps) != n {
		h.t.Fatalf("prompts = %d, want %d (calls %v)", len(ps), n, h.srv.Calls())
	}
	return ps
}

func lines(text string) int { return strings.Count(text, "\n") + 1 }

func TestDeliverPrompts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Session: "s1", Status: herdr.StatusIdle})
		s := h.sub(testPane, "s1", "claude", prN(1))
		h.eng.addPending(s.ID, comments(1, 2)...)

		d, _ := h.eng.Pending(s.ID)
		v, _ := h.eng.PRView(s.PR)
		want := compose.Message([]compose.Group{{SubID: s.ID, Ref: v.Ref, Events: d.Events, UpToSeq: d.UpToSeq,
			View: compose.View{HeadOID: v.HeadOID, State: v.State, Baselined: v.Baselined}}}, compose.Limits{})

		h.start()
		h.advance(9 * time.Second)
		h.wantPrompts(0)
		h.advance(2 * time.Second)
		p := h.wantPrompts(1)[0]
		if p.Target != testPane {
			t.Errorf("target = %q", p.Target)
		}
		if p.Text != want.Text || lines(p.Text) != 2 {
			t.Errorf("text = %q, want %q", p.Text, want.Text)
		}
		if p.Wait == nil || !reflect.DeepEqual(p.Wait.Until, []string{herdr.StatusWorking}) || p.Wait.TimeoutMs != 5000 {
			t.Errorf("wait = %+v", p.Wait)
		}
		if got := h.eng.ackLog(); !reflect.DeepEqual(got, []map[string]uint64{{s.ID: 2}}) {
			t.Errorf("acks = %v", got)
		}
		if n := h.eng.pendingCount(s.ID); n != 0 {
			t.Errorf("pending = %d", n)
		}
		if h.running() {
			t.Error("worker still running with nothing pending")
		}
		if !strings.Contains(h.logs.String(), "delivered") {
			t.Error("no delivered log")
		}
	})
}

func TestNothingPendingExits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Status: herdr.StatusIdle})
		h.sub(testPane, "", "", prN(1))
		h.start()
		if h.running() {
			t.Fatal("worker running with nothing pending")
		}
		if n := len(h.srv.Calls()); n != 0 {
			t.Errorf("herdr calls = %d", n)
		}
	})
}

func TestDropMarkerDelivered(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Status: herdr.StatusIdle})
		s := h.sub(testPane, "", "", prN(1))
		h.eng.setDropped(s.ID, 7)
		h.start()
		h.advance(11 * time.Second)
		h.wantPrompts(1)
		if h.running() {
			t.Error("worker still running after the drop marker was acked")
		}
	})
}

func TestGateHoldsWhileWorking(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Session: "s1", Status: herdr.StatusWorking})
		s := h.sub(testPane, "s1", "claude", prN(1))
		h.eng.addPending(s.ID, comments(1, 1)...)
		h.start()
		h.advance(10 * time.Minute)
		h.wantPrompts(0)
		if n := h.srv.CallCount("agent.wait"); n < 1 || n > 3 {
			t.Errorf("agent.wait calls = %d, want 1..3", n)
		}
		if _, ok := h.eng.reachFor(testRef); ok {
			t.Error("working pane reported as unable to receive")
		}
		for _, r := range h.srv.Requests() {
			if r.Method == "agent.wait" && !strings.Contains(string(r.Params), `"until":["idle","done"]`) {
				t.Errorf("agent.wait params = %s", r.Params)
			}
		}
		h.srv.SetStatus(testPane, herdr.StatusIdle)
		synctest.Wait()
		h.advance(9 * time.Second)
		h.wantPrompts(0)
		h.advance(2 * time.Second)
		h.wantPrompts(1)
	})
}

func TestGateQuietReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Status: herdr.StatusIdle})
		s := h.sub(testPane, "", "claude", prN(1))
		h.eng.addPending(s.ID, comments(1, 1)...)
		h.start()
		h.advance(7 * time.Second)
		h.srv.SetStatus(testPane, herdr.StatusWorking)
		h.srv.SetStatus(testPane, herdr.StatusIdle)
		h.w.poke()
		synctest.Wait()
		h.advance(4 * time.Second) // t=11
		h.wantPrompts(0)
		h.advance(7 * time.Second) // t=18
		h.wantPrompts(1)
	})
}

func TestGateSeqMovement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Status: herdr.StatusIdle, StateSeq: 4})
		s := h.sub(testPane, "", "claude", prN(1))
		h.eng.addPending(s.ID, comments(1, 1)...)
		h.start()
		h.advance(5 * time.Second)
		one := uint64(1)
		// A turn completed without a status event reaching the worker.
		h.pane(herdrtest.PaneState{Agent: "claude", Status: herdr.StatusIdle, StateSeq: 4, CompletionSeq: &one})
		h.advance(6 * time.Second) // t=11
		h.wantPrompts(0)
		h.advance(10 * time.Second) // t=21
		h.wantPrompts(1)
	})
}

func TestGateDoneIdleSameClass(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Status: herdr.StatusDone, StateSeq: 3})
		s := h.sub(testPane, "", "claude", prN(1))
		h.eng.addPending(s.ID, comments(1, 1)...)
		h.start()
		h.advance(5 * time.Second)
		h.pane(herdrtest.PaneState{Agent: "claude", Status: herdr.StatusIdle, StateSeq: 3})
		h.advance(6 * time.Second) // t=11
		h.wantPrompts(1)
	})
}

func TestGateBlocked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		t0 := time.Now()
		h.pane(herdrtest.PaneState{Agent: "claude", Status: herdr.StatusBlocked})
		s := h.sub(testPane, "", "claude", prN(1))
		h.eng.addPending(s.ID, comments(1, 1)...)
		h.start()
		h.advance(time.Minute)
		h.wantPrompts(0)
		if since, ok := h.eng.reachFor(testRef); !ok || !since.Equal(t0) {
			t.Errorf("reachability = %v %v, want %v", since, ok, t0)
		}
		h.srv.SetStatus(testPane, herdr.StatusIdle)
		synctest.Wait()
		if _, ok := h.eng.reachFor(testRef); ok {
			t.Error("idle pane still reported as unable to receive")
		}
		h.advance(11 * time.Second)
		h.wantPrompts(1)
		hist := h.eng.reachHistory()
		if len(hist) < 2 || hist[0].Since.IsZero() || !hist[len(hist)-1].Since.IsZero() {
			t.Errorf("reach history = %v", hist)
		}
	})
}

func TestGateAgentBlockedFromPrompt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Status: herdr.StatusIdle})
		s := h.sub(testPane, "", "claude", prN(1))
		h.eng.addPending(s.ID, comments(1, 1)...)
		h.srv.FailNext("agent.prompt", herdr.CodeAgentBlocked)
		h.start()
		h.advance(11 * time.Second)
		h.wantPrompts(0)
		if n := h.srv.CallCount("agent.prompt"); n != 1 {
			t.Fatalf("agent.prompt calls = %d", n)
		}
		if a := h.eng.ackLog(); len(a) != 0 {
			t.Fatalf("acked after agent_blocked: %v", a)
		}
		h.advance(10 * time.Second)
		h.wantPrompts(1)
		if a := h.eng.ackLog(); len(a) != 1 {
			t.Errorf("acks = %v", a)
		}
	})
}

func TestNeverPromptUnknown(t *testing.T) {
	neverPrompt(t, herdrtest.PaneState{Agent: "claude", Status: herdr.StatusUnknown})
}

func TestNeverPromptLaunchPending(t *testing.T) {
	neverPrompt(t, herdrtest.PaneState{Agent: "claude", Status: herdr.StatusIdle, LaunchPending: true})
}

func TestNeverPromptBareShell(t *testing.T) {
	neverPrompt(t, herdrtest.PaneState{Agent: ""})
}

func neverPrompt(t *testing.T, ps herdrtest.PaneState) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(ps)
		s := h.sub(testPane, "", "claude", prN(1))
		h.eng.addPending(s.ID, comments(1, 2)...)
		h.start()
		h.advance(time.Hour)
		if n := h.srv.CallCount("agent.prompt"); n != 0 {
			t.Fatalf("agent.prompt calls = %d", n)
		}
		if n := h.eng.pendingCount(s.ID); n != 2 {
			t.Errorf("pending = %d, want 2", n)
		}
		if _, ok := h.eng.reachFor(testRef); !ok {
			t.Error("pane not reported as unable to receive")
		}
		if c := h.eng.cancelLog(); len(c) != 0 {
			t.Errorf("cancels = %v", c)
		}
		if n := len(h.srv.Calls()); n > 200 {
			t.Errorf("herdr calls in 1h = %d (hot loop?)", n)
		}
	})
}

func TestCoalesce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Status: herdr.StatusIdle})
		a := h.sub(testPane, "", "claude", prN(1))
		b := h.sub(testPane, "", "claude", prN(2))
		h.eng.addPending(a.ID, comments(1, 3)...)
		h.eng.addPending(b.ID, comments(10, 2)...)
		h.eng.beforeAck = func() { h.eng.addPending(a.ID, comments(20, 2)...) }
		h.start()
		h.advance(11 * time.Second)
		p := h.wantPrompts(1)[0]
		if n := lines(p.Text); n != 5 {
			t.Errorf("lines = %d, want 5: %q", n, p.Text)
		}
		if !strings.Contains(p.Text, "#1") || !strings.Contains(p.Text, "#2") {
			t.Errorf("text missing a PR: %q", p.Text)
		}
		if n := h.eng.pendingCount(a.ID); n != 2 {
			t.Errorf("a pending = %d, want the 2 that arrived during the prompt", n)
		}
		if n := h.eng.pendingCount(b.ID); n != 0 {
			t.Errorf("b pending = %d", n)
		}
		if a := h.eng.ackLog(); len(a) != 1 || len(a[0]) != 2 {
			t.Errorf("acks = %v", a)
		}
	})
}

func TestSupersededSilentAck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Status: herdr.StatusIdle})
		s := h.sub(testPane, "", "claude", prN(1))
		h.eng.addPending(s.ID,
			model.Event{Type: model.EventCIFailed, Key: "f1", CheckName: "lint", HeadOID: "oldhead"},
			model.Event{Type: model.EventCIFailed, Key: "f2", CheckName: "test", HeadOID: "oldhead"})
		h.start()
		h.advance(11 * time.Second)
		if n := h.srv.CallCount("agent.prompt"); n != 0 {
			t.Fatalf("agent.prompt calls = %d", n)
		}
		if a := h.eng.ackLog(); len(a) != 1 || a[0][s.ID] != 2 {
			t.Errorf("acks = %v", a)
		}
		if n := h.eng.pendingCount(s.ID); n != 0 {
			t.Errorf("pending = %d", n)
		}
		if h.running() {
			t.Error("worker still running")
		}
	})
}

func TestPromptTimeoutCountsDelivered(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Status: herdr.StatusIdle})
		s := h.sub(testPane, "", "claude", prN(1))
		h.eng.addPending(s.ID, comments(1, 1)...)
		h.srv.FailNext("agent.prompt", herdr.CodeTimeout)
		h.start()
		h.advance(11 * time.Second)
		if a := h.eng.ackLog(); len(a) != 1 {
			t.Fatalf("acks = %v", a)
		}
		if n := h.eng.pendingCount(s.ID); n != 0 {
			t.Errorf("pending = %d", n)
		}
		logs := h.logs.String()
		if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "working not observed") {
			t.Errorf("no timeout warning in logs:\n%s", logs)
		}
		h.advance(time.Minute)
		if n := h.srv.CallCount("agent.prompt"); n != 1 {
			t.Errorf("agent.prompt calls = %d (re-sent after timeout)", n)
		}
	})
}

func TestPromptStalledRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Status: herdr.StatusIdle})
		s := h.sub(testPane, "", "claude", prN(1))
		h.eng.addPending(s.ID, comments(1, 1)...)
		h.srv.FailNext("agent.prompt", herdr.CodeAgentPromptStalled)
		h.start()
		h.advance(11 * time.Second)
		if a := h.eng.ackLog(); len(a) != 0 {
			t.Fatalf("acked after stalled: %v", a)
		}
		h.advance(10 * time.Second)
		h.wantPrompts(1)
		if a := h.eng.ackLog(); len(a) != 1 {
			t.Errorf("acks = %v", a)
		}
	})
}

func TestAckFailureKeepsQueued(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Status: herdr.StatusIdle})
		s := h.sub(testPane, "", "claude", prN(1))
		h.eng.addPending(s.ID, comments(1, 1)...)
		h.eng.setFailAck(errors.New("disk full"))
		h.start()
		h.advance(11 * time.Second)
		h.wantPrompts(1)
		if n := h.eng.pendingCount(s.ID); n != 1 {
			t.Errorf("pending = %d", n)
		}
		if !strings.Contains(h.logs.String(), "level=ERROR") {
			t.Errorf("no error log:\n%s", h.logs.String())
		}
		if !h.running() {
			t.Error("worker exited after a failed ack")
		}
	})
}

func TestPushTimeGone(t *testing.T) {
	setup := func(t *testing.T, upFor time.Duration) (*harness, []store.Subscription, store.Subscription) {
		h := newHarness(t)
		h.link.setUp(time.Now().Add(-upFor))
		a := h.sub(testPane, "s1", "claude", prN(1))
		b := h.sub(testPane, "s1", "claude", prN(2))
		other := h.sub("w1:p9", "s9", "claude", prN(1))
		h.eng.addPending(a.ID, comments(1, 1)...)
		h.eng.addPending(other.ID, comments(5, 1)...)
		return h, []store.Subscription{a, b}, other
	}
	check := func(t *testing.T, h *harness, mine []store.Subscription, other store.Subscription) {
		t.Helper()
		got := h.eng.cancelLog()
		if len(got) != len(mine) {
			t.Fatalf("cancels = %v", got)
		}
		for _, c := range got {
			if c.Reason != poller.ReasonPaneGone {
				t.Errorf("reason = %q", c.Reason)
			}
		}
		for _, s := range mine {
			if h.eng.hasSub(s.ID) {
				t.Errorf("sub %s not cancelled", s.ID)
			}
		}
		if !h.eng.hasSub(other.ID) {
			t.Error("sub on another pane cancelled")
		}
		if n := h.srv.CallCount("agent.prompt"); n != 0 {
			t.Errorf("agent.prompt calls = %d", n)
		}
		if h.running() {
			t.Error("worker still running")
		}
	}
	t.Run("up long enough", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h, mine, other := setup(t, time.Minute)
			h.start()
			h.advance(time.Second)
			check(t, h, mine, other)
		})
	})
	t.Run("within restore grace", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h, mine, other := setup(t, 2*time.Second)
			h.start()
			h.advance(7 * time.Second) // up for 9s
			if c := h.eng.cancelLog(); len(c) != 0 {
				t.Fatalf("cancelled within grace: %v", c)
			}
			h.advance(2 * time.Second) // up for 11s
			check(t, h, mine, other)
		})
	})
}

func TestUnreachableKeeps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Status: herdr.StatusIdle})
		s := h.sub(testPane, "", "claude", prN(1))
		h.eng.addPending(s.ID, comments(1, 1)...)
		down := time.Now().Add(-30 * time.Second)
		h.srv.Stop()
		h.link.setDown(down)
		h.start()
		h.advance(5 * time.Minute)
		if c := h.eng.cancelLog(); len(c) != 0 {
			t.Errorf("cancels = %v", c)
		}
		if a := h.eng.ackLog(); len(a) != 0 {
			t.Errorf("acks = %v", a)
		}
		if since, ok := h.eng.reachFor(testRef); !ok || !since.Equal(down) {
			t.Errorf("reachability = %v %v, want %v", since, ok, down)
		}
		h.srv.Restart()
		h.link.setUp(time.Now())
		h.w.poke()
		synctest.Wait()
		h.advance(11 * time.Second)
		h.wantPrompts(1)
		if n := h.eng.pendingCount(s.ID); n != 0 {
			t.Errorf("pending = %d", n)
		}
	})
}

func TestCancelIdentityMismatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Session: "s2", Status: herdr.StatusIdle})
		a := h.sub(testPane, "s1", "claude", prN(1))
		b := h.sub(testPane, "", "", prN(2))
		h.eng.addPending(a.ID, comments(1, 2)...)
		h.eng.addPending(b.ID, comments(10, 1)...)
		h.start()
		h.advance(11 * time.Second)
		if got := h.eng.cancelLog(); !reflect.DeepEqual(got, []fakeCancel{{SubID: a.ID, Reason: poller.ReasonIdentityMismatch}}) {
			t.Errorf("cancels = %v", got)
		}
		p := h.wantPrompts(1)[0]
		if lines(p.Text) != 1 || !strings.Contains(p.Text, "#2") {
			t.Errorf("text = %q, want only the legacy sub's line", p.Text)
		}
		if n := h.eng.pendingCount(b.ID); n != 0 {
			t.Errorf("b pending = %d", n)
		}
	})
}

func TestIdentityUnidentifiedHolds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Status: herdr.StatusIdle})
		s := h.sub(testPane, "s1", "claude", prN(1))
		h.eng.addPending(s.ID, comments(1, 1)...)
		h.start()
		h.advance(10 * time.Minute)
		h.wantPrompts(0)
		if c := h.eng.cancelLog(); len(c) != 0 {
			t.Errorf("cancels = %v", c)
		}
		if _, ok := h.eng.reachFor(testRef); !ok {
			t.Error("unidentified pane not reported as unable to receive")
		}
		// The session becomes known: delivered.
		h.pane(herdrtest.PaneState{Agent: "claude", Session: "s1", Status: herdr.StatusIdle})
		h.w.poke()
		synctest.Wait()
		h.advance(11 * time.Second)
		h.wantPrompts(1)
	})
}

func TestQuietNoHotLoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Status: herdr.StatusIdle})
		s := h.sub(testPane, "", "claude", prN(1))
		h.eng.addPending(s.ID, comments(1, 1)...)
		h.start()
		h.advance(5 * time.Second)
		if n := len(h.srv.Calls()); n > 1 {
			t.Errorf("herdr calls in the first half of the window = %d (%v)", n, h.srv.Calls())
		}
		h.advance(6 * time.Second)
		if n := len(h.srv.Calls()); n > 4 {
			t.Errorf("herdr calls in one window = %d (%v)", n, h.srv.Calls())
		}
		if n := h.srv.CallCount("agent.wait"); n != 0 {
			t.Errorf("agent.wait called while idle: %d", n)
		}
	})
}

func TestWaitNotRunningRechecks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.pane(herdrtest.PaneState{Agent: "claude", Session: "s1", Status: herdr.StatusWorking})
		s := h.sub(testPane, "s1", "claude", prN(1))
		h.eng.addPending(s.ID, comments(1, 1)...)
		h.start()
		h.advance(time.Second)
		if g, w := h.srv.CallCount("agent.get"), h.srv.CallCount("agent.wait"); g != 1 || w != 1 {
			t.Fatalf("agent.get = %d, agent.wait = %d", g, w)
		}
		h.srv.SetOccupant(testPane, "claude", "s2")
		synctest.Wait()
		if g := h.srv.CallCount("agent.get"); g != 2 {
			t.Errorf("agent.get after agent_not_running = %d, want 2", g)
		}
		if got := h.eng.cancelLog(); !reflect.DeepEqual(got, []fakeCancel{{SubID: s.ID, Reason: poller.ReasonIdentityMismatch}}) {
			t.Errorf("cancels = %v", got)
		}
		if h.running() {
			t.Error("worker still running with no subscriptions")
		}
		h.wantPrompts(0)
	})
}
