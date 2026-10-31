# Architecture Patterns

**Domain:** Local GitHub PR watcher daemon (Go) that pushes updates into herdr agent panes
**Researched:** 2026-10-31
**Overall confidence:** MEDIUM-HIGH. The herdr facts are HIGH: they come from `herdr api schema --json` (protocol 22, herdr 0.9.5), live `herdr agent get` output, and the official socket-API and plugin docs. The GitHub polling and Go library choices are MEDIUM, from training knowledge and not re-verified. Items marked "needs spike" should be tested early.

## Recommended Architecture

One long-lived daemon process holds all state. A thin CLI talks to it over a local Unix socket. The daemon talks to GitHub (pull, batched) and to one or more herdr servers (push). Everything between "GitHub JSON" and "herdr prompt" is pure, testable Go with durable state in SQLite.

```
 agent pane                         herdr plugin [[startup]]
   |  pr-relay sub o/r#1                 |  pr-relay daemon --ensure (one-shot, exits)
   v                                     v
 +---------+   JSON-lines / unix sock  +----------------------------------------------+
 |  CLI    |-------------------------->|                  DAEMON                      |
 | (client)|<--------------------------|  ctl server -> Registry --+                  |
 +---------+  ack + baseline summary   |                           | watch set        |
                                        |                           v                  |
                                        |  Scheduler/Poller --> gh client (GraphQL)    |
                                        |        |  PRState                            |
                                        |        v                                     |
                                        |  Snapshot store + Differ -> typed Events     |
                                        |        |  (one SQLite txn: snapshot+outbox)  |
                                        |        v                                     |
                                        |  Router/Filter (per-sub events, own-user)    |
                                        |        v                                     |
                                        |  Outbox (durable) -> per-pane Delivery       |
                                        |        worker (coalesce, status gate)        |
                                        |        v                                     |
                                        |  Herdr endpoint manager (per socket path)    |
                                        +----------------------|-----------------------+
                                                               v
                              herdr socket: agent.get / agent.wait / agent.prompt /
                              events.subscribe(pane.closed) / notification.show /
                              pane.report_metadata
```

Layout: one Go module, one binary (`pr-relay`) with subcommands `sub|unsub|ls|daemon|status`. `main.go` stays thin. Packages under `internal/`:

| Package | Contents |
|---------|----------|
| `ctl` | Wire protocol and server/client for the CLI socket |
| `registry` | Subscriptions and the watch set |
| `ghclient` | gh auth, GraphQL, rate-limit accounting |
| `poller` | Scheduler and adaptive intervals |
| `model` | `PRState`, `Event` types (no I/O) |
| `diff` | Pure function: `(prev, next, now) -> []Event` |
| `route` | Filter and own-user exclusion |
| `outbox` | Durable queue |
| `deliver` | Per-pane workers, coalescing, message rendering |
| `herdr` | Socket client, reconnect, endpoint manager |
| `store` | SQLite access |

### Component Boundaries

| Component | Responsibility | Talks to | Must NOT |
|-----------|---------------|----------|----------|
| CLI | Parse args, detect pane/socket from env, auto-start daemon, send one request, print result, exit. | Daemon ctl socket only | Touch GitHub or herdr (except nothing) or hold state |
| Ctl server | Request/response on the CLI socket: `sub`, `unsub`, `ls`, `status`, `ping`. | Registry, store | Do network I/O inline. `sub` returns after the baseline is fetched or after a short timeout. |
| Registry | Source of truth for subscriptions `(id, pr_key, pane_id, terminal_id, herdr_socket, events mask, include_own, created_at)`. Maintains the deduped `pr_key -> set(sub)` map. Emits "watch set changed". | Store, Poller (add/remove PR) | Know about GitHub payloads |
| Poller/Scheduler | Owns the timers per PR. Batches due PRs into GraphQL queries. Applies the rate-limit budget and backoff. Yields `PRState`. | ghclient, Registry (read), Differ | Know about panes or subscriptions |
| ghclient | Token (`gh auth token`, re-read on 401), GraphQL transport, `rateLimit{remaining resetAt cost}` parsing, secondary-limit and `Retry-After` handling. | GitHub | Contain diff logic |
| Snapshot store + Differ | Persist the last `PRState` per PR. Pure diff produces typed events with stable `event_key`s. | store | Know about subscriptions |
| Router/Filter | Fan an event to matching subscriptions (event-type mask, own-user exclusion by login, subscription created-after cut-off). Writes outbox rows. | Registry, Outbox | Talk to herdr |
| Outbox | Durable per-subscription event rows with `UNIQUE(sub_id, event_key)`, states `pending/sent`. | store | |
| Delivery worker (one goroutine per pane) | Gate on agent status, coalesce pending rows into one message, `agent.prompt`, mark sent. Handles `agent_blocked`, `not_found`, and herdr down. | Outbox, herdr client | Poll GitHub |
| Herdr client / endpoint manager | One endpoint per distinct herdr socket path (named sessions have different sockets). Reconnect with backoff. Runs the `pane.closed` subscription. Provides `Get`, `Wait`, `Prompt`, `Notify`, `ReportMetadata`. | herdr socket | Know about PRs |
| Plugin package | `herdr-plugin.toml` with `[[startup]]` that runs `pr-relay daemon --ensure`. Also the skill/AGENTS.md snippet. | herdr to CLI | |

