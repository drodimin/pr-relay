package autostart

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"github.com/drodimin/pr-relay/internal/ctl"
	"github.com/drodimin/pr-relay/internal/paths"
)

// The test binary doubles as a fake daemon when re-executed with these set.
const (
	envFake        = "AUTOSTART_FAKE_DAEMON"
	envFakeVersion = "AUTOSTART_FAKE_VERSION"
	envFakeFail    = "AUTOSTART_FAKE_FAIL"
	// envFakeLinger makes the fake hold its socket lock for this long
	// (a time.Duration string) after unlinking the socket on shutdown.
	envFakeLinger = "AUTOSTART_FAKE_LINGER"
)

func TestMain(m *testing.M) {
	if os.Getenv(envFake) == "1" {
		os.Exit(fakeDaemon())
	}
	os.Exit(m.Run())
}

// fakeDaemon mimics the real daemon's lifecycle contract: socket lock first,
// stale socket removal, listen 0600, SignalReady, and on shutdown close the
// listener, unlink the socket and release the lock last.
func fakeDaemon() int {
	if msg := os.Getenv(envFakeFail); msg != "" {
		SignalReady(errors.New(msg))
		return 1
	}
	socket, err := paths.SocketPath()
	if err != nil {
		SignalReady(err)
		return 1
	}
	lk := flock.New(socket + ".lock")
	ok, err := lk.TryLock()
	if err != nil {
		SignalReady(err)
		return 1
	}
	if !ok {
		// Another daemon owns the socket; report it like the real daemon.
		SignalReady(errors.New(AlreadyRunningMsg))
		return 0
	}
	_ = os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		SignalReady(err)
		return 1
	}
	_ = os.Chmod(socket, 0o600)

	self := ctl.Peer{Version: os.Getenv(envFakeVersion), Protocol: ctl.Protocol, PID: os.Getpid()}
	done := make(chan struct{})
	var once sync.Once
	srv := ctl.NewServer(ctl.ServerOptions{
		Self: self,
		Handlers: map[string]ctl.Handler{
			ctl.MethodStatus: func(context.Context, json.RawMessage) (any, error) {
				return ctl.StatusResult{Version: self.Version, PID: self.PID, Socket: socket}, nil
			},
		},
		OnShutdown: func() { once.Do(func() { close(done) }) },
	})
	go func() { _ = srv.Serve(ln) }()
	SignalReady(nil)

	select {
	case <-done:
	case <-time.After(60 * time.Second): // never leak a fake past the test run
	}
	_ = srv.Close(time.Second)
	_ = os.Remove(socket)
	if d, err := time.ParseDuration(os.Getenv(envFakeLinger)); err == nil && d > 0 {
		time.Sleep(d) // shutdown window: socket gone, lock still held
	}
	_ = lk.Unlock()
	_ = lk.Close()
	return 0
}

// setup isolates the test's socket and returns Options that spawn the test
// binary as a fake daemon at version fakeVersion.
func setup(t *testing.T, cliVersion, fakeVersion string) Options {
	t.Helper()
	isolate(t)
	o := Options{Version: cliVersion, Exe: os.Args[0], Env: fakeEnv(fakeVersion)}
	t.Cleanup(func() {
		_, _ = Stop(context.Background(), o)
	})
	return o
}

// isolate points the socket, log and state paths at a fresh short temp dir
// (short because of the macOS sun_path limit) and returns the socket path.
func isolate(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "as")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv(paths.EnvSocket, filepath.Join(dir, "s", "pr-relay.sock"))
	t.Setenv(paths.EnvLogDir, filepath.Join(dir, "log"))
	t.Setenv(paths.EnvStateDir, filepath.Join(dir, "state"))
	socket, err := paths.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	return socket
}

