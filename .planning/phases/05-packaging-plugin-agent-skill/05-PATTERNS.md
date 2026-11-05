# Phase 5: Packaging, Plugin & Agent Skill - Pattern Map

**Mapped:** 2026-11-05
**Files analyzed:** 27 (new and modified)
**Analogs found:** 22 / 27

## File Classification

### Release gate: CR-01..03 and WR-03/08/09 (D-21, D-26). These come first.

| File | Role | Data Flow | Closest Analog | Match Quality |
|------|------|-----------|----------------|---------------|
| `internal/poller/engine.go` (modify: `pollMu` becomes a context-aware semaphore, CR-01; `subscribeIn` clears check-ins, CR-03) | service | event-driven / CRUD | itself: `Keep` (engine.go:561-581), `withoutCheckins` (lifecycle.go:51-59) | exact |
| `internal/poller/subscribe.go` (modify: `SubscribeVerified` uses `lockPoll(ctx)` and checks `ctx` before committing, CR-01) | service | request-response | itself, lines 72-98, 144-200 | exact |
| `internal/poller/subscribe_test.go` (add `TestSubscribeVerified*Deadline`, `TestResubscribeClearsCheckin`) | test | — | `TestResubRevivesExpired` (subscribe_test.go:315-370) | exact |
| `internal/store/store.go` (modify: closed guard and mutex on `Save`/`Close`, CR-02) | model/store | file-I/O | itself: `Close` (store.go:275-288) | exact |
| `internal/store/store_test.go` (add `TestSaveAfterClose`) | test | — | `TestStoreLocked` (store_test.go:203-216) | exact |
| `internal/ctl/server.go` (modify: `Close` waits a bounded time for `s.wg` after cancel, CR-02) | middleware/server | request-response | itself: `Close` (server.go:128-152) | exact |
| `internal/ctl/ctl_test.go` / `internal/daemon/daemon_test.go` (add `TestShutdownDrain`) | test | — | `TestCloseDrainTimeout` (ctl_test.go:301), `startDaemon`/`lockFree` (daemon_test.go:126,201) | exact |
| `internal/daemon/daemon.go` (modify: take the socket lock before `OpenLog`/`SetCrash`, WR-09) | service/host | startup ordering | itself, lines 118-164 | exact |
| `internal/autostart/autostart.go` (modify: `ensureUp` retries after "already running", WR-03; hello timeout retried with `postSpawnTimeout`, WR-08) | service | request-response | itself: `ensureUp` (108-124), `Connect` (79-104), `waitExited` (193-210) | exact |
| `internal/autostart/autostart_test.go` (add WR-03 and WR-08 tests) | test | — | `TestConcurrentConnect`, `TestStaleSocket` (autostart_test.go:178, 261) and the `fakeDaemon` re-exec (30-94) | exact |

### Code changes: `--ensure`, `skill` and the version fallback

| File | Role | Data Flow | Closest Analog | Match Quality |
|------|------|-----------|----------------|---------------|
| `cmd/pr-relay/daemon_cmd.go` (modify: `--ensure` flag plus `runEnsure`) | controller (cobra cmd) | request-response | `runDaemonRestart` (daemon_cmd.go:97-110) and its wiring (40-60) | exact |
| `cmd/pr-relay/main.go` (modify: register `newSkillCmd()`, `version` default `""` resolved once) | config/entry | — | itself, lines 12-38 | exact |
| `cmd/pr-relay/version.go` (new: `resolveVersion`) | utility | transform | `versionString` (main.go:15-17), `cliError` (cli.go:61-70) as a small pure-helper style | role-match |
| `cmd/pr-relay/version_test.go` (new `TestResolveVersion`) | test | — | `main_test.go:5-9` and table tests in `cli_test.go` | role-match |
| `cmd/pr-relay/main_test.go` (modify) | test | — | itself. **It asserts `"pr-relay dev"` today, and this must still hold after resolution under `go test`, where buildinfo is `(devel)` or empty** | exact |
| `cmd/pr-relay/skill_cmd.go` (new: `skill`, `skill install [--dir]`) | controller (cobra cmd) | file-I/O | `reset.go` (flags plus a testable `runX` with injected writers), `ls.go:15-29` (flag declaration), `store.Save` (store.go:221-263, atomic write) | role-match |
| `cmd/pr-relay/skill_test.go` (new `TestSkill`) | test | — | `cli_test.go:104-128` (`bytes.Buffer` stdout/stderr, call `runX` directly) | role-match |
| `skills/embed.go` (new: `package skills`, `//go:embed pr-relay/SKILL.md`) | utility | static asset | `internal/ghquery/query.go:15-16` (`//go:embed pr.graphql`) | exact |
| `skills/pr-relay/SKILL.md` (new: canonical skill) | doc/asset | — | the golden lines in `internal/render/testdata/*.golden` (examples must be copied verbatim) | partial |
| `skills/skills_test.go` (new: frontmatter rules, prescriptive-word lint, `Snippet()`) | test | — | `internal/render/render_test.go:207` `TestLineGrammar` style | partial |
| `cmd/pr-relay/e2e_test.go` (add `TestDaemonEnsure`) | test (integration) | — | `TestDaemonRestartCmd` (e2e_test.go:286-298), `TestVersionMismatch` (137-160) | exact |

