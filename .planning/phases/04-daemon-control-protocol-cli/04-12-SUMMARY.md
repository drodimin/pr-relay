---
phase: 04-daemon-control-protocol-cli
plan: 12
subsystem: testing
tags: [e2e, harness, autostart, version-gate, lifecycle, crash-safety]
requires:
  - 04-10 (daemon.Run, handlers, pid file, shutdown order)
  - 04-11 (CLI commands sub/unsub/keep/ls/status/daemon)
  - 03 herdrtest fake herdr
provides:
  - "cmd/pr-relay/harness_test.go: e2e harness v2 (newE2E, env, run/runBin/mustRun, eventually, waitPrompt, readState, autoIdle)"
  - "binOld test binary (main.version=0.0.1-old) built by TestMain"
  - "fakeGH.setMerged and fakeGH.addFailedCheck"
affects: [phase 05 plugin/skill work can reuse newE2E for install/startup tests]
tech-stack:
  added: []
  patterns:
    - "Every e2e test gets its own short socket dir (os.MkdirTemp(\"\", \"prr\")) and an env scrubbed of HERDR_/PR_RELAY_/GH_/GITHUB_/XDG_RUNTIME_DIR"
    - "CLI runs use cmd.Output with WaitDelay=1s, so a daemon that inherits stdout shows up as a hang/error"
    - "The autoIdle goroutine flips a working fake pane back to idle, standing in for an agent finishing its turn; tests that need delivery held turn it off"
key-files:
  created:
    - cmd/pr-relay/harness_test.go
    - cmd/pr-relay/e2e_test.go
    - cmd/pr-relay/lifecycle_e2e_test.go
  modified:
    - cmd/pr-relay/crash_test.go
    - cmd/pr-relay/fakegh_test.go
decisions:
  - "TestDowngradeError raises state.json's version to store.SchemaVersion+1 before running binOld. binOld is built from the same source, so it reads v3 state; SchemaVersion is a const and cannot be lowered with ldflags."
  - "Cleanup runs `daemon stop` and SIGKILLs the pid in daemon.pid only if that file is still there and the process is still alive. A daemon that stopped cleanly has already removed the file."
  - "TestCrashLifecycle uses checkin_after=3s so a second, legitimate check-in cannot be queued between the restart and the exactly-once count."
metrics:
  duration: ~25m
  completed: 2026-11-05
  tasks: 2
  files: 5
requirements: [SUB-01, SUB-02, SUB-03, SUB-05, SUB-06, SUB-07, SUB-09, DMN-01, DMN-05, DMN-06, DMN-07, DMN-08, LIFE-01, LIFE-06, LIFE-07]
---

# Phase 4 Plan 12: End-to-end phase gate Summary

This plan adds end-to-end tests that run the real `pr-relay` binary against a fake GitHub GraphQL server and a fake herdr on a scratch socket. Together they demonstrate all five Phase 4 ROADMAP success criteria. They also show that a lifecycle event survives a daemon SIGKILL.

## What was built

- **harness_test.go (e2e harness v2)**
  - `newE2E(t, extraCfg)` sets up an isolated machine: state dir, a short socket dir (`os.MkdirTemp("", "prr")`), a log dir, a config (fast poll, 200ms quiet period, plus any extra TOML), a fake GitHub and a scratch fake herdr with pane `w1:p1`.
  - `env(pane)` drops every inherited `HERDR_*`, `PR_RELAY_*`, `GH_*`, `GITHUB_*` and `XDG_RUNTIME_DIR` entry, then sets the test values: `PR_RELAY_SOCKET`, `PR_RELAY_LOG_DIR`, `PR_RELAY_CONFIG`, the GraphQL URL, `GH_TOKEN=test`, fast poll and the scratch `HERDR_SOCKET_PATH`.
  - `run`/`runBin` use `cmd.Output` with `WaitDelay`. This exposes the Pitfall 1 stdio-inheritance hang.
  - A cleanup step stops the daemon, then kills any daemon that is still alive, using the pid in `daemon.pid`.
  - The `autoIdle` loop returns a working pane to idle after each prompt.
