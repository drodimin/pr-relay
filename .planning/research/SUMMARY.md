# Project Research Summary

**Project:** pr-relay
**Domain:** Local dev-tooling daemon. It watches GitHub PRs and pushes terse updates into coding-agent panes via herdr.
**Researched:** 2026-10-31
**Confidence:** MEDIUM. herdr facts are HIGH (local schema and docs). GitHub behaviour and rate-limit numbers are MEDIUM (training knowledge). Several runtime behaviours need spikes.

## Executive Summary

pr-relay is a single-binary Go daemon with a thin CLI. One poller per machine watches the deduplicated set of subscribed PRs. A pure diff engine turns GitHub state changes into typed events. A router applies per-subscription filters: event mask, own-user exclusion, and bot policy. A per-pane delivery worker waits until the agent is idle, coalesces pending events into one terse message, and injects it with herdr `agent.prompt`. The prior art all polls or blocks inside the agent's own loop. Pushing into the pane is the differentiator, and a correct, low-noise event model is the product.

The design keeps a pure core with impure edges:
- **Pure core first.** Build `model`, `diff`, `render` and `sanitize` as pure Go, tested against fixtures.
- **Durable state** sits behind a narrow `Store` interface with a transactional outbox, so a snapshot and its events commit together.
- **Unreliable edges.** GitHub and herdr are both treated as unreliable. Polling never depends on herdr being up, and herdr events are wake-ups, never state.

Stack:
- go-gh for auth only
- hand-rolled HTTP and GraphQL for polling
- an in-repo herdr NDJSON client
- a Unix-socket NDJSON control protocol between CLI and daemon
- cobra for the CLI
- GoReleaser and a Homebrew tap for release

Key risks:
1. **Prompt injection.** The relay types into an agent that has a shell. So messages carry no third-party free text, and every interpolated string is sanitized and capped.
2. **Typing collision and delivery races.** An idle agent does not guarantee an empty input box, and `agent.prompt` has no idempotency key.
3. **Event noise.** CI flapping, lazily computed `mergeable`, four comment object types, force-pushes, and bot spam.
4. **GraphQL has no ETags.** "ETags + batching" must become a concrete hybrid plan, with secondary-limit handling from day one.
5. **Pane identity.** Pane IDs may be reused, so a stale subscription could type into a bare shell.

## Resolved Conflicts

| Question | Decision | Rationale |
|---|---|---|
| Persistence: JSON (STACK) vs SQLite (ARCH) | **One JSON state file written atomically** (temp file, fsync, rename). It holds subscriptions, snapshots, ETags and the outbox in one document, behind a `Store` interface. SQLite is the documented escape hatch. | SQLite's advantage is atomically committing snapshot and outbox together. A single file replaced by rename gives the same all-or-nothing guarantee, because both live in one document. The data is tiny (tens of PRs; IDs and hashes, no bodies) and single-process. JSON avoids ~5–10 MB of binary, the modernc init and RSS cost, and schema migrations. It is also inspectable in bug reports. Switch to SQLite if state grows past ~1 MB, if rewrite cost shows up in profiles, or if event history becomes a feature. |
| Lifecycle owner | **CLI auto-start is primary.** It uses a flock around check-and-spawn, then re-execs `pr-relay daemon` with `Setsid` and sends stdio to the log. **`[[startup]]` is a convenience hook** that runs `pr-relay daemon --ensure`, which spawns the daemon if absent and exits. | The herdr docs describe `[[startup]]` as one-shot and unsupervised, and it does not run on attach or enable. All starters go through one `daemon` entry point guarded by a flock held for the daemon's lifetime. Use one canonical XDG state dir, never `HERDR_PLUGIN_STATE_DIR`. No launchd or systemd units in v1, only recipes. The daemon exits after N hours with no subscriptions and no herdr. |
| Multi-herdr-session | **Supported in the v1 data model.** The subscription key is `(herdr_socket_path, pane_id)`, with an endpoint manager keyed by socket path. | The CLI already has `HERDR_SOCKET_PATH` in its environment, so capturing it costs nothing. Retrofitting a session dimension into persisted state, workers and reconnect logic later is painful. A single global socket would silently break named sessions. |
| Delivery gating | A per-pane worker loop. Call `agent.get`; if the agent is idle or done, send. If it is working or blocked, call `agent.wait` until idle or done, with a timeout and a slow safety-net ticker. An `agent_blocked` result from `agent.prompt` is the authoritative signal to keep the message queued. Never send to a pane in `unknown` state. One global `pane.closed` subscription runs on its own connection. | The architecture loop and the pitfalls point ("`agent.prompt` is the only atomic check") agree once `agent.get` is treated as an optimization. Evaluate `agent.prompt`'s `wait` option in a spike. |

