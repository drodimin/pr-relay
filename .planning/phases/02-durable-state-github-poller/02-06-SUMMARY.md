---
phase: 02-durable-state-github-poller
plan: 06
subsystem: store
tags: [persistence, atomic-write, flock, schema, outbox]
requires:
  - phase: 02-01
    provides: gofrs/flock pin, testutil.Golden
  - phase: 02-02
    provides: model.Memory/Event/PRRef json tags, Memory.Clone
provides:
  - internal/store State schema v1 (State, PRRecord, Subscription, PendingEvent)
  - NewState, State.Clone (deep), PRKey, SubID, SchemaVersion
  - Store with Open/Save/Close/Quarantine/Dir, ErrLocked, ErrNewerVersion
  - testdata/state-v1.json schema golden
affects: [02-09 engine, 02-10+ daemon wiring, status (quarantine info)]
tech-stack:
  added: []
  patterns: [single-file transactional outbox, copy-on-write state, temp+fsync+rename+dir fsync]
key-files:
  created:
    - internal/store/state.go
    - internal/store/state_test.go
    - internal/store/store.go
    - internal/store/store_test.go
    - internal/store/testdata/state-v1.json
  modified: []
decisions:
  - "Clone keeps nil slices nil (so DeepEqual round-trips hold) and turns nil maps empty"
  - "Version < 1 (incl. missing version) and a second-stage parse failure both quarantine; Version > SchemaVersion is refused and the lock released"
  - "Loaded state is normalized: nil maps become empty, null PR/sub entries are dropped, Memory maps made non-nil via Memory.Clone"
  - "Quarantine name collisions get a -N suffix so two corrupt files in one second never clobber each other"
  - "Save appends a trailing newline to MarshalIndent output (golden uses the same form)"
metrics:
  duration: ~25min (excluding usage-limit pause)
  completed: 2026-11-01
---

# Phase 2 Plan 06: Durable Store Summary

Single-file JSON state store: schema v1 with deep copy-on-write Clone, an exclusive flock on state.lock, quarantine of corrupt files to state.json.corrupt-<UTC>, refusal of newer schemas, and atomic Save (0600 temp, fsync, rename, directory fsync).

## Tasks

| Task | Name | Commits |
|------|------|---------|
| 1 | Schema v1 types, keys and deep Clone | 781f952 (test), 07bb2a8 (feat) |
| 2 | Open (lock, cleanup, load, quarantine) and atomic Save | da1f69d (test), 18ac300 (feat) |

## Verification

- `go test -race -count=1 ./internal/store/` passes (macOS)
- `go vet` clean, gofmt clean, `go build ./...` ok
- Golden `testdata/state-v1.json` loads DeepEqual and re-marshals byte-identically

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Correctness] Normalize loaded state**
- **Found during:** Task 2
- **Issue:** A hand-edited or older file could carry `null` PR/sub entries or nil Memory maps, which would cause nil dereferences or nil-map writes in the engine.
- **Fix:** `normalize` drops null entries and runs Memory.Clone so maps are non-nil.
- **Commit:** 18ac300

**2. [Rule 2 - Correctness] Quarantine path no-clobber**
- Appended a `-N` counter when a corrupt-<timestamp> name already exists (the plan mentioned this; covered by TestStoreQuarantineNoClobber).

Additional tests beyond the plan: TestStateCloneNil, TestStoreSaveIndented, TestStoreNoQuarantineWhenClean, TestStoreQuarantineNoClobber, TestStoreNilMapsBecomeEmpty, and quarantine subcases for a missing version and a bad field type.

### Notes

- Startup worktree check found the base at 5cd46a0 and reset to the expected base 860001b before any work (as the startup instructions say to do).
- `github.com/gofrs/flock` is still marked `// indirect` in go.mod. It is now a direct import, so `go mod tidy` will drop the marker. go.mod was left alone as instructed, and the build works.
- golangci-lint is not installed locally, so only `go vet` and gofmt were run.

## TDD Gate Compliance

RED commits (test) come before GREEN commits (feat) for both tasks. No refactor commits were needed.

## Threat Flags

None. All surface is covered by T-02-18 to T-02-22: dir 0700/files 0600, TryLock, quarantine, newer-version refusal, atomic save.

## Self-Check: PASSED

- All five files exist; commits 781f952, 07bb2a8, da1f69d and 18ac300 are present in git log.
