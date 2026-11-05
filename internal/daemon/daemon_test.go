package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"github.com/drodimin/pr-relay/internal/autostart"
	"github.com/drodimin/pr-relay/internal/config"
	"github.com/drodimin/pr-relay/internal/ctl"
	"github.com/drodimin/pr-relay/internal/ghclient"
	"github.com/drodimin/pr-relay/internal/ghquery"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/paths"
	"github.com/drodimin/pr-relay/internal/store"
)

const testVersion = "1.2.3-test"

// fakeFetcher answers every Batch from per-PR scripted results. A PR with
// no script is OPEN.
type fakeFetcher struct {
	mu    sync.Mutex
	state map[string]string // PR key -> State ("OPEN", "MERGED", ...)
	errs  map[string]error  // PR key -> per-item error
	batch error             // batch-level error
	calls int
}

func newFakeFetcher() *fakeFetcher {
	return &fakeFetcher{state: map[string]string{}, errs: map[string]error{}}
}

func (f *fakeFetcher) Batch(_ context.Context, refs []model.PRRef, _ ghquery.Window) (ghclient.BatchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.batch != nil {
		return ghclient.BatchResult{}, f.batch
	}
	res := ghclient.BatchResult{
		Viewer:      "me",
		RateLimit:   ghquery.RateLimit{Cost: 1, Limit: 5000, Remaining: 4999},
		ResetAt:     time.Now().Add(time.Hour),
		TokenSource: "GH_TOKEN",
	}
	for _, r := range refs {
		k := store.PRKey(r)
		if err := f.errs[k]; err != nil {
			res.Items = append(res.Items, ghquery.AliasResult{Ref: r, Err: err})
			continue
		}
		st := f.state[k]
		if st == "" {
			st = "OPEN"
		}
		res.Items = append(res.Items, ghquery.AliasResult{Ref: r, Snapshot: model.Snapshot{Ref: r, State: st, HeadOID: "head1"}})
	}
	return res, nil
}

func (f *fakeFetcher) set(key, state string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state[key] = state
	if err != nil {
		f.errs[key] = err
	} else {
		delete(f.errs, key)
	}
}

func (f *fakeFetcher) setBatchErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batch = err
}

// testEnv points every pr-relay path at fresh temp locations and strips
// the environment that could reach a real herdr or daemon. It returns the
// socket path.
func testEnv(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "dmn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	t.Setenv(paths.EnvSocket, sock)
	t.Setenv(paths.EnvStateDir, filepath.Join(t.TempDir(), "state"))
	t.Setenv(paths.EnvLogDir, filepath.Join(t.TempDir(), "log"))
	t.Setenv(paths.EnvConfig, filepath.Join(t.TempDir(), "config.toml"))
	t.Setenv(config.EnvFastPoll, "1")
	t.Setenv("GH_TOKEN", "ghp_secretvalue123")
	for _, k := range []string{"HERDR_SOCKET_PATH", "HERDR_SESSION", "HERDR_PANE_ID", "HERDR_ENV", "XDG_RUNTIME_DIR", autostart.EnvReadyFD, "GITHUB_TOKEN"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	return sock
}

// daemonRun is one daemon started by startDaemon.
type daemonRun struct {
	sock   string
	cancel context.CancelFunc
	done   chan error
	host   *host
}

// startDaemon runs the daemon in a goroutine and waits until hello answers.
func startDaemon(t *testing.T, o Options) *daemonRun {
	t.Helper()
	sock := os.Getenv(paths.EnvSocket)
	if o.Version == "" {
		o.Version = testVersion
	}
	if o.Fetcher == nil {
		o.Fetcher = newFakeFetcher()
	}
	hostCh := make(chan *host, 1)
	testHostHook = func(h *host) { hostCh <- h }
	t.Cleanup(func() { testHostHook = nil })

	ctx, cancel := context.WithCancel(context.Background())
	d := &daemonRun{sock: sock, cancel: cancel, done: make(chan error, 1)}
	go func() { d.done <- Run(ctx, o) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-d.done:
		case <-time.After(10 * time.Second):
			t.Errorf("daemon did not stop")
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case err := <-d.done:
			t.Fatalf("daemon exited during startup: %v", err)
		default:
		}
		if err := hello(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not answer hello")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case d.host = <-hostCh:
	case <-time.After(time.Second):
		t.Fatal("host hook not called")
	}
	return d
}

// wait waits for Run to return.
func (d *daemonRun) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-d.done:
		d.done <- err // keep it for the cleanup
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not stop")
		return nil
	}
}

func hello(sock string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := ctl.NewClient(sock, testVersion, nil).Call(ctx, ctl.MethodHello, nil, nil)
	return err
}

func call(t *testing.T, sock, method string, params, result any) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := ctl.NewClient(sock, testVersion, nil).Call(ctx, method, params, result)
	return err
}