func fakeEnv(version string, extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HERDR_") || strings.HasPrefix(kv, "XDG_RUNTIME_DIR=") ||
			strings.HasPrefix(kv, "AUTOSTART_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, envFake+"=1", envFakeVersion+"="+version)
	return append(env, extra...)
}

func status(t *testing.T, c *ctl.Client) ctl.StatusResult {
	t.Helper()
	var st ctl.StatusResult
	if _, err := c.Call(context.Background(), ctl.MethodStatus, nil, &st); err != nil {
		t.Fatalf("status: %v", err)
	}
	return st
}

// reap collects an exited fake daemon (the test process is its parent) and
// reports whether pid is gone.
func reap(pid int) bool {
	done := make(chan struct{})
	go func() {
		var ws syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &ws, 0, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		return false
	}
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func TestConnectSpawns(t *testing.T) {
	o := setup(t, "0.4.0", "0.4.0")
	ctx := context.Background()
	c, err := Connect(ctx, o)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	st := status(t, c)
	if st.PID == os.Getpid() || st.PID == 0 {
		t.Fatalf("status pid %d, want a separate daemon", st.PID)
	}
	running, err := Stop(ctx, o)
	if err != nil || !running {
		t.Fatalf("Stop = %v, %v; want true, nil", running, err)
	}
	if _, err := os.Lstat(c.Socket()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still present after Stop: %v", err)
	}
	if !reap(st.PID) {
		t.Fatalf("daemon %d still alive after Stop", st.PID)
	}
	running, err = Stop(ctx, o)
	if err != nil || running {
		t.Fatalf("second Stop = %v, %v; want false, nil", running, err)
	}
}

func TestConcurrentConnect(t *testing.T) {
	o := setup(t, "0.4.0", "0.4.0")
	const n = 20
	var wg sync.WaitGroup
	pids := make([]int, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var st ctl.StatusResult
			peer, err := Call(context.Background(), o, ctl.MethodStatus, nil, &st)
			pids[i], errs[i] = peer.PID, err
		}()
	}
	wg.Wait()
	set := map[int]bool{}
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		set[pids[i]] = true
	}
	if len(set) != 1 {
		t.Fatalf("daemon PIDs %v, want exactly one", set)
	}
}

func TestMismatchRestart(t *testing.T) {
	o := setup(t, "0.4.0", "0.4.0")
	ctx := context.Background()
	old := o
	old.Version, old.Env = "0.3.0", fakeEnv("0.3.0")
	c, err := Connect(ctx, old)
	if err != nil {
		t.Fatalf("Connect old: %v", err)
	}
	oldPID := status(t, ctl.NewClient(c.Socket(), "0.3.0", nil)).PID

	var stderr bytes.Buffer
	o.Stderr = &stderr
	var st ctl.StatusResult
	peer, err := Call(ctx, o, ctl.MethodStatus, nil, &st)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if peer.Version != "0.4.0" || st.Version != "0.4.0" {
		t.Fatalf("daemon version %q / %q, want 0.4.0", peer.Version, st.Version)
	}
	if got, want := stderr.String(), "restarted daemon (0.3.0 -> 0.4.0)\n"; got != want {
		t.Fatalf("stderr %q, want %q", got, want)
	}
	if peer.PID == oldPID {
		t.Fatalf("daemon pid unchanged (%d)", oldPID)
	}
	if !reap(oldPID) {
		t.Fatalf("old daemon %d still alive", oldPID)
	}
}

func TestMismatchTwice(t *testing.T) {
	o := setup(t, "0.4.0", "0.3.0") // every spawned daemon is 0.3.0
	var stderr bytes.Buffer
	o.Stderr = &stderr
	_, err := Call(context.Background(), o, ctl.MethodStatus, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "version mismatch persists") {
		t.Fatalf("Call err = %v, want version mismatch persists", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected restart note %q", stderr.String())
	}
}

func TestStartupError(t *testing.T) {
	o := setup(t, "0.4.0", "0.4.0")
	const msg = "state file was written by a newer pr-relay"
	o.Env = fakeEnv("0.4.0", envFakeFail+"="+msg)
	_, err := Connect(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), msg) {
		t.Fatalf("Connect err = %v, want %q", err, msg)
	}
}

