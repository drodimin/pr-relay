package diff

import (
	"fmt"
	"testing"

	"github.com/drodimin/pr-relay/internal/model"
)

func user(login string) model.Actor { return model.Actor{Login: login, Type: "User"} }

// rc builds an inline review comment. replyTo "" means a non-reply comment.
func rc(id, path, replyTo, login string) model.ReviewComment {
	return model.ReviewComment{
		ID: id, URL: "https://github.com/o/r/pull/1#discussion_r" + id,
		Path: path, ReplyToID: replyTo, Author: user(login),
	}
}

// withReview appends a review and bumps ReviewsTotal by one.
func withReview(id, state, login string, hasBody bool, comments ...model.ReviewComment) func(*model.Snapshot) {
	return withReviewBy(id, state, user(login), hasBody, comments...)
}

func withReviewBy(id, state string, author model.Actor, hasBody bool, comments ...model.ReviewComment) func(*model.Snapshot) {
	return func(s *model.Snapshot) {
		s.Reviews = append(s.Reviews, model.Review{
			ID: id, State: state, URL: "https://github.com/o/r/pull/1#pullrequestreview-" + id,
			Author: author, HasBody: hasBody, Comments: comments, CommentsTotal: len(comments),
		})
		s.ReviewsTotal++
	}
}

// withIssueComment appends an issue comment and bumps CommentsTotal by one.
func withIssueComment(id, login string) func(*model.Snapshot) {
	return func(s *model.Snapshot) {
		s.Comments = append(s.Comments, model.IssueComment{
			ID: id, URL: "https://github.com/o/r/pull/1#issuecomment-" + id, Author: user(login),
		})
		s.CommentsTotal++
	}
}

func withTotals(reviews, comments int) func(*model.Snapshot) {
	return func(s *model.Snapshot) { s.ReviewsTotal, s.CommentsTotal = reviews, comments }
}

func TestReviewInlineGrouped(t *testing.T) {
	cs := []model.ReviewComment{rc("c1", "a.go", "", "alice"), rc("c2", "a.go", "", "alice"), rc("c3", "b.go", "", "alice"), rc("c4", "c.go", "", "alice")}
	steps, m := run(snap(), snap(withReview("R1", "CHANGES_REQUESTED", "alice", false, cs...)))
	evs := steps[0]
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %+v", evs)
	}
	e := evs[0]
	if e.Type != model.EventReview || e.Verdict != "CHANGES_REQUESTED" || e.Actor != user("alice") ||
		e.InlineCount != 4 || e.URL != "https://github.com/o/r/pull/1#pullrequestreview-R1" || e.Key != "review:R1" {
		t.Fatalf("review event wrong: %+v", e)
	}
	if n := len(ofType(evs, model.EventComment)); n != 0 {
		t.Fatalf("inline comments emitted %d comment events", n)
	}
	for _, c := range cs {
		if !m.SeenComments[c.ID] {
			t.Fatalf("comment %s not marked seen", c.ID)
		}
	}
	if !m.SeenReviews["R1"] {
		t.Fatal("review not marked seen")
	}

	// InlineCount falls back to len(Comments) when CommentsTotal is 0, and
	// prefers CommentsTotal when set.
	s := snap(withReview("R2", "COMMENTED", "alice", false, cs[:2]...))
	s.Reviews[0].CommentsTotal = 0
	steps, _ = run(snap(), s)
	if steps[0][0].InlineCount != 2 {
		t.Fatalf("fallback InlineCount: %+v", steps[0][0])
	}
	s = snap(withReview("R3", "COMMENTED", "alice", false, cs[:2]...))
	s.Reviews[0].CommentsTotal = 7
	steps, _ = run(snap(), s)
	if steps[0][0].InlineCount != 7 {
		t.Fatalf("CommentsTotal InlineCount: %+v", steps[0][0])
	}
}

func TestReviewApproved(t *testing.T) {
	steps, _ := run(snap(), snap(withReview("R1", "APPROVED", "bob", false)))
	if len(steps[0]) != 1 {
		t.Fatalf("want 1 event, got %+v", steps[0])
	}
	e := steps[0][0]
	if e.Type != model.EventReview || e.Verdict != "APPROVED" || e.InlineCount != 0 || e.Key != "review:R1" {
		t.Fatalf("approved event wrong: %+v", e)
	}
}

