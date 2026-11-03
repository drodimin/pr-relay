---
phase: 03-herdr-client-delivery
plan: 06
subsystem: deliver
tags: [delivery, worker, gate, identity, synctest]
requires:
  - "03-02 (herdr.Client, herdrtest)"
  - "03-03 (compose.Message, compose.View, compose.Group)"
  - "03-04 (Engine PendingWake, PRView, AckMany, Cancel, SetPaneReachability, Drain.DroppedSeq)"
provides:
  - "internal/deliver: Engine interface (var _ Engine = (*poller.Engine)(nil))"
  - "internal/deliver: checkIdentity D-13 rule (identMatch, identMismatch, identUnidentified)"
  - "internal/deliver: worker (newWorker, poke, run), link interface (satisfied by *herdr.Endpoint), workerConfig defaults"
affects: [03-07 (manager starts workers, routes events to poke), 03-08]
tech-stack:
  added: []
  patterns:
    - "Timer plus a coalescing cap-1 signal channel while idle; agent.wait only while working or blocked"
    - "Quiet window re-checked by a (free class, state_change_seq, completion_seq) snapshot"
    - "Fake engine with only a mutex and channels, so it is synctest-safe"
key-files:
  created:
    - internal/deliver/doc.go
    - internal/deliver/engine.go
    - internal/deliver/identity.go
    - internal/deliver/identity_test.go
    - internal/deliver/fakeengine_test.go
    - internal/deliver/worker.go
    - internal/deliver/worker_test.go
  modified: []
decisions:
  - "With a stored session and a current session that is not known yet, a different agent kind is already a mismatch. Only the same kind (or no stored kind) is held as unidentified."
  - "An empty agent_session.value counts as not identified, the same as null"
  - "A signal during the quiet window restarts it. The manager decides which events poke a worker."
  - "When the worker exits because nothing is pending, it reports the pane receivable"
  - "An unexpected agent.wait or agent.get error backs off for Recheck (60s) instead of re-looping at once"
  - "newWorker defaults report to eng.SetPaneReachability and now to time.Now. The manager can override both."
metrics:
  duration: "~25 min"
  completed: 2026-11-02
  tasks: 2
  files: 7
requirements: [DLV-01, DLV-02, DLV-03, DLV-04, DLV-07, LIFE-02, LIFE-04, LIFE-08]
---

# Phase 3 Plan 06: Per-pane delivery worker Summary

The new `internal/deliver` package holds a per-pane worker. It types one coalesced message, rendered from each PR's current state, and only into a free, correctly identified agent. It gates on herdr's idle or done status and a 10 s quiet period that runs on a timer. It acks only after the prompt has gone through, cancels subscriptions when the pane is gone or holds a different agent, and reports LIFE-08 reachability.

## What was built

- **engine.go**: the `Engine` interface (List, Pending, PRView, AckMany, Cancel, SetPaneReachability, PendingWake), with a compile-time check against `*poller.Engine`.
- **identity.go**: `checkIdentity(sub, info)`.
  - With a stored session: a matching session is match and a different one is mismatch. A session that is null or empty is unidentified, unless the stored kind differs from the current agent, which is mismatch.
  - With no stored session: the kind is compared.
  - A legacy sub with neither a session nor a kind matches anything.
  - terminal_id and focused are never read.
- **worker.go**:
  - `link` interface, which `*herdr.Endpoint` satisfies.
  - `workerConfig` defaults: QuietPeriod 10s, RestoreGrace 10s, WaitTimeout 5m, Recheck 60s, PromptWait 5s, MaxBytes `compose.DefaultMaxBytes`.
  - `newWorker`, `poke`, and `run`, which follows Pattern 3:
    - Unreachable: cannot receive since `link.DownSince()`.
    - agent_not_found: a follow-up `PaneGet` tells the cases apart. `pane_not_found` cancels every sub of the pane with `ReasonPaneGone` once the link has been up for RestoreGrace, and otherwise waits out the rest of the grace. A bare shell is held.
    - Identity: mismatch cancels with `ReasonIdentityMismatch`, and unidentified is held.
    - unknown or launch_pending: held.
    - working or blocked: `AgentWait(until [idle, done], 5m)`.
    - idle or done: a timer quiet window, then a fresh `agent.get` snapshot comparison and a second identity check, then `deliver`.
  - `deliver` builds one `compose.Group` per sub from `Pending` and `PRView`, then calls `compose.Message`. If every event was superseded it acks silently. Otherwise it sends one `agent.prompt` with `wait {until:[working], timeout_ms:5000}`. A nil result or `timeout` means it acks (timeout logs a Warn). Every other result keeps the updates queued.
  - Info logs carry only counts, pane and socket. The text is logged at Debug only.
- **Tests**:
  - `fakeengine_test.go` is an in-memory engine with ack, cancel and reach recorders, `failAck`, and a `beforeAck` hook.
  - `worker_test.go` has 24 synctest tests over `herdrtest.Dialer()` and a fake link. They cover every behaviour bullet in the plan, plus nothing-pending exit, drop-marker delivery and ack-failure keep.

## Tasks

| Task | Name | Commits |
| ---- | ---- | ------- |
| 1 | Engine interface, D-13 identity rule and fake engine | a5c9532 (test), 6844ae3 (feat) |
| 2 | Per-pane worker | 438250e (test), 5ea9d02 (feat) |

## Verification

- `go vet ./...` is clean. `go test -race ./internal/deliver/... -count=3` passes, and `go test ./... -count=1` passes.
- All the acceptance greps match. There are no `.Focused` or `.TerminalID` reads in worker.go or identity.go. `AgentWait(` appears only in `waitFree`, which is called from the working and blocked branches only.

## Deviations from Plan

1. **[Rule 2 - Correctness] Stored session plus null current session plus a different kind is treated as mismatch.** The plan's table lists this row as mismatch, but its prose describes the null-session case only as "hold". The code implements the table.
2. **[Rule 2 - Correctness] Hot-loop backoff on unexpected errors.** If `agent.wait`, `agent.get` or `pane.get` returns an error code the worker does not handle, it now sleeps for Recheck. Without that, an unrecognised code that answers immediately would spin the loop.
3. **Fake engine hook.** The plan described the fake recording only. The fake also has a `beforeAck` hook, so TestCoalesce can add events after the drain is taken and before the ack. That is the "events added during the prompt" window.

## TDD Gate Compliance

Each task has a RED `test(...)` commit that failed to compile, followed by a GREEN `feat(...)` commit. No refactor commit was needed.

## Threat Model

- T-03-20 is mitigated. The worker prompts only on idle or done, and never on unknown, launch_pending or agent_not_found (TestNeverPrompt*).
- T-03-21 is mitigated. checkIdentity runs before the window and again after it, and a mismatch cancels the sub.
- T-03-22 is mitigated. The prompt text comes only from `compose.Message`.
- T-03-23 is mitigated. TestQuietNoHotLoop allows at most 4 calls per window and no agent.wait while idle. Holds are capped by Recheck.
- T-03-24 is mitigated. AckMany runs only after a nil or timeout prompt result.
- T-03-25 is mitigated. The text is logged at Debug only.

## Known Stubs

None. The manager (03-07) owns starting workers and routing herdr events into `poke`.

## Self-Check: PASSED

- FOUND: internal/deliver/{doc,engine,identity,identity_test,fakeengine_test,worker,worker_test}.go
- FOUND commits: a5c9532, 6844ae3, 438250e, 5ea9d02
