package diff

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/drodimin/pr-relay/internal/model"
)

func TestClosed(t *testing.T) {
	// OPEN -> MERGED, then repeat.
	steps, _ := run(
		snap(),
		snap(withState("MERGED"), withMergedBy("me")),
		snap(withState("MERGED"), withMergedBy("me")),
	)
	if len(steps[0]) != 1 {
		t.Fatalf("merge: want 1 event, got %+v", steps[0])
	}
	e := steps[0][0]
	if e.Type != model.EventClosed || !e.Merged || e.Key != "closed:1:MERGED" || e.Actor.Login != "me" {
		t.Fatalf("merge event wrong: %+v", e)
	}
	if len(steps[1]) != 0 {
		t.Fatalf("repeat merged step emitted: %+v", steps[1])
	}

	// OPEN -> CLOSED.
	steps, _ = run(snap(), snap(withState("CLOSED"), withClosedBy("carol")))
	if len(steps[0]) != 1 {
		t.Fatalf("close: want 1 event, got %+v", steps[0])
	}
	e = steps[0][0]
	if e.Type != model.EventClosed || e.Merged || e.Key != "closed:1:CLOSED" || e.Actor.Login != "carol" {
		t.Fatalf("close event wrong: %+v", e)
	}

	// CLOSED -> OPEN (reopen) is silent but updates memory; a second close fires again.
	steps, m := run(
		snap(withState("CLOSED")),
		snap(),
	)
	if len(steps[0]) != 0 || m.State != "OPEN" {
		t.Fatalf("reopen: events %+v state %q", steps[0], m.State)
	}
	r := Step(m, snap(withState("CLOSED"), withClosedBy("carol")))
	if len(r.Events) != 1 {
		t.Fatalf("close after reopen: %+v", r.Events)
	}
}

func TestConflict(t *testing.T) {
	steps, _ := run(
		snap(),
		snap(withMergeable("CONFLICTING")),
		snap(withMergeable("CONFLICTING")),
		snap(withMergeable("MERGEABLE")),
	)
	if len(steps[0]) != 1 || !steps[0][0].Conflicting || steps[0][0].Type != model.EventConflict {
		t.Fatalf("became conflicting: %+v", steps[0])
	}
	if steps[0][0].Key != "conflict:0:h1:b1:CONFLICTING" {
		t.Fatalf("conflict key: %q", steps[0][0].Key)
	}
	if len(steps[1]) != 0 {
		t.Fatalf("repeat conflicting emitted: %+v", steps[1])
	}
	if len(steps[2]) != 1 || steps[2][0].Conflicting || steps[2][0].Key != "conflict:0:h1:b1:MERGEABLE" {
		t.Fatalf("cleared: %+v", steps[2])
	}
}

func TestMergeableUnknown(t *testing.T) {
	steps, m := run(
		snap(),
		snap(withMergeable("UNKNOWN")),
		snap(withMergeable("MERGEABLE")),
	)
	if n := len(flatten(steps)); n != 0 {
		t.Fatalf("MERGEABLE->UNKNOWN->MERGEABLE emitted %d events", n)
	}
	if m.Mergeable != "MERGEABLE" {
		t.Fatalf("memory mergeable %q", m.Mergeable)
	}

	steps, m = run(
		snap(),
		snap(withMergeable("UNKNOWN")),
		snap(withMergeable("CONFLICTING")),
	)
	if len(steps[0]) != 0 {
		t.Fatalf("UNKNOWN emitted: %+v", steps[0])
	}
	all := flatten(steps)
	if len(all) != 1 || !all[0].Conflicting {
		t.Fatalf("MERGEABLE->UNKNOWN->CONFLICTING: %+v", all)
	}
	if m.Mergeable != "CONFLICTING" {
		t.Fatalf("memory mergeable %q", m.Mergeable)
	}

	// UNKNOWN never changes memory.
	base := Baseline(snap(withMergeable("CONFLICTING")))
	r := Step(base, snap(withMergeable("UNKNOWN")))
	if r.Memory.Mergeable != "CONFLICTING" {
		t.Fatalf("UNKNOWN changed memory to %q", r.Memory.Mergeable)
	}
}

func TestMergeableUnknownBaselineConflicting(t *testing.T) {
	steps, m := run(
		snap(withMergeable("UNKNOWN")),
		snap(withMergeable("UNKNOWN")),
		snap(withMergeable("CONFLICTING")),
		snap(withMergeable("CONFLICTING")),
	)
	if m0 := Baseline(snap(withMergeable("UNKNOWN"))); m0.Mergeable != "" {
		t.Fatalf("baseline UNKNOWN stored %q", m0.Mergeable)
	}
	all := flatten(steps)
	if len(all) != 1 || all[0].Type != model.EventConflict || !all[0].Conflicting {
		t.Fatalf("D-21: want exactly 1 conflicting event, got %+v", all)
	}
	if m.Mergeable != "CONFLICTING" {
		t.Fatalf("memory mergeable %q", m.Mergeable)
	}
}

