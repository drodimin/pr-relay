# Phase 3: herdr Client & Delivery - Pattern Map

**Mapped:** 2026-11-02
**Files analyzed:** 22 (new and modified, tests included)
**Analogs found:** 20 / 22

**Scope note:** The user decided that a cross-workspace `pane.moved` does NOT re-key a subscription. RESEARCH Pattern 8 (`Engine.Repoint`) is **out of scope** and has no file here. A `pane.moved` event whose `previous_pane_id` is a subscribed pane on that socket is handled as "pane gone": cancel with reason `pane_gone` (D-15). Since a moved pane would also hit `pane_not_found` at push time, the event only cancels it sooner. Keep `pane.moved` in the global subscription list for that reason.

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `internal/herdr/client.go` (new: `Client`, `Call`, dialer, deadlines) | I/O client | request-response | `internal/ghclient/client.go` | role-match (HTTP to NDJSON/unix) |
| `internal/herdr/errors.go` (new: `*Error{Code,Message}`, `UnreachableError`, `Code()`, `IsUnreachable()`) | utility (typed errors) | n/a | `internal/ghclient/errors.go` | exact |
| `internal/herdr/types.go` (new: AgentInfo, PaneInfo, PromptParams, WaitOpts, event/sub constants) | model | transform | `internal/model/model.go` (Memory JSON tags), `ghclient.BatchResult` | role-match |
| `internal/herdr/endpoint.go` (new: supervised events loop, reconnect, `SetPanes`, `Up`/`DownSince`) | service (long-lived loop) | event-driven / streaming | `internal/poller/scheduler.go` (`Run` + `sleep`) | partial (loop shape only) |
| `internal/herdr/doc.go` (new) | doc | n/a | `internal/ghclient/doc.go`, `internal/poller/doc.go` | exact |
| `internal/herdr/herdrtest/server.go` (new: fake herdr, unix listener + `net.Pipe` dialer) | test helper (importable) | request-response + streaming | `internal/poller/fakegh_test.go` | role-match (in-memory fake, synctest-safe) |
| `internal/herdr/*_test.go` | test | - | `internal/ghclient/client_test.go`, `internal/poller/scheduler_test.go` | role-match |
| `internal/compose/compose.go` (new: `Group`, `Message`, `Limits`, flood guard, `Acks`) | pure core | transform | `internal/render/render.go`, `internal/poller/fanout.go` | exact (pure, no clock) |
| `internal/compose/supersede.go` (new: `Supersede(view, []PendingEvent)`) | pure core | transform | `internal/filter/filter.go` | exact |
| `internal/compose/*_test.go` + `testdata/*.json` + `messages.golden` | test | - | `internal/render/render_test.go`, `internal/diff/scenario_test.go` | exact |
| `internal/deliver/manager.go` (new: groups subs by (socket,pane), endpoint per socket, starts workers on `PendingWake`) | service | event-driven | `internal/poller/scheduler.go` + `cmd/pr-relay/dev.go` `devPrinter.drain` | partial |
| `internal/deliver/worker.go` (new: gate, quiet period, identity, prompt, ack, cancel) | service (state machine) | event-driven | `scheduler.go` `sleep` (timer + wake + ctx select), `devPrinter.drain` (pending, deliver, ack) | partial |
| `internal/deliver/*_test.go` (synctest + herdrtest, `TestLiveHerdr` opt-in) | test | - | `internal/poller/scheduler_test.go` (`sched` harness) | role-match |
| `internal/poller/engine.go` (modify: `pendingWake`, `PendingWake`, `PRView`, `AckMany`, `Cancel`, `SetPaneReachability`, `SubRequest.AgentKind`, poke in `commit`) | service | CRUD (copy-on-write) | same file: `Wake`/`poke`, `Ack`, `Unsubscribe`, `Pending` | exact |
| `internal/poller/scheduler.go` (modify `planLocked`: LIFE-08 base override) | service | batch | same file lines 94-104 | exact |
| `internal/poller/status.go` (modify: recent cancellations ring for Phase 4) | model | n/a | same file `SetQuarantine` 83-87 | exact |
| `internal/poller/*_test.go` (`TestPendingWake`, `TestUndeliverableCadence`, AckMany, Cancel) | test | - | `internal/poller/engine_test.go`, `scheduler_test.go` | exact |
| `internal/store/state.go` (modify: `SchemaVersion = 2`, `Subscription.AgentKind`) | model | n/a | same file lines 42-43, 89-120 | exact |
| `internal/store/store.go` (modify `migrate` 1 to 2) | migration | file-I/O | same file lines 134-143 | exact |
| `internal/store/testdata/state-v2.json` + migration tests | test fixture | - | `state_test.go` `TestStateV1Golden` 178-205, `store_test.go` `TestStoreNewerVersion` 171-200 | exact |
| `internal/config/config.go` (modify: `[delivery] quiet_period`) | config | n/a | same file `PollConfig`/`Defaults`/`Validate`/`minIntervalFloor` | exact |
| `internal/render/render.go` (modify: `DroppedLine(ref,n)`, `HeldLine(n)`) | pure core | transform | same file `Lines`/`line`/`fits` | exact |
| `cmd/pr-relay/dev.go` (modify: `dev deliver` subcommand) | CLI command | request-response | same file `newDevCmd`/`runDevWatch` | exact |

