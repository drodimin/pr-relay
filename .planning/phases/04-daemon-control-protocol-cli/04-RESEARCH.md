# Phase 4: Daemon, Control Protocol & CLI - Research

**Researched:** 2026-11-04
**Domain:** Go per-user daemon lifecycle (singleton, self re-exec, unix-socket NDJSON control protocol), cobra CLI, subscription lifecycle on top of the existing poller/delivery engine
**Confidence:** HIGH for codebase seams and stdlib/flock mechanics (read from source in this session), MEDIUM for the recommended engine changes (design reasoning against the code), LOW only where flagged

## Summary

Phases 1-3 already built almost everything this phase needs, and most of it is behind clean seams. `poller.Engine` owns the state and every mutation (`mutate` does clone, save, then swap). It already exposes `Subscribe` (returning co-subscribers, interval and next poll), `Unsubscribe`, `Cancel` with a recent-cancellations ring, `Keep`, `ResetRate`, `PollNow`, `List`, `Schedule` and `Status`. `deliver.Manager.Run` is the delivery loop. `store.Open` already takes an exclusive `gofrs/flock` on `state.lock` for the life of the process. `cmd/pr-relay/dev.go::setupDev` + `runDevDeliver` is a working blueprint for wiring the store, ghclient, engine and manager together. The phase has five parts: (1) a new control-protocol package; (2) a `daemon` package that hosts the engine, the manager, a lifecycle ticker and the control server; (3) a CLI auto-start path (spawn lock, Setsid re-exec, ready pipe); (4) the cobra commands with terse text formatting; and (5) a set of targeted engine changes for the lifecycle rules (D-08, D-11, D-14, D-16).

The engine changes carry the real design risk, and the planner should treat them as their own plan(s). The current `Subscribe` persists before any fetch, which violates D-08 ("an unverified subscription is never created"). It needs a verified variant that probes GitHub first and uses the probe snapshot as the silent baseline. Removal after delivery (`closed`, `expired`) fits the existing outbox best as an `Ending`/`EndSeq` marker on the subscription that `ackSub` acts on. An ack that covers `EndSeq` deletes the subscription, so crash safety (EVT-09) and the existing worker and compose path cover lifecycle messages without any new delivery mechanism (D-05). Check-in and expiry become synthetic `model.Event` types. They are not in `AllEventTypes()`, so `--events` cannot select them, and they bypass the mask the same way `closed` must (D-16).

The daemon mechanics are standard, but they have sharp edges specific to this project. The spawned daemon must never inherit the CLI's stdout or stderr, because the agent's tool runner waits for pipe EOF and would hang. Tests run inside a real herdr pane, so they must strip `HERDR_*` from subprocess environments. macOS `t.TempDir()` paths can overflow the 104-byte `sun_path` limit. On graceful shutdown the listener must be closed (which unlinks the socket) *before* the state lock is released. Otherwise the old daemon can delete the new daemon's socket.

**Primary recommendation:** Build four packages: `internal/ctl` (NDJSON wire types, client and server, one request per connection, version gate on every request), `internal/daemon` (host, socket lock, ready pipe, logging), `internal/autostart` (spawn lock, Setsid re-exec, restart on version mismatch) and the commands in `cmd/pr-relay`. Put all lifecycle rules inside `poller.Engine` (schema v3, additive migration). Keep the herdr worker unchanged, except for the IN-01/02/03 fixes.

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions

#### Carried forward (already locked, do not re-ask)
- **D-01:** Daemon startup: the CLI re-execs itself as `pr-relay daemon` with `SysProcAttr{Setsid: true}` and redirects stdio, with a `gofrs/flock` singleton lock and a stale-socket check. Never double-fork. Spike S5 proved a setsid child survives `herdr session stop`. IPC is a unix socket speaking newline-delimited JSON (`{id,method,params}` → `{id,result|error}`). The socket is mode 0600 inside a 0700 directory, and the path must fit within the macOS 104-byte sun_path limit (see CLAUDE.md §IPC). Socket and log paths go into `internal/paths`, as its doc comment already anticipates.
- **D-02:** `sub` output reports the other panes subscribed to the PR, the current interval and the next poll time (P2 D-16). Re-subscribing updates the subscription in place, replaces the mask, resets the clocks and keeps the pending events (P2 D-18).
- **D-03:** `keep` resets only the subscription's check-in clock (`ClockAt`). It does not change the poll rate (P2 D-14, `Engine.Keep`).
- **D-04:** Cancellations caused by identity mismatch, a missing pane or a closed pane are only logged, and no message goes to the pane (P3 D-17). The only place they surface is `status`.
- **D-05:** Lifecycle messages use the locked line format `[pr-relay] owner/repo#N <event>: ...` (P1 D-01), are never longer than the flood guard allows, and go through the Phase 3 idle-gated delivery path. No new delivery mechanism is added.
- **D-06:** Only github.com is accepted (DMN-08). Validate with `model.ParsePRURL`.

#### sub UX & validation
- **D-07:** A successful `sub` prints **terse text of 1–3 lines** with no JSON. For example:
  ```
  subscribed o/r#12 events=all
  polling every 1m (next in 58s); also watched by: w2:p1
  updates arrive in this pane; unsub: pr-relay unsub https://github.com/o/r/pull/12
  ```
  Leave the co-subscriber clause out when there are none.
- **D-08:** **Validation (SUB-07) makes a live GitHub lookup with a bounded wait of about 5 s.** `not_found` or `forbidden` fails with a clear error. A timeout, rate limit, secondary limit or network error **also fails**: it says why and tells the agent to retry later. **An unverified subscription is never created.** The whole `sub` call, including any daemon auto-start, must still finish within "a few seconds" (SUB-01).
- **D-09:** **The force-poll command is `pr-relay poll <PR link>`.** It is a separate command and the realisation of P2 D-15. It polls now, resets the PR's shared rate to 60 s, and prints a one-line result. Any resulting events arrive in the pane through normal delivery and are not printed by `poll`. It is not throttled, but it counts toward the budget cap (P2 D-15).
- **D-10:** **When `sub` runs in a pane where herdr reports no agent, or can't identify one, it subscribes anyway and prints a warning:** updates are delivered only while an agent runs in the pane. Identity follows the P3 D-13 and D-14 rules after that. Outside herdr with no `--pane`, `sub` still fails clearly (SUB-03).

#### Check-in & expiry (LIFE-06, LIFE-07)
- **D-11:** **The inactivity clock is per subscription.** Delivering an event to *that* subscription resets it. That means an event that passed its `--events` mask and was acked. `keep` and re-subscribing also reset it. Activity that the mask filters out does **not** count, so a `--events ci` subscriber on a PR with only comments goes inactive. Check-in and expiry messages themselves **never** reset the clock.
- **D-12:** **Check-in wording** is a single line that puts the commands first:
  `[pr-relay] o/r#12 check-in: no activity for 24h. still needed? keep: pr-relay keep <url> | drop: pr-relay unsub <url>`
  **Expiry wording:**
  `[pr-relay] o/r#12 expired: no activity for 7d, subscription removed. re-subscribe: pr-relay sub <url>`
  The duration shown follows the configured thresholds, and golden tests cover it.
- **D-13:** **The check-in repeats every 24 h** (every check-in interval) until `keep`, `unsub`, an activity reset or expiry. ⚠ **This overrides LIFE-06's "one check-in message".** The user chose the repeat on purpose. Update the REQUIREMENTS.md wording during planning or verification.
- **D-14:** **At 7 d of inactivity, the subscription stops counting for polling straight away.** If it was the PR's last subscriber, the PR stops being polled. The "expired" message is queued and delivered once the pane can receive, and **then** the subscription record is deleted. The existing P3 rules still apply: if the pane is gone, the subscription and its pending updates are dropped silently (D-15 / LIFE-04).
- **D-15:** The check-in and expiry thresholds are set in `config.toml` (LIFE-07), with defaults of 24h and 7d, bounds validated in the style of the existing `config.Validate`.

#### Merged/closed cleanup (LIFE-01)
- **D-16:** **`closed` is always delivered, even when the subscription's `--events` mask excludes it,** because it ends the subscription and the agent needs to know why. The subscription is removed after the `closed` message is delivered and acked. A reopened PR is **not** tracked, so the agent has to re-subscribe. (P1 D-20 still applies: `closed` is delivered even when the gh user merged the PR themselves.)

