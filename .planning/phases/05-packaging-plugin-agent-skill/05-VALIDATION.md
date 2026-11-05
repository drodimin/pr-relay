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

Task IDs filled by the planner (2026-11-05). CR-02's shutdown-drain test lives in `internal/ctl` (TestShutdownDrain) rather than `internal/daemon`, because the drain is a ctl.Server property:

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 05-01-T1 | 05-01 | 1 | D-21 CR-01 | T-05-01, T-05-02 | sub honours its deadline and persists nothing on failure; PollNow bounded | unit | `go test ./internal/poller -run Deadline -race -count=1` | ❌ W0 | ⬜ pending |
| 05-01-T2 | 05-01 | 1 | D-21 CR-03 | T-05-03 | live re-sub clears the pending check-in | unit | `go test ./internal/poller -run TestResubscribeClearsCheckin -count=1` | ❌ W0 | ⬜ pending |
| 05-02-T1 | 05-02 | 1 | D-21 CR-02 | T-05-04, T-05-05 | Save after Close errors; Server.Close waits for handlers | unit | `go test ./internal/store -run 'TestSaveAfterClose\|TestSaveCloseRace' -race && go test ./internal/ctl -run TestShutdownDrain -race` | ❌ W0 | ⬜ pending |
| 05-02-T2 | 05-02 | 1 | D-26 WR-09 | T-05-06 | loser does not touch the live log | unit | `go test ./internal/daemon -run TestLoserDoesNotTouchLog -race` | ❌ W0 | ⬜ pending |
| 05-03-T1 | 05-03 | 1 | D-26 WR-08 | T-05-07 | slow hello retried; hung daemon names the log | unit | `go test ./internal/autostart -run 'TestSlowHelloRetried\|TestHelloNeverAnswers' -race` | ❌ W0 | ⬜ pending |
| 05-03-T2 | 05-03 | 1 | D-26 WR-03 | T-05-07 | CLI during shutdown succeeds | unit | `go test ./internal/autostart -run TestConnectDuringShutdown -race -count=3` | ❌ W0 | ⬜ pending |
| 05-04-T1 | 05-04 | 1 | DIST-04 | T-05-11 | SKILL.md spec-valid, non-prescriptive, agent-agnostic, golden examples | unit | `go test ./skills -count=1` | ❌ W0 | ⬜ pending |
| 05-04-T2 | 05-04 | 1 | DIST-04 | T-05-09, T-05-10 | `skill install --dir` byte-equal, refuses symlinks, atomic | unit | `go test ./cmd/pr-relay -run TestSkill -count=1` | ❌ W0 | ⬜ pending |
| 05-05-T1 | 05-05 | 2 | DIST-02 | T-05-15 | version fallback via ReadBuildInfo | unit | `go test ./cmd/pr-relay -run TestResolveVersion -count=1` | ❌ W0 | ⬜ pending |
| 05-05-T2 | 05-05 | 2 | DIST-03 | T-05-13 | `daemon --ensure` idempotent / spawn / restart on mismatch | integration | `go test ./cmd/pr-relay -run 'TestRunEnsure\|TestDaemonEnsure\|TestAlreadyRunningMsgMatches' -race -count=1` | ❌ W0 | ⬜ pending |
| 05-06-T1 | 05-06 | 3 | DIST-03 | T-05-16, T-05-18 | manifest matches herdr 0.9.5 schema; no [[build]] | static | `sh -n herdr-plugin/run.sh` | ❌ W0 | ⬜ pending |
| 05-06-T2 | 05-06 | 3 | DIST-03 | T-05-16, T-05-17 | run.sh exits 0 without pr-relay; passes args via "$@"; strict manifest decode | unit | `go test ./cmd/pr-relay -run 'TestPluginManifest\|TestPluginRunSh' -count=1` | ❌ W0 | ⬜ pending |
| 05-07-T1..T3 | 05-07 | 4 | DIST-03 | T-05-20 | action output visible (spike + conditional popup) | manual + unit | `go test ./cmd/pr-relay -run TestPlugin -count=1` | ❌ W0 | ⬜ pending |
| 05-08-T1/T2 | 05-08 | 5 | DIST-04/05 | T-05-22..24 | docs examples ⊆ goldens; all event types, commands, flags, config keys documented | unit | `go test ./cmd/pr-relay -run TestDocsDrift -count=1` | ❌ W0 | ⬜ pending |
| 05-08-T3 | 05-08 | 5 | D-25 | — | planning-doc drift fixed | grep | `grep -c "SUB-10\|SUB-11" .planning/REQUIREMENTS.md` | ✅ | ⬜ pending |
| 05-09-T1 | 05-09 | 6 | DIST-01 | T-05-SC, T-05-28 | 4 static targets, checksums, cask xattr hook | snapshot | `go run github.com/goreleaser/goreleaser/v2@v2.20.0 check` + local snapshot | ❌ W0 | ⬜ pending |
| 05-09-T2 | 05-09 | 6 | DIST-01/02 | T-05-25, T-05-26 | tag-only release, CI snapshot job, scoped permissions | CI | `ruby -ryaml -e '...'` + CI goreleaser job | ❌ W0 | ⬜ pending |
| 05-10-T1..T3 | 05-10 | 7 | DIST-02 | T-05-29..31 | LICENSE chosen by user; tap repo; PAT secret | manual | `gh secret list -R drodimin/pr-relay` | — | ⬜ pending |
| 05-11-T1..T3 | 05-11 | 8 | D-22 | T-05-32..34 | full gate + real herdr/GitHub UAT | manual + full suite | `go test -race -count=1 ./...` | ✅ | ⬜ pending |
| 05-12-T1..T3 | 05-12 | 9 | DIST-01/02/03 | T-05-35..38 | v0.1.0 artifacts verified; brew/go install/download; plugin from GitHub | manual + scripted | `gh release view v0.1.0` | — | ⬜ pending |

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
