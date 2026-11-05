---
phase: 04-daemon-control-protocol-cli
status: human_needed
verified: 2026-11-05
score: 5/5 roadmap truths, 15/15 requirements
verifier: gsd-verifier (report transcribed by orchestrator)
human_verification:
  - "Real herdr pane: `pr-relay sub <real PR>` returns within seconds and a later update is pushed into the pane"
  - "Real machine: twenty concurrent cold `pr-relay ls` calls leave exactly one daemon process"
  - "Optional, after a CR-01 fix: stall a poll for more than 10s, run `sub`, and confirm the CLI outcome matches stored state"
---

# Phase 4 Verification: Daemon, Control Protocol & CLI

**Goal:** An agent can run one non-blocking command to subscribe to a PR and later receive updates, with a single self-managing daemon and a complete subscription lifecycle.

**Result:** The goal is achieved and there are no blocking gaps. The status is `human_needed` because every automated check runs against fakes (a fake GitHub and a fake herdr). Nothing was tested against a real herdr pane or real GitHub.

**Automated checks (re-run by the verifier):** `go build ./...`, `go vet ./...` and `go test -race -count=1 ./...` all pass on main. The suite includes the 04-12 end-to-end gate, which runs the real binary.

## Roadmap truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | `sub` returns within seconds and supports `--events` and `--pane`. It fails clearly outside herdr, on an inaccessible PR, and on a non-PR or non-github.com link. | VERIFIED | 04-12 `e2e_test.go` covers a cold sub in under 10s and every failure path. The worst case is the CR-01 edge described below. |
| 2 | Twenty concurrent CLI calls start exactly one daemon, and a version mismatch is handled. | VERIFIED | `autostart_test.go`. The e2e gate makes 20 parallel `status` calls and gets one pid, and an old-version daemon is replaced once. |
| 3 | `ls`, `unsub`, `unsub --all` and `status` work, and logs rotate. | VERIFIED | e2e SC-3 scenarios. `daemon.log` is 0600 (04-04, 04-12). |
| 4 | Check-in after 24h, `keep` resets the clock, expiry after 7d, and the thresholds are configurable. | VERIFIED (with the CR-03 caveat) | `lifecycle_test.go` and `lifecycle_e2e_test.go`. The `[lifecycle]` config is from 04-01. |
| 5 | The subscription is removed after the merged or closed notification is delivered. | VERIFIED | `ending_test.go`. The e2e SC-5 scenario checks that "closed: merged" reaches a comment-only subscription, which is then removed. |

## Requirements

All 15 IDs below map to Phase 4 in REQUIREMENTS.md. Each is satisfied by the truths above, and none is orphaned.

SUB-01, SUB-02, SUB-03, SUB-05, SUB-06, SUB-07, SUB-09, DMN-01, DMN-05, DMN-06, DMN-07, DMN-08, LIFE-01, LIFE-06, LIFE-07

## Code review criticals (04-REVIEW.md): classified as WARNING, not gaps

The verifier re-read the code at each location. All three are real defects, but none leaves a must-have or requirement unmet in normal operation.

- **CR-01 (fix before release).** `internal/poller/subscribe.go:87-91` takes `pollMu` without a deadline, and `subscribeVerifiedCommit` never checks `ctx`. If a scheduled poll holds the lock for more than 10s, the CLI reports a timeout but the subscription can still be saved. This breaks the D-08 contract only in a degraded-network edge. The `poll` handler has the same unbounded wait. Fix: a context-aware lock, plus a `ctx.Err()` check before commit.
- **CR-02.** `internal/ctl/server.go:128-152` returns after the 2s drain while handlers may still be running, and `internal/daemon/daemon.go:321-337` then closes the store and releases the locks. A late `Store.Save` could overwrite a restarted daemon's `state.json`. In practice this needs a handler blocked as in CR-01. Fixing CR-01 largely closes it; also add a closed guard to `Store.Save`.
- **CR-03.** In `internal/poller/engine.go:439-466`, re-subscribing a live subscription keeps `CheckinAt` and the pending check-in event (D-11 deviation). `keep` itself works. Fix: clear `CheckinAt` and run `withoutCheckins` on every re-subscribe, and add a test.

## Human verification

1. In a real herdr pane, run `pr-relay sub <real PR>`. Confirm it returns within a few seconds and that a later update is pushed into the pane.
2. On a real machine with no daemon running, start twenty `pr-relay ls` calls at once. Confirm exactly one daemon process is running afterwards.
3. Optional, after a CR-01 fix: stall a poll for more than 10s, run `sub`, and confirm the CLI outcome matches stored state.

## Recommendation

The phase goal is achieved. Close CR-01, CR-02 and CR-03 with a short gap-closure plan or as pre-release hardening. They do not block Phase 5.
