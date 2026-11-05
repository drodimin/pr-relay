---
phase: 04-daemon-control-protocol-cli
plan: 03
subsystem: ctl, paths
tags: [ipc, ndjson, unix-socket, version-gate, paths]
requires: []
provides:
  - internal/ctl wire contract (Protocol, Peer, Request, Response, Error, codes)
  - internal/ctl method schemas (hello, shutdown, sub, unsub, keep, poll, ls, status, reset)
  - ctl.Client.Call and ctl.Server (Serve, ServeConn, Close) with version gate
  - paths.SocketPath, SocketLockPath, SpawnLockPath, LogDir
affects: [04-06 autostart, 04-07 CLI formatting, 04-10 daemon handlers, 04-11 commands]
tech-stack:
  added: []
  patterns:
    - one request line / one response line per connection (mirrors herdr client)
    - net.Pipe + testing/synctest for socket protocol tests
key-files:
  created:
    - internal/ctl/wire.go
    - internal/ctl/methods.go
    - internal/ctl/client.go
    - internal/ctl/server.go
    - internal/ctl/ctl_test.go
  modified:
    - internal/paths/paths.go
    - internal/paths/paths_test.go
decisions:
  - "Explicit PR_RELAY_SOCKET is used verbatim; only derived socket paths get the 104-byte fallback"
  - "Handler errors wrapping context.DeadlineExceeded map to CodeTimeout; handler panics map to CodeInternal"
  - "Server.Close cancels handler contexts once drain completes or times out; it returns an error on drain timeout"
  - "Client default call deadline is 10 s; poll callers must pass a longer ctx (server poll timeout is 15 s)"
metrics:
  duration: ~15m
  completed: 2026-11-04
---

# Phase 4 Plan 03: Control protocol and socket paths Summary

I added an NDJSON control protocol over a unix socket: a version gate where hello and shutdown are exempt, a dial guard that checks uid and that the path is a socket, a 1 MiB line cap, a 5 s read deadline and per-method handler timeouts. The plan also adds per-user socket, lock and log paths with a sun_path fallback.

## What was built

- **internal/paths**: `SocketPath` (PR_RELAY_SOCKET, then `<PR_RELAY_STATE_DIR>/pr-relay.sock`, then `$XDG_RUNTIME_DIR/pr-relay/pr-relay.sock`, then `<state dir>/pr-relay.sock`; a derived path of 104 bytes or more falls back to `os.TempDir()/pr-relay-<uid>/`), `SocketLockPath`, `SpawnLockPath`, `LogDir` and `ensurePrivateDir`. `ensurePrivateDir` refuses a directory that is a symlink, is not a directory, or is owned by another uid, with the message "unsafe socket dir".
- **internal/ctl/wire.go**: `Protocol = 1`, `Peer`, `Request`, `Response`, `Error`, 16 error codes, `MaxLine` and `ReadTimeout`, `UnreachableError`, `VersionMismatchError`, `Code()` and `IsUnreachable()`.
- **internal/ctl/methods.go**: 9 method constants and every param and result struct, with snake_case JSON tags.
- **internal/ctl/client.go**: `DialUnix` with `Lstat`, a `ModeSocket` check and a `Getuid` check. `NewClient` and `Call(ctx, method, params, result) (Peer, error)`. It uses crypto/rand request IDs and checks that the response ID matches.
- **internal/ctl/server.go**: `NewServer(ServerOptions)`, `Serve(ln)`, `ServeConn(c)` and `Close(drain)`. Default method timeouts are 8 s for sub, 15 s for poll and 3 s for everything else. Logs record only the method and the error code.

## Verification

- `go test -race -count=5 ./internal/ctl ./internal/paths` passes, and `go vet` and `gofmt` are clean.
- The ctl tests cover:
  - round trip
  - version gate on version and on protocol, with the handler never run
  - hello and shutdown working despite a mismatch
  - unknown method and malformed JSON returning bad_request
  - a 2 MiB line returning bad_request and closing the connection
  - read timeout at exactly 5 s of synctest time
  - handler error mapping: not_found, internal, timeout after 15 s, panic
  - drain and drain timeout
  - a real unix-socket `Serve`
  - the dial guard: missing path (ErrNotExist), regular file, stale socket returning ECONNREFUSED

## Deviations from Plan

### Auto-added (Rule 2)

**1. Handler panic recovery and timeout code mapping**
- **Found during:** Task 3
- **Issue:** A panicking handler would crash the daemon, and a handler that hits its deadline would have been reported as `internal`.
- **Fix:** `Server.call` recovers panics and turns them into `CodeInternal`. Errors that wrap `context.DeadlineExceeded` become `CodeTimeout`.
- **Commit:** 00d5a85

**2. Write deadline on replies**
- **Fix:** I set a 2 s write deadline on every reply, so a client that never reads cannot pin a server goroutine (T-04-13).
- **Commit:** 00d5a85

### TDD gate note

The tasks were marked `tdd="true"`. I wrote each task's tests and implementation together and committed them as a single `feat` commit, so no separate RED `test(...)` commit exists. All behaviours listed in the plan are covered by the tests.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: internal/ctl/wire.go, methods.go, client.go, server.go, ctl_test.go
- FOUND: internal/paths/paths.go, paths_test.go
- FOUND commits: e00d70b, eb85884, 00d5a85
