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
// Watermarks (CR-01): CommentsWatermark is the newest issue-comment createdAt
// seen so far and ReviewsWatermark the newest submittedAt of a non-PENDING
// review. Deleting a recent item lets an older one slide into the snapshot
// window with an ID never seen before. An unseen item strictly older than the
// watermark is therefore history: it is marked seen and emits nothing (a
// review skipped this way also has its inline comments marked seen). The
// comparison is strict because GitHub timestamps have one-second resolution,
// so a genuinely new item in the same second as the watermark still fires
// (pitfall 4). Items with a zero (missing) timestamp always fire. PENDING
// reviews never move or consult the review watermark. Watermarks advance
// after the comparison loop, to the max of themselves and every visible
// timestamp.
//
// Gap: when ReviewsTotal or CommentsTotal grows by more than the number of new
// IDs visible in the snapshot window, gap is true and no extra event is
// emitted. The same applies per review (WR-06): when a seen review's
// CommentsTotal grows by more than its newly visible comment IDs, gap is true.
// Items skipped by a watermark do not count as new. Totals are updated on
// every step. On a gap the poller re-fetches with wide windows before it
// commits the step (plan 02-09, RESEARCH Pattern 8).
//
// Deferred (RESEARCH open question 2): SeenReviews and SeenComments are never
// pruned. They grow with PR activity (tens to hundreds of IDs per PR).
func stepActivity(m *model.Memory, s model.Snapshot) (events []model.Event, gap bool) {
	if m.ReviewCommentTotals == nil {
		m.ReviewCommentTotals = map[string]int{}
	}
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
		prevTotal, known := m.ReviewCommentTotals[r.ID]
		m.ReviewCommentTotals[r.ID] = r.CommentsTotal
		if m.SeenReviews[r.ID] {
			late := lateComments(m, r)
			// WR-06: more inline comments arrived than are visible.
			if known && r.CommentsTotal-prevTotal > len(late) {
				gap = true
			}
			events = append(events, late...)
			continue
		}
		m.SeenReviews[r.ID] = true
		if !r.SubmittedAt.IsZero() && r.SubmittedAt.Before(m.ReviewsWatermark) {
			// CR-01: an old review slid into the window. History, not news.
			for _, c := range r.Comments {
				m.SeenComments[c.ID] = true
			}
			continue
		}
		newReviews++
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

	for _, r := range s.Reviews {
		if r.State != "PENDING" && r.SubmittedAt.After(m.ReviewsWatermark) {
			m.ReviewsWatermark = r.SubmittedAt
		}
	}

	newComments := 0
	for _, c := range s.Comments {
		if m.SeenComments[c.ID] {
			continue
		}
		m.SeenComments[c.ID] = true
		if !c.CreatedAt.IsZero() && c.CreatedAt.Before(m.CommentsWatermark) {
			// CR-01: an old comment slid into the window after a deletion.
			continue
		}
		newComments++
		events = append(events, model.Event{
			Type:        model.EventComment,
			Key:         "comment:" + c.ID,
			Actor:       c.Author,
			URL:         c.URL,
			CommentKind: "comment",
		})
	}

	for _, c := range s.Comments {
		if c.CreatedAt.After(m.CommentsWatermark) {
			m.CommentsWatermark = c.CreatedAt
		}
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
