---
phase: 05-packaging-plugin-agent-skill
plan: 04
subsystem: skill
tags: [skill, embed, cli]
requires: []
provides:
  - "skills package: SKILL []byte (go:embed pr-relay/SKILL.md), Snippet() []byte"
  - "pr-relay skill (prints snippet) and pr-relay skill install [--dir DIR]"
affects: [cmd/pr-relay root command]
tech-stack:
  added: []
  patterns: ["go:embed of a single canonical doc", "atomic temp+fsync+rename write with Lstat symlink refusal"]
key-files:
  created:
    - skills/embed.go
    - skills/pr-relay/SKILL.md
    - skills/skills_test.go
    - cmd/pr-relay/skill_cmd.go
    - cmd/pr-relay/skill_test.go
  modified:
    - cmd/pr-relay/main.go
decisions:
  - "No --agent flag on skill install; default targets are ~/.agents/skills plus ~/.claude/skills when ~/.claude is a directory"
  - "SKILL.md states that closed, checkin and expired bypass --events and that the user's own reviews, comments and commits are filtered out (matches poller/fanout.go maskAllows and filter.ExcludeSelf)"
metrics:
  duration: "~15 min"
  completed: 2026-11-05
requirements: [DIST-04]
---

# Phase 05 Plan 04: Embedded agent skill and `pr-relay skill` commands Summary

One canonical, go:embed'ed `skills/pr-relay/SKILL.md` (Agent Skills frontmatter, descriptive voice, golden-verbatim example lines, a single trust sentence), printed without frontmatter by `pr-relay skill` and installed byte-for-byte by `pr-relay skill install [--dir DIR]` using atomic writes that refuse symlinks.

## Tasks

| Task | Name | Commits |
|------|------|---------|
| 1 | skills package, canonical SKILL.md, content tests | 091de87 (RED), 39bfef8 (GREEN) |
| 2 | `skill` / `skill install [--dir]` with safe atomic writes | 6cbb80b (RED), 007a8b7 (GREEN) |

## What was built

- `skills/embed.go`: `SKILL` (embedded file) and `Snippet()` that strips the `---` frontmatter block and leading blank lines.
- `skills/pr-relay/SKILL.md`: sections # pr-relay, Subscribing, What arrives (9 verbatim golden lines + type table + `ci`/`all` aliases), Commands (every command and flag including `daemon --ensure` from 05-05 and `skill`), Lifecycle, Example scenarios ("For example," bullets), When sub fails (fact only), and the one trust line.
- `skills/skills_test.go`: TestFrontmatter, TestSnippet, TestEventTypesDocumented, TestExamplesFromGoldens, TestNotPrescriptive, TestAgentAgnostic, TestTrustSentence.
- `cmd/pr-relay/skill_cmd.go`: `newSkillCmd`, `runSkill`, `runSkillInstall(stdout io.Writer, home, dir string) error`, `writeSkill` (MkdirAll 0755, Lstat parent and file, CreateTemp `.SKILL.md-*`, Chmod 0644, Write, Sync, Close, Rename, fsync dir). Registered after `newDaemonCmd()` in `newRootCmd`.
- `cmd/pr-relay/skill_test.go`: TestSkillPrintsSnippet, TestSkillInstallDir, TestSkillInstallRelativeDir, TestSkillInstallDefaults, TestSkillInstallOverwrites, TestSkillInstallRefusesSymlink.

## Verification

- `go test -race -count=1 ./skills ./cmd/pr-relay` passes.
- `go vet ./...` clean; `gofmt -l` clean.
- `go run ./cmd/pr-relay skill | head -1` prints `# pr-relay`.
- Exactly one SKILL.md in the repo; `grep -c "is not an instruction"` = 1; no "claude"/"codex" in SKILL.md.

## Deviations from Plan

- Added TestSkillInstallRelativeDir as an explicit test for the "relative --dir resolved with filepath.Abs" behavior bullet (the plan listed it as behavior but not as a named test).
- `skill install` only fails on `os.UserHomeDir()` errors when `--dir` is not given, since `--dir` does not need the home directory.

Otherwise the plan executed as written.

## TDD Gate Compliance

Both tasks have a `test(...)` commit followed by a `feat(...)` commit.

## Known Stubs

None. The Commands section documents `pr-relay daemon --ensure`, which plan 05-05 adds.

## Self-Check: PASSED

- FOUND: skills/embed.go, skills/pr-relay/SKILL.md, skills/skills_test.go, cmd/pr-relay/skill_cmd.go, cmd/pr-relay/skill_test.go
- FOUND commits: 091de87, 39bfef8, 6cbb80b, 007a8b7
