package diff

import "github.com/drodimin/pr-relay/internal/model"

const (
	stateOpen   = "OPEN"
	stateMerged = "MERGED"
	stateClosed = "CLOSED"

	mergeableUnknown     = "UNKNOWN"
	mergeableConflicting = "CONFLICTING"
	mergeableMergeable   = "MERGEABLE"
)

// stepLifecycle detects closed, conflict and commits/force-push events and
// updates the lifecycle fields of m. A head change resets per-head CI memory
// before the CI step runs.
func stepLifecycle(m *model.Memory, s model.Snapshot) []model.Event {
	var evs []model.Event

	// closed: OPEN -> MERGED|CLOSED. Never self-filtered (D-20).
	if m.State == stateOpen && (s.State == stateMerged || s.State == stateClosed) {
		e := model.Event{Type: model.EventClosed, Key: "closed:" + s.State, Merged: s.State == stateMerged}
		if e.Merged {
			e.Actor = s.MergedBy
		} else {
			e.Actor = s.ClosedBy
		}
		evs = append(evs, e)
	}
	m.State = s.State

	// conflict: only resolved values count; UNKNOWN is ignored (EVT-06).
	if s.State == stateOpen && (s.Mergeable == mergeableConflicting || s.Mergeable == mergeableMergeable) &&
		s.Mergeable != m.Mergeable {
		// D-21: from an unresolved baseline ("") a first CONFLICTING is
		// announced, a first MERGEABLE is adopted silently.
		if m.Mergeable != "" || s.Mergeable == mergeableConflicting {
			evs = append(evs, model.Event{
				Type:        model.EventConflict,
				Key:         "conflict:" + s.HeadOID + ":" + s.BaseOID + ":" + s.Mergeable,
				Conflicting: s.Mergeable == mergeableConflicting,
				HeadOID:     s.HeadOID,
			})
		}
		m.Mergeable = s.Mergeable
	}

	// commits / force-push (EVT-05).
	var newPush *model.ForcePush
	for i := range s.ForcePushes {
		fp := &s.ForcePushes[i]
		if m.SeenForcePushes[fp.ID] {
			continue
		}
		m.SeenForcePushes[fp.ID] = true
		if fp.AfterOID == s.HeadOID {
			newPush = fp
		}
	}
	if s.HeadOID != "" && s.HeadOID != m.HeadOID {
		e := model.Event{
			Type:       model.EventCommits,
			Key:        "commits:" + s.HeadOID,
			HeadOID:    s.HeadOID,
			OldHeadOID: m.HeadOID,
		}
		idx := -1
		for i, c := range s.Commits {
			if c.OID == m.HeadOID {
				idx = i
			}
		}
		if idx >= 0 {
			e.Count = len(s.Commits) - 1 - idx
			for _, c := range s.Commits[idx+1:] {
				e.Authors = append(e.Authors, c.Author)
			}
		}
		if newPush != nil {
			e.ForcePushed = true
			e.Actor = newPush.Actor
		} else if idx < 0 {
			// Old head no longer in history: treat as a force-push with an
			// unknown pusher and an unknown count.
			e.ForcePushed = true
		}
		evs = append(evs, e)

		m.HeadOID = s.HeadOID
		m.FailedReported = map[string]bool{}
		m.SettleCandidate = ""
		m.SettledResult = ""
		m.SettleSeq = 0
	}
	m.BaseOID = s.BaseOID

	return evs
}