### Data Flow

Subscribe path:
1. The CLI reads `HERDR_PANE_ID`, `HERDR_SOCKET_PATH`, and `HERDR_ENV` (see "How the CLI finds the calling pane"). It sends `sub {pr, events, include_own, pane, herdr_socket}`.
2. The daemon validates the pane via `agent.get` (also captures `terminal_id`). It inserts the subscription and upserts the PR into the watch set.
3. It fetches the PR immediately, stores that state as the baseline snapshot, and creates no events for pre-existing content. Optionally it returns a one-line current-state summary to the CLI, so the agent gets "current state" synchronously and pushes only carry deltas.
4. The CLI prints "subscribed" and exits 0.

Update path:
1. The poller wakes for due PRs. It batches them into one GraphQL request with aliased `pullRequest(number:)` fields per repo (dozens per request). It retrieves status check rollups, reviews, comments, mergeable state, head SHA, and state.
2. The differ compares the new `PRState` with the stored one and emits events with keys such as `ci:<name>:<headSha>:<conclusion>`, `review:<reviewId>`, `comment:<commentId>`, `commits:<headSha>`, `conflict:<mergeable>:<headSha>`, `state:merged`.
3. In one SQLite transaction: write the new snapshot, run the router, and insert outbox rows. The snapshot and the events are therefore atomic. A crash replays neither a lost event nor a duplicate.
4. The delivery worker for each affected pane wakes and checks the pane status. If it is `idle` or `done`, it sends the coalesced message. If it is `working`, it uses `agent.wait` (see below). If it is `blocked`, it waits too.
5. On `agent.prompt` success, the rows are marked `sent`. If merged or closed was delivered, the subscription is deleted and the PR is dropped from the watch set when no other subscribers remain.

Cleanup path: the global `pane.closed` subscription (no `pane_id` parameter needed) triggers deletion of that pane's subscriptions. Reconciliation also runs on every herdr (re)connect, using `pane.list` or `session.snapshot` to drop subscriptions whose pane or `terminal_id` no longer exists.

## Patterns to Follow

### Pattern 1: Single-writer actor core, goroutines at the edges
**What:** The registry and scheduler state are owned by one goroutine (channel inbox) or by SQLite transactions. Network workers (GitHub, herdr per pane) are goroutines that send results back. **When:** Always. It avoids lock webs and makes shutdown simple with `context.Context`.

### Pattern 2: Pure diff, durable baseline
**What:** `diff(prev, next)` is a pure function over normalized `PRState`. Table-driven tests use recorded GraphQL fixtures. The snapshot is persisted, so dedupe across restarts falls out for free: after restart the first poll diffs against the persisted snapshot. **When:** Always. It is the core correctness property and the cheapest to test.

### Pattern 3: Transactional outbox
**What:** Events become rows in the same transaction as the snapshot. The delivery worker drains rows. Dedupe is `UNIQUE(sub_id, event_key)`. **When:** Always. It gives at-least-once delivery with no lost events.

