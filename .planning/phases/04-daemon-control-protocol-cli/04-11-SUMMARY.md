---
phase: 04-daemon-control-protocol-cli
plan: 11
subsystem: cli
tags: [cli, cobra, autostart, sub, unsub, keep, poll, ls, status, reset, daemon]
requires:
  - 04-02 (herdr.ResolveSocket, PaneFromEnv, InHerdr)
  - 04-06 (autostart.Call, Stop, Restart)
  - 04-07 (parseEvents, formatSub/Ls/Status/Poll/Keep)
  - 04-10 (daemon.Run, daemon.ErrAlreadyRunning)
provides:
  - "pr-relay sub, unsub, keep, poll, ls, status, reset, daemon [stop|restart] commands"
  - "cmd/pr-relay/cli.go: caller type, daemonCaller, resolveTarget(getenv, paneFlag), cliError"
affects: [phase 05 plugin/skill docs (command surface and messages)]
tech-stack:
  added: []
  patterns:
    - "Each command is a thin newXCmd wrapper over runX(ctx, writers, caller, getenv, ...) so tests inject a fake caller and env map"
    - "All local validation (ParsePRLink, parseEvents, resolveTarget) happens before the first caller invocation"
key-files:
  created:
    - cmd/pr-relay/cli.go
    - cmd/pr-relay/cli_test.go
    - cmd/pr-relay/sub.go
    - cmd/pr-relay/unsub.go
    - cmd/pr-relay/keep.go
    - cmd/pr-relay/poll.go
    - cmd/pr-relay/ls.go
    - cmd/pr-relay/status.go
    - cmd/pr-relay/reset.go
    - cmd/pr-relay/daemon_cmd.go
  modified:
    - cmd/pr-relay/main.go
decisions:
  - "Deadlines: sub 10s (SUB-01 hard bound), unsub/keep/ls/status/reset/stop 10s, poll 20s (server gives poll 15s), restart 20s."
  - "`pr-relay daemon restart` calls autostart.Stop then autostart.Restart, because autostart.Restart is a no-op when the running daemon already has the CLI's version. It then reports the pid from a hello."
  - "ls --pane ID inside or outside herdr lists that pane only; ls outside herdr with no --pane lists every pane with the PANE column."
  - "unsub --all with exactly one removal prints 'unsubscribed 1 subscription'; reset pluralises the same way."
  - "cliError maps *ctl.Error to its message (or its code when the message is empty); main applies it to every returned error. Unreachable and version-mismatch errors pass through unchanged."
  - "The foreground daemon prints 'daemon already running' to stdout and exits 0 on ErrAlreadyRunning."
metrics:
  duration: ~20m
  completed: 2026-11-04
  tasks: 2
  files: 11
---

# Phase 4 Plan 11: CLI Commands Summary

This plan adds the agent-facing cobra commands: `sub`, `unsub`, `keep`, `poll`, `ls`, `status`, `reset` and `daemon [stop|restart]`. They sit on top of autostart, the 04-07 formatters and `daemon.Run`. Every pane-scoped command validates its input locally before it dials the daemon.

## What was built

- **cli.go**: holds the `caller` abstraction. `daemonCaller` wraps `autostart.Call` with `Options{Version, Stderr}`, so a version mismatch is handled with one restart and one stderr note (D-17). `resolveTarget` takes the pane from `--pane` or `HERDR_PANE_ID` and the socket from `herdr.ResolveSocket`. If there is no pane it fails with "not inside a herdr pane (HERDR_PANE_ID unset); pass --pane". `cliError` strips the wire codes from errors.
- **sub**: runs ParsePRLink, then parseEvents, then resolveTarget, and only after that calls the daemon, under a 10s deadline. It prints formatSub to stdout and `warning: ...` to stderr when SubResult.Warning is set (D-10).
- **unsub**: requires exactly one of a link or `--all`. Output follows D-24: "not subscribed" (exit 0), "unsubscribed o/r#N", or "unsubscribed N subscriptions".
- **keep** prints formatKeep. **poll** needs no pane, uses a 20s deadline and prints formatPoll.
- **ls**: scoped as in D-21. **status**: D-23, and it auto-starts the daemon like every other command.
- **reset**: requires `--yes`, or a "y"/"yes" answer to the stderr prompt when stdin is a TTY. The TTY check uses `os.ModeCharDevice` from the stdlib (D-25, T-04-44).
- **daemon**: runs `daemon.Run` in the foreground with `GraphQLEndpoint` taken from `PR_RELAY_GITHUB_GRAPHQL_URL`, and maps ErrAlreadyRunning to exit 0. `stop` prints "daemon stopped" or "daemon not running". `restart` prints "daemon restarted (pid N)".
- **main.go**: registers every new command. The `version` and hidden `dev` commands are still there (D-20). No command has `--json` (D-22), and TestRootCommands checks this.

## Verification

- `go test ./cmd/pr-relay -count=1`: all tests pass, including the new TestResolveTarget, TestSubLocalValidation, TestSubCallsDaemon, TestCliError, TestUnsubNotSubscribed, TestUnsubArgs, TestKeepAndPollLocalValidation, TestLsScope, TestResetConfirm, TestRootCommands and TestDaemonCommands.
- `go vet ./cmd/pr-relay` is clean, and `go build ./...` succeeds.
- Smoke test: running the built binary `sub https://github.com/o/r/pull/1` with HERDR_PANE_ID unset printed the pane error and exited 1. No daemon socket was created.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] `daemon restart` would not restart a daemon that was already on the current version**
- **Found during:** Task 2
- **Issue:** `autostart.Restart` returns early when the running daemon already reports the CLI's version and protocol. It was built for the version gate. Because of that, a user-run `pr-relay daemon restart` would have done nothing.
- **Fix:** the restart subcommand now calls `autostart.Stop` first and then `autostart.Restart`, which spawns a fresh daemon.
- **Files modified:** cmd/pr-relay/daemon_cmd.go
- **Commit:** 7fffe58

### Additions

- Added tests beyond the plan's list: TestSubCallsDaemon (params plus the stderr warning), TestUnsubArgs, TestKeepAndPollLocalValidation and TestDaemonCommands (stop, already-running and restart output, via injected funcs).

## TDD Gate Compliance

- RED: e4d25c5 (task 1) and bf804a4 (task 2), both `test(04-11)` commits that failed to compile against missing symbols.
- GREEN: 9a5975f (task 1) and 7fffe58 (task 2).

## Commits

- e4d25c5 test(04-11): add failing tests for CLI helpers and sub/unsub/keep/poll
- 9a5975f feat(04-11): add CLI helpers and sub/unsub/keep/poll commands
- bf804a4 test(04-11): add failing tests for ls, reset, daemon commands and root registration
- 7fffe58 feat(04-11): add ls, status, reset, daemon commands and register the CLI

## Self-Check: PASSED

All 11 listed files exist, and all four commits are present in `git log`.
