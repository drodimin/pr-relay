---
phase: 04-daemon-control-protocol-cli
plan: 08
subsystem: poller-subscribe
tags: [poller, subscribe, verify, unsubscribe, reset, lifecycle]
requires:
  - 04-05 (Subscription.Ending/EndSeq/CheckinAt, ReasonClosed/ReasonExpired)
provides:
  - poller.Engine.SubscribeVerified(ctx, SubRequest) (SubInfo, error)
  - poller.VerifyError{Class, Err}, ClassTimeout ("timeout"), ClassPRClosed ("pr_closed")
  - poller.Engine.UnsubscribePane(socket, pane, *PRRef) (int, error)
  - poller.Engine.Reset() (int, error)
  - subscribeIn / subInfoLocked shared by Subscribe and SubscribeVerified
affects: [04-10 daemon sub/unsub/reset handlers, 04-11 CLI reset gating]
tech-stack:
  added: []
  patterns:
    - "Probe outside e.mu but under pollMu, then re-check and mutate; failures before the mutate never call Save"
    - "Join-first: a baselined, OPEN, error-free record is joined without a GitHub call"
key-files:
  created:
    - internal/poller/subscribe.go
    - internal/poller/subscribe_test.go
  modified:
    - internal/poller/engine.go
decisions:
  - "Probe and commit both run under pollMu, and SubscribeVerified re-checks the record after taking it. Two concurrent subscribes to a new PR make exactly one GitHub call, and the second joins the first's baseline."
  - "A gate refusal is reported as secondary_limit when the budget is in secondary backoff, otherwise as rate_limited. Err is a *RateLimitedError carrying Until."
  - "Reviving an expired-ending sub drops queued checkin events as well as expired ones, since re-subscribing resets the clock just as keep does."
  - "An existing record that is not yet baselined (created by the dev Subscribe path) adopts the probe baseline and clears LastError. A record that is already baselined keeps its Memory."
  - "Subscribe (the dev path) also returns a pr_closed *VerifyError when re-subscribing a closed-ending sub, because it shares subscribeIn."
metrics:
  duration: "~20 min"
  completed: 2026-11-04
  tasks: 2
  files: 3
requirements: [SUB-07, SUB-05, SUB-02, LIFE-07]
---

# Phase 4 Plan 08: Verified subscribe, expired revive, pane-scoped unsubscribe and reset Summary

`SubscribeVerified` persists a subscription only after a budget-gated, forced GitHub probe has shown the PR is readable and OPEN. The probe snapshot becomes the silent baseline. Re-subscribing an expired sub revives it. `UnsubscribePane` and `Reset` give the daemon pane-scoped and machine-wide removal, and both are idempotent.

## What was built

- **engine.go**: The upsert body of `Subscribe` now lives in `subscribeIn(next, req, mask, now) (id, created, err)`, and SubInfo assembly lives in `subInfoLocked`. Re-subscribing does the following, depending on the existing sub's `Ending`:
  - `Ending == "expired"`: clears `Ending`, `EndSeq` and `CheckinAt`, drops queued `expired`/`checkin` events, resets `ClockAt` and replaces the mask. Other pending events are kept.
  - `Ending == "closed"`: returns a `*VerifyError{Class: "pr_closed"}`.

  Apart from that, `Subscribe` behaves exactly as it did before.