## Pattern Assignments

### `internal/herdr/client.go` (I/O client, request-response)

**Analog:** `internal/ghclient/client.go`

**Imports and module path** (lines 1-20). The module is `github.com/drodimin/pr-relay`. Use the stdlib only: `bufio`, `context`, `encoding/json`, `errors`, `fmt`, `log/slog`, `net`, `os`, `sync/atomic`, `syscall`, `time`.

**Named limits as documented consts** (lines 22-33). Copy this style for `dialTimeout = 2*time.Second`, `callTimeout = 5*time.Second`, `waitSlack = 5*time.Second`, `maxLine = 4<<20`:
```go
// maxBody caps a response body (T-02-28). DecodeBatch expects bounded input.
const maxBody = 16 << 20

// defaultHTTPTimeout is the whole-request timeout of the default http.Client.
const defaultHTTPTimeout = 30 * time.Second
```

**Options struct, zero-value defaults and a discarding logger** (lines 40-49 and 80-104). Use the same shape for `herdr.Options{Socket string; Dial func(ctx, socket string) (net.Conn, error); Logger *slog.Logger}`:
```go
func New(o Options) (*Client, error) {
	...
	c := &Client{endpoint: ep, tokens: o.Tokens, http: o.HTTP, ua: o.UserAgent, log: o.Logger}
	if c.http == nil {
		c.http = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	return c, nil
}
```
`checkLoopback` (lines 106-119) is a guard-at-construction analog for the ASVS V4 socket check. Before dialing, `os.Lstat` the path and require `ModeSocket` plus an owner of `os.Getuid()`. Fail closed with an error that never includes secrets.

**Transport-failure classification** (lines 244-282). Every dial, write or read failure becomes a typed transient error, and a ctx error is wrapped separately. Map this to `*UnreachableError` (DLV-08: keep queued):
```go
hr, err := c.http.Do(req)
if err != nil {
	if ctx.Err() != nil {
		return nil, fmt.Errorf("ghclient: request: %w", ctx.Err())
	}
	return nil, &TransientError{Err: err}
}
```
Errors use a `herdr:` prefix, mirroring `ghclient:`.

**Bounded reads** (line 266 `io.LimitReader(hr.Body, maxBody+1)` with an over-limit error at 273-275). For herdr, use `bufio.NewReaderSize(conn, 64<<10).ReadBytes('\n')` with a max-line check (RESEARCH Pattern 1), not `bufio.Scanner`.

**Debug logging per call** (lines 187-188). Copy `c.log.Debug("herdr call", "method", m, "socket", s, "duration", ...)`. Never log prompt text at info level (RESEARCH Security V7).

Core body: RESEARCH Pattern 1 (one dial per `Call`, string ids, close after one response). It has no in-repo analog.

---

### `internal/herdr/errors.go` (typed errors)

**Analog:** `internal/ghclient/errors.go` (whole file, 62 lines)

