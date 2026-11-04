---
phase: 03-herdr-client-delivery
reviewed: 2026-11-03T12:00:00Z
depth: standard
files_reviewed: 44
files_reviewed_list:
  - cmd/pr-relay/dev.go
  - cmd/pr-relay/devdeliver_test.go
  - internal/compose/compose.go
  - internal/compose/compose_test.go
  - internal/compose/doc.go
  - internal/compose/supersede.go
  - internal/compose/supersede_test.go
  - internal/config/config.go
  - internal/config/config_test.go
  - internal/deliver/doc.go
  - internal/deliver/engine.go
  - internal/deliver/engine_integration_test.go
  - internal/deliver/fakeengine_test.go
  - internal/deliver/identity.go
  - internal/deliver/identity_test.go
  - internal/deliver/live_test.go
  - internal/deliver/manager.go
  - internal/deliver/manager_test.go
  - internal/deliver/worker.go
  - internal/deliver/worker_test.go
  - internal/herdr/client.go
  - internal/herdr/client_test.go
  - internal/herdr/doc.go
  - internal/herdr/endpoint.go
  - internal/herdr/endpoint_test.go
  - internal/herdr/errors.go
  - internal/herdr/herdrtest/server.go
  - internal/herdr/herdrtest/server_test.go
  - internal/herdr/herdrtest/subscribe.go
  - internal/herdr/stream.go
  - internal/herdr/types.go
  - internal/poller/cadence.go
  - internal/poller/cadence_test.go
  - internal/poller/delivery_test.go
  - internal/poller/engine.go
  - internal/poller/scheduler.go
  - internal/poller/scheduler_test.go
  - internal/poller/status.go
  - internal/render/render.go
  - internal/render/render_test.go
  - internal/store/state.go
  - internal/store/state_test.go
  - internal/store/store.go
  - internal/store/store_test.go
findings:
  critical: 2
  warning: 6
  info: 6
  total: 14
status: issues_found
---

# Phase 3: Code Review Report

**Reviewed:** 2026-11-03T12:00:00Z
**Depth:** standard
**Files Reviewed:** 44
**Status:** issues_found

## Narrative Findings (AI reviewer)

## Summary

This review covers the phase 3 diff (0c1cf62..HEAD): the herdr NDJSON client, endpoint and stream, the delivery manager and worker, compose/supersede, the poller hooks (reachability, PendingWake, Cancel, AckMany, PRView), the v1-to-v2 store migration, and the `dev deliver` harness.

The pure layers (compose, render, config, store migration) are in good shape. The defects are in the I/O layers:

- **Known SC3 loss (confirmed, CR-01).** The worker acks after a wait-for-working timeout. The unit test, the package doc and the live test all encode this behaviour, so none of them can catch it.
- **Contributing cause of SC3 (WR-01).** The worker never checks `interactive_ready`, although herdr reports it and the client already decodes it.
- **Scratch guard bypass (CR-02).** `dev deliver` checks only its own `--socket` flag. It then runs a Manager over the whole real state file, so it can type into panes on any socket.
- **Destructive identity check (WR-02).** The identity verdict, which can cancel a subscription and drop its queue, is applied before the launch-pending and unknown-status gate.
- **Hot loop (WR-03).** When a cancellation fails to persist, the worker spins with no backoff.
- **Missed cancellation (WR-04).** After a herdr outage, panes without a worker are never re-checked for closure, so a pane closed during the outage keeps its subscription.
- **Flake root cause confirmed (WR-06).** `TestUnreachableReachability` depends on the endpoint's random jitter, and Manager has no way to inject it.

## Critical Issues

### CR-01: A wait-for-working timeout is treated as delivered and acked, so updates are lost silently (known SC3 gap, confirmed)

**File:** `internal/deliver/worker.go:335-350`. Also encoded in `internal/deliver/doc.go:20-22`, `internal/deliver/worker_test.go:497-521` (`TestPromptTimeoutCountsDelivered`) and `internal/deliver/live_test.go:120-127`.

**Issue:** When `agent.prompt` returns `CodeTimeout`, the code logs `prompt submitted, working not observed` and falls through to `w.ack(res.Acks)`. The live run after a herdr restart showed that herdr can accept the prompt while the relaunched agent drops the text, so the update is acked but never typed. This violates DLV-08 and the user's "never lose an update" priority. D-05 accepts a duplicate but never a loss. This is the same defect recorded in 03-08-SUMMARY and is counted once here.