- **subscribe.go**:
  - `VerifyError` formats as `poller: verify PR: CLASS: ERR` and implements Unwrap. Its classes are the `errClass` values plus `timeout` (a DeadlineExceeded anywhere in the chain, or on ctx) and `pr_closed`.
  - `SubscribeVerified` runs these steps in order:
    1. Joins without a fetch when the record is baselined, OPEN and has no error. It returns pr_closed when the record is baselined and not OPEN.
    2. Otherwise it takes `pollMu`, re-checks, and probes. The probe uses `budget.Gate(now, forced=true)`, so a refusal fails immediately. It then calls `fetch.Batch(ctx, [ref], NormalWindow)`. On error it calls `applyRateLimit`; on success it calls `Record`, `Observe` and `OnClean`, the same as `pollRefsLocked`.
    3. When the record is still absent, it creates one with `Memory = diff.Step(Memory{}, snap).Memory` and `LastPollAt = RateAnchor = now`. It sets `Viewer` if that is empty. Then it calls `subscribeIn`.
    4. It pokes the scheduler for a new record, and pokes delivery on an update.
  - `UnsubscribePane` matches subs on `filepath.Clean(socket)` plus pane, and optionally on the PR. `Reset` matches every sub. Both go through `removeSubs`, which:
    - checks for any match on the live state, and returns 0 without a save when there is none;
    - otherwise removes the matches with `unsubscribeIn` while iterating the clone, in one mutate;
    - logs "subscriptions removed", calls `pokePending`, and adds no cancellation-ring entry.

## Commits

| Task | Gate | Commit | Message |
|------|------|--------|---------|
| 1 | RED | 4d5336b | test(04-08): add failing tests for verified subscribe and expired revive |
| 1 | GREEN | 924e438 | feat(04-08): SubscribeVerified with probe baseline and expired revive |
| 2 | RED | 2ee1eea | test(04-08): add failing tests for UnsubscribePane and Reset |
| 2 | GREEN | 105ffce | feat(04-08): pane-scoped UnsubscribePane and Reset |

## Verification

- `go test ./internal/poller -count=1 -race`: pass.
- `go test ./... -count=1`: all packages pass.
- `go vet` and `gofmt -l`: clean.
- These tests pass:
  - TestSubscribeVerifiedOK, FakeGH (real ghclient over the fake GitHub, with a silent first scheduled poll), Errors, GateRefusal, Closed, Join, ErroredRecordProbes and Concurrent
  - TestResubRevivesExpired, TestUnsubscribePane and TestReset
- Every case in the Errors table checks zero saves and empty state. The table covers not_found, forbidden, unauthorized, no_token, rate_limited, secondary_limit, transient and timeout.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Concurrent probes could commit out of order**
- **Found during:** Task 1
- **Issue:** If the probe released `pollMu` before committing, a later probe could commit first. "Memory from the first committed probe" would then be nondeterministic, and every concurrent caller would spend a request.
- **Fix:** The probe and the commit both happen under `pollMu`, with a join re-check after acquiring it. Lock order stays pollMu then mu, which matches `pollRefsLocked`.
- **Commit:** 924e438

**2. [Rule 2 - Correctness] Additional tests beyond the plan's behaviour list**
- I added TestSubscribeVerifiedGateRefusal (T-04-31, a fast failure with zero fetches), TestSubscribeVerifiedErroredRecordProbes, TestSubscribeVerifiedFakeGH, and a `transient` row in the error table.
- **Commits:** 4d5336b

No other deviations.

## Notes for 04-10 (daemon handlers)

- Call `SubscribeVerified` with a context of about 5s. Map `errors.As(err, *poller.VerifyError)` with `.Class` to the wire error code. Errors that are not a VerifyError are validation errors (bad ref or unknown event type) or save failures.
- `pollMu` is not context-aware. If a scheduled poll is in flight, the probe waits for it before starting, and the ctx deadline only bounds the fetch itself.
- `UnsubscribePane(socket, pane, nil)` means "all of this pane". A count of 0 is success (D-24). `Reset` returns the count; gate it in the CLI (04-11, D-25).
- `Subscribe` (dev path) can now return a `*VerifyError` pr_closed for a closed-ending sub.
- Re-subscribing a live (non-ending) sub that has a pending check-in leaves `CheckinAt` and the check-in event in place. Only `keep` and the expired revive clear them. 04-09's lifecycle tick owns that behaviour.

## TDD Gate Compliance

Both tasks have a `test(...)` RED commit, which fails to compile because the methods do not exist yet, followed by a `feat(...)` GREEN commit. No refactor commits were needed.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: internal/poller/subscribe.go
- FOUND: internal/poller/subscribe_test.go
- FOUND commits: 4d5336b, 924e438, 2ee1eea, 105ffce
