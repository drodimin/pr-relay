# Phase 3: herdr Client & Delivery - Discussion Log

> **Audit trail only.** Do not use this log as input to planning, research or execution agents.
> The decisions are captured in CONTEXT.md. This log keeps the alternatives that were considered.

**Date:** 2026-11-02
**Phase:** 03-herdr-client-delivery
**Areas discussed:** Typing-collision guard, Message size ceiling, Pane identity & restarts

---

## Typing-collision guard

| Option | Description | Selected |
|--------|-------------|----------|
| Quiet period only | Deliver when idle/done and nothing has changed for N s; ignore `focused` | ✓ |
| Unfocused + quiet period | Also require the pane to be unfocused in herdr; the focused pane may never get updates | |
| Focused → longer quiet period | Short quiet period when unfocused, long one when focused | |

| Quiet time | Selected |
|---|---|
| ~10 seconds | ✓ |
| ~30 seconds | |
| Configurable, Claude picks the default | |

| Disclosure | Selected |
|---|---|
| Skill doc + README only | ✓ |
| Also a per-sub opt-out flag | |

**User's choice:** Quiet period of about 10 s, `focused` ignored, documented only.

---

## Message size ceiling

| Option | Description | Selected |
|--------|-------------|----------|
| Send it anyway, whole | Keep Phase 1 D-03; only a high hard flood guard | ✓ |
| Summarize per event type above the limit | Collapse to counts, which relaxes D-03 | |
| Split across several prompts | Each under 800 B, each a separate turn | |

| Design around Claude Code paste-collapse? | Selected |
|---|---|
| No: design for the agent, not the viewer | ✓ |
| Yes: keep the message readable to the human | |

**User's choice:** Always one whole prompt. The S6 800 B recommendation is overridden.

---

## Pane identity & restarts

| Identity | Selected |
|---|---|
| agent_session; fallback to same agent kind | ✓ |
| agent_session; fallback to terminal_id (strict) | |
| Pane id only | |

| Pane exists, no agent | Selected |
|---|---|
| Hold, keep queued | ✓ |
| Cancel immediately | |
| Grace window, then cancel | |

| LIFE-08 undeliverable threshold | Selected |
|---|---|
| ~30 minutes | ✓ |
| ~10 minutes | |
| Immediately | |

| Cancellation notice | Selected |
|---|---|
| Log only | ✓ |
| Tell the new agent in that pane | |

---

## Claude's Discretion

- Stale-update rules (DLV-07). The area was offered but not selected.
- The herdr client's structure, reconnect backoff and `events_lost` reconcile.
- Confirming submission with `prompt wait: until working`.
- The cadence hook for "can receive".
- The flood-guard size and its overflow rendering.

## Deferred Ideas

None.
