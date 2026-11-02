---
phase: 02-durable-state-github-poller
plan: 12
status: draft (Task 2 pending human sign-off)
subsystem: ghquery / live verification
tags: [live-github, fixtures, graphql, verification]
requires: ["02-11"]
provides:
  - internal/ghquery/testdata/batch-live.json (real two-alias PRBatch response)
  - TestDecodeBatchLiveFixture
affects: [internal/ghquery]
tech-stack:
  added: []
  patterns: ["go test -overlay helper kept in the session scratchpad to print BuildBatch request bodies without touching the repo"]
key-files:
  created:
    - internal/ghquery/testdata/batch-live.json
    - internal/ghquery/batch_live_test.go
  modified:
    - internal/ghquery/batch_test.go
decisions:
  - "A5 verified live: an Actions rerun (runAttempt 2) keeps event=pull_request, so the @event key suffix is stable across reruns"
requirements: [DMN-03, DMN-04, LIFE-05]
metrics:
  completed: pending (Task 2)
  tasks: 1/2
---

# Phase 2 Plan 12: Live GitHub verification and sign-off Summary (DRAFT)

Live probes against drodimin/pr-relay-fixtures confirm A1, A2 and A5. A real two-alias PRBatch response is committed as a regression fixture with a decode test. Dev watch authenticates through the gh keyring with no env token and logs cost and remaining budget on every poll. **Task 2 (human sign-off and the A6 answer) is pending.**

All GitHub calls were read-only. Nothing was pushed, commented, rerun or otherwise written.

## Task 1: Live probes and fixture (commit d94ff55)

### A1: GitHub accepts the fragment-plus-variables document

Via gh (no GH_TOKEN/GITHUB_TOKEN set):

```
$ gh api graphql -F query=@internal/ghquery/pr.graphql -F owner=drodimin -F repo=pr-relay-fixtures -F number=1 --jq '.errors // "none"'
none
```

Via the daemon's own POST (`PR_RELAY_LIVE=1 go test -count=1 -run TestLiveBatch -v ./internal/ghclient/`):

```
=== RUN   TestLiveBatch
    live_test.go:51: cost=1 remaining=4999 nodeCount=1580
--- PASS: TestLiveBatch (1.01s)
PASS
ok  	github.com/drodimin/pr-relay/internal/ghclient	1.349s
```

**A1: VERIFIED.**

### A2: A missing PR fails only its own alias

The same TestLiveBatch run batches #1 and #99999. It asserts that item 0 decodes with a non-empty HeadOID, that item 1 is `errors.Is(err, ghquery.ErrNotFound)`, and that there is no batch-level error. It passed (output above). Dev watch showed the same result on every poll (`err=not_found` for #99999 only, see below).

**A2: VERIFIED.**

### A5: An Actions rerun keeps the workflowRun.event of its first attempt

The fixture is the ci-flaky-rerun PR #13. Its CI run 41255535711 was rerun with `--failed` in plan 01-07. The `statusCheckRollup.contexts` query with `checkSuite { workflowRun { runAttempt event } }` returned:

```
CI/lint run=41255535711 attempt=2 event=pull_request conclusion=SUCCESS
Other/build run=41255535713 attempt=1 event=pull_request conclusion=SUCCESS
CI/test run=41255535711 attempt=2 event=pull_request conclusion=SUCCESS
CI/flaky run=41255535711 attempt=2 event=pull_request conclusion=SUCCESS
CI/slow run=41255535711 attempt=2 event=pull_request conclusion=SKIPPED
CI/build run=41255535711 attempt=2 event=pull_request conclusion=SUCCESS
```

The original attempt, read through REST (`GET /repos/drodimin/pr-relay-fixtures/actions/runs/41255535711/attempts/1` and `.../runs/41255535711`):

```
run=41255535711 attempt=1 event=pull_request conclusion=failure
run=41255535711 attempt=2 event=pull_request conclusion=success (latest)
```

Attempt 1 (failed, the original `pull_request` trigger) and attempt 2 (the rerun) both report `event=pull_request`. **A5: VERIFIED** from existing data. No rerun was triggered.

### Live batch fixture

