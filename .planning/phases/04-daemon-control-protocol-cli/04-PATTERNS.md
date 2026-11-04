# Phase 4: Daemon, Control Protocol & CLI - Pattern Map

**Mapped:** 2026-11-04
**Files analyzed:** 31 (new or modified, tests included)
**Analogs found:** 29 / 31

Module path: `github.com/drodimin/pr-relay`. All line numbers refer to the tree at commit `9d13f9b`.

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `internal/ctl/wire.go` (NEW: Request/Response/Peer/Error, codes, Protocol) | model (wire types) | request-response | `internal/herdr/client.go` L95-105 + `internal/herdr/errors.go` L8-64 | exact |
| `internal/ctl/client.go` (NEW) | service (IPC client) | request-response | `internal/herdr/client.go` L34-196 (`DialUnix`, `roundTrip`) | exact |
| `internal/ctl/server.go` (NEW: Serve(ln), ServeConn(conn), version gate) | controller (IPC server) | request-response | `internal/herdr/herdrtest/server.go` L356-373, L438-519 | role-match |
| `internal/ctl/*_test.go` | test | request-response | `internal/herdr/client_test.go` + `herdrtest.Server.Dialer()` (net.Pipe, synctest) | exact |
| `internal/daemon/daemon.go` (NEW: Run, locks, listen, ready fd, host) | service (host) | event-driven | `cmd/pr-relay/dev.go` `runDevDeliver` L98-209 + `setupDev` L298-374 | exact |
| `internal/daemon/handlers.go` (NEW: sub/unsub/keep/poll/ls/status/reset/shutdown) | controller | request-response | `cmd/pr-relay/dev.go` L140-181 (identity capture + Subscribe) | role-match |
| `internal/daemon/log.go` (NEW: slog + lumberjack, SetCrashOutput) | utility | file-I/O | `cmd/pr-relay/dev.go` L323 (slog handler) | partial |
| `internal/autostart/autostart.go` (NEW: Connect, spawn, Restart, Stop) | service | request-response + process spawn | `spikes/setsid-probe/main.go` L68-98 | exact (spawn); flock from `internal/store/store.go` L60-80 |
| `internal/autostart/sysproc_unix.go` (NEW, optional build file) | config | n/a | `spikes/setsid-probe/sid_darwin.go`, `sid_other.go` | exact |
| `internal/paths/paths.go` (EXTEND: SocketPath, LogDir, SpawnLockPath) | utility | file-I/O | same file, `StateDir` L35-60 | exact |
| `internal/paths/paths_test.go` (EXTEND) | test | n/a | same file L10-48 (`clearEnv`) | exact |
| `internal/herdr/env.go` (NEW: ResolveSocket(getenv), PaneFromEnv) | utility | transform (pure) | `internal/paths/paths.go` `ConfigPath` L64-76 | role-match |
| `internal/herdr/endpoint.go` (FIX IN-02, IN-03: KindResubscribed) | service | event-driven | same file L190-249 | exact |
| `internal/deliver/manager.go` (FIX IN-03 handling, add `Sockets()`, WR-09 abs-socket guard) | service | event-driven | same file L403-427, L200-206 | exact |
| `internal/compose/compose.go` (FIX IN-01 partial-ack cap) | utility (pure) | transform | same file L143-160 | exact |
| `internal/poller/engine.go` (EXTEND: SubscribeVerified, UnsubscribePane, Reset, ackSub clock/EndSeq, Keep clears check-in) | service | CRUD (copy-on-write) | same file `Subscribe` L361-439, `Cancel` L468-487, `ackSub` L618-629 | exact |
| `internal/poller/lifecycle.go` (NEW: LifecycleTick, RunLifecycle) | service | batch (ticker) | `internal/poller/scheduler.go` `Run` L36-63 + `Engine.mutate` | exact |
| `internal/poller/fanout.go` (EXTEND: mask bypass for closed + lifecycle) | utility | transform | same file L31-41, L63-101 | exact |
| `internal/poller/scheduler.go` (EXTEND: skip `Ending != ""` subs in `planLocked`) | service | batch | same file L87-103 | exact |
| `internal/poller/status.go` (EXTEND: Subs excludes Ending, lifecycle info) | model | request-response | same file L25-84 | exact |
| `internal/store/state.go` (EXTEND: SchemaVersion 3, new Subscription fields) | model | CRUD | same file L42-43, L89-124 | exact |
| `internal/store/store.go` (EXTEND: migrate v2→v3) | migration | file-I/O | same file `migrate` L134-156 | exact |
| `internal/model/model.go` (EXTEND: ParsePRLink, EventCheckin/EventExpired, IsLifecycle, IdleSecs) | model | transform (pure) | same file `ParsePRURL` L43-89, EventType L184-211 | exact |
| `internal/render/render.go` (EXTEND: check-in/expired lines) | utility (pure) | transform | same file `renderer.line` L135-172 | exact |
| `internal/render/testdata/lines.golden` / lifecycle golden | test | n/a | `internal/render/render_test.go` `TestGolden` L88 + `internal/testutil/golden.go` L19-37 | exact |
| `internal/config/config.go` (EXTEND: `[lifecycle]`, `Nd` durations) | config | n/a | same file L84-127, L171-197 | exact |
| `cmd/pr-relay/main.go` (EXTEND: register commands) | route (CLI root) | request-response | same file L19-36 | exact |
| `cmd/pr-relay/{sub,unsub,keep,poll,ls,status,reset,daemon}.go` (NEW) | controller (CLI command) | request-response | `cmd/pr-relay/dev.go` `newDevCmd` L42-78 | exact |
| `cmd/pr-relay/format.go` (NEW: terse text for sub/ls/status/poll) | utility (pure) | transform | `cmd/pr-relay/dev.go` `devPrinter.onCommit` L269-283 | partial |
| `cmd/pr-relay/*_test.go` e2e harness v2 (cold sub, 20x concurrent autostart, version mismatch) | test | e2e process | `cmd/pr-relay/crash_test.go` L25-160 + `devdeliver_test.go` L117-134 | exact |
| `internal/daemon/*_test.go` (lock order, stale socket, restart ×10) | test | integration | `internal/store/store_test.go` `TestStoreLocked` L203 | role-match |

