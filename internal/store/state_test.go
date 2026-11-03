package store

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/testutil"
)

func TestNewState(t *testing.T) {
	s := NewState()
	if s.Version != SchemaVersion || SchemaVersion != 2 {
		t.Fatalf("Version = %d, SchemaVersion = %d, want 2", s.Version, SchemaVersion)
	}
	if s.PRs == nil || len(s.PRs) != 0 {
		t.Fatalf("PRs = %#v, want empty non-nil map", s.PRs)
	}
	if s.Subs == nil || len(s.Subs) != 0 {
		t.Fatalf("Subs = %#v, want empty non-nil map", s.Subs)
	}
}

func TestPRKey(t *testing.T) {
	got := PRKey(model.PRRef{Owner: "Owner", Repo: "Repo", Number: 7})
	if got != "owner/repo#7" {
		t.Fatalf("PRKey = %q, want owner/repo#7", got)
	}
	if PRKey(model.PRRef{Owner: "owner", Repo: "REPO", Number: 7}) != got {
		t.Fatal("case variants must share one key")
	}
}

func TestSubID(t *testing.T) {
	id := SubID("sock", "w1:p1", "o/r#1")
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) {
		t.Fatalf("SubID = %q, want 32 hex chars", id)
	}
	if SubID("sock", "w1:p1", "o/r#1") != id {
		t.Fatal("SubID not stable")
	}
	for _, other := range []string{
		SubID("sock2", "w1:p1", "o/r#1"),
		SubID("sock", "w1:p2", "o/r#1"),
		SubID("sock", "w1:p1", "o/r#2"),
		SubID("soc", "kw1:p1", "o/r#1"), // the separator matters
	} {
		if other == id {
			t.Fatalf("SubID collision: %q", other)
		}
	}
}

var t0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// fullState returns a State with every field populated.
func fullState() *State {
	ref := model.PRRef{Owner: "acme", Repo: "widgets", Number: 42}
	key := PRKey(ref)
	s := NewState()
	s.Seq = 3
	s.NextGen = 2
	s.Viewer = "octocat"
	s.PRs[key] = &PRRecord{
		Ref: ref,
		Memory: model.Memory{
			Baselined:           true,
			HeadOID:             "aaaa",
			BaseOID:             "bbbb",
			State:               "OPEN",
			Mergeable:           "MERGEABLE",
			FailedReported:      map[string]bool{"CI/test": true},
			SettleCandidate:     "fp1",
			SettledResult:       "failed:CI/test",
			SettleSeq:           2,
			SeenReviews:         map[string]bool{"R1": true},
			SeenComments:        map[string]bool{"C1": true, "C2": true},
			SeenForcePushes:     map[string]bool{"F1": true},
			ReviewsTotal:        1,
			CommentsTotal:       2,
			Epoch:               4,
			CommitsTotal:        5,
			CommentsWatermark:   t0.Add(-time.Hour),
			ReviewsWatermark:    t0.Add(-2 * time.Hour),
			ReviewCommentTotals: map[string]int{"R1": 3},
		},
		Gen:          1,
		CreatedAt:    t0.Add(-24 * time.Hour),
		RateAnchor:   t0.Add(-time.Minute),
		LastActivity: t0.Add(-time.Minute),
		LastPollAt:   t0,
		LastError:    "NOT_FOUND",
		Terminal:     true,
	}
	id1 := SubID("/tmp/herdr.sock", "w1:p1", key)
	id2 := SubID("/tmp/herdr.sock", "w1:p2", key)
	s.Subs[id1] = &Subscription{
		ID: id1, PR: key, HerdrSocket: "/tmp/herdr.sock", PaneID: "w1:p1",
		AgentSession: "sess-1", AgentKind: "codex", TerminalID: "term-1",
		Events:    []model.EventType{model.EventCIFailed, model.EventReview},
		CreatedAt: t0.Add(-time.Hour), UpdatedAt: t0, ClockAt: t0,
		Pending: []PendingEvent{
			{Seq: 2, DetectedAt: t0, Event: model.Event{
				Type: model.EventCIFailed, Key: "ci.failed:aaaa:CI/test:4",
				CheckName: "test", CheckWorkflow: "CI", CheckURL: "https://example.invalid/c", HeadOID: "aaaa",
			}},
			{Seq: 3, DetectedAt: t0, Event: model.Event{
				Type: model.EventReview, Key: "review:R1", Actor: model.Actor{Login: "rev", Type: "User"},
				URL: "https://example.invalid/r", Verdict: "APPROVED", InlineCount: 3,
			}},
		},
		Dropped:    5,
		DroppedSeq: 1,
	}
	s.Subs[id2] = &Subscription{
		ID: id2, PR: key, HerdrSocket: "/tmp/herdr.sock", PaneID: "w1:p2",
		Events:    model.AllEventTypes(),
		CreatedAt: t0, UpdatedAt: t0, ClockAt: t0,
		Pending: []PendingEvent{
			{Seq: 1, DetectedAt: t0.Add(-time.Minute), Event: model.Event{
				Type: model.EventCommits, Key: "commits:aaaa", HeadOID: "aaaa", OldHeadOID: "9999",
				Count: 2, ForcePushed: true, Authors: []model.Actor{{Login: "dev", Type: "User"}},
			}},
			{Seq: 2, DetectedAt: t0, Event: model.Event{
				Type: model.EventCISettled, Key: "ci.settled:aaaa:2", HeadOID: "aaaa",
				Total: 3, FailedNames: []string{"CI/test"},
			}},
		},
	}
	return s
}

