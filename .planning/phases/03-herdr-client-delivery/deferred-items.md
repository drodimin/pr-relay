# Phase 03 Deferred Items

## TestUnreachableReachability is flaky (found during 03-08, pre-existing from 03-07)

- **Test:** `internal/deliver/manager_test.go` `TestUnreachableReachability`
- **Symptom:** `reachability after restart = 1999-12-31 19:00:00 ..., want cleared`. It fails about 5% of runs: 13 of 200 with `go test -race ./internal/deliver/ -run TestUnreachableReachability -count=200`, and the same rate at base 45c745f without any 03-08 files present.
- **Likely cause (unconfirmed):** after 10 minutes down, the endpoint's reconnect backoff (capped, possibly jittered) does not always land within the 31 s the test advances, or the reachability clear only runs on a later sync. So the pane is still reported unreachable when the test checks.
- **Scope:** Not caused by 03-08 (dev deliver, live test). Left unfixed per the executor scope boundary. A gap-closure fix should either make the advance cover the maximum backoff plus jitter, or confirm that reachability is cleared on `KindReconnected`.
