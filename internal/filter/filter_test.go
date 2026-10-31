package filter

import (
	"reflect"
	"testing"

	"github.com/drodimin/pr-relay/internal/model"
)

const self = "drodimin"

var (
	me      = model.Actor{Login: "Drodimin", Type: "User"}
	meLower = model.Actor{Login: "drodimin", Type: "User"}
	botMe   = model.Actor{Login: "drodimin", Type: "Bot"}
	ghost   = model.Actor{Login: "ghost", Type: "Ghost"}
	alice   = model.Actor{Login: "alice", Type: "User"}
	unknown = model.Actor{}
)

func ev(t model.EventType, a model.Actor) model.Event {
	return model.Event{Type: t, Key: string(t) + ":" + a.Login, Actor: a}
}

func commits(force bool, actor model.Actor, authors ...model.Actor) model.Event {
	return model.Event{Type: model.EventCommits, Key: "commits:h2", ForcePushed: force, Actor: actor, Authors: authors}
}

func TestExcludeSelf(t *testing.T) {
	cases := []struct {
		name string
		self string
		in   model.Event
		keep bool
	}{
		{"empty self keeps own review", "", ev(model.EventReview, me), true},
		{"own review case-insensitive", self, ev(model.EventReview, me), false},
		{"own comment", self, ev(model.EventComment, meLower), false},
		{"bot review with self login", self, ev(model.EventReview, botMe), true},
		{"bot comment with self login", self, ev(model.EventComment, botMe), true},
		{"ghost comment", self, ev(model.EventComment, ghost), true},
		{"other user review", self, ev(model.EventReview, alice), true},
		{"other user comment", self, ev(model.EventComment, alice), true},
		{"force push by self", self, commits(true, me, alice), false},
		{"force push by other", self, commits(true, alice, me), true},
		{"force push empty actor, self authors", self, commits(true, unknown, me, meLower), false},
		{"force push empty actor, other author", self, commits(true, unknown, alice), true},
		{"push all self authors", self, commits(false, unknown, me, meLower), false},
		{"push mixed authors", self, commits(false, unknown, me, alice), true},
		{"push unknown author", self, commits(false, unknown, me, unknown), true},
		{"push no authors", self, commits(false, unknown), true},
		{"push bot author with self login", self, commits(false, unknown, botMe), true},
		{"closed by self (D-20)", self, ev(model.EventClosed, me), true},
		{"conflict", self, ev(model.EventConflict, me), true},
		{"ci.failed", self, ev(model.EventCIFailed, me), true},
		{"ci.settled", self, ev(model.EventCISettled, me), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := ExcludeSelf([]model.Event{tc.in}, tc.self)
			if got := len(out) == 1; got != tc.keep {
				t.Fatalf("keep = %v, want %v (%+v)", got, tc.keep, tc.in)
			}
		})
	}
}

func TestExcludeSelfNoMutate(t *testing.T) {
	in := []model.Event{
		ev(model.EventClosed, me),
		ev(model.EventReview, me),
		ev(model.EventReview, alice),
		ev(model.EventComment, me),
		ev(model.EventComment, botMe),
		commits(false, unknown, me),
		ev(model.EventCISettled, alice),
	}
	orig := append([]model.Event(nil), in...)
	out := ExcludeSelf(in, self)
	if !reflect.DeepEqual(in, orig) {
		t.Fatal("input slice mutated")
	}
	want := []model.Event{in[0], in[2], in[4], in[6]}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("order or content wrong:\n got %+v\nwant %+v", out, want)
	}

	// Empty self returns a copy, not the same backing array.
	cp := ExcludeSelf(in, "")
	if !reflect.DeepEqual(cp, in) {
		t.Fatal("empty self changed content")
	}
	cp[0].Key = "changed"
	if in[0].Key == "changed" {
		t.Fatal("empty self returned aliased slice")
	}
}
