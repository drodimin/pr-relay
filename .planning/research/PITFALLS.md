# Domain Pitfalls

**Domain:** Local GitHub PR watcher daemon (Go) pushing updates into herdr agent panes
**Researched:** 2026-10-31
**Overall confidence:** MEDIUM. herdr facts are HIGH (read from `herdr api schema --json`, `herdr agent prompt --help`, and the socket-API docs). GitHub API facts come mostly from training knowledge and are MEDIUM. Items marked "VERIFY" must be confirmed against docs.github.com or an experiment in the phase that implements them.

Phase names below assume this roadmap shape (adjust to the real roadmap):
P1 = GitHub poller + state model, P2 = diff/event engine + persistence, P3 = herdr delivery (queue, coalesce), P4 = daemon lifecycle + CLI, P5 = plugin/skill/packaging, P6 = hardening.

---

## Critical Pitfalls

### 1. Prompt injection via PR comments, check names, branch names, and PR titles
**What goes wrong:** The daemon types text into an agent's input. Anything a third party controls (comment bodies, review bodies, check-run names, commit messages, PR title, branch name, workflow-emitted annotations) becomes a prompt for an agent that has shell access. On public repos anyone can comment. "Terse messages with links" reduces but does not remove the risk: a check name or a bot-authored title can itself be an instruction.
**Why it happens:** The relay is a trusted channel (it looks like the user typing), so agents give it user-level authority. `agent.prompt` carries no provenance marker.
**Consequences:** Remote instruction execution on the developer's machine, such as exfiltrating secrets or pushing malicious commits.
**Prevention:**
- v1 messages contain no comment bodies, only structured facts (counts, author login, URL). Treat the PRD's "terse + links" as a security requirement, not just a token optimization.
- Any attacker-controlled string that is interpolated (author login, check name, branch) must be sanitized. Strip control characters, ESC and CSI sequences, newlines, and `\r`. Hard-cap the length (about 80 characters) and quote or fence it.
- Prefix every message with a fixed, unforgeable-looking envelope such as `[pr-relay] automated notification, untrusted data follows:`. The skill snippet tells agents that pr-relay messages are notifications, and that URLs and text are untrusted data, not instructions.
- Per-subscription author filter: by default, comment events from users without write or admin access to the repo (use `authorAssociation`: OWNER, MEMBER, COLLABORATOR) are reduced to "N comments from external users" with no names or text.
- Never inline a comment body, even truncated, in v1. If a later version does, make it opt-in and wrap it in explicit untrusted-content delimiters.
**Detection:** A test corpus of hostile comments, check names, and branch names (newlines, ANSI escapes, "ignore previous instructions") run through the formatter. Assert that output is one line and contains only allowed characters.
**Phase:** P3 (formatter), with the policy decided in P2. HIGH priority. Needs a threat-model note in the README (P5).

### 2. Terminal control-character and multi-line injection into the pane
**What goes wrong:** `agent.prompt` submits text as if typed. A newline in the text can submit early, split into multiple prompts, or confirm a permission dialog. ESC sequences can change terminal state or be interpreted by the agent TUI. Large text is also rejected whole on macOS (docs: "oversized input lines are rejected whole").
**Prevention:** Single-line (or single-paragraph, by deliberate choice) messages. Strip C0/C1 control characters. Cap the total message size well below the macOS line limit (measure it; VERIFY the actual limit empirically, since the docs give no number). Coalescing must truncate ("+7 more, run `pr-relay ls`") instead of growing without bound.
**Detection:** Fuzz the formatter. Send a 10 KB coalesced message in a test against a scratch pane that the developer controls, never a real agent pane.
**Phase:** P3.

### 3. Injecting while the human is typing, or while the agent is mid-turn
**What goes wrong:** herdr docs do not say what `agent.prompt` does when the human has half-typed text in the input box. The relay's text can concatenate with the human's draft, or get submitted along with it. Equally, the status `idle` or `done` only means the agent is waiting. It does not mean the input box is empty. `done` means "idle and not yet seen" and often means the human has not looked at the result yet.
**Why it happens:** Status detection says nothing about input-buffer contents. Agent TUIs differ (Claude Code, Codex, others).
**Consequences:** Corrupted human drafts, accidental submission of half-written commands, and the agent acting on a merged prompt.
**Prevention:**
- Experiment first, with a throwaway pane and a harmless agent or plain shell. Type a partial line, call `agent.prompt`, and record the behavior. Do this before designing the delivery state machine. Do not send prompts to real agent panes.
- Consider a "human recently active" guard. `pane_output_changed` events (with `revision`) and `pane_focused` are available. Delay delivery when the pane is focused and has had output or typing in the last N seconds. Always deliver after a max-delay ceiling, and prefer the toast (`notification.show`) as the fallback when the guard keeps deferring.
- Prefer delivering on `idle`, and treat `done` as eligible but lower-priority. Make this configurable.
- Check `agent.explain` output to learn what the detection can and cannot see.
**Detection:** User reports of "my half-typed message got sent". Log every delivery with pane status, focus state, and time since the last output.
**Phase:** P3. Needs a spike (research flag).