- The request body is `BuildBatch([#1, #13], NormalWindow)`, printed by a one-off `go test -overlay` helper that lives only in the session scratchpad. It was sent with `gh api graphql --input <file>` and piped through the capture.sh `walk(... .body = "PRRELAY-BODY-SENTINEL")` jq rewrite.
- Result: `errors: none`, `viewer=drodimin cost=1 remaining=4997 pr0=1 pr1=13`, 15375 bytes.
- `grep -c 'gh[op]_' internal/ghquery/testdata/batch-live.json` printed `0`. The fixture has no email addresses. These two PRs have no comment or review bodies, so the fixture contains no `body` keys at all.
- `TestDecodeBatchLiveFixture` checks: no batch error and no warnings, both items decode with the requested Ref and a non-empty HeadOID, every check has a non-empty Key, Viewer is set, rateLimit.cost >= 1, and badBodies is clean.
- `go test -count=1 ./internal/ghquery/` and `go test -race -count=1 ./...` both pass.

### Dev watch (DMN-03, DMN-04)

The binary was built from the worktree and run as `pr-relay dev watch .../pull/1 .../pull/99999`, with GH_TOKEN and GITHUB_TOKEN unset and PR_RELAY_STATE_DIR in the session scratchpad. It ran for about 185 s and was stopped with SIGINT (exit 0).

stdout:

```
poll prs=2 cost=1 remaining=4996/5000 reset=2026-11-02T17:36:41Z forced=false
  drodimin/pr-relay-fixtures#1 new=0 interval=1m0s next=2026-11-02T16:39:37Z
  drodimin/pr-relay-fixtures#99999 new=0 interval=1m0s next=2026-11-02T16:39:37Z err=not_found
poll prs=2 cost=1 remaining=4997/5000 reset=2026-11-02T17:36:47Z forced=false
  drodimin/pr-relay-fixtures#1 new=0 interval=1m0s next=2026-11-02T16:40:31Z
  drodimin/pr-relay-fixtures#99999 new=0 interval=1m0s next=2026-11-02T16:40:31Z err=not_found
poll prs=2 cost=1 remaining=4995/5000 reset=2026-11-02T17:36:41Z forced=false
  ...
poll prs=2 cost=1 remaining=4994/5000 reset=2026-11-02T17:36:41Z forced=false
  ...
```

stderr (first lines):

```
level=INFO msg=poll prs=2 cost=1 remaining=4996 limit=5000 reset_at=2026-11-02T17:36:41.000Z node_count=1580 stretch=1 batch_size=20 duration_ms=877
level=INFO msg="github auth ok" token_source=gh
```

Observations:

- Auth went through the gh keyring fallback (`token_source=gh`) with no prompt.
- Every poll logs cost and remaining on both stdout and the slog line. The 2-PR batch costs 1 point, with node_count=1580.
- `err=not_found` for #99999 on every poll, while #1 decodes normally (A2 in production code).
- `interval=1m0s` throughout, which is expected for quiet PRs. Polls came about 54 s apart (11:38:38, 11:39:32, 11:40:26, 11:41:20), consistent with jitter on the 1 m interval.
- GitHub's `remaining` was not monotonic (4996, 4997, 4995, 4994) and `resetAt` changed between 17:36:41 and 17:36:47. This looks like GitHub-side eventual consistency across API nodes. Something to keep in mind if budget logic ever compares successive `remaining` values strictly.
- `grep -c 'gh[op]_'` printed 0 for both the stdout and stderr logs. The state dir held `state.json` and `state.lock`, both at mode 0600 in a 0700 directory.
- No events were printed, because nothing changed on #1 during the window.

## Task 2: Human sign-off (PENDING)

Awaiting the user. The steps are in 02-12-PLAN.md Task 2: live dev watch, a second-account comment, a self comment, restart dedup, a review of this draft, and the **A6** answer. A6 has not been answered. This summary will be completed once the user replies.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] TestBatchFixturesHaveNoBodies hard-coded 4 batch fixtures**
- **Found during:** Task 1
- **Issue:** The plan-mandated name `batch-live.json` matches the `testdata/batch-*.json` glob, which asserted exactly 4 files, so `go test ./internal/ghquery/` failed.
- **Fix:** The expected count is now 5, with a comment. The live fixture is therefore also covered by that test's body check.
- **Files modified:** internal/ghquery/batch_test.go
- **Commit:** d94ff55

## Known Stubs

None.

## Self-Check (Task 1): PASSED

- FOUND: internal/ghquery/testdata/batch-live.json
- FOUND: internal/ghquery/batch_live_test.go
- FOUND: commit d94ff55
