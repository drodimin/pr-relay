---
phase: 04-daemon-control-protocol-cli
plan: 06
subsystem: autostart
tags: [daemon, autostart, flock, setsid, version-gate]
requires:
  - internal/ctl (04-03)
  - internal/paths SocketPath, SocketLockPath, SpawnLockPath, LogDir (04-03)
provides:
  - autostart.Options, Connect, Call, Stop, Restart
  - autostart.EnvReadyFD, SignalReady (daemon side of the ready pipe)
affects: [04-10 daemon startup must call SignalReady and hold pr-relay.sock.lock, 04-11 CLI commands]
tech-stack:
  added: []
  patterns:
    - helper-process tests (test binary re-execs itself as a fake daemon via TestMain)
    - ready pipe on fd 3 ("ok" / "err MSG")
key-files:
  created:
    - internal/autostart/ready.go
    - internal/autostart/spawn_unix.go
    - internal/autostart/autostart.go
    - internal/autostart/autostart_test.go
  modified: []
decisions:
  - "Restart checks under the spawn lock and skips the restart when the daemon is already at o.Version, so concurrent mismatching CLIs restart it only once"
  - "If spawn reports an error but a daemon answers hello afterwards (another spawner won the socket lock), Connect succeeds"
  - "Stop proves full exit by acquiring and releasing <socket>.lock; the daemon (04-10) must release that lock last"
  - "Startup-failure hint names the log directory (paths.LogDir), since the log file name is not fixed yet"
metrics:
  duration: ~20m
  completed: 2026-11-04
---

# Phase 4 Plan 06: Daemon auto-start Summary

I added `internal/autostart`, the CLI side of the daemon lifecycle. A CLI call on a cold machine spawns exactly one `pr-relay daemon`, using a Setsid self re-exec with stdio on /dev/null and cwd `/`. The daemon's startup result comes back on a ready pipe on fd 3. When versions differ, the CLI restarts the daemon once, retries once and prints `restarted daemon (OLD -> NEW)`.

## What was built

- **ready.go**: `EnvReadyFD = "PR_RELAY_READY_FD"`. `SignalReady(err)` writes `ok` or `err MSG` to fd 3, then closes it and unsets the env var. It does nothing when the env var is unset. `readReady` maps a deadline to "daemon did not become ready within X" and EOF to "daemon exited during startup; see the daemon log in DIR".
- **spawn_unix.go**: `spawn` uses `exec.Command`, `Setsid: true` and `/dev/null` for all three stdio streams (no `os.Stdout`/`os.Stderr` anywhere in the file). The ready pipe is the only entry in ExtraFiles. The CLI never waits on the child (`Process.Release`). EPERM/EACCES produce a message that names a possible agent sandbox.
- **autostart.go**:
  - `Connect`: a 300 ms hello first. On a down result (ENOENT or ECONNREFUSED), it takes `spawn.lock` with `TryLockContext` (LockTimeout 4 s), checks again under the lock, spawns, then sends a 1 s hello. EPERM/EACCES on dial produce a "sandbox may block local sockets" message.
  - `Call`: Connect and call. On `*ctl.VersionMismatchError` it runs Restart once and retries once. A second mismatch returns "daemon version mismatch persists after restart".
  - `Stop`: hello without spawning, then `shutdown`, then waits for `<socket>.lock` (ExitTimeout 5 s).
  - `Restart`: holds the spawn lock while it runs Stop, spawn and hello.

## Verification

- `go test ./internal/autostart -count=3 -race` passes. `go vet` and `gofmt` are clean. No fake daemon processes were left over after the run.
- The tests cover:
  - TestConnectSpawns: spawns, answers status, Stop removes the socket and the PID is gone; a second Stop reports not running.
  - TestConcurrentConnect: 20 cold `Call`s that all succeed and share one daemon PID.
  - TestMismatchRestart: 0.3.0 to 0.4.0, exactly one stderr line, new PID, old PID reaped.
  - TestMismatchTwice: the persists error and no restart note.
  - TestStartupError: the daemon's own message reaches the CLI.
  - TestStaleSocket: an ECONNREFUSED socket file counts as down.
  - TestReadyTimeoutAndEOF.

## Deviations from Plan

### Auto-added (Rule 2)

**1. Restart is idempotent under concurrency**
- **Found during:** Task 2
- **Issue:** Several CLIs that hit the same mismatch at once would each stop and respawn the daemon. Each later CLI would kill the fresh, correct daemon that an earlier one had just started.
- **Fix:** Under the spawn lock, Restart first sends hello and returns early if the daemon already reports the CLI's version and protocol.
- **Commit:** ee3fea6

**2. Spawn-race tolerance**
- **Found during:** Task 2
- **Issue:** A spawned daemon that loses the socket lock exits without signalling ready, so `spawn` reports an error even though a daemon is running.
- **Fix:** After spawning, `ensureUp` always sends hello and only returns the spawn error if hello also fails.
- **Commit:** ee3fea6

**3. Extra readReady unit test** (TestReadyTimeoutAndEOF) for the timeout and EOF paths.

### TDD gate note

Task 2 was marked `tdd="true"`. I wrote the tests and the implementation together and committed them as one `feat` commit, so there is no separate RED commit. Every behaviour in the plan's list has a test.

## Notes for 04-10 (daemon)

- Take `<socket>.lock` with TryLock first and exit quietly if it is held.
- Remove a stale socket file, listen with mode 0600, then call `SignalReady(nil)`. If startup fails, call `SignalReady(err)` and exit non-zero.
- On shutdown, close the listener, unlink the socket and close the store. Release the socket lock last, because Stop relies on that order.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: internal/autostart/ready.go, spawn_unix.go, autostart.go, autostart_test.go
- FOUND commits: 6b731ee, ee3fea6
