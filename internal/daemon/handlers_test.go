package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/ctl"
	"github.com/drodimin/pr-relay/internal/ghclient"
	"github.com/drodimin/pr-relay/internal/ghquery"
	"github.com/drodimin/pr-relay/internal/herdr"
	"github.com/drodimin/pr-relay/internal/herdr/herdrtest"
	"github.com/drodimin/pr-relay/internal/poller"
	"github.com/drodimin/pr-relay/internal/store"
)

const (
	prURL  = "https://github.com/o/r/pull/12"
	prKey  = "o/r#12"
	pr2URL = "https://github.com/o/r/pull/13"
)

// fakeClock is a settable, goroutine-safe clock for the poller.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fixture is a daemon plus a fake herdr with panes w1:p1 (claude, s1),
// w1:p2 (codex, s2) and w1:p3 (bare shell).
type fixture struct {
	d     *daemonRun
	sock  string
	hsock string
	hd    *herdrtest.Server
	f     *fakeFetcher
	clk   *fakeClock
}

func setup(t *testing.T) *fixture {
	t.Helper()
	sock := testEnv(t)
	hd := herdrtest.New(t)
	hd.SetPane(herdrtest.PaneState{ID: "w1:p1", Agent: "claude", Session: "s1", Status: herdr.StatusIdle, TerminalID: "t1"})
	hd.SetPane(herdrtest.PaneState{ID: "w1:p2", Agent: "codex", Session: "s2", Status: herdr.StatusIdle, TerminalID: "t2"})
	hd.SetPane(herdrtest.PaneState{ID: "w1:p3", Status: herdr.StatusUnknown, TerminalID: "t3"})
	hsock := hd.ListenUnix()
	f := newFakeFetcher()
	clk := &fakeClock{t: time.Now().Round(0)}
	d := startDaemon(t, Options{Fetcher: f, Now: clk.Now})
	return &fixture{d: d, sock: sock, hsock: hsock, hd: hd, f: f, clk: clk}
}

func (fx *fixture) target(pane string) ctl.Target {
	return ctl.Target{HerdrSocket: fx.hsock, PaneID: pane}
}

func (fx *fixture) sub(t *testing.T, url, pane string) (ctl.SubResult, error) {
	t.Helper()
	var r ctl.SubResult
	err := call(t, fx.sock, ctl.MethodSub, ctl.SubParams{URL: url, Target: fx.target(pane)}, &r)
	return r, err
}

func (fx *fixture) mustSub(t *testing.T, url, pane string) ctl.SubResult {
	t.Helper()
	r, err := fx.sub(t, url, pane)
	if err != nil {
		t.Fatalf("sub %s %s: %v", url, pane, err)
	}
	return r
}

func wantCode(t *testing.T, err error, code, msg string) {
	t.Helper()
	if got := ctl.Code(err); got != code {
		t.Fatalf("code = %q (%v), want %q", got, err, code)
	}
	if msg != "" && !strings.Contains(err.Error(), msg) {
		t.Fatalf("error %q lacks %q", err.Error(), msg)
	}
}

func (fx *fixture) subs() []store.Subscription { return fx.d.host.eng.List() }

