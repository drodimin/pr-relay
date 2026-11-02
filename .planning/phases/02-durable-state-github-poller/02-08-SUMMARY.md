---
phase: 02-durable-state-github-poller
plan: 08
subsystem: poller
tags: [cadence, rate-limit, budget, pure-functions, tdd]
requires: []
provides:
  - "poller.BaseInterval / Effective / NextDue / CoalesceWindow (pure cadence curve)"
  - "poller.Budget, BudgetParams, NewBudget with Record/Spent/Observe/ProjectedPerHour/Stretch/Gate/OnSecondary/OnPrimaryExhausted/OnClean"
affects: [02-10 scheduler wiring]
tech-stack:
  added: []
  patterns: ["pure time functions: every time passed as an argument, no time.Now, no I/O"]
key-files:
  created:
    - internal/poller/doc.go
    - internal/poller/cadence.go
    - internal/poller/cadence_test.go
    - internal/poller/budget.go
    - internal/poller/budget_test.go
  modified: []
decisions:
  - "Step thresholds compared as quiet*1m < upTo*min (float products) instead of dividing by 1m, so scaled thresholds stay exact (100ms base hits 1s exactly)"
  - "Cap gate waits until enough oldest ledger entries age out to drop below the cap (a superset of 'oldest entry ages out')"
  - "Primary reset jitter is a multiplicative hash of ResetAt.UnixNano mod 5s, because GitHub's resetAt has whole-second precision and Nanosecond() alone would always give 0"
  - "Secondary wait on consecutive hits = max(RetryAfter-or-60s, min(2*lastWait, 15m)); every hit halves BatchSize (floor 1); every 5 consecutive clean batches double it toward MaxBatch"
metrics:
  duration: "~15m"
  completed: 2026-11-01
  tasks: 2
  files: 5
---

# Phase 2 Plan 08: Cadence Curve and Rate-Limit Budget Summary

The cadence ramp (1m up to a 16m ceiling over 4h, about 58 polls) and the D-05..D-07 rate-limit budget (own-spend cap stretch, low-budget stretch, primary and secondary gates, batch halving and restore) are now pure, table-tested functions in `internal/poller`.

## What was built

- **cadence.go**: a `steps` table relative to a 1-minute base, scaled by `min`. `BaseInterval(quiet, min, max)` gives 1m, 90s, 2m, 3m, 4m, 6m, 8m, 11m, then 16m from 240m on. Negative quiet (clock skew) counts as active. `Effective` ignores stretch below 1, caps a stretched interval at `max(maxStretched, base)`, and clamps jitter to [-10%, 0]. `NextDue` returns the zero time for a PR that has never been polled. `CoalesceWindow` is `min(15s, interval/4)`.
- **budget.go**: a 60-minute ledger that is pruned on `Record`, and a per-PR cost EWMA (starting at 0.25, alpha 0.3). `Stretch = max(capStretch, lowStretch)` and stays finite at remaining 0. `Gate` checks in this order: secondary limit, then primary exhausted (plus a deterministic jitter of at most 5s), then the cap for non-forced polls only. `OnSecondary`, `OnPrimaryExhausted` and `OnClean` implement D-07.
- **doc.go**: the package doc lists every rule with its decision ID and states that the two files are pure.

## Verification

- `go test -race -count=1 ./internal/poller/` passes: 15 tests.
- `go vet` is clean.
- `grep time.Now` finds nothing in cadence.go or budget.go.
- The `240 * time.Minute` and `15 * time.Minute` literals are present.
- The 4h quiet simulation gives 58 polls (the required range is 55..59), and the interval after 4h is 16m.

## TDD Gate Compliance

- RED `4632003` test(02-08): cadence, then GREEN `0be3d0d` feat(02-08)
- RED `bbccf2f` test(02-08): budget, then GREEN `18fa790` feat(02-08)
- No refactor commits were needed.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Float division made scaled thresholds inexact**
- **Found during:** Task 1 GREEN
- **Issue:** The planned `scale = min/1m` and the check `quiet < upTo*scale` gave 10m×(100ms/1m) ≈ 0.999…s. So at min=100ms, `BaseInterval(1s)` returned the first step instead of the second.
- **Fix:** Compare `quiet*1m < upTo*min` as float products. These are exact for these magnitudes and cannot overflow int64.
- **Files modified:** internal/poller/cadence.go
- **Commit:** 0be3d0d

**2. [Rule 1 - Bug] Reset jitter from Nanosecond() alone is always 0 for GitHub resetAt**
- **Found during:** Task 2
- **Issue:** GitHub reports resetAt in whole seconds, so jitter based only on nanoseconds would never spread retries.
- **Fix:** The jitter is now a multiplicative hash of `ResetAt.UnixNano()` mod 5s. It is still deterministic and stays at or below 5s.
- **Files modified:** internal/poller/budget.go
- **Commit:** 18fa790

## Known Stubs

None.

## Self-Check: PASSED

- All 5 created files exist.
- Commits 4632003, 0be3d0d, bbccf2f and 18fa790 are present in git log.
