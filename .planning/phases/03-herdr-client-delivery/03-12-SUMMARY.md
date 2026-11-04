---
phase: 03-herdr-client-delivery
plan: 12
subsystem: deliver
tags: [herdr, delivery, gap-closure, live-verification, sc3, readiness]
gap_closure: true
requires:
  - 03-09 CR-01 fix (no ack on a wait-for-working timeout) and tri-state readiness gate
  - 03-10 isolated, fail-closed dev deliver and the read-back TestLiveHerdr
  - 03-11 lifecycle gap closure (cancel backoff, reconnect reconcile, injected jitter)
provides:
  - green automated gate on HEAD (race suite, flake 200/200, scratchguard refusal spot-check)
  - deferred-items.md entries for IN-01, IN-02 and IN-03; reachability flake marked resolved
  - live SC3 evidence against herdr 0.9.5 (approved by the user)
  - recorded herdr 0.9.5 readiness semantics (interactive_ready / launch_pending)
affects:
  - 03-VERIFICATION.md gap 1 (SC3 / DLV-08) closed on live evidence
  - phase 4 hardening backlog (IN-01..IN-03)
tech-stack:
  added: []
  patterns:
    - "Live verification runs only on a scratch herdr session that scratchguard accepts"
key-files:
  created:
    - .planning/phases/03-herdr-client-delivery/03-12-SUMMARY.md
  modified:
    - .planning/phases/03-herdr-client-delivery/deferred-items.md
decisions:
  - "On herdr 0.9.5 a Claude agent reports interactive_ready as an explicit true (not omitted) and launch_pending is never present; the tri-state gate handles both cases and cannot hold a ready agent forever"
  - "IN-01, IN-02 and IN-03 are deferred to phase 4 hardening (info severity, not a phase 3 success criterion)"
requirements: [DLV-08, DLV-01, LIFE-02, LIFE-04]
metrics:
  duration: ~45m (including live run)
  completed: 2026-11-04
  tasks: 2
  files: 2
---

# Phase 3 Plan 12: Live SC3 re-verification after gap closure Summary

The automated gate is green on HEAD after plans 03-09..03-11. Live SC3 then passed against a real herdr 0.9.5 scratch session: an update queued while the agent was busy survived a herdr stop and relaunch, appeared in the pane as `[pr-relay] drodimin/pr-relay-fixtures#20 ci.settled: passed (6 checks)`, and was acked only after that. Nothing was lost or duplicated.

## What was done

**Task 1: automated gate, binary build and deferred items (commit ae464ba)**
- `go build ./...`, `go vet ./...` and `go test -race ./... -count=1` all pass.
- `go test -race ./internal/deliver/ -run TestUnreachableReachability -count=200` passes 200 of 200.
- The TestLiveHerdr spot-check against the default socket fails with the scratchguard refusal and never dials (T-03-52).
- The binary is built at `$TMPDIR/pr-relay`.
- In deferred-items.md, the TestUnreachableReachability entry is marked "Resolved by 03-11 (WR-06: injected Jitter)". IN-01, IN-02 and IN-03 are recorded as phase 4 hardening candidates.

**Task 2: live SC3 re-verification (approved by the user)**

The orchestrator ran this at the user's request, on scratch session prspike only (socket `~/.config/herdr/sessions/prspike/herdr.sock`). The default session was never touched.

