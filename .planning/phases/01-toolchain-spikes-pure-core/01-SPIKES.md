# Phase 1 Spike Report (S0-S7)

Plan 01-08. Every herdr probe ran only in the scratch session `prspike`
(socket `~/.config/herdr/sessions/prspike/herdr.sock`), through the guarded
`hr` wrapper in `spikes/lib.sh`, with `# `-prefixed payloads (D-16). Raw logs
live in `spikes/out/` (gitignored). The excerpts below are redacted (`~` for
the home directory, no tokens) and abridged (`...`).

Environment: herdr 0.9.5 (protocol 22), gh 2.103.3, Go 1.27.3, macOS (Darwin 25.3).
Interactive spikes ran on 2026-11-01. S4 ran on 2026-10-31.

**How the runs were driven.** The human ran S0 and S1 through
`spikes/run-interactive.sh`. After S1, at the user's request, the orchestrator
drove S7, S2a, S2b, S6, S5 and S3 itself with `spikes/auto-lib.sh`. That script
uses the same `hr` guard and the same prspike-only socket. It typed with
`herdr pane send-text`, approved or denied with `herdr pane send-keys`
(`enter` / `esc`) and observed the panes with `herdr pane read`. Those answers
are logged as `ANSWER(auto)` with the evidence that was observed. The human
re-attached prspike for S3. Before S2, pane A's Claude Code was switched from
auto mode to manual mode (Shift+Tab) so that permission prompts would appear.
Pane A (`w1:p1`) ran Claude Code in an empty temp dir. Pane B (`w1:p2`) was a
bare zsh.

## S0: Scratch session and listing

**Question:** Can the spikes reach the prspike session, and only that session, through the guarded socket?

**Method:** `bash spikes/run-interactive.sh` (step S0): `herdr session list`, `herdr agent list` and `herdr agent get <pane>` via `hr`, with `HERDR_SOCKET_PATH=~/.config/herdr/sessions/prspike/herdr.sock`. The human confirms the listing shows only prspike panes.

**Answer:** YES (only the prspike agent `w1:p1` was listed; the human confirmed no default-session agents appeared)

**Evidence:** (spikes/out/s0.log)

```
socket=~/.config/herdr/sessions/prspike/herdr.sock herdr=herdr 0.9.5
CALL session list -> RC=0 latency_ms=14
  default  running  ~/.config/herdr  ~/.config/herdr/herdr.sock
  prspike  running  ~/.config/herdr/sessions/prspike  ~/.config/herdr/sessions/prspike/herdr.sock
CALL agent list -> RC=0 latency_ms=15
  {"agents":[{"agent":"claude","agent_session":{"kind":"id","source":"herdr:claude","value":"91726c7d-..."},
   "agent_status":"idle","focused":true,"pane_id":"w1:p1","tab_id":"w1:t1","terminal_id":"term_65f3de20fb4001",...}]}
ANSWER q="Does the agent list above show ONLY your prspike panes?" a=y
CALL agent get B: herdr agent get w1:p2 -> RC=1
  {"error":{"code":"agent_not_found","message":"agent target w1:p2 not found"}}
```

**Implication:** The socket path alone isolates sessions. Pane ids such as `w1:p1` exist in both sessions, so Phase 3 must store the herdr socket path with every subscription (captured from `HERDR_SOCKET_PATH` at `sub` time) and never address a pane by id alone. `herdr agent list` / `agent get` include only panes with a detected agent. A bare shell is `agent_not_found`, so any pane-existence check (for example the LIFE guard, or the spike guard) must use `pane list` (see the Task 2 guard fix).

## S1: Typing collision

**Question:** Does agent.prompt clobber or merge with a half-typed draft?

**Method:** The human types `hello` (no Enter) in the agent pane, then in the bare shell pane. The script sends `herdr agent prompt <pane> "# pr-relay spike: S1 draft collision on <pane>"` and the human records c/s/r/x.

