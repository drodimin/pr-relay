// Package diff turns successive PR snapshots into typed events. It is a pure,
// deterministic state machine: Step takes the Memory carried from the previous
// poll plus a new Snapshot and returns the events and the next Memory. It does
// no I/O, reads no clock and never formats text (render owns all wording).
package diff

import (
	"sort"

	"github.com/drodimin/pr-relay/internal/model"
)

// Result is the output of one Step.
type Result struct {
	Events []model.Event
	Memory model.Memory
	// Gap reports that more activity arrived than the snapshot window holds
	// (set by stepActivity).
	Gap bool
}

// Baseline records the current state of s silently. A Step on an identical
// snapshot afterwards emits nothing.
func Baseline(s model.Snapshot) model.Memory {
	m := model.Memory{}.Clone() // non-nil empty maps
	m.Baselined = true
	m.HeadOID = s.HeadOID
	m.BaseOID = s.BaseOID
	m.State = s.State
	if s.Mergeable == "MERGEABLE" || s.Mergeable == "CONFLICTING" {
		m.Mergeable = s.Mergeable
	}
	for _, fp := range s.ForcePushes {
		m.SeenForcePushes[fp.ID] = true
	}
	m.ReviewsTotal = s.ReviewsTotal
	m.CommentsTotal = s.CommentsTotal
	for _, r := range s.Reviews {
		if r.State == "PENDING" {
			continue
		}
		m.SeenReviews[r.ID] = true
		for _, rc := range r.Comments {
			m.SeenComments[rc.ID] = true
		}
	}
	for _, c := range s.Comments {
		m.SeenComments[c.ID] = true
	}
	baselineCI(&m, s)
	return m
}

// Step advances m by one snapshot. It never mutates m: all work happens on a
// deep clone. An un-baselined Memory is baselined silently.
func Step(m model.Memory, s model.Snapshot) Result {
	if !m.Baselined {
		return Result{Memory: Baseline(s)}
	}
	next := m.Clone()
	var events []model.Event
	events = append(events, stepLifecycle(&next, s)...)
	events = append(events, stepCI(&next, s)...)
	// activity: reviews and comments (plan 01-06)
	ev, gap := stepActivity(&next, s)
	events = append(events, ev...)
	sortEvents(events)
	return Result{Events: events, Memory: next, Gap: gap}
}

// sortEvents orders events by type rank (closed, conflict, commits, ci.failed,
// ci.settled, ...) and then by Key.
func sortEvents(evs []model.Event) {
	sort.SliceStable(evs, func(i, j int) bool {
		ri, rj := evs[i].Type.Rank(), evs[j].Type.Rank()
		if ri != rj {
			return ri < rj
		}
		return evs[i].Key < evs[j].Key
	})
}
