package diff

import "github.com/drodimin/pr-relay/internal/model"

// stepActivity detects new reviews and comments (D-10, D-11, D-13).
//
// Rules:
//   - Only IDs never seen before fire. Edits (changed state, URL, body) and
//     deletions of known IDs are ignored (D-13).
//   - A PENDING review never fires and is not marked seen, so it fires once
//     when it is later submitted.
//   - A new "real review" (APPROVED, CHANGES_REQUESTED or DISMISSED, or it has
//     a body, or it has at least one non-reply inline comment) emits one review
//     event. Its inline comments are marked seen and emit nothing (D-10).
//   - A new "reply wrapper" (COMMENTED, no body, every inline comment is a
//     reply) emits one comment event per reply, kind "reply" (D-11).
//   - A new comment ID on an already-seen review emits one comment event, kind
//     "reply" or "inline".
//   - A new issue comment emits one comment event, kind "comment".
//
// Assumption A1 (RESEARCH): replying in a review thread creates a new
// COMMENTED review that wraps the reply. Plan 01-09 confirms this against real
// fixtures. If replies turn out to arrive without a wrapper review, the
// "new comment on an already-seen review" path already covers them.
//
// Gap: when ReviewsTotal or CommentsTotal grows by more than the number of new
// IDs visible in the snapshot window, gap is true and no extra event is
// emitted. Phase 2 paginates on it. Totals are updated on every step.
//
// Deferred to Phase 2 (persistence owns Memory size): SeenReviews and
// SeenComments are never pruned. They grow with PR activity (tens to hundreds
// of IDs per PR).
func stepActivity(m *model.Memory, s model.Snapshot) (events []model.Event, gap bool) {
	newReviews := 0
	for _, r := range s.Reviews {
		if r.State == "PENDING" {
			// Still count it as visible for gap purposes: the total may
			// include the viewer's own pending review.
			if !m.SeenReviews[r.ID] {
				newReviews++
			}
			continue
		}
		if m.SeenReviews[r.ID] {
			events = append(events, lateComments(m, r)...)
			continue
		}
		newReviews++
		m.SeenReviews[r.ID] = true
		if isRealReview(r) {
			for _, c := range r.Comments {
				m.SeenComments[c.ID] = true
			}
			n := r.CommentsTotal
			if n == 0 {
				n = len(r.Comments)
			}
			events = append(events, model.Event{
				Type:        model.EventReview,
				Key:         "review:" + r.ID,
				Actor:       r.Author,
				URL:         r.URL,
				Verdict:     r.State,
				InlineCount: n,
			})
			continue
		}
		// Reply wrapper: one line per reply.
		events = append(events, lateComments(m, r)...)
	}

	newComments := 0
	for _, c := range s.Comments {
		if m.SeenComments[c.ID] {
			continue
		}
		newComments++
		m.SeenComments[c.ID] = true
		events = append(events, model.Event{
			Type:        model.EventComment,
			Key:         "comment:" + c.ID,
			Actor:       c.Author,
			URL:         c.URL,
			CommentKind: "comment",
		})
	}

	if s.ReviewsTotal-m.ReviewsTotal > newReviews || s.CommentsTotal-m.CommentsTotal > newComments {
		gap = true
	}
	m.ReviewsTotal = s.ReviewsTotal
	m.CommentsTotal = s.CommentsTotal
	return events, gap
}

// isRealReview reports whether a new non-PENDING review deserves a review
// line rather than per-reply comment lines.
func isRealReview(r model.Review) bool {
	switch r.State {
	case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
		return true
	}
	if r.HasBody {
		return true
	}
	for _, c := range r.Comments {
		if c.ReplyToID == "" {
			return true
		}
	}
	return false
}

// lateComments emits one comment event per unseen inline comment of r and
// marks them seen. Kind is "reply" when the comment replies to another, else
// "inline".
func lateComments(m *model.Memory, r model.Review) []model.Event {
	var out []model.Event
	for _, c := range r.Comments {
		if m.SeenComments[c.ID] {
			continue
		}
		m.SeenComments[c.ID] = true
		kind := "inline"
		if c.ReplyToID != "" {
			kind = "reply"
		}
		out = append(out, model.Event{
			Type:        model.EventComment,
			Key:         "comment:" + c.ID,
			Actor:       c.Author,
			URL:         c.URL,
			CommentKind: kind,
			Path:        c.Path,
		})
	}
	return out
}