## Pattern Assignments

### `internal/ctl/wire.go` + `internal/ctl/client.go` (IPC client, request-response)

**Analog:** `internal/herdr/client.go`

**Imports** (L3-17):
```go
import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)
```

**Timeout/limit constants** (L19-32): copy the shape. Name ctl's own `dialTimeout`, `callTimeout` and `maxLine` (research: a 1 MiB line cap).
```go
const dialTimeout = 2 * time.Second
const callTimeout = 5 * time.Second
const maxLine = 4 << 20
```

**Injectable dialer for synctest** (L34-45): ctl.Client should take a `DialFunc` the same way, so tests run over net.Pipe.
```go
type DialFunc func(ctx context.Context, socket string) (net.Conn, error)
type Options struct {
	Socket string
	Dial   DialFunc
	Logger *slog.Logger
}
```

**Owner-checked dial (T-03-04)** (L79-93): copy it as-is for the control socket (Research Pattern 1, "Client dial guard").
```go
func DialUnix(ctx context.Context, socket string) (net.Conn, error) {
	fi, err := os.Lstat(socket)
	if err != nil {
		return nil, fmt.Errorf("herdr: %w", err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("herdr: %s: not a unix socket", socket)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) {
		return nil, fmt.Errorf("herdr: %s: not owned by current user", socket)
	}
	d := net.Dialer{Timeout: dialTimeout}
	return d.DialContext(ctx, "unix", socket)
}
```
The autostart `isDown(err)` must recognise ENOENT, which arrives wrapped from `os.Lstat`, as well as ECONNREFUSED from the dial.

**Wire structs** (L95-105). ctl adds `Client Peer` to the request and `Daemon Peer` to the response (Research Pattern 1).
```go
type request struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}
type response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}
```

**Core round trip** (L126-196): one request per connection, a ctx-close AfterFunc, a deadline clamped to the ctx, and a `ReadSlice` loop with a line cap. Copy it verbatim:
```go
conn, err := c.dial(ctx, c.socket)
...
defer conn.Close()
stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
defer stop()
dl := time.Now().Add(timeout)
if cd, ok := ctx.Deadline(); ok && cd.Before(dl) { dl = cd }
_ = conn.SetDeadline(dl)
...
br := bufio.NewReaderSize(conn, 64<<10)
var line []byte
for {
	chunk, err := br.ReadSlice('\n')
	if len(line)+len(chunk) > maxLine {
		return fmt.Errorf("herdr: decode %s: response line exceeds %d bytes", method, maxLine)
	}
	line = append(line, chunk...)
	if err == nil { break }
	if errors.Is(err, bufio.ErrBufferFull) { continue }
	return fail(err)
}
... if resp.Error != nil { return resp.Error }
if resp.ID != id { return fmt.Errorf(... "response id %q, want %q" ...) }
```

**Error type plus code helpers** (`internal/herdr/errors.go` L8-64). ctl.Error should mirror `herdr.Error` (`Code`, `Message`, an `Error()` with a `ctl:` prefix), a `ctl.Code(err)` helper that uses `errors.As`, and an `UnreachableError` that wraps dial failures, so `autostart` can tell "daemon down" from "daemon replied with an error":
```go
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
func Code(err error) string {
	var he *Error
	if errors.As(err, &he) { return he.Code }
	return ""
}
func IsUnreachable(err error) bool { var ue *UnreachableError; return errors.As(err, &ue) }
```

---

### `internal/ctl/server.go` (IPC server, request-response)

**Analog:** `internal/herdr/herdrtest/server.go` (the only NDJSON server in the repo)

**Accept loop** (L356-373):
```go
func (s *Server) accept(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		...
		go s.serve(c)
	}
}
```
For production, track the in-flight conns in a `sync.WaitGroup` so shutdown can drain them (at most 2 s, Research Pattern 2 shutdown order).

