# Phase 1: Toolchain, Spikes & Pure Core - Context

**Gathered:** 2026-10-31
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 1 delivers three things:
1. A working Go toolchain and a public GitHub repo whose CI is green on macOS and Linux.
2. A written spike report on the risky herdr and GitHub runtime behaviours:
   - typing collision with `agent.prompt`
   - `agent.wait` while blocked, and `agent.prompt`'s `wait` option
   - whether pane id and `terminal_id` stay stable across a herdr restart
   - GraphQL query shape and cost
   - whether a setsid child survives herdr stopping
3. A pure, fixture-tested core (`model`, `diff`, `render`, `sanitize`). It turns PR state snapshot pairs into events (ci.failed, ci.settled, review, comment, commits, conflict, closed), and renders them as terse, sanitized `[pr-relay]` messages.

Requirements covered: EVT-01 to EVT-08, DLV-05, DLV-06.

Out of scope for this phase:
- persistence and the store
- the live poller
- delivery to herdr
- the daemon and CLI
- packaging

</domain>

<decisions>
## Implementation Decisions

### Message shape (render package)
- **D-01:** One line per item. Each line is self-contained and starts with the fixed tag and the PR identity: `[pr-relay] owner/repo#N <event>: <detail> <url>`.
- **D-02:** When updates are coalesced, the result is several self-contained lines in a single prompt. Updates are not merged into one clause-joined line, and there is no header/bullet block.
- **D-03:** An event with many items gets one line per item, each with its own link. For example, 9 failing checks produce 9 `ci.failed` lines. Do not truncate with "+N more". The user said "just do multiple lines".
- **D-04:** The PR title never appears in messages. `owner/repo#N` plus the URL is the PR's identity, so no free text authored by the PR opener reaches the agent. Third-party strings still go through the sanitizer: check names, usernames and file paths.
- **D-05:** Messages never contain comment bodies, review text or log content (DLV-05, already locked).

### CI event semantics (diff package)
- **D-06:** ci.failed fails fast. It fires as soon as a check run concludes failed on the current head SHA, at most once per check name per head SHA. A new head SHA resets that per-check state.
- **D-07:** ci.settled fires once when every check on the head SHA has completed. It carries the overall result: passed, or failed with the failing names.
- **D-08:** If a check fails and a rerun on the same SHA then passes, nothing extra is sent. The earlier ci.failed stands, and ci.settled reports the final state. There is no `ci.recovered` event, and a check that fails again on rerun does not re-fire ci.failed for that SHA.
- **D-09:** Only check runs count. Legacy commit statuses (the `StatusContext` entries in the rollup) are ignored. Required-check awareness stays v2 (EVT2-03).

