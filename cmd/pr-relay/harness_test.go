package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/config"
	"github.com/drodimin/pr-relay/internal/herdr"
	"github.com/drodimin/pr-relay/internal/herdr/herdrtest"
	"github.com/drodimin/pr-relay/internal/model"
)

// e2eCfg polls fast and delivers after a short quiet period. Lifecycle
// thresholds stay at their defaults unless a test appends a [lifecycle]
// table.
const e2eCfg = "[poll]\nmin_interval = \"100ms\"\nmax_interval = \"200ms\"\nmax_stretched_interval = \"400ms\"\n\n[delivery]\nquiet_period = \"200ms\"\n"

// e2e is one isolated machine for the real CLI and daemon: its own state,
// socket, log and config, a fake GitHub and a fake herdr on a scratch
// socket. Nothing reaches the user's real herdr, GitHub or state.
type e2e struct {
	t         *testing.T
	gh        *fakeGH
	herdr     *herdrtest.Server
	herdrSock string
	stateDir  string
	sockDir   string
	logDir    string
	cfgPath   string
	// autoIdle, when set, flips a working fake pane back to idle shortly
	// after each prompt, as a real agent finishing its turn would.
	autoIdle atomic.Bool
}

// newE2E builds the environment. extraCfg is appended to e2eCfg.
func newE2E(t *testing.T, extraCfg string) *e2e {
	t.Helper()
	dir := t.TempDir()
	// The socket dir must be short: a long t.TempDir() path pushes the
	// socket past the 104-byte sun_path limit on macOS (Pitfall 3).
	sockDir, err := os.MkdirTemp("", "prr")
	if err != nil {
		t.Fatal(err)
	}
	e := &e2e{
		t:        t,
		gh:       newFakeGH(t),
		stateDir: filepath.Join(dir, "state"),
		sockDir:  sockDir,
		logDir:   filepath.Join(dir, "log"),
		cfgPath:  filepath.Join(dir, "config.toml"),
	}
	if err := os.WriteFile(e.cfgPath, []byte(e2eCfg+extraCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	e.herdr, e.herdrSock = scratchHerdr(t)
	e.autoIdle.Store(true)

	stop := make(chan struct{})
	idleDone := make(chan struct{})
	go func() {
		defer close(idleDone)
		tk := time.NewTicker(25 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
			}
			if !e.autoIdle.Load() {
				continue
			}
			for _, id := range []string{"w1:p1", "w1:p2"} {
				if p, ok := e.herdr.Pane(id); ok && p.Status == herdr.StatusWorking {
					e.herdr.SetStatus(id, herdr.StatusIdle)
				}
			}
		}
	}()

	// Registered last, so it runs first: the daemon is stopped while the
	// fake herdr and fake GitHub are still up (T-04-49).
	t.Cleanup(func() {
		close(stop)
		<-idleDone
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, binPath, "daemon", "stop")
		cmd.Env = e.env("")
		_ = cmd.Run()
		cancel()
		// A cleanly stopped daemon removes its pid file. One that is left
		// belongs to a daemon that did not stop (an old version refusing
		// the request, a wedged process): kill it.
		if pid := e.daemonPID(); pid > 0 && syscall.Kill(pid, 0) == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		_ = os.RemoveAll(sockDir)
	})
	return e
}

// env is the subprocess environment. Every inherited HERDR_*, PR_RELAY_*,
// GH_*, GITHUB_* and XDG_RUNTIME_DIR entry is dropped: this machine may run
// inside a real herdr pane, and a test must never type into it (Pitfall 2,
// T-04-48), nor use a real token (T-04-50). pane sets HERDR_PANE_ID when
// non-empty.
func (e *e2e) env(pane string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HERDR_") || strings.HasPrefix(kv, "PR_RELAY_") ||
			strings.HasPrefix(kv, "GH_") || strings.HasPrefix(kv, "GITHUB_") ||
			strings.HasPrefix(kv, "XDG_RUNTIME_DIR=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"PR_RELAY_STATE_DIR="+e.stateDir,
		"PR_RELAY_SOCKET="+e.socket(),
		"PR_RELAY_LOG_DIR="+e.logDir,
		"PR_RELAY_CONFIG="+e.cfgPath,
		envGraphQLURL+"="+e.gh.url(),
		"GH_TOKEN=test",
		config.EnvFastPoll+"=1",
		"HERDR_SOCKET_PATH="+e.herdrSock,
	)
	if pane != "" {
		env = append(env, "HERDR_PANE_ID="+pane)
	}
	return env
}

func (e *e2e) socket() string { return filepath.Join(e.sockDir, "pr-relay.sock") }

