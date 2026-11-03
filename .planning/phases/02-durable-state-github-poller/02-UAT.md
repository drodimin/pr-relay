---
status: complete
phase: 02-durable-state-github-poller
source: [02-01-SUMMARY.md, 02-02-SUMMARY.md, 02-03-SUMMARY.md, 02-04-SUMMARY.md, 02-05-SUMMARY.md, 02-06-SUMMARY.md, 02-07-SUMMARY.md, 02-08-SUMMARY.md, 02-09-SUMMARY.md, 02-10-SUMMARY.md, 02-11-SUMMARY.md, 02-12-SUMMARY.md, 02-REVIEW-FIX.md]
started: 2026-11-02T21:30:00Z
updated: 2026-11-03T02:13:00Z
---

## Current Test
<!-- OVERWRITE each test - shows where we are -->

[testing complete]

## Tests

### 1. Cold Start Smoke Test (gh auth, silent baseline)
expected: With a fresh state dir and no GH_TOKEN/GITHUB_TOKEN, `pr-relay dev watch .../pull/1` authenticates through gh, prints a `poll prs=1 cost=1 remaining=...` line and `#1 new=0 interval=1m0s`, prints no `[pr-relay]` event lines, and exits cleanly on Ctrl-C.
result: pass

### 2. Already-merged / already-closed PR notifies once (WR-04)
expected: `rm -rf $S` then `PR_RELAY_STATE_DIR=$S /tmp/pr-relay dev watch --sink $S/sink.jsonl .../pull/17 .../pull/8`. The first poll prints exactly one `[pr-relay] ...#17 closed: merged ...` line and one `[pr-relay] ...#8 closed: closed ...` line. On later polls (wait ~1-2 min) #17 is no longer polled, while #8 keeps polling, and neither event repeats.
result: pass
note: "Run by Claude at user's request. #17 'closed: merged by drodimin' and #8 'closed: closed by drodimin' each printed once on the first poll; #17 then dropped out of polling (prs=1, interval=0s), #8 kept polling at 1m. sink.jsonl = 2 lines. Test setup needed `mkdir -p $S` first: dev watch opens --sink before creating the state dir."

### 3. Restart does not replay delivered events
expected: Ctrl-C the test-2 run, then rerun the exact same command with the same $S. Polls resume, and the two closed events from test 2 are NOT printed again. `$S/sink.jsonl` still has exactly 2 lines (`wc -l $S/sink.jsonl`).
result: pass
note: "Restart with the same state dir: no closed events re-printed, sink.jsonl still 2 lines."

### 4. min_interval floor rejects a typo config (WR-07)
expected: `printf '[poll]\nmin_interval = "1s"\n' > $S/bad.toml` then `PR_RELAY_STATE_DIR=$S /tmp/pr-relay dev watch --config $S/bad.toml .../pull/1`. It refuses to start with a config error naming min_interval and the 10s minimum, and makes no polls.
result: pass
note: "Exit 1 with 'pr-relay: config: min_interval: must be at least 10s, got 1s'; no polls."

### 5. Own activity is filtered
expected: `rm -rf $S` and run `dev watch .../pull/1 .../pull/10`. After the first poll, post a comment on PR #10 from your own gh account (`gh pr comment 10 -R drodimin/pr-relay-fixtures -b "uat own"`). Wait for the next poll(s): no `[pr-relay]` comment line appears for #10. (Delete the comment afterwards.)
result: pass
note: "Comment 'uat own' posted by drodimin on #10 (user approved) at 02:04:45Z; next three polls #10 new=0, no event line. Comment deleted afterwards."

### 6. Another account's comment is delivered exactly once on a multi-PR watch (CR-01)
expected: Keep the test-5 watch running (#1 and #10). Post a comment on #10 from a different GitHub account. Within about one poll interval one `[pr-relay] ...#10 comment ...` line appears with the author and URL (no comment body), and it does not repeat on later polls or after a Ctrl-C and restart with the same $S.
result: skipped
reason: "No second GitHub account available (user). CR-01 is covered by cmd/pr-relay/sink_test.go."

### 7. One poll per interval with a duplicate subscription
expected: `rm -rf $S` then `dev watch .../pull/1 .../pull/1`. Each poll line reports `prs=1` (one GraphQL request, cost=1) rather than two, and #1 appears once per poll.
result: pass
note: "Same URL twice: every poll prs=1 cost=1, #1 listed once."

## Summary

total: 7
passed: 6
issues: 0
pending: 0
skipped: 1
blocked: 0

## Gaps

[none yet]
