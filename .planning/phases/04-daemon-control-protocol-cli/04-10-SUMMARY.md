---
phase: 04-daemon-control-protocol-cli
plan: 10
subsystem: daemon
tags: [daemon, singleton, unix-socket, ctl, handlers, shutdown-order]
requires:
  - 04-02 (deliver.Manager.Sockets, herdr PaneGet)
  - 04-03 (ctl server/client, method structs, paths)
  - 04-04 (OpenLog, SetCrash)
  - 04-06 (autostart.SignalReady)
  - 04-08 (SubscribeVerified, VerifyError, UnsubscribePane, Reset)
  - 04-09 (RunLifecycle, LifecycleDue)
provides:
  - "daemon.Run(ctx, Options) error, daemon.Options{Version, GraphQLEndpoint, Fetcher, Now}, daemon.ErrAlreadyRunning"
  - "ctl handlers for sub, unsub, keep, poll, ls, status, reset"
affects: [04-11 CLI commands (pr-relay daemon, sub, unsub, keep, poll, ls, status, reset)]
tech-stack:
  added: []
  patterns:
    - "Startup errors go through one fail() that calls SignalReady(err) and runs the startup undo stack in reverse"
    - "Separate trigger ctx (signal, parent, shutdown method) and work ctx (engine, delivery, lifecycle), so in-flight requests drain before components stop"
    - "testHostHook package var exposes the built host to tests"
key-files:
  created:
    - internal/daemon/daemon.go
    - internal/daemon/handlers.go
    - internal/daemon/daemon_test.go
    - internal/daemon/handlers_test.go
  modified: []
decisions:
  - "A lost socket-lock race calls SignalReady(ErrAlreadyRunning) and returns ErrAlreadyRunning, as the plan says. autostart already treats a spawn error followed by a working hello as success. 04-11 should map ErrAlreadyRunning to exit 0."
  - "The startup auth source comes from go-gh auth.TokenFromEnvOrConfig (env or hosts.yml), falling back to \"gh\" when the gh binary is on PATH and otherwise \"none\". It never runs `gh auth token` at startup. status prefers the poller's Auth.Source once a poll has resolved the token."
  - "A crash-file setup failure is logged as a warning and does not stop startup."
  - "Params decode strictly with DisallowUnknownFields (T-04-40). The version gate means the CLI and daemon always share the structs."
  - "unsub with neither a URL nor All, or with both, is bad_request."
  - "Per-PR poll errors (PollResult.Err) are mapped through the same class table as verify errors."
  - "Event names are checked against model.AllEventTypes in the handler, so a bad --events value is bad_request. The stored, normalised mask is reported back."
  - "ls with a PaneID and no HerdrSocket filters by pane only. With both set, it filters by the cleaned socket and the pane."
metrics:
  duration: ~25m
  completed: 2026-11-04
  tasks: 2
  files: 4
requirements: [DMN-01, DMN-05, DMN-06, DMN-07, DMN-08, SUB-01, SUB-03, SUB-05, SUB-06, SUB-07, SUB-09, LIFE-01, LIFE-06, LIFE-07]
---

# Phase 4 Plan 10: Daemon host and control handlers Summary

This plan adds the per-user pr-relay daemon. It takes the socket lock first, then the state lock. It replaces only a real stale socket and listens with mode 0600. It then runs the poller, delivery manager, lifecycle ticker and control server, and serves every control method. Shutdown releases the socket lock last, so restarting is safe.

## What was built

- **daemon.go: `Run(ctx, Options) error`**. Startup runs in this order:
  1. Resolve paths.
  2. `OpenLog` and `SetCrash`.
  3. Create `<socket>.lock` (0600) and `TryLock` it. If it is held, call `SignalReady(ErrAlreadyRunning)` and return `ErrAlreadyRunning`.
  4. `config.Load`.
  5. `store.Open`. `ErrLocked` becomes "state in use by another pr-relay (dev watch?) at DIR". `ErrNewerVersion` becomes "state file was written by a newer pr-relay; upgrade pr-relay".
  6. Build the ghclient, or use `Options.Fetcher` if one is supplied.
  7. `poller.New`, quarantine, then `deliver.New`.
  8. `Lstat` the socket path. A socket is removed. Any other file type fails with "SOCK exists and is not a socket" and is left untouched.
  9. `net.Listen` and `os.Chmod(socket, 0o600)`, then write `daemon.pid` (0600).
  10. Start `ctl.NewServer` and the engine, delivery and `RunLifecycle(10m, 1m)` goroutines. Under fast-poll, the lifecycle uses 0 and 50ms instead.
  11. `SignalReady(nil)`, then log "daemon started" with the version, pid, socket, state_dir and auth_source.

  Every startup failure calls `SignalReady(err)` and undoes the earlier steps in reverse.

  Shutdown is triggered by ctx, SIGINT/SIGTERM or the shutdown method. It runs in this order:
  1. `srv.Close(2s)`
  2. stop the work ctx and wait for the components
  3. `ln.Close()`, which unlinks the socket
  4. remove the pid file
  5. `st.Close()`
  6. unlock and close the socket lock
  7. close the log

  The daemon never exits on its own. Only a failed listener ends Run with an error.