#### Daemon lifecycle (DMN-01, DMN-07)
- **D-17:** **A version mismatch triggers a transparent auto-restart.** The CLI asks the old daemon to shut down gracefully and flush state, waits for it to exit, spawns the new binary and retries the original command. It prints a single stderr note, such as `restarted daemon (0.3.0 -> 0.4.0)`. Subscriptions survive because state persists (LIFE-03). It restarts whichever side is newer; the user did not ask for any downgrade guard.
- **D-18:** **The daemon never exits on its own.** It runs until it is stopped or receives a signal, and it keeps running even with zero subscriptions or herdr absent. Idle cost is close to zero because nothing is polled.
- **D-19:** **The daemon commands are `pr-relay daemon` (runs in the foreground; brew services, systemd and the Phase 5 plugin hook use this form), `pr-relay daemon stop` and `pr-relay daemon restart`.** Stopping is graceful: flush state and remove the socket. Mismatch handling (D-17) reuses the same shutdown path. On a cold machine, 20 concurrent CLI calls must start exactly one daemon (SC-2).
- **D-20:** **The hidden `dev watch` and `dev deliver` commands stay in the shipped binary.** The crash and live tests depend on them, and `dev deliver` keeps its scratch-socket guard.

#### ls / status / unsub / reset
- **D-21:** **`ls` inside a herdr pane lists only that pane's subscriptions.** `ls --all` lists every subscription on the machine with a PANE column, and outside herdr `ls` shows all of them automatically. Each row shows the PR, events, current poll interval, last activity (with the event type), when the next check-in is due and when the subscription expires (SUB-06).
- **D-22:** **v1 has no `--json` on any command.** Output is text only.
- **D-23:** **`status` shows** daemon health, the subscription and PR counts and the rate-limit budget (DMN-05), and in addition:
  - **recent cancellations** with their reason (`pane_closed`, `pane_gone`, `identity_mismatch`, `expired`, `closed`)
  - **herdr connectivity per socket** (up or down, since when, pending count)
  - **the version, PID, uptime and the socket, state and log paths**

  The poller's existing `Status` fields (auth state, quarantined state file, per-PR errors) also appear. They are already gathered, so this costs nothing.
- **D-24:** **`unsub <PR link>` is idempotent.** If the calling pane has no matching subscription, it prints `not subscribed` and exits 0. `unsub --all` covers the **calling pane only** (or the `--pane` given). Outside herdr with no `--pane`, it errors the same way `sub` does.
- **D-25:** **`pr-relay reset` drops every subscription on the machine** and its pending updates. The daemon keeps running. It prints how many subscriptions it removed. *Claude's choice, to revisit if the user objects:* it asks for confirmation when stdin is a TTY and requires `--yes` otherwise, so an agent can't wipe other agents' subscriptions by accident.

#### Phase 3 hardening (folded in)
- **D-26:** **Fix IN-01, IN-02 and IN-03** from `.planning/phases/03-herdr-client-delivery/deferred-items.md` in this phase:
  - IN-01: cap the flood-guard partial ack so that a superseded pair is never split.
  - IN-02: resubscribe after RestoreGrace, or on `pane_agent_detected`, for panes that were missing at connect.
  - IN-03: a pane-set resubscribe must not reset `UpSince` or poke every worker.

  IN-03 matters most, because every `sub` now triggers a resubscribe.

### Claude's Discretion
- The `--events` flag syntax: comma-separated names from the `model.EventType` set, plus any group aliases such as `ci` → `ci.failed,ci.settled`. Unknown names are rejected clearly.
- Which URL forms are accepted: `/pull/N`, a trailing `/files`, `/commits` or `#fragment`, and `http` versus `https`. Everything else is rejected according to SUB-09.
- The control-protocol method names and schema, and how the version handshake works (for example a `hello` method on every connection).
- How herdr identity (agent kind and session, P3 D-13) is captured at `sub` time: the CLI asks herdr directly, or the daemon does it with the socket and pane the CLI passes.
- Log location and rotation (DMN-06): use the per-user state or log directory, slog plus lumberjack, or rotating size files of our own. Keep it small.
- The auto-start retry and backoff timings, and the stale-socket cleanup.
- Exactly how `ls` lays out its columns and formats times.
- How expiry, check-in and closed removal map onto the existing outbox. For example, synthetic `model.Event` kinds rendered by `render` and acked through `Engine.Ack`, so that crash safety (EVT-09) covers them as well.

### Deferred Ideas (OUT OF SCOPE)
- `--json` output for `ls` and `status` was offered and declined for v1 (D-22). Revisit if the Phase 5 plugin or scripts need it.
- Documentation follow-ups: change LIFE-06's wording to the repeating check-in (D-13). P2's deferred note still needs LIFE-05/LIFE-06 updated to the 16-min ceiling. Add a requirement for `poll` (a SUB-10, P2 D-15) and one for `reset`.
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| SUB-01 | `sub <link>` returns within a few seconds and never blocks waiting for events | Time budget in Pattern 3 (spawn ≤3 s ready, herdr ≤1.5 s, GitHub probe ≤5 s, hard CLI deadline about 10 s). The ready pipe avoids blind polling. Never inherit CLI stdio (Pitfall 1). |
| SUB-02 | `--events <type,...>`, default all | `StringSliceVar` plus the alias table (Pattern 7). Validate locally in the CLI and again in `normalizeMask`. `closed` is always added (D-16). |
| SUB-03 | Detect pane and session from `HERDR_PANE_ID`/`HERDR_SOCKET_PATH`, `--pane` override, clear error outside herdr | New `herdr.ResolveSocket(getenv)` using herdr's documented order. The CLI canonicalises the socket path (Pitfall 6). |
| SUB-05 | `unsub <link>`, `unsub --all` (calling pane) | Daemon computes `store.SubID(canonicalSocket, pane, PRKey)`. New `Engine.UnsubscribePane`. Idempotent "not subscribed" (D-24). |
| SUB-06 | `ls` shows events, polling rate, last activity, expiry | New per-sub fields `LastEventAt`/`LastEventType`/`CheckinAt` (schema v3) plus `Engine.Schedule`. Formatting in `cmd/pr-relay/format.go` with golden tests. |
| SUB-07 | Validate PR exists and is accessible, fail fast | New `Engine.SubscribeVerified(ctx, req)`: probe through `Fetcher.Batch` with a 5 s ctx and use the snapshot as the baseline (Pattern 4). Errors mapped through `errClass`. |
| SUB-09 | Reject non-PR links | New lenient `model.ParsePRLink` that normalises and then calls strict `ParsePRURL`, with distinct messages for repo, issue and host (Pattern 7). |
| DMN-01 | Exactly one daemon, auto-start, concurrent CLIs never start two | Two locks: CLI `spawn.lock` (`TryLockContext`) plus daemon `<socket>.lock` and the existing `state.lock` (Pattern 2). Test with 20 concurrent `status` calls. |
| DMN-05 | `status`: health, sub count, rate-limit budget | `Engine.Status()` + `RecentCancellations()` + new `Manager.Sockets()` snapshot + daemon info (version, pid, uptime, paths). |
| DMN-06 | Rotating logs in a per-user location | slog TextHandler → lumberjack v2.2.1 (5 MB × 3). macOS `~/Library/Logs/pr-relay/`, Linux `$XDG_STATE_HOME/pr-relay/log/`. `debug.SetCrashOutput` for panics. |
| DMN-07 | Detect version mismatch, restart or clear error | The version gate runs daemon-side on every request. `hello`/`shutdown` are frozen schemas. The CLI restart loop runs at most once. ErrNewerVersion on downgrade surfaces through the ready pipe (Pitfall 8). |
| DMN-08 | github.com only | `ParsePRLink` rejects other hosts with the message "only github.com is supported". |
| LIFE-01 | Remove sub after merged/closed is delivered | `appendPending` bypasses the mask for `closed`, sets `Ending="closed"`, `EndSeq`. `ackSub` covering `EndSeq` deletes the sub and records a cancellation (Pattern 5). |
| LIFE-06 | Check-in after 24h inactivity (repeats, D-13), `keep` resets | `Engine.LifecycleTick(now)` queues `EventCheckin`. `Keep` resets `ClockAt`. Acked real events reset `ClockAt` (D-11). |
| LIFE-07 | Expire at 7d with a final message, both configurable | `EventExpired` + `Ending="expired"`. The sub is excluded from polling at once (D-14). Config `[lifecycle] checkin_after/expire_after` (Pattern 6). |
</phase_requirements>

## Project Constraints (from CLAUDE.md)

