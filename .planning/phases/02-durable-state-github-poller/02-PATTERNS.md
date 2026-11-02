# Phase 2: Durable State & GitHub Poller - Pattern Map

**Mapped:** 2026-11-01
**Files analyzed:** 38 new or modified files (grouped by package below)
**Analogs found:** 27 / 38 (11 have only a partial analog or none and should use the RESEARCH.md patterns)

The repo has just two kinds of code today: a pure core from Phase 1 (`internal/{model,diff,filter,ghquery,render,sanitize,testutil}`) and a version-only `cmd/pr-relay/main.go`. Nothing does network I/O, holds persistent state, runs a scheduler or uses cobra yet. That gives two outcomes:
- Modifications to the Phase 1 packages and the new pure helpers (`ghquery` batch, `poller/cadence.go`, `poller/budget.go`, `poller/fanout.go`) have **exact** analogs and must copy Phase 1 conventions closely.
- The new I/O shells (`store`, `ghclient`, `poller/engine.go`, the cobra wiring) have **partial** analogs at best. Take the shape from RESEARCH.md Patterns 1-8 and the conventions (doc comments, error wrapping, file modes, test helpers) from the excerpts below.

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|---|---|---|---|---|
| `go.mod` (mod) | config | — | `go.mod` (`module github.com/drodimin/pr-relay`, `go 1.26`) | exact (bump to 1.26.7 via tidy, pitfall 9) |
| `internal/model/model.go` (mod: json tags, WR-07, watermark/epoch fields) | model | transform | itself, lines 16-82, 203-277 | exact |
| `internal/model/model_test.go` (mod) | test | — | itself, `roundTrip` lines 139-181 | exact |
| `internal/diff/lifecycle.go` (mod: WR-01, WR-02, WR-03) | service (pure) | transform | itself, lines 18-99 | exact |
| `internal/diff/activity.go` (mod: CR-01, WR-06) | service (pure) | transform | itself, lines 33-96 | exact |
| `internal/diff/ci.go` + `internal/ghquery/decode.go` `decodeChecks` (mod: WR-05) | service (pure) | transform | `decode.go` lines 288-350 | exact |
| `internal/diff/*_test.go` (new tests: `TestEpochKeys`, `TestWatermark`, ...) | test | — | `internal/diff/diff_test.go`, `snap_test.go` builders | exact |
| `internal/diff/testdata/scenarios/{delete-comment-31,reopen-close,big-push-30,parallel-trigger,review-comments-31}/` | test fixture | — | `testdata/scenarios/synthetic-*` + `scenario_test.go` | exact |
| `internal/sanitize/sanitize.go` (mod: CR-02 NFKC fold) | utility (pure) | transform | itself, `NeutralizeTag` lines 130-159 | exact |
| `internal/testutil/hostile.go` (mod: lookalike tags, IN-01 fillers) | test util | — | itself, lines 12-70 | exact |
| `internal/render/render.go` `fits` (mod: fold before tag count) | utility (pure) | transform | itself, lines 140-143 | exact |
| `internal/ghquery/pr.graphql` (mod: `fragment PRFields` + window vars) | config/query | request-response | itself | exact |
| `internal/ghquery/query.go` (mod: embed batch builder input) | utility | — | itself (11 lines, `//go:embed`) | exact |
| `internal/ghquery/batch.go` (new: `BuildBatch`, `DecodeBatch`) | utility (pure) | transform | `internal/ghquery/decode.go` | exact |
| `internal/ghquery/batch_test.go` + `testdata/batch-*.json` | test | — | `decode_test.go` lines 1-60, `fixtures_test.go` | exact |
| `scripts/capture.sh` (mod: batch capture) | script | request-response | itself | exact |
| `internal/paths/paths.go` (+ `_test.go`) | utility | file-I/O | `spikes/setsid-probe/main.go` `outputDir` lines 51-66 | partial |
| `internal/config/config.go` (+ `_test.go`) | config | file-I/O | none (no TOML yet); conventions from `model` | none |
| `internal/store/state.go` (State, PRRecord, Subscription, PendingEvent, Clone) | model | CRUD | `model.Memory.Clone` lines 261-277 | role-match |
| `internal/store/store.go` (Open+flock, Load+quarantine, Save atomic) | store | file-I/O | `internal/testutil/golden.go` (file I/O), setsid-probe `OpenFile` 0o600 | partial |
| `internal/store/store_test.go` + `testdata/state-v1.json` | test | — | `model_test.go` `roundTrip`; `testutil.Golden` | role-match |
| `internal/ghclient/token.go` (TokenSource) | service | request-response (exec) | setsid-probe `exec.Command` lines 86-97 | partial |
| `internal/ghclient/client.go` (Batch, http.Client) | service | request-response | none (first network code) | none |
| `internal/ghclient/errors.go` (typed errors / classification) | utility | — | `ghquery.Decode` error wrapping lines 189-214 | partial |
| `internal/ghclient/*_test.go` (httptest) | test | — | none (first httptest) | none |
| `internal/poller/cadence.go` (pure) | utility (pure) | transform | `diff/ci.go` small pure helpers; `filter.go` doc-comment style | role-match |
| `internal/poller/budget.go` (pure) | utility (pure) | transform | same as cadence | role-match |
| `internal/poller/fanout.go` (mask, self-filter, cap 200) | service (pure) | transform | `internal/filter/filter.go` | exact |
| `internal/poller/engine.go` (Engine, Subscribe, PollNow, Ack, ...) | service | CRUD + event-driven | `diff/scenario_test.go` `runScenario` lines 78-123 (the pipeline it wraps) | partial |
| `internal/poller/scheduler.go` (Run loop) | service | event-driven (timer) | setsid-probe `runChild` ticker loop lines 100-123 | partial |
| `internal/poller/fakegh_test.go` (RoundTripper fake) | test util | request-response | none | none |
| `internal/poller/*_test.go` (synctest) | test | — | `diff_test.go` style; no synctest yet | partial |
| `cmd/pr-relay/main.go` (mod: cobra root + version) | controller (CLI) | request-response | itself, lines 1-16 | exact |
| `cmd/pr-relay/main_test.go` (keep `TestVersionString`) | test | — | itself | exact |
| `cmd/pr-relay/dev.go` (hidden `dev watch`) | controller (CLI) | streaming | none (first cobra subcommand) | none |
| `cmd/pr-relay/crash_test.go` (TestMain build + SIGKILL loop) | test (e2e) | batch | setsid-probe `spawn` lines 68-98 (subprocess) | partial |
| `.github/workflows/ci.yml` (likely unchanged) | config | — | itself (`go test -race -count=1 ./...`) | exact |

