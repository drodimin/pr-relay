---
phase: 04-daemon-control-protocol-cli
plan: 01
subsystem: pure-core
tags: [model, render, config, lifecycle, url-parsing]
requires: []
provides:
  - model.ParsePRLink (lenient link parser in front of strict ParsePRURL)
  - model.EventCheckin / model.EventExpired / EventType.IsLifecycle / Event.IdleSecs
  - render D-12 check-in and expired lines (+ unexported human duration formatter)
  - config.LifecycleConfig ([lifecycle] checkin_after / expire_after), Duration "Nd" form
affects: [poller engine lifecycle, CLI sub/unsub/keep validation, daemon config loading]
tech-stack:
  added: []
  patterns:
    - "Lenient normaliser that always ends in the strict parser (single source of truth)"
    - "Synthetic event types kept out of AllEventTypes so --events cannot select or mask them"
key-files:
  created:
    - internal/render/testdata/lifecycle.golden
  modified:
    - internal/model/model.go
    - internal/model/model_test.go
    - internal/render/render.go
    - internal/render/render_test.go
    - internal/config/config.go
    - internal/config/config_test.go
decisions:
  - "A bare scheme-less 'owner/repo[/...]' whose first segment has no '.' or ':' is read as a github.com path, so 'o/r' gets the repository message instead of a host error. A first segment that has a dot is treated as a host, so 'gitlab.com/...' gets 'only github.com is supported'."
  - "ParsePRLink rejects userinfo explicitly. Rebuilding the canonical URL would otherwise drop it silently before the strict parser could see it (T-04-01)."
  - "Anything after an allowed /files|/commits|/checks tab is accepted, for example /commits/<sha>, matching RESEARCH Pattern 7."
  - "The Duration day form takes digits only with n >= 1 (rejects 'd', '0d', '-1d', '+1d', '1.5d', '1dd') and is capped so n*24h cannot overflow."
metrics:
  duration: "~5 min"
  completed: 2026-11-04
  tasks: 3
  files: 7
requirements: [SUB-09, DMN-08, LIFE-06, LIFE-07]
---

# Phase 4 Plan 01: Pure-core lifecycle types, link parser and config Summary

This plan adds a lenient `ParsePRLink` that normalises agent-supplied links and then delegates to the strict `ParsePRURL`, with distinct errors for other hosts, issues, repositories and shorthand. It also adds the synthetic `checkin`/`expired` event types with D-12 rendered lines, golden-tested, and a bounded `[lifecycle]` config table that accepts `Nd` durations.

## What was built

- **model**: `ParsePRLink(s)` accepts http or https, a missing scheme, `www.`, any host case, a trailing slash, the `/files|/commits|/checks` tabs, a query, a fragment and surrounding whitespace. It always finishes in `ParsePRURL(canonical)`. Its error messages are distinct:
  - other hosts and ports: `only github.com is supported (got HOST)`
  - issue links: `"..." is an issue, not a pull request`
  - repository links: `"..." is a repository; pr-relay watches single pull requests`
  - `owner/repo#N`: `"..." is not a link; pass the full pull request URL`
  - everything else: `invalid pull request link "...": ...`

  `EventCheckin`/`EventExpired` live in a separate const block, outside `AllEventTypes()`, which is unchanged at 7 entries. `IsLifecycle()` and `Event.IdleSecs` (`json:"idle_secs,omitempty"`) are new.
- **render**: `renderer.line` has `case model.EventCheckin` and `case model.EventExpired`, which build the exact D-12 lines from `r.prefix`, `human(IdleSecs)` and `r.pr`. If a line does not fit, it falls back to the head form through `fits()`. `human`: whole days from 48h ("7d", "2d"), otherwise whole hours ("24h", "36h"), otherwise minutes ("90m").
- **config**: `LifecycleConfig{CheckinAfter, ExpireAfter}` under `[lifecycle]`, defaulting to 24h and 7d. `Validate` enforces checkin_after in [1h, 30d] and expire_after > checkin_after and <= 90d, with messages in the existing `config: lifecycle.<key>: ...` style. `lifecycleFloor()` drops the floor to `fastPollFloor` when `PR_RELAY_TEST_FAST_POLL=1`. `Duration.UnmarshalText` now accepts `Nd`.

## Commits

| Task | Gate | Commit | Message |
|------|------|--------|---------|
| 1 | RED | 214d57f | test(04-01): add failing tests for ParsePRLink and lifecycle event types |
| 1 | GREEN | 76251f2 | feat(04-01): add lenient ParsePRLink and synthetic lifecycle event types |
| 2 | RED | 86de767 | test(04-01): add lifecycle line golden and fit tests |
| 2 | GREEN | b67a8a7 | feat(04-01): render D-12 check-in and expired lines |
| 3 | RED | bb9b27c | test(04-01): add failing tests for [lifecycle] config and day durations |
| 3 | GREEN | d913ea9 | feat(04-01): add [lifecycle] config table with Nd durations |

## Verification

- `go test ./... -count=1`: all packages pass, and the existing goldens are unchanged.
- `go vet ./internal/...` and `go build ./...` are clean, and `gofmt -l` reports nothing.
- `internal/render/testdata/lifecycle.golden` matches the D-12 strings byte for byte (24h check-in and 7d expired lines checked by `grep -F` and by direct asserts in the test).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] TestLoadAllFields expected a zero Lifecycle**
- **Found during:** Task 3
- **Issue:** The existing all-fields load test compares against a full `Config` literal, so the new defaulted `Lifecycle` field made it fail.
- **Fix:** Added a `[lifecycle]` table (12h/3d) to the test's TOML and expected struct, so the test still covers every field.
- **Files modified:** internal/config/config_test.go
- **Commit:** d913ea9

**2. [Rule 2 - Security] Explicit userinfo rejection in ParsePRLink**
- **Found during:** Task 1
- **Issue:** Rebuilding the canonical URL drops userinfo, so the strict parser would never see `https://user@github.com/...` (T-04-01).
- **Fix:** ParsePRLink rejects `pu.User != nil` before rebuilding, and a table test covers it.
- **Commit:** 76251f2

## TDD Gate Compliance

Each task has a `test(...)` commit followed by a `feat(...)` commit. Task 1's tests were written right after the implementation was drafted and were committed first, so the RED commit does not compile against the base `model.go`. The RED for Task 3 was confirmed with a compile failure (`d.Lifecycle undefined`) before implementation. No refactor commits were needed.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: internal/model/model.go (`func ParsePRLink`, `IsLifecycle`, `IdleSecs int64`)
- FOUND: internal/render/testdata/lifecycle.golden
- FOUND: internal/config/config.go (`toml:"checkin_after"`, `toml:"expire_after"`, `func lifecycleFloor`)
- FOUND commits: 214d57f, 76251f2, 86de767, b67a8a7, bb9b27c, d913ea9
