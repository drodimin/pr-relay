---
phase: 4
slug: daemon-control-protocol-cli
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-11-04
---

# Phase 4 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (stdlib `testing` + `testing/synctest` + `httptest`; golden files via `internal/testutil.Golden` with `-update`) |
| **Config file** | none (CI: `.github/workflows/ci.yml`) |
| **Quick run command** | `go test ./internal/... -count=1` |
| **Full suite command** | `go test -race -count=1 ./...` |
| **Estimated runtime** | ~15 seconds (quick), ~60 seconds (full, race; `cmd/pr-relay` builds the binary in TestMain) |

---

## Sampling Rate

- **After every task commit:** Run `go test ./internal/<touched pkg> -count=1` (or the quick run command)
- **After every plan wave:** Run `go test -race -count=1 ./...`
- **Before `/gsd:verify-work`:** Full suite must be green on macOS and Linux CI
- **Max feedback latency:** 60 seconds

---

## Per-Task Verification Map

Task IDs are filled in by the planner/executor; requirement → test mapping comes from 04-RESEARCH.md § Validation Architecture.

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| TBD | TBD | TBD | SUB-09, DMN-08 | — | Non-PR / non-github.com links rejected with distinct messages | unit (table) | `go test ./internal/model -run TestParsePRLink -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | SUB-02 | — | Unknown event types rejected; closed always allowed | unit | `go test ./cmd/pr-relay -run TestParseEvents -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | SUB-03 | — | Outside-herdr error; socket canonicalised | unit | `go test ./internal/herdr -run TestResolveSocket -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | SUB-07 | — | Failed verification persists no subscription | unit (fake Fetcher) | `go test ./internal/poller -run TestSubscribeVerified -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | SUB-01 | — | Cold `sub` returns <10s, no stdio hang, later delivers | e2e | `go test ./cmd/pr-relay -run TestSubEndToEnd -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | SUB-05 | DoS | `unsub --all` scoped to calling pane | unit + e2e | `go test ./internal/poller -run TestUnsubscribePane -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | SUB-06 | — | N/A | golden | `go test ./cmd/pr-relay -run TestFormatLs -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | DMN-01 | — | 20 concurrent cold calls → exactly one daemon | e2e | `go test ./cmd/pr-relay -run TestConcurrentAutostart -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | DMN-07 | — | Version mismatch → single restart or clear error | e2e (two builds) | `go test ./cmd/pr-relay -run TestVersionMismatch -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | DMN-05 | — | N/A | golden + e2e | `go test ./cmd/pr-relay -run TestFormatStatus -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | DMN-06 | Info disclosure | Log file 0600, dir 0700, rotation, no token logged | unit | `go test ./internal/daemon -run TestLogging -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | ctl | Tampering | Version gate; line cap; deadline; unknown method → bad_request | unit | `go test ./internal/ctl -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | daemon | Tampering / EoP | Lock order; only ModeSocket removed; dir ownership via Lstat | integration | `go test ./internal/daemon -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | LIFE-06 | — | Check-in timing; keep resets; lifecycle ack does not | unit (synctest) | `go test ./internal/poller -run TestLifecycle -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | LIFE-07 | — | Expiry queued, polling stops, deleted on ack; config bounds | unit | `go test ./internal/poller -run TestExpiry -count=1 && go test ./internal/config -run TestLifecycle -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | LIFE-01 | — | Closed bypasses mask; sub removed after ack | unit | `go test ./internal/poller -run TestClosedRemoval -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | D-12 | Tampering | Only validated URL + durations interpolated | golden | `go test ./internal/render -run TestLifecycleLines -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | EVT-09 (lifecycle) | — | Crash between queue and ack re-delivers | e2e (extend) | `go test ./cmd/pr-relay -run TestCrash -count=1` | ✅ | ⬜ pending |
| TBD | TBD | TBD | IN-01 | — | Partial ack split-pair + no-progress fallback | unit | `go test ./internal/compose -run TestPartialAckSupersede -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | IN-02 | — | Omitted pane later resubscribed | unit | `go test ./internal/herdr -run TestEndpointOmittedPane -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | IN-03 | — | Pane-set change keeps UpSince, no poke | unit | `go test ./internal/herdr -run TestResubscribeKeepsUpSince -count=1 && go test ./internal/deliver -run TestResubscribedNoPoke -count=1` | ❌ W0 | ⬜ pending |
| TBD | TBD | TBD | Store v3 | — | v2 migrates additively; newer schema refused | unit (extend) | `go test ./internal/store -run TestMigrate -count=1` | ✅ | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `cmd/pr-relay` e2e harness v2 — strips `HERDR_*`, `XDG_RUNTIME_DIR`, `PR_RELAY_*`; short `os.MkdirTemp` socket dir; fake herdr on scratch socket (reuse `scratchHerdr`); `t.Cleanup` runs `daemon stop` and kills leftover PIDs
- [ ] Second test binary build with `-ldflags "-X main.version=..."` for mismatch tests
- [ ] `internal/ctl` test helpers (net.Pipe listener)
- [ ] Golden testdata dirs for `cmd/pr-relay` formatting and render lifecycle lines

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Live delivery into a real herdr pane from a real PR | SUB-01 | CI must never prompt real panes; needs a real PR and gh auth | In a scratch herdr session: `pr-relay sub <real PR>`, comment on the PR, confirm the update appears in the pane |
| Agent sandbox reachability of the control socket | SUB-01 | Depends on the user's agent sandbox config (LOW confidence) | Run `pr-relay sub` from a sandboxed Claude Code / Codex pane and confirm it connects or fails clearly |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 60s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
