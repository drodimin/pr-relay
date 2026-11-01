---
phase: 01-toolchain-spikes-pure-core
verified: 2026-11-01T23:00:00Z
status: passed
score: 5/5 must-haves verified
overrides_applied: 0
gaps: []
carry_forward:
  - id: CR-01
    severity: warning-for-phase-2
    note: "Sliding-window false positive (deleted comment/review resurfaces an old unseen item). Add createdAt/submittedAt watermarks before Phase 2 poller relies on exactly-once (EVT-09)."
  - id: CR-02
    severity: must-fix-before-phase-3
    note: "Lookalike (fullwidth/Cyrillic) forms of the [pr-relay] tag survive sanitize/render. Must be fixed before Phase 3 delivers lines into real panes."
  - id: WR-01..WR-07
    severity: robustness
    note: "See table below. None defeats a Phase 1 success criterion."
---

# Phase 1: Toolchain, Spikes, Pure Core: Verification Report

**Goal:** Go toolchain working, risky herdr/GitHub behaviours answered in writing, and a pure fixture-tested core that turns PR state changes into correct, safe, terse events and messages.
**Status:** passed (with carried-forward review items)
**Re-verification:** No, initial verification

## Observable Truths (ROADMAP success criteria)

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | Go installed; repo builds and tests green on macOS and Linux via CI | VERIFIED | `go build ./...` and `go test -count=1 ./...` pass locally (8 packages ok). `.github/workflows/ci.yml` matrix `[ubuntu-latest, macos-latest]`. `gh run list`: latest main runs (41357475770, 41356360410) completed success. |
| 2 | Written spike report (scratch panes only) answering typing collision, agent.wait while blocked and wait option, pane/terminal_id stability across restart, GraphQL shape and cost, setsid survival | VERIFIED | `01-SPIKES.md` has S0-S7 with Question/Method/Answer/Evidence/Implication. S1 merges with draft; S2 wait spans blocked with explicit `until`, `prompt --wait` returns `agent_blocked`; S3 pane_id stable, terminal_id not, agent_session stable; S4 about 0.25 pts/PR, batch of 20 costs 5; S5 setsid child survives session stop. Isolated via guarded prspike socket (S0). Note: `agent_prompt_stalled` unobserved and S6 payload limit UNKNOWN; both honestly recorded and not required by the criterion. |
| 3 | Real GraphQL fixture pairs yield exactly one event per type (ci.failed, ci.settled, review, comment, commits, conflict, closed); flaky reruns no duplicate; UNKNOWN emits nothing; force-pushes detected | VERIFIED | `internal/diff` (diff, lifecycle, ci, activity) with scenario goldens under `internal/diff/testdata/scenarios/` (ci-fail-pass, ci-flaky-rerun, ci-cancelled, conflict-on-off, force-push, closed, issue-comment, bot-authors, ...). All diff tests pass, plus invariants test. Flaky and UNKNOWN covered by scenarios `ci-flaky-rerun`, `conflict-on-off`, `force-push`. |
| 4 | gh-authenticated user's activity never appears; bots and other humans treated the same | VERIFIED | `internal/filter` with tests passing; `bot-authors` scenario golden; `closed-by-self` scenario. Review WR-01 notes an edge where an unknown-pusher large push is not self-filtered; this is an attribution edge, not a defeat of the criterion for fixture pairs. |
| 5 | Messages carry [pr-relay] tag, PR identity, names and URLs only (no bodies); hostile corpus neutralized and capped in every interpolated field | VERIFIED (with caveat) | `internal/render` and `internal/sanitize` with `hostile_test.go`, `fuzz_test.go`, `testutil.HostileCorpus`, all passing; render tests assert no bodies. Caveat: CR-02 lookalike-bracket spoof is outside the corpus and is not neutralized (see below). |

**Score:** 5/5

## Requirements Coverage