func TestSubOK(t *testing.T) {
	fx := setup(t)
	r := fx.mustSub(t, prURL, "w1:p1")
	if r.PR != prKey || r.URL != prURL || !r.Created || r.Warning != "" {
		t.Errorf("sub = %+v", r)
	}
	if r.Interval <= 0 || !r.NextPollAt.After(fx.clk.Now()) {
		t.Errorf("interval %v next %v (now %v)", r.Interval, r.NextPollAt, fx.clk.Now())
	}
	if len(r.CoSubscribers) != 0 {
		t.Errorf("co-subscribers = %v", r.CoSubscribers)
	}
	s := fx.subs()
	if len(s) != 1 || s[0].AgentKind != "claude" || s[0].AgentSession != "s1" || s[0].TerminalID != "t1" {
		t.Errorf("stored = %+v", s)
	}

	r2 := fx.mustSub(t, "github.com/o/r/pull/12/files", "w1:p2")
	if len(r2.CoSubscribers) != 1 || r2.CoSubscribers[0] != "w1:p1" {
		t.Errorf("co-subscribers = %v, want [w1:p1]", r2.CoSubscribers)
	}
	again := fx.mustSub(t, prURL, "w1:p1")
	if again.Created {
		t.Errorf("re-subscribe Created = true")
	}
	if fx.f.count() != 1 {
		t.Errorf("GitHub calls = %d, want 1 (joins need no fetch)", fx.f.count())
	}

	// Event masks are normalised and reported.
	var rm ctl.SubResult
	if err := call(t, fx.sock, ctl.MethodSub, ctl.SubParams{URL: pr2URL, Events: []string{"review", "review", "ci.failed"}, Target: fx.target("w1:p1")}, &rm); err != nil {
		t.Fatal(err)
	}
	if strings.Join(rm.Events, ",") != "review,ci.failed" {
		t.Errorf("events = %v", rm.Events)
	}
	err := call(t, fx.sock, ctl.MethodSub, ctl.SubParams{URL: pr2URL, Events: []string{"bogus"}, Target: fx.target("w1:p1")}, nil)
	wantCode(t, err, ctl.CodeBadRequest, "unknown event type")
}

func TestSubHerdr(t *testing.T) {
	fx := setup(t)
	_, err := fx.sub(t, prURL, "w9:p9")
	wantCode(t, err, ctl.CodePaneNotFound, "pane w9:p9 not found")
	if n := len(fx.subs()); n != 0 {
		t.Errorf("stored %d subs after pane_not_found", n)
	}

	dead := filepath.Join(filepath.Dir(fx.hsock), "dead.sock")
	err = call(t, fx.sock, ctl.MethodSub, ctl.SubParams{URL: prURL, Target: ctl.Target{HerdrSocket: dead, PaneID: "w1:p1"}}, nil)
	wantCode(t, err, ctl.CodeHerdrUnreachable, "herdr not reachable")
	if n := len(fx.subs()); n != 0 {
		t.Errorf("stored %d subs after herdr_unreachable", n)
	}
	if fx.f.count() != 0 {
		t.Errorf("GitHub was called %d times before herdr confirmed the pane", fx.f.count())
	}

	r := fx.mustSub(t, prURL, "w1:p3")
	if !strings.Contains(r.Warning, "updates are delivered only while an agent runs in the pane") {
		t.Errorf("warning = %q", r.Warning)
	}
}

func TestSubGitHubErrors(t *testing.T) {
	cases := []struct {
		name  string
		state string
		item  error
		batch error
		code  string
		msg   string
	}{
		{"not found", "", ghquery.ErrNotFound, nil, ctl.CodeNotFound, "not found"},
		{"forbidden", "", ghquery.ErrForbidden, nil, ctl.CodeForbidden, prKey},
		{"unauthorized", "", nil, ghclient.ErrUnauthorized, ctl.CodeUnauthorized, "gh auth login"},
		{"no token", "", nil, ghclient.ErrNoToken, ctl.CodeNoToken, "gh auth login"},
		{"rate limited", "", nil, &ghclient.PrimaryExhaustedError{ResetAt: time.Now().Add(time.Hour)}, ctl.CodeRateLimited, "retry later"},
		{"secondary", "", nil, &ghclient.SecondaryLimitError{Status: 403}, ctl.CodeSecondaryLimit, "retry later"},
		{"transient", "", nil, &ghclient.TransientError{Status: 502}, ctl.CodeUnavailable, "retry later"},
		{"merged", "MERGED", nil, nil, ctl.CodePRClosed, "already merged or closed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := setup(t)
			fx.f.set(prKey, c.state, c.item)
			fx.f.setBatchErr(c.batch)
			_, err := fx.sub(t, prURL, "w1:p1")
			wantCode(t, err, c.code, c.msg)
			if c.code == ctl.CodeNotFound && !strings.Contains(err.Error(), prKey) {
				t.Errorf("message %q lacks %s", err, prKey)
			}
			if n := len(fx.subs()); n != 0 {
				t.Errorf("stored %d subs after %s", n, c.code)
			}
		})
	}
}