- **handlers.go**: one map with `ctl.MethodSub`, `MethodUnsub`, `MethodKeep`, `MethodPoll`, `MethodLs`, `MethodStatus` and `MethodReset`.
  - **sub** re-validates the link with `ParsePRLink` and requires an absolute socket and a pane ID. It then calls herdr `PaneGet` with a 1.5s timeout:
    - `pane_not_found` gives "pane P not found in herdr at S".
    - An unreachable herdr gives `herdr_unreachable`, and nothing is stored.
    - A pane with no agent still subscribes, with the D-10 warning.

    Next it calls `SubscribeVerified` with a 5s timeout. Each `VerifyError.Class` maps to a code with a message the agent can act on. Co-subscribers are listed as pane IDs, with a `socket:` prefix when they are on another herdr socket.
  - **unsub** calls `UnsubscribePane`. It is pane-scoped and idempotent, and removing nothing is not an error.
  - **keep** maps a missing subscription to `not_watched` "not subscribed" and an ended one to `ended` "subscription has ended; re-subscribe with pr-relay sub URL". On success it returns the times from `LifecycleDue`.
  - **poll** calls `PollNow`. A PR that is not watched gives `not_watched`; a rate limit gives `rate_limited`, with the time it ends. On success it returns `Schedule`.
  - **ls** filters by pane unless All is set or PaneID is empty. Rows are sorted by pane, then PR, and include `LastEvent*`, `CheckinDue`, `ExpiresAt` and `Ending`.
  - **status** returns the D-23 fields. Cancellations are listed newest first; this is the only place they appear (D-04). It also reports per-socket herdr health with the pending count.
  - **reset** returns how many subscriptions it removed.
  - Each call logs only the method, the result code, the PR key and the pane (T-04-42).

## Commits

| Task | Commit | Message |
|------|--------|---------|
| 1 | b0b0025 | feat(04-10): daemon host with strict lock, listen and shutdown order |
| 2 | 27d2a8b | feat(04-10): control method handlers for sub, unsub, keep, poll, ls, status, reset |

## Verification

- `go test ./internal/daemon -count=3 -race` passes:
  - TestDaemonStartsAndStops, AlreadyRunning, StaleSocket (stale socket replaced; regular file refused and left alone), StateLocked, RestartLoop (10 cycles of start, shutdown method, socket gone), Idle (2s) and TestLoggingDaemonStarted
  - TestSubOK, SubHerdr, SubGitHubErrors (not_found, forbidden, unauthorized, no_token, rate_limited, secondary_limit, transient, merged) and SubBadRequest
  - TestUnsub, Keep (including ended after expiry), Poll, Ls, Status and Reset
- `go test ./... -count=1` passes. `go vet ./...` and `gofmt -l` are clean.
- All the acceptance greps match: the `func Run(ctx context.Context, o Options) error` signature, `SignalReady(`, `RunLifecycle(`, `os.Chmod(socket, 0o600)`, the 7 `ctl.Method*` registrations, `SubscribeVerified(`, `ParsePRLink(` and `PaneGet(`.

## Deviations from Plan

### Auto-added (Rule 2)

**1. Strict param decoding and event validation**
- `DisallowUnknownFields` and a daemon-side check of the event list mean malformed params and unknown event types become `bad_request` before any herdr or GitHub call (T-04-40).
- **Commit:** 27d2a8b

**2. Startup undo stack**
- A failure part-way through startup releases everything taken so far (log, socket lock, store, listener, pid file), so a failed start never leaves the lock held. The regular-file test checks that the lock is free afterwards.
- **Commit:** b0b0025

### Notes

- The plan extends TestLogging with a daemon check. That check went into a new test, `TestLoggingDaemonStarted` in daemon_test.go, because log_test.go is not in this plan's file list. `-run TestLogging` still selects it.
- Task 1 was committed with a placeholder `handlers()` returning an empty map. Task 2 replaced it.

## TDD Gate Compliance

Both tasks were marked `tdd="true"`. As in 04-03, I wrote each task's tests and implementation together and committed them as one `feat` commit, so there is no separate RED `test(...)` commit. Every behaviour in the plan is covered by a test.

## Known Stubs

None.

## Threat Flags

None. The daemon's surfaces (control socket, herdr dial, pid and lock files) are all covered by T-04-37..43.

## Self-Check: PASSED

- FOUND: internal/daemon/daemon.go, handlers.go, daemon_test.go, handlers_test.go
- FOUND commits: b0b0025, 27d2a8b
