---
phase: 02-durable-state-github-poller
verified: 2026-11-02T00:00:00Z
status: passed
score: 4/4 roadmap success criteria verified (with 5 recorded warnings)
overrides_applied: 0
gaps: []
warnings:
  - id: CR-01
    severity: WARNING (fix before relying on crash test as multi-PR evidence)
  - id: CR-02
    severity: WARNING (security; fix before Phase 3 delivers text into panes)
  - id: WR-01
    severity: WARNING
  - id: WR-02
    severity: WARNING
  - id: WR-05
    severity: WARNING
---

# Phase 2: Durable State & GitHub Poller Verification Report

**Phase Goal:** Subscriptions, baselines and pending events survive crashes and reboots, and one poller watches each PR once with rate-limit-safe adaptive intervals.
**Re-verification:** No, initial verification.
**Status:** passed, with warnings. Details on why the two review criticals are not gaps are below.

## Success Criteria

| # | Criterion | Status | Evidence |
|---|-----------|--------|----------|
| 1 | kill -9 neither replays delivered nor loses detected-undelivered events | VERIFIED (single-PR evidence) | `cmd/pr-relay/crash_test.go` `TestCrashLoop` (50 SIGKILL iterations, random kill points, 40 comments, one PR). Engine design is sound: global Seq, per-subscription `Pending`/`Ack(upTo)`, copy-on-write Clone, Save (temp, fsync, rename, dir fsync), Gen check. `TestAckPartial` in `internal/poller/engine_test.go`. Caveat: see CR-01. |
| 2 | Silent baseline; re-subscribe upserts | VERIFIED | `TestBaselineSilent` (poller and diff), `TestResubscribeUpsert`, crash test baselines 3 pre-existing comments and emits none. |
| 3 | One poll per PR per interval; gh token; token change picked up after 401 | VERIFIED | `TestOnePollPerPR` (`internal/poller/scheduler_test.go:205`), `TestTokenChangeAfter401` (:585), `TestInvalidate` (`internal/ghclient/client_test.go:395`), token via go-gh resolution. Live UAT: gh keyring auth worked, no extra setup. |
| 4 | Batched, logs cost, adaptive curve 1m to 1h, snap-back, backoff on low budget or secondary limit | VERIFIED | `internal/ghquery/batch.go` (aliased batching), `internal/poller/cadence.go`, `budget.go`, `scheduler.go` with tests. Live UAT: 1-point polls at 1m interval; A6 confirmed snap-back semantic. Caveat: WR-01 (edge path). |

I ran `go build ./...` and `go test -race -count=1 ./...` myself. All packages pass.

## Requirements Coverage

All 8 phase IDs are claimed by at least one PLAN frontmatter and are satisfied. REQUIREMENTS.md lists no extra Phase 2 IDs, so there are no orphans. Plan 02-02 also claims DLV-06, which is a Phase 1 requirement hardened here.

| ID | Plans | Status | Evidence |
|----|-------|--------|----------|
| SUB-04 | 02-11 | SATISFIED | `TestResubscribeUpsert` |
| SUB-08 | 02-11 | SATISFIED | `TestBaselineSilent` |
| EVT-09 | 02-02, 02-04(diff), 02-07, 02-09, 02-10, 02-11 | SATISFIED | crash test and engine Ack/Pending (CR-01 caveat) |
| LIFE-03 | 02-01, 02-07, 02-10 | SATISFIED | `internal/store` atomic save, flock, quarantine, restart tests |
| LIFE-05 | 02-05(cadence), 02-09, 02-12 | SATISFIED | cadence curve plus scheduler tests plus UAT |
| DMN-02 | 02-06, 02-11, 02-12 | SATISFIED | `TestOnePollPerPR` |
| DMN-03 | 02-07, 02-09, 02-10, 02-12 | SATISFIED | ghclient token and 401 tests, UAT |
| DMN-04 | 02-01, 02-02, 02-05, 02-06, 02-08, 02-09, 02-12 | SATISFIED (WR-01 caveat) | batching, budget, backoff tests |

Bookkeeping: REQUIREMENTS.md still shows these 8 as unchecked and "Pending" in the traceability table. Update them to Complete. This is documentation only.

## Disposition of review findings

