---
phase: 01-toolchain-spikes-pure-core
plan: 05
subsystem: render
tags: [render, sanitize, golden, security, DLV-05, DLV-06]
requires:
  - internal/model (01-02)
  - internal/sanitize, internal/testutil HostileCorpus/Golden (01-03, 01-01)
provides:
  - "render.Lines(ref model.PRRef, events []model.Event) []string"
  - "render.MaxLineBytes = 512"
  - internal/render/testdata/lines.golden
affects:
  - Phase 3 delivery (joins lines, owns any flood ceiling after spike S6)
tech-stack:
  added: []
  patterns: [golden-file tests, structural reflect guard, corpus x field x type property test]
key-files:
  created:
    - internal/render/render.go
    - internal/render/render_test.go
    - internal/render/hostile_test.go
    - internal/render/testdata/lines.golden
  modified: []
decisions:
  - "Over-cap minimal line is `[pr-relay] o/r#N <head> … P` (head = grammar keyword, e.g. `ci.failed:`), not the literal `<type> P`, so it still matches the plan's line grammar regex"
  - "The line validity check (<=512 bytes and exactly one case-insensitive tag) runs on the assembled line. Any failure falls back to the minimal line, which also guards against tag assembly across field boundaries"
  - "Force push with an invalid new or old SHA falls back to P/commits and omits `(was …)`"
  - "Collision qualification `<workflow> / <name>` applies only to events with a non-empty CheckWorkflow"
  - "An empty comment Path omits ` on <path>`. An unknown CommentKind renders as a plain `comment by`"
  - "No flood ceiling. Joining, coalescing and a per-message line ceiling are deferred to Phase 3 (documented in the package doc)"
metrics:
  duration: ~10 min
  completed: 2026-10-31
  tasks: 2
  files: 4
---

# Phase 1 Plan 05: Message Renderer Summary

`render.Lines` turns typed PR events into one sanitized, self-contained `[pr-relay] owner/repo#N <event> … <github-url>` line per event. Every third-party field goes through `sanitize`, URLs go through an allow-list with PRRef-built fallbacks, and lines are capped at 512 bytes. A golden file, a grammar regex, a structural no-body guard and a full hostile-corpus property test lock this down.

## Tasks

| Task | Name | Commits | Files |
| ---- | ---- | ------- | ----- |
| 1 | render.Lines with every line format and golden | 169351f (RED), 5ef8275 (GREEN) | render.go, render_test.go, testdata/lines.golden |
| 2 | Hostile-input render test | 5c3897a | hostile_test.go |

## What was built

- **Grammar:** every row of the approved grammar. Verdicts come from a fixed table (unknown values become `submitted`, and the raw value is never shown). Inline and check counts handle the singular. Commits cover `N new`, `new commits` for Count 0, and force-push with a compare URL built only from ShortSHA-validated hex.
- **Bots (D-12):** a bot renders as `login[bot]` whether or not the suffix is already present, matched case-insensitively.
- **D-03:** 9 failures produce 9 `ci.failed` lines with distinct URLs and no `+N more`.
- **Size:** in `ci.settled: failed`, the names are dropped before the line goes over the cap. Any line still over the cap, or with a bad tag count, falls back to the minimal line.
- **Tests:**
  - TestGolden, TestLineGrammar, TestNoBodyFields (reflect: no Body/Title/Text field), TestD03NoTruncation, TestInvalidRef, TestBotLogin, TestSettledNamesDroppedWhenLong.
  - TestHostileRender covers 86 corpus entries x 12 base variants (all 7 types) x 3 actor types x 2 comment-kind modes x 2 refs. One ref has 100-character owner and repo and Number=MaxInt64. It also covers the collision pair.
  - TestHostileCheckURLFallback and TestOverCapFallback.
- `go vet` is clean and `go test -race ./internal/render/` passes. Non-test imports are fmt, strings, internal/model and internal/sanitize. There are 14 `sanitize.` call sites.

## Golden file (internal/render/testdata/lines.golden)

