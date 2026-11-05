package autostart

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"github.com/drodimin/pr-relay/internal/ctl"
	"github.com/drodimin/pr-relay/internal/paths"
)

// Timeouts for the liveness probes.
const (
	probeTimeout     = 300 * time.Millisecond // first hello, before any spawn
	postSpawnTimeout = time.Second            // hello after a spawn
	lockRetry        = 25 * time.Millisecond
)

// AlreadyRunningMsg is the startup error a spawned daemon reports when
// another daemon holds the socket lock. It must equal
// daemon.ErrAlreadyRunning.Error(); autostart cannot import daemon (daemon
// imports autostart), so a guard test in cmd/pr-relay keeps them in step.
const AlreadyRunningMsg = "daemon: already running"

// Options configures Connect, Call, Stop and Restart.
type Options struct {
	// Version is the CLI's version, compared with the daemon's.
	Version string
	// Exe is the binary to spawn; default os.Executable().
	Exe string
	// Args are the daemon arguments; default {"daemon"}.
	Args []string
	// Env is the daemon environment; default os.Environ().
	Env []string
	// Stderr receives the one-line restart note; default io.Discard.
	Stderr io.Writer
	// SpawnTimeout bounds waiting for the ready pipe; default 3 s.
	SpawnTimeout time.Duration
	// LockTimeout bounds waiting for another CLI's spawn; default 4 s.
	LockTimeout time.Duration
	// ExitTimeout bounds waiting for a stopped daemon to exit; default 5 s.
	ExitTimeout time.Duration
}

func (o Options) withDefaults() (Options, error) {
	if o.Exe == "" {
		exe, err := os.Executable()
		if err != nil {
			return o, fmt.Errorf("autostart: resolve executable: %w", err)
		}
		o.Exe = exe
	}
	if o.Args == nil {
		o.Args = []string{"daemon"}
	}
	if o.Env == nil {
		o.Env = os.Environ()
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if o.SpawnTimeout <= 0 {
		o.SpawnTimeout = 3 * time.Second
	}
	if o.LockTimeout <= 0 {
		o.LockTimeout = 4 * time.Second
	}
	if o.ExitTimeout <= 0 {
		o.ExitTimeout = 5 * time.Second
	}
	return o, nil
}

// Connect returns a client for a live daemon, spawning one when none is
// running. Concurrent callers serialise on the spawn lock and re-check under
// it, so a cold machine gets one daemon (D-19). Connect only proves
// liveness: hello is exempt from the version gate.
func Connect(ctx context.Context, o Options) (*ctl.Client, error) {
	o, err := o.withDefaults()
	if err != nil {
		return nil, err
	}
	socket, err := paths.SocketPath()
	if err != nil {
		return nil, err
	}
	c := ctl.NewClient(socket, o.Version, nil)
	if _, err := probe(ctx, c, socket); err == nil {
		return c, nil
	} else if !isDown(err) {
		return nil, reachErr(socket, err)
	}

	unlock, err := spawnLock(ctx, o)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := ensureUp(ctx, o, c, socket); err != nil {
		return nil, err
	}
	return c, nil
}

// ensureUp runs under the spawn lock: it spawns the daemon unless another
// CLI already did, then proves it answers.
func ensureUp(ctx context.Context, o Options, c *ctl.Client, socket string) error {
	if _, err := probe(ctx, c, socket); err == nil {
		return nil
	} else if !isDown(err) {
		return reachErr(socket, err)
	}
	spawnErr := spawn(ctx, o.Exe, o.Args, o.Env, o.SpawnTimeout)
	_, err := hello(ctx, c, postSpawnTimeout)
	if err == nil {
		// A daemon answers: either ours, or one that won the socket lock
		// while ours lost it and exited.
		return nil
	}
	// Ours lost the socket lock, or came up and nobody answers: an old
	// daemon is in its shutdown window (socket unlinked, lock still held).
	// Wait for it to release the lock and spawn exactly once more (WR-03).
	// A genuine startup error is returned as is, never retried.
	alreadyRunning := spawnErr != nil && strings.Contains(spawnErr.Error(), AlreadyRunningMsg)
	if alreadyRunning || (spawnErr == nil && isDown(err)) {
		if werr := waitExited(ctx, socket, o.ExitTimeout); werr != nil {
			return werr
		}
		spawnErr = spawn(ctx, o.Exe, o.Args, o.Env, o.SpawnTimeout)
		if _, err = hello(ctx, c, postSpawnTimeout); err == nil {
			return nil
		}
	}
	if spawnErr != nil {
		return spawnErr
	}
	return reachErr(socket, err)
}

// Call connects (spawning if needed) and sends one request. When the daemon
// refuses with version_mismatch, Call restarts it once, retries once and
// writes "restarted daemon (OLD -> NEW)" to o.Stderr. A second mismatch is
// an error, never a loop (D-17, T-04-25).
func Call(ctx context.Context, o Options, method string, params, result any) (ctl.Peer, error) {
	o, err := o.withDefaults()
	if err != nil {
		return ctl.Peer{}, err
	}
	c, err := Connect(ctx, o)
	if err != nil {
		return ctl.Peer{}, err
	}
	peer, err := c.Call(ctx, method, params, result)
	var vm *ctl.VersionMismatchError
	if !errors.As(err, &vm) {
		return peer, err
	}
	old := vm.Daemon.Version
	if err := Restart(ctx, o); err != nil {
		return ctl.Peer{}, err
	}
	peer, err = c.Call(ctx, method, params, result)
	if errors.As(err, &vm) {
		return peer, fmt.Errorf("daemon version mismatch persists after restart (cli %s, daemon %s)",
			o.Version, vm.Daemon.Version)
	}
	if err != nil {
		return peer, err
	}
	fmt.Fprintf(o.Stderr, "restarted daemon (%s -> %s)\n", old, o.Version)
	return peer, nil
}

// Stop asks a running daemon to shut down and waits until it has fully
// exited. It never spawns. wasRunning is false when no daemon answered.
func Stop(ctx context.Context, o Options) (wasRunning bool, err error) {
	o, err = o.withDefaults()
	if err != nil {
		return false, err
	}
	socket, err := paths.SocketPath()
	if err != nil {
		return false, err
	}
	return stop(ctx, o, ctl.NewClient(socket, o.Version, nil), socket)
}

func stop(ctx context.Context, o Options, c *ctl.Client, socket string) (bool, error) {
	if _, err := hello(ctx, c, postSpawnTimeout); err != nil {
		if isDown(err) {
			return false, nil
		}
		return false, reachErr(socket, err)
	}
	sctx, cancel := context.WithTimeout(ctx, postSpawnTimeout)
	_, err := c.Call(sctx, ctl.MethodShutdown, nil, nil)
	cancel()
	if err != nil && !ctl.IsUnreachable(err) {
		return true, err
	}
	return true, waitExited(ctx, socket, o.ExitTimeout)
}

// waitExited waits for the daemon's socket lock to become free. The daemon
// releases it last, after unlinking the socket and closing the store, so
// acquiring it proves the daemon fully exited.
func waitExited(ctx context.Context, socket string, timeout time.Duration) error {
	lk := flock.New(socket + ".lock")
	defer lk.Close()
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ok, err := lk.TryLockContext(wctx, lockRetry)
	if ok {
		_ = lk.Unlock()
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("autostart: wait for daemon exit: %w", err)
	}
	return fmt.Errorf("daemon did not exit within %s", timeout)
}

// Restart stops the running daemon (whichever version it is; no downgrade
// guard, D-17) and spawns this binary's, all under the spawn lock. When
// another CLI already restarted it to o.Version, Restart does nothing.
func Restart(ctx context.Context, o Options) error {
	o, err := o.withDefaults()
	if err != nil {
		return err
	}
	socket, err := paths.SocketPath()
	if err != nil {
		return err
	}
	c := ctl.NewClient(socket, o.Version, nil)
	unlock, err := spawnLock(ctx, o)
	if err != nil {
		return err
	}
	defer unlock()

	if p, err := hello(ctx, c, probeTimeout); err == nil &&
		p.Version == o.Version && p.Protocol == ctl.Protocol {
		return nil
	}
	if _, err := stop(ctx, o, c, socket); err != nil {
		return err
	}
	return ensureUp(ctx, o, c, socket)
}

// spawnLock takes the spawn lock (created 0600) within o.LockTimeout and
// returns its release function.
func spawnLock(ctx context.Context, o Options) (func(), error) {
	path, err := paths.SpawnLockPath()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("autostart: create spawn lock: %w", err)
	}
	_ = f.Close()
	lk := flock.New(path)
	lctx, cancel := context.WithTimeout(ctx, o.LockTimeout)
	defer cancel()
	ok, err := lk.TryLockContext(lctx, lockRetry)
	if !ok {
		_ = lk.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("autostart: spawn lock: %w", err)
		}
		return nil, errors.New("timed out waiting for another pr-relay to start the daemon")
	}
	return func() {
		_ = lk.Unlock()
		_ = lk.Close()
	}, nil
}

