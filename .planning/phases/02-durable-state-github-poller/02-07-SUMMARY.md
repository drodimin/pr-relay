---
phase: 02-durable-state-github-poller
plan: 07
subsystem: ghclient
tags: [github, graphql, auth, http, error-taxonomy, tdd]
requires:
  - 02-01 (go-gh v2.17.0 pinned)
  - 02-05 (ghquery.BuildBatch / DecodeBatch, ErrRateLimited, ErrNotFound)
provides:
  - ghclient.TokenSource, ErrNoToken, DefaultTokenTimeout
  - ghclient.Client, New, Options, TokenProvider, BatchResult, DefaultEndpoint
  - Client.Batch, Client.TokenSource, Client.Invalidate
  - ErrUnauthorized, ErrForbidden, *SecondaryLimitError, *PrimaryExhaustedError, *TransientError
  - Opt-in TestLiveBatch (PR_RELAY_LIVE=1) for plan 02-12
affects:
  - poller scheduler (maps the typed errors onto D-07 backoff; calls Invalidate on ErrNoToken)
  - cmd/pr-relay dev watch (ghclient.New with loopback-only endpoint override)
tech-stack:
  added: []
  patterns:
    - "config.Reload before every token resolution (go-gh config.Read is sync.Once)"
    - "exec.CommandContext + WaitDelay so a killed gh cannot hold pipes open"
    - "own http.Client with io.LimitReader(16 MiB+1) and bounded drain before Close"
    - "classification on raw status + headers before body decode"
key-files:
  created:
    - internal/ghclient/doc.go
    - internal/ghclient/token.go
    - internal/ghclient/token_test.go
    - internal/ghclient/errors.go
    - internal/ghclient/client.go
    - internal/ghclient/client_test.go
    - internal/ghclient/live_test.go
  modified: []
decisions:
  - "A 200 with usable data is returned even if x-ratelimit-remaining is 0; PrimaryExhaustedError is raised only for non-200 with remaining 0 or a RATE_LIMITED GraphQL error"
  - "All transport errors (not only timeouts) are *TransientError; a cancelled caller context returns the wrapped ctx error instead"
  - "Secondary-limit classification also covers a 403 whose REST-style body message says 'secondary rate limit'"
  - "A config.Reload error is ignored (stale cache kept) so env tokens and the gh fallback still work with a malformed hosts.yml"
  - "gh subprocess exit errors are rewritten to ProcessState.String() so captured stderr can never be wrapped into an error"
metrics:
  duration: ~20m
  completed: 2026-11-01
  tasks: 3
  files: 7
---

# Phase 2 Plan 07: ghclient token source and GraphQL client Summary

`internal/ghclient` is the only package that touches the network or a token. `TokenSource` resolves the token the way gh does (GH_TOKEN, GITHUB_TOKEN, hosts.yml oauth_token, then a `gh auth token --secure-storage` fallback bounded at 10s), calling `config.Reload` first every time so a `gh auth login` under a running daemon is picked up. `Client.Batch` POSTs `ghquery.BuildBatch` output with our own `http.Client`, re-resolves and retries once on 401, and classifies each failure into a typed error.

## Tasks

| Task | Name | Commits |
|------|------|---------|
| 1 | TokenSource with reload and bounded gh fallback | 7fcedbc (test), 5082472 (feat) |
| 2 | GraphQL client, error classification, 401 refresh | 7e9293a (test), 6ead0a9 (feat) |
| 3 | Opt-in live test for A1/A2 | 763b1d1 |

## Behavior

- Classification order: 401 (invalidate, re-resolve, retry once, then `ErrUnauthorized`). After that comes secondary limit: any 429, a Retry-After on 200/403, or a "secondary rate limit" message in `message` or `errors[].message`. Retry-After can be delta seconds or an HTTP date. On a non-200, `x-ratelimit-remaining: 0` gives `*PrimaryExhaustedError` with ResetAt taken from `x-ratelimit-reset`. Then 5xx gives `*TransientError` and any other 403 gives `ErrForbidden`. Remaining statuses get a generic `ghclient: unexpected status N`. On a 200 the body goes to `DecodeBatch`, and `ErrRateLimited` maps to `*PrimaryExhaustedError`, with ResetAt from the header or else from `rateLimit.resetAt`.
- `New` rejects any endpoint other than `DefaultEndpoint` unless it uses http(s) and its host is 127.0.0.1, ::1 or localhost. Tests check that lookalike hosts such as `127.0.0.1.example.com` and `api.github.com.evil.example` are rejected.
- Debug logging records only the status, request ID, token source, attempt number and duration. No error or log line contains the token, the headers or the body, and tests assert this for the 401 body and for gh stdout/stderr.

## Deviations from Plan

1. **[Rule 1 - Bug] A 200 with remaining 0 returns its data.** Read literally, the plan turns `x-ratelimit-remaining == "0"` into `PrimaryExhaustedError` on any status. That would throw away valid data from the request that used the last point. I apply the header rule only to non-200 responses. On a 200, a RATE_LIMITED error still gives `PrimaryExhaustedError`, and successful data comes back with `RateLimit.Remaining == 0`, which the scheduler uses to back off. The plan's TestPrimaryExhausted case (header plus RATE_LIMITED) passes. Commit 6ead0a9.
2. **[Rule 2] All transport errors are classified as `*TransientError`, not only timeouts.** Connection refused or DNS failure while offline should back off and retry. Without this they would hit the generic path. If the caller cancels the context, the wrapped ctx error is returned instead. Commit 6ead0a9.
3. **[Rule 2] `cmd.WaitDelay` on the gh fallback.** `CommandContext` kills only the direct child. A grandchild that holds stdout open (for example a gh wrapper script) would otherwise block `Output()` past the timeout. Commit 5082472.
4. **[Rule 1 - test] The timeout test's script uses `/bin/sleep`.** The test empties PATH, so a bare `sleep` failed at once and the script printed its token, which made the test vacuous. Fixed before the GREEN commit (5082472).
5. Tests were added beyond the plan: GITHUB_TOKEN source, the GhPath field, empty gh output, gh failure output not leaking, 403 with a REST secondary message, 403 with primary headers, 404, build errors sending no request, Invalidate and the token cache, and ErrNoToken propagating with no request sent.

## TDD Gate Compliance

Task 1 RED 7fcedbc (build failed: undefined TokenSource) was followed by GREEN 5082472. Task 2 RED 7e9293a (build failed: undefined TransientError and others) was followed by GREEN 6ead0a9. Task 3 is a test-only task (not TDD).

## Verification

- `go test -race -count=1 ./internal/ghclient/` passes. `go test -race -count=1 ./...` passes.
- `go vet ./...` and `gofmt -l internal/` are clean.
- Without PR_RELAY_LIVE, TestLiveBatch reports SKIP.
- Acceptance greps: `config.Reload` and `exec.CommandContext` are in token.go, and token.go contains no slog or log calls. `io.LimitReader`, `127.0.0.1`, `ghquery.BuildBatch` and `ghquery.DecodeBatch` are in client.go. The Authorization grep with Header.Set filtered out prints nothing.
- go.mod and go.sum are unchanged, and `go mod tidy` was not run. go-gh stays `// indirect` until 02-11.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: internal/ghclient/{doc,token,token_test,errors,client,client_test,live_test}.go
- FOUND: 7fcedbc, 5082472, 7e9293a, 6ead0a9, 763b1d1