## Key Findings

### Recommended Stack (see STACK.md)
Go 1.27.x, built as one static binary with `CGO_ENABLED=0`, using the stdlib where possible. Go is **not installed** on the dev machine yet.

- **cobra v1.10.2** for the CLI: `sub`, `unsub`, `ls`, `status`, `daemon`, `doctor`, `version`.
- **cli/go-gh/v2 v2.17.0** only for `auth.TokenForHost`, re-resolved on a 401.
  - Pin the version, and test it against the macOS keyring and the Linux config file.
  - Polling uses plain `net/http` with our own ETag, rate-limit and GraphQL transport.
  - Never shell out to `gh api` on each poll.
- **stdlib `net` + NDJSON** for both the CLI-daemon protocol and the herdr client, sharing one `jsonline` package.
- **JSON state file** with atomic replace, behind a `Store` interface.
- **Supporting libraries:** BurntSushi/toml (config), gofrs/flock (single-instance lock), lumberjack (log rotation) and `log/slog`.
- **Release:** GoReleaser v2.19, a Homebrew tap, and GitHub Actions.
- **Avoid:** go-github, githubv4, viper, kardianos/service, zap, zerolog, httpcache.

Tests:
- a fake GitHub server (`httptest`)
- a fake herdr server on a temp Unix socket
- an injectable clock or `synctest`
- golden files for rendered messages
- a hostile-string corpus for the sanitizer

### Expected Features (see FEATURES.md)

**Must have:**
- **Daemon and CLI:** one daemon with per-PR dedup; idempotent, non-blocking `sub`, `unsub` and `ls`; pane taken from `HERDR_PANE_ID` with a `--pane` override; CLI auto-starts the daemon.
- **Events:**
  - CI settled per head SHA (failed with check names, or all green)
  - review verdicts
  - review and issue comments
  - merge conflicts
  - new commits by others
  - merged or closed, with auto-unsubscribe
- **Filtering:** own-activity exclusion with `--include-own`, plus a bot policy.
- **State:** a baseline snapshot taken on subscribe; subscriptions and cursors persisted across restarts.
- **Delivery:** queue until the agent is idle; coalesce per pane; terse messages tagged `[pr-relay]` with the PR identity in every line; clean up on `pane.closed`.
- **Operations:** rate-limit safety, adaptive polling, reuse of `gh` auth, a `status` command and logs.
- **Distribution:** skill snippet, plugin manifest, README and install instructions.

**Should have:**
- a settle window for flaky checks
- `--once-on ci`
- event presets and `--only-failures`
- toast notifications
- sidebar metadata
- inferring the PR from the current branch
- `doctor`
- an events preview
- actionable hints in messages

**Defer:**
- webhooks
- watching for review requests
- awareness of required checks
- auto-merge and draft events
- subscription TTL
- GitHub Enterprise Server
- the REST ETag pre-probe, until measured
- comment bodies in messages

**Anti-features:**
- a blocking `wait`
- interrupting working or blocked agents
- per-check-run events
- comment bodies or log tails in messages
- delivery targets other than herdr
- a TUI
- agent orchestration

### Architecture Approach (see ARCHITECTURE.md)
One daemon owns all state. Data flows through these stages:

1. The Poller runs batched GraphQL queries and builds a `PRState`.
2. The pure Differ compares it with the persisted snapshot and emits typed Events.
3. The Router applies filters: event mask, own-user, bot policy, and a created-after cut-off.
4. Events go into the durable Outbox.
5. A per-pane Delivery Worker gates on agent status, coalesces, renders, and calls `agent.prompt`.
6. The herdr Endpoint Manager holds the connection to each herdr socket.