Sentinel plus struct errors, each with an `Error()` and an `Unwrap` where it wraps:
```go
// TransientError is a failure worth retrying soon: a 5xx, a timeout or
// another transport error. Status is 0 for transport errors.
type TransientError struct {
	Status int
	Err    error
}

func (e *TransientError) Error() string { ... }

func (e *TransientError) Unwrap() error { return e.Err }
```
Apply this shape to:
- `*Error{Code, Message string}`, the decoded herdr error object, with json tags `code` and `message`
- `*UnreachableError{Socket string; Err error}` with `Unwrap`
- `func Code(err error) string`, which uses `errors.As` into `*Error`
- `func IsUnreachable(err error) bool`
- constants `CodeAgentNotFound`, `CodeAgentBlocked`, `CodeAgentNotRunning`, `CodePaneNotFound`, `CodeTabNotFound`, `CodeTimeout`, `CodeEventsLost`, `CodeInvalidRequest`, `CodeAgentPromptStalled`

---

### `internal/herdr/types.go` (wire structs and constants)

**Analog:** `internal/model/model.go` lines 256-277 (`Memory`, snake_case json tags with inline comments)

Declare the wire structs with explicit `json:"snake_case"` tags. Mark nullable fields as pointers (`CompletionSeq *uint64`, `AgentSession *AgentSession`). Unknown fields are ignored by default (Security V5). Keep both event spellings as explicit constant pairs. Do not normalise them (RESEARCH Code Examples, the note after `normalize`):
```go
const (
	EvPaneClosed        = "pane_closed";         EvPaneClosedDot        = "pane.closed"
	EvPaneMoved         = "pane_moved";          EvPaneMovedDot         = "pane.moved"
	EvPaneAgentDetected = "pane_agent_detected"; EvPaneAgentDetectedDot = "pane.agent_detected"
	EvAgentStatusChanged = "pane.agent_status_changed"
	...
)
```

---

### `internal/herdr/endpoint.go` (supervised events loop, event-driven)

**Analog:** `internal/poller/scheduler.go`

**Single-Run guard** (scheduler.go lines 36-40; field `running atomic.Bool` at engine.go 182-183):
```go
func (e *Engine) Run(ctx context.Context) error {
	if !e.running.CompareAndSwap(false, true) {
		return errRunning
	}
	defer e.running.Store(false)
	for ctx.Err() == nil {
```

**Interruptible sleep with timer, wake channel and ctx** (lines 248-266). Use it for the reconnect backoff wait. A `SetPanes` call pokes the wake channel:
```go
func (e *Engine) sleep(ctx context.Context, d time.Duration) bool {
	d = min(max(d, 0), maxSleep)
	if d == 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-e.wake:
	case <-t.C:
	}
	return true
}
```

**Stable jitter helper** (lines 268-272, `jitterFor` with fnv). It is an analog for the ±20% backoff jitter. Randomness has to be injectable or deterministic under synctest.

**Coalescing poke** (engine.go 231-236). Use it for `SetPanes` and the per-pane "status changed" channels (buffer 1):
```go
func (e *Engine) poke() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}
```
Loop body: RESEARCH Pattern 2. It runs ping, `pane.list`, builds the subscription list (globals `pane.closed`, `pane.moved`, `pane.agent_detected`, `pane.exited`, `tab.closed`, `workspace.closed`, plus `pane.agent_status_changed` only for listed panes), subscribes, then reads only. On `events_lost` it reconnects without backoff, and after every (re)subscribe it emits a synthetic `Reconnected`. **No `Repoint` handling.** `pane.moved` is surfaced as an event the manager treats as pane gone.

---

### `internal/herdr/herdrtest/server.go` (fake herdr)

**Analog:** `internal/poller/fakegh_test.go` lines 23-70

Copy the struct layout: one mutex, scripted state, recorded requests, and block/entered channels for race tests. The doc comment states that it is synctest-safe:
```go
// fakeGH is an in-memory GitHub GraphQL endpoint implemented as an
// http.RoundTripper. It never opens a socket, so it also works inside a
// testing/synctest bubble (plan 02-10).
type fakeGH struct {
	t *testing.T

	mu         sync.Mutex
	...
	requests   [][]string                // PR keys per request, alias order
	reqAt      []time.Time // time.Now per request
	...
	// block, when non-nil, makes RoundTrip signal entered and then wait for
	// block to be closed (or the request context to end).
	block   chan struct{}
	entered chan struct{}
}
```
Differences:
- It must be an **importable non-test package** (`herdrtest`), because `internal/deliver` tests use it. Take `testing.TB` in the constructor.
- It provides a `Dialer()` that returns a `net.Pipe` end, for synctest. Without that, real sockets break the bubble (RESEARCH Pitfall 8).
- It provides `ListenUnix()` on `os.MkdirTemp("", "hd")` + `h.sock`, because of the macOS sun_path limit (Pitfall 7).
- It must reproduce every verified quirk in the RESEARCH Wave 0 list.
- Scripted pane state: status, `state_change_seq`, `completion_seq`, session, kind, exists/bare.
- Methods: `Prompts()` recorder, `EmitEvent` (both spellings), `LoseEvents()`, `Stop()`/`Restart()`.

