---
phase: 02-durable-state-github-poller
plan: 04
subsystem: sanitize, render
tags: [security, dlv-06, unicode, confusables]
requires: ["02-01 (golang.org/x/text v0.43.0 pinned)"]
provides:
  - "sanitize.Fold (per-rune NFKC + lowercase + tag skeleton)"
  - "Folded NeutralizeTag"
  - "IN-01 filler stripping in sanitize.clean"
  - "Folded render tag guard (render.fits)"
  - "Lookalike and filler entries in testutil.HostileCorpus"
affects: [internal/sanitize, internal/render, internal/testutil]
tech-stack:
  added: ["golang.org/x/text/unicode/norm (first direct import)"]
  patterns: ["Match on a rune-count-preserving folded form; never render the folded form"]
key-files:
  created:
    - internal/sanitize/fold.go
    - internal/sanitize/fold_test.go
  modified:
    - internal/sanitize/sanitize.go
    - internal/sanitize/hostile_test.go
    - internal/testutil/hostile.go
    - internal/render/render.go
    - internal/render/hostile_test.go
decisions:
  - "Fold is per rune and keeps the rune count, so a folded match index maps directly onto the original runes. NFKC is applied only when it yields exactly one rune."
  - "ASCII runes skip NFKC (fast path). NeutralizeTag's early exit checks whether any rune folds to '['."
  - "NeutralizeTag keeps the rune count but no longer keeps the byte count, because a fullwidth bracket becomes an ASCII paren. All caps are in runes, so nothing depends on byte length."
metrics:
  duration: "~15 min"
  completed: 2026-11-01
  tasks: 2
  files: 7
---

# Phase 2 Plan 04: Confusable Fold and Filler Strip Summary

This plan closes CR-02 and IN-01 before Phase 3 starts joining lines into prompts. `sanitize.Fold` applies NFKC to each rune, lowercases it, and maps Cyrillic and Greek lookalikes of the tag letters (plus Unicode brackets and hyphens) to ASCII. `NeutralizeTag` and the render tag guard now match on the folded form, and `clean` strips the five printable-but-invisible fillers.

## Tasks

| Task | Name | Commits |
|------|------|---------|
| 1 | sanitize.Fold, filler strip, folded NeutralizeTag | f7e0cac (RED), baaae75 (GREEN) |
| 2 | Folded render guard and hostile render tests | 01722c1 (RED), 7c273b8 (GREEN) |

## What changed

- `internal/sanitize/fold.go`: `foldRune` (NFKC when it yields a single rune, then `unicode.ToLower`, then the skeleton map from the plan) and `Fold`.
- `internal/sanitize/sanitize.go`: `isFiller` drops U+3164, U+115F, U+1160, U+FFA0 and U+2800 in `clean`. `NeutralizeTag` compares `foldRune(rs[i+j])` against the tag. The ASCII-only `ContainsRune('[')` early exit is replaced by `hasOpenBracket`, which checks the folded runes. The package doc gained two bullets (CR-02, IN-01).
- `internal/render/render.go`: `fits` now uses `strings.Count(sanitize.Fold(l), tag) == 1`, and the package doc has a bullet on the folded guard.
- `internal/testutil/hostile.go`: 8 new entries (fullwidth, Cyrillic, U+27E6/U+27E7, U+2010, filler-split tag, filler-only, braille-blank login, the Hangul fillers). Each also gets the embedded `before ... after` variant.
- Tests: `checkProps` (shared by TestHostile and FuzzField) asserts `!strings.Contains(Fold(out), "[pr-relay]")`. The render `checkLine` uses the folded count. New tests: TestFold, TestNeutralizeTagFolded, TestFillersStripped, TestFitsFolded and TestHostileFoldedFields.

## Verification

- `go test -race -count=1 ./internal/sanitize/ ./internal/render/ ./internal/diff/` passes.
- `go test -count=1 ./...` passes.
- `go test -run '^$' -fuzz=FuzzField -fuzztime=10s ./internal/sanitize/` ran about 1.9M execs and found no failures.
- `git diff --stat internal/render/testdata internal/diff/testdata` is empty, so ASCII output is byte-identical.
- The grep acceptance checks pass: `norm.NFKC` and `func Fold(s string) string` in fold.go, `0x3164` in sanitize.go, `sanitize.Fold(l)` in render.go, and `pr-relay］` in hostile.go.

## Deviations from Plan

None in behaviour. Two small implementation notes:
- The "fuzz test: add the same folded assertion" step is covered by changing `checkProps`, because FuzzField already calls `checkProps` for Field, Path and Login. fuzz_test.go needed no edit.
- U+0406 has no entry of its own. `foldRune` lowercases before the skeleton lookup, so U+0406 becomes U+0456 and then 'l'.

## Follow-ups (out of scope)

- go.mod still lists `golang.org/x/text v0.43.0 // indirect`, but it is now imported directly. Running `go mod tidy` would drop the `// indirect` marker. I did not change it because the instructions say not to edit go.mod in this wave. Builds and tests are unaffected.

## Threat Flags

None. The changes stay within T-02-11..T-02-13 (mitigated) and T-02-14 (accepted).

## Self-Check: PASSED

- FOUND: internal/sanitize/fold.go, internal/sanitize/fold_test.go
- FOUND commits: f7e0cac, baaae75, 01722c1, 7c273b8
- TDD gates: test(02-04) commits come before each feat(02-04) commit.
