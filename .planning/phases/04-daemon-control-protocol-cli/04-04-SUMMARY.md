---
phase: 04-daemon-control-protocol-cli
plan: 04
subsystem: daemon
tags: [logging, slog, lumberjack, crash-capture, supply-chain]
requires: []
provides:
  - "daemon.OpenLog(dir) (*Log, error) with Logger(), Path(), Close()"
  - "daemon.SetCrash(dir) (*os.File, error) wired to debug.SetCrashOutput"
  - "internal/daemon package doc"
affects: [04-10, 04-11]
tech-stack:
  added: ["gopkg.in/natefinch/lumberjack.v2 v2.2.1"]
  patterns: ["pre-create log file 0600 so lumberjack's mode-copy on rotation keeps 0600"]
key-files:
  created:
    - internal/daemon/log.go
    - internal/daemon/log_test.go
  modified:
    - go.mod
    - go.sum
decisions:
  - "Lumberjack dependency approved by the user at the Task 1 checkpoint; the approved branch was taken (no in-repo rotator)"
  - "daemon.log is pre-created and tightened to 0600 (plus any daemon-*.log backups) before lumberjack opens it, because lumberjack copies the old file's mode onto each rotated file"
  - "Rotation size is the package var maxSizeMB (5) so tests can lower it to 1 MB; backups use lumberjack's daemon-<timestamp>.log naming"
metrics:
  duration: "~10 min"
  completed: 2026-11-04
  tasks: 2
  files: 4
---

# Phase 4 Plan 04: Rotating Daemon Log and Crash Capture Summary

The daemon's file logger is a slog TextHandler writing to lumberjack v2.2.1. It keeps `daemon.log` at mode 0600 in a 0700 directory, rotates at 5 MB and keeps 3 backups. `SetCrash` sends runtime panic output to a 0600 `crash.log` through `debug.SetCrashOutput`.

## Tasks

| Task | Name | Commit | Files |
| ---- | ---- | ------ | ----- |
| 1 | Verify lumberjack legitimacy (checkpoint:human-verify, blocking-human) | n/a (decision only) | none |
| 2 | Rotating daemon log and crash capture (TDD) | ad5f141 (RED), 729a04c (GREEN) | internal/daemon/log.go, internal/daemon/log_test.go, go.mod, go.sum |

## Checkpoint Decision (Task 1, T-04-SC)

- **User answer: "approved".**
- What the orchestrator checked before the user approved: proxy.golang.org lists v2.2.1 (2023-02-06, origin gopkg.in/natefinch/lumberjack.v2, hash 4cb27fcfbb0f35cb48c542c5ea80b7c1d18933d0). GitHub natefinch/lumberjack is MIT-licensed, not archived, has about 5.5k stars, and the v2.2.1 tag is present.
- **Branch taken: approved.** I ran `go get gopkg.in/natefinch/lumberjack.v2@v2.2.1`, and go.mod now lists it as a direct require. `go mod verify` reports all modules verified.

## What Was Built

- `OpenLog(dir)` creates the directory and tightens it to 0700. It pre-creates `daemon.log` at 0600, tightens a looser existing file and any backups, builds `&lumberjack.Logger{MaxSize: 5, MaxBackups: 3, Compress: false}`, and writes one startup line containing only the path and pid.
- `Logger()` returns a slog TextHandler at LevelInfo. `Path()` returns `dir/daemon.log`. `Close()` uses sync.Once, so calling it twice is safe.
- `SetCrash(dir)` opens `dir/crash.log` with O_CREATE|O_WRONLY|O_APPEND at 0600 and calls `debug.SetCrashOutput(f, debug.CrashOptions{})`.
- The package doc for `internal/daemon` says it hosts the poller engine, delivery manager, lifecycle ticker and control server in one per-user process.

## Verification

- `go test ./internal/daemon -run TestLogging -count=1 -v`: PASS. It has 4 subtests: modes and message, tightening a loose existing file, rotation capped at 1+3 files all 0600 with a double Close, and crash file at 0600 in a 0700 dir.
- `go test -race ./internal/daemon -count=10`: PASS
- `go build ./...` and `go test ./...`: PASS
- `go mod verify`: all modules verified

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Security] Tighten backups and a pre-existing log so rotated files stay 0600**
- **Found during:** Task 2
- **Issue:** lumberjack's `openNew` copies the existing file's mode onto the new file. A `daemon.log` left at 0644 by any earlier run would therefore pass 0644 on to every later rotation.
- **Fix:** OpenLog pre-creates `daemon.log` at 0600 and Chmods it, and also tightens any `daemon-*.log` backups. This is the plan's "Chmod after first write" requirement, done before lumberjack opens the file.
- **Files modified:** internal/daemon/log.go
- **Commit:** 729a04c

The plan named the test hook "MaxBytes". lumberjack sizes in whole megabytes, so the hook is the unexported `maxSizeMB` and the rotation test writes about 6 MB at a 1 MB threshold.

## TDD Gate Compliance

The RED commit (`test(04-04)`, ad5f141) comes before the GREEN commit (`feat(04-04)`, 729a04c). No refactor commit was needed.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: internal/daemon/log.go
- FOUND: internal/daemon/log_test.go
- FOUND: commit ad5f141
- FOUND: commit 729a04c
