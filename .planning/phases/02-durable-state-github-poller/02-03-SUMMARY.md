---
phase: 02-durable-state-github-poller
plan: 03
subsystem: diff
tags: [diff, dedupe-keys, watermarks, scenarios, goldens]
requires: ["02-02"]
provides:
  - "Epoch-bearing Event.Key grammar for lifecycle and CI events (documented in the diff package doc)"
  - "CR-01 comment and review watermarks; WR-06 per-review comment gap via Result.Gap"
  - "WR-01 big-push rule based on the CommitsTotal delta"
  - "Five regression scenarios: delete-comment-31, reopen-close, big-push-30, parallel-trigger, review-comments-31"
affects: [02-06, 02-09, 02-12]
tech-stack:
  added: []
  patterns:
    - "Memory.Epoch is bumped before any key of the step is built, once for a state change and once for a head change"
    - "Watermarks are compared strictly (Before) against the pre-step value, then advanced after the loop"
key-files:
  created:
    - internal/diff/testdata/scenarios/delete-comment-31/
    - internal/diff/testdata/scenarios/reopen-close/
    - internal/diff/testdata/scenarios/big-push-30/
    - internal/diff/testdata/scenarios/parallel-trigger/
    - internal/diff/testdata/scenarios/review-comments-31/
  modified:
    - internal/diff/diff.go
    - internal/diff/lifecycle.go
    - internal/diff/ci.go
    - internal/diff/activity.go
    - internal/diff/lifecycle_test.go
    - internal/diff/activity_test.go
    - internal/diff/ci_test.go
    - internal/diff/diff_test.go
    - internal/diff/snap_test.go
    - internal/diff/invariants_test.go
    - internal/diff/testdata/scenarios/README.md
decisions:
  - "Epoch is bumped at the top of stepLifecycle (state change, then head change), so conflict, commits and CI keys in one step all share the post-bump epoch"
  - "review:<id> and comment:<id> keys carry no epoch, because GitHub node IDs never repeat"
  - "Items skipped by a watermark do not count as new for the totals gap check (conservative: a gap triggers the wide re-fetch)"
  - "The WR-06 per-review gap only fires for reviews that already have a ReviewCommentTotals entry, so a missing map entry never produces a spurious gap"
metrics:
  duration: "~35m (including a usage-limit pause)"
  completed: 2026-11-01
  tasks: 3
  files: 30
---

# Phase 2 Plan 03: D-04 diff fixes Summary

The diff now produces true dedupe keys and no longer invents or loses events in the D-04 cases. Lifecycle and CI keys carry `Memory.Epoch`. Fast-forward pushes beyond the commit window are no longer labelled force-pushes. Comments and reviews that slide into the window after a deletion stay silent, and a review whose inline comments overflow the window reports a gap. Five scenario goldens guard these fixes.

## What was done

**Task 1 (5873760): epoch keys (WR-02) and the big-push rule (WR-01)**
- `stepLifecycle` bumps `m.Epoch` on `s.State != m.State` and on a head change, before any keys are built.
- New key grammar: `closed:<epoch>:<state>`, `conflict:<epoch>:<head>:<base>:<mergeable>`, `commits:<epoch>:<head>`, `ci.failed:<epoch>:<head>:<checkKey>`, `ci.settled:<epoch>:<head>:<seq>`. `review:<id>` and `comment:<id>` are unchanged. The grammar is documented in the package doc in diff.go.
- WR-01 case: the old head is not visible and there is no new force-push item. If `m.CommitsTotal > 0`, `delta >= len(s.Commits)` and `s.CommitsTotal > len(s.Commits)`, the step emits a plain commits event with `Count = delta` and authors taken from the visible commits. In every other case the old force-push-unknown behaviour stays. `m.CommitsTotal = s.CommitsTotal` is set on every step, and Baseline records it as well.
- Tests: TestEpochKeys covers reopen, a head returning for commits and ci.failed, a head returning for ci.settled, and the baseline epoch. TestBigPush covers 40 total with 30 visible giving Count 35 and no force-push, and a total of 6 giving a force-push. Key literals in the existing tests were updated. New builder: `withCommitsTotal`.

