---
phase: 04-daemon-control-protocol-cli
reviewed: 2026-11-05T00:00:00Z
depth: standard
files_reviewed: 40
files_reviewed_list:
  - cmd/pr-relay/cli.go
  - cmd/pr-relay/daemon_cmd.go
  - cmd/pr-relay/events.go
  - cmd/pr-relay/format.go
  - cmd/pr-relay/keep.go
  - cmd/pr-relay/ls.go
  - cmd/pr-relay/main.go
  - cmd/pr-relay/poll.go
  - cmd/pr-relay/reset.go
  - cmd/pr-relay/status.go
  - cmd/pr-relay/sub.go
  - cmd/pr-relay/unsub.go
  - go.mod
  - internal/autostart/autostart.go
  - internal/autostart/ready.go
  - internal/autostart/spawn_unix.go
  - internal/compose/compose.go
  - internal/config/config.go
  - internal/ctl/client.go
  - internal/ctl/methods.go
  - internal/ctl/server.go
  - internal/ctl/wire.go
  - internal/daemon/daemon.go
  - internal/daemon/handlers.go
  - internal/daemon/log.go
  - internal/deliver/manager.go
  - internal/herdr/endpoint.go
  - internal/herdr/env.go
  - internal/herdr/stream.go
  - internal/model/model.go
  - internal/paths/paths.go
  - internal/poller/engine.go
  - internal/poller/fanout.go
  - internal/poller/lifecycle.go
  - internal/poller/scheduler.go
  - internal/poller/status.go
  - internal/poller/subscribe.go
  - internal/render/render.go
  - internal/store/state.go
  - internal/store/store.go
findings:
  critical: 3
  warning: 10
  info: 5
  total: 18
status: issues_found
---

# Phase 4: Code Review Report

**Reviewed:** 2026-11-05T00:00:00Z
**Depth:** standard
**Files Reviewed:** 40
**Status:** issues_found

## Narrative Findings (AI reviewer)

## Summary

I reviewed the daemon host, the control protocol, autostart and restart, the CLI commands, the poller's subscribe and lifecycle paths, and the herdr endpoint and compose hardening (IN-01/02/03). The startup lock ordering is correct: the socket lock comes first, the store lock is taken under it, the socket lock is released last, and stale-socket removal happens only under the lock and only for a real socket. Strict param decoding and the hello/shutdown version-gate exemption are also correct.

The main defects are in how request handlers interact with `pollMu` and with shutdown:

- The `sub` and `poll` handlers wait on `pollMu` with no deadline. As a result, a `sub` can be reported to the CLI as failed and then be persisted anyway.
- Handlers that outlive the 2 s drain keep writing to `state.json` after the store lock and the socket lock are released, so a restarted daemon can have its state overwritten.

I confirmed the known lead (a live re-subscribe keeps a stale pending check-in). It is a real defect. Several robustness gaps also break D-18 ("the daemon never exits on its own") or make autostart and restart fail in supervised or degraded environments.

## Critical Issues

### CR-01: `sub` and `poll` wait on `pollMu` with no deadline, so a `sub` can be persisted after the CLI was told it failed

**File:** `internal/poller/subscribe.go:87-91`, `internal/poller/engine.go:939-941` (pollRefs), `internal/daemon/handlers.go:154-162,374`
**Issue:** `SubscribeVerified` calls `e.pollMu.Lock()` without checking the context. A scheduled poll holds `pollMu` for the whole network call, including the optional wide re-fetch. With ghclient's 30 s HTTP timeout, that can last up to about 60 s on a slow network or a stalled token lookup. During that time the `sub` handler blocks past its 5 s verify window, the server's 8 s method timeout and the CLI's 10 s `subTimeout`, and the CLI prints a deadline error.

Once the scheduled poll releases the lock, the handler runs the "re-check under pollMu" fast path (`subscribeVerifiedCommit(req, mask, nil)`). That path never looks at `ctx`. If the PR record is baselined, for example because another pane watches it or the poll that held the lock just baselined it, the subscription is saved and updates start arriving in a pane whose agent was told the subscribe failed. This breaks SUB-01 (bounded `sub`) and the D-08 contract that the CLI outcome matches the stored state.