## Pattern Assignments

### `internal/model/model.go` (model, modified)

**Analog:** itself.

**Package doc / purity contract** (lines 1-4). Keep it. `model` stays stdlib-only, so the json tags add no imports:
```go
// Package model defines the pure data contract shared by the decoder, the
// diff engine, the filter and the renderer. It performs no I/O and depends on
// the standard library only.
package model
```

**WR-07 hook.** `Valid` (lines 31-34) is the single validation gate, and `nameRE` (line 14) currently accepts `.` and `..`. Add the rejection here so `ParsePRURL` (which calls `Valid` at line 78) and every persistence caller inherit it:
```go
var nameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
func (r PRRef) Valid() bool {
	return nameRE.MatchString(r.Owner) && nameRE.MatchString(r.Repo) && r.Number > 0
}
```
Put the new reject cases in the `reject` slice of `TestParsePRURL` (model_test.go line ~31), e.g. `"https://github.com/../r/pull/1"`, `"https://github.com/o/./pull/1"`.

**JSON tags.** `Memory` (lines 244-259) and `Event` (lines 204-241) have no tags today. Add explicit snake_case tags (RESEARCH Pattern 2). Keep `Clone` (lines 261-277) in sync whenever a map field is added, e.g. watermark or epoch fields:
```go
func (m Memory) Clone() Memory {
	c := m
	c.FailedReported = cloneSet(m.FailedReported)
	c.SeenReviews = cloneSet(m.SeenReviews)
	c.SeenComments = cloneSet(m.SeenComments)
	c.SeenForcePushes = cloneSet(m.SeenForcePushes)
	return c
}
```
New scalar Memory fields (CR-01 `CommentWatermark`/`ReviewWatermark time.Time`, WR-02 `Epoch int`) need no Clone change. New `Snapshot` fields (`CreatedAt` on `IssueComment`/`ReviewComment`, `SubmittedAt` on `Review`, `ReopenedBy`) follow the existing comment style, which documents the wire origin on the right.

