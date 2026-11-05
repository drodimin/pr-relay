package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/store"
)

var (
	e2eRef = model.PRRef{Owner: "o", Repo: "r", Number: 12}
	e2eURL = "https://github.com/o/r/pull/12"
	pidRE  = regexp.MustCompile(`pid (\d+)`)
)

// TestSubEndToEnd is ROADMAP SC-1 / SUB-01: a cold sub through the real
// binary, with stdout captured through a pipe, returns quickly, and a later
// comment is typed into the subscribing pane.
func TestSubEndToEnd(t *testing.T) {
	e := newE2E(t, "")
	e.gh.addPR(e2eRef)

	r := e.mustRun("w1:p1", "sub", e2eURL)
	if r.took >= 10*time.Second {
		t.Fatalf("cold sub took %s, want < 10s", r.took)
	}
	if !strings.HasPrefix(r.stdout, "subscribed o/r#12 events=all") {
		t.Fatalf("sub stdout:\n%s", r)
	}
	e.waitBaselined(e2eRef)
	e.gh.addComment(e2eRef, "c1")
	e.waitPrompt("w1:p1", "[pr-relay] o/r#12 comment", 15*time.Second)
	if p := e.promptTexts("w1:p2"); len(p) != 0 {
		t.Errorf("prompts reached another pane: %q", p)
	}
}

// TestSubErrors checks every sub failure is clear and exits 1.
func TestSubErrors(t *testing.T) {
	e := newE2E(t, "")
	e.gh.addPR(e2eRef)

	t.Run("outside herdr", func(t *testing.T) {
		r := e.run("", "sub", e2eURL)
		if r.code != 1 || !strings.Contains(r.stderr, "not inside a herdr pane") {
			t.Fatalf("want exit 1 and the pane error:\n%s", r)
		}
		if _, err := os.Stat(e.socket()); !os.IsNotExist(err) {
			t.Fatalf("a daemon socket appeared (stat err %v); sub must fail before dialing", err)
		}
	})

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"not found", []string{"sub", "https://github.com/o/r/pull/999"}, "not found"},
		{"issue link", []string{"sub", "https://github.com/o/r/issues/5"}, "is an issue, not a pull request"},
		{"repo link", []string{"sub", "https://github.com/o/r"}, "is a repository"},
		{"gitlab link", []string{"sub", "https://gitlab.com/o/r/-/merge_requests/1"}, "only github.com is supported"},
		{"bogus events", []string{"sub", "--events", "bogus", e2eURL}, "unknown event type"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := e.run("w1:p1", c.args...)
			if r.code != 1 || !strings.Contains(r.stderr, c.want) {
				t.Fatalf("want exit 1 with %q:\n%s", c.want, r)
			}
		})
	}
	if r := e.mustRun("w1:p1", "ls", "--all"); !strings.Contains(r.stdout, "no subscriptions") {
		t.Errorf("failed subs left subscriptions:\n%s", r)
	}
}

// TestSubEventsMask checks --events ci delivers a failing check and not a
// comment (SUB-02).
func TestSubEventsMask(t *testing.T) {
	e := newE2E(t, "")
	e.gh.addPR(e2eRef)
	r := e.mustRun("w1:p1", "sub", "--events", "ci", e2eURL)
	if !strings.Contains(r.stdout, "events=ci.failed,ci.settled") {
		t.Fatalf("sub stdout:\n%s", r)
	}
	e.waitBaselined(e2eRef)
	e.gh.addComment(e2eRef, "masked")
	e.gh.addFailedCheck(e2eRef, "e2e", 99)
	e.waitPrompt("w1:p1", "ci.failed", 15*time.Second)
	time.Sleep(500 * time.Millisecond)
	if p := e.prompts("w1:p1", " comment"); len(p) != 0 {
		t.Errorf("comment delivered despite --events ci: %q", p)
	}
}

// TestConcurrentAutostart is ROADMAP SC-2 / DMN-01: 20 CLIs on a cold
// machine share one daemon.
func TestConcurrentAutostart(t *testing.T) {
	e := newE2E(t, "")
	const n = 20
	results := make([]result, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = e.run("", "status")
		}()
	}
	wg.Wait()
	pids := map[string]int{}
	for i, r := range results {
		if r.code != 0 {
			t.Fatalf("status %d: %s", i, r)
		}
		m := pidRE.FindStringSubmatch(r.stdout)
		if m == nil {
			t.Fatalf("status %d printed no pid:\n%s", i, r)
		}
		pids[m[1]]++
	}
	if len(pids) != 1 {
		t.Fatalf("status runs saw %d daemons, want 1: %v", len(pids), pids)
	}
}