**Answer:** YES (it merges: the draft and the prompt were concatenated and submitted as one message, `hello# pr-relay spike: S1 ...`)

**Answer:** NO (a bare shell pane is not touched: `agent_not_found`, nothing is typed)

**Evidence:** (spikes/out/s1.log, spikes/out/s7.log observation)

```
CALL prompt over draft (w1:p1): herdr agent prompt w1:p1 '# pr-relay spike: S1 draft collision on w1:p1'
RC=0 latency_ms=344  type=agent_prompted agent_status=idle
ANSWER q="Pane w1:p1: what happened? c/s/r/x" a=c
CALL prompt over draft (w1:p2): herdr agent prompt w1:p2 '# pr-relay spike: S1 draft collision on w1:p2'
RC=1 latency_ms=23  {"error":{"code":"agent_not_found","message":"agent target w1:p2 not found"}}
ANSWER q="Pane w1:p2: what happened? c/s/r/x" a=x
OBS w1:p1 (agent's own reading): S1 came with "hello" in front of it
  ("hello# pr-relay spike: S1 draft collision on w1:p1")
```

**Implication:** herdr does not protect a user draft. `agent.prompt` types its text after whatever is in the input box and presses Enter, which submits the user's half-typed draft early, with the update glued onto it. Phase 3 must hold delivery while the user may be typing: do not deliver while the pane is focused (`focused=true`), and require a quiet period after the last state change. Prefix each message with a newline-free, recognisable marker (for example `[pr-relay]`) so that a merged message is still readable to the agent. herdr exposes no "draft present" signal, so the focus/quiet heuristic is the only guard available. Record this as a known limitation in the skill doc.

## S2: agent.wait while blocked, and the prompt wait option

**Question:** (S2a) Does `agent wait --until idle --until done` span a blocked (permission) state and return only after the turn completes? Does a default `agent wait` return immediately with blocked? (S2b) Does `agent prompt --wait` return `agent_blocked` for a blocked agent and `agent_prompt_stalled` for a bare shell?

**Method:** S2a: the agent is in a permission prompt (`Please run the shell command: ls /tmp`); background `herdr agent wait w1:p1 --until idle --until done --timeout 60000`; approve after about 10 s (`pane send-keys enter`); record the return time. Then trigger a second prompt (`ls /var`) and run `herdr agent wait w1:p1 --timeout 5000` while blocked. S2b: `herdr agent prompt <p> "# pr-relay spike: S2b ..." --wait --until working --timeout 15000` on the blocked agent, then (after denying with `esc`) on the idle agent, then on the bare shell.

**Answer:** YES (S2a: the `--until idle --until done` wait spans the blocked state and returned only after approval and turn completion, with status done)

**Answer:** YES (S2a: a default `agent wait` while blocked returns immediately with `agent_status: blocked`)

**Answer:** YES (S2b: `agent prompt --wait` on a blocked agent returns `agent_blocked` at once and types nothing into the permission dialog)

**Answer:** NO (S2b: a bare shell returns `agent_not_found`, not `agent_prompt_stalled`; the stalled path was not exercised)

**Evidence:** (spikes/out/s2a.log, spikes/out/s2a-bgwait.out, spikes/out/s2b.log)

```
S2a  CALL status while blocked: agent get w1:p1 -> RC=0 agent_status=blocked
S2a  started background: agent wait w1:p1 --until idle --until done --timeout 60000 at t0
S2a  auto-driver approved (send-keys enter on "1. Yes") at t0+10.0s
S2a  BGWAIT RC=0 elapsed_ms=14254 {"id":"cli:agent:wait",... "agent_status":"done","completion_seq":10}
S2a  CALL wait while blocked (default until, 5s): agent wait w1:p1 --timeout 5000
S2a  RC=0 latency_ms=17 agent_status=blocked
S2b  prompt --wait on BLOCKED: RC=1 latency_ms=13
       {"error":{"code":"agent_blocked","message":"agent w1:p1 is blocked and requires interactive input"}}
S2b  (permission denied with esc) prompt --wait on IDLE: RC=0 latency_ms=522 agent_status=working
S2b  prompt --wait on bare SHELL: RC=1 latency_ms=13 {"error":{"code":"agent_not_found",...}}
S2b  OBS w1:p1: "The S2b idle marker arrived cleanly as its own message"
```