Related places that keep this bug from being caught (same root cause, not counted separately):
- `TestPromptTimeoutCountsDelivered` asserts the lossy behaviour: one ack, zero pending, no re-send.
- The package doc presents ack-on-timeout as part of the contract.
- `TestLiveHerdr` decides success from `eng.ackLog()` alone. Under this bug it passes even when nothing reaches the pane, exactly as in SC3.

**Fix:** On `CodeTimeout`, keep the update queued and let the loop re-gate. This accepts an at-least-once duplicate, as D-05 allows. Then:
- invert the unit test so it asserts pending stays and a second prompt is sent;
- update the doc comment;
- before the live test asserts on the ack, make it confirm the text appears in the pane (for example with `pane read`).

```go
case herdr.Code(err) == herdr.CodeTimeout:
	// Submission is unconfirmed (SC3: a just-relaunched agent can drop it).
	// Keep queued; a duplicate is acceptable under D-05, a loss is not.
	w.log.Warn("prompt not confirmed (working not observed), kept queued", "lines", res.Lines)
	return
```

### CR-02: `dev deliver` bypasses its scratch-socket guard and delivers to every subscription in the real state file

**File:** `cmd/pr-relay/dev.go:101-176` (with `setupDev`, `dev.go:265-306`, and `deliver/manager.go:195-277`)

**Issue:**
- `guardScratchSocket` validates only the `--socket` flag. `setupDev` then opens the user's real state directory (`paths.StateDir()`), and `deliver.New` gets an Engine over all of it. `Manager.sync` starts endpoints and workers for **every** subscription in the state file, whatever its socket. Any pending subscription on a non-scratch socket therefore gets typed into, which defeats T-03-30 ("prompt-typing tools fail closed on any socket outside /sessions/"). Such subscriptions include a real `~/.config/herdr/herdr.sock` subscription that the Phase 4 daemon will write to the same state dir, and earlier `dev watch` subscriptions.
- The store lock only blocks running at the same time as the daemon. Once the daemon is stopped, `dev deliver` reads its real subscriptions.
- The `dev deliver` subscriptions are never removed on exit, so they stay in the real state file and the future daemon will keep delivering to the scratch pane.
- `dev watch` subscriptions use the relative socket `"dev"` (`dev.go:38`). That makes `dev deliver` run a reconnecting endpoint against `./dev` relative to the current directory.

**Fix:** Have `dev deliver` use an isolated state directory, for example a required `--state-dir` or a `t.TempDir`-style temp dir. Also refuse to start, or filter the Engine view, if any subscription in the state fails `guardScratchSocket`. As defence in depth, give `deliver.Options` a socket filter:

```go
for _, s := range env.eng.List() {
	if err := guardScratchSocket(s.HerdrSocket); err != nil {
		return fmt.Errorf("dev deliver: state has a non-scratch subscription %s (%s); use a scratch state dir", s.ID, s.HerdrSocket)
	}
}
```

Unsubscribe the harness's own subscriptions on exit.

## Warnings

### WR-01: The worker ignores `interactive_ready` and gates only on `launch_pending` (contributes to SC3)

**File:** `internal/deliver/worker.go:168` and `internal/deliver/worker.go:201`. The field is decoded at `internal/herdr/types.go:46`.

**Issue:** herdr reports `interactive_ready` per agent, and `AgentInfo.InteractiveReady` decodes it, but nothing reads it. In the SC3 run, the relaunched Claude was `idle` and not launch-pending, yet it was not accepting input. The quiet period does not cover this, because a freshly relaunched agent sits idle for longer than 10 s before it is ready. The fake server always sets `interactive_ready = !launch_pending` (`herdrtest/server.go:700-701`), so no test covers the case where the agent is idle but not ready. This is a separate fix from CR-01. Gating on readiness avoids the drop, and CR-01 makes sure a drop is not acked.

**Fix:** Treat `!info.InteractiveReady` like `LaunchPending`: mark the pane unable to receive and re-check. Check it in both the run-loop gate and the post-quiet re-check. Let the fake server set the two flags independently, and add a test for idle with `interactive_ready=false`. First confirm on herdr 0.9.5 that `interactive_ready` is reliably true for long-running idle agents, so delivery is not held forever.

### WR-02: The destructive identity check runs before the launch-pending and unknown-status gate

**File:** `internal/deliver/worker.go:160-172`