### Pattern 4: Gate delivery on `agent.wait`, not on blind status events
**What:** `events.subscribe` for `pane.agent_status_changed` requires a `pane_id` per subscription (schema: required `["type","pane_id"]`), and the whole request is rejected if any referenced pane is missing. A per-pane `agent.wait {target, until:[idle,done], timeout_ms}` is event-driven, pins the resolved pane occupant (a replacement agent cannot satisfy it), and fails cleanly. The delivery loop is:
```go
for pending(pane) {
    a := herdr.AgentGet(pane)               // not_found => drop subs
    switch a.AgentStatus {
    case "idle", "done":
        err := herdr.AgentPrompt(pane, render(coalesce(rows)))
        if errors.Is(err, ErrAgentBlocked) { continue } // race: re-evaluate
        markSent(rows)
        time.Sleep(settle)                   // let status flip to working
    case "working", "blocked":
        herdr.AgentWait(pane, []string{"idle","done"}, 60_000) // loop on timeout
    case "unknown":                          // no agent detected: do NOT type into a shell
        backoffOrAgeOut(rows)
    }
}
```
Keep the global `pane.closed` subscription on its own connection for cleanup. The user brief mentions `pane.agent_status_changed`. Use it only if the `agent.wait` spike shows a problem, for example `agent.wait` timeouts leaking a goroutine per pane. **Confidence:** MEDIUM. The schema is verified. Runtime behavior of `agent.wait` against `blocked` needs a spike.

### Pattern 5: Coalescing by quiet window
**What:** After the first pending row, a short debounce (for example 2-5 s) lets related events (CI fails, then a review lands) combine. Rendering groups by PR: `[pr-relay] o/r#123: CI failed (lint, test) <url>; review: changes requested by bob <url>; 2 new comments <url>`. A hard cap on message length applies with "+N more" so a flood stays terse. A message prefix tag (`[pr-relay]`) lets the agent skill recognize it.

### Pattern 6: Adaptive poll tiers with a global budget
**What:** Per-PR state machine: `hot` (CI pending or just changed, 15-30 s), `warm` (open, 60-120 s), `cold` (settled or draft, 5 min). Add jitter. Global token bucket sized from `rateLimit.remaining/resetAt`. When remaining falls below a threshold, stretch every interval proportionally, and on 403/secondary-limit sleep until `Retry-After` or reset. Request volume depends on the number of PRs, not subscribers. GraphQL has no ETag/304 support, so batching is the main saver (about 1 point per query). REST with ETag/304 (free) is an optimization for a quiet-PR "has anything changed" probe, for example `GET /repos/o/r/pulls/N` conditional, before spending a GraphQL query. Defer it until measured. **Confidence:** MEDIUM (training knowledge, no spike yet).

### Pattern 7: Daemon discovery, single-instance, auto-start
**What:** The socket sits at `$XDG_RUNTIME_DIR/pr-relay/ctl.sock` on Linux, or `~/Library/Caches` or `~/.local/state/pr-relay/ctl.sock` on macOS (mode 0600, dir 0700). The CLI dials it. On `ECONNREFUSED`/`ENOENT`, it takes a `flock` on `daemon.lock`, spawns `pr-relay daemon` detached (`setsid`, stdio to the log file), and retries the dial for about 2 s. The daemon also holds the flock for its lifetime, which ensures one instance. It removes a stale socket after confirming the flock is free. Add a protocol version field to the handshake, and restart or refuse on a mismatch (the binary may be upgraded under a running daemon).

## How the CLI finds the calling pane (verified)

Verified by running inside a pane: herdr sets `HERDR_ENV=1`, `HERDR_SOCKET_PATH`, `HERDR_WORKSPACE_ID`, `HERDR_TAB_ID`, `HERDR_PANE_ID` (here `w1:p3`), plus `HERDR_BIN_PATH`. The docs confirm the same set.

- `--pane` overrides `HERDR_PANE_ID`. With neither, fail with a clear error ("not inside a herdr pane; pass --pane").
- Capture `HERDR_SOCKET_PATH` and send it with the subscription. Herdr has named sessions (`~/.config/herdr/sessions/<name>/herdr.sock`; resolution order is `--session`, `HERDR_SOCKET_PATH`, `HERDR_SESSION`, default). A pane ID like `w1:p3` is meaningful only within one herdr server, so the key is `(socket_path, pane_id)`. The daemon needs an endpoint manager keyed by socket path. A single global socket would be a bug for users with multiple sessions.
- Resolve and store `terminal_id` (from `agent.get`, for example `term_65f2787d5b58b6`) and optionally `agent_session`. Pane IDs are `w<N>:p<N>` and the docs say cross-workspace `pane.move` assigns a new public pane ID. Whether pane IDs survive a herdr restart or are reused is not documented. Use `terminal_id` as a guard: if `agent.get(pane_id).terminal_id` differs from the stored one, treat the subscription as orphaned and drop it. Needs spike: restart herdr and see if `terminal_id` and `pane_id` persist.
- Alternative: `pane.current` accepts a `caller_pane_id`. It is unnecessary when the env var is present, but it is a cheap validator.
- Do not rely on the daemon's own env (it is spawned from whichever pane first called the CLI, or from the plugin). All pane identity comes from the request.

