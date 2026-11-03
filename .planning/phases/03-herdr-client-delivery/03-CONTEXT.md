# Phase 3: herdr Client & Delivery - Context

**Gathered:** 2026-11-02
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 3 takes the pending events that Phase 2 already queues durably for each subscription and delivers them into the subscribed herdr pane with `agent.prompt`. Delivery has to:
- happen only when the pane's agent is free (idle or done), and never to a blocked agent, a bare shell, or a pane with unknown status
- send one coalesced message per pane, rendered from the PR's current state at delivery time
- survive herdr restarts and dropped sockets without losing queued updates, then reconcile pane state on its own
- work with several herdr sessions (sockets) at once
- cancel a subscription when its pane closes, is gone, or now holds a different agent
- report PRs with no deliverable pane to the poller so they drop to the slowest rate

Requirements: DLV-01, DLV-02, DLV-03, DLV-04, DLV-07, DLV-08, DLV-09, LIFE-02, LIFE-04, LIFE-08.

Out of scope:
- the `sub`/`unsub`/`ls`/`status` CLI, daemon auto-start and the control socket (Phase 4)
- LIFE-01 cleanup after merged/closed, and LIFE-06/07 check-ins and expiry (Phase 4)
- the plugin and docs (Phase 5)
- `notification.show` and `pane.report_metadata` (v2: CONV-04, CONV-05)

A headless way to exercise delivery against a scratch herdr session is in scope. Extend the hidden `dev` command or add a sibling to it. Phase 4 wires delivery into the real daemon.

</domain>

<decisions>
## Implementation Decisions

### Carried forward (already locked, do not re-ask)
- **D-01:** Message format comes from Phase 1 D-01 to D-05. Every line is `[pr-relay] owner/repo#N <event>: <detail> <url>` with one line per item. A coalesced delivery is several such lines in **one** `agent.prompt`. There is no header, no PR title, and no bodies.
- **D-02:** Every subscription stores the herdr socket path captured at `sub` time. Panes are always addressed as (socket, pane_id) and never by pane_id alone, because pane ids such as `w1:p1` repeat across sessions (S0). One daemon keeps one herdr client per distinct socket (DLV-09).
- **D-03:** Use `agent.wait` with an explicit `until: [idle, done]` as the "agent is free again" signal, together with `events.subscribe` (`pane.agent_status_changed`, `pane.closed`). Never call `agent.wait` without `until`, because it returns at once while the agent is blocked and becomes a busy loop (S2a).
- **D-04:** How `agent.prompt` errors map to actions (S2b):
  - `agent_blocked`: keep the update queued and retry when free. This is never a failure.
  - `agent_not_found`: the pane has no agent right now. See D-13.
  - `agent_prompt_stalled`: has never been observed, so treat it as retryable.
  - pane or tab not found: cancel the subscription (D-15).
- **D-05:** Phase 2 already handles pending-event durability: there is no TTL, the cap is 200 per subscription, and a "some updates dropped" marker is kept (P2 D-19). Delivery acks with `Engine.Ack(subID, upToSeq)` only after `agent.prompt` succeeds. A crash between the prompt and the ack may re-deliver once, which is accepted. A crash must never lose an update.
- **D-06:** "Slowest rate" means 16 min (P2 D-11).
- **D-07:** Message lines never start with `#`, which Claude Code renders as a markdown heading (S6). The `[pr-relay]` tag already satisfies this.

### Typing-collision guard
- **D-08:** Use **a quiet period only**. Deliver when the agent status is idle or done **and** nothing has changed for the quiet period: no status change and no movement in `completion_seq` / `state_change_seq`. **Ignore herdr's `focused` flag entirely.** herdr always reports the only pane as focused, so a focus rule would hold its updates forever.
- **D-09:** The quiet period is **about 10 seconds**. Claude chooses whether to make it configurable in `config.toml`. If it is configurable, keep 10 s as the default.
- **D-10:** The typing-collision risk is accepted and documented: a delivery can still land on a half-typed draft and be submitted with it (S1). Mention it only in the skill doc and README (Phase 5), and add no runtime knob or per-subscription opt-out. The `[pr-relay]` tag keeps a merged message readable.