All plan-declared IDs: EVT-01..EVT-08, DLV-05, DLV-06 (plus phase-internal SC-1, SC-2, D-22). Every ID is marked Complete in REQUIREMENTS.md (lines 24-31, 40-41, 130-131, 143-150) and mapped to Phase 1. EVT-09 is mapped to Phase 2 (Pending), so it is correctly not claimed. No orphaned requirements.

| Req | Status | Evidence |
|-----|--------|----------|
| EVT-01, EVT-02 | SATISFIED | ci.go, scenarios ci-fail-pass / ci-flaky-rerun / ci-cancelled |
| EVT-03, EVT-04 | SATISFIED | activity.go, scenarios issue-comment / bot-authors |
| EVT-05 | SATISFIED | lifecycle.go, force-push scenario (WR-01 edge noted) |
| EVT-06 | SATISFIED | conflict-on-off scenario, UNKNOWN emits nothing |
| EVT-07 | SATISFIED | closed scenario (WR-03 attribution edge noted) |
| EVT-08 | SATISFIED | internal/filter, tests |
| DLV-05 | SATISFIED | internal/render goldens |
| DLV-06 | SATISFIED for control chars/ESC/newline/length; lookalike tag hardening open (CR-02) |

## Code Review Triage (01-REVIEW.md: 2 critical, 7 warnings)

| Finding | Defeats a Phase 1 SC? | Disposition |
|---------|----------------------|-------------|
| CR-01 deleted comment resurfaces old comment (sliding window) | No. SC3 is about fixture pairs yielding one event per change; the defect needs more than 30 comments plus a deletion. It does threaten EVT-09 exactly-once. | Carry to Phase 2 (add watermarks and scenario golden). Not a Phase 1 gap. |
| CR-02 lookalike spoof of [pr-relay] tag | Borderline. SC5 as written (control chars, ESC, newlines, over-length, cap) is met and the corpus passes. The package doc promise "can never spoof the envelope" is overstated, and DLV-06's stated aim (prompt injection) is only partly met. | Must fix before Phase 3 delivery into real panes (NFKC fold plus bracket-lookalike neutralization, add to HostileCorpus). Recommended as an early Phase 2 task or a small gap-closure plan. Does not block the phase verdict because no delivery path exists yet. |
| WR-01 30+ commit push reported as force-push | No; force-push detection (SC3) works on fixtures. Wrong label on a large normal push. | Phase 2 (use CommitsTotal) |
| WR-02 dedupe keys repeat across reopen / head return | No. Keys only matter once Phase 2/3 dedupe on them. | Phase 2 (epoch in Memory) |
| WR-03 closed names earlier closer | No | Phase 2 (decode ReopenedEvent) |
| WR-04 any GraphQL error discards response | No (decoder is pure; real-poller robustness) | Phase 2 poller |
| WR-05 newest-databaseId per Key hides parallel-trigger failure | No for fixtures; real correctness edge | Phase 2 |
| WR-06 review comments beyond 30 silently missed | No | Phase 2 (gap signal) |
| WR-07 PRRef accepts "." and ".." | No in pure core; becomes path-traversal risk when Phase 3+ keys state on PRRef | Fix before persistence (Phase 2); trivial change |

Info items (IN-01..IN-07) are hardening only. IN-01 (invisible filler chars) pairs with CR-02.

## Anti-Patterns

No TBD/FIXME/XXX debt markers found in `internal/` or `cmd/`. No stubs observed; core packages have substantive logic and goldens.

## Human Verification

None required. Spike results were human-driven in a scratch session and are recorded with evidence; CI status was confirmed from run list.

## Gaps Summary

No blocking gaps. The phase goal is achieved for the pure, fixture-tested core. Recommended (non-blocking) follow-ups: fix CR-02 and WR-07 early, and CR-01 plus WR-01..WR-06 inside Phase 2 planning.

_Verified: 2026-11-01_
_Verifier: Claude (gsd-verifier)_
