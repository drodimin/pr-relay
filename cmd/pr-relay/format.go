package main

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/drodimin/pr-relay/internal/ctl"
	"github.com/drodimin/pr-relay/internal/model"
)

// This file is the CLI's whole text layer. Every function is pure over a ctl
// result and an explicit clock so the output is golden-testable. v1 prints
// text only, never JSON (D-22). PR and URL strings come from validated PR
// references on the daemon; no PR body text is ever formatted (T-04-29).

// formatSub renders a successful sub in the 1-3 line D-07 shape. The D-10
// warning (r.Warning) is not included: the caller prints it to stderr.
func formatSub(r ctl.SubResult, now time.Time) string {
	var b strings.Builder
	verb := "subscribed"
	if !r.Created {
		verb = "updated"
	}
	fmt.Fprintf(&b, "%s %s events=%s", verb, r.PR, eventsLabel(r.Events))
	if len(r.Events) > 0 && !containsString(r.Events, string(model.EventClosed)) {
		b.WriteString(" (closed always included)")
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "polling every %s (next in %s)", humanDur(r.Interval), humanDur(r.NextPollAt.Sub(now)))
	if len(r.CoSubscribers) > 0 {
		fmt.Fprintf(&b, "; also watched by: %s", strings.Join(r.CoSubscribers, ", "))
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "updates arrive in this pane; unsub: pr-relay unsub %s\n", r.URL)
	return b.String()
}

// formatLs renders ls (D-21, SUB-06). Without all it prints the calling
// pane's rows in the approved preview shape; with all it prints a table with
// a leading PANE column.
func formatLs(r ctl.LsResult, all bool) string {
	if len(r.Rows) == 0 {
		return "no subscriptions\n"
	}
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	if all {
		_, _ = fmt.Fprintln(tw, "PANE\tPR\tEVENTS\tPOLL\tLAST\tCHECK-IN\tEXPIRES")
	}
	for _, row := range r.Rows {
		last := lastActivity(row, r.Now)
		var cells []string
		if all {
			checkin, expires := tableUntil(row.CheckinDue, r.Now, "due"), tableUntil(row.ExpiresAt, r.Now, "now")
			if row.Ending != "" {
				checkin, expires = "-", "ending: "+row.Ending
			}
			cells = []string{row.PaneID, row.PR, eventsLabel(row.Events), humanDur(row.Interval), last, checkin, expires}
		} else {
			cells = []string{row.PR, "events=" + eventsLabel(row.Events), "poll=" + humanDur(row.Interval), "last=" + last}
			if row.Ending != "" {
				cells = append(cells, "ending: "+row.Ending)
			} else {
				cells = append(cells, "check-in "+inOr(row.CheckinDue, r.Now, "due"), "expires "+inOr(row.ExpiresAt, r.Now, "now"))
			}
		}
		_, _ = fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	_ = tw.Flush() // writes to a strings.Builder cannot fail
	return b.String()
}

// formatStatus renders status (D-23, DMN-05). The auth line names the token
// source only, never the token (T-04-28).
func formatStatus(r ctl.StatusResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "daemon: running (version %s, pid %d, up %s)\n", r.Version, r.PID, humanDur(r.Now.Sub(r.StartedAt)))
	fmt.Fprintf(&b, "socket: %s\n", r.Socket)
	fmt.Fprintf(&b, "state: %s\n", r.StateDir)
	fmt.Fprintf(&b, "log: %s\n", r.LogPath)

	auth := r.AuthState
	if auth == "" {
		auth = "unknown"
	}
	if r.AuthSource != "" {
		auth += " (" + r.AuthSource + ")"
	}
	fmt.Fprintf(&b, "auth: %s\n", auth)

	if r.Limit == 0 {
		b.WriteString("rate limit: unknown (no GitHub response yet)")
	} else {
		fmt.Fprintf(&b, "rate limit: %d/%d remaining, resets in %s, spent %d in last hour",
			r.Remaining, r.Limit, humanDur(r.ResetAt.Sub(r.Now)), r.SpentLastHour)
	}
	if r.Stretch > 1 {
		fmt.Fprintf(&b, ", intervals stretched x%g", r.Stretch)
	}
	b.WriteByte('\n')
	if r.SecondaryUntil.After(r.Now) {
		fmt.Fprintf(&b, "secondary limit until %s (in %s)\n",
			r.SecondaryUntil.Format(time.RFC3339), humanDur(r.SecondaryUntil.Sub(r.Now)))
	}

	prNoun := "PRs"
	if r.PRs == 1 {
		prNoun = "PR"
	}
	fmt.Fprintf(&b, "subscriptions: %d on %d %s\n", r.Subs, r.PRs, prNoun)

	b.WriteString("herdr:\n")
	if len(r.Herdr) == 0 {
		b.WriteString("  none\n")
	}
	for _, s := range r.Herdr {
		state := "down"
		if s.Up {
			state = "up"
		}
		fmt.Fprintf(&b, "  %s %s", s.Socket, state)
		if !s.Since.IsZero() {
			fmt.Fprintf(&b, " since %s", ago(s.Since, r.Now))
		}
		fmt.Fprintf(&b, ", %d pending\n", s.Pending)
	}

	b.WriteString("recent cancellations:\n")
	if len(r.Cancellations) == 0 {
		b.WriteString("  none\n")
	}
	for _, c := range r.Cancellations {
		fmt.Fprintf(&b, "  %s %s %s %s\n", c.PR, c.PaneID, c.Reason, ago(c.At, r.Now))
	}

	if r.QuarantinedPath != "" {
		fmt.Fprintf(&b, "quarantined state: %s\n", r.QuarantinedPath)
	}
	if len(r.PRErrors) > 0 {
		b.WriteString("pr errors:\n")
		prs := make([]string, 0, len(r.PRErrors))
		for pr := range r.PRErrors {
			prs = append(prs, pr)
		}
		sort.Strings(prs)
		for _, pr := range prs {
			fmt.Fprintf(&b, "  %s: %s\n", pr, r.PRErrors[pr])
		}
	}
	return b.String()
}