**Implication:** Phase 3 can use `agent.wait` with explicit `until: [idle, done]` as a long-poll for "the agent is free again", instead of polling `agent.get`. It spans blocked states correctly. Never call `agent.wait` without `until`, because it returns immediately on blocked and would turn into a busy loop. `agent.prompt` refuses blocked agents (`agent_blocked`) without disturbing the permission dialog, so the delivery loop can treat `agent_blocked` as "queue and retry later" rather than as a failure. `prompt --wait --until working` confirms submission in about 0.5 s on an idle agent, and is the cheap delivery acknowledgement. `agent_not_found` means "this pane has no agent now" (bare shell, agent exited) and must be handled distinctly from `agent_blocked`. `agent_prompt_stalled` (the 5000 ms herdr timeout) is still unobserved, so treat it as a retryable error until it is seen.

## S3: Identity across session restart

**Question:** Is pane_id stable across restart? Is terminal_id stable? Is agent_session stable?

**Method:** `herdr agent list` and `herdr pane list` before and after `herdr session stop prspike` (one stop shared with S5, run by the auto-driver against prspike only) and the human's `herdr session attach prspike`. Claude Code in pane A came back on re-attach.

**Answer:** YES (pane_id is stable: `w1:p1` and `w1:p2` before and after; tab_id is also stable)

**Answer:** NO (terminal_id changes for every pane on restart)

**Answer:** YES (agent_session, the Claude session id, is stable)

**Evidence:** (spikes/out/s3.log)

```
BEFORE  A: pane_id=w1:p1 tab=w1:t1 terminal_id=term_65f3de20fb4001 agent_session=91726c7d-...
        B: pane_id=w1:p2 tab=w1:t2 terminal_id=term_65f3de80a5aef2 (no agent)
CALL session stop prspike rc=0 out="stopped session prspike"
human re-attached prspike (herdr session attach prspike)
AFTER   A: pane_id=w1:p1 tab=w1:t1 terminal_id=term_65f3e3db183661 agent_session=91726c7d-...
        B: pane_id=w1:p2 tab=w1:t2 terminal_id=term_65f3e3db193192 (no agent)
RESULT  pane_id+tab_id+agent_session stable; terminal_id changes.
```

**Implication:** pane_id alone cannot tell "the same agent" from "a new agent in a restored pane", because it survives restart and is reused across sessions (S0). terminal_id changes on every restart, so it is too strict for a guard that should survive a herdr restart. The LIFE-02 identity guard should key on (socket path, pane_id, agent_session): deliver only if `agent_session` matches the one captured at `sub` time. If the agent has no `agent_session` (a non-Claude agent that herdr cannot identify), fall back to terminal_id and accept that a herdr restart drops the subscription to "needs re-confirm". Store terminal_id as a secondary signal, not the primary key.

## S4: GraphQL query shape and cost

**Question:** Does the PRState query (`internal/ghquery/pr.graphql`) return valid data, and what does it cost per PR and when batched (aliased) to 5 and 20 PRs?

**Method:** `spikes/s4-graphql-cost.sh` (no herdr). Runs `gh api graphql -F query=@internal/ghquery/pr.graphql -F owner=cli -F repo=cli -F number=1`, once with `rateLimit(dryRun: true)` substituted into a temp copy and once for real. Then builds aliased batch queries `pr0..prN-1: repository(owner:"cli", name:"cli") { pullRequest(number: N) { <same selection> } }` for the 5 and 20 most recent cli/cli PRs and runs each with dryRun and for real. Latency is wall clock via perl Time::HiRes. Run on 2026-10-31.

**Answer:** YES (the query is valid, returns the full shape with no errors, and costs about 0.25 points per PR)

