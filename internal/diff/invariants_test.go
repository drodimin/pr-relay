package diff_test

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/render"
)

// lineGrammar mirrors render_test.go: the shape every delivered line must
// match (DLV-05).
var lineGrammar = regexp.MustCompile(`^\[pr-relay\] [A-Za-z0-9._-]+/[A-Za-z0-9._-]+#[0-9]+ (ci\.failed:|ci\.settled:|review|comment|reply|commits:|conflict:|closed:) .* https://github\.com/[^ ]+$`)

const bodySentinel = "PRRELAY-BODY-SENTINEL"

// ev pairs an event with the step file that produced it.
type ev struct {
	File string
	model.Event
}

// delivered flattens the post-ExcludeSelf events of every step.
func delivered(steps []stepOutput) []ev {
	var out []ev
	for _, s := range steps {
		for _, e := range s.Events {
			out = append(out, ev{s.File, e})
		}
	}
	return out
}

// raw flattens the pre-filter diff.Step events of every step.
func raw(steps []stepOutput) []ev {
	var out []ev
	for _, s := range steps {
		for _, e := range s.Raw {
			out = append(out, ev{s.File, e})
		}
	}
	return out
}

func where(evs []ev, pred func(ev) bool) []ev {
	var out []ev
	for _, e := range evs {
		if pred(e) {
			out = append(out, e)
		}
	}
	return out
}

func ofType(t model.EventType) func(ev) bool {
	return func(e ev) bool { return e.Type == t }
}

func failedCheck(name string) func(ev) bool {
	return func(e ev) bool { return e.Type == model.EventCIFailed && e.CheckName == name }
}

func by(login string) func(ev) bool {
	return func(e ev) bool { return strings.EqualFold(e.Actor.Login, login) }
}

func and(ps ...func(ev) bool) func(ev) bool {
	return func(e ev) bool {
		for _, p := range ps {
			if !p(e) {
				return false
			}
		}
		return true
	}
}

func wantN(t *testing.T, what string, got []ev, n int) {
	t.Helper()
	if len(got) != n {
		t.Errorf("%s: got %d events, want %d: %+v", what, len(got), n, got)
	}
}

// settledOnce asserts exactly one delivered ci.settled with the given result.
func settledOnce(t *testing.T, steps []stepOutput, passed bool) {
	t.Helper()
	s := where(delivered(steps), ofType(model.EventCISettled))
	wantN(t, "ci.settled", s, 1)
	if len(s) == 1 && s[0].Passed != passed {
		t.Errorf("ci.settled Passed = %v, want %v", s[0].Passed, passed)
	}
}

const second = "dmitriy-medme"

