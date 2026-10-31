# Technology Stack

**Project:** pr-relay (local GitHub PR watcher daemon + CLI, pushes into herdr panes)
**Researched:** 2026-10-31
**Overall confidence:** MEDIUM-HIGH. Versions were checked against proxy.golang.org and GitHub releases on 2026-10-31. Library behaviour is from pkg.go.dev, the herdr docs and the local herdr schema.

Note: Go is not installed on this machine (`go: command not found`), so nothing was compiled. Install Go first, because phase 1 needs it.

## Recommended Stack

### Core
| Technology | Version | Purpose | Why | Confidence |
|------------|---------|---------|-----|------------|
| Go | 1.27.x (latest stable is 1.27.3). Set the `go` directive to the oldest release you actually test. | Language | Static binary, `log/slog`, `net/http` and `encoding/json` in the stdlib, and cross-compiling that needs no cgo. | HIGH |
| spf13/cobra | v1.10.2 | CLI framework (`sub`, `unsub`, `ls`, `daemon`, `status`, `version`) | The de facto standard. `gh` and many CLIs use it. It has shell completions and subcommands, and its last release was Dec 2025. The CLI is small, so any framework would work, and cobra is the one contributors already know. | HIGH |
| cli/go-gh/v2 | v2.17.0 (2026-10-20) | GitHub auth reuse, REST and GraphQL clients | It is the official library for building `gh`-adjacent tools. `auth.TokenForHost(host)` resolves a token in this order: env var (`GH_TOKEN`/`GITHUB_TOKEN`), then the `gh` config file, then the keyring through `gh auth token`. It also handles GHES hosts. | HIGH |
| stdlib `net` (unix socket) + `encoding/json` | n/a | CLI-to-daemon IPC and the herdr client | The newline-delimited JSON framing uses `bufio.Scanner`/`json.Decoder` and needs no dependency. | HIGH |
| stdlib `log/slog` | n/a | Structured logging | `slog.NewJSONHandler` or the text handler writes to a rotating file. No third-party logger is needed. | HIGH |

### Database / State
| Technology | Version | Purpose | Why | Confidence |
|------------|---------|---------|-----|------------|
| JSON state file (atomic write: temp file, fsync, rename) | n/a | Persist subscriptions, ETags, last-seen event cursors and the pending queue | The data is small (tens of PRs) and one process owns it. It is human-inspectable, needs no dependency and keeps the binary small. | HIGH |
| modernc.org/sqlite (fallback only) | v1.61.1 | Use it only if the state grows in size or needs queries | Pure Go, so there is no cgo and GoReleaser cross-compiles cleanly. The cost is about 5-10 MB of binary size and a higher cold-start cost. It is not justified for v1. | MEDIUM |

Decision: **JSON file, with state held in memory and flushed (debounced) on change.** Hide it behind a `Store` interface so SQLite can replace it later without touching the callers. Revisit SQLite only if you add event history or an audit log.

### Infrastructure (runtime, release, distribution)
| Technology | Version | Purpose | Why | Confidence |
|------------|---------|---------|-----|------------|
| GoReleaser | v2.19.0 (OSS) | Cross-build darwin/linux for arm64 and amd64, archives, checksums, GitHub release, Homebrew tap publish | It is the standard tool. Use `CGO_ENABLED=0`, `homebrew_casks` or `brews` for the tap, and `goreleaser-action` in GitHub Actions. Check the current docs, because GoReleaser has been moving formulae toward casks, and confirm which one fits a CLI. | HIGH on the tool, MEDIUM on the tap config details |
| Homebrew tap repo (`homebrew-tap`) | n/a | `brew install <owner>/tap/pr-relay`, plus `brew services` | A tap is the normal install route for macOS CLI users, and `brew services` gives launchd auto-start for free. | HIGH |
| golangci-lint | v2.x | Lint | Standard. | MEDIUM (version not checked) |
| GitHub Actions | n/a | CI (test on macOS and Linux) and release | Standard. | HIGH |

