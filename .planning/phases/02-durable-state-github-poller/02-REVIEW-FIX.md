---
phase: 02-durable-state-github-poller
fixed_at: 2026-11-02T20:45:00Z
review_path: .planning/phases/02-durable-state-github-poller/02-REVIEW.md
iteration: 1
findings_in_scope: 9
fixed: 9
skipped: 0
status: all_fixed
---

# Phase 2: Code Review Fix Report

**Fixed at:** 2026-11-02T20:45:00Z
**Source review:** .planning/phases/02-durable-state-github-poller/02-REVIEW.md
**Iteration:** 1

**Summary:**
- Findings in scope: 9 (2 critical, 7 warning; Info findings were out of scope)
- Fixed: 9
- Skipped: 0

Verification after all fixes: `go build ./...`, `go vet ./...` and `go test -count=1 ./...` all pass, and `gofmt -l` is clean. Every new regression test fails without its fix and passes with it.

## Fixed Issues

### CR-01: The sink's global high-water mark drops events across subscriptions

**Files modified:** `cmd/pr-relay/sink.go`, `cmd/pr-relay/dev.go`, `cmd/pr-relay/sink_test.go`
**Commits:** 04143c9 (fix), ae480d1 (test)
**Applied fix:** `sink.maxSeq` is now a `map[string]uint64` keyed by PR key (`sinkLine.PR`). `scan` builds the mark per PR, and `write` skips and advances only that PR's mark. At startup, `runDevWatch` acks each subscription only up to its own PR's high-water mark, using the new `highWater(prKey)`, and skips PRs that have no mark. The new `TestSinkPerPRHighWater` writes PR B's higher seqs before PR A's lower ones and checks the per-PR marks after reopening the sink.
**Status:** fixed: requires human verification (delivery-semantics logic)

### CR-02: Invisible combining marks split the envelope tag and get past NeutralizeTag and the render guard

**Files modified:** `internal/sanitize/sanitize.go`, `internal/sanitize/fold_test.go`, `internal/sanitize/hostile_test.go`, `internal/render/render.go`, `internal/render/hostile_test.go`, `internal/testutil/hostile.go`
**Commit:** 11ba97f
**Applied fix:**
- `isFiller` now also strips every `unicode.Other_Default_Ignorable_Code_Point` rune (U+034F, U+17B4/U+17B5, the Hangul fillers, and so on) in `clean`. The Mongolian FVS runes U+180B-U+180F were already stripped as Variation_Selector or Cf.
- Tag matching moved into `tagMatches`. It skips Mn, Me, Cf, variation selectors and fillers, then compares the folded remaining runes, and it maps matches back to original rune indices. Rune count is still preserved.
- New `sanitize.CountTag` uses the same matcher, and `render.fits` now uses it instead of `strings.Count(Fold(l), tag)`.
- Added hostile corpus entries (`[pr͏-relay]`, `[p឴r-relay]`, `[p឵r-rel᠋ay]`, `[pŕ-relay]`) and `TestMarksCannotSplitTag`. The hostile tests in sanitize and render now assert with `CountTag`.
**Status:** fixed

### WR-01: A secondary rate limit on the gap re-fetch is ignored

**Files modified:** `internal/poller/engine.go`, `internal/poller/engine_test.go`, `internal/poller/fakegh_test.go`
**Commit:** e403f99
**Applied fix:** Extracted `applyRateLimit(now, err)`, which calls `OnSecondary` or `OnPrimaryExhausted` and is shared by `batchFailed` and the wide re-fetch error path. `budget.OnClean()` now runs after the gap block, and only when neither fetch hit a rate limit. The fake GitHub gained `wideStatus`, which scripts responses for wide-window requests only. `TestGapRefetchSecondaryLimitBacksOff` checks that a 429 on the re-fetch sets `SecondaryUntil` and the streak.
**Status:** fixed: requires human verification (backoff logic)

### WR-02: A transient failure of the wide re-fetch permanently drops activity

**Files modified:** `internal/poller/engine.go`, `internal/poller/engine_test.go`
**Commit:** 35ad3b9
**Applied fix:**
- A batch-level failure of the wide re-fetch now sets `stepped.gapErr` instead of `gapLost`.
- In `commit`, such PRs leave their Memory and pending queues unchanged and add no drop marker. Only `LastPollAt` and `LastError` are updated in memory, as `batchFailed` does, and the PollResult or report carries the error. The next poll re-derives the gap.
- A per-item error in the wide result now records the item error and keeps Memory unchanged, instead of marking the PR gapLost.
- `gapLost` is still set only when the wide window itself still shows `Gap`.
- `TestGapRefetchTransientFailureRecovers` checks that a 502 on the re-fetch leaves Memory unchanged with no drops, and that the next poll delivers all 40 comments.
**Status:** fixed: requires human verification (state handling logic)

