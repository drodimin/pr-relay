# Phase 1: Toolchain, Spikes & Pure Core - Research

**Researched:** 2026-10-31
**Domain:** Go toolchain/CI bootstrap, herdr + GitHub runtime spikes, pure event-diff/render/sanitize core
**Confidence:** MEDIUM-HIGH. GitHub GraphQL schema facts are HIGH (checked against the published schema `schema.docs.graphql`). herdr facts are HIGH (local `herdr api schema --json`, CLI help, v0.9.5 docs). Some GitHub runtime behaviours (rerun representation in the rollup, whether a thread reply creates a review object, Bot login format) are ASSUMED and must be confirmed by the fixture capture.

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions

#### Message shape (render package)
- **D-01:** One line per item. Each line is self-contained and starts with the fixed tag and the PR identity: `[pr-relay] owner/repo#N <event>: <detail> <url>`.
- **D-02:** When updates are coalesced, the result is several self-contained lines in a single prompt. Updates are not merged into one clause-joined line, and there is no header/bullet block.
- **D-03:** An event with many items gets one line per item, each with its own link. For example, 9 failing checks produce 9 `ci.failed` lines. Do not truncate with "+N more". The user said "just do multiple lines".
- **D-04:** The PR title never appears in messages. `owner/repo#N` plus the URL is the PR's identity, so no free text authored by the PR opener reaches the agent. Third-party strings still go through the sanitizer: check names, usernames and file paths.
- **D-05:** Messages never contain comment bodies, review text or log content (DLV-05, already locked).

#### CI event semantics (diff package)
- **D-06:** ci.failed fails fast. It fires as soon as a check run concludes failed on the current head SHA, at most once per check name per head SHA. A new head SHA resets that per-check state.
- **D-07:** ci.settled fires once when every check on the head SHA has completed. It carries the overall result: passed, or failed with the failing names.
- **D-08:** If a check fails and a rerun on the same SHA then passes, nothing extra is sent. The earlier ci.failed stands, and ci.settled reports the final state. There is no `ci.recovered` event, and a check that fails again on rerun does not re-fire ci.failed for that SHA.
- **D-09:** Only check runs count. Legacy commit statuses (the `StatusContext` entries in the rollup) are ignored. Required-check awareness stays v2 (EVT2-03).

