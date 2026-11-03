---
phase: 03-herdr-client-delivery
plan: 05
subsystem: herdr-client
tags: [herdr, events, reconnect, backoff, synctest, fake-server]
requires:
  - "03-02: herdr Client, DialFunc, herdrtest Server with subscribeHandler/statusHook"
provides:
  - "internal/herdr: Subscription, Sub* constants, Client.Subscribe, Stream (Next, Close), EventKind, Kind* constants, Event"
  - "internal/herdr: Endpoint, EndpointOptions, NewEndpoint, Run, SetPanes, Events, Up, UpSince, DownSince, Socket"
  - "internal/herdr/herdrtest: events.subscribe support (EmitRaw, ClosePane, MovePane, DetectAgent, CloseTab, LoseEvents, SetProtocol, Subscribes, Subscribers, LastSubscription, CloseOnWrites)"
affects: [03-06, 03-07, 03-08]
tech-stack:
  added: []
  patterns:
    - "One read-only events connection per socket; a watcher goroutine closes it on ctx done or SetPanes poke, so nothing is ever written to it"
    - "Every connect snapshots the pane set, so SetPanes pokes coalesce safely in a cap-1 channel"
    - "Fake subscriber writes go through a per-subscriber queue and writer goroutine, so emitting under s.mu never blocks on a slow reader"
key-files:
  created:
    - internal/herdr/stream.go
    - internal/herdr/endpoint.go
    - internal/herdr/endpoint_test.go
    - internal/herdr/herdrtest/subscribe.go
  modified:
    - internal/herdr/herdrtest/server.go
decisions:
  - "KindDisconnected is emitted only on the up-to-down transition; repeated failed attempts while down emit nothing, and a never-connected endpoint emits none"
  - "events_lost and SetPanes resubscribes emit no Disconnected (only the following Reconnected)"
  - "SetPanes with an unchanged (sorted, deduped) set does not poke, so callers can call it freely without churning the stream"
  - "A malformed event line is logged and skipped (framing is intact); unknown event names are ignored; any herdr error line other than events_lost counts as a drop with backoff"
  - "Backoff is not reset by a successful connect, only by a connection that lasted StableAfter (anti-flap); a SetPanes poke wakes the backoff sleep early"
  - "pane_not_found retry: 1 attempt + 3 immediate re-list/resubscribe retries, then normal backoff"
  - "Fake DetectAgent changes the occupant and emits pane_agent_detected only (no status event); CloseTab emits tab_closed only (A7 unverified)"
metrics:
  duration: "~30 min"
  completed: 2026-11-02
---

# Phase 3 Plan 05: herdr events Endpoint Summary

`herdr.Endpoint` keeps one supervised, read-only `events.subscribe` connection for a herdr socket. Before every subscribe it takes a fresh `pane.list` and adds status subscriptions only for watched panes that list shows, so a vanished pane can't break coverage. A `pane_not_found` race is retried with a bounded count. On reconnect it backs off 250ms to 30s with +-20% jitter, and resets the backoff after a connection that stayed up 60s. It emits `Reconnected` after every subscribe and `Disconnected` when the connection drops. The fake herdr now serves `events.subscribe` with every verified quirk.

## What was built

- **stream.go**:
  - `Subscription` and the seven dotted `Sub*` types.
  - `Client.Subscribe`: id `sub-<n>`, a callTimeout deadline only on the handshake, the `subscription_started` check, and `*Error`/`*UnreachableError` failures.
  - `Stream.Next` reads with a bounded `readLine` (maxLine). It decodes both event spellings through explicit `case` pairs, with no normalising. `pane_moved` takes its ids from `previous_pane_id` and `pane.pane_id`.
  - `Stream.Close` is idempotent. `Stream` has no write method.
  - `EventKind`, the 9 `Kind*` constants and `Event`.