- Go, single static binary, `CGO_ENABLED=0`. macOS and Linux both first-class.
- Auth reuses `gh` through go-gh `auth.TokenForHost`. Do not shell out to `gh api`, and do not require the `gh` binary at runtime.
- Low idle memory and CPU. Request volume stays independent of subscriber count, with backoff on a low budget.
- Agent-agnostic: nothing specific to Claude Code.
- CLI: spf13/cobra v1.10.2. Logging: stdlib `log/slog`. Log rotation: lumberjack (if logging to a file). Singleton: gofrs/flock v0.13.1.
- IPC: unix socket in `$XDG_RUNTIME_DIR/pr-relay/` (Linux) or a short path on macOS, mode 0600, directory 0700, within the 104-byte `sun_path` limit with an `os.TempDir()/pr-relay-<uid>/` fallback. NDJSON `{id,method,params}` → `{id,result|error}`. **Do not use** gRPC, net/rpc or HTTP-over-socket.
- Daemonization: **never double-fork**. Re-exec with `Setsid: true`, stdio to the log file or `/dev/null`. flock lock for the daemon's lifetime plus a stale-socket check. Do not use kardianos/service or go-daemon.
- Graceful shutdown: `signal.NotifyContext` (SIGINT/SIGTERM), flush state, remove the socket.
- Testing: stdlib `testing`, `httptest` fake GitHub, fake herdr server over a temp unix socket, injectable clock / `testing/synctest`, golden files for message formatting. **Never send prompts to real panes in CI.**
- GSD workflow enforcement: edits go through GSD commands. This applies to the executor, not to research.

## Architectural Responsibility Map

The tiers here are processes and packages, not web tiers.

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| URL parsing, `--events` parsing, herdr env detection (SUB-03/09, DMN-08) | CLI process (`cmd/pr-relay`, `internal/model`, `internal/herdr` env helper) | Daemon re-validates | Fail before any daemon start or socket dial. These are pure functions. |
| Auto-start, spawn lock, version-mismatch restart (DMN-01/07) | CLI process (`internal/autostart`) | Daemon (lock + ready pipe) | Only the CLI knows a daemon is needed. Only the daemon can prove it owns the singleton. |
| Control protocol framing and version gate | `internal/ctl` (shared by both processes) | — | One package for the wire types keeps client and server in lockstep. |
| herdr identity capture at sub time (D-10, P3 D-13) | Daemon (`pane.get` through the herdr client) | — | The daemon already owns the herdr client and `DialUnix` ownership guard. The CLI stays thin, and one place maps herdr errors. |
| GitHub validation probe and baseline (SUB-07, SUB-08) | Daemon → `poller.Engine` | `ghclient` | Budget accounting, viewer, errClass and the copy-on-write state all live in the engine. |
| Lifecycle (check-in, expiry, closed removal) | `poller.Engine` (state mutations, schema v3) | `render` (lines), daemon (ticker goroutine) | Every state mutation must go through `Engine.mutate`. Crash safety comes from the outbox. |
| Delivery of lifecycle lines | `deliver` worker plus `compose` (unchanged path) | — | D-05: no new delivery mechanism. |
| Output formatting (`sub`, `ls`, `status`) | CLI (`cmd/pr-relay/format.go`) | — | The daemon returns structured results and the CLI renders text (D-22: text only, so formatting is a CLI concern). |
| Logs | Daemon (`internal/daemon` + `internal/paths`) | — | Only the daemon logs to a file. The CLI writes errors to stderr. |

## Standard Stack

### Core (all already in go.mod unless marked)
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| Go | go1.27.3 installed; `go 1.26.7` directive | Language | [VERIFIED: `go version` locally, go.mod] |
| github.com/spf13/cobra | v1.10.2 (2025-12-03) | Commands: `sub`, `unsub`, `keep`, `poll`, `ls`, `status`, `reset`, `daemon [stop\|restart]`, `version`, hidden `dev` | [VERIFIED: proxy.golang.org @latest, go.mod] |
| github.com/gofrs/flock | v0.13.1 (2026-08-27) | `spawn.lock` (CLI), `<socket>.lock` (daemon), existing `state.lock` | [VERIFIED: proxy.golang.org; module source read: `TryLock`, `TryLockContext(ctx, retryDelay)`, `Unlock`, `Close`] |
| github.com/BurntSushi/toml | v1.6.0 | `[lifecycle]` config table | [VERIFIED: go.mod] |
| github.com/cli/go-gh/v2 | v2.17.0 | Token (already wired through `ghclient.TokenSource`) | [VERIFIED: go.mod] |
| stdlib `net`, `encoding/json`, `bufio` | — | Control protocol over a unix socket | [CITED: CLAUDE.md §IPC] |
| stdlib `os/exec` + `syscall.SysProcAttr{Setsid:true}` | — | Daemon re-exec; `ExtraFiles[0]` becomes fd 3 in the child (ready pipe) | [VERIFIED: spikes/setsid-probe works; ExtraFiles fd numbering is CITED from pkg.go.dev/os/exec] |
| stdlib `runtime/debug.SetCrashOutput` | Go ≥1.23 | Capture daemon panics into a crash file while stdio is /dev/null | [VERIFIED: `go doc runtime/debug.SetCrashOutput`] |
| stdlib `testing/synctest` | Go ≥1.25 | Lifecycle tick, ctl server timeouts and autostart backoff tests (with net.Pipe, no real network inside a bubble) | [VERIFIED: `go doc testing/synctest`] |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| gopkg.in/natefinch/lumberjack.v2 | v2.2.1 (2023-02-06) | Size-based log rotation for `daemon.log` | Recommended by CLAUDE.md. Zero deps. New files are created with mode 0600 (source read: `mode := os.FileMode(0600)` in `openNew`). Pre-create the log dir 0700 yourself. **NEW dependency, gate behind checkpoint (slopcheck unavailable).** |
| golang.org/x/sys/unix | v0.49.0 (already indirect) | Optional: peer-credential check (`GetsockoptXucred` darwin, `GetsockoptUcred` linux) | Only if the planner adopts defense-in-depth beyond the 0700/0600 perms. Verified the functions exist in the module cache. |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| lumberjack | Own size rotator (about 60 lines: on Write, if size > N then rename `.1`..`.3` and reopen) | Zero new deps, and full control of the 0600 mode. Use it if the user rejects the new dependency at the checkpoint. |
| One request per connection | A persistent multiplexed connection | Every CLI call makes exactly one request. Multiplexing adds id-matching for no gain. herdr itself is used one request per connection here (`herdr.Client` dials per call). |
| Separate `hello` round-trip | Version fields embedded in every request and response | Embedding saves a round-trip and gates *before* execution. Keep `hello` only as a cheap, frozen probe for `daemon stop`/`status --ping`. |
| golang.org/x/term for TTY detection (`reset`) | `os.Stdin.Stat()` and `Mode()&os.ModeCharDevice != 0` | The stdlib check is enough. No new dependency. |

**Installation (only new module):**
```bash
go get gopkg.in/natefinch/lumberjack.v2@v2.2.1
```

## Package Legitimacy Audit

slopcheck could not be installed in this session (the tool install was denied by the environment). Per protocol, the new package is tagged `[ASSUMED]` and the planner must gate it.

| Package | Registry | Age | Downloads | Source Repo | slopcheck | Disposition |
|---------|----------|-----|-----------|-------------|-----------|-------------|
| gopkg.in/natefinch/lumberjack.v2 | Go module proxy | v2.2.1 tagged 2023-02-06; project since 2014 | widely used (not measured) | github.com/natefinch/lumberjack (source fetched, `lumberjack.go` read) | unavailable | `[ASSUMED]`. Planner adds `checkpoint:human-verify` before `go get`, with the own-rotator fallback. |

Already-present modules (cobra, flock, toml, go-gh, x/sys) are not new installs. **Packages removed:** none. **Packages flagged [SUS]:** none.

## Architecture Patterns

### System Architecture Diagram

```
 agent pane (env: HERDR_PANE_ID, HERDR_SOCKET_PATH)
   │  pr-relay sub <link> [--events] [--pane]
   ▼
 CLI process ──────────────────────────────────────────────────────────────
   1. ParsePRLink (SUB-09/DMN-08)  2. events parse  3. herdr.ResolveSocket/pane (SUB-03)
   │      └── any failure → stderr + exit 1 (no daemon start)
   ▼
 autostart.Connect(ctx)
   dial <socket> ──ok──────────────────────────────────────────┐
     │ ENOENT/ECONNREFUSED                                      │
     ▼                                                          │
   flock spawn.lock (TryLockContext ≤3s) → re-dial ─ok─────────┤
     │ still down                                               │
     ▼                                                          │
   exec self "daemon" {Setsid, Dir:/, stdio:/dev/null, fd3=ready pipe}
     │ read fd3: "ok" | "err <msg>" | EOF(child died)           │
     ▼                                                          │
   dial ────────────────────────────────────────────────────────┤
                                                                ▼
                     ctl request {id, method, params, client:{version,protocol}}
                                                                │
 Daemon process (pr-relay daemon) ───────────────────────────────▼──────────
   ctl.Server ─► version gate ─mismatch─► error version_mismatch ─► CLI: shutdown old,
     │                                                              wait state.lock, respawn,
     │ ok                                                           retry once, stderr note
     ▼
   handler "sub": herdr pane.get(socket,pane) ─► identity / pane_not_found / warn(no agent)
                  Engine.SubscribeVerified(ctx 5s) ─► existing OPEN baselined record? join
                                                    └► probe Fetcher.Batch([ref]) ─► errClass
                                                       ─► create record with probe baseline
                  ◄─ SubInfo (co-subs, interval, next) ─► CLI formats D-07 lines
   other handlers: unsub, keep, poll(PollNow), ls(List+Schedule), status, reset, shutdown

   Engine.Run (scheduler) ──poll──► GitHub GraphQL ──► commit: pending events
        │                                           (closed: mask bypass + Ending/EndSeq)
   Lifecycle ticker (1/min) ─► Engine.LifecycleTick ─► check-in / expired events (+Ending)
        │                                   │
        └──────── PendingWake ──────────────┘
                     ▼
   deliver.Manager ─► worker per pane ─► compose ─► herdr agent.prompt ─► AckMany
                                                          │
                     ackSub: real event acked → ClockAt=now (D-11)
                             EndSeq covered  → delete sub (+cancellation ring)
   logs: slog → lumberjack daemon.log ; panics → debug.SetCrashOutput(crash.log)
```

