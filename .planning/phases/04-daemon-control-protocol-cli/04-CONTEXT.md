# Phase 4: Daemon, Control Protocol & CLI - Context

**Gathered:** 2026-11-04
**Status:** Ready for planning

<domain>
## Phase Boundary

This phase wraps the Phase 2 poller and the Phase 3 delivery manager in a single self-managing per-user daemon, reached over a unix-socket NDJSON control protocol. It adds the user-facing CLI: `sub`, `unsub`, `keep`, `poll`, `ls`, `status`, `reset`, `daemon [stop|restart]` and `version`. It also completes the subscription lifecycle: the 24h check-in, the 7d expiry, and removal after merge or close. This is the first end-to-end usable tool.

Out of scope: packaging, the herdr plugin, the agent skill and the README (Phase 5), and anything already listed as v2 in REQUIREMENTS.md.

Requirements: SUB-01, SUB-02, SUB-03, SUB-05, SUB-06, SUB-07, SUB-09, DMN-01, DMN-05, DMN-06, DMN-07, DMN-08, LIFE-01, LIFE-06, LIFE-07.

</domain>

<decisions>
## Implementation Decisions

### Carried forward (already locked, do not re-ask)
- **D-01:** Daemon startup: the CLI re-execs itself as `pr-relay daemon` with `SysProcAttr{Setsid: true}` and redirects stdio, with a `gofrs/flock` singleton lock and a stale-socket check. Never double-fork. Spike S5 proved a setsid child survives `herdr session stop`. IPC is a unix socket speaking newline-delimited JSON (`{id,method,params}` → `{id,result|error}`). The socket is mode 0600 inside a 0700 directory, and the path must fit within the macOS 104-byte sun_path limit (see CLAUDE.md §IPC). Socket and log paths go into `internal/paths`, as its doc comment already anticipates.
- **D-02:** `sub` output reports the other panes subscribed to the PR, the current interval and the next poll time (P2 D-16). Re-subscribing updates the subscription in place, replaces the mask, resets the clocks and keeps the pending events (P2 D-18).
- **D-03:** `keep` resets only the subscription's check-in clock (`ClockAt`). It does not change the poll rate (P2 D-14, `Engine.Keep`).
- **D-04:** Cancellations caused by identity mismatch, a missing pane or a closed pane are only logged, and no message goes to the pane (P3 D-17). The only place they surface is `status`.
- **D-05:** Lifecycle messages use the locked line format `[pr-relay] owner/repo#N <event>: ...` (P1 D-01), are never longer than the flood guard allows, and go through the Phase 3 idle-gated delivery path. No new delivery mechanism is added.
- **D-06:** Only github.com is accepted (DMN-08). Validate with `model.ParsePRURL`.

### sub UX & validation
- **D-07:** A successful `sub` prints **terse text of 1–3 lines** with no JSON. For example:
  ```
  subscribed o/r#12 events=all
  polling every 1m (next in 58s); also watched by: w2:p1
  updates arrive in this pane; unsub: pr-relay unsub https://github.com/o/r/pull/12
  ```
  Leave the co-subscriber clause out when there are none.
- **D-08:** **Validation (SUB-07) makes a live GitHub lookup with a bounded wait of about 5 s.** `not_found` or `forbidden` fails with a clear error. A timeout, rate limit, secondary limit or network error **also fails**: it says why and tells the agent to retry later. **An unverified subscription is never created.** The whole `sub` call, including any daemon auto-start, must still finish within "a few seconds" (SUB-01).
- **D-09:** **The force-poll command is `pr-relay poll <PR link>`.** It is a separate command and the realisation of P2 D-15. It polls now, resets the PR's shared rate to 60 s, and prints a one-line result. Any resulting events arrive in the pane through normal delivery and are not printed by `poll`. It is not throttled, but it counts toward the budget cap (P2 D-15).
- **D-10:** **When `sub` runs in a pane where herdr reports no agent, or can't identify one, it subscribes anyway and prints a warning:** updates are delivered only while an agent runs in the pane. Identity follows the P3 D-13 and D-14 rules after that. Outside herdr with no `--pane`, `sub` still fails clearly (SUB-03).

