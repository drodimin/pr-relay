# Phase 2: Durable State & GitHub Poller - Research

**Researched:** 2026-11-01
**Domain:** Go daemon core: crash-safe JSON state (transactional outbox in one file), GitHub GraphQL batch polling, gh-token reuse, adaptive cadence and rate-limit budgeting
**Confidence:** HIGH for the stack, go-gh auth behaviour, fsync/rename semantics, synctest and GitHub limits (all checked against source or official docs this session). MEDIUM for the cadence/budget formulas, which are design recommendations.

## Summary

Phase 2 sits on top of a finished pure core. `diff.Step(Memory, Snapshot)` never mutates its input and is deterministic. `ghquery.Decode` turns one PRState response into a `Snapshot` plus `RateLimit`. `filter.ExcludeSelf` and `render.Lines` complete the pipeline. `go.mod` has no dependencies yet, and `main.go` only prints the version. Phase 2 adds four I/O packages: `store` (atomic JSON state file), `ghclient` (token and GraphQL transport), `poller` (scheduler, cadence, budget, fan-out) and `config`. It also adds a batch builder and batch decoder to `ghquery`, folds in the nine Phase 1 review fixes, and adds a hidden `dev watch` cobra command.

Exactly-once event creation (EVT-09, SC-1) works because the whole state lives in one file. "Commit the new Memory and append the derived events to every matching subscription's pending list" is a single in-memory mutation followed by one atomic file replace: write a temp file, `fsync`, `rename`, then `fsync` the directory. A crash before the rename leaves the old Memory, so the next poll derives the same events again, and they were never stored anywhere before. A crash after the rename leaves both the events and the Memory. No two-phase problem exists. Go's `(*os.File).Sync` already issues `F_FULLFSYNC` on macOS (verified in the Go 1.27.3 source). Copy-on-write is required: clone the state, mutate, persist, then swap. In-memory state must never advance past what is on disk.

Two non-obvious findings shape the plan. (1) The gh config that go-gh reads is cached process-wide behind a `sync.Once` (`config.Read`). Calling `auth.TokenForHost` again after a 401 does not see a rewritten `hosts.yml` unless `config.Reload(nil)` is called first (verified in the go-gh v2.17.0 source). The keyring fallback runs `gh auth token` through `exec.Command` with no timeout. (2) `testing/synctest` fakes `time` inside a bubble, but a goroutine blocked on real network I/O is not "durably blocked", so an `httptest.Server` inside a bubble stops the fake clock from advancing. Cadence and budget tests should use an in-memory `http.RoundTripper` fake inside synctest. Real `httptest` servers are only for the subprocess crash loop and the ghclient wire tests.

**Primary recommendation:** Build one mutex-guarded `poller.Engine`. It owns a copy-on-write `store.State` that is persisted atomically after every state-changing step. One scheduler goroutine batches all due PRs into one aliased GraphQL query per tick, with concurrency 1. Cadence and budget are pure functions of persisted timestamps. Tokens come from go-gh `auth` after `config.Reload`. Prove SC-1 with a SIGKILL subprocess loop against an `httptest` fake GitHub, using an idempotent, seq-numbered dev sink.

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions

#### Carried forward (already locked)
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