### Supporting Libraries
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| github.com/shurcooL/githubv4 | pseudo-version `v0.0.0-20260209031235-2402fdf4a9ed` | Typed GraphQL client | Optional. go-gh already ships a GraphQL client (`api.NewGraphQLClient`, with `Do` and `Query`). Use go-gh's GraphQL client, which accepts hand-written query strings. Avoid githubv4 unless you want its struct-tag queries. Batched multi-PR queries use aliases (`pr0: repository(...){...}`), so strings are easier to build dynamically. |
| github.com/BurntSushi/toml | v1.6.0 | Config file (`config.toml`) | Use it for config. It matches herdr's own TOML config, which gives users a familiar format. pelletier/go-toml/v2 (v2.4.3) is an equally good alternative. |
| github.com/gofrs/flock | v0.13.1 | Daemon singleton lock and a spawn race guard for auto-start | Use it so that two CLIs starting at once cannot both start a daemon. |
| github.com/stretchr/testify | v1.12.1 | Test assertions | Optional. The stdlib `testing` package is enough, and testify just trims boilerplate. |
| (stdlib) `net/http/httptest` | n/a | Fake GitHub and fake herdr servers | Required for testing. See Testing below. |
| gopkg.in/natefinch/lumberjack.v2 | v2.x (not version-checked) | Log rotation | Use it if logging to a file. The alternative is writing to stderr and leaving rotation to launchd or systemd. |

### Not recommended (see the table below)
google/go-github (v90.0.0), kardianos/service, urfave/cli/v3 (v3.14.0), alecthomas/kong (v1.16.1), hashicorp/go-retryablehttp (v0.7.8), gregjones/httpcache.

## Key design answers

### GitHub client: go-gh REST/GraphQL clients plus a thin ETag layer of our own
- **Use `go-gh`'s `api.NewRESTClient` / `api.NewGraphQLClient`.** They pick up `gh` auth, the host and the headers automatically, and they also respect `GH_HOST` and `GH_ENTERPRISE_TOKEN`.
- **ETag handling is our own code.** The client's built-in cache (`ClientOptions.EnableCache`, `CacheTTL`) is TTL-based and does not do conditional revalidation as far as I can tell (MEDIUM, not confirmed in source). For polling you want to send `If-None-Match` yourself and treat 304 as "nothing changed" without parsing a body. To do that, use the client's `RequestWithContext`/`DoWithContext` (go-gh returns the raw `*http.Response`), or build your own `http.Client` with `Authorization: Bearer <token>` from `auth.TokenForHost`. The second option is about 30 lines and gives you full control of headers, so prefer it for the poller.
- **Polling strategy this stack supports.** REST conditional requests (304 responses do not count against the primary rate limit) are cheap for "has anything changed" checks, for example `GET /repos/{o}/{r}/pulls/{n}`, `/commits/{sha}/check-runs`, `/issues/{n}/comments`, and `/pulls/{n}/reviews`. GraphQL does not support ETags, so use batched GraphQL (aliased PR lookups) when you need to refresh many active PRs in one request, and read the `rateLimit { cost remaining resetAt }` field. Plan the hybrid in the architecture phase. Reading `X-RateLimit-*` and `Retry-After` headers drives backoff.
- **Token handling.** Call `auth.TokenForHost(host)` and re-resolve on a 401. Do not cache the token forever, because the user may run `gh auth login` again. Do not shell out to `gh api` per poll (process churn, and it defeats low memory use). Do not require the `gh` binary at runtime, because `TokenForHost` only runs `gh auth token` as a keyring fallback.
- **Gotcha.** go-gh's pkg.go.dev page warns the auth package is "not in the latest version of its module", so pin the version and test token resolution on macOS (keyring) and Linux (plain config file) with CI. Own-activity exclusion needs the authenticated login, which comes from one `viewer { login }` query at startup.

