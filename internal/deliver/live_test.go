package deliver

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/herdr"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/poller"
	"github.com/drodimin/pr-relay/internal/scratchguard"
	"github.com/drodimin/pr-relay/internal/store"
)

// TestLiveHerdr delivers one synthetic update into a real pane of a scratch
// herdr session. It passes only when the delivered text is read back from the
// pane with pane.read, and only then checks the ack: an ack alone (the SC3
// failure signature: acked but never typed) fails the test. It is opt-in and
// never runs in CI: it runs only when PR_RELAY_HERDR_SCRATCH_SOCKET names a
// scratch session socket (scratchguard.Check: a path containing /sessions/,
// also after resolving symlinks; the default ~/.config/herdr/herdr.sock is
// refused with a failure, never a skip) and PR_RELAY_HERDR_SCRATCH_PANE names
// a pane running an agent. The gate is deliberately not herdr's own pane
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
	if err := scratchguard.Check(sock); err != nil {
		t.Fatal(err)
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

	// Unique per run, so text from an earlier run cannot satisfy the check.
	ref := model.PRRef{Owner: "pr-relay-live", Repo: "test", Number: int(time.Now().Unix()%1_000_000) + 1}
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

	t.Logf("waiting up to 2m for %s to appear in pane %s (the agent must go idle)", ref, paneID)
	want := ref.String()
	var last string
	deadline := time.Now().Add(2 * time.Minute)
	for {
		text, err := readPane(ctx, cl, paneID)
		if err != nil {
			t.Logf("pane.read: %v", err)
		} else {
			last = text
			if strings.Contains(text, want) {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared in pane %s within 2m (pending=%d acks=%v)\nlast 20 lines read:\n%s",
				want, paneID, eng.pendingCount(sub.ID), eng.ackLog(), lastLines(last, 20))
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("%s read back from pane %s", want, paneID)

	// The ack may follow a completion confirmation (plan 03-09).
	deadline = time.Now().Add(60 * time.Second)
	for len(eng.ackLog()) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("text reached the pane but no ack within 60s (pending=%d)", eng.pendingCount(sub.ID))
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("delivered: acks=%v", eng.ackLog())
	if c := eng.cancelLog(); len(c) != 0 {
		t.Errorf("unexpected cancellations: %v", c)
	}
}

// readPane returns the recent unwrapped text of pane via herdr pane.read.
func readPane(ctx context.Context, cl *herdr.Client, pane string) (string, error) {
	var out struct {
		Type string `json:"type"`
		Read struct {
			Text string `json:"text"`
		} `json:"read"`
	}
	params := map[string]any{"pane_id": pane, "source": "recent_unwrapped", "lines": 400}
	if err := cl.Call(ctx, "pane.read", params, &out); err != nil {
		return "", err
	}
	if out.Type != "pane_read" {
		return "", fmt.Errorf("pane.read: unexpected result type %q", out.Type)
	}
	return out.Read.Text, nil
}

// lastLines returns the last n lines of s.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