### Recommended Project Structure
```
internal/
├── ctl/            # NEW: wire types, method param/result structs, error codes, Client, Server (pure net.Conn based)
├── daemon/         # NEW: Run(ctx, Options): locks, socket, ready pipe, logging, hosts engine+manager+lifecycle+ctl handlers
├── autostart/      # NEW: Connect(ctx) / Restart / Stop: spawn.lock, Setsid re-exec (unix build files), mismatch restart
├── paths/          # EXTEND: SocketPath(), LogDir(), SpawnLockPath(); env PR_RELAY_SOCKET, PR_RELAY_LOG_DIR
├── herdr/          # EXTEND: ResolveSocket(getenv), PaneFromEnv; IN-02/IN-03 endpoint fixes (KindResubscribed)
├── poller/         # EXTEND: SubscribeVerified, LifecycleTick, Ending/EndSeq handling, UnsubscribePane, Reset, LifecycleStatus
├── store/          # EXTEND: SchemaVersion 3, new Subscription fields, migrate v2→v3 (additive)
├── model/          # EXTEND: ParsePRLink (lenient), EventCheckin/EventExpired (+IsLifecycle), event field for idle duration
├── render/         # EXTEND: check-in / expired lines (D-12) + golden
├── compose/        # FIX: IN-01 partial-ack cap
├── deliver/        # FIX: IN-03 handling of KindResubscribed; add Manager.Sockets() snapshot for status
└── config/         # EXTEND: [lifecycle] checkin_after / expire_after, Validate bounds, optional "Nd" durations
cmd/pr-relay/
├── main.go         # root + version + command registration
├── sub.go unsub.go keep.go poll.go ls.go status.go reset.go daemon.go
├── format.go       # terse text for sub/ls/status/poll (golden tests in testdata/)
└── dev.go          # unchanged (D-20); consider the WR-09 fix
```

### Pattern 1: Control protocol (`internal/ctl`)
**What:** One NDJSON request line per connection and one response line, then close. Every request carries the client identity, and every response carries the daemon identity.
```go
// Source: design mirroring internal/herdr/client.go roundTrip (read this session)
const Protocol = 1 // bump on any incompatible schema change

type Peer struct {
    Version  string `json:"version"`  // main.version
    Protocol int    `json:"protocol"`
    PID      int    `json:"pid,omitempty"` // daemon only
}
type Request struct {
    ID     string          `json:"id"`
    Method string          `json:"method"`
    Params json.RawMessage `json:"params,omitempty"`
    Client Peer            `json:"client"`
}
type Response struct {
    ID     string          `json:"id"`
    Result json.RawMessage `json:"result,omitempty"`
    Error  *Error          `json:"error,omitempty"`
    Daemon Peer            `json:"daemon"`
}
type Error struct{ Code, Message string } // codes: bad_request, version_mismatch,
// not_found, forbidden, no_token, unauthorized, rate_limited, unavailable,
// pr_closed, pane_not_found, herdr_unreachable, not_watched, internal
```
- **Version gate (server):** if `req.Client.Version != self.Version || req.Client.Protocol != Protocol`, and the method is not in `{hello, shutdown}`, reply `version_mismatch` **without executing**. `hello` and `shutdown` have frozen schemas forever, so any CLI can stop any daemon.
- **Limits:** read deadline 5 s for the request line. Cap the line at 1 MiB (copy the `ReadSlice` loop from `herdr.Client.roundTrip`). Each method gets a context deadline (`sub` 8 s, `poll` 15 s, others 3 s). Make the server take a `net.Listener` and expose `ServeConn(net.Conn)`, so unit tests use `net.Pipe` inside `synctest` (synctest forbids real network).
- **Client dial guard:** before dialing, `Lstat` the socket and require `ModeSocket` and owner == current uid, the same check as `herdr.DialUnix` (T-03-04).

### Pattern 2: Singleton and auto-start (`internal/autostart`, `internal/daemon`)
**Daemon startup order (strict):**
1. Resolve paths. Create the socket dir 0700 and verify, with `Lstat`, that it is owned by the uid and is not a symlink. This matters for the `/tmp` fallback on Linux.
2. `flock.New(socket+".lock").TryLock()`. If it fails, report "already running" (exit 0 for foreground `daemon`, and write `err already running` to the ready fd).
3. `store.Open(stateDir)` takes the existing `state.lock`. `ErrLocked` → "state in use by another pr-relay (dev watch?)". `ErrNewerVersion` → clear downgrade error.
4. If the socket path exists: `Lstat`. If it is a socket, `os.Remove` it (the lock proves no live owner). If it is anything else, refuse.
5. `net.Listen("unix", sock)`, then `os.Chmod(sock, 0600)`. `UnixListener` unlinks on Close by default.
6. Write an informational `daemon.pid`. Start the engine, the manager, the lifecycle ticker and the ctl server.
7. Write `ok\n` to fd 3 (if `PR_RELAY_READY_FD` is set) and close it.

**Shutdown order (strict):** stop accepting → drain in-flight requests (≤2 s) → cancel ctx → wait for the engine, manager and lifecycle goroutines → **close the listener (unlinks the socket)** → `store.Close()` (releases `state.lock`) → release the socket lock → exit. Do not release a lock before the socket is gone (Pitfall 4).

**CLI connect:**
```go
// Source: design over gofrs/flock v0.13.1 API (read from module cache)
func Connect(ctx context.Context) (*ctl.Client, error) {
    if c, err := dial(ctx, sock, 300*time.Millisecond); err == nil { return c, nil } else if !isDown(err) { return nil, err } // isDown: ENOENT, ECONNREFUSED
    lk := flock.New(paths.SpawnLock())
    lctx, cancel := context.WithTimeout(ctx, 4*time.Second); defer cancel()
    if ok, err := lk.TryLockContext(lctx, 25*time.Millisecond); err != nil || !ok { return nil, fmt.Errorf("timed out waiting for another pr-relay to start the daemon") }
    defer lk.Close() // Close unlocks
    if c, err := dial(ctx, sock, 300*time.Millisecond); err == nil { return c, nil }
    if err := spawn(ctx); err != nil { return nil, err } // waits on the ready pipe ≤3s
    return dial(ctx, sock, time.Second)
}

func spawn(ctx context.Context) error {
    exe, _ := os.Executable()
    r, w, _ := os.Pipe()
    devnull, _ := os.OpenFile(os.DevNull, os.O_RDWR, 0)
    cmd := exec.Command(exe, "daemon")
    cmd.Dir = "/"                                  // never pin the agent's worktree
    cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull // NEVER the CLI's stdio (Pitfall 1)
    cmd.ExtraFiles = []*os.File{w}                 // fd 3 in the child
    cmd.Env = append(os.Environ(), "PR_RELAY_READY_FD=3")
    cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
    if err := cmd.Start(); err != nil { return err }
    _ = w.Close(); _ = devnull.Close()             // the parent keeps only the read end
    // read one line from r with a 3s deadline: "ok" → nil; "err <msg>" → error(msg); EOF → "daemon exited at startup; see <log>"
    ... ; return cmd.Process.Release()
}
```
- Go opens files with O_CLOEXEC, so the CLI's `spawn.lock` fd is **not** inherited by the daemon [ASSUMED from Go runtime behaviour, HIGH confidence]. Do not pass it in ExtraFiles.
- Concurrency proof for SC-2: `spawn.lock` serialises spawners, and the daemon's own socket lock makes any extra daemon (for example one started at the same moment by the plugin or brew services) exit harmlessly.

### Pattern 3: Version mismatch restart (D-17) and the `sub` time budget
- When the CLI gets `version_mismatch`: (a) send `shutdown` (frozen); (b) under `spawn.lock`, wait until `flock(state.lock).TryLockContext(≤5 s)` succeeds, which proves the old process fully exited, then unlock immediately; (c) spawn; (d) retry the original request **once**; (e) print `restarted daemon (OLD -> NEW)` to stderr. A second mismatch is reported as an error, never as a loop.
- **Budget for `sub` (SUB-01 "a few seconds"):** dial 0.3 s, spawn plus ready ≤3 s (typically under 200 ms), herdr `pane.get` ≤1.5 s, GitHub probe ≤5 s (D-08), hard CLI deadline about 10 s. The cold path is about 1 s plus GitHub latency.