func TestSubBadRequest(t *testing.T) {
	fx := setup(t)
	_, err := fx.sub(t, "https://gitlab.com/o/r/pull/1", "w1:p1")
	wantCode(t, err, ctl.CodeBadRequest, "only github.com is supported")
	_, err = fx.sub(t, "o/r#12", "w1:p1")
	wantCode(t, err, ctl.CodeBadRequest, "not a link")
	err = call(t, fx.sock, ctl.MethodSub, ctl.SubParams{URL: prURL, Target: ctl.Target{HerdrSocket: "rel/h.sock", PaneID: "w1:p1"}}, nil)
	wantCode(t, err, ctl.CodeBadRequest, "absolute")
	err = call(t, fx.sock, ctl.MethodSub, ctl.SubParams{URL: prURL, Target: ctl.Target{HerdrSocket: fx.hsock}}, nil)
	wantCode(t, err, ctl.CodeBadRequest, "pane id")
	err = call(t, fx.sock, ctl.MethodSub, map[string]any{"url": prURL, "bogus": 1}, nil)
	wantCode(t, err, ctl.CodeBadRequest, "invalid params")
	if fx.f.count() != 0 || len(fx.subs()) != 0 {
		t.Errorf("bad requests reached GitHub (%d) or state (%d)", fx.f.count(), len(fx.subs()))
	}
}

func TestUnsub(t *testing.T) {
	fx := setup(t)
	fx.mustSub(t, prURL, "w1:p1")
	fx.mustSub(t, pr2URL, "w1:p1")
	fx.mustSub(t, prURL, "w1:p2")

	var r ctl.UnsubResult
	if err := call(t, fx.sock, ctl.MethodUnsub, ctl.UnsubParams{URL: prURL, Target: fx.target("w1:p3")}, &r); err != nil {
		t.Fatalf("unsub without sub: %v", err)
	}
	if r.Removed != 0 {
		t.Errorf("removed = %d, want 0", r.Removed)
	}
	err := call(t, fx.sock, ctl.MethodUnsub, ctl.UnsubParams{Target: fx.target("w1:p1")}, nil)
	wantCode(t, err, ctl.CodeBadRequest, "--all")

	if err := call(t, fx.sock, ctl.MethodUnsub, ctl.UnsubParams{All: true, Target: fx.target("w1:p1")}, &r); err != nil {
		t.Fatal(err)
	}
	if r.Removed != 2 {
		t.Errorf("removed = %d, want 2", r.Removed)
	}
	s := fx.subs()
	if len(s) != 1 || s[0].PaneID != "w1:p2" {
		t.Errorf("left = %+v", s)
	}
	if err := call(t, fx.sock, ctl.MethodUnsub, ctl.UnsubParams{URL: prURL, Target: fx.target("w1:p2")}, &r); err != nil || r.Removed != 1 {
		t.Errorf("unsub URL = %d, %v", r.Removed, err)
	}
}

