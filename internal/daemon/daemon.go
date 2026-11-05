package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/cli/go-gh/v2/pkg/auth"
	"github.com/gofrs/flock"

	"github.com/drodimin/pr-relay/internal/autostart"
	"github.com/drodimin/pr-relay/internal/config"
	"github.com/drodimin/pr-relay/internal/ctl"
	"github.com/drodimin/pr-relay/internal/deliver"
	"github.com/drodimin/pr-relay/internal/ghclient"
	"github.com/drodimin/pr-relay/internal/paths"
	"github.com/drodimin/pr-relay/internal/poller"
	"github.com/drodimin/pr-relay/internal/store"
)

// ErrAlreadyRunning is returned by Run when another daemon holds the socket
// lock. The caller should exit quietly: the other daemon serves the socket.
var ErrAlreadyRunning = errors.New("daemon: already running")

// Shutdown and lifecycle timing.
const (
	// drainTimeout bounds how long shutdown waits for in-flight requests.
	drainTimeout = 2 * time.Second
	// lifecycleGrace delays the first lifecycle tick after start, so a
	// machine that was off for days does not mass-expire subscriptions
	// before agents had a chance to keep them.
	lifecycleGrace = 10 * time.Minute
	lifecycleEvery = time.Minute
	// Fast-poll (test) values.
	fastLifecycleEvery = 50 * time.Millisecond

	pidFileName = "daemon.pid"
)

// Options configures Run.
type Options struct {
	// Version is the daemon version reported to clients and compared by
	// the version gate. Empty means "dev".
	Version string
	// GraphQLEndpoint overrides the GitHub GraphQL endpoint (tests only;
	// ghclient refuses non-loopback hosts).
	GraphQLEndpoint string
	// Fetcher replaces the GitHub client. It is a test hook; nil means a
	// ghclient using the user's gh credentials.
	Fetcher poller.Fetcher
	// Now is the poller clock. nil means time.Now.
	Now func() time.Time
}

// testHostHook, when set (tests only), receives the host once it is built
// and before the daemon signals ready.
var testHostHook func(*host)

// host is the running daemon: everything the control handlers need.
type host struct {
	eng        *poller.Engine
	mgr        *deliver.Manager
	log        *slog.Logger
	opts       Options
	version    string
	socket     string
	stateDir   string
	logPath    string
	startedAt  time.Time
	authSource string
	now        func() time.Time
}

