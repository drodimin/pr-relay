---
phase: 03-herdr-client-delivery
plan: 07
subsystem: deliver
tags: [delivery, manager, herdr, endpoints, lifecycle, reachability, synctest]
requires:
  - "03-04 (Engine PendingWake, Cancel, Reason*, SetPaneReachability)"
  - "03-05 (herdr.Endpoint, Event kinds, herdrtest events.subscribe)"
  - "03-06 (worker, workerConfig, link, fakeEngine)"
provides:
  - "internal/deliver: Options, New, Manager, (*Manager).Run"
affects: [Phase 4 daemon (starts Manager.Run next to Engine.Run), 03-08]
tech-stack:
  added: []
  patterns:
    - "All manager state is owned by the Run goroutine; endpoints, workers and checks report back over channels"
    - "Endpoint and worker maps keyed by filepath.Clean(socket); reachability fanned out to every stored socket spelling"
    - "Each goroutine's send back to Run selects on the run ctx, and one WaitGroup joins everything on shutdown"
key-files:
  created:
    - internal/deliver/manager.go
    - internal/deliver/manager_test.go
    - internal/deliver/engine_integration_test.go
  modified:
    - internal/deliver/fakeengine_test.go
decisions:
  - "Workers are keyed by (cleaned socket, pane). Their reachability reports go to every raw stored PaneRef of that pane, because the engine keys reach by the stored socket string."
  - "Panes on a socket with no worker get SetPaneReachability(DownSince) while the endpoint is down and zero while it is up. Reachability is cleared for panes that are no longer subscribed."
  - "Tab/workspace-closed checks run at most once per pane at a time. They wait until the endpoint has been up for RestoreGrace, then cancel only on pane_not_found while the endpoint is still up for at least that long."
  - "Events from a retired endpoint and check results for one are ignored, by comparing the epState pointer."
  - "Integration test: a stub Fetcher returns the current scripted Snapshot on every Batch, so extra scheduler polls derive nothing. PollNow drives the scripted polls. No test-only exports were added to poller."
metrics:
  duration: "~40 min"
  completed: 2026-11-03
  tasks: 2
  files: 4
requirements: [DLV-01, DLV-08, DLV-09, LIFE-02, LIFE-08]
---

# Phase 3 Plan 07: Delivery manager Summary

`deliver.Manager` ties the poller Engine, one herdr Endpoint per cleaned socket and one worker per pane with pending updates together. Workers start at startup, on `PendingWake`, on a 60 s resync and after a worker exits. herdr lifecycle events turn into cancellations, worker pokes and re-syncs. Panes on an unreachable herdr are reported to the engine as unable to receive (LIFE-08).

## What was built

- **manager.go**:
  - `Options` and `New`. A nil Engine is an error. The defaults are DialUnix, QuietPeriod `config.DefaultQuietPeriod`, RestoreGrace 10s and Resync 60s.
  - `Run` refuses a second concurrent call. Its select loop covers ctx, `PendingWake`, the resync ticker, tagged endpoint events, worker exits and check results. On ctx done it cancels everything, waits on a WaitGroup and returns nil.
  - `sync` groups subs by `filepath.Clean(socket)` and pane. It retires endpoints that no longer have subs, along with their workers. It creates endpoints, calling `SetPanes` before `Run`, and re-asserts `SetPanes`. It starts workers for panes with pending events or a drop marker, and maintains reachability for panes without a worker.
  - Event routing:
    - `KindPaneClosed` cancels with `ReasonPaneClosed`.
    - `KindPaneMoved` (by PreviousPaneID) cancels with `ReasonPaneGone`. There is no re-keying.
    - `KindTabClosed` and `KindWorkspaceClosed` start bounded pane.get checks.
    - Status, detected and exited events poke the pane's worker.
    - `KindReconnected` pokes every worker on that socket and re-syncs.
    - `KindDisconnected` re-syncs, which updates reachability.
  - "herdr reachable" and "herdr unreachable" are logged at Info. Cancellations are logged only by the engine (D-17).