func TestRealReviewRule(t *testing.T) {
	reply := rc("c1", "a.go", "c0", "alice")
	cases := []struct {
		name  string
		opt   func(*model.Snapshot)
		isRev bool
	}{
		{"approved", withReview("R", "APPROVED", "alice", false), true},
		{"changes", withReview("R", "CHANGES_REQUESTED", "alice", false), true},
		{"dismissed", withReview("R", "DISMISSED", "alice", false), true},
		{"commented with body", withReview("R", "COMMENTED", "alice", true, reply), true},
		{"commented with non-reply inline", withReview("R", "COMMENTED", "alice", false, reply, rc("c2", "b.go", "", "alice")), true},
		{"commented reply-only no body", withReview("R", "COMMENTED", "alice", false, reply), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps, _ := run(snap(), snap(tc.opt))
			got := len(ofType(steps[0], model.EventReview)) == 1
			if got != tc.isRev {
				t.Fatalf("real review = %v, want %v: %+v", got, tc.isRev, steps[0])
			}
		})
	}
}

func TestReplyWrapper(t *testing.T) {
	steps, m := run(snap(), snap(withReview("R1", "COMMENTED", "alice", false,
		rc("c1", "a.go", "c0", "alice"), rc("c2", "b.go", "c9", "alice"))))
	evs := steps[0]
	if len(ofType(evs, model.EventReview)) != 0 {
		t.Fatalf("reply wrapper emitted review: %+v", evs)
	}
	cm := ofType(evs, model.EventComment)
	if len(cm) != 2 {
		t.Fatalf("want 2 comment events, got %+v", evs)
	}
	e := cm[0]
	if e.CommentKind != "reply" || e.Path != "a.go" || e.URL != "https://github.com/o/r/pull/1#discussion_rc1" ||
		e.Actor != user("alice") || e.Key != "comment:c1" {
		t.Fatalf("reply event wrong: %+v", e)
	}
	if cm[1].Key != "comment:c2" || cm[1].Path != "b.go" {
		t.Fatalf("second reply wrong: %+v", cm[1])
	}
	if !m.SeenReviews["R1"] || !m.SeenComments["c1"] || !m.SeenComments["c2"] {
		t.Fatalf("seen sets not updated: %+v %+v", m.SeenReviews, m.SeenComments)
	}
}

func TestLateCommentOnSeenReview(t *testing.T) {
	base := snap(withReview("R1", "COMMENTED", "alice", true, rc("c1", "a.go", "", "alice")))
	later := snap(withReview("R1", "COMMENTED", "alice", true,
		rc("c1", "a.go", "", "alice"), rc("c2", "a.go", "c1", "bob"), rc("c3", "z.go", "", "carol")))
	steps, _ := run(base, later)
	evs := steps[0]
	if len(evs) != 2 || len(ofType(evs, model.EventComment)) != 2 {
		t.Fatalf("want 2 comment events, got %+v", evs)
	}
	if evs[0].Key != "comment:c2" || evs[0].CommentKind != "reply" || evs[0].Path != "a.go" || evs[0].Actor != user("bob") {
		t.Fatalf("late reply wrong: %+v", evs[0])
	}
	if evs[1].Key != "comment:c3" || evs[1].CommentKind != "inline" || evs[1].Path != "z.go" || evs[1].Actor != user("carol") {
		t.Fatalf("late inline wrong: %+v", evs[1])
	}
}

func TestIssueComment(t *testing.T) {
	steps, m := run(snap(withIssueComment("I1", "alice")), snap(withIssueComment("I1", "alice"), withIssueComment("I2", "bob")))
	if len(steps[0]) != 1 {
		t.Fatalf("want 1 event, got %+v", steps[0])
	}
	e := steps[0][0]
	if e.Type != model.EventComment || e.CommentKind != "comment" || e.Key != "comment:I2" ||
		e.URL != "https://github.com/o/r/pull/1#issuecomment-I2" || e.Actor != user("bob") {
		t.Fatalf("issue comment wrong: %+v", e)
	}
	if !m.SeenComments["I2"] {
		t.Fatal("issue comment not marked seen")
	}
}

func TestPendingReview(t *testing.T) {
	steps, m := run(
		snap(),
		snap(withReview("R1", "PENDING", "alice", true, rc("c1", "a.go", "", "alice"))),
	)
	if len(steps[0]) != 0 {
		t.Fatalf("pending emitted: %+v", steps[0])
	}
	if m.SeenReviews["R1"] || m.SeenComments["c1"] {
		t.Fatal("pending review or its comments marked seen")
	}
	r := Step(m, snap(withReview("R1", "COMMENTED", "alice", true, rc("c1", "a.go", "", "alice"))))
	if len(r.Events) != 1 || r.Events[0].Key != "review:R1" || r.Events[0].Verdict != "COMMENTED" {
		t.Fatalf("submitted review: %+v", r.Events)
	}
	r = Step(r.Memory, snap(withReview("R1", "COMMENTED", "alice", true, rc("c1", "a.go", "", "alice"))))
	if len(r.Events) != 0 {
		t.Fatalf("submitted review fired twice: %+v", r.Events)
	}

	// A pending review present at baseline is not seen either.
	m = Baseline(snap(withReview("R2", "PENDING", "alice", false)))
	r = Step(m, snap(withReview("R2", "APPROVED", "alice", false)))
	if len(r.Events) != 1 || r.Events[0].Key != "review:R2" {
		t.Fatalf("baseline pending then approved: %+v", r.Events)
	}
}

