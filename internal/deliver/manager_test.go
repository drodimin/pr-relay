package deliver

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/drodimin/pr-relay/internal/herdr"
	"github.com/drodimin/pr-relay/internal/herdr/herdrtest"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/poller"
	"github.com/drodimin/pr-relay/internal/store"
)

const (
	sockA = "/s/a/herdr.sock"
	sockB = "/s/b/herdr.sock"
)

// mgrHarness runs a Manager against several fake herdr servers, routed by
// cleaned socket path, inside a synctest bubble.
type mgrHarness struct {
	t      *testing.T
	eng    *fakeEngine
	srvs   map[string]*herdrtest.Server
	logs   *lockedBuf
	m      *Manager
	cancel context.CancelFunc
	done   chan error

	mu    sync.Mutex
	dials map[string]int
}

func newMgrHarness(t *testing.T, socks ...string) *mgrHarness {
	t.Helper()
	h := &mgrHarness{t: t, eng: newFakeEngine(), srvs: map[string]*herdrtest.Server{}, logs: &lockedBuf{},
		dials: map[string]int{}}
	for _, s := range socks {
		h.srvs[filepath.Clean(s)] = herdrtest.New(t)
	}
	log := slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m, err := New(Options{Engine: h.eng, Dial: h.dial, Logger: log, QuietPeriod: 10 * time.Second,
		RestoreGrace: 10 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.m = m
	return h
}

func (h *mgrHarness) dial(ctx context.Context, socket string) (net.Conn, error) {
	key := filepath.Clean(socket)
	h.mu.Lock()
	h.dials[key]++
	h.mu.Unlock()
	srv := h.srvs[key]
	if srv == nil {
		return nil, errors.New("no such socket")
	}
	return srv.Dialer()(ctx, socket)
}

func (h *mgrHarness) dialCount(sock string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dials[filepath.Clean(sock)]
}

func (h *mgrHarness) srv(sock string) *herdrtest.Server { return h.srvs[filepath.Clean(sock)] }

func (h *mgrHarness) start() {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan error, 1)
	go func() { h.done <- h.m.Run(ctx) }()
	h.t.Cleanup(h.stop)
	synctest.Wait()
}

func (h *mgrHarness) stop() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	if err := <-h.done; err != nil {
		h.t.Errorf("Run = %v", err)
	}
	h.cancel = nil
}

func (h *mgrHarness) advance(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

// sub adds a subscription on (sock, pane) for pr with a current view.
func (h *mgrHarness) sub(sock, pane string, pr model.PRRef) store.Subscription {
	key := store.PRKey(pr)
	s := store.Subscription{ID: store.SubID(sock, pane, key), PR: key, HerdrSocket: sock, PaneID: pane,
		AgentKind: "claude"}
	h.eng.addSub(s)
	h.eng.setView(key, poller.PRView{Ref: pr, HeadOID: "head1", State: "OPEN", Baselined: true})
	return s
}

func (h *mgrHarness) idle(sock, pane string) {
	h.srv(sock).SetPane(herdrtest.PaneState{ID: pane, TabID: "t1", WorkspaceID: "w1", Agent: "claude",
		Status: herdr.StatusIdle})
}

// drainWake empties the fake PendingWake so a test can prove startup
// delivery needs no wake.
func (h *mgrHarness) drainWake() {
	select {
	case <-h.eng.pending:
	default:
	}
}

func wantPromptCount(t *testing.T, srv *herdrtest.Server, n int) []herdrtest.Prompt {
	t.Helper()
	ps := srv.Prompts()
	if len(ps) != n {
		t.Fatalf("prompts = %d, want %d (calls %v)", len(ps), n, srv.Calls())
	}
	return ps
}

func TestTwoSessions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMgrHarness(t, sockA, sockB)
		h.idle(sockA, testPane)
		h.idle(sockB, testPane)
		a := h.sub(sockA, testPane, prN(1))
		b := h.sub(sockB, testPane, prN(2))
		h.eng.addPending(a.ID, comments(1, 1)...)
		h.eng.addPending(b.ID, comments(2, 1)...)
		h.start()
		h.advance(11 * time.Second)
		pa := wantPromptCount(t, h.srv(sockA), 1)[0]
		pb := wantPromptCount(t, h.srv(sockB), 1)[0]
		if !strings.Contains(pa.Text, "acme/widgets#1 ") || strings.Contains(pa.Text, "acme/widgets#2 ") {
			t.Errorf("session a text = %q", pa.Text)
		}
		if !strings.Contains(pb.Text, "acme/widgets#2 ") || strings.Contains(pb.Text, "acme/widgets#1 ") {
			t.Errorf("session b text = %q", pb.Text)
		}
		if h.eng.pendingCount(a.ID) != 0 || h.eng.pendingCount(b.ID) != 0 {
			t.Error("pending not acked")
		}
	})
}

func TestPendingWakeStartsWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMgrHarness(t, sockA)
		h.idle(sockA, testPane)
		s := h.sub(sockA, testPane, prN(1))
		h.start()
		h.advance(30 * time.Second)
		wantPromptCount(t, h.srv(sockA), 0)
		h.eng.addPending(s.ID, comments(1, 1)...)
		synctest.Wait()
		h.advance(9 * time.Second)
		wantPromptCount(t, h.srv(sockA), 0)
		h.advance(2 * time.Second)
		wantPromptCount(t, h.srv(sockA), 1)
	})
}

func TestStartupDelivers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMgrHarness(t, sockA)
		h.idle(sockA, testPane)
		s := h.sub(sockA, testPane, prN(1))
		h.eng.addPending(s.ID, comments(1, 2)...)
		h.drainWake()
		h.start()
		h.advance(11 * time.Second)
		wantPromptCount(t, h.srv(sockA), 1)
		if n := h.eng.pendingCount(s.ID); n != 0 {
			t.Errorf("pending = %d", n)
		}
	})
}

func TestCancelPaneClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMgrHarness(t, sockA, sockB)
		h.idle(sockA, testPane)
		h.idle(sockB, testPane)
		a1 := h.sub(sockA, testPane, prN(1))
		a2 := h.sub(sockA, testPane, prN(2))
		b := h.sub(sockB, testPane, prN(1))
		h.eng.addPending(a1.ID, comments(1, 1)...)
		h.start()
		h.advance(3 * time.Second)
		h.srv(sockA).ClosePane(testPane)
		synctest.Wait()
		calls := h.srv(sockA).CallCount("agent.get")
		h.advance(time.Minute)
		wantPromptCount(t, h.srv(sockA), 0)
		got := map[string]string{}
		for _, c := range h.eng.cancelLog() {
			got[c.SubID] = c.Reason
		}
		if len(got) != 2 || got[a1.ID] != poller.ReasonPaneClosed || got[a2.ID] != poller.ReasonPaneClosed {
			t.Errorf("cancels = %v", h.eng.cancelLog())
		}
		if !h.eng.hasSub(b.ID) {
			t.Error("same pane id on the other socket was cancelled")
		}
		if n := h.srv(sockA).CallCount("agent.get"); n != calls {
			t.Errorf("agent.get after close: calls = %d, want %d (worker kept running)", n, calls)
		}
	})
}

func TestPaneMovedCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMgrHarness(t, sockA)
		h.idle(sockA, testPane)
		s := h.sub(sockA, testPane, prN(1))
		h.eng.addPending(s.ID, comments(1, 1)...)
		h.start()
		h.advance(2 * time.Second)
		h.srv(sockA).MovePane(testPane, "w2:p5")
		synctest.Wait()
		h.advance(time.Minute)
		wantPromptCount(t, h.srv(sockA), 0)
		if got := h.eng.cancelLog(); len(got) != 1 || got[0] != (fakeCancel{SubID: s.ID, Reason: poller.ReasonPaneGone}) {
			t.Errorf("cancels = %v", got)
		}
		for _, sub := range h.eng.List() {
			if sub.PaneID == "w2:p5" {
				t.Errorf("sub re-keyed to the new pane: %+v", sub)
			}
		}
	})
}

func TestTabClosedRechecks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMgrHarness(t, sockA)
		h.idle(sockA, "w1:p1")
		h.idle(sockA, "w1:p2")
		gone := h.sub(sockA, "w1:p1", prN(1))
		kept := h.sub(sockA, "w1:p2", prN(2))
		h.start()
		h.advance(time.Second)
		h.srv(sockA).RemovePane("w1:p1")
		h.srv(sockA).CloseTab("t1")
		synctest.Wait()
		h.advance(5 * time.Second)
		if len(h.eng.cancelLog()) != 0 {
			t.Fatalf("cancelled before the restore grace: %v", h.eng.cancelLog())
		}
		h.advance(10 * time.Second)
		if got := h.eng.cancelLog(); len(got) != 1 || got[0] != (fakeCancel{SubID: gone.ID, Reason: poller.ReasonPaneGone}) {
			t.Errorf("cancels = %v", got)
		}
		if !h.eng.hasSub(kept.ID) {
			t.Error("a pane that still exists was cancelled")
		}
	})
}

func TestStatusEventPokesWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMgrHarness(t, sockA)
		h.idle(sockA, testPane)
		s := h.sub(sockA, testPane, prN(1))
		h.eng.addPending(s.ID, comments(1, 1)...)
		h.start()
		h.advance(7 * time.Second)
		h.srv(sockA).SetStatus(testPane, herdr.StatusWorking)
		h.srv(sockA).SetStatus(testPane, herdr.StatusIdle)
		synctest.Wait()
		h.advance(9 * time.Second) // t=16: the window restarted at t=7
		wantPromptCount(t, h.srv(sockA), 0)
		h.advance(2 * time.Second) // t=18
		wantPromptCount(t, h.srv(sockA), 1)
	})
}

func TestHerdrRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMgrHarness(t, sockA)
		h.idle(sockA, testPane)
		s := h.sub(sockA, testPane, prN(1))
		h.eng.addPending(s.ID, comments(1, 1)...)
		h.start()
		srv := h.srv(sockA)
		subs := srv.Subscribes()
		h.advance(5 * time.Second)
		srv.Stop()
		synctest.Wait()
		h.advance(2 * time.Minute)
		wantPromptCount(t, srv, 0)
		if len(h.eng.cancelLog()) != 0 {
			t.Fatalf("cancelled while herdr was down: %v", h.eng.cancelLog())
		}
		if n := h.eng.pendingCount(s.ID); n != 1 {
			t.Fatalf("pending = %d, want 1", n)
		}
		if _, ok := h.eng.reachFor(poller.PaneRef{HerdrSocket: sockA, PaneID: testPane}); !ok {
			t.Error("pane on a down herdr not reported unable to receive")
		}
		srv.Restart()
		h.advance(31 * time.Second) // at most one capped backoff
		if srv.Subscribes() <= subs {
			t.Fatal("endpoint did not resubscribe after restart")
		}
		h.advance(11 * time.Second)
		wantPromptCount(t, srv, 1)
		if n := h.eng.pendingCount(s.ID); n != 0 {
			t.Errorf("pending after delivery = %d", n)
		}
		if _, ok := h.eng.reachFor(poller.PaneRef{HerdrSocket: sockA, PaneID: testPane}); ok {
			t.Error("pane still reported unable to receive after delivery")
		}
	})
}

func TestUnreachableReachability(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMgrHarness(t, sockA)
		h.srv(sockA).Stop()
		h.sub(sockA, testPane, prN(1))
		t0 := time.Now()
		ref := poller.PaneRef{HerdrSocket: sockA, PaneID: testPane}
		h.start()
		h.advance(10 * time.Minute)
		if since, ok := h.eng.reachFor(ref); !ok || !since.Equal(t0) {
			t.Fatalf("reachability = %v, %v; want %v", since, ok, t0)
		}
		h.idle(sockA, testPane)
		h.srv(sockA).Restart()
		h.advance(31 * time.Second)
		if since, ok := h.eng.reachFor(ref); ok {
			t.Errorf("reachability after restart = %v, want cleared", since)
		}
	})
}

func TestEndpointRetired(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMgrHarness(t, sockA)
		h.idle(sockA, testPane)
		s := h.sub(sockA, testPane, prN(1))
		h.start()
		h.advance(time.Second)
		srv := h.srv(sockA)
		if srv.Subscribers() != 1 {
			t.Fatalf("subscribers = %d, want 1", srv.Subscribers())
		}
		h.eng.removeSub(s.ID)
		synctest.Wait()
		h.advance(time.Second)
		if srv.Subscribers() != 0 {
			t.Errorf("subscribers after retire = %d", srv.Subscribers())
		}
		dials := h.dialCount(sockA)
		srv.Stop() // a live endpoint would now reconnect with backoff
		h.advance(10 * time.Minute)
		if got := h.dialCount(sockA); got != dials {
			t.Errorf("dials after retire = %d, want %d", got, dials)
		}
	})
}

func TestSocketKeyCleaned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMgrHarness(t, sockA)
		h.idle(sockA, testPane)
		a := h.sub("/s/a//herdr.sock", testPane, prN(1))
		b := h.sub(sockA, testPane, prN(2))
		h.eng.addPending(a.ID, comments(1, 1)...)
		h.eng.addPending(b.ID, comments(2, 1)...)
		h.start()
		h.advance(11 * time.Second)
		srv := h.srv(sockA)
		if srv.Subscribes() != 1 || srv.Subscribers() != 1 {
			t.Errorf("subscribes = %d, subscribers = %d; want one endpoint", srv.Subscribes(), srv.Subscribers())
		}
		p := wantPromptCount(t, srv, 1)[0]
		if !strings.Contains(p.Text, "acme/widgets#1 ") || !strings.Contains(p.Text, "acme/widgets#2 ") {
			t.Errorf("text = %q, want both PRs in one prompt", p.Text)
		}
	})
}

func TestRunStopsOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMgrHarness(t, sockA, sockB)
		h.srv(sockA).SetPane(herdrtest.PaneState{ID: testPane, Agent: "claude", Status: herdr.StatusWorking})
		s := h.sub(sockA, testPane, prN(1))
		h.sub(sockB, testPane, prN(2)) // sockB has no pane: an endpoint without a worker
		h.eng.addPending(s.ID, comments(1, 1)...)
		h.start()
		h.advance(time.Minute)
		h.stop() // Run returns nil; synctest fails on any leaked goroutine
	})
}

func TestRunTwiceManager(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newMgrHarness(t, sockA)
		h.start()
		if err := h.m.Run(context.Background()); err == nil {
			t.Error("second concurrent Run succeeded")
		}
	})
}

func TestNewNilEngine(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Error("New without an engine succeeded")
	}
}