### 4. Race between status-change event and prompt delivery (TOCTOU)
**What goes wrong:** The daemon sees `idle`, calls `agent.prompt`, but the agent started working (or the human submitted something) in between. Or `agent_blocked` is returned. Or delivery succeeds but the status never changes to `working`.
**Prevention:**
- Treat `agent.prompt` as the only atomic check. Its documented contract is that it rejects with `agent_blocked` before any input is sent, so treat that error as the normal "keep queued" path, not a failure. Do not pre-check with `agent.get` and then send.
- Use the `wait` option in the same request so there is no gap between submit and observation (docs: this avoids a race between two calls). Beware `agent_prompt_stalled` (no working/blocked state seen within 5000 ms): it means the message may have been typed but not processed. Do not retry blindly or you will duplicate it. Mark the message "delivered-unconfirmed", then reconcile via status and a later dedupe key.
- A delivery succeeds only on an explicit ack. Remove the message from the queue only after the ack, and put it back on `agent_blocked`, `timeout`, or transport error.
- Message idempotence: include a short per-pane sequence in the text (for example `#12`) so a duplicate is recognizable to the agent and to the human.
- "Success acknowledges queue admission, not completion" (docs). Do not interpret success as "the agent read it".
- A per-pane delivery lock in the daemon, so two events cannot both deliver at once.
**Phase:** P3.

### 5. Duplicate events after daemon restart, and lost events while the daemon was down
**What goes wrong:** If the state snapshot is only in memory, a restart treats the whole PR as new and emits all comments, checks, and reviews again. If the state is persisted but delivery is not transactional, a crash between "diff computed" and "message delivered" either loses or duplicates events.
**Prevention:**
- Persist per-PR watermark state (last-seen comment and review IDs, last head SHA, last check conclusions keyed by a stable identity, merge state, ETags) in SQLite (WAL) or an atomic-rename JSON file under XDG state dir.
- On first sight of a subscription, snapshot silently ("baseline") and emit nothing, except optionally one "current state" line. Do the same on any restart where state is missing.
- On restart with saved state, diff against the saved state and emit a single coalesced "while pr-relay was down" message. Put an upper bound on its size.
- Outbox pattern: write events to a persistent outbox with an ID, and the delivery worker marks them delivered after the ack. Accept at-least-once with a visible sequence number rather than pretending to have exactly-once.
- Subscriptions persist too, or the restart loses them. Persist and reconcile them against live panes at startup (see pitfall 14).
**Detection:** Kill -9 the daemon mid-delivery in tests. Restart with a PR whose state changed while down.
**Phase:** P2 (state and outbox), P4 (restart behavior).

### 6. Flapping CI states and duplicated/re-run checks
**What goes wrong:** A check goes failure -> queued (re-run) -> success, or workflows create new check runs with the same name on re-run, so a name-keyed state map oscillates. Matrix jobs, `pull_request` plus `push` triggers, and external apps (Vercel, Codecov) post their own statuses. The user gets "CI failed", "CI passed", "CI failed" in 5 minutes.
**Why it happens:** Check runs are per check suite per head SHA. A re-run creates a new run (and sometimes a new suite) with the same name. The API list can contain several runs of the same name (`filter=latest` is available on the REST list endpoint; the GraphQL `statusCheckRollup` contexts already show the latest per context. VERIFY both).
**Prevention:**
- Compute a single rolled-up state per head SHA (pending / failing / passing) from the latest run per (app, name). Emit events only on rollup transitions, not per-check transitions.
- Debounce: a failure is reported only after a settle window (for example, wait until the rollup stops being pending, or until N seconds after the first failure while other checks are still running). Prefer "CI finished: 2 failed (names, links)" over "check X failed" the moment it happens. Also offer `fail-fast` as an option.
- Report each failing check once per (head SHA, check name, run id/attempt).
- Reset all CI state on a new head SHA (force-push or new commit), and emit "new commits pushed" once, not CI noise for the old SHA. Ignore check events whose SHA is not the current head.
- Treat `stale`, `skipped`, `neutral`, `cancelled`, `timed_out`, `action_required`, and `startup_failure` explicitly. `cancelled` caused by a newer push is not a failure. `action_required` (fork PR approval) is not a failure either, but the human should know about it.
**Phase:** P2.

### 7. Check runs vs commit statuses vs check suites, and required vs optional checks
**What goes wrong:** Reading only `check-runs` misses legacy commit statuses (`/commits/{sha}/status`) used by many integrations. Reading only `statusCheckRollup` is fine, but it mixes both types, so the code must handle `CheckRun` and `StatusContext`. Reporting every failing check, including non-required ones, creates noise. Conversely, "all required checks passed" is the only signal that matters for merge readiness.
**Prevention:**
- Use GraphQL `statusCheckRollup { state contexts { ... on CheckRun {...} ... on StatusContext {...} } }`. It already unifies both. Note that `contexts` is paginated, and PRs with more than 100 checks need pagination or `totalCount` handling.
- Required-ness: `isRequired(pullRequestNumber:)` exists on the CheckRun and StatusContext GraphQL types (VERIFY). It needs sufficient permission to read branch protection and may be false or unavailable for rulesets or without admin. Fall back to "all checks" and label this in the output. Do not silently assume that a check is optional.
- Let the user choose `ci` (all failures) vs `ci-required`. Default to the one that tends to give fewer false alarms, which is the rollup `state` plus the failing names.
**Phase:** P1 (query shape), P2 (event rule).

