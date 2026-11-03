---
phase: 03-herdr-client-delivery
plan: 01
subsystem: store, config
tags: [schema-migration, config, delivery]
requires: []
provides:
  - "store.SchemaVersion = 2 with Subscription.AgentKind (json agent_kind, omitempty)"
  - "store.migrate case 1 -> 2 (version key rewrite only)"
  - "config.DeliveryConfig{QuietPeriod}, DefaultQuietPeriod, MinQuietPeriod, MaxQuietPeriod"
affects: [delivery worker identity check (LIFE-02), delivery gate (DLV-02)]
tech-stack:
  added: []
  patterns: ["additive schema migration through map[string]json.RawMessage", "test-only EnvFastPoll floor override reused for quiet_period"]
key-files:
  created:
    - internal/store/testdata/state-v2.json
  modified:
    - internal/store/state.go
    - internal/store/store.go
    - internal/store/state_test.go
    - internal/store/store_test.go
    - internal/config/config.go
    - internal/config/config_test.go
decisions:
  - "v1 -> v2 migration only rewrites the version key; AgentKind stays empty on migrated subs and is omitted from the JSON"
  - "quiet_period bounds [1s, 5m]; PR_RELAY_TEST_FAST_POLL=1 lowers only the floor (1ms), the 5m max still applies"
metrics:
  duration: "~10m"
  completed: 2026-11-02
  tasks: 2
  files: 7
requirements: [LIFE-02, DLV-02]
---

# Phase 3 Plan 01: Store schema v2 and delivery quiet period Summary

Store schema v2 adds `Subscription.AgentKind` (the D-13 fallback identity) with a lossless v1 -> v2 migration, and config gains a range-validated `[delivery] quiet_period` that defaults to 10s (D-09).

## Tasks

| Task | Name | Commits |
| ---- | ---- | ------- |
| 1 | Store schema v2 with AgentKind and v1 -> v2 migration | cbbfd60 (test), ab79729 (feat) |
| 2 | [delivery] quiet_period config key | 92665d7 (test), f0d25ed (feat) |

## What was built

- `internal/store/state.go`: `SchemaVersion = 2`, and `AgentKind string \`json:"agent_kind,omitempty"\`` after `AgentSession`. `Clone` needed no change because the field is a plain string.
- `internal/store/store.go`: `migrate` has a `case 1:` that decodes into `map[string]json.RawMessage`, sets `version` to 2 and re-marshals. Unknown versions still return the error, and newer-than-binary files are still refused before migration runs.
- `internal/store/testdata/state-v2.json`: a new golden. Its only differences from `state-v1.json` are `"version": 2` and `"agent_kind": "codex"` on one sub. `state-v1.json` is unchanged and is used as the migration input.
- Tests: `TestStateV2Golden`, `TestMigrateV1ToV2` (open a copy of v1, check `Version == 2` and that the PRs, Subs and scalars match, save, check the file is v2 with no `agent_kind`), `TestSubscriptionAgentKindOmitEmpty`, and `TestStoreNewerVersion` now uses version 3.
- `internal/config/config.go`: `DefaultQuietPeriod`, `MinQuietPeriod`, `MaxQuietPeriod`, `DeliveryConfig`, `Config.Delivery` (toml `delivery`), `quietPeriodFloor()`, two new `Validate` cases, and a D-09 package doc bullet.
- Tests: `TestQuietPeriod`, plus new `TestLoadInvalid` cases (0s, -1s, 500ms, 6m, unparseable). `TestLoadBoundsInclusive` checks that 1s and 5m are accepted, and `TestDefaults` and `TestLoadAllFields` were extended.

## Verification

- `go test ./internal/store/... ./internal/config/... -count=1` passes.
- `go vet ./...` and `go test -race ./... -count=1` pass.
- Every acceptance-criteria grep matches. `git diff --quiet internal/store/testdata/state-v1.json` shows the file is unchanged.

## Deviations from Plan

None. The plan was executed as written. One minor test detail: `go vet` flagged `got.Version != SchemaVersion || got.Version != 2` as a suspect `or`, so the migration test asserts `got.Version != 2` directly.

## TDD Gate Compliance

Both tasks have a RED `test(...)` commit, which failed to compile because the fields did not exist yet, followed by a GREEN `feat(...)` commit. No refactor commit was needed.

## Threat Model

- T-03-01 is mitigated. The migration rewrites only the version key, and `TestStoreNewerVersion` (version 3) checks that newer files are refused without being quarantined or modified.
- T-03-02 is mitigated. `Validate` bounds quiet_period to [1s, 5m].

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: internal/store/testdata/state-v2.json
- FOUND commits: cbbfd60, ab79729, 92665d7, f0d25ed
