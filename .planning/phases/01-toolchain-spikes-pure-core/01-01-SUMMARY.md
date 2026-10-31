---
phase: 01-toolchain-spikes-pure-core
plan: 01
subsystem: toolchain
tags: [go, ci, github-actions, golangci-lint, golden-files]
requires: []
provides:
  - "Go module github.com/drodimin/pr-relay (go 1.26, stdlib-only)"
  - "internal/testutil.Golden helper with -update flag"
  - "CI matrix ubuntu-latest + macos-latest x go.mod/stable, plus golangci-lint v2.15.0"
  - "Public repo github.com/drodimin/pr-relay (origin, SSH)"
affects: [all later phase-01 plans]
tech-stack:
  added: [go 1.27.3 (dev), gh 2.103.3 (dev), golangci-lint v2.15.0 (CI only)]
  patterns: ["golden files under testdata/ rewritten only with -update", "unexported compare() for testing mismatch output"]
key-files:
  created:
    - go.mod
    - cmd/pr-relay/main.go
    - cmd/pr-relay/main_test.go
    - internal/testutil/golden.go
    - internal/testutil/golden_test.go
    - .gitignore
    - .golangci.yml
    - .github/workflows/ci.yml
  modified: []
decisions:
  - "CI matrix uses fail-fast: false so every OS/Go cell reports independently"
  - "Push uses the SSH remote (git@github.com:drodimin/pr-relay.git); the gh token has no workflow scope, which SSH push does not need"
metrics:
  completed: 2026-10-31
  tasks: 3
  files: 8
---

# Phase 1 Plan 01: Toolchain, Repo and CI Bootstrap Summary

A stdlib-only Go module (`github.com/drodimin/pr-relay`, `go 1.26`) with a version-printing binary, a shared golden-file helper, golangci-lint v2 config and a GitHub Actions matrix. The module is pushed to the new public repo drodimin/pr-relay, and CI is green on ubuntu-latest and macos-latest.

## Environment

- `go version`: go version go1.27.3 darwin/arm64
- `gh --version`: gh version 2.103.3 (2026-10-26)
- GitHub login: `drodimin` (keyring; git protocol ssh; scopes admin:public_key, gist, read:org, repo)
- Repo: https://github.com/drodimin/pr-relay (visibility PUBLIC)
- Green CI run: https://github.com/drodimin/pr-relay/actions/runs/41252378217. All five jobs passed: lint, test (ubuntu-latest, go.mod), test (ubuntu-latest, stable), test (macos-latest, go.mod), test (macos-latest, stable).

## Tasks

| Task | Name | Commit |
|------|------|--------|
| 1 | Install Go and gh via Homebrew | none (machine-only) |
| 2 | Human authenticates gh and approves public repo creation | none (checkpoint; user replied "approved") |
| 3 | Go module, golden helper, lint and CI config, push and green CI | 0f80d1b |

## Verification

- `gofmt -l .` printed nothing. `go build ./...`, `go vet ./...` and `go test -race -count=1 ./...` passed locally.
- go.mod has `module github.com/drodimin/pr-relay`, `go 1.26`, and no `require` lines.
- `git remote get-url origin` is `git@github.com:drodimin/pr-relay.git`.

## Deviations from Plan

None. The plan executed as written.

## Notes for Later Plans

- Golden usage: `testutil.Golden(t, "testdata/scenarios/x/expected.golden", got)`. Regenerate with `go test ./... -update` and review the diff. Never pass `-update` in CI.
- CI shows informational annotations: ubuntu-latest is migrating to Ubuntu 26, and macOS arm64 runners may queue longer. Neither needs action.
- The full git history, including `.planning/` and `CLAUDE.md`, is now public. BRIEF.md and `.planning/config.json` were left uncommitted.

## Self-Check: PASSED

- All 8 created files exist. Commit 0f80d1b is on origin/main. CI run 41252378217 concluded success.
