---
phase: 2
slug: durable-state-github-poller
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-11-01
---

# Phase 2 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (stdlib `testing` + `testing/synctest` + `net/http/httptest`; golden files via `internal/testutil`) |
| **Config file** | none (CI: `.github/workflows/ci.yml` runs `go test -race -count=1 ./...` on ubuntu + macos) |
| **Quick run command** | `go test -count=1 ./internal/...` |
| **Full suite command** | `go test -race -count=1 ./...` (crash loop included; `-short` reduces iterations) |
| **Estimated runtime** | ~60 seconds |

---

## Sampling Rate

- **After every task commit:** Run `go test -count=1 ./internal/...`
- **After every plan wave:** Run `go test -race -count=1 ./...`
- **Before `/gsd-verify-work`:** Full suite must be green on macOS + Linux CI, plus a manual `pr-relay dev watch` session
- **Max feedback latency:** 60 seconds

---

## Per-Task Verification Map

Task IDs are filled in by the planner. Requirement → test mapping (from 02-RESEARCH.md §Validation Architecture):

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| TBD | TBD | TBD | EVT-09 / SC-1 | — | N/A | subprocess e2e | `go test -race -count=1 -run TestCrashLoop ./cmd/pr-relay/` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | EVT-09 | — | N/A | unit | `go test -run TestCommitFailureDoesNotAdvance ./internal/poller/` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | EVT-09 (CR-01, WR-02) | — | N/A | scenario golden + unit | `go test -run 'TestScenarios\|TestWatermark\|TestEpochKeys' ./internal/diff/` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | LIFE-03 | Tampering (local files) | State dir 0700, file 0600; corrupt file quarantined; newer version refused | unit + golden | `go test -run 'TestStore\|TestStateV1Golden' ./internal/store/` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | SUB-08 | — | N/A | engine + fake transport | `go test -run TestBaselineSilent ./internal/poller/` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | SUB-04 | — | N/A | unit | `go test -run TestResubscribeUpsert ./internal/poller/` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | DMN-02 | — | N/A | synctest | `go test -run TestOnePollPerPR ./internal/poller/` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | DMN-03 | Info disclosure (token) | Token never logged/persisted; 401 triggers config Reload; gh fallback times out | unit/httptest | `go test -run 'TestToken\|TestUnauthorizedRefresh' ./internal/ghclient/` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | DMN-04 | DoS (malicious response) | Variables not interpolation; LimitReader; per-alias decode isolation | unit + fixtures | `go test -run 'TestBuildBatch\|TestDecodeBatch' ./internal/ghquery/` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | DMN-04 | — | N/A | unit | `go test -run TestCostLogged ./internal/poller/` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | DMN-04 / D-07 | DoS (self/ban) | Honour Retry-After exactly; doubling to 15m cap | synctest | `go test -run TestSecondaryBackoff ./internal/poller/` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | DMN-04 / D-05/D-06 | — | N/A | synctest + pure | `go test -run 'TestBudget' ./internal/poller/` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | LIFE-05 | — | N/A | synctest + pure | `go test -run 'TestCadence\|TestQuietRamp\|TestActivityReset' ./internal/poller/` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | D-04 (CR-02, WR-01..07) | Spoofing (lookalike tag), path traversal | NFKC + skeleton fold; `..` rejected | unit + scenario + hostile corpus | `go test ./internal/diff/ ./internal/model/ ./internal/ghquery/ ./internal/sanitize/ ./internal/render/` | partial | ⬜ pending |
| TBD | TBD | TBD | D-21 | — | N/A | e2e | `go test -run TestDevWatchOutput ./cmd/pr-relay/` | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `internal/poller/fakegh_test.go` — in-memory `http.RoundTripper` fake GitHub (scripted PR states, alias counting, injectable status/headers). Must be a RoundTripper, not httptest, so synctest time advances.
- [ ] `internal/ghquery/testdata/batch-*.json` — batch fixtures (mixed NOT_FOUND alias, nested FORBIDDEN partial, RATE_LIMITED, viewer)
- [ ] `internal/store/testdata/state-v1.json` — schema golden
- [ ] `cmd/pr-relay/crash_test.go` + `TestMain` binary build + httptest fake GitHub
- [ ] New diff scenarios: `delete-comment-31`, `reopen-close`, `big-push-30`, `parallel-trigger`, `review-comments-31`
- [ ] Framework install: none (stdlib)

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Live batch query accepted by GitHub (fragment + variables, per-alias null on missing PR) | DMN-04 (A1/A2) | Requires real GitHub API | Run `pr-relay dev watch <real PR> <missing PR>`; confirm cost line logged and missing PR reported as a per-alias warning |
| Keyring token resolution on macOS | DMN-03 | Requires real `gh` keyring | With no GH_TOKEN, run `pr-relay dev watch <PR>`; confirm auth succeeds |
| Quiet ramp observed live | LIFE-05 | Hours-long real time | Leave `dev watch` running on an idle PR; confirm logged interval grows per table |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 60s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
