---
phase: 02-durable-state-github-poller
plan: 05
subsystem: ghquery
tags: [graphql, batching, decoder, tdd]
requires:
  - 02-02 (ghquery Fragment, snapshotFromWire, errorMessages, GraphQLError, RateLimit)
provides:
  - ghquery.BuildBatch / DecodeBatch (pure batch build and decode)
  - Window, NormalWindow (20/30/30), WideWindow (100/100/100), MaxBatch = 20
  - AliasResult, BatchData, BatchError, ErrNotFound, ErrForbidden, ErrRateLimited
affects:
  - 02-07 ghclient (sends BuildBatch output, feeds bounded bodies to DecodeBatch)
tech-stack:
  added: []
  patterns: [variable-only GraphQL with index-derived aliases, per-alias error isolation]
key-files:
  created:
    - internal/ghquery/batch.go
    - internal/ghquery/batch_test.go
    - internal/ghquery/testdata/batch-basic.json
    - internal/ghquery/testdata/batch-not-found.json
    - internal/ghquery/testdata/batch-forbidden-nested.json
    - internal/ghquery/testdata/batch-rate-limited.json
  modified: []
decisions:
  - "RATE_LIMITED fails the whole batch even if it is alias-scoped and alias data is present (same rule as 02-02's Decode)"
  - "Alias errors are tolerated as warnings only when the path is <alias>.pullRequest.<field>... (len >= 3), the per-alias equivalent of WR-04"
  - "A path-less or unknown-path error with alias data present becomes BatchData.Warnings; without alias data it is a *BatchError"
  - "Snapshot.Ref is set to the requested ref after a case-insensitive owner/repo + exact number match"
metrics:
  duration: ~25m (excluding usage-limit pause)
  completed: 2026-11-01
---

# Phase 2 Plan 05: Batch Query Builder and Decoder Summary

ghquery now has a pure batch builder and decoder. `BuildBatch` emits one aliased `query PRBatch(...)` operation for up to 20 PRs. Every value is passed as a variable (`$o<i>/$r<i>/$n<i>` plus the window variables), aliases come from the index only, and the query selects `rateLimit` and `viewer { login }` and appends the shared `PRFields` fragment once. `DecodeBatch` splits the response into one result per alias, so a single missing, forbidden or spoofed PR never fails the rest of the batch.

## Tasks

| Task | Name | Commits |
|------|------|---------|
| 1 | BuildBatch (RED then GREEN) | 0bc6e5a (test), 56725b0 (feat) |
| 2 | DecodeBatch with per-alias error isolation (RED then GREEN) | 5819986 (test + fixtures), 5103bc8 (feat), 981b9c3 (refactor) |

## Behavior

- `BuildBatch` rejects an empty batch, more than `MaxBatch` refs, any ref that fails `!Valid()` (this blocks injection attempts like `evil){x}`, quotes and `..`), and duplicate refs, compared case-insensitively. When it returns an error it returns no query and no vars.
- `DecodeBatch` handles each failure mode this way:
  - A null repository, null pullRequest or missing alias gives `ErrNotFound`. If the alias error has type FORBIDDEN it gives `ErrForbidden` instead. The GraphQL messages are wrapped with `%w`.
  - A nested `<alias>.pullRequest.*` error is returned as a `"TYPE: message"` warning, and the snapshot is still returned.
  - Any RATE_LIMITED error gives a `*BatchError`, and `errors.Is(err, ErrRateLimited)` is true for it.
  - If a returned URL decodes to a different ref than the one requested, that alias gets an Err containing "ref mismatch".
  - If one alias fails JSON decoding, only that alias gets an Err.
- The doc comment says response size limiting belongs to ghclient (02-07).

## Deviations from Plan

1. **[Rule 2 - Missing critical functionality] Added `BatchData.Warnings`.** The plan said path-less errors that come with alias data should not fail the batch, but gave them nowhere to go, so they would have been dropped silently. They now land in `BatchData.Warnings`. Errors whose path points at no requested alias (for example `["viewer"]`) are handled as batch-level errors. Commit 5103bc8.
2. **[Rule 1] RATE_LIMITED is batch-level even when it has an alias path.** This follows the 02-02 executor's note that rate limiting is never tolerated. Commit 5103bc8.
3. I split the sentinel declarations into separate `var` statements so the plan's `grep 'var ErrNotFound'` acceptance check matches. Commit 981b9c3.

## TDD Gate Compliance

Both RED gates (0bc6e5a, 5819986) failed to compile before any implementation was written. Both GREEN gates (56725b0, 5103bc8) followed them, and the REFACTOR commit (981b9c3) kept the tests green.

## Verification

- `go test -race -count=1 ./internal/ghquery/` passes.
- `go vet` and `gofmt -l` are clean.
- `ls internal/ghquery/testdata/batch-*.json | wc -l` prints 4.
- The no-interpolation grep (`Sprintf.*Owner|+ r.Owner|+ ref.Owner`) prints nothing.

## Self-Check: PASSED
