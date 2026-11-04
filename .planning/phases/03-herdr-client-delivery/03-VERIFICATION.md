---
phase: 03-herdr-client-delivery
verified: 2026-11-04T16:00:00Z
status: passed
score: 5/5 roadmap success criteria verified
overrides_applied: 0
re_verification:
  previous_status: gaps_found
  previous_score: 4/5
  gaps_closed:
    - "SC3 / DLV-08 ack on prompt timeout (CR-01)"
    - "interactive_ready never checked (WR-01)"
    - "Identity cancel before settled gate (WR-02)"
    - "Cancel failure hot loop (WR-03)"
    - "Reconnect does not re-check idle panes (WR-04)"
    - "pane_moved cancels unconditionally (WR-05)"
    - "dev deliver scratch guard bypass (CR-02)"
    - "TestUnreachableReachability flake (WR-06)"
  gaps_remaining: []
  regressions: []
warnings:
  - "WR-07: poke-interruptible re-send backoff, no attempt cap (duplicates and token cost, not loss)"
  - "WR-08: nil pre-prompt completion_seq baseline can confirm on first completion (narrow theoretical loss path)"
  - "WR-09: dev watch writes subs into the real state dir (dev tooling only; Phase 4 daemon hazard)"
---

# Phase 3: herdr Client & Delivery Verification Report (re-verification)

**Phase Goal:** Queued updates reach the right agent pane at the right moment, coalesced and current, across herdr sessions and herdr restarts, without ever interrupting a busy agent or typing into the wrong terminal.
**Status:** passed (3 non-blocking warnings carried forward)
**Re-verification:** Yes, after gap closure plans 03-09..03-12

## Roadmap Success Criteria

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | Prompt only after idle/done; held while working/blocked; never to unknown status or bare shell | VERIFIED | worker.go run loop switches on status: working/blocked wait (`waitFree`), idle/done go to `quietThenDeliver`, default (unknown) marks cannot and sleeps. `settled()` (worker.go ~L224) additionally refuses launch_pending, unknown/empty status, and explicit `interactive_ready:false`, and it runs both pre-quiet and in the post-quiet re-check. Live PASS in 03-08 (blocked ~5 min, nothing typed). |
| 2 | Coalesced single message from current state | VERIFIED | `compose.Message` builds one text per delivery; empty result acks silently without typing. Live PASS (2 events, one prompt). Unit tests pass. |
| 3 | Herdr restart/socket drop leaves queue intact; daemon reconnects and reconciles | VERIFIED | Prompt `CodeTimeout` no longer acks: worker.go L458-476 stores `unconfirmed`, keeps updates queued, and backs off. Acks occur only on `err == nil` or a completion-confirmed turn. `KindReconnected` now re-checks worker-less subscribed panes (manager.go ~L416-427, test `TestReconnectRechecksIdlePanes`). Live SC3 re-run on herdr 0.9.5 (03-12): update queued during busy, herdr stopped and relaunched, `[pr-relay] ... ci.settled: passed (6 checks)` appeared in the pane, acked only afterwards, no loss or duplicate. I did not re-run the live scenario myself; I rely on the user-approved 03-12 record. |
| 4 | One daemon, two herdr sessions | VERIFIED (automated) | `TestTwoSessions` (synctest) passes; endpoint per socket in Manager. No live run; optional per plan. |
| 5 | Pane close, id reuse, tab/pane gone cancel sub and drop pending; no-deliverable-pane polled slowest | VERIFIED | Live pane_closed cancel within 1s. Identity check now follows the settled gate (WR-02), cancel failures hold with backoff (WR-03), `pane_moved` only cancels when id actually changed (WR-05). LIFE-08 via `SetPaneReachability`; flake fixed by injected Jitter (200/200 per 03-12). |

**Score:** 5/5

## Previously Reported Gaps