**Test pattern:** extend `TestJSONRoundTrip` (model_test.go lines 154-181) with every new field set, using the generic helper:
```go
func roundTrip[T any](t *testing.T, v T) {
	t.Helper()
	b, err := json.Marshal(v)
	...
	if !reflect.DeepEqual(got, v) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, v)
	}
}
```
`TestMemoryCloneDeep` (line 102) must cover any new map field.

---

### `internal/diff/lifecycle.go`, `activity.go`, `ci.go` (pure service, modified for D-04)

**Analog:** themselves. The rules are a pure function with no I/O and no clock, operating on `next := m.Clone()` passed as `*model.Memory` (diff.go lines 56-70):
```go
func Step(m model.Memory, s model.Snapshot) Result {
	if !m.Baselined {
		return Result{Memory: Baseline(s)}
	}
	next := m.Clone()
	var events []model.Event
	events = append(events, stepLifecycle(&next, s)...)
	events = append(events, stepCI(&next, s)...)
	ev, gap := stepActivity(&next, s)
	events = append(events, ev...)
	sortEvents(events)
	return Result{Events: events, Memory: next, Gap: gap}
}
```
Any new watermark or epoch initialisation must also go into `Baseline` (diff.go lines 24-54), which builds from `model.Memory{}.Clone()`.

**WR-02 epoch keys.** These key literals are what change. Prefix them with the epoch:
- lifecycle.go line 23: `Key: "closed:" + s.State`
- lifecycle.go line 41: `Key: "conflict:" + s.HeadOID + ":" + s.BaseOID + ":" + s.Mergeable`
- lifecycle.go line 64: `Key: "commits:" + s.HeadOID`
- activity.go `"review:" + r.ID`, `"comment:" + c.ID` (line 81)

**WR-01 site** (lifecycle.go lines 68-86). Today `idx < 0` (old head beyond the `commits(last:30)` window) is labelled a force-push. Use `s.CommitsTotal` against the Memory's prior total to tell "pushed more than 30" apart from "rewritten":
```go
		if newPush != nil {
			e.ForcePushed = true
			e.Actor = newPush.Actor
		} else if idx < 0 {
			// Old head no longer in history: treat as a force-push ...
			e.ForcePushed = true
		}
```

**WR-03 site:** lifecycle.go lines 21-30 (`closed` actor), plus decode.go timeline switch lines 268-282 (add `case "ReopenedEvent":`, and add `ReopenedEvent` to the `timelineItems(itemTypes:...)` in pr.graphql).

**CR-01 / WR-06 site:** activity.go gap check lines 88-90:
```go
	if s.ReviewsTotal-m.ReviewsTotal > newReviews || s.CommentsTotal-m.CommentsTotal > newComments {
		gap = true
	}
```
The watermark check goes where each unseen ID is accepted (activity.go `newComments++` near line 77, and the review loop at lines 33-60). Use strict `<` (pitfall 4) and skip PENDING reviews for the watermark. WR-06 adds a per-review `r.CommentsTotal > len(r.Comments)` gap condition.

**WR-05 site:** decode.go lines 327-334 (Key construction plus newest-databaseId collapse):
```go
		if cr.Workflow != "" {
			cr.Key = cr.Workflow + "/" + cr.Name
		} else {
			cr.Key = cr.App + "/" + cr.Name
		}
		if prev, ok := newest[cr.Key]; !ok || cr.DatabaseID > prev.DatabaseID {
			newest[cr.Key] = cr
		}
```
Add `workflowRun { event }` to the wire struct (decode.go lines 139-144) and the query, and always suffix `@event` for Actions runs (pitfall 12). Then regenerate the scenario goldens and confirm `lines.golden` text is unchanged.

**Unit test pattern** (diff_test.go lines 39-56, functional-option snapshot builders from snap_test.go):
```go
func TestStepNoMutate(t *testing.T) {
	s1 := snap(withCommits("h1"), withCheck("ci/test", "IN_PROGRESS", "", 1))
	m := Baseline(s1)
	before := m.Clone()
	s2 := snap(withHead("h2"), withCommits("h1", "h2"), ...)
	r := Step(m, s2)
	...
	if !reflect.DeepEqual(m, before) {
		t.Fatalf("Step mutated input memory:\n got %+v\nwant %+v", m, before)
	}
}
```
Add builders such as `withCommentAt(id, login, ts)` next to `withIssueComment` (activity_test.go line 36), in the same `func(*model.Snapshot)` option style.