// result is one finished CLI run.
type result struct {
	stdout, stderr string
	code           int
	took           time.Duration
}

func (r result) String() string {
	return "exit " + strconv.Itoa(r.code) + "\nstdout:\n" + r.stdout + "\nstderr:\n" + r.stderr
}

// run runs the current binary as pane (no pane when empty).
func (e *e2e) run(pane string, args ...string) result {
	e.t.Helper()
	return e.runBin(binPath, pane, args...)
}

// runBin runs bin with cmd.Output, so stdout is a pipe: a daemon that
// inherited it would hold the pipe open and make the run hang until the
// WaitDelay (Pitfall 1).
func (e *e2e) runBin(bin, pane string, args ...string) result {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = e.env(pane)
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	out, err := cmd.Output()
	r := result{stdout: string(out), stderr: stderr.String(), took: time.Since(start)}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.code = ee.ExitCode()
	default:
		e.t.Fatalf("run %v: %v (after %s)\n%s", args, err, r.took, r)
	}
	if ctx.Err() != nil {
		e.t.Fatalf("run %v timed out after %s\n%s", args, r.took, r)
	}
	return r
}

// mustRun runs the current binary and fails the test on a non-zero exit.
func (e *e2e) mustRun(pane string, args ...string) result {
	e.t.Helper()
	r := e.run(pane, args...)
	if r.code != 0 {
		e.t.Fatalf("%v: %s", args, r)
	}
	return r
}

// daemonPID returns the pid recorded in the socket dir, or 0.
func (e *e2e) daemonPID() int {
	raw, err := os.ReadFile(filepath.Join(e.sockDir, "daemon.pid"))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	return pid
}

// eventually polls cond until it holds or limit passes.
func (e *e2e) eventually(limit time.Duration, what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out after %s waiting for %s\nprompts: %q\ndaemon log:\n%s", limit, what, e.promptTexts(""), e.daemonLog())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// prompts returns the texts of prompts typed into pane that contain sub.
func (e *e2e) prompts(pane, sub string) []string {
	var out []string
	for _, p := range e.herdr.Prompts() {
		if p.Target == pane && strings.Contains(p.Text, sub) {
			out = append(out, p.Text)
		}
	}
	return out
}

// promptTexts returns every prompt text (all panes when pane is empty).
func (e *e2e) promptTexts(pane string) []string {
	var out []string
	for _, p := range e.herdr.Prompts() {
		if pane == "" || p.Target == pane {
			out = append(out, p.Target+": "+p.Text)
		}
	}
	return out
}

// waitPrompt waits until pane received a prompt containing sub.
func (e *e2e) waitPrompt(pane, sub string, limit time.Duration) {
	e.t.Helper()
	e.eventually(limit, "prompt "+strconv.Quote(sub)+" in "+pane, func() bool { return len(e.prompts(pane, sub)) > 0 })
}

// e2eState is the subset of state.json the e2e tests read.
type e2eState struct {
	Version int `json:"version"`
	PRs     map[string]struct {
		Memory struct {
			Baselined bool `json:"baselined"`
		} `json:"memory"`
	} `json:"prs"`
	Subs map[string]struct {
		PaneID  string `json:"pane_id"`
		Pending []struct {
			Seq   uint64 `json:"seq"`
			Event struct {
				Type string `json:"type"`
			} `json:"event"`
		} `json:"pending"`
	} `json:"subs"`
}

func (e *e2e) readState() (e2eState, bool) {
	var st e2eState
	raw, err := os.ReadFile(filepath.Join(e.stateDir, "state.json"))
	if err != nil {
		return st, false
	}
	return st, json.Unmarshal(raw, &st) == nil
}

// waitBaselined waits until the daemon has baselined ref, so later fake
// GitHub changes are new activity.
func (e *e2e) waitBaselined(ref model.PRRef) {
	e.t.Helper()
	key := strings.ToLower(ref.String())
	e.eventually(10*time.Second, "baseline of "+key, func() bool {
		st, ok := e.readState()
		return ok && st.PRs[key].Memory.Baselined
	})
}

func (e *e2e) daemonLog() string {
	raw, err := os.ReadFile(filepath.Join(e.logDir, "daemon.log"))
	if err != nil {
		return "(no daemon log: " + err.Error() + ")"
	}
	s := string(raw)
	if len(s) > 8000 {
		s = "...\n" + s[len(s)-8000:]
	}
	return s
}

// addPane adds a second idle fake agent pane.
func (e *e2e) addPane(id, agent, session string) {
	e.herdr.SetPane(herdrtest.PaneState{ID: id, TabID: "t1", WorkspaceID: "w1", Agent: agent, Session: session, Status: herdr.StatusIdle})
}
