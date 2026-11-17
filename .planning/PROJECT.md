# pr-relay

## What This Is

A local daemon + CLI that watches GitHub pull requests on behalf of many coding agents (Claude Code, Codex, or anything else) running in herdr panes, and **pushes** short, noise-filtered updates into each subscribed agent's pane. Agents subscribe with one non-blocking command and never poll, sleep, or block waiting on PR state. Built as a public tool for the herdr community from day one, shipped as a herdr plugin with an agent-facing skill snippet.

## Core Value

An agent runs `pr-relay sub https://github.com/owner/repo/pull/123` once, returns immediately, and later receives a relevant PR update (CI failure, review verdict, comment, conflict, merge) in its pane, without spending any tokens on polling. One poller serves every agent on the machine.

## Requirements

### Validated

- [x] Single local daemon per machine with one GitHub poller; subscriptions deduplicated per PR (N agents on one PR = one poll) — Validated in Phase 4: Daemon, Control Protocol & CLI (end-to-end tests against fakes; real herdr/GitHub UAT passed in Phase 5)
- [x] `pr-relay sub <PR link> [--events ...] [--pane ID]` returns immediately; the pane is auto-detected from the herdr environment; only single-PR links are accepted (no whole-repo subscriptions) — Validated in Phase 4: Daemon, Control Protocol & CLI (end-to-end tests against fakes; real herdr/GitHub UAT passed in Phase 5)
- [x] `pr-relay unsub` and `pr-relay ls` (list subscriptions) — Validated in Phase 4: Daemon, Control Protocol & CLI (end-to-end tests against fakes; real herdr/GitHub UAT passed in Phase 5)
- [x] CLI auto-starts the daemon if it isn't running — Validated in Phase 4: Daemon, Control Protocol & CLI (end-to-end tests against fakes; real herdr/GitHub UAT passed in Phase 5)
- [x] Ships as a herdr plugin (`herdr-plugin.toml` with `[[startup]]` launching the daemon) — Validated in Phase 5: startup hook proven on a real herdr restart; status/list actions open popup panes (herdr has no plugin menu)
- [x] Ships an agent-facing skill / CLAUDE.md / AGENTS.md snippet telling agents to subscribe instead of polling — Validated in Phase 5: embedded SKILL.md, `pr-relay skill` / `skill install` (~/.agents and ~/.claude); a fresh agent answered from it
- [x] Public-ready install (single binary; GitHub releases / `go install`) and README — Validated in Phase 5: v0.1.0 released 2026-11-17 with checksums; MIT-0 license; Homebrew dropped for v0.1.0 (user decision 2026-11-07)
- [x] Works on macOS and Linux — Validated in Phase 5: CI tests on both; static darwin/linux amd64+arm64 release builds (real runtime UAT on macOS only)

### Active

- [ ] Events: CI check status changes (failing check names + links); reviews submitted (approved / changes requested); new review comments and issue comments; merge-state / conflict changes; new commits pushed; PR merged / closed
- [ ] Own-activity exclusion: items authored by the gh-authenticated user are filtered out by default; per-subscription opt-in to include them
- [ ] Push delivery via herdr `agent.prompt` with terse messages (what changed + names + URLs; the agent fetches details itself)
- [ ] Queue messages while the target agent is `working` or `blocked`; deliver when it goes idle/done (via `events.subscribe` / `pane.agent_status_changed`)
- [ ] Coalesce multiple queued updates for one pane into a single message
- [ ] Optional `notification.show` toast for the human
- [ ] Auto-cleanup: drop subscriptions when the PR is merged/closed or the pane closes (`pane.closed`)
- [ ] Uses existing `gh` auth (no separate token setup)
- [ ] Efficient polling: batched GraphQL and/or REST with ETags (304s), adaptive intervals (fast while CI pending, slow when settled), backoff on low rate-limit budget
- [ ] Optional `pane.report_metadata` to show PR state in herdr sidebar

### Out of Scope