**Scenario pattern:** new directories under `internal/diff/testdata/scenarios/` with `00-baseline.json ... NN-*.json` and `expected.golden`. `TestScenarios` (scenario_test.go lines 146-167) picks them up automatically. Add per-scenario assertions to the `scenarioChecks` map (invariants_test.go line 101). Synthetic scenarios must include their own settle repeat (see the scenario_test.go comment, lines 71-77).

---

### `internal/sanitize/sanitize.go` (CR-02), `internal/testutil/hostile.go`, `internal/render/render.go` `fits`

**Analog:** themselves.

**Package-doc convention.** Every resolved discretion choice is listed in the package doc (sanitize.go lines 7-21). Add the NFKC + skeleton fold and the IN-01 filler strip set as new bullets there.

**Fold site.** `NeutralizeTag` (sanitize.go lines 130-159) matches per rune with `unicode.ToLower(rs[i+j]) != want` over `tagRunes`. CR-02 should compare against `fold(rs[i+j])`. Keep it **length-preserving and idempotent**, and keep replacing `rs[i]='('` and the last rune with `')'`. NFKC can change the rune count, so map rune-by-rune (`norm.NFKC.String(string(r))` per rune, then the skeleton) rather than folding the whole string first.
```go
	for i := 0; i+len(tagRunes) <= len(rs); i++ {
		match := true
		for j, want := range tagRunes {
			if unicode.ToLower(rs[i+j]) != want {
```
The early exit `if !strings.ContainsRune(s, '[')` (line 135) must widen to the bracket lookalikes (`［`, U+FF3B, etc.), or be removed.

**render guard** (render.go lines 140-143). Apply the same fold before counting the tag:
```go
func fits(l string) bool {
	return len(l) <= MaxLineBytes && strings.Count(strings.ToLower(l), tag) == 1
}
```

**Hostile corpus** (testutil/hostile.go lines 12-70). Append entries such as `"［pr-relay］"` and `"[рr-relay]"` (Cyrillic р), plus the U+3164/U+115F/U+1160/U+FFA0/U+2800 fillers. The `init()` (lines 65-70) auto-adds the embedded variants. `checkProps` in sanitize/hostile_test.go (lines 13-38) already asserts `!strings.Contains(strings.ToLower(out), "[pr-relay]")`. Strengthen it to check `fold(lower(out))`.

---

### `internal/ghquery/batch.go` (new, pure transform) + `pr.graphql` + `query.go`

**Analog:** `internal/ghquery/decode.go` (exact role).

**Imports** (decode.go lines 3-11):
```go
import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/drodimin/pr-relay/internal/model"
)
```

**Embed pattern** (query.go). Keep one file shared with capture.sh:
```go
// Query is the PRState GraphQL operation. scripts/capture.sh sends the same
// file, so captured fixtures always match the production query.
//
//go:embed pr.graphql
var Query string
```
`BuildBatch` should extract the `fragment PRFields ...` text from this embedded document (or embed a second `fragment.graphql`) and emit the aliased `query PRBatch(...)` from RESEARCH Pattern 7, with variables `$o0,$r0,$n0`, never interpolated values.

**Wire-struct pattern** (decode.go lines 54-65). Private wire types with an `Errors` slice. The batch variant needs `path` on each error:
```go
type wireResponse struct {
	Data *struct {
		RateLimit  *wireRateLimit `json:"rateLimit"`
		Repository *struct {
			PullRequest *wirePR `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"errors"`
}
```
For batch: `Data map[string]json.RawMessage`. Decode `rateLimit` and `viewer` explicitly, and decode each `prN` into `struct{ PullRequest *wirePR }`. Then **refactor** the body of `Decode` from line 211 (`pr := resp.Data.Repository.PullRequest`) through 283 into `snapshotFromWire(pr *wirePR) (model.Snapshot, error)`, so single and batch decoding share it. `Decode` must keep its signature, because `diff/scenario_test.go` line 94 and `fixtures_test.go` depend on it.