### Check-in & expiry (LIFE-06, LIFE-07)
- **D-11:** **The inactivity clock is per subscription.** Delivering an event to *that* subscription resets it. That means an event that passed its `--events` mask and was acked. `keep` and re-subscribing also reset it. Activity that the mask filters out does **not** count, so a `--events ci` subscriber on a PR with only comments goes inactive. Check-in and expiry messages themselves **never** reset the clock.
- **D-12:** **Check-in wording** is a single line that puts the commands first:
  `[pr-relay] o/r#12 check-in: no activity for 24h. still needed? keep: pr-relay keep <url> | drop: pr-relay unsub <url>`
  **Expiry wording:**
  `[pr-relay] o/r#12 expired: no activity for 7d, subscription removed. re-subscribe: pr-relay sub <url>`
  The duration shown follows the configured thresholds, and golden tests cover it.
- **D-13:** **The check-in repeats every 24 h** (every check-in interval) until `keep`, `unsub`, an activity reset or expiry. ⚠ **This overrides LIFE-06's "one check-in message".** The user chose the repeat on purpose. Update the REQUIREMENTS.md wording during planning or verification.
- **D-14:** **At 7 d of inactivity, the subscription stops counting for polling straight away.** If it was the PR's last subscriber, the PR stops being polled. The "expired" message is queued and delivered once the pane can receive, and **then** the subscription record is deleted. The existing P3 rules still apply: if the pane is gone, the subscription and its pending updates are dropped silently (D-15 / LIFE-04).
- **D-15:** The check-in and expiry thresholds are set in `config.toml` (LIFE-07), with defaults of 24h and 7d, bounds validated in the style of the existing `config.Validate`.

### Merged/closed cleanup (LIFE-01)
- **D-16:** **`closed` is always delivered, even when the subscription's `--events` mask excludes it,** because it ends the subscription and the agent needs to know why. The subscription is removed after the `closed` message is delivered and acked. A reopened PR is **not** tracked, so the agent has to re-subscribe. (P1 D-20 still applies: `closed` is delivered even when the gh user merged the PR themselves.)

### Daemon lifecycle (DMN-01, DMN-07)
- **D-17:** **A version mismatch triggers a transparent auto-restart.** The CLI asks the old daemon to shut down gracefully and flush state, waits for it to exit, spawns the new binary and retries the original command. It prints a single stderr note, such as `restarted daemon (0.3.0 -> 0.4.0)`. Subscriptions survive because state persists (LIFE-03). It restarts whichever side is newer; the user did not ask for any downgrade guard.
- **D-18:** **The daemon never exits on its own.** It runs until it is stopped or receives a signal, and it keeps running even with zero subscriptions or herdr absent. Idle cost is close to zero because nothing is polled.
- **D-19:** **The daemon commands are `pr-relay daemon` (runs in the foreground; brew services, systemd and the Phase 5 plugin hook use this form), `pr-relay daemon stop` and `pr-relay daemon restart`.** Stopping is graceful: flush state and remove the socket. Mismatch handling (D-17) reuses the same shutdown path. On a cold machine, 20 concurrent CLI calls must start exactly one daemon (SC-2).
- **D-20:** **The hidden `dev watch` and `dev deliver` commands stay in the shipped binary.** The crash and live tests depend on them, and `dev deliver` keeps its scratch-socket guard.

### ls / status / unsub / reset
- **D-21:** **`ls` inside a herdr pane lists only that pane's subscriptions.** `ls --all` lists every subscription on the machine with a PANE column, and outside herdr `ls` shows all of them automatically. Each row shows the PR, events, current poll interval, last activity (with the event type), when the next check-in is due and when the subscription expires (SUB-06).
- **D-22:** **v1 has no `--json` on any command.** Output is text only.
- **D-23:** **`status` shows** daemon health, the subscription and PR counts and the rate-limit budget (DMN-05), and in addition:
  - **recent cancellations** with their reason (`pane_closed`, `pane_gone`, `identity_mismatch`, `expired`, `closed`)
  - **herdr connectivity per socket** (up or down, since when, pending count)
  - **the version, PID, uptime and the socket, state and log paths**

  The poller's existing `Status` fields (auth state, quarantined state file, per-PR errors) also appear. They are already gathered, so this costs nothing.
