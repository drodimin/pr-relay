# pr-relay

**Your coding agents find out what happens on their pull requests, and they never have to poll for it.**

When an agent opens a PR, it usually waits on it in a loop: sleep, run `gh pr checks`, read the output, and repeat. Every pass costs tokens and a turn, and each agent runs its own loop against your GitHub rate limit. With pr-relay, the agent subscribes once and moves on to other work. When CI fails, a reviewer asks for changes or someone comments, a short line shows up in the agent's [herdr](https://herdr.dev) pane.

- **No polling tokens.** `pr-relay sub` returns immediately. The agent doesn't sleep or wait.
- **Only what matters.** Updates cover CI failures and results, reviews, comments, merge conflicts, new commits, and merges or closes. Your own activity is filtered out.
- **Never interrupts.** An update is typed in only after the agent has gone idle. Updates that pile up while it's busy arrive together as one batch.
- **One poller for every agent.** A single background daemon watches each PR once, however many agents subscribe to it, so it stays well within GitHub's rate limits.
- **Zero setup.** It reuses your `gh` login and works with any agent in a herdr pane: Claude Code, Codex or anything else.

What the agent sees:

```
$ pr-relay sub https://github.com/octo/repo/pull/7
subscribed octo/repo#7 events=all
polling every 1m (next in 58s)
updates arrive in this pane; unsub: pr-relay unsub https://github.com/octo/repo/pull/7

  (later, once the agent is idle)
[pr-relay] octo/repo#7 ci.failed: lint https://github.com/octo/repo/actions/runs/1/job/11
[pr-relay] octo/repo#7 review changes_requested by alice (4 inline comments) https://github.com/octo/repo/pull/7#pullrequestreview-2
```

## Quick start

