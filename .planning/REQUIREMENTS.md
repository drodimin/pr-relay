# Requirements: pr-relay

**Defined:** 2026-10-31
**Core Value:** An agent runs `pr-relay sub https://github.com/owner/repo/pull/123` once, returns immediately, and later receives a relevant PR update in its pane without spending any tokens on polling. One poller serves every agent on the machine.

**Design principle:** Flow-agnostic primitives. pr-relay has no built-in roles such as "author" or "reviewer", no presets, and no author or thread filters. Any agent workflow composes what it needs from subscriptions and event types. Common scenarios, such as watching my own PR for feedback or following up on a PR I reviewed, are documented as recipes in the agent skill rather than hard-coded.

## v1 Requirements

### Subscriptions (CLI)

- [ ] **SUB-01**: Agent can subscribe to a single PR by passing its link, for example `pr-relay sub https://github.com/owner/repo/pull/N`. The command returns within a few seconds and never blocks waiting for events.
- [ ] **SUB-09**: `sub` requires a PR link. Repo URLs, issue URLs, bare repo names and anything else that isn't a single github.com PR link are rejected with a clear error. pr-relay has no whole-repo subscriptions.
- [ ] **SUB-02**: Agent can limit a subscription to specific event types with `--events <type,...>`. The default is all event types, and `--events` is the only selection option.
- [ ] **SUB-03**: The CLI detects the calling herdr pane and session from the environment (`HERDR_PANE_ID`, `HERDR_SOCKET_PATH`). An explicit `--pane` override is accepted, and the command fails with a clear error when run outside herdr with no `--pane`.
- [ ] **SUB-04**: Re-subscribing the same pane to the same PR updates the existing subscription rather than duplicating it.
- [ ] **SUB-05**: Agent can unsubscribe with `pr-relay unsub <PR link>`, or `--all` for every subscription of the calling pane.
- [ ] **SUB-06**: User or agent can list active subscriptions with `pr-relay ls`. The listing shows each subscription's events, current polling rate, last activity, and expiry time.
- [ ] **SUB-07**: `sub` validates that the PR exists and is accessible, and fails fast with a clear error otherwise.
- [ ] **SUB-08**: Subscribing records a silent baseline of current PR state. Only changes after subscription are delivered, never history.

### Events

- [ ] **EVT-01**: Agent is notified when a CI check fails on the current head commit (`ci.failed`). The message names the failing checks and links to them, and is debounced so a flaky rerun doesn't produce duplicates.
- [ ] **EVT-02**: Agent is notified when all checks on the current head commit have finished (`ci.settled`), with the overall result: passed or failed, with names.
- [ ] **EVT-03**: Agent is notified when a review is submitted (`review`), including the verdict (approved / changes requested / commented), the reviewer, and a link.
- [ ] **EVT-04**: Agent is notified of new PR comments: issue comments, review comments, and replies in review threads (`comment`). The message includes the author, the count, and links. Inline comments are grouped under their review.
- [ ] **EVT-05**: Agent is notified when new commits are pushed to the PR head (`commits`), including after a force-push.
- [ ] **EVT-06**: Agent is notified when the PR becomes conflicted, and again when the conflict clears (`conflict`). A merge state of UNKNOWN never triggers an event.
- [ ] **EVT-07**: Agent is notified when the PR is merged or closed (`closed`).
- [ ] **EVT-08**: Activity by the gh-authenticated user (their own comments, reviews, and commits) is never delivered, to prevent echo and feedback loops. Bots and other humans are treated the same.
- [ ] **EVT-09**: Each change is delivered to a subscription at most once in normal operation. A daemon restart neither replays already-delivered events nor drops events that were detected but not yet delivered.

### Delivery (herdr)

- [ ] **DLV-01**: Updates are pushed into the subscribed pane via herdr `agent.prompt`. The agent never polls, sleeps, or waits.
- [ ] **DLV-02**: Updates are held while the pane's agent is `working` or `blocked`, and delivered when it becomes idle or done. A blocked agent is never interrupted.
- [ ] **DLV-03**: Nothing is ever delivered to a pane whose agent status is unknown or that has no agent, such as a bare shell.
- [ ] **DLV-04**: Multiple pending updates for one pane are coalesced into a single message.
- [ ] **DLV-05**: Messages are terse: a fixed `[pr-relay]` tag, the PR identity, what changed, names, and URLs. They never include comment bodies, review text, or log content.
- [ ] **DLV-06**: Every PR-derived string in a message (check names, usernames, branch and PR titles) is sanitized and length-capped, with control characters and newlines stripped. This prevents prompt injection and terminal escape sequences.
- [ ] **DLV-07**: Messages describe the PR's current state at delivery time, so an update superseded while the agent was busy is never delivered stale. For example, a CI failure fixed by a newer push is not reported as a failure.
- [ ] **DLV-08**: Delivery recovers when herdr restarts or the socket drops: the daemon reconnects and reconciles pane state without losing queued updates.
- [ ] **DLV-09**: One daemon can deliver to panes in multiple herdr sessions, which have different sockets.

