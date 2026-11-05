---
phase: 04-daemon-control-protocol-cli
plan: 07
subsystem: cli
tags: [cli, formatting, golden, events]
requires:
  - 04-01 (model.AllEventTypes, lifecycle types outside it)
  - 04-03 (ctl result structs)
provides:
  - parseEvents([]string) ([]string, error) and eventsLabel
  - formatSub, formatLs, formatStatus, formatPoll, formatKeep, humanDur, ago
affects: [04-11 commands]
tech-stack:
  added: []
  patterns:
    - pure formatters over ctl results with an explicit clock, golden-tested
    - text/tabwriter for ls alignment
key-files:
  created:
    - cmd/pr-relay/events.go
    - cmd/pr-relay/events_test.go
    - cmd/pr-relay/format.go
    - cmd/pr-relay/format_test.go
    - cmd/pr-relay/testdata/sub.golden
    - cmd/pr-relay/testdata/ls.golden
    - cmd/pr-relay/testdata/ls_all.golden
    - cmd/pr-relay/testdata/status.golden
  modified: []
decisions:
  - "Pane-mode ls is also aligned with tabwriter, so a single row has exactly two-space gaps (the approved preview) and multi-row output lines up"
  - "ls --all cells drop the 'in' prefix (CHECK-IN 21h, EXPIRES 6d); ending rows show '-' and 'ending: <reason>'"
  - "A past-due check-in renders as 'due' and a past expiry as 'now'; ago() of a future time is 'just now'"
  - "formatSub omits the D-10 warning; the 04-11 caller prints SubResult.Warning to stderr"
  - "status prints 'rate limit: unknown (no GitHub response yet)' when Limit is 0, adds ', intervals stretched xN' when Stretch > 1, and prints the secondary-limit deadline as RFC3339 plus a relative duration"
metrics:
  duration: ~12m
  completed: 2026-11-04
---

# Phase 4 Plan 07: --events parsing and CLI text formatters Summary

This plan adds the CLI's text layer as pure functions: `--events` validation with the `ci` and `all` aliases, plus golden-tested formatters for `sub` (D-07), `ls` and `ls --all` (D-21), `status` (D-23), `poll` and `keep`. None of them emits JSON (D-22).

## What was built

- **`parseEvents`** (`cmd/pr-relay/events.go`) splits each value on commas, trims it and lower-cases it. It expands `ci` to `ci.failed,ci.settled`, treats `all` on its own as the empty mask (nil), and returns an error if `all` is combined with other types. It accepts only `model.AllEventTypes()` names, so `checkin` and `expired` are rejected as unknown. The result is deduped and ordered the same way as `AllEventTypes`. Unknown-name errors list `valid: closed, conflict, commits, ci.failed, ci.settled, review, comment, ci, all`. An empty item gives `empty event type in --events`. The daemon's `normalizeMask` checks the mask again (T-04-27).
- **`eventsLabel`** returns `all` for an empty mask and a comma-joined list otherwise.
- **`format.go`**:
  - `formatSub` prints exactly the three D-07 lines. The co-subscriber clause is left out when there are none, the first word is `updated` when Created is false, and `(closed always included)` is added for masks that leave out closed.
  - `formatLs` prints pane-mode rows and the `--all` PANE table, including `last=never`, `ending: closed|expired` and `no subscriptions`.
  - `formatStatus` prints the daemon, socket, state, log, auth (source name only, per T-04-28), rate limit, an optional secondary limit, the counts, a per-socket herdr block, recent cancellations, and the optional quarantine and sorted per-PR errors.
  - `formatPoll` uses a singular noun for 1 event. `formatKeep` and `humanDur` (`58s`, `3m`, `21h`, `2h3m`, `6d`, `now`) round out the file.

## Commits

| Task | Commit | Message |
|------|--------|---------|
| 1 RED | 342502b | test(04-07): add failing test for --events parsing |
| 1 GREEN | 4a963ae | feat(04-07): implement --events parsing with ci/all aliases |
| 2 RED | 52b5932 | test(04-07): add failing golden tests for CLI text formatters |
| 2 GREEN | a67942e | feat(04-07): implement golden-tested CLI text formatters |

## Verification

- `go test ./cmd/pr-relay -run 'TestParseEvents|TestFormat' -count=1` passes, and so does the full `go test ./cmd/pr-relay -count=1`.
- `go vet` and `gofmt` are clean.
- The acceptance greps match: the sub.golden D-07 lines, PANE in ls_all.golden and `recent cancellations:` in status.golden. `format.go` does not import `encoding/json`, and `events.go` does not reference `EventCheckin` or `EventExpired`.

## Deviations from Plan

None to the plan's behavior. I made two small additions within the plan's discretion. First, `subscriptions: N on 1 PR` uses the singular, and `formatPoll` writes `1 new event`. Second, the status `secondary limit until` line shows an RFC3339 timestamp followed by `(in <dur>)`.

## Known Stubs

None. The formatters are wired to cobra commands in 04-11, as the plan intends.

## TDD Gate Compliance

Each task has a RED `test(...)` commit followed by a GREEN `feat(...)` commit. No refactor commit was needed.

## Self-Check: PASSED

- All 8 created files exist.
- Commits 342502b, 4a963ae, 52b5932 and a67942e are present in git log.
