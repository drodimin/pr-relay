# Phase 3: herdr Client & Delivery - Research

**Researched:** 2026-11-02
**Domain:** NDJSON unix-socket client for herdr (protocol 22), per-pane delivery gating, coalescing, staleness rules, and a cadence hook into the Phase 2 poller
**Confidence:** HIGH for herdr wire behaviour, which was probed live against the `prspike` scratch session in this session. HIGH for integration points (read from source). MEDIUM for a few herdr semantics that are not documented (flagged).

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions

#### Carried forward (already locked, do not re-ask)
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

#### Typing-collision guard
- **D-08:** Use **a quiet period only**. Deliver when the agent status is idle or done **and** nothing has changed for the quiet period: no status change and no movement in `completion_seq` / `state_change_seq`. **Ignore herdr's `focused` flag entirely.** herdr always reports the only pane as focused, so a focus rule would hold its updates forever.
- **D-09:** The quiet period is **about 10 seconds**. Claude chooses whether to make it configurable in `config.toml`. If it is configurable, keep 10 s as the default.
- **D-10:** The typing-collision risk is accepted and documented: a delivery can still land on a half-typed draft and be submitted with it (S1). Mention it only in the skill doc and README (Phase 5), and add no runtime knob or per-subscription opt-out. The `[pr-relay]` tag keeps a merged message readable.

#### Message size
- **D-11:** **Always send the whole coalesced message as one prompt.** Don't split it into several prompts or summarise lines. Phase 1 D-03 (no "+N more") applies to coalesced deliveries too.
- **D-12:** **Don't design around Claude Code's paste-collapse at 900 B or more.** pr-relay is agent-agnostic and the agent is the reader. This **overrides the S6 recommendation** of an 800 B ceiling. Keep only a high hard flood guard as a safety net: herdr accepted 16 KB in S6, so the ceiling can sit at or below that. Claude chooses the exact value and the overflow behaviour, but the guard should almost never fire, and when it does it must document itself in the message.

#### Pane identity & restarts
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

### Deferred Ideas (OUT OF SCOPE)
None. The discussion stayed within the phase scope. A per-subscription "require unfocused pane" opt-out was offered and declined (D-10). It is not deferred.

Also out of scope (CONTEXT §Phase Boundary): the `sub`/`unsub`/`ls`/`status` CLI, daemon auto-start and the control socket (Phase 4); LIFE-01, LIFE-06/07 (Phase 4); plugin and docs (Phase 5); `notification.show` and `pane.report_metadata` (v2).
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| DLV-01 | Push via `agent.prompt`, and the agent never polls | §herdr Wire Facts (prompt shape, `agent_prompted` result); Pattern 3 (per-pane worker) |
| DLV-02 | Hold while working or blocked, deliver on idle or done | `agent.wait until [idle,done]` verified (returns in 1 ms when already idle, `timeout` code otherwise); Pattern 3 gate; Pitfall 4 (wait on idle returns instantly) |
| DLV-03 | Never deliver to unknown status or a bare shell | `agent_status: "unknown"` on a bare shell in `pane.get` (verified); `agent.prompt` on a bare shell is `agent_not_found` (S1/S6); gate rule "status in {idle,done} only" |
| DLV-04 | Coalesce pending updates for one pane into one message | Pattern 4 (compose across every sub of the pane, one prompt); one-shot `AckMany` |
| DLV-07 | Current state at delivery time | Pure `compose.Supersede` rules over the `PRRecord.Memory` view (HeadOID, Mergeable, State); new `Engine.PRView` accessor |
| DLV-08 | Survive herdr restart or socket drop | Verified: one response per connection, subscribe conn closes on EOF or `events_lost`; Pattern 2 (reconnect loop + reconcile); outbox is untouched while herdr is down |
| DLV-09 | Multiple herdr sessions | Endpoint manager keyed by socket path; verified that socket isolation alone separates sessions (S0) |
| LIFE-02 | Cancel on pane close or a different occupant | Global `pane.closed` subscription (no pane_id needed, verified to start cleanly); D-13 identity check using `agent_session.value` + `agent`; `agent.wait` returns `agent_not_running` when the occupant changes (verified) |
| LIFE-04 | Cancel at push time when the pane or tab is gone; keep queued if herdr is unreachable | `pane.get` returns `pane_not_found` (verified); `agent.*` methods never return `pane_not_found`, so a second call is needed (verified); dial failure means unreachable |
| LIFE-08 | All panes undeliverable means the slowest rate | New in-memory `Engine.SetPaneReachability`; override `base = MaxInterval` in `planLocked` and `scheduleLocked` |
</phase_requirements>

## Project Constraints (from CLAUDE.md)

- Go only, single static binary, `CGO_ENABLED=0`, macOS and Linux first-class. No new runtime processes (do not shell out to the `herdr` CLI from the daemon).
- Low idle memory and CPU in a long-lived daemon. This phase must not add busy loops. Every wait is event-driven or capped by a timer.
- Agent-agnostic: nothing specific to Claude Code (D-12 already applies this to message size).
- IPC is stdlib `net` + `encoding/json` NDJSON. No gRPC, net/rpc or HTTP-over-socket. The herdr client is an in-repo package (CLAUDE.md suggests about 200 lines; expect about 400 with reconnect). No herdr Go SDK exists.
- Use a separate long-lived events connection and short-lived request connections. CLAUDE.md says "do not assume requests can be mixed on a subscribe connection". **This session verified that they cannot** (see Wire Facts).
- Testing: stdlib `testing`, a fake herdr server over a temp unix socket, an injectable clock or `testing/synctest`, golden files for message output, and an opt-in real-herdr test. Never send prompts to real panes in CI.
- Logging is `log/slog`. Config is BurntSushi/toml (already a dependency).
- GSD workflow enforcement: edits only via GSD commands (the planner and executor handle this).
- Established code patterns (from Phases 1-2): pure packages have no I/O and no clock. I/O packages take `Now func() time.Time`. The store is copy-on-write (clone, mutate, Save, swap). Golden tests go through `internal/testutil.Golden`.

## Summary

Phase 3 adds three things on top of the Phase 2 outbox:
1. **`internal/herdr`**: a small NDJSON client per socket. It sends requests one per connection, and runs a single long-lived, read-only events connection with a reconnect loop.
2. **`internal/compose`**: a pure package that takes every pending event for one pane (across all its subscriptions and PRs), drops superseded state events against the PR's latest polled `Memory`, renders lines through the existing `render.Lines`, and applies a byte flood guard.
3. **`internal/deliver`**: a manager with one goroutine per pane that has pending events. It gates on status in {idle, done} plus a 10 s quiet period (comparing `state_change_seq` and `completion_seq`), runs the D-13 identity check, prompts, and acks. It cancels subscriptions whose pane is gone or holds a different agent, and reports "cannot receive since T" per pane back to the Engine for LIFE-08.