// Run is the daemon: it takes the singleton socket lock, opens the state,
// listens on the control socket and serves until ctx is cancelled, SIGINT or
// SIGTERM arrives, or a client sends shutdown. It never exits on its own
// otherwise, not even with zero subscriptions or herdr absent (D-18).
//
// Startup order (D-01): socket lock, state lock, stale socket removal,
// listen (0600), engine, delivery, lifecycle and control server, then the
// ready signal. Shutdown order (Pitfall 4): stop accepting and drain, stop
// the goroutines, close the listener (unlinking the socket), close the
// store and release the socket lock last, so a new daemon can never start
// while this one still owns the socket path.
func Run(ctx context.Context, o Options) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	version := o.Version
	if version == "" {
		version = "dev"
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}

	// undo runs the startup cleanups in reverse when startup fails.
	var undo []func()
	fail := func(e error) error {
		autostart.SignalReady(e)
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
		return e
	}

	socket, err := paths.SocketPath()
	if err != nil {
		return fail(err)
	}
	sockLock := socket + ".lock"
	stateDir, err := paths.StateDir()
	if err != nil {
		return fail(err)
	}
	cfgPath, err := paths.ConfigPath()
	if err != nil {
		return fail(err)
	}
	logDir, err := paths.LogDir()
	if err != nil {
		return fail(err)
	}

	lg, err := OpenLog(logDir)
	if err != nil {
		return fail(err)
	}
	undo = append(undo, func() { _ = lg.Close() })
	log := lg.Logger()
	if crash, err := SetCrash(logDir); err != nil {
		log.Warn("crash file unavailable", "error", err)
	} else {
		undo = append(undo, func() { _ = crash.Close() })
		defer func() { _ = crash.Close() }()
	}

	// 1. Socket lock: the singleton guard (D-19).
	if err := touchLock(sockLock); err != nil {
		return fail(fmt.Errorf("daemon: socket lock: %w", err))
	}
	lk := flock.New(sockLock)
	ok, err := lk.TryLock()
	if err != nil {
		_ = lk.Close()
		return fail(fmt.Errorf("daemon: socket lock: %w", err))
	}
	if !ok {
		_ = lk.Close()
		log.Info("another daemon holds the socket lock; exiting", "socket", socket)
		return fail(ErrAlreadyRunning)
	}
	undo = append(undo, func() { _ = lk.Unlock(); _ = lk.Close() })

	// 2. Config and state.
	cfg, warns, err := config.Load(cfgPath)
	for _, w := range warns {
		log.Warn("config", "warning", w)
	}
	if err != nil {
		return fail(err)
	}
	st, state, err := store.Open(stateDir)
	switch {
	case errors.Is(err, store.ErrLocked):
		return fail(fmt.Errorf("state in use by another pr-relay (dev watch?) at %s", stateDir))
	case errors.Is(err, store.ErrNewerVersion):
		return fail(errors.New("state file was written by a newer pr-relay; upgrade pr-relay"))
	case err != nil:
		return fail(err)
	}
	undo = append(undo, func() { _ = st.Close() })

	// 3. Engine and delivery.
	fetcher := o.Fetcher
	if fetcher == nil {
		gc, err := ghclient.New(ghclient.Options{
			Endpoint:  o.GraphQLEndpoint,
			Tokens:    &ghclient.TokenSource{},
			UserAgent: "pr-relay/" + version,
			Logger:    log,
		})
		if err != nil {
			return fail(err)
		}
		fetcher = gc
	}
	eng, err := poller.New(poller.Options{
		Saver:   st,
		State:   state,
		Fetcher: fetcher,
		Config:  cfg,
		Logger:  log,
		Now:     o.Now,
	})
	if err != nil {
		return fail(err)
	}
	if qp, at := st.Quarantine(); qp != "" {
		log.Error("state file was unreadable and has been quarantined", "path", qp)
		eng.SetQuarantine(qp, at)
	}
	mgr, err := deliver.New(deliver.Options{
		Engine:      eng,
		Logger:      log,
		QuietPeriod: cfg.Delivery.QuietPeriod.Duration,
	})
	if err != nil {
		return fail(err)
	}

	// 4. Socket: remove a stale one only while holding the lock, and only
	// if it really is a socket (T-04-38).
	if fi, err := os.Lstat(socket); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return fail(fmt.Errorf("%s exists and is not a socket", socket))
		}
		if err := os.Remove(socket); err != nil {
			return fail(fmt.Errorf("daemon: remove stale socket: %w", err))
		}
		log.Info("removed stale socket", "socket", socket)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(fmt.Errorf("daemon: stat socket: %w", err))
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return fail(fmt.Errorf("daemon: listen: %w", err))
	}
	undo = append(undo, func() { _ = ln.Close() })
	if err := os.Chmod(socket, 0o600); err != nil {
		return fail(fmt.Errorf("daemon: chmod socket: %w", err))
	}
	pidPath := filepath.Join(filepath.Dir(socket), pidFileName)
	if err := writePID(pidPath); err != nil {
		log.Warn("pid file not written", "path", pidPath, "error", err)
	}
	undo = append(undo, func() { _ = os.Remove(pidPath) })

	// 5. Host, control server and goroutines.
	h := &host{
		eng:        eng,
		mgr:        mgr,
		log:        log,
		opts:       o,
		version:    version,
		socket:     socket,
		stateDir:   stateDir,
		logPath:    lg.Path(),
		startedAt:  now(),
		authSource: authSource(),
		now:        now,
	}
	sigCtx, stopSig := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSig()
	trigger, shutdown := context.WithCancel(sigCtx)
	defer shutdown()
	srv := ctl.NewServer(ctl.ServerOptions{
		Self:       ctl.Peer{Version: version, Protocol: ctl.Protocol, PID: os.Getpid()},
		Handlers:   h.handlers(),
		OnShutdown: shutdown,
		Logger:     log,
	})
	if testHostHook != nil {
		testHostHook(h)
	}

	work, stopWork := context.WithCancel(context.Background())
	defer stopWork()
	var wg sync.WaitGroup
	goRun := func(name string, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(work); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("component stopped", "component", name, "error", err)
			}
		}()
	}
	goRun("poller", eng.Run)
	goRun("delivery", mgr.Run)
	grace, every := lifecycleGrace, lifecycleEvery
	if os.Getenv(config.EnvFastPoll) == "1" {
		grace, every = 0, fastLifecycleEvery
	}
	goRun("lifecycle", func(c context.Context) error { return eng.RunLifecycle(c, grace, every) })

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	autostart.SignalReady(nil)
	log.Info("daemon started",
		"version", version, "pid", os.Getpid(), "socket", socket,
		"state_dir", stateDir, "auth_source", h.authSource)

	// Serve until shutdown. Only a failed listener stops the daemon by
	// itself.
	var runErr error
	select {
	case <-trigger.Done():
		log.Info("daemon stopping", "reason", stopReason(ctx, sigCtx))
	case err := <-serveErr:
		if err != nil {
			runErr = fmt.Errorf("daemon: control socket: %w", err)
			log.Error("control socket failed", "error", err)
		}
		serveErr <- nil
	}

	// Shutdown, in this exact order (Pitfall 4, T-04-43).
	if err := srv.Close(drainTimeout); err != nil {
		log.Warn("drain incomplete", "error", err)
	}
	<-serveErr
	stopWork()
	wg.Wait()
	_ = ln.Close() // unlinks the socket (already closed by srv.Close)
	_ = os.Remove(pidPath)
	if err := st.Close(); err != nil {
		log.Warn("store close", "error", err)
	}
	log.Info("daemon stopped", "pid", os.Getpid())
	_ = lk.Unlock()
	_ = lk.Close()
	_ = lg.Close()
	return runErr
}

func stopReason(parent, sig context.Context) string {
	switch {
	case parent.Err() != nil:
		return "context"
	case sig.Err() != nil:
		return "signal"
	}
	return "shutdown request"
}

// touchLock creates the lock file with mode 0600 if needed.
func touchLock(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	cerr := f.Chmod(0o600)
	if err := f.Close(); err != nil && cerr == nil {
		cerr = err
	}
	return cerr
}

// writePID writes the informational pid file (0600).
func writePID(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
	cerr := f.Chmod(0o600)
	if err := f.Close(); err != nil && werr == nil {
		werr = err
	}
	if werr != nil {
		return werr
	}
	return cerr
}

// authSource names where a GitHub token would come from, without reading
// the keyring or spawning gh (the poller resolves the token itself on its
// first poll). It never returns the token.
func authSource() string {
	if tok, src := auth.TokenFromEnvOrConfig("github.com"); tok != "" {
		return src
	}
	if _, err := exec.LookPath("gh"); err == nil {
		return "gh"
	}
	return "none"
}
