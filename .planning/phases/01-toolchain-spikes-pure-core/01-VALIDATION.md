---
phase: 1
slug: toolchain-spikes-pure-core
status: draft
nyquist_compliant: false
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
| (filled by planner from RESEARCH.md "Phase Requirements -> Test Map") | | | EVT-01..08, DLV-05, DLV-06 | | | | | ❌ W0 | ⬜ pending |

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

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 30s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
