---
phase: 03-herdr-client-delivery
reviewed: 2026-11-04T14:23:40Z
depth: standard
files_reviewed: 14
files_reviewed_list:
  - cmd/pr-relay/dev.go
  - cmd/pr-relay/devdeliver_test.go
  - internal/compose/compose.go
  - internal/deliver/doc.go
  - internal/deliver/fakeengine_test.go
  - internal/deliver/live_test.go
  - internal/deliver/manager.go
  - internal/deliver/manager_test.go
  - internal/deliver/worker.go
  - internal/deliver/worker_test.go
  - internal/herdr/herdrtest/server.go
  - internal/herdr/types.go
  - internal/scratchguard/scratchguard.go
  - internal/scratchguard/scratchguard_test.go
findings:
  critical: 0
  warning: 3
  info: 4
  total: 7
status: issues_found
---

# Phase 3: Code Review Report (re-review after gap closure 03-09..03-12)

**Reviewed:** 2026-11-04T14:23:40Z
**Depth:** standard
**Files Reviewed:** 14
**Status:** issues_found

## Narrative Findings (AI reviewer)

## Summary

This re-review covers the gap-closure diff `22c15f3..HEAD` for the 14 listed files. `go vet ./...` is clean. `go test -race` passes for `internal/deliver`, `internal/scratchguard`, `internal/compose`, `internal/herdr` and `internal/herdr/herdrtest`.

Both prior critical findings are fixed, and so is every targeted warning and info item (see "Prior findings status"). A wait-for-working timeout no longer acks, and `dev deliver` can no longer reach the real state or non-scratch sockets.

The new unconfirmed-prompt machinery adds two robustness gaps:
- **WR-07.** The documented exponential re-send backoff is cut short by any poke, and nothing caps the number of attempts.
- **WR-08.** Confirmation is weakest when the pre-prompt `completion_seq` is absent. That is exactly the freshly relaunched agent in SC3.

**WR-09.** `dev watch` still writes relative-socket subscriptions into the real state directory. That part of CR-02 remains for the Phase 4 daemon.

No new blockers were found. New IDs continue from the prior report (WR-07+, IN-07+) so they do not collide with the deferred IN-01..IN-03.

## Prior findings status

| ID | Status | Evidence |
|----|--------|----------|
| CR-01 | **Resolved** | `worker.go:458-476`: a `CodeTimeout` keeps the update queued, records `unconfirmed` and backs off. Acks happen only on `err == nil` (`worker.go:456`, `484-486`) or on a completion confirmation (`worker.go:263-287`). `TestPromptTimeoutKeepsQueued` now asserts that the update stays pending and is re-sent. `doc.go:24-31` is updated. `TestLiveHerdr` requires a `pane.read` read-back before it checks the ack (`live_test.go:126-145`). Residual robustness gaps are in WR-07 and WR-08. |
| CR-02 | **Resolved for `dev deliver`; one residual** | `dev.go:112-124` uses a temp state dir by default, or `--state-dir`. `dev.go:134-138` refuses before any dial if a stored sub fails `scratchguard.Check`. `dev.go:159-166` unsubscribes the run's own subs on exit. Tests cover all three: `TestDevDeliver`, `TestDevDeliverRefusesForeignSubs` and `TestDevDeliverTempStateDefault`. Residual: `dev watch` still writes `HerdrSocket: "dev"` subs into the real state dir (WR-09). |
| WR-01 | **Resolved in code; inert on herdr 0.9.5** | `types.go:49` makes `InteractiveReady` a tri-state `*bool`, and `worker.go:224-234` (`settled`) refuses an explicit `false`. Tests: `TestNeverPromptNotReady`, `TestNotReadyThenReadyDelivers`. The 03-12 live run found `interactive_ready: true` throughout the relaunch, so this gate never fires on 0.9.5. SC3 protection actually comes from the `status: unknown` gate plus CR-01. The comments now contradict the live evidence (IN-07). |
| WR-02 | **Resolved** | `settled(info)` runs before `identify` (`worker.go:181-186`) and again in the post-quiet re-check (`worker.go:245`). Test: `TestIdentityHeldWhileUnsettled`. |
| WR-03 | **Resolved** | `cancel` returns its error (`worker.go:392-400`), and `identify` reports `failed` (`worker.go:374-388`). The run loop sleeps `Recheck` on failure (`worker.go:187-193`). `noAgent` holds instead of exiting (`worker.go:341-353`). Tests: `TestCancelFailureNoHotLoop`, `TestPaneGoneCancelFailureHolds`. |
| WR-04 | **Resolved** | `manager.go:416-427` runs `startCheck` for every worker-less subscribed pane on `KindReconnected`. `paneGone` keeps the RestoreGrace wait (`manager.go:492-515`). Test: `TestReconnectRechecksIdlePanes`. Because of IN-03 (deferred), every resubscribe triggers this too. That is acknowledged in the code comment. |
| WR-05 | **Resolved** | `manager.go:383-388`: a `pane_moved` with an empty or unchanged previous id only pokes the worker. Tests: `TestPaneMovedSameIDPokes`, `TestPaneMovedEmptyPreviousNoCancel`. |
| WR-06 | **Resolved** | `Options.Jitter` (`manager.go:37-39`) is passed through to `EndpointOptions` (`manager.go:285-286`). `newMgrHarness` injects zero jitter (`manager_test.go:51-53`). 03-12 reports 200 of 200 passes. |
| IN-04 | **Resolved** | An invalid ref is skipped and kept queued (`worker.go:424-431`), with a `Recheck` sleep so it does not loop hot (`worker.go:436-441`). Test: `TestInvalidRefKeepsQueued`. Side effect noted in IN-08. |
| IN-05 | **Resolved** | There is now one shared rule in `internal/scratchguard`, used by `dev.go:83-88`, `dev.go:135` and `live_test.go:45`. It resolves parent-directory symlinks (`scratchguard.go:35-45`), and `client.go:80-84` `Lstat` refuses a final-component symlink. Residual notes are in IN-09. |
| IN-06 | **Resolved** | `compose.go:66-112`: `max` was renamed to `limit`. |
| IN-01, IN-02, IN-03 | **Deferred** (deferred-items.md, phase 4 hardening) | Not re-raised. IN-03 makes WR-07 worse (see there). |