### Plugin, docs and release

| File | Role | Data Flow | Closest Analog | Match Quality |
|------|------|-----------|----------------|---------------|
| `herdr-plugin/herdr-plugin.toml` | config | — | none in the repo (RESEARCH Pattern 2) | none |
| `herdr-plugin/run.sh` | utility (POSIX sh) | — | `scripts/capture.sh`, `spikes/lib.sh` (repo shell style only) | partial |
| `cmd/pr-relay/plugin_test.go` (new `TestPluginManifest`, `TestPluginRunSh`) | test | file-I/O | `internal/config/config_test.go` (BurntSushi decode), `harness_test.go:159-194` (exec with a controlled env) | role-match |
| `cmd/pr-relay/docs_test.go` (new `TestDocsDrift`) | test | transform | `render_test.go:88-122` (golden source), `testutil.Golden` | role-match |
| `README.md`, `LICENSE` (LICENSE text chosen at the human checkpoint) | doc | — | none | none |
| `.goreleaser.yaml` | config | batch | none (RESEARCH Code Examples) | none |
| `.github/workflows/release.yml` (new) and `ci.yml` (modify: snapshot job) | config (CI) | — | `.github/workflows/ci.yml` | exact |
| `.planning/REQUIREMENTS.md`, `.planning/ROADMAP.md` (D-25 drift fixes) | doc | — | n/a | n/a |

## Pattern Assignments

### CR-01: `internal/poller/engine.go` + `subscribe.go` (context-aware poll lock)

**Current lock** (engine.go:206-208):
```go
type Engine struct {
	mu     sync.Mutex
	pollMu sync.Mutex
```
**Lock sites to convert.** There are exactly two (from grep):
- `subscribe.go:87-88`: `e.pollMu.Lock(); defer e.pollMu.Unlock()` inside `SubscribeVerified`
- `engine.go:939-946` `pollRefs`:
```go
func (e *Engine) pollRefs(ctx context.Context, keys []string, forced bool) ([]PollResult, error) {
	e.pollMu.Lock()
	results, report, err := e.pollRefsLocked(ctx, keys, forced)
	e.pollMu.Unlock()
	if err == nil && report != nil && e.onCommit != nil {
		e.onCommit(*report)
	}
	return results, err
}
```
- Initialise `pollSem: make(chan struct{}, 1)` in `New` (the `e := &Engine{...}` literal, engine.go:254). The review's `lockPoll`/`unlockPoll` code is at 04-REVIEW.md:86-108.
- Error mapping already exists: `verifyErr(ctx, err)` (used at subscribe.go:121, 131). Reuse it for the lock-wait failure.
- **Commit site needing a `ctx` check:** `subscribeVerifiedCommit(req, mask, p)` (subscribe.go:144). It has no `ctx` parameter today, so add one, or check `ctx.Err()` in `SubscribeVerified` right before calling it at line 89 and line 96, and again inside before `e.mutateLocked` (line 160) while holding `e.mu`. The unlocked fast path at line 80 runs before any wait, so it can stay as it is.
- `PollNow` (engine.go:869) reaches the lock through `pollRefs`, so `pollRefs` must return `ctx.Err()` from `lockPoll`.

### CR-03: `subscribeIn` re-subscribe of a live sub (engine.go:439-466)