### Pattern 4: Verified subscribe (D-08, SUB-07, SUB-08)
`Engine.Subscribe` today persists **before** any fetch (engine.go doc comment), so D-08 needs a new entry point:
```go
// SubscribeVerified (new): never persists an unverified subscription.
func (e *Engine) SubscribeVerified(ctx context.Context, req SubRequest) (SubInfo, error) {
    key := store.PRKey(req.PR)
    e.mu.Lock(); rec := e.state.PRs[key]; e.mu.Unlock()
    // 1. Already watched, baselined, OPEN and error-free: the same token already proved access → join (no GitHub call).
    // 2. Watched, baselined but State != OPEN → ErrPRClosed ("already merged/closed; nothing to watch").
    // 3. Otherwise probe: budget.Gate(now, forced=true) (fail fast on GitHub limits, never wait),
    //    fetch.Batch(ctx /*5s*/, []PRRef{req.PR}, ghquery.NormalWindow), budget.Record/Observe,
    //    item.Err → errClass (not_found / forbidden / ...), Snapshot.State != "OPEN" → ErrPRClosed.
    // 4. mutate: if the record is still absent, create it with Memory = diff.Step(model.Memory{}, snap).Memory,
    //    LastPollAt = now, RateAnchor = now (so "next in ~58s", D-07); set Viewer if empty; then the
    //    normal subscription upsert (factor Subscribe's body into subscribeIn(next, req, now)).
}
```
- Using the probe snapshot **as** the baseline avoids a second fetch. It also avoids the window between the probe and a later baseline in which events could be silently swallowed (SUB-08).
- Concurrency: two panes subscribing to a new PR at once both probe. The second mutate sees the record and joins without overwriting Memory. A record that exists but is not baselined (legacy dev state) gets probed for validation, then joined, and the scheduler baselines it as it does today.
- Rejecting merged or closed PRs at `sub` time replaces the WR-04 "BaselineClosed" signal for new subscriptions. Keep `BaselineClosed` in the poll path for legacy records.

### Pattern 5: Lifecycle in the outbox (LIFE-01/06/07, D-11/13/14/16)
**Schema v3 additive fields on `store.Subscription`** (migration identical in shape to the existing v1→v2 hook: rewrite the version key only):
```go
LastEventAt   time.Time       `json:"last_event_at,omitempty"`   // last acked real event (ls "last=")
LastEventType model.EventType `json:"last_event_type,omitempty"`
CheckinAt     time.Time       `json:"checkin_at,omitempty"`      // last check-in queued
Ending        string          `json:"ending,omitempty"`          // "", "closed", "expired"
EndSeq        uint64          `json:"end_seq,omitempty"`         // Seq of the ending event
```
**New event types** in `model` (not in `AllEventTypes()`, so `--events` cannot select them, `normalizeMask` rejects them, and `Rank()` returns 99): `EventCheckin = "checkin"`, `EventExpired = "expired"`, plus `func (t EventType) IsLifecycle() bool`. Carry the threshold in a new field such as `IdleSecs int64 \`json:"idle_secs,omitempty"\`` so the line shows the configured duration (D-12).

**Rules (all inside `Engine`, via `mutate`):**
| Rule | Where | Behaviour |
|------|-------|-----------|
| Mask bypass | `maskAllows` / `appendPending` / `refilter` | `closed` and lifecycle types are always allowed (D-16). |
| Closed ending | `commit` | When a sub gets a `closed` pending event: `Ending="closed"`, `EndSeq=seq`. |
| Excluded from polling | `planLocked` `subscribed` map, `panesLocked`, `undeliverableLocked`, `Status.Subs` | Subs with `Ending != ""` do not count (D-14). A PR whose only subs are Ending is not polled, so a reopen is not tracked (D-16). |
| Clock reset on delivery | `ackSub(sub, upTo, now)` | If any acked pending event is a non-lifecycle type: `ClockAt = now`, `LastEventAt/Type = latest acked`. Lifecycle events never reset it (D-11). |
| Removal after delivery | `ackSub` / `AckMany` | `EndSeq != 0 && EndSeq <= upTo` → `unsubscribeIn` + append Cancellation{Reason: "closed"/"expired"} (D-23). |
| Check-in | `LifecycleTick(now)` | `idle = now - ClockAt`. If `idle >= checkin` and `now - max(ClockAt, CheckinAt) >= checkin` → **replace** any pending check-in with a new one, `CheckinAt = now` (D-13 repeat; never stack). |
| Expiry | `LifecycleTick(now)` | If `idle >= expire`, drop pending check-ins, append `EventExpired`, set `Ending="expired"`, `EndSeq` (D-14). |
| keep / re-sub | `Keep`, `subscribeIn` | Reset `ClockAt`, clear `CheckinAt`, drop pending check-ins. Re-sub on `Ending=="expired"` revives it: clear Ending/EndSeq and drop the pending expired event. `keep` on an Ending sub → error "expired; re-subscribe". |
| Pane gone | existing `Cancel` | Unchanged: drops pending, including lifecycle events, silently (D-14 / LIFE-04). |

The daemon runs `RunLifecycle(ctx)`: a 1-minute ticker that compares wall-clock time (`Round(0)`, as the scheduler does for laptop sleep) and calls `LifecycleTick`, which mutates only when something is due and then calls `pokePending`.

### Pattern 6: Config `[lifecycle]`
```toml
[lifecycle]
checkin_after = "24h"
expire_after  = "168h"   # or "7d" if the Duration parser gains a day suffix
```
Bounds (recommendation): `checkin_after` in [1h, 30d] and `expire_after` in (`checkin_after`, 90d]. `EnvFastPoll=1` lowers the floors to 1 ms for e2e tests, the same mechanism as `minIntervalFloor`. Go's `time.ParseDuration` does **not** accept `d` [VERIFIED: stdlib behaviour, units are ns..h], so users will write "7d" and get an error. Recommend extending `config.Duration.UnmarshalText` to accept `^\d+d$`.

### Pattern 7: Link and events parsing (discretion, recommended)
- `model.ParsePRLink(s)`: trim whitespace; add `https://` if there is no scheme; `http`→`https`; host lower-cased and must be `github.com` or `www.github.com` (otherwise "only github.com is supported (got HOST)"); drop query and fragment; path segments `owner/repo/pull/N` followed by optional `files|commits|checks` and anything after them. Then build the canonical URL and pass it through the **strict** `ParsePRURL` (D-06). Use distinct errors for `/issues/N` ("is an issue, not a pull request"), `/owner/repo` ("is a repository; pr-relay watches single pull requests"), and `owner/repo#N` shorthand (reject; not a link).
- `--events`: `cmd.Flags().StringSliceVar` (accepts both `a,b` and repeated flags). Names are exactly the `model.EventType` values. Aliases: `ci` → `ci.failed,ci.settled`; `all` → empty mask. Lower-case, dedupe, reject unknown names with the valid list. Print the mask in `sub` output and note that `closed` is always included.

### Pattern 8: herdr identity at sub time (discretion, recommended: daemon-side)
CLI: `socket := herdr.ResolveSocket(os.Getenv)` using herdr's documented order: `HERDR_SOCKET_PATH`, then `HERDR_SESSION` → `~/.config/herdr/sessions/<name>/herdr.sock`, then `~/.config/herdr/herdr.sock` [CITED: herdr.dev/tui/docs/socket-api]. Make it absolute and `filepath.Clean` it. `pane := --pane or HERDR_PANE_ID`. If there is no pane → error "not inside a herdr pane (HERDR_PANE_ID unset); pass --pane". The daemon calls **one** `pane.get` (`PaneInfo` carries `Agent`, `AgentSession`, `TerminalID`): `pane_not_found` → error; reachable with no agent → subscribe and warn (D-10); unreachable → error "herdr not reachable at SOCKET" (see Open Question 2).

