---
phase: 03-herdr-client-delivery
plan: 08
subsystem: cli
tags: [dev-harness, herdr, delivery, live-test, scratch-guard]
requires:
  - "03-07 (deliver.New, Manager.Run)"
  - "03-01 (delivery.quiet_period config)"
provides:
  - "pr-relay dev deliver --socket PATH --pane ID <PR-URL>... (hidden)"
  - "guardScratchSocket (refuses non-scratch herdr sockets before dialing)"
  - "TestLiveHerdr (opt-in, PR_RELAY_HERDR_SCRATCH_SOCKET)"
affects: [Phase 4 sub/daemon (same identity capture and Engine+Manager pairing), gap-closure for SC3/DLV-08]
tech-stack:
  added: []
  patterns:
    - "setupDev is the shared dev bootstrap (URLs, config, logger, store, ghclient, poller, quarantine)"
    - "Engine.Run and Manager.Run run side by side under one signal ctx; the first error cancels both"
    - "Prompt-typing tools fail closed on any socket outside /sessions/"
key-files:
  created:
    - cmd/pr-relay/devdeliver_test.go
    - internal/deliver/live_test.go
    - .planning/phases/03-herdr-client-delivery/deferred-items.md
  modified:
    - cmd/pr-relay/dev.go
decisions:
  - "dev deliver captures agent kind, agent_session value and terminal id with one agent.get at start, as Phase 4 sub will"
  - "The guard checks the cleaned path: it must not equal ~/.config/herdr/herdr.sock and must contain /sessions/. The same rule runs in TestLiveHerdr, which fails rather than skips on a refused path"
  - "The live test is gated on PR_RELAY_HERDR_SCRATCH_SOCKET plus PR_RELAY_HERDR_SCRATCH_PANE, never on herdr's in-pane env marker"
metrics:
  duration: "~45 min (plus live verification by the orchestrator)"
  completed: 2026-11-03
  tasks: 3
  files: 4
requirements: [DLV-01, DLV-02, DLV-03, DLV-04, DLV-07, DLV-08, DLV-09, LIFE-02, LIFE-04, LIFE-08]
---

# Phase 3 Plan 08: dev deliver harness and live verification Summary

A hidden `pr-relay dev deliver` command runs the poller and the delivery manager in the foreground, typing into one pane of a scratch herdr session only. It has a subprocess test against a fake herdr on a real unix socket and an opt-in live test. The orchestrator verified it live against scratch session prspike and PR drodimin/pr-relay-fixtures#19: SC1, SC2 and SC5 pass, SC3 fails (an update can be lost silently right after a herdr restart), and SC4 was skipped.

## What was built

- **cmd/pr-relay/dev.go**
  - `dev deliver --socket PATH --pane ID [--config FILE] <PR-URL>...`. `--socket` and `--pane` are required.
  - `guardScratchSocket(path)` refuses the default `~/.config/herdr/herdr.sock`, any path without `/sessions/`, and an empty path. The error reads `dev deliver: refusing <path>: only scratch herdr sessions (a path containing /sessions/) are allowed` and is returned before anything dials.
  - `runDevDeliver`:
    - Calls `agent.get` once on the pane. `agent_not_found` becomes `pane <id> has no agent`.
    - Subscribes each PR with `HerdrSocket` (cleaned), `PaneID`, `AgentKind`, `AgentSession` and `TerminalID`, and prints `subscribed <ref> pane=<pane> agent=<kind>`.
    - Builds `deliver.New` with the configured quiet period, then runs `Engine.Run` and `Manager.Run` concurrently. The first error cancels both, and a signal exits 0.
  - The shared bootstrap moved into `setupDev`. `dev watch` behaves the same, and its tests and the crash test still pass.
- **cmd/pr-relay/devdeliver_test.go**
  - `TestGuardScratchSocket` covers the guard's accept and refuse cases.
  - `TestDevDeliverRefuses` runs the binary against the default socket, a path without `/sessions/`, and with `--socket` or `--pane` missing.
  - `TestDevDeliver` runs the real binary, the fake GitHub, and herdrtest listening at `<tmp>/sessions/t/h.sock` with a 200ms quiet period. It checks:
    - exactly one prompt reaches w1:p1, containing `[pr-relay]` and the PR ref
    - the subscribe line is printed
    - the state file has `agent_kind` "claude" and `agent_session` "s1"
    - SIGTERM exits 0
  - A `startArgs` helper sits beside the harness; crash_test.go is unchanged.
- **internal/deliver/live_test.go**
  - `TestLiveHerdr` skips without `PR_RELAY_HERDR_SCRATCH_SOCKET` and fails closed on a refused path.
  - Its steps are Ping (with a warning on a protocol mismatch), pane.list, and agent.get. It then delivers one synthetic comment from a fakeEngine through a real Manager with a 2s quiet period, waits up to 2 minutes for the ack, and asserts no cancellations.
  - The doc comment gives the invocation.

## Tasks