// TestVersionMismatch is ROADMAP SC-2 / DMN-07: an older daemon is
// replaced by the current CLI with one stderr note, and subscriptions
// survive.
func TestVersionMismatch(t *testing.T) {
	e := newE2E(t, "")
	e.gh.addPR(e2eRef)
	if r := e.runBin(binOld, "w1:p1", "sub", e2eURL); r.code != 0 {
		t.Fatalf("old sub: %s", r)
	}
	if r := e.runBin(binOld, "", "status"); !strings.Contains(r.stdout, "version 0.0.1-old") {
		t.Fatalf("old daemon not running:\n%s", r)
	}

	r := e.mustRun("", "status")
	if got := strings.Count(r.stderr, "restarted daemon (0.0.1-old -> "); got != 1 {
		t.Fatalf("want exactly one restart note, got %d:\n%s", got, r)
	}
	if !strings.Contains(r.stdout, "version "+version) {
		t.Errorf("status after restart is not the current version:\n%s", r)
	}
	if ls := e.mustRun("", "ls", "--all"); !strings.Contains(ls.stdout, "o/r#12") {
		t.Errorf("subscription lost across the version restart:\n%s", ls)
	}
	if r2 := e.mustRun("", "status"); strings.Contains(r2.stderr, "restarted daemon") {
		t.Errorf("second status restarted again:\n%s", r2)
	}
}

// TestDowngradeError is Pitfall 8: a binary facing a state file from a
// newer schema refuses with an upgrade hint instead of clobbering it. The
// old build shares this source, so the file's version is raised past
// store.SchemaVersion to stand in for a newer release.
func TestDowngradeError(t *testing.T) {
	e := newE2E(t, "")
	e.gh.addPR(e2eRef)
	e.mustRun("w1:p1", "sub", e2eURL)
	e.mustRun("", "daemon", "stop")

	path := filepath.Join(e.stateDir, "state.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if v, _ := doc["version"].(float64); int(v) != store.SchemaVersion {
		t.Fatalf("state version %v, want %d", doc["version"], store.SchemaVersion)
	}
	doc["version"] = store.SchemaVersion + 1
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	r := e.runBin(binOld, "", "status")
	if r.code == 0 || !strings.Contains(r.stderr, "newer pr-relay") {
		t.Fatalf("want a newer-state error:\n%s", r)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Error("the refused state file was rewritten")
	}
}

// TestLsUnsubStatus is ROADMAP SC-3: ls, unsub, unsub --all and status
// against a live daemon, and the daemon log on disk.
func TestLsUnsubStatus(t *testing.T) {
	e := newE2E(t, "")
	ref13 := model.PRRef{Owner: "o", Repo: "r", Number: 13}
	url13 := ref13.URL()
	e.gh.addPR(e2eRef)
	e.gh.addPR(ref13)
	e.addPane("w1:p2", "codex", "s2")

	e.mustRun("w1:p1", "sub", e2eURL)
	e.mustRun("w1:p2", "sub", e2eURL)
	e.mustRun("w1:p2", "sub", url13)

	ls := e.mustRun("w1:p1", "ls")
	rows := strings.Split(strings.TrimRight(ls.stdout, "\n"), "\n")
	if len(rows) != 1 {
		t.Fatalf("ls from w1:p1 printed %d rows, want 1:\n%s", len(rows), ls)
	}
	for _, w := range []string{"o/r#12", "events=", "poll=", "last=", "expires in"} {
		if !strings.Contains(rows[0], w) {
			t.Errorf("ls row lacks %q: %q", w, rows[0])
		}
	}
	all := e.mustRun("w1:p1", "ls", "--all")
	for _, w := range []string{"PANE", "w1:p1", "w1:p2", "o/r#13"} {
		if !strings.Contains(all.stdout, w) {
			t.Errorf("ls --all lacks %q:\n%s", w, all)
		}
	}

	e.mustRun("w1:p1", "sub", url13)
	if r := e.mustRun("w1:p1", "unsub", e2eURL); !strings.Contains(r.stdout, "unsubscribed") {
		t.Errorf("unsub:\n%s", r)
	}
	if r := e.mustRun("w1:p1", "unsub", e2eURL); !strings.Contains(r.stdout, "not subscribed") {
		t.Errorf("repeated unsub:\n%s", r)
	}
	if r := e.mustRun("w1:p2", "unsub", "--all"); !strings.Contains(r.stdout, "unsubscribed 2 subscriptions") {
		t.Errorf("unsub --all:\n%s", r)
	}
	all = e.mustRun("", "ls", "--all")
	if strings.Contains(all.stdout, "w1:p2") || !strings.Contains(all.stdout, "w1:p1") || !strings.Contains(all.stdout, "o/r#13") {
		t.Errorf("unsub --all from w1:p2 should leave only w1:p1 o/r#13:\n%s", all)
	}

	st := e.mustRun("", "status")
	for _, w := range []string{"daemon: running", "subscriptions: 1 on 1 PR", "rate limit:"} {
		if !strings.Contains(st.stdout, w) {
			t.Errorf("status lacks %q:\n%s", w, st)
		}
	}
	fi, err := os.Stat(filepath.Join(e.logDir, "daemon.log"))
	if err != nil {
		t.Fatalf("daemon log: %v", err)
	}
	if m := fi.Mode().Perm(); m != 0o600 {
		t.Errorf("daemon.log mode %o, want 600", m)
	}
}

