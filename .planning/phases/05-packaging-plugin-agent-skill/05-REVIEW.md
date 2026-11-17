---
phase: 05-packaging-plugin-agent-skill
reviewed: 2026-11-16T00:00:00Z
depth: standard
files_reviewed: 51
files_reviewed_list:
  - .github/workflows/ci.yml
  - .github/workflows/release.yml
  - .goreleaser.yaml
  - cmd/pr-relay/daemon_cmd.go
  - cmd/pr-relay/skill_cmd.go
  - cmd/pr-relay/version.go
  - cmd/pr-relay/dev.go
  - cmd/pr-relay/format.go
  - cmd/pr-relay/keep.go
  - cmd/pr-relay/ls.go
  - cmd/pr-relay/main.go
  - cmd/pr-relay/poll.go
  - cmd/pr-relay/reset.go
  - cmd/pr-relay/status.go
  - cmd/pr-relay/sub.go
  - cmd/pr-relay/unsub.go
  - cmd/pr-relay/crash_test.go
  - cmd/pr-relay/daemon_cmd_test.go
  - cmd/pr-relay/docs_test.go
  - cmd/pr-relay/e2e_test.go
  - cmd/pr-relay/plugin_test.go
  - cmd/pr-relay/skill_test.go
  - cmd/pr-relay/version_test.go
  - go.mod
  - herdr-plugin/herdr-plugin.toml
  - herdr-plugin/run.sh
  - internal/autostart/autostart.go
  - internal/autostart/autostart_test.go
  - internal/autostart/ready.go
  - internal/autostart/spawn_unix.go
  - internal/config/config.go
  - internal/ctl/client.go
  - internal/ctl/server.go
  - internal/ctl/ctl_test.go
  - internal/daemon/daemon.go
  - internal/daemon/daemon_test.go
  - internal/ghclient/token.go
  - internal/herdr/client.go
  - internal/herdr/client_test.go
  - internal/herdr/endpoint.go
  - internal/herdr/stream.go
  - internal/herdr/herdrtest/server_test.go
  - internal/paths/paths_test.go
  - internal/poller/engine.go
  - internal/poller/engine_test.go
  - internal/poller/subscribe.go
  - internal/poller/subscribe_test.go
  - internal/store/store.go
  - internal/store/store_test.go
  - skills/embed.go
  - skills/skills_test.go
findings:
  critical: 0
  warning: 1
  info: 6
  total: 7
status: issues_found
---

# Phase 5: Code Review Report

**Reviewed:** 2026-11-16
**Depth:** standard
**Files Reviewed:** 51
**Status:** issues_found

## Narrative Findings (AI reviewer)

## Summary

I reviewed the Phase 5 diff (`daf5446..HEAD`): the CR-01, CR-02 and CR-03 fixes, the WR-03/WR-08/WR-09 autostart and daemon changes, `daemon --ensure`, the skill embed and install, the version fallback, the herdr plugin, GoReleaser and the release CI, and the errcheck sweep. The errcheck sweep is mechanical and correct. The store close guard (CR-02), the re-subscribe check-in clearing (CR-03), the lock-before-log reorder (WR-09) and the skill install's atomic write are sound. The plugin manifest version drift is already covered by a test (`TestPluginManifest`).

The main gap is that the CR-01 fix only half closes the problem. It bounds the daemon-side wait by the handler's own context. That context does not depend on the CLI's deadline, so the CLI can still report a timeout while the daemon later commits the subscription. The WR-08 probe retries make this more likely. The other items are Info-level robustness and accuracy notes. Already-tracked follow-ups F1, F2 and F3 are not repeated here.

## Warnings

### WR-01: The CR-01 promise that "CLI failure and stored state agree" does not hold, because the CLI deadline never reaches the daemon

**File:** `internal/poller/subscribe.go:94-97,165-168`, `internal/ctl/server.go:42-52,224-233`, `cmd/pr-relay/cli.go:18`, `internal/autostart/autostart.go:152-170,304-321`

**Issue:** `subscribeVerifiedCommit` checks `ctx.Err()` before committing. Its comment says this makes "the CLI's failure and the stored state agree". But that `ctx` is the server's handler context: `context.WithTimeout(s.base, 8s)` in `Server.call`. It is not tied to the CLI's deadline or to the client connection. The CLI has a single 10 s budget (`subTimeout`) that also covers `autostart.Call`, and in Phase 5 that path got longer:

- `probe` now retries a slow hello for up to about 2.6 s (WR-08).
- On a version mismatch, `Call` runs `Restart` first (spawn lock, stop, `waitExited`, spawn) and only then sends `sub` with whatever budget is left.

Example: after an upgrade, the first `sub` spends about 3-6 s restarting the daemon. It then sends `sub`, and the daemon gets a fresh 8 s. Meanwhile the new daemon's first scheduled poll holds `pollSem`. The CLI hits its 10 s deadline and tells the agent the sub timed out. The daemon then acquires the lock within its own 8 s, probes and commits. The agent was told the subscription failed but starts receiving updates anyway, which is the exact inconsistency CR-01 was meant to remove. The handler context is also never cancelled when the client hangs up.

