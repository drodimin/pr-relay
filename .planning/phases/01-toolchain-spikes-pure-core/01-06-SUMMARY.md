---
phase: 01-toolchain-spikes-pure-core
plan: 06
subsystem: diff, filter
tags: [diff, reviews, comments, self-exclusion, pure-core]
requires:
  - phase: 01-04
    provides: "diff.Step/Baseline, Memory seen sets and totals, snap_test.go builders"
provides:
  - "stepActivity: review and comment events, Result.Gap"
  - "filter.ExcludeSelf"
affects: [01-09 fixture scenarios, phase 2 store/router, render]
tech-stack:
  added: []
  patterns: ["ID-based seen sets for new-only detection", "type-gated actor comparison"]
key-files:
  created:
    - internal/diff/activity.go
    - internal/diff/activity_test.go
    - internal/filter/filter.go
    - internal/filter/filter_test.go
  modified:
    - internal/diff/diff.go
key-decisions:
  - "New unseen PENDING reviews count as visible IDs in the gap check, so the viewer's own pending review included in ReviewsTotal does not raise a false gap"
  - "The comments gap compares only issue comments (Snapshot.CommentsTotal). Inline comments are per-review and have no PR-level total"
  - "A force-push with an empty actor falls through to the all-authors rule; an empty Authors list is kept"
  - "SeenReviews/SeenComments pruning deferred to Phase 2 (persistence owns Memory size)"
requirements-completed: [EVT-03, EVT-04, EVT-08]
duration: ~10min
completed: 2026-10-31
---

# Phase 1 Plan 06: Review/Comment Activity and Self-Exclusion Summary

**stepActivity in diff emits one review event per new real review, with inline comments grouped into it, and one comment event per issue comment, thread reply or late inline comment. It ignores edits, deletions and PENDING reviews and flags window gaps. filter.ExcludeSelf drops the user's own review, comment and commit activity, matching only User-type actors.**

## Accomplishments
- `internal/diff/activity.go`: `stepActivity(m *model.Memory, s model.Snapshot) ([]model.Event, bool)`, called from `Step` at the 01-04 marker. `Result.Gap` is now set from it. Keys are `review:<id>` and `comment:<id>`.
- Real review rule: APPROVED, CHANGES_REQUESTED or DISMISSED, or HasBody, or at least one non-reply inline comment. For a real review, InlineCount is `CommentsTotal`, falling back to `len(Comments)`. Reply wrappers emit one `reply` comment event per comment.
- Doc comments record assumption A1 (reply wrapper review, which 01-09 confirms) and the deferral of Seen-map pruning to Phase 2.
- `internal/filter/filter.go`: `ExcludeSelf(events, self)` returns a new slice. Login matching is case-insensitive (`strings.EqualFold`) and gated on `Actor.IsUser()`. closed, conflict and ci.* events are always kept. Its only internal dependency is `internal/model`.

## Task Commits
1. Task 1 RED: `c0e1232` test(01-06): add failing review/comment activity tests
2. Task 1 GREEN: `58635fc` feat(01-06): implement stepActivity for reviews, comments and gap
3. Task 2 RED: `e9bbec8` test(01-06): add failing ExcludeSelf tests
4. Task 2 GREEN: `20d00ad` feat(01-06): implement filter.ExcludeSelf own-activity exclusion

## Verification
- `go vet ./... && go test -race -count=1 ./internal/diff/ ./internal/filter/` passes: the 11 activity tests, all 01-04 tests, TestExcludeSelf (21 rows) and TestExcludeSelfNoMutate.
- `grep -A2 'plan 01-06' internal/diff/diff.go | grep stepActivity` matches.
- `go list -deps ./internal/filter | grep pr-relay` lists only internal/model and internal/filter.

## Deviations from Plan
None. The plan was executed as written. The interpretation choices (pending reviews counted in the gap check, comments gap counting issue comments only) are listed under key-decisions.

## TDD Gate Compliance
RED (`test`) commits precede GREEN (`feat`) commits for both tasks. No refactor commit was needed.

## Known Stubs
None.

## Self-Check: PASSED
All 5 key files exist, and commits c0e1232, 58635fc, e9bbec8 and 20d00ad are present in git log.
