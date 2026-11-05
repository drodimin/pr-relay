---
phase: 04-daemon-control-protocol-cli
plan: 05
subsystem: poller-outbox
tags: [store, migration, lifecycle, outbox, poller]
requires:
  - 04-01 (model.EventCheckin / EventExpired / IsLifecycle)
provides:
  - store.SchemaVersion 3 with Subscription.LastEventAt, LastEventType, CheckinAt, Ending, EndSeq
  - stepwise additive migrate (1->2->3)
  - poller.ReasonClosed / ReasonExpired, poller.ErrEnded
  - ackSub(sub, upToSeq, now) (ended bool), with removal-on-ack in Ack/AckMany
  - closed and lifecycle mask bypass in maskAllows
  - ending subs excluded from planning, co-subscribers, undeliverable checks and Status.Subs
affects: [04-08 subscribe path, 04-09 lifecycle tick, ls last=, status]
tech-stack:
  added: []
  patterns:
    - "Ack-driven removal inside the same mutate/Save as the ack, with ring append under the same lock"
    - "omitzero for time.Time fields so zero lifecycle times are absent from state.json"
key-files:
  created:
    - internal/poller/ending_test.go
    - internal/store/testdata/state-v3.json
  modified:
    - internal/store/state.go
    - internal/store/store.go
    - internal/store/store_test.go
    - internal/store/state_test.go
    - internal/poller/engine.go
    - internal/poller/fanout.go
    - internal/poller/scheduler.go
    - internal/poller/status.go
    - internal/poller/engine_test.go
    - internal/poller/fanout_test.go
decisions:
  - "time.Time lifecycle fields use `omitzero`, not `omitempty`. encoding/json never omits a zero struct with omitempty, and go.mod is at 1.26, which supports omitzero."
  - "testdata/state-v2.json is now a frozen v2 fixture for TestMigrateV2ToV3. The current-schema golden moved to state-v3.json (TestStateV3Golden)."
  - "AckMany walks subscription IDs in sorted order, so the cancellation ring order is deterministic when several subs end in one save."
  - "markEnding uses the first pending closed event's Seq as EndSeq, and acks covering it end the sub even if later events are still queued."
metrics:
  duration: "~15 min"
  completed: 2026-11-04
  tasks: 2
  files: 12
requirements: [LIFE-01, LIFE-06, LIFE-07, SUB-02]
---

# Phase 4 Plan 05: Outbox lifecycle rules (schema v3, closed bypass, removal on ack) Summary

This plan moves the "remove after delivery" lifecycle into the crash-safe outbox. State schema v3 adds the lifecycle fields and migrates older files additively. A `closed` event now always passes the mask and marks the subscription as ending. Acking that final event deletes the subscription in the same save and records a `closed`/`expired` cancellation. Acked real events restart the inactivity clock (D-11), and `keep` answers a check-in or refuses an ending sub.

## What was built

- **store**: `SchemaVersion = 3`. Subscription gains `LastEventAt`, `LastEventType`, `CheckinAt`, `Ending`, `EndSeq`, each omitted from JSON when zero. `migrate` loops version steps: 1->2 and 2->3 each rewrite only the `version` key, and errors read `store: migrate: vN: ...`. A version-4 file returns `ErrNewerVersion` and is left untouched.
- **fanout**: `maskAllows` returns true first for `EventClosed` and any `IsLifecycle()` type, so `refilter` keeps them too.
- **engine**:
  - `commit` skips ending subs entirely (no new events, no gap marker). After `appendPending`, `markEnding` sets `Ending="closed"` and `EndSeq` to the queued closed event's Seq.
  - `ackSub(sub, upToSeq, now) (ended bool)`: when an acked event is non-lifecycle, sets `ClockAt = LastEventAt = now` and `LastEventType`. It returns `EndSeq != 0 && EndSeq <= upToSeq`.
  - `Ack` and `AckMany` take `e.mu`, call `mutateLocked`, and remove ended subs with `unsubscribeIn` in the same save. Once the save succeeds they log "subscription ended" and append to the ring through the new `recordCancelLocked`, which `Cancel` now also uses.
  - `Keep` returns `ErrEnded` on an ending sub. Otherwise it resets `ClockAt`, zeroes `CheckinAt` and filters `EventCheckin` out of Pending.
  - `panesLocked` ignores ending subs, which also takes them out of CoSubscribers and the undeliverable check.
