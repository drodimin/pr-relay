---
phase: 02-durable-state-github-poller
plan: 11
subsystem: cli
tags: [cli, cobra, dev-harness, crash-test, e2e, sink, go-mod]
requires:
  - 02-04 (render.Lines)
  - 02-06 (store.Open, ErrLocked, Quarantine)
  - 02-07 (ghclient.New loopback-only endpoint override, TokenSource)
  - 02-10 (Engine.Run, SetQuarantine)
provides:
  - "cobra root `pr-relay` with `version` and hidden `dev watch`"
  - "idempotent seq sink (cmd/pr-relay/sink.go)"
  - "subprocess e2e harness: TestMain binary build, fakeGH loopback server, TestDevWatchOutput, TestCrashLoop"
affects:
  - Phase 3/4 CLI commands attach to newRootCmd
tech-stack:
  added: []
  patterns:
    - "deliver = sink append + fsync, then Ack; on startup Ack the sink's max seq"
    - "subprocess tests isolated by PR_RELAY_STATE_DIR, --config, GH_TOKEN=test and a loopback GraphQL URL"
key-files:
  created:
    - cmd/pr-relay/dev.go
    - cmd/pr-relay/sink.go
    - cmd/pr-relay/fakegh_test.go
    - cmd/pr-relay/devwatch_test.go
    - cmd/pr-relay/crash_test.go
  modified:
    - cmd/pr-relay/main.go
    - go.mod
    - go.sum
decisions:
  - "The sink truncates a torn trailing line on open, so an append after a killed write starts on a fresh line"
  - "Half of the crash-loop kills are targeted: they fire when a rendered line appears, which falls between the sink fsync and the Ack"
  - "The crash loop keeps iterating until the comment driver finishes, so -short runs more than 10 iterations (about 30) and still takes about 8s"
  - "The test binary is built without -race; the race detector covers the test process and the in-package tests"
metrics:
  duration: ~45m
  completed: 2026-11-02
  tasks: 2
  files: 8
---

# Phase 2 Plan 11: Binary wiring, dev watch and the SIGKILL crash loop Summary

`pr-relay` now has a cobra root with `version` and a hidden `dev watch <PR-URL>...` command. `dev watch` runs the real store, ghclient and poller in the foreground: it prints each new event, one poll line with GraphQL cost and remaining budget, and each PR's interval and next poll time. A subprocess test SIGKILLs the real binary 50 times while comments keep arriving and restarts it each time. Every comment still reaches the sink exactly once with one seq, and no pending events or temp files are left behind (ROADMAP SC-1).

## Tasks

| Task | Name | Commit |
|------|------|--------|
| 1 | cobra root, hidden dev watch, idempotent sink, go mod tidy | c2c764d |
| 2 | dev watch output test and SIGKILL crash loop | 2ba6865 |

## Behavior

- **Root**: SilenceUsage and SilenceErrors are set. A failure prints `pr-relay: <err>` to stderr and exits 1. `--help` lists only completion, help and version.
- **dev watch**: every argument goes through `model.ParsePRURL`. The config path is `--config` or `paths.ConfigPath()`, and config warnings go to stderr. `store.ErrLocked` becomes `another pr-relay is using <dir>`. A quarantined state file is logged and passed to `SetQuarantine`. The endpoint comes from `PR_RELAY_GITHUB_GRAPHQL_URL`, which is test-only, and ghclient enforces loopback. Tokens come from `&ghclient.TokenSource{}`. Each ref is subscribed as (`dev`, `dev-watch`) with all event types, so a restart upserts. The subscriptions are drained once before `Run` under `signal.NotifyContext(SIGINT, SIGTERM)`.
- **Delivery order**: Pending, then sink write with fsync, then stdout lines, then `Ack(UpToSeq)`. On startup the command acks up to the sink's max seq first, so an event that reached the sink but was never acked before a kill is cleared rather than re-emitted.
- **Output**: `poll prs=<n> cost=<c> remaining=<r>/<limit> reset=<RFC3339> forced=<bool>`, then for each PR `  <ref> new=<n> interval=<d> next=<RFC3339>`, plus `err=<code>` when set. Only rendered (sanitized) lines and counts are printed, never tokens or bodies (T-02-41).
- **go.mod**: `go mod tidy` moved go-gh v2.17.0, cobra v1.10.2, BurntSushi/toml v1.6.0, gofrs/flock v0.13.1 and x/text v0.43.0 to direct requirements without changing any version. Tidy also added `github.com/kr/pretty v0.3.1 // indirect`, a test-only transitive dependency of a dependency.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Correctness] The crash test could not catch a broken sink**
- **Found during:** Task 2 (mutation check)
- **Issue:** I disabled both the startup Ack and the sink's seq skip, and the 50-iteration random-kill loop still passed. Random kills at 0..400ms almost never landed in the few milliseconds between the sink fsync and the Ack.
- **Fix:** Every other iteration now kills the process as soon as a rendered `[pr-relay] ` line appears on stdout (600ms timeout). That line is printed after the sink fsync and before the Ack. With the same mutation the test now fails with duplicate deliveries (for example `comment:c35 delivered 4 times`). With the real code it passes. In -short runs about half the kills land right after a delivery.
- **Files modified:** cmd/pr-relay/crash_test.go
- **Commit:** 2ba6865

**2. [Rule 2] Torn sink line handling.** A kill during a sink append could leave a partial last line, and the next append would join onto it. `openSink` now truncates to the last newline. Commit c2c764d.

### Other notes

- I simplified the invalid-URL error to `ParsePRURL`'s own error, which already quotes the URL.
- The fake strips timeline items and forces the template PR to OPEN, so the only changes between polls are the comments the test adds.

## TDD Gate Compliance

Task 2 tests the behavior Task 1 had already committed, so there was no failing RED commit. Instead, the mutation check above stands in for the RED gate: with idempotency disabled, TestCrashLoop fails.

## Verification

- `go build ./... && go vet ./...` is clean, and `gofmt -l cmd` prints nothing.
- `go run ./cmd/pr-relay version` prints `pr-relay dev`, and `--help | grep -c dev` gives 0. `dev watch https://github.com/o/../pull/1` exits 1 with an error that names the URL.
- `go test -race -count=1 ./...` passes. TestCrashLoop with -race ran 50 kills (about 12s), and TestDevWatchOutput took about 2.5s.
- `go test -count=1 -short -run TestCrashLoop ./cmd/pr-relay/` passes in about 8s.
- These greps all match: `Hidden: true` and `signal.NotifyContext` in dev.go, `Process.Kill()` and `testing.Short()` in crash_test.go, and the five direct requirements in go.mod.
- golangci-lint is not installed locally, so I did not run it. CI will run it.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: cmd/pr-relay/main.go, dev.go, sink.go, fakegh_test.go, devwatch_test.go, crash_test.go
- FOUND commits: c2c764d, 2ba6865
