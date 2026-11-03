package compose

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/render"
	"github.com/drodimin/pr-relay/internal/sanitize"
	"github.com/drodimin/pr-relay/internal/store"
	"github.com/drodimin/pr-relay/internal/testutil"
)

// prLine is the D-01 grammar from internal/render, extended with the note
// event. heldLine is the one pane-level line, which names no PR.
var (
	prLine   = regexp.MustCompile(`^\[pr-relay\] [A-Za-z0-9._-]+/[A-Za-z0-9._-]+#[0-9]+ (ci\.failed:|ci\.settled:|review|comment|reply|commits:|conflict:|closed:|note:) .* https://github\.com/[^ ]+$`)
	heldLine = regexp.MustCompile(`^\[pr-relay\] note: [0-9]+ more updates? held \(message size limit\), they arrive with the next delivery$`)
)

var (
	alpha = model.PRRef{Owner: "octo", Repo: "alpha", Number: 1}
	beta  = model.PRRef{Owner: "octo", Repo: "beta", Number: 2}
	gamma = model.PRRef{Owner: "octo", Repo: "gamma", Number: 3}
)

func comment(login string) model.Event {
	return model.Event{Type: model.EventComment, CommentKind: "comment", Actor: model.Actor{Login: login, Type: "User"}}
}

func review(login, verdict string) model.Event {
	return model.Event{Type: model.EventReview, Verdict: verdict, Actor: model.Actor{Login: login, Type: "User"}}
}

func group(sub string, ref model.PRRef, view View, evs ...store.PendingEvent) Group {
	g := Group{SubID: sub, Ref: ref, Events: evs, View: view}
	for _, p := range evs {
		if p.Seq > g.UpToSeq {
			g.UpToSeq = p.Seq
		}
	}
	return g
}

func checkLines(t *testing.T, text string) {
	t.Helper()
	if text == "" {
		return
	}
	if strings.HasSuffix(text, "\n") {
		t.Errorf("trailing newline in %q", text)
	}
	for _, l := range strings.Split(text, "\n") {
		if !prLine.MatchString(l) && !heldLine.MatchString(l) {
			t.Errorf("line does not match grammar: %q", l)
		}
		if strings.HasPrefix(l, "#") {
			t.Errorf("line starts with '#': %q", l)
		}
		if sanitize.CountTag(l) != 1 || strings.Count(l, "[pr-relay]") != 1 {
			t.Errorf("not exactly one tag: %q", l)
		}
	}
}

func twoPR() []Group {
	cur := View{HeadOID: sha1, State: "OPEN", Baselined: true}
	// beta has the higher SubID but the lower Seq, so it comes first.
	a := group("sub-a", alpha, cur,
		pe(4, review("alice", "APPROVED")),
		pe(5, comment("bob")),
		pe(7, model.Event{Type: model.EventCommits, Count: 2, HeadOID: sha1}),
	)
	b := group("sub-b", beta, cur,
		pe(2, failed(sha1, "lint")),
		pe(6, comment("carol")),
	)
	return []Group{a, b}
}

func TestMessageCoalesces(t *testing.T) {
	res := Message(twoPR(), Limits{})
	lines := strings.Split(res.Text, "\n")
	if len(lines) != 5 || res.Lines != 5 || res.Held != 0 {
		t.Fatalf("got %d lines (Lines=%d Held=%d), want 5:\n%s", len(lines), res.Lines, res.Held, res.Text)
	}
	if !strings.HasPrefix(lines[0], "[pr-relay] octo/beta#2 ") || !strings.HasPrefix(lines[2], "[pr-relay] octo/alpha#1 ") {
		t.Errorf("groups not ordered by lowest Seq:\n%s", res.Text)
	}
	if res.Acks["sub-a"] != 7 || res.Acks["sub-b"] != 6 || len(res.Acks) != 2 {
		t.Errorf("acks %v, want sub-a:7 sub-b:6", res.Acks)
	}
	checkLines(t, res.Text)
}

func TestMessageTieBreakBySubID(t *testing.T) {
	a := group("z", alpha, View{}, pe(3, comment("bob")))
	b := group("a", beta, View{}, pe(3, comment("bob")))
	res := Message([]Group{a, b}, Limits{})
	if !strings.HasPrefix(res.Text, "[pr-relay] octo/beta#2 ") {
		t.Errorf("tie not broken by SubID:\n%s", res.Text)
	}
}

