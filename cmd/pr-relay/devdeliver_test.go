package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/herdr"
	"github.com/drodimin/pr-relay/internal/herdr/herdrtest"
	"github.com/drodimin/pr-relay/internal/model"
)

// startArgs runs the binary with custom args in the harness environment.
func (h *harness) startArgs(args ...string) *proc {
	h.t.Helper()
	p := &proc{cmd: exec.Command(binPath, args...), lines: make(chan string, 1024), done: make(chan struct{})}
	p.cmd.Env = h.env()
	p.cmd.Stderr = &p.stderr
	out, err := p.cmd.StdoutPipe()
	if err != nil {
		h.t.Fatal(err)
	}
	if err := p.cmd.Start(); err != nil {
		h.t.Fatalf("start pr-relay: %v", err)
	}
	go func() {
		defer close(p.done)
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			l := sc.Text()
			_, _ = p.stdout.Write([]byte(l + "\n"))
			select {
			case p.lines <- l:
			default:
			}
		}
		_, _ = io.Copy(io.Discard, out)
	}()
	return p
}

func TestGuardScratchSocket(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	refused := []string{
		"",
		filepath.Join(home, ".config/herdr/herdr.sock"),
		filepath.Join(home, ".config/herdr/./herdr.sock"),
		"/tmp/herdr.sock",
		"/tmp/sessionsx/h.sock",
	}
	for _, p := range refused {
		err := guardScratchSocket(p)
		if err == nil || !strings.Contains(err.Error(), "refusing") || !strings.Contains(err.Error(), "scratch") {
			t.Errorf("guardScratchSocket(%q) = %v, want a refusal", p, err)
		}
	}
	for _, p := range []string{
		filepath.Join(home, ".config/herdr/sessions/prspike/herdr.sock"),
		"/tmp/hd1/sessions/t/h.sock",
	} {
		if err := guardScratchSocket(p); err != nil {
			t.Errorf("guardScratchSocket(%q) = %v, want nil", p, err)
		}
	}
}

// TestDevDeliverRefuses checks the binary refuses non-scratch sockets and
// missing flags without dialing.
func TestDevDeliverRefuses(t *testing.T) {
	h := newHarness(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	url := "https://github.com/o/r/pull/1"
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"default socket", []string{"--socket", filepath.Join(home, ".config/herdr/herdr.sock"), "--pane", "w1:p1", url}, []string{"refusing", "scratch"}},
		{"no sessions", []string{"--socket", "/tmp/x/h.sock", "--pane", "w1:p1", url}, []string{"refusing", "scratch"}},
		{"missing socket", []string{"--pane", "w1:p1", url}, []string{"socket"}},
		{"missing pane", []string{"--socket", "/tmp/hd/sessions/t/h.sock", url}, []string{"pane"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command(binPath, append([]string{"dev", "deliver", "--config", h.cfgPath}, c.args...)...)
			cmd.Env = h.env()
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("exit 0, want failure\n%s", out)
			}
			for _, w := range c.want {
				if !strings.Contains(string(out), w) {
					t.Errorf("output lacks %q\n%s", w, out)
				}
			}
		})
	}
}

const devDeliverCfg = "[poll]\nmin_interval = \"100ms\"\nmax_interval = \"200ms\"\nmax_stretched_interval = \"400ms\"\n\n[delivery]\nquiet_period = \"200ms\"\n"

// scratchHerdr starts a fake herdr on a real unix socket under a /sessions/
// path with one idle claude pane w1:p1.
func scratchHerdr(t *testing.T) (*herdrtest.Server, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "hd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockDir := filepath.Join(dir, "sessions", "t")
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	srv := herdrtest.New(t)
	sock := srv.ListenUnixAt(filepath.Join(sockDir, "h.sock"))
	srv.SetPane(herdrtest.PaneState{ID: "w1:p1", TabID: "t1", WorkspaceID: "w1", Agent: "claude", Session: "s1", Status: herdr.StatusIdle})
	return srv, sock
}

