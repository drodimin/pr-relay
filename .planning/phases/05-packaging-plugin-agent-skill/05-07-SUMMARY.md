---
phase: 05-packaging-plugin-agent-skill
plan: 07
subsystem: distribution
tags: [herdr, plugin, spike, checkpoint]
requires:
  - "05-06: herdr-plugin manifest and run.sh PATH guard"
provides:
  - "herdr-plugin [[panes]] status-view and list-view (placement popup)"
  - "run.sh --hold and --open PANE_ID"
  - "Spike findings on herdr 0.9.5 action-output visibility"
affects:
  - "05-08 README: must document a [[keys.command]] keybinding snippet (no TUI action menu exists)"
  - "05-11 UAT: startup hook execution by herdr itself is unproven; auth shows unknown before first poll"
  - "05-12 UAT: remove dev copy ~/.local/bin/pr-relay before brew install (D-06)"
tech-stack:
  added: []
  patterns:
    - "Plugin actions that need visible output open a popup [[panes]] entrypoint via $HERDR_BIN_PATH plugin pane open"
key-files:
  created: []
  modified:
    - herdr-plugin/herdr-plugin.toml
    - herdr-plugin/run.sh
    - cmd/pr-relay/plugin_test.go
decisions:
  - "Result: not visible. herdr 0.9.5 shows action stdout only in the plugin command log, so status and list-all open popup panes instead"
  - "run.sh --open does not pass --placement: the herdr 0.9.5 CLI's --placement flag doesn't accept popup, so the manifest's placement = \"popup\" applies"
  - "restart and install-skill stay as direct commands; their effect shows elsewhere (daemon pid, skill files)"
metrics:
  duration: "~15m (Task 3 and summary; Tasks 1-2 in the previous session)"
  completed: 2026-11-07
  tasks: 3
  files: 3
---

# Phase 5 Plan 07: herdr Action-Output Visibility Spike Summary

The spike result was **not visible**: herdr 0.9.5 shows no plugin action output in the TUI. The status and list-all actions now open popup panes, which run the command and wait for Enter, so the output is readable. The four D-09 action ids are unchanged.

## Spike findings (Task 1, herdr 0.9.5)

- `command -v pr-relay` printed nothing, so a dev copy was built with `go build -o ~/.local/bin/pr-relay ./cmd/pr-relay` (version `v0.0.0-20261105193847-51497c6af071+dirty`). `~/.local/bin` is on the herdr server's PATH. **This dev copy at `/Users/drodi/.local/bin/pr-relay` MUST be removed before the brew install UAT in plan 05-12 (D-06, single copy).**
- `herdr plugin link /Users/drodi/repos/pr-relay/herdr-plugin` returned `plugin_linked`, enabled, with no warnings. `herdr plugin list` shows `pr-relay (pr-relay) enabled [local:/Users/drodi/repos/pr-relay/herdr-plugin]`. The action list shows install-skill, list-all, restart and status.
- `herdr plugin action invoke pr-relay.status` returns right away with `status: "running"` and metadata only. It **does not print the action's stdout**.
- `herdr plugin log list --plugin pr-relay` entries **do** contain `stdout`, `stderr` and `exit_code`. The status stdout began `daemon: running (version v0.0.0-...+dirty, pid 13462, up 4s)`, and the list-all stdout was `no subscriptions\n`.
- herdr gave no manifest warnings anywhere.
- **D-08 gap:** `plugin link` doesn't run `[[startup]]`, and running `run.sh` by hand doesn't log to herdr. Whether herdr itself runs the startup hook is still unproven, since that needs a herdr restart. Flagged for UAT plan 05-11.
- `pr-relay status` showed `auth: unknown (gh)` before any poll. UAT should confirm that it resolves after the first poll.

## Human check (Task 2)

The user answered **"not visible"**. The herdr TUI has no plugin-related UI: the help panel (prefix+?) and settings show nothing about plugins. The user chose "go ahead with the popup pane".