### 8. `mergeable` is null / UNKNOWN while GitHub computes it
**What goes wrong:** `mergeable` (REST) is `null`, and `mergeable` (GraphQL) is `UNKNOWN`, until a background job runs. It is computed lazily on the first request after a push or base change. Naive diffing sees MERGEABLE -> UNKNOWN -> CONFLICTING or MERGEABLE -> UNKNOWN -> MERGEABLE and emits two false events. `mergeStateStatus` (BEHIND, BLOCKED, DIRTY, UNSTABLE, CLEAN, HAS_HOOKS, DRAFT) mixes concerns, so it flaps while checks run.
**Prevention:**
- UNKNOWN/null is "no information": never store it as a new state, never emit on it. Keep the last known value and re-poll soon (a few seconds, bounded retries) until it resolves.
- Emit "conflict" only on a transition from not-CONFLICTING to CONFLICTING (resolved), and "conflict resolved" on the reverse. Do not emit on `BEHIND` or `BLOCKED`. Those are expected states.
- Do not derive "conflict" from `mergeStateStatus` alone. Use `mergeable == CONFLICTING`.
- Remember that a base-branch push changes `mergeable` for every open PR targeting it with no event on the PR itself. Only polling catches this, which is a good argument for polling, but it also means conflict events can appear with no activity on the PR.
**Phase:** P2.

### 9. Comment model confusion: review threads vs review comments vs issue comments vs reviews
**What goes wrong:** GitHub has four distinct objects. Issue comments (PR conversation tab), review comments (inline on diff, belong to a review and a thread), reviews (APPROVED/CHANGES_REQUESTED/COMMENTED/DISMISSED/PENDING, with an optional body), and review threads (GraphQL only, with `isResolved` and `isOutdated`). Each has a different endpoint and a different ID space. Missing one class means "the agent never hears about inline review feedback", which is the most important feedback. Also:
- Submitting a review with 8 inline comments creates 1 review + 8 review comments. Emitting 9 events is noise. Group by review ID.
- `PENDING` reviews (drafts) are visible only to their author. Never emit on them.
- Edited comments keep their ID and change `updatedAt`/`lastEditedAt`. Deleted comments vanish. A watermark of "max ID seen" misses edits and cannot detect deletes. A `since`-style cursor on `createdAt` will re-emit when comments are edited if you key on `updatedAt`.
- Resolving a thread and a review being dismissed are state changes with no new objects.
**Prevention:**
- Key seen-state by object ID, store a content hash or `updatedAt`, and emit only on creation (by default). Treat edits as silent unless a verdict changed (for example, the review state changed to DISMISSED). Treat deletions as silent.
- Group review comments under their parent review for one event.
- Use GraphQL `reviewThreads(first:N)` with `comments` and `isResolved` to report "unresolved threads: N". It is a useful, low-noise signal.
- Pagination: `comments`, `reviews`, and `reviewThreads` have `first: 100` limits. PRs with more than 100 items need `pageInfo`. Fetch only the tail (`last: 20`) on steady-state polls. Items are then compared against the watermark, and a deep PR cannot lose items unless more than 20 arrive between polls (detect through `totalCount` delta and then paginate).
**Phase:** P1 (queries), P2 (grouping and rules).

### 10. Own-activity exclusion is subtly wrong
**What goes wrong:** Exclusion keyed on the `gh` login mis-handles: (a) bot-attributed actions, such as commits made through GitHub App tokens or Copilot, (b) reviews or comments made by `github-actions[bot]` on behalf of the user, (c) a user who switches `gh` accounts (`gh auth switch`) while the daemon is running, which changes whose activity to exclude, (d) comments whose author is deleted (`ghost`, null `author` in GraphQL, which causes nil-pointer crashes), (e) the known limitation that the human's own comments are hidden. Also: the user's own push counts as "new commits" and should be excluded using the commit author or pusher, not just the comment author.
**Prevention:** Resolve the viewer login once at startup and re-resolve on token or auth change (see pitfall 12). Treat null `author` explicitly. Filter new-commit events by the pusher (`headRefOid` change, with commit authors as a heuristic). Document the limitation in the skill so agents know to use `--include-own` when the human comments under the same account. Make the filter reason visible in `pr-relay ls --verbose`/logs ("suppressed: own").
**Phase:** P2.

### 11. Force-pushes and head SHA changes
**What goes wrong:** A force-push replaces the head SHA. Commit lists are rewritten, so "new commits" diffs by commit list can show -5/+5 commits. Old check results disappear. Review comments become `outdated` and may re-attach to new commits, bumping `updatedAt`. If the diff keys on SHA lists, a rebase onto the base branch looks like 30 new commits "by others".
**Prevention:** Track `headRefOid` only. On change, emit one "head moved to <short sha> (force-push|N new commits)" event. Distinguish with `compare` or `timelineItems(itemTypes: [HEAD_REF_FORCE_PUSHED_EVENT, PULL_REQUEST_COMMIT])` if the distinction is needed, and keep it optional. Reset CI state (pitfall 6). Do not emit comment events caused solely by outdated-ness.
**Phase:** P2.