| Gap | Status | Evidence |
|-----|--------|----------|
| CR-01 ack on CodeTimeout | CLOSED | worker.go L450-476 as above; `TestPromptTimeoutKeepsQueued` replaces the lossy test; doc.go updated; `TestLiveHerdr` requires a pane.read read-back before asserting ack. |
| WR-01 interactive_ready | CLOSED in code | Tri-state `*bool` in types.go; `settled()` refuses explicit false. Inert on 0.9.5 (live shows explicit true), so protection there comes from status unknown plus CR-01. Stale comments (IN-07) are cosmetic. |
| WR-02 identity before gate | CLOSED | `settled` precedes `identify` in loop and post-quiet. |
| WR-03 hot loop | CLOSED | cancel returns error, loop sleeps Recheck on failure. |
| WR-04 reconnect reconcile | CLOSED | manager.go reconnect handler. |
| WR-05 pane_moved | CLOSED | guarded on changed id. |
| WR-06 flake | CLOSED | Options.Jitter injected. |
| CR-02 dev deliver guard | CLOSED for dev deliver | Temp state dir default, refuse foreign subs, unsubscribe on exit, shared `internal/scratchguard`. Residual is WR-09. |

## Judgment of the new review warnings

- **WR-07 (re-send backoff interruptible, no cap):** Does not defeat any SC or must_have. The failure mode is duplicate prompts (at-least-once, explicitly accepted by D-05) and wasted agent tokens for agents herdr never classifies as `working` within PromptWait. It is a robustness and cost issue (tension with the project's token-saving value) but never loses an update or types into a busy or wrong pane (the settled/idle gate still applies before every prompt). Warning, recommend fixing in Phase 4 (retry deadline plus attempt cap).
- **WR-08 (nil baseline confirms on first completion):** A real but narrow theoretical loss path against DLV-08: it requires a prompt timeout, text actually dropped, a nil pre-prompt completion_seq, and then a first completed turn that was not caused by our text (for example a user turn). Live SC3 did not hit it (prompt succeeded directly), and the roadmap criterion as observed passes. I do not treat it as defeating SC3, but it is the most important follow-up: require `sawWorking` or always re-send when baseline is nil. Warning.
- **WR-09 (dev watch real state dir):** Dev tooling residual, outside every roadmap SC. No daemon exists yet to be affected. Fix before Phase 4 (temp state dir for dev watch, and have Manager.sync skip non-absolute sockets). Warning.

## Requirements Coverage

All 10 IDs (DLV-01, 02, 03, 04, 07, 08, 09, LIFE-02, 04, 08) are claimed in plan frontmatter and present in REQUIREMENTS.md; no orphans.

| Req | Status | Evidence |
|-----|--------|----------|
| DLV-01 | SATISFIED | agent.prompt push, live |
| DLV-02 | SATISFIED | working/blocked hold, live |
| DLV-03 | SATISFIED | settled() + default branch |
| DLV-04 | SATISFIED | compose coalescing, live |
| DLV-07 | SATISFIED | render from current state + supersede |
| DLV-08 | SATISFIED (see WR-08 caveat) | no ack on timeout; live restart passed |
| DLV-09 | SATISFIED (automated) | TestTwoSessions |
| LIFE-02 | SATISFIED | live pane_closed; guarded pane_moved |
| LIFE-04 | SATISFIED | cancel at send, reconnect re-check |
| LIFE-08 | SATISFIED | reachability override, flake fixed |

Bookkeeping: REQUIREMENTS.md checkboxes and traceability still show DLV-02, 03, 04, 07, 09 and LIFE-08 as unchecked/Pending. Update them to Complete.

## Behavioral Spot-Checks

`go build ./...`, `go vet ./...`, `go test -race ./...` run by me on HEAD edd6a4e: all packages ok.

## Anti-Patterns

No TBD/FIXME/XXX introduced that I found in the reviewed paths; review reports 0 critical. Info items IN-07..IN-10 and deferred IN-01..IN-03 are non-blocking (deferred-items.md lists Phase 4 hardening).

## Human Verification

None outstanding. Live SC3 already approved by user in 03-12. Optional: live two-session run for SC4.

## Recommended follow-ups (non-blocking, before or in Phase 4)

1. WR-08: require observed `working` (or re-send) when no pre-prompt completion baseline.
2. WR-07: retry deadline rather than poke-interruptible sleep, plus attempt cap with ERROR log.
3. WR-09: isolate `dev watch` state; skip non-absolute sockets in Manager.sync.
4. Tick REQUIREMENTS.md boxes.

---
_Verified: 2026-11-04_
_Verifier: Claude (gsd-verifier)_