From the herdr v0.9.5 docs (plugins, configuration, cli-reference and socket-api .mdx): the TUI has no command palette or plugin menu. The only way to trigger an action from the TUI is a keybinding in the user's own config. Plugins cannot declare keybindings in the manifest:

```toml
[[keys.command]]
key = "prefix+l"
type = "plugin_action"
command = "pr-relay.status"
description = "pr-relay status"   # optional; shown in prefix+? help
```

**Plan 05-08's README must document a `[[keys.command]]` snippet for `pr-relay.status` and `pr-relay.list-all`.**

## Final manifest shape (Task 3)

- `[[startup]]`: `sh run.sh daemon --ensure` (unchanged)
- `[[actions]]` (4, ids unchanged, contexts `["global"]`):
  - `status`: `sh run.sh --open status-view`
  - `list-all`: `sh run.sh --open list-view`
  - `restart`: `sh run.sh daemon restart`
  - `install-skill`: `sh run.sh skill install`
- `[[panes]]` (2, `placement = "popup"`):
  - `status-view` "pr-relay status": `sh run.sh --hold status`
  - `list-view` "pr-relay subscriptions": `sh run.sh --hold ls --all`

`run.sh` options:
- `--open PANE_ID` runs `exec "${HERDR_BIN_PATH:-herdr}" plugin pane open --plugin pr-relay --entrypoint "$PANE_ID"`. Arguments are quoted and there is no eval (T-05-20).
- `--hold` runs `pr-relay "$@"`, or prints the D-10 missing message to stderr. It then prints `press Enter to close`, reads one line and exits 0.

Schema check: `PluginManifestPane` has the fields `id`, `title`, `command`, `placement`, `width`, `height`, `description` and `platforms`, and `PluginPanePlacement` includes `popup`. `herdr plugin pane open --help` lists `--placement` values as overlay|split|tab|zoomed only, with no popup, so `--open` relies on the manifest's placement.

## Live verification after Task 3

- herdr reloaded the linked manifest without a relink: `herdr plugin action list` showed the new `--open` commands.
- `herdr plugin action invoke pr-relay.status` produced log entry `plugin-log-3`: command `["sh","run.sh","--open","status-view"]`, `status: succeeded`, `exit_code: 0`, stdout `{"id":"cli:plugin","result":{"type":"ok"}}`. The pane-open call succeeded from the CLI with no `no_foreground_client` or `ui_busy` error. A status popup waiting for Enter was probably opened on the user's screen.
- `herdr plugin action invoke pr-relay.install-skill` succeeded with exit 0 and stdout `installed /Users/drodi/.agents/skills/pr-relay/SKILL.md` and `installed /Users/drodi/.claude/skills/pr-relay/SKILL.md`. `ls -l ~/.agents/skills/pr-relay/SKILL.md` shows `-rw-r--r-- 5695 Nov 7 09:43`.
- `go test ./cmd/pr-relay -run TestPlugin -count=1 -v` passes: TestPluginManifest, plus the TestPluginRunSh subtests missing, passthrough, exitcode, hold, hold-missing, open and open-fallback. `sh -n herdr-plugin/run.sh` is clean.

## Commits

- `171689c` feat(05-07): open popup panes for status and list-all plugin actions

## Deviations from Plan

**1. [Rule 3 - Blocking] `--open` passes no `--placement` flag**
- **Found during:** Task 3
- **Issue:** The herdr 0.9.5 `plugin pane open --placement` flag doesn't accept `popup`.
- **Fix:** Omit the flag so the manifest's `placement = "popup"` applies. Verified live: the call returned `ok`.
- **Commit:** 171689c

**2. [Rule 2 - Coverage] Added hold-missing and open-fallback subtests**
- These cover the D-10 message under `--hold` and the fallback to `herdr` when `HERDR_BIN_PATH` is empty. Both were stated requirements without tests.
- **Commit:** 171689c

## Known Stubs

None.

## Self-Check: PASSED

- herdr-plugin/herdr-plugin.toml, herdr-plugin/run.sh and cmd/pr-relay/plugin_test.go exist and were modified in 171689c
- Commit 171689c is present in git log
