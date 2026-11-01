---
phase: 01-toolchain-spikes-pure-core
plan: 09
subsystem: diff
tags: [scenarios, goldens, invariants, ci, evt-01, evt-02, evt-03, evt-04, evt-05, evt-06, evt-07, evt-08, dlv-05, dlv-06]
requires:
  - "01-04: diff.Baseline/Step (closed, conflict, commits, CI)"
  - "01-05: render.Lines grammar and MaxLineBytes"
  - "01-06: filter.ExcludeSelf and activity diff (reviews, comments, replies)"
  - "01-07: real fixture sequences, D-21 synthetics, scenarios/README.md expected-events table"
  - "01-08: setsid-probe spike (fixed here for Linux)"
provides:
  - "internal/diff/scenario_test.go: TestScenarios end-to-end harness (Decode -> Baseline/Step -> ExcludeSelf -> render.Lines) plus the runScenario helper"
  - "internal/diff/invariants_test.go: TestScenarioInvariants, global and per-scenario wording-independent assertions with a completeness check"
  - "expected.golden for all 21 scenario directories, approved by the user"
  - "Green CI for the phase head (ubuntu-latest + macos-latest, both Go versions, lint)"
affects: [phase-02 poller (consumes diff.Step output as-is), phase-03 delivery (message wording is now locked)]
tech-stack:
  added: []
  patterns:
    - "Every scenario replays its last snapshot once (`step replay <name>:`) to prove settle-confirm and exactly-once"
    - "Goldens record self-filtered events as untagged `  excluded (self): ...` lines so filtering is visible but never looks deliverable"
    - "Invariants assert on raw diff output (stepOutput.Raw) when the event is legitimately filtered before delivery"
