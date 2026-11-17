---
phase: quick-261116-qss
plan: 01
subsystem: lint / CI
tags: [golangci-lint, errcheck, staticcheck, ci, release-gate]
requires: []
provides:
  - "Code that passes golangci-lint v2.15.0 against the unchanged .golangci.yml"
affects: [ci, v0.1.0 release]
tech-stack:
  added: []
  patterns:
    - "Explicit ignore idiom: `_ = x.Close()` / `defer func() { _ = x.Close() }()` / `_, _ = fmt.Fprint...`"
key-files:
  created: []
  modified:
    - cmd/pr-relay/{daemon_cmd,dev,format,keep,ls,main,poll,reset,skill_cmd,status,sub,unsub}.go
    - internal/autostart/{autostart,ready,spawn_unix}.go
    - internal/ctl/{client,server}.go
    - internal/daemon/daemon.go
    - internal/herdr/{client,endpoint,stream}.go
    - internal/config/config.go
    - internal/ghclient/token.go
    - internal/ctl/ctl_test.go
    - internal/daemon/daemon_test.go
    - internal/herdr/client_test.go
    - internal/herdr/herdrtest/server_test.go
    - internal/paths/paths_test.go
    - internal/poller/engine_test.go
decisions:
  - "Unused channel receives of error values (`<-serveErr`, `case <-d.done:`) are consumed into a variable and logged when non-nil, because errcheck flags the bare receive and staticcheck S1005 flags `_ = <-ch`"
metrics:
  duration: ~10m
  completed: 2026-11-16
requirements: [QUICK-LINT-01]
---

# Quick 261116-qss: Fix golangci-lint issues blocking v0.1.0 Summary

All 62 golangci-lint v2.15.0 issues are fixed in code (60 errcheck, 2 staticcheck QF1008) with explicit-ignore idioms. No nolint directives were added and the lint config is unchanged. Lint now reports `0 issues.` and the race tests pass.

## Task 1: Local reproduction

- `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0` installed `/Users/drodi/go/bin/golangci-lint`, which reports version 2.15.0 (built with go1.27.3).
- `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0 ./...` reported **62 issues: 60 errcheck and 2 staticcheck**.
- Differences from lint-issues.txt (40 issues): 22 more sites, all the same kind of issue the file already lists. CI's default `max-same-issues` truncation probably hid them. They are:
  - Unchecked `fmt.Fprint*` in cmd/pr-relay dev.go (8), format.go (2), main.go, reset.go (2), status.go, sub.go (2), unsub.go (4), and skill_cmd.go:69
  - `f.Close()` on the sync-failure path at skill_cmd.go:109
- The listed daemon.go:327:2 (`<-serveErr`) and daemon_test.go:145:8 (`case <-d.done:`) locations are real. errcheck v2.15.0 flags discarded receives from `chan error`.

## Tasks 2 and 3: Fixes

- Fprint* writes to CLI writers now use `_, _ = fmt.Fprint...(...)`.
- Best-effort Close/Remove calls now use `_ = x.Close()` or `defer func() { _ = x.Close() }()`. The early-error-path closes in skill_cmd.go (chmod, write and sync failures) get the same treatment. The success-path `f.Close()` before `os.Rename` is still checked and returned (T-q-01).
- `SignalReady` keeps its signature. Only the deferred close is explicitly ignored.
- QF1008: `d.Duration.String()` becomes `d.String()`. No `Duration.String` method exists, so there is no recursion. `ee.ProcessState.String()` becomes `ee.String()`.
- Channel receives: `_ = <-ch` fails staticcheck S1005, so:
  - daemon.go: `if err := <-serveErr; err != nil { log.Debug("control socket serve returned after close", ...) }`. `ctl.Server.Serve` returns nil after Close, and the select branch re-queues nil, so this never fires in normal runs.
  - daemon_test.go cleanup: `case err := <-d.done: if err != nil { t.Logf(...) }`. This adds no assertion.

## Verification

- `golangci-lint run ./...` prints `0 issues.`
- `go test -race -count=1 ./...` passes in all packages.
- `gofmt -l .` prints nothing, and `go vet ./...` is clean.
- No `nolint` appears in any .go file. `.golangci.yml` and `.github/` are unchanged.
- `.planning/config.json` is still modified and unstaged, and `BRIEF.md` is still untracked.

## Commits

| Task | Commit | Message |
|------|--------|---------|
| 1 | (none) | Tool install and reproduction only |
| 2 | 795ade6 | fix(quick-261116-qss): handle unchecked errors in non-test code for golangci-lint |
| 3 | 731e224 | test(quick-261116-qss): handle unchecked errors in tests for golangci-lint |

## Deviations from Plan

**1. [Rule 3 - Blocking] 22 more issues beyond lint-issues.txt**
- **Fix:** Applied the same rules to the extra Fprint* sites and to skill_cmd.go:109. The Task 2 commit includes dev.go, format.go, main.go, reset.go, status.go, sub.go and unsub.go, which are not in the plan's files list.

**2. [Rule 3 - Blocking] errcheck/S1005 conflict on channel receives**
- **Issue:** The plan's `_ =` fallback for daemon.go:327 triggers staticcheck S1005, which also applies to the daemon_test.go:145 case.
- **Fix:** Bind the received error and log it when it is non-nil (Debug in the daemon, t.Logf in test cleanup). Output only changes if a late non-nil error occurs, which the current code never produces.

## Self-Check: PASSED

- Commits 795ade6 and 731e224 are in `git log`.
- `/Users/drodi/go/bin/golangci-lint` exists and reports 2.15.0.