- **manager_test.go** has 14 synctest tests using a dialer that routes by socket path across several herdrtest servers: TestTwoSessions, TestPendingWakeStartsWorker, TestStartupDelivers, TestCancelPaneClosed, TestPaneMovedCancels, TestTabClosedRechecks, TestStatusEventPokesWorker, TestHerdrRestart, TestUnreachableReachability, TestEndpointRetired, TestSocketKeyCleaned, TestRunStopsOnCancel, TestRunTwiceManager and TestNewNilEngine.
- **engine_integration_test.go**: `TestEngineIntegration` runs a real `poller.New` engine, a `memSaver` and a stub Fetcher, with both Engine.Run and Manager.Run going. It has three subtests:
  - coalesce: ci.failed and a comment arrive as one 2-line prompt after the quiet period, and pending is empty afterwards.
  - superseded-ci: the pane is working, a ci.failed on sha1 is queued, then the head moves to sha2. Once the pane is idle, the single prompt has no ci.failed and pending is empty.
  - pane-closed: the sub is removed and `RecentCancellations` holds `pane_closed`.
- **fakeengine_test.go**: added `removeSub`, which deletes a sub and pokes PendingWake.

## Tasks

| Task | Name | Commits |
| ---- | ---- | ------- |
| 1 | Manager: endpoints per socket, worker lifecycle, event routing, reachability | 77ed205 (test), 7c08580 (feat) |
| 2 | Integration test against a real poller.Engine | 082a6eb |

## Verification

- `go vet ./...` is clean, and so is `GOOS=linux go vet ./internal/deliver/`. gofmt is clean.
- `go test -race ./... -count=1` passes. `go test -race ./internal/deliver/ -count=10` passes.
- The acceptance greps pass:
  - The Run signature, `filepath.Clean`, `ReasonPaneClosed`, `ReasonPaneGone`, `NewEndpoint(` and `PendingWake()` are all present.
  - There are no `Repoint` matches in deliver or poller, and no `.Wake()` in manager.go.
  - `poller.New(` appears in the integration test.

## Deviations from Plan

1. **[Rule 1 - Test fix] TestCancelPaneClosed agent.get assertion.** The first version expected exactly 1 agent.get in total. The startup `KindReconnected` legitimately pokes the worker, which re-runs agent.get. The test now checks that no agent.get happens after the close.
2. **[Rule 2 - Correctness] Reachability fan-out to raw socket spellings.** The engine keys reachability by the stored `HerdrSocket` string, so the manager reports to every stored spelling of a cleaned socket. Without this, a sub stored as `/s/a//herdr.sock` would never be marked.
3. **Additions:** TestRunTwiceManager and TestNewNilEngine.

## TDD Gate Compliance

Task 1 has a RED commit, 77ed205 (`test(03-07)`), which failed to compile because Manager, New and Options were undefined. The GREEN commit 7c08580 (`feat(03-07)`) follows it. Task 2 is test-only (082a6eb) against the GREEN code. No refactor commit was needed.

## Threat Model

- T-03-26 is mitigated: every map is keyed by (cleaned socket, pane id), and events are tagged with their endpoint. TestTwoSessions and TestCancelPaneClosed show that a same-id pane on the other socket is untouched.
- T-03-27 is mitigated: only pane_closed and pane_moved cancel directly. The tab/workspace checks need pane_not_found after RestoreGrace of uptime (TestTabClosedRechecks).
- T-03-28 is mitigated: endpoints retire with their last sub (TestEndpointRetired), and ctx cancel joins every goroutine (TestRunStopsOnCancel, where synctest would flag leaks).
- T-03-29 is mitigated: Dial defaults to `herdr.DialUnix`.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: internal/deliver/manager.go, internal/deliver/manager_test.go, internal/deliver/engine_integration_test.go
- FOUND commits: 77ed205, 7c08580, 082a6eb