**Error-wrapping convention** (decode.go lines 189-214). Prefix every error with `ghquery:`, join GraphQL errors as `Type: Message`, and wrap with `%w`:
```go
	if err := json.Unmarshal(data, &resp); err != nil {
		return model.Snapshot{}, RateLimit{}, fmt.Errorf("ghquery: decode response: %w", err)
	}
	...
			if e.Type != "" {
				msgs = append(msgs, e.Type+": "+e.Message)
	...
		return model.Snapshot{}, rl, fmt.Errorf("ghquery: graphql errors: %s", strings.Join(msgs, "; "))
```
WR-04 changes the policy, though not the style. An error whose `path[0]` is an alias belongs to that alias, and a deeper path with data present is a warning. Only errors with no path, or `RATE_LIMITED`, are batch-level. Expose typed sentinels (e.g. `var ErrNotFound = errors.New("ghquery: pull request not found")`) for the engine's `LastError`.

**RateLimit type** (decode.go lines 13-21) is reused as-is. Its `ResetAt` is a `string`, so the poller must `time.Parse(time.RFC3339, ...)` it.

**Test pattern** (decode_test.go lines 16-49). Use the `load`/`decodeFile`/`mutate` helpers over `testdata/*.json`, and write batch fixtures as `testdata/batch-{basic,not-found,forbidden-nested,rate-limited}.json`. The `mutate` helper (lines 37-49) is the model for building variant batch payloads in-test. Keep `TestDecodeAllFixtures` (fixtures_test.go lines 52-86) green, including the body-sentinel check `badBodies` (lines 31-50), and run it over the batch fixtures too.

**capture.sh.** Keep `-F query=@internal/ghquery/pr.graphql` working with the fragment document (assumption A1). Follow its guard style (refuses unless `PR_RELAY_FIXTURE_REPO` matches, body sentinel rewrite).

---

### `internal/store/state.go` + `store.go` (store, file-I/O / CRUD)

**Analog:** partial. Shape from RESEARCH Patterns 1, 2 and pitfall 7. Conventions from:

**Deep-clone convention** (model.go lines 261-277, shown above). `State.Clone()` must deep-copy the `PRs` and `Subs` maps, each `*PRRecord`/`*Subscription`, the `Pending` slices and `Memory` (via `Memory.Clone()`). Back it with a `TestStateCloneDeep` modelled on model_test.go `TestMemoryCloneDeep` (line 102).

**File I/O convention** (testutil/golden.go lines 20-35, setsid-probe lines 62, 80). Errors carry the operation and path. Dirs are `0o700` and private files `0o600` (golden uses 0o644/0o755 because those are committed test files, so do not copy those modes):
```go
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}
	...
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer func() { _ = logFile.Close() }()
```
Note the `defer func() { _ = f.Close() }()` idiom. golangci-lint `standard` (errcheck) is enabled in `.golangci.yml`, so ignored errors must be explicit `_ =`.

**Atomic save.** Copy RESEARCH Pattern 1 verbatim (CreateTemp `state.json.tmp-*` → Chmod 0600 → Write → Sync → Close → Rename → open dir → Sync). **Open**: take `flock.TryLock` on `state.lock`, remove leftover `state.json.tmp-*`, then Load. **Load**: a parse error renames the file to `state.json.corrupt-<timestamp>` and records `CorruptAt` (D-20). `Version > 1` returns an error rather than quarantining. Add a `migrate(version int, raw json.RawMessage)` no-op hook.

**Golden schema test.** Use `testutil.Golden(t, "testdata/state-v1.json", got)` (golden.go lines 18-37) for the marshalled fixture state, plus a load test of the committed file. `-update` regenerates it, and `-update` is never passed in CI (comment at golden.go line 17).

---

### `internal/paths/paths.go` (utility, file-I/O)

**Analog:** `spikes/setsid-probe/main.go` `outputDir` (lines 51-66). It resolves a dir, creates it 0o700 and returns a cleaned path with wrapped errors:
```go
func outputDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	...
	dir := filepath.Join(filepath.Dir(exe), "..", "out")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}
	return filepath.Clean(dir), nil
}
```
Resolution order (from RESEARCH): `PR_RELAY_STATE_DIR`, then `XDG_STATE_HOME/pr-relay`, then the macOS `~/Library/Application Support/pr-relay` or Linux `~/.local/state/pr-relay`. Config: `XDG_CONFIG_HOME/pr-relay/config.toml`. Test with `t.Setenv`.

