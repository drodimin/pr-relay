---
phase: 05-packaging-plugin-agent-skill
plan: 10
subsystem: release
tags: [release, license, homebrew-dropped]
requires:
  - "05-09: .goreleaser.yaml archives.files and the homebrew_casks section"
provides:
  - "LICENSE: MIT-0 (MIT No Attribution), Copyright 2026 drodimin"
  - ".goreleaser.yaml archives ship LICENSE, README.md and pr-relay; no homebrew_casks"
  - "release.yml/ci.yml use GITHUB_TOKEN only; no HOMEBREW_TAP_GITHUB_TOKEN anywhere"
affects:
  - "05-11: HUMAN-UAT steps that mention brew are dropped"
  - "05-12: brew install verification is out of scope; verify go install and release download only"
tech-stack:
  added: []
  patterns: []
key-files:
  created:
    - LICENSE
  modified:
    - .goreleaser.yaml
    - .github/workflows/release.yml
    - .github/workflows/ci.yml
    - README.md
    - cmd/pr-relay/docs_test.go
    - cmd/pr-relay/plugin_test.go
    - herdr-plugin/run.sh
    - .planning/REQUIREMENTS.md
    - .planning/ROADMAP.md
decisions:
  - "License is MIT-0 with holder drodimin and year 2026, using the GitHub licenses API text verbatim (user decision)"
  - "Homebrew is dropped for v0.1.0 (user decision 2026-11-07): no drodimin/homebrew-tap repo, no tap PAT, no homebrew_casks"
  - "TestDocsDrift now fails if README.md mentions brew at all, so the dropped install path cannot creep back into the docs"
metrics:
  duration: "~10m (continuation)"
  completed: 2026-11-07
  tasks: 2
  files: 10
---

# Phase 5 Plan 10: Release Prerequisites (License, Homebrew Dropped) Summary

pr-relay ships under MIT-0 (`Copyright 2026 drodimin`), and release archives contain `LICENSE`, `README.md` and `pr-relay`. At the decision checkpoint the user dropped Homebrew for v0.1.0, so the cask, the tap token and every brew mention in shipped files are gone. The install paths are now `go install` and the release download.

## Tasks

| Task | Name | Status | Commit |
|------|------|--------|--------|
| 1 | Choose license and tap | Resolved by user: MIT-0, holder drodimin, Homebrew dropped | (decision only) |
| 2 | LICENSE, archives, drop Homebrew from release config | Done (modified per user decision) | c252be0 |
| 2b | Drop Homebrew from README, docs test, plugin hint, planning docs | Done | dae9746 |
| 3 | User creates HOMEBREW_TAP_GITHUB_TOKEN secret | Skipped: Homebrew dropped, so there is no tap repo to push to and no token is needed | n/a |

## What was done

- `LICENSE` holds the body from `gh api /licenses/mit-0` with `[year] [fullname]` replaced by `2026 drodimin`. Nothing else was edited. The text matches the canonical SPDX MIT-0 wording.
- `.goreleaser.yaml`: `archives.files: [LICENSE, README.md]`. The whole `homebrew_casks` section is removed, and the ldflags comment no longer mentions brew.
- `release.yml`: the `HOMEBREW_TAP_GITHUB_TOKEN` env line is removed. Only `GITHUB_TOKEN` (with `contents: write`) is used, and the header comment was updated. In `ci.yml` the snapshot job's dummy token env is removed.
- `README.md`: the Homebrew Quickstart option and the cask-quarantine sentence are removed. The `go install` and release-download paths are kept, including `checksums.txt` and the `xattr -d com.apple.quarantine` note. A `## License` section (MIT-0) is added at the end, after Troubleshooting, so the D-23 heading order still holds.
- `cmd/pr-relay/docs_test.go`: the `install` subtest no longer requires `brew install drodimin/tap/pr-relay` and now fails if the README mentions "brew".
- `herdr-plugin/run.sh` and `cmd/pr-relay/plugin_test.go`: the D-10 missing-binary hint now reads `install via go install github.com/drodimin/pr-relay/cmd/pr-relay@latest, or a release download`.
- `REQUIREMENTS.md` DIST-02 and the Phase 5 text in `ROADMAP.md` now say "Homebrew dropped for v0.1.0, user decision 2026-11-07". Checkbox and traceability states are unchanged.

## Verification

- `go run github.com/goreleaser/goreleaser/v2@v2.20.0 check` passes: 1 configuration file validated.
- `go run github.com/goreleaser/goreleaser/v2@v2.20.0 release --snapshot --clean --skip=publish` succeeds without any HOMEBREW env var.
- `tar -tzf dist/pr-relay_*_linux_amd64.tar.gz` lists `LICENSE`, `README.md` and `pr-relay`.
- `go test -count=1 ./...` passes in every package, including TestDocsDrift and the plugin tests.
- A grep for brew/homebrew/HOMEBREW_TAP/cask, excluding `.planning/`, `dist/` and `.git`, finds only the new docs_test guard and `CLAUDE.md`.

## Deviations from Plan

The plan was modified by the user's decision at Task 1. That is a decision outcome, not an auto-fix. Two changes went beyond the plan's file list:

**1. [Rule 2 - Correctness] Plugin missing-binary hint named brew**
- **Found during:** Task 2, step 5 (repo grep)
- **Issue:** `herdr-plugin/run.sh` told users to `brew install drodimin/tap/pr-relay`, but that tap will not exist.
- **Fix:** Removed the brew alternative from the hint and from the pinned constant in `plugin_test.go`.
- **Commit:** dae9746

**2. [Rule 2 - Correctness] Workflow comments and CI snapshot env referenced the tap token**
- **Fix:** Removed them so no shipped file references HOMEBREW_TAP_GITHUB_TOKEN.
- **Commit:** c252be0

Not changed: `CLAUDE.md` still describes Homebrew in its stack research tables. Those are project instructions, which an executor agent must not edit. The user may want to update them.

## Carry forward

- **05-12:** Verifying the brew install path is out of scope. Verify only `go install` and the release download (with checksums and the macOS `xattr` note) plus the plugin from GitHub.
- **05-12 (D-06):** The dev copy at `~/.local/bin/pr-relay` still has to be removed before install-path UAT, so that only one binary is on PATH.
- **05-11 / 05-12 HUMAN-UAT:** Drop every step that mentions brew, `brew install drodimin/tap/pr-relay`, the cask or `HOMEBREW_TAP_GITHUB_TOKEN`.

## Self-Check: PASSED

- FOUND: LICENSE, .goreleaser.yaml (LICENSE in archives.files, no homebrew_casks)
- FOUND: commits c252be0, dae9746
