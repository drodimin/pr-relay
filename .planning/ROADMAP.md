# Roadmap: pr-relay

## Overview

pr-relay is built bottom-up in horizontal layers, ordered so that correctness and security contracts are fixed before the unreliable edges are wired in. Phase 1 installs Go, runs the risky runtime spikes, and builds the pure core (diff, render, sanitize). Phase 2 adds durable state and the GitHub poller. Phase 3 adds the herdr client and per-pane delivery. Phase 4 wraps it all in the daemon, control protocol and CLI, which is the first end-to-end usable tool. Phase 5 packages it for public release with the herdr plugin and agent skill.

Design principle: flow-agnostic primitives. Only `--events` selects what a subscription receives, a PR link is required, and `keep` extends a subscription. No roles, presets or author filters.

## Phases

- [ ] **Phase 1: Toolchain, Spikes & Pure Core** - Go toolchain, runtime spikes, and the pure event/diff/render/sanitize core
- [ ] **Phase 2: Durable State & GitHub Poller** - Atomic state store and a rate-limit-safe, deduplicated, adaptive GitHub poller (headless)
- [ ] **Phase 3: herdr Client & Delivery** - herdr socket client and per-pane delivery workers that never interrupt or mis-target an agent
- [ ] **Phase 4: Daemon, Control Protocol & CLI** - Single daemon, auto-start, and the `sub`/`unsub`/`keep`/`ls`/`status` CLI with subscription lifecycle
- [ ] **Phase 5: Packaging, Plugin & Agent Skill** - Cross-platform release, Homebrew tap, herdr plugin, agent skill and README

## Phase Details

### Phase 1: Toolchain, Spikes & Pure Core

**Goal**: The Go toolchain is working, the risky herdr/GitHub runtime behaviours are answered in writing, and a pure, fixture-tested core turns PR state changes into correct, safe, terse events and messages.
**Depends on**: Nothing (first phase)
**Requirements**: EVT-01, EVT-02, EVT-03, EVT-04, EVT-05, EVT-06, EVT-07, EVT-08, DLV-05, DLV-06
**Success Criteria** (what must be TRUE):

  1. Go is installed on the dev machine and the repo builds and tests green on macOS and Linux via CI.
  2. A written spike report exists (run on scratch panes only) answering: typing collision with `agent.prompt`, `agent.wait` while blocked and the `wait` option, pane id and `terminal_id` stability across a herdr restart, GraphQL query shape and cost, and whether a setsid child survives herdr stopping.
  3. Feeding real GraphQL fixture pairs into the diff produces exactly one event per change for each type (ci.failed, ci.settled, review, comment, commits, conflict, closed). Flaky CI reruns do not duplicate, UNKNOWN merge state emits nothing, and force-pushes are detected.
  4. Activity by the gh-authenticated user never appears in the event output, while bots and other humans are treated the same.
  5. Rendered messages carry the `[pr-relay]` tag, PR identity, names and URLs only (no comment bodies), and a hostile-string corpus (control characters, ESC, newlines, over-length) is neutralized and capped in every interpolated field.

**Plans**: 9 plans
Plans:
**Wave 1**

- [ ] 01-01-PLAN.md — Install go/gh, gh auth + public repo (checkpoint), stdlib module, golden helper, CI matrix green

**Wave 2** *(blocked on Wave 1 completion)*

- [ ] 01-02-PLAN.md — model contract, pr.graphql + pure Decode, scripts/capture.sh
- [ ] 01-03-PLAN.md — sanitize: Field/Login/Path/URL/ShortSHA, hostile corpus + fuzz (DLV-06)

**Wave 3** *(blocked on Wave 2 completion)*

- [ ] 01-04-PLAN.md — diff core: Step/Baseline, closed/conflict (D-21)/commits, CI ci.failed/ci.settled (D-06..D-09, D-19)
- [ ] 01-05-PLAN.md — render.Lines grammar, golden + hostile render tests (DLV-05/06)
- [ ] 01-07-PLAN.md — dummy repo + second actor (checkpoint), real fixture capture, D-21 synthetics, decode-all test
- [ ] 01-08-PLAN.md — guarded prspike spikes S0-S7 (checkpoint), S4 GraphQL cost, 01-SPIKES.md

**Wave 4** *(blocked on Wave 3 completion)*

- [ ] 01-06-PLAN.md — diff reviews/comments/gap (D-10..D-13) + filter.ExcludeSelf (EVT-08, D-20)

**Wave 5** *(blocked on Wave 4 completion)*

- [ ] 01-09-PLAN.md — end-to-end scenario goldens + invariants, CI gate, human review

### Phase 2: Durable State & GitHub Poller