## Warnings

### WR-07: The documented re-send backoff is cut short by any poke, and nothing caps the attempts

**File:** `internal/deliver/worker.go:473-476`, `worker.go:239-243` and `worker.go:289-297`, with `internal/deliver/manager.go:403-415` and `internal/deliver/doc.go:28-31`

**Issue:**
- After a timeout, the backoff is `w.sleep(ctx, w.unconfirmedBackoff())`, and that sleep returns on any `w.sig`. The loop then re-runs `agent.get`. `confirmUnconfirmed` is false (no completion), the agent is idle, so `quietThenDeliver` waits only `QuietPeriod` (10 s) and prompts again.
- These pokes reach the worker:
  - every `pane.agent_status_changed`, `pane_agent_detected` or `pane_exited` for the pane;
  - every `KindReconnected` on the socket. Because of IN-03 (deferred), that includes **every resubscribe caused by any subscribe or unsubscribe of any pane on the same herdr session** (`manager.go:410-414`).
- So the doc's promise, "re-sent with exponential backoff from the recheck interval, capped at 16 minutes" (`doc.go:28-31`), does not hold. On a busy session the same update can be re-typed about every `QuietPeriod`.
- There is also no attempt cap and no escalation. `unconfirmedRuns` only feeds the delay.
- The tool is agent-agnostic. herdr may never classify some agents as `working` within `PromptWait` (5 s), for example an agent that finishes faster than herdr's status sampling, or one with weak status detection. For such an agent every prompt times out, and the same update is re-typed forever. Each re-type costs the agent tokens, against the core value of the project.
- `TestPromptTimeoutBackoff` never pokes during the backoff, so it cannot catch this.

**Fix:**
- Make the backoff a deadline, not a sleep. A poke should re-gate (so confirmation and working-wait still react at once) but must not re-prompt before the deadline.
- Add an attempt cap. After N unconfirmed prompts, log at ERROR, report the pane unable to receive, and stop re-sending until a status change shows `working` or a completion.

```go
// worker field: retryAt time.Time
case herdr.Code(err) == herdr.CodeTimeout:
    ...
    w.retryAt = w.now().Add(w.unconfirmedBackoff())
    return

// in quietThenDeliver, before w.deliver:
if w.unconfirmed != nil && w.now().Before(w.retryAt) {
    w.sleep(ctx, w.retryAt.Sub(w.now())) // poke still re-gates/confirm
    return
}
```

Add a test that pokes the worker (status event, or `KindReconnected` through the manager) during the backoff and asserts no extra `agent.prompt`.

### WR-08: When the pre-prompt `completion_seq` is absent, any later completion confirms, which is weakest in the SC3 relaunch case

**File:** `internal/deliver/worker.go:263-270` and `worker.go:464-469`

**Issue:**
- `confirmUnconfirmed` acks as soon as `info.CompletionSeq != nil` whenever `u.completion == nil` (`worker.go:265-268`).
- A nil baseline is exactly what a freshly launched or relaunched agent reports. herdr documents `completion_seq` as absent until the agent's first turn, and 03-12 observed `completion_seq` absent on an idle 0.9.5 get. That is the SC3 situation: the agent can drop the typed text there.
- Any first turn of that agent then confirms and acks the dropped update. The turn could be user-driven or a startup or resume turn the agent runs by itself. That is a loss, not a duplicate.
- T-03-43 accepts a false confirm from a user turn, but its risk was judged for a compare against a known baseline. With no baseline the window is widest exactly where the drop happens.
- The live run never exercised this path, because the 03-12 prompt succeeded directly.

**Fix:** With no baseline, require stronger evidence before confirming. One option: also require that the agent was observed `working` after the unconfirmed prompt. Track `sawWorking` when the run loop sees `StatusWorking` while `w.unconfirmed != nil`, and confirm only if `sawWorking && completion advanced`. The other option is never to confirm from a nil baseline and always re-send, accepting the duplicate under D-05.

