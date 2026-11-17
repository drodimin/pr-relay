---
phase: quick-261116-qss
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - cmd/pr-relay/daemon_cmd.go
  - cmd/pr-relay/keep.go
  - cmd/pr-relay/ls.go
  - cmd/pr-relay/poll.go
  - cmd/pr-relay/skill_cmd.go
  - internal/autostart/autostart.go
  - internal/autostart/ready.go
  - internal/autostart/spawn_unix.go
  - internal/ctl/client.go
  - internal/ctl/server.go
  - internal/daemon/daemon.go
  - internal/herdr/client.go
  - internal/herdr/endpoint.go
  - internal/herdr/stream.go
  - internal/config/config.go
  - internal/ghclient/token.go
  - internal/ctl/ctl_test.go
  - internal/daemon/daemon_test.go
  - internal/herdr/client_test.go
  - internal/herdr/herdrtest/server_test.go
  - internal/paths/paths_test.go
  - internal/poller/engine_test.go
autonomous: true
requirements: [QUICK-LINT-01]

must_haves:
  truths:
    - "`golangci-lint run ./...` with golangci-lint v2.15.0 reports 0 issues against the unchanged .golangci.yml"
    - "`go test -race -count=1 ./...` passes"
    - "`gofmt -l .` prints nothing"
    - "No linter is disabled or excluded and no `//nolint` directive is added"
    - "Runtime behaviour is unchanged: same output, same error returns, same exit codes"
  artifacts:
    - path: "$(go env GOPATH)/bin/golangci-lint"
      provides: "Local golangci-lint v2.15.0 (same version CI pins)"
    - path: "cmd/pr-relay/skill_cmd.go"
      provides: "Atomic SKILL.md install with every Close on the temp file handled"
  key_links:
    - from: ".golangci.yml"
      to: "golangci-lint v2.15.0"
      via: "unchanged config (version 2, linters.default standard, gofmt)"
      pattern: "default: standard"
---

<objective>
Clear every golangci-lint v2.15.0 issue that failed CI run 43985717305 on main and blocks the v0.1.0 tag, by fixing the code rather than the lint config.

Purpose: green CI on main so v0.1.0 can be tagged (tagging, pushing and releasing are NOT part of this plan).
Output: lint-clean code, three atomic commits.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@./CLAUDE.md
@.golangci.yml
@.planning/quick/261116-qss-fix-golangci-lint-issues-blocking-v0-1-0/lint-issues.txt

Notes from planning (verified against the working tree):

- lint-issues.txt lists 38 errcheck and 2 staticcheck QF1008 (config.go:138 and ghclient/token.go:100), 40 in total. The task description says 41. **The local golangci-lint v2.15.0 output from Task 1 is the source of truth.** Fix every issue it reports, including any that are missing from the file.
- Two listed locations do not match the current source. internal/daemon/daemon.go:327:2 is `<-serveErr`, and internal/daemon/daemon_test.go:145:8 is a `case <-d.done:` line. Neither is a call. Do not guess: use the locations the local run reports.
- Already correct, so leave them alone: skill_cmd.go's final `if err := f.Close(); err != nil { return ... }` before `os.Rename` is already checked and returned. The flagged closes at skill_cmd.go:101 and :105 sit on early-error paths (chmod/write failure) where an error is already being returned.
- internal/autostart/ready.go `SignalReady(startErr error)` returns nothing and already ignores the write error with `_, _ = io.WriteString(f, msg)`. The parent's `readReady` deadline or EOF handling already detects a failed signal. Changing the signature would change behaviour, so explicitly ignore the close error here.
- The repo idiom for explicit ignores already exists: `_ = lk.Close()`, `defer func() { _ = crash.Close() }()` (internal/daemon/daemon.go:153, :166). Use the same forms.
- Do not stage `.planning/config.json` (a pre-existing modification) or `BRIEF.md` (untracked). Stage files by explicit path only, never `git add -A` or `git add .`.
</context>

<tasks>

<task type="auto">
  <name>Task 1: Install golangci-lint v2.15.0 locally and reproduce the issue list</name>
  <files>(no repo files; installs into $(go env GOPATH)/bin)</files>
  <action>
Install the exact CI version with `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0`. The binary lands in `$(go env GOPATH)/bin`, which is currently /Users/drodi/go/bin and does not exist yet. If `go install` fails, use the official install script from golangci-lint's GitHub releases with `-b "$(go env GOPATH)/bin" v2.15.0`. Call the binary by its absolute path `"$(go env GOPATH)/bin/golangci-lint"`, because that directory may not be on PATH. Confirm that `golangci-lint version` reports 2.15.0.