Method: I confirmed CR-01 by reading `cmd/pr-relay/sink.go`. `maxSeq` is a single scalar, `write` skips `ev.Seq <= s.maxSeq`, and the crash test calls `start(withSink, prURL)` with exactly one PR. I did not re-run the CR-02 sanitizer repro. I accepted it from the review, and it is consistent with the plan-04 truth set, which lists only U+3164, U+115F, U+1160, U+FFA0 and U+2800.

**CR-01 (dev sink global high-water mark): non-blocking warning, not a gap.**
- The defect is in the dev-harness sink, which is the test oracle. The production durability path (store, engine commit, per-subscription `Ack`) is a different code path. Reading it, it is correct for many subscriptions: Ack removes only that subscription's events with seq at or below the cutoff.
- SC1 asks for no replay and no loss, verified by crash tests. For one PR, which is the tested scenario, the criterion is met. Product behavior is not demonstrably wrong.
- What is overstated is the evidence. The crash test proves exactly-once only for one PR and one subscription. Multi-PR crash safety is by design and unit test (`TestAckPartial`), not by crash test. The sink also silently loses events in normal multi-PR runs, so it is unsafe to use as an oracle for more than one PR.
- Fix it before Phase 4 reuses the sink or the crash test as a regression guard. Use a per-PR or per-subscription high-water mark, and add a two-PR crash test.
- If you want a strict reading (SC1 must be proven for multi-PR), reclassify this as `gaps_found`. I did not, because the goal is about the system's guarantees and the engine path honors them.

**CR-02 (sanitizer bypass via U+034F, U+17B4, U+17B5): non-blocking for Phase 2, but fix before Phase 3.**
- It does not touch any Phase 2 roadmap success criterion or phase requirement ID. It belongs to DLV-06 (Phase 1, already marked Complete) and plan 02-04's hardening truths. Every plan-04 truth as written (fullwidth, Cyrillic/Greek lookalikes, listed fillers, double-tag render guard, byte-identical ASCII) is met and tested.
- It is a real residual security hole in a threat the plan set out to close in spirit. Nothing sends text into panes until Phase 3 (herdr delivery), so there is no live exposure yet.
- Recommendation: a fix task at the start of Phase 3, or a `/gsd-quick` fix now. Strip all Default_Ignorable code points, make tag matching ignore Mn/Me, and add the repro strings to `testutil.HostileCorpus`.

**WR-01 (secondary or primary limit on the gap re-fetch not fed to the budget): warning.**
- DMN-04 and SC4 are met on the main batch path, which is tested. The hole is the rare wide-window re-fetch, which only fires when reviews or comments exceed the normal window. Impact is a possible repeated hit on a secondary limit, not a failure of the normal backoff. Fix soon.

**WR-02 (wide re-fetch transient failure permanently drops activity): warning.**
- It affects only the rare gap path, and it is signalled by a "dropped" marker, so loss is announced rather than silent. It does erode EVT-09's no-loss spirit for overflow cases. Fix with WR-01 (do not commit gapped PRs on transient error).

**WR-05 (Save error after rename leaves memory behind disk): warning.**
- It requires a directory fsync failure after a successful rename, which is rare. Worst case is a re-delivered event, which is a duplicate, not a loss, so it is an at-least-once degradation on a disk-fault path and not a violation of "normal operation" in EVT-09. Fix by treating post-rename failure as committed, or correct the false comment.

Other findings: WR-03 (callback under `pollMu`; matters for Phase 3 if the callback calls `PollNow`), WR-04 (subscribing to an already merged PR yields no signal; a Phase 4 CLI concern), WR-06 and WR-07 (dev-harness torn line, `min_interval` floor), and IN-01 to IN-08 are not goal-blocking. Track them for Phases 3 and 4.

## Human Verification

None outstanding. Live UAT on 2026-11-02 (02-12-SUMMARY.md) covered gh keyring auth, 1-point polls at a 1m interval, delivery of another account's comment, own-comment filtering and no replay on restart. The user confirmed A6.

## Anti-patterns

I did not run a full marker scan. The review reports no TBD, FIXME or XXX debt markers.

## Gaps Summary

No blocking gaps. The phase goal is achieved in the engine, store and poller. Recommended follow-ups before or at the start of the next phase: CR-02, CR-01 (plus a multi-PR crash test), WR-01 and WR-02, WR-05, and the REQUIREMENTS.md traceability update.

_Verified: 2026-11-02_
_Verifier: Claude (gsd-verifier)_
