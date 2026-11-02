package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/model"
)

// binPath is the pr-relay binary built once by TestMain.
var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pr-relay-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mkdtemp: %v\n", err)
		os.Exit(1)
	}
	binPath = filepath.Join(dir, "pr-relay")
	out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build pr-relay: %v\n%s", err, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// harness is one isolated environment for running the binary: its own state
// dir, config, sink and fake GitHub. Nothing touches the user's real state,
// real GitHub or real herdr.
type harness struct {
	t        *testing.T
	gh       *fakeGH
	stateDir string
	cfgPath  string
	sinkPath string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{
		t:        t,
		gh:       newFakeGH(t),
		stateDir: filepath.Join(dir, "state"),
		cfgPath:  filepath.Join(dir, "config.toml"),
		sinkPath: filepath.Join(dir, "sink.jsonl"),
	}
	cfg := "[poll]\nmin_interval = \"100ms\"\nmax_interval = \"200ms\"\nmax_stretched_interval = \"400ms\"\n"
	if err := os.WriteFile(h.cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) env() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GH_") || strings.HasPrefix(kv, "GITHUB_") || strings.HasPrefix(kv, "PR_RELAY_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"PR_RELAY_STATE_DIR="+h.stateDir,
		envGraphQLURL+"="+h.gh.url(),
		"GH_TOKEN=test",
	)
}

// syncBuf is a goroutine-safe output buffer.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// proc is a running pr-relay. Stdout lines are also sent on lines.
type proc struct {
	cmd    *exec.Cmd
	stdout syncBuf
	stderr syncBuf
	lines  chan string
	done   chan struct{}
}

func (h *harness) start(withSink bool, prURL string) *proc {
	h.t.Helper()
	args := []string{"dev", "watch", "--config", h.cfgPath}
	if withSink {
		args = append(args, "--sink", h.sinkPath)
	}
	args = append(args, prURL)
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

// wait reaps the process after its stdout is drained.
func (p *proc) wait() error {
	<-p.done
	return p.cmd.Wait()
}

func (p *proc) output() string {
	return "stdout:\n" + p.stdout.String() + "\nstderr:\n" + p.stderr.String()
}

// killed reports whether err is the exit of a SIGKILLed process.
func killed(err error) bool {
	var ee *exec.ExitError
	if e, ok := err.(*exec.ExitError); ok {
		ee = e
	}
	if ee == nil {
		return false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL
}

type stateFile struct {
	PRs map[string]struct {
		Memory struct {
			Baselined bool `json:"baselined"`
		} `json:"memory"`
	} `json:"prs"`
	Subs map[string]struct {
		PaneID  string            `json:"pane_id"`
		Pending []json.RawMessage `json:"pending"`
	} `json:"subs"`
}

// readState parses state.json. ok is false when it does not exist yet.
func (h *harness) readState() (st stateFile, ok bool, err error) {
	raw, err := os.ReadFile(filepath.Join(h.stateDir, "state.json"))
	if os.IsNotExist(err) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return st, true, fmt.Errorf("state.json is not valid JSON: %w", err)
	}
	return st, true, nil
}

func (h *harness) readSink() []sinkLine {
	h.t.Helper()
	raw, err := os.ReadFile(h.sinkPath)
	if err != nil {
		h.t.Fatalf("read sink: %v", err)
	}
	var out []sinkLine
	for i, l := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if l == "" {
			continue
		}
		var sl sinkLine
		if err := json.Unmarshal([]byte(l), &sl); err != nil {
			h.t.Fatalf("sink line %d %q: %v", i+1, l, err)
		}
		out = append(out, sl)
	}
	return out
}

// waitForEventLine reports whether p printed a rendered event line within
// limit.
func waitForEventLine(p *proc, limit time.Duration) bool {
	timeout := time.After(limit)
	for {
		select {
		case l := <-p.lines:
			if strings.HasPrefix(l, "[pr-relay] ") {
				return true
			}
		case <-p.done:
			return false
		case <-timeout:
			return false
		}
	}
}

var prLineRE = regexp.MustCompile(`^  \S+ new=(\d+) interval=`)

