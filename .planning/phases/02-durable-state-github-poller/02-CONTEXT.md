# Phase 2: Durable State & GitHub Poller - Context

**Gathered:** 2026-11-01
**Status:** Ready for planning

<domain>
## Phase Boundary

Phase 2 delivers a headless core with two parts.

1. **Durable store.** It holds subscriptions, the per-PR `diff.Memory`, and pending events per subscription. These survive `kill -9` and reboots. A restart never replays an event that was already delivered and never loses one that was detected but not yet delivered.
2. **GitHub poller.** It polls each watched PR once per interval, however many panes subscribe. It uses the user's existing `gh` token and re-resolves it after a 401. Queries are batched GraphQL, intervals adapt to activity, rate-limit backoff applies, and the GraphQL cost of each poll is logged.

Requirements: SUB-04, SUB-08, EVT-09, LIFE-03, LIFE-05, DMN-02, DMN-03, DMN-04.

Out of scope for this phase:
- herdr delivery and agent-status gating (Phase 3)
- the control socket, the user-facing CLI, daemon auto-start and the check-in/expiry messages (Phase 4)
- packaging (Phase 5)

Phase 2 must still expose the primitives those later phases call: subscribe/update, list co-subscribers, current interval and next poll time, force a poll now, reset the rate, `keep`, and draining pending events.

</domain>

<decisions>
## Implementation Decisions

### Carried forward (already locked)
- **D-01:** State lives in one JSON file, written atomically (temp file, fsync, rename), behind a narrow `Store` interface. SQLite is only a fallback (STATE.md, research).
- **D-02:** v1 polls with GraphQL only. Batch up to 20 PRs per aliased query (S4: 20 PRs cost 5 points and take about 3 s; batches of 4–5 cost 1 point). Read `rateLimit { cost remaining resetAt }` on every response. REST ETag probes stay out of v1, even though spike S4 suggested them.
- **D-03:** `diff.Step(Memory, Snapshot)` from Phase 1 is the state machine. The store persists one `Memory` per PR. The new Memory and the events derived from it must commit in the same atomic write (transactional-outbox semantics), so events are created exactly once.
- **D-04:** The Phase 1 review and verification follow-ups are part of this phase:
  - CR-01: a createdAt/submittedAt watermark, so a deleted comment or review can't bring back an old unseen one.
  - WR-01: use CommitsTotal so a push of 30+ commits isn't labelled a force-push.
  - WR-02: add an epoch to the dedupe keys across reopen and head return.
  - WR-03: decode ReopenedEvent so `closed` names the right actor.
  - WR-04: tolerate partial GraphQL errors.
  - WR-05: fix the case where the newest databaseId per Key hides a failure from a parallel trigger.
  - WR-06: report a gap for review comments beyond 30.
  - WR-07: reject `.` and `..` in PRRef before it is used as a persistence key.
  - CR-02: NFKC folding and neutralising look-alikes of the `[pr-relay]` tag in sanitize. This must land before Phase 3.