- **D-24:** **`unsub <PR link>` is idempotent.** If the calling pane has no matching subscription, it prints `not subscribed` and exits 0. `unsub --all` covers the **calling pane only** (or the `--pane` given). Outside herdr with no `--pane`, it errors the same way `sub` does.
- **D-25:** **`pr-relay reset` drops every subscription on the machine** and its pending updates. The daemon keeps running. It prints how many subscriptions it removed. *Claude's choice, to revisit if the user objects:* it asks for confirmation when stdin is a TTY and requires `--yes` otherwise, so an agent can't wipe other agents' subscriptions by accident.

### Phase 3 hardening (folded in)
- **D-26:** **Fix IN-01, IN-02 and IN-03** from `.planning/phases/03-herdr-client-delivery/deferred-items.md` in this phase:
  - IN-01: cap the flood-guard partial ack so that a superseded pair is never split.
  - IN-02: resubscribe after RestoreGrace, or on `pane_agent_detected`, for panes that were missing at connect.
  - IN-03: a pane-set resubscribe must not reset `UpSince` or poke every worker.

  IN-03 matters most, because every `sub` now triggers a resubscribe.

### Claude's Discretion
- The `--events` flag syntax: comma-separated names from the `model.EventType` set, plus any group aliases such as `ci` → `ci.failed,ci.settled`. Unknown names are rejected clearly.
- Which URL forms are accepted: `/pull/N`, a trailing `/files`, `/commits` or `#fragment`, and `http` versus `https`. Everything else is rejected according to SUB-09.
- The control-protocol method names and schema, and how the version handshake works (for example a `hello` method on every connection).
- How herdr identity (agent kind and session, P3 D-13) is captured at `sub` time: the CLI asks herdr directly, or the daemon does it with the socket and pane the CLI passes.
- Log location and rotation (DMN-06): use the per-user state or log directory, slog plus lumberjack, or rotating size files of our own. Keep it small.
- The auto-start retry and backoff timings, and the stale-socket cleanup.
- Exactly how `ls` lays out its columns and formats times.
- How expiry, check-in and closed removal map onto the existing outbox. For example, synthetic `model.Event` kinds rendered by `render` and acked through `Engine.Ack`, so that crash safety (EVT-09) covers them as well.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project scope & requirements
- `.planning/PROJECT.md`: the vision and the constraints (agent-agnostic, low resource use, gh auth)
- `.planning/REQUIREMENTS.md`: SUB-01/02/03/05/06/07/09, DMN-01/05/06/07/08, LIFE-01/06/07. LIFE-06's "one check-in" is overridden by D-13.
- `.planning/ROADMAP.md` §Phase 4: the goal and the 5 success criteria
- `CLAUDE.md` §IPC, §Daemonization / auto-start, §Logging, §Testing: socket path rules, the re-exec and flock pattern, the 104-byte sun_path limit, `testing/synctest`

### Prior phase decisions
- `.planning/phases/01-toolchain-spikes-pure-core/01-CONTEXT.md`: message line format (D-01 to D-05) and the closed-by-self exception (D-20)
- `.planning/phases/01-toolchain-spikes-pure-core/01-SPIKES.md` §S5: setsid survival
- `.planning/phases/02-durable-state-github-poller/02-CONTEXT.md`: cadence (D-09 to D-15), subscriptions (D-16 to D-19), the corrupt-state quarantine (D-20) and the dev harness (D-21, D-22)
- `.planning/phases/03-herdr-client-delivery/03-CONTEXT.md`: identity (D-13, D-14), pane gone (D-15), cancellations only logged (D-17)
- `.planning/phases/03-herdr-client-delivery/deferred-items.md`: IN-01, IN-02 and IN-03, folded in by D-26

### Research
- `.planning/research/ARCHITECTURE.md`: the daemon, router and IPC structure
- `.planning/research/PITFALLS.md`: daemon races and stale sockets
- `.planning/research/STACK.md`: library versions (cobra, flock, lumberjack)