func TestEditsIgnored(t *testing.T) {
	base := snap(
		withReview("R1", "CHANGES_REQUESTED", "alice", false, rc("c1", "a.go", "", "alice")),
		withIssueComment("I1", "bob"),
	)
	edited := snap(
		withReview("R1", "DISMISSED", "alice", true, rc("c1", "b.go", "", "alice")),
		withIssueComment("I1", "bob"),
	)
	edited.Reviews[0].URL = "https://github.com/o/r/pull/1#pullrequestreview-R1-edited"
	edited.Reviews[0].Comments[0].URL = "https://github.com/o/r/pull/1#discussion_rc1-edited"
	edited.Comments[0].URL = "https://github.com/o/r/pull/1#issuecomment-I1-edited"
	steps, _ := run(base, edited, edited)
	if n := len(flatten(steps)); n != 0 {
		t.Fatalf("edits emitted %d events: %+v", n, flatten(steps))
	}
}

func TestDeletionsIgnored(t *testing.T) {
	base := snap(
		withReview("R1", "APPROVED", "alice", false, rc("c1", "a.go", "", "alice")),
		withIssueComment("I1", "bob"),
		withIssueComment("I2", "bob"),
	)
	gone := snap(withIssueComment("I2", "bob"), withTotals(0, 1))
	steps, _ := run(base, gone, base)
	if n := len(flatten(steps)); n != 0 {
		t.Fatalf("deletions (and reappearance) emitted %d events: %+v", n, flatten(steps))
	}
}

func TestGap(t *testing.T) {
	// ReviewsTotal grows by 3, only one new review ID is visible.
	m := Baseline(snap(withReview("R1", "APPROVED", "alice", false)))
	s := snap(withReview("R2", "APPROVED", "bob", false), withTotals(4, 0))
	r := Step(m, s)
	if !r.Gap {
		t.Fatal("review window gap not flagged")
	}
	if len(r.Events) != 1 || r.Events[0].Key != "review:R2" {
		t.Fatalf("gap must not add events: %+v", r.Events)
	}
	if r.Memory.ReviewsTotal != 4 {
		t.Fatalf("ReviewsTotal not updated: %d", r.Memory.ReviewsTotal)
	}

	// Comment gap.
	m = Baseline(snap(withIssueComment("I1", "a")))
	s = snap(withIssueComment("I1", "a"), withIssueComment("I2", "b"), withTotals(0, 5))
	r = Step(m, s)
	if !r.Gap || r.Memory.CommentsTotal != 5 || len(r.Events) != 1 {
		t.Fatalf("comment gap: gap=%v total=%d events=%+v", r.Gap, r.Memory.CommentsTotal, r.Events)
	}
	// After the gap step, steady state is not a gap.
	r = Step(r.Memory, s)
	if r.Gap || len(r.Events) != 0 {
		t.Fatalf("steady state after gap: gap=%v events=%+v", r.Gap, r.Events)
	}

	// Normal growth is not a gap, and a shrinking window (old items fall out
	// of last:N) is not a gap either.
	var opts []func(*model.Snapshot)
	for i := 0; i < 3; i++ {
		opts = append(opts, withIssueComment(fmt.Sprintf("I%d", i), "a"))
	}
	m = Baseline(snap(opts...))
	r = Step(m, snap(append(opts, withIssueComment("I9", "b"), withReview("R1", "APPROVED", "c", false))...))
	if r.Gap {
		t.Fatal("normal growth flagged as gap")
	}
	r = Step(r.Memory, snap(withIssueComment("I9", "b"), withTotals(1, 4)))
	if r.Gap || len(r.Events) != 0 {
		t.Fatalf("window slide: gap=%v events=%+v", r.Gap, r.Events)
	}
}

func TestBotReview(t *testing.T) {
	bot := model.Actor{Login: "github-actions", Type: "Bot"}
	steps, _ := run(snap(), snap(withReviewBy("R1", "CHANGES_REQUESTED", bot, false, rc("c1", "a.go", "", "github-actions"))))
	if len(steps[0]) != 1 {
		t.Fatalf("want 1 event, got %+v", steps[0])
	}
	e := steps[0][0]
	if e.Type != model.EventReview || e.Verdict != "CHANGES_REQUESTED" || e.Actor != bot || e.InlineCount != 1 || e.Key != "review:R1" {
		t.Fatalf("bot review wrong: %+v", e)
	}
}