### 12. Token types, scopes, and `gh` auth reuse
**What goes wrong:**
- Fetching the token: `gh auth token` works and prints the active account's token. The daemon must not read `~/.config/gh/hosts.yml` directly, because newer `gh` stores tokens in the system keyring, so the file may have no token. Call `gh auth token [--hostname H]`, or `GH_TOKEN`/`GITHUB_TOKEN` if set.
- Daemon environment: a launchd/systemd-started daemon has a minimal environment. `gh` may not be on PATH, `GH_TOKEN` and `GH_HOST` and `GH_ENTERPRISE_TOKEN` are absent, and the macOS keychain may prompt or be unavailable for a headless launchd session. A daemon auto-started by a CLI inside a herdr pane inherits that pane's env. A daemon started by the herdr plugin gets herdr's env. These differ, which produces "works in my shell, fails as plugin".
- Token expiry/rotation: OAuth tokens can be revoked or rotated, and `gh auth refresh` changes scopes. A cached token goes stale and produces 401s. Re-fetch on 401, and rate-limit retrying.
- Scopes: classic OAuth token from `gh auth login` has `repo` (and `read:org`, `gist`) by default, which is enough. Fine-grained PATs need per-repo access plus Pull requests: read, Contents/Metadata: read, and Checks/Commit statuses: read, and they cannot access repos outside their selected owner. A missing permission gives `null`/empty data or a `403`/`404` that looks like "repo not found" rather than "permission denied". SSO-protected orgs require the token to be SSO-authorized, and the error looks like a 403 with an `X-GitHub-SSO` header.
- GitHub Enterprise Server hosts: repos may be `ghe.example.com/owner/repo#N`. `gh` supports multiple hosts. Decide up front whether v1 supports only github.com (state it), and reject others clearly.
**Prevention:** One `auth` module: obtain token via `gh auth token --hostname`, cache with TTL, refresh on 401, never log the token, and surface permission errors verbatim in `pr-relay ls` ("PR not accessible: token lacks X"). Provide `pr-relay doctor` that checks `gh` on PATH, auth, scopes (`X-OAuth-Scopes` header), rate limit, herdr socket, and daemon state. Make the daemon resolve `gh` via a configured absolute path, falling back to a login-shell PATH lookup.
**Phase:** P1 (auth), P4 (daemon environment), P5 (doctor).

### 13. Rate limits: ETag, GraphQL cost, secondary limits
**What goes wrong:**
- **ETag/304:** A conditional request (`If-None-Match`/`If-Modified-Since`) that returns 304 does not count against the primary rate limit (documented by GitHub for REST. VERIFY the current wording at docs.github.com "Use conditional requests if appropriate"). It still counts toward secondary limits in some interpretations. Treat the secondary budget as shared. Not all REST endpoints return useful ETags: some are computed and vary, so a PR resource ETag may change on unrelated updates (any `updated_at` bump, including a label or a comment, changes the body). The `/pulls/{n}` ETag does not cover check runs, which are on a separate endpoint (`/commits/{sha}/check-runs`) with their own ETag. The ETag is per authenticated user and per URL. Send the exact same `Accept` headers, because the ETag depends on the representation. A weak ETag (`W/"..."`) is normal, so compare it as an opaque string.
- **GraphQL does not support ETags/conditional requests.** Every GraphQL call costs points against a separate 5,000 points/hour budget (for a personal user token), and a 304 shortcut does not exist. A "batched GraphQL for N PRs" query is the only way to reduce the cost. The plan "batched GraphQL and/or REST with ETags" must be resolved into a concrete design. The sound hybrid is: cheap REST conditional requests as a change detector (one `GET /repos/{o}/{r}/pulls/{n}` plus `.../issues/{n}/comments`, etc.), and GraphQL only when a detector says something changed, or on slow timers. Alternatively, `GET /repos/{o}/{r}/issues/events` or `/repos/.../events` with `X-Poll-Interval`. The per-repo events endpoint can lag by minutes and does not carry all PR events (VERIFY), so do not rely on it for latency.
- **GraphQL cost and limits:** cost is computed from the number of requests needed to fulfill the connections (`first`/`last` multiplied), divided by 100 and rounded, minimum 1 point. A single query is limited to 500,000 total nodes and 10-second timeouts. Deep nested connections (PRs -> reviewThreads(100) -> comments(100) -> ...) blow up node counts and cost. Use `rateLimit { cost remaining resetAt }` in every query, log the cost, and ask the API for the cost with a `dryRun: true` argument during development. Alias-batching of 20 PRs in one query is fine only if each PR's connection sizes are small (`first: 20`).
- **Secondary rate limits:** concurrency (max ~100 concurrent requests), ~900 REST points/min or ~2,000 GraphQL points/min per endpoint family, CPU-time limits (90 s per 60 s of real time), and content-creation limits (not relevant, because the daemon is read-only). They are triggered by bursts, such as polling 50 PRs simultaneously after a restart or when all timers align. The response is 403 or 429 with `Retry-After` or `x-ratelimit-reset` and sometimes only a message text ("You have exceeded a secondary rate limit"). Continuing to request during a secondary limit can lead to integration bans.
**Prevention:**
- A single global scheduler with jitter, a concurrency cap of 1-4 in-flight requests, and a token-bucket-like pacing. Never one goroutine-per-PR with independent tickers.
- Always read `x-ratelimit-remaining`, `x-ratelimit-reset`, `retry-after`. Honor `Retry-After` exactly, and fall back to exponential backoff plus jitter with a floor of 60 s when the message mentions "secondary".
- Adaptive intervals: fast (10-20 s) while a rollup is pending, 60 s when open and quiet, 5+ min for a long-idle PR or draft. Stagger the start after restart (spread first polls across the interval).
- Budget guard: if remaining is below about 10% of the limit, stretch all intervals. If below about 2%, only poll PRs with a pending CI.
- Per-repo batching: group subscriptions by repo/owner into one GraphQL query with aliases.
- Keep an in-process counter of requests per hour exposed in `pr-relay status`, so a rate-limit regression is visible.
**Detection:** `x-ratelimit-remaining` trending down faster than expected with subscribers constant. 403s with "secondary" in the body.
**Phase:** P1 (design decision on REST-ETag detector plus GraphQL fetch needs a spike, flagged), P6 (tuning).

