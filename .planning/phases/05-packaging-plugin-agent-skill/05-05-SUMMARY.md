---
phase: 05-packaging-plugin-agent-skill
plan: 05
subsystem: cli
tags: [version, daemon, cli, autostart]
requires:
  - "05-03: autostart.AlreadyRunningMsg, bounded probe and ensureUp"
  - "05-02: lock-first daemon startup"
provides:
  - "resolveVersion: ldflags, then debug.ReadBuildInfo module version, then dev"
  - "pr-relay daemon --ensure for the herdr [[startup]] hook"
affects:
  - "plan 05-06 run.sh / herdr-plugin.toml (calls daemon --ensure)"
  - "plan 05-09 GoReleaser ldflags (-X main.version=v{{ .Version }})"
tech-stack:
  added: []
  patterns:
    - "init() resolves a package var after ldflags assignment"
key-files:
  created:
    - cmd/pr-relay/version.go
    - cmd/pr-relay/version_test.go
    - cmd/pr-relay/daemon_cmd_test.go
  modified:
    - cmd/pr-relay/daemon_cmd.go
    - cmd/pr-relay/e2e_test.go
    - cmd/pr-relay/crash_test.go
decisions:
  - "A VCS pseudo-version from build info is reported unchanged. Only empty or (devel) falls back to dev"
  - "runEnsure does not call hello when ensure fails, and prints nothing on any error"
metrics:
  duration: "~10m"
  completed: 2026-11-05
  tasks: 2
  files: 6
---

# Phase 5 Plan 05: Version Fallback and daemon --ensure Summary

A `go install ...@v0.1.0` build now reports `pr-relay v0.1.0` by reading its module build info. `pr-relay daemon --ensure` starts a background daemon or confirms one is running, prints `daemon running (pid N, VERSION)` and returns. It does this entirely through `autostart.Restart` and `autostart.Call`.

## Tasks

| Task | Name | Commits | Files |
| ---- | ---- | ------- | ----- |
| 1 | Version resolution with debug.ReadBuildInfo fallback | 01afffa (test), 4da36b9 (feat) | cmd/pr-relay/version.go, version_test.go, crash_test.go |
| 2 | `pr-relay daemon --ensure` | 2427589 (test), a9283a6 (feat) | cmd/pr-relay/daemon_cmd.go, daemon_cmd_test.go, e2e_test.go |

## What changed

- `version.go`: `resolveVersion(ld, info, ok)` returns the ldflags value unless it is empty or `dev`. Otherwise it returns `info.Main.Version` unless that is empty or `(devel)`. Otherwise it returns `dev`. `init()` assigns the result to `version`, so every reader sees it before cobra runs. `main.go` was not touched.
- `crash_test.go` TestMain: both `go build` calls now pass `-buildvcs=false`. Without it, VCS stamping gives binPath a pseudo-version, which would break e2e comparisons against the in-process `version` (`dev`).
- `daemon_cmd.go`: a new `--ensure` flag, with Use changed to `daemon [--ensure]`. `runEnsure` mirrors `runDaemonRestart`: it runs under `withTimeout(ctx, 2*callTimeout)`, calls ensure, then hello, then prints one line. Plain `daemon` still runs in the foreground. No spawn, pid or flock logic was added (grep count is 0).

## Verification

- `go test ./cmd/pr-relay -run 'TestRunEnsure|TestAlreadyRunningMsgMatches|TestDaemonEnsure|TestDaemonRestartCmd' -race -count=1 -v`: all pass, including the three TestDaemonEnsure subtests (spawns when down, no-op when running with the pid unchanged, restarts a mismatched version).
- `go test -race -count=1 ./cmd/pr-relay ./internal/autostart`: ok. `go vet ./cmd/pr-relay`: clean.
- `go build -ldflags "-X main.version=v9.9.9"` prints `pr-relay v9.9.9`. `go build -buildvcs=false` prints `pr-relay dev`.
- RED was observed for both tasks: the build failed on the undefined `resolveVersion` and `runEnsure`.
- `TestAlreadyRunningMsgMatches` is the guard that wave 1 (05-03) asked for.

## Deviations from Plan

None. The plan was executed as written. I added one small test that is not in the plan: TestRunEnsure checks that ensure receives a ctx with a deadline (T-05-13).

## TDD Gate Compliance

Each task has a `test(05-05)` RED commit followed by a `feat(05-05)` GREEN commit. No refactor was needed.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: cmd/pr-relay/version.go, version_test.go, daemon_cmd_test.go
- FOUND commits: 01afffa, 4da36b9, 2427589, a9283a6
