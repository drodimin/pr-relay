# Phase 1: Toolchain, Spikes & Pure Core - Discussion Log

> **Audit trail only.** Do not use this file as input to planning, research or execution agents.
> Decisions are captured in CONTEXT.md. This log preserves the alternatives considered.

**Date:** 2026-10-31
**Phase:** 01-toolchain-spikes-pure-core
**Areas discussed:** Message shape, CI event semantics, Reviews/comments & bots, Spikes & toolchain

---

## Message shape

| Option | Description | Selected |
|--------|-------------|----------|
| One line | Fewest tokens, no newlines | |
| Header + bullet lines | Readable, but puts newlines into agent.prompt | |
| You decide | Claude picks | ✓ |

| Option (PR title) | Description | Selected |
|--------|-------------|----------|
| No title | Only identity + URL, no third-party free text | ✓ |
| Sanitized title capped ~60 | More context | |
| Title only in check-in/expiry | Limited use | |

| Option (coalescing) | Description | Selected |
|--------|-------------|----------|
| One clause per event type | Single line joined by "; " | |
| One line per event, single prompt | Self-contained lines | ✓ |
| You decide | | |

| Option (many items) | Description | Selected |
|--------|-------------|----------|
| First few + "+N more" | Bounded | |
| Everything, then cap line | | |
| Counts only | | |
| (clarified) One line per item | Each item its own line and link | ✓ |
| (clarified) Header + item lines | | |
| (clarified) Wrap with sane cap | | |

**Notes:** The user said "just do multiple lines" and chose one line per item with no truncation.

---

## CI event semantics

| Option (when ci.failed fires) | Selected |
|--------|----------|
| Fail-fast, once per check per SHA | ✓ |
| Only at settle | |
| Fail-fast with short settle window | |

| Option (flaky rerun passes) | Selected |
|--------|----------|
| Nothing extra; ci.settled reports final state | ✓ |
| Emit ci.recovered | |
| Re-fire if fails again | |

| Option (conclusion mapping) | Selected |
|--------|----------|
| cancelled/skipped/neutral = neutral | |
| cancelled = failure | |
| You decide | ✓ |

| Option (which checks) | Selected |
|--------|----------|
| All checks + commit statuses via rollup | |
| Check runs only | ✓ |

---

## Reviews, comments & bots

| Option (review with inline comments) | Selected |
|--------|----------|
| One review line with inline count | ✓ |
| Review line + one line per inline | |

| Option (standalone comments) | Selected |
|--------|----------|
| One line per comment | ✓ |
| One line per author with count | |

| Option (bots) | Selected |
|--------|----------|
| Bots = humans in v1 | ✓ |
| Mark bots in line | |

| Option (edits/deletes) | Selected |
|--------|----------|
| Ignore | ✓ |
| Notify on edit | |

---

## Spikes & toolchain

| Option (Go install) | Selected |
|--------|----------|
| brew install go | ✓ |
| Official go.dev installer | |
| Version manager | |

**Notes:** The user asked whether end users need Go. They don't: Go is dev-only, and users get a prebuilt binary. `go install` is just one optional install route.

| Option (spike driver) | Selected |
|--------|----------|
| Agent scripts, user assists at checkpoints | ✓ |
| User runs by hand | |
| Fully automated only | |

| Option (CI / remote) | Selected |
|--------|----------|
| Create public GitHub repo in Phase 1 | ✓ |
| Private repo for now | |
| Workflow file only, push later | |

| Option (fixtures) | Selected |
|--------|----------|
| Scratch PRs on the new repo | |
| Real public PRs + scratch | |
| Hand-written JSON | |
| (free text) Scratch PR on a dummy repo the user supplies | ✓ |

**Notes:** The review, comment and bot scenarios need a second account or a bot, because of EVT-08 and because GitHub doesn't allow reviewing your own PR.

---

## Claude's Discretion

- single-event layout (settled as one line)
- mapping of check conclusions
- ci.settled when a head SHA has zero checks
- wording of `commits` events and force-pushes
- an optional high flood-guard ceiling
- per-field length caps and the unicode stripping set
- where the spike report lives, the package layout, the lint config, and the minimum Go version

## Deferred Ideas

None.