- **Atomic commit and at-least-once delivery.** The snapshot and the outbox commit together, so delivery is at-least-once, using stable event keys per subscription.
- **Snapshots stay user-agnostic.** Own-user filtering happens in the router.
- **Render at delivery time.** A CI result that has been superseded is never sent stale.

Components (`internal/`):
1. `model`, `diff`, `render`, `sanitize`: pure.
2. `store`, `registry`: the atomic state file, subscription CRUD, the watch set, snapshots and the outbox.
3. `ghclient`, `poller`:
   - auth and batched GraphQL
   - rate-limit accounting
   - hot, warm and cold polling tiers
   - one global scheduler with jitter and a concurrency cap, scheduling by due time rather than tickers
4. `herdr`: the NDJSON client and endpoint manager, reconnect, and subscribe-then-snapshot recovery.
5. `route`, `outbox`, `deliver`: filters, the durable queue, per-pane workers, coalescing, TTLs and caps.
6. `ctl` and CLI: version handshake, flock singleton, and `Setsid` auto-start.

### Critical Pitfalls (see PITFALLS.md, 28 total)
1. **Prompt injection via PR-controlled strings.** This is a security requirement.
   - Send no comment bodies in v1.
   - Sanitize and cap every interpolated string: strip control characters, ESC and newlines, and cap at about 80 characters.
   - Wrap messages in a fixed `[pr-relay]` envelope.
   - Reduce comments from external authors to counts, using `authorAssociation`.
   - Test against a hostile-string corpus.
2. **Typing collision, TOCTOU races and duplicates.**
   - Spike on a scratch pane first.
   - `agent_blocked` is the normal "keep queued" path.
   - Never blindly retry after `agent_prompt_stalled`.
   - Never prompt panes in `unknown` state.
   - Use per-pane delivery locks, plus a maximum-delay ceiling with a toast fallback.
3. **Noisy or wrong events.**
   - Emit CI events only on transitions of the settled rollup per head SHA.
   - Never store or emit `mergeable: UNKNOWN`.
   - Key seen-state by object ID, not timestamp.
   - Group inline comments under their review.
   - Track only `headRefOid` for new commits.
   - Apply a bot policy.
4. **Rate limits.** GraphQL has no ETags.
   - One global scheduler with a concurrency cap of 1 to 4.
   - Batch per repo, with small `first:` and `last:` windows.
   - Log `rateLimit.cost`, and honour `Retry-After` exactly.
   - Stretch intervals when under 10% of budget remains.
   - Stagger the first polls after a restart or laptop wake.
5. **Subscription identity and lifecycle.**
   - Store `terminal_id` and validate it before each delivery.
   - Handle `pane.closed` and `pane.moved`.
   - Reconcile via `session.snapshot` on reconnect.
   - Hold a flock for the daemon's lifetime, re-exec with `Setsid`, and do a CLI-daemon version handshake.
   - Use a short socket path in a 0700 directory, because macOS limits socket paths to 104 bytes.

Two more:
- **Token environment.** It differs by launch path, so resolve the token inside the daemon and re-resolve on a 401.
- **Validation in `sub`.** `sub` validates the PR with a short synchronous timeout and must never block indefinitely.

## Implications for Roadmap

Reconciled build order:
1. Correctness contracts: the pure core, then durable state.
2. The two independent unreliable edges: GitHub and herdr.
3. The process shell that makes it work end-to-end.
4. Extras.
5. Packaging.

The sanitizer and the spikes belong in Phase 1.

### Phase 1: Spikes + Pure Core (model, diff, render, sanitize)
**Rationale:** It has no external dependencies, carries the highest correctness and security risk, and its spikes gate later design decisions.

**Delivers:**
- Go toolchain, repo skeleton, and CI on macOS and Linux.
- `PRState`, `Event`, `diff` and `render`, with golden tests built from real `gh api graphql` fixtures.
- The sanitizer, tested against a hostile corpus.
- Event semantics:
  - CI based on the rollup, covering settled state, flaps and a per-SHA reset
  - `mergeable: UNKNOWN` treated as no information
  - ID-keyed seen sets
  - comments grouped under their review
- Own-user and bot filters as pure functions.
- Written results from these spikes, run on scratch panes only:
  - typing collision with `agent.prompt`
  - `agent.wait` while blocked, and `agent.prompt`'s `wait` option
  - whether pane IDs and `terminal_id` survive a herdr restart
  - GraphQL query shape and cost
  - whether a `setsid` child survives herdr stopping
  - the macOS prompt size limit