| Task | Name | Commit |
| ---- | ---- | ------ |
| 1 | `dev deliver` subcommand, scratch-socket guard, subprocess test | 695e636 |
| 2 | Opt-in TestLiveHerdr against a scratch session | a4df704 |
| 3 | Live verification (checkpoint, run by the orchestrator, approved with issues) | n/a |
| (extra) | Logged the pre-existing flaky test in deferred-items.md | 12f13c1 |

## Verification

- `go test ./cmd/pr-relay/... -count=1` passes.
- Without its env var, `go test ./internal/deliver -run TestLiveHerdr -v` reports SKIP. With `PR_RELAY_HERDR_SCRATCH_SOCKET=$HOME/.config/herdr/herdr.sock` it fails with the refusal message and never dials.
- `go build -o /tmp/pr-relay ./cmd/pr-relay` succeeds. In `go test -race ./... -count=1`, every package passes except `TestUnreachableReachability`, which flakes about 5% of the time. That test predates this plan (see Deferred Issues), and a rerun of `internal/deliver` under race passed.
- The acceptance greps all match: `func guardScratchSocket(path string) error`, `/sessions/`, `AgentKind:` and `deliver.New(` in dev.go, and `PR_RELAY_HERDR_SCRATCH_SOCKET` and `/sessions/` in live_test.go. `HERDR_ENV` does not appear in live_test.go.

## Live verification results (Task 3)

The orchestrator ran these against scratch session `prspike` and the throwaway PR drodimin/pr-relay-fixtures#19.

| Criterion | Result | Observed |
| --------- | ------ | -------- |
| SC1: held while busy or blocked, delivered after idle plus quiet period | PASS | The agent was blocked on an AskUserQuestion for about 5 minutes, and nothing was typed into the pane. The update was delivered about 13s after the question was answered and the agent went idle. |
| SC2: coalescing | PASS | Two queued events (conflict, commits) arrived as one 2-line prompt with links. The wording reads cleanly. |
| SC3: survives a herdr restart | **FAIL (gap)** | Reconnect worked: the endpoint logged dropped and unreachable, backed off, reconnected on attempt 9, and the pane came back with the same agent_session. About 15s later the worker sent the queued commits update. The log shows `prompt submitted, working not observed` and then `delivered`, and the update was acked. The text never appeared in the pane and the pending queue ended up empty, so the update was **lost silently**. |
| SC4: two sessions isolated | SKIPPED (optional) | Not run live. Covered by the TestTwoSessions synctest. |
| SC5: pane close cancels, and the guard refuses | PASS | `subscription cancelled ... reason=pane_closed` appeared within 1s, and the state has no subscriptions. `dev deliver` refused both `~/.config/herdr/herdr.sock` and `/tmp/x/herdr.sock` before dialing. |

### SC3 root cause and proposed gap closure

`internal/deliver/worker.go`, around line 337, treats `CodeTimeout` from agent.prompt's wait-for-working as "submitted" and acks the update. Right after a herdr restart, the relaunched Claude reported `idle` before it was accepting input, so herdr reported the prompt as accepted but the text was dropped. This violates DLV-08 (no loss). Proposed fix for a gap-closure plan:

- On a wait-for-working timeout, keep the update queued and retry. A duplicate is acceptable under D-05 (at-least-once). Alternatively, confirm that the text appears in the pane before acking.
- Consider a longer grace after a reconnect, or after an occupant relaunch, before the first delivery.

### Note for later (not a Phase 3 bug)

When a commit's author is not linked to a GitHub account, own-activity filtering falls back to the committer. As a result, commits the user pushes are filtered out as the user's own activity even when they were authored by someone else.

## Deviations from Plan

- **TDD gate (Task 1 was tdd="true"):** the implementation and the tests went in as one `feat` commit (695e636). There was no separate failing RED commit before GREEN. The tests were run and pass, but the RED/GREEN split in git history is missing.
- **[Rule 3 - Scope] The pre-existing flake was logged, not fixed:** `TestUnreachableReachability` failed during Task 3's automated check. It fails at about the same rate with this plan's files removed (13 of 200 runs), so it comes from 03-07. It is recorded in `deferred-items.md`.
- **Wording:** the live_test.go doc comment refers to "herdr's own pane environment marker" instead of naming the variable, so the acceptance grep for `HERDR_ENV` returns nothing.

## TDD Gate Compliance

WARNING: Task 1 has no RED (`test(...)`) commit before its GREEN (`feat(...)`) commit. Only 695e636 (feat) exists.

## Deferred Issues

- `TestUnreachableReachability` fails about 5% of runs (see deferred-items.md). Likely cause: the reconnect backoff after 10 minutes down can land beyond the 31s the test advances.
- The SC3 silent loss after a herdr restart needs a gap-closure plan (see above).

## Known Stubs

None.

## Self-Check: FAILED

- FOUND: cmd/pr-relay/dev.go, cmd/pr-relay/devdeliver_test.go, internal/deliver/live_test.go, deferred-items.md
- FOUND commits: 695e636, a4df704, 12f13c1
- FAILED: live success criterion SC3 (survives a herdr restart without loss). An update was acked but never shown in the pane (DLV-08). This needs gap closure.
- Not run live: SC4 (optional).