- Webhooks / `gh webhook forward` — deferred to v2; polling with ETags + adaptive intervals is enough for v1 and works without repo admin rights
- Verbose/detailed message payloads (comment bodies, log tails) — v1 messages are terse with links; agents fetch details themselves
- Per-agent identity tracking (distinguishing agent vs human actions under the same gh user) — too complex for v1; exclusion is by gh user
- Whole-repo / org subscriptions — subscriptions are per PR, by PR link only
- Windows support — herdr targets macOS/Linux
- Non-herdr delivery targets (tmux, raw terminals, MCP) — agent-agnostic within herdr is the scope
- Building on prior art (prhub, claude-gh-monitor, pr-sentinel, github-webhook-mcp) — none push into herdr panes; reviewed, not reused

## Context

- **Problem today:** each agent runs its own `sleep` + `gh pr view/checks` loop. Every poll is a tool call in the agent's context (token waste), each loop is a separate process (memory), and N agents on one PR means N pollers (rate limit).
- **herdr** is a terminal multiplexer for coding agents. Its socket API speaks newline-delimited JSON. Relevant methods: `agent.prompt` (returns `agent_blocked` if the agent is blocked), `agent.get`, `events.subscribe` (`pane.agent_status_changed`, `pane.closed`), `pane.report_metadata`, `notification.show`. Plugins use `herdr-plugin.toml` with `[[startup]]`. Docs: https://herdr.dev/tui/docs/socket-api/ and https://herdr.dev/tui/docs/cli-reference/. Exact payload schemas: `herdr api schema --json`.
- **Prior art:** prhub has a shared poller, but agents block on `prhub wait`, it ignores comments, and it doesn't push. claude-gh-monitor and pr-sentinel are per-session Claude Code watchers. github-webhook-mcp is a pull-based MCP server.
- **Known limitation:** because agents usually push and comment as the user's own gh account, default own-exclusion also hides the human's own PR comments. The per-subscription include-own flag covers that case.

## Constraints

- **Tech stack**: Go, giving a single static binary with a low memory footprint. Users install one file, which matters for a public tool. Python was considered and rejected because of the runtime dependency and a ~30-50 MB resident daemon.
- **Platform**: macOS and Linux both first-class, because herdr runs on both.
- **Auth**: Must reuse the user's existing `gh` CLI auth, so there's no extra setup.
- **Resources**: Low idle memory and CPU. It's a long-lived background daemon.
- **GitHub rate limit**: The poller must keep request volume roughly independent of subscriber count and must back off as the budget runs low.
- **Agent-agnostic**: Anything running in a herdr pane must work. Nothing may be specific to Claude Code.

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Go for daemon + CLI | Single binary install for public users, small footprint, good concurrency | — Pending |
| Push via herdr `agent.prompt` with queue-until-idle | Agents never loop or block, and delivery never interrupts a working or blocked agent | — Pending |
| Terse messages with links | Minimizes injected tokens; agents pull details only when needed | — Pending |
| Exclude own activity by gh user (opt-in include) | Simple and predictable; precise agent attribution is too complex for v1 | — Pending |
| Polling only in v1 (no webhooks) | Works without admin rights; ETags + adaptive intervals keep cost low | — Pending |
| Public from day one, shipped as herdr plugin + agent skill | Target is the herdr community, so install and docs matter in v1 | — Pending |

## Evolution

This document evolves at phase transitions and milestone boundaries.

**After each phase transition** (via `/gsd-transition`):
1. Requirements invalidated? → Move to Out of Scope with reason
2. Requirements validated? → Move to Validated with phase reference
3. New requirements emerged? → Add to Active
4. Decisions to log? → Add to Key Decisions
5. "What This Is" still accurate? → Update if drifted

**After each milestone** (via `/gsd:complete-milestone`):
1. Full review of all sections
2. Core Value check — still the right priority?
3. Audit Out of Scope — reasons still valid?
4. Update Context with current state

---
*Last updated: 2026-11-17 after Phase 5 (v0.1.0 published: GoReleaser release + go install, herdr plugin with startup hook and popup panes, embedded agent skill, README + docs drift test, MIT-0; Phase 4 criticals CR-01..03 fixed; real herdr/GitHub UAT passed. Follow-ups: F1-F3, review WR-01)*