**Copy from `Keep`** (engine.go:570-578), which is the reference behaviour:
```go
sub.ClockAt = e.now()
sub.CheckinAt = time.Time{}
var kept []store.PendingEvent
for _, p := range sub.Pending {
	if p.Event.Type != model.EventCheckin {
		kept = append(kept, p)
	}
}
sub.Pending = kept
```
The helper already exists (lifecycle.go:51): `withoutCheckins(p []store.PendingEvent) []store.PendingEvent`. Insert it after the `switch sub.Ending` block (line 457) and before `sub.Events = mask`:
```go
sub.CheckinAt = time.Time{}
sub.Pending = withoutCheckins(sub.Pending)
```
**Test analog:** `TestResubRevivesExpired` (subscribe_test.go:315-356) uses `subTHarness`, `h.setSub(id, func(s *store.Subscription){...})`, `h.clk.Advance`, `h.liveSub(id)` and the assertions `!s.CheckinAt.IsZero()` and `len(s.Pending)`. Copy it with `Ending == ""` and `Pending = [{Seq: 8, comment}, {Seq: 9, EventCheckin}]`, then assert that only Seq 8 remains.

### CR-02: `internal/store/store.go` + `internal/ctl/server.go`

**Store struct** (store.go:38-43): add `mu sync.Mutex`. **Close** (275-288) already nils `s.lock`, so guard on the same field:
```go
func (s *Store) Close() error {
	if s.lock == nil {
		return nil
	}
	err := s.lock.Unlock()
	...
	s.lock = nil
```
Add `s.mu.Lock(); defer s.mu.Unlock()` to both `Close` and `Save` (221). At the top of `Save`, return `errors.New("store: save after close")`. Keep the `"store: save: "` prefix convention, because `TestStoreSaveFailureKeepsOldFile` (store_test.go:234) checks that prefix for save errors. A name such as `ErrClosed` beside `ErrLocked` (store.go:23) follows the existing sentinel style.
**Test analog:** `TestStoreLocked` (store_test.go:203-216) uses `Open(dir)`, `s1.Close()` and `errors.Is`.

**Server.Close** (server.go:128-152). Both branches call `s.cancel()`. The timeout branch returns without waiting:
```go
case <-t.C:
	s.cancel()
	return fmt.Errorf("ctl: drain timed out after %s", drain)
```
After `s.cancel()`, also `select` on `done` with a second bounded timer. **Daemon shutdown order** (daemon.go:320-336) is `srv.Close(drainTimeout)` → `<-serveErr` → `stopWork()` → `wg.Wait()` → `st.Close()` → `lk.Unlock()`. Keep this order. The fix only makes `srv.Close` return after the handlers have finished.
**Test analog:** `TestCloseDrainTimeout` (ctl_test.go:301) and `daemon_test.go` `startDaemon`/`lockFree` (126, 201).

### WR-09: `internal/daemon/daemon.go` startup ordering (lines 118-164)

Today the order is: paths (118-134) → `OpenLog(logDir)` (136-141) → `SetCrash` (142-147) → socket lock (149-164). Move block "1. Socket lock" above `OpenLog`. Before the logger exists, `log.Info("another daemon holds the socket lock; exiting", ...)` at line 161 has no logger, so either drop it (the `fail(ErrAlreadyRunning)` result reaches the ready pipe through `autostart.SignalReady`, line 111) or write it to stderr. The `undo` stack pattern (109-116) still applies: push the lock undo first.
```go
lk := flock.New(sockLock)
ok, err := lk.TryLock()
...
if !ok {
	_ = lk.Close()
	return fail(ErrAlreadyRunning)
}
undo = append(undo, func() { _ = lk.Unlock(); _ = lk.Close() })
```

### WR-03 / WR-08: `internal/autostart/autostart.go`

**Connect probe** (89-93). A timeout falls through to `reachErr` because `isDown` is false:
```go
if _, err := hello(ctx, c, probeTimeout); err == nil {
	return c, nil
} else if !isDown(err) {
	return nil, reachErr(socket, err)
}
```
For WR-08, when `errors.Is(err, context.DeadlineExceeded)` and `ctx.Err() == nil`, retry `hello(ctx, c, postSpawnTimeout)` before failing. Use the same change in `ensureUp` (109-113). The error should name the daemon log, which comes from `paths.LogDir()`.