**Serve one connection** (L438-477): read one line, decode, reply with `invalid_request` on a bad decode, dispatch, reply, close. ctl must replace `ReadBytes` with the capped `ReadSlice` loop from `herdr/client.go` L163-178, and set a 5 s read deadline before reading.
```go
func (s *Server) serve(c net.Conn) {
	sc := &serverConn{conn: c, br: bufio.NewReader(c), ...}
	line, err := sc.br.ReadBytes('\n')
	if err != nil { s.dropConn(c); return }
	var req wireRequest
	var id string
	if err := json.Unmarshal(line, &req); err != nil || json.Unmarshal(req.ID, &id) != nil {
		s.reply(sc, "", nil, errReply(herdr.CodeInvalidRequest, "invalid request"))
		s.dropConn(c)
		return
	}
	...
	result, werr, send := s.dispatch(sc, req.Method, req.Params)
	if send { s.reply(sc, id, result, werr) }
	s.dropConn(c)
}
```
**Reply** (L506-519):
```go
msg := map[string]any{"id": id}
if werr != nil { msg["error"] = werr } else { msg["result"] = result }
b, err := json.Marshal(msg)
_, _ = sc.conn.Write(append(b, '\n'))
```
ctl should use a typed `Response` struct with `Daemon Peer`, not a map. The version gate goes between decode and dispatch. It exempts `hello` and `shutdown`.

**Test transport:** `herdrtest.Server.Dialer()` (L293-313) serves each connection over `net.Pipe`, which makes it synctest-safe. Copy that idea as `ServeConn(net.Conn)` and use it directly from ctl tests.

---

### `internal/daemon/daemon.go` (host, event-driven)

**Analog:** `cmd/pr-relay/dev.go` `setupDev` (L298-374) and `runDevDeliver` (L98-209)

**Imports** (dev.go L3-28): the same internal packages plus `ctl`, `paths`, `gofrs/flock`, `runtime/debug`.

**Bootstrap: config, logger, store, ghclient, engine, quarantine** (L308-373). Lift this into the daemon nearly verbatim, swapping stderr for the rotating log writer:
```go
cfg, warns, err := config.Load(cfgPath)
for _, w := range warns { fmt.Fprintf(stderr, "pr-relay: config: %s\n", w) }
...
log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
...
st, state, err := store.Open(dir)
if errors.Is(err, store.ErrLocked) {
	return nil, fmt.Errorf("another pr-relay is using %s", dir)
}
...
client, err := ghclient.New(ghclient.Options{
	Endpoint:  os.Getenv(envGraphQLURL),
	Tokens:    &ghclient.TokenSource{},
	UserAgent: "pr-relay/" + version,
	Logger:    log,
})
...
eng, err := poller.New(poller.Options{Saver: st, State: state, Fetcher: client, Config: cfg, Logger: log, OnCommit: onCommit})
...
if qp, at := st.Quarantine(); qp != "" {
	log.Error("state file was unreadable and has been quarantined", "path", qp)
	eng.SetQuarantine(qp, at)
}
```
Note that `envGraphQLURL` (dev.go L33) is a `main` package constant. Either move it into `internal/daemon` or pass the endpoint in through `daemon.Options`.

**Manager construction** (L183-190):
```go
mgr, err := deliver.New(deliver.Options{
	Engine:      env.eng,
	Logger:      env.log,
	QuietPeriod: env.cfg.Delivery.QuietPeriod.Duration,
})
```

**Signal context plus N goroutines, first error stops all** (L109-110, L192-208). Extend it from 2 goroutines to 4: the engine, the manager, `RunLifecycle` and the ctl server.
```go
ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
defer stop()
...
runCtx, cancel := context.WithCancel(ctx)
defer cancel()
errs := make(chan error, 2)
go func() { errs <- env.eng.Run(runCtx) }()
go func() { errs <- mgr.Run(runCtx) }()
var first error
for i := 0; i < 2; i++ {
	if err := <-errs; err != nil && first == nil && !errors.Is(err, context.Canceled) {
		first = err
	}
	cancel()
}
```
**Shutdown ordering differs from dev.go.** dev.go relies on `defer st.Close()`. The daemon must close the listener (which unlinks the socket) **before** `st.Close()` and before the socket-lock `Close()` (Research Pitfall 4). Make the order explicit rather than relying on defers.

**Lock pattern:** copy `internal/store/store.go` L60-80. Create the lock file 0600, then `flock.New(path).TryLock()`, and treat `!ok` as already running:
```go
lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
...
lk := flock.New(lockPath)
ok, err := lk.TryLock()
if err != nil { _ = lk.Close(); return ... }
if !ok { _ = lk.Close(); return nil, nil, ErrLocked }
```
The release order comes from `Store.Close` (store.go L261-274): `Unlock()` then `Close()`.

**Ready pipe and the lock-then-listen sequence:** no analog in the codebase. Use Research "Code Examples" L491-517 directly.

---

### `internal/daemon/handlers.go` (ctl method handlers)

**Analog:** `cmd/pr-relay/dev.go` L140-181 (identity capture, then Subscribe)

**herdr identity capture** (L140-154). Phase 4 uses `PaneGet`, not `AgentGet` (Research Pattern 8; `PaneInfo` at `internal/herdr/types.go` L54-62 carries `Agent`, `AgentSession`, `TerminalID`):
```go
hc, err := herdr.NewClient(herdr.Options{Socket: socket, Logger: env.log})
...
info, err := hc.AgentGet(ctx, pane)
if err != nil {
	if herdr.Code(err) == herdr.CodeAgentNotFound {
		return fmt.Errorf("dev deliver: pane %s has no agent", pane)
	}
	return fmt.Errorf("dev deliver: agent.get %s: %w", pane, err)
}
session := ""
if info.AgentSession != nil { session = info.AgentSession.Value }
```
Map errors as follows: `herdr.CodePaneNotFound` becomes ctl `pane_not_found`, `herdr.IsUnreachable(err)` becomes `herdr_unreachable`, and an empty `info.Agent` becomes a warning in the result (D-10).

