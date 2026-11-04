# Phase 03 Deferred Items

## TestUnreachableReachability is flaky (found during 03-08, pre-existing from 03-07)

**Status:** Resolved by 03-11 (WR-06: injected Jitter). Re-checked in 03-12: `go test -race ./internal/deliver/ -run TestUnreachableReachability -count=200` passes 200 of 200.

- **Test:** `internal/deliver/manager_test.go` `TestUnreachableReachability`
- **Symptom:** `reachability after restart = 1999-12-31 19:00:00 ..., want cleared`. It fails about 5% of runs: 13 of 200 with `go test -race ./internal/deliver/ -run TestUnreachableReachability -count=200`, and the same rate at base 45c745f without any 03-08 files present.
- **Likely cause (unconfirmed):** after 10 minutes down, the endpoint's reconnect backoff (capped, possibly jittered) does not always land within the 31 s the test advances, or the reachability clear only runs on a later sync. So the pane is still reported unreachable when the test checks.
- **Scope:** Not caused by 03-08 (dev deliver, live test). Left unfixed per the executor scope boundary. A gap-closure fix should either make the advance cover the maximum backoff plus jitter, or confirm that reachability is cleared on `KindReconnected`.

## IN-01: A flood-guard partial ack can split a superseded conflict pair (from 03-REVIEW.md)

- **File:** `internal/compose/compose.go:143-160` with `internal/compose/supersede.go:46-53`
- **Symptom:** Supersede applies R4 (a conflict raised and then cleared cancels out) across the whole queue. If the flood guard cuts a group between the two events, `acks = h.seq - 1` acks the dropped "raised" event while the "cleared" event stays pending. On the next delivery the lone "cleared" event survives R4 and is typed as a "conflict cleared" line with no matching raise. R2 and R3 have the same exposure. It is rare, because the guard almost never fires.
- **Proposed fix:** When cutting, cap the ack at the lowest Seq of any superseded event whose partner is held. The simpler option is to cut only at group boundaries when a group contains superseded pairs.
- **Scope:** Info severity, not a phase 3 success criterion. Candidate for phase 4 hardening.

## IN-02: A pane absent from `pane.list` at connect gets no status subscription for the life of that connection (from 03-REVIEW.md)

- **File:** `internal/herdr/endpoint.go:281-295` and `endpoint.go:129-146`
- **Symptom:** `subscriptions` drops watched panes that are missing from the list (the Pitfall 2 workaround). If the pane appears later, for example when herdr restores panes lazily after a restart (the reason RestoreGrace exists), `SetPanes` sees the same id set and never resubscribes. The worker loses its status pokes and falls back to the 60 s `Recheck` and to `agent.wait`. It stays correct but is slower, and status changes do not restart the quiet window.
- **Proposed fix:** Record which watched panes were omitted. If any were, schedule a resubscribe after RestoreGrace, or when `pane_agent_detected` arrives for one of them.
- **Scope:** Info severity, not a phase 3 success criterion. Candidate for phase 4 hardening.

## IN-03: A pane-set resubscribe looks like a reconnect and resets `UpSince` (from 03-REVIEW.md)

- **File:** `internal/herdr/endpoint.go:219-237`, with `internal/deliver/manager.go:389-401`
- **Symptom:** On `endResub` (any subscription added to or removed from the socket), Run calls `markUp(connectedAt)` and emits `KindReconnected`. That resets `UpSince` and restarts the RestoreGrace window used by `paneGone` and `noAgent`. The manager then pokes every worker on the socket, which restarts each worker's quiet window (`quietThenDeliver` returns on any signal). So subscribing on one pane can delay delivery to every other pane on that socket by up to one more quiet period.
- **Proposed fix:** Keep `upSince` across resubscribes, since the link never went down. Either emit a distinct kind for a resubscribe, or skip the worker pokes when the link never went down.
- **Scope:** Info severity, not a phase 3 success criterion. Candidate for phase 4 hardening.
