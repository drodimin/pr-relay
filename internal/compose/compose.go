package compose

import (
	"sort"
	"strings"

	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/render"
	"github.com/drodimin/pr-relay/internal/store"
)

// DefaultMaxBytes is the D-12 flood guard: below the 16 KiB herdr accepted
// in S6; meant to almost never fire.
const DefaultMaxBytes = 12 << 10

// Group is the pending outbox of one subscription of the pane.
type Group struct {
	SubID string
	Ref   model.PRRef
	// Events are the pending events in Seq order.
	Events []store.PendingEvent
	// Dropped and DroppedSeq are the Phase 2 drop marker (rule R7).
	Dropped    int
	DroppedSeq uint64
	// UpToSeq is the Seq that acks the whole group, drop marker included.
	UpToSeq uint64
	View    View
}

// Limits bounds one message. The zero value uses DefaultMaxBytes.
type Limits struct {
	MaxBytes int
}

// Result is one composed prompt and the acks that clear what it carries.
type Result struct {
	// Text is the lines joined by "\n", with no header and no trailing
	// newline. It is empty when every event was superseded.
	Text string
	// Acks maps SubID to the upToSeq to ack after the prompt is delivered.
	// A group none of whose lines made it into Text has no entry.
	Acks map[string]uint64
	// Lines is the number of lines in Text, the held note included.
	Lines int
	// Held is the number of rendered lines held back for the next delivery.
	Held int
}

// item is one candidate line. line is "" for an event that renders nothing
// (unknown type); such an item costs no bytes and is acked like the rest.
type item struct {
	group int
	line  string
	seq   uint64 // event Seq; 0 for the drop note
	drop  bool
}

// Message coalesces every pending event of one pane into a single prompt
// (DLV-04). Groups are ordered by their lowest pending Seq (the drop marker
// counts), ties broken by SubID; within a group events keep Seq order after
// Supersede. When the joined text would exceed lim.MaxBytes, whole lines are
// added in order until the next one does not fit, the rest are held, and a
// render.HeldLine note ends the message (D-12). Only what was included is
// acked, so nothing is lost. The input is not modified.
func Message(groups []Group, lim Limits) Result {
	max := lim.MaxBytes
	if max <= 0 {
		max = DefaultMaxBytes
	}

	gs := make([]Group, len(groups))
	copy(gs, groups)
	sort.SliceStable(gs, func(i, j int) bool {
		ki, kj := orderKey(gs[i]), orderKey(gs[j])
		if ki != kj {
			return ki < kj
		}
		return gs[i].SubID < gs[j].SubID
	})

	var items []item
	total := 0 // renderable lines
	for gi, g := range gs {
		if g.Dropped > 0 {
			items = append(items, item{group: gi, line: render.DroppedLine(g.Ref, g.Dropped), drop: true})
		}
		kept := Supersede(g.View, g.Events)
		evs := make([]model.Event, len(kept))
		for i, p := range kept {
			evs[i] = p.Event
		}
		rendered := render.Each(g.Ref, evs)
		for i, p := range kept {
			var l string
			if i < len(rendered) {
				l = rendered[i]
			}
			items = append(items, item{group: gi, line: l, seq: p.Seq})
		}
	}
	for _, it := range items {
		if it.line != "" {
			total++
		}
	}

	// Without overflow everything is included. With overflow, reserve room
	// for the held note sized for the worst-case count.
	budget := max
	if joinedLen(items) > max {
		budget = max - len(render.HeldLine(total)) - 1
	}

	var lines []string
	size := 0
	cut := len(items) // index of the first held item
	for i, it := range items {
		if it.line == "" {
			continue
		}
		add := len(it.line)
		if len(lines) > 0 {
			add++
		}
		if size+add > budget {
			cut = i
			break
		}
		size += add
		lines = append(lines, it.line)
	}

	held := 0
	for _, it := range items[cut:] {
		if it.line != "" {
			held++
		}
	}
	if held > 0 {
		lines = append(lines, render.HeldLine(held))
	}

	acks := map[string]uint64{}
	firstHeld := map[int]item{}
	for _, it := range items[cut:] {
		if _, ok := firstHeld[it.group]; !ok {
			firstHeld[it.group] = it
		}
	}
	for gi, g := range gs {
		h, partial := firstHeld[gi]
		switch {
		case !partial:
			acks[g.SubID] = g.UpToSeq
		case h.drop || isFirst(items, cut, gi):
			// Nothing of this group made it in; it is delivered next time.
		default:
			acks[g.SubID] = h.seq - 1
		}
	}

	return Result{Text: strings.Join(lines, "\n"), Acks: acks, Lines: len(lines), Held: held}
}

// orderKey is the lowest pending Seq of g, the drop marker included.
func orderKey(g Group) uint64 {
	k := g.UpToSeq
	for _, p := range g.Events {
		if p.Seq < k {
			k = p.Seq
		}
	}
	if g.Dropped > 0 && g.DroppedSeq < k {
		k = g.DroppedSeq
	}
	return k
}

// isFirst reports whether the item at cut is the first item of group gi,
// meaning nothing of the group was included.
func isFirst(items []item, cut, gi int) bool {
	for i := 0; i < cut; i++ {
		if items[i].group == gi {
			return false
		}
	}
	return true
}

func joinedLen(items []item) int {
	n, count := 0, 0
	for _, it := range items {
		if it.line != "" {
			n += len(it.line)
			count++
		}
	}
	if count > 1 {
		n += count - 1
	}
	return n
}
