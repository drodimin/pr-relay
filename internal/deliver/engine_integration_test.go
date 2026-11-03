package deliver

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/drodimin/pr-relay/internal/config"
	"github.com/drodimin/pr-relay/internal/ghclient"
	"github.com/drodimin/pr-relay/internal/ghquery"
	"github.com/drodimin/pr-relay/internal/herdr"
	"github.com/drodimin/pr-relay/internal/herdr/herdrtest"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/poller"
	"github.com/drodimin/pr-relay/internal/store"
)

// memSaver is a minimal in-memory poller.Saver (a copy of the poller test
// helper, which is not importable).
type memSaver struct {
	mu    sync.Mutex
	saves int
}

func (s *memSaver) Save(*store.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	return nil
}

// stubFetcher answers every batch with the current scripted snapshot, so a
// scheduler poll the test did not ask for repeats the last state and
// derives no new events.
type stubFetcher struct {
	mu   sync.Mutex
	snap model.Snapshot
}

func (f *stubFetcher) set(s model.Snapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snap = s
}

func (f *stubFetcher) Batch(_ context.Context, refs []model.PRRef, _ ghquery.Window) (ghclient.BatchResult, error) {
	f.mu.Lock()
	snap := f.snap
	f.mu.Unlock()
	reset := time.Now().Add(time.Hour)
	res := ghclient.BatchResult{
		Viewer:    "me",
		RateLimit: ghquery.RateLimit{Cost: 1, Limit: 5000, Remaining: 4900, Used: 100, ResetAt: reset.UTC().Format(time.RFC3339)},
		ResetAt:   reset,
	}
	for _, r := range refs {
		s := snap
		s.Ref = r
		res.Items = append(res.Items, ghquery.AliasResult{Ref: r, Snapshot: s})
	}
	return res, nil
}

var integPR = model.PRRef{Owner: "acme", Repo: "widgets", Number: 7}

func integSnap(head string, commits []string, check *model.CheckRun, comments ...model.IssueComment) model.Snapshot {
	s := model.Snapshot{
		Ref:       integPR,
		NodeID:    "PR_7",
		State:     "OPEN",
		HeadOID:   head,
		BaseOID:   "base1",
		Mergeable: "MERGEABLE",
		CIKnown:   true,
		Comments:  comments,
	}
	for _, c := range commits {
		s.Commits = append(s.Commits, model.Commit{OID: c, Author: model.Actor{Login: "alice", Type: "User"}})
	}
	s.CommitsTotal = len(commits)
	s.CommentsTotal = len(comments)
	if check != nil {
		s.Checks = []model.CheckRun{*check}
	}
	return s
}

func lintCheck(status, conclusion string) *model.CheckRun {
	return &model.CheckRun{Key: "CI/lint", Name: "lint", Workflow: "CI", DatabaseID: 1, Status: status,
		Conclusion: conclusion, URL: "https://github.com/acme/widgets/runs/1"}
}

func bobComment() model.IssueComment {
	return model.IssueComment{ID: "IC_1", URL: "https://github.com/acme/widgets/pull/7#issuecomment-1",
		Author: model.Actor{Login: "bob", Type: "User"}, CreatedAt: time.Now()}
}

// integ runs a real *poller.Engine and a Manager against one fake herdr.
type integ struct {
	t   *testing.T
	f   *stubFetcher
	e   *poller.Engine
	srv *herdrtest.Server
	sub string

	cancel context.CancelFunc
	done   chan struct{}
}