// probeRetryDelays are the pauses before each longer hello retry in probe.
var probeRetryDelays = []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}

// probe checks for a live daemon. A hello that times out means something is
// listening but slow (a busy daemon), so probe retries twice with the longer
// post-spawn timeout before giving up with a message naming the daemon log
// (WR-08). Any other error, such as a missing or refused socket, is returned
// unchanged so isDown still routes the caller to a spawn. The retries are
// bounded, so a hung daemon costs at most about 2.6 s (T-05-07).
func probe(ctx context.Context, c *ctl.Client, socket string) (ctl.Peer, error) {
	p, err := hello(ctx, c, probeTimeout)
	for _, d := range probeRetryDelays {
		if err == nil || !isTimeout(err) || ctx.Err() != nil {
			return p, err
		}
		select {
		case <-ctx.Done():
			return ctl.Peer{}, ctx.Err()
		case <-time.After(d):
		}
		p, err = hello(ctx, c, postSpawnTimeout)
	}
	if err != nil && isTimeout(err) && ctx.Err() == nil {
		return ctl.Peer{}, fmt.Errorf("daemon at %s is not answering (hello timed out); see %s: %w", socket, logHint(), err)
	}
	return p, err
}

// isTimeout reports whether a hello failed because its deadline passed. The
// connection deadline equals the context deadline, so either can fire first.
func isTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded)
}

// hello probes the daemon with its own timeout.
func hello(ctx context.Context, c *ctl.Client, timeout time.Duration) (ctl.Peer, error) {
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.Call(hctx, ctl.MethodHello, nil, nil)
}

// isDown reports whether err means no daemon is listening: the socket file
// is missing or nobody accepts on it (a stale socket).
func isDown(err error) bool {
	return ctl.IsUnreachable(err) &&
		(errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED))
}

// reachErr explains a failure to talk to the daemon, naming a possible
// agent sandbox for EPERM/EACCES (Pitfall 12).
func reachErr(socket string, err error) error {
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		return fmt.Errorf("cannot reach the pr-relay daemon at %s (operation not permitted); your agent sandbox may block local sockets", socket)
	}
	return err
}
