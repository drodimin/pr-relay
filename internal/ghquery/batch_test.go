package ghquery

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drodimin/pr-relay/internal/model"
)

var (
	refA = model.PRRef{Owner: "synth-owner", Repo: "synth-repo", Number: 7}
	refB = model.PRRef{Owner: "other-owner", Repo: "other.repo", Number: 42}
)

func TestBuildBatchShape(t *testing.T) {
	q, vars, err := BuildBatch([]model.PRRef{refA, refB}, NormalWindow)
	if err != nil {
		t.Fatalf("BuildBatch: %v", err)
	}
	if !strings.HasPrefix(q, "query PRBatch(") {
		t.Errorf("query does not start with PRBatch: %q", q[:40])
	}
	for _, want := range []string{
		"$o0: String!", "$r0: String!", "$n0: Int!",
		"$o1: String!", "$r1: String!", "$n1: Int!",
		"$reviewsLast: Int!", "$commentsLast: Int!", "$reviewCommentsFirst: Int!",
		"pr0: repository(owner: $o0, name: $r0)",
		"pr1: repository(owner: $o1, name: $r1)",
		"pullRequest(number: $n0) { ...PRFields }",
		"pullRequest(number: $n1) { ...PRFields }",
		"viewer { login }",
		"rateLimit {",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q", want)
		}
	}
	if n := strings.Count(q, "fragment PRFields"); n != 1 {
		t.Errorf("fragment PRFields appears %d times, want 1", n)
	}
	if !strings.Contains(q, Fragment) {
		t.Error("query does not embed the shared Fragment")
	}
	if strings.Contains(q, "pr2:") {
		t.Error("query has an extra alias")
	}
	want := map[string]any{
		"o0": refA.Owner, "r0": refA.Repo, "n0": refA.Number,
		"o1": refB.Owner, "r1": refB.Repo, "n1": refB.Number,
		"reviewsLast": 20, "commentsLast": 30, "reviewCommentsFirst": 30,
	}
	if len(vars) != len(want) {
		t.Errorf("vars has %d keys, want %d: %v", len(vars), len(want), vars)
	}
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("vars[%q] = %v, want %v", k, vars[k], v)
		}
	}
	for _, s := range []string{refA.Owner, refA.Repo, refB.Owner, refB.Repo} {
		if strings.Contains(q, s) {
			t.Errorf("query text contains ref value %q (must be a variable)", s)
		}
	}
}

func TestBuildBatchWideWindow(t *testing.T) {
	_, vars, err := BuildBatch([]model.PRRef{refA}, WideWindow)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"reviewsLast", "commentsLast", "reviewCommentsFirst"} {
		if vars[k] != 100 {
			t.Errorf("vars[%q] = %v, want 100", k, vars[k])
		}
	}
}

func TestBuildBatchRejects(t *testing.T) {
	many := make([]model.PRRef, MaxBatch+1)
	for i := range many {
		many[i] = model.PRRef{Owner: "o", Repo: "r", Number: i + 1}
	}
	full := many[:MaxBatch]
	if _, _, err := BuildBatch(full, NormalWindow); err != nil {
		t.Errorf("BuildBatch(%d refs): %v", MaxBatch, err)
	}
	cases := map[string][]model.PRRef{
		"nil":       nil,
		"empty":     {},
		"too many":  many,
		"duplicate": {refA, refB, refA},
		"dup case":  {refA, {Owner: "SYNTH-owner", Repo: "Synth-Repo", Number: 7}},
		"injection": {{Owner: "evil){x}", Repo: "r", Number: 1}},
		"quote":     {{Owner: `a"b`, Repo: "r", Number: 1}},
		"zero num":  {{Owner: "o", Repo: "r", Number: 0}},
		"dotdot":    {{Owner: "..", Repo: "r", Number: 1}},
	}
	for name, refs := range cases {
		q, vars, err := BuildBatch(refs, NormalWindow)
		if err == nil {
			t.Errorf("%s: BuildBatch returned no error", name)
		}
		if q != "" || vars != nil {
			t.Errorf("%s: BuildBatch returned output alongside an error", name)
		}
		if err != nil && !strings.HasPrefix(err.Error(), "ghquery: ") {
			t.Errorf("%s: error %q lacks ghquery prefix", name, err)
		}
	}
}

func decodeBatchFile(t *testing.T, name string, refs []model.PRRef) BatchData {
	t.Helper()
	bd, err := DecodeBatch(load(t, name), refs)
	if err != nil {
		t.Fatalf("DecodeBatch(%s): %v", name, err)
	}
	if len(bd.Items) != len(refs) {
		t.Fatalf("DecodeBatch(%s): %d items, want %d", name, len(bd.Items), len(refs))
	}
	return bd
}