I probed herdr 0.9.5 live, against the `prspike` scratch session only. Five behaviours from those probes reshape the design and are not in the docs:
- **herdr closes every connection after one response.** Pipelining or pooling is impossible, so each request dials a fresh connection.
- **Writing anything on a subscribe connection makes herdr close it.**
- **`agent.prompt`, `agent.get` and `agent.wait` against a non-existent pane return `agent_not_found`, never `pane_not_found`.** The "gone versus bare shell" decision therefore always needs a follow-up `pane.get`, which returns `pane_not_found`.
- **`pane.agent_status_changed` subscriptions require a `pane_id`, and one missing pane fails the whole subscribe with `pane_not_found` and closes the connection.** Global `pane.updated` does **not** fire on status changes.
- **Lifecycle event lines use underscore names** (`"event":"pane_agent_detected"`, `"pane_closed"`), while the per-pane subscription events use dotted names (`"event":"pane.agent_status_changed"`).

Two integration traps in the existing code also matter:
- `Engine.Wake()` is **not** a "new pending events" signal, despite what CONTEXT says. It is the scheduler's own wake channel, consumed in `scheduler.go:sleep`, and it fires only for a new PR or ResetRate. A delivery loop reading it would steal scheduler wake-ups. Delivery needs a new `Engine.PendingWake()` channel, poked in `commit` when `appendPending` adds events.
- herdr documents that a **cross-workspace pane move assigns a new public pane id without emitting `pane.closed`**. Without handling `pane.moved`, a moved agent looks "gone" at the next push and is cancelled.

**Primary recommendation:** Build `internal/herdr` (one-shot request client + `Endpoint` with a supervised events loop), the pure `internal/compose` (supersede, render, flood guard, golden tests), and `internal/deliver` (per-pane workers, gate, identity, cancel, reachability). Add five small Engine hooks (`PendingWake`, `PRView`, `AckMany`, `SetPaneReachability`, `Cancel` with reason) and a store schema v2 that adds `AgentKind`. Test everything against an in-process fake herdr that reproduces the verified quirks above.

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| herdr socket framing, one-shot requests, events stream, reconnect | `internal/herdr` (I/O) | - | The only code that touches herdr sockets. Knows nothing about PRs. |
| Per-socket endpoint lifecycle (DLV-09) | `internal/deliver` manager | `internal/herdr.Endpoint` | The manager creates or retires endpoints as the set of subscription sockets changes |
| Gate (status, quiet period), identity check, prompt, ack, cancel | `internal/deliver` per-pane worker | Engine (Ack, Cancel) | Worker owns the timing. Engine owns durable mutation. |
| Superseded-event rules, coalescing, flood guard | `internal/compose` (pure) | `internal/render` | No I/O, no clock, fixture and golden tested like `diff` and `render` |
| Line rendering and sanitizing (DLV-05/06, already done) | `internal/render` | `internal/sanitize` | Reused. Add one `DroppedLine` and one `HeldLine` helper. |
| Durable outbox, Ack, Unsubscribe | `internal/poller.Engine` + `internal/store` | - | Copy-on-write, already implemented |
| LIFE-08 cadence override | `internal/poller` (planLocked, scheduleLocked) | deliver reports reachability | Cadence lives in the poller. Reachability is in-memory input. |
| Agent kind captured at subscribe time | `internal/store` (schema v2) | Phase 4 `sub` fills it | D-13 needs it. The Phase 3 dev harness fills it from `agent.get`. |
| Headless exercise | `cmd/pr-relay dev deliver` | - | Hidden dev command, guarded to named scratch sessions |

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| Go stdlib `net` (unix) | go1.27.3 toolchain, `go 1.26.7` directive [VERIFIED: `go version`, go.mod] | Dial herdr sockets | CLAUDE.md mandates it. herdr is NDJSON over a unix socket [CITED: herdr.dev/tui/docs/socket-api/] |
| `encoding/json` + `bufio.Reader` | stdlib | Line framing, decode events | Each line is one JSON object [VERIFIED: live probe] |
| `log/slog` | stdlib | Cancellation and diagnostic logs (D-17) | Project standard |
| `testing/synctest` | stdlib (`Test`/`Wait` since go1.25) [CITED: pkg.go.dev/testing/synctest] | Quiet-period, backoff and threshold tests without real sleeps | Already used in `internal/poller/scheduler_test.go` [VERIFIED: grep] |
| `net.Pipe` | stdlib | In-bubble fake herdr transport for synctest tests | synctest docs: goroutines blocked on real network I/O are not durably blocked, so use `net.Pipe` [CITED: pkg.go.dev/testing/synctest] |
| BurntSushi/toml | v1.6.0 (already in go.mod) [VERIFIED: go.mod] | New `[delivery]` config table | Existing config loader |

### Supporting
None new. `internal/testutil.Golden` and the hostile corpus are reused.

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| One connection per request | Pooled or persistent request connection | **Not possible**: herdr closes the connection after the first response [VERIFIED: probe "TWO REQUESTS ONE CONN" returned 1 response then EOF] |
| Per-pane `pane.agent_status_changed` subs | Global `pane.updated` | `pane.updated` did not fire on any of 8 status transitions [VERIFIED: probe2]. `agent_status_changed` without `pane_id` is `invalid_request` [VERIFIED] |
| Shelling out to `herdr agent prompt` | - | Process churn. CLAUDE.md forbids per-poll subprocesses. |

**Installation:** none. No new modules.

## Package Legitimacy Audit

This phase installs **no external packages**. Everything is Go stdlib, plus BurntSushi/toml, which is already in go.mod from Phase 2. slopcheck was not run because there is nothing to check.

| Package | Registry | Age | Downloads | Source Repo | slopcheck | Disposition |
|---------|----------|-----|-----------|-------------|-----------|-------------|
| (none new) | - | - | - | - | n/a | n/a |

**Packages removed due to slopcheck [SLOP] verdict:** none
**Packages flagged as suspicious [SUS]:** none

## herdr Wire Facts (protocol 22, herdr 0.9.5)

All rows are [VERIFIED: live probe against `~/.config/herdr/sessions/prspike/herdr.sock`, 2026-11-02] unless tagged otherwise. Probe scripts are in the session scratchpad. No prompts were sent and no real-agent state was changed. A fake agent was reported on the bare-shell pane `w1:p2` and released afterwards.