- Step 2, idle readiness (herdr 0.9.5, Claude agent): `interactive_ready: true` was PRESENT, though the plan expected it absent. `launch_pending` was absent. `completion_seq` was absent on the idle get. `interactive_ready: true` was also present right after `agent start`.
- Step 3, TestLiveHerdr: PASS. "pr-relay-live/test#762070 read back from pane w2:p1", then delivered, with acks recorded.
- Step 4, SC3 timeline (2026-11-03, EST):
  - 22:15:22: `dev deliver` started and printed "state dir ... (temporary)". The agent was given a long essay task with no tool use and started working.
  - 22:15:52: an empty commit was pushed to the fixture PR.
  - 22:17:24: the update `ci.settled passed (6 checks)` was queued while the agent was working.
  - 22:17:34: prspike was stopped. The log shows "herdr events connection dropped" and "herdr unreachable", then reconnect attempts 1 to 7.
  - 22:18:21: herdr was relaunched, and the agent was restored with `claude --resume <session>`.
  - 22:18:36: the log shows "herdr events connected" and "herdr reachable".
  - 22:18:46: `delivered pane=w2:p1 lines=1`. The pane shows `[pr-relay] drodimin/pr-relay-fixtures#20 ci.settled: passed (6 checks)` and the agent responded to it. The queue was empty afterwards.
  - Delivery took the confirmed path, so no "kept queued" re-send was needed.
- Step 4, relaunch readiness sampled every 0.5s: `interactive_ready` was always true and `launch_pending` was never present. Status sequence: idle, then unknown (agent null), then unknown (claude), then idle, then working (after delivery).
- Step 6: SIGINT made `dev deliver` exit. The temporary state dir was removed. The real state dir (`~/Library/Application Support/pr-relay`) does not exist, so no subscription leaked (T-03-53).
- Step 7, SC4 (optional): not run.

## Readiness semantics on herdr 0.9.5

On 0.9.5, herdr sends `interactive_ready` as an explicit `true` for a Claude agent, both when idle and right after launch or relaunch. `launch_pending` was never observed. The tri-state gate treats absent and true as ready, and only an explicit false as not ready. So it handles this case, and no false negative was observed. It cannot hold a ready agent forever. The not-ready window during relaunch showed up as `status: unknown` (agent null, then claude), and the existing agent and status gating handled it before the pane returned to idle.

## Deviations from Plan

**1. [Rule 3 - Blocking] Pane w2:p1 used instead of w1:p1**
- prspike had no w1:p1, only w1:p2, whose worktree cwd had been deleted. A workspace "sc3" was created, and Claude was started with `herdr agent start --kind claude` in pane w2:p1 (cwd /Users/drodi/repos/pr-relay). The workspace was closed afterwards.

**2. [Rule 3 - Blocking] Fixture PR #20 used instead of #19**
- Fixture PR #19 was closed and its branch deleted, so it could not be reopened. Temporary fixture PR drodimin/pr-relay-fixtures#20 (branch sc3-live-rerun) was created for the run. It was then closed and its branch deleted.

**3. [Rule 3 - Blocking] Headless herdr relaunch through a sized pty**
- herdr was stopped with `herdr session stop prspike` and relaunched with `script -q /dev/null sh -c 'stty rows 40 cols 140; exec herdr --session prspike'`, because herdr rejects a zero-size pty ("terminal reported a zero-sized grid"). The client was detached afterwards and the server left running.

**4. Readiness expectation differed**
- The plan expected `interactive_ready` to be absent on 0.9.5, but it was present as true. This does not affect correctness, because the tri-state gate treats true as ready. It is recorded above.

No product code was edited in this plan.

## Deferred Issues

IN-01, IN-02 and IN-03 are in deferred-items.md (phase 4 hardening candidates).

## Commits

| Task | Commit | Message |
|------|--------|---------|
| 1 | ae464ba | docs(03-12): record deferred IN-01..IN-03 and mark reachability flake resolved |
| 2 | none | manual live verification, approved by the user |

## Threat Flags

None. T-03-52 is mitigated: only the prspike socket was used, and the scratchguard refusal was spot-checked in Task 1. T-03-53 is mitigated: the temp state dir was removed and the real state dir does not exist.

## Known Stubs

None.

## Self-Check: PASSED

- .planning/phases/03-herdr-client-delivery/deferred-items.md is present and contains IN-03.
- Commit ae464ba is present in git log.