### Anti-Patterns to Avoid
- **Inheriting CLI stdio in the daemon:** the agent's shell tool hangs until the daemon exits (Pitfall 1).
- **`Subscribe` then `PollNow` then `Unsubscribe` on failure:** this briefly persists an unverified sub, triggers a herdr resubscribe and can survive a crash. It violates D-08.
- **Lifecycle state outside the engine** (for example a daemon-side map of check-ins): this breaks the copy-on-write invariant and crash safety.
- **Adding `checkin`/`expired` to `AllEventTypes()`:** this makes them selectable and maskable through `--events`.
- **A pid file as the liveness check:** pids are reused. The flock is the truth, and the pid file is informational only.
- **Checking the version client-side only:** by the time the CLI sees the daemon version, an old daemon has already executed the request.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Daemon singleton | pid files, `kill -0` probing | `gofrs/flock` on `<socket>.lock` + the existing `state.lock` | The kernel releases the lock on death, so there are no stale locks. |
| Waiting for another CLI's spawn | sleep loops | `flock.TryLockContext(ctx, 25ms)` | Already in v0.13.1. Bounded and ctx-aware. |
| Daemonizing | double fork, go-daemon | `exec.Command(self,"daemon")` + `Setsid` (spike S5) | Go cannot fork safely. Already proven. |
| Readiness detection | polling dial in a loop | Ready pipe on fd 3 (`ExtraFiles`) | Gives precise startup errors (ErrLocked, ErrNewerVersion, listen failure) instead of a timeout. |
| Panic capture with stdio on /dev/null | redirecting stderr to the rotating log | `debug.SetCrashOutput(f, debug.CrashOptions{})` | lumberjack rotation would race a raw fd write. |
| Log rotation | ad-hoc truncation | lumberjack (or a small rotator if the dependency is rejected) | Size limits, backups, mode 0600. |
| NDJSON framing | a new reader | Copy the `ReadSlice` loop and line cap from `herdr.Client.roundTrip` | Already hardened (max line, deadlines, ctx close). |
| Delivering lifecycle messages | any direct `agent.prompt` from the lifecycle code | pending events in the outbox, picked up by the existing worker | D-05. The idle gate, identity checks and acks come for free. |
| PR URL validation | a new regex | `model.ParsePRURL` (strict), behind a normaliser | D-06. Already rejects traversal, ports and userinfo. |

**Key insight:** Nearly every hard problem in this phase (durability, delivery gating, rate-limit backoff, identity) is already solved behind `Engine` and `Manager`. The phase's job is to *route* to them, not to re-implement them. Any new state must go through `Engine.mutate`.

## Common Pitfalls

### Pitfall 1: Spawned daemon holds the agent's stdout pipe open
**What goes wrong:** `pr-relay sub` prints its lines and exits, but the agent's tool call never returns.
**Why it happens:** Agent harnesses (Claude Code Bash tool, Codex exec) read the command's stdout/stderr until EOF. A child that inherited those fds keeps the pipe open for its whole lifetime.
**How to avoid:** Give the spawned daemon `/dev/null` for stdin, stdout and stderr, and only the ready pipe on fd 3. The CLI closes its copy of the write end right after `Start`.
**Warning signs:** An e2e test that runs `sub` through `exec.Command(...).Output()` hangs. Make that a regression test: run `sub` with `cmd.Output()` and a 10 s timeout on a cold machine.

