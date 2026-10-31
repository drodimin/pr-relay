package ghquery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/drodimin/pr-relay/internal/model"
)

func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeFile(t *testing.T, name string) (model.Snapshot, RateLimit) {
	t.Helper()
	s, rl, err := Decode(load(t, name))
	if err != nil {
		t.Fatalf("Decode(%s): %v", name, err)
	}
	return s, rl
}

// mutate decodes a fixture into a generic map, applies f to
// data.repository.pullRequest, and re-encodes it.
func mutate(t *testing.T, name string, f func(pr map[string]any)) []byte {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(load(t, name), &root); err != nil {
		t.Fatal(err)
	}
	pr := root["data"].(map[string]any)["repository"].(map[string]any)["pullRequest"].(map[string]any)
	f(pr)
	b, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeBasicFields(t *testing.T) {
	s, _ := decodeFile(t, "synthetic-basic.json")
	if s.Ref != (model.PRRef{Owner: "synth-owner", Repo: "synth-repo", Number: 7}) {
		t.Errorf("Ref = %+v", s.Ref)
	}
	if s.NodeID != "PR_kwDOsynth7" || s.State != "CLOSED" || s.Mergeable != "MERGEABLE" {
		t.Errorf("NodeID/State/Mergeable = %q/%q/%q", s.NodeID, s.State, s.Mergeable)
	}
	if s.HeadOID != "head000000000000000000000000000000000001" || s.BaseOID != "base000000000000000000000000000000000001" {
		t.Errorf("HeadOID/BaseOID = %q/%q", s.HeadOID, s.BaseOID)
	}
	if !s.CIKnown || !s.ChecksTruncated {
		t.Errorf("CIKnown=%v ChecksTruncated=%v, want true/true", s.CIKnown, s.ChecksTruncated)
	}
	if s.CommitsTotal != 3 || s.ReviewsTotal != 3 || s.CommentsTotal != 2 {
		t.Errorf("totals = %d/%d/%d", s.CommitsTotal, s.ReviewsTotal, s.CommentsTotal)
	}
	if len(s.Reviews) != 3 || s.Reviews[0].ID != "PRR_1" || s.Reviews[2].ID != "PRR_3" {
		t.Errorf("reviews not in API order: %+v", s.Reviews)
	}
	if len(s.Comments) != 2 || s.Comments[0].ID != "IC_1" || s.Comments[1].ID != "IC_2" {
		t.Errorf("comments not in API order: %+v", s.Comments)
	}
	r := s.Reviews[0]
	if r.State != "CHANGES_REQUESTED" || r.CommentsTotal != 2 || len(r.Comments) != 2 {
		t.Errorf("review 0 = %+v", r)
	}
	if c := r.Comments[1]; c.ReplyToID != "PRRC_0" || c.Path != "src/x.go" || c.ID != "PRRC_2" {
		t.Errorf("review comment 1 = %+v", c)
	}
	if r.Comments[0].ReplyToID != "" {
		t.Errorf("non-reply comment has ReplyToID %q", r.Comments[0].ReplyToID)
	}
}

func TestDecodeSkipsStatusContext(t *testing.T) {
	s, _ := decodeFile(t, "synthetic-basic.json")
	if len(s.Checks) != 3 {
		t.Fatalf("got %d checks, want 3 (StatusContext dropped, dup deduped): %+v", len(s.Checks), s.Checks)
	}
	for _, c := range s.Checks {
		if c.Name == "" || c.Key == "" || c.DatabaseID == 0 {
			t.Errorf("check looks like a non-CheckRun node: %+v", c)
		}
	}
}

func TestDecodeDedupeNewest(t *testing.T) {
	s, _ := decodeFile(t, "synthetic-basic.json")
	keys := make([]string, 0, len(s.Checks))
	for _, c := range s.Checks {
		keys = append(keys, c.Key)
	}
	if !sort.StringsAreSorted(keys) {
		t.Errorf("checks not sorted by Key: %v", keys)
	}
	want := []string{"ci/build", "ci/test", "some-app/lint"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	b := s.Checks[0]
	if b.DatabaseID != 20 || b.Conclusion != "SUCCESS" || b.RunAttempt != 2 || b.Workflow != "ci" || b.App != "github-actions" {
		t.Errorf("ci/build = %+v, want newest databaseId 20", b)
	}
	if b.URL != "https://github.com/synth-owner/synth-repo/runs/20" || b.DetailsURL == "" {
		t.Errorf("ci/build URLs = %q %q", b.URL, b.DetailsURL)
	}
	if tst := s.Checks[1]; tst.Status != "IN_PROGRESS" || tst.Conclusion != "" {
		t.Errorf("ci/test = %+v, want IN_PROGRESS with empty conclusion", tst)
	}
	if l := s.Checks[2]; l.Workflow != "" || l.App != "some-app" || l.Name != "lint" {
		t.Errorf("lint key fallback = %+v", l)
	}
}

func TestDecodeDropsBodies(t *testing.T) {
	s, _ := decodeFile(t, "synthetic-basic.json")
	if !s.Reviews[0].HasBody {
		t.Error("sentinel review: HasBody = false")
	}
	if s.Reviews[1].HasBody {
		t.Error("whitespace-only review: HasBody = true")
	}
	if s.Reviews[2].HasBody {
		t.Error("empty review: HasBody = true")
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "PRRELAY-BODY-SENTINEL") {
		t.Errorf("marshalled snapshot leaks review body: %s", b)
	}
}

func TestDecodeGhost(t *testing.T) {
	s, _ := decodeFile(t, "synthetic-basic.json")
	ghost := model.Actor{Login: "ghost", Type: "Ghost"}
	if s.Reviews[1].Author != ghost {
		t.Errorf("null review author = %+v", s.Reviews[1].Author)
	}
	if s.Comments[0].Author != ghost {
		t.Errorf("null issue comment author = %+v", s.Comments[0].Author)
	}
	if s.Reviews[0].Comments[1].Author != ghost {
		t.Errorf("null review comment author = %+v", s.Reviews[0].Comments[1].Author)
	}
	if ghost.IsUser() {
		t.Error("ghost IsUser() = true")
	}
	if a := s.Reviews[2].Author; a != (model.Actor{Login: "helper", Type: "Bot"}) {
		t.Errorf("bot author = %+v", a)
	}
	if a := s.Comments[1].Author; a != (model.Actor{Login: "carol", Type: "User"}) {
		t.Errorf("user author = %+v", a)
	}
}

func TestDecodeCommitAuthor(t *testing.T) {
	s, _ := decodeFile(t, "synthetic-basic.json")
	want := []model.Commit{
		{OID: "c1", Author: model.Actor{Login: "alice", Type: "User"}},
		{OID: "c2", Author: model.Actor{Login: "bob", Type: "User"}},
		{OID: "head000000000000000000000000000000000001", Author: model.Actor{}},
	}
	if !reflect.DeepEqual(s.Commits, want) {
		t.Errorf("commits = %+v, want %+v", s.Commits, want)
	}
}

func TestDecodeUnknownConclusion(t *testing.T) {
	s, _ := decodeFile(t, "synthetic-basic.json")
	if !reflect.DeepEqual(s.UnknownConclusions, []string{"FUTURE_THING"}) {
		t.Errorf("UnknownConclusions = %v", s.UnknownConclusions)
	}
	if s.Checks[2].Conclusion != "FUTURE_THING" {
		t.Errorf("unknown conclusion not kept verbatim: %q", s.Checks[2].Conclusion)
	}
}

func TestDecodeRollupMismatch(t *testing.T) {
	s, _ := decodeFile(t, "synthetic-mismatch.json")
	if s.CIKnown {
		t.Error("CIKnown = true for rollup commit != head")
	}
	if len(s.Checks) != 0 || len(s.UnknownConclusions) != 0 || s.ChecksTruncated {
		t.Errorf("stale rollup leaked checks: %+v / %v / %v", s.Checks, s.UnknownConclusions, s.ChecksTruncated)
	}
	if s.HeadOID != "newhead" || s.Mergeable != "UNKNOWN" || s.State != "OPEN" {
		t.Errorf("fields = %q %q %q", s.HeadOID, s.Mergeable, s.State)
	}
}

func TestDecodeNullRollup(t *testing.T) {
	data := mutate(t, "synthetic-mismatch.json", func(pr map[string]any) { pr["statusCheckRollup"] = nil })
	s, _, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if !s.CIKnown || len(s.Checks) != 0 {
		t.Errorf("null rollup: CIKnown=%v checks=%d, want true/0", s.CIKnown, len(s.Checks))
	}
}

func TestDecodeErrors(t *testing.T) {
	if _, _, err := Decode(load(t, "synthetic-errors.json")); err == nil {
		t.Error("errors array: want error")
	}
	cases := map[string][]byte{
		"null pullRequest": []byte(`{"data":{"rateLimit":null,"repository":{"pullRequest":null}}}`),
		"null repository":  []byte(`{"data":{"repository":null}}`),
		"no data":          []byte(`{}`),
		"not json":         []byte(`<html>`),
		"bad url": mutate(t, "synthetic-mismatch.json", func(pr map[string]any) {
			pr["url"] = "https://evil.example/synth-owner/synth-repo/pull/8"
		}),
	}
	for name, data := range cases {
		if _, _, err := Decode(data); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestDecodeClosedBy(t *testing.T) {
	s, _ := decodeFile(t, "synthetic-basic.json")
	if s.ClosedBy != (model.Actor{Login: "dave", Type: "User"}) {
		t.Errorf("ClosedBy = %+v, want newest ClosedEvent actor dave", s.ClosedBy)
	}
	if s.MergedBy != (model.Actor{}) {
		t.Errorf("MergedBy = %+v, want zero for unmerged PR", s.MergedBy)
	}
	want := []model.ForcePush{{ID: "FP_1", BeforeOID: "before1", AfterOID: "head000000000000000000000000000000000001", Actor: model.Actor{Login: "alice", Type: "User"}}}
	if !reflect.DeepEqual(s.ForcePushes, want) {
		t.Errorf("ForcePushes = %+v", s.ForcePushes)
	}

	merged := mutate(t, "synthetic-basic.json", func(pr map[string]any) {
		pr["state"] = "MERGED"
		pr["merged"] = true
		pr["mergedBy"] = map[string]any{"__typename": "User", "login": "erin"}
	})
	m, _, err := Decode(merged)
	if err != nil {
		t.Fatal(err)
	}
	if m.State != "MERGED" || m.MergedBy != (model.Actor{Login: "erin", Type: "User"}) {
		t.Errorf("merged: State=%q MergedBy=%+v", m.State, m.MergedBy)
	}
}

func TestDecodeRateLimit(t *testing.T) {
	_, rl := decodeFile(t, "synthetic-basic.json")
	want := RateLimit{Cost: 1, Limit: 5000, Remaining: 4987, Used: 13, NodeCount: 312, ResetAt: "2026-10-31T12:00:00Z"}
	if rl != want {
		t.Errorf("RateLimit = %+v, want %+v", rl, want)
	}
}

func TestQueryShape(t *testing.T) {
	if !strings.Contains(Query, "query PRState") {
		t.Error("Query lacks operation PRState")
	}
	if n := len(regexp.MustCompile(`\bbody\b`).FindAllString(Query, -1)); n != 1 {
		t.Errorf("Query selects body %d times, want exactly 1 (reviews only)", n)
	}
	if strings.Contains(Query, "StatusContext") {
		t.Error("Query selects StatusContext (D-09)")
	}
	rev := strings.Index(Query, "reviews(")
	body := strings.Index(Query, " body")
	cmts := strings.Index(Query, "\n      comments(")
	if rev < 0 || cmts < 0 || body < rev || body > cmts {
		t.Errorf("body is not inside the reviews selection (reviews=%d body=%d comments=%d)", rev, body, cmts)
	}
}