`PollNow` (`h.poll`) has the same unbounded wait, so `pr-relay poll` can also hang past its 15 s/20 s budget.
**Fix:** Make the poll lock context-aware and re-check `ctx` before committing:
```go
// Engine: replace pollMu sync.Mutex with a 1-slot semaphore
pollSem chan struct{} // make(chan struct{}, 1)

func (e *Engine) lockPoll(ctx context.Context) error {
	select {
	case e.pollSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (e *Engine) unlockPoll() { <-e.pollSem }

// SubscribeVerified
if err := e.lockPoll(ctx); err != nil {
	return SubInfo{}, verifyErr(ctx, err)
}
defer e.unlockPoll()
if err := ctx.Err(); err != nil {
	return SubInfo{}, verifyErr(ctx, err)
}
```
Also check `ctx.Err()` right before the `mutateLocked` in `subscribeVerifiedCommit`, so a commit never happens once the caller's deadline has passed.

### CR-02: Shutdown closes the store and releases the singleton lock while handlers can still save state

**File:** `internal/daemon/daemon.go:321-337`, `internal/ctl/server.go:128-152`, `internal/store/store.go:221,275-288`
**Issue:** `srv.Close(drainTimeout)` waits only 2 s. If handlers are still running, it cancels their context and returns, and the daemon goes on to `stopWork`, `wg.Wait`, `st.Close()` and `lk.Unlock()`. Leftover handlers are not tracked after that point.

`Store.Save` has no closed guard: it writes a temp file and renames it over `state.json` whether or not the lock is held. A handler that is blocked on `pollMu` (CR-01), or inside the fsync in `mutateLocked`, can therefore save after `st.Close()` and after the socket lock is released. That is exactly the moment `autostart.Restart` (the D-17 version-mismatch path) spawns the new daemon, which then loads `state.json`.

The old process's late rename can then overwrite a state the new daemon has already written (an Ack, a new subscription). The result is lost subscriptions or re-delivered events, which breaks the copy-on-write "memory never ahead of disk" invariant and EVT-09. Stopping `work` cancels the poller, and that releases `pollMu` to a blocked handler at the worst possible moment.
**Fix:** Do not close the store until every handler has returned, and make Save refuse to run after Close:
```go
// ctl.Server.Close: after the drain timer fires and s.cancel(), still wait
// (bounded, e.g. another few seconds) for s.wg; return an error if it never drains.

// store.Store
func (s *Store) Save(st *State) error {
	if s.lock == nil {
		return errors.New("store: save after close")
	}
	...
}
```
Store.Close and Save also need a mutex for this check to be race-free. Combined with CR-01, a cancelled handler then exits promptly instead of waking up after the poller stops.

### CR-03: A live re-subscribe keeps a stale pending check-in (lead from 04-08, confirmed)

**File:** `internal/poller/engine.go:439-466` (subscribeIn), compare with `engine.go:561-582` (Keep)
**Issue:** `subscribeIn` drops pending `checkin`/`expired` events only when `sub.Ending == ReasonExpired`. For a live subscription with an outstanding check-in (queued but not yet delivered because the pane was busy or herdr was down), a re-subscribe resets `ClockAt` but leaves the `checkin` event in `Pending` and leaves `CheckinAt` set. `refilter` does not remove it either, because `maskAllows` always passes lifecycle events (`fanout.go:34`).

So an agent that just ran `pr-relay sub <url>` later receives `check-in: no activity for 24h. still needed? keep: … | drop: …`. That claim is false: D-11 says re-subscribing resets the inactivity clock, and `Keep`, which resets the same clock, does drop the pending check-in. An agent following the prompt may then unsubscribe a subscription it just renewed. Plan 04-08 recorded this and 04-09 did not fix it, and no test covers live re-subscribe with a pending check-in.
**Fix:** Treat a re-subscribe like `keep` for lifecycle state:
```go
if sub := next.Subs[id]; sub != nil {
	switch sub.Ending { /* closed / expired cases unchanged */ }
	sub.CheckinAt = time.Time{}
	sub.Pending = withoutCheckins(sub.Pending)
	...
	sub.ClockAt = now
	sub.Pending = refilter(sub.Pending, mask)
```
Add a test: queue a check-in, re-subscribe the live sub, and assert that `Pending` holds no `EventCheckin` and that `CheckinAt` is zero.

