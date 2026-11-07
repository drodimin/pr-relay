---
phase: 05-packaging-plugin-agent-skill
plan: 08
subsystem: docs
tags: [docs, readme, drift-test]
requires:
  - "05-04/05-05: SKILL.md and pr-relay skill commands"
  - "05-07: final plugin manifest (popup panes, no TUI action menu)"
provides:
  - "README.md (task-first, D-23 section order)"
  - "TestDocsDrift pinning README and SKILL.md to goldens, cobra tree, event set and config keys"
  - "REQUIREMENTS.md/ROADMAP.md drift fixes (SUB-10, SUB-11, 16-minute ceiling, softened DIST-04)"
affects:
  - "05-09+ release plans: README asserts the Homebrew cask clears quarantine in a post-install step; the cask config must actually do it"
tech-stack:
  added: []
  patterns:
    - "Docs drift test: every [pr-relay] line or backtick span in docs must be a golden line; table-cell \\| escapes are undone before comparing"
key-files:
  created:
    - README.md
    - cmd/pr-relay/docs_test.go
  modified:
    - go.mod
    - .planning/REQUIREMENTS.md
    - .planning/ROADMAP.md
decisions:
  - "Drift test unescapes \\| inside table-cell code spans, because GitHub Markdown needs the escape and every checkin golden line contains a pipe"
  - "README documents [[keys.command]] keybindings for pr-relay.status and pr-relay.list-all, since herdr 0.9.5 has no plugin menu"
  - "LIFE-08 slowest rate is poll.max_interval (16m default), read from scheduler.go (base = maxI)"
metrics:
  duration: "~25m"
  completed: 2026-11-07
  tasks: 3
  files: 6
---

# Phase 5 Plan 08: README, Docs Drift Test and Planning-Doc Fixes Summary

There is now a single task-first README.md in the D-23 section order. `TestDocsDrift` fails whenever README.md or SKILL.md drifts from the render goldens, the cobra command and flag tree, the 9 event types or the config.toml keys. REQUIREMENTS.md and ROADMAP.md now match the shipped behaviour.

## What was built

- **cmd/pr-relay/docs_test.go**: `TestDocsDrift` has the subtests examples_catch_table_drift (an in-memory self-check), examples, events, commands, flags, config, sections and install. `exampleSpans` collects whole `[pr-relay] ` lines and backtick spans, including spans in table cells. Command paths come from walking `newRootCmd()` (skipping hidden commands, help and completion), and flags come from `LocalNonPersistentFlags().VisitAll`. Config tables and keys are found by reflecting over `config.Config` toml tags. The test imports `github.com/spf13/pflag`, which `go mod tidy` moved from an indirect to a direct dependency.
- **README.md** has these sections: pitch, Quickstart (brew, go install with the PATH and `herdr server stop` note, release download plus checksums; plugin; skill; sub), Commands (a table of all 14 command paths with their flags, the plugin actions, and a `[[keys.command]]` keybinding snippet), Events (9 rows with golden examples, the `ci`/`all` aliases and a fenced block of more examples), Configuration (per-OS path and all 10 keys with defaults), How it works, Threat model, and Troubleshooting (status, log and state paths per OS, plugin log, the PATH error line, `xattr -d com.apple.quarantine`, skill reload).
- **Planning docs**: the design principle now says "example scenarios". LIFE-05 has the 16-minute ceiling. LIFE-08 names `poll.max_interval`. SUB-10 (poll) and SUB-11 (reset) were added, with traceability rows, and coverage is 50/50. DIST-04 and Phase 5 SC-3 are softened. Phase 2 SC-4 has the 16-minute ceiling, and Phase 4 SC-4 says check-ins repeat. LIFE-06 already said check-ins repeat, so it is unchanged.

## Verification

- `go test ./cmd/pr-relay -run TestDocsDrift -count=1 -v`: all 8 subtests pass. `go test ./skills` passes. `go test ./...` passes.
- RED state before the README existed: only README-related failures. With an empty README, the output contained no SKILL failures.
- Mutation check: changing only the ci.failed Example cell makes TestDocsDrift fail (rc 0 for the negated test). README.md was restored afterwards.
- README has 7 `## ` headings. It contains no "homebrew avoids quarantine", and there is no docs/ directory.

## Commits

- `7469960` test(05-08): add failing docs drift test for README and SKILL.md
- `193ba2e` docs(05-08): write task-first README pinned by the drift test
- `3ec6989` docs(05-08): fix REQUIREMENTS and ROADMAP drift (D-25, D-16)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Table-cell pipe escaping in the drift test**
- **Found during:** Task 2
- **Issue:** Every checkin golden line contains `|`. In a GitHub Markdown table cell that pipe must be written as `\|`, even inside a code span, so the extracted span never matched the golden line.
- **Fix:** Added `unescapeCell` and applied it to captured spans in `exampleSpans` and in the per-row events check. The mutation check still fails as required.
- **Files modified:** cmd/pr-relay/docs_test.go
- **Commit:** 193ba2e

**2. [Rule 3 - Blocking] go.mod: pflag moved to a direct dependency**
- The test imports pflag for the `VisitAll` callback type, and `go mod tidy` moved the require line. No version changed.
- **Commit:** 7469960

## Known Stubs

None.

## Notes for later plans

- The README says the Homebrew cask removes the quarantine attribute in its post-install step (research Pitfall 1). The release plan's cask config must include that `xattr` hook, or the README line must be revised.
- The release-download instructions use `checksums.txt` and generic archive names, because the GoReleaser config does not exist yet.

## Self-Check: PASSED

- README.md, cmd/pr-relay/docs_test.go and this SUMMARY exist
- Commits 7469960, 193ba2e and 3ec6989 are present in git log