func TestMessageDropMarker(t *testing.T) {
	a := group("sub-a", alpha, View{}, pe(5, comment("bob")))
	b := group("sub-b", beta, View{}, pe(9, comment("carol")))
	b.Dropped, b.DroppedSeq = 3, 4
	res := Message([]Group{a, b}, Limits{})
	lines := strings.Split(res.Text, "\n")
	if len(lines) != 3 || lines[0] != render.DroppedLine(beta, 3) {
		t.Fatalf("drop marker not first line of its group (DroppedSeq orders it first):\n%s", res.Text)
	}
	if res.Acks["sub-b"] != 9 {
		t.Errorf("acks %v, want sub-b:9", res.Acks)
	}
	checkLines(t, res.Text)
}

func TestMessageAllSuperseded(t *testing.T) {
	cur := View{HeadOID: sha1, State: "OPEN"}
	a := group("sub-a", alpha, cur, pe(1, failed(sha0, "lint")), pe(2, settled(sha0, false)))
	b := group("sub-b", beta, cur, pe(3, conflict(true)), pe(4, conflict(false)))
	res := Message([]Group{a, b}, Limits{})
	if res.Text != "" || res.Lines != 0 {
		t.Errorf("text %q, want empty", res.Text)
	}
	if res.Acks["sub-a"] != 2 || res.Acks["sub-b"] != 4 {
		t.Errorf("acks %v, want sub-a:2 sub-b:4", res.Acks)
	}
}

func TestMessageEmpty(t *testing.T) {
	res := Message(nil, Limits{})
	if res.Text != "" || len(res.Acks) != 0 {
		t.Errorf("got %+v, want empty", res)
	}
}

func TestMessageDoesNotMutate(t *testing.T) {
	gs := twoPR()
	first := gs[0].SubID
	Message(gs, Limits{})
	if gs[0].SubID != first {
		t.Errorf("groups slice reordered in place")
	}
}

// floodGroups gives three subs; each line is rendered in advance so the
// test can size MaxBytes exactly.
func floodGroups() ([]Group, []string) {
	a := group("a", alpha, View{}, pe(1, comment("bob")), pe(2, comment("carol")), pe(3, comment("dave")))
	b := group("b", beta, View{}, pe(4, comment("erin")), pe(5, comment("frank")), pe(6, comment("grace")))
	c := group("c", gamma, View{}, pe(7, comment("heidi")))
	var lines []string
	for _, g := range []Group{a, b, c} {
		var evs []model.Event
		for _, p := range g.Events {
			evs = append(evs, p.Event)
		}
		lines = append(lines, render.Lines(g.Ref, evs)...)
	}
	return []Group{a, b, c}, lines
}

func TestFloodGuard(t *testing.T) {
	gs, lines := floodGroups()
	max := len(strings.Join(lines[:4], "\n")) + 1 + len(render.HeldLine(len(lines)))
	res := Message(gs, Limits{MaxBytes: max})
	if len(res.Text) > max {
		t.Errorf("text is %d bytes, over %d", len(res.Text), max)
	}
	want := strings.Join(lines[:4], "\n") + "\n" + render.HeldLine(3)
	if res.Text != want {
		t.Errorf("text:\n%s\nwant:\n%s", res.Text, want)
	}
	if res.Held != 3 || res.Lines != 5 {
		t.Errorf("Held=%d Lines=%d, want 3 and 5", res.Held, res.Lines)
	}
	if res.Acks["a"] != 3 {
		t.Errorf("fully included group: ack %d, want 3", res.Acks["a"])
	}
	if res.Acks["b"] != 4 {
		t.Errorf("partial group: ack %d, want 4 (first held Seq 5 - 1)", res.Acks["b"])
	}
	if _, ok := res.Acks["c"]; ok {
		t.Errorf("group with nothing included got an ack: %v", res.Acks)
	}
	checkLines(t, res.Text)
}

func TestFloodGuardNoHoldWhenItFits(t *testing.T) {
	gs, lines := floodGroups()
	max := len(strings.Join(lines, "\n"))
	res := Message(gs, Limits{MaxBytes: max})
	if res.Held != 0 || res.Text != strings.Join(lines, "\n") {
		t.Errorf("held %d lines although the whole message fits:\n%s", res.Held, res.Text)
	}
}

