---
phase: 05-packaging-plugin-agent-skill
plan: 03
subsystem: autostart
tags: [autostart, concurrency, release-gate]
requires: []
provides:
  - "autostart.probe: hello with bounded timeout retries"
  - "autostart.AlreadyRunningMsg exported constant"
  - "ensureUp: one bounded wait-and-respawn across a daemon shutdown window"
affects:
  - "cmd/pr-relay (plan 05-05 adds the AlreadyRunningMsg == daemon.ErrAlreadyRunning guard test)"
  - "herdr [[startup]] hook `daemon --ensure`"
tech-stack:
  added: []
  patterns:
    - "Treat both context.DeadlineExceeded and os.ErrDeadlineExceeded as a hello timeout"
key-files:
  created: []
  modified:
    - internal/autostart/autostart.go
    - internal/autostart/autostart_test.go
decisions:
  - "A hello timeout is either context.DeadlineExceeded or os.ErrDeadlineExceeded wrapped in UnreachableError, because the conn deadline equals the ctx deadline and either can fire first"
  - "ensureUp re-spawns only when the spawn reported AlreadyRunningMsg, or the spawn succeeded but no daemon answers; a real startup error is returned without retrying"
metrics:
  duration: "~15m"
  completed: 2026-11-05
  tasks: 2
  files: 2
---

# Phase 5 Plan 03: Autostart WR-03/WR-08 Race Fixes Summary

Autostart now retries a slow daemon hello twice with the 1 s timeout before failing, and gives a "not answering ... see the daemon log in DIR" error. When a CLI starts during another daemon's shutdown window, it waits for the lock to be released and spawns exactly once more. Before this change it printed "daemon: already running".

## Tasks

| Task | Name | Commits | Files |
| ---- | ---- | ------- | ----- |
| 1 | WR-08 retry a slow hello before failing | 64e3a52 (test), df84851 (feat) | internal/autostart/autostart.go, internal/autostart/autostart_test.go |
| 2 | WR-03 wait out a shutting-down daemon and spawn again | d46698d (test), fab56da (feat) | internal/autostart/autostart.go, internal/autostart/autostart_test.go |

## What changed

- `probe(ctx, c, socket)` calls hello with the 300 ms timeout. On a timeout it retries hello twice with the 1 s timeout, after sleeps of 100 ms and 200 ms that honour ctx. If every attempt times out it returns `daemon at SOCKET is not answering (hello timed out); see <logHint>: <err>`. Any other error is returned unchanged, so `isDown` still routes to a spawn. The worst case is about 2.6 s. `Connect` and `ensureUp` use probe. `Restart` keeps its plain hello.
- `const AlreadyRunningMsg = "daemon: already running"`, with a doc comment requiring it to equal `daemon.ErrAlreadyRunning.Error()`.
- `ensureUp` waits and re-spawns when the post-spawn hello fails and either the spawn reported AlreadyRunningMsg or the spawn succeeded but the socket is down. The wait is `waitExited(ctx, socket, o.ExitTimeout)`, followed by one more spawn and hello. There is exactly one re-spawn and no loop (T-05-07).
- The fake daemon now reports `SignalReady(errors.New(AlreadyRunningMsg))` when it loses the lock. It has a new `AUTOSTART_FAKE_LINGER` knob (`envFakeLinger`) that holds the socket lock after unlinking the socket on shutdown.
- New tests: `TestSlowHelloRetried`, which uses an in-process ctl server behind a 700 ms-per-accept listener and a spawn path that cannot exist; `TestHelloNeverAnswers`, which uses a listener that never accepts; and `TestConnectDuringShutdown`. The temp-dir isolation was moved out of `setup` into an `isolate(t)` helper that returns the socket path.

## Verification

- `go test ./internal/autostart -race -count=3`: ok
- `go test -race -count=1 ./cmd/pr-relay -run 'TestVersionMismatch|TestDaemonRestartCmd|TestConcurrent'`: ok
- `go test -race -count=1 ./...`: all packages ok
- RED was observed for each task before GREEN. The slow and hung tests failed with a bare `i/o timeout`. TestConnectDuringShutdown failed with `daemon: already running`, which is the WR-03 symptom.

## Deviations from Plan

**1. [Rule 1 - Bug] Timeout detection includes os.ErrDeadlineExceeded**
- **Found during:** Task 1 RED
- **Issue:** The plan keyed the retry on `errors.Is(err, context.DeadlineExceeded)`. In practice the conn deadline (equal to the ctx deadline) fires first, and ctl returns `*UnreachableError{i/o timeout}`. Checking only the ctx error would never retry.
- **Fix:** `isTimeout(err)` accepts either `context.DeadlineExceeded` or `os.ErrDeadlineExceeded`. Probe still requires the parent `ctx.Err() == nil`.
- **Commit:** df84851

**2. [Rule 1 - Correctness] No re-spawn for a real startup error**
- **Found during:** Task 2
- **Issue:** The plan's condition `alreadyRunning || isDown(helloErr)` would re-spawn after a deterministic startup failure such as a newer state schema. That doubles the work and changes nothing.
- **Fix:** The condition is now `alreadyRunning || (spawnErr == nil && isDown(helloErr))`. The WR-03 path is unaffected, and startup errors still surface on the first attempt (TestStartupError is green).
- **Commit:** fab56da

## Known Stubs

None.

## Self-Check: PASSED

- internal/autostart/autostart.go and internal/autostart/autostart_test.go exist.
- Commits 64e3a52, df84851, d46698d and fab56da exist on the branch.
