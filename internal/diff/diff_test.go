package diff

import (
	"reflect"
	"testing"

	"github.com/drodimin/pr-relay/internal/model"
)

func TestBaselineSilent(t *testing.T) {
	s := snap(
		withCommits("c0", "h1"),
		withForcePush("FP1", "x", "h1", "alice"),
		withCheck("ci/build", "COMPLETED", "SUCCESS", 10),
		withCheck("ci/test", "COMPLETED", "FAILURE", 11),
		func(s *model.Snapshot) {
			s.Reviews = []model.Review{{ID: "R1", State: "APPROVED", Comments: []model.ReviewComment{{ID: "RC1"}}}}
			s.ReviewsTotal = 1
			s.Comments = []model.IssueComment{{ID: "IC1"}}
			s.CommentsTotal = 1
		},
	)
	base := Baseline(s)
	if !base.Baselined {
		t.Fatal("Baseline did not set Baselined")
	}
	r := Step(base, s)
	if len(r.Events) != 0 {
		t.Fatalf("identical step emitted %d events: %+v", len(r.Events), r.Events)
	}
	if !reflect.DeepEqual(r.Memory, base) {
		t.Fatalf("memory changed on identical step:\n got %+v\nwant %+v", r.Memory, base)
	}
	if !base.SeenReviews["R1"] || !base.SeenComments["RC1"] || !base.SeenComments["IC1"] || !base.SeenForcePushes["FP1"] {
		t.Fatalf("baseline did not mark seen items: %+v", base)
	}
}

func TestStepNoMutate(t *testing.T) {
	s1 := snap(withCommits("h1"), withCheck("ci/test", "IN_PROGRESS", "", 1))
	m := Baseline(s1)
	before := m.Clone()
	s2 := snap(
		withHead("h2"), withCommits("h1", "h2"),
		withMergeable("CONFLICTING"),
		withForcePush("FP9", "h1", "h2", "bob"),
		withCheck("ci/test", "COMPLETED", "FAILURE", 2),
	)
	r := Step(m, s2)
	if len(r.Events) == 0 {
		t.Fatal("expected events from changed snapshot")
	}
	if !reflect.DeepEqual(m, before) {
		t.Fatalf("Step mutated input memory:\n got %+v\nwant %+v", m, before)
	}
}

func TestStepUnbaselined(t *testing.T) {
	s := snap(withMergeable("CONFLICTING"), withState("MERGED"))
	r := Step(model.Memory{}, s)
	if len(r.Events) != 0 {
		t.Fatalf("unbaselined step emitted events: %+v", r.Events)
	}
	if !r.Memory.Baselined {
		t.Fatal("unbaselined step did not baseline")
	}
	if !reflect.DeepEqual(r.Memory, Baseline(s)) {
		t.Fatalf("unbaselined step memory differs from Baseline")
	}
}

func TestEventOrder(t *testing.T) {
	// A real step: conflict and commits together come back in Rank order.
	steps, _ := run(
		snap(withCommits("h1")),
		snap(withHead("h2"), withCommits("h1", "h2"), withMergeable("CONFLICTING")),
	)
	got := steps[0]
	if len(got) != 2 || got[0].Type != model.EventConflict || got[1].Type != model.EventCommits {
		t.Fatalf("step order wrong: %+v", got)
	}

	// The sort itself: closed, conflict, commits by Rank, ties by Key.
	evs := []model.Event{
		{Type: model.EventCommits, Key: "commits:h2"},
		{Type: model.EventConflict, Key: "conflict:h2:b:CONFLICTING"},
		{Type: model.EventCIFailed, Key: "ci.failed:h2:z"},
		{Type: model.EventClosed, Key: "closed:MERGED"},
		{Type: model.EventCIFailed, Key: "ci.failed:h2:a"},
	}
	sortEvents(evs)
	want := []string{"closed:MERGED", "conflict:h2:b:CONFLICTING", "commits:h2", "ci.failed:h2:a", "ci.failed:h2:z"}
	for i, k := range want {
		if evs[i].Key != k {
			t.Fatalf("sorted[%d] = %q, want %q (all: %+v)", i, evs[i].Key, k, evs)
		}
	}
}