**Test-wide shared fake for real-socket tests:** `cmd/pr-relay/fakegh_test.go` (200 lines) shows the httptest-backed variant used by subprocess tests.

---

### `internal/compose/supersede.go` (pure, transform)

**Analog:** `internal/filter/filter.go` (whole file, 64 lines)

**Package doc enumerating the rules** (lines 1-16). Write the supersede rules 1-8 (RESEARCH Pattern 5) as a bullet list in the package or file doc in this style:
```go
// ExcludeSelf rules (EVT-08, D-12, D-14, D-20):
//   - Logins are compared case-insensitively and only for actors whose Type is
//     "User". ...
//   - closed is always delivered, even when self merged or closed the PR
//     (D-20). ...
package filter
```

**Non-mutating filter returning a new slice** (lines 25-37):
```go
func ExcludeSelf(events []model.Event, self string) []model.Event {
	out := make([]model.Event, 0, len(events))
	for _, e := range events {
		if self != "" && isOwn(e, self) {
			continue
		}
		out = append(out, e)
	}
	return out
}
```
**Switch on event type with a safe default** (lines 43-63). The `default` keeps the event, mirroring "never drop on unknown state" (rule 8).

Input type: `store.PendingEvent` (state.go 122-127). Event types: `model.EventCIFailed`, `EventCISettled`, `EventConflict` (`.Conflicting`), `EventClosed`, `.HeadOID` (see render.go 93-105 for the full set).

---

### `internal/compose/compose.go` (pure, transform)

**Analogs:** `internal/render/render.go` (pure renderer), `internal/poller/fanout.go` (pure, "no clock, no I/O" header)

**File-header purity statement** (fanout.go lines 1-4):
```go
// Fan-out rules. Everything in this file is pure: no clock (now is an
// argument), no I/O, no locking.
```
**Rendering is delegated, never re-implemented.** Call `render.Lines(ref, evs)` (render.go 45-60) after mapping `[]store.PendingEvent` to `[]model.Event`, the same way `cmd/pr-relay/dev.go` lines 92-98 does:
```go
evs := make([]model.Event, len(d.Events))
for i, pe := range d.Events {
	evs[i] = pe.Event
}
for _, l := range render.Lines(s.ref, evs) {
```
**Drop marker plus ack-up-to semantics.** `poller.Drain{Events, Dropped, UpToSeq}` is at engine.go 79-85. `UpToSeq` already covers `DroppedSeq` (engine.go 459-462). The flood guard must ack per sub only up to the highest fully-included Seq. Note that `Ack` clears the drop marker only when `upToSeq >= DroppedSeq` (engine.go 481-483). That matters for partial acks, because the DroppedLine must be counted as included only when it was emitted.

**Exported cap constant style:** `render.MaxLineBytes = 512` (render.go 31-32). Add `compose.DefaultMaxBytes = 12 << 10` with a D-12 comment.

---

### `internal/compose/*_test.go` + `testdata/` (golden and fixtures)

**Analog:** `internal/render/render_test.go` lines 1-60, plus `internal/testutil/golden.go`

Golden usage (testutil/golden.go 16-36). `go test -update` rewrites the files:
```go
testutil.Golden(t, "testdata/messages.golden", got)
```
Fixture table style: `goldenEvents()` in render_test.go 28-60, with helpers `user(l)`/`bot(l)`, `testRef`, and constants `sha1`/`sha0`. The grammar regex `lineGrammar` (render_test.go 15) should be reused, or copied and extended with `note:`, to assert that every composed line matches D-01. Scenario-directory fixtures: `internal/diff/scenario_test.go` 19-60 (`scenarioDirs`, sorted `os.ReadDir`), if supersede fixtures are kept as JSON files under `testdata/`. Hostile corpus: `internal/testutil/hostile.go` and `internal/render/hostile_test.go`.

