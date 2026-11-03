---
phase: 03-herdr-client-delivery
plan: 03
subsystem: compose
tags: [render, compose, supersede, coalescing, flood-guard, golden]
requires: [internal/render, internal/store.PendingEvent, internal/model]
provides:
  - render.Each (aligned rendering), render.DroppedLine, render.HeldLine
  - compose.View, compose.Supersede
  - compose.Group, compose.Limits, compose.Result, compose.DefaultMaxBytes, compose.Message
affects: [internal/deliver (Wave 2 consumer)]
tech-stack:
  added: []
  patterns: [pure package (no clock, no I/O), golden-file tests, named rule table tests]
key-files:
  created:
    - internal/compose/doc.go
    - internal/compose/supersede.go
    - internal/compose/compose.go
    - internal/compose/supersede_test.go
    - internal/compose/compose_test.go
    - internal/compose/testdata/messages.golden
  modified:
    - internal/render/render.go
    - internal/render/render_test.go
    - internal/render/testdata/lines.golden
decisions:
  - "The flood guard reserves room for HeldLine only when the full message exceeds MaxBytes, so a message that fits is never held"
  - "ci events with an empty HeadOID are never dropped by R1, even with a non-empty view (never drop on unknown state)"
  - "Supersede sorts a copy by Seq, so its output is in Seq order whatever the input order"
metrics:
  duration: ~15m
  completed: 2026-11-02
  tasks: 2
  files: 9
---

# Phase 3 Plan 03: Pure compose layer Summary

This plan adds a pure `internal/compose` package. It applies the DLV-07 supersede rules (stale-head CI, settled-over-failed, rerun dedupe, conflict cancellation). It also coalesces every pending event of one pane into a single `\n`-joined prompt (DLV-04), enforces the 12 KiB D-12 flood guard with a `HeldLine` note, and computes per-subscription acks. All of the text comes from the new `render.Each`, `DroppedLine` and `HeldLine` helpers.

## Tasks

| Task | Name | Commits |
|------|------|---------|
| 1 | render.Each, DroppedLine and HeldLine | 6cbf09a (RED), 429465d (GREEN) |
| 2 | Pure compose package: Supersede, Message, flood guard, acks | 5846b91 (RED), 43e555d (GREEN) |

## What was built

- `render.Each(ref, events)` renders one element per event. An event with an unknown type gives `""`, and collision qualification is computed across the whole slice. `Lines` is now Each with the empty strings filtered out, and the existing golden lines are byte-identical. The golden diff contains only the four appended note lines.
- `render.DroppedLine(ref, n)` produces `[pr-relay] owner/repo#N note: n earlier update(s) dropped <url>`. `render.HeldLine(n)` produces `[pr-relay] note: n more update(s) held (message size limit), they arrive with the next delivery`.
- `compose.Supersede(view, events)` implements rules R1-R4 and R8 and never mutates its input. The rules are listed in the package doc, and `TestSupersede` checks them in a named table.
- `compose.Message(groups, lim)`:
  - It orders groups by their lowest pending Seq, with the drop marker counting, and breaks ties by SubID.
  - It applies Supersede to each group and puts the DroppedLine first in its group.
  - It adds whole lines in order until the next one does not fit, then appends `HeldLine(k)`.
  - Acks per group:
    - A fully included group acks `UpToSeq`.
    - A partially included group acks the first held Seq minus 1.
    - A group with nothing included gets no entry.
  - If every event was superseded, Text is `""` and the acks still cover each group.
- `messages.golden` holds four messages: two PRs coalesced, superseded CI with a cancelled conflict, a drop marker, and an overflow with a held note.

## Deviations from Plan

**1. [Rule 1 - Bug] Off-by-one and needless holds in the flood-guard budget**
- **Found during:** Task 2
- **Issue:** The planned check `len(text)+1+len(line) <= MaxBytes - len(HeldLine)` leaves no room for the `\n` before the held note, so the message could reach MaxBytes+1. It also reserved space on every message, which could hold lines from a message that fits in full.
- **Fix:** The reserve is `len(HeldLine(total)) + 1`, and it applies only when the full joined text exceeds MaxBytes. `TestFloodGuardNoHoldWhenItFits` covers this.
- **Files modified:** internal/compose/compose.go
- **Commit:** 43e555d

**2. [Rule 2] R1 keeps ci events with an empty HeadOID**
- This matches the "never drop on unknown state" principle from R8. A ci event with no head is not provably stale, even when the view has a head.

No other deviations.

## Known edge (accepted, documented)

- A partial ack can split a cancelling conflict pair. For example, raised at Seq 3 is acked below the cutoff while cleared at Seq 10 is held, so the next delivery shows `conflict: cleared` on its own. This needs a flood-guard overflow in the middle of a conflict pair. It is rare, and the extra line is harmless. A re-rendered drop note after a partial ack is the other accepted side effect named in the plan.

## TDD Gate Compliance

Each task has a RED `test(03-03)` commit followed by a GREEN `feat(03-03)` commit. No refactor commits were needed.

## Verification

- `go vet ./internal/compose/...` passes, and `go test ./... -count=1` passes in every package.
- The purity grep (no net, os, slog, poller, herdr or deliver imports, and no `time.Now`) returns nothing.

## Self-Check: PASSED

- All 9 key files exist. The commits 6cbf09a, 429465d, 5846b91 and 43e555d are present in `git log`.