## herdr not running, or restarting

- Core principle: GitHub polling and snapshot/outbox writes never depend on herdr being up. Only delivery does. The outbox just accumulates (coalesced at send time).
- The endpoint manager dials with exponential backoff (1 s to 30 s cap, jitter) per socket path. A missing socket file or `ECONNREFUSED` means "down", not an error, and logs at debug level.
- On connect: `ping` (check `protocol` compatibility, 22 today, and warn on mismatch), open `events.subscribe [pane.closed]`, run `session.snapshot` or `pane.list` to reconcile (drop dead panes, refresh `terminal_id`), then kick all delivery workers.
- `events_lost` (subscriber too slow) or connection close: reconnect, then re-snapshot and reconcile. Do not trust the event stream for state, only as an invalidation signal. This is the documented recovery recipe.
- During a herdr outage, the polling of subscriptions on that endpoint can continue (events go to the outbox), but cap pending age with a TTL (for example 24 h) and cap per-pane backlog. Optionally pause the polling of PRs that have only dead-endpoint subscribers after N hours, to save rate limit. This is an optimization, not required for v1.
- Whether subscriptions should survive a herdr restart is a product decision. Recommendation: keep them for a grace period (about 10 min) while the endpoint is down. Then reconcile on reconnect: keep subscriptions whose `(pane_id, terminal_id)` still exist, delete the rest. This handles the herdr `live_handoff` case, where a new server takes over the socket and the startup hook re-runs.
- The daemon outlives herdr. Document this: either exit after N hours with no subscriptions and no herdr (recommended, and cheap since the CLI auto-starts it), or run forever with idle backoff.

## Delivery semantics (recommendation: at-least-once with persisted dedupe)

| Option | Failure mode | Verdict |
|--------|--------------|---------|
| At-most-once (mark sent before `agent.prompt`) | A crash or herdr failure silently loses a CI failure, which is the exact thing the agent is waiting for | Reject |
| At-least-once (mark sent after `agent.prompt` success) | A crash between prompt and commit gives one duplicate terse message after restart | **Choose**. The messages are informational and idempotent, and a duplicate costs about 50 tokens. |
| Exactly-once | Impossible, since `agent.prompt` has no idempotency key | Not available |

How it works:
- Events are born exactly once. The snapshot and the outbox rows commit in the same transaction (see Pattern 3), so a restart never re-derives old events.
- Delivery is at-least-once. Rows are marked `sent` only after the `agent.prompt` response arrives. On startup, `pending` rows are redelivered.
- Narrow the duplicate window: write `attempting` plus a timestamp before sending, and on restart treat `attempting` rows younger than a few seconds as "possibly sent" (log it, but resend, since a loss is worse than a duplicate).
- Dedupe keys are stable GitHub identities (comment node IDs, review IDs, check name plus head SHA plus conclusion, head SHA), never timestamps. They must be scoped per subscription, since two panes both need the event.
- New subscription cut-off: events only for items newer than the baseline, so subscribing to an old PR does not replay history.
- Rewrites and flapping: CI re-run on the same SHA flips `failure -> pending -> success`. Key CI events on `(check, sha, conclusion, run_attempt)` and only emit on a transition to a terminal state. This avoids "CI failed" repeated for each poll. Emit a single "CI all green" or "CI failed: a, b" per head SHA once all checks settle (a settled-rollup event). This is the most valuable and the trickiest event. Per-check events spam.
- Deleted or edited comments are ignored in v1. Edits keep the same ID, so there are no duplicates.
- Own-user exclusion is applied in the router (not the differ), so toggling `include_own` per subscription works and the snapshot stays user-agnostic. Resolve the login once from `gh api user` (cache, and re-read on auth change).

## Persistence

- Recommended: SQLite through `modernc.org/sqlite` (pure Go, so no CGO and static cross-compiled binaries for Homebrew/GitHub releases on macOS and Linux). WAL mode, one writer. Tables: `subscriptions`, `pr_snapshots(pr_key, json, etag?, updated_at, poll_tier)`, `outbox(id, sub_id, event_key, payload, state, created_at, sent_at, UNIQUE(sub_id,event_key))`, `meta(user_login, schema_version)`. Memory cost is small. **Confidence:** MEDIUM. The pure-Go driver is slower, which is irrelevant at this scale. Needs a check on binary size and idle RSS. bbolt or a single JSON-file-with-atomic-rename is an acceptable fallback if SQLite size or RSS disappoints. Keep the `store` interface narrow so it can be swapped.
- State directory: `$XDG_STATE_HOME/pr-relay` or `~/.local/state/pr-relay` (do not use `HERDR_PLUGIN_STATE_DIR`, because the CLI may auto-start the daemon outside plugin context and the two would diverge). Include a schema version and forward migrations from day one.
- Prune: delete `sent` rows after about 24 h, and snapshots for PRs with no subscribers.