---

### `internal/render/render.go` (modify: `DroppedLine`, `HeldLine`)

**Analog:** same file

Build the line from `tag`, `ref.String()` and `ref.URL()` exactly like `Lines` (line 51), then pass it through `fits` (lines 146-151):
```go
r := renderer{ref: ref, prefix: tag + " " + ref.String() + " ", pr: ref.URL()}
...
l := r.prefix + body + " " + url
if !fits(l) {
	l = r.prefix + head(e) + " … " + r.pr
}
```
`DroppedLine(ref, n)` returns `[pr-relay] owner/repo#N note: n earlier updates dropped <pr-url>` and uses `plural` (232-237). `HeldLine(n)` has no ref (Assumption A6). It is still `tag + " note: ..."`, and it must never start with `#` (D-07). Add both lines to `testdata/lines.golden`. The current ad-hoc version to replace is `cmd/pr-relay/dev.go` line 101: `"[pr-relay] %s note: %d updates dropped\n"`.

---

### `internal/deliver/manager.go` and `worker.go` (service, event-driven)

**Analogs:** `internal/poller/scheduler.go` (loop and sleep), `cmd/pr-relay/dev.go` lines 76-109 (pending, deliver, ack)

**The existing delivery loop to generalize** (dev.go 78-109). Phase 3 replaces "sink + stdout" with `compose.Message` + `agent.prompt`, and per-sub `Ack` with one `AckMany`:
```go
func (p *devPrinter) drain() {
	for _, s := range p.subs {
		d, err := p.eng.Pending(s.id)
		if err != nil {
			p.log.Error("pending", "pr", s.key, "error", err)
			continue
		}
		...
		if d.UpToSeq > 0 {
			if err := p.eng.Ack(s.id, d.UpToSeq); err != nil {
				p.log.Error("ack failed", "pr", s.key, "error", err)
			}
		}
	}
}
```
**Engine dependency as an interface** (engine.go 22-30: `Fetcher`, `Saver`). Declare a narrow interface in `deliver` (`List`, `Pending`, `PRView`, `AckMany`, `Cancel`, `SetPaneReachability`, `PendingWake`), so tests can fake it and `*poller.Engine` satisfies it.

**Options with injected clock and logger** (engine.go 32-47, defaults at 213-218):
```go
	// Now is the clock. Tests inject a fake one.
	Now func() time.Time
...
	if e.log == nil {
		e.log = slog.Default()
	}
	if e.now == nil {
		e.now = time.Now
	}
```
Add `Dial` (a pass-through to `herdr.Options.Dial`), `QuietPeriod`, `RestoreGrace` and `UndeliverableAfter`.

**Quiet-period wait.** Use the `sleep` select shape (scheduler.go 250-266) with a per-pane status channel in place of `e.wake`. Never `agent.wait` while idle (Pitfall 4).

**PaneRef key.** Reuse `poller.PaneRef{HerdrSocket, PaneID}` (engine.go 59-63) as the map key, with `filepath.Clean` applied to the socket (Pitfall 10).

**Cancellation logging (D-17).** Copy the slog key/value style, e.g. `e.log.Warn("github auth failed", "state", state, "retry_in", authRetry)` (scheduler.go ~244). Log `"subscription cancelled", "sub", id, "pr", key, "pane", p, "socket", s, "reason", reason`. The reasons are `pane_gone` (which covers a cross-workspace `pane.moved`), `identity_mismatch` and `pane_closed`.

---

### `internal/deliver/*_test.go` (synctest)

**Analog:** `internal/poller/scheduler_test.go` lines 1-130

Copy the harness shape exactly: an options struct, `newX(t, opt)`, `start()` running `Run` in a goroutine and then `synctest.Wait()`, `stop()` that cancels and checks for a nil return, and `advance(d)` = `time.Sleep(d); synctest.Wait()`:
```go
func (s *sched) start() {
	s.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan error, 1)
	go func() { s.done <- s.e.Run(ctx) }()
	synctest.Wait()
}

// advance moves fake time forward by d and lets every goroutine settle.
func (s *sched) advance(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

func TestRunStopsOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSched(t, schedOpt{})
		...
```
Silent logger: `slog.New(slog.DiscardHandler)` (scheduler_test.go 56). For a real Engine behind the manager, use `memSaver` (engine_test.go 46-70). It is in `poller`'s test files and cannot be imported, so copy a minimal one into `deliver`, or keep it behind the narrow interface with a fake engine. The call-count assertion analog is `s.f.requestCount()` (scheduler_test.go TestRunIdleNoRequests). Use it as the Pitfall 4 "fewer than N herdr calls per quiet window" check.