**Issue:** `w.identify(subs, info)` can cancel subscriptions, which also drops their pending queues. It runs before the `info.LaunchPending || Status == unknown` gate. While an agent is relaunching (herdr restart, occupant restart), herdr explicitly reports the pane as not settled. Even so, `checkIdentity` returns `identMismatch` if `info.Agent` is transiently empty or different, or if `agent_session` holds a stale or placeholder value. The verdict is irreversible (D-15/D-17 drop the queue), and it is taken on the one snapshot that herdr itself flags as unreliable.

**Fix:** Move the gate on launch-pending, unknown status (and, per WR-01, interactive readiness) ahead of `identify`. Alternatively, have `identify` hold instead of cancel while `info.LaunchPending || info.Status == herdr.StatusUnknown`:

```go
if info.LaunchPending || info.Status == herdr.StatusUnknown || info.Status == "" {
	w.markCannot(w.now()); w.sleep(ctx, w.cfg.Recheck); continue
}
matching, held := w.identify(subs, info)
```

### WR-03: A failed Cancel makes the worker spin with no backoff

**File:** `internal/deliver/worker.go:160-167`, `worker.go:289-294` and `worker.go:248-258`

**Issue:**
- **Identity mismatch.** `identify` calls `w.cancel`, and `cancel` only logs when `eng.Cancel` fails, for example when the save fails with disk full or EIO. The sub is still in `List()`, `matching` is empty and `held` is false, so the loop hits `continue` with no sleep. It then calls `agent.get` again, cancels again, fails again, and so on. The result is a tight loop that hammers the herdr socket and floods the log with `cancel failed`.
- **Pane gone.** `noAgent` returns `true` after a failed cancel. The worker exits, `Manager.Run`'s `exited` handler calls `sync`, and sync restarts the worker at once because pending events still exist. That makes the same loop across worker restarts.

**Fix:** Have `cancel` return its error. If any cancellation fails, `markCannot` and `w.sleep(ctx, w.cfg.Recheck)` before re-looping. In `noAgent`, return `false` (and sleep) when a cancellation failed.

### WR-04: After a herdr outage, panes without a worker are never re-checked, so a pane closed during the outage keeps its subscription

**File:** `internal/deliver/manager.go:389-401`, with `internal/herdr/endpoint.go:281-295`

**Issue:** On `KindReconnected`, only running workers are poked ("lifecycle events are not replayed: every worker re-checks its pane"). Workers exist only for panes with pending events. If a subscribed pane with an empty queue closed while herdr was down, or while the events connection was lost (`events_lost`), its `pane.closed` event is never replayed. `connect()` also silently omits it from the status subscriptions because it is absent from `pane.list`. The subscription survives, and its PR keeps polling at the normal cadence for a pane that no longer exists, which violates LIFE-04. It is only cancelled if a later GitHub event happens to create pending work.

**Fix:** On `KindReconnected`, run `m.startCheck(ctx, es, ref)` for every subscribed pane on that socket that has no worker, just as for `KindTabClosed`/`KindWorkspaceClosed`. `paneGone` already applies the RestoreGrace.

### WR-05: `pane_moved` cancels unconditionally, even when the pane id did not change

**File:** `internal/deliver/manager.go:374-379`

**Issue:** The handler cancels every subscription on `ev.PreviousPaneID` without checking whether the move changed the id. The research note (03-RESEARCH.md:107) says only that a **cross-workspace** move assigns a new id. If herdr emits `pane_moved` for an intra-workspace or tab move where `previous_pane_id == pane.pane_id` (unverified either way), a live, correctly identified agent loses its subscriptions and its queued updates.

**Fix:** Cancel only when the id actually changed, and otherwise just poke the worker:

```go
case herdr.KindPaneMoved:
	if ev.PreviousPaneID == "" || ev.PreviousPaneID == ev.PaneID {
		if ws := m.workers[poller.PaneRef{HerdrSocket: es.socket, PaneID: ev.PaneID}]; ws != nil { ws.w.poke() }
		return
	}
	if m.cancelPane(...ReasonPaneGone) { m.sync(ctx) }
```

### WR-06: `TestUnreachableReachability` flakes because Manager has no way to inject the endpoint's random jitter (root cause confirmed)

**File:** `internal/deliver/manager_test.go:334-352`, `internal/deliver/manager.go:279-286` and `internal/herdr/endpoint.go:99-101`

**Issue:** This confirms the hypothesis in deferred-items.md. `startEndpoint` builds `herdr.EndpointOptions` without `Jitter`, so the endpoint uses `rand.Float64()` even inside the synctest bubble. After 10 minutes down, the backoff is capped at 30 s and jittered by ±20%, giving 24 s to 36 s. The test restarts the server and advances only 31 s. Whenever the in-flight backoff sleep has more than 31 s left, the endpoint has not reconnected yet, so sync still reports the pane unreachable. Nothing in the product code is wrong. The test is non-deterministic by construction.

