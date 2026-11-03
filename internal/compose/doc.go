// Package compose turns the pending events of one herdr pane into a single
// prompt. Everything here is pure: no clock, no I/O, no locking.
//
// Supersede rules (DLV-07), applied per subscription before rendering:
//   - R1: ci.failed and ci.settled whose HeadOID is not the PR's current head
//     (View.HeadOID) are dropped, because a newer push superseded them. An
//     event with an empty HeadOID is never dropped by this rule.
//   - R2: for one head, a ci.settled drops every ci.failed for that head with
//     a lower Seq. The settled line already names every failing check, and a
//     passing settle makes the failures stale.
//   - R3: several ci.settled for one head (a rerun) keep only the last.
//   - R4: conflict events reduce to their net change. If the last state
//     equals the state before the first event (raised then cleared), all are
//     dropped; otherwise only the last is kept. All conflict events are
//     dropped when the PR is no longer open (View.State set and not "OPEN").
//   - R5: commits, review and comment are history and are never dropped.
//   - R6: closed is never dropped.
//   - R7: a Phase 2 drop marker (Group.Dropped > 0) renders as
//     render.DroppedLine at the start of that PR's group. Message applies it.
//   - R8: an empty View (no head, no state: record missing or never
//     baselined) applies no head-based or state-based rule. Unknown state
//     never drops anything.
//
// Superseded events are still covered by the ack, so they leave the queue
// silently. Message never formats text itself: every line comes from
// internal/render, and compose only orders, bounds and joins lines.
package compose