func TestFloodGuardDropNoteHeld(t *testing.T) {
	gs, lines := floodGroups()
	gs[2].Dropped, gs[2].DroppedSeq = 2, 7
	max := len(strings.Join(lines[:6], "\n")) + 1 + len(render.HeldLine(len(lines)+1))
	res := Message(gs, Limits{MaxBytes: max})
	if res.Held != 2 {
		t.Errorf("Held=%d, want 2 (drop note and comment)", res.Held)
	}
	if _, ok := res.Acks["c"]; ok {
		t.Errorf("group whose drop note was held got an ack: %v", res.Acks)
	}
	if res.Acks["b"] != 6 {
		t.Errorf("acks %v, want b:6", res.Acks)
	}
}

func TestFloodGuardDefault(t *testing.T) {
	var evs []store.PendingEvent
	for i := 1; i <= 300; i++ {
		evs = append(evs, pe(uint64(i), comment(fmt.Sprintf("user%03d", i))))
	}
	res := Message([]Group{group("a", alpha, View{}, evs...)}, Limits{})
	if len(res.Text) > DefaultMaxBytes {
		t.Errorf("text is %d bytes, over DefaultMaxBytes %d", len(res.Text), DefaultMaxBytes)
	}
	if res.Held == 0 || res.Lines+res.Held != 301 {
		t.Errorf("Lines=%d Held=%d, want a hold covering 300 events", res.Lines, res.Held)
	}
	if got, want := res.Acks["a"], uint64(300-res.Held); got != want {
		t.Errorf("ack %d, want %d", got, want)
	}
	checkLines(t, res.Text)
}

func TestMessageHostile(t *testing.T) {
	for _, h := range testutil.HostileCorpus {
		evs := []store.PendingEvent{
			pe(1, model.Event{Type: model.EventCIFailed, CheckName: h, CheckWorkflow: h, CheckURL: h}),
			pe(2, model.Event{Type: model.EventCISettled, FailedNames: []string{h, h}, Total: 3}),
			pe(3, model.Event{Type: model.EventReview, Verdict: h, Actor: model.Actor{Login: h, Type: "User"}, URL: h}),
			pe(4, model.Event{Type: model.EventComment, CommentKind: "inline", Path: h, Actor: model.Actor{Login: h, Type: "Bot"}, URL: h}),
			pe(5, model.Event{Type: model.EventClosed, Merged: true, Actor: model.Actor{Login: h, Type: "User"}}),
		}
		g := group("a", alpha, View{}, evs...)
		g.Dropped, g.DroppedSeq = 1, 1
		res := Message([]Group{g, group("b", beta, View{}, pe(6, comment(h)))}, Limits{MaxBytes: 900})
		checkLines(t, res.Text)
	}
}

func TestMessageGolden(t *testing.T) {
	cur := View{HeadOID: sha1, State: "OPEN", Baselined: true}

	superseded := group("sub-a", alpha, cur,
		pe(1, failed(sha0, "lint")),
		pe(2, settled(sha0, false)),
		pe(3, model.Event{Type: model.EventCommits, ForcePushed: true, HeadOID: sha1, OldHeadOID: sha0}),
		pe(4, failed(sha1, "unit")),
		pe(5, settled(sha1, true)),
		pe(6, conflict(true)),
		pe(7, conflict(false)),
	)

	dropped := group("sub-b", beta, cur, pe(12, review("alice", "CHANGES_REQUESTED")), pe(13, comment("bob")))
	dropped.Dropped, dropped.DroppedSeq = 3, 11

	gs, lines := floodGroups()
	max := len(strings.Join(lines[:4], "\n")) + 1 + len(render.HeldLine(len(lines)))

	msgs := []struct {
		name string
		res  Result
	}{
		{"two-pr coalesced", Message(twoPR(), Limits{})},
		{"superseded ci and cancelled conflict", Message([]Group{superseded}, Limits{})},
		{"drop marker", Message([]Group{dropped, group("sub-c", gamma, cur, pe(14, model.Event{Type: model.EventClosed, Merged: true, Actor: model.Actor{Login: "alice", Type: "User"}}))}, Limits{})},
		{"overflow held", Message(gs, Limits{MaxBytes: max})},
	}
	var b strings.Builder
	for _, m := range msgs {
		checkLines(t, m.res.Text)
		fmt.Fprintf(&b, "=== %s (lines=%d held=%d)\n%s\n", m.name, m.res.Lines, m.res.Held, m.res.Text)
	}
	testutil.Golden(t, "testdata/messages.golden", []byte(b.String()))
}