**Evidence:** (spikes/out/s4.log, abridged)

```
label=1pr-dryrun  rc=0 latency_ms=302  bytes=121    rateLimit={"cost":1,"nodeCount":790}   errors=[]
label=1pr-real    rc=0 latency_ms=615  bytes=1904   rateLimit={"cost":1,"nodeCount":790}   errors=[]
label=5pr-dryrun  rc=0 latency_ms=425  bytes=122    rateLimit={"cost":1,"nodeCount":3950}  errors=[]
label=5pr-real    rc=0 latency_ms=1400 bytes=62709  rateLimit={"cost":1,"nodeCount":3950}  errors=[]
label=20pr-dryrun rc=0 latency_ms=354  bytes=123    rateLimit={"cost":5,"nodeCount":15800} errors=[]
label=20pr-real   rc=0 latency_ms=3076 bytes=209245 rateLimit={"cost":5,"nodeCount":15800} errors=[]
```

| PRs per query | cost (points) | nodeCount | latency (real) | response size |
|---|---|---|---|---|
| 1 | 1 | 790 | 615 ms | 1.9 KB (old PR, no checks) |
| 5 | 1 | 3950 | 1400 ms | 63 KB |
| 20 | 5 | 15800 | 3076 ms | 209 KB |

- dryRun cost and nodeCount equal the real run, so the poller can price a batch before sending it.
- nodeCount is 790 per PR and scales linearly. `reviews(last: 20) { comments(first: 30) }` accounts for 600 of the 790 (the nested connection is 20 x 30). This is far below the 500,000 node limit.
- Cost works out to roughly 1 point per 4-5 PRs (minimum 1 per query). Batching does not lower points per PR beyond that floor, but it cuts request count, which matters for the secondary rate limit.
- All 20 batch PRs had `contexts.pageInfo.hasNextPage = false` (largest had 49 check contexts), so `contexts(first: 100)` is enough for typical repos.
- Latency and response size grow roughly linearly with batch size (about 150 ms and 10 KB per active PR).

**Implication:** Phase 2 should batch active PRs in aliased GraphQL queries of up to 20 PRs (cost 5, about 3 s, about 200 KB). Batches of 4-5 are the cheapest unit (cost 1). At 20 subscribed PRs polled every 60 s the poller spends about 300 points/hour, 6% of the 5000/hour budget. A 30 s interval spends about 600/hour (12%), which leaves room for the adaptive backoff to tighten intervals on PRs with activity. Read `rateLimit { cost remaining resetAt }` on every response and price new batches with `dryRun` if the selection changes. If cost becomes a problem, shrink `reviews(last: 20) { comments(first: 30) }` first, since it dominates nodeCount. Use REST conditional requests (ETag/304) for cheap "did anything change" checks on idle PRs, and GraphQL only to refresh PRs that changed.

## S5: setsid survival

**Question:** Does a setsid child survive herdr session stop?

**Method:** In prspike pane `w1:p2`: `go build -o spikes/setsid-probe/setsid-probe ./spikes/setsid-probe && spikes/setsid-probe/setsid-probe` (started by the auto-driver via `herdr pane run`). The child (re-exec with `SysProcAttr{Setsid: true}`) appends `heartbeat <ts> pid= ppid= sid=` to `spikes/out/setsid-heartbeat.txt` every second. Then `herdr session stop prspike` (the single stop shared with S3), a 20 s wait, and a check that heartbeats keep advancing. The child was killed afterwards (`pkill`, 0 remaining).

**Answer:** YES (the setsid child kept running after the session stopped: heartbeat 21 s after the stop, ppid=1, its own session)

**Evidence:** (spikes/out/s5.log)