// TestCrashLoop is the end-to-end proof of ROADMAP SC-1 (D-22, EVT-09,
// LIFE-03): the real binary is SIGKILLed at random points while comments
// keep arriving, restarted each time, and every comment still reaches the
// sink exactly once.
func TestCrashLoop(t *testing.T) {
	iterations := 50
	if testing.Short() {
		iterations = 10
	}
	const total = 40
	h := newHarness(t)
	ref := model.PRRef{Owner: "crash-owner", Repo: "crash-repo", Number: 42}
	prURL := h.gh.addPR(ref)
	key := strings.ToLower(ref.String())
	// Baseline comments exist before the first subscribe (SUB-08).
	old := time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		h.gh.addCommentAt(ref, "pre"+strconv.Itoa(i), old.Add(time.Duration(i)*time.Minute))
	}

	// The driver starts once the baseline is committed; earlier comments
	// would legitimately be absorbed into the baseline.
	stop := make(chan struct{})
	driverDone := make(chan struct{})
	var stopOnce sync.Once
	stopDriver := func() { stopOnce.Do(func() { close(stop) }) }
	t.Cleanup(stopDriver)
	go func() {
		tk := time.NewTicker(20 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				close(driverDone)
				return
			case <-tk.C:
			}
			if st, ok, err := h.readState(); err == nil && ok && st.PRs[key].Memory.Baselined {
				break
			}
		}
		h.gh.driveComments(ref, "c", total, 150*time.Millisecond, stop, driverDone)
	}()

	deadline := time.Now().Add(45 * time.Second)
	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 22))
	runs, targeted := 0, 0
	for done := false; !done; {
		select {
		case <-driverDone:
			done = runs >= iterations
		default:
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("crash loop did not finish: %d runs, %d requests", runs, h.gh.requestCount())
		}
		p := h.start(true, prURL)
		if runs%2 == 0 {
			time.Sleep(time.Duration(rng.IntN(401)) * time.Millisecond)
		} else {
			// Targeted kill: a rendered line is printed after the sink
			// fsync and before Ack, so killing on it lands in the window
			// where an event is both in the sink and still pending.
			if waitForEventLine(p, 600*time.Millisecond) {
				targeted++
			}
		}
		if err := p.cmd.Process.Kill(); err != nil {
			t.Fatalf("kill: %v", err)
		}
		if err := p.wait(); err != nil && !killed(err) {
			t.Fatalf("run %d exited on its own: %v\n%s", runs, err, p.output())
		} else if err == nil {
			t.Fatalf("run %d exited cleanly before the kill\n%s", runs, p.output())
		}
		runs++
		if _, _, err := h.readState(); err != nil {
			t.Fatalf("after kill %d: %v", runs, err)
		}
	}
	t.Logf("%d killed runs (%d killed right after a delivery), %d GraphQL requests", runs, targeted, h.gh.requestCount())

	// Final run: let it catch up until three consecutive polls find nothing.
	p := h.start(true, prURL)
	quiet := 0
	timeout := time.After(15 * time.Second)
	for quiet < 3 {
		select {
		case l := <-p.lines:
			m := prLineRE.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			if m[1] == "0" {
				quiet++
			} else {
				quiet = 0
			}
		case <-timeout:
			_ = p.cmd.Process.Kill()
			_ = p.wait()
			t.Fatalf("final run never went quiet\n%s", p.output())
		}
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := p.wait(); err != nil {
		t.Fatalf("final run exit: %v\n%s", err, p.output())
	}

	lines := h.readSink()
	count := map[string]int{}
	seqOf := map[string]uint64{}
	var last uint64
	for i, l := range lines {
		if l.Seq <= last {
			t.Errorf("sink line %d: seq %d not above previous %d", i+1, l.Seq, last)
		}
		last = l.Seq
		if s, ok := seqOf[l.Key]; ok && s != l.Seq {
			t.Errorf("key %s appears with seqs %d and %d", l.Key, s, l.Seq)
		}
		seqOf[l.Key] = l.Seq
		count[l.Key]++
	}
	for i := 0; i < total; i++ {
		k := "comment:c" + strconv.Itoa(i)
		if count[k] != 1 {
			t.Errorf("%s delivered %d times, want exactly once", k, count[k])
		}
	}
	for i := 0; i < 3; i++ {
		if k := "comment:pre" + strconv.Itoa(i); count[k] != 0 {
			t.Errorf("baseline comment %s was delivered", k)
		}
	}
	for k, n := range count {
		if !strings.HasPrefix(k, "comment:c") {
			t.Errorf("unexpected event %s (x%d) in sink", k, n)
		}
	}

	st, ok, err := h.readState()
	if err != nil || !ok {
		t.Fatalf("final state: ok=%v err=%v", ok, err)
	}
	found := false
	for id, s := range st.Subs {
		if s.PaneID != devPane {
			continue
		}
		found = true
		if len(s.Pending) != 0 {
			t.Errorf("subscription %s still has %d pending events", id, len(s.Pending))
		}
	}
	if !found {
		t.Error("dev subscription missing from final state")
	}
	tmps, _ := filepath.Glob(filepath.Join(h.stateDir, "state.json.tmp-*"))
	if len(tmps) != 0 {
		t.Errorf("leftover temp files: %v", tmps)
	}
}