| Fact | Evidence |
|------|----------|
| Request: `{"id":"<string>","method":"<m>","params":{...}}\n`. Response: `{"id":..,"result":{"type":"<t>",...}}` or `{"id":..,"error":{"code":..,"message":..}}` | ping returns `{"id":"p1","result":{"type":"pong","version":"0.9.5","protocol":22,"capabilities":{...}}}` |
| **The server closes the connection after one response** | Two pings written on one connection gave one response, then EOF |
| A non-string id gives an error with `"id":""` | `{"id":5,...}` returns `{"id":"","error":{"code":"invalid_request",...}}` |
| `events.subscribe` acknowledges with `{"id":..,"result":{"type":"subscription_started"}}`, then streams event lines with **no id** | probe2 |
| **Writing a request on a subscribe connection closes it** | A ping after `subscription_started` gave EOF and no pong |
| Event envelope: `{"event":"<name>","data":{...}}`. Lifecycle events use **underscore** names, and `data.type` repeats the name: `{"event":"pane_agent_detected","data":{"type":"pane_agent_detected","agent":"fakeagent","pane_id":"w1:p2","workspace_id":"w1"}}` | probe2. Schema `EventKind` enum is all underscores |
| Per-pane subscription events use **dotted** names and no `data.type`: `{"event":"pane.agent_status_changed","data":{"agent":"..","agent_status":"working","pane_id":"w1:p2","workspace_id":"w1"}}` | probe2. Schema `SubscriptionEventKind` |
| Subscription request `type` values are dotted: `pane.closed`, `pane.moved`, `pane.agent_detected`, `pane.exited`, `tab.closed`, `workspace.closed`, and `pane.agent_status_changed` (which **requires** `pane_id`) | Schema `Subscription`. Omitting pane_id gives `invalid_request: missing field 'pane_id'` |
| One missing pane in subscribe gives `{"error":{"code":"pane_not_found","message":"pane w9:p9 not found"}}`, then the connection closes | probe |
| `agent.get`, `agent.prompt` and `agent.wait` on a non-existent pane all return `agent_not_found` (**never `pane_not_found`**) | probe |
| `pane.get {pane_id}` on a missing pane returns `pane_not_found`. On a bare shell it returns `pane_info` with `agent_status:"unknown"` and no `agent` | probe |
| `tab.get` on a missing tab returns `tab_not_found` | probe |
| `agent.wait {until:[idle,done]}` on an already-idle agent returns `agent_info` in about 1 ms | probe. This is a busy-loop trap if used to "wait for idle" while idle (Pitfall 4) |
| `agent.wait {until:[working], timeout_ms:800}` with nothing happening returns `{"error":{"code":"timeout","message":"timed out waiting for agent status"}}` at about 830 ms | probe |
| `agent.wait` with no `timeout_ms` blocks indefinitely (client gave up at 2 s) | probe |
| `agent.wait` returns `agent_not_running` ("agent is no longer running in the target pane") when the pinned occupant goes away | probe2. Docs: wait "pins the resolved pane occupant so a replacement cannot satisfy the wait" [CITED: socket-api] |
| `agent.prompt` on blocked returns `agent_blocked`, "without sending input" | probe2 + S2b + docs |
| Unfocused pane, working then idle, is reported as `done`. Docs: "`done` means idle and not yet seen" | probe2 (`agent_status: done` event), [CITED: socket-api]. This answers the open S7 question: `done` appears on unfocused panes |
| `AgentInfo` carries `agent` (kind, e.g. `claude`), `agent_session {source,agent,kind,value}` or null, `agent_status`, `state_change_seq` (default 0), `completion_seq` (**nullable**), `terminal_id`, `focused`, `launch_pending`, `interactive_ready` | Schema + probe (`completion_seq` was null for the reported agent, `state_change_seq` 5 then 7) |
| `pane.moved` event: `{previous_pane_id, previous_tab_id, previous_workspace_id, pane: PaneInfo}`. "Cross-workspace moves keep the internal pane and terminal alive but assign a new public pane id ... Herdr does not emit fake pane close/create events" | Schema + [CITED: socket-api] |
| `events_lost`: "the server sends an error response with the original request `id` and `error.code: "events_lost"`, then closes that subscription connection" | [CITED: socket-api] (not provoked) |
| "Ordering is guaranteed within each subscription entry, not across entries." Lifecycle subscriptions "do not replay events" | [CITED: socket-api] |
| Error codes seen: `agent_not_found`, `agent_not_running`, `agent_blocked`, `pane_not_found`, `tab_not_found`, `timeout`, `invalid_request` (+ doc: `events_lost`; spike-only: `agent_prompt_stalled` never seen) | probes |
| `ping` reports `protocol: 22` and capabilities | probe |

## Architecture Patterns

### System Architecture Diagram

```
 GitHub poll (Phase 2)                                  herdr session A sock      herdr session B sock
        |                                                     ^   ^                       ^   ^
        v                                                     |   | one-shot req conns    |   |
 Engine.commit --appendPending--> state.json (outbox)         |   |                       |   |
        |                                                     |   +-- events conn (RO) ---+   |
        +--poke--> PendingWake chan                           |       pane.closed/moved/      |
                        |                                     |       agent_detected +        |
                        v                                     |       agent_status_changed    |
              deliver.Manager  <-------- events ---------- herdr.Endpoint(A) ... Endpoint(B)
               | groups subs by (socket,pane)                         ^
               | ensures Endpoint per socket (DLV-09)                 |
               v                                                      |
       per-pane worker (only while pane has pending)                  |
         1. agent.get --------------------------------------------->--+
            - agent_not_found -> pane.get: pane_not_found -> CANCEL (LIFE-04)
                                           exists          -> hold, unreachable (D-14)
            - identity mismatch (D-13)                    -> CANCEL (LIFE-02)
            - working/blocked -> agent.wait until[idle,done] (loop on timeout)
            - unknown / launch_pending                    -> hold
            - idle/done -> quiet timer 10s (reset on status event)
         2. agent.get again: same status class + same seqs? else back to 1
         3. Engine.Pending(all subs of pane) + Engine.PRView(each PR)
         4. compose.Message(...) = supersede -> render.Lines -> flood guard
         5. agent.prompt{text, wait{until:[working], timeout_ms}} --------->
            ok / timeout     -> Engine.AckMany(acks)   (at-least-once)
            agent_blocked    -> back to 1 (no failure)
            agent_not_found  -> back to 1 (pane.get decides)
            dial error       -> unreachable, keep queued (DLV-08)
         6. Manager -> Engine.SetPaneReachability(pane, cannotReceiveSince)  (LIFE-08)
```

### Recommended Project Structure
```
internal/
├── herdr/            # NDJSON client: Call (one-shot), Endpoint (events loop + reconnect), types, error codes
│   └── herdrtest/    # fake herdr server (unix listener AND net.Pipe dialer), reproduces verified quirks
├── compose/          # PURE: Supersede rules, Message assembly, flood guard; testdata/ goldens + fixtures
├── deliver/          # Manager + per-pane worker, gate, identity, reachability; synctest tests via herdrtest
├── poller/           # + PendingWake, PRView, AckMany, Cancel(reason), SetPaneReachability, cadence override
├── store/            # SchemaVersion 2: Subscription.AgentKind; migrate 1->2 (additive, no-op on data)
├── config/           # + [delivery] quiet_period (default 10s)
└── render/           # + DroppedLine(ref, n), HeldLine(n) helpers (sanitized, golden)
cmd/pr-relay/dev.go   # + `dev deliver` subcommand
```

