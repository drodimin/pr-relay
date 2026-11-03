---
phase: 03-herdr-client-delivery
plan: 04
subsystem: poller
tags: [delivery-hooks, cadence, lifecycle]
requires: ["03-01 (store.Subscription.AgentKind)"]
provides:
  - "Engine.PendingWake() channel, separate from Wake()"
  - "Engine.PRView(key) (PRView, bool)"
  - "Engine.AckMany(map[string]uint64) error, one Save"
  - "Engine.Cancel(subID, reason), RecentCancellations(), Reason* constants, Status.Cancellations"
  - "Engine.SetPaneReachability(PaneRef, time.Time), UndeliverableAfter = 30m"
  - "SubRequest.AgentKind, Drain.DroppedSeq"
  - "cadence.NextDueBase pure helper"
affects: [delivery manager (03-05+), Phase 4 status]
tech-stack:
  added: []
  patterns: ["dedicated non-blocking cap-1 signal channel per consumer", "in-memory capped ring under e.mu", "base-interval override at both cadence call sites"]
key-files:
  created:
    - internal/poller/delivery_test.go
  modified:
    - internal/poller/engine.go
    - internal/poller/status.go
    - internal/poller/scheduler.go
    - internal/poller/cadence.go
    - internal/poller/cadence_test.go
    - internal/poller/scheduler_test.go
decisions:
  - "PendingWake also fires when pending-cap overflow sets a new drop marker, not only for gap-lost markers. Either way the sub has a new marker to deliver."
  - "Cancel holds e.mu across capture, mutateLocked and the ring append, so the logged PR, pane and socket always match the sub that was removed"
  - "SetPaneReachability pokes Wake only on the cannot-receive to receivable transition. Marking a pane unreachable does not wake the scheduler, because the 30-minute threshold is caught by the scheduler's 30s maxSleep re-plan"
metrics:
  duration: "~20m"
  completed: 2026-11-02
  tasks: 2
  files: 7
requirements: [DLV-04, DLV-07, LIFE-02, LIFE-04, LIFE-08]
---

# Phase 3 Plan 04: Engine delivery hooks and the LIFE-08 cadence override Summary

The poller Engine now has the copy-on-write hooks the delivery manager needs: a dedicated `PendingWake` signal, a `PRView` of current polled state, a single-save `AckMany`, and a logged `Cancel` that keeps a 50-entry ring. The scheduler also drops a PR to the 16-minute rate once all of its panes have been unable to receive for 30 minutes, and goes back to normal as soon as one pane can receive.

## Tasks

| Task | Name | Commits |
| ---- | ---- | ------- |
| 1 | PendingWake, PRView, AckMany, Cancel, AgentKind and Drain.DroppedSeq | 618310c (test), 2e4706e (feat) |
| 2 | SetPaneReachability and the LIFE-08 slowest-rate override | e1c992b (test), aecb4db (feat) |

## What was built

- `engine.go`:
  - `SubRequest.AgentKind` is set on both Subscribe branches.
  - `Drain.DroppedSeq` is filled by `Pending`.
  - `pendingWake` (cap 1) has `PendingWake()` and `pokePending()`. They fire after a successful commit save when a sub got new events, a cap-overflow drop marker or a gap-lost marker, and on the Subscribe refilter path. `Wake()` is untouched.
  - `PRView`.
  - `AckMany` shares an `ackSub` helper with `Ack`, skips unknown subs and saves nothing for an empty map.
  - `Cancel` reuses a new shared `unsubscribeIn` with `Unsubscribe` and logs `"subscription cancelled"` with sub, pr, pane, socket and reason. It appends to the ring capped at 50, and `RecentCancellations()` returns a copy.
  - New constants: `ReasonPaneClosed`, `ReasonPaneGone`, `ReasonIdentityMismatch` and `UndeliverableAfter`.
  - `SetPaneReachability` uses an in-memory `reach` map, and a pane becoming receivable again pokes the scheduler.
  - `undeliverableLocked` is applied in `scheduleLocked`, which covers Schedule, Subscribe info and commit reports.
- `scheduler.go` `planLocked`: when the PR is undeliverable, the cand's base becomes `maxI` (so the budget projection uses the slow base) and its due time comes from `NextDueBase`.
- `cadence.go`: `NextDueBase` is pure and is `lastPoll + Effective(base, ...)`, or the zero time when the PR has never been polled.
- `status.go`: `Status.Cancellations`.
- Tests:
  - `delivery_test.go`: `TestPendingWake`, `TestPendingWakeMarkerOnly`, `TestPRView`, `TestAckMany` (with a save-failure subtest), `TestAckManyDropMarker`, `TestCancel` (log attrs, ring cap and order, copy semantics, unknown id) and `TestSubscribeAgentKind`.
  - `cadence_test.go`: `TestNextDueBase`.
  - `scheduler_test.go`, using synctest: `TestUndeliverableCadence` checks normal gaps before 30m, 14m24s to 16m gaps after it, a 16m Schedule interval, and that after recovery the next poll comes no more than 6m after the last one. `TestUndeliverableNeedsEveryPane` covers one receivable pane, a pane with no entry and a stale entry for an unsubscribed pane. `TestSetPaneReachability` checks the 29m/30m boundary, the Schedule values and the Wake poke happening only on recovery.

## Verification

- `go test -race ./... -count=1` passes, including every Phase 2 poller test. `go vet ./...` is clean.
- All acceptance greps match. `pokePending()` appears 3 times in engine.go (the definition, commit and Subscribe), and `undeliverableLocked(` appears 3 times across scheduler.go and engine.go.

## Deviations from Plan

None that change behaviour. One small extension: the plan said to poke `PendingWake` "where appendPending returns > 0 and where gapLost sets a marker". `appendPending` can also set a new drop marker on cap overflow while adding nothing, for example when the queue is full of events that were already pending. That case also counts as `added` now, so a new marker is never left without a signal.

## TDD Gate Compliance

Both tasks have a RED `test(...)` commit, which failed to compile because the API did not exist yet, followed by a GREEN `feat(...)` commit. No refactor commit was needed.

## Threat Model

- T-03-12 is mitigated. `AckMany` runs through `mutate` (clone, apply, save, swap), and `TestAckMany/save_failure` proves the state is unchanged after a failed save.
- T-03-13 is mitigated. Every cancellation is logged at Info with all five keys and kept in the ring.
- T-03-14 is mitigated. The ring is capped at 50, receivable panes are deleted from the reach map, and neither is persisted.
- T-03-15 is mitigated. Undeliverable PRs drop to MaxInterval, and recovery pokes the scheduler.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: internal/poller/delivery_test.go, internal/poller/engine.go, internal/poller/scheduler.go, internal/poller/cadence.go
- FOUND commits: 618310c, 2e4706e, e1c992b, aecb4db
