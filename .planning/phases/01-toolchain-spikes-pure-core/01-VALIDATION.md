---
phase: 1
slug: toolchain-spikes-pure-core
status: draft
nyquist_compliant: true
wave_0_complete: false
created: 2026-10-31
---

# Phase 1 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (stdlib `testing`, golden files, native fuzz seeds) |
| **Config file** | none — Wave 0 creates `go.mod`, `.golangci.yml`, `.github/workflows/ci.yml` |
| **Quick run command** | `go test ./internal/...` |
| **Full suite command** | `go vet ./... && go test -race -count=1 ./...` |
| **Estimated runtime** | ~15 seconds |

---

## Sampling Rate

- **After every task commit:** Run `go test ./internal/...`
- **After every plan wave:** Run `go vet ./... && go test -race -count=1 ./...`
- **Before `/gsd:verify-work`:** Full suite must be green, CI green on ubuntu + macOS, 01-SPIKES.md complete
- **Max feedback latency:** 30 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 01-01-T1 | 01-01 | 1 | (toolchain) | T-01-SC | brew core formulae only | env | `go version && gh --version` | n/a | ⬜ pending |
| 01-01-T2 | 01-01 | 1 | (gh auth, D-17) | — | human gh auth login | checkpoint | `gh api user --jq .login` | n/a | ⬜ pending |
| 01-01-T3 | 01-01 | 1 | SC-1 | T-01-01 | CI `permissions: contents: read` | CI | `go test -race ./... && gh run list --limit 1` | ❌ W0 | ⬜ pending |
| 01-02-T1 | 01-02 | 2 | EVT-* contract | T-01-07 | strict PR URL parse | unit | `go test ./internal/model/` | ❌ W0 | ⬜ pending |
| 01-02-T2 | 01-02 | 2 | DLV-05, EVT-01 (D-09) | T-01-04/05 | no bodies decoded, StatusContext dropped | unit | `go test ./internal/ghquery/` | ❌ W0 | ⬜ pending |
| 01-03-T1 | 01-03 | 2 | DLV-06 | T-01-10..14 | single-line, stripped, capped, tag-neutralized | unit+fuzz | `go test ./internal/sanitize/ -run 'TestHostile\|FuzzField'` | ❌ W0 | ⬜ pending |
| 01-03-T2 | 01-03 | 2 | DLV-06 | T-01-15 | github.com URL allow-list | unit | `go test ./internal/sanitize/ -run 'TestURL\|TestShortSHA'` | ❌ W0 | ⬜ pending |
| 01-04-T1 | 01-04 | 3 | EVT-05, EVT-06, EVT-07 | T-01-17/18 | UNKNOWN never emits; D-21 announce | unit | `go test ./internal/diff/ -run 'TestConflict\|TestMergeable\|TestClosed\|TestCommits'` | ❌ W0 | ⬜ pending |
| 01-04-T2 | 01-04 | 3 | EVT-01, EVT-02 | T-01-16 | once per key per SHA; D-19 re-arm | unit | `go test ./internal/diff/ -run 'TestCI\|TestSettled\|TestConclusionMapping'` | ❌ W0 | ⬜ pending |
| 01-05-T1 | 01-05 | 3 | DLV-05 | T-01-20/22 | no titles/bodies, grammar | golden | `go test ./internal/render/ -run 'TestGolden\|TestLineGrammar\|TestNoBodyFields'` | ❌ W0 | ⬜ pending |
| 01-05-T2 | 01-05 | 3 | DLV-06 | T-01-20/21/23 | hostile fields → safe lines | unit | `go test ./internal/render/ -run TestHostileRender` | ❌ W0 | ⬜ pending |
| 01-06-T1 | 01-06 | 4 | EVT-03, EVT-04 | T-01-27/28 | edits ignored, gap flag | unit | `go test ./internal/diff/ -run 'TestReview\|TestReply\|TestIssueComment\|TestGap\|TestEdits'` | ❌ W0 | ⬜ pending |
| 01-06-T2 | 01-06 | 4 | EVT-08 | T-01-25/26 | User-type-gated self exclusion | unit | `go test ./internal/filter/` | ❌ W0 | ⬜ pending |
| 01-07-T1 | 01-07 | 3 | (fixtures, D-22) | T-01-29 | token never logged | checkpoint | `gh auth status` | n/a | ⬜ pending |
| 01-07-T2 | 01-07 | 3 | EVT-01,02,05,06,07 | T-01-30/31 | dummy-repo guard, body sentinel | fixture | `ls internal/diff/testdata/scenarios/*/00-baseline.json` | ❌ W0 | ⬜ pending |
| 01-07-T3 | 01-07 | 3 | EVT-03,04,08 | T-01-31/33 | fixtures decode, bodies blanked | unit | `go test ./internal/ghquery/ -run TestDecodeAllFixtures` | ❌ W0 | ⬜ pending |
| 01-08-T1 | 01-08 | 3 | SC-2 (S4) | T-01-34/35 | prspike socket guard | script | `bash -n spikes/*.sh && go vet ./spikes/...` | ❌ W0 | ⬜ pending |
| 01-08-T2 | 01-08 | 3 | SC-2 | T-01-36 | human-only session stop | checkpoint | `ls spikes/out/*.log` | n/a | ⬜ pending |
| 01-08-T3 | 01-08 | 3 | SC-2 | T-01-37 | redacted evidence | manual+grep | `grep -cE '^\*\*Answer:\*\* (YES\|NO\|UNKNOWN)' 01-SPIKES.md` | ❌ W0 | ⬜ pending |
| 01-09-T1 | 01-09 | 5 | EVT-01..08, DLV-05 | T-01-39 | no sentinel in output | scenario golden | `go test ./internal/diff/ -run TestScenarios` | ❌ W0 | ⬜ pending |
| 01-09-T2 | 01-09 | 5 | EVT-01..08, DLV-05, DLV-06, SC-1 | T-01-40/41 | exactly-once, grammar, byte cap | scenario invariants + CI | `go test -race ./... && gh run list --limit 1` | ❌ W0 | ⬜ pending |
| 01-09-T3 | 01-09 | 5 | SC-2..5 review | T-01-42 | human sign-off | checkpoint | `go test ./...` | n/a | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `brew install go gh` + `gh auth login` checkpoint
- [ ] Repo + `go mod init`, `cmd/pr-relay/main.go`, `.gitignore`
- [ ] `.github/workflows/ci.yml` (ubuntu-latest + macos-latest; build, vet, test -race; golangci-lint)
- [ ] `.golangci.yml` (v2 format)
- [ ] Golden helper with `-update` flag + scenario harness skeleton (`internal/diff` TestScenarios)
- [ ] Dummy repo + fixture capture via `internal/ghquery/pr.graphql`

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Spike report answers S1–S6 | SC-2 | Requires live herdr in scratch `prspike` session | `grep -E '^\*\*Answer:\*\* (YES\|NO\|UNKNOWN)' .planning/phases/01-toolchain-spikes-pure-core/01-SPIKES.md` + human review |
| CI green on both OSes | SC-1 | Runs on GitHub Actions | Check the Actions run for the phase head commit |

---

## Validation Sign-Off

- [x] All tasks have `<automated>` verify or Wave 0 dependencies
- [x] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [x] No watch-mode flags
- [ ] Feedback latency < 30s
- [x] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