**Opt-in live test.** The env-gated live test analog is `internal/ghclient/live_test.go` (52 lines) and `internal/ghquery/batch_live_test.go`. Gate on `PR_RELAY_HERDR_SCRATCH_SOCKET`, and refuse unless the path contains `/sessions/` and is not `~/.config/herdr/herdr.sock`. That mirrors `spikes/lib.sh` `guard_socket` (lines 20-31):
```sh
PRSPIKE_SOCKET="$HOME/.config/herdr/sessions/prspike/herdr.sock"
DEFAULT_SOCKET="$HOME/.config/herdr/herdr.sock"
guard_socket() {
	case "$HERDR_SOCKET_PATH" in
	*/sessions/prspike/*) ;;
```

---

### `internal/poller/engine.go` (modify: Engine hooks)

**Analog:** same file

**`PendingWake` channel.** Copy the `wake` field, its construction and the getter/poke (lines 173, 209, 227-236) as `pendingWake chan struct{}` (cap 1), `PendingWake()` and `pokePending()`. **Do not reuse `Wake()`**, because `scheduler.go:sleep` (line 260) consumes it. Poke it in `commit` after a successful save (lines 917-921) when `appendPending(...) > 0` or `gapLost` set a marker (lines 893-903). Track that with a local bool and poke after `e.mu` is released (`commit` holds `defer e.mu.Unlock()`, so poke at the end; it is non-blocking, so poking under the lock is also safe). Also poke in `Subscribe`, on the existing-sub refilter path (lines 329-336).

**`AckMany(map[string]uint64)`.** Copy `Ack` (lines 466-486) into one `mutate` loop over the map. Skip unknown subs with `continue`, not `return ErrUnknownSub`:
```go
func (e *Engine) Ack(subID string, upToSeq uint64) error {
	return e.mutate(func(next *store.State) error {
		sub := next.Subs[subID]
		if sub == nil {
			return ErrUnknownSub
		}
		var kept []store.PendingEvent
		for _, p := range sub.Pending {
			if p.Seq > upToSeq {
				kept = append(kept, p)
			}
		}
		sub.Pending = kept
		if sub.DroppedSeq != 0 && sub.DroppedSeq <= upToSeq {
			sub.Dropped, sub.DroppedSeq = 0, 0
		}
		return nil
	})
}
```

**`Cancel(subID, reason)`.** Wrap `Unsubscribe` (lines 374-390), which already removes the PR record with its last sub. Before mutating, capture the sub's PR, pane and socket under `e.mu` (as `Pending` does at 452-458) for the D-17 log. Then append to an in-memory ring guarded by `mu`.

**`PRView(key)`.** A read-under-lock copy, like `Pending` (lines 452-464) or `Schedule` (515-519). Copy `HeadOID`, `Mergeable`, `State` and `Baselined` from `e.state.PRs[key].Memory` (model.go 256-261).

**`SetPaneReachability(PaneRef, since time.Time)`.** Copy the in-memory setter style `SetQuarantine` (status.go 83-87):
```go
func (e *Engine) SetQuarantine(path string, at time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.quarPath, e.quarAt = path, at
}
```
Add a `reach map[PaneRef]time.Time` field next to `quarPath`/`quarAt` (engine.go 177-181, "guarded by mu"). When a pane goes from unreachable to receivable, call `e.poke()` so the scheduler re-plans.

**`SubRequest.AgentKind`.** Add it to `SubRequest` (lines 50-57). Set it in both Subscribe branches (lines 329-350), alongside `AgentSession`/`TerminalID`.

**Undeliverable helper.** Add `undeliverableLocked(key, now) bool`. It iterates `panesLocked(key)` (lines 488-503) and returns true only if every pane has a non-zero `since` with `now.Sub(since) >= UndeliverableAfter`.

---

### `internal/poller/scheduler.go` (modify `planLocked`) and `engine.go` `scheduleLocked`