### WR-03: OnCommit runs while pollMu is held

**Files modified:** `internal/poller/engine.go`, `internal/poller/engine_test.go`
**Commit:** 63a864a
**Applied fix:** The body moved to `pollRefsLocked`, which returns `(results, *CommitReport, error)`. `pollRefs` locks `pollMu`, calls it, unlocks, and only then invokes `onCommit`. The `Options.OnCommit` doc now says the callback runs outside all engine locks, may call PollNow, and is not serialized across concurrent polls (the dev printer already takes its own mutex). `TestOnCommitMayPoll` calls PollNow from inside OnCommit, with a 5s deadlock guard.
**Status:** fixed

### WR-04: Subscribing to an already-merged PR never produces a notification

**Files modified:** `internal/diff/lifecycle.go`, `internal/diff/lifecycle_test.go`, `internal/poller/engine.go`, `internal/poller/engine_test.go`
**Commit:** 67fb77c
**Applied fix:**
- New `diff.BaselineClosed(snapshot)` returns a one-time closed event (key `closed:0:<STATE>`, with the MergedBy or ClosedBy actor) for MERGED or CLOSED snapshots. `diff.Baseline` and `Step` stay silent, so the existing baseline-silence tests still hold.
- On a baselining step (`!t.mem.Baselined`), `pollRefs` appends that event, so subscribers to a PR that is already merged or closed hear about it once. Epoch 0 is never reused by later lifecycle steps, which always bump the epoch first.
- `SubInfo` gained `Terminal`, set when a subscription joins a record that is already Terminal.
- `scheduleLocked` (and therefore `Schedule`, `SubInfo` and `PRReport`) returns a zero Interval and NextPollAt for Terminal records.
- Tests: `TestMergedAtBaseline`, `TestClosedAtBaseline` (no repeat on a later poll) and `TestBaselineClosed`.
**Status:** fixed: requires human verification (new event semantics: a CLOSED-at-baseline PR now notifies once too, which goes slightly beyond the MERGED case in the finding)

### WR-05: Save can fail after the rename and leave in-memory state behind disk

**Files modified:** `internal/store/store.go`, `internal/poller/engine.go`, `internal/poller/engine_test.go`
**Commit:** 8a611a3
**Applied fix:**
- New `store.ErrNotDurable`. The open-dir, sync-dir and close-dir failures after the rename now wrap it (`%w: %w`).
- The Save doc comment now describes this accurately.
- The engine's new `save()` helper, used by `mutateLocked` and `commit`, treats `ErrNotDurable` as committed. It logs a warning and swaps the new state in.
- `TestSaveNotDurableStillSwaps` checks that an Ack with a non-durable save still clears the queue in memory.
**Status:** fixed

### WR-06: A partial sink write leaves a torn line mid-file

**Files modified:** `cmd/pr-relay/sink.go`
**Commit:** 199447e
**Applied fix:** `write` records the file size (via Stat) before writing. If the write or the fsync fails, it truncates back to that offset with a new `rollback` helper, which joins any truncate error. The mark is not advanced on failure. No automated test, because injecting ENOSPC or EIO is not practical here. Verified by build, vet and the existing crash test.
**Status:** fixed

### WR-07: min_interval has no lower bound

**Files modified:** `internal/config/config.go`, `internal/config/config_test.go`, `cmd/pr-relay/crash_test.go`
**Commit:** 5faf919
**Applied fix:**
- `Validate` now requires `min_interval >= MinMinInterval` (10s).
- The test-only env var `PR_RELAY_TEST_FAST_POLL=1` (`config.EnvFastPoll`) lowers the floor to 1ms. The crash test sets it for its 100ms subprocess config, and `TestLoadDurations` sets it with `t.Setenv`.
- The package doc was updated.
- New `TestMinIntervalFloor` checks that 1ms, 100ms and 9s are rejected, 10s is accepted, and 100ms is accepted with the override.
**Status:** fixed

---

_Fixed: 2026-11-02T20:45:00Z_
_Fixer: Claude (gsd-code-fixer)_
_Iteration: 1_