func TestKeep(t *testing.T) {
	fx := setup(t)
	err := call(t, fx.sock, ctl.MethodKeep, ctl.KeepParams{URL: prURL, Target: fx.target("w1:p1")}, nil)
	wantCode(t, err, ctl.CodeNotWatched, "not subscribed")

	fx.mustSub(t, prURL, "w1:p1")
	fx.clk.Advance(time.Hour)
	var r ctl.KeepResult
	if err := call(t, fx.sock, ctl.MethodKeep, ctl.KeepParams{URL: prURL, Target: fx.target("w1:p1")}, &r); err != nil {
		t.Fatal(err)
	}
	now := fx.clk.Now()
	if d := r.CheckinDue.Sub(now.Add(24 * time.Hour)); d < -time.Second || d > time.Second {
		t.Errorf("checkin due = %v, want about %v", r.CheckinDue, now.Add(24*time.Hour))
	}
	if d := r.ExpiresAt.Sub(now.Add(7 * 24 * time.Hour)); d < -time.Second || d > time.Second {
		t.Errorf("expires = %v, want about %v", r.ExpiresAt, now.Add(7*24*time.Hour))
	}
	if r.PR != prKey {
		t.Errorf("pr = %q", r.PR)
	}

	// An expired subscription cannot be kept. The pane is busy so the
	// expired line stays queued.
	fx.hd.SetStatus("w1:p1", herdr.StatusWorking)
	fx.clk.Advance(8 * 24 * time.Hour)
	waitFor(t, "subscription expiring", func() bool {
		s := fx.subs()
		return len(s) == 1 && s[0].Ending == poller.ReasonExpired
	})
	err = call(t, fx.sock, ctl.MethodKeep, ctl.KeepParams{URL: prURL, Target: fx.target("w1:p1")}, nil)
	wantCode(t, err, ctl.CodeEnded, "re-subscribe")

	var ls ctl.LsResult
	if err := call(t, fx.sock, ctl.MethodLs, ctl.LsParams{All: true}, &ls); err != nil {
		t.Fatal(err)
	}
	if len(ls.Rows) != 1 || ls.Rows[0].Ending != poller.ReasonExpired {
		t.Errorf("ls rows = %+v", ls.Rows)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPoll(t *testing.T) {
	fx := setup(t)
	err := call(t, fx.sock, ctl.MethodPoll, ctl.PollParams{URL: prURL}, nil)
	wantCode(t, err, ctl.CodeNotWatched, "not watched")

	fx.mustSub(t, prURL, "w1:p1")
	var r ctl.PollResult
	if err := call(t, fx.sock, ctl.MethodPoll, ctl.PollParams{URL: prURL}, &r); err != nil {
		t.Fatal(err)
	}
	if r.PR != prKey || r.NewEvents != 0 || r.Interval <= 0 || r.NextPollAt.IsZero() {
		t.Errorf("poll = %+v", r)
	}

	fx.hd.SetStatus("w1:p1", herdr.StatusWorking)
	fx.f.set(prKey, "CLOSED", nil)
	if err := call(t, fx.sock, ctl.MethodPoll, ctl.PollParams{URL: prURL}, &r); err != nil {
		t.Fatal(err)
	}
	if r.NewEvents != 1 {
		t.Errorf("new events = %d, want 1 (closed)", r.NewEvents)
	}

	err = call(t, fx.sock, ctl.MethodPoll, ctl.PollParams{URL: "https://gitlab.com/o/r/pull/12"}, nil)
	wantCode(t, err, ctl.CodeBadRequest, "only github.com is supported")
}

func TestLs(t *testing.T) {
	fx := setup(t)
	fx.mustSub(t, pr2URL, "w1:p1")
	fx.mustSub(t, prURL, "w1:p2")
	fx.mustSub(t, prURL, "w1:p1")

	var r ctl.LsResult
	if err := call(t, fx.sock, ctl.MethodLs, ctl.LsParams{Target: fx.target("w1:p1")}, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 2 || r.Rows[0].PR != prKey || r.Rows[1].PR != "o/r#13" {
		t.Fatalf("pane rows = %+v", r.Rows)
	}
	for _, row := range r.Rows {
		if row.PaneID != "w1:p1" || row.URL == "" || row.HerdrSocket != fx.hsock || row.Interval <= 0 ||
			row.CheckinDue.IsZero() || row.ExpiresAt.IsZero() || row.Ending != "" {
			t.Errorf("row = %+v", row)
		}
	}
	if r.Now.IsZero() {
		t.Errorf("now is zero")
	}
	for _, p := range []ctl.LsParams{{All: true, Target: fx.target("w1:p1")}, {}} {
		if err := call(t, fx.sock, ctl.MethodLs, p, &r); err != nil {
			t.Fatal(err)
		}
		if len(r.Rows) != 3 || r.Rows[0].PaneID != "w1:p1" || r.Rows[2].PaneID != "w1:p2" {
			t.Errorf("all rows (%+v) = %+v", p, r.Rows)
		}
	}
	other := filepath.Join(filepath.Dir(fx.hsock), "other.sock")
	if err := call(t, fx.sock, ctl.MethodLs, ctl.LsParams{Target: ctl.Target{HerdrSocket: other, PaneID: "w1:p1"}}, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 0 {
		t.Errorf("rows for another socket = %+v", r.Rows)
	}
}

func TestStatus(t *testing.T) {
	fx := setup(t)
	fx.mustSub(t, prURL, "w1:p1")
	fx.hd.SetStatus("w1:p2", herdr.StatusWorking)
	fx.mustSub(t, pr2URL, "w1:p2")
	fx.f.set("o/r#13", "CLOSED", nil)
	if err := call(t, fx.sock, ctl.MethodPoll, ctl.PollParams{URL: pr2URL}, nil); err != nil {
		t.Fatal(err)
	}
	id := store.SubID(fx.hsock, "w1:p1", prKey)
	if err := fx.d.host.eng.Cancel(id, poller.ReasonPaneClosed); err != nil {
		t.Fatal(err)
	}

	var st ctl.StatusResult
	waitFor(t, "herdr socket row", func() bool {
		st = ctl.StatusResult{}
		if err := call(t, fx.sock, ctl.MethodStatus, nil, &st); err != nil {
			t.Fatal(err)
		}
		return len(st.Herdr) == 1 && st.Herdr[0].Up
	})
	if st.Version != testVersion || st.PID != os.Getpid() || st.Socket != fx.sock || st.StartedAt.IsZero() {
		t.Errorf("identity = %+v", st)
	}
	if st.StateDir != os.Getenv("PR_RELAY_STATE_DIR") || !strings.HasSuffix(st.LogPath, "daemon.log") {
		t.Errorf("paths: state %q log %q", st.StateDir, st.LogPath)
	}
	if st.AuthSource == "" || st.AuthState == "" {
		t.Errorf("auth = %q/%q", st.AuthState, st.AuthSource)
	}
	if st.Subs != 1 && st.Subs != 0 {
		t.Errorf("subs = %d", st.Subs)
	}
	if st.Herdr[0].Socket != fx.hsock || st.Herdr[0].Pending != 1 {
		t.Errorf("herdr row = %+v, want pending 1 on %s", st.Herdr[0], fx.hsock)
	}
	if len(st.Cancellations) != 1 || st.Cancellations[0].Reason != poller.ReasonPaneClosed ||
		st.Cancellations[0].PaneID != "w1:p1" || st.Cancellations[0].PR != prKey {
		t.Errorf("cancellations = %+v", st.Cancellations)
	}
	// D-04: nothing was typed into the cancelled pane.
	for _, p := range fx.hd.Prompts() {
		if p.Target == "w1:p1" {
			t.Errorf("cancelled pane got a prompt: %+v", p)
		}
	}
}

func TestReset(t *testing.T) {
	fx := setup(t)
	fx.mustSub(t, prURL, "w1:p1")
	fx.mustSub(t, pr2URL, "w1:p1")
	fx.mustSub(t, prURL, "w1:p2")
	var r ctl.ResetResult
	if err := call(t, fx.sock, ctl.MethodReset, nil, &r); err != nil {
		t.Fatal(err)
	}
	if r.Removed != 3 || len(fx.subs()) != 0 {
		t.Errorf("reset removed %d, left %d", r.Removed, len(fx.subs()))
	}
	if err := call(t, fx.sock, ctl.MethodReset, nil, &r); err != nil || r.Removed != 0 {
		t.Errorf("second reset = %d, %v", r.Removed, err)
	}
}
