---
status: passed
phase: 04-daemon-control-protocol-cli
source: [04-VERIFICATION.md]
started: 2026-11-05T00:00:00Z
updated: 2026-11-10T21:30:00Z
---

## Current Test

[testing complete: all 3 passed]

## Tests

### 1. Real herdr subscribe and push
expected: In a real herdr pane, `pr-relay sub <real PR>` returns within a few seconds. A later PR update, such as a comment, is typed into that pane.
result: passed (2026-11-10, covered by 05-HUMAN-UAT test 4). Build v0.1.0-uat, herdr 0.9.5. `pr-relay sub https://github.com/drodimin/pr-relay/pull/1 --pane w1:p9` returned in 0.6s. Later two lines were typed into the idle agent pane w1:p9: `[pr-relay] drodimin/pr-relay#1 ci.settled: passed (5 checks) ...` and `[pr-relay] drodimin/pr-relay#1 comment by dmitriy-medme ...`, the second after a comment from a second account.

### 2. One daemon under a concurrent cold start
expected: With no daemon running, twenty concurrent `pr-relay ls` calls leave exactly one `pr-relay daemon` process.
result: passed (automated, plan 05-11, 2026-11-07). Build v0.1.0-uat at ~/.local/bin/pr-relay. With the daemon stopped (`pgrep -f "pr-relay daemon" | wc -l` = 0), 20 concurrent `pr-relay ls --all` processes ran and were awaited: 20/20 exited 0 and printed `no subscriptions`. Afterwards `pgrep -f "pr-relay daemon" | wc -l` = 1 (pid 52362). Exactly one daemon.

### 3. CR-01 repro (optional, after fix)
expected: With a poll stalled for more than 10s, the result `sub` reports matches stored state. A reported failure means nothing was saved.
result: passed (covered by automated tests from plan 05-01). `go test -race -count=1 -v -run TestSubscribeVerifiedDeadline ./internal/poller` on 2026-11-07: `--- PASS: TestSubscribeVerifiedDeadline (0.31s)` and `--- PASS: TestSubscribeVerifiedDeadlineAfterAcquire (0.00s)`, `ok github.com/drodimin/pr-relay/internal/poller`.

## Summary

total: 3
passed: 3
issues: 0
pending: 0
skipped: 0
blocked: 0

## Gaps

None. All items passed.