```
OBS w1:p2: child pid=73204 (parent pid=73203 sid=68292)
BEFORE  heartbeat 1793570475 pid=73204 ppid=1 sid=73204
STOPPED-AT 1793570490 (herdr session stop prspike rc=0)
AFTER   heartbeat 1793570511 pid=73204 ppid=1 sid=73204
PS      73204  1  0  ~/.../spikes/setsid-probe/setsid-probe child
ANSWER(auto) a=y evidence="stop at 1793570490; heartbeat 1793570511 (+21s) ... prspike status=stopped"
cleanup: pkill setsid-probe child; remaining=0
```

**Implication:** Self re-exec with `Setsid: true` gives a daemon that outlives the herdr session (and the pane) that spawned it. The parent's sid 68292 was the pane shell's session, and the child left it. Phase 4/5 can therefore rely on CLI auto-start (re-exec of `pr-relay daemon`, guarded by flock) as a robust path, and the plugin `[[startup]]` entry can launch the daemon the same way without tying its lifetime to herdr. The flip side is that nothing stops the daemon when herdr goes away. Phase 4/5 must decide the policy explicitly: an idle exit when no herdr socket from any subscription answers for N minutes, or staying resident under `brew services` / a systemd user unit.

## S6: Multi-line and payload size

**Question:** Does an embedded \n produce a single submission? What is the largest accepted single payload (bytes)?

**Method:** `herdr agent prompt <pane> <payload>` to the agent (`w1:p1`) and the bare shell (`w1:p2`) with 2-line and 30-line payloads (every line starts with `# `), then single-line payloads of 900 B, 1100 B, 4096 B and 16384 B. The auto-driver read the pane after each to check how many submissions arrived.

**Answer:** YES (an embedded `\n` produces a single submission: the 2-line and 30-line payloads each arrived as one user turn, all lines in order)

**Answer:** UNKNOWN (no upper limit was found: the largest payload tested, **16384 bytes**, was accepted, rc=0 in 339 ms, and submitted as one untruncated message)

**Evidence:** (spikes/out/s6.log)

```
2-line  -> w1:p1 RC=0 latency_ms=317  a=1 "one user turn with line 1 and line 2"
30-line -> w1:p1 RC=0 latency_ms=317  a=1 "lines 1..30 arrived in order as one message"
900B    -> w1:p1 RC=0 latency_ms=317  a=1
1100B   -> w1:p1 RC=0 latency_ms=331  a=1
4096B   -> w1:p1 RC=0 latency_ms=329  a=1
16384B  -> w1:p1 RC=0 latency_ms=339  a=1 "one pasted block, not truncated"
NOTE Claude Code collapses >=900B input into a single [Pasted text] block; leading # not
     shown in transcript render; delivery latency flat ~317-339ms up to 16KB
all six -> w1:p2 RC=1 {"error":{"code":"agent_not_found","message":"agent target w1:p2 not found"}}
```

**Implication:** The Phase 3 line joiner does not need to flatten multi-line messages into one line for correctness. herdr delivers embedded newlines as one submission to Claude Code (likely through bracketed paste). Keep messages short anyway: at 900 B and above Claude Code shows the message as a collapsed `[Pasted text]` block, which hides it from the human watching the pane. Set the flood ceiling at about 800 B per delivered message (below the paste-collapse threshold), and coalesce or summarise above that rather than splitting into several prompts. Each split would be a separate turn and costs tokens. Lines starting with `#` render as markdown headings in Claude Code, so the product message format should not start lines with `#`. The `# ` prefix was a spike-only safety measure. Behaviour in other agents (Codex and others) was not tested and should be checked before relying on multi-line delivery for them. Prompts never reach a bare shell (`agent_not_found`), which is the property the agent-agnostic delivery relies on.

## S7: Prompt after a completed turn, pane unfocused

**Question:** Is a prompt accepted and submitted when the agent has just finished a turn and the pane has not been focused since?

**Method:** After the agent finished its S1 turn: `herdr agent get w1:p1`, then `herdr agent prompt w1:p1 "# pr-relay spike: S7 after done, unfocused"`. The auto-driver read the pane to confirm submission.

**Answer:** YES (accepted, rc=0 in 316 ms, and submitted cleanly as its own message; the agent replied)