**ensureUp after spawn** (114-120):
```go
spawnErr := spawn(ctx, o.Exe, o.Args, o.Env, o.SpawnTimeout)
if _, err := hello(ctx, c, postSpawnTimeout); err != nil {
	if spawnErr != nil {
		return spawnErr
	}
	return reachErr(socket, err)
}
```
For WR-03, when `spawnErr` carries "already running", or hello fails after a spawn, call `waitExited(ctx, socket, o.ExitTimeout)` (193-210, which already waits on `socket + ".lock"`), then `spawn` once more and hello again. Find out how `daemon.ErrAlreadyRunning` crosses the ready pipe (`ready.go`, `readReady`) before you match on it. It is probably a string, because autostart cannot import daemon.
**Test analog:** the re-exec of `fakeDaemon` in `autostart_test.go:30-94`, with `envFake*` env vars and `setup(t, cliVersion, fakeVersion)` (95). Add an env knob for the fake, for example a slow hello or a delayed lock release.

---

### `cmd/pr-relay/daemon_cmd.go` (`--ensure`, D-08)

**Analog:** the same file. The flag pattern comes from `ls.go:16-27`, and the run-func shape from `runDaemonRestart`.

**Flag on a command that has RunE** (follow `ls.go:16,26`):
```go
var all bool
...
cmd.Flags().BoolVar(&all, "all", false, "list the subscriptions of every pane")
```
Inside `newDaemonCmd`'s RunE (24-28), branch to `runEnsure(...)` when `ensure` is true, before `runDaemonForeground`.

**Testable run func with injected deps** (daemon_cmd.go:97-110). Copy this shape:
```go
func runDaemonRestart(ctx context.Context, stdout io.Writer, restart func(context.Context) error,
	hello func(context.Context) (ctl.Peer, error)) error {
	ctx, cancel := withTimeout(ctx, 2*callTimeout)
	defer cancel()
	if err := restart(ctx); err != nil {
		return err
	}
	peer, err := hello(ctx)
	...
	fmt.Fprintf(stdout, "daemon restarted (pid %d)\n", peer.PID)
	return nil
}
```
**Options builder** (64-66): `autostartOptions(cmd.ErrOrStderr())`. Reuse it. **Version gate:** `autostart.Restart` (215-239) is already a no-op when `p.Version == o.Version && p.Protocol == ctl.Protocol`. That means `--ensure` can be `autostart.Connect`, then hello, then `Restart` on a mismatch (RESEARCH Pattern 1). Alternatively, a simpler correct form is `autostart.Restart(ctx, o)` followed by `autostart.Call(ctx, o, ctl.MethodHello, nil, nil)`, because Restart spawns when nothing answers (via `stop` → `ensureUp`). Either way, no logic is duplicated (D-01).

**E2E analog:** `TestDaemonRestartCmd` (e2e_test.go:286-298) uses `e.run("", "daemon", "restart")`, checks `r.code` and `r.stdout`, then runs `status` and looks for `"daemon: running"`. For the mismatch case, copy `TestVersionMismatch` (137-160) with `binOld` (crash_test.go:43-44 builds it with `-ldflags -X main.version=0.0.1-old`). For idempotence, call `--ensure` twice and assert that `e.daemonPID()` (harness_test.go:204) is unchanged.

### `cmd/pr-relay/main.go` + `version.go` (D-05, D-18)

**Current** (main.go:12-17):
```go
// version is overridden at release time via -ldflags "-X main.version=...".
var version = "dev"

func versionString() string {
	return "pr-relay " + version
}
```
- `version` is read directly by `autostartOptions` (daemon_cmd.go:65), `daemonCaller` (cli.go:36), `daemon.Options{Version: version}` (daemon_cmd.go:26) and `TestVersionMismatch` (`"version "+version`). Resolve it once in `init()` so that every reader sees the final value.
- The daemon already maps `""` to `"dev"` (daemon.go:100-102), so it tolerates an empty value.
- **The test binary build in `crash_test.go:37` is a plain `go build` inside a git checkout.** It now stamps a VCS pseudo-version (RESEARCH Pattern 3), so `binPath` reports something like `v0.0.0-...+dirty` instead of `dev`. That is fine for the e2e tests, which compare against the in-process `version` only via `TestVersionMismatch`, where the in-process test binary resolves `(devel)` to `dev`. **Risk:** `TestVersionMismatch` line 151 checks that the `binPath` daemon status contains `"version "+version`, and in-process `version` (`dev`) may differ from `binPath`'s stamped version. Pass `-buildvcs=false` in TestMain's builds, or keep ldflags-free builds resolving to `dev`.
- **Register the command** at main.go:34-35, next to `newDaemonCmd()`.