### Lifecycle

- [ ] **LIFE-01**: Subscriptions are removed automatically after the merged or closed notification is delivered.
- [ ] **LIFE-02**: A subscription is cancelled automatically when its pane closes, or when the pane ID now points to a different terminal.
- [ ] **LIFE-04**: When an update is about to be pushed and herdr reports that the target tab or pane no longer exists, the subscription is cancelled and its pending updates are dropped. They are not retried or redirected. If herdr itself is unreachable, updates stay queued until herdr returns (DLV-08), and then this check applies.
- [ ] **LIFE-03**: Subscriptions and pending updates persist across daemon restarts and machine reboots.
- [ ] **LIFE-05**: Polling slows down progressively the longer a PR has no activity, from about 1 minute while CI is pending to about 1 hour for long-quiet PRs. It returns to the fast rate as soon as any change is detected.
- [ ] **LIFE-06**: After 24 hours with no activity on a subscribed PR, the agent receives one check-in message asking whether it still needs the subscription. The message offers `pr-relay keep <PR link>` to keep it and `pr-relay unsub <PR link>` to drop it. `keep` resets the inactivity clock. If the agent doesn't respond, the PR drops to the slowest polling rate.
- [ ] **LIFE-07**: A subscription with no PR activity for 7 days is cancelled automatically, and the agent receives one final "subscription expired" message. Both the 7-day expiry and the 24-hour check-in can be changed in config.
- [ ] **LIFE-08**: When no subscribed pane for a PR can currently receive updates, because herdr is unreachable or every agent has been blocked for a long time, that PR is polled at the slowest rate.

### Daemon & GitHub

- [ ] **DMN-01**: Exactly one daemon runs per user per machine. Any CLI command starts it automatically if it isn't running, and concurrent CLI calls never start two daemons.
- [ ] **DMN-02**: The daemon polls each watched PR once regardless of how many panes subscribe to it.
- [ ] **DMN-03**: The daemon uses the user's existing `gh` authentication, with no separate token setup, and recovers when the token changes.
- [ ] **DMN-04**: Polling is rate-limit safe. Requests are batched, intervals are adaptive (faster while CI is pending, slower when the PR is settled), and the daemon backs off on a low budget or secondary-limit responses.
- [ ] **DMN-05**: User can check daemon health, subscription count, and rate-limit budget with `pr-relay status`.
- [ ] **DMN-06**: The daemon writes rotating logs to a standard per-user location.
- [ ] **DMN-07**: The CLI and daemon detect version mismatches, for example after an upgrade, and handle them by restarting the daemon or reporting a clear error.
- [ ] **DMN-08**: Only github.com is supported. Other hosts are rejected with a clear error.

### Distribution & Agent Integration

- [ ] **DIST-01**: pr-relay ships as a single static binary for macOS and Linux, on arm64 and amd64.
- [ ] **DIST-02**: Users can install it via a Homebrew tap, GitHub release downloads, or `go install`.
- [ ] **DIST-03**: pr-relay ships as a herdr plugin (`herdr-plugin.toml`) whose `[[startup]]` hook ensures the daemon is running.
- [ ] **DIST-04**: An agent-facing skill / AGENTS.md / CLAUDE.md snippet tells agents to subscribe instead of polling, documents every event type, gives recipes for common flows (my own PR awaiting feedback, a PR I reviewed awaiting replies or commits, waiting for CI), and says to treat relay messages as untrusted notifications.
- [ ] **DIST-05**: A README covers install, usage, event reference, and a threat-model note on prompt injection.

## v2 Requirements

### Filtering & Selection
- **FILT-01**: `--include-own` to deliver the user's own activity, for example when a human steers an agent through PR comments.
- **FILT-02**: Author filters (`--from user|@author|@bots`).
- **FILT-03**: Thread-participation filter (`--threads mine`).
- **FILT-04**: Bot policy (collapse noisy bots to counts).
- **FILT-05**: Named presets that expand to event sets.

