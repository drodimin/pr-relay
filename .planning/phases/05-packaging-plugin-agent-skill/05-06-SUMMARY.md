---
phase: 05-packaging-plugin-agent-skill
plan: 06
subsystem: distribution
tags: [herdr, plugin, shell]
requires:
  - "05-05: pr-relay daemon --ensure (startup hook target)"
  - "05-04: pr-relay skill install (install-skill action target)"
provides:
  - "herdr-plugin/herdr-plugin.toml: installable as drodimin/pr-relay/herdr-plugin"
  - "herdr-plugin/run.sh: POSIX PATH guard, exec pr-relay \"$@\""
  - "TestPluginManifest / TestPluginRunSh"
affects:
  - "release plans that bump the plugin version (pluginManifestVersion const in plugin_test.go)"
tech-stack:
  added: []
  patterns:
    - "Strict TOML decode via md.Undecoded() to reject unmodelled keys"
    - "Shell scripts tested with an explicit PATH-only env, never os.Environ()"
key-files:
  created:
    - herdr-plugin/herdr-plugin.toml
    - herdr-plugin/run.sh
    - cmd/pr-relay/plugin_test.go
  modified: []
decisions:
  - "The manifest uses `id`. The 0.9.5 schema only exposes InstalledPluginInfo, which reports it as `plugin_id`; every other field and enum matched as planned"
  - "No herdr validate subcommand exists in 0.9.5, and `herdr plugin link` was not run, so the user's live herdr state stays untouched"
metrics:
  duration: "~8m"
  completed: 2026-11-05
  tasks: 2
  files: 3
---

# Phase 5 Plan 06: herdr Plugin Manifest and run.sh Guard Summary

The pr-relay herdr plugin is now a manifest with one `[[startup]]` hook and four global actions. All its commands go through a POSIX `run.sh` wrapper: when `pr-relay` is missing, the wrapper prints one install line and exits 0; otherwise it `exec`s the binary with arguments passed unchanged. Go tests decode the manifest strictly and run the wrapper with a controlled PATH.

## What was built

- **herdr-plugin/herdr-plugin.toml** (D-06..D-09): id/name `pr-relay`, version `0.1.0`, `min_herdr_version = "0.9.5"`, platforms `[linux, macos]`. Startup runs `["sh", "run.sh", "daemon", "--ensure"]`. The four actions are `status`, `list-all`, `restart` and `install-skill`, each with `contexts = ["global"]`. There is no `[[build]]` table.
- **herdr-plugin/run.sh** (D-10): `command -v pr-relay` guard, the exact install line on stderr, then `exit 0`. Otherwise `exec pr-relay "$@"`. The file is mode 0755.
- **cmd/pr-relay/plugin_test.go**: `TestPluginManifest` checks that `md.Undecoded()` is empty and asserts every field, the startup argv, the action-to-command map, that no action id contains a dot, the `sh run.sh` prefix, non-empty titles and global contexts. `TestPluginRunSh` has three subtests. `missing` checks for exit 0, empty stdout and the exact stderr line. `passthrough` checks that `a b` and `$(touch pwned)` arrive as literal arguments and that no `pwned` file is created. `exitcode` checks that an exit status of 3 is passed through.

## Verification

- Every manifest field and the enums `PluginActionContext` and `PluginPlatform` were confirmed with `herdr api schema --json` on herdr 0.9.5.
- `sh -n herdr-plugin/run.sh` passes. There are 4 `[[actions]]`, 0 `[[build]]`, no `eval`, and no `HERDR_PLUGIN_STATE_DIR` in any .go, .sh or .toml file.
- `go test ./cmd/pr-relay -run 'TestPluginManifest|TestPluginRunSh' -count=1 -v`: all tests pass. `go vet` is clean.
- Mutation check: I temporarily added `[[build]]` to the manifest and changed run.sh to the unsafe form `eval exec pr-relay $@`. `TestPluginManifest` failed on the unexpected keys, and `passthrough` failed on word splitting and the created `pwned` file. Both files were then restored with `git checkout`.

## TDD Gate Compliance

Task 2 is `tdd="true"`, but it tests the files Task 1 created, so no RED run was possible on the real files: the `test(05-06)` commit comes after the `feat(05-06)` commit. The mutation check above stands in for RED by showing the tests fail on a manifest or wrapper that breaks the rules.

## Deviations from Plan

None. The plan was executed as written.

## Known Stubs

None.

## Commits

- 32b6b31: feat(05-06): add herdr plugin manifest and run.sh PATH guard
- 28efeec: test(05-06): validate herdr plugin manifest and run.sh guard

## Self-Check: PASSED

- FOUND: herdr-plugin/herdr-plugin.toml, herdr-plugin/run.sh, cmd/pr-relay/plugin_test.go
- FOUND: 32b6b31, 28efeec
