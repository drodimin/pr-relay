package compose

import (
	"sort"

	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/store"
)

// View is the current polled state of a PR, copied by the caller from the
// PR's Memory. The zero View means "unknown" and disables rules R1 and R4's
// state check (R8).
type View struct {
	HeadOID   string
	Mergeable string
	State     string
	Baselined bool
}

// Supersede returns a new slice holding the events that are still current
// for view, in Seq order (see the package doc for rules R1-R8). The input is
// not modified.
func Supersede(view View, events []store.PendingEvent) []store.PendingEvent {
	in := make([]store.PendingEvent, len(events))
	copy(in, events)
	sort.SliceStable(in, func(i, j int) bool { return in[i].Seq < in[j].Seq })

	stale := func(e model.Event) bool {
		return view.HeadOID != "" && e.HeadOID != "" && e.HeadOID != view.HeadOID
	}

	// Highest ci.settled Seq per head among the current-head events.
	lastSettled := map[string]uint64{}
	var conflicts []int
	for i, p := range in {
		switch p.Event.Type {
		case model.EventCISettled:
			if !stale(p.Event) && p.Seq > lastSettled[p.Event.HeadOID] {
				lastSettled[p.Event.HeadOID] = p.Seq
			}
		case model.EventConflict:
			conflicts = append(conflicts, i)
		}
	}

	// keepConflict is the index of the one conflict event kept by R4, or -1.
	keepConflict := -1
	if n := len(conflicts); n > 0 && (view.State == "" || view.State == "OPEN") {
		prior := !in[conflicts[0]].Event.Conflicting
		if last := conflicts[n-1]; in[last].Event.Conflicting != prior {
			keepConflict = last
		}
	}

	out := make([]store.PendingEvent, 0, len(in))
	for i, p := range in {
		e := p.Event
		switch e.Type {
		case model.EventCIFailed:
			if stale(e) {
				continue // R1
			}
			if s, ok := lastSettled[e.HeadOID]; ok && p.Seq < s {
				continue // R2
			}
		case model.EventCISettled:
			if stale(e) {
				continue // R1
			}
			if p.Seq < lastSettled[e.HeadOID] {
				continue // R3
			}
		case model.EventConflict:
			if i != keepConflict {
				continue // R4
			}
		default:
			// R5, R6: commits, review, comment, closed and unknown types are kept.
		}
		out = append(out, p)
	}
	return out
}
