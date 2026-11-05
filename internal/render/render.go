// Package render turns typed PR events into terse, sanitized, self-contained
// "[pr-relay]" lines (DLV-05). It is the last line of the DLV-06 defence:
// every third-party string (check name, workflow, login, path) goes through
// internal/sanitize, every URL goes through sanitize.URL with a fallback
// built from the validated PRRef, and SHAs go through sanitize.ShortSHA.
//
// The PR title, comment bodies, review text and log content are never
// rendered; model.Event carries no such field (D-04, D-05).
//
// Lines and Each return one line per event and never join them. Joining
// lines into one prompt, coalescing and the per-message flood guard belong
// to internal/compose, which owns the prompt size (D-02, D-03). DroppedLine
// and HeldLine are the note lines compose uses for drop markers and held
// overflow.
//
// The tag guard (fits) counts the tag with sanitize.CountTag over the whole
// line (folded, with combining marks and invisible runes skipped), so a
// lookalike second tag ("［pr-relay］", Cyrillic letters, Unicode brackets,
// or one split by marks) formed anywhere in the line, including across field
// boundaries, forces the minimal fallback line (CR-02).
package render

import (
	"fmt"
	"strings"
	"time"

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
	var out []string
	for _, l := range Each(ref, events) {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Each renders events aligned 1:1 with the input; element i is "" when event
// i has an unknown type. It returns nil if ref is not Valid.
func Each(ref model.PRRef, events []model.Event) []string {
	if !ref.Valid() {
		return nil
	}
	r := renderer{ref: ref, prefix: tag + " " + ref.String() + " ", pr: ref.URL()}
	r.qualify = collisions(events)
	out := make([]string, len(events))
	for i, e := range events {
		if l, ok := r.line(e); ok {
			out[i] = l
		}
	}
	return out
}

// DroppedLine is the note shown at the start of a PR's group when the
// pending cap dropped n earlier updates. It returns "" if ref is not Valid.
func DroppedLine(ref model.PRRef, n int) string {
	if !ref.Valid() {
		return ""
	}
	return tag + " " + ref.String() + " note: " + plural(n, "earlier update") + " dropped " + ref.URL()
}

// HeldLine is the note that ends a message when the flood guard held n
// lines back for the next delivery. It names no PR, because the held lines
// can span several.
func HeldLine(n int) string {
	return tag + " note: " + plural(n, "more update") + " held (message size limit), they arrive with the next delivery"
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
	case model.EventCheckin:
		// D-12: built whole, from the validated ref URL and an integer
		// duration only, so no sanitize call is needed.
		l := r.prefix + "check-in: no activity for " + human(idle(e)) +
			". still needed? keep: pr-relay keep " + r.pr + " | drop: pr-relay unsub " + r.pr
		if !fits(l) {
			l = r.prefix + head(e) + " … " + r.pr
		}
		return l, true
	case model.EventExpired:
		l := r.prefix + "expired: no activity for " + human(idle(e)) +
			", subscription removed. re-subscribe: pr-relay sub " + r.pr
		if !fits(l) {
			l = r.prefix + head(e) + " … " + r.pr
		}
		return l, true
	default:
		return "", false
	}
	l := r.prefix + body + " " + url
	if !fits(l) {
		l = r.prefix + head(e) + " … " + r.pr
	}
	return l, true
}

func idle(e model.Event) time.Duration { return time.Duration(e.IdleSecs) * time.Second }

// human renders a lifecycle threshold the way a user would write it in
// config.toml: whole days from 48h up ("7d"), else whole hours ("24h",
// "36h"), else minutes ("90m").
func human(d time.Duration) string {
	const day = 24 * time.Hour
	switch {
	case d >= 2*day && d%day == 0:
		return fmt.Sprintf("%dd", d/day)
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	default:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
}

// fits reports whether l fits the cap and carries exactly one tag in folded
// form (the prefix tag at offset 0). sanitize.CountTag skips combining marks
// and invisible runes, so a tag split by them is still counted.
func fits(l string) bool {
	return len(l) <= MaxLineBytes && sanitize.CountTag(l) == 1
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