**Fix:** Add `Jitter func() float64` (and optionally the backoff durations) to `deliver.Options`, pass it through to `EndpointOptions`, and have `newMgrHarness` inject `func() float64 { return 0 }`. Alternatively, advance at least `defaultBackoffMax*(1+jitterFrac)+1s` (37 s).

## Info

### IN-01: A flood-guard partial ack can split a superseded conflict pair

**File:** `internal/compose/compose.go:143-160` with `internal/compose/supersede.go:46-53`

**Issue:** Supersede decides R4 (conflict raised then cleared cancels out) over the whole queue. If the flood guard cuts a group between the two events, `acks = h.seq - 1` acks the dropped "raised" event, while the "cleared" event stays pending. On the next delivery the lone "cleared" event survives R4 and is typed as a "conflict cleared" line with no matching raise. R2/R3 have the same exposure. It is rare, because the guard almost never fires.

**Fix:** When cutting, cap the ack at the lowest Seq of any superseded event whose partner is held. The simpler option is to cut only at group boundaries when a group contains superseded pairs.

### IN-02: A pane absent from `pane.list` at connect never gets a status subscription for the life of that connection

**File:** `internal/herdr/endpoint.go:281-295` and `endpoint.go:129-146`

**Issue:** `subscriptions` drops watched panes that are missing from the list (the Pitfall 2 workaround). If the pane appears later, for example when herdr restores panes lazily after a restart (the reason RestoreGrace exists), `SetPanes` sees the same id set and never resubscribes. The worker loses its status pokes and falls back to the 60 s `Recheck` and to `agent.wait`. It stays correct, but it is slower, and the quiet window is not restarted on status changes.

**Fix:** Record which watched panes were omitted. If there were any, schedule a resubscribe after RestoreGrace, or when `pane_agent_detected` arrives for one of them.

### IN-03: A pane-set resubscribe looks like a reconnect: it resets `UpSince` and pokes every worker

**File:** `internal/herdr/endpoint.go:219-237`, with `internal/deliver/manager.go:389-401`

**Issue:** On `endResub` (any subscription added or removed on the socket), Run calls `markUp(connectedAt)` and emits `KindReconnected`. This resets `UpSince`, which restarts the RestoreGrace window used by `paneGone` and `noAgent`. The manager then pokes every worker on the socket, which restarts each worker's quiet window (`quietThenDeliver` returns on any signal). Subscribing on one pane can therefore delay delivery to every other pane on the socket by up to one more quiet period.

**Fix:** Keep `upSince` across resubscribes, since there was no down transition. Either emit a distinct kind for resubscribe, or skip the worker pokes when the link never went down.

### IN-04: `refFromKey` can produce an invalid ref, and then the events are acked silently

**File:** `internal/deliver/worker.go:316-318` and `worker.go:445-453`

**Issue:** When `PRView` is missing, the ref is parsed from the key and the `Atoi` error is ignored. An invalid ref makes `render.Each` and `DroppedLine` return empty strings. `compose.Message` then yields `Text == ""`, and the worker acks the whole group with "Everything was superseded" (`worker.go:325-328`). A malformed key, or a sub whose PR record vanished, therefore discards updates silently instead of keeping them queued.

**Fix:** If `!g.Ref.Valid()`, log an error and skip the group, so nothing is acked for it.

### IN-05: The scratch guard is duplicated, and an intermediate symlink can bypass it

**File:** `cmd/pr-relay/dev.go:79-93` and `internal/deliver/live_test.go:39-42`

**Issue:** The same rule is written twice and can drift. `filepath.Clean` is lexical. A path such as `/tmp/sessions/herdr.sock`, where `/tmp/sessions` is a symlink to `~/.config/herdr`, passes the guard. `DialUnix`'s `Lstat` checks only the final component, so the dial succeeds against the real session.

**Fix:** Move the guard into a shared internal helper. Apply `filepath.EvalSymlinks` to the parent directory before the `/sessions/` and default-path checks.

### IN-06: The local variable `max` shadows the builtin

**File:** `internal/compose/compose.go:66`

**Issue:** `max := lim.MaxBytes` shadows Go's builtin `max` for the rest of the function. A future edit that reaches for `max(a, b)` will fail to compile or confuse readers.

**Fix:** Rename it to `limit`.

---

_Reviewed: 2026-11-03T12:00:00Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
