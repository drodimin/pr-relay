---
phase: 04-daemon-control-protocol-cli
plan: 09
subsystem: poller-lifecycle
tags: [poller, lifecycle, checkin, expiry, outbox]
requires:
  - 04-01 (model.EventCheckin / EventExpired, Event.IdleSecs, config.Lifecycle)
  - 04-05 (Subscription.CheckinAt / Ending / EndSeq, ackSub removal, ReasonExpired)
provides:
  - "Engine.LifecycleTick(now) error"
  - "Engine.RunLifecycle(ctx, grace, every) error"
  - "Engine.LifecycleDue(sub) (checkinDue, expiresAt time.Time)"
affects: [04-10 daemon wiring, ls due times]
tech-stack:
  added: []
  patterns:
    - "Lifecycle events appended directly into the outbox inside one mutate, bypassing mask and pending cap"
    - "Package-level sync.Map run guard keyed by *Engine, so engine.go is untouched"
key-files:
  created:
    - internal/poller/lifecycle.go
    - internal/poller/lifecycle_test.go
  modified: []
decisions:
  - "RunLifecycle uses its own lifeSleep instead of Engine.sleep. Engine.sleep caps every wait at maxSleep (30s), which would turn a 10m grace into 30s, and it reads e.wake, so it would steal wakes meant for the scheduler."
  - "Subs are walked in sorted ID order inside the tick, so Seq numbering is deterministic when several subs become due together."
  - "Expiry keeps pending real events and drops only check-ins; the expired line is queued after them and becomes EndSeq."
  - "A non-positive every falls back to 1 minute."
metrics:
  duration: "~12 min"
  completed: 2026-11-04
  tasks: 2
  files: 2
requirements: [LIFE-06, LIFE-07]
---

# Phase 4 Plan 09: Check-in and expiry lifecycle clock Summary

The check-in/expiry clock now runs as a lifecycle pass inside `poller.Engine`. It queues synthetic `checkin` and `expired` events into the crash-safe outbox through `mutate`. A startup grace keeps a machine that was off for a long time from mass-expiring its subscriptions.

## What was built

- **`LifecycleTick(now)`**: first checks, under `e.mu`, whether anything is due, and returns without saving if nothing is. Otherwise it runs one `mutateLocked`:
  - **Check-in** (idle >= `Lifecycle.CheckinAfter`, and the last check-in is at least that old): drops any pending check-in, appends a new one with Key `checkin:<unix>` and IdleSecs set to the threshold, and sets `CheckinAt = now`. Check-ins repeat and never stack (D-13).
  - **Expiry** (idle >= `Lifecycle.ExpireAfter`): drops pending check-ins, appends `expired:<unix>` with IdleSecs set to the threshold, and sets `Ending="expired"` and `EndSeq` to that event's Seq. The sub stops counting for polling and status at once, and the existing ack path deletes it with reason `expired`.
  - Ending subs are skipped. After a change it logs "check-in queued" or "subscription expiring" (sub, pr, pane, socket) and calls `pokePending()`.
- **`LifecycleDue(sub)`**: returns `max(ClockAt, CheckinAt)+checkin` and `ClockAt+expire`. Both are zero for an ending sub.
- **`RunLifecycle(ctx, grace, every)`**: waits `grace`, then ticks every `every` using `e.now().Round(0)`. A failed tick is logged as "lifecycle tick failed" and the loop keeps running. A second concurrent call returns `poller: lifecycle already running`. Cancelling ctx returns nil.

## Commits

| Task | Gate | Commit | Message |
|------|------|--------|---------|
| 1 | RED | 2a1993e | test(04-09): add failing tests for check-in and expiry lifecycle tick |
| 1 | GREEN | a9f1f04 | feat(04-09): lifecycle tick queues check-ins and expiry through the outbox |
| 2 | RED | a982480 | test(04-09): add failing tests for RunLifecycle grace, ticks and guard |
| 2 | GREEN | e1a42cd | feat(04-09): RunLifecycle loop with startup grace and run guard |

## Verification

- `go test ./internal/poller -run 'TestLifecycle|TestExpiry|TestInactiveSlowestTier' -count=1 -race -v`: all pass (TestLifecycleCheckin, Repeat, KeepResets, AckDoesNotReset, Due, TestExpiry, TestExpiryKeepsRealEvents, TestExpiryPaneGone, TestInactiveSlowestTier).
- `go test ./internal/poller -run TestRunLifecycle -count=1 -race -v`: Grace, Ticks, KeepsWake, SaveError and SecondRun all pass (synctest).
- `go test -race -count=1 ./...`: every package passes. `go vet` and `gofmt -l` are clean.
- All acceptance greps match: the `LifecycleTick`, `LifecycleDue` and `RunLifecycle` signatures, `Lifecycle.CheckinAfter`, and `lifeRunning sync.Map`.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] `Engine.sleep` is unsafe for the lifecycle loop**
- **Found during:** Task 2
- **Issue:** The plan said to use `e.sleep(ctx, d)`. That function caps every wait at `maxSleep` (30s), so a 10m grace would end after 30s and the expiry-storm guard would be defeated. It also selects on `e.wake`, the scheduler's wake channel. The lifecycle loop would then swallow wakes from Subscribe and ResetRate, delaying a new PR's first poll by up to 30s.
- **Fix:** Added a private `lifeSleep(ctx, d)` in lifecycle.go that only waits on a timer or ctx. Added `TestRunLifecycleKeepsWake` and `TestRunLifecycleSaveError` to cover this behaviour.
- **Files modified:** internal/poller/lifecycle.go, internal/poller/lifecycle_test.go
- **Commit:** e1a42cd

No files owned by 04-08 (engine.go, subscribe.go) were touched. New test helpers use the `lifeT` prefix.

## TDD Gate Compliance

Both tasks have a `test(...)` RED commit (a compile failure on the missing methods) followed by a `feat(...)` GREEN commit. No refactor commits were needed.

## Notes for downstream plans

- 04-10 should start `go e.RunLifecycle(ctx, 10*time.Minute, time.Minute)`, or use grace 0 and every 50ms under `config.EnvFastPoll`.
- `ls` can call `LifecycleDue(sub)` for each entry from `List()`.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: internal/poller/lifecycle.go
- FOUND: internal/poller/lifecycle_test.go
- FOUND commits: 2a1993e, a9f1f04, a982480, e1a42cd