## Herdr plugin packaging (verified from docs; one risk)

- Manifest fields: `id`, `name`, `version`, `min_herdr_version`, `description`, `platforms`, plus `[[build]]` (runs on GitHub `herdr plugin install`, receives no herdr env) and `[[startup]] command = [argv...]`.
- `[[startup]]` is a one-shot hook (not a supervised daemon). It runs once after the session restores and the API socket is ready, runs again on live handoff, does not run on attach or enable, and failures do not stop the server. Therefore the hook should be `command = ["bin/pr-relay", "daemon", "--ensure"]`: start the daemon detached if it is not running, then exit. The CLI auto-start remains the primary path and the hook is a convenience for restoring subscriptions after a herdr restart.
- Risk (LOW confidence, needs spike): the docs say processes surviving server exit are not protected from cleanup and detached background processes must be stopped before their parent exits. It is unclear if herdr kills the plugin's process tree on server stop or if a `setsid` child survives. If it is killed, the daemon would die with herdr. That is acceptable (herdr is gone, so there is nothing to deliver to), and the next hook or CLI call restarts it. Design for the daemon being killed uncleanly at any time. This is already ensured by the transactional outbox.
- `[[build]]` needs a toolchain on the user's machine (herdr does not install one). Prefer shipping a prebuilt binary through GitHub releases and a `[[build]]` that downloads it (or `go build`, documenting the Go requirement). Needs a decision in the packaging phase.
- Also ship a `herdr plugin link` dev flow and the skill/AGENTS.md snippet. `pr-relay` binary on `PATH` (the agent runs `pr-relay sub`) is a separate requirement from the plugin, so document both Homebrew/`go install` and plugin install.

## Anti-Patterns to Avoid

### Anti-Pattern 1: One GitHub request per subscription (or per pane)
**Why bad:** Defeats the core value and burns the 5,000 pt/h budget. **Instead:** Poll per PR (deduped), batch PRs per GraphQL query, and fan out afterward.

### Anti-Pattern 2: Diffing per subscriber
**Why bad:** Duplicated logic and divergent baselines. **Instead:** One snapshot per PR, one event stream, then route/filter per subscription.

### Anti-Pattern 3: Treating herdr events as state
**Why bad:** `events_lost`, restarts, and missed events leave stale caches. **Instead:** Events are wake-ups. Re-read `agent.get` before every send.

### Anti-Pattern 4: Prompting an `unknown` or `blocked` pane
**Why bad:** `unknown` can be a plain shell, so the "prompt" would be typed as a shell command. `blocked` returns `agent_blocked`, and a prompt could answer a permission dialog with the wrong text. **Instead:** Only send on `idle` or `done`, and handle `agent_blocked` as a retry.

### Anti-Pattern 5: Blocking `sub` on the first poll indefinitely
**Why bad:** Violates "returns immediately". **Instead:** Insert the subscription, then do the baseline fetch with a short timeout (about 3-5 s). On timeout, return success and let the poller baseline in the background. Mark the subscription `baselining` so events during that window are derived against the baseline. Race to avoid: events created between subscribe and baseline must not be replayed or lost, so take the baseline snapshot before enabling the router for that subscription.

### Anti-Pattern 6: Shelling out to `gh` for every poll
**Why bad:** Process spawn costs, and it cannot read `rateLimit` or batch easily. **Instead:** Call `gh auth token` only to obtain the token (cached, refreshed on 401), then use `net/http` directly against `api.github.com`. Support `GH_HOST` and GHE later. Note `GH_TOKEN`/`GITHUB_TOKEN` env overrides, and that a daemon spawned from a pane inherits that pane's env, so the token source should be resolved with `gh auth token` and not the process env alone.

### Anti-Pattern 7: Putting message text in the diff layer
**Why bad:** Ties tests to wording. **Instead:** Differ emits typed events with data. A separate `render` function owns terse formatting. This also makes the agent-facing format versionable.

## Scalability Considerations