func TestMergeableUnknownBaselineMergeable(t *testing.T) {
	steps, m := run(
		snap(withMergeable("UNKNOWN")),
		snap(withMergeable("MERGEABLE")),
	)
	if n := len(flatten(steps)); n != 0 {
		t.Fatalf("D-21: MERGEABLE adoption emitted %d events", n)
	}
	if m.Mergeable != "MERGEABLE" {
		t.Fatalf("memory mergeable %q, want MERGEABLE", m.Mergeable)
	}
	// Later change emits as usual.
	r := Step(m, snap(withMergeable("CONFLICTING")))
	if len(r.Events) != 1 || !r.Events[0].Conflicting {
		t.Fatalf("later conflict: %+v", r.Events)
	}
}

func TestNoConflictWhenClosed(t *testing.T) {
	steps, _ := run(
		snap(withState("CLOSED")),
		snap(withState("CLOSED"), withMergeable("CONFLICTING")),
		snap(withState("MERGED"), withMergeable("MERGEABLE")),
	)
	if n := len(ofType(flatten(steps), model.EventConflict)); n != 0 {
		t.Fatalf("conflict events while not OPEN: %d", n)
	}
}

func TestCommitsCount(t *testing.T) {
	steps, m := run(
		snap(withCommits("c0", "h1")),
		snap(withHead("h2"), withCommits("c0", "h1", "c2", "h2")),
	)
	if len(steps[0]) != 1 {
		t.Fatalf("want 1 commits event, got %+v", steps[0])
	}
	e := steps[0][0]
	if e.Type != model.EventCommits || e.Key != "commits:1:h2" || e.Count != 2 ||
		e.OldHeadOID != "h1" || e.HeadOID != "h2" || e.ForcePushed {
		t.Fatalf("commits event wrong: %+v", e)
	}
	wantAuthors := []model.Actor{{Login: "a-c2", Type: "User"}, {Login: "a-h2", Type: "User"}}
	if !reflect.DeepEqual(e.Authors, wantAuthors) {
		t.Fatalf("authors %+v, want %+v", e.Authors, wantAuthors)
	}
	if m.HeadOID != "h2" {
		t.Fatalf("memory head %q", m.HeadOID)
	}
	// Same head again: nothing.
	if r := Step(m, snap(withHead("h2"), withCommits("c0", "h1", "c2", "h2"))); len(r.Events) != 0 {
		t.Fatalf("repeat head emitted: %+v", r.Events)
	}
}

func TestForcePush(t *testing.T) {
	// Force-push event targeting the new head.
	steps, m := run(
		snap(withCommits("c0", "h1"), withForcePush("FP0", "x", "h1", "old")),
		snap(withHead("h2"), withCommits("c0", "h2"),
			withForcePush("FP0", "x", "h1", "old"),
			withForcePush("FP1", "h1", "h2", "bob")),
	)
	if len(steps[0]) != 1 {
		t.Fatalf("want 1 event, got %+v", steps[0])
	}
	e := steps[0][0]
	if e.Type != model.EventCommits || !e.ForcePushed || e.Actor.Login != "bob" || e.HeadOID != "h2" || e.OldHeadOID != "h1" {
		t.Fatalf("force-push event wrong: %+v", e)
	}
	if !m.SeenForcePushes["FP1"] || !m.SeenForcePushes["FP0"] {
		t.Fatalf("force pushes not marked seen: %+v", m.SeenForcePushes)
	}

	// Fallback: old head absent from Commits, no force-push event.
	steps, _ = run(
		snap(withCommits("c0", "h1")),
		snap(withHead("h3"), withCommits("c0", "h3")),
	)
	if len(steps[0]) != 1 {
		t.Fatalf("fallback: want 1 event, got %+v", steps[0])
	}
	e = steps[0][0]
	if !e.ForcePushed || e.Actor.Login != "" || e.Count != 0 {
		t.Fatalf("fallback force-push event wrong: %+v", e)
	}
}

