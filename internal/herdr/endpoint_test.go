package herdr_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/drodimin/pr-relay/internal/herdr"
	"github.com/drodimin/pr-relay/internal/herdr/herdrtest"
)

func zeroJitter() float64 { return 0 }

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

func newEndpoint(t *testing.T, o herdr.EndpointOptions) *herdr.Endpoint {
	t.Helper()
	if o.Socket == "" {
		o.Socket = "/fake/herdr.sock"
	}
	if o.Jitter == nil {
		o.Jitter = zeroJitter
	}
	ep, err := herdr.NewEndpoint(o)
	if err != nil {
		t.Fatalf("NewEndpoint: %v", err)
	}
	return ep
}

// start runs ep until the test ends (cancel + wait in Cleanup, which runs
// inside the bubble for synctest tests).
func start(t *testing.T, ep *herdr.Endpoint) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ep.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
}

// next returns the next queued event after the bubble settles.
func next(t *testing.T, ep *herdr.Endpoint) herdr.Event {
	t.Helper()
	synctest.Wait()
	select {
	case ev := <-ep.Events():
		return ev
	default:
		t.Fatalf("no event queued")
		return herdr.Event{}
	}
}

func noEvent(t *testing.T, ep *herdr.Endpoint) {
	t.Helper()
	synctest.Wait()
	select {
	case ev := <-ep.Events():
		t.Fatalf("unexpected event %+v", ev)
	default:
	}
}

func wantKind(t *testing.T, ev herdr.Event, k herdr.EventKind) {
	t.Helper()
	if ev.Kind != k {
		t.Fatalf("event = %+v, want kind %q", ev, k)
	}
	if ev.At.IsZero() {
		t.Fatalf("event %+v has zero At", ev)
	}
}

func globals() []map[string]any {
	return []map[string]any{
		{"type": "pane.closed"}, {"type": "pane.moved"}, {"type": "pane.agent_detected"},
		{"type": "pane.exited"}, {"type": "tab.closed"}, {"type": "workspace.closed"},
	}
}

func statusSub(pane string) map[string]any {
	return map[string]any{"type": "pane.agent_status_changed", "pane_id": pane}
}

func agentPane(id string) herdrtest.PaneState {
	return herdrtest.PaneState{ID: id, TabID: "t1", WorkspaceID: "w1", Agent: "claude", Session: "s-" + id, Status: herdr.StatusIdle}
}

func TestEndpointSubscribesListedPanes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := herdrtest.New(t)
		srv.SetPane(agentPane("w1:p1"))
		ep := newEndpoint(t, herdr.EndpointOptions{Dial: srv.Dialer()})
		ep.SetPanes([]string{"w9:p9", "w1:p1", "w1:p1"})
		if ep.Up() || ep.DownSince().IsZero() {
			t.Fatalf("before Run: Up=%v DownSince=%v, want down since construction", ep.Up(), ep.DownSince())
		}
		start(t, ep)
		wantKind(t, next(t, ep), herdr.KindReconnected)
		if !ep.Up() || ep.UpSince().IsZero() || !ep.DownSince().IsZero() {
			t.Fatalf("Up=%v UpSince=%v DownSince=%v", ep.Up(), ep.UpSince(), ep.DownSince())
		}
		want := append(globals(), statusSub("w1:p1"))
		if got := srv.LastSubscription(); !reflect.DeepEqual(got, want) {
			t.Fatalf("subscription = %v, want %v", got, want)
		}
		noEvent(t, ep)
	})
}

func TestEndpointPaneNotFoundRace(t *testing.T) {
	t.Run("retry", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			srv := herdrtest.New(t)
			srv.SetPane(agentPane("w1:p1"))
			srv.FailNext("events.subscribe", herdr.CodePaneNotFound)
			ep := newEndpoint(t, herdr.EndpointOptions{Dial: srv.Dialer()})
			ep.SetPanes([]string{"w1:p1"})
			t0 := time.Now()
			start(t, ep)
			wantKind(t, next(t, ep), herdr.KindReconnected)
			if d := time.Since(t0); d != 0 {
				t.Fatalf("retry took %v, want immediate", d)
			}
			if n := srv.CallCount("events.subscribe"); n != 2 {
				t.Fatalf("subscribe calls = %d, want 2", n)
			}
			if n := srv.CallCount("pane.list"); n != 2 {
				t.Fatalf("pane.list calls = %d, want 2 (re-list before retry)", n)
			}
		})
	})
	t.Run("bounded", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			srv := herdrtest.New(t)
			srv.SetPane(agentPane("w1:p1"))
			for range 5 {
				srv.FailNext("events.subscribe", herdr.CodePaneNotFound)
			}
			ep := newEndpoint(t, herdr.EndpointOptions{Dial: srv.Dialer()})
			ep.SetPanes([]string{"w1:p1"})
			start(t, ep)
			noEvent(t, ep)
			if n := srv.CallCount("events.subscribe"); n != 4 {
				t.Fatalf("subscribe calls = %d, want 4 (1 + 3 retries) before backoff", n)
			}
			time.Sleep(249 * time.Millisecond)
			noEvent(t, ep)
			time.Sleep(time.Millisecond)
			wantKind(t, next(t, ep), herdr.KindReconnected)
			if n := srv.CallCount("events.subscribe"); n != 6 {
				t.Fatalf("subscribe calls = %d, want 6", n)
			}
		})
	})
}

func TestEndpointEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := herdrtest.New(t)
		srv.SetPane(agentPane("w1:p1"))
		srv.SetPane(agentPane("w1:p2"))
		ep := newEndpoint(t, herdr.EndpointOptions{Dial: srv.Dialer()})
		ep.SetPanes([]string{"w1:p1", "w1:p2"})
		start(t, ep)
		wantKind(t, next(t, ep), herdr.KindReconnected)

		srv.SetStatus("w1:p1", herdr.StatusWorking)
		ev := next(t, ep)
		wantKind(t, ev, herdr.KindAgentStatusChanged)
		if ev.PaneID != "w1:p1" || ev.AgentStatus != herdr.StatusWorking || ev.Agent != "claude" || ev.WorkspaceID != "w1" {
			t.Fatalf("status event = %+v", ev)
		}

		srv.ClosePane("w1:p2")
		ev = next(t, ep)
		wantKind(t, ev, herdr.KindPaneClosed)
		if ev.PaneID != "w1:p2" || ev.WorkspaceID != "w1" {
			t.Fatalf("closed event = %+v", ev)
		}

		srv.MovePane("w1:p1", "w2:p1")
		ev = next(t, ep)
		wantKind(t, ev, herdr.KindPaneMoved)
		if ev.PreviousPaneID != "w1:p1" || ev.PaneID != "w2:p1" {
			t.Fatalf("moved event = %+v", ev)
		}

		srv.DetectAgent("w2:p1", "codex", "s9")
		ev = next(t, ep)
		wantKind(t, ev, herdr.KindAgentDetected)
		if ev.PaneID != "w2:p1" || ev.Agent != "codex" {
			t.Fatalf("detected event = %+v", ev)
		}

		srv.CloseTab("t1")
		ev = next(t, ep)
		wantKind(t, ev, herdr.KindTabClosed)
		if ev.TabID != "t1" {
			t.Fatalf("tab event = %+v", ev)
		}

		raw := []struct {
			line string
			want herdr.Event
		}{
			{`{"event":"pane.closed","data":{"pane_id":"w3:p1","workspace_id":"w3"}}`,
				herdr.Event{Kind: herdr.KindPaneClosed, PaneID: "w3:p1", WorkspaceID: "w3"}},
			{`{"event":"pane_agent_status_changed","data":{"agent":"codex","agent_status":"idle","pane_id":"w3:p2"}}`,
				herdr.Event{Kind: herdr.KindAgentStatusChanged, PaneID: "w3:p2", Agent: "codex", AgentStatus: "idle"}},
			{`{"event":"pane_exited","data":{"type":"pane_exited","pane_id":"w3:p3"}}`,
				herdr.Event{Kind: herdr.KindPaneExited, PaneID: "w3:p3"}},
			{`{"event":"pane.exited","data":{"pane_id":"w3:p4"}}`,
				herdr.Event{Kind: herdr.KindPaneExited, PaneID: "w3:p4"}},
			{`{"event":"workspace_closed","data":{"type":"workspace_closed","workspace_id":"w4"}}`,
				herdr.Event{Kind: herdr.KindWorkspaceClosed, WorkspaceID: "w4"}},
			{`{"event":"workspace.closed","data":{"workspace_id":"w5"}}`,
				herdr.Event{Kind: herdr.KindWorkspaceClosed, WorkspaceID: "w5"}},
			{`{"event":"tab.closed","data":{"tab_id":"t7"}}`,
				herdr.Event{Kind: herdr.KindTabClosed, TabID: "t7"}},
			{`{"event":"pane.agent_detected","data":{"pane_id":"w3:p5","agent":"claude"}}`,
				herdr.Event{Kind: herdr.KindAgentDetected, PaneID: "w3:p5", Agent: "claude"}},
			{`{"event":"pane.moved","data":{"previous_pane_id":"w3:p6","pane":{"pane_id":"w6:p1","tab_id":"t6","workspace_id":"w6"}}}`,
				herdr.Event{Kind: herdr.KindPaneMoved, PaneID: "w6:p1", PreviousPaneID: "w3:p6", TabID: "t6", WorkspaceID: "w6"}},
		}
		for _, r := range raw {
			srv.EmitRaw(`{"event":"pane.updated","data":{"pane_id":"x"}}`) // unknown: ignored
			srv.EmitRaw(`not json`)                                        // malformed: ignored
			srv.EmitRaw(r.line)
			ev := next(t, ep)
			at := ev.At
			ev.At = time.Time{}
			if ev != r.want || at.IsZero() {
				t.Fatalf("%s decoded to %+v (At %v), want %+v", r.line, ev, at, r.want)
			}
		}
		noEvent(t, ep)
		if !ep.Up() {
			t.Fatal("malformed or unknown lines must not drop the stream")
		}
	})
}