// scenarioChecks encodes the "Expected events" tables in
// testdata/scenarios/README.md, independent of wording.
var scenarioChecks = map[string]func(t *testing.T, self string, steps []stepOutput){
	"ci-fail-pass": func(t *testing.T, self string, steps []stepOutput) {
		wantN(t, "ci.failed test", where(delivered(steps), failedCheck("test")), 1)
		wantN(t, "ci.failed total", where(delivered(steps), ofType(model.EventCIFailed)), 1)
		settledOnce(t, steps, true)
		wantN(t, "delivered commits (self push)", where(delivered(steps), ofType(model.EventCommits)), 0)
		wantN(t, "raw commits", where(raw(steps), ofType(model.EventCommits)), 1)
	},
	"ci-flaky-rerun": func(t *testing.T, self string, steps []stepOutput) {
		wantN(t, "ci.failed flaky", where(delivered(steps), failedCheck("flaky")), 1)
		wantN(t, "ci.failed total", where(delivered(steps), ofType(model.EventCIFailed)), 1)
		settledOnce(t, steps, true) // D-08: no ci.recovered, one final settle
	},
	"ci-cancelled": func(t *testing.T, self string, steps []stepOutput) {
		wantN(t, "ci.failed slow", where(delivered(steps), failedCheck("slow")), 0)
		wantN(t, "ci.failed total", where(delivered(steps), ofType(model.EventCIFailed)), 0)
		settledOnce(t, steps, true) // CANCELLED is neutral
	},
	"synthetic-startup-failure": func(t *testing.T, self string, steps []stepOutput) {
		wantN(t, "ci.failed test", where(delivered(steps), failedCheck("test")), 1)
		wantN(t, "ci.failed total", where(delivered(steps), ofType(model.EventCIFailed)), 1)
		settledOnce(t, steps, false)
	},
	"synthetic-many-failures": func(t *testing.T, self string, steps []stepOutput) {
		f := where(delivered(steps), ofType(model.EventCIFailed))
		wantN(t, "ci.failed", f, 9) // D-03: one per failing check
		urls := map[string]bool{}
		for _, e := range f {
			if urls[e.CheckURL] {
				t.Errorf("duplicate CheckURL %q", e.CheckURL)
			}
			urls[e.CheckURL] = true
		}
		settledOnce(t, steps, false)
		s := where(delivered(steps), ofType(model.EventCISettled))
		if len(s) == 1 && len(s[0].FailedNames) != 9 {
			t.Errorf("ci.settled FailedNames = %v, want 9", s[0].FailedNames)
		}
	},
	"review-changes-requested-inline": func(t *testing.T, self string, steps []stepOutput) {
		r := where(delivered(steps), ofType(model.EventReview))
		wantN(t, "review", r, 1)
		if len(r) == 1 && (r[0].Verdict != "CHANGES_REQUESTED" || r[0].InlineCount != 3 || r[0].Actor.Login != second) {
			t.Errorf("review = %+v, want CHANGES_REQUESTED by %s with 3 inline", r[0].Event, second)
		}
		wantN(t, "comment (grouped into review, D-10)", where(delivered(steps), ofType(model.EventComment)), 0)
	},
	"review-approved": func(t *testing.T, self string, steps []stepOutput) {
		r := where(delivered(steps), ofType(model.EventReview))
		wantN(t, "review", r, 1)
		if len(r) == 1 && (r[0].Verdict != "APPROVED" || r[0].InlineCount != 0) {
			t.Errorf("review = %+v, want APPROVED with 0 inline", r[0].Event)
		}
	},
	"review-commented": func(t *testing.T, self string, steps []stepOutput) {
		r := where(delivered(steps), ofType(model.EventReview))
		wantN(t, "review", r, 3)
		for _, e := range r {
			if e.Verdict != "COMMENTED" || e.InlineCount != 1 || e.Actor.Login != second {
				t.Errorf("review = %+v, want COMMENTED by %s with 1 inline", e.Event, second)
			}
		}
		wantN(t, "comment", where(delivered(steps), ofType(model.EventComment)), 0)
	},
	"issue-comment": func(t *testing.T, self string, steps []stepOutput) {
		c := where(delivered(steps), ofType(model.EventComment))
		wantN(t, "comment", c, 1)
		if len(c) == 1 && (c[0].CommentKind != "comment" || c[0].Actor.Login != second) {
			t.Errorf("comment = %+v, want kind=comment by %s", c[0].Event, second)
		}
		// D-13: an edit changes nothing in the query, so every other step is silent.
		wantN(t, "all delivered", delivered(steps), 1)
	},
	"thread-reply": func(t *testing.T, self string, steps []stepOutput) {
		d := delivered(steps)
		wantN(t, "reply by second", where(d, and(ofType(model.EventComment), by(second))), 1)
		for _, e := range where(d, ofType(model.EventComment)) {
			if e.CommentKind != "reply" {
				t.Errorf("comment kind = %q, want reply", e.CommentKind)
			}
		}
		wantN(t, "delivered by self (EVT-08)", where(d, by(self)), 0)
		wantN(t, "raw reply by self", where(raw(steps), and(ofType(model.EventComment), by(self))), 1)
		wantN(t, "review (reply wrapper, A1)", where(d, ofType(model.EventReview)), 0)
	},
	"bot-authors": func(t *testing.T, self string, steps []stepOutput) {
		d := delivered(steps)
		bots := where(d, func(e ev) bool { return e.Actor.Type == "Bot" })
		if len(bots) < 1 {
			t.Errorf("no Bot-authored events delivered")
		}
		wantN(t, "delivered vs raw (D-12: bots never filtered)", d, len(raw(steps)))
		wantN(t, "bot events", bots, 4)
	},
	"own-activity": func(t *testing.T, self string, steps []stepOutput) {
		d := delivered(steps)
		for _, typ := range []model.EventType{model.EventReview, model.EventComment, model.EventCommits} {
			wantN(t, "delivered "+string(typ), where(d, ofType(typ)), 0)
		}
		r := raw(steps)
		wantN(t, "raw self review/comment", where(r, by(self)), 3)
		wantN(t, "raw self push", where(r, ofType(model.EventCommits)), 1)
		settledOnce(t, steps, true) // CI is never self activity
	},
	"push": func(t *testing.T, self string, steps []stepOutput) {
		c := where(raw(steps), ofType(model.EventCommits))
		wantN(t, "raw commits", c, 1)
		if len(c) == 1 && (c[0].Count != 2 || c[0].ForcePushed) {
			t.Errorf("commits = %+v, want Count 2 not force-pushed", c[0].Event)
		}
		wantN(t, "delivered commits (all authors self)", where(delivered(steps), ofType(model.EventCommits)), 0)
		settledOnce(t, steps, true)
	},
	"push-mixed-authors": func(t *testing.T, self string, steps []stepOutput) {
		c := where(delivered(steps), ofType(model.EventCommits))
		wantN(t, "delivered commits (one foreign author)", c, 1)
		if len(c) == 1 && (c[0].Count != 2 || c[0].ForcePushed) {
			t.Errorf("commits = %+v, want Count 2 not force-pushed", c[0].Event)
		}
		settledOnce(t, steps, true)
	},
	"force-push": func(t *testing.T, self string, steps []stepOutput) {
		c := where(raw(steps), ofType(model.EventCommits))
		wantN(t, "raw commits", c, 1)
		if len(c) == 1 && !c[0].ForcePushed {
			t.Errorf("commits = %+v, want ForcePushed (EVT-05)", c[0].Event)
		}
		wantN(t, "delivered commits (self force-push)", where(delivered(steps), ofType(model.EventCommits)), 0)
		settledOnce(t, steps, true)
	},
	"conflict-on-off": func(t *testing.T, self string, steps []stepOutput) {
		c := where(delivered(steps), ofType(model.EventConflict))
		wantN(t, "conflict", c, 2)
		if len(c) == 2 && (!c[0].Conflicting || c[1].Conflicting) {
			t.Errorf("conflict sequence = %v,%v; want true then false", c[0].Conflicting, c[1].Conflicting)
		}
		settledOnce(t, steps, true)
	},
	"mergeable-unknown": func(t *testing.T, self string, steps []stepOutput) {
		wantN(t, "conflict", where(delivered(steps), ofType(model.EventConflict)), 0)
		wantN(t, "all delivered", delivered(steps), 0)
	},
	"synthetic-mergeable-unknown-conflicting": func(t *testing.T, self string, steps []stepOutput) {
		c := where(delivered(steps), ofType(model.EventConflict))
		wantN(t, "conflict (D-21)", c, 1)
		if len(c) == 1 && (!c[0].Conflicting || !strings.HasPrefix(c[0].File, "01-")) {
			t.Errorf("conflict = %+v at %s, want Conflicting at step 01-*", c[0].Event, c[0].File)
		}
		wantN(t, "all delivered", delivered(steps), 1)
	},
	"synthetic-mergeable-unknown-mergeable": func(t *testing.T, self string, steps []stepOutput) {
		for _, s := range steps {
			if len(s.Raw) != 0 || len(s.Lines) != 0 {
				t.Errorf("step %s emitted %v, want nothing (D-21)", s.File, s.Lines)
			}
		}
	},
	"merged": func(t *testing.T, self string, steps []stepOutput) {
		c := where(delivered(steps), ofType(model.EventClosed))
		wantN(t, "closed", c, 1)
		if len(c) == 1 && !c[0].Merged {
			t.Errorf("closed = %+v, want Merged", c[0].Event)
		}
	},
	"closed": func(t *testing.T, self string, steps []stepOutput) {
		c := where(delivered(steps), ofType(model.EventClosed))
		wantN(t, "closed (D-20: delivered even when self closed it)", c, 1)
		if len(c) == 1 && (c[0].Merged || !strings.EqualFold(c[0].Actor.Login, self)) {
			t.Errorf("closed = %+v, want Merged=false by %s", c[0].Event, self)
		}
	},
}