#### Rate-limit budget
- **D-05:** Soft cap: pr-relay aims to use at most **10%** of the hourly GraphQL budget (about 500 of 5000 points per hour). If the cadence curve would go over the cap, intervals stretch to fit. Forced polls count toward the cap.
- **D-06:** When the overall remaining budget is low (pr-relay shares it with the user's own `gh` and with agents), stretch every interval in proportion. **Never pause entirely**: polling always continues, at least at the stretched slowest rate. At remaining = 0, requests fail anyway, so the poller waits for `resetAt`. That wait is forced by GitHub, not a pause policy.
- **D-07:** On a secondary rate limit (403/429), honour `Retry-After`, or wait 60 s if the header is missing. Double the wait on each repeated hit, up to about 15 min, and shrink the batch size after the first hit.
- **D-08:** The budget cap percentage, the low-budget threshold, and the min and max intervals can be set in `config.toml`, with the defaults above.

#### Polling cadence (LIFE-05, redefined by the user)
- **D-09:** **60 s** for any active PR, including while CI is pending. There is no separate CI-pending rate.
- **D-10:** A PR with no activity ramps geometrically: about 1m → 1.5m → 2m → 3m → 4m → 6m → 8m → 11m → **16m**. It reaches 16 min after about 4 h of quiet. The planner can tune the exact steps, but the endpoints are 60 s at t=0 and 16 min at about 4 h.
- **D-11:** **The ceiling is 16 min**, replacing LIFE-05's "about 1 hour". The PR stays at 16 min through the 24 h check-in and after it, including when the agent doesn't answer the check-in. Only new activity resets the rate. Wherever LIFE-06 or LIFE-08 says "slowest rate", it means 16 min. Budget stretching (D-05, D-06) is the only thing that can make an interval longer than 16 min.
- **D-12:** "Activity" that resets the rate to 60 s means **events that would be delivered, plus the gh user's own pushes**. Your own comments and reviews, which are filtered out, don't reset it. Your own pushes do, because CI follows them.
- **D-13:** The **first** subscription to a PR that isn't watched yet starts polling at 60 s. A further subscription to a PR that is already watched does **not** reset the rate: it joins the existing cadence.
- **D-14:** `keep` (Phase 4 CLI) leaves the rate at 16 min and only resets the 24 h check-in clock. Phase 2 stores the activity and check-in timestamps that support this.
- **D-15:** **Force poll / rate reset.** An explicit command, run after `sub` (name decided in Phase 4, e.g. `pr-relay poll <PR>`), polls the PR immediately and resets its shared rate to 60 s. It is **not throttled**, but it still counts toward the budget cap. Phase 2 builds the poller primitive (`PollNow` / `ResetRate`).

#### Subscriptions, baseline & pending events
- **D-16:** **Multiple subscribers per PR are allowed.** One poll serves all of them (DMN-02). The subscribe result reports the **other panes subscribed to that PR**, the **current interval** and the **next poll time**, so the agent knows who else is watching and how fresh the data is. Phase 2 provides that data, and Phase 4 renders it.
- **D-17:** A second subscriber **reuses the PR's existing Memory and polling**. There is no fresh fetch and **no timestamp history filter**. The user accepts that the next poll may deliver something that happened up to one interval before the subscription. The first subscriber to an unwatched PR gets a fresh baseline fetch (SUB-08).
- **D-18:** **Re-subscribing** the same (socket, pane) to the same PR updates the subscription in place (SUB-04). It replaces the event mask and refreshes the activity and expiry clocks. It **keeps** the baseline and the pending events, re-filtered against the new mask.
- **D-19:** **Pending events have no TTL.** They stay until delivered or until the subscription ends. Each subscription holds about 200 at most. When that cap is exceeded, the oldest are dropped and a "some updates dropped" marker is recorded for delivery. Phase 3 renders from the PR's current state at delivery time.
- **D-20:** **Corrupt or unreadable state file at startup:** rename it to `state.json.corrupt-<timestamp>`, start with empty state, and log an error. Phase 4's `status` should show this. Agents have to re-subscribe.

#### Headless harness & verification
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

### Deferred Ideas (OUT OF SCOPE)
- **The force-poll / rate-reset CLI command** (D-15) is a new CLI surface for Phase 4. Add it to REQUIREMENTS.md (for example a SUB-10) when planning Phase 4.
- **Update REQUIREMENTS.md LIFE-05 and LIFE-06 and ROADMAP Phase 2 SC-4** to the 16-min ceiling (D-11). This is a documentation change.

Also out of scope for Phase 2 (CONTEXT boundary): herdr delivery and agent-status gating (Phase 3); the control socket, user-facing CLI, daemon auto-start, check-in/expiry messages (Phase 4); packaging (Phase 5).
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| SUB-04 | Re-subscribing the same pane to the same PR updates in place | Subscription key = (herdrSocket, paneID, PRRef) (S0/S3 findings). `Subscribe` upserts, replaces the mask, refreshes clocks and re-filters pending (D-18). See Pattern 4. |
| SUB-08 | Silent baseline at subscribe; only later changes delivered | First sub creates a PR record with `Memory{Baselined:false}` that is due now. `diff.Step` on an un-baselined Memory returns `Baseline(s)` with no events (verified in `diff.go`). Second sub joins the existing Memory (D-17). |
| EVT-09 | At most once in normal operation; restart neither replays nor drops | Single-file transactional outbox: Memory, pending appends and the seq counter go in one atomic write (Pattern 1). Epoch keys (WR-02), watermarks (CR-01), and seq-based `Ack(sub, upToSeq)`. |
| LIFE-03 | Subscriptions and pending survive restart and reboot | Atomic write with `F_FULLFSYNC` on darwin (Go source) plus a directory fsync. Corrupt-file quarantine (D-20). Leftover temp-file cleanup. |
| LIFE-05 | Progressive slowdown and snap-back (ceiling 16 min per D-11) | Pure `cadence.Interval(quietFor)` step table anchored on persisted `RateAnchor` (Pattern 5). Activity classification per D-12. |
| DMN-02 | One poll per PR regardless of subscriber count | PR records keyed by `PRRef.String()`. Subscriptions reference them. The scheduler iterates PR records, never subscriptions. |
| DMN-03 | Uses existing gh auth, recovers on token change | `config.Reload(nil)` then `auth.TokenFromEnvOrConfig`, then a bounded `gh auth token` fallback. Re-resolve on 401. `viewer { login }` in every batch (Pattern 3). |
| DMN-04 | Batched, adaptive, backs off on low budget or secondary limits | Aliased batch query with variables and fragments, up to 20 PRs. Cost ledger and stretch factor (D-05/D-06). Secondary-limit state machine (D-07). Cost logged per poll (Pattern 6). |
</phase_requirements>

## Project Constraints (from CLAUDE.md)

- Go, single static binary, `CGO_ENABLED=0`. Low idle memory and CPU. macOS and Linux both first-class.
- Auth must reuse the existing `gh` auth through go-gh `auth` (`TokenForHost` resolution order). Never shell out to `gh api` per poll. Never require the `gh` binary except as the keyring fallback. Re-resolve the token on 401 and do not cache it forever. Never log the token.
- Use your own `http.Client` for the poller (about 30 lines, full header control) rather than go-gh's REST/GraphQL clients.
- Request volume must stay roughly independent of subscriber count and must back off as the budget runs low.
- State: JSON file with atomic write (temp, fsync, rename). SQLite only as fallback.
- Logging: stdlib `log/slog`. Config: `config.toml` via `github.com/BurntSushi/toml`. Singleton lock: `github.com/gofrs/flock`. CLI: `spf13/cobra`.
- Testing: stdlib `testing` and `httptest` fake GitHub. Injectable clock or `testing/synctest`. Golden files for formatting. Never send prompts to real panes in CI (not relevant here, since Phase 2 has no herdr I/O).
- Agent-agnostic. Nothing Claude Code-specific.
- GSD workflow: edits only through GSD commands (planner/executor concern).

## Architectural Responsibility Map

This project is a local daemon, so web tiers are mapped onto daemon layers.

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Subscription registry and upsert (SUB-04) | Engine (`poller.Engine`, in-process core) | Store (persistence) | Business rules (key, mask, clocks) live in the core. The store only serializes. |
| Durable state and outbox (LIFE-03, EVT-09) | Store (`internal/store`, filesystem) | — | Atomic file replace, lock, quarantine and schema version. No business logic. |
| Baseline and diff (SUB-08) | Pure core (`diff`) | Engine | Already built. The engine decides when a Memory is new. |
| Event fan-out (mask, self-filter, cap 200) | Engine | `filter` (pure) | Per-subscription routing happens after the PR-level diff (ARCHITECTURE anti-pattern 2). |
| Scheduling, cadence, budget (LIFE-05, DMN-04) | Engine (`poller`): `cadence.go` and `budget.go` as pure functions | ghclient (reports cost and headers) | Pure functions of persisted timestamps can be tested in synctest. |
| GraphQL transport, token, error classification (DMN-03) | ghclient (network edge) | go-gh `auth`/`config` | The only package that touches the network or the token. |
| Batch query build and decode (WR-04) | `ghquery` (pure) | — | Keeps the decoder pure and fixture-tested. |
| Paths and config (D-08) | `config`, `paths` | — | Tiny, and lets tests override directories through env. |
| Dev harness (D-21) | `cmd/pr-relay` (cobra, hidden) | Engine | Thin wiring only. |

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| Go toolchain | go1.27.3 installed. The `go` directive must become **1.26.7** (see pitfall 9). | Language | [VERIFIED: `go version`; go-gh go.mod says `go 1.26.7`] |
| `github.com/cli/go-gh/v2` (only `pkg/auth` and `pkg/config`) | v2.17.0 (2026-10-20) | Token resolution: env, then hosts.yml, then `gh auth token`; `config.Reload` | [VERIFIED: proxy.golang.org @latest plus source read of `pkg/auth/auth.go` and `pkg/config/config.go`] |
| stdlib `net/http`, `encoding/json`, `os`, `log/slog`, `syscall` | — | Own GraphQL client, state file, logging | [CITED: CLAUDE.md] |
| `github.com/spf13/cobra` | v1.10.2 (2025-12-03) | Root command, `version`, hidden `dev watch` | [VERIFIED: proxy.golang.org] CLAUDE.md stack |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `github.com/BurntSushi/toml` | v1.6.0 (2025-12-18) | `config.toml` (D-08 knobs) | Config load at startup. A missing file means defaults. [VERIFIED: proxy.golang.org] |
| `github.com/gofrs/flock` | v0.13.1 (2026-08-27) | Exclusive lock on `<stateDir>/state.lock`, so `dev watch` and a later daemon never both write | `store.Open` takes `TryLock` and fails fast if the lock is held. [VERIFIED: proxy.golang.org] |
| `golang.org/x/text/unicode/norm` | v0.43.0 (2026-10-09) | NFKC folding for CR-02 | `sanitize` only. It is already a transitive dependency of go-gh. [VERIFIED: proxy.golang.org; go-gh go.mod requires v0.43.0] |
| stdlib `testing/synctest` | Go ≥1.25 (`synctest.Test`, `synctest.Wait`) | Fake time for cadence, budget and backoff tests | [VERIFIED: `go doc testing/synctest` on 1.27.3] |
| stdlib `net/http/httptest` | — | Fake GitHub for ghclient wire tests and the crash loop | Outside synctest bubbles only. |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `auth.TokenForHost` as-is | `config.Reload` + `auth.TokenFromEnvOrConfig` + own `exec.CommandContext(gh auth token --secure-storage --hostname h)` with a 10 s timeout | Same resolution order (mirrors the `TokenForHost` source exactly), but the keyring fallback is bounded, so a locked keychain cannot hang the poller. **Recommended.** |
| gofrs/flock | `syscall.Flock(fd, LOCK_EX\|LOCK_NB)` | Zero dependencies and works on darwin and linux. Use flock for consistency with Phase 4's daemon singleton (CLAUDE.md). Either is fine. |
| Injectable `Clock` interface | `testing/synctest` with real `time` | synctest needs no production abstraction, but its fake time breaks with real sockets. Use synctest plus a RoundTripper fake. Keep a `now func() time.Time` field only where a pure function needs a timestamp argument anyway. |
| Mutex-guarded Engine | Actor goroutine with channel inbox | Both work. A mutex is simpler for synchronous primitives (`Subscribe` returns a `SubInfo`). Note that `sync.Mutex` is not durably blocking under synctest, but short critical sections are fine. |

**Installation:**
```bash
go get github.com/cli/go-gh/v2@v2.17.0 github.com/spf13/cobra@v1.10.2 \
       github.com/BurntSushi/toml@v1.6.0 github.com/gofrs/flock@v0.13.1 \
       golang.org/x/text@v0.43.0
go mod tidy   # bumps the go directive to 1.26.7 because go-gh requires it
```

## Package Legitimacy Audit

slopcheck could not be installed (the pip install was denied by the sandbox), so per protocol every package below is **[ASSUMED]** for legitimacy. Mitigating evidence: all five are named in the project's own CLAUDE.md stack, all resolve on proxy.golang.org with official VCS origins (shown below), and go-gh's source was downloaded and read. Go modules have no postinstall scripts, so that risk does not apply.

| Package | Registry | Age | Downloads | Source Repo | slopcheck | Disposition |
|---------|----------|-----|-----------|-------------|-----------|-------------|
| github.com/cli/go-gh/v2 | Go proxy | v2.17.0 2026-10-20 (project since 2022) | n/a (Go) | github.com/cli/go-gh | unavailable | Approved [ASSUMED]; official GitHub CLI org |
| github.com/spf13/cobra | Go proxy | v1.10.2 2025-12-03 | n/a | github.com/spf13/cobra | unavailable | Approved [ASSUMED] |
| github.com/BurntSushi/toml | Go proxy | v1.6.0 2025-12-18 | n/a | github.com/BurntSushi/toml | unavailable | Approved [ASSUMED] |
| github.com/gofrs/flock | Go proxy | v0.13.1 2026-08-27 | n/a | github.com/gofrs/flock | unavailable | Approved [ASSUMED] |
| golang.org/x/text | Go proxy | v0.43.0 2026-10-09 | n/a | go.googlesource.com/text | unavailable | Approved [ASSUMED]; Go project |

**Packages removed due to slopcheck [SLOP] verdict:** none
**Packages flagged as suspicious [SUS]:** none. slopcheck was unavailable, so the planner may add one `checkpoint:human-verify` before the first `go get`, per protocol.

## Architecture Patterns

### System Architecture Diagram

```
 dev watch (cobra, hidden)            [Phase 4: ctl server calls the same Engine API]
   | Subscribe / PollNow / Pending / Ack
   v
 +--------------------------- poller.Engine (mutex) ---------------------------+
 |  state *store.State  (copy-on-write; disk is source of truth)               |
 |                                                                             |
 |  Scheduler goroutine:                                                       |
 |    timer -> pick due PRs (+ near-due within the coalesce window)            |
 |          -> budget gate (secondary backoff? remaining==0? cap ledger?)      |
 |          -> chunk to batchSize (20, shrinks after a secondary hit)          |
 |          -----------------------------+                                     |
 |                                       v                                     |
 |                    ghclient.Batch(ctx, refs)  (lock NOT held)               |
 |                       token: config.Reload -> env/hosts.yml -> gh (10 s)    |
 |                       POST api.github.com/graphql (30 s timeout)            |
 |                       classify: 401 | 403/429 secondary | RATE_LIMITED |    |
 |                                 5xx/timeout | per-alias errors             |
 |                                       |                                     |
 |                    ghquery.DecodeBatch -> per-alias Snapshot|err, RateLimit,|
 |                                         viewer login                        |
 |          <----------------------------+                                     |
 |    lock; for each PR result (skip if PR generation changed):                |
 |       r := diff.Step(pr.Memory, snap)                                       |
 |       if r.Gap -> wide re-fetch, re-Step from the SAME old Memory           |
 |       delivered := filter.ExcludeSelf(r.Events, viewer)                     |
 |       activity  := len(delivered)>0 || any raw commits event  (D-12)        |
 |       for sub in subs(pr): append mask-matching events (seq++, cap 200)     |
 |    next := state.Clone() + mutations  -> store.Save(next) (atomic)          |
 |    on success: state = next ; record cost in ledger ; reschedule            |
 |    on failure: discard next (Memory not advanced -> events re-derived)      |
 +-----------------------------------------------------------------------------+
                                          |
                                          v
            <stateDir>/state.json   (tmp -> fsync -> rename -> fsync dir)
            <stateDir>/state.lock   (flock, held for the process lifetime)
```

### Recommended Project Structure
```
internal/
├── paths/        # StateDir(), ConfigPath(); honors PR_RELAY_STATE_DIR, XDG_STATE_HOME, XDG_CONFIG_HOME
├── config/       # Config struct + defaults + toml load (D-08)
├── store/        # State types (schema v1), Open/lock, Load (quarantine), Save (atomic), Clone
├── ghclient/     # TokenSource, Client.Batch(ctx, []PRRef, window) -> BatchResult, typed errors
├── ghquery/      # + fragment.graphql, BuildBatch(refs, window), DecodeBatch(data, n)
├── poller/       # Engine, scheduler loop, cadence.go (pure), budget.go (pure), fanout.go
├── diff/ model/ filter/ render/ sanitize/   # Phase 1, plus the D-04 fixes
cmd/pr-relay/     # cobra root, version, hidden "dev watch"
```

### Pattern 1: Single-file transactional outbox (copy-on-write)
**What:** All durable state is one `store.State` value. Every mutation (poll commit, subscribe, ack, keep, reset) does: clone, mutate, `Save`, then swap the pointer. The events and the Memory they came from are therefore in the same rename.
**When to use:** Every state change that must survive a crash. Pure timing data (`LastPollAt`) may skip a write when nothing else changed. See pitfall 6.
```go
// store.Save: atomic replace. Source: Go os docs + Go 1.27.3 internal/poll/fd_fsync_darwin.go
func (s *Store) Save(st *State) error {
	b, err := json.Marshal(st) // or MarshalIndent for inspectability
	if err != nil { return err }
	f, err := os.CreateTemp(s.dir, "state.json.tmp-*")
	if err != nil { return err }
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after a successful rename
	if err = f.Chmod(0o600); err == nil {
		if _, err = f.Write(b); err == nil {
			err = f.Sync() // F_FULLFSYNC on darwin, fsync on linux
		}
	}
	if cerr := f.Close(); err == nil { err = cerr }
	if err != nil { return err }
	if err = os.Rename(tmp, filepath.Join(s.dir, "state.json")); err != nil { return err }
	d, err := os.Open(s.dir) // make the rename itself durable (linux needs this)
	if err != nil { return err }
	defer d.Close()
	return d.Sync()
}
```

### Pattern 2: State schema (v1)
Recommended shape. The planner may rename fields, but the structure matters.
```go
type State struct {
	Version   int                     `json:"version"`    // 1; Load refuses Version > known (pitfall 7)
	Seq       uint64                  `json:"seq"`        // global monotonic pending-event seq
	Viewer    string                  `json:"viewer"`     // last seen viewer.login
	PRs       map[string]*PRRecord    `json:"prs"`        // key: PRRef.String() (after WR-07 validation)
	Subs      map[string]*Subscription `json:"subs"`      // key: stable sub ID (e.g. hash of socket|pane|pr)
}
type PRRecord struct {
	Ref          model.PRRef  `json:"ref"`
	Memory       model.Memory `json:"memory"`       // Baselined=false until the first poll
	Gen          uint64       `json:"gen"`          // bumped on (re)create; stale in-flight results are dropped
	CreatedAt    time.Time    `json:"created_at"`
	RateAnchor   time.Time    `json:"rate_anchor"`  // cadence anchor: first sub, activity, ResetRate (D-13/D-15)
	LastActivity time.Time    `json:"last_activity"`// D-12 activity only (drives the check-in clock with Sub.ClockAt)
	LastPollAt   time.Time    `json:"last_poll_at"`
	LastError    string       `json:"last_error,omitempty"` // NOT_FOUND/FORBIDDEN etc. for status/SUB-07
	Terminal     bool         `json:"terminal,omitempty"`   // MERGED: stop polling
}
type Subscription struct {
	ID, PR, HerdrSocket, PaneID, AgentSession, TerminalID string // identity per S0/S3
	Events     []model.EventType // mask
	CreatedAt, UpdatedAt, ClockAt time.Time // ClockAt: re-sub (D-18) and keep (D-14) reset it
	Pending    []PendingEvent
	Dropped    int // D-19 "some updates dropped" marker count since last drain
}
type PendingEvent struct {
	Seq        uint64      `json:"seq"`
	Event      model.Event `json:"event"`
	DetectedAt time.Time   `json:"detected_at"`
}
```
Add explicit `json` tags to `model.Memory` and `model.Event`, so persisted names do not change silently when Go fields are renamed. They stay stdlib-only and pure. Add a golden "state v1" fixture test that loads a committed `testdata/state-v1.json`.

### Pattern 3: Token source with reload and bounded keyring fallback
```go
// Mirrors go-gh v2.17.0 auth.TokenForHost (source-verified), with Reload + timeout.
func (t *TokenSource) Token(ctx context.Context) (tok, source string, err error) {
	_ = config.Reload(nil)                       // config.Read is sync.Once-cached
	if tok, src := auth.TokenFromEnvOrConfig("github.com"); tok != "" {
		return tok, src, nil                     // GH_TOKEN, GITHUB_TOKEN, hosts.yml oauth_token
	}
	gh := os.Getenv("GH_PATH")
	if gh == "" { gh, _ = exec.LookPath("gh") } // Phase 4/5: add /opt/homebrew/bin, /usr/local/bin fallbacks
	if gh == "" { return "", "", ErrNoToken }
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, gh, "auth", "token", "--secure-storage", "--hostname", "github.com").Output()
	if tok = strings.TrimSpace(string(out)); err != nil || tok == "" { return "", "gh", ErrNoToken }
	return tok, "gh", nil
}
```
Cache the token in memory and re-resolve only on a 401, or when `ErrNoToken` is in effect (then retry every about 5 min, D-discretion). On a 401: re-resolve, retry the batch once, and if it still fails, enter an `auth_failed` state with slow retries. Expose `AuthState{OK|NoToken|Unauthorized, Source, Since}` for Phase 4 `status`. Put `viewer { login }` in every batch query. It costs nothing, and a `gh auth switch` that changes the account updates the self-filter login without another request.

### Pattern 4: Subscribe/upsert semantics
```go
func (e *Engine) Subscribe(req SubRequest) (SubInfo, error)
// key := (req.HerdrSocket, req.PaneID, req.PR)   // SUB-04
// existing sub: replace mask, UpdatedAt=ClockAt=now, re-filter Pending by new mask (D-18)
// new sub, PR unwatched: create PRRecord{Memory:{}, RateAnchor:now, Gen:++}, due now (D-13, SUB-08)
// new sub, PR watched:   attach; do NOT touch RateAnchor (D-13, D-17)
// persist, then return SubInfo{SubID, Created bool, CoSubscribers []PaneRef, Interval, NextPollAt}
```
Other primitives Phase 3 and 4 call: `Unsubscribe(subID)` (when the last sub goes, delete the PRRecord), `Keep(subID)` (`ClockAt=now` only, D-14), `PollNow(ctx, ref) (PollResult, error)` (sets `RateAnchor=now`, runs a forced batch immediately and returns the result or error, so SUB-07 can validate later), `ResetRate(ref)`, `Pending(subID) ([]PendingEvent, dropped int)`, `Ack(subID, upToSeq)` (persisted), `CoSubscribers(ref)`, `Schedule(ref) (interval, next)`, `Status()` (budget, auth, corrupt-file flag, per-PR errors).

### Pattern 5: Cadence as a pure function of persisted time
```go
// quiet = now - pr.RateAnchor. Endpoints fixed by D-09/D-10/D-11; table tunable (discretion).
var steps = []struct{ upTo, every time.Duration }{
	{10 * time.Minute, 1 * time.Minute},
	{20 * time.Minute, 90 * time.Second},
	{35 * time.Minute, 2 * time.Minute},
	{55 * time.Minute, 3 * time.Minute},
	{85 * time.Minute, 4 * time.Minute},
	{120 * time.Minute, 6 * time.Minute},
	{165 * time.Minute, 8 * time.Minute},
	{240 * time.Minute, 11 * time.Minute},
} // beyond 240m -> 16m (max). About 57 polls in the first 4 h, then 3.75 per hour.
func BaseInterval(quiet time.Duration, min, max time.Duration) time.Duration
func Effective(base time.Duration, stretch float64, jitter float64) time.Duration // jitter in [-0.1,+0.1]
// next due = pr.LastPollAt + Effective(BaseInterval(LastPollAt - RateAnchor), stretch, j)
```
Because the curve is derived from `RateAnchor` (persisted), a restart resumes the same position on the curve. It needs no step counter. Activity (D-12) sets `RateAnchor = LastActivity = now`. When the first subscription creates the record, `RateAnchor = now` (D-13). `ResetRate` and `PollNow` set `RateAnchor = now` (D-15). `keep` does not touch it (D-14). Scale the table with `min`/`max` from config, which lets the crash test run at 100 ms.

**Batch coalescing (discretion):** when the timer fires, take every PR whose due time is ≤ `now + min(15s, 0.25*its interval)`. The query cost floor is 1 point per query (S4: 1 PR and 5 PRs both cost 1), so pulling near-due PRs forward is nearly free and lowers query count. Order by due time, then chunk to `batchSize`. Run batches one at a time (concurrency 1). Cap the scheduler's sleep at 30 s and recompute from wall-clock `time.Now()`. That way, after a laptop sleep, overdue PRs are found within 30 s and go out as a few batches, not a burst.

### Pattern 6: Budget (D-05, D-06, D-07) as pure functions
```go
type Budget struct {
	Ledger     []spend   // own cost per request in the last 60 min (forced polls included, D-15)
	Limit, Remaining int // from the last rateLimit
	ResetAt    time.Time
	SecondaryUntil time.Time; SecondaryStreak int; BatchSize int
}
// capStretch: projected own spend per hour vs cap.
//   projected = Σ_pr 3600/base_pr × costPerPRPoll  (EWMA of cost/len(batch), initial 0.25, floor-aware)
//   capStretch = max(1, projected / (capPct × Limit))      // default capPct 0.10 -> 500
// lowStretch: if Remaining < lowThreshold (default 0.20 × Limit):
//   lowStretch = lowThreshold / max(Remaining, 1), clamped to [1, maxStretch]
// stretch = max(capStretch, lowStretch); effective intervals are capped at maxStretchedInterval
//   (e.g. 60 m, configurable): never a full pause (D-06).
// Hard gates before sending a non-forced batch:
//   now < SecondaryUntil                       -> wait (forced polls also wait: GitHub-enforced)
//   Remaining == 0 && now < ResetAt            -> wait until ResetAt + small jitter
//   sum(Ledger) >= cap                          -> delay until the oldest ledger entry expires
// Secondary hit: wait = Retry-After or 60 s; ×2 per consecutive hit, ≤ 15 min; BatchSize = max(1, BatchSize/2)
//   on the first hit; after N clean batches, restore toward 20.
```
Log every batch at Info with `slog`: `prs`, `cost`, `remaining`, `limit`, `reset_at`, `node_count`, `stretch`, `batch_size`, `duration_ms`. `dev watch` prints the same line (D-21).

### Pattern 7: Batch query = fragment + variables + aliases
Refactor `pr.graphql` into `fragment PRFields on PullRequest { ... }`, and parameterize the activity windows (`$reviewsLast`, `$commentsLast`, `$reviewCommentsFirst`) so the normal and wide (gap) queries share one fragment. The single-PR `query PRState(...)` stays in the same document, so `scripts/capture.sh` (`-F query=@internal/ghquery/pr.graphql`) keeps working. GitHub accepts a document with one operation plus fragments [ASSUMED, standard GraphQL]. The batch builder emits:
```graphql
query PRBatch($o0:String!,$r0:String!,$n0:Int!, ..., $rl:Int!,$cl:Int!,$rcf:Int!) {
  rateLimit { cost limit remaining used resetAt nodeCount }
  viewer { login }
  pr0: repository(owner:$o0, name:$r0) { pullRequest(number:$n0) { ...PRFields } }
  ...
}
fragment PRFields on PullRequest { ... }
```
Pass owner, repo and number as variables, never interpolated, even though `PRRef.Valid` already restricts them. `DecodeBatch` unmarshals `data` into `map[string]json.RawMessage` and reuses the existing `wirePR` path per alias. Errors are classified by `path[0]`: an alias prefix means that PR's error. A deeper path inside the PR (for example `checkSuite.workflowRun` `FORBIDDEN`) is a warning while the data is still present (WR-04). No path, or type `RATE_LIMITED`, means a batch-level error.

### Pattern 8: Gap follow-up = discard and re-Step (discretion)
`diff.Step` is pure, so when `r.Gap` is true the engine throws `r` away, re-fetches that one PR with wide windows (`last: 100`) and re-runs `Step` from the **same old Memory** before committing. If it is still `Gap` at 100, commit and record a `Dropped`-style marker on the matching subscriptions. Committing the narrow result first would be wrong with CR-01 watermarks: the watermark would move past the unseen items and suppress them forever.

### Anti-Patterns to Avoid
- **Advancing in-memory state before `Save` succeeds:** on a full disk or I/O error, the Memory moves ahead and the events exist only in RAM. A crash then loses them, which violates EVT-09. Use copy-on-write and swap after a successful save.
- **One goroutine or ticker per PR:** creates request bursts and secondary limits (PITFALLS 13). Use one scheduler.
- **Iterating subscriptions to decide polls:** breaks DMN-02. Iterate PR records.
- **Deduping pending by `Event.Key` before WR-02:** keys repeat across reopen and head return, which drops legitimate events. Fix the keys first, then dedupe per subscription on Key.
- **`httptest.Server` inside a synctest bubble:** the fake clock never advances because the accept goroutine is blocked on network I/O. Use an in-memory `RoundTripper`.
- **Using the go-gh REST/GraphQL clients for the poller:** CLAUDE.md asks for your own client, and you need raw status and headers (`Retry-After`, `X-RateLimit-*`).
- **Logging the token or `Authorization` header**, or response bodies, which can contain user content.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| gh token discovery (env, hosts.yml, GHES rules) | Parsing `~/.config/gh/hosts.yml` yourself | go-gh `auth.TokenFromEnvOrConfig` + `config.Reload` | Handles GH_CONFIG_DIR, XDG_CONFIG_HOME, env precedence and tenancy hosts (source-verified). |
| Unicode compatibility folding (CR-02) | Your own fullwidth tables | `golang.org/x/text/unicode/norm.NFKC` | Covers the fullwidth and compatibility forms. You only add a small Cyrillic/Greek skeleton map for `p r e l a y` and bracket lookalikes. |
| TOML parsing | — | BurntSushi/toml | Stack decision. |
| File locking | pid files | gofrs/flock | pid reuse and stale files (PITFALLS). |
| Fake time | sleeps in tests | `testing/synctest` | Deterministic, instant, stdlib. |
| Durable fsync on macOS | `syscall.Fsync` | `(*os.File).Sync` | Go already uses F_FULLFSYNC with an ENOTSUP fallback (verified). |

**Key insight:** the hard parts here are semantic (which timestamp anchors what, when a commit is allowed), not mechanical. Keep cadence, budget and batch decode as pure functions and test them exhaustively. Keep the I/O shells thin.

## Runtime State Inventory

Not a rename or migration phase, so the full inventory does not apply. New runtime state that this phase introduces, for Phase 4/5 awareness:
- Stored data: `<stateDir>/state.json`, `state.lock`, `state.json.corrupt-*` (new). No pre-existing data needs migration (verified: no state code exists in the repo).
- Live service config, OS-registered state, secrets: none (the token is read at runtime and never persisted).
- Build artifacts: none.

## Common Pitfalls

### Pitfall 1: go-gh config is cached for the process lifetime
**What goes wrong:** after `gh auth login` rewrites hosts.yml, the daemon keeps getting 401s with the stale token.
**Why:** `config.Read` is `sync.Once` (go-gh v2.17.0 `pkg/config/config.go`), and `TokenForHost` → `TokenFromEnvOrConfig` → `config.Read(nil)`.
**How to avoid:** call `config.Reload(nil)` before each resolution. Test: write hosts.yml under a temp `GH_CONFIG_DIR`, resolve, rewrite it, and expect the new token.
**Warning signs:** repeated 401s while `gh auth status` is fine.

### Pitfall 2: Inherited `GH_TOKEN` wins forever
**What goes wrong:** the daemon (or `dev watch`) started from a pane with a stale `GH_TOKEN` keeps using it, because env beats config.
**How to avoid:** log the token *source* (`GH_TOKEN`/`oauth_token`/`gh`) on resolution and on 401. Expose it in status. Document it. Do not silently ignore env, since CLAUDE.md wants env supported.

### Pitfall 3: Keyring fallback can hang
`tokenFromGh` uses `exec.Command(...).Output()` with no context (source-verified). On macOS a locked keychain can block. Use your own `exec.CommandContext` with a timeout (Pattern 3). This user's gh stores tokens in the keyring (`gh auth status` shows "(keyring)"), so this path is the live one on the dev machine.

### Pitfall 4: CR-01 watermark ties
GitHub timestamps have one-second resolution. Skip an unseen item only if `createdAt < watermark` (strictly older), never `<=`, or a second comment from the same second that shows up a poll late (eventual consistency) is lost. PENDING reviews have a null `submittedAt`. Exclude them from the review watermark. Parse with `time.Parse(time.RFC3339, …)` rather than comparing strings.

### Pitfall 5: Gap handling must happen before commit
See Pattern 8. With watermarks, committing a gapped Step permanently hides the missed items.

### Pitfall 6: Write amplification versus restart burst
Writing the full state on every quiet poll is 100 KB+ of `F_FULLFSYNC` per minute. Skipping all writes on quiet polls means `LastPollAt` is lost on crash, so every PR is due at restart, which is only a burst of cost-1 batches. **Recommendation:** persist when Memory, subs, pending, anchors or errors change. Compare `reflect.DeepEqual(old.Memory, new.Memory)` or have `Step` callers track a dirty flag. Otherwise update `LastPollAt` only in memory. On startup, spread overdue PRs with jitter across the first interval.

### Pitfall 7: Schema version and corrupt-file handling
D-20: an unparseable file is renamed to `state.json.corrupt-<RFC3339-basic>` and startup continues empty, with a `CorruptAt` flag for status. **But** a file whose `version` is greater than the binary knows about (a downgrade) is *not* corrupt. Refuse to start with a clear error rather than quarantine it, or a downgrade would wipe a newer daemon's subscriptions. Also remove leftover `state.json.tmp-*` files at `Open` (a SIGKILL between CreateTemp and rename leaves them). Keep a `migrate(version int, raw json.RawMessage)` hook, a no-op for v1.

### Pitfall 8: Stale in-flight results after unsubscribe/resubscribe
If the last subscriber leaves while a batch is in flight and someone resubscribes before it returns, the old result could be applied to the fresh (unbaselined) record and fire events for history. Bump `PRRecord.Gen` on creation, capture it at dispatch, and drop results whose Gen differs.

### Pitfall 9: go-gh forces the go directive to 1.26.7
go-gh v2.17.0's go.mod declares `go 1.26.7`, and `go mod tidy` raises the project's `go 1.26` to match. The CI "go.mod" matrix leg then installs 1.26.7. That is expected. Do not fight it.

### Pitfall 10: Secondary-limit detection is not just the status code
GitHub documents GraphQL secondary-limit responses as **200 or 403** with an error message, primary exhaustion as **200** with `x-ratelimit-remaining: 0`, and timeouts as 502/504, which deduct extra points [CITED: docs.github.com GraphQL rate limits]. 429 is used for secondary limits on REST [ASSUMED for GraphQL]. Classify by `Retry-After` present, or a message containing "secondary rate limit", on 200/403/429 → secondary. `x-ratelimit-remaining == 0` or error type `RATE_LIMITED` → primary (wait for reset). Any other 403, for example a SAML SSO or permission error, is a per-batch auth problem, not a backoff.

### Pitfall 11: Laptop sleep and monotonic timers
`time.Timer` runs on the monotonic clock, which may not advance during system sleep, so a 16-minute timer can fire long after wall time says it is due [ASSUMED; platform-dependent]. Mitigate by capping each sleep at 30 s and comparing against wall-clock due times (Pattern 5).

### Pitfall 12: WR-05 key change ripples through goldens
If the check Key gains the workflow-run event (`Workflow/Name@pull_request`), the `FailedReported` keys, settle fingerprints and `SettledResult` strings change, and several scenario goldens must be regenerated. Rendering uses Name/Workflow, so message text should not change. Verify `lines.golden` is unchanged as a guard. Recommended: add `workflowRun { event }` to the query and always suffix the event for Actions check runs, which is stable across polls, rather than suffixing only on collision, which is unstable.

### Pitfall 13: Closed vs merged polling (discretion)
MERGED is terminal: after committing the `closed` event, set `Terminal=true` and stop polling. A CLOSED PR can be reopened. Keep it at the 16 min rate (no reset) until Phase 4 LIFE-01 cleanup removes the subscriptions. A reopen produces activity and therefore resets the rate. WR-02's epoch bump on reopen keeps the next `closed` key unique.

## Code Examples

### ghclient request (own client)
```go
// Source: CLAUDE.md guidance; GitHub GraphQL docs (POST /graphql, bearer token)
req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/graphql", bytes.NewReader(body))
req.Header.Set("Authorization", "bearer "+tok)
req.Header.Set("Content-Type", "application/json")
req.Header.Set("User-Agent", "pr-relay/"+version)
resp, err := c.http.Do(req) // http.Client{Timeout: 30 * time.Second}
// read with io.LimitReader(resp.Body, 16<<20); always drain+close
// capture resp.StatusCode, Retry-After, X-RateLimit-Remaining/Reset, X-GitHub-Request-Id
```
The base URL must be injectable (`Client{Endpoint string}`) so tests point it at httptest or the RoundTripper fake.

### synctest cadence test with an in-memory transport
```go
// Source: go doc testing/synctest (Go 1.27.3)
func TestQuietRamp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeGitHub()               // implements http.RoundTripper; no sockets
		eng := newTestEngine(t, fake)         // store in t.TempDir()
		eng.Subscribe(subReq("o/r#1"))
		go eng.Run(t.Context())
		time.Sleep(4*time.Hour + time.Minute) // instant; fake clock
		synctest.Wait()
		got := fake.PollTimes("o/r#1")
		// assert gaps follow the step table, ending at 16m; ~57 polls in the first 4h
	})
}
```
File I/O inside the bubble (the store in `t.TempDir()`) is fine. It is a syscall that completes and does not need to be durably blocking.

### CR-02 fold (sketch)
```go
import "golang.org/x/text/unicode/norm"
func fold(s string) string {
	s = norm.NFKC.String(s)                  // ［ ］ -> [ ], fullwidth letters -> ASCII
	return strings.Map(skeleton, s)          // р->p е->e а->a у->y ... ; bracket lookalikes -> '('/')'
}
// NeutralizeTag and render.fits() match on fold(lower(s)); add the cases to testutil.HostileCorpus,
// plus the IN-01 invisible fillers (U+3164, U+115F, U+1160, U+FFA0, U+2800) to the strip set.
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Injectable Clock interfaces for timing tests | `testing/synctest` (stable `synctest.Test` since Go 1.25) | Go 1.25 | No clock plumbing in production code. Needs fake transports, not sockets. |
| `TokenForHost` reads config once | `config.Reload` exists in go-gh v2.17.0 | v2.17.0 era | Must be called for 401 recovery. |
| LIFE-05 "about 1 h" ceiling | 16 min ceiling (D-11) | 2026-11-01 discuss | Tests assert 16 m. ROADMAP SC-4 text is stale (deferred doc fix). |
| REST ETag probes (S4 suggestion) | GraphQL-only v1 (D-02) | Phase 2 discuss | No ETag store needed. |

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | GitHub accepts a GraphQL document with one operation plus a `fragment`, both via `gh api graphql -F query=@file` and via raw POST | Pattern 7 | capture.sh breaks. Fallback: the batch builder inlines the selection, as `s4-graphql-cost.sh` does. Verify with one live call in the first ghquery task. |
| A2 | In a batch, a missing PR yields `data.prN = {pullRequest:null}` plus an error with `path:["prN","pullRequest"]`, and the other aliases still return data | Pattern 7, WR-04 | Batch decode must handle `null` aliases. Verify with a live call that includes a non-existent PR number. |
| A3 | GraphQL secondary limits may also return 429 | Pitfall 10 | Harmless: 429 is treated as secondary anyway. |
| A4 | Monotonic timers lag after system sleep | Pitfall 11 | The 30 s sleep cap costs nothing even if this is wrong. |
| A5 | Actions re-runs keep the same `workflowRun.event`, so a `@event` key suffix is stable across reruns while separating parallel triggers | Pitfall 12, WR-05 | A wrong key could re-fire `ci.failed` on rerun. Cover with a synthetic scenario and verify against the fixture repo (rerun plus `push` and `pull_request` triggers). |
| A6 | D-12 "events that would be delivered" means PR-level: any event surviving `ExcludeSelf`, independent of subscriber masks | Architecture diagram | If the user meant mask-dependent, a PR whose subscribers only mask `ci.*` would still reset on comments. Low impact. Confirm in plan review. |
| A7 | Budget formulas (capStretch, lowStretch = threshold/remaining, maxStretchedInterval 60 m, low threshold 20%) | Pattern 6 | These are design choices within D-05/D-06. Tunable through config. |
| A8 | All five Go modules are legitimate (slopcheck unavailable) | Package audit | Very low: CLAUDE.md stack, official orgs, proxy-verified. |

## Open Questions

1. **Does Phase 2 Subscribe block on the first fetch?**
   - Known: SUB-07 (Phase 4) wants fail-fast validation. ARCHITECTURE suggests a 3-5 s bounded baseline.
   - Recommendation: `Subscribe` returns immediately after persisting. Expose `PollNow(ctx, ref)` returning the per-PR result/error, so Phase 4 can do "subscribe, then await the first poll with a timeout". On a NOT_FOUND first poll, the engine records `LastError` and keeps the record. Phase 4 decides whether to remove it.
2. **Pruning `SeenComments`/`SeenReviews`.** They grow without bound. With watermarks, IDs older than the watermark that have also left the window could be pruned, but only if per-ID createdAt is kept. Recommendation: defer, and measure the state size in the crash or dev runs. Tens of PRs × hundreds of IDs is far under 1 MB.
3. **Ack semantics for Phase 3.** `Ack(subID, upToSeq)` is recommended (it removes pending entries with seq ≤ N). Phase 3 must persist "attempting" if it wants a narrower duplicate window. That is not needed in Phase 2.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | build/test | ✓ | go1.27.3 darwin/arm64 | — |
| gh CLI (authenticated) | DMN-03 live check, `dev watch` against real PRs | ✓ | 2.103.3. Two github.com accounts, keyring storage, active `drodimin` | `GH_TOKEN` env |
| Network to api.github.com | live dev watch only | assumed ✓ | — | All automated tests use fakes |
| slopcheck | package audit | ✗ (install denied) | — | Packages marked [ASSUMED] |
| GitHub Actions macOS + Linux | CI | ✓ (existing ci.yml matrix with `-race`) | — | — |

**Missing dependencies with no fallback:** none.

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | stdlib `testing` (+ `testing/synctest`, `net/http/httptest`), golden files via `internal/testutil` |
| Config file | none (CI: `.github/workflows/ci.yml` runs `go test -race -count=1 ./...` on ubuntu and macos) |
| Quick run command | `go test -count=1 ./internal/...` |
| Full suite command | `go test -race -count=1 ./...` (crash loop included; `-short` reduces iterations) |

### Phase Requirements → Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| EVT-09 / SC-1 | SIGKILL at random points, then restart: every expected event key appears exactly once in sink ∪ pending; no key with two seqs | subprocess e2e | `go test -race -count=1 -run TestCrashLoop ./cmd/pr-relay/` | ❌ Wave 0 |
| EVT-09 | Save failure (read-only dir or injected writer error) leaves the in-memory state un-advanced, and the next poll re-derives the same events | unit | `go test -run TestCommitFailureDoesNotAdvance ./internal/poller/` | ❌ |
| EVT-09 (CR-01) | Delete a comment on a 31+ comment PR: no resurrected event; same-second tie still fires | scenario golden + unit | `go test -run 'TestScenarios\|TestWatermark' ./internal/diff/` | ❌ (new scenario dir) |
| EVT-09 (WR-02) | Close, reopen, close gives distinct keys; head return gives distinct commits/ci keys | unit | `go test -run TestEpochKeys ./internal/diff/` | ❌ |
| LIFE-03 | Round-trip: subscribe, add pending, close the store, reopen: identical state; tmp leftovers removed; corrupt file quarantined (D-20); newer version refused | unit | `go test -run 'TestStore' ./internal/store/` | ❌ |
| LIFE-03 | `state-v1.json` golden loads (schema stability) | golden | `go test -run TestStateV1Golden ./internal/store/` | ❌ |
| SUB-08 | First sub: the first poll emits zero events even when the PR has history; changes afterwards do emit | engine + fake transport | `go test -run TestBaselineSilent ./internal/poller/` | ❌ |
| SUB-04 | Re-sub of the same (socket, pane, PR) keeps one sub, replaces the mask, re-filters pending, keeps the baseline | unit | `go test -run TestResubscribeUpsert ./internal/poller/` | ❌ |
| DMN-02 | N subs on one PR give exactly one alias per batch per interval (fake transport counts aliases) | synctest | `go test -run TestOnePollPerPR ./internal/poller/` | ❌ |
| DMN-03 | Token from GH_TOKEN; from a temp GH_CONFIG_DIR hosts.yml; after a 401 the rewritten hosts.yml is used (Reload); the gh fallback via a fake `GH_PATH` script; the timeout; no-token gives slow retry and AuthState=NoToken | unit/httptest | `go test -run 'TestToken\|TestUnauthorizedRefresh' ./internal/ghclient/` | ❌ |
| DMN-04 | Batch builder: ≤ batchSize aliases, uses variables; DecodeBatch: per-alias NOT_FOUND, nested FORBIDDEN warning (WR-04), RATE_LIMITED batch error | unit + fixtures | `go test -run 'TestBuildBatch\|TestDecodeBatch' ./internal/ghquery/` | ❌ |
| DMN-04 | Cost is logged per batch (slog handler capture) | unit | `go test -run TestCostLogged ./internal/poller/` | ❌ |
| DMN-04 / D-07 | 403 + Retry-After waits exactly that long; missing header 60 s; doubling to a 15 m cap; batch size halves; restores after clean batches | synctest | `go test -run TestSecondaryBackoff ./internal/poller/` | ❌ |
| DMN-04 / D-05/D-06 | 60 active PRs: projected spend over the cap stretches intervals so ledger ≤ about 500/h; remaining < threshold stretches proportionally but never stops; remaining=0 waits for resetAt | synctest + pure | `go test -run 'TestBudget' ./internal/poller/` | ❌ |
| LIFE-05 | Ramp follows the table to 16 m at about 4 h; activity snaps back to 60 s; own comment does not reset, own push does (D-12); second sub does not reset (D-13); ResetRate does (D-15); keep does not (D-14) | synctest + pure | `go test -run 'TestCadence\|TestQuietRamp\|TestActivityReset' ./internal/poller/` | ❌ |
| D-04 | WR-01 (30+ commits not force-push), WR-03 (reopen actor), WR-05 (parallel trigger), WR-06 (review comment gap), WR-07 (`..` rejected), CR-02 (fullwidth/Cyrillic tag neutralized) | unit + scenario + hostile corpus | `go test ./internal/diff/ ./internal/model/ ./internal/ghquery/ ./internal/sanitize/ ./internal/render/` | partial (extend existing) |
| D-21 | `dev watch` against the fake prints rendered lines plus a cost line | e2e (part of the crash test helper) | `go test -run TestDevWatchOutput ./cmd/pr-relay/` | ❌ |

### Crash-loop design (D-22, SC-1)
- `TestMain` in `cmd/pr-relay` runs `go build -o $TMP/pr-relay .` once, so the test exercises the real binary.
- **Fake GitHub** (`httptest.Server`, real time, outside synctest) holds one or two PRs. Its state advances on its own clock, independent of daemon restarts: every K ms it appends a new issue comment `c<i>` (and optionally a review `r<i>`) until N items exist, then holds still. Comments and reviews each give exactly one event however the daemon samples them (pushes may be skipped by sampling, so avoid them as assertions). Keep the items per poll under the 30-item window, or rely on the gap re-fetch to test it.
- **Binary config for the test:** `PR_RELAY_STATE_DIR=$TMP/state`, `PR_RELAY_GITHUB_GRAPHQL_URL=<fake>`, `GH_TOKEN=test`, and a config with `min_interval=100ms`, `max_interval=200ms`. Hide the endpoint override behind the dev command or env, and document it as test-only.
- **Idempotent sink:** `dev watch --sink $TMP/sink.jsonl` drains pending, appends `{"seq":S,"key":K}` lines (fsync), then `Ack(upToSeq)`. On start it reads the max seq in the sink and acks up to it before printing anything new. A kill between the sink write and the Ack therefore never re-emits.
- **Loop:** for i in 1..50 (10 with `-short`): start the binary, sleep rand(0..400ms), SIGKILL, and assert the state file (if present) parses. After the fake stops changing, run the binary once more until quiet, then SIGTERM.
- **Assertions:** (a) every expected key `comment:c1..cN` (with the epoch prefix per WR-02) appears in the sink exactly once; (b) no key appears with two different seqs (that would mean the event was created twice); (c) the final state has no pending entries left and no leftover `state.json.tmp-*`; (d) the sink has no seq gaps among delivered keys. Under SIGKILL, Go has no deferred cleanup, which is the point.

### Sampling Rate
- **Per task commit:** `go test -count=1 ./internal/...`
- **Per wave merge:** `go test -race -count=1 ./...`
- **Phase gate:** full suite green on macOS and Linux CI, plus a manual `pr-relay dev watch <fixture PR>` session showing cost logs and the ramp, before `/gsd:verify-work`.

### Wave 0 Gaps
- [ ] `internal/poller/fakegh_test.go`: in-memory `http.RoundTripper` fake GitHub with scripted PR states, alias counting and injectable status/headers
- [ ] `internal/ghquery/testdata/batch-*.json`: batch fixtures (mixed NOT_FOUND alias, nested FORBIDDEN partial, RATE_LIMITED, viewer). Capture one live batch through an extended `scripts/capture.sh`.
- [ ] `internal/store/testdata/state-v1.json`: schema golden
- [ ] `cmd/pr-relay/crash_test.go` + `TestMain` binary build + httptest fake
- [ ] New diff scenarios: `delete-comment-31`, `reopen-close`, `big-push-30`, `parallel-trigger`, `review-comments-31`
- [ ] Framework install: none (stdlib)

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | yes (consumes a credential) | go-gh resolution. The token is held only in memory, never logged or persisted, and sent only to `api.github.com` over TLS |
| V3 Session Management | no | — |
| V4 Access Control | yes (local files) | State dir 0700, state file 0600, lock file 0600 |
| V5 Input Validation | yes | `PRRef.Valid` + WR-07; GraphQL variables (no string interpolation); `io.LimitReader` on responses; decode errors are non-fatal per alias |
| V6 Cryptography | no (TLS via stdlib only) | — |
| V7 Logging | yes | slog never logs the token, headers or bodies. Log the request ID, cost and status only |

### Known Threat Patterns

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Path traversal via PRRef used as a key or filename | Tampering | WR-07 reject `.`/`..`. PRRef is used only as a JSON map key, never as a filename |
| Token leakage in logs or state | Information disclosure | Never persist the token. Redact it in errors (wrap `http` errors without the request headers) |
| Lookalike `[pr-relay]` tag from check names, workflows or logins | Spoofing | CR-02 NFKC + skeleton fold + filler strip, before Phase 3 |
| Oversized or malicious GraphQL response | DoS | Response size limit (16 MB), 30 s timeout, per-alias decode isolation |
| Another local process editing state | Tampering | 0600/0700 permissions + flock. Same-user processes are inside the trust boundary |
| Integration ban from ignoring rate limits | DoS (self) | D-07 backoff, concurrency 1, honour Retry-After exactly (GitHub warns of bans) |

## Sources

### Primary (HIGH confidence)
- go-gh v2.17.0 module zip from proxy.golang.org: `pkg/auth/auth.go` (`TokenForHost`, `TokenFromEnvOrConfig`, `tokenFromGh` with `exec.Command`, no timeout) and `pkg/config/config.go` (`Read` is sync.Once, `Reload`, GH_CONFIG_DIR precedence). go.mod has `go 1.26.7` and `golang.org/x/text v0.43.0`.
- proxy.golang.org `@latest`: go-gh v2.17.0, cobra v1.10.2, BurntSushi/toml v1.6.0, gofrs/flock v0.13.1, x/text v0.43.0
- Go 1.27.3 source `internal/poll/fd_fsync_darwin.go`: `Sync` uses F_FULLFSYNC and falls back to fsync on ENOTSUP
- `go doc testing/synctest` (Go 1.27.3): `Test`, `Wait`, the durable-blocking rules (network I/O and mutexes are not durably blocking)
- https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api: 5000 pts/h, cost formula, secondary limits (100 concurrent, 2000 pts/min GraphQL, CPU 60 s/60 s), 200/403 secondary responses, retry-after, wait ≥1 min, ban warning, 10 s timeout with 502/504 and an extra point deduction
- Repo code: `internal/diff/*.go`, `internal/model/model.go`, `internal/ghquery/*`, `internal/filter/filter.go`, `cmd/pr-relay/main.go`, `.github/workflows/ci.yml`, `go.mod`
- Phase 1 artifacts: 01-REVIEW.md (CR-01..WR-07, IN-01..07), 01-VERIFICATION.md carry-forward, 01-SPIKES.md S0/S3/S4

### Secondary (MEDIUM confidence)
- `.planning/research/ARCHITECTURE.md`, `PITFALLS.md` (outbox, scheduler, laptop sleep, secondary limits). These are project research from training knowledge, cross-checked above where possible.

### Tertiary (LOW confidence)
- Batch alias error shape (A2), fragment document acceptance (A1), Actions rerun event stability (A5). Each needs one live verification.

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH. Versions come from the proxy, and the go-gh behaviour comes from source.
- Architecture (outbox, copy-on-write, engine shape): HIGH. Follows directly from D-01/D-03 and the pure-core API.
- Cadence and budget formulas: MEDIUM. These are design recommendations within locked bounds.
- Pitfalls: HIGH for 1, 3, 4, 5, 7, 8, 9 (source or logic-verified). MEDIUM for 10 and 11.

**Research date:** 2026-11-01
**Valid until:** 2026-12-01 (stable domain; recheck the go-gh release if the version is bumped)