### `cmd/pr-relay/skill_cmd.go` (D-12)

**Analog:** `reset.go` (cobra shell plus a pure `runReset` with injected io) and `ls.go` (flags).
```go
func newResetCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "reset [--yes]",
		Short: "...",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReset(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), ...)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	return cmd
}
```
- `skill` (RunE prints `skills.Snippet()`), with a subcommand `install` added through `cmd.AddCommand`, in the same way `daemon stop`/`restart` are added (daemon_cmd.go:30-60). Use the `StringVar` flag form from `ls.go:27`: `cmd.Flags().StringVar(&dir, "dir", "", "write `DIR`/pr-relay/SKILL.md instead of the defaults")`.
- Inject `home string` (or a `getenv`) into `runSkillInstall`, following the `getenv func(string) string` injection in `runLs`/`resolveTarget` (cli.go:44). That lets tests use `t.TempDir()` and never touch the real `~/.claude`.
- **Atomic write:** copy the sequence from `store.Save` (store.go:227-250): `os.CreateTemp(dir, pattern)` → `defer os.Remove(tmp)` → `Chmod` → `Write` → `Sync` → `Close` → `os.Rename`. Use mode 0644 here instead of 0600. Before the rename, `os.Lstat` the target and refuse a symlink (the `os.Lstat` style matches store.go:202 and daemon.go:225-227: `fi.Mode()&os.ModeSymlink`).
- Output: one terse line per path, matching `fmt.Fprintf(stdout, "removed %d %s\n", ...)` (reset.go:64).

### `skills/embed.go` (D-12)

**Analog:** `internal/ghquery/query.go:13-16`:
```go
//go:embed pr.graphql
var Query string
```
This needs `import _ "embed"`. Use `var SKILL string` or `[]byte`. `skills/` is a new top-level package, so its import path is `github.com/drodimin/pr-relay/skills`.

### `cmd/pr-relay/docs_test.go` (D-24) and `skills/skills_test.go`

**Golden sources** (render_test.go:97, 122):
```go
testutil.Golden(t, "testdata/lines.golden", []byte(strings.Join(lines, "\n")+"\n"))
testutil.Golden(t, "testdata/lifecycle.golden", []byte(strings.Join(lines, "\n")+"\n"))
```
These are bare `[pr-relay] ...` lines, one per line, for example `[pr-relay] octo/repo#7 ci.failed: lint https://github.com/octo/repo/actions/runs/1/job/11` and `[pr-relay] o/r#12 expired: no activity for 7d, subscription removed. re-subscribe: pr-relay sub https://github.com/o/r/pull/12`. From `cmd/pr-relay`, read them with `os.ReadFile("../../internal/render/testdata/lines.golden")`. Test cwd is the package dir, the same convention `testutil.Golden` relies on (golden.go:15-17).
**Event set:** `model.AllEventTypes()` (model.go:281-282) plus `model.EventCheckin`/`EventExpired` (289-290).
**Command tree:** `newRootCmd()` (main.go:19). Skip `Hidden` commands, because `newDevCmd()` is the hidden one. Also skip `help` and `completion`.
**Config keys for the README:** use the `toml:"..."` tags in `internal/config/config.go:143-177` (`poll.min_interval`, `max_interval`, `max_stretched_interval`, `budget_pct`, `low_budget_threshold`, `batch_size`, `subscriptions.pending_cap`, `delivery.quiet_period`, `lifecycle.checkin_after`, `expire_after`). A drift test can reflect over `config.Config` tags.

### `cmd/pr-relay/plugin_test.go`