func TestScenarioInvariants(t *testing.T) {
	self := readSelf(t)
	dirs := scenarioDirs(t)

	var missing []string
	for _, name := range dirs {
		if scenarioChecks[name] == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("scenario directories without a scenarioChecks entry: %v", missing)
	}

	for _, name := range dirs {
		t.Run(name, func(t *testing.T) {
			steps := runScenario(t, filepath.Join(scenariosDir, name), self)
			globalInvariants(t, self, steps)
			scenarioChecks[name](t, self, steps)
		})
	}
}

func globalInvariants(t *testing.T, self string, steps []stepOutput) {
	t.Helper()
	keys := map[string]string{}
	for _, s := range steps {
		// (a) exactly-once: no Key twice across the scenario (raw, so
		// filtered events are covered too).
		for _, e := range s.Raw {
			if prev, ok := keys[e.Key]; ok {
				t.Errorf("event key %q emitted at %s and again at %s", e.Key, prev, s.File)
			}
			keys[e.Key] = s.File
		}
		for _, e := range s.Events {
			// (b) UNKNOWN mergeable never emits a conflict (EVT-06).
			if e.Type == model.EventConflict && s.Snapshot.Mergeable == "UNKNOWN" {
				t.Errorf("conflict event at %s while mergeable is UNKNOWN", s.File)
			}
			// (c) no own review/comment survives ExcludeSelf (EVT-08).
			if (e.Type == model.EventReview || e.Type == model.EventComment) &&
				e.Actor.IsUser() && strings.EqualFold(e.Actor.Login, self) {
				t.Errorf("own %s by %s delivered at %s", e.Type, e.Actor.Login, s.File)
			}
		}
		if len(s.Lines) != len(s.Events) {
			t.Errorf("step %s: %d lines for %d events", s.File, len(s.Lines), len(s.Events))
		}
		for _, l := range s.Lines {
			// (d) grammar and no body leakage (DLV-05, D-05).
			if !lineGrammar.MatchString(l) {
				t.Errorf("step %s: line does not match grammar: %q", s.File, l)
			}
			if strings.Contains(l, bodySentinel) {
				t.Errorf("step %s: line leaks body: %q", s.File, l)
			}
			// (e) byte cap (DLV-06).
			if len(l) > render.MaxLineBytes {
				t.Errorf("step %s: line is %d bytes > %d: %q", s.File, len(l), render.MaxLineBytes, l)
			}
		}
	}
}
