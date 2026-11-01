---
phase: 01-toolchain-spikes-pure-core
plan: 08
subsystem: spikes
tags: [spikes, herdr, graphql, setsid, dlv-05, dlv-06, evt-01]
requires:
  - "01-01: Go module, gitignored spikes/out/"
  - "01-02: internal/ghquery/pr.graphql (the PRState query measured in S4)"
provides:
  - "01-SPIKES.md: S0-S7 answered (15 Answer lines) with evidence, phase implications and a Summary table"
  - "spikes/lib.sh guard (prspike socket only, pane must exist via pane list, # payloads, hr wrapper)"
  - "spikes/run-interactive.sh (human-driven) and spikes/auto-lib.sh (agent-driven) spike drivers"
  - "spikes/setsid-probe: Go Setsid re-exec heartbeat probe"
  - "spikes/s4-graphql-cost.sh: dryRun and real cost and latency for 1/5/20 aliased PRs"
affects: [phase-02 poller batching, phase-03 delivery, LIFE-02 identity guard, phase-04/05 daemon lifecycle]
tech-stack:
  added: []
  patterns:
    - "Session isolation by socket path, never by pane id"
    - "Every herdr call goes through a guarded hr wrapper"
key-files:
  created:
    - spikes/lib.sh
    - spikes/run-interactive.sh
    - spikes/auto-lib.sh
    - spikes/s4-graphql-cost.sh
    - spikes/setsid-probe/main.go
    - spikes/README.md
    - .planning/phases/01-toolchain-spikes-pure-core/01-SPIKES.md
  modified: []
decisions:
  - "Phase 2 batches active PRs in aliased GraphQL queries of up to 20 (cost 5); about 300 points/h at 20 PRs every 60 s"
  - "agent.prompt concatenates with a half-typed draft, so Phase 3 must hold delivery while the pane is focused or recently active"
  - "Phase 3 waits with agent.wait using explicit until [idle, done], never a bare wait (it returns immediately when blocked)"
  - "agent_blocked means queue and retry; agent_not_found means the pane has no agent and is handled separately"
  - "The LIFE-02 identity guard keys on socket path + pane_id + agent_session; terminal_id changes on every herdr restart"
  - "Self re-exec with Setsid survives herdr session stop, so Phase 4/5 needs an explicit idle-exit policy"
  - "Embedded newlines arrive as one submission in Claude Code; flood ceiling about 800 B because Claude collapses input of 900 B or more into a pasted block"
metrics:
  duration: "about 25 min of executor time across two days (Task 1 on 2026-10-31, Tasks 2-3 on 2026-11-01)"
  completed: 2026-11-01
  tasks: 3
  files: 7
---

# Phase 1 Plan 08: Toolchain spikes S0-S7 Summary

A written spike report backed by guarded probes against the scratch herdr session `prspike`. It found three things. herdr's agent.prompt merges with a user's half-typed draft. An `agent.wait --until idle --until done` call spans permission-blocked states. pane_id and agent_session survive a herdr restart but terminal_id does not. The report also covers setsid survival and multi-line delivery, and it measures the PRState GraphQL query at about 0.25 points per PR.

## Tasks

| Task | Name | Commit |
|---|---|---|
| 1 | Guarded spike scripts, setsid probe, S4 GraphQL cost run, report skeleton | a73519c |
| 2 | Interactive spikes in the prspike session (human S0/S1, then agent-driven) | f0d47bd (guard fix), eaa788b (auto-lib.sh); logs in spikes/out/ (gitignored) |
| 3 | Spike answers, evidence, implications and Summary table in 01-SPIKES.md | ee26558 |

## Spike Summary table