| Concern | 1 agent / few PRs | 10 agents / ~30 PRs | 100s of PRs |
|---------|-------------------|---------------------|-------------|
| GitHub budget | Trivial | 1-2 GraphQL queries per tick | Tiering and global budget matter, with batches of about 50 PRs per query |
| Daemon memory | A few MB | Same | Snapshots in SQLite, not in RAM |
| Herdr connections | 1 endpoint, 1 events connection | Same, plus transient `agent.wait` per pane | Per-pane goroutines are cheap, but cap concurrent waits |
| Outbox growth | Tiny | Tiny | TTL and per-pane caps prevent runaway backlog if a pane never goes idle |

## Suggested Build Order (dependencies)

1. **Model, differ, renderer (no I/O).** `PRState`, `Event`, `diff`, `render`, and fixtures from real `gh api graphql` output. Highest correctness risk and no dependencies. Includes the settled-CI semantics and own-user filtering as pure functions.
2. **Store and registry.** SQLite schema, migrations, subscription CRUD, snapshot and outbox with transactional commit. Test restart-dedupe here.
3. **ghclient and poller.** Token acquisition, batched GraphQL, rateLimit parsing, adaptive tiers, backoff. Run it headless (log events) to measure real API cost against live PRs.
4. **Herdr client.** Framing, `ping`, `agent.get/wait/prompt`, `events.subscribe(pane.closed)`, `notification.show`, reconnect and endpoint manager. Spike items go here: `agent.wait` while `blocked`, pane/terminal ID persistence across herdr restart, and whether `agent.prompt` on a `done` pane is accepted cleanly. Use a fake herdr socket server for tests.
5. **Delivery worker and outbox drain.** Status gating, coalescing, TTL, retry and crash-recovery tests.
6. **Ctl protocol, daemon lifecycle, CLI.** `sub/unsub/ls/status`, env detection, flock single instance, auto-start. This is the first phase where the product is end-to-end usable. Steps 1-5 can be driven by a test harness until then.
7. **Cleanup, extras:** `pane.closed` and merge/close auto-unsubscribe, `notification.show`, `pane.report_metadata` (sidebar PR state, with `ttl_ms` and `source` identifier, and `seq` for ordering), daemon idle-exit.
8. **Packaging:** `herdr-plugin.toml`, `[[startup]] --ensure`, skill/AGENTS.md snippet, goreleaser (darwin/linux, amd64/arm64), Homebrew tap, README.
9. **Later (v2):** `gh webhook forward`, REST ETag pre-probe, GHE hosts.

Ordering rationale: the diff and outbox layers define the correctness contract and have no external dependencies, so they go first. The herdr client is independent of the poller, so steps 3 and 4 can be built in parallel. The CLI comes late because it is a thin shell over everything else. The packaging phase depends on a stable CLI and on the lifecycle spike results.

## Research Flags

- Needs a spike: `agent.wait` behavior with `blocked` and timeouts, and whether the `agent.prompt` `wait` option is useful (submit and wait for `working` in one call, which would shrink the duplicate window and confirm acceptance).
- Needs a spike: pane ID and `terminal_id` stability across a herdr restart or live handoff (the docs do not cover it).
- Needs a spike: does a `setsid`-detached daemon survive herdr server stop, started from `[[startup]]`?
- Needs a spike: GraphQL query cost and field choice (`statusCheckRollup` contexts, review threads vs comments, `mergeable` being computed lazily and returning `UNKNOWN` initially, which must not emit a "conflict cleared" event).
- Standard patterns, unlikely to need research: SQLite store, Unix-socket JSON-lines protocol, flock single-instance, goreleaser packaging.

## Sources

- `herdr api schema --json` on herdr 0.9.5 (protocol 22): request methods, subscription requirements, `AgentInfo` fields (HIGH)
- Live `herdr agent get` and `env | grep HERDR` from inside a pane (HIGH)
- https://herdr.dev/tui/docs/socket-api/ : socket resolution, framing, `events.subscribe` and `events_lost` recovery, `agent.prompt` / `agent_blocked`, plugin env vars (HIGH)
- https://herdr.dev/tui/docs/plugins/ : `[[startup]]`, `[[build]]`, install/link semantics (HIGH for what is stated, with the lifecycle gap flagged above)
- GitHub GraphQL batching, rate limit points, no ETag on GraphQL, `mergeable` laziness, `modernc.org/sqlite` suitability: training knowledge (LOW-MEDIUM, verify during the poller phase)