## Warnings

### WR-01: A reopened PR cannot be re-subscribed while cached Memory says CLOSED, and records with only ending subs are joined without verification

**File:** `internal/poller/subscribe.go:148-155`
**Issue:** `subscribeVerifiedCommit` returns `pr_closed` straight from the cached `rec.Memory.State` without probing GitHub. When every sub on a PR is ending, the PR is no longer polled (`scheduler.go:91`), so that Memory never refreshes. A closed-ending sub on a pane that never becomes idle can stay pending indefinitely. Until it is acked, every new `sub` to that PR, from any pane and even after the PR is reopened, fails with "already merged or closed". The P1/D-16 guidance is "re-subscribe after reopen", and this path makes that impossible.

The mirror case is a record whose only subs are expired-ending. It is baselined and OPEN but stale (days old, not polled), and a new `sub` joins it on the fast path without any GitHub call, even if the PR has since been deleted or made inaccessible. That is an unverified subscription, which goes against D-08.
**Fix:** Allow the no-probe fast path only when the record has at least one live (non-ending) subscriber and `LastPollAt` is recent. When the cached state is not OPEN, probe instead of failing, and on an OPEN probe result refresh `rec.Memory` and clear `Terminal` before calling `subscribeIn`.

### WR-02: Any non-timeout Accept error stops the daemon for good

**File:** `internal/ctl/server.go:92-102`, `internal/daemon/daemon.go:487-493`
**Issue:** `Serve` retries only `ne.Timeout()` errors. EMFILE/ENFILE (fd exhaustion, which can happen with many herdr endpoint connections plus a burst of CLIs) and ECONNABORTED come back as plain errors. `Serve` returns, and the daemon shuts down with "control socket failed". This breaks D-18 ("never exits on its own") and leaves every agent without delivery until something spawns a daemon again.
**Fix:** Do what `net/http` does: on a temporary accept error (`errors.Is(err, syscall.EMFILE)`, `ENFILE`, `ECONNABORTED`, `ENOBUFS`), back off exponentially (5 ms up to 1 s) and `continue`. Return only when `s.isClosed()` or the error is `net.ErrClosed`.

### WR-03: During any shutdown, a concurrent CLI fails with "daemon: already running"

**File:** `internal/daemon/daemon.go:496-509`, `internal/autostart/autostart.go:108-124`, `internal/autostart/autostart.go:162-188`
**Issue:** The socket is unlinked at `srv.Close`, but the singleton lock is held until after `wg.Wait`, `st.Close` and the final Save. In that window a CLI sees ENOENT, so `isDown` returns true. It spawns a daemon, which gets `ErrAlreadyRunning`, and the post-spawn hello also gets ENOENT. `ensureUp` then returns `spawnErr`, and the agent sees `pr-relay: daemon: already running` for an ordinary `sub`.

`pr-relay daemon stop` and SIGTERM do not take the spawn lock, so they do not serialise with autostart. Only `Restart` is protected.
**Fix:** In `ensureUp`, when the spawn reported `already running` (or hello fails right after a spawn), wait on the socket lock (`waitExited`-style, bounded) and spawn once more. Alternatively, have the daemon wait on the socket lock with `TryLockContext` for a couple of seconds before giving up, so a successor started during a shutdown takes over cleanly.

### WR-04: The version-mismatch restart fights launchd, systemd or herdr-plugin supervision

**File:** `internal/autostart/autostart.go:130-158,215-239`
**Issue:** D-19 names `brew services`, systemd and the herdr `[[startup]]` hook as primary owners of `pr-relay daemon`. When the supervised (older) binary mismatches the CLI, `Call` sends `shutdown` and spawns the CLI's own binary. A KeepAlive supervisor restarts the old binary at the same moment, and whichever daemon wins the socket lock serves. When the old one wins, the retried call gets `version_mismatch` again and fails with "mismatch persists after restart". Every later CLI call repeats the stop/spawn cycle, which drops in-memory state each time (budget, stretch, reachability, the cancellation ring, the auth state).
**Fix:** At minimum, detect this case and stop retrying. After a restart, if the answering daemon's PID differs from ours and its version still mismatches, return a clear error ("daemon is managed by a supervisor running pr-relay X; upgrade it or restart the service"). Do not restart again for a while; for example, record the last restart time next to the spawn lock and skip restarts for 60 s. Document the interaction.