**Analog:** same files

Override the base interval at the two places it is computed.

planLocked (scheduler.go lines 94-104):
```go
	for k, rec := range e.state.PRs {
		if rec.Terminal || !subscribed[k] {
			continue
		}
		var quiet time.Duration
		if !rec.LastPollAt.IsZero() {
			quiet = rec.LastPollAt.Sub(rec.RateAnchor)
		}
		base := BaseInterval(quiet, minI, maxI)
		// + if e.undeliverableLocked(k, now) { base = maxI }   // LIFE-08, D-06
```
In the same function, the due time comes from `NextDue(rec.LastPollAt, rec.RateAnchor, ...)` (line 121), which recomputes the base internally. It therefore needs the same override. Either pass the overridden base through a new helper, or compute `next = rec.LastPollAt.Add(Effective(base, ...))` when undeliverable. scheduleLocked (engine.go 521-534) has the same two call sites. Keep `cadence.go` pure and unchanged, or add a pure `NextDueBase(lastPoll, base, ...)` there, with a test in `cadence_test.go`.

---

### `internal/store/state.go` and `store.go` (schema v2)

**Analog:** same files

- state.go line 43: `const SchemaVersion = 1` becomes `2`.
- state.go 98-102: add `AgentKind string \`json:"agent_kind,omitempty"\`` beside `AgentSession`, with the doc-comment style `// AgentKind is the herdr agent kind (e.g. "codex") captured at subscribe time; D-13 fallback identity.`
- `Clone` (140-175) needs no change, because the new field is a plain string copied by `sc := *sub`.
- store.go `migrate` (134-143) is the hook:
```go
func migrate(version int, raw []byte) ([]byte, error) {
	switch version {
	case SchemaVersion:
		return raw, nil
	default:
		return nil, fmt.Errorf("store: migrate: no migration from version %d", version)
	}
}
```
Add `case 1:`, which rewrites the version field to 2 (unmarshal to `map[string]json.RawMessage`, set `"version"`, re-marshal), and update the doc comment ("version 1 is current").
- Tests: `TestStateV1Golden` (state_test.go 178-205) becomes `TestStateV2Golden`, writing `testdata/state-v2.json` with `AgentKind` populated in `fullState()`. Keep `state-v1.json` as the migration input: Open a dir containing it, then assert version 2 and DeepEqual on the subs. `TestStoreNewerVersion` (store_test.go 171-200) hard-codes `"version": 2` as "newer", so bump it to 3. Its final line writes `{"version": 1}` and still opens through migration.

---

### `internal/config/config.go` (modify: `[delivery]`)

**Analog:** same file

- Defaults constant block (lines 30-38): add `DefaultQuietPeriod = 10 * time.Second // D-09 typing-collision quiet period`.
- Table struct (lines 82-95 style):
```go
// DeliveryConfig is the [delivery] table.
type DeliveryConfig struct {
	QuietPeriod Duration `toml:"quiet_period"`
}
```
- `Config` (98-101): add `Delivery DeliveryConfig \`toml:"delivery"\``. `Defaults()` (104-116): fill it.
- `Validate` (144-164): add a case to the switch with the floor pattern from `minIntervalFloor` (56-62), so `EnvFastPoll` lowers the floor to 1 ms:
```go
	case p.MinInterval.Duration < floor:
		return fmt.Errorf("config: min_interval: must be at least %s, got %s", floor, p.MinInterval.Duration)
```
Use the range [1s, 5m] (RESEARCH Open Question 4). Update the package doc bullet list (lines 4-16) with a D-09 entry. Add tests in `config_test.go` following its existing cases.

---

### `cmd/pr-relay/dev.go` (modify: `dev deliver`)

**Analog:** same file