func newInteg(t *testing.T, status string) *integ {
	t.Helper()
	f := &stubFetcher{}
	f.set(integSnap("sha1", []string{"sha1"}, lintCheck("IN_PROGRESS", "")))
	e, err := poller.New(poller.Options{Saver: &memSaver{}, State: store.NewState(), Fetcher: f,
		Config: config.Defaults(), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("poller.New: %v", err)
	}
	srv := herdrtest.New(t)
	srv.SetPane(herdrtest.PaneState{ID: testPane, TabID: "t1", WorkspaceID: "w1", Agent: "claude", Status: status})
	info, err := e.Subscribe(poller.SubRequest{PR: integPR, HerdrSocket: testSock, PaneID: testPane, AgentKind: "claude"})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	m, err := New(Options{Engine: e, Dial: srv.Dialer(), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := &integ{t: t, f: f, e: e, srv: srv, sub: info.SubID}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = e.Run(ctx) }()
	go func() { defer wg.Done(); _ = m.Run(ctx) }()
	go func() { wg.Wait(); close(h.done) }()
	t.Cleanup(func() { cancel(); <-h.done })
	synctest.Wait() // the scheduler baselines the new PR
	return h
}

func (h *integ) poll(s model.Snapshot) {
	h.t.Helper()
	h.f.set(s)
	if _, err := h.e.PollNow(context.Background(), integPR); err != nil {
		h.t.Fatalf("PollNow: %v", err)
	}
	synctest.Wait()
}

func (h *integ) pending() int {
	h.t.Helper()
	d, err := h.e.Pending(h.sub)
	if err != nil {
		h.t.Fatalf("Pending: %v", err)
	}
	return len(d.Events)
}

func TestEngineIntegration(t *testing.T) {
	t.Run("coalesce", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newInteg(t, herdr.StatusIdle)
			if n := h.pending(); n != 0 {
				t.Fatalf("pending after baseline = %d", n)
			}
			h.poll(integSnap("sha1", []string{"sha1"}, lintCheck("COMPLETED", "FAILURE"), bobComment()))
			if n := h.pending(); n != 2 {
				t.Fatalf("pending = %d, want 2 (ci.failed + comment)", n)
			}
			time.Sleep(9 * time.Second)
			synctest.Wait()
			if n := len(h.srv.Prompts()); n != 0 {
				t.Fatalf("prompted inside the quiet period: %d", n)
			}
			time.Sleep(2 * time.Second)
			synctest.Wait()
			ps := h.srv.Prompts()
			if len(ps) != 1 {
				t.Fatalf("prompts = %d, want 1", len(ps))
			}
			text := ps[0].Text
			if lines(text) != 2 || !strings.Contains(text, "ci.failed") || !strings.Contains(text, "comment by bob") {
				t.Errorf("text = %q, want one ci.failed and one comment line", text)
			}
			if n := h.pending(); n != 0 {
				t.Errorf("pending after delivery = %d (not acked)", n)
			}
		})
	})

	t.Run("superseded-ci", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newInteg(t, herdr.StatusWorking)
			h.poll(integSnap("sha1", []string{"sha1"}, lintCheck("COMPLETED", "FAILURE")))
			if n := h.pending(); n != 1 {
				t.Fatalf("pending = %d, want 1 (ci.failed on sha1)", n)
			}
			time.Sleep(time.Minute)
			synctest.Wait()
			// The head moves before the pane becomes free.
			h.poll(integSnap("sha2", []string{"sha1", "sha2"}, nil))
			if n := len(h.srv.Prompts()); n != 0 {
				t.Fatalf("prompted a working pane: %d", n)
			}
			h.srv.SetStatus(testPane, herdr.StatusIdle)
			synctest.Wait()
			time.Sleep(11 * time.Second)
			synctest.Wait()
			ps := h.srv.Prompts()
			if len(ps) != 1 {
				t.Fatalf("prompts = %d, want 1", len(ps))
			}
			if strings.Contains(ps[0].Text, "ci.failed") {
				t.Errorf("superseded ci.failed for sha1 delivered: %q", ps[0].Text)
			}
			if n := h.pending(); n != 0 {
				t.Errorf("pending after delivery = %d", n)
			}
		})
	})

	t.Run("pane-closed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newInteg(t, herdr.StatusIdle)
			h.srv.ClosePane(testPane)
			synctest.Wait()
			if subs := h.e.List(); len(subs) != 0 {
				t.Errorf("subs after pane close = %v", subs)
			}
			cs := h.e.RecentCancellations()
			if len(cs) != 1 || cs[0].SubID != h.sub || cs[0].Reason != poller.ReasonPaneClosed {
				t.Errorf("cancellations = %+v", cs)
			}
		})
	})
}