### 14. Subscription identity: pane ID reuse, moves, and herdr restarts
**What goes wrong:** Pane IDs look like `w1:p3` and herdr documents no reuse policy. After a herdr restart or a closed pane, a new pane may get the same ID, so a stale subscription delivers PR updates into an unrelated agent (or a plain shell, where the text gets executed as a command!). `pane.moved` assigns a new public ID to the same terminal, so subscriptions silently orphan. Subscribing from a plain shell pane (no agent) makes `agent.prompt` fail or behave unexpectedly.
**Prevention:**
- Subscribe to `pane_closed`, `pane_exited`, and `pane_moved` (events in the schema, with `previous_pane_id` and the new `pane`) and update or drop subscriptions accordingly.
- Store with each subscription a pane fingerprint (workspace ID, agent name, creation time, or a herdr-provided stable ID if `PaneInfo` has one; inspect `PaneInfo` fields and check that nothing like a `terminal_id` or session ID exists). Re-validate with `agent.get`/pane read before each delivery: the target must still exist and still report an agent (not a bare shell).
- On herdr restart, panes may be restored with the same IDs or not. Reconcile subscriptions against `session.snapshot` at connect time. Drop the ones whose pane is missing. Decide a policy for "pane exists but agent status is unknown": do not deliver, and keep the message queued with a TTL.
- Never deliver to a pane whose agent is `unknown` unless the user opted in. A prompt typed into a bare shell is command execution (combined with pitfall 1, a serious risk).
- Queue TTL and cap per pane (for example, 1 hour and 20 events), so a dead pane does not accumulate state forever.
**Phase:** P3.

### 15. herdr socket: reconnects, `events_lost`, and invalid subscriptions
**What goes wrong:** From the docs: history is not durable, a slow subscriber gets `events_lost` and its connection is closed, lifecycle subscriptions do not replay, one invalid entry (for example a subscription to a nonexistent pane) fails the whole `events.subscribe` request and closes the connection, ordering holds only within one subscription entry, and transport errors close the connection without a response. The socket file disappears when herdr stops, and a restarted herdr might use a new session. Also, named sessions use a different socket path (`~/.config/herdr/sessions/<name>/herdr.sock`), and `HERDR_SOCKET_PATH` from the CLI's environment is how the CLI knows which one, but the daemon is single per machine. A user with two herdr sessions can have panes in both, and the daemon must connect to each.
**Prevention:**
- A reconnect loop with backoff that never exits. On reconnect: subscribe first, wait for `subscription_started`, then call `session.snapshot` on a separate connection and reconcile (this is the documented recovery sequence). Treat `events_lost` identically.
- Subscribe to global event kinds (`pane.agent_status_changed` for all panes, `pane.closed`) rather than per-pane subscriptions that can fail on a missing pane. Filter in the daemon.
- A status-poll safety net: since events can be lost and `idle` can be missed, run a slow ticker (for example, 15-30 s) that checks the status of panes with non-empty queues through `agent.get`/`agent.list`. Do not depend only on edge events.
- Record each subscription's socket path (from `HERDR_SOCKET_PATH` at `sub` time) so one daemon can serve multiple herdr sessions. This matters in the design of "single daemon per machine", so decide it in P1/P4. If it is deferred, state the limitation: one herdr session per daemon.
- Test: `herdr server stop` while the daemon runs, then restart it. Delete the socket file.
**Phase:** P3 (reconnect), P4 (multi-session decision).

