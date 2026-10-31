package render

import (
	"math"
	"strings"
	"testing"
	"unicode"

	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/testutil"
)

// hostileBases is one base event per (type, variant). Every string field is
// overwritten with the hostile value before rendering.
var hostileBases = []model.Event{
	{Type: model.EventClosed, Merged: true},
	{Type: model.EventClosed},
	{Type: model.EventConflict, Conflicting: true},
	{Type: model.EventCommits, Count: 2},
	{Type: model.EventCommits, ForcePushed: true},
	{Type: model.EventCIFailed},
	{Type: model.EventCISettled, Passed: true, Total: 3},
	{Type: model.EventCISettled, Total: 3},
	{Type: model.EventReview, InlineCount: 2},
	{Type: model.EventComment, CommentKind: "comment"},
	{Type: model.EventComment, CommentKind: "reply"},
	{Type: model.EventComment, CommentKind: "inline"},
}

func hostileEvent(base model.Event, h string, actorType string, keepKind bool) model.Event {
	e := base
	e.CheckName, e.CheckWorkflow, e.CheckURL, e.URL, e.Path = h, h, h, h, h
	e.HeadOID, e.OldHeadOID, e.Verdict = h, h, h
	e.Actor = model.Actor{Login: h, Type: actorType}
	e.Authors = []model.Actor{{Login: h, Type: actorType}}
	if !e.Passed && e.Type == model.EventCISettled {
		e.FailedNames = []string{h, h, h}
	}
	if !keepKind {
		e.CommentKind = h
	}
	return e
}

func checkLine(t *testing.T, l string) {
	t.Helper()
	if strings.ContainsAny(l, "\r\n") {
		t.Errorf("line break in %q", l)
	}
	if len(l) > MaxLineBytes {
		t.Errorf("line is %d bytes (> %d): %q", len(l), MaxLineBytes, l)
	}
	if !strings.HasPrefix(l, "[pr-relay] ") || strings.Count(strings.ToLower(l), "[pr-relay]") != 1 {
		t.Errorf("tag not exactly once at offset 0: %q", l)
	}
	for _, r := range l {
		if r != ' ' && !unicode.IsPrint(r) {
			t.Errorf("non-printable rune %U in %q", r, l)
			break
		}
	}
	if !lineGrammar.MatchString(l) {
		t.Errorf("grammar mismatch: %q", l)
	}
	if u := l[strings.LastIndex(l, " ")+1:]; !strings.HasPrefix(u, "https://github.com/") {
		t.Errorf("trailing URL %q is not github.com", u)
	}
}

func TestHostileRender(t *testing.T) {
	refs := []model.PRRef{
		testRef,
		{Owner: strings.Repeat("o", 100), Repo: strings.Repeat("r", 100), Number: math.MaxInt64},
	}
	types := map[model.EventType]bool{}
	for _, ref := range refs {
		for _, h := range testutil.HostileCorpus {
			for _, base := range hostileBases {
				types[base.Type] = true
				for _, at := range []string{"User", "Bot", h} {
					for _, keep := range []bool{true, false} {
						e := hostileEvent(base, h, at, keep)
						lines := Lines(ref, []model.Event{e})
						if len(lines) != 1 {
							t.Fatalf("%s with %q: got %d lines, want 1", e.Type, h, len(lines))
						}
						checkLine(t, lines[0])
					}
				}
			}
			// Collision path: two ci.failed with the same name, hostile workflows.
			pair := []model.Event{
				hostileEvent(model.Event{Type: model.EventCIFailed}, h, "User", true),
				hostileEvent(model.Event{Type: model.EventCIFailed}, h, "User", true),
			}
			pair[1].CheckWorkflow = h + "x"
			lines := Lines(ref, pair)
			if len(lines) != 2 {
				t.Fatalf("collision pair with %q: got %d lines", h, len(lines))
			}
			for _, l := range lines {
				checkLine(t, l)
			}
		}
	}
	if len(types) != len(model.AllEventTypes()) {
		t.Errorf("covered %d event types, want %d", len(types), len(model.AllEventTypes()))
	}
}

func TestHostileCheckURLFallback(t *testing.T) {
	for _, u := range []string{"https://evil.example/", "javascript:alert(1)", "https://github.com.evil.example/x", "https://github.com/x y"} {
		got := Lines(testRef, []model.Event{{Type: model.EventCIFailed, CheckName: "lint", CheckURL: u}})
		want := "[pr-relay] octo/repo#7 ci.failed: lint https://github.com/octo/repo/pull/7/checks"
		if len(got) != 1 || got[0] != want {
			t.Errorf("CheckURL %q: got %q, want %q", u, got, want)
		}
	}
}

func TestOverCapFallback(t *testing.T) {
	ref := model.PRRef{Owner: strings.Repeat("o", 100), Repo: strings.Repeat("r", 100), Number: math.MaxInt64}
	name := strings.Repeat("\U0001F600", 200) // 4-byte runes, capped at 80 runes
	url := "https://github.com/" + strings.Repeat("a", 280)
	got := Lines(ref, []model.Event{{Type: model.EventCIFailed, CheckName: name, CheckURL: url}})
	want := "[pr-relay] " + ref.String() + " ci.failed: … " + ref.URL()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	checkLine(t, got[0])
}
