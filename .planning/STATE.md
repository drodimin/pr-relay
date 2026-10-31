# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-10-31)

**Core value:** An agent runs `pr-relay sub <PR link>` once, returns immediately, and later receives a relevant PR update in its pane without spending tokens on polling. One poller serves every agent on the machine.
**Current focus:** Phase 1 - Toolchain, Spikes & Pure Core

## Current Position

Phase: 1 of 5 (Toolchain, Spikes & Pure Core)
Plan: 0 of TBD in current phase
Status: Ready to plan
Last activity: 2026-10-31 - Roadmap created

Progress: [░░░░░░░░░░] 0%

## Performance Metrics

**Velocity:**
- Total plans completed: 0
- Average duration: - min
- Total execution time: 0.0 hours

**By Phase:**

| Phase | Plans | Total | Avg/Plan |
|-------|-------|-------|----------|
| - | - | - | - |

**Recent Trend:**
- Last 5 plans: -
- Trend: -

*Updated after each plan completion*

## Accumulated Context

### Decisions

Decisions are logged in PROJECT.md Key Decisions table.
Recent decisions affecting current work:

- [Roadmap]: 5 horizontal-layer phases (spikes+pure core, state+poller, herdr+delivery, daemon+CLI, packaging)
- [Roadmap]: Flow-agnostic primitives; only `--events` selects; PR link required; `keep` command for check-ins
- [Research]: Single atomic JSON state file behind a `Store` interface; GraphQL-only polling in v1

### Pending Todos

None yet.

### Blockers/Concerns

- [Phase 1]: Go is not installed on the dev machine; must be installed first.
- [Phase 1]: Spike results (typing collision, pane/terminal_id stability, agent.wait while blocked, GraphQL cost) gate Phase 2 and 3 design.

## Deferred Items

| Category | Item | Status | Deferred At |
|----------|------|--------|-------------|
| *(none)* | | | |

## Session Continuity

Last session: 2026-10-31
Stopped at: Roadmap created
Resume file: None