### External
- herdr socket API: https://herdr.dev/tui/docs/socket-api/, and local `herdr api schema --json` (0.9.5, protocol 22) for `agent.get` and `pane.get` when identity is captured at sub time

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- `internal/poller/engine.go`: `Subscribe(SubRequest) (SubInfo, error)` returns co-subscribers, interval, next poll and terminal state, which covers the D-07 output. Also `Unsubscribe`, `Cancel`, `Keep`, `ResetRate`, `PollNow` (D-09), `List`, `Schedule`, `CoSubscribers` and `RecentCancellations` (D-23).
- `internal/poller/status.go`: `Engine.Status()` already returns auth state, limit, remaining and reset, spend in the last hour, stretch, the secondary-limit deadline, PR and subscription counts, the quarantine path and per-PR errors. It is most of `status` already.
- `internal/store/state.go`: `Subscription.ClockAt` is the check-in and expiry clock, and `PRRecord.LastActivity` already exists. A per-subscription "last delivered" time and a "check-in last sent" marker are probably new fields, which means a schema bump through the `migrate` hook.
- `internal/deliver/manager.go`: the `Manager.Run` delivery loop that the daemon hosts. `cancelPane` and the cancel reasons live here.
- `internal/herdr/client.go`: `AgentGet` and `PaneGet` for capturing identity and checking pane existence at sub time.
- `internal/model/model.go`: `ParsePRURL` (D-06), and the `EventType` constants for `--events`.
- `internal/paths/paths.go`: `StateDir` and `ConfigPath`. Add `SocketPath` and `LogDir` here.
- `internal/config/config.go`: extend it with a `[lifecycle]` table (check-in and expiry), following the same Duration and Validate pattern.
- `cmd/pr-relay/main.go`: the cobra root with `version`. `cmd/pr-relay/dev.go` shows how the store, poller and delivery manager are wired, which is the blueprint for `daemon`.
- `spikes/setsid-probe/`: a working Setsid re-exec example (with darwin and other build files).

### Established Patterns
- Pure core packages (diff, render, compose, sanitize) have no I/O. I/O packages take an injected clock and are tested with `testing/synctest`.
- The copy-on-write store mutates through `Engine.mutate`. In-memory state never gets ahead of disk.
- Golden-file tests (`internal/testutil/golden.go`) cover all message output, and the new check-in and expiry lines need golden tests too.
- The test-only env overrides (`PR_RELAY_STATE_DIR`, `PR_RELAY_CONFIG`, `PR_RELAY_TEST_FAST_POLL`, `PR_RELAY_GITHUB_GRAPHQL_URL`) keep tests away from the real home directory. A socket-path override follows the same convention.
- The fake herdr server is `internal/herdr/herdrtest`.

### Integration Points
- New: a control-protocol package (NDJSON server in the daemon, client in the CLI), the `daemon` command that hosts the poller and the delivery manager, the auto-start in the CLI, and a lifecycle ticker (check-in and expiry) that feeds the outbox.
- `sub` connects herdr identity capture (CLI environment → daemon) with `Engine.Subscribe` and the live validation poll (D-08).

</code_context>

<specifics>
## Specific Ideas

- The exact `sub` output shape is in D-07, and the check-in and expiry wording is in D-12. The user approved both previews.
- `ls` preview (approved shape):
  ```
  $ pr-relay ls
  o/r#12  events=all        poll=1m   last=3m ago (ci.failed)  check-in in 21h  expires in 6d
  $ pr-relay ls --all
  PANE    PR       EVENTS  POLL  LAST     EXPIRES
  w1:p1   o/r#12   all     1m    3m ago   6d
  ```
- The user asked how to drop all subscriptions. The answer is a dedicated `reset` command (D-25), not an overloaded `unsub --all`.

</specifics>

<deferred>
## Deferred Ideas

- `--json` output for `ls` and `status` was offered and declined for v1 (D-22). Revisit if the Phase 5 plugin or scripts need it.
- Documentation follow-ups: change LIFE-06's wording to the repeating check-in (D-13). P2's deferred note still needs LIFE-05/LIFE-06 updated to the 16-min ceiling. Add a requirement for `poll` (a SUB-10, P2 D-15) and one for `reset`.

</deferred>

---

*Phase: 04-daemon-control-protocol-cli*
*Context gathered: 2026-11-04*
