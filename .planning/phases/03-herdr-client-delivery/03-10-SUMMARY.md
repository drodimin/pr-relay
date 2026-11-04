---
phase: 03-herdr-client-delivery
plan: 10
subsystem: dev-tooling
tags: [scratch-guard, dev-deliver, live-test, safety, gap-closure]
gap_closure: true
requires: []
provides:
  - "internal/scratchguard.Check(path string) error (symlink-aware scratch socket guard)"
  - "pr-relay dev deliver --state-dir DIR (temp state by default, foreign-sub refusal, unsubscribe on exit)"
  - "TestLiveHerdr confirms delivered text via pane.read before the ack"
affects:
  - "03-12 live SC3 re-run (uses dev deliver and TestLiveHerdr)"
tech-stack:
  added: []
  patterns:
    - "Fail-closed path guard: lexical check, then EvalSymlinks on the parent and re-check; non-not-exist errors refuse"
    - "Dev tooling owns an isolated temp state dir by default and cleans up its own subscriptions"
key-files:
  created:
    - internal/scratchguard/scratchguard.go
    - internal/scratchguard/scratchguard_test.go
  modified:
    - cmd/pr-relay/dev.go
    - cmd/pr-relay/devdeliver_test.go
    - internal/deliver/live_test.go
    - internal/compose/compose.go
decisions:
  - "scratchguard.Check also compares against the symlink-resolved ~/.config/herdr/herdr.sock when that directory exists"
  - "dev deliver never falls back to paths.StateDir() or PR_RELAY_STATE_DIR; empty --state-dir means a fresh 0700 temp dir"
  - "Foreign-subscription check runs after the store opens and before herdr.NewClient, so a refusal makes zero herdr calls"
metrics:
  duration: "~25m"
  completed: 2026-11-03
  tasks: 3
  files: 6
requirements: [DLV-08, DLV-01, LIFE-02]
---

# Phase 3 Plan 10: Safe dev deliver and honest live test Summary

There is now one shared scratch guard, `internal/scratchguard`, and it resolves symlinks. `dev deliver` keeps its own state, either in an explicit `--state-dir` or in a temp dir it deletes on exit. It refuses to start, before any herdr call, if any subscription in that state has a socket that is not a scratch socket, and it unsubscribes its own subscriptions when it exits. TestLiveHerdr now passes only when the delivered PR ref is read back from the pane with `pane.read`.

## Tasks

| Task | Name | Commits | Files |
| ---- | ---- | ------- | ----- |
| 1 | Shared, symlink-aware scratch guard (IN-05) and compose `max` rename (IN-06) | 84ebaf8 (RED), 7928a62 (GREEN), 4497147 (refactor) | internal/scratchguard/*, internal/compose/compose.go |
| 2 | dev deliver: isolated state, foreign-sub refusal, unsubscribe on exit (CR-02) | 746c704 (RED), 9a3c516 (GREEN) | cmd/pr-relay/dev.go, cmd/pr-relay/devdeliver_test.go |
| 3 | TestLiveHerdr uses the shared guard and checks pane text with pane.read | dbcfb31 | internal/deliver/live_test.go |

## What changed

- **scratchguard.Check** refuses these paths: an empty path, the default `~/.config/herdr/herdr.sock` (both the literal path and its symlink-resolved form), and any path without `/sessions/`. When the parent directory exists, it resolves symlinks with `filepath.EvalSymlinks` and checks the resolved path against the same rules. If the parent does not exist, the lexical result stands. Any other resolution error, such as ENOTDIR, is a refusal. It never dials. Tests cover a `/sessions/` symlink to a non-scratch directory and a symlink to a stand-in `~/.config/herdr` (HOME is overridden).
- **dev.go** changes:
  - `guardScratchSocket` now calls `scratchguard.Check` and wraps the error as `dev deliver: ...`, so the existing error text still matches.
  - `setupDev` takes a `stateDir` argument. Empty means `paths.StateDir()`, and dev watch passes `""`, so dev watch is unchanged.
  - Without `--state-dir`, `runDevDeliver` creates a temp dir with `os.MkdirTemp`, sets it to 0700, prints `state dir <dir> (temporary)` to stderr and removes the dir after the store closes.
  - It checks every `env.eng.List()` subscription before the herdr client is built.
  - It records its own SubIDs and unsubscribes them in a defer. That defer runs after both Run goroutines return and before the store closes.
- **devdeliver_test.go** changes:
  - TestDevDeliver now passes `--state-dir` and checks the agent identity while the process is running. After exit it asserts that no w1:p1 subscription is left.
  - New test TestDevDeliverRefusesForeignSubs: it seeds a `dev watch` subscription with socket "dev". dev deliver must exit non-zero with "non-scratch subscription", and the fake herdr must receive zero calls.
  - New test TestDevDeliverTempStateDefault: dev deliver must ignore `PR_RELAY_STATE_DIR` and deliver exactly one prompt. The temp dir must be gone after SIGTERM, and the configured `state.json` must be byte-identical to before the run.
- **live_test.go** changes:
  - It uses `scratchguard.Check` and still fails rather than skips on a refused socket.
  - The PR number is unique per run.
  - It polls `pane.read` (`recent_unwrapped`, 400 lines) every 500ms for up to 2 minutes until `ref.String()` appears. On timeout it fails and reports the pending count, the ack log and the last 20 lines read.
  - Only after that does it wait up to 60s for the ack.
- **compose.go**: the local variable `max` is renamed to `limit`. Behaviour is unchanged.

## Verification

- `go build ./... && go vet ./...` passes.
- `go test -race ./cmd/pr-relay/ ./internal/scratchguard/ ./internal/compose/ -count=1` passes. The dev deliver tests passed 3 of 3 times with `-count=3`.
- `go test ./internal/deliver -run TestLiveHerdr -v` reports SKIP when the env var is unset. Setting `PR_RELAY_HERDR_SCRATCH_SOCKET=$HOME/.config/herdr/herdr.sock` makes it FAIL with the refusal text and no dial.
- RED was confirmed for both TDD tasks. Task 1 failed to compile with Check undefined. For Task 2, the new tests failed against the previous dev.go.

## Deviations from Plan

None. The plan was executed as written. One small addition: the scratch guard test file also covers a symlink to a stand-in `~/.config/herdr` and an ENOTDIR resolution error, on top of the cases listed in the plan.

## Deferred Issues

- `internal/deliver` TestUnreachableReachability failed once in one full `-race` run and then passed 5 of 5. This is the known jitter flake (WR-06) that plan 03-11 covers. This plan does not touch it.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: internal/scratchguard/scratchguard.go, internal/scratchguard/scratchguard_test.go
- FOUND commits: 84ebaf8, 7928a62, 4497147, 746c704, 9a3c516, dbcfb31