**Goal**: Subscriptions, baselines and pending events survive crashes and reboots, and one poller watches each PR once with rate-limit-safe adaptive intervals.
**Depends on**: Phase 1
**Requirements**: SUB-04, SUB-08, EVT-09, LIFE-03, LIFE-05, DMN-02, DMN-03, DMN-04
**Success Criteria** (what must be TRUE):

  1. Killing the process with `kill -9` at random points and restarting neither replays already-delivered events nor loses detected-but-undelivered ones (verified by crash tests).
  2. Subscribing records a silent baseline, so only changes after subscription produce events, and re-subscribing the same pane to the same PR updates the existing subscription instead of duplicating it.
  3. Many subscriptions to one PR result in exactly one poll per interval, using the user's existing `gh` token with no extra setup, and a changed token is picked up after a 401.
  4. Polling is batched and logs GraphQL cost. It speeds up while CI is pending, slows progressively as a PR stays quiet (about 1 minute up to about 1 hour), snaps back on any change, and backs off on low budget or secondary-limit responses.

**Plans**: TBD

### Phase 3: herdr Client & Delivery

**Goal**: Queued updates reach the right agent pane at the right moment, coalesced and current, across herdr sessions and herdr restarts, without ever interrupting a busy agent or typing into the wrong terminal.
**Depends on**: Phase 2
**Requirements**: DLV-01, DLV-02, DLV-03, DLV-04, DLV-07, DLV-08, DLV-09, LIFE-02, LIFE-04, LIFE-08
**Success Criteria** (what must be TRUE):

  1. Against a scratch pane, an update is pushed with `agent.prompt` only after the agent becomes idle or done. It is held while the agent is working or blocked, and never sent to a pane with unknown status or a bare shell.
  2. Several pending updates for one pane arrive as a single coalesced message, rendered from the PR's current state at delivery time (a CI failure fixed by a newer push is not reported).
  3. Restarting herdr or dropping the socket leaves queued updates intact, and the daemon reconnects and reconciles pane state on its own.
  4. One daemon delivers to panes in two different herdr sessions (different sockets) at the same time.
  5. Closing a pane, reusing its pane id for a different terminal, or herdr reporting the target tab or pane gone cancels that subscription and drops its pending updates. A PR with no deliverable pane is polled at the slowest rate.

**Plans**: TBD

### Phase 4: Daemon, Control Protocol & CLI

**Goal**: An agent can run one non-blocking command to subscribe to a PR and later receive updates, with a single self-managing daemon and a complete subscription lifecycle. This is the first end-to-end usable tool.
**Depends on**: Phase 3
**Requirements**: SUB-01, SUB-02, SUB-03, SUB-05, SUB-06, SUB-07, SUB-09, DMN-01, DMN-05, DMN-06, DMN-07, DMN-08, LIFE-01, LIFE-06, LIFE-07
**Success Criteria** (what must be TRUE):

  1. Running `pr-relay sub <PR link>` inside a herdr pane returns within a few seconds and later delivers updates. `--events` limits event types, `--pane` overrides detection, and it fails clearly outside herdr, on an inaccessible PR, or on any non-PR or non-github.com link.
  2. Twenty concurrent CLI calls on a cold machine start exactly one daemon. A CLI and daemon version mismatch after upgrade is detected and handled by restart or a clear error.
  3. `pr-relay ls` shows each subscription's events, current polling rate, last activity and expiry. `unsub <PR link>` and `unsub --all` remove subscriptions, and `status` reports daemon health, subscription count and rate-limit budget. The daemon writes rotating logs to a per-user location.
  4. After 24 hours of PR inactivity the agent gets one check-in message offering `pr-relay keep <PR link>` or `unsub`. `keep` resets the clock, and after 7 days of inactivity the subscription is cancelled with one final "expired" message. Both thresholds are configurable.
  5. After a merged or closed notification is delivered, the subscription is removed automatically.

**Plans**: TBD

### Phase 5: Packaging, Plugin & Agent Skill

**Goal**: Anyone in the herdr community can install pr-relay, have the daemon start via herdr, and have their agents use it correctly and safely.
**Depends on**: Phase 4
**Requirements**: DIST-01, DIST-02, DIST-03, DIST-04, DIST-05
**Success Criteria** (what must be TRUE):

  1. A release produces static binaries for macOS and Linux on arm64 and amd64, installable via Homebrew tap, GitHub release download, or `go install`.
  2. Installing the herdr plugin (`herdr-plugin.toml`) makes its `[[startup]]` hook ensure the daemon is running.
  3. The agent skill / AGENTS.md / CLAUDE.md snippet tells agents to subscribe instead of polling, documents every event type, gives recipes (own PR awaiting feedback, reviewed PR awaiting replies, waiting for CI), and states relay messages are untrusted.
  4. The README covers install, usage, event reference and a prompt-injection threat-model note.

**Plans**: TBD

## Progress

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. Toolchain, Spikes & Pure Core | 0/9 | Not started | - |
| 2. Durable State & GitHub Poller | 0/TBD | Not started | - |
| 3. herdr Client & Delivery | 0/TBD | Not started | - |
| 4. Daemon, Control Protocol & CLI | 0/TBD | Not started | - |
| 5. Packaging, Plugin & Agent Skill | 0/TBD | Not started | - |