### IPC (CLI to daemon)
- Unix domain socket in `$XDG_RUNTIME_DIR/pr-relay/pr-relay.sock` on Linux. Use `~/Library/Application Support/pr-relay/` or `~/.local/state/pr-relay/` on macOS, because macOS has no XDG_RUNTIME_DIR. Mode 0600, parent directory 0700. Watch the **104-byte sun_path limit on macOS**: keep the path short (fall back to `os.TempDir()/pr-relay-<uid>/`).
- Protocol: newline-delimited JSON request/response (`{"id","method","params"}` -> `{"id","result"|"error"}`), the same shape as herdr's. It is deliberately symmetrical with herdr, so one small `jsonline` package can serve both. Do not use gRPC, net/rpc or HTTP-over-socket, since they add dependencies and weight for 4 methods.
- The CLI `sub` command must be fast: dial with a short timeout, and if refused, take the flock, spawn the daemon and retry with backoff.

### herdr client
Verified against local herdr 0.9.5 (protocol 22) via `herdr api schema --json` and the docs.
- Socket resolution: `HERDR_SOCKET_PATH`, then `HERDR_SESSION`, then `~/.config/herdr/herdr.sock`. The environment inside a pane already provides `HERDR_PANE_ID` (for example `w1:p3`) and `HERDR_SOCKET_PATH`, so `sub` can capture both at subscribe time and store the socket path per subscription. That supports multiple herdr sessions.
- Request/response: one line in, one line out, matched by string `id`. Methods are confirmed in the schema: `agent.prompt` (with `target`, `text` and an optional `wait`), `agent.get`, `events.subscribe`, `pane.report_metadata`, `notification.show`, `pane.closed` and `pane.agent_status_changed`.
- **`events.subscribe` holds its connection open** and pushes event lines. Use a dedicated long-lived connection for events and a separate short-lived (or pooled) connection for requests. The docs do not say whether requests can be mixed on a subscribe connection, so do not assume it works. If a subscriber falls behind, herdr sends `events_lost` and closes the connection. So the client needs a reconnect loop that re-subscribes and then reconciles by calling `agent.get` for every pane with queued messages, because lifecycle events are not replayed. If any referenced pane in the request does not exist, the whole subscribe fails.
- Write this as an in-repo package, about 200 lines. No Go SDK for herdr was found, so none is recommended.

### Daemonization / auto-start
- **Do not double-fork.** Go cannot safely fork. Instead, the CLI re-execs itself (`os/exec` with `Setsid: true` through `syscall.SysProcAttr`, stdin/stdout/stderr redirected to the log file or `/dev/null`) as `pr-relay daemon`. This is the usual pattern, it works on macOS and Linux, and it stays inside the stdlib.
- Singleton: `flock` on a lock file held for the daemon's lifetime, plus a stale-socket check (try to dial, and on ECONNREFUSED remove the socket file).
- Primary lifecycle owner: the herdr plugin `[[startup]]` entry (herdr-plugin.toml requires `min_herdr_version` and a `platforms` list; see the docs). Auto-start from the CLI is the fallback, and `brew services` or a documented systemd user unit is an optional extra. Do not use kardianos/service: it targets system-level services and Windows, which is unneeded here.
- Graceful shutdown: `signal.NotifyContext` on SIGINT/SIGTERM, flush state, remove the socket. Exit if herdr is gone for a long time, or retry quietly (decide in the architecture phase).

### Logging
`log/slog` with a JSON or text handler to `~/.local/state/pr-relay/daemon.log` (macOS: `~/Library/Logs/pr-relay/`), with lumberjack for rotation. Level from config or `PR_RELAY_LOG`. Never log tokens. The `Authorization` header must not appear in debug request dumps.

### Config
A TOML file at `os.UserConfigDir()/pr-relay/config.toml` (BurntSushi/toml), plus env var overrides. Keep it small: poll intervals, own-activity default, toast on/off. Do not use viper (heavy dependency tree for a handful of keys).

### Testing
- Stdlib `testing` plus `httptest` for a fake GitHub (verify `If-None-Match` and 304 handling, rate-limit headers).
- A **fake herdr server** over a temp unix socket (a test helper that speaks the same NDJSON). Cover `agent_blocked`, `events_lost` reconnects and pane closure.
- Make the clock injectable, or use `testing/synctest` (stable since Go 1.25) for adaptive-interval and coalescing tests without real sleeps. MEDIUM: check the exact API in the Go release you choose.
- Golden-file tests for message formatting (the terse messages are the product).
- One opt-in integration test against a real herdr, gated by `HERDR_ENV`. Never send prompts to real panes in CI.