### Message size
- **D-11:** **Always send the whole coalesced message as one prompt.** Don't split it into several prompts or summarise lines. Phase 1 D-03 (no "+N more") applies to coalesced deliveries too.
- **D-12:** **Don't design around Claude Code's paste-collapse at 900 B or more.** pr-relay is agent-agnostic and the agent is the reader. This **overrides the S6 recommendation** of an 800 B ceiling. Keep only a high hard flood guard as a safety net: herdr accepted 16 KB in S6, so the ceiling can sit at or below that. Claude chooses the exact value and the overflow behaviour, but the guard should almost never fire, and when it does it must document itself in the message.

### Pane identity & restarts
- **D-13:** **Identity check before every delivery.** If an `agent_session` was captured at `sub` time, the current one must match. If none was captured (herdr couldn't identify the agent), accept the same socket and pane_id **plus the same agent kind** (for example `codex`). Don't use `terminal_id` as the check, because it changes on every herdr restart (S3). The accepted risk is that a different agent of the same kind, started in the same pane, inherits the updates. A mismatch cancels the subscription (LIFE-02).
- **D-14:** **The pane exists but no agent is running** (`agent_not_found`, for example the agent exited and left a bare shell): **keep the updates queued** and don't cancel. If an agent comes back, D-13 decides: the same agent resumes delivery, and a different one cancels. While the pane has no agent it counts as undeliverable for LIFE-08.
- **D-15:** **The pane or tab is gone** (`pane.closed` event, or herdr reporting the pane or tab missing when a push is about to happen): cancel the subscription and drop its pending updates. Don't retry or redirect them (LIFE-02, LIFE-04). If herdr itself is unreachable, keep everything queued (DLV-08). Make the pane-existence check through `pane list`, not `agent get`, because a bare shell is `agent_not_found` (S0).
- **D-16:** **Undeliverable threshold for LIFE-08: about 30 minutes.** When every subscribed pane for a PR has been unable to receive for about 30 min, the PR polls at the slowest rate. "Unable to receive" means blocked, herdr unreachable, or no agent in the pane. Short permission prompts don't slow polling down. The normal cadence resumes as soon as any pane can receive again.
- **D-17:** **Cancellations caused by identity mismatch or a missing pane are only logged.** No message goes to the pane, because whoever is there now isn't the subscriber. The log entry should carry enough detail (sub ID, PR, reason) for Phase 4 `status`/`ls` to explain the cancellation later.

### Claude's Discretion
- **Stale-update rules (DLV-07).** The user did not discuss this, so Claude decides which pending events are dropped or rewritten when the PR's current state at delivery supersedes them. Guidance:
  - A `ci.failed` or `ci.settled` for a head SHA that is no longer the head is stale. Drop it, or report it against the current head.
  - A conflict raised and then cleared before delivery cancels out.
  - Reviews, comments and commits are history, not state, and are never dropped.
  - `closed` always gets through.
  - The Phase 2 "some updates dropped" marker is rendered as a line.

  Keep the rules pure and fixture-tested, in the style of `diff` and `render`.
- How the herdr NDJSON client is structured (one events connection plus short-lived request connections per socket, as CLAUDE.md suggests), along with reconnect backoff and the `events_lost` reconcile (re-subscribe, then `agent.get` / `pane list` for each pane that has queued messages).
- The `events.subscribe` failure mode where one missing pane fails the whole subscribe. Handle it by checking panes first, or by subscribing without pane filters.
- Whether to confirm submission with `agent.prompt` `wait: {until: working}` (S2b, about 0.5 s). Recommended.
- How "can receive" state is fed into the poller's cadence (a new Engine hook).
- The exact flood-guard size and its overflow rendering (D-12).

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project scope & requirements
- `.planning/PROJECT.md`: vision, constraints (agent-agnostic, low resource use)
- `.planning/REQUIREMENTS.md`: DLV-01 to DLV-04, DLV-07 to DLV-09, LIFE-02, LIFE-04, LIFE-08
- `.planning/ROADMAP.md` §Phase 3: goal and the 5 success criteria
- `CLAUDE.md` §herdr client: socket resolution, request/response framing, a separate long-lived events connection, `events_lost` reconnect and reconcile, the whole-subscribe-fails-on-missing-pane caveat. Also §Testing: a fake herdr server over a temp unix socket, `testing/synctest`, and the opt-in real-herdr test gated by `HERDR_ENV`.

### Spike evidence (gates this phase)
- `.planning/phases/01-toolchain-spikes-pure-core/01-SPIKES.md`:
  - S0: socket isolation, and `pane list` versus `agent get`
  - S1: typing collision
  - S2a/S2b: `agent.wait` with `until`, `agent_blocked`, `agent_not_found`
  - S3: identity stability across restart
  - S6: multi-line delivery and payload size. The 800 B ceiling is overridden by D-12.
  - S7: an idle or done agent is a valid target
  - "Open for Phase 3 planning": `agent_prompt_stalled` and whether `done` persists

### Prior phase decisions
- `.planning/phases/01-toolchain-spikes-pure-core/01-CONTEXT.md`: message shape D-01 to D-05
- `.planning/phases/02-durable-state-github-poller/02-CONTEXT.md`: pending/ack outbox (D-03, D-19), cadence (D-09 to D-13), subscriptions (D-16 to D-18)

### Research
- `.planning/research/ARCHITECTURE.md`: router and delivery responsibilities
- `.planning/research/PITFALLS.md`: typing collision, prompt injection
- `.planning/research/STACK.md`: library versions

### External
- herdr socket API: https://herdr.dev/tui/docs/socket-api/, and local `herdr api schema --json` (herdr 0.9.5, protocol 22) for exact payloads (`agent.prompt`, `agent.get`, `agent.wait`, `events.subscribe`, `pane.list`)

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- `internal/poller/engine.go`: `Engine.Pending(subID) (Drain, error)`, `Engine.Ack(subID, upToSeq)`, `Engine.Unsubscribe`, `Engine.List`, and `Engine.Wake()` (a channel that fires when new pending events land). These are the delivery-side outbox API.
- `internal/store/state.go`: `Subscription` already stores `HerdrSocket`, `PaneID`, `AgentSession`, `TerminalID`, `Pending` (Seq-ordered), `Dropped` and `DroppedSeq`. **There is no agent-kind field yet.** D-13 needs one, which means a schema change and migration through the existing hook (`SchemaVersion` is 1).
- `internal/render/render.go`: `render.Lines(ref, events)` renders sanitized lines, and coalescing reuses it.
- `internal/poller/cadence.go` / `scheduler.go`: where the LIFE-08 "undeliverable means slowest rate" input plugs in.
- `cmd/pr-relay/dev.go` / `sink.go`: the hidden dev harness from Phase 2, to extend for headless delivery runs.
- `internal/testutil`: golden files and a hostile-string corpus.
- `spikes/lib.sh`, `spikes/auto-lib.sh`: the guarded scratch-session (`prspike`) herdr wrappers, reusable for the opt-in integration test.

### Established Patterns
- Pure core packages (diff, render, sanitize) have no I/O and no clock. I/O packages take an injected clock and are tested with `testing/synctest`.
- Copy-on-write store: Clone, mutate, Save, then swap. In-memory state must never get ahead of disk.
- Golden-file tests for message output.

### Integration Points
- New `internal/herdr` client package (NDJSON over a unix socket, about 200 lines per CLAUDE.md), plus a delivery/router component that consumes `Engine.Wake()` and per-pane agent status.
- Phase 4 starts the delivery loop inside the daemon and records the pane kind/session in `sub`.

</code_context>

<specifics>
## Specific Ideas

- The user's priority order is: never interrupt a busy agent, never type into the wrong terminal, and never lose an update. Rare typing collisions and a rare duplicate after a crash are acceptable costs.
- Design for the agent as the reader, not for the human watching the pane (D-12).

</specifics>

<deferred>
## Deferred Ideas

None. The discussion stayed within the phase scope. A per-subscription "require unfocused pane" opt-out was offered and declined (D-10). It is not deferred.

</deferred>

---

*Phase: 03-herdr-client-delivery*
*Context gathered: 2026-11-02*