From the repo root, run `"$(go env GOPATH)/bin/golangci-lint" run ./...` and save the output to the scratchpad. Use `--max-issues-per-linter=0 --max-same-issues=0` so nothing is truncated. Compare the output with lint-issues.txt and record the real count and any differences, such as the daemon.go:327 and daemon_test.go:145 locations or a 41st issue, for the SUMMARY.

Do not change .golangci.yml or .github/workflows/ci.yml. There is nothing to commit for this task. Its evidence goes in the SUMMARY.
  </action>
  <verify>
    <automated>"$(go env GOPATH)/bin/golangci-lint" version | grep -q '2\.15\.0' && ("$(go env GOPATH)/bin/golangci-lint" run --max-issues-per-linter=0 --max-same-issues=0 ./... ; true)</automated>
  </verify>
  <done>golangci-lint 2.15.0 runs locally and reports a non-zero issue list that matches CI (any differences are noted).</done>
</task>

<task type="auto">
  <name>Task 2: Fix errcheck and staticcheck issues in non-test code</name>
  <files>cmd/pr-relay/daemon_cmd.go, cmd/pr-relay/keep.go, cmd/pr-relay/ls.go, cmd/pr-relay/poll.go, cmd/pr-relay/skill_cmd.go, internal/autostart/autostart.go, internal/autostart/ready.go, internal/autostart/spawn_unix.go, internal/ctl/client.go, internal/ctl/server.go, internal/daemon/daemon.go, internal/herdr/client.go, internal/herdr/endpoint.go, internal/herdr/stream.go, internal/config/config.go, internal/ghclient/token.go</files>
  <action>
Fix every issue the Task 1 run reports in non-`_test.go` files. Apply the following rules and do not change behaviour.

1. Write paths where a failed Close means an incomplete write must be checked. In cmd/pr-relay/skill_cmd.go the success-path `f.Close()` before `os.Rename` is already checked and returned, so keep it. At the two early-error-path closes (chmod failure around line 101, write failure around line 105), an error is already being returned, so change `f.Close()` to `_ = f.Close()`. Do not wrap or join the close error, because that would change the returned message. Change `defer os.Remove(tmp)` to `defer func() { _ = os.Remove(tmp) }()`. This is best-effort cleanup, and after a successful rename it fails with ENOENT by design. Change `defer d.Close()` on the read-only directory handle to `defer func() { _ = d.Close() }()`. The directory fsync is checked separately and that is what matters.
2. In internal/autostart/ready.go, change `defer f.Close()` in `SignalReady` to `defer func() { _ = f.Close() }()`. The function has no error return and already ignores the write error, and the reader detects failure through its deadline or EOF (see Context). Do not change the signature.
3. Best-effort Close of connections, locks, devnull, pipes and streams becomes `_ = x.Close()` inline or `defer func() { _ = x.Close() }()` when deferred. This covers autostart.go `lk.Close` (waitExited), spawn_unix.go `devnull.Close` and `r.Close`, ctl/client.go `conn.Close`, ctl/server.go `c.Close`, herdr/client.go `conn.Close`, herdr/endpoint.go `stream.Close`, and herdr/stream.go `s.Close()` at both call sites in `Next`. In stream.go the error being returned is the original `UnreachableError`/`sl.Error`, so keep it exactly as is.
4. CLI output to cobra writers uses the minimal `_, _ = fmt.Fprint...(...)` form. This covers daemon_cmd.go (Fprintln at about lines 89, 104, 106 and Fprintf at about 123, 140), keep.go:46, ls.go:52, poll.go:38, and autostart.go:178 (the `fmt.Fprintf(o.Stderr, "restarted daemon ...")` call). Do not turn these into returned errors, which matches the user's instruction to prefer `_, _ =`.
5. internal/daemon/daemon.go: fix whatever unchecked call the local run reports, using rule 3 or 4 according to its kind. If the reported line is the same unnamed "Error return value is not checked" at 327:2, inspect the expression at that exact position and wrap it with `_ =` or `defer func() { _ = ... }()` as appropriate.
6. staticcheck QF1008: in internal/config/config.go `Duration.MarshalText`, change `d.Duration.String()` to `d.String()`. Duration embeds time.Duration and does not define its own String method, so confirm with grep that no `func (d Duration) String` exists before changing it, because that would cause recursion. In internal/ghclient/token.go `sanitizeExecErr`, change `ee.ProcessState.String()` to `ee.String()`. exec.ExitError embeds *os.ProcessState and its promoted String method is the same call.