- **e2e_test.go**
  - TestSubEndToEnd: a cold `sub` returns in under 10s with "subscribed o/r#12 events=all", and a later comment is typed into w1:p1.
  - TestSubErrors: covers running outside herdr (no socket is created), a not-found PR, an issue link, a repo link, a gitlab link and `--events bogus`.
  - TestSubEventsMask: with `--events ci`, ci.failed is delivered and the comment is not.
  - TestConcurrentAutostart: 20 parallel `status` calls all report one pid.
  - TestVersionMismatch: binOld starts the daemon and subscribes. The current `status` then prints exactly one "restarted daemon (0.0.1-old -> dev)" note, and `ls` still shows the subscription.
  - TestDowngradeError: binOld facing a newer-version state file fails with "newer pr-relay" and leaves the file untouched.
  - TestLsUnsubStatus: covers the pane-scoped and `--all` ls output, unsub and its idempotent repeat, `unsub --all` scoped to one pane, the status lines, and `daemon.log` existing with mode 0600.
  - TestClosedRemovalE2E: a merge is delivered as "closed: merged" to a comment-only subscription, which is then removed.
  - TestDaemonRestartCmd: 10 restarts, each followed by a successful `status`.
- **lifecycle_e2e_test.go: TestLifecycleE2E**
  - With checkin 2s and expire 6s, a check-in arrives that includes the keep and unsub commands.
  - `keep` prints "kept o/r#12", and no further check-in arrives within 1.5s.
  - The expired line arrives with the re-subscribe command. `ls --all` then prints "no subscriptions", and status lists "o/r#12 w1:p1 expired".
- **crash_test.go**
  - TestMain also builds `binOld` with `-ldflags "-X main.version=0.0.1-old"`.
  - The crash harness `env()` now also strips `HERDR_*` and `XDG_RUNTIME_DIR`.
  - TestCrashLifecycle: while the pane is working, the check-in stays pending in state.json. The test SIGKILLs the daemon and checks no prompt was sent, then sets the pane idle. `status` auto-restarts the daemon with a new pid, and the check-in is typed exactly once.

## Verification

- `go test ./cmd/pr-relay -run 'TestSubEndToEnd|...|TestDaemonRestartCmd' -count=1` passes, and also passes with `-race -v`.
- `go test ./cmd/pr-relay -run 'TestLifecycleE2E|TestCrash' -count=1 -v` passes: TestCrashLoop, TestCrashLifecycle and TestLifecycleE2E.
- The full gate, `go test -race -count=1 ./...`, passes in all packages.
- No pr-relay daemon processes remain after the runs (checked with `pgrep`).
- The acceptance greps match: `HasPrefix(kv, "HERDR_")` in harness_test.go, `main.version=0.0.1-old` in crash_test.go, `MkdirTemp("", "prr")` in harness_test.go, `func TestLifecycleE2E` and `func TestCrashLifecycle`.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] fakeGH could not express a merge or a new failing check**
- **Found during:** Task 1
- **Issue:** fakeGH always served OPEN PRs with only the template's checks, so TestSubEventsMask and TestClosedRemovalE2E could not be written.
- **Fix:** I added `merged` and `checks` fields to fakePR, plus `setMerged` and `addFailedCheck`. `prJSON` uses them to report state MERGED and to append CheckRun nodes. `fakegh_test.go` is not in the plan's file list.
- **Commit:** b524ff2

**2. [Rule 3 - Blocking] The downgrade scenario could not happen as written**
- **Found during:** Task 1
- **Issue:** binOld differs from the current build only in `main.version`, so it reads schema v3 without complaint. `store.SchemaVersion` is a const and ldflags cannot change it.
- **Fix:** after the current binary has written v3 state and the daemon has stopped, the test checks that the file is at `store.SchemaVersion`, then raises it by one. binOld's `status` must then fail with "newer pr-relay" and leave the file byte-for-byte unchanged. This still exercises the same ErrNewerVersion startup path, and the same error reaching the CLI over the ready pipe.
- **Commit:** b524ff2

## TDD Gate Compliance

Both tasks are `tdd="true"`, but they are test-only: they prove behaviour that plans 04-01 to 04-11 already implemented. There is no production code to add, so no RED-fails step was possible, and each task is a single `test(04-12)` commit. Every test passed on its first run.

## Known Stubs

None.

## Threat Flags

None. The mitigations for T-04-48, T-04-49 and T-04-50 are in harness_test.go:
- T-04-48 (scrubbed herdr env and a scratch socket)
- T-04-49 (daemon stop plus a pid kill on cleanup)
- T-04-50 (GH_TOKEN=test and the fake GraphQL server)

## Commits

| Task | Commit | Message |
|------|--------|---------|
| 1 | b524ff2 | test(04-12): e2e harness v2 and core CLI/daemon scenarios |
| 2 | 8adabba | test(04-12): lifecycle e2e and crash re-delivery of lifecycle events |

## Self-Check: PASSED

- FOUND: cmd/pr-relay/harness_test.go, e2e_test.go, lifecycle_e2e_test.go, crash_test.go, fakegh_test.go
- FOUND commits: b524ff2, 8adabba