func TestStateCloneDeep(t *testing.T) {
	orig := fullState()
	want := fullState()
	c := orig.Clone()
	if !reflect.DeepEqual(c, orig) {
		t.Fatal("clone differs from original")
	}
	key := PRKey(model.PRRef{Owner: "acme", Repo: "widgets", Number: 42})
	id1 := SubID("/tmp/herdr.sock", "w1:p1", key)
	id2 := SubID("/tmp/herdr.sock", "w1:p2", key)

	c.Seq = 99
	c.PRs[key].Memory.SeenComments["C9"] = true
	c.PRs[key].Memory.ReviewCommentTotals["R1"] = 99
	c.PRs[key].Gen = 7
	c.Subs[id1].Pending[0].Seq = 99
	c.Subs[id1].Pending[1].Event.Verdict = "DISMISSED"
	c.Subs[id1].Events[0] = model.EventClosed
	c.Subs[id2].Pending[0].Event.Authors[0].Login = "mallory"
	c.Subs[id2].Pending[1].Event.FailedNames[0] = "other"
	c.Subs[id1].Pending = append(c.Subs[id1].Pending, PendingEvent{Seq: 100})
	c.PRs["x/y#1"] = &PRRecord{}
	c.Subs["new"] = &Subscription{}
	delete(c.Subs, id2)

	if !reflect.DeepEqual(orig, want) {
		t.Fatal("mutating the clone changed the original")
	}
}

func TestStateCloneNil(t *testing.T) {
	var s *State
	if s.Clone() != nil {
		t.Fatal("nil Clone must be nil")
	}
	c := (&State{Version: 1}).Clone()
	if c.PRs == nil || c.Subs == nil {
		t.Fatal("Clone must turn nil maps into empty maps")
	}
}

func TestStateV2Golden(t *testing.T) {
	s := fullState()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	testutil.Golden(t, "testdata/state-v2.json", b)

	raw, err := os.ReadFile("testdata/state-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var got State
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&got, s) {
		t.Fatalf("golden unmarshals to a different State:\n got %#v\nwant %#v", &got, s)
	}
	again, err := json.MarshalIndent(&got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(append(again, '\n')) != string(raw) {
		t.Fatal("golden does not re-marshal byte-identically")
	}
}

func TestSubscriptionAgentKindOmitEmpty(t *testing.T) {
	b, err := json.Marshal(&Subscription{ID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "agent_kind") {
		t.Fatalf("empty AgentKind marshalled: %s", b)
	}
	b, err = json.Marshal(&Subscription{ID: "x", AgentKind: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"agent_kind":"claude"`) {
		t.Fatalf("AgentKind not marshalled: %s", b)
	}
}
