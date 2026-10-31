---
phase: 01-toolchain-spikes-pure-core
plan: 03
subsystem: sanitize
tags: [security, sanitize, unicode, fuzzing, dlv-06]
requires:
  - "01-01: Go module and internal/testutil package"
provides:
  - "internal/sanitize: Field, Login, Path, NeutralizeTag, URL, ShortSHA; caps MaxLogin=40, MaxCheckName=80, MaxPath=100, MaxURLBytes=300"
  - "internal/testutil.HostileCorpus: 43 base entries plus an embedded 'before X after' variant of each (86 total), for reuse by render tests (01-05)"
  - "FuzzField native fuzz target seeded from the corpus"
affects: [01-05 render]
tech-stack:
  added: []
  patterns:
    - "Stdlib-only security boundary (strings, unicode, unicode/utf8, net/url, regexp)"
    - "Shared property checker (checkProps) used by both TestHostile and FuzzField"
    - "Non-ASCII test literals written as \\u/\\U escapes, never literal invisible characters"
key-files:
  created:
    - internal/sanitize/sanitize.go
    - internal/sanitize/url.go
    - internal/sanitize/sanitize_test.go
    - internal/sanitize/hostile_test.go
    - internal/sanitize/fuzz_test.go
    - internal/sanitize/url_test.go
    - internal/testutil/hostile.go
  modified: []
decisions:
  - "Whitespace is collapsed lazily: a space is emitted only before the next kept rune, so output never has leading/trailing spaces and the space counts against the cap"
  - "Combining-mark limit (2 per base) counts kept Mn/Me runes; a space or any non-mark resets the count; stripped runes between marks do not reset it"
  - "NeutralizeTag compares rune-by-rune with unicode.ToLower and swaps only the brackets, so it is length-preserving and keeps the original case: [PR-RELAY] -> (PR-RELAY)"
  - "Path truncation keeps the tail and left-trims a leading space after the ellipsis"
  - "URL additionally requires the literal prefix https://github.com/ (stricter than the plan): this rejects HTTPS:// (url.Parse lowercases the scheme) and the bare https://github.com"
  - "Field/Path clamp maxRunes to at least 1, so Field(x, 1) of a long input is the ellipsis alone"
metrics:
  duration: "~10 min"
  completed: 2026-10-31
  tasks: 2
  files: 7
---

# Phase 1 Plan 03: Sanitize Security Boundary Summary

`internal/sanitize` is the DLV-06 boundary. Every PR-derived string becomes a single printable line with a rune cap, an ellipsis inside the cap, at most 2 combining marks per base and a neutralized `[pr-relay]` tag. A strict `https://github.com/` URL allow-list never truncates, and ShortSHA accepts only hex. A shared hostile corpus of 86 entries and a seeded native fuzz target back it.

## Tasks

| Task | Name | Commits |
| ---- | ---- | ------- |
| 1 | Field, Login, Path, NeutralizeTag with hostile corpus and fuzz | 7d0c08f (RED), 41f361f (GREEN) |
| 2 | URL allow-list and ShortSHA | e52ad97 (RED), 59cd765 (GREEN) |

## Verification

- `go vet ./internal/... && go test -race -count=1 ./internal/...`: pass
- `go test ./internal/sanitize -run '^$' -fuzz=FuzzField -fuzztime=15s`: about 3.4M execs, no failure
- `go list -deps ./internal/sanitize | grep -c pr-relay` prints 1. Non-test code has no internal imports.
- `grep -c '^\s*"' internal/testutil/hostile.go` is 40 (at least 30 required). The file contains `\x1b]52;`, `\u202e`, `\U000E0041`, `\ufe0f` and `[PR-RELAY]`.
- TestURLAllowList has 27 rows, including `javascript:` and `github.com.evil.com`.
- golangci-lint is not installed locally, so CI runs it. gofmt is clean.

## Threat Mitigations (all `mitigate` dispositions applied)

- T-01-10/11/13: IsPrint, Variation_Selector and RuneError stripping plus whitespace collapse. TestHostile asserts "every rune IsPrint or space" and "no \n/\r".
- T-01-12: NeutralizeTag runs after stripping. The corpus covers `[pr-\u200brelay]` and `[pr-\x1brelay]`.
- T-01-14: rune cap. Collection stops at cap+1 for Field, and there are at most 2 marks per base.
- T-01-15: the URL allow-list returns raw or fallback byte-for-byte. TestURLHostileCorpus checks that every corpus entry yields the fallback.

## TDD Gate Compliance

Each task has a `test(...)` RED commit followed by a `feat(...)` GREEN commit. No refactor commits were needed.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] The Write tool turned `\uXXXX` escapes into literal characters**
- **Found during:** Task 1 RED
- **Issue:** Go source written with `\u` escapes ended up with literal BOM, bidi and zero-width characters. The compiler rejected them ("illegal byte order mark").
- **Fix:** After each Write, a scratch script turns every non-ASCII rune in the test and corpus files back into `\u`/`\U` escapes. sanitize.go keeps a literal `\u2026` in the ellipsis constant and doc comment, which is valid Go.
- **Files modified:** internal/testutil/hostile.go, internal/sanitize/sanitize_test.go, internal/sanitize/url_test.go
- **Commit:** 7d0c08f, e52ad97

**2. [Rule 2 - Security hardening] Stricter URL check**
- Added the literal-prefix requirement `https://github.com/`. Without it, `HTTPS://github.com/x` would pass because url.Parse normalizes the scheme. This is documented under decisions.

### Notes

- The worktree started at 0f80d1b, which is behind the expected base a4e38aa. Per the branch check, it was reset to a4e38aa before any work began.

## Known Stubs

None.

## Self-Check: PASSED

- All 7 created files exist. Commits 7d0c08f, 41f361f, e52ad97 and 59cd765 are present in `git log`.