func lockFree(t *testing.T, sock string) bool {
	t.Helper()
	lk := flock.New(sock + ".lock")
	ok, err := lk.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		_ = lk.Unlock()
	}
	_ = lk.Close()
	return ok
}

func TestDaemonStartsAndStops(t *testing.T) {
	sock := testEnv(t)
	d := startDaemon(t, Options{})
	fi, err := os.Lstat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode = %v, want socket 0600", fi.Mode())
	}
	di, err := os.Stat(filepath.Dir(sock))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("socket dir mode = %o, want 700", di.Mode().Perm())
	}
	if lockFree(t, sock) {
		t.Errorf("socket lock is free while the daemon runs")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(sock), pidFileName)); err != nil {
		t.Errorf("pid file: %v", err)
	}
	d.cancel()
	if err := d.wait(t); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket still present after stop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(sock), pidFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("pid file still present: %v", err)
	}
	if !lockFree(t, sock) {
		t.Errorf("socket lock still held after stop")
	}
}

func TestDaemonAlreadyRunning(t *testing.T) {
	sock := testEnv(t)
	startDaemon(t, Options{})
	testHostHook = nil
	err := Run(context.Background(), Options{Version: testVersion, Fetcher: newFakeFetcher()})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Run = %v, want ErrAlreadyRunning", err)
	}
	if err := hello(sock); err != nil {
		t.Fatalf("first daemon no longer answers: %v", err)
	}
}

func TestDaemonStaleSocket(t *testing.T) {
	t.Run("stale socket replaced", func(t *testing.T) {
		sock := testEnv(t)
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		ln.(*net.UnixListener).SetUnlinkOnClose(false)
		_ = ln.Close()
		if _, err := os.Lstat(sock); err != nil {
			t.Fatalf("leftover socket missing: %v", err)
		}
		startDaemon(t, Options{})
		if err := hello(sock); err != nil {
			t.Fatalf("hello: %v", err)
		}
	})
	t.Run("regular file refused", func(t *testing.T) {
		sock := testEnv(t)
		if err := os.WriteFile(sock, []byte("not a socket"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := Run(context.Background(), Options{Version: testVersion, Fetcher: newFakeFetcher()})
		if err == nil || !strings.Contains(err.Error(), "exists and is not a socket") {
			t.Fatalf("Run = %v, want 'exists and is not a socket'", err)
		}
		b, rerr := os.ReadFile(sock)
		if rerr != nil || string(b) != "not a socket" {
			t.Errorf("regular file touched: %q %v", b, rerr)
		}
		if !lockFree(t, sock) {
			t.Errorf("socket lock held after failed start")
		}
	})
}

func TestDaemonStateLocked(t *testing.T) {
	testEnv(t)
	dir, err := paths.StateDir()
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	err = Run(context.Background(), Options{Version: testVersion, Fetcher: newFakeFetcher()})
	if err == nil || !strings.Contains(err.Error(), "state in use by another pr-relay") {
		t.Fatalf("Run = %v, want 'state in use by another pr-relay'", err)
	}
}

func TestDaemonRestartLoop(t *testing.T) {
	sock := testEnv(t)
	for i := 0; i < 10; i++ {
		d := startDaemon(t, Options{})
		if err := hello(sock); err != nil {
			t.Fatalf("iteration %d: hello: %v", i, err)
		}
		if err := call(t, sock, ctl.MethodShutdown, nil, nil); err != nil {
			t.Fatalf("iteration %d: shutdown: %v", i, err)
		}
		if err := d.wait(t); err != nil {
			t.Fatalf("iteration %d: Run = %v", i, err)
		}
		if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("iteration %d: socket left behind: %v", i, err)
		}
	}
	startDaemon(t, Options{})
	if err := hello(sock); err != nil {
		t.Fatalf("final hello: %v", err)
	}
}

func TestDaemonIdle(t *testing.T) {
	sock := testEnv(t)
	d := startDaemon(t, Options{})
	time.Sleep(2 * time.Second)
	select {
	case err := <-d.done:
		t.Fatalf("daemon exited on its own: %v", err)
	default:
	}
	if err := hello(sock); err != nil {
		t.Fatalf("hello after idle: %v", err)
	}
}

func TestLoggingDaemonStarted(t *testing.T) {
	testEnv(t)
	d := startDaemon(t, Options{})
	b, err := os.ReadFile(d.host.logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(b)
	var started string
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "daemon started") {
			started = l
		}
	}
	if started == "" || !strings.Contains(started, "level=INFO") {
		t.Fatalf("no info 'daemon started' line in %q", log)
	}
	if !strings.Contains(started, "auth_source=GH_TOKEN") {
		t.Errorf("started line lacks auth source: %q", started)
	}
	if strings.Contains(log, "GH_TOKEN=") || strings.Contains(log, "ghp_secretvalue123") {
		t.Errorf("log contains token text: %q", log)
	}
	if fi, err := os.Stat(d.host.logPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("log mode: %v %v", fi, err)
	}
}