---

### `internal/config/config.go` (config, file-I/O)

**Analog:** none. Use BurntSushi/toml with defaults-first. A missing file means defaults, and unknown keys warn via `toml.MetaData.Undecoded()`. Conventions to copy: name constants the way sanitize.go does (lines 31-36, exported caps grouped in a `const` block with one-line doc) for the D-05..D-08 defaults (`DefaultBudgetPct = 0.10`, `DefaultLowThreshold = 0.20`, `DefaultMinInterval = time.Minute`, `DefaultMaxInterval = 16 * time.Minute`, batch size 20, pending cap 200). Durations in TOML can be strings parsed with `time.ParseDuration`, which lets the crash test set `100ms`.

---

### `internal/ghclient/token.go`, `client.go`, `errors.go` (service, request-response)

**Analog:** partial. `token.go` copies RESEARCH Pattern 3 (config.Reload → `auth.TokenFromEnvOrConfig` → bounded `exec.CommandContext` gh fallback). The repo's only exec site is the setsid-probe (lines 86-91). Copy its explicit-path and wrapped-error style, but use `CommandContext` with a 10 s timeout, and do not use Setsid:
```go
	cmd := exec.Command(exe, "child")
	cmd.Stdin = devNull
	...
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start child: %w", err)
	}
```
`client.go` copies the RESEARCH "ghclient request" example: injectable `Endpoint` and `http.Client{Timeout: 30s}`, `io.LimitReader(resp.Body, 16<<20)`, drain and close the body, and capture `Retry-After`/`X-RateLimit-*`/`X-GitHub-Request-Id`. The `User-Agent` uses `"pr-relay/"+version`, so a version value has to be passed in from `main` (`var version` in main.go line 8). `errors.go` follows the `ghquery:` prefix convention as `ghclient:` and defines typed errors (`ErrUnauthorized`, `*SecondaryLimitError{RetryAfter}`, `ErrPrimaryExhausted{ResetAt}`, `ErrNoToken`) classified per pitfall 10. Never put the token or headers in an error string or a log.

---

### `internal/poller/fanout.go` (pure service, transform)

**Analog:** `internal/filter/filter.go` (exact).

**Doc-comment convention** (filter.go lines 1-17). The package doc lists every rule with its requirement ID. Use the same layout for D-12 activity classification, the D-18 mask re-filter and the D-19 cap:
```go
// ExcludeSelf rules (EVT-08, D-12, D-14, D-20):
//   - Logins are compared case-insensitively ...
//   - review and comment events by self are dropped.
```

**Copy-not-mutate slice filter** (filter.go lines 28-37). The mask filter and pending re-filter should copy this exactly:
```go
func ExcludeSelf(events []model.Event, self string) []model.Event {
	out := make([]model.Event, 0, len(events))
	for _, e := range events {
		if self != "" && isOwn(e, self) {
			continue
		}
		out = append(out, e)
	}
	return out
}
```
D-12 activity is `len(filter.ExcludeSelf(r.Events, viewer)) > 0 || any raw r.Events[i].Type == model.EventCommits`. Reuse `filter.ExcludeSelf`, and do not reimplement `isOwn`.

---

### `internal/poller/cadence.go`, `budget.go` (pure utility, transform)

**Analog:** role-match with the small pure helpers in `internal/diff/ci.go` (e.g. `IsFailed` line 37, `fingerprint` line 63) and the package doc of `diff.go` lines 1-4 ("pure, deterministic ... reads no clock"). These functions take `now time.Time` / `quiet time.Duration` as **arguments** and never call `time.Now()`. Use table-driven tests in the style of `TestConclusionMapping` (ci_test.go line 15). The step table and formulas come from RESEARCH Patterns 5 and 6.

---

### `internal/poller/engine.go` + `scheduler.go` (service, CRUD + event-driven)