type devSubState struct {
	PaneID       string `json:"pane_id"`
	HerdrSocket  string `json:"herdr_socket"`
	AgentKind    string `json:"agent_kind"`
	AgentSession string `json:"agent_session"`
}

// readSubs parses the subscriptions of dir/state.json.
func readSubs(t *testing.T, dir string) (map[string]devSubState, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Subs map[string]devSubState `json:"subs"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	return st.Subs, raw
}

// waitBaselined polls dir/state.json until ref is baselined.
func waitBaselined(t *testing.T, p *proc, dir string, ref model.PRRef) {
	t.Helper()
	key := strings.ToLower(ref.String())
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
		if err == nil {
			var st stateFile
			if json.Unmarshal(raw, &st) == nil && st.PRs[key].Memory.Baselined {
				return
			}
		}
		if time.Now().After(deadline) {
			_ = p.cmd.Process.Kill()
			_ = p.wait()
			t.Fatalf("no baseline in %s\n%s", dir, p.output())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitPrompts waits for the first prompt, then gives a duplicate a chance to
// arrive, and returns every prompt received.
func waitPrompts(t *testing.T, p *proc, srv *herdrtest.Server) []herdrtest.Prompt {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(srv.Prompts()) == 0 {
		if time.Now().After(deadline) {
			_ = p.cmd.Process.Kill()
			_ = p.wait()
			t.Fatalf("no prompt reached the fake herdr\n%s", p.output())
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	return srv.Prompts()
}

// seedForeignSub runs dev watch until it has subscribed, leaving h.stateDir
// with a subscription whose socket is "dev" (not a scratch socket).
func seedForeignSub(t *testing.T, h *harness) {
	t.Helper()
	ref := model.PRRef{Owner: "foreign-owner", Repo: "foreign-repo", Number: 3}
	p := h.start(false, h.gh.addPR(ref))
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, ok, err := h.readState()
		if err == nil && ok && len(st.Subs) > 0 && st.PRs[strings.ToLower(ref.String())].Memory.Baselined {
			break
		}
		if time.Now().After(deadline) {
			_ = p.cmd.Process.Kill()
			_ = p.wait()
			t.Fatalf("dev watch did not subscribe\n%s", p.output())
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	_ = p.wait()
	subs, _ := readSubs(t, h.stateDir)
	foreign := false
	for _, s := range subs {
		if s.HerdrSocket == devSocket {
			foreign = true
		}
	}
	if !foreign {
		t.Fatalf("seeded state has no %q subscription: %+v", devSocket, subs)
	}
}

// TestDevDeliver runs the real binary against the fake GitHub and a fake
// herdr on a real unix socket under a /sessions/ path. It checks that a new
// comment is typed into the pane, that the pane identity is captured while
// running, and that the run's own subscription is removed on exit (CR-02).
func TestDevDeliver(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(h.cfgPath, []byte(devDeliverCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, sock := scratchHerdr(t)

	ref := model.PRRef{Owner: "deliver-owner", Repo: "deliver-repo", Number: 9}
	prURL := h.gh.addPR(ref)

	p := h.startArgs("dev", "deliver", "--config", h.cfgPath, "--state-dir", h.stateDir, "--socket", sock, "--pane", "w1:p1", prURL)

	// The comment must land after the baseline poll, or it is absorbed.
	waitBaselined(t, p, h.stateDir, ref)
	h.gh.addComment(ref, "IC_deliver1")

	got := waitPrompts(t, p, srv)
	if len(got) != 1 {
		t.Errorf("got %d prompts, want 1: %+v", len(got), got)
	}
	if got[0].Target != "w1:p1" || !strings.Contains(got[0].Text, "[pr-relay]") || !strings.Contains(got[0].Text, ref.String()) {
		t.Errorf("prompt = %+v, want [pr-relay] line for %s into w1:p1", got[0], ref)
	}

	// Identity is captured while the run is live.
	subs, raw := readSubs(t, h.stateDir)
	found := false
	for _, s := range subs {
		if s.PaneID != "w1:p1" {
			continue
		}
		found = true
		if s.AgentKind != "claude" || s.AgentSession != "s1" {
			t.Errorf("sub identity kind=%q session=%q, want claude/s1", s.AgentKind, s.AgentSession)
		}
	}
	if !found {
		t.Errorf("no subscription for w1:p1 in state while running\n%s", raw)
	}

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := p.wait(); err != nil {
		t.Fatalf("exit after SIGTERM: %v\n%s", err, p.output())
	}
	if !strings.Contains(p.stdout.String(), "subscribed "+ref.String()+" pane=w1:p1 agent=claude") {
		t.Errorf("no subscribe line\n%s", p.output())
	}

	// No subscription outlives the run.
	subs, raw = readSubs(t, h.stateDir)
	for _, s := range subs {
		if s.PaneID == "w1:p1" {
			t.Errorf("subscription for w1:p1 left behind after exit\n%s", raw)
		}
	}
}

// TestDevDeliverRefusesForeignSubs checks that a state dir holding any
// subscription with a non-scratch socket stops dev deliver before it dials
// herdr (CR-02).
func TestDevDeliverRefusesForeignSubs(t *testing.T) {
	h := newHarness(t)
	seedForeignSub(t, h)
	srv, sock := scratchHerdr(t)

	ref := model.PRRef{Owner: "deliver-owner", Repo: "deliver-repo", Number: 10}
	prURL := h.gh.addPR(ref)
	cmd := exec.Command(binPath, "dev", "deliver", "--config", h.cfgPath, "--state-dir", h.stateDir, "--socket", sock, "--pane", "w1:p1", prURL)
	cmd.Env = h.env()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("exit 0, want failure\n%s", out)
	}
	if !strings.Contains(string(out), "non-scratch subscription") {
		t.Errorf("output lacks the refusal\n%s", out)
	}
	if c := srv.Calls(); len(c) != 0 {
		t.Errorf("fake herdr got calls %v, want none", c)
	}
}

// TestDevDeliverTempStateDefault checks that without --state-dir dev deliver
// ignores PR_RELAY_STATE_DIR, uses a temporary state dir it removes on exit,
// and leaves the configured state untouched (CR-02).
func TestDevDeliverTempStateDefault(t *testing.T) {
	h := newHarness(t)
	seedForeignSub(t, h)
	if err := os.WriteFile(h.cfgPath, []byte(devDeliverCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(h.stateDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv, sock := scratchHerdr(t)

	ref := model.PRRef{Owner: "deliver-owner", Repo: "deliver-repo", Number: 11}
	prURL := h.gh.addPR(ref)
	p := h.startArgs("dev", "deliver", "--config", h.cfgPath, "--socket", sock, "--pane", "w1:p1", prURL)

	re := regexp.MustCompile(`state dir (\S+) \(temporary\)`)
	var tmp string
	deadline := time.Now().Add(10 * time.Second)
	for {
		if m := re.FindStringSubmatch(p.stderr.String() + p.stdout.String()); m != nil {
			tmp = m[1]
			break
		}
		if time.Now().After(deadline) {
			_ = p.cmd.Process.Kill()
			_ = p.wait()
			t.Fatalf("no temporary state dir line\n%s", p.output())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if tmp == h.stateDir {
		t.Fatalf("temporary state dir is the configured state dir %s", tmp)
	}

	waitBaselined(t, p, tmp, ref)
	h.gh.addComment(ref, "IC_deliver_tmp")
	got := waitPrompts(t, p, srv)
	if len(got) != 1 || !strings.Contains(got[0].Text, ref.String()) {
		t.Errorf("got %d prompts, want 1 for %s: %+v", len(got), ref, got)
	}

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := p.wait(); err != nil {
		t.Fatalf("exit after SIGTERM: %v\n%s", err, p.output())
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("temporary state dir %s still exists after exit (stat err %v)", tmp, err)
	}
	after, err := os.ReadFile(filepath.Join(h.stateDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("configured state.json changed during the run\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
