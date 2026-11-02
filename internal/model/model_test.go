package model

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestParsePRURL(t *testing.T) {
	ok := []struct {
		in   string
		want PRRef
	}{
		{"https://github.com/o/r/pull/12", PRRef{"o", "r", 12}},
		{"https://github.com/o/r/pull/12/", PRRef{"o", "r", 12}},
		{"https://github.com/My-Org.x/re_po.go/pull/1", PRRef{"My-Org.x", "re_po.go", 1}},
	}
	for _, tc := range ok {
		got, err := ParsePRURL(tc.in)
		if err != nil {
			t.Errorf("ParsePRURL(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParsePRURL(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
		if !got.Valid() {
			t.Errorf("ParsePRURL(%q) returned invalid ref", tc.in)
		}
	}

	reject := []string{
		"http://github.com/o/r/pull/12",
		"https://github.example.com/o/r/pull/12",
		"https://evil.com/github.com/o/r/pull/12",
		"https://github.com.evil.com/o/r/pull/12",
		"https://github.com/o/r/issues/12",
		"https://github.com/o/r/pull/0",
		"https://github.com/o/r/pull/abc",
		"https://github.com/o/r/pull/-3",
		"https://github.com/o/r/pull/+3",
		"https://github.com/o/r/pull/12/files",
		"https://user@github.com/o/r/pull/12",
		"https://user:pw@github.com/o/r/pull/12",
		"https://github.com:443/o/r/pull/12",
		"https://github.com/o%20x/r/pull/12",
		"https://github.com/o$/r/pull/12",
		"https://github.com/o/r/pull/12?x=1",
		"https://github.com/o/r/pull/12#frag",
		"https://github.com//r/pull/12",
		"https://GitHub.com/o/r/pull/12",
		"github.com/o/r/pull/12",
		"https://github.com/../r/pull/1",
		"https://github.com/o/./pull/1",
		"https://github.com/./../pull/1",
		"",
	}
	for _, in := range reject {
		if got, err := ParsePRURL(in); err == nil {
			t.Errorf("ParsePRURL(%q) = %+v, want error", in, got)
		}
	}
}

func TestPRRefFormat(t *testing.T) {
	r := PRRef{"o", "r", 12}
	if r.String() != "o/r#12" {
		t.Errorf("String() = %q", r.String())
	}
	if r.URL() != "https://github.com/o/r/pull/12" {
		t.Errorf("URL() = %q", r.URL())
	}
	for _, bad := range []PRRef{{"", "r", 1}, {"o", "", 1}, {"o", "r", 0}, {"o/x", "r", 1}, {".", "r", 1}, {"o", "..", 1}, {"..", ".", 1}} {
		if bad.Valid() {
			t.Errorf("%+v.Valid() = true", bad)
		}
	}
	for _, good := range []PRRef{{"a.b", "c..d", 1}, {"...", ".x", 1}} {
		if !good.Valid() {
			t.Errorf("%+v.Valid() = false", good)
		}
	}
}

func TestActorIsUser(t *testing.T) {
	cases := map[string]bool{"User": true, "Bot": false, "Ghost": false, "": false, "Mannequin": false, "Organization": false}
	for typ, want := range cases {
		if got := (Actor{Login: "x", Type: typ}).IsUser(); got != want {
			t.Errorf("Actor{Type:%q}.IsUser() = %v, want %v", typ, got, want)
		}
	}
}

func TestAllEventTypes(t *testing.T) {
	want := []EventType{"closed", "conflict", "commits", "ci.failed", "ci.settled", "review", "comment"}
	got := AllEventTypes()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AllEventTypes() = %v, want %v", got, want)
	}
	for i, et := range want {
		if et.Rank() != i {
			t.Errorf("%s.Rank() = %d, want %d", et, et.Rank(), i)
		}
	}
	if EventType("nope").Rank() != 99 {
		t.Errorf("unknown Rank() = %d, want 99", EventType("nope").Rank())
	}
}

func TestMemoryCloneDeep(t *testing.T) {
	var zero Memory
	zc := zero.Clone()
	if zc.FailedReported == nil || zc.SeenReviews == nil || zc.SeenComments == nil || zc.SeenForcePushes == nil || zc.ReviewCommentTotals == nil {
		t.Fatalf("zero Memory clone has nil maps: %+v", zc)
	}

	orig := Memory{
		Baselined:           true,
		HeadOID:             "abc",
		FailedReported:      map[string]bool{"ci/build": true},
		SeenReviews:         map[string]bool{"R1": true},
		SeenComments:        map[string]bool{"C1": true},
		SeenForcePushes:     map[string]bool{"F1": true},
		ReviewsTotal:        1,
		Epoch:               3,
		CommitsTotal:        4,
		CommentsWatermark:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		ReviewsWatermark:    time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
		ReviewCommentTotals: map[string]int{"R1": 2},
	}
	c := orig.Clone()
	if !reflect.DeepEqual(c, orig) {
		t.Fatalf("clone differs: %+v vs %+v", c, orig)
	}
	c.FailedReported["x"] = true
	c.SeenReviews["x"] = true
	c.SeenComments["x"] = true
	c.SeenForcePushes["x"] = true
	for name, m := range map[string]map[string]bool{
		"FailedReported": orig.FailedReported, "SeenReviews": orig.SeenReviews,
		"SeenComments": orig.SeenComments, "SeenForcePushes": orig.SeenForcePushes,
	} {
		if m["x"] {
			t.Errorf("mutating clone changed original %s", name)
		}
		if len(m) != 1 {
			t.Errorf("original %s has %d entries, want 1", name, len(m))
		}
	}
	c.ReviewCommentTotals["R1"] = 9
	c.ReviewCommentTotals["x"] = 1
	if orig.ReviewCommentTotals["R1"] != 2 || len(orig.ReviewCommentTotals) != 1 {
		t.Errorf("mutating clone changed original ReviewCommentTotals: %v", orig.ReviewCommentTotals)
	}
}

func roundTrip[T any](t *testing.T, v T) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var got T
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, v) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, v)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	u := Actor{Login: "alice", Type: "User"}
	roundTrip(t, Snapshot{
		Ref: PRRef{"o", "r", 1}, NodeID: "PR_1", State: "OPEN", MergedBy: u,
		HeadOID: "h", BaseOID: "b", Mergeable: "MERGEABLE",
		Commits: []Commit{{OID: "h", Author: u}}, CommitsTotal: 1, CIKnown: true,
		Checks:             []CheckRun{{Key: "ci/build", Name: "build", Workflow: "ci", App: "github-actions", DatabaseID: 9, Status: "COMPLETED", Conclusion: "SUCCESS", URL: "u", DetailsURL: "d", RunAttempt: 1}},
		ChecksTruncated:    true,
		UnknownConclusions: []string{"FUTURE"},
		Reviews:            []Review{{ID: "R", State: "APPROVED", URL: "u", Author: u, HasBody: true, Comments: []ReviewComment{{ID: "c", URL: "u", Path: "p", ReplyToID: "r", Author: u}}, CommentsTotal: 1}},
		ReviewsTotal:       1,
		Comments:           []IssueComment{{ID: "IC", URL: "u", Author: u}},
		CommentsTotal:      1,
		ForcePushes:        []ForcePush{{ID: "F", BeforeOID: "a", AfterOID: "b", Actor: u}},
	})
	roundTrip(t, Event{
		Type: EventCISettled, Key: "k", Actor: u, URL: "u", CheckName: "n", CheckWorkflow: "w",
		CheckURL: "cu", HeadOID: "h", Passed: true, Total: 3, FailedNames: []string{"a"},
		Verdict: "APPROVED", InlineCount: 2, CommentKind: "reply", Path: "p", OldHeadOID: "o",
		Count: 2, ForcePushed: true, Authors: []Actor{u}, Conflicting: true, Merged: true,
	})
	roundTrip(t, Memory{
		Baselined: true, HeadOID: "h", BaseOID: "b", State: "OPEN", Mergeable: "MERGEABLE",
		FailedReported: map[string]bool{"k": true}, SettleCandidate: "fp", SettledResult: "passed",
		SettleSeq: 2, SeenReviews: map[string]bool{"r": true}, SeenComments: map[string]bool{"c": true},
		SeenForcePushes: map[string]bool{"f": true}, ReviewsTotal: 1, CommentsTotal: 1,
		Epoch: 7, CommitsTotal: 12,
		CommentsWatermark:   time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
		ReviewsWatermark:    time.Date(2026, 3, 4, 5, 6, 8, 0, time.UTC),
		ReviewCommentTotals: map[string]int{"r": 3, "r2": 0},
	})
	roundTrip(t, PRRef{"o", "r", 5})
	roundTrip(t, IssueComment{ID: "IC", URL: "u", Author: u, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	roundTrip(t, Review{ID: "R", State: "COMMENTED", SubmittedAt: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)})
}