- **scheduler**: `planLocked` marks a PR subscribed only for subs with `Ending == ""`, so a PR whose only subs are ending is not polled (T-04-20).
- **status**: `Status.Subs` counts only live subs.

## Commits

| Task | Gate | Commit | Message |
|------|------|--------|---------|
| 1 | RED | ced54b5 | test(04-05): add failing tests for state schema v3 lifecycle fields |
| 1 | GREEN | 1e53f95 | feat(04-05): state schema v3 with subscription lifecycle fields |
| 2 | RED | 7c1b2c3 | test(04-05): add failing tests for closed bypass, ending subs and ack clock reset |
| 2 | GREEN | edf4be8 | feat(04-05): closed mask bypass, ending subs removed on ack, D-11 clock reset |

## Verification

- `go test -race -count=1 ./internal/store ./internal/poller ./internal/deliver`: pass.
- `go test ./... -count=1`: all packages pass. `go vet` and `gofmt -l` are clean.
- `TestMaskBypass`, `TestClosedRemoval`, `TestClosedRemovalAck` (an extra test for the single-sub `Ack` path with reason expired), `TestEndingExcluded`, `TestAckClockReset`, `TestKeepLifecycle`, `TestMigrateV1ToV3` and `TestMigrateV2ToV3` all pass.
- All acceptance greps match (SchemaVersion 3, ending/end_seq tags, ackSub signature, ReasonClosed/ReasonExpired, IsLifecycle in fanout, `Ending == ""` in the scheduler).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] `omitempty` does not omit zero `time.Time`**
- **Found during:** Task 1
- **Issue:** The plan asked for `omitempty` on `LastEventAt`/`CheckinAt`, but encoding/json never omits struct values, so zero times would be written as `"0001-01-01T00:00:00Z"`. That breaks the "absent when zero" behaviour.
- **Fix:** Used `omitzero` for the two time fields. `omitempty` stays on `last_event_type`, `ending` and `end_seq`, as specified.
- **Commit:** 1e53f95

**2. [Rule 3 - Blocking] state-v2 golden regenerated from fullState()**
- **Found during:** Task 1
- **Issue:** `TestStateV2Golden` (state_test.go, not in files_modified) rewrites `testdata/state-v2.json` from `NewState()`. That would turn the v2 fixture into v3 and break the v2->v3 migration test.
- **Fix:** Renamed the test to `TestStateV3Golden`, writing the new `testdata/state-v3.json`. `fullState()` now sets all five new fields. state-v2.json is left byte-identical as the frozen v2 fixture. `TestNewState` expects version 3. A new test, `TestSubscriptionLifecycleOmitEmpty`, checks the zero values are omitted.
- **Commits:** ced54b5, 1e53f95

**3. [Rule 1 - Bug] Existing tests assumed the old semantics**
- **Found during:** Task 2
- **Issue:** `TestMaskAllows` expected closed to be filtered by a `[ci.failed]` mask. `TestClosedAtBaseline` re-polled after acking the closed event, but the sub (and with it the PR record) is now removed.
- **Fix:** TestMaskAllows now expects closed to pass. TestClosedAtBaseline adds a second pane to keep the record watched and asserts `PollResult.NewEvents == 0` on the repeat poll.
- **Commit:** 7c1b2c3

## Notes for downstream plans

- `Subscribe` on an existing ending sub currently resets ClockAt and the mask but leaves `Ending`/`EndSeq` set. Plan 04-08 (subscribe path) owns that behaviour. It should clear the marker or recreate the sub.
- `PollNow` still polls a PR whose only subs are ending, because the record exists. Only the scheduler excludes it.
- Plan 04-09 should set `Ending="expired"`/`EndSeq` when it queues an expired event, and `CheckinAt` when it queues a check-in. The ack path already handles removal and the `expired` reason.

## TDD Gate Compliance

Both tasks have a `test(...)` RED commit (a compile failure on the new fields and identifiers) followed by a `feat(...)` GREEN commit. No refactor commits were needed.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: internal/poller/ending_test.go
- FOUND: internal/store/testdata/state-v3.json
- FOUND: `SchemaVersion = 3` in internal/store/state.go
- FOUND commits: ced54b5, 1e53f95, 7c1b2c3, edf4be8