**Avoids:** pitfalls 1, 2, 6–11, 21, 22, 25.

### Phase 2: Durable State + GitHub Poller (headless)
**Rationale:** It needs the model from Phase 1, and gives a poller that can be measured before anything depends on it.

**Delivers:**
- **Store:**
  - an atomic JSON store with `schema_version`
  - registry dedup
  - snapshot and outbox committed synchronously
  - kill -9 tests checking for duplicates after restart
- **`ghclient`:** the go-gh token, re-resolving on a 401, `viewer { login }`, and parsing GraphQL `errors`.
- **Poller:**
  - batched aliased GraphQL queries
  - one global scheduler with tiers, jitter and a concurrency cap
  - `rateLimit` accounting, plus `Retry-After` and secondary-limit backoff
  - re-polling `mergeable` while it is unknown
  - a request counter

**Avoids:** pitfalls 5, 12, 13, 23, 27. Recommendation: GraphQL only in v1, and defer the REST conditional pre-probe until it can be measured.

### Phase 3: Herdr Client + Delivery
**Rationale:** It can run in parallel with Phase 2. It must come before the CLI, because delivery is the riskiest runtime piece.

**Delivers:**
- **herdr client:**
  - an NDJSON client and an endpoint manager per socket path
  - reconnect, recovery from `events_lost`, and subscribe-then-snapshot reconcile
  - a fake herdr server for tests
- **Per-pane delivery workers:**
  - status gate and `agent.wait`, with a safety-net ticker
  - retry on `agent_blocked`, and no sends to `unknown` panes
- **Coalescing:**
  - severity ordering, a quiet window, and a line cap with "+N more"
  - outbox TTLs and caps
- **Rendering and validation:**
  - render at delivery time
  - validate `terminal_id`
  - clean up on `pane.closed` and `pane.moved`
  - crash-recovery tests

**Avoids:** pitfalls 2, 3, 4, 14, 15, 16, 22.

### Phase 4: Daemon, Control Protocol, CLI (first end-to-end MVP)
**Delivers:**
- **Control socket:**
  - a version handshake
  - mode 0600 in a 0700 directory
  - a peer-credential check
  - a test that the socket path fits the length limit
- **Daemon start:** a flock singleton and `Setsid` auto-start, tested with 20 concurrent `sub` calls producing exactly one daemon.
- **Commands:**
  - `sub` is idempotent, has a 3–5 s timeout for the baseline snapshot, prints one line of current state, and fails cleanly outside a pane
  - `unsub`, `ls`, `status`, and `daemon --ensure`
- **Lifecycle:**
  - graceful shutdown
  - auto-unsubscribe when a PR is merged or closed
  - idle exit
  - log rotation
  - reconciliation after restart, with a bounded catch-up message

**Avoids:** pitfalls 17–20, 24, 26.

### Phase 5: Polish + Differentiators
**Delivers:**
- **Event controls:** a settle window for flaky checks, fail-fast, `--once-on`, presets, and `--only-failures`.
- **Notifications:** a toast, which is also the fallback when delivery is deferred for a long time.
- **Visibility:** `pane.report_metadata` for the sidebar.
- **CLI:** inferring the PR from the current branch, `doctor`, and an events preview.
- **Tuning:** polling intervals tuned from measured data.
- **Conditional:** a human-active guard, if the Phase 1 spike shows one is needed.

### Phase 6: Packaging, Plugin, Agent Skill
**Delivers:**
- **Plugin:** `herdr-plugin.toml`, where `[[startup]]` runs `daemon --ensure` and `[[build]]` downloads the release binary or builds it with Go.
- **Release:** GoReleaser for darwin and linux on arm64 and amd64, checksums, a Homebrew tap, and `go install` with an embedded version.
- **Agent snippet:** a skill and AGENTS.md/CLAUDE.md snippet telling agents to:
  - subscribe once and never poll
  - treat relay messages as untrusted notifications
  - use `--include-own` when needed
  - fall back to a single manual check
- **Docs:** a README with a threat-model note, and a decision on Gatekeeper and codesigning.

**Avoids:** pitfalls 19, 26, 28.