### Pattern 1: One-shot request over a fresh connection
**What:** `Call(ctx, socket, method, params) (json.RawMessage, error)` dials, writes one line, reads one line, and closes. It returns a typed `*herdr.Error{Code, Message}` for error responses. The dialer is injectable (`Dialer func(ctx, socket string) (net.Conn, error)`) so tests can use `net.Pipe`.
**When:** Every request except `events.subscribe`.
**Timeouts:** Dial at 2 s. Use a read deadline of 5 s for ordinary calls. For `agent.wait`, use `timeout_ms + 5 s`, and always send `timeout_ms` (Pitfall 5).
```go
// Source: verified framing (probe); stdlib only
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	conn, err := c.dial(ctx, c.socket)
	if err != nil {
		return &UnreachableError{Socket: c.socket, Err: err} // DLV-08: keep queued
	}
	defer conn.Close()
	if dl, ok := deadline(ctx, c.callTimeout); ok {
		_ = conn.SetDeadline(dl)
	}
	id := c.nextID() // string ids; herdr echoes them
	b, _ := json.Marshal(request{ID: id, Method: method, Params: params})
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return &UnreachableError{Socket: c.socket, Err: err}
	}
	line, err := bufio.NewReaderSize(conn, 64<<10).ReadBytes('\n')
	if err != nil {
		return &UnreachableError{Socket: c.socket, Err: err} // EOF before reply = herdr went away
	}
	var resp response // {ID string; Result json.RawMessage; Error *Error}
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("herdr: decode %s: %w", method, err)
	}
	if resp.Error != nil {
		return resp.Error // *Error{Code, Message}; errors.As in callers
	}
	if out != nil {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}
```
Use `bufio.Reader.ReadBytes`, not `bufio.Scanner`. Scanner's 64 KiB default token limit fails on large `pane.list` results (one probe line was about 1.5 KB per pane, and `PaneInfo` can carry many fields). If you use Scanner, call `Buffer(…, 4<<20)`.

### Pattern 2: Endpoint = supervised read-only events connection per socket
**What:** `Endpoint.Run(ctx)` loops: dial, `ping` (warn if `protocol != 22`), `pane.list`, build the subscription list, `events.subscribe`, read lines until error, back off, repeat. Callers change the watched pane set with `Endpoint.SetPanes([]string)`. That closes the current events connection, so the loop re-subscribes with the new set (it never writes to the open connection, per Wire Facts).
**Subscription list:** globals `pane.closed`, `pane.moved`, `pane.agent_detected`, `pane.exited`, `tab.closed`, `workspace.closed`, plus one `pane.agent_status_changed{pane_id}` **only for watched panes present in the `pane.list` just taken**. This avoids the whole-subscribe-fails trap. If the subscribe still fails with `pane_not_found` (a race), re-list and retry immediately, at most 3 times, then back off.
**After every (re)subscribe:** emit a synthetic `Reconnected` event so workers re-run `agent.get`. Lifecycle events are not replayed, so a pane closed while the daemon was disconnected is caught by the push-time `pane.get` check (LIFE-04).
**Backoff:** 250 ms doubling to a 30 s cap, ±20% jitter. Reset after the connection has stayed up for 60 s. While disconnected, `Endpoint.Up()` is false and `DownSince()` feeds reachability.
**`events_lost`:** an error line with the subscribe id and code `events_lost` means reconnect at once (no backoff) and emit `Reconnected`.
**Decoding:** switch on the envelope `event` string and accept both spellings (`pane_closed` / `pane.closed`) defensively.

### Pattern 3: Per-pane worker state machine (gate + identity)
**What:** One goroutine per (socket, pane) that currently has pending events (or a drop marker) in any subscription. It exits when the pane has nothing pending. The manager starts workers on `PendingWake` and at startup, after scanning `Engine.List()`.
```
loop:
  info, err := agent.get(pane)
  switch {
  case Unreachable(err):            mark cannotReceive(since=endpoint.DownSince); wait endpoint Up or ctx
  case code(err)=="agent_not_found":
       p, err2 := pane.get(pane)
       if code(err2)=="pane_not_found" && endpoint.UpFor() >= restoreGrace { cancelAll(pane,"pane_gone"); return }
       mark cannotReceive("no_agent"); wait for pane.agent_detected / Reconnected / 60s recheck
  case identityMismatch(sub, info): cancel(sub, "identity_mismatch")  // per sub; others may still match
  case info.Status=="unknown" || info.LaunchPending: mark cannotReceive("unknown"); wait status event / 60s
  case info.Status=="working":     mark receivable; agent.wait(until idle,done, timeout_ms=5m) (loop on "timeout")
  case info.Status=="blocked":     mark cannotReceive("blocked"); agent.wait(until idle,done, 5m)
  case idle||done:                 mark receivable; snap := (status class, state_change_seq, completion_seq)
                                   select { quiet timer(10s) | status event for pane -> continue loop | ctx }
                                   info2 := agent.get; if !same(snap, info2) || !idleOrDone(info2) -> continue loop
                                   identity check again; deliver(); continue loop
  }
```
- The identity check (D-13) compares the stored `AgentSession` against `info.agent_session.value`. If both are present and differ, that is a mismatch. If the stored session is non-empty but the current one is null, treat it as "not identified yet" (hold, not a mismatch; see Assumption A3). If no session was stored, compare `AgentKind` with `info.agent`, and a differing kind is a mismatch.
- `agent.wait` returning `agent_not_running` means the occupant changed or exited. Loop back to `agent.get`, which decides between D-13 and D-14.
- Pass "quiet period reset" signals from the endpoint's events to the worker over a per-pane channel (buffered 1, coalescing). The seq comparison at the end of the quiet window is the authoritative check. Events are only an early-reset optimisation, so a dropped events connection does not weaken the gate.