key-files:
  created:
    - internal/diff/scenario_test.go
    - internal/diff/invariants_test.go
    - internal/diff/testdata/scenarios/*/expected.golden
    - spikes/setsid-probe/sid_darwin.go
    - spikes/setsid-probe/sid_other.go
  modified:
    - internal/diff/testdata/scenarios/synthetic-many-failures/01-nine-failures.json
    - internal/diff/testdata/scenarios/synthetic-many-failures/02-settle-repeat.json
    - internal/diff/testdata/scenarios/README.md
    - spikes/setsid-probe/main.go
    - internal/diff/lifecycle_test.go
    - internal/diff/snap_test.go
decisions:
  - "Message wording approved as-is: lowercase verdicts (`review changes_requested by X`), `closed: merged by X` / `closed: closed by X`, ci.settled failure lists every failing check name, force-push links to the compare URL"
  - "Self-authored pushes and force-pushes are filtered by EVT-08 like any other own activity; tests prove detection on raw diff output instead of weakening the filter"
  - "No production code in diff/ghquery/render needed changing: the real fixtures confirmed assumptions A1-A3"
metrics:
  duration: "about 20 min of executor time for Tasks 1-2 and CI fixes, plus the human review"
  completed: 2026-11-01
  tasks: 3
  files: 30
---

# Phase 1 Plan 09: End-to-end scenario goldens and invariants Summary

All 21 real and synthetic fixture sequences now run through the whole pure core (Decode, Baseline/Step, ExcludeSelf, render.Lines). The output is locked by per-scenario goldens that the user approved, and by wording-independent invariant tests. CI is green on macOS and Linux for the phase head. No production code needed changing: the real GitHub data matched the assumptions the diff was built on.

## Tasks

| Task | Name | Commit |
|---|---|---|
| 1 | TestScenarios harness and per-scenario goldens | 27ad352 |
| 2 | TestScenarioInvariants and CI gate | de8a569, CI fixes 226ea88 and 99d2635; green run https://github.com/drodimin/pr-relay/actions/runs/41356360410 |
| 3 | Human review of the scenario goldens and the spike report | none (the user replied "approved" with no changes) |

## What the goldens and invariants demonstrate

- **Exactly once (SC-3):** across every scenario, no event Key repeats, and the replay step emits nothing new except a pending ci.settled confirmation.
- **CI:** ci-flaky-rerun gives one `ci.failed: flaky` and one `ci.settled: passed` (D-06, D-08). ci-cancelled gives no ci.failed, because cancelled is neutral. synthetic-many-failures gives nine ci.failed lines with distinct links (D-03), followed by `ci.settled: failed (9 of 14): test-1, ...`.
- **Reviews and comments:** review-changes-requested-inline gives one review line with `(3 inline comments)` and no separate comment lines (D-10). Standalone comments and replies get one line each (D-11). A1 holds: replies have no wrapper review. Edits produce nothing (D-13). The real edit produced no snapshot change, and TestEditsIgnored covers it as a unit test.
- **Commits:** push gives `commits: 2 new`, and force-push gives one `force-pushed, head X (was Y)` event linking to the compare URL (EVT-05).
- **Conflict (D-21):** UNKNOWN never emits. synthetic-mergeable-unknown-conflicting emits exactly one `conflict: conflicting` at step 01. synthetic-mergeable-unknown-mergeable emits no lines. conflict-on-off emits `conflicting` and then `cleared`.
- **Closed:** merged and closed each emit once, even though drodimin was the actor (D-20).
- **Own activity (EVT-08, D-12, D-14):** nothing by drodimin (User) is delivered. bot-authors delivers all four github-actions[bot] events, and push-mixed-authors is delivered because it includes a second-account commit.
- **Safety (DLV-05, DLV-06):** every line matches the render grammar, stays within render.MaxLineBytes and contains no PRRELAY-BODY-SENTINEL.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] The synthetic-many-failures fixture shared one link across nine checks**
- **Found during:** Task 1
- **Issue:** All nine test-N runs had the same url/detailsUrl, which breaks D-03 (distinct links). Real GitHub gives each run its own URL.
- **Fix:** Rewrote them as url=`runs/<databaseId>` and detailsUrl=`job/<databaseId>`, and updated the `_synthetic` tag and README.
- **Files modified:** synthetic-many-failures/01-nine-failures.json, 02-settle-repeat.json, scenarios/README.md
- **Commit:** 27ad352

**2. [Rule 3 - Blocking] The 01-08 setsid-probe spike broke the Linux build and lint**
- **Found during:** Task 2 (first push)
- **Issue:** `syscall.Getsid` exists only on darwin.
- **Fix:** Split `sid()` into build-tagged files. sid_darwin.go uses syscall.Getsid, and sid_other.go uses RawSyscall(SYS_GETSID).
- **Commit:** 226ea88

**3. [Rule 3 - Blocking] Pre-existing staticcheck findings showed up once CI lint ran**
- **Issue:** S1009 (a redundant nil check) in lifecycle_test.go, and an unused withBase helper in snap_test.go.
- **Fix:** Removed both.
- **Commit:** 99d2635

### Interpretation notes (no code weakened)

- **Self-authored push and force-push:** the plan expected a delivered commits event in the push and force-push scenarios. Both real fixtures were pushed by drodimin, so EVT-08 correctly filters them. The goldens list them as indented `  excluded (self): ...` lines. The invariants check them on the raw diff.Step output (stepOutput.Raw) and assert that nothing is delivered. The filter was not relaxed. push-mixed-authors proves a delivered commits event.
- **Replay step:** the harness replays each scenario's last file once (`step replay <name>:`). This is where a pending ci.settled is confirmed. Synthetic scenarios emit nothing on replay, which the exactly-once invariant confirms.
- **Cleanup:** deleted a stray freebsd cross-compile binary at the repo root. It was never committed.

### Fixes from fixtures contradicting assumptions

None. A1 (replies have no wrapper review), A2 (contexts list all attempts) and A3 (Bot.login already includes [bot]) all held on the real data. No change was needed in internal/diff/{activity,ci,lifecycle}.go, internal/ghquery/decode.go or internal/render/render.go.

## User review

The user approved the wording as-is: lowercase verdicts such as `changes_requested`, `closed: merged by X` and `closed: closed by X`, ci.settled failures listing every failing check name, and force-push linking to the compare URL. The 01-SPIKES.md report was also reviewed with no changes.

## Requirements

EVT-01 through EVT-08, DLV-05 and DLV-06 are demonstrated at the pure-core level, which is this phase's scope. Actual pane delivery belongs to Phase 3.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: internal/diff/scenario_test.go, internal/diff/invariants_test.go and all 21 expected.golden files
- FOUND: commits 27ad352, de8a569, 226ea88 and 99d2635
