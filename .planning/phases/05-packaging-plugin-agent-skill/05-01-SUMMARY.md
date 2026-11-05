---
phase: 05-packaging-plugin-agent-skill
plan: 01
subsystem: poller
tags: [poller, concurrency, release-gate]
requires: []
provides:
  - "Engine.lockPoll(ctx)/unlockPoll: context-aware one-slot poll semaphore (pollSem)"
  - "SubscribeVerified never persists after the caller's context ended"
  - "Live re-subscribe resets check-in state like keep"
affects: [internal/daemon, cmd/pr-relay]
tech-stack:
  added: []
  patterns: ["buffered chan struct{} of size 1 as a ctx-aware mutex"]
key-files:
  created: []
  modified:
    - internal/poller/engine.go
    - internal/poller/subscribe.go
    - internal/poller/subscribe_test.go
decisions:
  - "subscribeVerifiedCommit checks ctx at all three call sites (including the unlocked fast path), so a caller whose context already ended never joins; the fast path itself is otherwise unchanged"
  - "A cancelled (not timed-out) context maps through verifyErr to a VerifyError that still unwraps to context.Canceled"
metrics:
  duration: "~10 min"
  completed: 2026-11-05
requirements: [DIST-02]
---

# Phase 5 Plan 01: Poller release-gate fixes (CR-01, CR-03) Summary

Context-aware poll semaphore (`pollSem` with `lockPoll(ctx)`/`unlockPoll`) replaces `pollMu`, and there is a ctx re-check under `e.mu` before commit, so a sub that timed out is never persisted. A live re-subscribe now clears pending check-ins and `CheckinAt`, the same way `keep` does.

## What was done

### Task 1: CR-01, context-aware poll lock
- `Engine.pollMu sync.Mutex` is replaced by `pollSem chan struct{}` (capacity 1, created in `New`).
- `lockPoll(ctx)` selects between sending into `pollSem` and `<-ctx.Done()`, and returns `ctx.Err()` if the context ends first. `unlockPoll()` releases the slot.
- `pollRefs` (and through it `PollNow` and scheduled polls) returns `nil, err` when the wait times out. `OnCommit` still runs outside the lock.
- `SubscribeVerified` acquires through `lockPoll(ctx)`, maps failures through `verifyErr`, and re-checks `ctx.Err()` right after it acquires the lock.
- `subscribeVerifiedCommit` takes `ctx` and checks `ctx.Err()` under `e.mu` immediately before `mutateLocked`. If the context has ended it returns done with the error and changes nothing.
- Tests:
  - `TestSubscribeVerifiedDeadline`: the lock is held and the sub times out after about 200ms with a timeout `VerifyError`. After the lock is released there is no sub, no save and no Batch call.
  - `TestSubscribeVerifiedDeadlineAfterAcquire`: the context is cancelled inside the probe. The result is an error, with no sub, no PR record and no save.
  - `TestPollNowDeadline`: the lock is held and `PollNow` returns `context.DeadlineExceeded` after about 200ms.

### Task 2: CR-03, re-subscribe clears check-ins
- After the `switch sub.Ending` block, `subscribeIn` now sets `sub.CheckinAt = time.Time{}` and `sub.Pending = withoutCheckins(sub.Pending)`. The closed and expired branches are unchanged.
- Test: `TestResubscribeClearsCheckin`.

## Commits

| Task | Commit | Message |
|------|--------|---------|
| 1 RED | 93e348a | test(05-01): add failing tests for deadline-aware poll lock (CR-01) |
| 1 GREEN | 0507abc | fix(05-01): bound poll-lock waits by caller context (CR-01) |
| 2 RED | 5b4dac5 | test(05-01): add failing test for live re-subscribe check-in reset (CR-03) |
| 2 GREEN | 65e9253 | fix(05-01): live re-subscribe clears pending check-ins (CR-03) |

## Verification

- `go vet ./internal/poller`: clean.
- `go test -race -count=1 ./internal/poller ./internal/daemon ./cmd/pr-relay`: all pass.
- `pollMu` no longer appears in non-comment code under `internal/poller/`. The only remaining mention is a historical comment in `engine_test.go`, which is outside this plan's files.
- `subscribeVerifiedCommit(ctx` appears at 3 call sites plus the definition.

## TDD Gate Compliance

Both tasks have a RED `test(...)` commit before their GREEN commit. Task 1's RED failed at compile time, because `pollSem` did not exist yet. Task 2's RED failed on assertions.

## Deviations from Plan

None. The plan executed as written. The only small addition is that `ctx` is also passed to the fast-path call of `subscribeVerifiedCommit`, because that call shares the function the plan required to take `ctx`.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: internal/poller/engine.go, internal/poller/subscribe.go, internal/poller/subscribe_test.go
- FOUND commits: 93e348a, 0507abc, 5b4dac5, 65e9253
