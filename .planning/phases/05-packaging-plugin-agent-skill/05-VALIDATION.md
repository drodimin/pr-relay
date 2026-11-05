---
phase: 5
slug: packaging-plugin-agent-skill
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-11-05
---

# Phase 5 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Go stdlib `testing` + `internal/testutil.Golden` |
| **Config file** | none (`go test`); CI in `.github/workflows/ci.yml` |
| **Quick run command** | `go test ./cmd/pr-relay/ ./skills/ ./internal/autostart/ ./internal/poller/ ./internal/store/ ./internal/daemon/ -count=1` |
| **Full suite command** | `go test -race -count=1 ./... && go vet ./...` |
| **Estimated runtime** | ~60 seconds |

---

## Sampling Rate

- **After every task commit:** Run the quick command for the touched packages
- **After every plan wave:** Run the full suite command
- **Before `/gsd:verify-work`:** Full suite must be green, CI snapshot release green on ubuntu and macOS
- **Max feedback latency:** 60 seconds

---

## Per-Task Verification Map

Planner and executor fill in task IDs. These are the requirement-to-test anchors from RESEARCH.md § Validation Architecture:

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| TBD | TBD | 1 | D-21 CR-01 | — | sub honours its deadline and persists nothing on failure | unit | `go test ./internal/poller -run Deadline` | ❌ W0 | ⬜ pending |
| TBD | TBD | 1 | D-21 CR-02 | — | Save after Close errors; shutdown drains handlers | unit | `go test ./internal/store -run TestSaveAfterClose && go test ./internal/daemon -run TestShutdownDrain` | ❌ W0 | ⬜ pending |
| TBD | TBD | 1 | D-21 CR-03 | — | live re-sub clears the pending check-in | unit | `go test ./internal/poller -run TestResubscribeClearsCheckin` | ❌ W0 | ⬜ pending |
| TBD | TBD | 1 | D-26 WR-03/08/09 | — | concurrent CLI during shutdown succeeds; slow hello retried; loser does not touch the live log | unit/integration | `go test ./internal/autostart ./internal/daemon -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | — | DIST-01 | T-supply-chain | 4 static targets, checksums | CI snapshot | `goreleaser release --snapshot --clean --skip=publish` (CI) | ❌ W0 | ⬜ pending |
| TBD | TBD | — | DIST-02 | — | version fallback via ReadBuildInfo | unit | `go test ./cmd/pr-relay -run TestResolveVersion` | ❌ W0 | ⬜ pending |
| TBD | TBD | — | DIST-03 | — | `daemon --ensure` idempotent / spawn / restart on mismatch | integration | `go test ./cmd/pr-relay -run TestDaemonEnsure` | ❌ W0 | ⬜ pending |
| TBD | TBD | — | DIST-03 | T-shell | run.sh exits 0 without pr-relay; passes args via "$@" | unit | `go test ./cmd/pr-relay -run TestPluginRunSh` | ❌ W0 | ⬜ pending |
| TBD | TBD | — | DIST-03 | — | manifest decodes, required fields, ids without dots | unit | `go test ./cmd/pr-relay -run TestPluginManifest` | ❌ W0 | ⬜ pending |
| TBD | TBD | — | DIST-04 | T-symlink | SKILL.md spec-valid; `skill install --dir` byte-equal, refuses symlinks | unit | `go test ./skills ./cmd/pr-relay -run TestSkill` | ❌ W0 | ⬜ pending |
| TBD | TBD | — | DIST-04/05 | — | docs examples ⊆ goldens; all event types, commands and flags documented | unit | `go test ./cmd/pr-relay -run TestDocsDrift` | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `skills/` package + `skills_test.go`
- [ ] `cmd/pr-relay/docs_test.go`, `version_test.go`, ensure test in the existing e2e harness
- [ ] Plugin tests (`run.sh` exec test and manifest decode) under `cmd/pr-relay`, which read `../../herdr-plugin/*`
- [ ] CI `goreleaser` snapshot job in `ci.yml`

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| brew install, release download and go install all work and print v0.1.0 | DIST-02 | Needs the real tag, tap repo and PAT | D-20 release UAT |
| macOS cask install runs without a quarantine block | DIST-02 | Homebrew 7.x Gatekeeper policy | `brew install drodimin/tap/pr-relay && pr-relay version` on macOS |
| herdr start runs the startup hook, which brings the daemon up | DIST-03 | Real herdr TUI | P5 install-path UAT (D-22) |
| herdr action output is visible | DIST-03 | Unverified herdr behaviour | spike: `herdr plugin link` + `herdr plugin action invoke pr-relay.status` |
| real sub, then real update in a pane | D-22 | Real GitHub and herdr | 04-HUMAN-UAT plus the P5 install UAT |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 60s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