### Reviews, comments & bots (diff + render)
- **D-10:** A submitted review produces one `review` line with the verdict, the reviewer, the number of inline comments and the review link, e.g. `review changes_requested by alice (4 inline comments) <review-url>`. Inline comments that belong to that review do not produce separate `comment` lines.
- **D-11:** Each standalone new comment (issue comment, or a thread reply that isn't part of a new review) produces one `comment` line. The line has the author, the kind (comment, or reply on <file>) and the comment's own link.
- **D-12:** In v1, bots are treated exactly like humans: same lines, same rules (EVT-08). Bot noise control waits for FILT-04.
- **D-13:** Edits and deletions of comments and reviews are ignored. Only newly created items fire.
- **D-14:** Activity by the gh-authenticated user is always excluded (EVT-08, already locked).

### Spikes & toolchain
- **D-15:** Install Go on the dev machine with `brew install go`. This is a dev-only dependency, since users get a prebuilt static binary. CI pins its own version via the `go` directive in `go.mod` and `actions/setup-go`.
- **D-16:** The executor writes small probe programs or scripts and runs them against scratch herdr panes. Steps that need a human, such as typing mid-prompt or restarting herdr, become checkpoints: the user performs the action and the executor records the result. Never send prompts to non-scratch panes.
- **D-17:** Create the public GitHub repo in this phase. This is a checkpoint: the user creates it, or approves `gh repo create`. Then push and add a GitHub Actions matrix covering ubuntu-latest and macos-latest (build, vet, test).
- **D-18:** GraphQL fixtures are real `gh api graphql` snapshots captured from scratch PRs on a dummy repo that the user supplies, collected at a checkpoint. The scenarios needed are:
  - CI fail and pass, a flaky rerun, and a cancelled run
  - reviews of each verdict, with inline comments
  - issue comments and thread replies
  - a new push and a force-push
  - conflict and conflict clear, including UNKNOWN mergeable
  - merged and closed
  - bot authors

  GitHub doesn't let you review your own PR, and EVT-08 filters your own activity, so the review, comment and bot scenarios need a second account or a bot acting on the dummy repo. The plan must ask the user for this.

### Post-research decisions (2026-10-31, answers to RESEARCH.md Q1–Q5)
- **D-19:** If a job is rerun on a SHA where `ci.settled` was already reported, re-arm settle tracking. Emit a new `ci.settled` only when the new settled result differs from the one already reported for that SHA. (Q1)
- **D-20:** Deliver `closed`/`merged` even when the gh-authenticated user merged or closed the PR themselves. This is an explicit exception to EVT-08's self-exclusion. (Q2)
- **D-21:** If mergeable is UNKNOWN at baseline, the first resolved value is announced: if it is CONFLICTING, emit a conflict event immediately so the agent learns the PR was already conflicting. If it is MERGEABLE, adopt it silently, since there's nothing to report. Later changes emit as usual. UNKNOWN itself still emits nothing (EVT-06). (Q3: the user chose option (b), "announce it".)
- **D-22:** Fixture second actor: use both a `workflow_dispatch` workflow on the dummy repo that acts via `GITHUB_TOKEN` as `github-actions[bot]` (bot comments, plus approvals if the repo setting allows) and a second human GitHub account (approve, request-changes and comment reviews). The capture checkpoint must ask the user for the second account. (Q4)
- **D-23:** Go module path: `github.com/drodimin/pr-relay`, which is also the public repo for D-17. (Q5)

### Claude's Discretion
- How a single non-coalesced event is laid out. This is effectively settled by D-01 to D-03: one line.
- How check conclusions map to failed or neutral. The research recommendation is:
  - failure-like: failure, timed_out, action_required, startup_failure
  - neutral: skipped, neutral, cancelled, stale

  Follow GitHub's semantics.
- How ci.settled behaves when a head SHA has zero check runs.
- How `commits` events read: count, short SHAs, and how a force-push is marked.
- Whether to add an absolute flood guard. D-03 rules out "+N more" truncation, but a pathological case could still need a hard per-message line ceiling, such as a monorepo with 200 checks. If one is added, keep it high and document it.
- How long each sanitized field may be. Research suggests about 80 characters. Also which unicode categories to strip beyond control characters, ESC and newlines: zero-width and bidi overrides are recommended.
- Where the spike report lives (suggested: `.planning/phases/01-toolchain-spikes-pure-core/01-SPIKES.md`), and the package layout, lint config and minimum Go version.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project scope & requirements
- `.planning/PROJECT.md`: vision, constraints, key decisions, out of scope
- `.planning/REQUIREMENTS.md`: EVT-01 to EVT-08, DLV-05 and DLV-06 (this phase), and the "flow-agnostic primitives" design principle
- `.planning/ROADMAP.md` §Phase 1: goal and the 5 success criteria
- `CLAUDE.md`: technology stack, the herdr client notes, and testing guidance (golden files, fake servers, `testing/synctest`)

### Research
- `.planning/research/SUMMARY.md`: Phase 1 deliverables, the spike list, and the gaps to address
- `.planning/research/PITFALLS.md`: prompt injection, typing collision, CI flapping, lazy `mergeable`, comment object types, force-pushes
- `.planning/research/ARCHITECTURE.md`: pure core package boundaries (model, diff, render, sanitize) and data flow
- `.planning/research/FEATURES.md`: event feature landscape
- `.planning/research/STACK.md`: Go and library versions

### External
- herdr socket API: https://herdr.dev/tui/docs/socket-api/ and local `herdr api schema --json` (herdr 0.9.5, protocol 22)

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- None. This is a greenfield repo with only planning docs and `BRIEF.md`.

### Established Patterns
- None yet. This phase sets the conventions: Go module layout, test style and golden-file layout.

### Integration Points
- The pure core packages are consumed later: by the store and poller in Phase 2, and by delivery coalescing in Phase 3. Keep `diff` and `render` free of I/O and clock dependencies, and inject time if it's needed.
- Spike results gate the Phase 2 and Phase 3 designs, so record them as answers (YES, NO or UNKNOWN, plus evidence), not as narrative.

</code_context>

<specifics>
## Specific Ideas

- Example lines the user approved:
  - `[pr-relay] owner/repo#123 ci.failed: lint <check-url>` (one per failing check)
  - `[pr-relay] owner/repo#123 review changes_requested by alice (4 inline comments) <review-url>`
  - `[pr-relay] owner/repo#123 comment by bob <comment-url>`
  - `[pr-relay] owner/repo#123 reply by carol on src/x.go <comment-url>`
- Go is dev-only. Users install the binary, so make no assumption that Go exists at runtime.

</specifics>

<deferred>
## Deferred Ideas

None. The discussion stayed within the phase scope. These were noted as already v2 in REQUIREMENTS.md: bot policy (FILT-04), required checks (EVT2-03), and notify-on-edit (not planned).

</deferred>

---

*Phase: 01-toolchain-spikes-pure-core*
*Context gathered: 2026-10-31*
