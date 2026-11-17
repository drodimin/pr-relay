---
phase: 05-packaging-plugin-agent-skill
plan: 12
subsystem: release
tags: [release, tag, uat, checkpoint, goreleaser, herdr-plugin]
requires:
  - phase: 05-11
    provides: D-22 release gate open (04-HUMAN-UAT and 05-HUMAN-UAT passed)
  - phase: quick-261116-qss
    provides: golangci-lint v2.15.0 clean tree (62 issues fixed) so CI lint passes
provides:
  - Public release v0.1.0 (tag on 230e75d) with 4 tar.gz archives and checksums.txt
  - Module version github.com/drodimin/pr-relay@v0.1.0 resolvable through proxy.golang.org
  - herdr plugin installable from github ref v0.1.0
affects: [post-v0.1.0 follow-ups]
tech-stack:
  added: []
  patterns:
    - "Run golangci-lint locally (~/go/bin/golangci-lint run) before pushing; CI lint gates the release"
key-files:
  created:
    - .planning/phases/05-packaging-plugin-agent-skill/05-12-SUMMARY.md
  modified:
    - .planning/phases/05-packaging-plugin-agent-skill/05-HUMAN-UAT.md
key-decisions:
  - "05-12: v0.1.0 published 2026-11-17 after the user chose 'publish now'. The tag was held until CI went green after the lint fix in quick 261116-qss."
  - "05-12: Homebrew stays out of v0.1.0. The install paths are go install, the GitHub release download and herdr plugin install --ref v0.1.0."
  - "05-12: a remote herdr plugin install from a non-interactive shell needs --yes. The README omits it (follow-up F3)."
requirements-completed: [DIST-01, DIST-02, DIST-03]
duration: multi-session (2026-11-16 to 2026-11-17)
completed: 2026-11-17
---

# Phase 5 Plan 12: Publish v0.1.0 and verify install paths Summary

**pr-relay v0.1.0 is public on GitHub. It has 4 darwin/linux amd64/arm64 archives and checksums.txt, all verified. `go install ...@v0.1.0` and the release binary both print `pr-relay v0.1.0`. herdr installed the plugin from `github:drodimin/pr-relay/herdr-plugin@v0.1.0`, and after a restart its startup hook brought up the single go-installed daemon (pid 13450, v0.1.0).**

## Performance

- Duration: multi-session (decision on 2026-11-16, release and human verification on 2026-11-17)
- Tasks: 3 of 3
- Files modified: 1 (05-HUMAN-UAT.md), plus this summary

## Accomplishments

- **Task 1 (decision):** the user chose "publish now". After the first CI lint failure they replied "go ahead with the fix" and then "push".
- **Task 2 (automated release):**
  - The first push of main (5cd46a0..494a986) failed CI lint with 62 golangci-lint v2.15.0 issues. Lint had never been run locally.
  - The tag was held and the issues were fixed in quick task 261116-qss (795ade6, 731e224, c961e79, 230e75d).
  - The second push (494a986..230e75d) passed CI run 43987742829: lint, 4 test jobs and the goreleaser check.
  - The annotated tag v0.1.0 on 230e75d was pushed, and release workflow run 43988046368 succeeded.
  - The release is at https://github.com/drodimin/pr-relay/releases/tag/v0.1.0.
  - Checksums are OK, and the archives contain LICENSE, README.md and pr-relay.
- **Task 3 (human, adapted for no Homebrew):**
  - The dev copy was replaced by a go-installed v0.1.0 in `~/.local/bin`, which is the only binary (D-06).
  - The local plugin link was swapped for the remote install at ref v0.1.0, commit 230e75d.
  - After a herdr restart, plugin-log-1 `daemon --ensure` exited 0. One daemon process ran, and `pr-relay status` reported v0.1.0.
  - Linux was not run. The linux_amd64 archive was checksum- and content-verified instead.

## Task Commits

1. **Task 1: Confirm push and tag:** decision only, no commit
2. **Task 2: Release and artifact verification:** `6a503da` (test). The lint fixes landed in quick 261116-qss: `795ade6`, `731e224`, `c961e79`, `230e75d`.
3. **Task 3: Post-release human verification:** `4c5b742` (test)

## Files Created/Modified

- `.planning/phases/05-packaging-plugin-agent-skill/05-HUMAN-UAT.md` has new "Post-release (automated)" and "Post-release (human)" sections, plus F3.

## Decisions Made

- v0.1.0 was published only after CI went fully green. The tag was held through the lint failure, so no broken tag reached proxy.golang.org.
- The Homebrew cask checks are N/A, because Homebrew was dropped on 2026-11-07. The DIST-02 brew clause and the DIST-03 "brew-installed daemon" clause were satisfied with the go-installed binary instead.

## Deviations from Plan

1. **CI lint gate failure (blocking, Rule 3, fixed outside the plan).** The first push failed golangci-lint (62 issues). The fix went through quick task 261116-qss with user approval, and only then was the tag pushed.
2. **Homebrew steps adapted.** The plan's brew install, Gatekeeper and cask checks were replaced by `go install` into `~/.local/bin`, following the 05-10 decision to drop Homebrew.
3. **`--yes` needed for the remote plugin install.** `herdr plugin install ... --ref v0.1.0` refuses in a non-interactive shell without `--yes`. It was re-run with `--yes`. The docs gap is recorded as F3.

## Issues Encountered

- golangci-lint was not part of the local workflow. It is now installed at `~/go/bin/golangci-lint`, which is not on PATH. Run `~/go/bin/golangci-lint run` locally before every push, or add `~/go/bin` to PATH.

## Follow-ups (post v0.1.0)

- **F1:** `pr-relay sub <url> --pane OTHER` prints "updates arrive in this pane" even when the target is another pane. The text should name the target pane.
- **F2:** `pr-relay status` shows `auth: ok (gh)` rather than the authenticated login, as in `auth: ok (<login>, gh)`.
- **F3:** The README plugin install step (`herdr plugin install drodimin/pr-relay/herdr-plugin`) omits `--yes`, which a non-interactive shell needs, and does not show `--ref v0.1.0`. The README was not edited in this close-out.

## User Setup Required

None.

## Next Phase Readiness

- This is the last plan of phase 5, so the v1.0 milestone is ready for phase verification.
- F1, F2 and F3 are candidates for a v0.1.1 gap-closure or quick task.

## Self-Check: PASSED

- FOUND: .planning/phases/05-packaging-plugin-agent-skill/05-HUMAN-UAT.md
- FOUND commits: 6a503da, 795ade6, 731e224, c961e79, 230e75d, 4c5b742