### Pattern 4: Deliver = compose across every subscription of the pane, prompt once, ack once
```go
// deliver(): all subs of (socket,pane), possibly several PRs
var groups []compose.Group
for _, s := range subsOfPane {
	d, err := eng.Pending(s.ID)         // copy; UpToSeq covers drop marker
	if errors.Is(err, poller.ErrUnknownSub) { continue }
	view, _ := eng.PRView(s.PR)         // HeadOID, Mergeable, State from PRRecord.Memory
	groups = append(groups, compose.Group{SubID: s.ID, Ref: ref(s), Drain: d, View: view})
}
msg := compose.Message(groups, compose.Limits{MaxBytes: 12 << 10})
if msg.Text == "" { eng.AckMany(msg.Acks); return }   // everything superseded: ack silently
err := cl.Call(ctx, "agent.prompt", herdr.PromptParams{Target: pane, Text: msg.Text,
	Wait: &herdr.WaitOpts{Until: []string{"working"}, TimeoutMs: 5000}}, &res)
switch {
case err == nil, herdr.Code(err) == "timeout":   // text was submitted; the wait just didn't see working
	eng.AckMany(msg.Acks)                          // at-least-once; never ack before prompt (D-05)
case herdr.Code(err) == "agent_blocked", herdr.Code(err) == "agent_not_found",
	herdr.Code(err) == "agent_prompt_stalled", herdr.IsUnreachable(err):
	// keep queued; loop re-gates
}
```
- `msg.Acks` maps each `subID` to an `upToSeq`. Without a flood-guard overflow, it is the Drain's `UpToSeq` for every group, including superseded events and the drop marker. With an overflow, each sub acks only up to the highest Seq it fully included (Pattern 5).
- Group order: groups by the lowest pending Seq (oldest PR news first). Within a group, Seq order. Join lines with `"\n"`, with no trailing newline (S6: embedded `\n` gives one submission).