### WR-05: The singleton lock lives in a volatile runtime dir, but the state lock does not

**File:** `internal/paths/paths.go:110-125`, `internal/daemon/daemon.go:297,325-339,349-352`
**Issue:** On Linux the socket and `pr-relay.sock.lock` are in `$XDG_RUNTIME_DIR`, while `state.lock` is in the persistent state dir. systemd-logind removes `/run/user/$UID` when the last session ends. A setsid daemon survives that (the design relies on it, per S5), but its socket and lock file are deleted. On the next login the CLI sees no socket and spawns a new daemon. That daemon takes a fresh socket lock, then fails `store.Open` with `ErrLocked`, which is reported as "state in use by another pr-relay (dev watch?)". The orphaned old daemon keeps polling but cannot be reached, and every `sub` fails until the user finds and kills it.
**Fix:** Put the singleton lock in the state dir (next to `state.lock`), or treat `store.ErrLocked` in the daemon as "another daemon is alive". In the second case, report its PID from `daemon.pid` and give a recovery message such as "kill PID" or "pr-relay daemon stop --force". Either way, `waitExited` must then wait on the same lock.

### WR-06: A subscription can expire without any check-in ever being queued

**File:** `internal/poller/lifecycle.go:28-40`, `internal/daemon/daemon.go:214-217`
**Issue:** `lifeDue` checks expiry before the check-in and needs no prior check-in. If the daemon was not running or the machine slept for longer than `expire_after - checkin_after` (6 days with the defaults, for example a week of vacation with the laptop closed), the first tick after wake or restart expires every subscription directly. The agent never gets the D-12/D-13 chance to `keep`. The 10-minute `lifecycleGrace` does not help: `ClockAt` moves only when a real event is delivered and acked, which almost never happens within 10 minutes. After a sleep the daemon does not restart, so the grace does not apply anyway.
**Fix:** Only expire a subscription that has had an outstanding check-in for a minimum time, for example `!sub.CheckinAt.IsZero() && now.Sub(sub.CheckinAt) >= checkin`. Otherwise queue a check-in first. Also detect large wall-clock jumps between ticks and apply the grace then too.

### WR-07: The IN-01 partial-ack cap re-delivers lines that were already shown, and the fallback still splits the pair

**File:** `internal/compose/compose.go:194-215` (partialAck)
**Issue:** When a cancelled conflict pair straddles the flood-guard cut, the ack is capped at `low-1`. Kept items with `seq` in `[low, heldSeq-1]` were included in this message (they come before `cut`) but are now unacked, so the next flush delivers those lines to the agent a second time. When `low-1 < first`, the function falls back to `heldSeq-1`, which splits the pair again: the very IN-01 case it was meant to fix.
**Fix:** Do not cap the ack. Move the cut instead: when a cancelled pair straddles it, end the included set before the first kept item whose seq is above `low` (hold those lines too), so that the ack `low-1` covers exactly what was shown. If that would include nothing, hold the whole group (as `isFirst` does). Add a compose test that asserts no line appears in two consecutive messages.

### WR-08: Connect treats a slow live daemon as a hard error

**File:** `internal/autostart/autostart.go:21,89-93,109-113`
**Issue:** The first probe uses a 300 ms hello timeout. On a timeout, `Client.Call` returns a wrapped `context.DeadlineExceeded`, not `*UnreachableError`, so `isDown` is false and Connect returns the error at once. The "spawn if down, otherwise retry" path is never taken. Under load (the 20-CLI cold start of SC-2, a daemon in a long fsync, or a machine just woken from sleep) an agent's `sub` fails with "ctl: hello: context deadline exceeded".
**Fix:** On a hello timeout, retry with the longer `postSpawnTimeout` (and backoff) before failing, and spawn only on `isDown`. The error message should name the daemon log.

### WR-09: A losing daemon opens and can rotate the running daemon's log before checking the singleton lock

