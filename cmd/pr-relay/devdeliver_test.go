package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

// TestDevDeliver runs the real binary against the fake GitHub and a fake
// herdr on a real unix socket under a /sessions/ path, and checks that a new
// comment is typed into the pane and that the pane identity is captured.
func TestDevDeliver(t *testing.T) {
	h := newHarness(t)
	cfg := "[poll]\nmin_interval = \"100ms\"\nmax_interval = \"200ms\"\nmax_stretched_interval = \"400ms\"\n\n[delivery]\nquiet_period = \"200ms\"\n"
	if err := os.WriteFile(h.cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

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

	ref := model.PRRef{Owner: "deliver-owner", Repo: "deliver-repo", Number: 9}
	prURL := h.gh.addPR(ref)

	p := h.startArgs("dev", "deliver", "--config", h.cfgPath, "--socket", sock, "--pane", "w1:p1", prURL)
	stopProc := func() {
		_ = p.cmd.Process.Kill()
		_ = p.wait()
	}

	// The comment must land after the baseline poll, or it is absorbed.
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, ok, err := h.readState()
		if err == nil && ok && st.PRs[strings.ToLower(ref.String())].Memory.Baselined {
			break
		}
		if time.Now().After(deadline) {
			stopProc()
			t.Fatalf("no baseline\n%s", p.output())
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.gh.addComment(ref, "IC_deliver1")

	var got []herdrtest.Prompt
	deadline = time.Now().Add(10 * time.Second)
	for {
		got = srv.Prompts()
		if len(got) > 0 {
			break
		}
		if time.Now().After(deadline) {
			stopProc()
			t.Fatalf("no prompt reached the fake herdr\n%s", p.output())
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Give a duplicate delivery a chance to show up.
	time.Sleep(500 * time.Millisecond)
	got = srv.Prompts()
	if len(got) != 1 {
		t.Errorf("got %d prompts, want 1: %+v", len(got), got)
	}
	if got[0].Target != "w1:p1" || !strings.Contains(got[0].Text, "[pr-relay]") || !strings.Contains(got[0].Text, ref.String()) {
		t.Errorf("prompt = %+v, want [pr-relay] line for %s into w1:p1", got[0], ref)
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

	raw, err := os.ReadFile(filepath.Join(h.stateDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Subs map[string]struct {
			PaneID       string `json:"pane_id"`
			AgentKind    string `json:"agent_kind"`
			AgentSession string `json:"agent_session"`
		} `json:"subs"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range st.Subs {
		if s.PaneID != "w1:p1" {
			continue
		}
		found = true
		if s.AgentKind != "claude" || s.AgentSession != "s1" {
			t.Errorf("sub identity kind=%q session=%q, want claude/s1", s.AgentKind, s.AgentSession)
		}
	}
	if !found {
		t.Errorf("no subscription for w1:p1 in state\n%s", raw)
	}
}