// TestClosedRemovalE2E is ROADMAP SC-5 / LIFE-01: a merge is delivered even
// to a comment-only subscription, and then the subscription is gone.
func TestClosedRemovalE2E(t *testing.T) {
	e := newE2E(t, "")
	e.gh.addPR(e2eRef)
	r := e.mustRun("w1:p1", "sub", "--events", "comment", e2eURL)
	if !strings.Contains(r.stdout, "closed always included") {
		t.Errorf("sub stdout:\n%s", r)
	}
	e.waitBaselined(e2eRef)
	e.gh.setMerged(e2eRef)
	e.waitPrompt("w1:p1", "[pr-relay] o/r#12 closed: merged", 15*time.Second)
	e.eventually(10*time.Second, "subscription removal after the closed line", func() bool {
		return strings.Contains(e.mustRun("", "ls", "--all").stdout, "no subscriptions")
	})
}

// TestDaemonRestartCmd is Pitfall 4: repeated restarts never leave a
// half-dead daemon or a stale socket behind.
func TestDaemonRestartCmd(t *testing.T) {
	e := newE2E(t, "")
	e.mustRun("", "status")
	for i := range 10 {
		r := e.run("", "daemon", "restart")
		if r.code != 0 || !strings.Contains(r.stdout, "daemon restarted") {
			t.Fatalf("restart %d: %s", i, r)
		}
		if st := e.run("", "status"); st.code != 0 || !strings.Contains(st.stdout, "daemon: running") {
			t.Fatalf("status after restart %d: %s", i, st)
		}
	}
}

// ensureRE is the single line `daemon --ensure` prints for the dev build.
var ensureRE = regexp.MustCompile(`^daemon running \(pid \d+, dev\)\n$`)

// TestDaemonEnsure is D-08: the herdr [[startup]] hook command spawns a
// daemon when none runs, is a no-op when this version already runs, and
// replaces a daemon of another version. It always returns.
func TestDaemonEnsure(t *testing.T) {
	t.Run("spawns when down", func(t *testing.T) {
		e := newE2E(t, "")
		r := e.run("", "daemon", "--ensure")
		if r.code != 0 || !ensureRE.MatchString(r.stdout) {
			t.Fatalf("ensure: %s", r)
		}
		if r.took > 10*time.Second {
			t.Errorf("ensure took %s", r.took)
		}
		if st := e.mustRun("", "status"); !strings.Contains(st.stdout, "daemon: running") {
			t.Fatalf("status after ensure: %s", st)
		}
	})

	t.Run("no-op when running", func(t *testing.T) {
		e := newE2E(t, "")
		e.mustRun("", "daemon", "--ensure")
		before := e.daemonPID()
		if before == 0 {
			t.Fatal("no daemon pid after first ensure")
		}
		r := e.run("", "daemon", "--ensure")
		if r.code != 0 || !ensureRE.MatchString(r.stdout) {
			t.Fatalf("second ensure: %s", r)
		}
		if after := e.daemonPID(); after != before {
			t.Errorf("daemon pid changed %d -> %d on a matching-version ensure", before, after)
		}
		if m := pidRE.FindStringSubmatch(r.stdout); m == nil || m[1] != strconv.Itoa(before) {
			t.Errorf("ensure reported %q, want pid %d", r.stdout, before)
		}
	})

	t.Run("restarts mismatched version", func(t *testing.T) {
		e := newE2E(t, "")
		if r := e.runBin(binOld, "", "status"); !strings.Contains(r.stdout, "version 0.0.1-old") {
			t.Fatalf("old daemon not running:\n%s", r)
		}
		old := e.daemonPID()
		if old == 0 {
			t.Fatal("no pid for the old daemon")
		}
		r := e.run("", "daemon", "--ensure")
		if r.code != 0 || !ensureRE.MatchString(r.stdout) {
			t.Fatalf("ensure over old daemon: %s", r)
		}
		if now := e.daemonPID(); now == old || now == 0 {
			t.Errorf("daemon pid %d after ensure, old was %d", now, old)
		}
		if st := e.mustRun("", "status"); !strings.Contains(st.stdout, "version dev") {
			t.Errorf("status after ensure is not the dev version:\n%s", st)
		}
	})
}
