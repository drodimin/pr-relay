---
phase: 3
slug: herdr-client-delivery
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-11-02
---

# Phase 3 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (stdlib `testing` + `testing/synctest`, go1.27.3) |
| **Config file** | none |
| **Quick run command** | `go test ./internal/herdr/... ./internal/compose/... ./internal/deliver/... ./internal/poller/... ./internal/store/...` |
| **Full suite command** | `go test -race ./...` |
| **Estimated runtime** | ~10 seconds (quick), ~60 seconds (full, race) |

---

## Sampling Rate

- **After every task commit:** Run the quick run command
- **After every plan wave:** Run `go test -race ./...`
- **Before `/gsd:verify-work`:** Full suite must be green, plus one manual `pr-relay dev deliver` run against a scratch herdr session
- **Max feedback latency:** 10 seconds

---

## Per-Task Verification Map

Filled in by the planner and executor per task. Requirement → test mapping (from RESEARCH.md):

| Requirement | Behavior | Test Type | Automated Command | File Exists | Status |
|-------------|----------|-----------|-------------------|-------------|--------|
| DLV-01 | Coalesced text reaches fake herdr via `agent.prompt` | integration (synctest) | `go test ./internal/deliver -run TestDeliverPrompts` | ❌ W0 | ⬜ pending |
| DLV-02 | Held while working/blocked; idle/done + quiet period gate | synctest | `go test ./internal/deliver -run TestGate` | ❌ W0 | ⬜ pending |
| DLV-03 | Never prompts unknown / launch_pending / bare shell | synctest | `go test ./internal/deliver -run TestNeverPrompt` | ❌ W0 | ⬜ pending |
| DLV-04 | Many pending updates → one prompt; AckMany | synctest + unit | `go test ./internal/deliver -run TestCoalesce; go test ./internal/compose` | ❌ W0 | ⬜ pending |
| DLV-07 | Supersede rules + flood guard | pure + golden | `go test ./internal/compose -run 'TestSupersede|TestFloodGuard'` | ❌ W0 | ⬜ pending |
| DLV-08 | herdr restart / events_lost: reconnect, reconcile, no loss | unix socket + synctest | `go test ./internal/herdr -run 'TestEndpointReconnect|TestEventsLost'; go test ./internal/deliver -run TestHerdrRestart` | ❌ W0 | ⬜ pending |
| DLV-09 | Two herdr sessions, same pane id, isolated delivery | synctest | `go test ./internal/deliver -run TestTwoSessions` | ❌ W0 | ⬜ pending |
| LIFE-02 | pane.closed / identity mismatch cancels + drops pending | synctest | `go test ./internal/deliver -run TestCancel` | ❌ W0 | ⬜ pending |
| LIFE-04 | Push-time pane gone cancels; dial failure keeps queue | synctest | `go test ./internal/deliver -run 'TestPushTimeGone|TestUnreachableKeeps'` | ❌ W0 | ⬜ pending |
| LIFE-08 | Undeliverable PR polled at slowest rate | unit (fake clock) | `go test ./internal/poller -run TestUndeliverableCadence` | ❌ W0 | ⬜ pending |
| support | herdr client wire quirks | unit | `go test ./internal/herdr` | ❌ W0 | ⬜ pending |
| support | Store schema v1→v2 migration | unit | `go test ./internal/store -run TestMigrate` | partial | ⬜ pending |
| support | PendingWake fires only on new events | unit | `go test ./internal/poller -run TestPendingWake` | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `internal/herdr/herdrtest/server.go` — fake herdr reproducing verified quirks (one response per conn, read-only subscribe conn, `pane_not_found`/`agent_not_found` codes, `agent.wait` semantics, both event spellings, `events_lost`, Stop/Restart, unix + `net.Pipe` modes)
- [ ] `internal/compose/testdata/` fixtures + `messages.golden`
- [ ] `internal/store/testdata/state-v2.json` + migration test
- [ ] Opt-in live test gated on `PR_RELAY_HERDR_SCRATCH_SOCKET` (refuse default socket / paths without `/sessions/`)

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| End-to-end delivery to a real scratch pane (SC 1-5) | DLV-01..09, LIFE-02/04/08 | Needs a live herdr and a real agent; never in CI | `pr-relay dev deliver --socket ~/.config/herdr/sessions/<scratch>/herdr.sock --pane <id> <PR-URL>`, then exercise idle/working, herdr restart, pane close |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 10s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