**Analog:** partial. The per-PR commit pipeline the engine wraps already exists, in test form, in `internal/diff/scenario_test.go` `runScenario` (lines 101-116):
```go
	mem := diff.Baseline(snaps[0])
	step := func(name string, s model.Snapshot) {
		r := diff.Step(mem, s)
		mem = r.Memory
		evs := filter.ExcludeSelf(r.Events, self)
		...
		Lines:    render.Lines(s.Ref, evs),
		Gap:      r.Gap,
	}
```
The engine version: `r := diff.Step(pr.Memory, snap)`. If `r.Gap`, re-fetch wide and re-Step from the **same** old Memory (Pattern 8). Then `delivered := filter.ExcludeSelf(...)`, fan out into a **cloned** `store.State`, `store.Save(next)`, and swap the pointer only on success (Pattern 1, anti-pattern 1). Note the engine starts from `Memory{}` (un-baselined) and lets `Step` baseline it (diff.go lines 57-59), rather than calling `Baseline` directly as the test does.

**Loop shape** (setsid-probe `runChild` lines 108-122) is the only timer loop in the repo. The scheduler replaces the ticker with a `time.Timer` capped at 30 s, recomputing due times from wall clock, and selects on `ctx.Done()` (from `signal.NotifyContext` in `dev watch`):
```go
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		...
		<-ticker.C
	}
```
Logging: `slog` with the fixed attribute set from Pattern 6 (`prs, cost, remaining, limit, reset_at, node_count, stretch, batch_size, duration_ms`). Inject `*slog.Logger` into the Engine so `TestCostLogged` can capture it.

**Tests:** `testing/synctest` + `fakegh_test.go` (in-memory `http.RoundTripper`, never an `httptest.Server` inside the bubble). Store under `t.TempDir()`. Assertion style follows diff_test.go (`t.Fatalf("...:\n got %+v\nwant %+v", ...)`).

---

### `cmd/pr-relay/main.go` (controller, modified) + `dev.go` (new)

**Analog:** `cmd/pr-relay/main.go` (lines 1-16). Keep the package doc, the `-ldflags -X main.version` variable and `versionString()`, because `main_test.go` asserts `versionString() == "pr-relay dev"`:
```go
// version is overridden at release time via -ldflags "-X main.version=...".
var version = "dev"

func versionString() string {
	return "pr-relay " + version
}
```
Replace `main()` with a cobra root (`version` subcommand printing `versionString()`) and a `dev` group with `Hidden: true` that holds `watch <PR-URL>...`. `watch` wiring: `paths` → `config.Load` → `store.Open` (flock) → `ghclient.New` (endpoint override from `PR_RELAY_GITHUB_GRAPHQL_URL`, test-only) → `poller.New` → `Subscribe` for each `model.ParsePRURL(arg)` → `Run(ctx)` under `signal.NotifyContext(SIGINT, SIGTERM)`. Print `render.Lines(ref, events)` for drained pending events, plus a cost/interval line per poll. With `--sink`, use the idempotent seq sink described in RESEARCH "Crash-loop design". Error-exit style from setsid-probe lines 34-36: `fmt.Fprintln(os.Stderr, "pr-relay:", err); os.Exit(1)`. Cobra's `SilenceUsage` gives the same effect.

---

### `cmd/pr-relay/crash_test.go` (e2e test)

**Analog:** partial, `spikes/setsid-probe/main.go` `spawn` (lines 68-98) for starting the real binary with redirected stdio. The test variant: `TestMain` runs `go build -o $TMP/pr-relay .` once; each iteration does `exec.Command(bin, "dev", "watch", "--sink", ..., url)` with `cmd.Env = append(os.Environ(), "PR_RELAY_STATE_DIR=...", "PR_RELAY_GITHUB_GRAPHQL_URL="+srv.URL, "GH_TOKEN=test")`, sleeps a random 0-400 ms, then calls `cmd.Process.Kill()` (SIGKILL) and `cmd.Wait()`. Use the `httptest.Server` fake GitHub in real time, outside any synctest bubble. Use `testing.Short()` to cut iterations from 50 to 10. Assertions are listed in RESEARCH "Crash-loop design".

## Shared Patterns

### Purity boundary
**Source:** `internal/diff/diff.go` lines 1-4, `internal/model/model.go` lines 1-3, `internal/filter/filter.go` lines 1-4.
**Apply to:** `ghquery/batch.go`, `poller/cadence.go`, `poller/budget.go`, `poller/fanout.go`, every D-04 fix.
Pure packages do no I/O, read no clock and never mutate their inputs (they clone first). Only `store`, `ghclient`, `paths`, `config`, `poller/engine.go`, `poller/scheduler.go` and `cmd/` may touch the OS, network or clock.