// waitEvent waits in real time (outside synctest) for the next event.
func waitEvent(t *testing.T, ep *herdr.Endpoint) herdr.Event {
	t.Helper()
	select {
	case ev := <-ep.Events():
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for event")
		return herdr.Event{}
	}
}

func TestEndpointReconnect(t *testing.T) {
	srv := herdrtest.New(t)
	srv.SetPane(agentPane("w1:p1"))
	path := srv.ListenUnix()
	ep := newEndpoint(t, herdr.EndpointOptions{Socket: path, BackoffBase: 10 * time.Millisecond,
		BackoffMax: 50 * time.Millisecond})
	ep.SetPanes([]string{"w1:p1"})
	start(t, ep)

	wantKind(t, waitEvent(t, ep), herdr.KindReconnected)
	if !ep.Up() {
		t.Fatal("not up after Reconnected")
	}
	srv.Stop()
	wantKind(t, waitEvent(t, ep), herdr.KindDisconnected)
	if ep.Up() || ep.DownSince().IsZero() || !ep.UpSince().IsZero() {
		t.Fatalf("after Stop: Up=%v UpSince=%v DownSince=%v", ep.Up(), ep.UpSince(), ep.DownSince())
	}
	time.Sleep(100 * time.Millisecond) // several failed attempts while down
	srv.Restart()
	wantKind(t, waitEvent(t, ep), herdr.KindReconnected)
	if !ep.Up() {
		t.Fatal("not up after restart")
	}
	srv.SetStatus("w1:p1", herdr.StatusWorking)
	ev := waitEvent(t, ep)
	wantKind(t, ev, herdr.KindAgentStatusChanged)
	select {
	case ev := <-ep.Events():
		t.Fatalf("duplicated or invented event %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
	if n := srv.CloseOnWrites(); n != 0 {
		t.Fatalf("close-on-write = %d, want 0", n)
	}
}

func TestEndpointBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := herdrtest.New(t)
		srv.SetPane(agentPane("w1:p1"))
		srv.Stop()
		var mu sync.Mutex
		var dials []time.Duration
		t0 := time.Now()
		inner := srv.Dialer()
		dial := func(ctx context.Context, socket string) (net.Conn, error) {
			mu.Lock()
			dials = append(dials, time.Since(t0))
			mu.Unlock()
			return inner(ctx, socket)
		}
		firstDialAfter := func(d time.Duration) time.Duration {
			mu.Lock()
			defer mu.Unlock()
			for _, x := range dials {
				if x > d {
					return x
				}
			}
			t.Fatalf("no dial after %v (dials %v)", d, dials)
			return 0
		}
		ep := newEndpoint(t, herdr.EndpointOptions{Dial: dial})
		start(t, ep)

		time.Sleep(70 * time.Second)
		synctest.Wait()
		mu.Lock()
		var gaps []time.Duration
		for i := 1; i < len(dials); i++ {
			gaps = append(gaps, dials[i]-dials[i-1])
		}
		mu.Unlock()
		ms := time.Millisecond
		want := []time.Duration{250 * ms, 500 * ms, time.Second, 2 * time.Second, 4 * time.Second,
			8 * time.Second, 16 * time.Second, 30 * time.Second}
		if !reflect.DeepEqual(gaps, want) {
			t.Fatalf("gaps = %v, want %v", gaps, want)
		}
		noEvent(t, ep) // never up, so no Disconnected

		// Up again at the next attempt (91.75s), then a short-lived
		// connection: the backoff is not reset.
		srv.Restart()
		time.Sleep(25 * time.Second) // t = 95s
		wantKind(t, next(t, ep), herdr.KindReconnected)
		srv.Stop()
		wantKind(t, next(t, ep), herdr.KindDisconnected)
		time.Sleep(31 * time.Second) // t = 126s
		synctest.Wait()
		if got := firstDialAfter(95 * time.Second); got != 125*time.Second {
			t.Fatalf("after short connection next dial at %v, want 125s", got)
		}

		// Next attempt at 155s connects; stay up 61s, then drop: reset.
		srv.Restart()
		time.Sleep(30 * time.Second) // t = 156s
		wantKind(t, next(t, ep), herdr.KindReconnected)
		time.Sleep(60 * time.Second) // t = 216s, up for 61s
		srv.Stop()
		wantKind(t, next(t, ep), herdr.KindDisconnected)
		time.Sleep(time.Second)
		synctest.Wait()
		if got := firstDialAfter(216 * time.Second); got != 216*time.Second+250*ms {
			t.Fatalf("after stable connection next dial at %v, want 216.25s", got)
		}
	})
}