**Task 2 (97d8508): watermarks (CR-01) and the per-review gap (WR-06)**
- An unseen issue comment or non-PENDING review with a non-zero timestamp strictly before the watermark is marked seen and emits nothing. A review skipped this way also has its inline comments marked seen. Watermarks advance after each loop. PENDING reviews never read or move the review watermark.
- WR-06: a seen review sets `gap` when `r.CommentsTotal - ReviewCommentTotals[r.ID]` exceeds its newly visible comment IDs. `ReviewCommentTotals` is updated for every non-PENDING review.
- Baseline sets both watermarks to the window maximum and fills `ReviewCommentTotals`.
- The activity doc now describes the watermark rules, the strict comparison (pitfall 4), the WR-06 gap and the poller's wide re-fetch (plan 02-09). The old "Deferred to Phase 2" note now refers only to pruning (RESEARCH open question 2).
- Tests: TestWatermark (slide-in, same second, zero time, old review, PENDING) and TestReviewCommentGap (1 of 3 visible gives a gap, 3 of 3 does not, steady state does not). New builders: `withCommentAt` and `withReviewAt`.

**Task 3 (94c12e3): five regression scenarios**
I generated the fixtures from captured fixtures with a scratch script. Each file has a `_synthetic` note, the bodies are `PRRELAY-BODY-SENTINEL` or empty, and the goldens were reviewed by hand after `-update`:
- delete-comment-31: step 01 is empty, step 02 has one comment line by dmitriy-medme.
- reopen-close: two closed lines, closed by dmitriy-medme and then by drodimin, with distinct keys.
- big-push-30: `commits: 32 new`, with no "force-pushed". The rollup still belongs to the old head, so CI is unknown and no CI lines appear.
- parallel-trigger: `ci.failed: test` (the push run), then `ci.settled: failed (1 of 7): test`.
- review-comments-31: step 01 has no lines and has `(gap)`.
- Each scenario has a scenarioChecks entry, and the README has one row per new scenario.

## Verification

- `go test -race -count=1 ./internal/diff/ ./internal/ghquery/ ./internal/render/` passes, and `go test ./internal/...` passes.
- No pre-existing `expected.golden` changed, and `internal/render/testdata/lines.golden` is unchanged.
- `grep -c force-pushed big-push-30/expected.golden` gives 0.
- Acceptance greps match: `strconv.Itoa(m.Epoch)` in lifecycle.go and ci.go, `m.CommitsTotal = s.CommitsTotal`, `Before(m.CommentsWatermark)`, `ReviewCommentTotals[` in activity.go, and `CommentsWatermark` in diff.go.
- TestStepNoMutate and TestBaselineSilent still pass, so diff stays pure.

## Deviations from Plan

- **Builder placement.** The plan suggested a `withState` builder, but it already existed. `withCommentAt` and `withReviewAt` live in snap_test.go, as the plan asked, rather than next to the activity builders.
- **Epoch bump point.** The plan said to bump "before building the commits event". The head-change bump runs at the top of stepLifecycle instead, so a conflict key in the same step also uses the new epoch. This still meets the requirement and keeps the keys within a step consistent.
- **Watermark-skipped items do not count as new for the totals gap check.** This is a discretion choice: the error falls toward a gap, which leads to a wide re-fetch.
- **TDD gate.** Tasks 1 and 2 were committed as single `feat` commits with tests and implementation together, with no separate RED commit. The new tests fail against the pre-plan code: keys collide, old comments fire, and there is no gap.

## Known Stubs

None.

## Threat Flags

None. T-02-08 is mitigated by the strict watermark and T-02-09 by the epoch keys. T-02-10 is accepted, and pruning stays deferred.

## Self-Check: PASSED

- FOUND: the five scenario directories, each with expected.golden
- FOUND commits: 5873760, 97d8508, 94c12e3