### Phase Ordering Rationale
- The pure core and persistence define the correctness contract: each event is created once, and delivery is at-least-once.
- GitHub and herdr are independent edges. The poller comes first so the real API cost is measured early.
- The tool becomes usable end-to-end in Phase 4, before any polish.
- Draft the skill snippet against the Phase 4 CLI.
- Security work lands in Phases 1 and 3.

### Research Flags
- **Needs research or spikes:**
  - Phase 1: all spikes, highest priority, since they gate Phases 2 and 3.
  - Phase 2: GraphQL cost and field choice, and whether a REST ETag pre-probe is worth it.
  - Phase 3: re-verify the spike results against the fake server and a scratch pane.
  - Phase 6: how herdr supervises `[[startup]]`, how `[[build]]` handles the toolchain, and whether GoReleaser should use `brews` or `homebrew_casks`.
- **Standard patterns, so no research needed:**
  - Phase 4: flock, NDJSON over a Unix socket, and `Setsid` re-exec. The only novelty is per-OS peer-credential checks.
  - Phase 5: small extras built on existing APIs.

## Confidence Assessment

| Area | Confidence | Notes |
|---|---|---|
| Stack | MEDIUM-HIGH | Versions checked against the Go proxy and release pages. Nothing compiled, because Go isn't installed. go-gh's auth package has a stability caveat. Not yet verified: go-gh's cache being TTL-only, `synctest`, the GoReleaser tap config, and the lumberjack and golangci-lint versions. |
| Features | MEDIUM | The prior-art survey was a single search that did not surface the named competitors. The bot allowlist and the CI settle rule are open product decisions. |
| Architecture | MEDIUM-HIGH | herdr behaviour verified from its schema, docs and a live pane. GitHub batching, query cost and tier numbers come from training knowledge. |
| Pitfalls | MEDIUM | herdr items are HIGH. The rate-limit numbers (900 and 2,000 points per minute, 500k nodes, 100 concurrent requests) must be re-verified before they are hard-coded. |

**Overall confidence:** MEDIUM. The design is consistent across all four files, but several runtime behaviours stay unverified until the Phase 1 spikes run.

### Gaps to Address
- **Typing collision with `agent.prompt`:** run the Phase 1 spike. If it collides, add a guard that detects human activity (focus and recent output), with a maximum-delay ceiling and a toast fallback.
- **Pane ID and `terminal_id` stability** across a herdr restart or handoff: run the spike. Until it's known, validate `terminal_id` before every delivery and drop the subscription on a mismatch.
- **Daemon survival when herdr stops:** design for the daemon being killed at any time. The outbox covers lost work, and the next CLI call or hook restarts it.
- **CI settle rule:** recommend reporting the settled rollup per head SHA, with optional fail-fast. Decide in requirements.
- **Default bot policy:** decide in requirements which bots pass through and which are reduced to a count.
- **Polling method:** recommend GraphQL batching with tiers in v1, rather than a hybrid with a REST ETag detector. Confirm in Phase 2 research.
- **Message size cap and envelope wording:** measure the macOS limit empirically.
- **GHES and multiple hosts:** decide in requirements between declaring github.com only in v1 and resolving hosts through go-gh.
- **PROJECT.md wording:** change "`[[startup]]` launching the daemon" to "`[[startup]]` runs `daemon --ensure`".

## Sources
**Primary (HIGH):**
- herdr 0.9.5 (protocol 22): `herdr api schema --json`, `herdr agent get`, `herdr agent prompt --help`, and the `HERDR_*` environment inside a pane
- herdr docs: https://herdr.dev/tui/docs/socket-api/ and https://herdr.dev/tui/docs/plugins/
- the Go module proxy, go.dev/dl, and the GitHub releases API
- pkg.go.dev for go-gh/v2 auth

**Secondary (MEDIUM):**
- GitHub REST and GraphQL docs, as recalled
- `gh auth token` and keyring behaviour
- agent PR-babysitting skills

**Tertiary (LOW):**
- specific rate-limit numbers
- go-gh cache internals
- `synctest` details
- GoReleaser tap config
- prior-art internals, taken from the brief
- `isRequired` under non-admin tokens

---
*Research completed: 2026-10-31. Ready for roadmap: yes.*
