---
phase: 02-durable-state-github-poller
plan: 09
subsystem: poller
tags: [poller, engine, outbox, fan-out, subscriptions, tdd]
requires:
  - 02-03 (diff.Step with epoch keys and Result.Gap)
  - 02-06 (store.State, Clone, Save, PRKey, SubID)
  - 02-07 (ghclient.Client.Batch and typed errors)
  - 02-08 (Budget, BaseInterval, Effective, NextDue)
provides:
  - poller.Engine, Options, New, Fetcher, Saver
  - Subscribe, Unsubscribe, Keep, ResetRate, PollNow, Pending, Ack, CoSubscribers, Schedule, List, Wake
  - SubRequest, SubInfo, PaneRef, Drain, PollResult, CommitReport, PRReport
  - ErrNotWatched, ErrUnknownSub, RateLimitedError
  - internal pollRefs(ctx, keys, forced) for the 02-10 scheduler
  - fakeGH test RoundTripper (socket-free, synctest-safe) and newTestClient
affects:
  - 02-10 scheduler (calls pollRefs, reads Wake, sets e.stretch, uses budget under e.mu)
  - Phase 3 delivery (Pending/Ack) and Phase 4 control socket (Subscribe, PollNow, Keep, ResetRate)
tech-stack:
  added: []
  patterns:
    - "clone-mutate-Save-swap for every state change (mutate / mutateLocked helpers)"
    - "network call outside all locks; pollMu serializes polls; Gen check drops stale results"
    - "injectable Options.Now clock"
key-files:
  created:
    - internal/poller/engine.go
    - internal/poller/fanout.go
    - internal/poller/engine_test.go
    - internal/poller/fanout_test.go
    - internal/poller/fakegh_test.go
  modified: []
decisions:
  - "Options gained Now func() time.Time (defaults to time.Now) so engine tests can control RateAnchor/LastPollAt timing"
  - "pollRefs gates non-forced polls with Budget.Gate(now,false) as well as forced ones; both return *RateLimitedError"
  - "pollRefs refuses more than ghquery.MaxBatch keys; chunking by BatchSize is the scheduler's job"
  - "A poll that changes nothing durable skips Save and updates LastPollAt in memory only; it still logs the poll line"
  - "Batch-level errors set LastPollAt and LastError in memory only (no Save) and feed OnSecondary/OnPrimaryExhausted"
  - "A gap that persists after the wide re-fetch (or a failed re-fetch) commits the best available step and records a dropped marker on every subscriber"
  - "Stale (Gen-changed) results come back as PollResult.Err = errStale (unexported) and are not applied"
metrics:
  duration: ~25m
  completed: 2026-11-01
  tasks: 2
  files: 5
---

# Phase 2 Plan 09: Poller engine core Summary

The poller Engine now owns the subscription registry and a single-file transactional outbox. Each poll fetches every due PR record once in one aliased batch, steps it through `diff.Step`, re-fetches gapped PRs with `ghquery.WideWindow` from the same old Memory, fans the self-filtered events out to each subscription's capped pending queue, and commits Memory plus pending events in one `Save` on a cloned state. A failed Save leaves the live state untouched.

## Tasks

| Task | Name | Commits |
|------|------|---------|
| 1 | Pure fan-out and the fake GitHub transport | 848fa3a (test), 22b8769 (feat) |
| 2 | Engine registry, commit pipeline and primitives | 24731fa (test), 6330f15 (feat) |

## Behavior

- **Subscribe** checks `PRRef.Valid` and each mask entry against `model.AllEventTypes`. An empty mask is stored as nil and means every type. A new PR record starts un-baselined with a new Gen and RateAnchor set to now, is due immediately, and wakes the scheduler. Joining an existing record does no fetch and leaves RateAnchor alone. Re-subscribing the same (socket, pane, PR) replaces the mask, refreshes UpdatedAt/ClockAt and re-filters pending events. The return value is the ID, Created, the co-subscribers (excluding the caller, sorted), the interval and the next poll time.
- **Unsubscribe** deletes the PR record along with the last subscription. **Keep** changes ClockAt only. **ResetRate** changes RateAnchor only. **Ack(upTo)** drops events with Seq <= upTo and clears the drop marker once it is covered. **Pending** returns a deep copy, with UpToSeq = max(event seqs, DroppedSeq).
- **PollNow** returns ErrNotWatched for an unwatched PR. Otherwise it persists RateAnchor = now and runs a forced pollRefs. Forced polls skip the own-spend cap but are still recorded in the ledger. A secondary or primary limit still holds them, and they get `*RateLimitedError` back.
- **Activity** (D-12/A6) is decided at PR level. It means any event that survives ExcludeSelf, or any raw commits event. Own comments and reviews do not count.
- **Pending cap** is `config.Subscriptions.PendingCap` (default 200). When it overflows, the oldest events are dropped, Dropped grows, and DroppedSeq takes a fresh seq.
- A **MERGED** PR becomes Terminal. Per-alias errors set LastError to `not_found`, `forbidden` or `error` and keep the record.
- **Logging**: each successful poll writes one Info `poll` line with prs, cost, remaining, limit, reset_at, node_count, stretch, batch_size and duration_ms. Failures log at Warn or Error with the error class only. No bodies, tokens or headers are logged (T-02-34).

## Deviations from Plan

1. **[Rule 3 - Blocking] Added `Options.Now`.** The tests need to tell an activity-driven RateAnchor reset apart from a PollNow reset, and to check Keep and ResetRate times. The plan's Options had no clock. It defaults to time.Now, so the 02-10 synctest scheduler is unaffected. Commit 6330f15.
2. **[Rule 2] Non-forced polls are gated as well.** `pollRefs(…, false)` calls `Budget.Gate(now, false)`, so the cap holds even if the scheduler forgets to check it. Commit 6330f15.
3. **Fake helper signature.** `setHead(ref, oid, login)` takes the commit author so own-push and other-push cases can be scripted. Also added: `addReview`, `queue`, `requestCount` and `varsOf`. Commit 848fa3a.
4. Tests added beyond the plan: TestSubscribeValidation, TestPersistentGapMarksDropped, TestAckPartial, TestBatchErrorNoSave, TestUnsubscribeRemovesRecord, TestPollNowRateLimited and TestSchedule. Commit 6330f15.

## TDD Gate Compliance

For Task 1, RED commit 848fa3a failed to build (undefined maskAllows) and GREEN commit 22b8769 followed. For Task 2, RED commit 24731fa failed to build (undefined Engine) and GREEN commit 6330f15 followed. The two extra tests and the gofmt fix were folded into the GREEN commit.

## Verification

- `go test -race -count=1 ./internal/...` passes.
- `grep` acceptance: `filter.ExcludeSelf` appears in fanout.go, `time.Now` does not appear in fanout.go, `httptest` does not appear in fakegh_test.go, and `diff.Step(`, `Clone()` and `ghquery.WideWindow` all appear in engine.go.
- TestOneAliasPerPR asserts `len(aliasesPerRequest()[0]) == 2` with three subscriptions on the first PR.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: internal/poller/engine.go, fanout.go, engine_test.go, fanout_test.go, fakegh_test.go
- FOUND commits: 848fa3a, 22b8769, 24731fa, 6330f15
