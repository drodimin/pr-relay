---
phase: 03-herdr-client-delivery
plan: 11
subsystem: deliver
tags: [herdr, delivery, gap-closure, lifecycle, reconnect, determinism]
gap_closure: true
requires:
  - 03-09 settled gate, identify ordering and the deliver(before) signature in worker.go
provides:
  - worker.cancel returns its error, and identify reports failed cancels
  - bounded retry on cancel failure (no hot loop, no exit-and-respawn)
  - reconnect reconciliation of worker-less subscribed panes (LIFE-04)
  - pane_moved guarded against no-op moves
  - deliver.Options.Jitter injection, passed through to herdr.EndpointOptions
affects:
  - plan 03-12 live SC3 re-verification
tech-stack:
  added: []
  patterns:
    - "A failed irreversible action holds the pane for Recheck instead of continuing or exiting"
    - "Injectable jitter keeps reconnect timing deterministic in synctest"
key-files:
  created: []
  modified:
    - internal/deliver/worker.go
    - internal/deliver/worker_test.go
    - internal/deliver/fakeengine_test.go
    - internal/deliver/manager.go
    - internal/deliver/manager_test.go
decisions:
  - "A failed cancel holds the whole pane for Recheck, even if other subs on it match. Cancel failures are rare store save errors, and a bounded delay is safer than a hot loop"
  - "noAgent never exits while a pane-gone cancel failed, because exiting with subs left makes the Manager respawn the worker at once"
  - "pane_moved with an empty previous id, or a previous id equal to the new one, only pokes the pane's worker"
  - "KindReconnected starts a startCheck for every subscribed pane that has no worker, after sync. RestoreGrace still guards each check through paneGone"
requirements: [LIFE-02, LIFE-04, DLV-08]
metrics:
  duration: ~20m
  completed: 2026-11-03
  tasks: 2
  files: 5
---

# Phase 3 Plan 11: Lifecycle correctness gap closure (WR-03..WR-06) Summary

A failed subscription cancel now backs off for Recheck instead of hot-looping or causing the worker to exit and respawn. After a herdr reconnect, every idle subscribed pane is re-checked with pane.get, so a pane closed during the outage is cancelled with pane_gone. pane_moved no longer cancels when the pane id did not change. Reconnect jitter is injectable, so TestUnreachableReachability is deterministic.

## What was built

**Task 1: a failed Cancel backs off (WR-03, T-03-48)**
- `func (w *worker) cancel(s store.Subscription, reason string) error` returns nil on success or ErrUnknownSub. On any other error it logs "cancel failed" and returns the error.
- `identify` returns `(matching, held, failed)`.
- In `run`, when `failed || (len(matching)==0 && held)`, the worker calls `markCannot(now)` and sleeps Recheck, then re-gates. When `len(matching)==0` and nothing failed (every sub was cancelled), it still continues at once, and the loop exits.
- `quietThenDeliver` does not deliver when a cancel failed.
- In `noAgent`'s pane-gone branch, a failed cancel calls markCannot, sleeps Recheck and returns false. The worker keeps running.
- `fakeEngine.setFailCancel(err)` makes Cancel fail without deleting the sub.

**Task 2: reconnect reconcile, guarded pane_moved and jitter (WR-04, WR-05, WR-06)**
- `Options.Jitter func() float64` is stored as `Manager.jitter` and passed as `Jitter: m.jitter` to `herdr.EndpointOptions`. The test harness injects `func() float64 { return 0 }`.
- KindPaneMoved: if `ev.PreviousPaneID == "" || ev.PreviousPaneID == ev.PaneID`, the Manager only pokes the worker at the new id. Otherwise the D-15 cancel of the previous id still runs.
- KindReconnected: after the worker pokes and `sync`, every subscribed pane on that socket with no worker gets `startCheck`. The comment notes the extra pane.get on pane-set resubscribes (IN-03, deferred).

## Tests

- New: TestCancelFailureNoHotLoop, TestPaneGoneCancelFailureHolds, TestReconnectRechecksIdlePanes, TestPaneMovedSameIDPokes, TestPaneMovedEmptyPreviousNoCancel.
- The existing TestPaneMovedCancels still passes (a move to a different id still cancels).
- `go test -race ./internal/deliver/ -run TestUnreachableReachability -count=200` passes.
- `go build ./... && go vet ./... && go test -race ./... -count=1` all pass.

## Commits

| Task | Gate | Commit | Message |
|------|------|--------|---------|
| 1 | RED | 5dd2594 | test(03-11): add failing tests for cancel-failure hot loop (WR-03) |
| 1 | GREEN | dca4f0f | fix(03-11): back off on failed cancel instead of hot-looping (WR-03) |
| 2 | RED | 7882b70 | test(03-11): add failing tests for reconnect recheck, guarded pane_moved, jitter (WR-04/05/06) |
| 2 | GREEN | b51b069 | fix(03-11): reconcile idle panes on reconnect, guard pane_moved, inject jitter (WR-04/05/06) |

## TDD Gate Compliance

Both tasks have a RED test commit followed by a GREEN commit. The GREEN commits use the `fix` type because they correct lifecycle bugs.

- Task 1 RED: the new tests hung inside synctest, because the hot-looping worker never let `synctest.Wait` settle. That is the defect itself.
- Task 2 RED: the commit failed to compile because `Options.Jitter` did not exist yet. With only the Jitter field added, TestPaneMovedSameIDPokes failed (the sub was cancelled and no prompt was sent) and TestReconnectRechecksIdlePanes failed (no cancel).
- TestPaneMovedEmptyPreviousNoCancel already passed before the fix, because `cancelPane` ignores an empty pane id. It is kept as a regression guard for the new explicit branch.

## Deviations from Plan

**1. Verify commands run from the worktree root**
- The plan's `<automated>` commands `cd` into /Users/drodi/repos/pr-relay. They were run from the worktree root instead, so the verification covers this branch.

**2. REQUIREMENTS.md not modified**
- LIFE-02, LIFE-04 and DLV-08 stay Pending. Plan 03-12 re-verifies SC3 live, and the phase verifier owns marking them complete.

At startup the worktree was reset from 5cd46a0 to the expected base e53e7bb, following the branch check.

## Threat Flags

None. No new surface. T-03-48, T-03-49 and T-03-50 are mitigated as planned. T-03-51 is accepted, because the cancel-failure logs still carry only sub id, reason and error.

## Known Stubs

None.

## Self-Check: PASSED

- The five modified files in internal/deliver are present.
- Commits 5dd2594, dca4f0f, 7882b70 and b51b069 are present in git log.
