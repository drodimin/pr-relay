---
phase: 05-packaging-plugin-agent-skill
plan: 11
subsystem: release-gate
tags: [uat, herdr, release-gate, checkpoint]
requires:
  - phase: 05-01
    provides: CR-01 deadline fix and tests (TestSubscribeVerifiedDeadline*)
  - phase: 05-07
    provides: herdr plugin linked locally, popup actions
  - phase: 05-08
    provides: skill install
  - phase: 05-10
    provides: MIT-0 license, Homebrew dropped
provides:
  - D-22 release gate open: 04-HUMAN-UAT and 05-HUMAN-UAT both status: passed
affects: [05-12]
tech-stack:
  added: []
  patterns: []
key-files:
  created:
    - .planning/phases/05-packaging-plugin-agent-skill/05-HUMAN-UAT.md
  modified:
    - .planning/phases/04-daemon-control-protocol-cli/04-HUMAN-UAT.md
key-decisions:
  - "D-22 gate passed on 2026-11-10: the real herdr startup hook, popups, skill install and real PR delivery all verified; v0.1.0 tag may proceed in 05-12"
  - "UAT findings F1 (sub --pane confirmation text) and F2 (status auth line lacks login) are non-blocking follow-ups after v0.1.0"
requirements-completed: [DIST-03, DIST-04]
duration: multi-session (2026-11-07 to 2026-11-10)
completed: 2026-11-10
---

# Phase 5 Plan 11: Install-path UAT and release gate Summary

**Real herdr 0.9.5 and GitHub UAT passed with build v0.1.0-uat. The startup hook brought up a single daemon. The status and list popups were readable. The skill installed and a fresh agent used it. A real PR sub returned in 0.6s and delivered both `ci.settled` and `comment by` lines to an idle agent pane.**

## Performance

- Started: 2026-11-07T22:40:36Z (Task 1)
- Completed: 2026-11-10 (Task 3, after the human checkpoint)
- Tasks: 3/3
- Files modified: 2

## Accomplishments

- Task 1 (ba55144): the full automated gate was green. The UAT build was installed to `~/.local/bin/pr-relay`. 04-UAT item 2 passed: 20 concurrent cold-start `ls` calls left exactly 1 daemon. 04-UAT item 3 is covered by the CR-01 deadline tests. 05-HUMAN-UAT.md was created.
- Task 2 (human checkpoint): the user approved all four steps.
- Task 3 (0e02f7f): results were recorded. 05-HUMAN-UAT tests 1-4 passed. 04-HUMAN-UAT item 1 passed, covered by 05 test 4. Both files are at `status: passed`.

## UAT Results (2026-11-10)

1. **Startup hook (D-08):** plugin-log-1 `sh run.sh daemon --ensure` exited 0 with stdout `daemon running (pid 74536, v0.1.0-uat)`. One daemon process was running. Status showed `running (version v0.1.0-uat, pid 74536, up 5m)`.
2. **Popups (D-09):** the status-view and list-view popups exited 0, were readable and closed with Enter.
3. **Skill:** it was installed to `~/.agents/skills` and `~/.claude/skills`, 5695 bytes each, byte-identical to the source. A fresh agent outside the repo answered with details only the skill contains.
4. **Real delivery:** this used draft PR drodimin/pr-relay#1. `sub --pane w1:p9` took 0.6s. `ci.settled: passed (5 checks)` was delivered at 21:07:54, and the agent reacted on its own. `comment by dmitriy-medme` was delivered at 21:15:08. Auth went from `unknown (gh)` to `ok (gh)`. After unsub, `ls --all` printed `no subscriptions`. The PR was closed and its branch deleted.

## Task Commits

1. Task 1: full gate, UAT build, automated Phase 4 items: `ba55144`
2. Task 2: human checkpoint (no commit)
3. Task 3: record UAT results: `0e02f7f`

## Deviations from Plan

None. The plan executed as written. No code changes were made, as instructed.

## Follow-ups (non-blocking, after v0.1.0)

- **F1:** `pr-relay sub <url> --pane OTHER` prints "updates arrive in this pane" even when the target is another pane. The message should name the target pane.
- **F2:** `pr-relay status` shows `auth: ok (gh)` rather than the authenticated login that the UAT expected.

## Carry-forward for 05-12

- Homebrew was dropped for v0.1.0 (05-10). There are no brew, cask or tap steps.
- Remove the dev binary `~/.local/bin/pr-relay` (v0.1.0-uat) before the install-path checks (D-06), so that `go install` and the release download are tested cleanly.
- Local `main` is 319 commits ahead of `origin/main`. 05-12 must push `main` before tagging. Push, tag and release each need explicit user confirmation.

## Next Phase Readiness

The D-22 gate is open because both UAT files say `status: passed`. 05-12, which tags and releases v0.1.0, can proceed after user confirmation.

## Self-Check: PASSED

- FOUND: .planning/phases/05-packaging-plugin-agent-skill/05-HUMAN-UAT.md (status: passed, 0 pending)
- FOUND: .planning/phases/04-daemon-control-protocol-cli/04-HUMAN-UAT.md (status: passed, 0 pending)
- FOUND: ba55144, 0e02f7f