### Pitfall 2: Tests run inside the developer's real herdr pane
**What goes wrong:** An e2e `sub` subscribes the developer's real pane (`HERDR_PANE_ID=w1:p3`, real `HERDR_SOCKET_PATH`), and the test daemon types into it. [VERIFIED: this session's env has `HERDR_ENV=1`, `HERDR_PANE_ID=w1:p3`, `HERDR_SOCKET_PATH=~/.config/herdr/herdr.sock`]
**How to avoid:** The new harness `env()` must strip `HERDR_*`, `XDG_RUNTIME_DIR` and `PR_RELAY_*`, then set `PR_RELAY_STATE_DIR`, `PR_RELAY_SOCKET`, `PR_RELAY_LOG_DIR`, `HERDR_SOCKET_PATH` (to the fake herdr's scratch socket) and `HERDR_PANE_ID`. The existing `crash_test.go` `env()` strips only `GH_`, `GITHUB_` and `PR_RELAY_`. Unit tests for env detection take an injected `getenv`.

### Pitfall 3: sun_path overflow on macOS test paths
**What goes wrong:** `listen unix ...: bind: invalid argument`. macOS `t.TempDir()` lives under `/var/folders/.../T/TestName.../001/`, which together with a socket name can exceed 104 bytes.
**How to avoid:** Use `os.MkdirTemp("", "prr")` for socket dirs (the existing tests already do `os.MkdirTemp("", "hd")` for herdr). `paths.SocketPath` must check `len(path) < 104` and fall back to `os.TempDir()/pr-relay-<uid>/`. Add a unit test for the length check.

### Pitfall 4: The old daemon deletes the new daemon's socket on restart
**What goes wrong:** After `daemon restart`, the new daemon is running but its socket file is gone, so every CLI call auto-spawns, fails the lock, and errors.
**Why it happens:** `UnixListener.Close` unlinks the path. If the old daemon releases its lock first, the new daemon binds, and then the old daemon's deferred `Close` unlinks the new socket.
**How to avoid:** Close the listener before releasing either lock (strict shutdown order, Pattern 2). Test: restart in a loop 10×, then `status` succeeds every time.

### Pitfall 5: Two state dirs, one socket
**What goes wrong:** On Linux, `XDG_RUNTIME_DIR` is shared, while `XDG_STATE_HOME` or `PR_RELAY_STATE_DIR` differ between shells. Two daemons each lock their own `state.lock`, and the second unlinks the first one's socket.
**How to avoid:** The daemon takes a lock **next to the socket** (`<socket>.lock`) before anything else. If `PR_RELAY_STATE_DIR` is set and `PR_RELAY_SOCKET` is not, derive the socket inside the state dir, so tests are isolated even when `XDG_RUNTIME_DIR` is set on CI.

### Pitfall 6: Socket path spelling changes SubID
**What goes wrong:** `unsub` reports "not subscribed" for a subscription that exists.
**Why it happens:** `store.SubID` hashes the **raw** socket string, and the manager only cleans paths for its own keys (Pitfall 10 of Phase 3).
**How to avoid:** The CLI canonicalises the socket path once (`filepath.Abs` + `Clean`, without resolving symlinks, so it matches herdr's own spelling) and sends that for every command. `UnsubscribePane` matches on the cleaned socket too.

### Pitfall 7: IN-03 is amplified by every `sub`
**What goes wrong:** Each `sub` adds a pane, which triggers `SetPanes` → `endResub` → `markUp(now)` + `KindReconnected`. That resets `UpSince` (the RestoreGrace window) and pokes every worker on the socket, which restarts each one's quiet period. With many agents subscribing, delivery to other panes keeps getting pushed back.
**How to avoid:** In `Endpoint.Run`, on `endResub` keep `upSince` and emit a new `KindResubscribed` instead of `KindReconnected`. The manager handles it by doing a `startCheck` only for **newly added**, worker-less panes, with no worker pokes. A real drop or `events_lost` still emits `KindReconnected`. If the resubscribe connect itself fails, use the existing `markDown` path.

### Pitfall 8: Downgrade restart fails on schema version
**What goes wrong:** D-17 restarts "whichever side is newer". An older CLI then restarts a newer daemon, and the old binary's `store.Open` returns `ErrNewerVersion` because this phase bumps the schema to v3.
**How to avoid:** The ready pipe carries `err state file was written by a newer pr-relay (v3 > v2); upgrade pr-relay`. The CLI prints it and exits 1. That is the "clear error" branch of DMN-07. Also cap the restart at one attempt.

### Pitfall 9: IN-01 fix that stalls progress
**What goes wrong:** Capping the partial ack below a superseded event's Seq can make the ack *smaller than the first included line*. Every delivery then re-sends the same lines forever.
**How to avoid:** Cap only when a superseded event with Seq < cut has a superseded partner with Seq > cut. If the capped ack would not advance past the group's first included Seq, fall back to the current `h.seq-1` (accept the rare spurious "cleared" line). Test both the split case and the no-progress case.

### Pitfall 10: Daemon environment differs by launcher
**What goes wrong:** A CLI-spawned daemon inherits one pane's env (`GH_TOKEN`, `PATH` with `gh`). One started by brew services or the plugin has a minimal env. The keyring fallback (`gh auth token`) then fails, so auth is `no_token`.
**How to avoid:** Log the token source at startup (already exposed as `AuthState.Source`) and show it in `status`. Phase 5 documents PATH for service launchers. Do not scrub env in this phase, but do not read `HERDR_*` in the daemon either (each subscription carries its socket).

### Pitfall 11: WR-09 dev subscriptions in the real state dir
**What goes wrong:** `dev watch` writes subscriptions with `HerdrSocket="dev"` into the real state dir. The daemon's manager then tries to start an endpoint for a relative socket "dev".
**How to avoid:** (recommended, not locked) `Manager.sync` skips non-absolute sockets, and/or `dev watch` defaults to a temp state dir. With the daemon holding `state.lock`, `dev watch` on the real dir now fails with `ErrLocked` anyway. Leftover dev subs are the remaining hazard.

### Pitfall 12: Agent sandboxes block local sockets or spawn
**What goes wrong:** Under a sandbox profile (for example Codex's seatbelt, or Claude Code with sandboxing on), `connect()` to the socket or writes to `~/Library/...` can fail with EPERM.
**How to avoid:** Map EPERM/EACCES on dial or spawn to a clear message: "cannot reach the pr-relay daemon (operation not permitted); your agent sandbox may block local sockets". Phase 5's skill snippet should document this. [ASSUMED: sandbox behaviour not tested in this session]

### Pitfall 13: Expiry storm after a long power-off
**What goes wrong:** After the machine is off for more than 7 d, the first lifecycle tick expires every subscription, before the poller and delivery have had any chance to deliver activity that happened while the machine was off.
**How to avoid:** Delay the first `LifecycleTick` after daemon start by a grace period (for example 10 min, longer than one poll plus quiet period). This is a discretion item, flagged in Open Questions.

## Code Examples

### Daemon lock and listen (order matters)
```go
// Source: design over gofrs/flock v0.13.1 + stdlib net (APIs verified this session)
lk := flock.New(sock + ".lock")
if ok, err := lk.TryLock(); err != nil || !ok { return errAlreadyRunning }
st, state, err := store.Open(stateDir) // existing state.lock
if err != nil { _ = lk.Close(); return err }
if fi, err := os.Lstat(sock); err == nil {
    if fi.Mode()&os.ModeSocket == 0 { return fmt.Errorf("%s exists and is not a socket", sock) }
    _ = os.Remove(sock) // lock held: no live owner
}
ln, err := net.Listen("unix", sock)
if err == nil { err = os.Chmod(sock, 0o600) }
// ... run ...
// shutdown: ln.Close() (unlinks) → st.Close() → lk.Close()
```

### Ready pipe (daemon side)
```go
if fdStr := os.Getenv("PR_RELAY_READY_FD"); fdStr != "" {
    f := os.NewFile(uintptr(3), "ready")
    defer f.Close()
    if startErr != nil { fmt.Fprintf(f, "err %s\n", startErr); return startErr }
    fmt.Fprintln(f, "ok")
}
```
Unset `PR_RELAY_READY_FD` in the daemon's own env after use so that nothing it spawns inherits the meaning.

### Lifecycle line rendering (D-12)
```go
// Source: extends internal/render/render.go renderer.line (read this session)
case model.EventCheckin:
    d := human(time.Duration(e.IdleSecs) * time.Second) // "24h", "7d", "90m"
    return r.prefix + "check-in: no activity for " + d + ". still needed? keep: pr-relay keep " + r.pr +
        " | drop: pr-relay unsub " + r.pr, true
case model.EventExpired:
    return r.prefix + "expired: no activity for " + d + ", subscription removed. re-subscribe: pr-relay sub " + r.pr, true
```
`r.pr` is `ref.URL()`, which is built from a validated ref, so it is safe without `sanitize`. Keep the `fits()` guard: the lines are about 170 bytes, well under `MaxLineBytes = 512`. `human`: if `d >= 48h` and `d%24h == 0` → `Nd`; else if `d%time.Hour == 0` → `Nh`; else minutes. Golden cases: 24h, 7d, 36h, 90m.

### TTY confirmation for `reset` (stdlib only)
```go
fi, _ := os.Stdin.Stat()
isTTY := fi.Mode()&os.ModeCharDevice != 0
if !yes && !isTTY { return errors.New("reset drops every subscription on this machine; re-run with --yes") }
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Double-fork daemons | Self re-exec + `Setsid` | Go has never supported fork | Already proven by spike S5 |
| Redirect stderr to capture panics | `runtime/debug.SetCrashOutput` | Go 1.23 | Stdio can stay /dev/null |
| Fake clocks everywhere | `testing/synctest` bubbles (net.Pipe only) | Go 1.25 stable | ctl server and lifecycle tests run without real sleeps |
| Subscribe-then-baseline in the background (research ARCHITECTURE.md "Instead:" note) | Probe-then-subscribe with the probe as the baseline | This phase (D-08) | Supersedes the older research note: no provisional subscriptions |

**Deprecated/outdated:** `.planning/research/ARCHITECTURE.md` line 195 suggests returning success on baseline timeout and baselining in the background. D-08 overrides this: a timeout fails.

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | Go-opened fds are CLOEXEC, so the spawn lock is not inherited by the daemon | Pattern 2 | All CLIs would block on `spawn.lock` for the daemon's lifetime. The 20-concurrent test would catch it. |
| A2 | Counting superseded-but-acked events as "delivered" for the D-11 clock reset is acceptable | Pattern 5 | Slight under-counting of inactivity. Alternatively, reset only for events that actually rendered (needs compose to report them). |
| A3 | Rejecting `sub` on an already merged or closed PR (error, exit 1) is the desired UX | Pattern 4 | The user might prefer "subscribed; closed notice will arrive". Confirm. |
| A4 | Agent sandboxes may block unix-socket connect | Pitfall 12 | Only affects error wording. |
| A5 | lumberjack v2.2.1 is acceptable as a new dependency | Standard Stack | The fallback is an own rotator of about 60 lines. |
| A6 | LIFE-06's "PR drops to the slowest polling rate" clause is already satisfied by the cadence curve (a PR quiet for 24h is at `max_interval` 16m) | Open Q3 | If the user expects per-sub slowdown, more engine work is needed. |
| A7 | `www.github.com` and scheme-less links are acceptable inputs | Pattern 7 | Trivial to tighten. |

## Open Questions

1. **What does `sub` do on an already merged or closed PR?**
   - What we know: the probe sees `State != OPEN`. The legacy WR-04 path would queue a `closed` event, after which LIFE-01 removes the subscription right away.
   - Recommendation: fail clearly ("o/r#12 is already merged; nothing to watch") and create no subscription. Confirm with the user, or record it in the plan as Claude's discretion.
2. **What does `sub` do when herdr is unreachable at the captured socket?**
   - D-10 covers "no agent" and "can't identify", not "herdr down". Inside a pane, herdr is almost always up.
   - Recommendation: fail with `herdr not reachable at SOCKET`. Subscribing blind would store an empty identity, which matches any agent.
3. **What about LIFE-06's "drops to the slowest polling rate" clause?**
   - The polling rate is PR-level and shared (DMN-02). The cadence already reaches `max_interval` long before 24 h of PR quiet. A per-sub "inactive" state (D-11) can coexist with an active PR that is polled fast for others.
   - Recommendation: treat it as satisfied by the existing cadence, and note this in VERIFICATION together with the D-13 wording change.
4. **Should daemon downtime count toward inactivity?**
   - Recommendation: no clock compensation (simple), plus a first-tick grace after startup (Pitfall 13).
5. **Should WR-07, WR-08 and WR-09 from the Phase 3 verification be in scope?**
   - They are not in D-26. WR-09 is a daemon hazard (Pitfall 11). Recommend at least the one-line `Manager.sync` absolute-socket guard. WR-07 and WR-08 stay deferred unless the planner has room.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | build/test | ✓ | go1.27.3 darwin/arm64 | — |
| herdr (real) | optional live test only | ✓ (this session runs inside herdr) | 0.9.5 / protocol 22 per prior research | Fake `herdrtest` server for all automated tests |
| `gh` auth | daemon at runtime | not probed this session (Phase 2 covered it) | — | `GH_TOKEN=test` + fake GraphQL in tests |
| Go module proxy | `go get lumberjack` | ✓ (proxy.golang.org reachable) | — | own rotator |
| slopcheck | package audit | ✗ (install denied) | — | `[ASSUMED]` + human checkpoint |
| GitHub Actions macOS + ubuntu runners | CI | ✓ (ci.yml matrix, `go test -race -count=1 ./...`) | — | — |

**Missing dependencies with no fallback:** none.

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | stdlib `testing` + `testing/synctest` + `httptest`; golden files via `internal/testutil.Golden` (`-update`) |
| Config file | none (go test). CI: `.github/workflows/ci.yml` |
| Quick run command | `go test ./internal/... -count=1` (about 15 s; baseline run this session: all packages ok) |
| Full suite command | `go test -race -count=1 ./...` (baseline about 22 s without -race; `cmd/pr-relay` about 21 s, builds the binary in TestMain) |

### Phase Requirements → Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| SUB-09, DMN-08 | ParsePRLink accepts the forms listed, rejects repo, issue, other hosts and shorthand with distinct messages | unit (table) | `go test ./internal/model -run TestParsePRLink -count=1` | ❌ Wave 0 |
| SUB-02 | events parsing, aliases, unknown rejected; closed always allowed | unit | `go test ./cmd/pr-relay -run TestParseEvents -count=1` and `./internal/poller -run TestMaskBypass` | ❌ |
| SUB-03 | env detection, `--pane`, outside-herdr error, socket canonicalisation | unit (injected getenv) | `go test ./internal/herdr -run TestResolveSocket -count=1` | ❌ |
| SUB-07 | SubscribeVerified: not_found / forbidden / rate_limited / timeout / closed → error and **no sub persisted**; OK → baselined record, LastPollAt=now | unit (fake Fetcher) | `go test ./internal/poller -run TestSubscribeVerified -count=1` | ❌ |
| SUB-01 | cold `sub` through the real binary returns in under 10 s with `cmd.Output()` (no stdio hang) and later delivers into the fake herdr pane | e2e (binary + fake GH + fake herdr on scratch socket) | `go test ./cmd/pr-relay -run TestSubEndToEnd -count=1` | ❌ |
| SUB-05 | unsub idempotent; `--all` only the calling pane | unit (engine) + e2e | `go test ./internal/poller -run TestUnsubscribePane` | ❌ |
| SUB-06 | `ls` rows and `--all` layout | golden | `go test ./cmd/pr-relay -run TestFormatLs -count=1` | ❌ |
| DMN-01 | 20 concurrent `status` on a cold machine → one unique daemon PID; extra spawns exit harmlessly | e2e | `go test ./cmd/pr-relay -run TestConcurrentAutostart -count=1` | ❌ |
| DMN-07 | binary A (`-X main.version=0.3.0`) daemon, binary B CLI → one restart, stderr note, subs survive; downgrade with schema v3 → clear error | e2e (two builds) | `go test ./cmd/pr-relay -run TestVersionMismatch -count=1` | ❌ |
| DMN-05 | status output includes budget, counts, cancellations, sockets, paths | golden + e2e smoke | `go test ./cmd/pr-relay -run TestFormatStatus` | ❌ |
| DMN-06 | log file created 0600 in LogDir, dir 0700; rotation at the size limit | unit | `go test ./internal/daemon -run TestLogging` | ❌ |
| ctl | version gate refuses non-exempt methods; hello/shutdown always work; line cap; deadline | unit (net.Pipe, synctest) | `go test ./internal/ctl -count=1` | ❌ |
| daemon | lock order; stale socket removed; non-socket refused; restart loop ×10 keeps socket (Pitfall 4) | integration (real unix socket in MkdirTemp) | `go test ./internal/daemon -count=1` | ❌ |
| LIFE-06 | check-in at threshold, repeats every interval, replaces itself, keep resets, acked real event resets, lifecycle ack does not | unit (synctest, fake clock) | `go test ./internal/poller -run TestLifecycle -count=1` | ❌ |
| LIFE-07 | expiry queues message, excludes sub from polling at once, deletes on ack; pane gone drops it; config bounds | unit + config unit | `go test ./internal/poller -run TestExpiry; go test ./internal/config -run TestLifecycle` | ❌ |
| LIFE-01 | closed bypasses mask, sub removed after ack, cancellation reason `closed`; reopen not tracked | unit | `go test ./internal/poller -run TestClosedRemoval` | ❌ |
| D-12 | check-in and expiry wording (24h, 7d, 36h, 90m) | golden | `go test ./internal/render -run TestLifecycleLines` | ❌ |
| EVT-09 for lifecycle | crash between queueing an expiry and its ack re-delivers and does not lose it | extend existing crash test | `go test ./cmd/pr-relay -run TestCrash -count=1` | ✅ (extend) |
| IN-01 | split-pair and no-progress cases | unit | `go test ./internal/compose -run TestPartialAckSupersede` | ❌ |
| IN-02 | omitted pane later appears → resubscribed after grace or on agent_detected | unit (herdrtest, synctest) | `go test ./internal/herdr -run TestEndpointOmittedPane` | ❌ |
| IN-03 | pane-set change keeps UpSince and pokes no worker | unit | `go test ./internal/herdr -run TestResubscribeKeepsUpSince; go test ./internal/deliver -run TestResubscribedNoPoke` | ❌ |
| Store v3 | v2 file migrates additively; v4 refused | unit | `go test ./internal/store -run TestMigrate` | ✅ (extend) |

### Sampling Rate
- **Per task commit:** `go test ./internal/<touched pkg> -count=1`
- **Per wave merge:** `go test -race -count=1 ./...`
- **Phase gate:** full suite green on macOS and Linux CI before `/gsd:verify-work`. Optional manual live check: `sub` in a scratch herdr session, then a real PR comment arrives in the pane.

### Wave 0 Gaps
- [ ] `cmd/pr-relay` e2e harness v2: strips `HERDR_*`, `XDG_RUNTIME_DIR` and `PR_RELAY_*`; short `MkdirTemp` socket dir; fake herdr on a scratch socket (reuse `scratchHerdr` from `devdeliver_test.go`); `t.Cleanup` runs `daemon stop` and kills any leftover PID.
- [ ] Second test binary build with `-ldflags "-X main.version=..."` for the mismatch tests.
- [ ] `internal/ctl` test helpers (net.Pipe listener).
- [ ] Golden testdata dirs for `cmd/pr-relay` formatting and render lifecycle lines.

## Security Domain

### Applicable ASVS Categories
| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | no (no new auth; gh token reuse unchanged) | go-gh `TokenForHost` via existing `ghclient.TokenSource` |
| V3 Session Management | no | — |
| V4 Access Control | yes | Socket dir 0700 (ownership verified with `Lstat`, no symlink), socket 0600, client checks the socket owner before dialing; optional peer-uid check (x/sys/unix) |
| V5 Input Validation | yes | Strict `ParsePRURL` after normalisation; `normalizeMask`; ctl line cap 1 MiB, JSON decode into fixed structs, unknown methods → `bad_request`; pane ID passed to herdr only as a JSON value |
| V6 Cryptography | no | — |
| V7 Logging | yes | Never log the token (existing T-02-38); log file 0600; log PR keys and pane IDs, never PR text |

### Known Threat Patterns
| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Another local user connects to the control socket and injects subscriptions into your panes | Elevation / Tampering | 0700 dir + 0600 socket; optional SO_PEERCRED/LOCAL_PEERCRED uid check |
| Attacker pre-creates `/tmp/pr-relay-<uid>/` (Linux fallback) or a symlink | Tampering | `Lstat` the dir: owner == uid, not a symlink, mode tightened to 0700, else refuse |
| A planted non-socket file at the socket path gets deleted | Tampering | Remove only if `ModeSocket`, after the lock is held |
| An agent wipes everyone's subscriptions | Denial of service | `reset` requires `--yes` without a TTY (D-25); `unsub --all` is pane-scoped (D-24) |
| A crafted `--pane`/socket makes the daemon dial an arbitrary path | Spoofing | `herdr.DialUnix` already refuses non-socket or other-owner paths (T-03-04) |
| Lifecycle line injection | Tampering | Only validated `ref.URL()` and config durations are interpolated; `fits()` tag guard retained |

## Sources

### Primary (HIGH confidence)
- Codebase, read this session: `internal/poller/{engine,fanout,status,scheduler}.go`, `internal/store/{state,store}.go`, `internal/deliver/{manager,worker,engine,identity}.go`, `internal/herdr/{client,endpoint,types}.go`, `internal/compose/{compose,supersede}.go`, `internal/render/render.go`, `internal/model/model.go`, `internal/config/config.go`, `internal/paths/paths.go`, `internal/ghclient/client.go`, `cmd/pr-relay/{main,dev,crash_test}.go`, `spikes/setsid-probe/main.go`
- gofrs/flock v0.13.1 source in the module cache (`TryLockContext`, `TryLock`, `Close`)
- golang.org/x/sys v0.49.0 module cache (`GetsockoptXucred`, `GetsockoptUcred`)
- `go doc runtime/debug.SetCrashOutput`, `go doc testing/synctest`
- proxy.golang.org `@latest` for cobra v1.10.2, flock v0.13.1, lumberjack v2.2.1, x/sys v0.49.0, x/term v0.47.0
- lumberjack v2.2.1 `lumberjack.go` raw source (file mode 0600 on new files)
- https://herdr.dev/tui/docs/socket-api/ (socket resolution order, session socket path, injected env)

### Secondary (MEDIUM confidence)
- `.planning/research/PITFALLS.md` §17-19 (daemon races, socket path, launch methods), cross-checked against code
- Phase 3 `03-VERIFICATION.md` (WR-07/08/09 follow-ups), `deferred-items.md` (IN-01..03)

### Tertiary (LOW confidence)
- Agent sandbox socket behaviour (Pitfall 12), not tested

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH. Everything except lumberjack is already in go.mod and was verified against the proxy and source.
- Architecture: HIGH for the seams (read from code). MEDIUM for the exact engine-change design, which is reasoned and not yet prototyped.
- Pitfalls: HIGH for 1-8 and 11 (mechanics verified or directly visible in code and env). MEDIUM for 9 and 13. LOW for 12.

**Research date:** 2026-11-04
**Valid until:** 2026-12-04 (stable domain; codebase seams valid until Phase 4 code lands)