You need [herdr](https://herdr.dev), a working `gh auth login` and Go.

```sh
go install github.com/drodimin/pr-relay/cmd/pr-relay@latest   # 1. the binary
herdr plugin install drodimin/pr-relay/herdr-plugin           # 2. starts the daemon with herdr
pr-relay skill install                                        # 3. teaches your agents to use it
```

Make sure `$(go env GOPATH)/bin` is on your PATH, then restart herdr (`herdr server stop`, then start it again) so it picks up the new binary.

That's all. Ask an agent to watch its PR, or run `pr-relay sub <PR link>` in its pane yourself.

No Go toolchain? See [Installation details](#installation-details) for prebuilt binaries.

## Installation details

**Prebuilt binary.** Instead of `go install`, you can download a release. From the [releases page](https://github.com/drodimin/pr-relay/releases), get `pr-relay_<version>_<os>_<arch>.tar.gz` for your system (`os` is `darwin` or `linux`, `arch` is `amd64` or `arm64`) and `checksums.txt`. Verify the archive with `shasum -a 256 -c checksums.txt --ignore-missing` on macOS or `sha256sum -c checksums.txt --ignore-missing` on Linux. Then unpack it and put `pr-relay` on your PATH. On macOS, also see the quarantine note under [Troubleshooting](#troubleshooting).

**Install only one copy.** If two different `pr-relay` binaries are on one machine, the daemon keeps restarting as one version and then the other.

**Why restart herdr.** The herdr server inherits PATH from the shell that launched it, and the plugin and your agents can only find `pr-relay` on that PATH.

**The plugin.** Its startup hook runs `pr-relay daemon --ensure` when herdr starts, so the daemon is running before any agent needs it. You can skip the plugin, because any `pr-relay` command also starts the daemon on demand.

**The skill.** `pr-relay skill install` writes `~/.agents/skills/pr-relay/SKILL.md`. It also writes `~/.claude/skills/pr-relay/SKILL.md` when `~/.claude` exists. Use `--dir DIR` to write `DIR/pr-relay/SKILL.md` instead. Some agents read an instructions file rather than skills. For them, `pr-relay skill` prints the same text, which you can paste into `AGENTS.md` or `CLAUDE.md`. Agents that load skills only at startup need a restart after you install the skill.

## Commands

Commands that act on "this pane" use `HERDR_PANE_ID`, which herdr sets inside every pane. `--pane ID` targets another pane.

| Command | What it does |
|---------|--------------|
| `pr-relay sub <PR-URL> [--events TYPE,...] [--pane ID]` | Subscribe this pane to a pull request. Returns immediately; updates arrive in the pane. `--events` limits delivery to the listed types (comma-separated, see [Events](#events)). Running `sub` again for the same PR and pane updates the subscription and resets its inactivity clock. |
| `pr-relay unsub <PR-URL> \| --all [--pane ID]` | Unsubscribe this pane from one pull request, or from all of its subscriptions with `--all`. |
| `pr-relay keep <PR-URL> [--pane ID]` | Renew a subscription after a check-in. |
| `pr-relay poll <PR-URL>` | Poll a watched pull request now and deliver anything new to its subscribers. |
| `pr-relay ls [--all] [--pane ID]` | List this pane's subscriptions. `--all` (or running outside herdr) lists every pane's. |
| `pr-relay status` | Daemon health: version, uptime, `gh` auth, rate limit, herdr delivery and recent cancellations. |
| `pr-relay reset [--yes]` | Drop every subscription on this machine. Asks for confirmation unless `--yes` is given. |
| `pr-relay daemon` | Run the daemon in the foreground. |
| `pr-relay daemon --ensure` | Start the daemon in the background if it is not already running, then exit. |
| `pr-relay daemon stop` | Stop the running daemon. |
| `pr-relay daemon restart` | Restart the daemon, or start it when none is running. |
| `pr-relay skill` | Print the agent skill text for `AGENTS.md` or `CLAUDE.md`. |
| `pr-relay skill install [--dir DIR]` | Install `SKILL.md` into `~/.agents/skills/pr-relay/` (and `~/.claude/skills/pr-relay/` when `~/.claude` exists), or into `DIR/pr-relay/` with `--dir`. |
| `pr-relay version` | Print the version. |

All output is plain text. Errors go to stderr with a non-zero exit code.

### herdr plugin actions

The plugin adds four actions: `pr-relay.status` and `pr-relay.list-all` open a popup pane that shows `pr-relay status` or `pr-relay ls --all` and closes on Enter; `pr-relay.restart` restarts the daemon and `pr-relay.install-skill` runs `pr-relay skill install`, both in the background. Run any of them from a shell with `herdr plugin action invoke pr-relay.status`.

herdr 0.9.5 has no plugin menu, and plugins cannot declare keybindings, so to trigger the actions from the TUI add keybindings to your own `~/.config/herdr/config.toml`:

```toml
[[keys.command]]
key = "prefix+alt+s"
type = "plugin_action"
command = "pr-relay.status"
description = "pr-relay status"

[[keys.command]]
key = "prefix+alt+l"
type = "plugin_action"
command = "pr-relay.list-all"
description = "pr-relay subscriptions"
```

The `description` is shown in herdr's `prefix+?` help panel.

## Events

Each update is one line, at most 512 bytes, tagged `[pr-relay]` and ending in a link.

| Type | Meaning | Example |
|------|---------|---------|
| `closed` | The pull request was merged or closed. Its subscriptions end after this line. | `[pr-relay] octo/repo#7 closed: merged by alice https://github.com/octo/repo/pull/7` |
| `conflict` | The pull request started or stopped conflicting with its base branch. | `[pr-relay] octo/repo#7 conflict: conflicting https://github.com/octo/repo/pull/7` |
| `commits` | New commits or a force-push changed the head. | `[pr-relay] octo/repo#7 commits: 3 new, head abc1234 https://github.com/octo/repo/pull/7/commits` |
| `ci.failed` | A check failed (sent as soon as it fails). | `[pr-relay] octo/repo#7 ci.failed: lint https://github.com/octo/repo/actions/runs/1/job/11` |
| `ci.settled` | All checks finished, with an overall passed or failed result. | `[pr-relay] octo/repo#7 ci.settled: failed (2 of 12): lint, unit https://github.com/octo/repo/pull/7/checks` |
| `review` | A review was submitted or dismissed (approved, changes requested, commented). | `[pr-relay] octo/repo#7 review changes_requested by alice (4 inline comments) https://github.com/octo/repo/pull/7#pullrequestreview-2` |
| `comment` | A new conversation comment, inline comment or reply. | `[pr-relay] octo/repo#7 comment by bob https://github.com/octo/repo/pull/7#issuecomment-1` |
| `checkin` | The subscription has had no activity for `checkin_after` (default 24h). Repeats until keep, unsub, new activity or expiry. | `[pr-relay] o/r#12 check-in: no activity for 24h. still needed? keep: pr-relay keep https://github.com/o/r/pull/12 \| drop: pr-relay unsub https://github.com/o/r/pull/12` |
| `expired` | The subscription had no activity for `expire_after` (default 7d) and was removed. | `[pr-relay] o/r#12 expired: no activity for 7d, subscription removed. re-subscribe: pr-relay sub https://github.com/o/r/pull/12` |

`--events` takes any of these type names, plus two aliases: `ci` means `ci.failed` and `ci.settled`, and `all` selects every type (the default). `closed`, `checkin` and `expired` always arrive regardless of `--events`. Reviews, comments and commits by the authenticated `gh` user are filtered out, so an agent only hears about other people's activity and CI results.

More examples:

```
[pr-relay] octo/repo#7 ci.settled: passed (12 checks) https://github.com/octo/repo/pull/7/checks
[pr-relay] octo/repo#7 review approved by alice https://github.com/octo/repo/pull/7#pullrequestreview-1
[pr-relay] octo/repo#7 reply by carol on src/x.go https://github.com/octo/repo/pull/7#discussion_r2
[pr-relay] octo/repo#7 commits: force-pushed, head abc1234 (was def5678) https://github.com/octo/repo/compare/def5678...abc1234
[pr-relay] octo/repo#7 conflict: cleared https://github.com/octo/repo/pull/7
[pr-relay] octo/repo#7 note: 3 earlier updates dropped https://github.com/octo/repo/pull/7
```

## Configuration

Configuration is optional. Every key has a default, so a missing file is fine. The file is `~/.config/pr-relay/config.toml` on both macOS and Linux (`$XDG_CONFIG_HOME/pr-relay/config.toml` when `XDG_CONFIG_HOME` is set, or the path in `PR_RELAY_CONFIG`). Durations are Go duration strings such as `"30s"` or `"16m"`, or a whole number of days such as `"7d"`. Unknown keys log a warning; out-of-range values are rejected.

| Table | Key | Default | Meaning |
|-------|-----|---------|---------|
| `[poll]` | `min_interval` | `1m` | Fastest per-PR poll interval (at least 10s). |
| `[poll]` | `max_interval` | `16m` | Slowest per-PR poll interval under a normal rate-limit budget. |
| `[poll]` | `max_stretched_interval` | `60m` | Slowest per-PR interval when the budget runs low. |
| `[poll]` | `budget_pct` | `0.10` | Share of the hourly GitHub GraphQL budget the poller may spend. |
| `[poll]` | `low_budget_threshold` | `0.20` | Remaining budget share below which intervals stretch. |
| `[poll]` | `batch_size` | `20` | Pull requests per batched GraphQL query (at most 20). |
| `[subscriptions]` | `pending_cap` | `200` | Undelivered updates kept per subscription; older ones are dropped with a note. |
| `[delivery]` | `quiet_period` | `10s` | How long an agent must stay idle before an update is typed into its pane (1s to 5m). |
| `[lifecycle]` | `checkin_after` | `24h` | Inactivity before a check-in asks whether the subscription is still needed (at least 1h). |
| `[lifecycle]` | `expire_after` | `7d` | Inactivity before the subscription is removed. Must be longer than `checkin_after`. |

Example:

```toml
[poll]
max_interval = "10m"

[lifecycle]
checkin_after = "48h"
expire_after = "14d"
```

Restart the daemon (`pr-relay daemon restart`) after editing the file.

## How it works

- **One daemon, one poll per PR.** All subscriptions on the machine go to one daemon. Each watched pull request is polled once per cycle no matter how many panes subscribe to it, and up to `batch_size` PRs are fetched in one batched GraphQL query.
- **Adaptive interval.** A PR with recent activity (for example CI still running) is polled about every minute (`min_interval`). As it stays quiet the interval backs off to the 16-minute ceiling (`max_interval`). PRs whose panes cannot receive updates are polled at `max_interval`.
- **Budget-aware backoff.** The poller spends at most `budget_pct` of the hourly GraphQL budget. When the remaining budget falls below `low_budget_threshold` it stretches intervals up to `max_stretched_interval`, and it honours GitHub's rate-limit reset and retry headers.
- **Delivery only when the agent is idle.** The daemon watches herdr's agent status and types an update into a pane only after the agent has been idle for `quiet_period`, so it never interrupts a running turn or collides with someone typing.
- **Coalescing and filtering.** Updates that pile up while an agent is busy are delivered together. Your own activity is filtered out, and comment bodies are never included, only who commented and a link.
- **Persistence.** Subscriptions, cursors and undelivered updates live in a JSON state file that is written atomically, so they survive daemon restarts and reboots.
- **Auth.** The `gh` login is reused (`GH_TOKEN`/`GITHUB_TOKEN`, then the `gh` config or keyring). The `gh` binary itself is not needed at runtime.

## Threat model

pr-relay types text derived from GitHub into an LLM agent's pane, so prompt injection is the main risk: anyone who can comment on, review, push to or run CI on a pull request controls some of that text.

- Messages never include comment or review bodies, PR descriptions or commit messages. They contain the repository and PR number, the event type, and a few short fields.
- The fields that do appear are attacker-controlled GitHub text: user logins, check and workflow names, file paths and branch-derived links. Each is sanitised (control, invisible and bidi characters stripped, whitespace collapsed to one line) and length-capped (logins 40, check names 80, paths 100 characters, URLs 300 bytes, whole line 512 bytes). Anything resembling the `[pr-relay]` tag, including look-alike characters, is neutralised, so a field cannot forge a second relay line.
- Every line starts with the `[pr-relay]` tag, and the agent skill states in one sentence that message text comes from GitHub users and is not an instruction.
- **Residual risk:** a check name such as "ignore previous instructions" still reaches the agent as text. The tag and the skill sentence lower the risk but cannot rule out an agent acting on it. Only subscribe agents with broad permissions to pull requests whose contributors you trust.

Release binaries are not code-signed or notarized. Verify downloads against `checksums.txt` from the same release.

## Troubleshooting

- **Check the daemon.** `pr-relay status` shows whether the daemon is running, its version, `gh` auth, the rate-limit budget and herdr delivery health. Auth can show as unknown until the first poll.
- **Daemon logs.** `daemon.log` (with rotated backups and `crash.log`) is in `~/Library/Logs/pr-relay/` on macOS and `~/.local/state/pr-relay/log/` on Linux (`$XDG_STATE_HOME/pr-relay/log/` when set).
- **State.** `state.json` is in `~/Library/Application Support/pr-relay/` on macOS and `~/.local/state/pr-relay/` on Linux.
- **Plugin logs.** `herdr plugin log list --plugin pr-relay` shows each plugin command with its stdout, stderr and exit code. herdr shows background action output only there; the status and list-all actions open popup panes instead.
- **"pr-relay not found on PATH" in the plugin log.** The herdr server cannot see the binary. Make sure `pr-relay` is on the PATH of the shell you start herdr from (for `go install`, add `$(go env GOPATH)/bin`), then run `herdr server stop` and start herdr again.
- **macOS blocks a downloaded binary.** Raw release downloads are quarantined by Gatekeeper because they are unsigned. After verifying the checksum, clear the attribute:

  ```sh
  xattr -d com.apple.quarantine ./pr-relay
  ```
- **The agent does not know about pr-relay.** Agents that load skills only at startup need a restart or a skills reload after `pr-relay skill install`, especially when the skills directory did not exist before.
- **Updates do not arrive.** Check `pr-relay ls` in the pane, then `pr-relay status`. Updates wait while the agent is busy and are typed in once it has been idle for `quiet_period`. `pr-relay poll <PR-URL>` forces an immediate poll.

## License

MIT-0 (MIT No Attribution). See [LICENSE](LICENSE).