**File:** `internal/daemon/daemon.go:311-322` vs `325-338`
**Issue:** `OpenLog` and `SetCrash` run before `TryLock`, so every losing starter writes into the live daemon's `daemon.log` through its own lumberjack instance: SC-2 spawns up to about 20, and the plugin's `[[startup]]` is another. If the file is near 5 MB, the loser rotates it (rename plus a new file). The running daemon keeps writing to the renamed backup, and the rotation bookkeeping of the two processes diverges. Rotated logs can be lost or misnamed, which makes DMN-06 diagnostics unreliable.
**Fix:** Take the socket lock first (logging any failure to stderr or the ready pipe), then open the log and crash file. Alternatively, have the loser log only to the ready pipe.

### WR-10: A new subscription does not wake the delivery manager

**File:** `internal/poller/subscribe.go:192-198`, `internal/deliver/manager.go:182-189`
**Issue:** A freshly created sub (`created == true`) only calls `poke()` (the scheduler) when it creates a new PR record, and calls `pokePending()` only for re-subscribes. The manager therefore learns of a new pane, or of the first sub on a new herdr socket, only at the next 60 s resync. Until then, no endpoint exists for that socket. `status` shows `herdr: none`, `pane_closed` and `agent_status` events for the pane are not received, and the D-26 premise ("every sub triggers a resubscribe", which is why IN-03 was folded in) does not hold.
**Fix:** Call `e.pokePending()` after every successful subscribe commit, not only when `!created`.

## Info

### IN-01: `ensurePrivateDir` silently chmods any parent directory of `PR_RELAY_SOCKET` to 0700

**File:** `internal/paths/paths.go:101-107,177-195`
**Issue:** `PR_RELAY_SOCKET=$HOME/x.sock` would chmod `$HOME` to 0700. `PR_RELAY_LOG_DIR` and `PR_RELAY_STATE_DIR` behave the same way. These are test hooks, but they are reachable by users.
**Fix:** Refuse an existing directory whose mode is looser than 0700 instead of tightening it, or tighten only directories that pr-relay created.

### IN-02: Pane IDs are never validated and are printed raw to the terminal

**File:** `internal/daemon/handlers.go:73-83` (target), `cmd/pr-relay/format.go:61,136`
**Issue:** `--pane` accepts any string, including newlines and ANSI escapes. That string is stored, logged and echoed in `ls` and `status` output, where it can forge rows. The risk is low because only the same user can do this.
**Fix:** Validate pane IDs against herdr's form (for example `^[A-Za-z0-9:_-]{1,64}$`) in `target()` and on the CLI side.

### IN-03: `keep` on a closed-ending sub suggests re-subscribing, which then fails

**File:** `internal/daemon/handlers.go:346-347`
**Issue:** For `Ending == "closed"`, the message says "re-subscribe with pr-relay sub …", but `subscribeIn` returns `pr_closed` for that sub.
**Fix:** Word the message by `Ending`, for example "the PR was merged or closed; this subscription is ending".

### IN-04: Test-quality issues

**File:** `internal/daemon/daemon_test.go:120-170`, `cmd/pr-relay/e2e_test.go:97`, `cmd/pr-relay/lifecycle_e2e_test.go:32`
**Issue:** In-process `daemon.Run` calls `debug.SetCrashOutput` and `signal.NotifyContext`, which are process-wide. Later panics in the test binary go to a crash.log inside a temp dir that has already been deleted, so their diagnostics are lost. The negative assertions ("comment not delivered", "no check-in within 1.5 s of keep") rely on fixed sleeps and pass vacuously if delivery is merely slow.
**Fix:** Inject the crash-output setter into the daemon (as a no-op in tests). Before asserting absence, wait for a positive marker event that was sent after the masked one.

### IN-05: The restart note prints even when this CLI did not restart anything

**File:** `internal/autostart/autostart.go:145-156,231-234`
**Issue:** When `Restart` returns early because another CLI already brought the daemon to `o.Version`, `Call` still prints `restarted daemon (OLD -> NEW)`, so with N concurrent CLIs the note appears N times.
**Fix:** Have `Restart` report whether it actually stopped and spawned a daemon, and print the note only in that case.

---

_Reviewed: 2026-11-05T00:00:00Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
