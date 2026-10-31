# Phase 1 Spike Report (S0-S7)

Plan 01-08. Every herdr probe ran only in the scratch session `prspike`
(socket `~/.config/herdr/sessions/prspike/herdr.sock`), through the guarded
`hr` wrapper in `spikes/lib.sh`, with `# `-prefixed payloads (D-16). Raw logs
live in `spikes/out/` (gitignored). The excerpts below are redacted (`~` for
the home directory, no tokens).

Environment: herdr 0.9.5 (protocol 22), gh 2.103.3, Go 1.27.3, macOS (Darwin 25.3).

## S0: Scratch session and listing

**Question:** Can the spikes reach the prspike session, and only that session, through the guarded socket?

**Method:** `bash spikes/run-interactive.sh` (step S0): `herdr session list`, `herdr agent list` and `herdr agent get <pane>` via `hr`, with `HERDR_SOCKET_PATH=~/.config/herdr/sessions/prspike/herdr.sock`. The human confirms the listing shows only prspike panes.

**Answer:** UNKNOWN (pending)

**Evidence:** (pending, spikes/out/s0.log)

**Implication:** (pending)

## S1: Typing collision

**Question:** Does agent.prompt clobber or merge with a half-typed draft?

**Method:** Human types `hello` (no Enter) in the agent pane, then in the bare shell pane. Script sends `herdr agent prompt <pane> "# pr-relay spike: S1 ..."` and records c/s/r/x.

**Answer:** UNKNOWN (pending)

**Evidence:** (pending, spikes/out/s1.log)

**Implication:** (pending: Phase 3 holds delivery while the user has focus or a draft)

## S2: agent.wait while blocked, and the prompt wait option

**Question:** (S2a) Does `agent wait --until idle --until done` span a blocked (permission) state and return only after the turn completes? Does a default `agent wait` return immediately with blocked? (S2b) Does `agent prompt --wait` return `agent_blocked` for a blocked agent and `agent_prompt_stalled` for a bare shell?

**Method:** S2a: agent in a permission prompt; background `herdr agent wait <agent> --until idle --until done --timeout 60000`; human approves; record return time. Then `herdr agent wait <agent> --timeout 5000` while blocked. S2b: `herdr agent prompt <p> "# pr-relay spike: S2b ..." --wait --until working --timeout 15000` on blocked agent, idle agent and bare shell.

**Answer:** UNKNOWN (pending)

**Evidence:** (pending, spikes/out/s2a.log, spikes/out/s2b.log)

**Implication:** (pending: Phase 3 uses agent.wait vs polling agent.get)

## S3: Identity across session restart

**Question:** Is pane_id stable across restart? Is terminal_id stable? Is agent_session stable?

**Method:** `herdr agent list` before and after the human runs `herdr session stop prspike` and `herdr session attach prspike` (agent restarted with native restore if offered).

**Answer:** UNKNOWN (pending)

**Evidence:** (pending, spikes/out/s3.log)

**Implication:** (pending: the LIFE-02 identity guard uses terminal_id or agent_session)

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

**Method:** In a prspike pane: `go build -o spikes/setsid-probe/setsid-probe ./spikes/setsid-probe && spikes/setsid-probe/setsid-probe`. The child (re-exec with `SysProcAttr{Setsid: true}`) appends `heartbeat <ts> pid= ppid= sid=` to `spikes/out/setsid-heartbeat.txt` every second. The human runs `herdr session stop prspike`, waits 20 s, and the script checks whether heartbeats keep advancing.

**Answer:** UNKNOWN (pending)

**Evidence:** (pending, spikes/out/s5.log)

**Implication:** (pending: Phase 4/5 daemon lifecycle via the plugin [[startup]] vs self re-exec)

## S6: Multi-line and payload size

**Question:** Does an embedded \n produce a single submission? Largest accepted single payload (bytes)?

**Method:** `herdr agent prompt <pane> <payload>` to the agent and the bare shell with 2-line and 30-line payloads (every line starts with `# `), then single-line payloads of 900 B, 1100 B, 4096 B and 16384 B. Human records 1/n/x after each.

**Answer:** UNKNOWN (pending)

**Evidence:** (pending, spikes/out/s6.log)

**Implication:** (pending: the Phase 3 line joiner and flood ceiling)

## S7: Prompt after a completed turn, pane unfocused

**Question:** Is a prompt accepted and submitted when the agent has just finished a turn (status done) and the pane has not been focused since?

**Method:** After the agent finishes a turn with the script terminal focused: `herdr agent get <agent>`, then `herdr agent prompt <agent> "# pr-relay spike: S7 ..."`; human records y/n.

**Answer:** UNKNOWN (pending)

**Evidence:** (pending, spikes/out/s7.log)

**Implication:** (pending)
