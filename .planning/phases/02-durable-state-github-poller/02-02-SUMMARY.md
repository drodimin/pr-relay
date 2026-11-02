---
phase: 02-durable-state-github-poller
plan: 02
subsystem: model, ghquery
tags: [model, graphql, decoder, wire-boundary]
requires: []
provides:
  - "model.Memory final Phase 2 schema (Epoch, CommitsTotal, CommentsWatermark, ReviewsWatermark, ReviewCommentTotals) with snake_case json tags"
  - "model.CheckRun.Event, IssueComment.CreatedAt, Review.SubmittedAt"
  - "ghquery.Fragment (fragment PRFields) for the batch builder"
  - "ghquery.GraphQLError, snapshotFromWire, errorMessages for 02-05"
affects: [02-03, 02-05, 02-06, 02-12]
tech-stack:
  added: []
  patterns:
    - "Single pr.graphql file holds both the operation and a reusable fragment; Fragment is sliced off at init"
    - "GraphQL errors tolerated only when every error is path-scoped under repository.pullRequest and the PR is non-null"
key-files:
  created:
    - internal/ghquery/testdata/synthetic-partial-forbidden.json
    - internal/ghquery/testdata/synthetic-reopened.json
    - internal/ghquery/testdata/synthetic-parallel-trigger.json
  modified:
    - internal/model/model.go
    - internal/model/model_test.go
    - internal/ghquery/pr.graphql
    - internal/ghquery/query.go
    - internal/ghquery/decode.go
    - internal/ghquery/decode_test.go
decisions:
  - "WR-04 tolerance additionally requires path[0]=repository and path[1]=pullRequest, and RATE_LIMITED is always fatal, even when path-scoped"
  - "Event json tags use omitempty on every field except type and key; Memory tags carry no omitempty so the persisted schema is explicit"
  - "Malformed or missing timestamps decode to the zero time instead of failing the snapshot"
metrics:
  duration: "~15m"
  completed: 2026-11-01
  tasks: 2
  files: 9
---

# Phase 2 Plan 02: Model contract and tolerant decoder Summary

The model contract for Phase 2 is now final. Memory v1 is persisted with snake_case JSON and carries epoch, commits total, comment and review watermarks, and per-review comment totals. pr.graphql is now `query PRState` plus a reusable `fragment PRFields`. The decoder now keeps timestamps, attributes ClosedBy correctly, tolerates path-scoped partial errors and keeps parallel Actions triggers apart.

## What was done

**Task 1 (87c401e): model contract**
- `PRRef.Valid` now goes through `validName`, which rejects `.` and `..` (WR-07). Names such as `a.b` and `c..d` are still accepted.
- Added json tags to PRRef, Actor, Event (omitempty except `type`/`key`) and Memory.
- New Memory fields: `Epoch`, `CommitsTotal`, `CommentsWatermark`, `ReviewsWatermark` and `ReviewCommentTotals`. `Clone` deep-copies the counts map through `cloneCounts`, and a nil map becomes an empty one. The doc comment says the schema is v1, persisted by internal/store, and that later plans add no fields.
- New Snapshot-side fields: `CheckRun.Event`, `IssueComment.CreatedAt` and `Review.SubmittedAt`.
- Tests: new reject URLs and Valid cases, deep-clone coverage for ReviewCommentTotals, a full round trip that includes times and maps, and `TestMemoryJSONKeys`, which checks the exact key set plus the PRRef, Actor and Event shapes.

**Task 2 (f4f8cee): fragment and decoder**
- pr.graphql: the operation spreads `...PRFields`. The windows are defaulted variables (`$reviewsLast = 20`, `$commentsLast = 30`, `$reviewCommentsFirst = 30`). The query also selects `workflowRun { runAttempt event workflow { name } }`, `REOPENED_EVENT` and `... on ReopenedEvent`.
- query.go: `Fragment` is the slice of the file starting at `fragment PRFields`. `init` panics if that text is missing.
- decode.go: `Decode` now delegates to `snapshotFromWire`. It uses `GraphQLError` (with Path), `errorMessages` and `prScoped`. Timestamps are parsed with RFC 3339 and fall back to the zero time. In the timeline walk, a ReopenedEvent resets ClosedBy. The Actions key is `Workflow/Name@Event` when an event is present, and reruns still collapse to the newest databaseId for the same key.
- Tests: TestDecodeErrors gained no-path, top-level-path, other-root, rate-limited and mixed cases, plus a `ghquery:` prefix check. Also new or extended: TestDecodeClosedBy (both WR-03 orderings), TestDecodePartialErrors, TestDecodeParallelTrigger (including keeping the event-less `ci/test` key), TestDecodeTimestamps (including null, bad and missing values), TestQueryShape and TestFragment.

## Verification

- `go test -race -count=1 ./internal/...`: all packages pass.
- `git diff --stat -- internal/diff/testdata`: empty, so every scenario golden is byte-identical.
- `grep -c '^query ' internal/ghquery/pr.graphql` returns 1.
- Live check of A1: `gh api graphql -F query=@internal/ghquery/pr.graphql -F owner=drodimin -F repo=pr-relay-fixtures -F number=1 --jq '.errors // "none"'` returned `none`, so the defaulted variables work with capture.sh's owner/repo/number-only call.

## Deviations from Plan

**1. [Rule 2 - Correctness] Stricter WR-04 scope test.** The plan's rule was "every error has len(Path) >= 2". The implementation also requires the path to start with `repository`, `pullRequest`, and it never tolerates `RATE_LIMITED`. This matches the plan's stated intent ("scoped inside the repository/pullRequest") and its behaviour bullet that RATE_LIMITED fails. Commit f4f8cee.

**2. Acceptance grep note (no change).** `grep -rn '"body": "[^"]' internal/ghquery/testdata/` still matches two lines in the pre-existing `synthetic-basic.json`: the intentional `PRRELAY-BODY-SENTINEL` and a whitespace-only body used by TestDecodeDropsBodies. The three new fixtures contain only `""` bodies. synthetic-basic.json is unchanged.

**3. TestQueryShape anchor.** The fragment uses shallower indentation, so the issue-comments anchor in the existing test changed from `"\n      comments("` to `"\n  comments("`. Without that change the old anchor would have matched the nested review comments.

## Known Stubs

None.

## TDD Gate Compliance

Tests and implementation were committed together for each task (one `feat` commit per task, with no separate `test` RED commit). The new tests exercise behaviour that the previous code could not satisfy: partial errors used to fail, keys had no `@event`, and timestamps were not decoded.

## Self-Check: PASSED

- FOUND: internal/model/model.go, internal/ghquery/{pr.graphql,query.go,decode.go,decode_test.go}, and the three new fixtures
- FOUND commits: 87c401e, f4f8cee
