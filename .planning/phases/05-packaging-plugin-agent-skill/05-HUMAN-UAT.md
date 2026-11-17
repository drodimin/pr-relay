---
status: passed
phase: 05-packaging-plugin-agent-skill
source: [05-CONTEXT.md D-22]
started: 2026-11-07T22:40:36Z
updated: 2026-11-10T21:30:00Z
---

## Current Test

[testing complete: all 4 passed, user approved 2026-11-10]

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
result: passed (2026-11-10, herdr 0.9.5). After `herdr server stop` and a restart, `herdr plugin log list --plugin pr-relay` showed the first entry plugin-log-1 = `['sh','run.sh','daemon','--ensure']`, exit 0, stdout `daemon running (pid 74536, v0.1.0-uat)`. `pgrep -fl "pr-relay daemon"` showed a single process, 74536. `pr-relay status` showed `daemon: running (version v0.1.0-uat, pid 74536, up 5m)`.

### 2. Status and List-all actions are readable in the TUI (D-09)
steps: Trigger `pr-relay.status` and `pr-relay.list-all`. Herdr has no plugin menu, so use either a `[[keys.command]] type = "plugin_action"` keybinding in your own herdr config or `herdr plugin action invoke pr-relay.status` / `herdr plugin action invoke pr-relay.list-all`.
expected: Each action opens a popup pane. The status popup shows the `daemon: running (version v0.1.0-uat ...)` block, and the list popup shows the subscriptions or `no subscriptions`. Both end with `press Enter to close`, and Enter closes the popup.
result: passed (2026-11-10). plugin-log-2 was status (`sh run.sh --open status-view`) and plugin-log-3 was list-all (`sh run.sh --open list-view`). Both exited 0. The user saw both popups with readable output and closed each with Enter.

### 3. Skill install
steps: Run `herdr plugin action invoke pr-relay.install-skill`, or `pr-relay skill install` in a pane. Then start an agent in a new pane and ask it what pr-relay can do.
expected: `~/.agents/skills/pr-relay/SKILL.md` exists, and so does `~/.claude/skills/pr-relay/SKILL.md` because `~/.claude` exists. The agent answers from the skill: one non-blocking `pr-relay sub <url>`, updates arrive as `[pr-relay]` lines, no polling.
result: passed (2026-11-10). plugin-log-4 install-skill exited 0 and installed `~/.agents/skills/pr-relay/SKILL.md` and `~/.claude/skills/pr-relay/SKILL.md`. Both are 5695 bytes and byte-identical to `skills/pr-relay/SKILL.md`. A fresh Claude Code agent started in /Users/drodi (outside the repo) was asked "what can pr-relay do?". It answered with details that only the skill contains: `--pane`, the `--events ci` alias, the keep/unsub check-in, the 24h/7d timings and the config path.

### 4. Real sub and real update in an agent pane (also 04-HUMAN-UAT test 1)
steps: In the agent pane from test 3, have the agent run `pr-relay sub https://github.com/OWNER/REPO/pull/N` on a real PR you control, such as the Phase 1 dummy repo. From a **different** GitHub account, add a comment to the PR. pr-relay excludes your own activity, so a comment from your own account is filtered out by design. Leave the agent idle for a few minutes. After the first poll, run `pr-relay status` in another pane. Finally, have the agent run `pr-relay unsub https://github.com/OWNER/REPO/pull/N`.
expected: `sub` returns within a few seconds. Within a few minutes a `[pr-relay] OWNER/REPO#N comment by <login> ...` line is typed into that agent pane. After the first poll, the `auth:` line in `pr-relay status` no longer says `unknown (gh)` and shows the resolved login. `unsub` succeeds, and `pr-relay ls --all` no longer lists the PR.
result: passed (2026-11-10). The user approved a throwaway draft PR, https://github.com/drodimin/pr-relay/pull/1. `pr-relay sub <url> --pane w1:p9` returned in 0.6s. Two lines were delivered to the idle agent pane w1:p9. First, `[pr-relay] drodimin/pr-relay#1 ci.settled: passed (5 checks) https://github.com/drodimin/pr-relay/pull/1/checks` (daemon log 21:07:54, delivered), and the agent reacted to it on its own. Second, after the second account dmitriy-medme posted a comment, `[pr-relay] drodimin/pr-relay#1 comment by dmitriy-medme https://github.com/drodimin/pr-relay/pull/1#issuecomment-6785002669` (21:15:08). After the first poll the `pr-relay status` auth line changed from `unknown (gh)` to `ok (gh)`, and the daemon log showed `github auth ok token_source=gh`. It shows the auth state rather than the login (see F2). `pr-relay unsub` succeeded, and `pr-relay ls --all` then printed `no subscriptions`. The PR was closed and its branch deleted.

