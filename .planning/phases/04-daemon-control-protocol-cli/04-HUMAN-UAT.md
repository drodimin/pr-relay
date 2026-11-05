---
status: partial
phase: 04-daemon-control-protocol-cli
source: [04-VERIFICATION.md]
started: 2026-11-05T00:00:00Z
updated: 2026-11-05T00:00:00Z
---

## Current Test

[awaiting human testing]

## Tests

### 1. Real herdr subscribe and push
expected: In a real herdr pane, `pr-relay sub <real PR>` returns within a few seconds. A later PR update, such as a comment, is typed into that pane.
result: [pending]

### 2. One daemon under a concurrent cold start
expected: With no daemon running, twenty concurrent `pr-relay ls` calls leave exactly one `pr-relay daemon` process.
result: [pending]

### 3. CR-01 repro (optional, after fix)
expected: With a poll stalled for more than 10s, the result `sub` reports matches stored state. A reported failure means nothing was saved.
result: [pending]

## Summary

total: 3
passed: 0
issues: 0
pending: 3
skipped: 0
blocked: 0

## Gaps
