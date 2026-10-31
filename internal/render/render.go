// Package render turns typed PR events into terse, sanitized, self-contained
// "[pr-relay]" lines (DLV-05). It is the last line of the DLV-06 defence:
// every third-party string (check name, workflow, login, path) goes through
// internal/sanitize, every URL goes through sanitize.URL with a fallback
// built from the validated PRRef, and SHAs go through sanitize.ShortSHA.
//
// The PR title, comment bodies, review text and log content are never
// rendered; model.Event carries no such field (D-04, D-05).
//
// Lines returns one line per event and never joins them. Joining lines into
// one prompt, coalescing, and any per-message line ceiling (flood guard)
// belong to Phase 3 delivery, after spike S6 measures how herdr handles
// multi-line and large prompts. That layer owns the prompt size, so it owns
// the ceiling too (D-02, D-03).
package render

import (
	"fmt"
	"strings"

	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/sanitize"
)

// MaxLineBytes is the hard cap on one rendered line, in bytes.
const MaxLineBytes = 512

const tag = "[pr-relay]"

// verdicts maps raw GraphQL review states to fixed display words. The raw
// value is never interpolated.
var verdicts = map[string]string{
	"APPROVED":          "approved",
	"CHANGES_REQUESTED": "changes_requested",
	"COMMENTED":         "commented",
	"DISMISSED":         "dismissed",
}

// Lines renders events for ref, one line per known event, in input order.
// Unknown event types are skipped. It returns nil if ref is not Valid.
func Lines(ref model.PRRef, events []model.Event) []string {
	if !ref.Valid() {
		return nil
	}
	r := renderer{ref: ref, prefix: tag + " " + ref.String() + " ", pr: ref.URL()}
	r.qualify = collisions(events)
	var out []string
	for _, e := range events {
		if l, ok := r.line(e); ok {
			out = append(out, l)
		}
	}
	return out
}

type renderer struct {
	ref     model.PRRef
	prefix  string
	pr      string
	qualify map[string]bool // sanitized check names that need "<workflow> / " qualification
}

// collisions returns the sanitized ci.failed check names that appear with
// more than one distinct CheckWorkflow in events.
func collisions(events []model.Event) map[string]bool {
	wf := map[string]map[string]bool{}
	for _, e := range events {
		if e.Type != model.EventCIFailed {
			continue
		}
		n := sanitize.Field(e.CheckName, sanitize.MaxCheckName)
		if wf[n] == nil {
			wf[n] = map[string]bool{}
		}
		wf[n][e.CheckWorkflow] = true
	}
	q := map[string]bool{}
	for n, set := range wf {
		if len(set) > 1 {
			q[n] = true
		}
	}
	return q
}

// head is the grammar keyword for each event type, used by the minimal line.
func head(e model.Event) string {
	switch e.Type {
	case model.EventCIFailed, model.EventCISettled, model.EventCommits, model.EventConflict, model.EventClosed:
		return string(e.Type) + ":"
	case model.EventComment:
		if e.CommentKind == "reply" {
			return "reply"
		}
		return "comment"
	default:
		return string(e.Type)
	}
}

func (r renderer) line(e model.Event) (string, bool) {
	var body, url string
	switch e.Type {
	case model.EventCIFailed:
		body = "ci.failed: " + r.checkName(e)
		url = sanitize.URL(e.CheckURL, r.pr+"/checks")
	case model.EventCISettled:
		body, url = r.settled(e), r.pr+"/checks"
	case model.EventReview:
		body = "review " + verdict(e.Verdict) + " by " + login(e.Actor) + inline(e.InlineCount)
		url = sanitize.URL(e.URL, r.pr)
	case model.EventComment:
		body = r.comment(e)
		url = sanitize.URL(e.URL, r.pr)
	case model.EventCommits:
		body, url = r.commits(e)
	case model.EventConflict:
		body, url = "conflict: cleared", r.pr
		if e.Conflicting {
			body = "conflict: conflicting"
		}
	case model.EventClosed:
		body, url = "closed: closed", r.pr
		if e.Merged {
			body = "closed: merged"
		}
		if e.Actor.Login != "" {
			body += " by " + login(e.Actor)
		}
	default:
		return "", false
	}
	l := r.prefix + body + " " + url
	if !fits(l) {
		l = r.prefix + head(e) + " … " + r.pr
	}
	return l, true
}

// fits reports whether l fits the cap and carries exactly one tag, at offset 0.
func fits(l string) bool {
	return len(l) <= MaxLineBytes && strings.Count(strings.ToLower(l), tag) == 1
}

func (r renderer) checkName(e model.Event) string {
	n := sanitize.Field(e.CheckName, sanitize.MaxCheckName)
	if r.qualify[n] && e.CheckWorkflow != "" {
		return sanitize.Field(e.CheckWorkflow, sanitize.MaxCheckName) + " / " + n
	}
	return n
}

func (r renderer) settled(e model.Event) string {
	if e.Passed {
		return "ci.settled: passed (" + plural(e.Total, "check") + ")"
	}
	counts := fmt.Sprintf("ci.settled: failed (%d of %d)", len(e.FailedNames), e.Total)
	if len(e.FailedNames) == 0 {
		return counts
	}
	names := make([]string, len(e.FailedNames))
	for i, n := range e.FailedNames {
		names[i] = sanitize.Field(n, sanitize.MaxCheckName)
	}
	withNames := counts + ": " + strings.Join(names, ", ")
	// Keep the names only while the whole line stays within the cap; the
	// ci.failed lines already named every check (D-03).
	if len(r.prefix)+len(withNames)+1+len(r.pr)+len("/checks") > MaxLineBytes {
		return counts
	}
	return withNames
}

func (r renderer) comment(e model.Event) string {
	who := " by " + login(e.Actor)
	switch e.CommentKind {
	case "reply":
		return "reply" + who + on(e.Path)
	case "inline":
		return "comment" + who + on(e.Path)
	default:
		return "comment" + who
	}
}

func (r renderer) commits(e model.Event) (string, string) {
	h := sanitize.ShortSHA(e.HeadOID)
	if e.ForcePushed {
		old := sanitize.ShortSHA(e.OldHeadOID)
		if old == "?" || h == "?" {
			return "commits: force-pushed, head " + h, r.pr + "/commits"
		}
		return "commits: force-pushed, head " + h + " (was " + old + ")",
			"https://github.com/" + r.ref.Owner + "/" + r.ref.Repo + "/compare/" + old + "..." + h
	}
	n := "new commits"
	if e.Count > 0 {
		n = fmt.Sprintf("%d new", e.Count)
	}
	return "commits: " + n + ", head " + h, r.pr + "/commits"
}

func on(path string) string {
	if path == "" {
		return ""
	}
	return " on " + sanitize.Path(path, sanitize.MaxPath)
}

func verdict(v string) string {
	if w, ok := verdicts[v]; ok {
		return w
	}
	return "submitted"
}

func inline(n int) string {
	if n <= 0 {
		return ""
	}
	return " (" + plural(n, "inline comment") + ")"
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// login renders an actor's login. Bots render as name[bot] whether or not
// GitHub already appended the suffix (D-12: identity, not policy).
func login(a model.Actor) string {
	if a.Type != "Bot" {
		return sanitize.Login(a.Login)
	}
	l := a.Login
	if len(l) >= 5 && strings.EqualFold(l[len(l)-5:], "[bot]") {
		l = l[:len(l)-5]
	}
	return sanitize.Login(l) + "[bot]"
}
