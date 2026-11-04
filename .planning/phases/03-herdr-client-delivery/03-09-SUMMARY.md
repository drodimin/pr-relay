---
phase: 03-herdr-client-delivery
plan: 09
subsystem: deliver
tags: [herdr, delivery, gap-closure, at-least-once, readiness]
gap_closure: true
requires:
  - 03-08 live SC3 evidence (timeout ack lost an update after herdr restart)
provides:
  - tri-state AgentInfo.InteractiveReady (*bool)
  - settled() gate ahead of the D-13 identity check
  - no-ack on agent.prompt wait-for-working timeout, completion-based confirmation, exponential re-send backoff
  - unrenderable PR groups kept queued
affects:
  - internal/deliver worker delivery contract (doc.go)
  - plan 03-12 live SC3 re-verification
tech-stack:
  added: []
  patterns:
    - "Tri-state optional JSON booleans via *bool (absent vs explicit false)"
    - "Unconfirmed-delivery tracking keyed on completion_seq, never state_change_seq"
key-files:
  created: []
  modified:
    - internal/herdr/types.go
    - internal/herdr/herdrtest/server.go
    - internal/deliver/worker.go
    - internal/deliver/worker_test.go
    - internal/deliver/doc.go
decisions:
  - "A wait-for-working timeout is not delivery: updates stay queued and are re-sent (overrides RESEARCH A1/Pitfall 9; D-05 accepts duplicates, never losses)"
  - "Only a completion_seq advance past the pre-prompt value confirms an unconfirmed prompt; state_change_seq and working status are excluded because both move during relaunch"
  - "Absent interactive_ready counts as ready; only explicit false holds (herdr 0.9.5 omits the key on long-running agents)"
  - "Re-send backoff: Recheck doubled per consecutive unconfirmed prompt, capped at 16m, poke-interruptible"
  - "When every pending group is unrenderable the worker sleeps Recheck instead of re-gating every quiet period"
requirements: [DLV-08, DLV-01, DLV-03, LIFE-02]
metrics:
  duration: ~15m
  completed: 2026-11-03
  tasks: 2
  files: 5
---

# Phase 3 Plan 09: No-loss prompt confirmation and settled readiness gate Summary

The worker no longer acks a prompt whose wait-for-working timed out. It keeps the update queued and re-sends it with exponential backoff, unless a later agent.get shows completion_seq advanced. The readiness and identity checks now run only on snapshots herdr reports as settled, and readiness is tri-state, so the absent key on herdr 0.9.5 still delivers.

## What was built

**Task 1: tri-state readiness and the settled gate (WR-01, WR-02)**
- `herdr.AgentInfo.InteractiveReady` is now `*bool`. nil means not reported (ready). Only an explicit false means not ready.
- The fake herdr has a new `PaneState.NotReady` field. `agentJSON` emits `interactive_ready: false` only when the pane is LaunchPending or NotReady, and omits the key otherwise, which matches what 0.9.5 does.
- `func settled(info herdr.AgentInfo) bool` returns false on launch pending, on unknown or empty status, and on explicit not-ready. In `run()` it is called right after AgentGet and before `w.identify`, so a snapshot taken mid-relaunch can no longer cancel a subscription. The old gate that ran after identify is removed. The post-quiet re-check in `quietThenDeliver` now uses `!settled(info)`.

**Task 2: no ack on timeout, confirmation by completion, backoff, invalid refs (CR-01, IN-04)**
- New worker fields `unconfirmed *unconfirmedPrompt` (acks plus a copied pre-prompt CompletionSeq) and `unconfirmedRuns`.
- `deliver(ctx, subs, before)`: the CodeTimeout branch records the unconfirmed prompt, logs a WARN "prompt not confirmed (working not observed), kept queued", sleeps for `unconfirmedBackoff()` (60s, 120s, 240s, ... capped at 16m, interruptible by a poke) and returns without acking. A successful prompt clears the unconfirmed state.
- `confirmUnconfirmed(info, subs)` runs in `run()` after identify and before the status switch. It acks the recorded acks, limited to subs that still exist, only when completion_seq went from nil to a value or rose above the pre-prompt value.
- A group with no PRView and an unparsable key (`!ref.Valid()`) is logged at ERROR and skipped: it is neither prompted nor acked. If every group is unrenderable, the worker sleeps Recheck.
- `doc.go` now states the new ack contract and the settled-only identity check.

## Tests

- New: TestNeverPromptNotReady, TestNotReadyThenReadyDelivers, TestIdentityHeldWhileUnsettled, TestPromptTimeoutKeepsQueued, TestPromptTimeoutConfirmedByCompletion, TestPromptTimeoutStateSeqDoesNotConfirm, TestPromptTimeoutBackoff, TestInvalidRefKeepsQueued.
- Deleted: TestPromptTimeoutCountsDelivered, which asserted the lossy behavior.
- `go build ./... && go vet ./... && go test -race ./... -count=1` all pass.

## Commits

| Task | Gate | Commit | Message |
|------|------|--------|---------|
| 1 | RED | 87d4a48 | test(03-09): add failing readiness and unsettled-identity tests |
| 1 | GREEN | ec8440d | feat(03-09): tri-state readiness and settled gate ahead of identity check |
| 2 | RED | e520327 | test(03-09): add failing tests for no-ack on prompt timeout |
| 2 | GREEN | a561d7e | fix(03-09): never ack on a wait-for-working timeout (CR-01) |

## TDD Gate Compliance

Both tasks have a RED test commit followed by a GREEN implementation commit. Task 2's GREEN commit uses the `fix` type rather than `feat` because it corrects lossy behavior. No refactor commits were needed.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Missing critical functionality] No hot loop when every group is unrenderable**
- **Found during:** Task 2
- **Issue:** If the only pending groups are skipped as unrenderable, `deliver` returns with nothing sent. The worker would then re-gate after every 10s quiet period and log an ERROR each time.
- **Fix:** When groups are skipped and nothing is left to send, `deliver` sleeps Recheck (interruptible by a poke). TestInvalidRefKeepsQueued also asserts a herdr call bound.
- **Files modified:** internal/deliver/worker.go
- **Commit:** a561d7e

**2. Plan's verify commands target the main repo path**
- The plan's `<automated>` commands `cd` into /Users/drodi/repos/pr-relay. They were run from the worktree root instead, so the verification covers this branch.

The worktree was also reset to base 22c15f3 at startup, following the branch check.

## Threat Flags

None. No new surface. T-03-40, T-03-41 and T-03-42 are mitigated as planned. T-03-43 is accepted and documented in the `confirmUnconfirmed` comment.

## Known Stubs

None.

## Self-Check: PASSED

- internal/herdr/types.go, internal/herdr/herdrtest/server.go, internal/deliver/worker.go, internal/deliver/worker_test.go and internal/deliver/doc.go are all modified and present.
- Commits 87d4a48, ec8440d, e520327 and a561d7e are present in git log.