func TestStaleSocket(t *testing.T) {
	o := setup(t, "0.4.0", "0.4.0")
	socket, err := paths.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	// Leave a socket file with no listener behind.
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	if _, err := os.Lstat(socket); err != nil {
		t.Fatalf("stale socket missing: %v", err)
	}
	if _, err := ctl.NewClient(socket, "0.4.0", nil).Call(context.Background(), ctl.MethodHello, nil, nil); !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("precondition: hello err %v, want ECONNREFUSED", err)
	}
	c, err := Connect(context.Background(), o)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if st := status(t, c); st.PID == 0 {
		t.Fatalf("no daemon pid")
	}
}

func TestReadyTimeoutAndEOF(t *testing.T) {
	t.Setenv(paths.EnvLogDir, t.TempDir())
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	err = readReady(r, 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not become ready within") {
		t.Fatalf("timeout err = %v", err)
	}
	_ = w.Close()
	err = readReady(r, time.Second)
	if err == nil || !strings.Contains(err.Error(), "exited during startup") {
		t.Fatalf("EOF err = %v", err)
	}
	_ = r.Close()
}

// slowListener delays every accepted connection, like a daemon whose accept
// loop is busy (WR-08).
type slowListener struct {
	net.Listener
	delay time.Duration
}

func (l slowListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		time.Sleep(l.delay)
	}
	return c, err
}

func TestSlowHelloRetried(t *testing.T) {
	socket := isolate(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := ctl.NewServer(ctl.ServerOptions{Self: ctl.Peer{Version: "v-test", Protocol: ctl.Protocol, PID: os.Getpid()}})
	go func() { _ = srv.Serve(slowListener{Listener: ln, delay: 700 * time.Millisecond}) }()
	t.Cleanup(func() { _ = srv.Close(time.Second) })

	// A spawn would fail, so success proves the live daemon was retried.
	o := Options{Version: "v-test", Exe: "/nonexistent/pr-relay"}
	if _, err := Connect(context.Background(), o); err != nil {
		t.Fatalf("Connect: %v", err)
	}
}

func TestHelloNeverAnswers(t *testing.T) {
	socket := isolate(t)
	ln, err := net.Listen("unix", socket) // never accepts
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	o := Options{Version: "v-test", Exe: "/nonexistent/pr-relay"}
	start := time.Now()
	_, err = Connect(context.Background(), o)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Connect took %s, want under 5s", elapsed)
	}
	if err == nil {
		t.Fatal("Connect succeeded against a hung daemon")
	}
	msg := err.Error()
	if !strings.Contains(msg, "not answering") || !strings.Contains(msg, "daemon log") {
		t.Fatalf("Connect err = %q, want it to name a non-answering daemon and the daemon log", msg)
	}
	if strings.Contains(msg, "nonexistent") {
		t.Fatalf("Connect err = %q, want no spawn attempt", msg)
	}
}

func TestConnectDuringShutdown(t *testing.T) {
	o := setup(t, "0.4.0", "0.4.0")
	o.Env = fakeEnv("0.4.0", envFakeLinger+"=800ms")
	ctx := context.Background()
	c, err := Connect(ctx, o)
	if err != nil {
		t.Fatalf("Connect A: %v", err)
	}
	pidA := status(t, c).PID

	// Ask A to shut down without waiting for it to exit.
	if _, err := c.Call(ctx, ctl.MethodShutdown, nil, nil); err != nil && !ctl.IsUnreachable(err) {
		t.Fatalf("shutdown A: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Lstat(c.Socket()); errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket still present 3s after shutdown")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A now lingers holding the socket lock.

	c2, err := Connect(ctx, o)
	if err != nil {
		if strings.Contains(err.Error(), "already running") {
			t.Fatalf("Connect during shutdown surfaced %q", err)
		}
		t.Fatalf("Connect during shutdown: %v", err)
	}
	if pidB := status(t, c2).PID; pidB == pidA || pidB == 0 {
		t.Fatalf("daemon pid %d after shutdown window, want a fresh daemon (A was %d)", pidB, pidA)
	}
	if !reap(pidA) {
		t.Fatalf("daemon A %d still alive", pidA)
	}
}