```go
if u.completion == nil && !u.sawWorking {
    return false // fresh agent: a first completion alone is not proof
}
```

Add a test: baseline nil, prompt timeout, the pane then reports `CompletionSeq=1` without ever being `working`. Expect a re-send and no ack.

### WR-09: `dev watch` still writes relative-socket subscriptions into the real state dir (CR-02 residual)

**File:** `cmd/pr-relay/dev.go:37-40`, `dev.go:384` and `dev.go:402`, with `internal/deliver/manager.go:203-205`

**Issue:**
- `dev deliver` is now isolated, but `runDevWatch` still calls `setupDev(..., "", ...)`, which opens `paths.StateDir()`. It subscribes with `HerdrSocket: "dev", PaneID: "dev-watch"` and never unsubscribes.
- `Manager.sync` skips only empty sockets and panes. Once the Phase 4 daemon runs a Manager over the real state, every leftover `dev watch` sub makes it start an endpoint dialing the relative path `./dev`, resolved against the daemon's cwd.
- That endpoint reconnects forever. It also keeps the PR polling and reports the `dev-watch` pane as unreachable in status.
- `TestDevDeliverRefusesForeignSubs` shows these subs persist (`seedForeignSub`).

**Fix:**
- Give `dev watch` the same temp or `--state-dir` default as `dev deliver`, or unsubscribe its subs on exit.
- As defence in depth, have `Manager.sync` skip, and log once, any sub whose `HerdrSocket` is not absolute:

```go
if s.HerdrSocket == "" || s.PaneID == "" || !filepath.IsAbs(s.HerdrSocket) {
    continue
}
```

## Info

### IN-07: The `interactive_ready` comments and fake-server fidelity contradict the 03-12 live evidence

**File:** `internal/herdr/types.go:46-48`, `internal/herdr/herdrtest/server.go:57-59` and `server.go:706-710`, `internal/deliver/worker.go:219-223`, `internal/deliver/doc.go:12-14`

**Issue:**
- Five comments say "herdr 0.9.5 omits interactive_ready for long-running agents".
- 03-12 recorded the opposite: an explicit `interactive_ready: true` when idle, after launch and throughout the relaunch, with `launch_pending` never present.
- The fake server never emits the real wire shape (`true`). It emits either no key or `false`.
- Future maintainers will believe the readiness gate protects SC3, but on 0.9.5 it never fires (see WR-01 status).

**Fix:**
- Rewrite the comments to: "0.9.5 sends explicit true; absent is treated as ready for older/other versions".
- Have `agentJSON` emit `"interactive_ready": true` for ready panes, so tests exercise the real shape.

### IN-08: Pending updates that can never be rendered pin the worker forever

**File:** `internal/deliver/worker.go:424-441`

**Issue:**
- The IN-04 fix correctly avoids the silent ack.
- But a sub whose key never parses, and whose PR has no polled view, stays pending forever. Its worker never exits and logs `ERROR pending update has no renderable PR` about every `QuietPeriod + Recheck` (about 70 s), indefinitely.
- Nothing escalates it, cancels it or quarantines it.

**Fix:**
- Log once per sub, not every cycle, for example by tracking a set of already-reported sub IDs in the worker.
- Consider surfacing it in status, or cancelling with a dedicated reason after a bound.

### IN-09: The scratch guard trusts every named herdr session, and it accepts a path whose parent does not exist yet

**File:** `internal/scratchguard/scratchguard.go:35-45` and `scratchguard.go:63-70`, with `internal/scratchguard/scratchguard_test.go:38` and `scratchguard_test.go:94`

**Issue:**
- `~/.config/herdr/sessions/<name>/herdr.sock` is accepted (and the tests confirm it), so any real named session a user works in (`HERDR_SESSION`) counts as "scratch". This is the inherited T-03-30 premise. The shared helper now enshrines it.
- When `EvalSymlinks` fails with not-exist, the lexical verdict stands. The endpoint then dials lazily and reconnects forever, so a parent created later (possibly through a symlink outside `/sessions/`) is never re-checked.

**Fix:**
- Require the socket to exist and be a socket at check time. Return `refuse` on `ErrNotExist`, because the dev tools dial at once anyway.
- Consider requiring a dedicated prefix, for example `/sessions/prspike` or an explicit allow-list env var, instead of any `/sessions/` path.

### IN-10: `TestLiveHerdr` logs the exact read-back needle before polling for it

**File:** `internal/deliver/live_test.go:126`

**Issue:** `t.Logf("waiting up to 2m for %s ...", ref)` prints `pr-relay-live/test#N` to the test output. If the test is run from inside the target scratch pane, for example by the agent itself, the pane shows the needle and the `pane.read` check passes without any delivery. The later ack check mostly guards against this, but the read-back alone is no longer proof.

**Fix:** Log a different token, or search for the full rendered line, for example `"[pr-relay] " + ref.String()`, which the log line does not contain.

---

_Reviewed: 2026-11-04T14:23:40Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