| Spike | Question | Answer | Gates (phase / requirement) |
|---|---|---|---|
| S0 | Guarded socket reaches only prspike | YES | Phase 3: store the socket path per subscription; pane existence via `pane list` |
| S1 | agent.prompt merges with a half-typed draft | YES (concatenated and submitted); bare shell `agent_not_found` | Phase 3 / DLV-05, DLV-06: hold delivery while focused or recently active |
| S2a | `wait --until idle --until done` spans blocked | YES (returned after approval, 14.3 s, done) | Phase 3: use agent.wait with explicit `until`, not agent.get polling |
| S2a | Default `agent wait` returns immediately when blocked | YES (17 ms, blocked) | Phase 3: never call wait without `until` |
| S2b | `prompt --wait` on blocked returns `agent_blocked` | YES (nothing typed into the dialog) | Phase 3: `agent_blocked` means queue and retry |
| S2b | Bare shell returns `agent_prompt_stalled` | NO (`agent_not_found`; stalled not observed) | Phase 3: handle `agent_not_found` separately; treat stalled as retryable |
| S3 | pane_id / terminal_id / agent_session stable across restart | YES / NO / YES | LIFE-02 identity guard keyed on socket + pane_id + agent_session |
| S4 | PRState query valid; cost per PR and batched | YES (1 PR = 1, 5 = 1, 20 = 5 points) | Phase 2 / EVT-01: batches of up to 20, about 300 pts/h at 20 PRs / 60 s |
| S5 | setsid child survives herdr session stop | YES (+21 s, ppid=1) | Phase 4/5: self re-exec works; needs an explicit idle-exit policy |
| S6 | Embedded `\n` gives a single submission | YES (2 and 30 lines) | Phase 3 / DLV-05: no forced flattening needed for Claude Code |
| S6 | Largest accepted single payload | UNKNOWN upper bound; 16384 B accepted | Phase 3: flood ceiling about 800 B (paste-collapse at 900 B) |
| S7 | Prompt accepted after a completed turn | YES (316 ms) | Phase 3: idle/done is a valid delivery state |

Open for Phase 3 planning: the `agent_prompt_stalled` trigger was never observed, the S6 upper payload limit was not found, `done`-vs-`idle` decay on a pane not focused in herdr was not isolated, and only Claude Code was tested (no Codex).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] The pane guard refused bare-shell panes**
- **Found during:** Task 2
- **Issue:** `guard_target` checked `herdr agent list`, which includes only panes with a detected agent, so bare-shell pane B (`w1:p2`) was always refused (exit 98).
- **Fix:** The guard checks `herdr pane list` instead. The socket check is unchanged.
- **Files modified:** spikes/lib.sh
- **Commit:** f0d47bd

### Process deviations (Task 2, approved by the user)

- **Agent-driven run after S1.** The human ran S0 and S1 through run-interactive.sh (S1 answers: A=c concatenated, B=x agent_not_found). After that, at the user's request, the orchestrator drove S7, S2a, S2b, S6, S5 and S3 itself with the new `spikes/auto-lib.sh` (commit eaa788b). It typed with `herdr pane send-text`, approved or denied with `herdr pane send-keys` (enter / esc) and observed with `herdr pane read`, all through the same prspike-only `hr` guard. These answers are logged as `ANSWER(auto)` with the evidence observed. The human only re-attached prspike for S3. This departs from D-16's "human performs the physical steps", which the user explicitly waived.
- **Manual mode for S2.** Pane A's Claude Code was switched from auto mode to manual mode (Shift+Tab) before S2, so that permission prompts would appear.
- **One shared session stop for S3 and S5.** The setsid probe started first, the agent and pane IDs were snapshotted, then one `herdr session stop prspike` ran (by the auto-driver, prspike only), followed by a 20 s heartbeat check and the human's re-attach. The setsid child was killed after S5 (0 remaining).
- **S7 focus caveat.** herdr reports `focused=true` for `w1:p1` because it was the only pane focused within herdr. "Unfocused" in S7 means the OS terminal focus was elsewhere.
- **S2b expectation.** The plan expected `agent_prompt_stalled` for a bare shell. herdr returns `agent_not_found` instead. This is recorded as NO with a note, not treated as a failure.

## Threat model compliance

- T-01-34 / T-01-36: every herdr call went through `hr` with the prspike socket. The only `session stop` named prspike explicitly, and no default-session pane was addressed.
- T-01-35: all spike payloads were `# pr-relay spike:` prefixed. The S2a permission triggers (`Please run the shell command: ls /tmp`, `ls /var`) went only to the scratch agent in an empty temp dir.
- T-01-37: 01-SPIKES.md has no `/Users/` paths and no token-like strings (checked by grep). Raw logs stay in gitignored spikes/out/.
- T-01-38: the setsid child was killed explicitly after S5.

## Known Stubs

None. The spike scripts are throwaway probes by design (spikes/README.md).

## Self-Check: PASSED

- FOUND: .planning/phases/01-toolchain-spikes-pure-core/01-SPIKES.md, spikes/lib.sh, spikes/auto-lib.sh, spikes/run-interactive.sh, spikes/s4-graphql-cost.sh, spikes/setsid-probe/main.go, spikes/README.md
- FOUND commits: a73519c, f0d47bd, eaa788b, ee26558
- Task 3 automated verify: PASS (15 Answer lines, no "UNKNOWN (pending)", Summary table present, no tokens, no /Users/ paths)