**Subcommand registration** (lines 38-57). Add `deliver` with `--socket`, `--pane`, `--config` and `--sink` beside `watch`:
```go
	watch := &cobra.Command{
		Use:   "watch [--sink FILE] [--config FILE] <PR-URL>...",
		Short: "Run the store and poller in the foreground for the given pull requests",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDevWatch(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args, sinkPath, cfgPath)
		},
	}
	watch.Flags().StringVar(&sinkPath, "sink", "", "append delivered events as JSON lines to `FILE` (crash testing)")
```
**Bootstrap sequence to reuse** (`runDevWatch` lines 133-241). It parses URLs, loads config and prints warnings, builds the slog text handler, runs `store.Open` with the `ErrLocked` message, builds `ghclient.New` with `envGraphQLURL`, then `poller.New`, then `SetQuarantine`, then Subscribe, then `signal.NotifyContext`. Factor the shared part into a helper, or copy it. Differences:
- Refuse the socket unless it contains `/sessions/` and is not the default socket (same guard as the live test).
- Call `agent.get` once through `herdr.Client` to fill `AgentSession`/`AgentKind` in `poller.SubRequest` (replacing `devSocket`/`devPane`).
- Run `eng.Run` and `deliver.Manager.Run` concurrently: an errgroup-less `go` + error channel, cancel on first error.

Subprocess test analog: `cmd/pr-relay/devwatch_test.go` (63 lines, uses `newHarness` from `crash_test.go` 57-160).

## Shared Patterns

### Copy-on-write mutation
**Source:** `internal/poller/engine.go` lines 238-270 (`mutate`, `mutateLocked`, `save` with `ErrNotDurable`)
**Apply to:** every new durable Engine hook (`AckMany`, `Cancel`). Never mutate `e.state` in place, except the documented in-memory-only fields (reachability, the cancellation ring).
```go
func (e *Engine) mutateLocked(fn func(next *store.State) error) error {
	next := e.state.Clone()
	if err := fn(next); err != nil {
		return err
	}
	if err := e.save(next); err != nil {
		return err
	}
	e.state = next
	return nil
}
```

### Injected clock and logger
**Source:** `internal/poller/engine.go` lines 32-47 and 213-218. `scheduler.go` line 44 uses `e.now().Round(0)` for wall-clock comparisons.
**Apply to:** `herdr.Endpoint`, `deliver.Manager`, `deliver` worker. Pure packages (`compose`, `render`) take no clock at all.

### Typed errors with errors.As / errors.Is
**Source:** `internal/ghclient/errors.go` (whole file), consumed in `engine.go` `errClass` (line 591) and `save` (`errors.Is(err, store.ErrNotDurable)`, line 265).
**Apply to:** `herdr.Code(err)`, `herdr.IsUnreachable(err)`, and `errors.Is(err, poller.ErrUnknownSub)` in deliver (engine.go 121-122).

### Structured slog logging
**Source:** `scheduler.go` ~240-246 and `ghclient/client.go` 187-188 (key/value pairs, Debug for per-call detail, Info/Warn for state changes).
**Apply to:** D-17 cancellation logs, gate transitions (Debug), reconnects (Info/Warn). Never log prompt text above Debug.

### Coalescing non-blocking signal channel
**Source:** `engine.go` 227-236 (`wake` cap 1 + `select default`)
**Apply to:** `PendingWake`, per-pane status-change channels, `Endpoint.SetPanes` resubscribe trigger.

### Pure-package discipline and golden tests
**Source:** `internal/poller/fanout.go` header (1-4), `internal/filter/filter.go`, `internal/testutil/golden.go`
**Apply to:** `internal/compose`, the new `render` helpers.

### synctest bubble harness
**Source:** `internal/poller/scheduler_test.go` 1-130
**Apply to:** all timing tests in `herdr` (backoff), `deliver` (quiet period, restore grace, 30 min threshold) and `poller` (`TestUndeliverableCadence`). Inside bubbles, use the `herdrtest` `net.Pipe` dialer only.

## No Analog Found

| File | Role | Data Flow | Reason |
|------|------|-----------|--------|
| `internal/herdr/client.go` NDJSON framing core | I/O client | request-response | No unix-socket or NDJSON code exists in the repo yet. Use RESEARCH Pattern 1 for the body. Only the surrounding structure comes from ghclient. |
| `internal/deliver/worker.go` gate state machine | service | event-driven | No per-entity goroutine or state machine exists. Use RESEARCH Pattern 3 for the logic. Only the timer/select and pending/ack pieces have analogs. |

## Metadata

**Analog search scope:** `internal/{poller,store,render,filter,diff,ghclient,ghquery,config,paths,testutil,model}`, `cmd/pr-relay`, `spikes/lib.sh`
**Files scanned:** 77 Go files (listed), 14 read in detail
**Pattern extraction date:** 2026-11-02
