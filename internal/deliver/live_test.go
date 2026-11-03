package deliver

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/herdr"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/poller"
	"github.com/drodimin/pr-relay/internal/store"
)

// TestLiveHerdr delivers one synthetic update into a real pane of a scratch
// herdr session. It is opt-in and never runs in CI: it runs only when
// PR_RELAY_HERDR_SCRATCH_SOCKET names a scratch session socket (a path
// containing /sessions/; the default ~/.config/herdr/herdr.sock is refused
// with a failure, never a skip) and PR_RELAY_HERDR_SCRATCH_PANE names a pane
// running an agent. The gate is deliberately not herdr's own pane
// environment marker, which is set in every herdr pane. Invocation:
//
//	PR_RELAY_HERDR_SCRATCH_SOCKET=~/.config/herdr/sessions/prspike/herdr.sock PR_RELAY_HERDR_SCRATCH_PANE=w1:p1 go test ./internal/deliver -run TestLiveHerdr -v
func TestLiveHerdr(t *testing.T) {
	sock := os.Getenv("PR_RELAY_HERDR_SCRATCH_SOCKET")
	if sock == "" {
		t.Skip("live herdr test; set PR_RELAY_HERDR_SCRATCH_SOCKET (scratch session socket) and PR_RELAY_HERDR_SCRATCH_PANE to run")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(sock, "~/") {
		sock = filepath.Join(home, sock[2:])
	}
	sock = filepath.Clean(sock)
	if sock == filepath.Join(home, ".config/herdr/herdr.sock") || !strings.Contains(sock, "/sessions/") {
		t.Fatalf("refusing %s: only scratch herdr sessions (a path containing /sessions/) are allowed", sock)
	}
	paneID := os.Getenv("PR_RELAY_HERDR_SCRATCH_PANE")
	if paneID == "" {
		t.Skip("set PR_RELAY_HERDR_SCRATCH_PANE to the scratch pane id (for example w1:p1)")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	cl, err := herdr.NewClient(herdr.Options{Socket: sock})
	if err != nil {
		t.Fatal(err)
	}

	t.Log("ping", sock)
	pong, err := cl.Ping(ctx)
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	if pong.Protocol != herdr.Protocol {
		t.Logf("warning: herdr %s speaks protocol %d, pr-relay expects %d", pong.Version, pong.Protocol, herdr.Protocol)
	}

	t.Log("pane.list")
	panes, err := cl.PaneList(ctx)
	if err != nil {
		t.Fatalf("pane.list: %v", err)
	}
	found := false
	for _, p := range panes {
		if p.PaneID == paneID {
			found = true
		}
	}
	if !found {
		t.Fatalf("pane %s not in pane.list (%d panes)", paneID, len(panes))
	}

	t.Log("agent.get", paneID)
	info, err := cl.AgentGet(ctx, paneID)
	if err != nil {
		t.Fatalf("agent.get %s: %v (the pane must run an agent)", paneID, err)
	}
	if info.Agent == "" {
		t.Fatalf("pane %s has no agent", paneID)
	}
	session := ""
	if info.AgentSession != nil {
		session = info.AgentSession.Value
	}
	t.Logf("agent=%s session=%q status=%s", info.Agent, session, info.Status)

	ref := model.PRRef{Owner: "pr-relay-live", Repo: "test", Number: 1}
	key := store.PRKey(ref)
	eng := newFakeEngine()
	sub := store.Subscription{ID: store.SubID(sock, paneID, key), PR: key, HerdrSocket: sock, PaneID: paneID,
		AgentKind: info.Agent, AgentSession: session, TerminalID: info.TerminalID}
	eng.addSub(sub)
	eng.setView(key, poller.PRView{Ref: ref, HeadOID: "head1", State: "OPEN", Baselined: true})
	eng.addPending(sub.ID, comments(1, 1)...)

	mgr, err := New(Options{
		Engine:      eng,
		Logger:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})),
		QuietPeriod: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- mgr.Run(runCtx) }()
	defer func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("Run = %v", err)
		}
	}()

	t.Log("waiting up to 2m for delivery (the agent must go idle)")
	deadline := time.Now().Add(2 * time.Minute)
	for len(eng.ackLog()) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no delivery within 2m (pending=%d)", eng.pendingCount(sub.ID))
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("delivered: acks=%v", eng.ackLog())
	if c := eng.cancelLog(); len(c) != 0 {
		t.Errorf("unexpected cancellations: %v", c)
	}
}
