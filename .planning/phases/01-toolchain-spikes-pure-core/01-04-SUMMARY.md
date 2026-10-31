---
phase: 01-toolchain-spikes-pure-core
plan: 04
subsystem: diff
tags: [diff, state-machine, ci, lifecycle, pure-core]
requires:
  - phase: 01-02
    provides: model.Snapshot, model.Memory, model.Event, EventType.Rank
provides:
  - diff.Step / diff.Baseline / diff.Result
  - diff.IsFailed conclusion mapping
  - lifecycle events (closed, conflict, commits/force-push)
  - CI events (ci.failed, ci.settled)
affects: [01-06 (adds stepActivity), 01-09 (fixture scenarios), phase 2 (persists Memory, outbox dedupes on Keys)]
tech-stack:
  added: []
  patterns: [pure step over explicit Memory on a deep clone, two-snapshot fingerprint confirmation]
key-files:
  created:
    - internal/diff/diff.go
    - internal/diff/lifecycle.go
    - internal/diff/ci.go
    - internal/diff/snap_test.go
    - internal/diff/diff_test.go
    - internal/diff/lifecycle_test.go
    - internal/diff/ci_test.go
  modified: []
decisions:
  - "ci.settled requires two consecutive all-COMPLETED snapshots with an identical key@databaseId@conclusion fingerprint. Baseline adopts both the candidate and the result silently"
  - "Conflict detection is skipped entirely (memory untouched) while the snapshot State is not OPEN"
  - "A head change with a known old head and no force-push event computes Count and Authors from the commits after the old head. A force push keeps Count when the old head is still listed, else 0 (unknown)"
metrics:
  duration: ~15m
  completed: 2026-10-31
  tasks: 2
  files: 7
---

# Phase 1 Plan 04: Diff state machine core (lifecycle + CI) Summary

`internal/diff` is a pure, clock-free step function. `Step(mem, snap)` runs on `Memory.Clone()` and emits closed, conflict (UNKNOWN-tolerant, D-21), commits/force-push, ci.failed (once per key per SHA) and ci.settled (two-snapshot confirmation, D-19 re-arm). Events come back sorted by Rank and then by Key, and 27 named unit tests cover the behaviour.

## Tasks

| Task | Name | Commits |
|------|------|---------|
| 1 | Step/Baseline orchestration and lifecycle events | defe6bf (test), 5e5cff7 (feat) |
| 2 | CI state machine and conclusion mapping | 98ee467 (test), 56449fa (feat) |

## Verification

- `go vet ./internal/diff/` is clean.
- `go test -race -count=1 ./internal/diff/` gives 27/27 PASS.
- Non-test diff files import no os, time, net or io.
- `stepCI(` is called in diff.go. The `// activity: reviews and comments (plan 01-06)` call site is in place.

## Deviations from Plan

**1. [Rule 1 - Spec inconsistency] TestEventOrder split into a real step plus a direct sort check**
- **Found during:** Task 1
- **Issue:** The behavior asked for one Step that produces closed + commits + conflict together. It also requires "no conflict events while State != OPEN". A closing step can never emit conflict, so no single step satisfies both.
- **Fix:** TestEventOrder checks a real Step that emits conflict + commits in Rank order. It then checks the unexported `sortEvents` helper on a shuffled list (closed, conflict, commits, plus two ci.failed events to cover the tie broken by Key).
- **Files:** internal/diff/diff_test.go, internal/diff/diff.go
- **Commit:** defe6bf / 5e5cff7

No other deviations.

## TDD Gate Compliance

Each task has a RED `test(01-04)` commit (it fails to compile because the symbols are not defined yet) followed by a GREEN `feat(01-04)` commit. No refactor commits were needed.

## Known Stubs

None. `Result.Gap` is always false for now. Plan 01-06 sets it when it adds stepActivity, as the plan intends.

## Self-Check: PASSED

- All 7 files exist under internal/diff/.
- Commits defe6bf, 5e5cff7, 98ee467 and 56449fa are present on the branch.
