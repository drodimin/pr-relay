---
phase: 01-toolchain-spikes-pure-core
plan: 07
subsystem: testing
tags: [fixtures, graphql, github, scenarios, D-18, D-21, D-22]
requires:
  - phase: 01-01
    provides: go module, CI, golden helper
  - phase: 01-02
    provides: internal/ghquery/pr.graphql, scripts/capture.sh, ghquery.Decode
provides:
  - "21 scenario directories (17 real, 4 synthetic) with 84 PRState fixtures under internal/diff/testdata/scenarios/"
  - "scenarios/README.md: catalogue, per-step expected events (diff and after ExcludeSelf), A1/A2/A3/A8 findings"
  - "scenarios/self.txt (drodimin)"
  - "TestDecodeAllFixtures and TestFixturesNoStatusContextLeak"
  - "scripts/fixture-repo template and the public dummy repo drodimin/pr-relay-fixtures"
affects: [01-09 TestScenarios / TestScenarioInvariants, phase 2 poller]
tech-stack:
  added: []
  patterns:
    - "Fixture capture driven by scratch scripts outside the repo; capture.sh always run as a background process and stopped with STOP"
    - "Synthetic fixtures derived only by jq mutation and tagged with a top-level _synthetic key"
key-files:
  created:
    - scripts/fixture-repo/.github/workflows/ci.yml
    - scripts/fixture-repo/.github/workflows/other.yml
    - scripts/fixture-repo/.github/workflows/actor.yml
    - scripts/fixture-repo/src/x.go
    - scripts/fixture-repo/src/y.go
    - scripts/fixture-repo/README.md
    - internal/diff/testdata/scenarios/README.md
    - internal/diff/testdata/scenarios/self.txt
    - internal/diff/testdata/scenarios/*/NN-*.json
    - internal/ghquery/fixtures_test.go
  modified: []
key-decisions:
  - "capture.sh deduplicates identical polls, so real scenarios record a settled CI state once. 01-09 should replay the last file one extra time to get ci.settled. Synthetic scenarios carry an explicit settle repeat"
  - "flaky (like slow) runs only on matching branch names (flaky-*), so the other scenarios keep a green CI baseline"
  - "Added an extra push-mixed-authors scenario: all other pushes are by self, so ExcludeSelf drops them. This one shows a delivered commits event"
patterns-established:
  - "Scenario dir layout: 00-baseline.json plus NN-<slug>.json steps, with expected events documented in README"
requirements-completed: [EVT-01, EVT-02, EVT-03, EVT-04, EVT-05, EVT-06, EVT-07, EVT-08]
duration: ~25min (Tasks 2-3, continuation)
completed: 2026-11-01
---

# Phase 1 Plan 07: Real GitHub Fixture Capture Summary

**84 real PRState snapshots from 17 scratch PRs on drodimin/pr-relay-fixtures, with three actor kinds (self, a second human dmitriy-medme, and the github-actions bot), plus 4 jq-derived synthetic scenarios (D-21 UNKNOWN baselines, STARTUP_FAILURE, 9 failures). Every fixture decodes, and the README records per-step expected events and the A1-A3 answers.**

## Performance

- **Duration:** about 25 min for Tasks 2-3. Task 1 was a human checkpoint, resolved before this continuation.
- **Started:** 2026-11-01T00:04:45Z
- **Completed:** 2026-11-01T00:20Z
- **Tasks:** 3 of 3 (Task 1 human, Tasks 2-3 automated)
- **Files created:** 6 template files, 84 fixtures, README.md, self.txt and fixtures_test.go

## Key facts

- **Dummy repo:** https://github.com/drodimin/pr-relay-fixtures (public). Its workflow permissions are set to write, with `can_approve_pull_request_reviews=true`.
- **Second account:** `dmitriy-medme`. It acted only through a per-command `GH_TOKEN=$(gh auth token --user dmitriy-medme)`. The active account stayed `drodimin` throughout, and no token was printed or written anywhere.
- **A1:** YES. A thread reply creates a new COMMENTED review with an empty body that wraps it (`thread-reply/01-reply-by-second.json` `.data.repository.pullRequest.reviews.nodes[1]`).
- **A2:** NO. contexts hold only the latest attempt. After `rerun --failed`, the attempt-1 runs vanish. Non-failed jobs are copied into attempt 2 with new databaseIds, and some of them are briefly **missing** (`ci-flaky-rerun/03-rerun-started.json` has totalCount 4, without lint or test).
- **A3:** NO. GraphQL `Bot.login` is `github-actions`, with no `[bot]` suffix (`bot-authors/01-bot-comment.json` `.comments.nodes[0].author`).
- **Single comment:** a "single comment" made through `POST /pulls/N/comments` also gets wrapped in a COMMENTED review.
- **UNKNOWN mergeable:** observed in real captures after head pushes, base pushes, at PR open and right after a merge. It is not guaranteed on every push.
- **A8:** github-actions[bot] approvals work.
- The full PRState query costs 1 rate-limit point (nodeCount 790).

## Task Commits

1. **Task 1: Human adds second account / approves repo.** No commit (checkpoint, resolved by the user: `repo=default second=dmitriy-medme`).
2. **Task 2: Template, dummy repo, CI/commit/lifecycle capture.** `43e9cff` (test)
3. **Task 3: Review/comment/bot/own-activity capture, synthetics, README, decode-all test.** `d03bd15` (test)

## Scenario Inventory

Real: ci-fail-pass, ci-flaky-rerun, ci-cancelled, push, push-mixed-authors (extra), force-push, conflict-on-off, mergeable-unknown, merged, closed, review-approved, review-changes-requested-inline, review-commented, issue-comment, thread-reply, bot-authors, own-activity.
Synthetic: synthetic-mergeable-unknown-conflicting, synthetic-mergeable-unknown-mergeable, synthetic-startup-failure, synthetic-many-failures.

A throwaway probe test (not committed) ran every scenario through `diff.Step` and `filter.ExcludeSelf`. The output matched the README's expected-events tables exactly. That covers D-03 (9 ci.failed lines), D-08 (no recovery event), D-10 (inline comments folded into the review), D-12 (bots delivered), D-20 (self merge/close delivered), D-21 (UNKNOWN baseline then CONFLICTING is announced, then MERGEABLE stays silent) and EVT-08 (own-activity delivers nothing attributed to drodimin).

## Decisions Made

- Real captures record a settled CI state once, because capture.sh writes only on change. 01-09 should replay the last step once to confirm the settle. The README documents this, and its tables include the "replay" row.
- The `flaky` job is gated with `if: contains(github.head_ref, 'flaky')`, the same way as `slow`. Without the gate, every PR would carry a failing check.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Gated the flaky job to flaky-* branches**
- **Found during:** Task 2 (template)
- **Issue:** As specified, `flaky` fails on attempt 1 of every run. That breaks ci-fail-pass ("remove FAIL, all pass") and every other scenario's green baseline.
- **Fix:** Added `if: contains(github.head_ref, 'flaky')`, matching the plan's preferred gating for `slow`. Elsewhere the job reports SKIPPED.
- **Files modified:** scripts/fixture-repo/.github/workflows/ci.yml
- **Commit:** 43e9cff

**2. [Rule 3 - Blocking] Re-ran ci-flaky-rerun on a new branch**
- **Found during:** Task 2
- **Issue:** A bug in the scratch driver script (the CI wait got the wrong argument) aborted the first run on PR #2.
- **Fix:** Fixed the driver and re-ran on branch `flaky-rerun-b` (PR #13). PR #2 was closed. No fixture from PR #2 is used.
- **Commit:** 43e9cff

**3. [Rule 2 - Missing coverage] Added the push-mixed-authors scenario**
- **Found during:** Task 3
- **Issue:** Every push in the plan is by drodimin, so `ExcludeSelf` drops every commits event and no fixture shows a delivered commits event.
- **Fix:** Added an extra scenario, PR #18. It pushes one commit authored as the second account's noreply identity and one by self.
- **Commit:** d03bd15

**4. [Documented gap] issue-comment has no edit snapshot**
- The edit was performed, but the query selects no body or updatedAt for issue comments, so the payload did not change and capture.sh wrote no file. The README records this as evidence that D-13 holds by construction.

### Other notes

- The README's expected-events tables were checked against the current engine with a temporary probe test (`internal/diff/zz_probe_test.go`). The probe was deleted before committing.
- The scratch PRs (#1, #3-#7, #9-#16, #18) are left open on the dummy repo. #2 and #8 are closed and #17 is merged.

## Issues Encountered

None blocking. `drodimin`'s token has no `workflow` scope. The push of the template's `.github/workflows/*` went over SSH and succeeded. `gh workflow run` worked with the `repo` scope.

## Known Stubs

None.

## Threat Flags

None. The commits were checked for tokens (`gh[op]_` pattern): 0 hits. Every body is "" or the sentinel, and TestDecodeAllFixtures enforces it. All writes went to drodimin/pr-relay-fixtures only. pr-relay was never pushed.

## Next Phase Readiness

- 01-09 can encode the README tables into TestScenarios and TestScenarioInvariants. Remember the final-step replay for real scenarios.
- Phase 2 note: during a rerun, checks can briefly drop out of the rollup (A2). Settle confirmation already handles this.

## Self-Check: PASSED

- FOUND: internal/diff/testdata/scenarios/README.md, self.txt, internal/ghquery/fixtures_test.go, scripts/fixture-repo/.github/workflows/actor.yml
- FOUND commits: 43e9cff, d03bd15
- `go test ./...` passes. TestDecodeAllFixtures checked 84 fixtures.
- No capture.sh or driver processes are running.
