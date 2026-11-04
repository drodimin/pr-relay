---
phase: 03-herdr-client-delivery
verified: 2026-11-03T18:00:00Z
status: gaps_found
score: 4/5 roadmap success criteria verified
overrides_applied: 0
gaps:
  - truth: "SC3 / DLV-08: Restarting herdr or dropping the socket leaves queued updates intact and delivery recovers without losing queued updates"
    status: failed
    reason: "Live run (03-08) showed an update acked but never typed into the pane after a herdr restart. internal/deliver/worker.go ~L337 treats agent.prompt CodeTimeout (wait-for-working) as delivered and calls w.ack. The relaunched agent was idle but not interactive-ready, so the text was dropped. (CR-01, confirmed in code at verification time.)"
    artifacts:
      - path: "internal/deliver/worker.go"
        issue: "case herdr.CodeTimeout falls through to w.ack(res.Acks); loses the update"
      - path: "internal/deliver/worker_test.go"
        issue: "TestPromptTimeoutCountsDelivered asserts the lossy behaviour"
      - path: "internal/deliver/doc.go"
        issue: "Documents ack-on-timeout as contract (L20-22)"
      - path: "internal/deliver/live_test.go"
        issue: "Passes on ack alone; never confirms text in the pane"
    missing:
      - "On CodeTimeout keep the update queued (return without ack) and re-gate; at-least-once duplicate is acceptable per D-05"
      - "Invert TestPromptTimeoutCountsDelivered (pending stays, second prompt sent); update doc.go"
      - "Make TestLiveHerdr confirm text appears in the pane (pane read) before asserting ack"
  - truth: "SC3 / DLV-08 (contributing): delivery only to a pane that can actually receive input"
    status: failed
    reason: "WR-01: worker never checks AgentInfo.InteractiveReady (decoded at internal/herdr/types.go:46, read nowhere outside tests). A relaunched agent that is idle but not ready receives the prompt. grep confirms no non-test use."
    artifacts:
      - path: "internal/deliver/worker.go"
        issue: "Gate at ~L168 and post-quiet re-check ~L201 only test LaunchPending and unknown status"
      - path: "internal/herdr/herdrtest/server.go"
        issue: "Sets interactive_ready = !launch_pending (L700-701); idle-but-not-ready is untestable"
    missing:
      - "Treat !InteractiveReady like LaunchPending in both gates (markCannot + recheck); let the fake set flags independently; add idle+not-ready test; confirm on herdr 0.9.5 that long-idle agents report ready=true"
  - truth: "LIFE-02 / LIFE-04 / SC5: identity/closure cancellation is safe and complete"
    status: partial
    reason: "WR-02: destructive identity verdict (cancels sub, drops queue) runs before the launch-pending/unknown-status gate, so a transient mid-relaunch snapshot can irreversibly drop a queue (relevant to wrong-terminal safety and no-loss). WR-04: on KindReconnected (manager.go L389-401) only existing workers are poked; subscribed panes with empty queues are never re-checked, so a pane closed during an outage keeps its subscription and keeps being polled (LIFE-04 reconciliation, SC3 'reconciles pane state'). WR-05: pane_moved cancels unconditionally even if the id did not change."
    artifacts:
      - path: "internal/deliver/worker.go"
        issue: "identify() at L160-172 precedes the LaunchPending/unknown gate"
      - path: "internal/deliver/manager.go"
        issue: "KindReconnected handler pokes only running workers (L396-400); pane_moved handler L374-379 cancels without checking id change"
    missing:
      - "Move readiness/launch-pending/unknown gate ahead of identify (or hold instead of cancel when unsettled)"
      - "On KindReconnected run startCheck for every subscribed pane on that socket without a worker"
      - "pane_moved: cancel only if previous_pane_id != pane_id, otherwise poke"
  - truth: "Scratch-only safety guard for prompt-typing tooling (T-03-30) holds"
    status: failed
    reason: "CR-02: dev deliver validates only --socket, then runs a Manager over every subscription in the real state dir, so it can type into panes on any socket (including real herdr sessions) and leaves its subscriptions in the real state file. Not a roadmap SC, but violates the phase 'never type into the wrong terminal' intent for the harness. Also WR-03 (failed Cancel makes worker hot-loop with no backoff) and WR-06 (TestUnreachableReachability flake, unseeded jitter in Manager) are open."
    artifacts:
      - path: "cmd/pr-relay/dev.go"
        issue: "setupDev uses real paths.StateDir(); no per-sub guard; subs not removed on exit"
      - path: "internal/deliver/worker.go"
        issue: "cancel() swallows errors; loop continues with no sleep (L160-167, 248-258, 289-294)"
      - path: "internal/deliver/manager.go"
        issue: "startEndpoint does not pass Jitter; test flakes ~5%"
    missing:
      - "dev deliver: isolated state dir or refuse if any sub fails guardScratchSocket; unsubscribe own subs on exit"
      - "cancel returns error; markCannot + sleep on failure; noAgent returns false on failed cancel"
      - "Add Jitter to deliver.Options, inject 0 in newMgrHarness (or advance >=37s)"