- Manifest: `toml.DecodeFile("../../herdr-plugin/herdr-plugin.toml", &m)`, with BurntSushi as used in `internal/config/config.go` (`config.Load`). Assert `min_herdr_version`, `platforms == [linux macos]`, the startup command, four actions and ids without dots.
- run.sh: `exec.Command("sh", "../../herdr-plugin/run.sh", "status")` with `cmd.Env = []string{"PATH=" + tmp}`. Follow the env-scrubbing discipline of `harness_test.go:114-140` (`e.env`), and never inherit `HERDR_*` (T-04-48). Use a fake `pr-relay` shell script in `tmp` that echoes its args to check pass-through. With an empty PATH dir, expect exit 0 and the D-10 message on stderr. Note that `sh` itself must still resolve, so use `/bin/sh` and an absolute path.

### `.github/workflows/release.yml` and the `ci.yml` snapshot job

**Analog:** `.github/workflows/ci.yml`. Pin the same action majors:
```yaml
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
```
The `permissions:` block sits at the top level (`contents: read` in ci.yml, `contents: write` in release.yml). The `lint` job (36-45) is the model for a single-OS extra job such as `goreleaser` snapshot. Add `fetch-depth: 0` to checkout. `.gitignore` already ignores `/dist/`.

## Shared Patterns

### Testable command shape
**Source:** `cmd/pr-relay/status.go:13-35`, `reset.go:17-66`
**Apply to:** `skill_cmd.go`, the `--ensure` path
The cobra `RunE` is a one-line forward to `runX(ctx, stdout, [stderr], deps..., flags...)`. Deps are injected functions (`caller`, `getenv`, or closures over `autostart`). Every daemon round trip is bounded with `withTimeout(ctx, callTimeout)` (cli.go:73).

### Error output
**Source:** `main.go:40-44`, `cli.go:59-70`
**Apply to:** all new commands
Return a plain `error`. `main` prints `pr-relay: <msg>` and exits 1. Daemon wire errors are unwrapped by `cliError`. Never print and return at the same time.

### Atomic file write
**Source:** `internal/store/store.go:221-263`
**Apply to:** `skill install`
Write a temp file in the same dir, then fsync, rename, and fsync the dir. Errors are prefixed `"<pkg>: <op>: <step>: %w"`.

### Concurrency primitives
**Source:** `gofrs/flock` with `TryLockContext(ctx, lockRetry)` (autostart.go:193-210, 241-271)
**Apply to:** WR-03 wait, CR-01 semaphore (a channel, not flock)
Each bounded wait derives `context.WithTimeout(ctx, timeout)` and distinguishes `ctx.Err()` (the caller's deadline) from its own timeout.

### Test isolation
**Source:** `cmd/pr-relay/harness_test.go:46-140`, `crash_test.go:30-53`
**Apply to:** `TestDaemonEnsure`, plugin run.sh test
Use a short `os.MkdirTemp("", "prr")` socket dir for the 104-byte sun_path limit, `PR_RELAY_*` env redirection, an env scrubbed of `HERDR_*`/`GH_*`/`GITHUB_*`/`XDG_RUNTIME_DIR`, and a `t.Cleanup` that runs `daemon stop` and SIGKILLs a leftover pid.

### Golden files
**Source:** `internal/testutil/golden.go:19-37` (with the `-update` flag)
**Apply to:** any new formatted output (for example the `--ensure` line, if it is goldened)

## No Analog Found

| File | Role | Data Flow | Reason |
|------|------|-----------|--------|
| `herdr-plugin/herdr-plugin.toml` | config | — | This is the first herdr plugin. Use RESEARCH Pattern 2, which was verified against the herdr 0.9.5 schema |
| `herdr-plugin/run.sh` | utility | — | No PATH-guard wrappers exist. Use the RESEARCH Pattern 2 script, and match `scripts/*.sh` only for style |
| `.goreleaser.yaml` | config | batch | No release config exists. Use the RESEARCH Code Examples (v2 syntax: `formats`, `binaries`, `install_steps`, `homebrew_casks`) |
| `README.md` | doc | — | No README exists. Follow the D-23 section order, with examples copied from the goldens |
| `LICENSE` | doc | — | The choice is deferred to the release human checkpoint. Do not pre-pick one (CONTEXT Discretion) |

## Metadata

**Analog search scope:** `cmd/pr-relay/`, `internal/{autostart,store,poller,ctl,daemon,ghquery,render,testutil,config,paths,model}/`, `.github/workflows/`
**Files scanned:** about 30 read or grepped
**Pattern extraction date:** 2026-11-05