### Rate-limit budget
- **D-05:** Soft cap: pr-relay aims to use at most **10%** of the hourly GraphQL budget (about 500 of 5000 points per hour). If the cadence curve would go over the cap, intervals stretch to fit. Forced polls count toward the cap.
- **D-06:** When the overall remaining budget is low (pr-relay shares it with the user's own `gh` and with agents), stretch every interval in proportion. **Never pause entirely**: polling always continues, at least at the stretched slowest rate. At remaining = 0, requests fail anyway, so the poller waits for `resetAt`. That wait is forced by GitHub, not a pause policy.
- **D-07:** On a secondary rate limit (403/429), honour `Retry-After`, or wait 60 s if the header is missing. Double the wait on each repeated hit, up to about 15 min, and shrink the batch size after the first hit.
- **D-08:** The budget cap percentage, the low-budget threshold, and the min and max intervals can be set in `config.toml`, with the defaults above.

### Polling cadence (LIFE-05, redefined by the user)
- **D-09:** **60 s** for any active PR, including while CI is pending. There is no separate CI-pending rate.
- **D-10:** A PR with no activity ramps geometrically: about 1m → 1.5m → 2m → 3m → 4m → 6m → 8m → 11m → **16m**. It reaches 16 min after about 4 h of quiet. The planner can tune the exact steps, but the endpoints are 60 s at t=0 and 16 min at about 4 h.
- **D-11:** **The ceiling is 16 min**, replacing LIFE-05's "about 1 hour". The PR stays at 16 min through the 24 h check-in and after it, including when the agent doesn't answer the check-in. Only new activity resets the rate. Wherever LIFE-06 or LIFE-08 says "slowest rate", it means 16 min. Budget stretching (D-05, D-06) is the only thing that can make an interval longer than 16 min.
- **D-12:** "Activity" that resets the rate to 60 s means **events that would be delivered, plus the gh user's own pushes**. Your own comments and reviews, which are filtered out, don't reset it. Your own pushes do, because CI follows them.
- **D-13:** The **first** subscription to a PR that isn't watched yet starts polling at 60 s. A further subscription to a PR that is already watched does **not** reset the rate: it joins the existing cadence.
- **D-14:** `keep` (Phase 4 CLI) leaves the rate at 16 min and only resets the 24 h check-in clock. Phase 2 stores the activity and check-in timestamps that support this.
- **D-15:** **Force poll / rate reset.** An explicit command, run after `sub` (name decided in Phase 4, e.g. `pr-relay poll <PR>`), polls the PR immediately and resets its shared rate to 60 s. It is **not throttled**, but it still counts toward the budget cap. Phase 2 builds the poller primitive (`PollNow` / `ResetRate`).

### Subscriptions, baseline & pending events
- **D-16:** **Multiple subscribers per PR are allowed.** One poll serves all of them (DMN-02). The subscribe result reports the **other panes subscribed to that PR**, the **current interval** and the **next poll time**, so the agent knows who else is watching and how fresh the data is. Phase 2 provides that data, and Phase 4 renders it.
- **D-17:** A second subscriber **reuses the PR's existing Memory and polling**. There is no fresh fetch and **no timestamp history filter**. The user accepts that the next poll may deliver something that happened up to one interval before the subscription. The first subscriber to an unwatched PR gets a fresh baseline fetch (SUB-08).
- **D-18:** **Re-subscribing** the same (socket, pane) to the same PR updates the subscription in place (SUB-04). It replaces the event mask and refreshes the activity and expiry clocks. It **keeps** the baseline and the pending events, re-filtered against the new mask.
- **D-19:** **Pending events have no TTL.** They stay until delivered or until the subscription ends. Each subscription holds about 200 at most. When that cap is exceeded, the oldest are dropped and a "some updates dropped" marker is recorded for delivery. Phase 3 renders from the PR's current state at delivery time.
- **D-20:** **Corrupt or unreadable state file at startup:** rename it to `state.json.corrupt-<timestamp>`, start with empty state, and log an error. Phase 4's `status` should show this. Agents have to re-subscribe.

### Headless harness & verification
- **D-21:** Add a **hidden dev command**, `pr-relay dev watch <PR>...`. It runs the real store and poller in the foreground and prints:
  - the rendered event lines
  - each poll's GraphQL cost and remaining budget
  - the PR's current and next interval

  It is how the phase is exercised against live PRs (for example the Phase 1 fixture repo). Phase 4 can keep it or remove it.
- **D-22:** Verify crash safety with a **subprocess kill loop**. A test spawns the real binary against an `httptest` fake GitHub, SIGKILLs it at random points over many iterations, restarts it, and asserts that no event is duplicated and none is lost (ROADMAP SC-1).

### Claude's Discretion
- Where the state and config files live, following the CLAUDE.md conventions for macOS and Linux, and the schema version and migration hook.
- Behaviour when the token disappears completely (`gh` logged out): keep retrying slowly and log, and expose this for `status`.
- Jitter on intervals, and how due PRs are grouped into batches.
- How closed or merged PRs stop being polled. The `closed` event is still detected, and cleanup is LIFE-01 in Phase 4.
- How to follow up a `Result.Gap` (paginate, or a targeted extra query).
- The exact step table between 60 s and 16 min.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project scope & requirements
- `.planning/PROJECT.md`: vision, constraints and key decisions
- `.planning/REQUIREMENTS.md`: SUB-04, SUB-08, EVT-09, LIFE-03, LIFE-05 (ceiling changed by D-11), DMN-02, DMN-03, DMN-04
- `.planning/ROADMAP.md` §Phase 2: goal and success criteria. SC-4's "up to about 1 hour" is superseded by D-11.
- `CLAUDE.md`: stack, go-gh `auth.TokenForHost`, the guidance to write our own HTTP client, and the state file and testing guidance

### Phase 1 outputs
- `.planning/phases/01-toolchain-spikes-pure-core/01-CONTEXT.md`: locked event semantics D-01 to D-23
- `.planning/phases/01-toolchain-spikes-pure-core/01-SPIKES.md` §S4: GraphQL cost and batching numbers. §S3 and §S0 cover the identity fields a subscription must store (socket path, pane_id, agent_session, terminal_id).
- `.planning/phases/01-toolchain-spikes-pure-core/01-REVIEW.md`: CR-01, CR-02 and WR-01 to WR-07 (folded into this phase, D-04)
- `.planning/phases/01-toolchain-spikes-pure-core/01-VERIFICATION.md`: carry-over table assigning those fixes to Phase 2

### Research
- `.planning/research/ARCHITECTURE.md`: registry, scheduler, snapshot store, outbox and router responsibilities, and the transactional outbox pattern (SQLite there; JSON file here per D-01)
- `.planning/research/PITFALLS.md`: rate limits, lazy `mergeable`, CI flapping
- `.planning/research/STACK.md`: library versions

### Code
- `internal/diff/diff.go`: `Baseline`, `Step` and `Result.Gap`
- `internal/model/model.go`: `Memory`, `Snapshot`, `Event`, `PRRef`
- `internal/ghquery/`: the PRState query and decoder
- `internal/filter/filter.go`: own-activity filter (EVT-08)

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- `diff.Baseline` / `diff.Step`: pure and clone-safe, and they never mutate their input. The store persists the returned `Memory`.
- `ghquery`: the PRState GraphQL query and decoder. The poller wraps the single-PR selection in aliased batch queries.
- `filter`: own-activity exclusion. The poller uses it to classify activity for D-12, telling delivered events apart from own pushes.
- `render` and `sanitize`: the dev watch command (D-21) prints lines with them.
- `internal/testutil` (golden files, hostile corpus).

### Established Patterns
- Pure core packages with no I/O and no clock. New I/O packages (store, ghclient, poller) should take an injected clock so cadence tests can use `testing/synctest`.
- Golden-file tests and fixture snapshots captured from real `gh api graphql` output.

### Integration Points
- Phase 3 drains pending events per subscription and marks them delivered.
- Phase 4's control server calls subscribe/update, co-subscribers, interval, PollNow/ResetRate and keep.
- A subscription stores the herdr socket path and pane identity (S0/S3), but Phase 2 doesn't interpret them.

</code_context>

<specifics>
## Specific Ideas

- Example subscribe reply (rendered in Phase 4): `subscribed owner/repo#123 (also watched by w1:p2, w2:p1; polling every 8m, next in 3m)`.
- The user deliberately preferred a slow, wide curve (16 min ceiling, about 4 h ramp) and no throttle on forced polls. They trust agents to call the reset command when they need freshness.
- The user considered allowing only one subscriber per PR, with agents sharing data among themselves, and rejected it. Polling is already shared, so co-subscribers cost nothing, and relaying between agents would cost tokens and fail if the relaying agent is busy or gone.

</specifics>

<deferred>
## Deferred Ideas

- **The force-poll / rate-reset CLI command** (D-15) is a new CLI surface for Phase 4. Add it to REQUIREMENTS.md (for example a SUB-10) when planning Phase 4.
- **Update REQUIREMENTS.md LIFE-05 and LIFE-06 and ROADMAP Phase 2 SC-4** to the 16-min ceiling (D-11). This is a documentation change.

</deferred>

---

*Phase: 02-durable-state-github-poller*
*Context gathered: 2026-11-01*