### Error wrapping
**Source:** `internal/ghquery/decode.go` lines 189-214; `spikes/setsid-probe/main.go` lines 55, 63, 75, 82.
**Apply to:** all new packages.
Use the `"<pkg>: <op>: %w"` format with lowercase text and no trailing punctuation. Export sentinels when callers must branch on them (`errors.Is`). Ignored errors are written as `_ = x.Close()` (errcheck runs under golangci-lint `default: standard`, see `.golangci.yml`).

### Package doc comments listing decisions
**Source:** `internal/sanitize/sanitize.go` lines 1-22; `internal/filter/filter.go` lines 1-17; `internal/render/render.go` lines 1-14.
**Apply to:** every new package (`store`, `ghclient`, `poller`, `config`, `paths`).
Each package doc states its responsibility, lists the resolved rules with requirement or decision IDs (D-xx, EVT-xx), and says what belongs to a later phase.

### Golden files
**Source:** `internal/testutil/golden.go` lines 13-37.
**Apply to:** `store/testdata/state-v1.json`, new diff scenarios, the `dev watch` output test.
```go
func Golden(t testing.TB, path string, got []byte) {
	t.Helper()
	if *update { ... os.WriteFile(path, got, 0o644) ...; return }
	want, err := os.ReadFile(path)
	...
}
```

### Fixture body hygiene
**Source:** `internal/ghquery/fixtures_test.go` lines 11-50 (`bodySentinel`, `badBodies`).
**Apply to:** every new captured or synthetic JSON fixture, batch fixtures included. Fake GitHub responses in poller and crash tests should also use `""` or the sentinel for bodies.

### Test helpers
**Source:** `internal/diff/snap_test.go` (functional options `snap(withX(...))`), `internal/ghquery/decode_test.go` lines 16-49 (`load`, `decodeFile`, `mutate`), `internal/model/model_test.go` lines 139-152 (`roundTrip[T]`).
**Apply to:** store, poller and ghquery batch tests. Helpers call `t.Helper()` first and fail with `t.Fatal`/`t.Fatalf`.

### File permissions
**Source:** `spikes/setsid-probe/main.go` lines 62, 80.
**Apply to:** `paths`, `store` (state dir 0o700; `state.json`, `state.lock` and temp files 0o600).

### CI
**Source:** `.github/workflows/ci.yml`. The matrix is ubuntu + macos × go.mod/stable, and the steps are `go build ./...`, `go vet ./...`, `go test -race -count=1 ./...` and golangci-lint v2.15.0. New tests must pass under `-race` on both OSes, so the crash loop must stay within CI time in `-race` mode. No workflow change is expected.

## No Analog Found

| File | Role | Data Flow | Reason |
|---|---|---|---|
| `internal/ghclient/client.go` | service | request-response | First network code in the repo. Use the RESEARCH "ghclient request" example. |
| `internal/ghclient/*_test.go` | test | — | First `httptest` usage. Use the RESEARCH Validation map (DMN-03 rows). |
| `internal/config/config.go` | config | file-I/O | No TOML or config yet. Use BurntSushi/toml defaults-first. |
| `internal/poller/fakegh_test.go` | test util | request-response | No fake transport exists yet. Use the RESEARCH Wave 0 description (scripted states, alias counting, injectable status/headers). |
| `cmd/pr-relay/dev.go` | CLI | streaming | No cobra usage yet. Use cobra v1.10.2 with `Hidden: true`. |
| synctest-based poller tests | test | — | No `testing/synctest` usage yet. Use the RESEARCH "synctest cadence test" example. |
| `internal/store/store.go` atomic save/flock/quarantine | store | file-I/O | Only generic file-I/O conventions exist. Copy RESEARCH Pattern 1 and pitfall 7. |

## Metadata

**Analog search scope:** `cmd/`, `internal/*`, `spikes/setsid-probe/`, `scripts/`, `.github/workflows/`, `.golangci.yml`, `go.mod`
**Files scanned:** 44 Go files (4,809 lines) + config and scripts
**Pattern extraction date:** 2026-11-01
