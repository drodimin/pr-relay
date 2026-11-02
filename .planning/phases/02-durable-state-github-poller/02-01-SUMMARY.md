---
phase: 02-durable-state-github-poller
plan: 01
subsystem: foundation
tags: [go-modules, paths, config, toml]
requires: []
provides:
  - "Pinned Phase 2 modules in go.mod/go.sum (go 1.26.7)"
  - "internal/paths: StateDir, ConfigPath"
  - "internal/config: Config, PollConfig, SubsConfig, Duration, Defaults, Load, Validate"
affects: [internal/store, internal/ghclient, internal/poller, cmd/pr-relay]
tech-stack:
  added: [github.com/cli/go-gh/v2@v2.17.0, github.com/spf13/cobra@v1.10.2, github.com/BurntSushi/toml@v1.6.0, github.com/gofrs/flock@v0.13.1, golang.org/x/text@v0.43.0]
  patterns: ["defaults-first TOML decode with MetaData.Undecoded warnings", "env-override path resolution for hermetic tests", "MkdirAll + Chmod 0700 to tighten pre-existing dirs"]
key-files:
  created: [internal/paths/paths.go, internal/paths/paths_test.go, internal/config/config.go, internal/config/config_test.go, go.sum]
  modified: [go.mod]
decisions:
  - "go mod tidy deliberately not run; requirements stay // indirect until plan 02-11 (toml is now imported but still labelled indirect, which does not affect builds)"
  - "Malformed TOML duration surfaces as `config: parse <path>: ...` (the toml error names the key, e.g. poll.min_interval)"
  - "Exported EnvStateDir/EnvConfig and MaxBatchSize constants for reuse by later plans and tests"
metrics:
  duration: "~6 min"
  completed: 2026-11-01
  tasks: 2
  files: 6
---

# Phase 2 Plan 01: Module pins, paths and config Summary

Pinned the five human-approved Phase 2 Go modules (go 1.26.7) and added `internal/paths` (0700 state dir, config path resolution with env overrides) and `internal/config` (D-05..D-08 defaults, TOML Load with unknown-key warnings and range validation).

## Task 1: Package legitimacy checkpoint (resolved)

The orchestrator checked proxy.golang.org and the human replied "Approve all five" for:

- github.com/cli/go-gh/v2@v2.17.0
- github.com/spf13/cobra@v1.10.2
- github.com/BurntSushi/toml@v1.6.0
- github.com/gofrs/flock@v0.13.1
- golang.org/x/text@v0.43.0 (source go.googlesource.com/text)

Transitive additions from `go get`: cli/safeexec v1.0.1, inconshreveable/mousetrap v1.1.0, spf13/pflag v1.0.9, golang.org/x/sys v0.49.0, gopkg.in/yaml.v3 v3.0.1.

## Task 2: paths and config (TDD)

- RED `be66892`: go.mod/go.sum pins plus failing table tests (build failed: undefined StateDir/ConfigPath/Load).
- GREEN `ae0c261`: implementation; `go build ./... && go vet ./... && go test -race -count=1 ./...` all pass.

Tests cover every StateDir branch (override, pre-existing 0755 tightened to 0700, XDG, darwin/linux default under temp HOME), every ConfigPath branch (and that it creates nothing), missing file gives defaults, duration strings, all fields, unknown-key warning text, and every Validate rejection including a malformed duration. Inclusive bounds (budget 1.0, threshold 0.0, batch 1, pending 1) are accepted.

## Deviations from Plan

None in code. One process note: the worktree started at 5cd46a0 instead of the expected base 0f6e5f4, so it was reset to 0f6e5f4 as the branch check instructs before any work.

## TDD Gate Compliance

`test(02-01)` commit be66892 is followed by `feat(02-01)` commit ae0c261. No refactor needed.

## Self-Check: PASSED

- FOUND: internal/paths/paths.go, internal/paths/paths_test.go, internal/config/config.go, internal/config/config_test.go, go.sum
- FOUND: be66892, ae0c261