**Evidence:** (spikes/out/s7.log)

```
AUTO: pane A focused-in-herdr="focused":true; human terminal focus is elsewhere
CALL status before: agent get w1:p1 -> RC=0 agent_status=idle completion_seq=4
CALL prompt after done: agent prompt w1:p1 '# pr-relay spike: S7 after done, unfocused'
RC=0 latency_ms=316 type=agent_prompted
OBS w1:p1: "S7 arrived clean, after my previous reply ..."
ANSWER(auto) a=y evidence="rc=0 in 316ms; pane shows S7 arrived clean and agent replied"
```

**Caveat:** herdr reported `focused=true` for `w1:p1`, because it was the only pane focused within herdr. "Unfocused" here means the OS terminal focus was elsewhere. herdr reported `idle` (not `done`) right after the completed turn, probably because the pane was focused in herdr. Whether `done` persists on a pane that is not focused in herdr was not isolated.

**Implication:** A finished agent (idle or done) is a valid delivery target. Phase 3 can deliver right after a turn completes and does not need the user to look at the pane first. Combined with S1, the delivery gate is: status in {idle, done}, the pane has not been focused in herdr for the quiet period, and `completion_seq` / `state_change_seq` have not advanced during the quiet window. `focused` in herdr reflects herdr's own pane focus, not OS window focus, so it is only a partial signal for "the user is typing".

## Summary table

| Spike | Question | Answer | Gates (phase / requirement) |
|---|---|---|---|
| S0 | Guarded socket reaches only prspike | YES | Phase 3: store the socket path per subscription; pane existence via `pane list` |
| S1 | agent.prompt merges with a half-typed draft | YES (concatenated and submitted); bare shell `agent_not_found` | Phase 3 / DLV-05, DLV-06: hold delivery while focused or recently active |
| S2a | `wait --until idle --until done` spans blocked | YES (returned after approval, 14.3 s, done) | Phase 3: use agent.wait with explicit `until`, not agent.get polling |
| S2a | Default `agent wait` returns immediately when blocked | YES (17 ms, blocked) | Phase 3: never call wait without `until` |
| S2b | `prompt --wait` on blocked returns `agent_blocked` | YES (nothing typed into the dialog) | Phase 3: `agent_blocked` means queue and retry |
| S2b | Bare shell returns `agent_prompt_stalled` | NO (`agent_not_found`; stalled not observed) | Phase 3: handle `agent_not_found` separately; treat stalled as retryable |
| S3 | pane_id / terminal_id / agent_session stable across restart | YES / NO / YES | LIFE-02 identity guard keyed on socket + pane_id + agent_session |
| S4 | PRState query valid; cost per PR and batched | YES (1 PR = 1, 5 = 1, 20 = 5 points) | Phase 2 / EVT-01: batches of up to 20, about 300 pts/h at 20 PRs / 60 s |
| S5 | setsid child survives herdr session stop | YES (+21 s, ppid=1) | Phase 4/5: self re-exec works; needs an explicit idle-exit policy |
| S6 | Embedded `\n` gives a single submission | YES (2 and 30 lines) | Phase 3 / DLV-05: no forced flattening needed for Claude Code |
| S6 | Largest accepted single payload | UNKNOWN upper bound; 16384 B accepted | Phase 3: flood ceiling about 800 B (paste-collapse at 900 B) |
| S7 | Prompt accepted after a completed turn | YES (316 ms) | Phase 3: idle/done is a valid delivery state |

## Open for Phase 3 planning

- `agent_prompt_stalled` was never observed (a bare shell is `agent_not_found`). Its trigger condition needs confirming against herdr docs or a targeted probe.
- The S6 upper payload limit was not found (16384 B worked). This is not needed if the flood ceiling is about 800 B.
- Whether `done` persists (instead of decaying to `idle`) on a pane that is not focused in herdr was not isolated in S7.
- Multi-line and draft-collision behaviour was tested only with Claude Code. Codex and other agents are untested.
