package ghquery

import (
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
