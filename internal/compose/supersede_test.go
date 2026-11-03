package compose

import (
	"reflect"
	"testing"

	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/store"
)

const (
	sha0 = "def5678abc1234def5678abc1234def5678abc12"
	sha1 = "abc1234def5678abc1234def5678abc1234def56"
)

func pe(seq uint64, e model.Event) store.PendingEvent { return store.PendingEvent{Seq: seq, Event: e} }

func failed(head, name string) model.Event {
	return model.Event{Type: model.EventCIFailed, HeadOID: head, CheckName: name}
}

func settled(head string, passed bool) model.Event {
	return model.Event{Type: model.EventCISettled, HeadOID: head, Passed: passed, Total: 3}
}

func conflict(c bool) model.Event { return model.Event{Type: model.EventConflict, Conflicting: c} }

func seqs(evs []store.PendingEvent) []uint64 {
	out := []uint64{}
	for _, p := range evs {
		out = append(out, p.Seq)
	}
	return out
}

func TestSupersede(t *testing.T) {
	open1 := View{HeadOID: sha1, State: "OPEN", Baselined: true}
	history := []store.PendingEvent{
		pe(1, model.Event{Type: model.EventCommits, Count: 1, HeadOID: sha0}),
		pe(2, model.Event{Type: model.EventReview, Verdict: "APPROVED"}),
		pe(3, model.Event{Type: model.EventComment, CommentKind: "comment"}),
		pe(4, model.Event{Type: model.EventClosed, Merged: true}),
	}
	cases := []struct {
		name string
		view View
		in   []store.PendingEvent
		want []uint64
	}{
		{"R1 stale head ci dropped (DLV-07 example)", open1,
			[]store.PendingEvent{pe(1, failed(sha0, "lint")), pe(2, settled(sha0, false)), pe(3, model.Event{Type: model.EventCommits, HeadOID: sha1})},
			[]uint64{3}},
		{"R1 current head ci kept", open1,
			[]store.PendingEvent{pe(1, failed(sha1, "lint")), pe(2, failed(sha1, "unit"))},
			[]uint64{1, 2}},
		{"R1 empty event head kept", open1,
			[]store.PendingEvent{pe(1, failed("", "lint"))},
			[]uint64{1}},
		{"R2 settled drops earlier failures for head", open1,
			[]store.PendingEvent{pe(1, failed(sha1, "lint")), pe(2, failed(sha1, "unit")), pe(3, settled(sha1, false)), pe(4, failed(sha1, "late"))},
			[]uint64{3, 4}},
		{"R3 rerun keeps last settled", open1,
			[]store.PendingEvent{pe(1, settled(sha1, false)), pe(2, settled(sha1, true)), pe(5, settled(sha1, true))},
			[]uint64{5}},
		{"R4 raised then cleared cancels out", open1,
			[]store.PendingEvent{pe(1, conflict(true)), pe(2, conflict(false))},
			[]uint64{}},
		{"R4 raised cleared raised keeps last", open1,
			[]store.PendingEvent{pe(1, conflict(true)), pe(2, conflict(false)), pe(3, conflict(true))},
			[]uint64{3}},
		{"R4 single raised kept", open1,
			[]store.PendingEvent{pe(1, conflict(true))},
			[]uint64{1}},
		{"R4 closed PR drops conflicts", View{HeadOID: sha1, State: "MERGED"},
			[]store.PendingEvent{pe(1, conflict(true)), pe(2, model.Event{Type: model.EventClosed, Merged: true})},
			[]uint64{2}},
		{"R5 R6 history never dropped", View{HeadOID: sha1, State: "CLOSED"}, history, []uint64{1, 2, 3, 4}},
		{"R8 empty view keeps ci on any head", View{},
			[]store.PendingEvent{pe(1, failed(sha0, "lint")), pe(2, failed(sha1, "unit")), pe(3, conflict(true))},
			[]uint64{1, 2, 3}},
		{"output in Seq order", open1,
			[]store.PendingEvent{pe(3, model.Event{Type: model.EventReview}), pe(1, model.Event{Type: model.EventComment})},
			[]uint64{1, 3}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := append([]store.PendingEvent(nil), c.in...)
			got := Supersede(c.view, c.in)
			if !reflect.DeepEqual(seqs(got), c.want) {
				t.Errorf("got seqs %v, want %v", seqs(got), c.want)
			}
			if !reflect.DeepEqual(before, c.in) {
				t.Errorf("input was mutated")
			}
		})
	}
}