func TestDecodeBatchBasic(t *testing.T) {
	refs := []model.PRRef{refA, refB}
	bd := decodeBatchFile(t, "batch-basic.json", refs)
	if bd.Viewer != "drodimin" {
		t.Errorf("Viewer = %q", bd.Viewer)
	}
	if bd.RateLimit.Cost != 2 || bd.RateLimit.Remaining != 4987 || bd.RateLimit.ResetAt == "" {
		t.Errorf("RateLimit = %+v", bd.RateLimit)
	}
	for i, it := range bd.Items {
		if it.Err != nil {
			t.Errorf("Items[%d].Err = %v", i, it.Err)
		}
		if it.Ref != refs[i] || it.Snapshot.Ref != refs[i] {
			t.Errorf("Items[%d] Ref=%+v Snapshot.Ref=%+v, want %+v", i, it.Ref, it.Snapshot.Ref, refs[i])
		}
		if len(it.Warnings) != 0 {
			t.Errorf("Items[%d].Warnings = %v", i, it.Warnings)
		}
	}
	if bd.Items[0].Snapshot.NodeID != "PR_kwDOsynth7" || bd.Items[1].Snapshot.NodeID != "PR_kwDOother42" {
		t.Errorf("NodeIDs = %q, %q", bd.Items[0].Snapshot.NodeID, bd.Items[1].Snapshot.NodeID)
	}
	// The batch path shares snapshotFromWire with Decode.
	if len(bd.Items[0].Snapshot.Checks) != 3 || !bd.Items[0].Snapshot.CIKnown {
		t.Errorf("Checks = %+v", bd.Items[0].Snapshot.Checks)
	}
}

func TestDecodeBatchCaseInsensitiveRef(t *testing.T) {
	upper := model.PRRef{Owner: "Synth-Owner", Repo: "SYNTH-REPO", Number: 7}
	bd := decodeBatchFile(t, "batch-basic.json", []model.PRRef{upper, refB})
	if bd.Items[0].Err != nil {
		t.Fatalf("Items[0].Err = %v", bd.Items[0].Err)
	}
	if bd.Items[0].Snapshot.Ref != upper {
		t.Errorf("Snapshot.Ref = %+v, want requested %+v", bd.Items[0].Snapshot.Ref, upper)
	}
}

func TestDecodeBatchNotFound(t *testing.T) {
	bd := decodeBatchFile(t, "batch-not-found.json", []model.PRRef{refA, refB})
	if bd.Items[0].Err != nil || bd.Items[0].Snapshot.NodeID != "PR_kwDOsynth7" {
		t.Errorf("Items[0] = err %v node %q", bd.Items[0].Err, bd.Items[0].Snapshot.NodeID)
	}
	if !errors.Is(bd.Items[1].Err, ErrNotFound) {
		t.Errorf("Items[1].Err = %v, want ErrNotFound", bd.Items[1].Err)
	}
	if bd.Items[1].Err != nil && !strings.Contains(bd.Items[1].Err.Error(), "Could not resolve") {
		t.Errorf("Items[1].Err %q lacks the GraphQL message", bd.Items[1].Err)
	}
	if bd.Viewer != "drodimin" {
		t.Errorf("Viewer = %q", bd.Viewer)
	}
}

func TestDecodeBatchNullRepository(t *testing.T) {
	raw := []byte(`{"data":{"viewer":{"login":"v"},"pr0":null},"errors":[{"type":"NOT_FOUND","path":["pr0"],"message":"Could not resolve to a Repository"}]}`)
	bd, err := DecodeBatch(raw, []model.PRRef{refA})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(bd.Items[0].Err, ErrNotFound) {
		t.Errorf("Err = %v, want ErrNotFound", bd.Items[0].Err)
	}
}

func TestDecodeBatchMissingAlias(t *testing.T) {
	raw := []byte(`{"data":{"viewer":{"login":"v"},"pr0":{"pullRequest":null}}}`)
	bd, err := DecodeBatch(raw, []model.PRRef{refA, refB})
	if err != nil {
		t.Fatal(err)
	}
	for i := range bd.Items {
		if !errors.Is(bd.Items[i].Err, ErrNotFound) {
			t.Errorf("Items[%d].Err = %v, want ErrNotFound", i, bd.Items[i].Err)
		}
	}
}

func TestDecodeBatchForbiddenNested(t *testing.T) {
	bd := decodeBatchFile(t, "batch-forbidden-nested.json", []model.PRRef{refA})
	it := bd.Items[0]
	if it.Err != nil {
		t.Fatalf("Err = %v, want nil (nested FORBIDDEN is a warning)", it.Err)
	}
	if len(it.Warnings) != 1 || !strings.HasPrefix(it.Warnings[0], "FORBIDDEN:") {
		t.Errorf("Warnings = %v", it.Warnings)
	}
	if it.Snapshot.NodeID != "PR_kwDOsynth7" {
		t.Errorf("Snapshot not returned: %+v", it.Snapshot.Ref)
	}
}