- **endpoint.go**:
  - `EndpointOptions` lets tests inject Now, Jitter and BackoffBase/Max and StableAfter.
  - `Run` refuses a second concurrent call. Its loop is ping (warns `herdr protocol mismatch` but carries on), then pane.list, subscribe and read.
  - An `endReason` (ctx / resub / lost / dropped) picks what happens next. Resub and lost reconnect immediately. Dropped marks the endpoint down, emits Disconnected and backs off.
  - `SetPanes` sorts, dedupes and pokes only on a change. `Up`/`UpSince`/`DownSince`: DownSince is the construction time before the first connect.
  - The Events channel holds 64 and blocks on ctx.
- **herdrtest/subscribe.go**:
  - Registered through `init()` into the 03-02 hooks.
  - Validation: unknown type, a status subscription without pane_id, or a missing pane gets an error, then the connection closes.
  - A subscriber is registered before the ack, and its writer goroutine starts after the ack, so the ack always comes first on the wire.
  - Close-on-write uses the `extra` channel and is counted in `CloseOnWrites`.
  - `LoseEvents` sends `events_lost` with each subscriber's subscribe id, then closes.
  - A dotted `pane.agent_status_changed` goes out on every status or occupant change, but only to subscribers that listed that pane.
- **herdrtest/server.go**: adds the subscriber state fields, has `Stop()` drop subscribers, and makes ping report `SetProtocol`'s value.

## Tasks

| Task | Name | Commit |
| ---- | ---- | ------ |
| 1 | Fake herdr events.subscribe and the Stream decoder | 7a0edf2 |
| 2 (RED) | Failing Endpoint tests | ef3c5de |
| 2 (GREEN) | Supervised read-only events Endpoint | a953907 |

## Verification

- `go vet ./internal/herdr/...` and `GOOS=linux go vet ./internal/herdr/...` both pass.
- `go test -race ./internal/herdr/... -count=10` passes. `go test ./... -count=1` passes.
- Every named test passes: TestEndpointSubscribesListedPanes, TestEndpointPaneNotFoundRace (retry and bounded), TestEndpointEvents, TestEndpointReconnect (real unix socket), TestEndpointBackoff (exact 250ms to 30s gaps, no reset after a 3s connection, reset after 61s), TestEndpointBackoffJitter, TestEventsLost, TestSetPanesResubscribes, TestRunTwice and TestEndpointProtocolMismatch.
- The acceptance greps pass: `subscription_started` and the explicit `"pane_closed", "pane.closed"` pair are present; there is no `strings.ReplaceAll` and no Stream Write/Send method; `events_lost` appears in subscribe.go; and `KindReconnected`, `KindDisconnected`, `CodeEventsLost`, and the `Run`/`SetPanes` signatures are in endpoint.go.

## Deviations from Plan

**1. [Rule 2 - Correctness] Additions the plan did not list:**
- The fake gained `SetProtocol`, needed for the protocol-mismatch test. It also gained `Subscribers()` and `CloseOnWrites()`, so tests can assert that the old stream was closed and that nothing was written.
- Added `TestEndpointBackoffJitter` and a check that unknown or malformed lines are skipped without dropping the stream.

**2. [Design detail] An unchanged pane set is a no-op.** `SetPanes` with the same set does not resubscribe. This avoids needless stream churn when the delivery manager re-asserts its pane set.

**3. [Design detail] A herdr error line other than `events_lost` is a drop.** If such a line arrives on the stream, the endpoint treats it as a drop with backoff rather than an immediate reconnect, so a misbehaving herdr cannot cause a tight loop (T-03-16).

## TDD Gate Compliance

The RED commit ef3c5de (`test(03-05)`; the tests did not compile because `herdr.EndpointOptions` was undefined) comes before the GREEN commit a953907 (`feat(03-05)`). No refactor commit was needed.

## Known Stubs

None.

## Notes for later plans

- The deliver manager should create one Endpoint per distinct socket, call `SetPanes` whenever its set of watched panes changes, and treat `KindReconnected` as "re-run agent.get / pane.get for every pane with pending events". Lifecycle events are not replayed.
- `KindPaneMoved` carries `PreviousPaneID` (the old id) and `PaneID` (the new id) for the repoint path.
- `KindTabClosed` and `KindWorkspaceClosed` carry no pane ids. On those events, re-check the subscribed panes on that socket (A7).

## Self-Check: PASSED
