---
status: partial
phase: 05-packaging-plugin-agent-skill
source: [05-CONTEXT.md D-22]
started: 2026-11-07T22:40:36Z
updated: 2026-11-07T22:40:36Z
---

## Current Test

[awaiting human testing]

## Preconditions (set up by plan 05-11 Task 1)

- The UAT build is at `~/.local/bin/pr-relay` and prints `pr-relay v0.1.0-uat`. It was built from HEAD 9d5868b.
- The daemon is stopped: `pgrep -f "pr-relay daemon" | wc -l` prints 0.
- The plugin is linked: `herdr plugin list` shows `pr-relay (pr-relay) enabled [local:/Users/drodi/repos/pr-relay/herdr-plugin]`.
- Homebrew was dropped for v0.1.0, so this UAT has no brew, cask or tap steps. The install paths are `go install` and the GitHub release download, and plan 05-12 checks both.
- Note: `pr-relay status`, `ls` and `sub` auto-start the daemon. In test 1, check the plugin log and `pgrep` **before** you run any `pr-relay` command. Otherwise the CLI auto-start hides whether herdr ran the hook.

## Tests

### 1. herdr start runs the plugin [[startup]] hook (D-08)
steps: Run `herdr server stop`, then start herdr again. Before running any `pr-relay` command, run `herdr plugin log list --plugin pr-relay --limit 5` and `pgrep -fl "pr-relay daemon"`. Then run `pr-relay status`.
expected: The newest plugin log entry is the startup command `["sh","run.sh","daemon","--ensure"]` with exit_code 0 and stdout `daemon running (pid N, v0.1.0-uat)`. `pgrep` shows one `pr-relay daemon` process with that same pid, and it is already running before any `pr-relay` CLI call. `pr-relay status` shows `daemon: running (version v0.1.0-uat, pid N, ...)`.
result: [pending]

### 2. Status and List-all actions are readable in the TUI (D-09)
steps: Trigger `pr-relay.status` and `pr-relay.list-all`. Herdr has no plugin menu, so use either a `[[keys.command]] type = "plugin_action"` keybinding in your own herdr config or `herdr plugin action invoke pr-relay.status` / `herdr plugin action invoke pr-relay.list-all`.
expected: Each action opens a popup pane. The status popup shows the `daemon: running (version v0.1.0-uat ...)` block, and the list popup shows the subscriptions or `no subscriptions`. Both end with `press Enter to close`, and Enter closes the popup.
result: [pending]

### 3. Skill install
steps: Run `herdr plugin action invoke pr-relay.install-skill`, or `pr-relay skill install` in a pane. Then start an agent in a new pane and ask it what pr-relay can do.
expected: `~/.agents/skills/pr-relay/SKILL.md` exists, and so does `~/.claude/skills/pr-relay/SKILL.md` because `~/.claude` exists. The agent answers from the skill: one non-blocking `pr-relay sub <url>`, updates arrive as `[pr-relay]` lines, no polling.
result: [pending]

### 4. Real sub and real update in an agent pane (also 04-HUMAN-UAT test 1)
steps: In the agent pane from test 3, have the agent run `pr-relay sub https://github.com/OWNER/REPO/pull/N` on a real PR you control, such as the Phase 1 dummy repo. From a **different** GitHub account, add a comment to the PR. pr-relay excludes your own activity, so a comment from your own account is filtered out by design. Leave the agent idle for a few minutes. After the first poll, run `pr-relay status` in another pane. Finally, have the agent run `pr-relay unsub https://github.com/OWNER/REPO/pull/N`.
expected: `sub` returns within a few seconds. Within a few minutes a `[pr-relay] OWNER/REPO#N comment by <login> ...` line is typed into that agent pane. After the first poll, the `auth:` line in `pr-relay status` no longer says `unknown (gh)` and shows the resolved login. `unsub` succeeds, and `pr-relay ls --all` no longer lists the PR.
result: [pending]

## Summary

total: 4
passed: 0
issues: 0
pending: 4
skipped: 0
blocked: 0

## Gaps
