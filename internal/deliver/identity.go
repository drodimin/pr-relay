package deliver

import (
	"github.com/drodimin/pr-relay/internal/herdr"
	"github.com/drodimin/pr-relay/internal/store"
)

// identity is the D-13 verdict on whether the agent now in a pane is the
// one that subscribed.
type identity int

const (
	// identMatch: the subscriber is still there; deliver.
	identMatch identity = iota
	// identMismatch: a different agent is in the pane; cancel the sub.
	identMismatch
	// identUnidentified: herdr has not identified the session yet; hold.
	identUnidentified
)

func (i identity) String() string {
	switch i {
	case identMatch:
		return "match"
	case identMismatch:
		return "mismatch"
	case identUnidentified:
		return "unidentified"
	default:
		return "invalid"
	}
}

// checkIdentity applies D-13 to one subscription:
//
//   - a stored session must equal the current agent_session value; a
//     current session that is not known yet is "unidentified" (hold, A3)
//     unless the agent kind already differs from the stored kind;
//   - without a stored session, the stored agent kind must equal the
//     current agent kind;
//   - a sub with neither (legacy or dev) matches any agent (A4).
func checkIdentity(sub store.Subscription, info herdr.AgentInfo) identity {
	if sub.AgentSession != "" {
		if info.AgentSession != nil && info.AgentSession.Value != "" {
			if info.AgentSession.Value == sub.AgentSession {
				return identMatch
			}
			return identMismatch
		}
		if sub.AgentKind != "" && info.Agent != sub.AgentKind {
			return identMismatch
		}
		return identUnidentified
	}
	if sub.AgentKind != "" && info.Agent != sub.AgentKind {
		return identMismatch
	}
	return identMatch
}