### Pattern 5: Pure supersede rules (`compose.Supersede(view, []PendingEvent) []PendingEvent`)
These are the recommended rules (Claude's discretion). Each has a fixture under `internal/compose/testdata/`:
1. `ci.failed` / `ci.settled` whose `HeadOID` is not `view.HeadOID` is dropped (a newer push superseded it, DLV-07 example).
2. For one head: if a `ci.settled` exists, drop every `ci.failed` for that head with a lower Seq (the settled line already lists every failing name; if it passed, the failures are stale).
3. Several `ci.settled` for one head (rerun): keep only the last.
4. `conflict`: an even number of pending conflict events is net zero, so drop them all. With an odd number, keep only the last. Also drop all conflict events when `view.State != "OPEN"`.
5. `commits`, `review`, `comment`: never dropped (history).
6. `closed`: never dropped.
7. Drop marker (`Drain.Dropped > 0`): render `render.DroppedLine(ref, n)`, e.g. `[pr-relay] owner/repo#N note: 3 earlier updates dropped <pr-url>`, as the group's first line.
8. Empty `view` (record missing, which means unsubscribed during the race, or never baselined): apply no head-based rule (keep events). Never drop on unknown state.
Superseded events are still covered by the ack, so they leave the queue silently.

**Flood guard (D-12):** `MaxBytes = 12 KiB` (below the 16 KiB herdr accepted in S6). Lines are capped at 512 B by render. Add whole lines in order while `len(text)+len(line)+1 <= MaxBytes - reserve(HeldLine)`. On overflow, stop. Append `render.HeldLine(k)` = `[pr-relay] note: k more updates held (message size limit), they arrive with the next delivery`, and ack only what was included. Nothing is lost (user priority 3), and the guard documents itself in the message (D-12). The held events go out at the next free window.

### Pattern 6: Engine hooks (small, additive)
| Hook | Purpose | Notes |
|------|---------|-------|
| `PendingWake() <-chan struct{}` | Delivery trigger | New `pendingWake chan struct{}` (cap 1). Poke after a committed Save in `commit` when any `appendPending` added events or gapLost set a marker, and in `Subscribe` (refilter). **Do not reuse `Wake()`**, which the scheduler consumes. |
| `PRView(key) (View, bool)` | DLV-07 current state | `{HeadOID, Mergeable, State string; Baselined bool}` copied from `PRRecord.Memory` under `mu` |
| `AckMany(map[string]uint64) error` | One Save for a multi-PR delivery | Same semantics as `Ack`. Skips unknown subs without error, because a sub may have been cancelled concurrently. |
| `Cancel(subID, reason string) error` | D-15/D-17 cancellation | `Unsubscribe` + `slog.Info("subscription cancelled", "sub", id, "pr", key, "pane", p, "socket", s, "reason", reason)`. Also keep a small in-memory ring (the last 50) for Phase 4 `status`. |
| `SetPaneReachability(PaneRef, cannotSince time.Time)` | LIFE-08 | In-memory map (zero time = can receive). In `planLocked` and `scheduleLocked`, if every sub's pane of a PR has a non-zero `cannotSince` older than or equal to `UndeliverableAfter` (30 min), use `base = MaxInterval` (16 m, D-06). Poke `wake` when a pane goes from unreachable to receivable, so normal cadence resumes on the next loop. Not persisted: after a restart every pane starts as receivable, and the manager re-marks unreachable panes, with since = manager start for sockets never connected. |

Reachability per pane: **working means receivable** (it will be free soon). **Blocked, no agent, unknown, or herdr down means cannot receive.** Keep the earliest `since` across consecutive cannot-receive states, so blocked followed by herdr-down continues the same clock. Reset to zero on any receivable observation.

### Pattern 7: Store schema v2
Add `AgentKind string \`json:"agent_kind,omitempty"\`` to `Subscription` and `SubRequest`. Bump `SchemaVersion` to 2. `migrate(1)` rewrites `"version":1` to `2` with no data change, because the new field is empty. Add `testdata/state-v1.json` to `state-v2` migration tests. An empty `AgentKind` together with an empty `AgentSession` (legacy or dev subs) means **no identity info**. Recommend treating that case as "accept any agent kind" and logging it, so pre-v2 subscriptions are not cancelled (Assumption A4).

### Pattern 8: `pane.moved` remap
When `pane.moved` arrives with `previous_pane_id` equal to a subscribed pane on this socket, re-key those subscriptions to `data.pane.pane_id`. This needs `Engine.Repoint(oldSubID, newPaneID)`, which recomputes `SubID` and keeps Pending, Seq and every other field. Then let the identity check run as usual. If the move was missed (daemon disconnected), the push-time `pane.get` returns `pane_not_found` and D-15 cancels. That case is accepted. See Open Question 1, because this extends D-15 (a move is not "gone").

### Anti-Patterns to Avoid
- **Reading `Engine.Wake()` in delivery.** It steals scheduler wake-ups, and it never fires for new pending events.
- **Writing to the events connection** (including ping or keepalive). herdr closes it.
- **Pooling or keeping request connections open.** herdr closes after one response.
- **Treating `agent_not_found` from `agent.prompt` as "pane gone".** It is identical for a bare shell. Always follow up with `pane.get`.
- **Using `focused`** (D-08) or **`terminal_id`** (D-13) in gates.
- **Calling `agent.wait until [idle,done]` when the agent is already idle.** It returns in 1 ms and becomes a busy loop with the quiet-period logic. Only wait when the status is working or blocked.
- **`agent.wait` without `timeout_ms`.** It blocks forever, so a herdr hang pins goroutines and connections.
- **Acking before the prompt result**, or acking per sub with separate Saves when one AckMany would do.
- **Sending any text when every event was superseded.** Ack silently instead.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Line rendering or sanitizing of PR strings | New formatting in deliver or compose | `render.Lines` + new `render.DroppedLine`/`HeldLine` | DLV-05/06 defences (tag guard, caps, URL fallback) already live there |
| Durable queue, ack or dedupe | A delivery-side "sent" log | `Engine.Pending`/`AckMany` | The outbox is the single source of truth (copy-on-write) |
| Fake time in tests | Sleep-based tests | `testing/synctest` + `net.Pipe` fake herdr | Already the project pattern. Real sockets break the bubble. |
| Event-type knowledge of herdr | Hand-maintained strings scattered | One `herdr/types.go` with the constants from the schema | Two naming styles (dot and underscore) |
| A herdr Go SDK | - | In-repo `internal/herdr` | None exists (CLAUDE.md) |

**Key insight:** The hard parts here are herdr's undocumented connection semantics and the state machine, not libraries. Encode the verified quirks once, in `herdrtest`, so every test runs against them.

## Common Pitfalls

### Pitfall 1: Pipelined requests silently lost
**What goes wrong:** A client that writes two requests on one connection gets one answer, then EOF.
**How to avoid:** One connection per `Call`. The fake server must also close after one response, so tests catch regressions.
**Warning signs:** Intermittent `EOF` on the second call; hangs waiting for a response id.

### Pitfall 2: Subscribe fails wholesale on a vanished pane
**What goes wrong:** A subscribed pane closes, the next re-subscribe includes it, and the result is `pane_not_found` with the connection closed. In a tight reconnect loop, the daemon loses all event coverage for that socket.
**How to avoid:** Run `pane.list` immediately before subscribing and include only panes it lists. On `pane_not_found`, re-list and retry (bounded). Global subscriptions never fail this way.

### Pitfall 3: Moved pane mistaken for a gone pane
**What goes wrong:** A cross-workspace move gives a new pane id and no `pane.closed`. The next push sees `pane_not_found` and cancels a live subscription.
**How to avoid:** Pattern 8 (subscribe to `pane.moved` and repoint).

### Pitfall 4: Quiet period becomes a hot loop
**What goes wrong:** The quiet logic calls `agent.wait until [idle,done]` while idle. It returns at once, and the loop repeats about 1000 times a second.
**How to avoid:** When idle or done, use a timer plus status events. Use `agent.wait` only when working or blocked. Add a test asserting fewer than N herdr calls per quiet window.

### Pitfall 5: Unbounded waits pin connections
**How to avoid:** Always set `timeout_ms` (5 min), with a client read deadline of timeout + 5 s, and loop on the `timeout` code. Cancel via ctx: closing the conn unblocks the read.

### Pitfall 6: Cancelling during herdr restore
**What goes wrong:** Right after herdr restarts, `pane.list` or `pane.get` may be called before panes are restored, or before agent detection and session ids are back (Claude relaunches). A naive check cancels every subscription.
**How to avoid:** (a) Act on `pane_not_found` only once the endpoint has been up for at least `restoreGrace` (≈10 s, Assumption A2). (b) A null current `agent_session` is "not identified yet", not a mismatch (A3). (c) No agent means hold (D-14). (d) Cancel by absence only at push time (LIFE-04), never from a reconcile listing alone. The global `pane.closed` event is the exception, because it is authoritative.

### Pitfall 7: Unix socket path length in tests (macOS 104 B sun_path)
**What goes wrong:** `t.TempDir()` on macOS (`/var/folders/.../T/TestVeryLongName.../001/`) plus `herdr.sock` exceeds 104 bytes, and `bind` fails with `invalid argument`.
**How to avoid:** For real unix-socket tests, use `os.MkdirTemp("", "hd")` (short), clean up in `t.Cleanup`, and keep the file name short (`h.sock`). Prefer the `net.Pipe` dialer for most tests.

### Pitfall 8: synctest plus real sockets deadlock or never advance time
**How to avoid:** Inside `synctest.Test`, the fake herdr must use `net.Pipe` through the injected Dialer. Real unix-socket tests (framing, EOF, reconnect after socket removal) run outside bubbles with short real timeouts.

### Pitfall 9: Prompt ack ambiguity
**What goes wrong:** `agent.prompt` with `wait.until [working]` returns `timeout` when the agent never showed as working (a very fast turn, or an agent whose status detection lags). The text **was** submitted. Retrying duplicates the message.
**How to avoid:** Treat `timeout` from the prompt's wait as delivered and log a warning. Duplicate-after-crash is accepted (D-05), but routine duplicates are not.

### Pitfall 10: Two endpoints for one session
**What goes wrong:** The same socket is stored with different spellings (`~` expansion, symlink, trailing slash), which gives two endpoints and two workers fighting over one pane.
**How to avoid:** Key endpoints by `filepath.Clean(socket)`. Do not `EvalSymlinks` in the key, because SubID is already derived from the captured string. Phase 4 should clean at capture time.

### Pitfall 11: Draft collision (accepted)
Delivery may merge with a half-typed draft (S1). D-10 says this is documented only. No code knob.

## Code Examples

### Event decoding with both naming styles
```go
// Source: verified event lines (probe2) + schema EventKind/SubscriptionEventKind
type envelope struct {
	ID    string          `json:"id,omitempty"`    // only on the ack and on error lines (events_lost)
	Event string          `json:"event,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
	Result *struct{ Type string `json:"type"` } `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

func normalize(name string) string { return strings.ReplaceAll(name, "_", ".") } // pane_closed -> pane.closed
// then switch normalize(env.Event) { case "pane.closed", "pane.moved", "pane.agent.detected" ... }
```
Note: `normalize("pane_agent_detected")` gives `pane.agent.detected`, while the subscription type is `pane.agent_detected`. Compare against explicit constant pairs instead of normalising, e.g. `case "pane_agent_detected", "pane.agent_detected":`.

### Subscribe request built from a fresh pane list
```go
subs := []map[string]any{
	{"type": "pane.closed"}, {"type": "pane.moved"}, {"type": "pane.agent_detected"},
	{"type": "pane.exited"}, {"type": "tab.closed"}, {"type": "workspace.closed"},
}
for _, p := range watched {
	if present[p] { // from pane.list taken just now
		subs = append(subs, map[string]any{"type": "pane.agent_status_changed", "pane_id": p})
	}
}
// write {"id":"sub-<n>","method":"events.subscribe","params":{"subscriptions":subs}}
// expect first line result.type == "subscription_started"; then read-only
```

### Prompt params (schema `AgentPromptParams`)
```json
{"id":"d-17","method":"agent.prompt","params":{"target":"w1:p1","text":"[pr-relay] o/r#1 ci.failed: test https://...\n[pr-relay] o/r#1 review approved by alice https://...","wait":{"until":["working"],"timeout_ms":5000}}}
```

## State of the Art

| Old Approach (in earlier research or spikes) | Current Approach | Why |
|--------------|------------------|-----|
| 800 B flood ceiling (S6) | 12 KiB guard, held overflow | D-12 override |
| Focus plus quiet guard (S1/S7) | Quiet period only | D-08 |
| terminal_id as identity (ARCHITECTURE.md) | agent_session.value, otherwise agent kind | S3, D-13 |
| "Pooled request connection" (CLAUDE.md option) | One connection per request | Verified: herdr closes after one response |
| `Engine.Wake()` as delivery trigger (CONTEXT code_context) | New `PendingWake()` | `Wake()` is the scheduler's channel and fires only for new PR or ResetRate [VERIFIED: engine.go:229-236, scheduler.go:260] |

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | `agent_prompt_stalled` and `timeout` on the prompt's `wait` both mean "the text may have been typed". Retry `stalled`, treat `timeout` as delivered. | Pattern 4, Pitfall 9 | A duplicate, or (worse) a lost message if stalled means "typed". Each is rare. |
| A2 | Right after a herdr restart, `pane.get` may briefly miss panes that are about to be restored. A 10 s grace after the endpoint comes up prevents false cancellations. | Pitfall 6 | With no grace there could be false cancellations. A longer grace delays real cancellations harmlessly. |
| A3 | A null current `agent_session` while one was stored means "not yet identified", so hold rather than cancel. | Pattern 3 | If herdr returns null permanently for a replaced agent of a different kind, the kind check still catches it. For the same kind with null session, it is held forever (counts toward LIFE-08). |
| A4 | Subs with no session and no kind (pre-v2 or dev) accept any agent. | Pattern 7 | Weak identity for legacy subs only. Phase 4 always fills both. |
| A5 | `launch_pending: true` means not ready, so hold. | Pattern 3 | Field semantics are undocumented. Holding is the safe side. |
| A6 | Wording of `DroppedLine` and `HeldLine`, and that a pane-level `HeldLine` without a PR ref is acceptable within D-01 grammar. | Pattern 5 | The user may want different wording. Needs a quick confirm in plan or UAT, the same way Phase 1 locked wording. |
| A7 | Closing a tab or workspace emits `pane_closed` for each pane. Not verified, so also subscribe `tab.closed`/`workspace.closed`, and on those re-check subscribed panes on that socket via `pane.get`. | Pattern 2 | Without the fallback, subscriptions in a closed tab linger until the next push-time check, which still cancels them (LIFE-04). |
| A8 | `state_change_seq` moves on every status transition, and `completion_seq` moves on turn completion (it may be null). | Pattern 3 | Observed 5 then 7 across transitions. If a seq is static, the status comparison still guards. |

## Open Questions

1. **`pane.moved` remap (Pattern 8). Does it extend D-15?**
   - What we know: herdr docs say cross-workspace moves give a new pane id and no close event.
   - Unclear: the user locked "pane gone means cancel". A move is arguably not "gone".
   - Recommendation: implement repoint on a live `pane.moved` event, where identity is still checked by D-13. Confirm with the user during plan review. If they decline, a moved agent loses its subscription (log reason `pane_gone`).
2. **Flood-guard overflow: hold versus drop.**
   - Recommendation: hold (ack only what was included) plus a `HeldLine`. It is consistent with "never lose an update", and D-12 leaves overflow behaviour to Claude.
3. **`done` decaying to `idle` resets the quiet period?**
   - A done to idle change (the user looked at the pane) is a status change, so under D-08 literal reading it restarts the 10 s window. That is fine and arguably desired (the human is there). Recommendation: compare a status *class* (idle and done are the same class) plus seqs, so a mere "seen" does not delay. Planner pick, low stakes. This research recommends the class comparison.
4. **Should the quiet period be configurable?** Recommend yes: `[delivery] quiet_period = "10s"`, validated to the range [1s, 5m], with `EnvFastPoll` lowering the floor to 1 ms for tests. This mirrors the existing `min_interval` pattern.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | build/test | yes | go1.27.3 darwin/arm64 (go.mod `go 1.26.7`) | - |
| herdr | dev harness, opt-in integration test | yes | 0.9.5, protocol 22 | Fake herdr for all CI tests |
| `prspike` scratch session | manual or opt-in live checks | yes, running | - | `herdr --session <name>` to create one |
| jq | spike scripts | yes | - | - |
| golangci-lint | lint | **no** | - | `go vet` (CI may install it) |

Full suite runs in about 20 s today (cmd/pr-relay accounts for 19 s).

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | stdlib `testing` + `testing/synctest` (go1.27.3) |
| Config file | none (go test) |
| Quick run command | `go test ./internal/herdr/... ./internal/compose/... ./internal/deliver/... ./internal/poller/... ./internal/store/...` |
| Full suite command | `go test -race ./...` |
| Golden update | existing `testutil.Golden` flag convention (see internal/testutil/golden.go) |

### Phase Requirements → Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| DLV-01 | Coalesced text reaches fake herdr via `agent.prompt` with the exact params | integration (fake herdr, synctest) | `go test ./internal/deliver -run TestDeliverPrompts` | no, Wave 0 |
| DLV-02 | Held while working or blocked; delivered after idle + 10 s quiet; quiet reset by a status event or seq movement; `agent_blocked` re-gates | synctest | `go test ./internal/deliver -run 'TestGate'` | no, Wave 0 |
| DLV-03 | Never prompts on unknown, `launch_pending`, or a bare shell (`agent_not_found` + pane exists) | synctest | `go test ./internal/deliver -run TestNeverPrompt` | no, Wave 0 |
| DLV-04 | Two PRs and five events for one pane give one prompt; AckMany covers all; events added mid-prompt stay | synctest + unit | `go test ./internal/deliver -run TestCoalesce; go test ./internal/compose` | no, Wave 0 |
| DLV-07 | Supersede rules 1-8 | pure fixture + golden | `go test ./internal/compose -run TestSupersede` | no, Wave 0 |
| DLV-07 | Flood guard holds overflow and acks a partial | pure | `go test ./internal/compose -run TestFloodGuard` | no, Wave 0 |
| DLV-08 | Fake herdr stops (listener closed, socket removed) then restarts: no loss, reconnect with backoff, reconcile, deliver; `events_lost` reconnects | real unix socket (non-synctest) + synctest backoff | `go test ./internal/herdr -run 'TestEndpointReconnect|TestEventsLost'; go test ./internal/deliver -run TestHerdrRestart` | no, Wave 0 |
| DLV-09 | Two fake herdr sockets with the same pane id `w1:p1`: each gets only its own messages | synctest | `go test ./internal/deliver -run TestTwoSessions` | no, Wave 0 |
| LIFE-02 | `pane.closed` cancels and drops pending; session mismatch cancels; kind mismatch cancels; null session holds | synctest | `go test ./internal/deliver -run 'TestCancel'` | no, Wave 0 |
| LIFE-04 | `agent_not_found` + `pane_not_found` at push time cancels; dial failure keeps queued; restore grace respected | synctest | `go test ./internal/deliver -run 'TestPushTimeGone|TestUnreachableKeeps'` | no, Wave 0 |
| LIFE-08 | All panes cannot-receive for at least 30 m gives a 16 m interval; one receivable pane restores normal; working is not unreachable | unit (poller, fake clock) | `go test ./internal/poller -run TestUndeliverableCadence` | no, Wave 0 |
| (support) | One response per conn; subscribe conn read-only; both event spellings decode; `pane_not_found` subscribe retry | unit vs herdrtest | `go test ./internal/herdr` | no, Wave 0 |
| (support) | Schema v1 to v2 migration preserves data | unit | `go test ./internal/store -run TestMigrate` | partial (state-v1.json exists) |
| (support) | Pending wake fires on commit with new events, not on unchanged polls | unit | `go test ./internal/poller -run TestPendingWake` | no, Wave 0 |
| SC-1..5 | Live scratch pane end-to-end | manual / opt-in | `PR_RELAY_HERDR_SCRATCH_SOCKET=~/.config/herdr/sessions/prspike/herdr.sock go test ./internal/deliver -run TestLiveHerdr -v` + `pr-relay dev deliver` | no, Wave 0 |

### Sampling Rate
- **Per task commit:** the quick run command (under 10 s)
- **Per wave merge:** `go test -race ./...`
- **Phase gate:** full suite green, plus one manual `dev deliver` run against a scratch session covering success criteria 1-5, before `/gsd:verify-work`

### Wave 0 Gaps
- [ ] `internal/herdr/herdrtest/server.go`: a fake herdr that must reproduce the verified quirks: close after one response; close the subscribe conn on any write; `pane_not_found` for missing panes in subscribe; `agent_not_found` for missing panes in agent.*; `agent.wait` with `until`, `timeout_ms` and the `timeout` / `agent_not_running` codes; scripted panes (status, seqs, session, kind); recorded prompts; `EmitEvent` (both spellings); `LoseEvents()` (`events_lost`); `Stop()` / `Restart()` (remove the socket); and both a unix listener (short MkdirTemp path) and a `net.Pipe` Dialer mode
- [ ] `internal/compose/testdata/` fixtures + `messages.golden`
- [ ] `internal/store/testdata/state-v2.json` + migration test
- [ ] Opt-in live test gated on `PR_RELAY_HERDR_SCRATCH_SOCKET`. Refuse unless the path contains `/sessions/` and is not `~/.config/herdr/herdr.sock`, reusing the guard logic from `spikes/lib.sh`. Never run in CI. CLAUDE.md's `HERDR_ENV` gate alone is unsafe, because it is set in **every** herdr pane, including the default session.

### Dev harness (`pr-relay dev deliver`)
`pr-relay dev deliver --socket <named-session sock> --pane <id> [--config F] [--sink F] <PR-URL>...` does the following:
- Refuses the default socket, or any path without `/sessions/`.
- Calls `agent.get` once to capture `agent`/`agent_session` (what Phase 4 `sub` will do).
- Subscribes with that identity.
- Runs `Engine.Run` + `deliver.Manager.Run` in the foreground and logs gate transitions with slog.
- Reuses the `envGraphQLURL` fake-GitHub hook so a scripted PR can be driven without the network.

## Security Domain

### Applicable ASVS Categories
| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | no (local unix sockets; herdr has no auth) | - |
| V4 Access Control | yes | Before dialing a stored socket path, `os.Lstat` it: it must be a socket (`ModeSocket`) owned by `os.Getuid()` (`Stat_t.Uid`). Otherwise treat it as unreachable and log. This prevents a crafted sub from pointing the daemon at another user's socket or a non-herdr socket. |
| V5 Input Validation | yes | All herdr responses are decoded into typed structs, with unknown fields ignored. Pane ids are only echoed back to herdr, never interpolated into messages. Message text comes only from `render` (already sanitized, DLV-06). |
| V6 Cryptography | no | - |
| V7 Logging | yes | D-17 cancellation logs contain the sub id, PR key, pane, socket and reason. Never log prompt text at info level (it may contain sanitized third-party names; debug only). |

### Known Threat Patterns
| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Prompt injection via PR strings | Tampering | Existing render/sanitize, the single-tag guard, no bodies (Phase 1). compose must only concatenate render output. |
| Delivery into the wrong agent (pane reuse) | Spoofing / Info disclosure | D-13 identity check before every prompt; `pane.closed` cancels; `agent.wait` occupant pinning |
| Text typed into a bare shell (command execution) | Elevation | Prompt only when `agent.get` reports status idle or done; herdr itself refuses `agent.prompt` on non-agent panes (`agent_not_found`, S1) |
| Flooding an agent's context | DoS | 12 KiB guard, pending cap 200 (Phase 2) |
| Hung herdr pins goroutines | DoS | Deadlines on every connection; `timeout_ms` on waits; ctx cancellation closes connections |

## Sources

### Primary (HIGH confidence)
- `herdr api schema --json` (herdr 0.9.5, protocol 22, schema_version 1): request methods, `AgentPromptParams`, `AgentWaitParams`, `Subscription`, `EventKind`, `SubscriptionEventKind`, `AgentInfo`, `PaneInfo`, `pane_moved` data
- Live probes against the prspike scratch socket (this session): framing, one-response-per-connection, subscribe read-only, error codes, event envelopes, `done` on an unfocused pane, wait timeout and `agent_not_running`
- Repo source: `internal/poller/engine.go`, `scheduler.go`, `cadence.go`, `fanout.go`, `internal/store/state.go`, `store.go`, `internal/render/render.go`, `internal/model/model.go`, `cmd/pr-relay/dev.go`, `internal/config/config.go`
- `.planning/phases/01-toolchain-spikes-pure-core/01-SPIKES.md` (S0-S7)
- https://pkg.go.dev/testing/synctest: durable blocking, `net.Pipe` guidance

### Secondary (MEDIUM confidence)
- https://herdr.dev/tui/docs/socket-api/: `events_lost`, ordering, no replay, cross-workspace move id change, `done` semantics, socket resolution order

### Tertiary (LOW confidence)
- None relied on.

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH. Stdlib only, already used in the repo.
- herdr wire behaviour: HIGH. Probed live. `events_lost` and the move semantics are MEDIUM (docs only, not provoked).
- Architecture / integration: HIGH. Derived from current source.
- Pitfalls: HIGH for 1, 2, 4, 5, 7, 8 (verified). MEDIUM for 3, 6, 9 (docs or reasoning).

**Research date:** 2026-11-02
**Valid until:** 2026-12-02, or until herdr's protocol changes from 22 (re-run the probe script on any herdr upgrade)