## Alternatives Considered

| Category | Recommended | Alternative | Why Not |
|----------|-------------|-------------|---------|
| CLI framework | cobra v1.10.2 | urfave/cli/v3 (v3.14.0), kong (v1.16.1) | Both work. Cobra wins on familiarity for contributors and on completion support. Kong would be fine and smaller if you want struct-tag declarations. This is low-stakes. |
| GitHub client | go-gh v2.17.0 + own ETag layer | google/go-github (v90.0.0) | go-github is a large typed REST client that still needs a token and has no gh auth integration. Its API churns across major versions (we are at v90). We only need about 5 endpoints plus GraphQL, so hand-rolled structs are less code than the dependency. |
| GraphQL | go-gh GraphQL client with raw query strings | shurcooL/githubv4 | It has no tagged release (pseudo-versions only). Struct-based queries make dynamic batched aliases awkward. |
| HTTP caching | Hand-rolled `If-None-Match` | gregjones/httpcache, go-gh cache | httpcache is unmaintained. We need per-resource ETag state that survives restarts, so it belongs in our own store. |
| Persistence | JSON file | modernc.org/sqlite v1.61.1, bbolt | SQLite adds binary size and schema migrations for tiny data. bbolt locks the file, so the CLI could not read it directly, and it is not needed anyway. |
| Auth | go-gh `auth.TokenForHost` | Shelling out to `gh auth token` | go-gh already does this as its keyring fallback, and it handles env vars and config first without spawning a process. |
| Auth | go-gh | Own OAuth device flow or PAT config | Violates the "no extra setup" constraint. |
| IPC | Unix socket + NDJSON | gRPC, net/rpc, HTTP | Too heavy, and net/rpc is frozen. |
| Daemon | Self re-exec + flock + plugin startup | kardianos/service, go-daemon | They add dependencies for system-service features we do not need. |
| Logging | slog | zap, zerolog | The stdlib is enough. |
| Config | BurntSushi/toml | viper | Heavy dependency tree. |

## Installation

```bash
# Prerequisite: install Go (not present on this machine)
brew install go

go mod init github.com/<owner>/pr-relay
go get github.com/cli/go-gh/v2@v2.17.0
go get github.com/spf13/cobra@v1.10.2
go get github.com/BurntSushi/toml@v1.6.0
go get github.com/gofrs/flock@v0.13.1
go get gopkg.in/natefinch/lumberjack.v2   # optional, for log rotation

# Dev / release
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
brew install goreleaser   # v2.19.0 at time of research
```

## Sources

- go-gh auth package: https://pkg.go.dev/github.com/cli/go-gh/v2/pkg/auth (HIGH; version v2.17.0 and the `TokenForHost` resolution order)
- herdr socket API docs: https://herdr.dev/tui/docs/socket-api/ (HIGH; framing, events.subscribe, plugin manifest)
- herdr CLI reference: https://herdr.dev/tui/docs/cli-reference/ (not fetched, relied on the local schema)
- Local `herdr api schema --json` and `herdr --version` (0.9.5, protocol 22), plus environment variables inside a pane (HIGH)
- Go module proxy (`proxy.golang.org/<module>/@latest`) for cobra, go-gh, go-github, modernc.org/sqlite, testify, flock, urfave/cli, kong, go-toml, retryablehttp, githubv4 and BurntSushi/toml (HIGH for version numbers)
- go.dev/dl JSON for the latest Go release 1.27.3 (HIGH)
- GitHub releases API for GoReleaser (v2.19.0) and go-gh (v2.17.0) (HIGH)
- Not verified against source: go-gh's built-in cache being TTL-only; `testing/synctest` details; GoReleaser `brews` vs `homebrew_casks` guidance; lumberjack and golangci-lint versions (flagged MEDIUM or LOW above)