---

# Phase 3: herdr Client & Delivery Verification Report

**Phase Goal:** Queued updates reach the right agent pane at the right moment, coalesced and current, across herdr sessions and herdr restarts, without ever interrupting a busy agent or typing into the wrong terminal.
**Status:** gaps_found
**Re-verification:** No (initial)

## Observable Truths (ROADMAP success criteria)

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | Pushed via agent.prompt only after idle/done; held while working/blocked; never to unknown/bare shell | VERIFIED (with caveat) | Live PASS (blocked ~5 min, nothing typed; delivered ~13s after idle). Unknown/no-agent gating present in worker. Caveat: interactive_ready not checked (WR-01) |
| 2 | Pending updates coalesced into one message rendered from current state; superseded CI failure not reported | VERIFIED | Live PASS (2 events, one 2-line prompt). internal/compose + supersede unit tests; review rates pure layers good |
| 3 | herdr restart/socket drop leaves queued updates intact; daemon reconnects and reconciles | FAILED (BLOCKER) | Reconnect works, but update acked and lost after restart (CR-01 confirmed at worker.go ~L337-350). Idle-pane reconciliation missing (WR-04) |
| 4 | One daemon delivers to two herdr sessions at once | VERIFIED (automated only) | Live skipped (optional); TestTwoSessions synctest covers it. No live evidence |
| 5 | Pane close / id reuse / tab or pane gone cancels sub and drops pending; no-deliverable-pane PR polled slowest | VERIFIED (with caveats) | Live PASS: cancelled reason=pane_closed within 1s. LIFE-08 via SetPaneReachability (plan 04). Caveats WR-02, WR-04, WR-05 |

**Score:** 4/5 SCs verified; SC3 failed.

## Requirements Coverage

All 10 IDs appear in plan frontmatter (03-01..03-08) and REQUIREMENTS.md; no orphans. REQUIREMENTS.md checkboxes for them are still unchecked.

| Req | Status | Evidence |
|-----|--------|----------|
| DLV-01 | SATISFIED (caveat WR-01) | agent.prompt push; live pass |
| DLV-02 | SATISFIED | blocked/working hold, live pass |
| DLV-03 | SATISFIED | unknown/no-agent gating; unit tests |
| DLV-04 | SATISFIED | coalescing live pass |
| DLV-07 | SATISFIED | render from current state / supersede |
| DLV-08 | BLOCKED | silent loss after herdr restart (CR-01); WR-01 contributes |
| DLV-09 | SATISFIED (synctest only) | TestTwoSessions; no live run |
| LIFE-02 | SATISFIED (caveats WR-02, WR-05) | live pane_closed cancel |
| LIFE-04 | PARTIAL | cancel-on-send works; reconnect reconciliation of idle panes missing (WR-04) |
| LIFE-08 | SATISFIED | slowest-rate override on unreachable, plan 04 tests |

## Anti-Patterns / Review Findings

| Finding | Severity | Impact |
|---------|----------|--------|
| CR-01 ack on CodeTimeout | BLOCKER | SC3/DLV-08 failed |
| WR-01 interactive_ready unchecked | BLOCKER (contributing to SC3) | prompt dropped into not-ready agent |
| CR-02 dev deliver bypasses scratch guard | BLOCKER for harness safety | can type into non-scratch panes |
| WR-02, WR-04, WR-05 | Warning, affect LIFE-02/04 | wrong-time cancel / missed cancel |
| WR-03 | Warning | hot loop on Cancel failure |
| WR-06 | Warning | flaky test (~5%) |
| IN-01..IN-06 | Info | see 03-REVIEW.md |

## Behavioral Spot-Checks

Not re-run by the verifier. Gate evidence supplied by the orchestrator: go build, go vet, go test -race ./... pass on HEAD 134e90c. Passing tests do not cover the failures above, since CR-01 is encoded by a passing test.

## Human Verification

After gap closure, re-run the live SC3 scenario (restart scratch herdr with queued update; confirm the text appears in the pane). Optionally run live SC4 with two sessions.

## Gaps Summary

One roadmap criterion (SC3, DLV-08) fails on live evidence and confirmed code inspection. The root cause is acking on a prompt wait timeout, compounded by never checking interactive_ready. Related correctness gaps hit LIFE-04 reconciliation (WR-04), identity-check ordering (WR-02), and harness safety (CR-02). Suggested closure plans:
1. Delivery no-loss: CR-01 + WR-01 (+ tests, doc, live test pane confirmation), then re-run live SC3.
2. Lifecycle correctness: WR-02, WR-03, WR-04, WR-05.
3. Harness and test hygiene: CR-02, WR-06, IN-04/IN-05 as desired.

---
_Verified: 2026-11-03_
_Verifier: Claude (gsd-verifier)_