Do not add `//nolint` comments and do not edit .golangci.yml. Run `gofmt -w` on the touched files. Commit only these paths with the message `fix(quick-261116-qss): handle unchecked errors in non-test code for golangci-lint` and the Co-Authored-By trailer.
  </action>
  <verify>
    <automated>cd /Users/drodi/repos/pr-relay && go build ./... && go vet ./... && "$(go env GOPATH)/bin/golangci-lint" run --max-issues-per-linter=0 --max-same-issues=0 ./... 2>&1 | grep -E '^[^ ]+\.go:' | grep -v '_test\.go:' | wc -l | grep -qx '[[:space:]]*0' && ! grep -rn 'nolint' --include='*.go' cmd internal</automated>
  </verify>
  <done>No remaining lint issues in non-test files. Build and vet pass. No nolint directives. The commit contains only Go source files from this task.</done>
</task>

<task type="auto">
  <name>Task 3: Fix test-file issues and run the full gate (lint 0, race tests, gofmt)</name>
  <files>internal/ctl/ctl_test.go, internal/daemon/daemon_test.go, internal/herdr/client_test.go, internal/herdr/herdrtest/server_test.go, internal/paths/paths_test.go, internal/poller/engine_test.go</files>
  <action>
Fix every remaining issue that golangci-lint reports in `_test.go` files, explicitly ignoring the errors in test cleanup.
- `defer x.Close()` becomes `defer func() { _ = x.Close() }()`. This covers ctl_test.go:189 and :254 `client.Close`, daemon_test.go:352 `st.Close`, client_test.go:359 and :387 `server.Close`, server_test.go:30 `c.Close`, and engine_test.go:842 `st2.Close`.
- A bare `x.Close()` statement becomes `_ = x.Close()`. This covers ctl_test.go:246 `client.Close`, ctl_test.go:430 `ln.Close`, client_test.go:321 `conn.Close`, and server_test.go:260 `pc.Close`.
- `t.Cleanup(func() { os.RemoveAll(dir) })` becomes `t.Cleanup(func() { _ = os.RemoveAll(dir) })` at ctl_test.go:367 and paths_test.go:163.
- daemon_test.go: fix the issue at the location the local run reports (the listed 145:8 does not match a call in the current source).
Do not change test assertions or logic.

Then run the full gate from the repo root: `"$(go env GOPATH)/bin/golangci-lint" run ./...` must report 0 issues, `go test -race -count=1 ./...` must pass, and `gofmt -l .` must print nothing. If lint still reports anything, including in non-test files, fix it here with the same rules as Task 2. Commit only the touched test files (plus any stragglers) with the message `test(quick-261116-qss): handle unchecked errors in tests for golangci-lint` and the Co-Authored-By trailer. Confirm with `git status --short` that `.planning/config.json` is still modified and unstaged and `BRIEF.md` is still untracked. Do not push or tag.
  </action>
  <verify>
    <automated>cd /Users/drodi/repos/pr-relay && "$(go env GOPATH)/bin/golangci-lint" run ./... && test -z "$(gofmt -l .)" && go test -race -count=1 ./... && git diff --quiet HEAD -- .golangci.yml .github/workflows/ci.yml</automated>
  </verify>
  <done>golangci-lint reports "0 issues.", race tests pass, gofmt is clean, the lint config and CI workflow are unchanged, config.json and BRIEF.md are not committed, and nothing is pushed or tagged.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| Go module proxy to local machine | golangci-lint v2.15.0 is downloaded and built as a dev tool |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-q-SC | Tampering | `go install golangci-lint@v2.15.0` | mitigate | Exact version pinned to match CI, fetched through the Go module proxy with checksum DB verification (sumdb). It is a dev tool only and not added to go.mod. |
| T-q-01 | Tampering | skill_cmd.go atomic install | mitigate | Keep the checked success-path Close before Rename so a truncated SKILL.md is never renamed into place. |
| T-q-02 | Repudiation | error-path Close ignores | accept | These Closes run only on paths that already return an error, so ignoring the close error loses no signal. |
</threat_model>

<verification>
- `"$(go env GOPATH)/bin/golangci-lint" run ./...` prints `0 issues.`
- `go test -race -count=1 ./...` passes
- `gofmt -l .` is empty
- `grep -rn nolint --include='*.go' .` shows no new directives, and .golangci.yml is unchanged
- `git log -2 --stat` shows only Go files, and `git status` still shows .planning/config.json modified and BRIEF.md untracked
</verification>

<success_criteria>
All golangci-lint v2.15.0 issues from CI run 43985717305 are resolved in code with no behaviour change, lint is verifiable locally, and main is ready for CI to go green ahead of the v0.1.0 tag (not tagged here).
</success_criteria>

<output>
Create `.planning/quick/261116-qss-fix-golangci-lint-issues-blocking-v0-1-0/261116-qss-SUMMARY.md` when done. Include the real issue count from the local run and any differences from lint-issues.txt.
</output>
