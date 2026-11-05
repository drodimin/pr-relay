---
phase: 04-daemon-control-protocol-cli
plan: 02
subsystem: herdr-delivery-hardening
tags: [herdr, deliver, compose, hardening, env]
requires: []
provides:
  - herdr.KindResubscribed
  - herdr.ResolveSocket / PaneFromEnv / InHerdr
  - herdr.EndpointOptions.OmittedRetry
  - deliver.Manager.Sockets() / deliver.SocketStatus
affects:
  - internal/compose
  - internal/herdr
  - internal/deliver
tech-stack:
  added: []
  patterns:
    - injected getenv for env-dependent resolution
    - mutex-guarded snapshot map for cross-goroutine reads of Run-owned state
key-files:
  created:
    - internal/herdr/env.go
    - internal/herdr/env_test.go
  modified:
    - internal/compose/compose.go
    - internal/compose/compose_test.go
    - internal/herdr/stream.go
    - internal/herdr/endpoint.go
    - internal/herdr/endpoint_test.go
    - internal/deliver/manager.go
    - internal/deliver/manager_test.go
decisions:
  - "IN-01 partner tracking is limited to R4-cancelled conflict events; R1-R3 drops keep their superseding event, so acking them alone loses nothing"
  - "Omitted-pane retry starts at OmittedRetry (10s) and doubles per timer retry up to 5 minutes, reset on a real reconnect or when nothing is omitted (T-04-06)"
  - "events_lost still emits KindReconnected (lifecycle events were lost); only pane-set and omitted-pane resubscribes emit KindResubscribed"
  - "Sockets() derives Up from a single UpSince read so a concurrent transition cannot pair Up=true with a zero Since"
metrics:
  duration: ~35m
  completed: 2026-11-04
  tasks: 3
  files: 9
---

# Phase 4 Plan 02: Phase 3 hardening (IN-01/02/03, WR-09) plus env and socket-health seams Summary

The flood-guard partial ack now stops below a split R4 conflict pair. Pane-set resubscribes emit `KindResubscribed`, keep `UpSince` and poke no worker. Watched panes missing from `pane.list` get resubscribed after `OmittedRetry` or as soon as `pane_agent_detected` arrives. `herdr.ResolveSocket`/`PaneFromEnv`/`InHerdr` give the CLI env detection with an injected getenv. `Manager.Sockets()` returns a race-free per-socket up/down snapshot for `status`.

## Tasks

| Task | Name | Commits |
|------|------|---------|
| 1 | IN-01 partial-ack cap in compose | 1455e54 (test), 00e1c17 (fix) |
| 2 | IN-02/IN-03 endpoint fixes and herdr env detection | 75aa189 (test), 0998fdf (feat) |
| 3 | Manager KindResubscribed, Sockets() snapshot, WR-09 guard | 3b81ec8 (test), cfea63a (feat) |

## What changed

- **compose (IN-01):** `Message` records per group the conflict Seqs that `Supersede` cancelled, by diffing input against output. `Supersede`'s signature is unchanged. In the partial branch, `partialAck` caps the ack at `lowest cancelled conflict - 1` when a cancelled partner is still held at or after the cut. It only does this when the capped ack still covers the group's first included event. Otherwise it falls back to `heldSeq-1`, so every delivery makes progress.
- **herdr endpoint (IN-03):** `Run` tracks `resubbing` after `endResub`. On the next successful connect it skips `markUp` and emits `KindResubscribed`. A failed connect clears the flag and goes through `markDown` as before. Real reconnects and events_lost still emit `KindReconnected`.
- **herdr endpoint (IN-02):** `subscriptions()` records omitted watched panes in a Run-owned `omitted` set. When that set is non-empty, `read`'s watcher arms one timer for `omitDelay`. That delay starts at `OmittedRetry` (default 10s), doubles up to 5m, and resets on a real reconnect. A `KindAgentDetected` for an omitted pane pokes the existing cap-1 `resub` channel. Nothing is written on the events connection.
- **herdr env (SUB-03):** `ResolveSocket` uses `HERDR_SOCKET_PATH`, then `HERDR_SESSION` (`~/.config/herdr/sessions/<name>/herdr.sock`), then `~/.config/herdr/herdr.sock`. It applies `filepath.Abs` and `filepath.Clean` but not `EvalSymlinks`. If HOME is needed and unset, it returns an error.
- **deliver manager:** A `case herdr.KindResubscribed` runs `sync` and then `checkIdlePanes`. It pokes no worker and does not touch `es.up`. `Sockets()` reads an `epMu`-guarded `epSnap` that `startEndpoint`/`retire` maintain and that is cleared on Run exit. `sync` skips non-absolute `HerdrSocket` (WR-09).

## Deviations from Plan

### Auto-fixed / adjusted

**1. [Rule 1 - Correctness] IN-01 partners restricted to R4 conflict events**
- **Found during:** Task 1
- **Issue:** The plan said "partner = any removed Seq in the same group". That would also cap on R1 (stale head) and R2/R3 drops. Those have no held partner whose meaning changes, so the cap would only cause needless duplicate lines.
- **Fix:** Only conflict events that R4 cancelled are treated as partners (`cancelledConflicts`). This still covers the reviewer's R4 split. It also covers the three-event case (raised/cleared/raised) where acking the first would lose the net "raised" line.
- **Commit:** 00e1c17

**2. [Rule 2 - DoS guard] Omitted-pane retry backs off**
- **Found during:** Task 2
- **Issue:** With a fixed retry of one timer per connect, a watched pane that never comes back would cause a resubscribe, and a `KindResubscribed` worker-less check, every 10s forever.
- **Fix:** The retry delay doubles per timer-triggered retry, capped at 5 minutes. It resets on a real reconnect or once nothing is omitted. `agent_detected` still retries at once.
- **Commit:** 0998fdf

**3. [Rule 1] Existing test updated**
- `TestSetPanesResubscribes` now expects `KindResubscribed` after a pane-set change. That is the intended IN-03 behaviour change.

## Verification

- `go test -race -count=1 ./...` passes.
- `go vet ./...` is clean.
- The new tests (`TestPartialAckSupersede`, `TestResubscribeKeepsUpSince`, `TestEndpointOmittedPane`, `TestResolveSocket`, `TestPaneFromEnv`, `TestResubscribedNoPoke`, `TestSocketsSnapshot`, `TestSyncSkipsRelativeSocket`) pass. The endpoint and manager tests were stress-run 30-50 times under `-race` together with `TestUnreachableReachability`.

## TDD Gate Compliance

Every task has a `test(...)` commit (RED) followed by a `fix`/`feat` commit (GREEN). The RED commits for Tasks 2 and 3 failed at compile time, because the new symbols did not exist yet. Task 1's RED failed at runtime (`ack 5, want < 5`). The no-progress sub-test passes against the old code, because it asserts the existing fallback behaviour.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: internal/herdr/env.go, internal/herdr/env_test.go
- FOUND commits: 1455e54, 00e1c17, 75aa189, 0998fdf, 3b81ec8, cfea63a