func TestHeadChangeResetsCI(t *testing.T) {
	m := Baseline(snap(withCommits("h1")))
	m.FailedReported["ci/test"] = true
	m.SettleCandidate = "fp"
	m.SettledResult = "failed:ci/test"
	m.SettleSeq = 3
	r := Step(m, snap(withHead("h2"), withCommits("h1", "h2")))
	if len(r.Memory.FailedReported) != 0 || r.Memory.SettleCandidate != "" ||
		r.Memory.SettledResult != "" || r.Memory.SettleSeq != 0 {
		t.Fatalf("CI memory not reset on head change: %+v", r.Memory)
	}
	if r.Memory.FailedReported == nil {
		t.Fatal("FailedReported reset to nil, want empty map")
	}
}

func TestEpochKeys(t *testing.T) {
	t.Run("reopen", func(t *testing.T) {
		steps, _ := run(
			snap(),
			snap(withState("CLOSED"), withClosedBy("carol")),
			snap(withState("OPEN")),
			snap(withState("CLOSED"), withClosedBy("carol")),
		)
		closed := ofType(flatten(steps), model.EventClosed)
		if len(closed) != 2 {
			t.Fatalf("want 2 closed events, got %+v", closed)
		}
		if closed[0].Key == closed[1].Key {
			t.Fatalf("closed keys collide: %q", closed[0].Key)
		}
	})
	t.Run("head return", func(t *testing.T) {
		steps, _ := run(
			snap(withHead("h0"), withCommits("h0")),
			snap(withHead("h1"), withCommits("h0", "h1"), withCheck("ci/test", "COMPLETED", "FAILURE", 1)),
			snap(withHead("h2"), withCommits("h0", "h1", "h2")),
			snap(withHead("h1"), withCommits("h0", "h1"), withCheck("ci/test", "COMPLETED", "FAILURE", 1)),
		)
		all := flatten(steps)
		var h1Commits []model.Event
		for _, e := range ofType(all, model.EventCommits) {
			if e.HeadOID == "h1" {
				h1Commits = append(h1Commits, e)
			}
		}
		if len(h1Commits) != 2 || h1Commits[0].Key == h1Commits[1].Key {
			t.Fatalf("commits keys for h1 visits not distinct: %+v", h1Commits)
		}
		f := ofType(all, model.EventCIFailed)
		if len(f) != 2 || f[0].Key == f[1].Key {
			t.Fatalf("ci.failed keys for h1 visits not distinct: %+v", f)
		}
	})
	t.Run("settled head return", func(t *testing.T) {
		pass := withCheck("ci/test", "COMPLETED", "SUCCESS", 1)
		steps, _ := run(
			snap(withCommits("h1")),
			snap(withCommits("h1"), withCheck("ci/test", "IN_PROGRESS", "", 1)),
			snap(withCommits("h1"), pass),
			snap(withCommits("h1"), pass),
			snap(withHead("h2"), withCommits("h1", "h2")),
			snap(withCommits("h1"), pass),
			snap(withCommits("h1"), pass),
		)
		st := ofType(flatten(steps), model.EventCISettled)
		if len(st) != 2 || st[0].Key == st[1].Key {
			t.Fatalf("ci.settled keys for h1 visits not distinct: %+v", st)
		}
	})
	t.Run("baseline epoch", func(t *testing.T) {
		m := Baseline(snap(withCommits("a", "b", "h1")))
		if m.Epoch != 0 || m.CommitsTotal != 3 {
			t.Fatalf("baseline Epoch=%d CommitsTotal=%d, want 0 and 3", m.Epoch, m.CommitsTotal)
		}
	})
}

func TestBigPush(t *testing.T) {
	visible := make([]string, 30)
	for i := range visible {
		visible[i] = fmt.Sprintf("n%02d", i)
	}
	visible[29] = "h40"
	base := snap(withHead("h5"), withCommits("h1", "h2", "h3", "h4", "h5"))

	t.Run("fast-forward beyond window", func(t *testing.T) {
		steps, m := run(base, snap(withHead("h40"), withCommits(visible...), withCommitsTotal(40)))
		c := ofType(flatten(steps), model.EventCommits)
		if len(c) != 1 {
			t.Fatalf("want 1 commits event, got %+v", c)
		}
		e := c[0]
		if e.ForcePushed || e.Count != 35 || len(e.Authors) != 30 || e.Authors[0].Login != "a-n00" {
			t.Fatalf("big push mislabelled: %+v", e)
		}
		if m.CommitsTotal != 40 {
			t.Fatalf("CommitsTotal = %d, want 40", m.CommitsTotal)
		}
	})
	t.Run("total grew less than window", func(t *testing.T) {
		steps, _ := run(base, snap(withHead("h40"), withCommits(visible...), withCommitsTotal(6)))
		c := ofType(flatten(steps), model.EventCommits)
		if len(c) != 1 || !c[0].ForcePushed {
			t.Fatalf("want force-push, got %+v", c)
		}
	})
}