func TestMemoryJSONKeys(t *testing.T) {
	b, err := json.Marshal(Memory{})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"baselined", "head_oid", "base_oid", "state", "mergeable", "failed_reported",
		"settle_candidate", "settled_result", "settle_seq", "seen_reviews", "seen_comments",
		"seen_force_pushes", "reviews_total", "comments_total", "epoch", "commits_total",
		"comments_watermark", "reviews_watermark", "review_comment_totals",
	}
	for _, k := range want {
		if _, ok := keys[k]; !ok {
			t.Errorf("Memory JSON missing key %q (got %s)", k, b)
		}
	}
	if len(keys) != len(want) {
		t.Errorf("Memory JSON has %d keys, want %d: %s", len(keys), len(want), b)
	}

	rb, _ := json.Marshal(PRRef{"o", "r", 1})
	if string(rb) != `{"owner":"o","repo":"r","number":1}` {
		t.Errorf("PRRef JSON = %s", rb)
	}
	ab, _ := json.Marshal(Actor{"a", "User"})
	if string(ab) != `{"login":"a","type":"User"}` {
		t.Errorf("Actor JSON = %s", ab)
	}
	eb, _ := json.Marshal(Event{Type: EventComment, Key: "k", HeadOID: "h"})
	var ek map[string]json.RawMessage
	_ = json.Unmarshal(eb, &ek)
	for _, k := range []string{"type", "key", "head_oid"} {
		if _, ok := ek[k]; !ok {
			t.Errorf("Event JSON missing %q: %s", k, eb)
		}
	}
	if _, ok := ek["check_name"]; ok {
		t.Errorf("Event JSON should omit empty check_name: %s", eb)
	}
}