**Fix:** Send the client's remaining budget with the request and have the server use the smaller of that budget and the method timeout, minus a margin for the reply. For example:

```go
// wire.go
type Request struct {
	...
	DeadlineMS int64 `json:"deadline_ms,omitempty"` // client's remaining budget
}

// client.go, when building the request
if dl, ok := ctx.Deadline(); ok {
	req.DeadlineMS = time.Until(dl).Milliseconds() - 250 // reply margin
}

// server.go call()
if req.DeadlineMS > 0 {
	if d := time.Duration(req.DeadlineMS) * time.Millisecond; d < timeout {
		timeout = d
	}
}
```

Alternatively, cancel the handler context when a read on the client conn returns EOF. Add an e2e or ctl test where the CLI deadline is shorter than the 8 s server timeout and the poll lock is held, and assert that nothing is persisted.

## Info

### IN-01: The version.go comment says local builds report "dev", but Go 1.24+ VCS-stamps them

**File:** `cmd/pr-relay/version.go:12-15`

**Issue:** With Go 1.24 or later, a plain `go build ./cmd/pr-relay` in this repo embeds a VCS pseudo-version in `info.Main.Version`. I confirmed this with a scratch build: `pr-relay v0.1.1-0.20261117004308-7f22364369f6+dirty`. So `resolveVersion` only returns "dev" for `-buildvcs=false` builds, which is what the tests use. Two consequences:

- The comment ("a local or devel build reports dev") is wrong.
- Successive edits on one commit all report the same `+dirty` version, so the version gate never restarts a stale dev daemon after a rebuild. This was already true with "dev", but the docs imply otherwise.

**Fix:** Correct the comment. Optionally, treat a `+dirty` version as never-equal in the gate, or append `vcs.time` or `vcs.modified` from `info.Settings`.

### IN-02: ensureUp's already-running branch can report "daemon did not exit" while a healthy daemon is still starting

**File:** `internal/autostart/autostart.go:132-145`

**Issue:** When our spawn loses the socket lock (`AlreadyRunningMsg`) and the 1 s hello still fails, the code assumes an old daemon is in its shutdown window and waits for the lock to be released. If the lock holder is a live daemon started outside the spawn lock (`brew services`, a manual foreground `pr-relay daemon`) that has not bound the socket yet, the lock is never released. The result is a misleading `daemon did not exit within 5s` error after about 9 s. The window is small, because startup between the lock and `Listen` is all local work, but the error text is wrong when it happens.

**Fix:** When `waitExited` times out, probe once more and return nil if a daemon answers. Or word the error so it covers both cases.

### IN-03: A handler still running after Close's second drain can reopen the log after the socket lock is released

**File:** `internal/ctl/server.go:149-159`, `internal/daemon/daemon.go:324-339`

**Issue:** `Close` now waits `drain` twice, but it can still return while a handler is running. After that the daemon closes the store, releases the socket lock and calls `lg.Close()`. lumberjack's `Write` reopens the file when `file == nil`, so a late `log.*` call from that handler would append to (and could rotate) the log that a successor daemon now owns. This is the situation WR-09 set out to prevent. The store side is protected by `ErrClosed`, but the logger is not.

**Fix:** Have the logger wrapper drop writes after Close (an atomic closed flag in the `OpenLog` handle), or wait on the server's wait group without a bound before releasing the lock.

### IN-04: The re-subscribe check-in clearing duplicates the expired-revival branch

**File:** `internal/poller/engine.go:451-466`

**Issue:** The `ReasonExpired` branch clears `CheckinAt` and filters `EventCheckin`. The new unconditional lines then do both again. This is harmless but redundant, and the two filters could drift apart.

**Fix:** In the expired branch, filter only `EventExpired` and rely on the shared `CheckinAt = {}` and `withoutCheckins` that follow. Or factor out a helper that does what `Keep` does and call it from both places.

### IN-05: run.sh `--open` with no pane id runs herdr with an empty entrypoint

**File:** `herdr-plugin/run.sh:10-13`

**Issue:** `pane_id="$2"` is never checked, so a manifest typo would produce `--entrypoint ""` and a confusing herdr error.

**Fix:** `[ -n "$2" ] || { echo "run.sh: --open needs a pane id" >&2; exit 2; }`

### IN-06: TestSlowHelloRetried depends on about 300 ms of timing slack

**File:** `internal/autostart/autostart_test.go` (TestSlowHelloRetried)

**Issue:** `slowListener` sleeps 700 ms per accept, serially. The second hello (sent at about 0.4 s with a 1 s timeout) is served at about 1.4 s, exactly at its deadline, so the test effectively passes on the third attempt with roughly 300 ms of margin. A loaded `-race` CI runner could flake.

**Fix:** Sleep in a per-connection goroutine instead of the accept loop, or use a delay (for example 500 ms) that sits clearly between `probeTimeout` and `postSpawnTimeout`.

---

_Reviewed: 2026-11-16_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