#### Reviews, comments & bots (diff + render)
- **D-10:** A submitted review produces one `review` line with the verdict, the reviewer, the number of inline comments and the review link, e.g. `review changes_requested by alice (4 inline comments) <review-url>`. Inline comments that belong to that review do not produce separate `comment` lines.
- **D-11:** Each standalone new comment (issue comment, or a thread reply that isn't part of a new review) produces one `comment` line. The line has the author, the kind (comment, or reply on <file>) and the comment's own link.
- **D-12:** In v1, bots are treated exactly like humans: same lines, same rules (EVT-08). Bot noise control waits for FILT-04.
- **D-13:** Edits and deletions of comments and reviews are ignored. Only newly created items fire.
- **D-14:** Activity by the gh-authenticated user is always excluded (EVT-08, already locked).

#### Spikes & toolchain
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

### Deferred Ideas (OUT OF SCOPE)
None. The discussion stayed within the phase scope. These were noted as already v2 in REQUIREMENTS.md: bot policy (FILT-04), required checks (EVT2-03), and notify-on-edit (not planned).
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| EVT-01 | `ci.failed` on current head, names + links, flaky rerun does not duplicate | Diff memory (`FailedReported` set per head SHA) in Pattern 2. Conclusion mapping table. Check-key rule (Pitfall 3). Rerun dedupe by newest `databaseId` |
| EVT-02 | `ci.settled` once per head SHA, passed / failed with names | Settled rule with a two-snapshot confirmation (Pitfall 2). Zero-check rule. Render format |
| EVT-03 | `review` with verdict, reviewer, link | `reviews(last:)` query fields. "Real review" vs reply-wrapper classification (Pitfall 4) |
| EVT-04 | `comment` for issue comments, review comments, thread replies, grouped under the review | Comment model in Pattern 3. `replyTo` and `pullRequestReview` fields verified in the schema |
| EVT-05 | `commits` on a head push, including force-push | `headRefOid` change plus `HeadRefForcePushedEvent` and ancestry check (Pitfall 6) |
| EVT-06 | `conflict` on and off, UNKNOWN never emits | `MergeableState` enum verified. Memory keeps the last resolved value (Pitfall 5) |
| EVT-07 | `closed` merged / closed | `state` / `merged` / `mergedBy` / `ClosedEvent.actor` fields verified |
| EVT-08 | Own activity never delivered, bots == humans | Pure `filter.ExcludeSelf`. Compare only `__typename == "User"` actors. Commit authorship heuristic (Pitfall 7) |
| DLV-05 | Terse: tag, identity, what changed, names, URLs, no bodies | Line grammar in Code Examples. Query never fetches comment bodies. Review `body` is reduced to a `HasBody bool` in the decoder |
| DLV-06 | Every PR-derived string sanitized + capped | `sanitize` design built on `unicode.IsPrint`, stripping Cf (bidi, zero-width, tag chars), variation selectors and invalid UTF-8. URL host allow-list. Hostile corpus + fuzz |
</phase_requirements>

## Project Constraints (from CLAUDE.md)

- Go only. The release is a single static binary (`CGO_ENABLED=0`). Go is a **dev-only** dependency.
- macOS and Linux are both first-class. CI must run on both.
- Reuse `gh` auth only, with no other token setup. Phase 1 uses `gh` only for fixture capture and repo creation.
- The design must stay agent-agnostic. Nothing may be specific to Claude Code.
- Testing: stdlib `testing` plus `httptest`. Golden-file tests for message formatting ("the terse messages are the product"). Inject the clock or use `testing/synctest` if time is needed. **Never send prompts to real panes in CI.**
- Avoid go-github, githubv4, viper, zap/zerolog, kardianos/service and httpcache.
- GSD workflow: file changes go through GSD commands. The executor runs under `/gsd-execute-phase`.
- Keep `diff` and `render` free of I/O and of clock dependencies (CONTEXT integration points).
- Spike answers are recorded as YES / NO / UNKNOWN plus evidence, not as narrative.

## Summary

Phase 1 has three independent tracks. They share one hard prerequisite: **neither Go nor `gh` is installed on this machine** (verified: `command -v go` and `command -v gh` both fail, and `~/.config/gh` does not exist). Wave 0 must therefore run `brew install go gh` (Go 1.27.3 and gh 2.103.3 bottles are available) and then a **human checkpoint for `gh auth login`**, which is interactive. Repo creation (D-17) and fixture capture (D-18) both depend on `gh`.

The pure core needs **no third-party Go modules at all**. Use the stdlib (`encoding/json`, `unicode`, `unicode/utf8`, `net/url`, `testing`, and native fuzzing), so the Package Legitimacy Gate has nothing to audit in this phase. The central design insight: a two-snapshot `diff(prev, next)` **cannot** satisfy D-06/D-08 on its own. Take the sequence fail, rerun (pending), fail again: the second failure looks like a fresh transition between the last two snapshots. The diff therefore has to be a pure **state-machine step**, `Step(mem Memory, next Snapshot) (events []Event, mem' Memory)`, where `Memory` holds per-SHA "already reported" sets, the last *resolved* mergeable value and the seen-ID sets. Phase 2 persists `Memory` next to the snapshot. Decode GraphQL JSON into a normalized `model.Snapshot` in a separate pure `ghquery` package. Its query lives in a `.graphql` file that is both `//go:embed`-ed and used by the fixture capture script, so the fixtures always match the production query shape.

For the spikes, the single biggest safety finding is that **this executor runs inside the user's default herdr session (pane `w1:p3`, alongside other live agent panes `w1:p1`, `w2:p1`, `w2:p3`)**. `herdr server stop` on the default session would kill the executor and the user's other agents. Every spike must run in a **dedicated named session** (`prspike`, with its socket at `~/.config/herdr/sessions/prspike/herdr.sock`), started by the human in a separate terminal window. A new spike question also comes out of D-02: what `agent.prompt` does with embedded `\n` in a multi-line coalesced prompt (one submission, or one per line?), and what the size limit is (macOS `MAX_CANON` = 1024 bytes, verified in the SDK header). Render must therefore return `[]string` lines and leave joining to Phase 3.

**Primary recommendation:** Bootstrap with `brew install go gh` and a `gh auth login` checkpoint. Use a stdlib-only Go module (`go 1.26` directive, CI on go stable plus oldstable, on ubuntu-latest and macos-latest). Build `model`, `ghquery` (decode), `diff` (pure step with Memory), `filter` (self-exclusion), `sanitize` and `render` (returns lines). Drive them with scenario sequences of real fixtures captured by a `gh api graphql -F query=@internal/ghquery/pr.graphql` loop script. Run all herdr spikes in a named `prspike` session with `#`-prefixed payloads.

## Architectural Responsibility Map

The phase is a single-process library. The tiers here are the internal layers of the daemon.

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| GraphQL query text + JSON decode into a normalized snapshot | `internal/ghquery` (pure decode) | Phase 2 `ghclient` (transport) | The query shape is shared by the fixtures and production. Decoding is pure and testable with fixtures |
| PR state model, event types, stable event keys | `internal/model` | — | Pure data. Consumed by diff, render, and the store/outbox later |
| Change detection (CI, reviews, comments, commits, conflict, closed) | `internal/diff` | Phase 2 store (persists Memory) | Pure state-machine step. No I/O, no clock |
| Own-activity exclusion | `internal/filter` | Phase 2/3 router | Kept separate from diff so the snapshot stays user-agnostic and FILT-01 (`--include-own`) can toggle it later |
| String neutralization + caps | `internal/sanitize` | render | Security boundary. Every interpolated field passes through it |
| Message lines | `internal/render` | Phase 3 delivery (joins lines, flood guard, coalescing) | Render returns `[]string`. Joining depends on the spike outcome for multi-line `agent.prompt` |
| Spike probes | `spikes/` (scripts + one Go probe) | Human checkpoints | Throwaway, but kept in the repo as evidence |
| Build/test automation | GitHub Actions | `go vet`, golangci-lint | CI on macOS and Linux (success criterion 1) |

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| Go toolchain | 1.27.3 local (via `brew install go`), `go 1.26` directive | Language | 1.27.3 and 1.26.10 are the two supported releases [VERIFIED: go.dev/dl JSON]. Setting the directive to the oldest supported release follows CLAUDE.md ("oldest release you actually test") |
| stdlib `encoding/json` | — | Decode the GraphQL fixtures | No dependency |
| stdlib `unicode`, `unicode/utf8` | Unicode 17.0.0 tables | Sanitizer | `IsPrint`, `Cf`, `Zl`, `Zp`, `Co`, `Variation_Selector`, `Bidi_Control` and `Join_Control` are all exported [CITED: pkg.go.dev/unicode] |
| stdlib `net/url` | — | URL allow-list validation | No dependency |
| stdlib `testing` (+ `testing.F` fuzzing), `flag` for `-update` | — | Table, golden and fuzz tests | CLAUDE.md says the stdlib is enough |
| stdlib `embed` | — | Embed `pr.graphql` | One query file shared by the capture script and the code |

### Supporting (dev tools, not Go modules)
| Tool | Version | Purpose | When to Use |
|------|---------|---------|-------------|
| gh CLI | 2.103.3 (brew bottle) [VERIFIED: brew info gh; GitHub releases API] | Repo creation, fixture capture via `gh api graphql` | Wave 0. **Not installed** at the moment |
| jq | installed at `/opt/homebrew/bin/jq` [VERIFIED] | Normalize fixtures and dedupe identical captures | Capture script |
| golangci-lint | v2.15.0 (2026-10-16) [VERIFIED: GitHub releases API] | Lint in CI | Via `golangci/golangci-lint-action@v9` (v9.3.0). Needs no local install |
| actions/setup-go | v7.0.0 [VERIFIED: GitHub releases API] | CI Go install | `go-version-file: go.mod` plus `stable` |
| actions/checkout | v7.0.1 [VERIFIED: GitHub releases API] | CI | — |
| herdr | 0.9.5, protocol 22 [VERIFIED: `herdr --version`, schema] | Spikes only | Already installed at `~/.local/bin/herdr` |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| stdlib assertions | testify v1.12.1 | Saves boilerplate, but adds a module and a legitimacy-audit item for no real gain in a pure-core phase |
| stdlib only | golang.org/x/text NFKC normalization | It would fold homoglyphs and full-width characters. That is not needed: the length cap plus IsPrint already neutralizes control. Revisit if spoofing becomes a concern |
| Bash + gh capture | Go capture tool | Bash is faster to write and needs only gh and jq. The Go version would duplicate Phase 2's ghclient |

**Installation:**
```bash
brew install go gh          # dev machine only (D-15)
gh auth login               # HUMAN CHECKPOINT (interactive; choose github.com, HTTPS, browser)
go version && gh auth status
```

## Package Legitimacy Audit

This phase installs **no third-party Go modules** (stdlib only). It installs only these Homebrew formulae on the dev machine, which come from the official homebrew-core tap and are not registry-installed packages: `go` and `gh`. GitHub Actions used: `actions/checkout`, `actions/setup-go`, `golangci/golangci-lint-action`, all official org repos with release tags verified.

| Package | Registry | Age | Downloads | Source Repo | slopcheck | Disposition |
|---------|----------|-----|-----------|-------------|-----------|-------------|
| (none: no Go modules) | — | — | — | — | not run (nothing to check) | — |

**Packages removed due to slopcheck [SLOP] verdict:** none
**Packages flagged as suspicious [SUS]:** none

If the planner adds any module (for example testify), it must run the gate first.

## Architecture Patterns

### System Architecture Diagram

```
 [fixture capture: scripts/capture.sh]          [future Phase 2 poller]
   gh api graphql -F query=@pr.graphql              same pr.graphql (embed)
              |                                            |
              v                                            v
     testdata/scenarios/<name>/NN-*.json  ----->  ghquery.Decode(json) ---> model.Snapshot
                                                                             |  (normalized: head SHA,
                                                                             |   checks for head only,
                                                                             |   reviews, comments,
                                                                             |   force-pushes, mergeable,
                                                                             |   state; no bodies)
                                                                             v
                                  model.Memory (prev) ------------> diff.Step(mem, snap)
                                         ^                                   |
                                         |  mem'                             | []model.Event (typed, stable Key,
                                         +-----------------------------------+  Actor{Login,IsBot,IsUser})
                                                                             v
                                                          filter.ExcludeSelf(events, selfLogin)
                                                                             |
                                                                             v
                                                         render.Lines(ref, events) -> []string
                                                           (every field via sanitize.Field /
                                                            sanitize.URL; one line per item)
                                                                             |
                                                                             v
                                                   golden files (tests)  |  Phase 3: join + agent.prompt
```

### Recommended Project Structure
```
go.mod                       # module github.com/<owner>/pr-relay ; go 1.26
cmd/pr-relay/main.go         # minimal: prints version (so CI builds a binary)
internal/
├── model/                   # Snapshot, Check, Review, Comment, ForcePush, Event, EventType, Memory, PRRef
├── ghquery/
│   ├── pr.graphql           # THE query (go:embed), also used by scripts/capture.sh
│   ├── query.go             # var Query string (embed)
│   └── decode.go            # Decode([]byte) (model.Snapshot, error); drops bodies -> HasBody
├── diff/                    # Step(mem, snap) ([]Event, Memory); Baseline(snap) Memory
├── filter/                  # ExcludeSelf(events, login) []Event
├── sanitize/                # Field(s, max) string; Path(s, max); Login(s); URL(raw, fallback) string
└── render/                  # Lines(ref, events) []string
testdata/                    # (or per-package testdata/)
├── scenarios/<name>/00-baseline.json, 01-*.json, ..., expected.golden
└── hostile/                 # corpus (Go table is fine too)
scripts/capture.sh           # capture loop + dedupe
spikes/
├── setsid-probe/main.go     # Go probe with SysProcAttr{Setsid:true}, heartbeat file
└── *.sh                     # herdr probes (prompt, wait, restart identity)
.github/workflows/ci.yml
.golangci.yml
.gitignore
.planning/phases/01-toolchain-spikes-pure-core/01-SPIKES.md
```
Use per-package `testdata/` (the Go convention, ignored by the build). Scenario fixtures used by the diff tests should live in `internal/diff/testdata/scenarios/`. The ghquery decode tests can read the same files through a relative path such as `../diff/testdata/...`. Alternatively, keep a top-level `testdata/` and use relative paths. Pick one and document it.

### Pattern 1: One query file, two consumers
**What:** `internal/ghquery/pr.graphql` is the single source of truth. `scripts/capture.sh` runs `gh api graphql -F owner=O -F repo=R -F number=N -F query=@internal/ghquery/pr.graphql`, and `query.go` does `//go:embed pr.graphql`.
**Why:** Fixtures always decode with the production decoder. A change to the query forces a recapture or makes the decode tests fail.
**Note:** gh's `-F` reads `@file` values and types integers. All fields other than `query` and `operationName` become GraphQL variables [CITED: cli.github.com/manual/gh_api].

### Pattern 2: Diff as a pure state-machine step with explicit Memory
**What:**
```go
// package model
type Memory struct {
    Baselined        bool
    HeadSHA          string
    FailedReported   map[string]bool // check key -> ci.failed already emitted for HeadSHA
    SettledReported  bool            // ci.settled emitted for HeadSHA
    SettleCandidate  string          // fingerprint of the all-complete check set seen last step ("" = none)
    Mergeable        string          // last RESOLVED value: "MERGEABLE" | "CONFLICTING" | "" (never resolved)
    State            string          // OPEN | CLOSED | MERGED
    SeenReviews      map[string]bool // node IDs
    SeenComments     map[string]bool // node IDs (issue + review comments)
    SeenForcePushes  map[string]bool
}
// package diff
func Baseline(s model.Snapshot) model.Memory                      // silent (SUB-08 groundwork)
func Step(m model.Memory, s model.Snapshot) ([]model.Event, model.Memory)
```
**When:** Always. `Step` must be deterministic and must not mutate its input (copy the maps). Event order within one step should be fixed (for example closed, conflict, commits, ci.failed, ci.settled, review, comment, each sorted by key) so golden files are stable.
**Why:** D-06 and D-08 need memory beyond the last snapshot. Phase 2 persists `Memory` atomically with the snapshot (EVT-09). Keep the maps JSON-serializable (string keys), and consider prune rules, for example SeenComments bounded to the IDs in the current window plus N.

### Pattern 3: Comment / review classification
- **Issue comment** (`PullRequest.comments`): new ID -> `comment by <author> <url>`.
- **Review** (`PullRequest.reviews`), new ID, state not PENDING:
  - "Real review": state is APPROVED, CHANGES_REQUESTED or DISMISSED, OR the review has a body (`HasBody`), OR it contains at least one *non-reply* inline comment. Emit one `review <verdict> by <author> (N inline comments) <review-url>`. Its inline comments are marked seen and produce no lines (D-10).
  - "Reply wrapper": state is COMMENTED, has no body, and every inline comment has `replyTo != null`. Emit one `reply by <author> on <path> <comment-url>` per comment (D-11).
  - Comments that show up later on an **already-seen** review (rare) are emitted as standalone comment or reply lines.
- **[ASSUMED]** that replying in a review thread through the UI or `POST .../comments/{id}/replies` creates a new `PullRequestReview` (state COMMENTED) that wraps the single reply. **The fixture capture must confirm this.** If it is wrong (replies have no new review), the rule simplifies: any new review comment whose review ID was already seen is a reply line.
- Single-comment "Add single comment" on a new line (not a reply) also creates a COMMENTED review with one non-reply comment [ASSUMED]. Under the rule above it renders as `review commented by X (1 inline comment)`. Acceptable, but the planner may prefer the `comment` form. Decide when the fixtures are in hand.

### Pattern 4: Golden files with `-update`
```go
var update = flag.Bool("update", false, "rewrite golden files")
func golden(t *testing.T, name string, got []byte) {
    t.Helper()
    p := filepath.Join("testdata", name+".golden")
    if *update { os.WriteFile(p, got, 0o644); return }
    want, err := os.ReadFile(p); if err != nil { t.Fatal(err) }
    if !bytes.Equal(got, want) { t.Errorf("%s mismatch:\n--- got\n%s\n--- want\n%s", name, got, want) }
}
```
Run `go test ./internal/... -update` to regenerate, then review the diff in git. Never pass `-update` in CI.

### Anti-Patterns to Avoid
- **Two-snapshot-only diff:** this breaks D-08 (it re-fires ci.failed on fail, rerun, fail).
- **Rendering strings inside diff:** the diff emits typed events with data, and render owns wording (ARCHITECTURE Anti-Pattern 7).
- **Fetching comment bodies "just in case":** fixtures would then contain bodies, and later code might leak them. Fetch `body` only for reviews, and only to compute `HasBody`, which the decoder discards.
- **Truncating URLs to fit a cap:** a broken link is worse than none. Validate the URL, and fall back to a constructed PR URL if it is invalid or too long.
- **Running spikes in the default herdr session:** see Pitfall 8.
- **Joining lines in render:** the `\n` behaviour of `agent.prompt` is unknown until the spike runs. Return `[]string`.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Unicode category classification | Your own rune range lists | `unicode.IsPrint`, `unicode.In(r, unicode.Cf, unicode.Variation_Selector, ...)` | The tables track Unicode 17. Hand-made lists miss tag characters, new format chars, etc. |
| UTF-8 validation | Byte loops | `utf8.ValidString` / `for _, r := range s` (yields `utf8.RuneError` for invalid bytes) | Correct handling of invalid sequences |
| URL parsing | Regex | `net/url.Parse` + check `Scheme == "https"` and `Host == "github.com"` | Edge cases (userinfo, `@`, ports, unicode hosts) |
| Fuzzing | Random string generators | `func FuzzX(f *testing.F)` (native since Go 1.18) | The seed corpus runs as regular tests in CI |
| GraphQL fixtures | Hand-written JSON | Real `gh api graphql` captures (D-18) | Hand-written JSON encodes our assumptions, which is exactly what the fixtures must test |
| Daemon detach in the setsid probe | `nohup`, or the `setsid` binary (absent on macOS) | Go `exec.Cmd` with `SysProcAttr: &syscall.SysProcAttr{Setsid: true}` | This is what the real daemon will use, so the probe tests the real mechanism. `setsid(1)` does not exist on macOS [VERIFIED: `command -v setsid` fails] |

**Key insight:** in this phase the risk is semantic rather than technical. The tests must be driven by *real* GitHub output, because the dangerous assumptions (rerun representation, reply-wraps-review, Bot login format, lazy mergeable) are about GitHub's behaviour, not about our code.

## Runtime State Inventory

Not applicable. This is a greenfield phase with no rename or migration. One note on external state the phase *creates*: a dummy GitHub repo with scratch PRs, a public `pr-relay` repo, and a named herdr session `prspike`. The plan should end with a cleanup checklist: `herdr session stop prspike && herdr session delete prspike`, and keep or archive the dummy repo.

## Common Pitfalls

### Pitfall 1: Conclusion mapping and the "failed" set
**What goes wrong:** GitHub has 9 `CheckConclusionState` values: ACTION_REQUIRED, CANCELLED, FAILURE, NEUTRAL, SKIPPED, STALE, STARTUP_FAILURE, SUCCESS, TIMED_OUT. It also has 6 `CheckStatusState` values: COMPLETED, IN_PROGRESS, PENDING, QUEUED, REQUESTED, WAITING [VERIFIED: GitHub GraphQL schema]. A run is "completed" only when `status == COMPLETED`. `conclusion` is null until then.
**How to avoid:** Use one exhaustive mapping function with a test for every enum value.
- Failed: FAILURE, TIMED_OUT, STARTUP_FAILURE, ACTION_REQUIRED.
- Neutral (completed, not failed): SUCCESS, NEUTRAL, SKIPPED, CANCELLED, STALE.
- Any unrecognized future value counts as neutral, and the decoder records it so a test can flag it.
- Note that ACTION_REQUIRED is often a fork-approval gate, not a code failure. Classifying it as failed is the CONTEXT recommendation and is acceptable. It needs a distinct render word only if the user asks.
**Warning signs:** a `ci.settled: passed` line while a check is still `WAITING` (deployment approval). WAITING is not COMPLETED, so the run counts as pending.

### Pitfall 2: Premature `ci.settled` (late-registered workflows)
**What goes wrong:** Workflow A completes before workflow B's check runs are even created (B is queued behind concurrency, or a third-party app posts late). At that moment "every check completed" is true and settled fires early. Then B fails.
**How to avoid:** Require **confirmation on two consecutive snapshots** with an identical fingerprint of the complete check set (the sorted keys plus newest run IDs). `Memory.SettleCandidate` holds the fingerprint, and settled fires on the second matching observation. This costs one poll interval of latency (about 1 min in the hot tier), stays pure, and needs no clock. **Zero check runs:** never emit settled, because "no CI configured" cannot be told apart from "CI not registered yet". [ASSUMED rule. Confirm with the user if desired.]
**Rerun after settle (open question):** D-07 says "fires once". If the user reruns a failed job after settled has fired and it then passes, D-07 as written sends nothing more. See Open Questions Q1.

### Pitfall 3: Check identity and rerun duplicates
**What goes wrong:** Two workflows can both have a job named `build`, and a rerun can show up as an additional CheckRun with the same name. Whether `statusCheckRollup.contexts` lists only the latest run per name or every attempt is **[ASSUMED] unknown**, so the fixture must show it (a flaky-rerun scenario).
**How to avoid:**
- Check key = `workflowName + "/" + name` when `checkSuite.workflowRun.workflow.name` is present, otherwise `appSlug + "/" + name`. Within one key, keep the run with the highest `databaseId` (newest).
- Filter to runs whose suite commit equals `headRefOid`. `PullRequest.statusCheckRollup` is the head commit's rollup. Fetch `statusCheckRollup.commit.oid` and assert it equals `headRefOid`. On a mismatch, treat CI as unknown for this step.
- Render shows the check `name` only. Show `workflow / name` only when two keys share a name.
**Note:** `WorkflowRun.runAttempt` exists in the schema [VERIFIED] and is useful as evidence in the fixtures.

### Pitfall 4: Reviews and comments: PENDING, replies, pagination windows
- Never emit on `PENDING` reviews. They are visible only to their author, which is normally the viewer.
- DISMISSED reviews: a review that is newly *created* as dismissed cannot happen. A dismissal changes the state of an existing ID, which counts as an edit, and D-13 ignores edits.
- Window gaps: `reviews(last: 20)`, `comments(last: 30)`. If more items than the window arrive between polls, some are missed. Have the decoder carry `TotalCount`, and if `TotalCount` grows by more than the number of new IDs seen, have diff set a `Gap` flag on its output (no event). Phase 2 then paginates. Phase 1 only needs the flag and a test.
- Null author (deleted user, "ghost"): `author` is null in GraphQL. The decoder maps it to the login `ghost`, which is not a User and never equals self.

### Pitfall 5: `mergeable` UNKNOWN and baseline adoption
**What goes wrong:** MERGEABLE, then UNKNOWN, then CONFLICTING looks like two transitions to a naive diff. `MergeableState` = CONFLICTING | MERGEABLE | UNKNOWN [VERIFIED].
**How to avoid:**
- UNKNOWN never updates `Memory.Mergeable` and never emits.
- Emit `conflict` only on a change between the two *resolved* values.
- If `Memory.Mergeable == ""` (the baseline was UNKNOWN), adopt the first resolved value silently. It is baseline completion, not a change. [ASSUMED policy. Open Question Q3.]
- Never derive conflict from `mergeStateStatus`. DIRTY does correlate, but BEHIND, BLOCKED and UNSTABLE flap.
- Event key: `conflict:<headSHA>:<baseRefOid>:<value>`. Base pushes can toggle conflict without a head change.

### Pitfall 6: Force-push and commits detection
**How to avoid:**
- Emit `commits` whenever `headRefOid` changes, and reset all CI memory (FailedReported, SettledReported, SettleCandidate).
- Force-push is detected when a new `HeadRefForcePushedEvent` ID appears in `timelineItems(last:10, itemTypes:[HEAD_REF_FORCE_PUSHED_EVENT, ...])` (fields `actor`, `beforeCommit`, `afterCommit`, `createdAt` [VERIFIED]). As a fallback, the old head is not among `commits(last: N)` OIDs.
- New-commit count = the position of the old head in the `commits(last:N)` list. If it is not found and the push is not a force-push, the count is "N+" (unknown; render "new commits").
- Several pushes between polls produce one `commits` event for the latest head. That is expected.

### Pitfall 7: Self-exclusion is subtly wrong
- Compare `actor.login == self` **only when `actor.__typename == "User"`**. In GraphQL, a `Bot` login is reported without the `[bot]` suffix (for example `github-actions`) [ASSUMED. Fixture will show]. Type-gating avoids any accidental collision and treats all bots like other humans (EVT-08).
- `viewerDidAuthor` exists on PullRequestReview, IssueComment and PullRequestReviewComment [VERIFIED schema]. It is a robust cross-check, but it bakes the viewer into the snapshot. Keep login comparison as the rule (snapshot stays user-agnostic) and optionally assert agreement in tests.
- Commits have no "pusher" in GraphQL except on force-push events (`actor`). Rule:
  - Suppress `commits` if the force-push actor is self.
  - For a non-force push, suppress if **every** new commit's `author.user.login` (or `committer.user.login`) is self.
  - If authorship is unknown (null user, for example an unlinked email), deliver. A false notification is safer than a silent miss.
- A head change by self still **resets CI memory**, and the following CI events are delivered. CI results are not "activity by the user".
- `closed` by self: open question Q2. The recommendation is to deliver it, since it is terminal and LIFE-01 cleanup keys off it.

### Pitfall 8: Spikes in the live herdr session (CRITICAL, safety)
**What goes wrong:** The executor runs in default-session pane `w1:p3`, and other live agents run in `w1:p1`, `w2:p1` and `w2:p3` [VERIFIED: `herdr agent list`]. A restart spike using `herdr server stop` would kill the executor itself and the user's other agents. A mistyped target would prompt a real agent.
**How to avoid:**
- The human starts a named session in a **separate terminal window** (not inside a herdr pane): `herdr session attach prspike`, or `herdr --session prspike`. Confirm with `herdr session list` that its socket is `~/.config/herdr/sessions/prspike/herdr.sock` [CITED: herdr socket-api docs, named session socket path]. Whether `session attach` creates a session that doesn't exist yet is undocumented, so the checkpoint should try it.
- Every probe uses `HERDR_SOCKET_PATH=$HOME/.config/herdr/sessions/prspike/herdr.sock herdr ...`, or `herdr --session prspike ...`. A guard in each spike script aborts unless the socket path contains `/sessions/prspike/` and the target pane is listed in that session.
- Stop and restart only with `herdr session stop prspike`. Named sessions are independent servers [CITED: persistence-remote docs].
- Payload text always starts with `# ` (for example `# pr-relay spike 3`), so text that lands in a shell is a harmless comment.

### Pitfall 9: Multi-line prompts and size (new spike item)
**What goes wrong:** D-02 puts several lines into one prompt. `agent.prompt` "submits text as if typed". An embedded `\n` might be sent as Enter, which would submit line 1 early or answer a dialog. The docs also say "An oversized encoded line, including Enter, is rejected in full" [CITED: socket-api docs]. The macOS canonical input limit is `MAX_CANON 1024` bytes [VERIFIED: SDK `sys/syslimits.h`]. Agent TUIs usually use raw mode, so whether this limit applies to `agent.prompt` is unknown.
**How to avoid:** Add spike S6, which covers multi-line and size: send 2-line and 30-line `#`-prefixed payloads, and payloads of 900 B, 1100 B, 4 KB and 16 KB, to a scratch agent. Record whether the result is one submission, split submissions, or a rejection. Render returns lines, and Phase 3 decides how to join them and sets the flood ceiling from this measurement.

### Pitfall 10: Check-name and path strings are attacker-controlled
**What goes wrong:** For a fork PR, the workflow file (and therefore the job names) can come from the PR head, and file paths come from the PR diff. The PR opener chooses both. Check `detailsUrl` is set by the check's app, and third-party apps can point it anywhere.
**How to avoid:** Run all names and paths through `sanitize.Field` / `sanitize.Path`. Accept URLs only when `https://github.com/...`. Otherwise fall back to `CheckRun.url` (a github.com permalink), and then to `https://github.com/O/R/pull/N/checks`. Build commit and compare URLs ourselves from hex-validated SHAs.

## Code Examples

### GraphQL query (draft for `internal/ghquery/pr.graphql`)
Field names below were verified against GitHub's published schema (`docs.github.com/public/fpt/schema.docs.graphql`, fetched 2026-10-31). The window sizes are starting points, and spike S4 measures the cost.
```graphql
query PRState($owner: String!, $repo: String!, $number: Int!) {
  rateLimit { cost limit remaining used resetAt nodeCount }
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      id number url state merged closed
      mergedBy { __typename login }
      headRefOid baseRefOid mergeable
      commits(last: 30) {
        totalCount
        nodes { commit { oid author { user { login } } committer { user { login } } } }
      }
      statusCheckRollup {
        commit { oid }
        contexts(first: 100) {
          totalCount
          pageInfo { hasNextPage endCursor }
          nodes {
            __typename
            ... on CheckRun {
              id databaseId name status conclusion url detailsUrl startedAt completedAt
              checkSuite { app { slug } workflowRun { runAttempt workflow { name } } }
            }
          }
        }
      }
      reviews(last: 20) {
        totalCount
        nodes {
          id state url submittedAt body
          author { __typename login }
          comments(first: 30) {
            totalCount
            nodes { id url path replyTo { id } author { __typename login } }
          }
        }
      }
      comments(last: 30) {
        totalCount
        nodes { id url createdAt author { __typename login } }
      }
      timelineItems(last: 10, itemTypes: [HEAD_REF_FORCE_PUSHED_EVENT, CLOSED_EVENT]) {
        nodes {
          __typename
          ... on HeadRefForcePushedEvent { id createdAt actor { __typename login } beforeCommit { oid } afterCommit { oid } }
          ... on ClosedEvent { id createdAt actor { __typename login } }
        }
      }
    }
  }
}
```
Notes:
- `StatusContext` nodes are deliberately not selected (D-09). `__typename` still lets the decoder skip them.
- The review `body` is fetched only to derive `HasBody`. Do not keep it.
- `rateLimit(dryRun: true)` exists in the schema [VERIFIED] and lets spike S4 measure cost without spending it.

### Capture loop (scripts/capture.sh, sketch)
```bash
#!/usr/bin/env bash
# usage: scripts/capture.sh owner repo number outdir [interval_s]
set -euo pipefail
o=$1 r=$2 n=$3 out=$4 iv=${5:-10}; mkdir -p "$out"; i=0; last=""
while :; do
  j=$(gh api graphql -F owner="$o" -F repo="$r" -F number="$n" -F query=@internal/ghquery/pr.graphql)
  h=$(jq -S 'del(.data.rateLimit)' <<<"$j" | shasum | cut -c1-12)
  if [[ $h != "$last" ]]; then printf '%s' "$j" | jq -S . > "$out/$(printf %02d $i).json"; i=$((i+1)); last=$h; fi
  sleep "$iv"
done
```
The executor then curates each scenario directory: it renames files to `NN-<what>.json` and trims the baseline. Run `gh` against the *dummy* repo only.

### Sanitizer (sketch)
```go
// Field neutralizes a third-party string for single-line interpolation.
func Field(s string, maxRunes int) string {
    var b strings.Builder
    prevSpace := false
    n := 0
    for _, r := range s { // invalid UTF-8 yields utf8.RuneError
        switch {
        case r == utf8.RuneError:
            continue
        case unicode.IsSpace(r): // \t \n \r \v \f, U+0085, U+00A0, U+2028/2029, Zs ...
            if !prevSpace && b.Len() > 0 { b.WriteByte(' '); n++ }
            prevSpace = true
            continue
        case !unicode.IsPrint(r), // drops Cc (incl. ESC, C1 CSI U+009B), Cf (bidi, ZW*, BOM, tag chars), Co, Cs
            unicode.Is(unicode.Variation_Selector, r): // VS1-256 ("emoji smuggling")
            continue
        }
        if n >= maxRunes { return strings.TrimRight(b.String(), " ") + "…" }
        b.WriteRune(r); n++; prevSpace = false
    }
    out := strings.TrimRight(b.String(), " ")
    return neutralizeTag(out) // case-insensitive "[pr-relay]" -> "(pr-relay)"
}
```
- Recommended caps: login 40 (GitHub's max is 39), check name 80, path 100 (keep the **tail** with a leading "…", because the filename matters), verdict and enum words come from a fixed table and are never interpolated raw.
- Also cap the whole line, for example at 512 bytes, as a defence in depth. URLs are validated, never truncated.
- Empty after sanitizing: use the `?` placeholder.
- Combining-mark floods (Mn are printable) are bounded by the rune cap. Optionally also cap Mn per base rune (say 2).
- **Do not strip `[`/`]` in general**, because bot logins render as `name[bot]`. Neutralize only the literal tag.
- ESC is removed, so any CSI or OSC tail (`[2J`, `]0;title`) is left as harmless printable text. That is acceptable, since it can no longer act as a control sequence.

### Line grammar (render)
From the user-approved examples (CONTEXT specifics), with the items left to discretion filled in. Each item is one line.
```
[pr-relay] o/r#N ci.failed: <check> <check-url>
[pr-relay] o/r#N ci.settled: passed (<k> checks) https://github.com/o/r/pull/N/checks
[pr-relay] o/r#N ci.settled: failed (<f> of <k>): <name>, <name> https://github.com/o/r/pull/N/checks
[pr-relay] o/r#N review approved by alice <review-url>
[pr-relay] o/r#N review changes_requested by alice (4 inline comments) <review-url>
[pr-relay] o/r#N review commented by bob (1 inline comment) <review-url>
[pr-relay] o/r#N comment by bob <comment-url>
[pr-relay] o/r#N reply by carol on src/x.go <comment-url>
[pr-relay] o/r#N commits: 3 new, head abc1234 https://github.com/o/r/pull/N/commits
[pr-relay] o/r#N commits: force-pushed, head abc1234 (was def5678) https://github.com/o/r/compare/def5678...abc1234
[pr-relay] o/r#N conflict: conflicting https://github.com/o/r/pull/N
[pr-relay] o/r#N conflict: cleared https://github.com/o/r/pull/N
[pr-relay] o/r#N closed: merged by alice https://github.com/o/r/pull/N
[pr-relay] o/r#N closed: closed by bob https://github.com/o/r/pull/N
```
- Bot authors render as `login[bot]`. This is identity, not policy, and it keeps D-12 equal treatment.
- In the `ci.settled: failed` line, include names only while the line stays within the cap. Otherwise drop the list and keep the counts. The individual `ci.failed` lines already named every check (D-03). This is a size safeguard, not a "+N more".
- Owner and repo come from the PRRef (validated to `[A-Za-z0-9._-]`), never from free text.

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| golangci-lint v1 config | v2 config (`version: "2"`, `linters.default: standard`, separate `formatters`) | golangci-lint v2 (2025) | Use the v2 format with action v9 [CITED: golangci-lint.run/docs/configuration/file] |
| Polling `mergeable` until non-null (REST) | GraphQL `MergeableState.UNKNOWN` treated as no information | — | Same semantics, encoded as an enum |
| Strip only C0 controls | Also strip Cf (Unicode tag "ASCII smuggling", bidi), variation selectors | 2024-2025 prompt-injection research | Covered by `!IsPrint` plus Variation_Selector |

**Deprecated/outdated:** the CLAUDE.md STACK claims an older Go is "latest". As of now, Go 1.27.3 and 1.26.10 are the supported releases [VERIFIED].

## Spike Plan (for 01-SPIKES.md)

Each spike records: **Question -> Method (exact commands) -> Answer (YES / NO / UNKNOWN) -> Evidence (transcript excerpt) -> Implication for Phase 2/3.** All herdr spikes run in session `prspike`.

| # | Question | Method | Human checkpoint? |
|---|----------|--------|-------------------|
| S0 | Setup: does the named scratch session exist with its own socket? | The human runs `herdr session attach prspike` in a separate terminal window and creates 2 to 3 panes. In one pane they start an agent, preferably a cheap one or a Claude session the user doesn't mind. Leave one pane as a bare shell. The executor checks `herdr session list` and runs `agent list` against the prspike socket | Yes |
| S1 | Typing collision: what does `agent.prompt` do when the human has half-typed input? | The human types `hello` without pressing Enter in the scratch agent. The executor sends `herdr agent prompt <p> "# spike S1"`. Record whether the text concatenates, the draft is submitted, the draft is replaced, or it is rejected. Repeat in the bare shell pane (status unknown) to record the response code | Yes |
| S2a | `agent.wait` while blocked: does `wait --until idle,done` keep waiting through `blocked`, and what does the default `until` (idle/done/blocked) return? | Put the scratch agent into a permission prompt (blocked). Run `agent wait <p> --until idle --until done --timeout 60000`. Resolve the prompt and observe whether the wait returns. Also run with no `--until` and confirm it returns immediately with blocked. The CLI help states the default matches idle/done/blocked [VERIFIED] | Yes (trigger the permission prompt) |
| S2b | `agent.prompt` `wait` option: success shape, `agent_blocked` while blocked, and `agent_prompt_stalled` on an unknown or bare-shell pane | Run `herdr agent prompt <p> "# spike" --wait --until working --timeout 15000` on idle, on blocked, and on the bare shell. Capture the raw JSON (use the socket directly, or the CLI JSON output) | Partly |
| S3 | Do `pane_id`, `terminal_id` and `agent_session.value` survive `herdr session stop prspike` plus re-attach? | Record `agent list` before, stop and re-attach, record after. Indirect evidence: `session.json` persists `public_pane_numbers` and `next_public_pane_number` but **no terminal ids** [VERIFIED by reading `~/.config/herdr/session.json` keys], and the docs say processes do not survive a stop [CITED: session-state docs]. Prediction: pane_id is stable, terminal_id changes, agent_session.value is stable for agents with native restore. **Implication:** if terminal_id always changes on restart, LIFE-02's terminal_id guard would cancel every subscription on a herdr restart. `agent_session.value` may be the better identity guard. Record this for Phase 3 | Yes |
| S4 | GraphQL shape and cost | Run the draft query with `rateLimit(dryRun:true)` and for real against a scratch PR. Then build a 5-PR and a 20-PR aliased batch. Record `cost`, `nodeCount` and latency. Confirm the rerun representation in `contexts` and the reply-review behaviour once fixtures exist | No (needs gh auth) |
| S5 | Does a setsid child survive herdr stopping? | Build `spikes/setsid-probe` (it re-execs itself with `Setsid:true` and stdio to a file, and the child writes a heartbeat each second). Launch it from a prspike pane, then `herdr session stop prspike`, and check whether the heartbeat continues. Optionally repeat with a linked plugin `[[startup]]` if cheap; otherwise defer to Phase 5 | Yes (stop/restart) |
| S6 | Multi-line and size: does `\n` in `agent.prompt` text produce one submission? What is the largest accepted payload? | Send `#`-prefixed payloads of 2 and 30 lines, and of 900 B, 1100 B, 4 KB and 16 KB, to the scratch agent. Record whether each is one submission, split submissions, or rejected (error code). Repeat with the bare shell for comparison | Observation only |
| S7 (cheap) | Is `agent.prompt` accepted on `done` the same as on `idle`? | Prompt after a turn completes, before focusing the pane | No |

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | A thread reply creates a new COMMENTED PullRequestReview wrapping the reply | Pattern 3 | The classification rule changes. A fixture confirms it, low cost |
| A2 | Whether `statusCheckRollup.contexts` lists every rerun attempt or only the latest is unknown | Pitfall 3 | The dedupe rule (newest databaseId per key) handles both cases. The fixture confirms |
| A3 | GraphQL `Bot.login` lacks the `[bot]` suffix | Pitfall 7 / render | Render could print `name[bot][bot]`. The fixture confirms, and render should strip a trailing `[bot]` before appending |
| A4 | Two-snapshot confirmation is an acceptable settle latency | Pitfall 2 | Adds one poll interval. The user may prefer immediate settle |
| A5 | Zero check runs means no ci.settled | Pitfall 2 | A repo with no CI never gets settled, which is likely desired |
| A6 | A first resolved mergeable after an UNKNOWN baseline is adopted silently | Pitfall 5 | The agent may miss a conflict that existed at subscribe time (Phase 4's `sub` can print current state) |
| A7 | A self-merge or self-close still emits `closed` | Pitfall 7 | Conflicts with a literal reading of EVT-08. User confirmation needed (Q2) |
| A8 | GitHub Actions' `GITHUB_TOKEN` can post comments, reviews (including approve, if the repo setting "Allow GitHub Actions to create and approve pull requests" is enabled) and replies, giving `github-actions[bot]` fixtures without a second human account | Fixture strategy | If not, the user needs a second account (D-18 already plans for asking) |
| A9 | `herdr session attach prspike` creates a new named session | Spike S0 | Fallback: `herdr --session prspike` |
| A10 | Field caps of 40/80/100 runes and a 512-byte line cap | Sanitizer | Purely tunable |

## Open Questions

1. **Q1: Should `ci.settled` re-arm after a rerun on the same SHA?** (D-07 vs real workflows)
   - What we know: D-07 says once, and D-08 covers a rerun *before* settle.
   - Unclear: the case where settled(failed) has fired and the user reruns the failed job, which then passes.
   - Recommendation: re-arm when the check set returns to pending after settling, and emit a second settled only if the overall result differs (key `ci.settled:<sha>:<n>`). Ask the user at plan review; if they decline, keep it strictly once.
2. **Q2: Is `closed` excluded when the viewer merged or closed the PR?** Recommendation: always deliver `closed`, since it is terminal and drives LIFE-01. EVT-08 lists "comments, reviews, and commits".
3. **Q3: Adopt the baseline mergeable silently?** Recommendation: yes, consistent with SUB-08.
4. **Q4: Which second actor will generate review and comment fixtures?** Option A: a workflow_dispatch "actor" workflow in the dummy repo that uses `gh pr review` / `gh pr comment` / `gh api .../comments/{id}/replies` with `GITHUB_TOKEN` (produces `github-actions[bot]`; needs the repo setting for approvals). Option B: a second human account. A plus B gives the best coverage (bot and human). This must be a checkpoint question (D-18).
5. **Q5: Repo owner and name for the module path** (`github.com/<owner>/pr-relay`). Needed before `go mod init`. This belongs to the D-17 checkpoint. The git user email is `drodimin@gmail.com`, and the GitHub login is unknown until `gh auth login`.

## Fixture Scenario Design (dummy repo)

Suggested dummy-repo workflow (`.github/workflows/ci.yml` in the dummy repo, not in pr-relay):
- `lint`: always passes.
- `test`: fails if the file `FAIL` exists in the tree. This drives fail and pass by commit.
- `flaky`: `if [ "${{ github.run_attempt }}" = "1" ]; then exit 1; fi`. It fails first and passes on "Re-run failed jobs", which gives the rerun fixture.
- `slow`: `sleep 600`. Cancel it from the UI to get the CANCELLED fixture.
- Optional second workflow with a job also named `build`, to cover the key-collision case.
- Actor workflow (`workflow_dispatch` with inputs `pr`, `action`) for bot reviews, comments and replies (Q4).

Scenarios become directories of ordered snapshots: `ci-fail-pass`, `ci-flaky-rerun`, `ci-cancelled`, `review-approved`, `review-changes-requested-inline`, `review-commented`, `issue-comment`, `thread-reply`, `push`, `force-push`, `conflict-on-off` (push a conflicting change to base, then resolve), `mergeable-unknown` (capture immediately after a base push), `merged`, `closed`, `bot-authors`, `own-activity` (the user's own comment, review on another PR, and push). Capture with `scripts/capture.sh` at a 5 to 10 s interval while the human performs the actions.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go | Everything | ✗ | — (brew offers 1.27.3) | `brew install go` (Wave 0) |
| gh CLI + auth | Repo creation, fixtures, S4 | ✗ (no binary, no `~/.config/gh`) | — (brew offers 2.103.3) | `brew install gh` + **human** `gh auth login` |
| Homebrew | Installs | ✓ | /opt/homebrew | — |
| git | Repo | ✓ | 2.50.1 | — |
| jq | Capture script | ✓ | /opt/homebrew/bin/jq | — |
| herdr | Spikes | ✓ | 0.9.5, protocol 22 | — |
| `setsid` binary | (not needed) | ✗ on macOS | — | Go probe with `SysProcAttr.Setsid` |
| golangci-lint (local) | Optional | ✗ | — | Run in CI only, via action v9 |
| A second GitHub actor | Review/comment fixtures | unknown | — | Actions bot workflow (A8) or a second account |

**Missing dependencies with no fallback:** none. All are installable, but `gh auth login` and the second actor need the human.
**Missing dependencies with fallback:** Go, gh (brew); setsid (Go probe); golangci-lint (CI action).

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` (table tests, golden files, native fuzz seed corpus). Go 1.27.3 local, CI on `go.mod` version + stable |
| Config file | none. Wave 0 creates `go.mod`, `.golangci.yml` and `.github/workflows/ci.yml` |
| Quick run command | `go test ./internal/...` |
| Full suite command | `go vet ./... && go test -race -count=1 ./...` (CI also runs golangci-lint) |
| Golden regenerate | `go test ./internal/render ./internal/diff -update` (local only, review the git diff) |
| Fuzz (local, optional) | `go test ./internal/sanitize -fuzz=FuzzField -fuzztime=30s` |

### Phase Requirements -> Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| EVT-01 | One ci.failed per (key, SHA); fail, rerun, fail does not re-fire; a new SHA resets | scenario + unit | `go test ./internal/diff -run 'TestScenarios/ci-(fail-pass\|flaky-rerun)\|TestCIFailedOncePerSHA'` | ❌ Wave 0 |
| EVT-01 | Conclusion mapping covers all 9 enum values | unit | `go test ./internal/diff -run TestConclusionMapping` | ❌ |
| EVT-02 | Settled once, after 2-snapshot confirmation; passed / failed with names; zero checks gives none; cancelled is neutral | scenario + unit | `go test ./internal/diff -run 'TestScenarios/ci-\|TestSettled'` | ❌ |
| EVT-03 | Review per verdict, PENDING ignored, inline comments grouped | scenario | `go test ./internal/diff -run 'TestScenarios/review-'` | ❌ |
| EVT-04 | Issue comment and reply lines; inline comments of a new review do not duplicate; edits silent | scenario + unit | `go test ./internal/diff -run 'TestScenarios/(issue-comment\|thread-reply)\|TestEditsIgnored'` | ❌ |
| EVT-05 | Push gives one commits event with a count; force-push detected; CI memory reset | scenario | `go test ./internal/diff -run 'TestScenarios/(push\|force-push)'` | ❌ |
| EVT-06 | UNKNOWN emits nothing; conflict on and off; baseline adoption | scenario + unit | `go test ./internal/diff -run 'TestScenarios/(conflict-on-off\|mergeable-unknown)\|TestMergeableUnknown'` | ❌ |
| EVT-07 | merged / closed emitted once | scenario | `go test ./internal/diff -run 'TestScenarios/(merged\|closed)'` | ❌ |
| EVT-08 | Self (User type) excluded for comments, reviews and commits; bots and others kept; a Bot with the same login string is not excluded | unit + scenario | `go test ./internal/filter ./internal/diff -run 'TestExcludeSelf\|TestScenarios/(own-activity\|bot-authors)'` | ❌ |
| DLV-05 | Every line matches the grammar regex; no body text is present (assert that the fixture review bodies never appear in output) | golden + property | `go test ./internal/render -run 'TestGolden\|TestNoBodies\|TestLineGrammar'` | ❌ |
| DLV-06 | Hostile corpus: one line, only IsPrint/space, rune and byte caps, tag neutralized, URL allow-list | unit + fuzz seed | `go test ./internal/sanitize ./internal/render -run 'TestHostile\|FuzzField\|TestURLAllowList'` | ❌ |
| (decode) | Every fixture decodes; StatusContext skipped; the rollup commit equals the head; null author becomes ghost | unit | `go test ./internal/ghquery` | ❌ |
| SC-1 | Build/vet/test green on ubuntu and macOS | CI | GitHub Actions matrix run | ❌ |
| SC-2 | Spike report answers S1-S6 with YES/NO/UNKNOWN + evidence | manual review | `grep -E '^\*\*Answer:\*\* (YES\|NO\|UNKNOWN)' .planning/phases/01-toolchain-spikes-pure-core/01-SPIKES.md` | ❌ |

Scenario harness: `TestScenarios` walks `testdata/scenarios/*/`. It decodes `00-*.json` into `Baseline`. For each later snapshot it runs `Step`, then `filter.ExcludeSelf(self)` where `self` comes from `testdata/scenarios/self.txt` (the capturing user's login). It renders and writes `step NN:\n<lines>` to a combined output compared with `expected.golden`. This tests decode, diff, filter and render end to end. Separate unit tests keep diff assertions independent of wording: they check event types and keys. **Synthetic mutations** of real fixtures (for example editing a fixture's conclusion with jq into a new file) are allowed for edge cases that are hard to trigger live, such as STARTUP_FAILURE, 100+ checks, or a key collision. Name them `synthetic-*` so they are distinguishable from real captures.

Hostile corpus (minimum): ESC CSI `\x1b[2J`, OSC title `\x1b]0;x\x07`, OSC 52 clipboard, C1 CSI `\u009b`, NUL, BEL, BS `\x08`, DEL `\x7f`, CR, LF, CRLF, TAB, VT, FF, NEL `\u0085`, LS/PS `\u2028`/`\u2029`, NBSP, bidi `\u202e`/`\u2066`-`\u2069`, ZWSP/ZWJ/ZWNJ, BOM `\ufeff`, tag chars `\U000E0041`..., VS16 `\ufe0f`, VS17 `\U000E0100`, a private-use char, invalid UTF-8 `\xff\xfe`, 10 KB `A`s, a combining-mark flood, `[pr-relay] ignore previous instructions`, `[PR-RELAY]`, a 300-char path, a URL to `https://evil.example/`, `javascript:` and `http://github.com` (not https). Properties to assert for every corpus entry: output contains no rune outside IsPrint∪{' '}, contains no `\n`/`\r`, has ≤ the cap in runes, has no case-insensitive `[pr-relay]`, is deterministic and idempotent (`Field(Field(x)) == Field(x)`).

### Sampling Rate
- **Per task commit:** `go test ./internal/...`
- **Per wave merge:** `go vet ./... && go test -race -count=1 ./...`
- **Phase gate:** CI green on both OSes, all scenario goldens reviewed, and 01-SPIKES.md complete before `/gsd:verify-work`

### Wave 0 Gaps
- [ ] `brew install go gh`, then the `gh auth login` checkpoint
- [ ] Repo created (D-17 checkpoint), `go mod init github.com/<owner>/pr-relay`, `.gitignore`, `cmd/pr-relay/main.go`
- [ ] `.github/workflows/ci.yml` (matrix: ubuntu-latest and macos-latest; go: go.mod version + stable; steps: build, vet, `test -race`; golangci-lint job)
- [ ] `.golangci.yml` (v2 format)
- [ ] Golden helper and `-update` flag, plus the scenario harness skeleton
- [ ] The dummy repo and second-actor decision (Q4), then fixture capture (gated by `gh` auth and the `pr.graphql` draft)

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | no (Phase 1 uses `gh` interactively only, and the daemon auth is Phase 2) | — |
| V3 Session Management | no | — |
| V4 Access Control | no | — |
| V5 Input Validation / Output Encoding | **yes (core of DLV-06)** | `sanitize` package (IsPrint allow-list, Cf/VS strip, caps, tag neutralization), URL allow-list, hex-validated SHAs, `[A-Za-z0-9._-]` owner/repo |
| V6 Cryptography | no | — |
| V14 Configuration (CI) | yes (minor) | Pin action major versions. `permissions: contents: read` in the CI workflow |

### Known Threat Patterns for this stack

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Prompt injection via check names, paths or logins | Tampering / Elevation | No bodies or titles (D-04/05). Per-field sanitize + caps. Fixed envelope tag. Literal tag neutralized inside fields |
| Terminal escape injection (CSI/OSC, including OSC 52 clipboard write) | Tampering | Strip all Cc (C0 and C1) |
| Invisible-text smuggling (Unicode tags, variation selectors, zero-width, bidi) | Spoofing / Tampering | Strip Cf + Variation_Selector |
| Malicious link via third-party `detailsUrl` | Spoofing | https://github.com host allow-list, with a fallback to a constructed URL |
| Early submission via newline in a coalesced prompt | Tampering | Sanitize removes newlines in fields. Joining between lines is decided by spike S6 (Phase 3) |
| Spike prompts landing in a real agent or shell | Tampering | Named `prspike` session guard, `#`-prefixed payloads, never the default session |
| Leaking review/comment bodies via fixtures in a public repo | Information disclosure | The dummy repo holds harmless content. The query fetches no comment bodies, and review `body` is fixture-only (no secrets on the dummy repo). Optionally blank `body` with jq during curation, keeping only emptiness: replace it with `"x"` or `""` |

## Sources

### Primary (HIGH confidence)
- GitHub GraphQL public schema (`https://docs.github.com/public/fpt/schema.docs.graphql`, fetched 2026-10-31): enums CheckConclusionState, CheckStatusState, MergeableState, PullRequestReviewState, PullRequestState, MergeStateStatus. Fields of PullRequest, CheckRun, CheckSuite, WorkflowRun (runAttempt), PullRequestReview, PullRequestReviewComment (replyTo, pullRequestReview, path), IssueComment, HeadRefForcePushedEvent, RateLimit, the `rateLimit(dryRun:)` argument and the StatusCheckRollupContext union.
- `herdr api schema --json` (protocol 22): AgentPromptParams (`target`, `text`, `wait{until,timeout_ms}`), AgentWaitParams, AgentStatus enum, AgentInfo fields (terminal_id, agent_session, focused, revision, completion_seq).
- `herdr agent prompt --help` / `agent wait --help` (agent_blocked, agent_prompt_stalled 5000 ms, default until values).
- `herdr agent list`, `herdr session list`, `~/.config/herdr/session.json` key structure.
- go.dev/dl JSON (go1.27.3, go1.26.10). GitHub releases API: setup-go v7.0.0, checkout v7.0.1, golangci-lint v2.15.0, golangci-lint-action v9.3.0, cli/cli v2.103.3. `brew info go/gh`.
- macOS SDK `sys/syslimits.h`: `MAX_CANON 1024`.
- pkg.go.dev/unicode: IsPrint semantics, exported tables, Unicode 17.0.0.

### Secondary (MEDIUM confidence)
- herdr docs v0.9.5: socket-api.mdx (named session socket path, oversized-line rejection, pane.moved, events_lost), session-state.mdx (processes do not survive a stop, layout restore), persistence-remote.mdx (`herdr session stop <name>`).
- cli.github.com/manual/gh_api (`-F` @file, GraphQL variables).
- golangci-lint.run configuration docs (v2 format).

### Tertiary (LOW confidence, to be validated by fixtures/spikes)
- Reply-creates-review, rerun representation, Bot login format, Actions bot review capability (A1-A3, A8).

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH. Stdlib only, and tool versions verified.
- Architecture: HIGH for the package split and the Memory-based step; MEDIUM for the review/reply classification (pending fixtures).
- Pitfalls: HIGH for the herdr safety and schema enums; MEDIUM for GitHub runtime behaviours.

**Research date:** 2026-10-31
**Valid until:** 2026-11-30 (herdr moves quickly, so re-check `herdr --version` and the protocol before the spikes)