func TestDecodeBatchAliasForbidden(t *testing.T) {
	for _, path := range []string{`["pr0"]`, `["pr0","pullRequest"]`} {
		raw := []byte(`{"data":{"viewer":{"login":"v"},"pr0":{"pullRequest":null}},"errors":[{"type":"FORBIDDEN","path":` + path + `,"message":"Resource not accessible by integration"}]}`)
		bd, err := DecodeBatch(raw, []model.PRRef{refA})
		if err != nil {
			t.Fatalf("path %s: %v", path, err)
		}
		if !errors.Is(bd.Items[0].Err, ErrForbidden) {
			t.Errorf("path %s: Err = %v, want ErrForbidden", path, bd.Items[0].Err)
		}
	}
}

func TestDecodeBatchRateLimited(t *testing.T) {
	_, err := DecodeBatch(load(t, "batch-rate-limited.json"), []model.PRRef{refA, refB})
	var be *BatchError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v (%T), want *BatchError", err, err)
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("errors.Is(err, ErrRateLimited) = false for %v", err)
	}
}

func TestDecodeBatchRateLimitedWithData(t *testing.T) {
	// RATE_LIMITED is never tolerated, even next to alias data.
	var root map[string]any
	if err := json.Unmarshal(load(t, "batch-basic.json"), &root); err != nil {
		t.Fatal(err)
	}
	root["errors"] = []any{map[string]any{"type": "RATE_LIMITED", "path": []any{"pr0", "pullRequest"}, "message": "slow down"}}
	raw, _ := json.Marshal(root)
	_, err := DecodeBatch(raw, []model.PRRef{refA, refB})
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("err = %v, want ErrRateLimited", err)
	}
}

func TestDecodeBatchPathlessError(t *testing.T) {
	raw := []byte(`{"data":null,"errors":[{"message":"Something went wrong"},{"type":"INTERNAL","message":"boom"}]}`)
	_, err := DecodeBatch(raw, []model.PRRef{refA})
	var be *BatchError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v (%T), want *BatchError", err, err)
	}
	if errors.Is(err, ErrRateLimited) {
		t.Error("pathless non-rate-limit error reported as rate limited")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Something went wrong") || !strings.Contains(msg, "INTERNAL: boom") {
		t.Errorf("Error() = %q, want both messages", msg)
	}
}

func TestDecodeBatchPathlessErrorWithData(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(load(t, "batch-basic.json"), &root); err != nil {
		t.Fatal(err)
	}
	root["errors"] = []any{map[string]any{"message": "partial outage"}}
	raw, _ := json.Marshal(root)
	bd, err := DecodeBatch(raw, []model.PRRef{refA, refB})
	if err != nil {
		t.Fatalf("err = %v, want alias data to survive", err)
	}
	if bd.Items[0].Err != nil || bd.Items[1].Err != nil {
		t.Errorf("item errs = %v, %v", bd.Items[0].Err, bd.Items[1].Err)
	}
	if len(bd.Warnings) != 1 || bd.Warnings[0] != "partial outage" {
		t.Errorf("Warnings = %v", bd.Warnings)
	}
}

func TestDecodeBatchRefMismatch(t *testing.T) {
	// Request order swapped relative to the response: both aliases mismatch.
	bd := decodeBatchFile(t, "batch-basic.json", []model.PRRef{refB, refA})
	for i, it := range bd.Items {
		if it.Err == nil {
			t.Errorf("Items[%d].Err = nil, want mismatch", i)
		} else if !strings.Contains(it.Err.Error(), "mismatch") {
			t.Errorf("Items[%d].Err = %v", i, it.Err)
		}
	}
	wrongNum := model.PRRef{Owner: refA.Owner, Repo: refA.Repo, Number: 8}
	bd = decodeBatchFile(t, "batch-basic.json", []model.PRRef{wrongNum, refB})
	if bd.Items[0].Err == nil || bd.Items[1].Err != nil {
		t.Errorf("errs = %v, %v", bd.Items[0].Err, bd.Items[1].Err)
	}
}

func TestDecodeBatchBadInput(t *testing.T) {
	if _, err := DecodeBatch([]byte(`{not json`), []model.PRRef{refA}); err == nil {
		t.Error("malformed JSON: no error")
	}
	if _, err := DecodeBatch(load(t, "batch-basic.json"), nil); err == nil {
		t.Error("no refs: no error")
	}
	bd, err := DecodeBatch([]byte(`{"data":{"pr0":{"pullRequest":{"url":5}}}}`), []model.PRRef{refA})
	if err != nil {
		t.Errorf("one undecodable alias failed the batch: %v", err)
	} else if bd.Items[0].Err == nil {
		t.Error("undecodable alias has no Err")
	}
}

func TestBatchFixturesHaveNoBodies(t *testing.T) {
	files, err := filepath.Glob("testdata/batch-*.json")
	if err != nil {
		t.Fatal(err)
	}
	// 4 synthetic batch fixtures plus the live capture batch-live.json (plan 02-12).
	if len(files) != 5 {
		t.Fatalf("found %d batch fixtures, want 5", len(files))
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var generic any
		if err := json.Unmarshal(raw, &generic); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		var bad []string
		badBodies(generic, "$", &bad)
		for _, p := range bad {
			t.Errorf("%s: %s holds a body other than \"\" or the sentinel", f, p)
		}
	}
}
