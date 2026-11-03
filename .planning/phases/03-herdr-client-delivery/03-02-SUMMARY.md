---
phase: 03-herdr-client-delivery
plan: 02
subsystem: herdr-client
tags: [herdr, ndjson, unix-socket, fake-server, synctest]
requires: []
provides:
  - "internal/herdr: Client, Options, DialFunc, DialUnix, Call, AgentGet, AgentWait, AgentPrompt, PaneGet, PaneList, Ping"
  - "internal/herdr: Error, UnreachableError, Code, IsUnreachable, nine Code* constants, Status* constants, Protocol"
  - "internal/herdr/herdrtest: fake herdr (net.Pipe Dialer, ListenUnix, Stop/Restart, scripted panes, recorders)"
affects: [03-05, 03-06, 03-07, 03-08]
tech-stack:
  added: []
  patterns:
    - "One request per connection; ctx cancel closes the conn via context.AfterFunc"
    - "Fake server wakes waiters with a close-and-replace broadcast channel (synctest-durable), never sync.Cond"
key-files:
  created:
    - internal/herdr/doc.go
    - internal/herdr/errors.go
    - internal/herdr/types.go
    - internal/herdr/client.go
    - internal/herdr/client_test.go
    - internal/herdr/herdrtest/server.go
    - internal/herdr/herdrtest/server_test.go
  modified: []
decisions:
  - "AgentWait rejects an empty until list and a non-positive timeout client-side, so neither ever reaches herdr (D-03, Pitfall 5)"
  - "A cancelled ctx returns a wrapped ctx error, not UnreachableError, so callers can tell shutdown from herdr being down"
  - "A response whose id does not match the request id is a decode error (error replies are returned first, since herdr answers invalid ids with id \"\")"
  - "Typed helpers check the result envelope type (agent_info, agent_prompted, pane_info, pane_list, pong) and fail with a decode error on mismatch"
  - "The fake renders wire JSON with hand-written snake_case maps rather than marshalling herdr types, so client tests also check the json tags"
metrics:
  duration: "~25 min"
  completed: 2026-11-02
---

# Phase 3 Plan 02: herdr one-shot client and fake herdr Summary

Stdlib-only NDJSON herdr client that dials a fresh unix-socket connection for each request (agent.get, agent.wait, agent.prompt, pane.get, pane.list, ping). Herdr error replies come back as `*herdr.Error`. Failing to reach herdr comes back as `*herdr.UnreachableError`, so DLV-08 can keep updates queued. Alongside it is `herdrtest`, an importable fake herdr that reproduces the verified protocol-22 request quirks over net.Pipe (synctest-safe) or a short-path unix socket.

## What was built

- **errors.go**: `Error{Code,Message}` (`herdr: <code>: <message>`), `UnreachableError{Socket,Err}` with Unwrap, `Code(err)`, `IsUnreachable(err)`, and the nine code constants.
- **types.go**: `AgentInfo`, `AgentSession`, `PaneInfo`, `WaitOpts`, `PromptParams` (wait omitempty), `Pong`, the status constants and `Protocol = 22`. Unknown fields are ignored.
- **client.go**:
  - Constants: `dialTimeout` 2s, `callTimeout` 5s, `waitSlack` 5s, `maxLine` 4 MiB.
  - `NewClient` cleans the socket path, defaults to `DialUnix`, and discards logs by default.
  - `Call` and the unexported `callWithTimeout`: string ids `pr-relay-<n>`, a deadline at the earlier of the ctx deadline and the timeout, and `context.AfterFunc` closing the conn on cancel. The read is bounded with `ReadSlice` (no Scanner).
  - `DialUnix` runs Lstat, then checks ModeSocket and that Uid matches `os.Getuid()`. It refuses with `not a unix socket` or `not owned by current user`.
  - Debug logging covers only method, socket, duration, code and unreachable. Params are never logged.
- **herdrtest/server.go**:
  - Scripting: `New(tb)`, `SetPane`, `RemovePane`, `Pane`, `SetStatus`, `SetOccupant`, `FailNext`.
  - Recorders: `Prompts`, `Requests` (raw params), `Calls`, `CallCount`, `Conns`.
  - Transport: `Dialer`, `ListenUnix` (`MkdirTemp("", "hd")/h.sock`), `ListenUnixAt`, `Stop` (refuses with ECONNREFUSED and removes the socket), `Restart`.
  - Wire behaviour: one response per connection, then close. A pipelined second request is drained and never answered. A non-string id gets `id:""` invalid_request. agent.* on a missing pane or bare shell answers agent_not_found, and pane.get answers pane_not_found. A blocked prompt records nothing. A prompt with `until:[working]` flips the pane to working. agent.wait covers no-until (the S2a trap), satisfied-at-once, broadcast wakeup, timeout_ms, agent_not_running on an occupant change, and abort when the client goes away.
  - Hooks for plan 03-05: the package-level `subscribeHandler` (owns the conn; gets `serverConn` with `gone` and `extra` channels) and `statusHook` (called with mu held on every status or occupant change).

## Tasks

| Task | Name | Commit |
| ---- | ---- | ------ |
| 1 | Wire types, typed errors and fake herdr server | f77d32f |
| 2 (RED) | Failing client tests | 469aa7e |
| 2 (GREEN) | One-shot request Client with socket ownership guard | 70551f6 |

## Verification

- `go vet ./internal/herdr/...` passes, and so does `GOOS=linux go vet`.
- `go test -race ./internal/herdr/... -count=5` passes.
- `go test ./... -count=1` passes.
- The acceptance greps pass: 9 Code constants, `json:"agent_status"`, `CompletionSeq *uint64`, no `sync.Cond`, `MkdirTemp("", "hd")`, `ModeSocket` and `os.Getuid()`, no `bufio.NewScanner`, `waitSlack` used more than once, and no params in logs.

## Deviations from Plan

**1. [Rule 3 - Blocking] DialFunc was created in client.go during Task 1.** The fake server's `Dialer()` returns `herdr.DialFunc`, so Task 1 committed a client.go that held only that type. Task 2 then filled the file in.

**2. [Rule 2 - Correctness] Added checks the plan did not list:**
- `AgentWait` rejects a timeout of zero or less.
- A response id that does not match the request id is an error.
- Each typed helper checks the result envelope `type`.
- The fake gained `Requests()`, `Conns()`, `Pane()` and `ListenUnixAt()`. The behaviour tests needed them to assert the recorded params and that each call used a new connection.

## TDD Gate Compliance

The RED commit (469aa7e, `test(...)`) comes before the GREEN commit (70551f6, `feat(...)`). No refactor commit was needed.

## Known Stubs

`events.subscribe` answers invalid_request until plan 03-05 sets `subscribeHandler`. This is intentional and in the plan.

## Self-Check: PASSED