```
[pr-relay] octo/repo#7 ci.failed: lint https://github.com/octo/repo/actions/runs/1/job/11
[pr-relay] octo/repo#7 ci.failed: unit https://github.com/octo/repo/pull/7/checks
[pr-relay] octo/repo#7 ci.failed: evil-link https://github.com/octo/repo/pull/7/checks
[pr-relay] octo/repo#7 ci.failed: Linux / build https://github.com/octo/repo/actions/runs/2/job/21
[pr-relay] octo/repo#7 ci.failed: macOS / build https://github.com/octo/repo/actions/runs/2/job/22
[pr-relay] octo/repo#7 ci.settled: passed (12 checks) https://github.com/octo/repo/pull/7/checks
[pr-relay] octo/repo#7 ci.settled: passed (1 check) https://github.com/octo/repo/pull/7/checks
[pr-relay] octo/repo#7 ci.settled: failed (2 of 12): lint, unit https://github.com/octo/repo/pull/7/checks
[pr-relay] octo/repo#7 review approved by alice https://github.com/octo/repo/pull/7#pullrequestreview-1
[pr-relay] octo/repo#7 review changes_requested by alice (4 inline comments) https://github.com/octo/repo/pull/7#pullrequestreview-2
[pr-relay] octo/repo#7 review commented by bob (1 inline comment) https://github.com/octo/repo/pull/7#pullrequestreview-3
[pr-relay] octo/repo#7 review dismissed by bob https://github.com/octo/repo/pull/7
[pr-relay] octo/repo#7 review submitted by bob https://github.com/octo/repo/pull/7
[pr-relay] octo/repo#7 review approved by dependabot[bot] https://github.com/octo/repo/pull/7#pullrequestreview-4
[pr-relay] octo/repo#7 review commented by copilot-pull-request-reviewer[bot] https://github.com/octo/repo/pull/7#pullrequestreview-5
[pr-relay] octo/repo#7 comment by bob https://github.com/octo/repo/pull/7#issuecomment-1
[pr-relay] octo/repo#7 reply by carol on src/x.go https://github.com/octo/repo/pull/7#discussion_r2
[pr-relay] octo/repo#7 comment by dave on src/y.go https://github.com/octo/repo/pull/7#discussion_r3
[pr-relay] octo/repo#7 comment by ghost https://github.com/octo/repo/pull/7
[pr-relay] octo/repo#7 comment by github-actions[bot] https://github.com/octo/repo/pull/7#issuecomment-4
[pr-relay] octo/repo#7 commits: 3 new, head abc1234 https://github.com/octo/repo/pull/7/commits
[pr-relay] octo/repo#7 commits: 1 new, head abc1234 https://github.com/octo/repo/pull/7/commits
[pr-relay] octo/repo#7 commits: new commits, head abc1234 https://github.com/octo/repo/pull/7/commits
[pr-relay] octo/repo#7 commits: force-pushed, head abc1234 (was def5678) https://github.com/octo/repo/compare/def5678...abc1234
[pr-relay] octo/repo#7 commits: force-pushed, head abc1234 https://github.com/octo/repo/pull/7/commits
[pr-relay] octo/repo#7 conflict: conflicting https://github.com/octo/repo/pull/7
[pr-relay] octo/repo#7 conflict: cleared https://github.com/octo/repo/pull/7
[pr-relay] octo/repo#7 closed: merged by alice https://github.com/octo/repo/pull/7
[pr-relay] octo/repo#7 closed: closed by bob https://github.com/octo/repo/pull/7
[pr-relay] octo/repo#7 closed: merged https://github.com/octo/repo/pull/7
[pr-relay] octo/repo#7 ci.failed: shard-1 https://github.com/octo/repo/actions/runs/9/job/900
[pr-relay] octo/repo#7 ci.failed: shard-2 https://github.com/octo/repo/actions/runs/9/job/901
[pr-relay] octo/repo#7 ci.failed: shard-3 https://github.com/octo/repo/actions/runs/9/job/902
[pr-relay] octo/repo#7 ci.failed: shard-4 https://github.com/octo/repo/actions/runs/9/job/903
[pr-relay] octo/repo#7 ci.failed: shard-5 https://github.com/octo/repo/actions/runs/9/job/904
[pr-relay] octo/repo#7 ci.failed: shard-6 https://github.com/octo/repo/actions/runs/9/job/905
[pr-relay] octo/repo#7 ci.failed: shard-7 https://github.com/octo/repo/actions/runs/9/job/906
[pr-relay] octo/repo#7 ci.failed: shard-8 https://github.com/octo/repo/actions/runs/9/job/907
[pr-relay] octo/repo#7 ci.failed: shard-9 https://github.com/octo/repo/actions/runs/9/job/908
```

(The input also includes an unknown `labeled` event, which is skipped, so 39 lines come from 40 events.)

## Deviations from Plan

1. **[Rule 1 - Bug] Minimal fallback line shape.** The plan text gives the fallback as `[pr-relay] o/r#N <type> P`. That literal form fails the plan's own grammar regex, which needs a `:` keyword for colon types and some text between the keyword and the URL. The fallback is now `[pr-relay] o/r#N <head> … P`. TestOverCapFallback and TestHostileRender (long ref) cover it. Commit 5ef8275.
2. **[Rule 2 - Defence in depth] Tag-count check on the assembled line.** As well as the byte cap, the renderer checks that the whole line holds exactly one case-insensitive `[pr-relay]`, and uses the minimal line otherwise. Commit 5ef8275.
3. **Extra tests beyond the plan:** TestInvalidRef, TestBotLogin, TestSettledNamesDroppedWhenLong, TestHostileCheckURLFallback and TestOverCapFallback. TestHostileRender also uses a maximum-length ref and the ci.failed collision pair.

## TDD Gate Compliance

- Task 1: the RED commit 169351f failed to compile because `Lines` and `MaxLineBytes` did not exist yet. It was followed by the GREEN commit 5ef8275.
- Task 2: hostile_test.go passed on its first run against the Task 1 implementation, because Task 1 had already sanitized every field and applied the cap fallback. No change to render.go was needed. This is a verification-only test commit (5c3897a), not a RED gate that failed.

## Known Stubs

None.

## Threat Flags

None. T-01-20 to T-01-23 are mitigated as planned. T-01-24 (joining) is transferred to Phase 3 and documented in the package doc comment.

## Self-Check: PASSED

- FOUND: internal/render/render.go, render_test.go, hostile_test.go, testdata/lines.golden
- FOUND commits: 169351f, 5ef8275, 5c3897a