func TestEndpointBackoffJitter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := herdrtest.New(t)
		srv.Stop()
		var mu sync.Mutex
		var dials []time.Time
		inner := srv.Dialer()
		dial := func(ctx context.Context, socket string) (net.Conn, error) {
			mu.Lock()
			dials = append(dials, time.Now())
			mu.Unlock()
			return inner(ctx, socket)
		}
		ep := newEndpoint(t, herdr.EndpointOptions{Dial: dial, Jitter: func() float64 { return 1 }})
		start(t, ep)
		time.Sleep(time.Second)
		synctest.Wait()
		mu.Lock()
		defer mu.Unlock()
		if len(dials) < 3 || dials[1].Sub(dials[0]) != 300*time.Millisecond || dials[2].Sub(dials[1]) != 600*time.Millisecond {
			t.Fatalf("jittered dials %v, want gaps 300ms, 600ms", dials)
		}
	})
}

func TestEventsLost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := herdrtest.New(t)
		srv.SetPane(agentPane("w1:p1"))
		ep := newEndpoint(t, herdr.EndpointOptions{Dial: srv.Dialer()})
		ep.SetPanes([]string{"w1:p1"})
		start(t, ep)
		wantKind(t, next(t, ep), herdr.KindReconnected)
		t0 := time.Now()
		srv.LoseEvents()
		wantKind(t, next(t, ep), herdr.KindReconnected)
		if d := time.Since(t0); d != 0 {
			t.Fatalf("resubscribe after events_lost took %v, want no delay", d)
		}
		if n := srv.Subscribes(); n != 2 {
			t.Fatalf("subscribes = %d, want 2", n)
		}
		noEvent(t, ep)
		if !ep.Up() {
			t.Fatal("not up after events_lost resubscribe")
		}
	})
}

func TestSetPanesResubscribes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := herdrtest.New(t)
		srv.SetPane(agentPane("w1:p1"))
		srv.SetPane(agentPane("w1:p2"))
		ep := newEndpoint(t, herdr.EndpointOptions{Dial: srv.Dialer()})
		ep.SetPanes([]string{"w1:p1"})
		start(t, ep)
		wantKind(t, next(t, ep), herdr.KindReconnected)

		ep.SetPanes([]string{"w1:p2", "w1:p1"})
		wantKind(t, next(t, ep), herdr.KindReconnected) // no Disconnected first
		want := append(globals(), statusSub("w1:p1"), statusSub("w1:p2"))
		if got := srv.LastSubscription(); !reflect.DeepEqual(got, want) {
			t.Fatalf("subscription = %v, want %v", got, want)
		}
		if n := srv.Subscribers(); n != 1 {
			t.Fatalf("open subscribers = %d, want 1 (old stream closed)", n)
		}

		ep.SetPanes([]string{"w1:p1", "w1:p2"}) // same set: no resubscribe
		noEvent(t, ep)
		if n := srv.Subscribes(); n != 2 {
			t.Fatalf("subscribes = %d, want 2", n)
		}
		if n := srv.CloseOnWrites(); n != 0 {
			t.Fatalf("close-on-write = %d, want 0 (never write on a stream)", n)
		}
	})
}

func TestRunTwice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := herdrtest.New(t)
		ep := newEndpoint(t, herdr.EndpointOptions{Dial: srv.Dialer()})
		start(t, ep)
		synctest.Wait()
		if err := ep.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "already running") {
			t.Fatalf("second Run = %v, want already running", err)
		}
	})
}

func TestEndpointProtocolMismatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := herdrtest.New(t)
		srv.SetProtocol(21)
		var buf lockedBuf
		log := slog.New(slog.NewTextHandler(&buf, nil))
		ep := newEndpoint(t, herdr.EndpointOptions{Dial: srv.Dialer(), Logger: log})
		start(t, ep)
		wantKind(t, next(t, ep), herdr.KindReconnected)
		out := buf.String()
		if !strings.Contains(out, "herdr protocol mismatch") || !strings.Contains(out, "got=21") {
			t.Fatalf("log = %q, want protocol mismatch warning", out)
		}
	})
}
