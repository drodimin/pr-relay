// Package filter holds pure event filters applied after diff. Keeping them out
// of diff keeps snapshots and Memory user-agnostic, so a later --include-own
// option (FILT-01) can simply skip the filter.
//
// ExcludeSelf rules (EVT-08, D-12, D-14, D-20):
//   - Logins are compared case-insensitively and only for actors whose Type is
//     "User". Bots, ghosts, mannequins and unknown actors are never treated as
//     self, so bots get the same treatment as other humans (D-12).
//   - review and comment events by self are dropped.
//   - commits events are dropped when the force-push actor is self, or, when
//     there is no force-push actor, when every listed author is self. Any
//     unknown or foreign author, or an empty author list, keeps the event: a
//     false notification is safer than a silent miss.
//   - closed is always delivered, even when self merged or closed the PR
//     (D-20). conflict, ci.failed and ci.settled are never activity by a user
//     and are always delivered.
package filter

import (
	"strings"

	"github.com/drodimin/pr-relay/internal/model"
)

// ExcludeSelf returns a new slice holding the events of events that are not
// the authenticated user's own activity, in their original order. The input is
// not modified. An empty self returns a copy of events.
func ExcludeSelf(events []model.Event, self string) []model.Event {
	out := make([]model.Event, 0, len(events))
	for _, e := range events {
		if self != "" && isOwn(e, self) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func isSelf(a model.Actor, self string) bool {
	return a.IsUser() && a.Login != "" && strings.EqualFold(a.Login, self)
}

func isOwn(e model.Event, self string) bool {
	switch e.Type {
	case model.EventReview, model.EventComment:
		return isSelf(e.Actor, self)
	case model.EventCommits:
		if e.ForcePushed && e.Actor.Login != "" {
			return isSelf(e.Actor, self)
		}
		if len(e.Authors) == 0 {
			return false
		}
		for _, a := range e.Authors {
			if !isSelf(a, self) {
				return false
			}
		}
		return true
	default:
		// closed (D-20), conflict, ci.failed, ci.settled.
		return false
	}
}