**Subscribe request** (L168-175), which now calls `SubscribeVerified(ctx, req)`:
```go
si, err := env.eng.Subscribe(poller.SubRequest{
	PR: ref, HerdrSocket: socket, PaneID: pane,
	AgentKind: info.Agent, AgentSession: session, TerminalID: info.TerminalID,
})
```
**Error class mapping:** reuse `poller.errClass` (engine.go L781-802). Its returned strings (`not_found`, `forbidden`, `unauthorized`, `no_token`, `secondary_limit`, `rate_limited`, `transient`) become the ctl error codes directly. Export it, or return a typed error that carries the class.

**Status assembly:** `Engine.Status()` (status.go L51-84), `Engine.RecentCancellations()` (engine.go L491-495), and the new `Manager.Sockets()`.

---

### `internal/autostart/autostart.go` (spawn and connect)

**Analog:** `spikes/setsid-probe/main.go` L68-98

```go
func spawn(outDir string) error {
	exe, err := os.Executable()
	if err != nil { return fmt.Errorf("resolve executable: %w", err) }
	devNull, err := os.Open(os.DevNull)
	if err != nil { return fmt.Errorf("open %s: %w", os.DevNull, err) }
	defer func() { _ = devNull.Close() }()
	...
	cmd := exec.Command(exe, "child")
	cmd.Stdin = devNull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil { return fmt.Errorf("start child: %w", err) }
	// Do not Wait: the parent exits and the child is reparented.
	return cmd.Process.Release()
}
```
Phase 4 deltas (Research Pattern 2): the argument is `"daemon"`, Stdout and Stderr go to **/dev/null** (never the log file, which avoids a lumberjack race, and never the CLI's stdio, per Pitfall 1), `cmd.Dir = "/"`, `cmd.ExtraFiles = []*os.File{w}` sets up the ready pipe, `PR_RELAY_READY_FD=3` is added to the environment, the parent closes `w` after `Start`, and the CLI reads one line with a 3 s deadline.
The file uses the `//go:build unix` header (main.go L1), because `Setsid` is unix-only.

**Spawn lock:** `flock.New(paths.SpawnLockPath()).TryLockContext(ctx, 25*time.Millisecond)` (Research L313-316). The create-0600 step comes from store.go L61-69.

---

### `internal/paths/paths.go` (EXTEND)

**Analog:** the same file.

**Env override constants** (L25-29). Add `EnvSocket = "PR_RELAY_SOCKET"` and `EnvLogDir = "PR_RELAY_LOG_DIR"`:
```go
const (
	EnvStateDir = "PR_RELAY_STATE_DIR"
	EnvConfig   = "PR_RELAY_CONFIG"
)
```
**Resolver shape** (L35-60): env first, then XDG, then a per-OS default using `runtime.GOOS == "darwin"`, then `filepath.Clean`, `MkdirAll(0o700)` and `Chmod(0o700)`. All errors use the `paths:` prefix:
```go
dir := os.Getenv(EnvStateDir)
if dir == "" {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		dir = filepath.Join(xdg, appName)
	} else {
		home, err := os.UserHomeDir()
		if err != nil { return "", fmt.Errorf("paths: resolve home dir: %w", err) }
		if runtime.GOOS == "darwin" {
			dir = filepath.Join(home, "Library", "Application Support", appName)
		} else {
			dir = filepath.Join(home, ".local", "state", appName)
		}
	}
}
dir = filepath.Clean(dir)
if err := os.MkdirAll(dir, 0o700); err != nil { return "", fmt.Errorf("paths: create state dir: %w", err) }
if err := os.Chmod(dir, 0o700); err != nil { return "", fmt.Errorf("paths: chmod state dir: %w", err) }
```
SocketPath additions: if `PR_RELAY_STATE_DIR` is set and `PR_RELAY_SOCKET` is not, derive the socket inside the state dir (Pitfall 5). Check the length is under 104 bytes, with an `os.TempDir()/pr-relay-<uid>/` fallback. `Lstat` the fallback dir to check the owner and that it is not a symlink. Update the package doc comment (L1-16), whose last line is "Phase 4 adds the socket and log paths here."

**Test pattern** (`paths_test.go` L10-20). Extend the `clearEnv` key list with `PR_RELAY_SOCKET`, `PR_RELAY_LOG_DIR` and `XDG_RUNTIME_DIR`:
```go
func clearEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"PR_RELAY_STATE_DIR", "XDG_STATE_HOME", "PR_RELAY_CONFIG", "XDG_CONFIG_HOME"} {
		t.Setenv(k, "")
	}
	return home
}
```

---

### `internal/herdr/env.go` (NEW, pure)

**Analog:** `internal/paths/paths.go` `ConfigPath` L64-76: an env-first chain that resolves without creating anything. Take `getenv func(string) string` instead of calling `os.Getenv`, so the unit tests never see the developer's real `HERDR_*` (Pitfall 2). Canonicalise with `filepath.Abs` + `filepath.Clean` and no `EvalSymlinks` (Pitfall 6). This matches `herdr.NewClient`, which already cleans the path (client.go L63).

---

### `internal/herdr/endpoint.go` (FIX IN-02 / IN-03)

**Analog:** the same file, `Run` L190-249.

The current code always calls `markUp` and emits `KindReconnected` after every connect, including a pane-set resubscribe:
```go
ep.log.Info("herdr events connected", "socket", socket, "attempt", attempt)
attempt = 0
connectedAt := ep.now()
ep.markUp(connectedAt)
if !ep.emit(ctx, Event{Kind: KindReconnected, At: connectedAt}) { ... }
reason, rerr := ep.read(ctx, stream)
switch reason {
case endCtx:
	return nil
case endResub:
	ep.log.Debug("herdr events resubscribing for new pane set", "socket", socket)
	continue
case endLost:
	...
```
The fix tracks a `resubbing bool` that is set on `endResub`. On the next successful connect it skips `markUp` (which keeps `upSince`) and emits `KindResubscribed` instead. A failed connect still goes through `markDown` (L212). Add the Kind constant beside `KindReconnected` (doc at L57).

---

### `internal/deliver/manager.go` (FIX IN-03, add `Sockets()`, WR-09 guard)

**Analog:** the same file.

**Existing KindReconnected handler** (L403-427). `KindResubscribed` gets a copy of the second loop only (startCheck for worker-less, newly added panes), with no worker poke and no `es.up` log:
```go
case herdr.KindReconnected:
	if !es.up { es.up = true; m.log.Info("herdr reachable", "socket", es.socket) }
	for _, ws := range m.workers {
		if ws.es == es { ws.w.poke() }
	}
	m.sync(ctx)
	for _, id := range m.subscribedPanes(es.socket) {
		ref := poller.PaneRef{HerdrSocket: es.socket, PaneID: id}
		if m.workers[ref] == nil { m.startCheck(ctx, es, ref) }
	}
```
The comment at L420-421 ("KindReconnected also fires on pane-set resubscribes (IN-03, deferred)") must be updated.

**WR-09 guard spot** (L202-206). Add `|| !filepath.IsAbs(s.HerdrSocket)` to the skip:
```go
for _, s := range m.eng.List() {
	if s.HerdrSocket == "" || s.PaneID == "" {
		continue
	}
	sock := filepath.Clean(s.HerdrSocket) // Pitfall 10: one key per path
```
**`Sockets()` snapshot:** `m.endpoints` is owned by the Run goroutine (L156). A status snapshot must either go through a request channel into the Run loop, as with `m.checked`/`m.exited` (L182-193), or be kept in a mutex-guarded copy. `es.ep.Up()`, `UpSince()` and `DownSince()` (endpoint.go L149-170) are already safe to call from outside.

---

### `internal/compose/compose.go` (FIX IN-01)

**Analog:** the same file, L143-160. This is the exact place for the partial-ack cap:
```go
for gi, g := range gs {
	h, partial := firstHeld[gi]
	switch {
	case !partial:
		acks[g.SubID] = g.UpToSeq
	case h.drop || isFirst(items, cut, gi):
		// Nothing of this group made it in; it is delivered next time.
	default:
		acks[g.SubID] = h.seq - 1
	}
}
```
Lower `h.seq - 1` below a superseded partner's Seq only when the pair straddles `cut`. Otherwise keep `h.seq - 1` (Pitfall 9, no-progress fallback). Supersede pairing lives in `internal/compose/supersede.go`.

---

### `internal/poller/engine.go` (EXTEND: verified subscribe, pane unsub, reset, ack lifecycle)

**Analog:** the same file.

**Mutation contract** (L298-317). Every new method goes through it:
```go
func (e *Engine) mutate(fn func(next *store.State) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.mutateLocked(fn)
}
func (e *Engine) mutateLocked(fn func(next *store.State) error) error {
	next := e.state.Clone()
	if err := fn(next); err != nil { return err }
	if err := e.save(next); err != nil { return err }
	e.state = next
	return nil
}
```
**Subscribe body to factor into `subscribeIn(next, req, now)`** (L361-439). Keep the post-mutate SubInfo assembly (L421-430) and the `poke`/`pokePending` calls (L432-437):
```go
info := SubInfo{SubID: id, Created: created}
for _, p := range e.panesLocked(key) {
	if p.HerdrSocket == req.HerdrSocket && p.PaneID == req.PaneID { continue }
	info.CoSubscribers = append(info.CoSubscribers, p)
}
rec := e.state.PRs[key]
info.Interval, info.NextPollAt = e.scheduleLocked(rec, now)
```
`SubscribeVerified` probes **outside** `e.mu`, using the fetcher plus budget the way `PollNow` (L741-759) and `pollRefsLocked` (L823+) do. It then mutates. Re-sub on an `Ending=="expired"` sub must clear `Ending`/`EndSeq`/`CheckinAt` inside the existing-sub branch (L390-400).

**Cancel with the cancellation ring** (L468-487). This is the template for the removal that happens inside `ackSub` when `EndSeq` is covered (reasons `closed` and `expired`; add the constants next to L101-106):
```go
c := Cancellation{SubID: subID, PR: sub.PR, HerdrSocket: sub.HerdrSocket, PaneID: sub.PaneID, Reason: reason}
if err := e.mutateLocked(func(next *store.State) error { return unsubscribeIn(next, subID) }); err != nil { return err }
c.At = e.now()
e.log.Info("subscription cancelled", "sub", c.SubID, "pr", c.PR, "pane", c.PaneID, "socket", c.HerdrSocket, "reason", c.Reason)
e.cancels = append(e.cancels, c)
if over := len(e.cancels) - maxCancellations; over > 0 {
	e.cancels = append([]Cancellation(nil), e.cancels[over:]...)
}
```
Because `AckMany` (L600-614) is a free-function path, `ackSub` needs `now` and must return the subscriptions it ended, so that `AckMany` can append to `e.cancels` after the mutate succeeds. `unsubscribeIn` (L448-461) does the deletion.

**ackSub** (L618-629), to be extended with the D-11 clock reset and the EndSeq removal:
```go
func ackSub(sub *store.Subscription, upToSeq uint64) {
	var kept []store.PendingEvent
	for _, p := range sub.Pending {
		if p.Seq > upToSeq { kept = append(kept, p) }
	}
	sub.Pending = kept
	if sub.DroppedSeq != 0 && sub.DroppedSeq <= upToSeq {
		sub.Dropped, sub.DroppedSeq = 0, 0
	}
}
```
**Keep** (L511-520): it must also clear `CheckinAt`, drop pending check-ins, and return an error on an `Ending` sub.

**UnsubscribePane / Reset:** copy `cancelPane`'s match (manager.go L457-459, `filepath.Clean(s.HerdrSocket)` compare) into one `mutate` that loops `unsubscribeIn`. Iterate `next.Subs`, not `e.state.Subs`.

**Closed ending in commit** (L1086-1101): set `Ending`/`EndSeq` right after `appendPending` when a `closed` event was appended.

---

### `internal/poller/lifecycle.go` (NEW, ticker)

**Analog:** `internal/poller/scheduler.go` `Run` L36-63:
```go
func (e *Engine) Run(ctx context.Context) error {
	if !e.running.CompareAndSwap(false, true) { return errRunning }
	defer e.running.Store(false)
	for ctx.Err() == nil {
		// Round(0) strips the monotonic reading, so comparisons with
		// persisted times are wall-clock (pitfall 11).
		now := e.now().Round(0)
		...
		if !e.sleep(ctx, wait) { return nil }
	}
	return nil
}
```
`RunLifecycle` uses its own running guard, the same `now().Round(0)`, `e.sleep` (scheduler.go L263) with 1 min (and a first-tick grace, Pitfall 13), and `LifecycleTick(now)`. That calls `mutate` only when something is due, then `e.pokePending()` (engine.go L291).

---

### `internal/poller/fanout.go` / `scheduler.go` / `status.go`

**Mask check** (fanout.go L31-41). Add the bypass `if t == model.EventClosed || t.IsLifecycle() { return true }` at the top so that `appendPending` (L72) and `refilter` (L96) both inherit it:
```go
func maskAllows(mask []model.EventType, t model.EventType) bool {
	if len(mask) == 0 { return true }
	for _, m := range mask { if m == t { return true } }
	return false
}
```
**Polling exclusion** (scheduler.go L87-90). Skip `s.Ending != ""`:
```go
subscribed := map[string]bool{}
for _, s := range e.state.Subs {
	subscribed[s.PR] = true
}
```
Apply the same filter in `panesLocked` (engine.go L632), `undeliverableLocked` (L672) and `Status.Subs` (status.go L66).

---

### `internal/store/state.go` + `store.go` (schema v3)

**Version constant** (state.go L42-43): `const SchemaVersion = 2` becomes `3`.

**Field style** (state.go L113-123). Every field has a doc comment and a snake_case json tag with `omitempty` for optional values:
```go
// ClockAt is the check-in/expiry clock. Re-subscribing (D-18) and keep
// (D-14) reset it.
ClockAt time.Time `json:"clock_at"`
...
DroppedSeq uint64 `json:"dropped_seq,omitempty"`
```
New fields (`LastEventAt`, `LastEventType`, `CheckinAt`, `Ending`, `EndSeq`) are value types, so `Clone` (L144-179) needs no changes. If `model.Event` gains `IdleSecs int64`, `cloneEvent` (L181-189) needs nothing either.

**Migration** (store.go L134-156). Add `case 2:` with the same version-key rewrite to `"3"`, and turn `case 1` into a chain (1→2→3) or rewrite it directly to the current version. Update the doc comment:
```go
case 1:
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("store: migrate: v1: %w", err)
	}
	m["version"] = json.RawMessage("2")
	out, err := json.Marshal(m)
	...
```
**Test fixtures:** `internal/store/testdata/state-v1.json` and `state-v2.json` exist. Add a test that loads v2 and migrates it to v3, and extend `TestStoreNewerVersion` (store_test.go L172) to cover v4.

---

### `internal/model/model.go` (ParsePRLink, lifecycle event types)

**Strict parser to wrap** (L43-89). `ParsePRLink` normalises the input and then calls this function. Follow the `bad := func(why string)` error style:
```go
func ParsePRURL(u string) (PRRef, error) {
	bad := func(why string) (PRRef, error) {
		return PRRef{}, fmt.Errorf("invalid pull request URL %q: %s", u, why)
	}
	pu, err := url.Parse(u)
	...
	if pu.Port() != "" || pu.Host != "github.com" {
		return bad("host must be exactly github.com")
	}
```
**EventType constants** (L184-211). Add `EventCheckin`/`EventExpired` in a **separate** const block that is not listed in `AllEventTypes()` (L199-201), so `Rank()` returns 99 for them. Add `func (t EventType) IsLifecycle() bool`. Event fields are grouped under per-type comments (L220-250), so add `// checkin, expired` above `IdleSecs int64 \`json:"idle_secs,omitempty"\``.

**Test style:** `internal/model/model_test.go` uses table tests for ParsePRURL. Extend them with a TestParsePRLink table.

---

### `internal/render/render.go` (lifecycle lines)

**Analog:** the same file, `renderer.line` L135-172. Add cases before `default:`. Lifecycle lines carry the commands as body and have no trailing URL, so they `return` early, but `fits(l)` must still guard them:
```go
case model.EventClosed:
	body, url = "closed: closed", r.pr
	if e.Merged { body = "closed: merged" }
	...
default:
	return "", false
}
l := r.prefix + body + " " + url
if !fits(l) {
	l = r.prefix + head(e) + " … " + r.pr
}
return l, true
```
`r.prefix` is `tag + " " + ref.String() + " "` and `r.pr` is `ref.URL()` (L63). Update `head()` (L121-133) so its `default` case still returns `string(e.Type)` (`checkin`/`expired`), which is acceptable. Golden: `internal/render/testdata/lines.golden` via `TestGolden` (render_test.go L88) and `testutil.Golden` (golden.go L19-37, with `-update` to write).

---

### `internal/config/config.go` (`[lifecycle]` table)

**Analog:** the same file.

**Defaults and bounds constants** (L33-58). Add `DefaultCheckinAfter = 24 * time.Hour`, `DefaultExpireAfter = 7 * 24 * time.Hour`, plus Min/Max bounds.

**Fast-poll floor helper** (L68-82). Copy it as `lifecycleFloor()`:
```go
func quietPeriodFloor() time.Duration {
	if os.Getenv(EnvFastPoll) == "1" { return fastPollFloor }
	return MinQuietPeriod
}
```
**Table struct** (L117-127). Add a `LifecycleConfig` with `CheckinAfter Duration \`toml:"checkin_after"\`` and `ExpireAfter Duration \`toml:"expire_after"\``, add `Lifecycle LifecycleConfig \`toml:"lifecycle"\`` to `Config`, and fill it in `Defaults()` (L130-143).

**Duration parser** (L87-95). Extend it to accept `^\d+d$`. `MarshalText` (L98-100) renders through `Duration.String()`, so "168h0m0s" is fine:
```go
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil { return err }
	d.Duration = v
	return nil
}
```
**Validate** (L171-197). Add `case` arms to the single `switch` with the same message format:
```go
case qp < qpFloor:
	return fmt.Errorf("config: quiet_period: must be at least %s, got %s", qpFloor, qp)
case qp > MaxQuietPeriod:
	return fmt.Errorf("config: quiet_period: must be at most %s, got %s", MaxQuietPeriod, qp)
```
Tests: copy `TestQuietPeriod` (config_test.go L210) and `TestDurationRoundTrip` (L172).

---

### `cmd/pr-relay/main.go` + command files (CLI controllers)

**Root** (main.go L19-36). Add `root.AddCommand(newSubCmd(), newUnsubCmd(), ...)`. Keep `SilenceUsage`/`SilenceErrors`, and keep the main error print (L38-43) unchanged:
```go
root := &cobra.Command{
	Use:           "pr-relay",
	Short:         "Watch GitHub pull requests and push updates into herdr panes",
	SilenceUsage:  true,
	SilenceErrors: true,
}
```
**Command constructor** (dev.go L61-76). Use a `newXCmd() *cobra.Command`, flag vars as closure locals, a `RunE` that delegates to a testable `runX(ctx, stdout, stderr io.Writer, ...)`, and backtick-quoted flag placeholders:
```go
var socket, pane, dcfgPath, dstateDir string
deliverCmd := &cobra.Command{
	Use:   "deliver --socket PATH --pane ID [--config FILE] [--state-dir DIR] <PR-URL>...",
	Short: "Deliver updates for the given pull requests into a scratch herdr pane",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDevDeliver(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args, socket, pane, dcfgPath, dstateDir)
	},
}
deliverCmd.Flags().StringVar(&pane, "pane", "", "herdr pane `ID` to deliver into (for example w1:p1)")
```
Use `cobra.ExactArgs(1)` for `sub`/`unsub`/`keep`/`poll`, and `cobra.NoArgs` (main.go L29) for `ls`/`status`/`reset`. `daemon` has `stop`/`restart` subcommands, in the same nesting style as `dev` → `watch`/`deliver` (dev.go L43-77).

**`daemon` foreground:** `RunE` calls `daemon.Run(cmd.Context(), daemon.Options{Version: version, ...})`. `version` is the package var at main.go L13.

---

### `cmd/pr-relay/format.go` (terse text)

**Analog (partial):** `devPrinter.onCommit` (dev.go L269-283). It formats with `fmt.Fprintf` and conditional suffixes:
```go
line := fmt.Sprintf("  %s new=%d interval=%s next=%s",
	pr.Ref, pr.NewEvents, pr.Interval, pr.NextPollAt.UTC().Format(time.RFC3339))
if pr.Err != "" {
	line += " err=" + pr.Err
}
```
Keep format.go pure (data in, string out, with `now` passed explicitly) so golden tests are deterministic. Golden files go under `cmd/pr-relay/testdata/` via `testutil.Golden`.

---

### `cmd/pr-relay` e2e tests (harness v2)

**Analog:** `crash_test.go` L25-89, plus `devdeliver_test.go` L117-134.

**Binary build in TestMain** (L28-44). Add a second build with `-ldflags "-X main.version=0.0.1-old"` for DMN-07:
```go
binPath = filepath.Join(dir, "pr-relay")
out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput()
```
**Env scrub** (L74-89). Extend the prefix list with `HERDR_` and `XDG_RUNTIME_DIR` (Pitfall 2), then add `PR_RELAY_SOCKET` (a short `os.MkdirTemp("", "prr")` dir, Pitfall 3), `PR_RELAY_LOG_DIR`, `HERDR_SOCKET_PATH` and `HERDR_PANE_ID`:
```go
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
	config.EnvFastPoll+"=1",
)
```
**Fake herdr on a short real socket** (devdeliver_test.go L119-134). Reuse `scratchHerdr(t)` as-is. It already uses `os.MkdirTemp("", "hd")`, which avoids the sun_path limit:
```go
srv := herdrtest.New(t)
sock := srv.ListenUnixAt(filepath.Join(sockDir, "h.sock"))
srv.SetPane(herdrtest.PaneState{ID: "w1:p1", TabID: "t1", WorkspaceID: "w1", Agent: "claude", Session: "s1", Status: herdr.StatusIdle})
```
Fake GitHub: `cmd/pr-relay/fakegh_test.go` (`newFakeGH(t)`, `h.gh.url()`). Cleanup must run `daemon stop` and then kill any leftover PID read from `daemon.pid`.

---

## Shared Patterns

### Copy-on-write state mutation
**Source:** `internal/poller/engine.go` L298-330
**Apply to:** every new Engine method (SubscribeVerified, UnsubscribePane, Reset, LifecycleTick, ackSub changes, Keep changes)
The rule: no write to `e.state` except through `mutate`/`mutateLocked`. Iterate and modify `next`, never `e.state`, inside the closure. Call `poke()` after a new record and `pokePending()` after new pending events, outside the lock (L432-437).

### Error style
**Source:** `internal/herdr/errors.go`, `internal/paths/paths.go`, `internal/config/config.go`
**Apply to:** all new packages
Use a package-prefixed `fmt.Errorf("pkg: context: %w", err)`, sentinel `var ErrX = errors.New("pkg: ...")` (as in `store.ErrLocked`, store.go L23), and typed code-carrying errors with `Code(err)`/`IsX(err)` helpers based on `errors.As`.

### Unix socket ownership check
**Source:** `internal/herdr/client.go` L79-93 (`DialUnix`)
**Apply to:** the ctl client dial, the daemon's stale-socket removal (remove only if `ModeSocket`), and the socket-dir check in `paths.SocketPath` (Lstat, uid, no symlink)

### File and dir modes
**Source:** `internal/paths/paths.go` L53-58, `internal/store/store.go` L54-69
**Apply to:** socket dir, log dir, lock files, pid file, crash file
Use `MkdirAll(0o700)` followed by `Chmod(0o700)`, files at `0o600`, and `Chmod` an existing file after opening it.

### flock usage
**Source:** `internal/store/store.go` L71-80, L261-274
**Apply to:** `<socket>.lock` (daemon), `spawn.lock` (CLI, with `TryLockContext`), and the CLI's wait for `state.lock` during a mismatch restart
Use `TryLock` with `!ok` → sentinel. Release with `Unlock()` then `Close()`.

### Logging
**Source:** `cmd/pr-relay/dev.go` L323; `internal/poller/engine.go` L480-481
**Apply to:** daemon, handlers, lifecycle
Use `slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))` with structured key/value pairs (`"sub", "pr", "pane", "socket", "reason"`). Never log the token or PR text. Packages default to `slog.New(slog.DiscardHandler)` when no Logger is given (herdr/client.go L67-69).

### Injected env and clock for tests
**Source:** `internal/paths/paths_test.go` L10-20 (`t.Setenv`), `internal/config/config.go` L64-82 (`EnvFastPoll` floors), `herdrtest.Server.Dialer()` (net.Pipe for synctest)
**Apply to:** `herdr.ResolveSocket(getenv)`, ctl server tests, lifecycle tick tests, config lifecycle floors

### Golden files
**Source:** `internal/testutil/golden.go` L19-37
**Apply to:** render lifecycle lines, `cmd/pr-relay/format.go` (sub, ls, ls --all, status, poll)

## No Analog Found

| File / Concern | Role | Data Flow | Reason |
|----------------|------|-----------|--------|
| Ready pipe on fd 3 (`PR_RELAY_READY_FD`) in `internal/daemon` + `internal/autostart` | utility | IPC (pipe) | No `ExtraFiles` use exists in the codebase. Use Research "Code Examples: Ready pipe" (L508-517) and Pattern 2 `spawn` (L322-336). |
| `internal/daemon/log.go` rotation (lumberjack or own rotator) plus `debug.SetCrashOutput` | utility | file-I/O | There is no file logging today, and every logger writes to stderr. Use Research Standard Stack/Supporting and "Don't Hand-Roll". lumberjack is a new dependency gated by `checkpoint:human-verify`. |

## Metadata

**Analog search scope:** `cmd/pr-relay/`, `internal/{ctl?,herdr,herdr/herdrtest,deliver,poller,store,model,render,compose,config,paths,testutil}`, `spikes/setsid-probe/`
**Files scanned:** about 30 source files read or grepped (out of 24k LOC of Go)
**Pattern extraction date:** 2026-11-04