// formatPoll renders the result of a forced poll.
func formatPoll(r ctl.PollResult) string {
	noun := "events"
	if r.NewEvents == 1 {
		noun = "event"
	}
	return fmt.Sprintf("polled %s: %d new %s (delivered to subscribed panes); polling every %s\n",
		r.PR, r.NewEvents, noun, humanDur(r.Interval))
}

// formatKeep renders a renewed subscription.
func formatKeep(r ctl.KeepResult, now time.Time) string {
	return fmt.Sprintf("kept %s; next check-in %s, expires %s\n",
		r.PR, inOr(r.CheckinDue, now, "due"), inOr(r.ExpiresAt, now, "now"))
}

// humanDur renders a duration tersely: whole days from 48h ("6d"), hours and
// minutes below that ("21h", "2h3m", "3m"), seconds under a minute ("58s").
// Values are truncated, and zero or negative durations are "now".
func humanDur(d time.Duration) string {
	if d < time.Second {
		return "now"
	}
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d < time.Minute:
		return fmt.Sprintf("%ds", d/time.Second)
	}
	h, m := d/time.Hour, (d%time.Hour)/time.Minute
	switch {
	case h == 0:
		return fmt.Sprintf("%dm", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dh%dm", h, m)
	}
}

// ago renders how long before now t was: "never" for the zero time and
// "just now" for t at or after now.
func ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	if d < time.Second {
		return "just now"
	}
	return humanDur(d) + " ago"
}

// inOr renders "in <dur>" for a future t, or past when t is not in the future.
func inOr(t, now time.Time, past string) string {
	if d := t.Sub(now); d >= time.Second {
		return "in " + humanDur(d)
	}
	return past
}

// tableUntil is inOr without the "in " prefix, for ls --all table cells.
func tableUntil(t, now time.Time, past string) string {
	if d := t.Sub(now); d >= time.Second {
		return humanDur(d)
	}
	return past
}

// lastActivity renders the LAST column: "never", or "3m ago (ci.failed)".
func lastActivity(row ctl.LsRow, now time.Time) string {
	s := ago(row.LastEventAt, now)
	if !row.LastEventAt.IsZero() && row.LastEventType != "" {
		s += " (" + row.LastEventType + ")"
	}
	return s
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
