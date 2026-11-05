package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/ctl"
	"github.com/drodimin/pr-relay/internal/testutil"
)

var fmtNow = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func TestFormatHumanDur(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{58 * time.Second, "58s"},
		{3 * time.Minute, "3m"},
		{21 * time.Hour, "21h"},
		{24 * time.Hour, "24h"},
		{6 * 24 * time.Hour, "6d"},
		{2*time.Hour + 3*time.Minute, "2h3m"},
		{58*time.Second + 400*time.Millisecond, "58s"},
		{0, "now"},
		{-time.Minute, "now"},
	}
	for _, tc := range cases {
		if got := humanDur(tc.d); got != tc.want {
			t.Errorf("humanDur(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
	if got := ago(time.Time{}, fmtNow); got != "never" {
		t.Errorf("ago(zero) = %q, want never", got)
	}
	if got := ago(fmtNow.Add(-3*time.Minute), fmtNow); got != "3m ago" {
		t.Errorf("ago(-3m) = %q", got)
	}
}

func subFixture() ctl.SubResult {
	return ctl.SubResult{
		PR:            "o/r#12",
		URL:           "https://github.com/o/r/pull/12",
		Created:       true,
		Interval:      time.Minute,
		NextPollAt:    fmtNow.Add(58 * time.Second),
		CoSubscribers: []string{"w2:p1"},
	}
}

func TestFormatSub(t *testing.T) {
	r := subFixture()
	want := "subscribed o/r#12 events=all\n" +
		"polling every 1m (next in 58s); also watched by: w2:p1\n" +
		"updates arrive in this pane; unsub: pr-relay unsub https://github.com/o/r/pull/12\n"
	got := formatSub(r, fmtNow)
	if got != want {
		t.Errorf("formatSub =\n%s\nwant\n%s", got, want)
	}

	var b strings.Builder
	b.WriteString("# co-subscriber, all events\n")
	b.WriteString(got)

	r2 := subFixture()
	r2.CoSubscribers = nil
	r2.Events = []string{"ci.failed", "ci.settled"}
	b.WriteString("# no co-subscribers, ci mask\n")
	s2 := formatSub(r2, fmtNow)
	if !strings.Contains(s2, "\npolling every 1m (next in 58s)\n") {
		t.Errorf("no co-subscriber line 2 wrong:\n%s", s2)
	}
	if !strings.HasPrefix(s2, "subscribed o/r#12 events=ci.failed,ci.settled (closed always included)\n") {
		t.Errorf("mask line 1 wrong:\n%s", s2)
	}
	b.WriteString(s2)

	r3 := subFixture()
	r3.Created = false
	r3.CoSubscribers = []string{"w2:p1", "/tmp/other.sock:w1:p3"}
	b.WriteString("# updated, two co-subscribers\n")
	s3 := formatSub(r3, fmtNow)
	if !strings.HasPrefix(s3, "updated o/r#12 events=all\n") {
		t.Errorf("updated line 1 wrong:\n%s", s3)
	}
	b.WriteString(s3)

	if n := strings.Count(got, "\n"); n < 1 || n > 3 {
		t.Errorf("formatSub emitted %d lines, want 1-3", n)
	}
	testutil.Golden(t, "testdata/sub.golden", []byte(b.String()))
}

func lsFixture() ctl.LsResult {
	return ctl.LsResult{
		Now: fmtNow,
		Rows: []ctl.LsRow{
			{
				PR: "o/r#12", URL: "https://github.com/o/r/pull/12",
				HerdrSocket: "/h/herdr.sock", PaneID: "w1:p1",
				Interval:    time.Minute,
				LastEventAt: fmtNow.Add(-3 * time.Minute), LastEventType: "ci.failed",
				CheckinDue: fmtNow.Add(21 * time.Hour), ExpiresAt: fmtNow.Add(6 * 24 * time.Hour),
			},
			{
				PR: "octo/widgets#345", URL: "https://github.com/octo/widgets/pull/345",
				HerdrSocket: "/h/herdr.sock", PaneID: "w2:p10",
				Events:     []string{"review", "comment"},
				Interval:   4 * time.Minute,
				CheckinDue: fmtNow.Add(23*time.Hour + 59*time.Minute), ExpiresAt: fmtNow.Add(7 * 24 * time.Hour),
			},
			{
				PR: "o/r#9", URL: "https://github.com/o/r/pull/9",
				HerdrSocket: "/h/herdr.sock", PaneID: "w1:p1",
				Interval:    16 * time.Minute,
				LastEventAt: fmtNow.Add(-2*time.Hour - 3*time.Minute), LastEventType: "closed",
				Ending: "closed",
			},
			{
				PR: "o/r#7", URL: "https://github.com/o/r/pull/7",
				HerdrSocket: "/h/herdr.sock", PaneID: "w3:p2",
				Interval:    16 * time.Minute,
				LastEventAt: fmtNow.Add(-8 * 24 * time.Hour), LastEventType: "comment",
				Ending: "expired",
			},
		},
	}
}

func TestFormatLs(t *testing.T) {
	res := lsFixture()
	one := ctl.LsResult{Now: fmtNow, Rows: res.Rows[:1]}
	want := "o/r#12  events=all  poll=1m  last=3m ago (ci.failed)  check-in in 21h  expires in 6d\n"
	if got := formatLs(one, false); got != want {
		t.Errorf("formatLs single row =\n%q\nwant\n%q", got, want)
	}
	if got := formatLs(ctl.LsResult{Now: fmtNow}, false); got != "no subscriptions\n" {
		t.Errorf("formatLs empty = %q", got)
	}
	if got := formatLs(ctl.LsResult{Now: fmtNow}, true); got != "no subscriptions\n" {
		t.Errorf("formatLs --all empty = %q", got)
	}

	pane := formatLs(res, false)
	if !strings.Contains(pane, "last=never") || !strings.Contains(pane, "ending: closed") || !strings.Contains(pane, "ending: expired") {
		t.Errorf("formatLs missing never/ending:\n%s", pane)
	}
	testutil.Golden(t, "testdata/ls.golden", []byte(pane))

	all := formatLs(res, true)
	if !strings.HasPrefix(all, "PANE") {
		t.Errorf("ls --all must start with the PANE header:\n%s", all)
	}
	testutil.Golden(t, "testdata/ls_all.golden", []byte(all))
}

func statusFixture() ctl.StatusResult {
	return ctl.StatusResult{
		Version:       "0.4.0",
		PID:           4242,
		StartedAt:     fmtNow.Add(-2*time.Hour - 3*time.Minute),
		Now:           fmtNow,
		Socket:        "/run/user/501/pr-relay/pr-relay.sock",
		StateDir:      "/home/u/.local/state/pr-relay",
		LogPath:       "/home/u/.local/state/pr-relay/daemon.log",
		AuthState:     "ok",
		AuthSource:    "GH_TOKEN",
		Limit:         5000,
		Remaining:     4321,
		ResetAt:       fmtNow.Add(12 * time.Minute),
		SpentLastHour: 30,
		Stretch:       1,
		PRs:           2,
		Subs:          3,
		Cancellations: []ctl.CancelRow{
			{PR: "o/r#12", PaneID: "w1:p1", Reason: "pane_closed", At: fmtNow.Add(-5 * time.Minute)},
		},
		Herdr: []ctl.SocketRow{
			{Socket: "/path/herdr.sock", Up: true, Since: fmtNow.Add(-10 * time.Minute), Pending: 2},
		},
	}
}

func TestFormatStatus(t *testing.T) {
	healthy := formatStatus(statusFixture())
	for _, w := range []string{
		"daemon: running (version 0.4.0, pid 4242, up 2h3m)\n",
		"socket: /run/user/501/pr-relay/pr-relay.sock\n",
		"state: /home/u/.local/state/pr-relay\n",
		"log: /home/u/.local/state/pr-relay/daemon.log\n",
		"auth: ok (GH_TOKEN)\n",
		"rate limit: 4321/5000 remaining, resets in 12m, spent 30 in last hour\n",
		"subscriptions: 3 on 2 PRs\n",
		"herdr:\n  /path/herdr.sock up since 10m ago, 2 pending\n",
		"recent cancellations:\n  o/r#12 w1:p1 pane_closed 5m ago\n",
	} {
		if !strings.Contains(healthy, w) {
			t.Errorf("status missing %q:\n%s", w, healthy)
		}
	}
	for _, nw := range []string{"secondary limit", "quarantined state", "pr errors", "{"} {
		if strings.Contains(healthy, nw) {
			t.Errorf("healthy status unexpectedly contains %q:\n%s", nw, healthy)
		}
	}

	deg := statusFixture()
	deg.AuthState = "unauthorized"
	deg.Remaining = 12
	deg.Stretch = 2
	deg.SecondaryUntil = fmtNow.Add(90 * time.Second)
	deg.QuarantinedPath = "/home/u/.local/state/pr-relay/state.json.corrupt-1"
	deg.PRErrors = map[string]string{"o/r#9": "not found", "a/b#1": "forbidden"}
	deg.Cancellations = nil
	deg.Herdr = []ctl.SocketRow{
		{Socket: "/path/herdr.sock", Up: true, Since: fmtNow.Add(-10 * time.Minute), Pending: 0},
		{Socket: "/other/herdr.sock", Up: false, Since: fmtNow.Add(-3 * time.Minute), Pending: 4},
	}
	degraded := formatStatus(deg)
	for _, w := range []string{
		"recent cancellations:\n  none\n",
		"  /other/herdr.sock down since 3m ago, 4 pending\n",
		"quarantined state: /home/u/.local/state/pr-relay/state.json.corrupt-1\n",
		"secondary limit until",
		"pr errors:\n  a/b#1: forbidden\n  o/r#9: not found\n",
	} {
		if !strings.Contains(degraded, w) {
			t.Errorf("degraded status missing %q:\n%s", w, degraded)
		}
	}

	none := statusFixture()
	none.AuthSource = ""
	none.AuthState = "no_token"
	none.Limit, none.Remaining, none.ResetAt, none.SpentLastHour = 0, 0, time.Time{}, 0
	none.Herdr = nil
	none.Cancellations = nil
	unknown := formatStatus(none)
	if !strings.Contains(unknown, "auth: no_token\n") || !strings.Contains(unknown, "herdr:\n  none\n") {
		t.Errorf("no-data status wrong:\n%s", unknown)
	}

	out := "# healthy\n" + healthy + "# degraded\n" + degraded + "# no data yet\n" + unknown
	testutil.Golden(t, "testdata/status.golden", []byte(out))
}

func TestFormatPollKeep(t *testing.T) {
	p := ctl.PollResult{PR: "o/r#12", NewEvents: 2, Interval: time.Minute, NextPollAt: fmtNow.Add(time.Minute)}
	if got, want := formatPoll(p), "polled o/r#12: 2 new events (delivered to subscribed panes); polling every 1m\n"; got != want {
		t.Errorf("formatPoll = %q, want %q", got, want)
	}
	p.NewEvents = 1
	if got := formatPoll(p); !strings.Contains(got, "1 new event (") {
		t.Errorf("formatPoll singular = %q", got)
	}
	k := ctl.KeepResult{PR: "o/r#12", CheckinDue: fmtNow.Add(24 * time.Hour), ExpiresAt: fmtNow.Add(7 * 24 * time.Hour)}
	if got, want := formatKeep(k, fmtNow), "kept o/r#12; next check-in in 24h, expires in 7d\n"; got != want {
		t.Errorf("formatKeep = %q, want %q", got, want)
	}
}
