---
phase: 05-packaging-plugin-agent-skill
plan: 02
subsystem: store, ctl, daemon
tags: [store, shutdown, daemon, release-gate]
requires: []
provides:
  - store.ErrClosed and a mutex-guarded Save/Close
  - ctl.Server.Close that waits a second bounded period after cancelling handlers
  - lock-first daemon startup (socket lock before OpenLog/SetCrash)
affects: [internal/store, internal/ctl, internal/daemon]
tech-stack:
  added: []
  patterns: [closed-guard sentinel under mutex, two-stage bounded drain]
key-files:
  created: []
  modified:
    - internal/store/store.go
    - internal/store/store_test.go
    - internal/ctl/server.go
    - internal/ctl/ctl_test.go
    - internal/daemon/daemon.go
    - internal/daemon/daemon_test.go
decisions:
  - "ErrClosed text is 'store: save after close', keeping the 'store: save' prefix convention"
  - "ctl.Server.Close's second wait after cancel uses the same drain duration, and Close still returns the drain-timeout error after it"
  - "A losing daemon does not log anything: no logger exists before the socket lock, and ErrAlreadyRunning reaches the spawner through SignalReady"
metrics:
  duration: ~15m
  completed: 2026-11-05
---

# Phase 5 Plan 02: CR-02 shutdown race and WR-09 loser log touch Summary

Store.Save now returns ErrClosed once Close has run (both hold a new mutex). ctl.Server.Close waits a second bounded drain period after cancelling handlers. The daemon takes its singleton socket lock before it opens daemon.log or crash.log, so a losing starter never touches the live daemon's logs.

## Tasks

| Task | Name | Commits | Files |
| ---- | ---- | ------- | ----- |
| 1 | CR-02 store closed guard and draining server Close | 2ff8094 (RED), 501e840 (GREEN) | internal/store/store.go, store_test.go, internal/ctl/server.go, ctl_test.go |
| 2 | WR-09 take the socket lock before opening the log | 1be7353 (RED), 700b6ce (GREEN) | internal/daemon/daemon.go, daemon_test.go |

## What changed

- **Store (T-05-04):** `mu sync.Mutex` is added to Store. Save and Close both take it. Save returns `ErrClosed` when `s.lock == nil`, before it creates any temp file. A second Close still returns nil.
- **ctl.Server.Close (T-05-05):** In the timeout branch it calls `s.cancel()`, then waits on `done` or a second `drain` timer, whichever comes first. It then returns `ctl: drain timed out after <drain>`. The success branch is unchanged. The daemon's shutdown order is unchanged (srv.Close, serveErr, stopWork, wg.Wait, st.Close, lk.Unlock).
- **daemon.Run (T-05-06):** The socket-lock block now comes right after path resolution and before `OpenLog`/`SetCrash`. The undo order now releases the log before the lock. The `log.Info("another daemon holds the socket lock; exiting")` call was removed. A new `log.Info("socket lock held", "socket", ...)` runs after OpenLog.

## Tests

- `TestSaveAfterClose`: Save after Close returns ErrClosed, state.json is unchanged, no temp files are left, and a second Close returns nil.
- `TestSaveCloseRace`: 8 goroutines call Save in a loop while Close runs, under `-race`. Every Save that starts after Close has returned gets ErrClosed.
- `TestShutdownDrain`: a handler ignores cancellation for 1.5x the drain period. Close returns only after that handler finishes, and still returns "drain timed out".
- `TestLoserDoesNotTouchLog`: a losing Run with a fresh PR_RELAY_LOG_DIR returns ErrAlreadyRunning and creates nothing in that directory. The live daemon's log directory listing is unchanged. In the RED run, the loser created `crash.log` and `daemon.log`.
- `TestCloseDrainTimeout` passes without changes: its handler exits as soon as it is cancelled, so Close still takes exactly 1s.

Verification: `go test -race -count=1 ./internal/store ./internal/ctl ./internal/daemon ./cmd/pr-relay` all ok.

## Deviations from Plan

None. The plan was executed as written.

## TDD Gate Compliance

Each task has a RED `test(05-02)` commit followed by a GREEN commit (`feat` for Task 1, `fix` for Task 2). No refactor commits were needed.

## Self-Check: PASSED

- FOUND: internal/store/store.go (ErrClosed, 2x s.mu.Lock())
- FOUND: internal/ctl/server.go (second bounded wait)
- FOUND: internal/daemon/daemon.go (TryLock at line 144 < OpenLog at line 155)
- FOUND commits: 2ff8094, 501e840, 1be7353, 700b6ce
