---
phase: 01-toolchain-spikes-pure-core
plan: 02
subsystem: core-contract
tags: [go, model, graphql, decoder, fixtures]
requires: ["01-01"]
provides:
  - internal/model data contract (PRRef, ParsePRURL, Actor, Snapshot, CheckRun, Review, ReviewComment, IssueComment, Commit, ForcePush, EventType, AllEventTypes, Event, Memory)
  - internal/ghquery.Query (embedded pr.graphql) and Decode([]byte) (model.Snapshot, RateLimit, error)
  - scripts/capture.sh fixture capture loop
affects: [01-04, 01-05, 01-06, 01-07, 01-08, 01-09]
tech-stack:
  added: []
  patterns: ["stdlib-only pure packages", "private wire structs -> public model", "go:embed shared query file", "table tests + synthetic fixtures"]
key-files:
  created:
    - internal/model/model.go
    - internal/model/model_test.go
    - internal/ghquery/pr.graphql
    - internal/ghquery/query.go
    - internal/ghquery/decode.go
    - internal/ghquery/decode_test.go
    - internal/ghquery/testdata/synthetic-basic.json
    - internal/ghquery/testdata/synthetic-mismatch.json
    - internal/ghquery/testdata/synthetic-errors.json
    - scripts/capture.sh
  modified: []
decisions:
  - "A null mergedBy decodes to Actor{} for unmerged PRs and to ghost only when merged=true, so unmerged PRs do not show a phantom merger"
  - "ClosedBy is the last ClosedEvent in timelineItems(last:10), which is chronological. Null event actors decode to ghost"
  - "UnknownConclusions is deduped and taken from the post-dedupe check set, in Key order"
  - "When CIKnown=false (rollup is for a stale commit), Checks, UnknownConclusions and ChecksTruncated are all left empty or false"
  - "capture.sh rewrites any non-blank string `body` anywhere in the payload (jq walk), not only the review body, as defence in depth. Whitespace-only bodies stay as they are, so HasBody semantics survive"
  - "ParsePRURL also rejects query strings, fragments, escaped paths, a non-lowercase host and signed numbers"
metrics:
  duration: "~15m"
  completed: 2026-10-31
  tasks: 2
  files: 10
---

# Phase 1 Plan 02: Model Contract, PRState Query and Decoder Summary

This plan adds a stdlib-only `internal/model` contract, one embedded `PRState` GraphQL query, and a pure `ghquery.Decode`. The decoder keeps only CheckRun nodes, keeps the newest check per key, gates checks on the head commit, turns null authors into ghost, and reduces the review body to `HasBody`. `scripts/capture.sh` captures fixtures with the same query file and is guarded to the dummy repo.

## Tasks

| Task | Name | Commits | Files |
|------|------|---------|-------|
| 1 | internal/model data contract | 262a2ef (RED), aae19f4 (GREEN) | internal/model/model.go, model_test.go |
| 2 | pr.graphql, embed, Decode, capture.sh | defe5f0 (RED), 5dea83a (GREEN) | internal/ghquery/*, scripts/capture.sh |

## Verification

- `go vet ./... && go test -race -count=1 ./internal/model/ ./internal/ghquery/` passes. `go test ./...` passes.
- `grep -c body internal/ghquery/pr.graphql` = 1, and `grep -c StatusContext` = 0.
- `grep -c 'EventType = "' internal/model/model.go` = 7.
- `bash -n scripts/capture.sh` passes and the script is executable. Running it with a mismatched `PR_RELAY_FIXTURE_REPO` exits 1 with "refusing to run" and creates no directory.
- I tested the jq body-rewrite filter by hand: non-blank bodies become `PRRELAY-BODY-SENTINEL`, and `""` and whitespace-only bodies stay as they are.
- All 12 named decode tests are present and pass, plus TestDecodeBasicFields. TestParsePRURL has 21 rejection cases. TestMemoryCloneDeep is present.

## Deviations from Plan

**1. [Note] The stdlib-only acceptance grep matches stdlib packages that have digits in their names**
- `go list -deps ./internal/model | grep -v '^[a-z/]*$' | grep -v pr-relay` prints `internal/chacha8rand`, `internal/trace/tracev2` and `unicode/utf8`. All three are stdlib, and they appear only because the regex has no digits.
- I confirmed stdlib-only status with `go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./internal/model`, which prints only the package itself. No code change was needed.

**2. [Setup] Worktree base fast-forward**
- The worktree started at 0f80d1b. The expected base a4e38aa is a direct descendant (the 01-01 SUMMARY commit), so I ran the prescribed `git reset --hard` to it before doing any work.

Otherwise the plan ran as written.

## TDD Gate Compliance

- Task 1: test commit 262a2ef came before feat commit aae19f4.
- Task 2: test commit defe5f0 came before feat commit 5dea83a.
- No refactor commits were needed.

## Known Stubs

None. The synthetic fixtures are intentionally decoder-mechanics only. Real behaviour fixtures come in plan 01-07.

## Threat Flags

None. The threat register (T-01-04..T-01-09) covers all new surface. T-01-04, 05, 07, 08 and 09 are mitigated and test-enforced. T-01-06 is transferred to internal/sanitize (01-03).

## Self-Check: PASSED

- FOUND: every file listed in key-files.created
- FOUND commits: 262a2ef, aae19f4, defe5f0, 5dea83a