## Summary

total: 4
passed: 4
issues: 0
pending: 0
skipped: 0
blocked: 0

## Gaps

None. All four tests passed.

## Notes / Findings (non-blocking, follow-ups after v0.1.0)

- **F1:** `pr-relay sub <url> --pane OTHER` prints "updates arrive in this pane" even when the target is another pane. The confirmation text should name the target pane when `--pane` differs from `$HERDR_PANE_ID`. Delivery itself went to the correct pane.
- **F2:** After auth succeeds, `pr-relay status` shows `auth: ok (gh)`. The UAT expected the authenticated login, as in `auth: ok (<login>, gh)`. Auth works. Only the display differs from what the UAT expected.

## Post-release (automated, 05-12 Task 2) — 2026-11-17

- First push of main (494a986): CI lint failed (62 golangci-lint v2.15.0 issues never run locally). Tag held. Fixed in quick task 261116-qss (795ade6, 731e224); local lint 0 issues, race tests green.
- Push main 494a986..230e75d: CI run 43987742829 — all jobs success (lint, 4x test, goreleaser).
- Tag v0.1.0 (annotated, on 230e75d) pushed; release workflow run 43988046368 success.
- Release https://github.com/drodimin/pr-relay/releases/tag/v0.1.0 — not draft, not prerelease. Assets: checksums.txt + pr-relay_0.1.0_{darwin,linux}_{amd64,arm64}.tar.gz (4 archives).
- `shasum -a 256 -c checksums.txt`: darwin_arm64 OK, linux_amd64 OK. Archives contain LICENSE, README.md, pr-relay.
- Downloaded darwin arm64 binary: `pr-relay v0.1.0`.
- `go install github.com/drodimin/pr-relay/cmd/pr-relay@v0.1.0` (proxy download): `pr-relay v0.1.0`.
- Homebrew cask checks: N/A (Homebrew dropped for v0.1.0, user decision 2026-11-07).

## Post-release (human, 05-12 Task 3) — 2026-11-17

Adapted for no Homebrew: the install path is `go install` into `~/.local/bin`.

1. **Dev copy replaced (D-06): PASS.** `pr-relay daemon stop` printed "daemon stopped". `GOBIN=$HOME/.local/bin go install github.com/drodimin/pr-relay/cmd/pr-relay@v0.1.0 && pr-relay version` printed `pr-relay v0.1.0`. Only one binary exists, `/Users/drodi/.local/bin/pr-relay`. There is none in `~/go/bin`.
2. **Plugin swap: PASS.** `herdr plugin unlink pr-relay` returned plugin_unlinked removed:true. `herdr plugin install drodimin/pr-relay/herdr-plugin --ref v0.1.0` first refused with "remote plugin install requires --yes when stdin is not interactive". With `--yes`, the preview showed id pr-relay, version 0.1.0, ref v0.1.0, commit 230e75d9aa1edda81ccd093c893b2acdb9dc9d80, actions 4, startup 1, panes 2, build 0. It then printed "Installed pr-relay from drodimin/pr-relay/herdr-plugin." `herdr plugin list` shows `pr-relay (pr-relay) enabled [github:drodimin/pr-relay/herdr-plugin@v0.1.0]`.
3. **herdr restarted by the user: PASS.**
4. **Startup hook after restart: PASS.** plugin-log-1 `sh run.sh daemon --ensure` exited 0 with stdout "daemon running (pid 13450, v0.1.0)". `pgrep -fl "pr-relay daemon"` showed a single process, `13450 /Users/drodi/.local/bin/pr-relay daemon`. `pr-relay status` showed "daemon: running (version v0.1.0, pid 13450, up 1m)".
5. **Linux (optional): not run.** The linux_amd64 archive checksum and contents were verified in Task 2.

Overall: approved. All install paths verified.

- **F3:** The README plugin install step (`herdr plugin install drodimin/pr-relay/herdr-plugin`) has no `--yes`. A remote install from a non-interactive shell, such as an agent pane or a script, is refused without it. The README should mention `--yes` for non-interactive use and consider showing `--ref v0.1.0`. This is a follow-up and the README was not edited in this close-out.
