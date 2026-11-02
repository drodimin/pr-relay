---
phase: 02-durable-state-github-poller
plan: 10
subsystem: poller
tags: [poller, scheduler, cadence, budget, backoff, auth, status, synctest, tdd]
requires:
  - 02-07 (ghclient typed errors, Invalidate, TokenSource)
  - 02-08 (BaseInterval, Effective, NextDue, CoalesceWindow, Budget)
  - 02-09 (Engine, pollRefs, Wake, fakeGH)
provides:
  - "(*Engine).Run scheduler loop"
  - "poller.Status, AuthState, AuthUnknown/AuthOK/AuthNoToken/AuthUnauthorized"
  - "(*Engine).Status, (*Engine).SetQuarantine"
  - "fakeGH extensions: pollTimes, requestTimes, requestCosts, set, churn, costFn, rejectToken, resetAt; swapTokens; newClientWith"
affects:
  - 02-11 dev command (runs Run, calls SetQuarantine after store.Open)
  - Phase 4 status command (Status)
tech-stack:
  added: []
  patterns:
    - "wall-clock due times recomputed every iteration, sleeps capped at 30s (pitfall 11)"
    - "stable per-PR jitter from an FNV hash of the PR key (no math/rand)"
    - "synctest bubbles with an in-memory RoundTripper for hours of fake time"
key-files:
  created:
    - internal/poller/scheduler.go
    - internal/poller/status.go
    - internal/poller/scheduler_test.go
  modified:
    - internal/poller/engine.go
    - internal/poller/fakegh_test.go
decisions:
  - "Coalescing: sleep until the earliest due time, then poll every PR due within its CoalesceWindow; sleeping to due-minus-window would shorten every single-PR interval by 15s"
  - "Once budget.ResetAt has passed, the scheduler treats Remaining as refilled to Limit, so a stale Remaining 0 does not stretch intervals to 60m"
  - "A second concurrent Run returns an error (atomic running flag) rather than racing"
  - "Unclassified poll errors (for example a Save failure, which leaves PRs due) pause the loop 30s; transient, rate-limit and auth errors rely on LastPollAt, the gate and the 5m auth retry"
  - "Auth state starts empty and Status reports it as unknown; Source comes from the fetcher's TokenSource() and never the token"
metrics:
  duration: ~35m
  completed: 2026-11-01
  tasks: 2
  files: 5
---

# Phase 2 Plan 10: Scheduler loop, budget wiring and Status Summary

`(*Engine).Run` is the single scheduler goroutine. Every iteration it reads wall-clock time and derives each watched PR's due time from its persisted LastPollAt and RateAnchor. The due time comes from the cadence curve, stretched by the budget and shortened by a stable per-PR jitter. Once a PR is due, Run polls it together with every PR due within its coalesce window, in chunks of the budget's batch size, one batch at a time. Over hours of synctest fake time the tests show the cadence ramp, one poll per PR per interval however many panes subscribe, the 10% own-spend cap, low-budget stretch, primary and secondary backoff, and token recovery without a restart.

## Tasks

| Task | Name | Commits |
|------|------|---------|
| 1 | Run loop, gating, auth retry and Status | 1f5eb32 (test), f3a6957 (feat) |
| 2 | synctest scheduler tests | 11a3a07 (fix), ae6c6d9 (test) |

## Behavior

- **Selection**: the scheduler polls only PRs that are non-terminal and have at least one subscription. A never-polled PR is due now. Stretch is `budget.Stretch(now, ProjectedPerHour(all bases))` and is stored in `e.stretch` under `e.mu`. Jitter is `-0.1 × (fnv64a(key) mod 1001)/1000`.
- **Gating**: `Budget.Gate(now, false)` runs before a batch and again after each chunk. The secondary limit, primary exhaustion and the own-spend cap each hold the loop, which sleeps in 30s steps.
- **Auth**: on `ErrNoToken` or `ErrUnauthorized` the scheduler sets the auth state, calls `Invalidate()`, and makes no further attempt for 5 minutes. A successful batch sets the state to `ok` and records the token source.
- **Sleep**: a `time.Timer` capped at 30s that ends early on `ctx.Done()` or `e.wake`. Subscribe of a new PR and ResetRate wake the loop.
- **Status** reports: auth state, source and since; limit, remaining and reset; spend in the last hour; stretch; secondary-until; batch size; PR and subscription counts; quarantine path and time; and per-PR last error classes.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] A stale Remaining 0 kept intervals at the 60m ceiling after the reset**
- **Found during:** Task 2 (TestBudgetPrimaryWait)
- **Issue:** After a primary exhaustion, `Budget.Remaining` stayed 0 until the next response. The low-budget stretch (1000/1) therefore pushed the next poll to about 60m, well after resetAt.
- **Fix:** `planLocked` sets Remaining to Limit once `now >= ResetAt`, because GitHub refills the window at resetAt. This change is in scheduler.go only; budget.go is untouched.
- **Commit:** 11a3a07

**2. [Rule 2] Hot-loop guard for unclassified errors.** A commit (Save) failure leaves PRs due immediately, so the loop pauses 30s after any error that is not transient, rate-limit or auth (T-02-37). Commit f3a6957.

**3. [Rule 2] Single Run guard.** A second concurrent `Run` returns an error. Commit f3a6957.

### Test adjustments

- `TestBudgetCapStretch` is named `TestBudgetCapStretchScheduler` because 02-08's pure `TestBudgetCapStretch` already exists. It still matches the `TestBudget` verify pattern.
- TestBudgetLowNeverStops checks that the first gap is stretched to about 3.3m and that polling continues for the whole hour. Gaps keep growing with the quiet ramp, so the test does not assert a fixed gap.
- TestBudgetPrimaryWait expects a 90s step after the reset, not 60s, because the PR has been quiet for 10m since its anchor.
- TestBudgetCapStretchScheduler uses `PendingCap = 3`, because no one drains the queues and this keeps state clones cheap. It takes about 3.7s under `-race` (about 1s without), which is over the "about 1s" target. The whole verify command runs in about 6s, well under 60s.
- Extra tests beyond the plan: TestRunStopsOnCancel, TestRunIdleNoRequests, TestRunWakesOnSubscribe, TestRunSkipsTerminalAndUnsubscribed, TestRunRejectsSecondRun and TestStatusQuarantine.

## TDD Gate Compliance

For Task 1, RED commit 1f5eb32 failed to build (`s.e.Run undefined`) and GREEN commit f3a6957 followed. Task 2 is a test task against existing behavior. Its tests found the refill bug, which was fixed in 11a3a07 before the test commit ae6c6d9.

## Verification

- `go test -race -count=1 ./...` passes. `go vet ./...` and `gofmt -l internal/` are clean.
- `grep -c 'synctest.Test' internal/poller/scheduler_test.go` gives 17. `httptest` does not appear in scheduler_test.go and `math/rand` does not appear in scheduler.go.
- `func (e *Engine) Run(ctx context.Context) error` and `30 * time.Second` are present in scheduler.go.
- go.mod is untouched.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: internal/poller/scheduler.go, status.go, scheduler_test.go
- FOUND commits: 1f5eb32, f3a6957, 11a3a07, ae6c6d9