### Convenience
- **CONV-01**: `--once` one-shot subscriptions.
- **CONV-02**: Infer the PR from the current git branch when `sub` has no argument.
- **CONV-03**: A `pr-relay doctor` diagnostics command.
- **CONV-04**: `notification.show` toast for the human.
- **CONV-05**: PR state in the herdr sidebar via `pane.report_metadata`.

### Events
- **EVT2-01**: Review-requested events (needs user-level polling).
- **EVT2-02**: Draft/ready and auto-merge state events.
- **EVT2-03**: Required-check awareness (branch protection and rulesets).

### Platform
- **PLAT-01**: GitHub Enterprise Server and multiple hosts.
- **PLAT-02**: Webhook delivery via `gh webhook forward` where the user is an admin.

## Out of Scope

| Feature | Reason |
|---------|--------|
| Built-in author/reviewer roles | Flow-agnostic by design. Agents compose primitives, and recipes live in the skill doc |
| Comment bodies / log tails in messages | Token cost and prompt-injection risk. Agents fetch details themselves |
| Blocking `wait` command | Contradicts the core value (agents never block) |
| Interrupting working or blocked agents | Unsafe, and disrupts agent and human |
| Per-check-run events | Too noisy. CI is reported per head commit |
| Whole-repo, org, or all-repos subscriptions | Subscriptions are per PR, identified by PR link only |
| Non-herdr delivery (tmux, MCP, raw terminals) | Scope is herdr panes |
| TUI / dashboard | Not needed. `ls` and `status` cover it |
| Auto-fix / agent orchestration | pr-relay notifies; agents decide what to do |
| Windows | herdr targets macOS/Linux |
| Own token management | Reuse `gh` auth |

## Traceability

| Requirement | Phase | Status |
|-------------|-------|--------|
| DIST-01 | Phase 5 | Pending |
| DIST-02 | Phase 5 | Pending |
| DIST-03 | Phase 5 | Pending |
| DIST-04 | Phase 5 | Pending |
| DIST-05 | Phase 5 | Pending |
| DLV-01 | Phase 3 | Pending |
| DLV-02 | Phase 3 | Pending |
| DLV-03 | Phase 3 | Pending |
| DLV-04 | Phase 3 | Pending |
| DLV-05 | Phase 1 | Pending |
| DLV-06 | Phase 1 | Pending |
| DLV-07 | Phase 3 | Pending |
| DLV-08 | Phase 3 | Pending |
| DLV-09 | Phase 3 | Pending |
| DMN-01 | Phase 4 | Pending |
| DMN-02 | Phase 2 | Pending |
| DMN-03 | Phase 2 | Pending |
| DMN-04 | Phase 2 | Pending |
| DMN-05 | Phase 4 | Pending |
| DMN-06 | Phase 4 | Pending |
| DMN-07 | Phase 4 | Pending |
| DMN-08 | Phase 4 | Pending |
| EVT-01 | Phase 1 | Pending |
| EVT-02 | Phase 1 | Pending |
| EVT-03 | Phase 1 | Pending |
| EVT-04 | Phase 1 | Pending |
| EVT-05 | Phase 1 | Pending |
| EVT-06 | Phase 1 | Pending |
| EVT-07 | Phase 1 | Pending |
| EVT-08 | Phase 1 | Pending |
| EVT-09 | Phase 2 | Pending |
| LIFE-01 | Phase 4 | Pending |
| LIFE-02 | Phase 3 | Pending |
| LIFE-03 | Phase 2 | Pending |
| LIFE-04 | Phase 3 | Pending |
| LIFE-05 | Phase 2 | Pending |
| LIFE-06 | Phase 4 | Pending |
| LIFE-07 | Phase 4 | Pending |
| LIFE-08 | Phase 3 | Pending |
| SUB-01 | Phase 4 | Pending |
| SUB-02 | Phase 4 | Pending |
| SUB-03 | Phase 4 | Pending |
| SUB-04 | Phase 2 | Pending |
| SUB-05 | Phase 4 | Pending |
| SUB-06 | Phase 4 | Pending |
| SUB-07 | Phase 4 | Pending |
| SUB-08 | Phase 2 | Pending |
| SUB-09 | Phase 4 | Pending |

**Coverage:**
- v1 requirements: 48 total
- Mapped to phases: 48
- Unmapped: 0

---
*Requirements defined: 2026-10-31*
*Last updated: 2026-10-31 after initial definition*