### 16. Agent status detection accuracy and `agent_blocked`
**What goes wrong:** Status comes from hooks (`pane.report_agent`) or screen detection, and herdr docs give no accuracy numbers. Misclassification (`unknown`, `idle` while the agent is in a permission dialog, `working` stuck) leads to messages never delivered or delivered into the wrong state. A blocked agent (permission prompt, or a question) may wait for the human for hours. The queue then holds the update while the PR moves on, so the delivered message is stale ("CI failing" when CI already passed).
**Prevention:**
- Deliver a freshly computed message, not a stored one. The queue stores "dirty" markers (PR X changed), and at delivery time the daemon re-reads the latest state and formats one up-to-date summary. This also gives coalescing for free and prevents stale "CI failed" notices.
- Treat `agent_blocked` as "keep queued, retry on the next status change". Also send the optional `notification.show` toast after a configurable blocked time, so the human knows an update is waiting.
- Handle `unknown` status explicitly (deliver never by default, or only after a timeout and by the opt-in).
- Cap the queue age: drop or compress updates older than their TTL if superseded by terminal states (merged/closed).
- Use `agent.explain` in `pr-relay doctor` for diagnosing why a pane does not receive messages.
**Phase:** P3.

### 17. Daemon auto-start races and stale socket/pid files
**What goes wrong:** Two CLIs started in two panes at once both find "no daemon" and both start one. A crashed daemon leaves a stale socket or pid file, so the CLI hangs on `connect`, or the daemon refuses to start ("address in use"). Naive "check pid file exists" is wrong, because pids are reused. Concurrent daemons double the poll rate and double-deliver messages.
**Prevention:**
- Single-instance enforcement through an exclusive `flock` on a lock file (held for the daemon's lifetime, released by the kernel on death, so there are no stale locks), not the pid file. The CLI start path takes the same lock briefly (`flock` with a timeout) around "check and spawn", and then waits for the socket to accept connections (poll `connect` with a total timeout of about 3-5 s).
- On startup, the daemon takes the lock first, and only then removes a stale socket file (`unlink` and `listen`). If connect to the existing socket succeeds, exit as "already running". Do not unlink a live socket.
- The daemon is spawned detached (`setsid`, stdio to the log file, not tied to the pane's process group). If it is a child of the CLI invoked in a herdr pane, closing the pane could kill it (SIGHUP). Use `Setsid: true` in `SysProcAttr` and ignore SIGHUP.
- A version handshake on connect: after `brew upgrade`, the CLI should detect an old daemon (protocol or version mismatch) and ask it to restart (or tell the user). Otherwise new CLI flags quietly hit an old daemon.
- Graceful SIGTERM handling: flush state, close the socket, remove the pid/socket.
**Detection:** Test with 20 concurrent `pr-relay sub` invocations against no daemon, and assert exactly one daemon. A kill -9 and restart test.
**Phase:** P4.

---

## Moderate Pitfalls

### 18. Unix socket path length limits and macOS vs Linux directory differences
**What goes wrong:** `sun_path` is limited to about 104 bytes on macOS and 108 on Linux. `$TMPDIR` on macOS (`/var/folders/xx/.../T/`) is long, and a deep `XDG_RUNTIME_DIR` or a long home path can overflow, which gives a confusing `invalid argument` error. `XDG_RUNTIME_DIR` is usually unset on macOS and can be unset in non-login Linux sessions. `$XDG_RUNTIME_DIR` is cleaned on logout.
**Prevention:** Use `$XDG_RUNTIME_DIR/pr-relay/` on Linux when set, otherwise a short path under the state directory (`~/.local/state/pr-relay/` or `~/Library/Application Support/...`, which is long). A good default for both platforms is a short fixed location like `~/.pr-relay/d.sock` or `/tmp/pr-relay-$UID/d.sock` with mode 0700 on the directory. Check the path length in a unit test and fall back to chdir plus a relative path if over about 100 bytes. Set mode 0600 on the socket and verify peer credentials (`SO_PEERCRED`/`LOCAL_PEERCRED`) so that other local users cannot subscribe or enqueue prompts into your panes. Note that herdr's own socket is at `~/.config/herdr/herdr.sock`, so copy its conventions where possible. Use `os.UserConfigDir`-style helpers with care, because on macOS it returns `~/Library/Application Support`, while herdr uses `~/.config`.
**Phase:** P4.

### 19. launchd/systemd vs herdr plugin startup
**What goes wrong:** Three ways to start the daemon (CLI auto-start, herdr `[[startup]]`, and service managers) collide. herdr startup hooks run once after session restore and the plugin gets `HERDR_BIN_PATH`, `HERDR_PLUGIN_ID`, `HERDR_PLUGIN_STATE_DIR` env vars. A plugin-launched process might be tied to the herdr lifetime (herdr stops, the daemon is killed or orphaned). If the daemon dies with the herdr server, subscriptions are lost. If it outlives herdr, it holds stale connections. A launchd/systemd unit starts before herdr is up and fails to connect.
**Prevention:** One code path: all three launch methods run the same `pr-relay daemon` entry, guarded by the flock from pitfall 17, so extra starters are no-ops. Do not ship launchd/systemd units in v1 (documented as optional recipes). Verify how herdr supervises `[[startup]]` processes (does it track and kill them? restart them on failure?) in the plugin phase, and read the plugin docs and the manifest schema. The plugin's `HERDR_PLUGIN_STATE_DIR` is a good place for state when running under the plugin, but then state location differs between start methods, which causes subscription loss. Choose one canonical state dir (XDG) and use it regardless of launch method.
**Phase:** P5 (research flag: herdr plugin lifecycle semantics).

### 20. Log growth, state growth, and memory
**What goes wrong:** A long-lived daemon with debug logging at 10 s intervals writes GBs. State rows for closed PRs accumulate. Per-PR in-memory caches of full comment lists balloon.
**Prevention:** Use `log/slog` with a size-rotated file (lumberjack-style, for example 5 MB x 3), or log to stderr and let the service manager capture it, but in auto-start mode, rotate yourself. Log events and decisions, not every poll, and keep poll-level detail at debug. Prune state for unsubscribed or merged PRs after a grace period. Store IDs and hashes, not bodies.
**Phase:** P4/P6.

### 21. Noisy bot comments and bot events
**What goes wrong:** Dependabot, Codecov, Vercel, Netlify, SonarCloud, github-actions, and Copilot review comments produce a stream of edits and new comments on every push (often edited in place). Each triggers an agent turn, which wastes the tokens this tool is designed to save.
**Prevention:** Default bot policy: collapse bot issue comments into a counter or ignore known noisy logins by default, with `--bots=all|none|summary`. Detect bots via `author.__typename == "Bot"` or the `[bot]` suffix. Coalesce per window (for example 20-30 s) before delivering. Never trigger an agent turn for edits of bot comments. Provide a per-subscription `--ignore-author` list. Let Copilot or review-bot findings through as "review comments from <bot>" (these are actionable), configurable.
**Phase:** P2.

### 22. Coalescing and message budget
**What goes wrong:** Coalescing everything into one growing message loses priority (a merge conflict gets buried below 10 comment lines), or the message reaches size limits. Failing to dedupe across the queue gives "CI failed" twice.
**Prevention:** Key the queue by (pane, PR, event class), where the latest wins. Order by severity (merged/closed > conflict > CI fail > changes requested > comments). Cap the lines, add "+N more". Include the PR identity in each line, because one pane may subscribe to several PRs.
**Phase:** P3.

### 23. Timeline / pagination assumptions
**What goes wrong:** Using the REST `/issues/{n}/timeline` or GraphQL `timelineItems` as the single source for all events. It is paginated (default 30, max 100 per page in REST), ordered oldest-first (so the newest items are on the last page: a naive "fetch first page" never sees new events on long PRs), and its events differ in shape. `timelineItems(last: N)` gets the tail. The REST timeline previously required a preview media type (VERIFY whether still true). Unbounded pagination on each poll burns the rate limit.
**Prevention:** Use `last:`/`since:`/`after:` cursors with the persisted watermark. Prefer targeted connections (reviews, comments, checks) over the timeline. Use the timeline only for rare event types (force-push, ready-for-review, converted-to-draft, labeled if wanted).
**Phase:** P1/P2.

### 24. Closed/merged/draft/transferred PRs and lifecycle edge cases
**What goes wrong:** A PR that is merged and then the branch is deleted, a closed PR reopened (subscription already dropped by auto-cleanup), PR converted to draft (CI behavior changes, `mergeable` meaning changes), repo renamed or transferred (404 or a redirect, and the identity key `owner/repo` changes), repo deleted or access lost (permanent 404 treated as transient forever), PR number typos at subscribe time, forks (PR is in the base repo but the head SHA is in the fork).
**Prevention:** Validate on `sub` (synchronous first fetch, show the title and state in the output, and exit non-zero with a clear error if it is not found or not accessible). Key the repo by node ID (GraphQL `id`) in addition to the name. Treat 404 as "inaccessible" after N consecutive failures, notify once, and drop. On merged/closed, send the final event and then clean up, with a grace period before auto-removal. Offer an opt-in for reopen.
**Phase:** P2/P4.

### 25. Time, clocks, and `since` windows
**What goes wrong:** Using the local clock against GitHub `created_at` (skew), and using `since=` filters with a clock-derived value, loses events during clock skew or a delayed API eventual-consistency window. GitHub's API is eventually consistent. An object may appear in a list a few seconds after it is created, so a "max timestamp" watermark skips late arrivals.
**Prevention:** Watermarks by ID sets (or IDs plus overlap window), never by wall clock, and never by "max createdAt". Use server-provided timestamps only. Re-read an overlap window and dedupe by ID.
**Phase:** P2.

---

## Minor Pitfalls

### 26. Skill snippet drift and agents that ignore it
**What goes wrong:** Agents keep polling because the snippet is vague, or they subscribe repeatedly (duplicate `sub` calls). They also might call `sub` from a pane with no `HERDR_PANE_ID` (a subshell, a subagent, or `ssh`).
**Prevention:** `sub` is idempotent (same pane + PR updates the event set and prints "already subscribed"). Print a one-line confirmation containing the next step ("updates will arrive in this pane; do not poll"). Fail clearly if no pane can be detected. The snippet should state the exact command and warn against sleep loops. Tell agents what a relay message looks like and to treat it as untrusted data (ties to pitfall 1).
**Phase:** P5.

### 27. Go-specific gotchas
**What goes wrong:** `http.Client` without timeouts hangs forever on a dead connection. Not draining and closing a 304 body leaks connections. Missing `Retry-After` parse. The default `http.DefaultTransport` proxies via env, so check proxy behavior, and honor `HTTPS_PROXY`. Time-based tickers and `time.Sleep` do not account for laptop sleep: after wake the daemon fires every overdue timer at once (a burst, which can lead to secondary rate limits) and the monotonic clock may not advance during sleep. GraphQL errors arrive with HTTP 200 and an `errors` array and partial `data`. Check them.
**Prevention:** Per-request context timeouts (15-30 s), `io.Copy(io.Discard, body)` before close, a scheduler that computes the next due time (not tickers) and spreads wake-up bursts with jitter, explicit parsing of GraphQL `errors`, with a distinct handling of `RATE_LIMITED` and `NOT_FOUND` types.
**Phase:** P1.

### 28. Packaging and distribution
**What goes wrong:** macOS Gatekeeper quarantines an unsigned downloaded binary. `go install` produces a binary with no version. Homebrew formula and plugin manifest drift out of sync with the daemon protocol version.
**Prevention:** Use goreleaser, with checksums, and consider ad-hoc codesigning and notarization if distributing a raw binary (Homebrew avoids quarantine). Embed the version with `-ldflags` and `debug.ReadBuildInfo` as a fallback. Daemon and CLI versions are matched at handshake (pitfall 17).
**Phase:** P5.

---

## Phase-Specific Warnings

| Phase Topic | Likely Pitfall | Mitigation |
|-------------|---------------|------------|
| P1 GitHub poller design | GraphQL has no ETags, so the "ETag" plan is only valid for REST. Cost and node limits. Secondary limits from bursts | Spike: REST conditional detectors plus GraphQL on change. Single scheduler with jitter and a concurrency cap. Log `rateLimit.cost` (13, 27) |
| P1 Auth | Keyring tokens, a minimal daemon env, fine-grained PAT gaps, SSO | `gh auth token`, refresh on 401, clear permission errors (12) |
| P2 Diff engine | Flapping CI, `mergeable` UNKNOWN, edits and deletes, force-push, bot noise, own-exclusion, eventual consistency | Rollup-based events, UNKNOWN ignored, ID-keyed seen sets, head-SHA reset, bot policy (6-11, 21, 25) |
| P2 Persistence | Duplicates on restart, loss while down | Baseline-on-first-sight, outbox with ack, restart catch-up summary (5) |
| P3 Formatter | Prompt injection, control characters, oversize lines | No bodies, sanitize, envelope, caps, hostile-corpus tests (1, 2) |
| P3 Delivery | Typing collision, TOCTOU, `agent_blocked`, `agent_prompt_stalled`, pane reuse, `unknown` status | Spike with a scratch pane, deliver on fresh state, treat `agent_blocked` as normal, validate the pane before each send (3, 4, 14, 16) |
| P3 herdr connection | `events_lost`, whole-request failure, socket gone | Subscribe-then-snapshot, global subscriptions, safety-net status ticker (15) |
| P4 Daemon lifecycle | Double-start, stale files, SIGHUP, version skew, socket path length, socket permissions | flock single-instance, setsid, handshake, short 0700 socket dir with peer-credential checks (17, 18) |
| P5 Plugin and packaging | `[[startup]]` semantics, state dir divergence, Gatekeeper, snippet quality | Single entry point and canonical state dir, goreleaser and brew, an idempotent `sub` (19, 26, 28) |
| P6 Hardening | Log growth, laptop-sleep timer bursts | Log rotation, a due-time scheduler (20, 27) |

## Research Flags (need deeper research or a spike before the phase)

1. **P3, human-typing behavior of `agent.prompt`** (undocumented in herdr docs). An experiment on a scratch pane is required.
2. **P3, `PaneInfo` stable identity**: check whether a stable terminal or session ID exists to defeat pane-ID reuse.
3. **P1, REST-detector plus GraphQL design**: verify which REST endpoints give useful ETags for PR, comments, reviews, and check-runs, and the real GraphQL cost of the chosen batched query.
4. **P5, herdr plugin `[[startup]]` supervision semantics**: restarts, kill on exit, env.
5. **P1/P2, `isRequired` availability** for the required-checks filter with non-admin tokens and rulesets.

## Sources

- herdr `herdr api schema --json` (protocol 22), `herdr agent prompt --help`, and https://herdr.dev/tui/docs/socket-api/ (HIGH: agent_blocked, agent_prompt_stalled 5000 ms, events_lost, no replay, pane.moved, macOS oversize lines, plugin env vars, startup hooks, `done` meaning)
- Local environment: `HERDR_SOCKET_PATH=~/.config/herdr/herdr.sock`, `HERDR_PANE_ID=w1:p3` format (HIGH)
- GitHub REST/GraphQL docs, "Rate limits for the REST API", "Resource limitations" (GraphQL node and cost limits), "Best practices for using the REST API" (conditional requests, secondary limits, `Retry-After`). Cited from training knowledge and not re-fetched in this session: MEDIUM. VERIFY the specific numbers (900 points/min, 2,000 points/min, 500,000 nodes, 100 concurrent) before hard-coding anything.
- `gh auth token` and keyring storage behavior: training knowledge (MEDIUM).
- The pitfalls on the PR object model (`mergeable` UNKNOWN, review vs thread vs issue comment, check run vs status): training knowledge, widely documented (MEDIUM).
