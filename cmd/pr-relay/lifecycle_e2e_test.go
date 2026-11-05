package main

import (
	"strings"
	"testing"
	"time"
)

// TestLifecycleE2E is ROADMAP SC-4 (LIFE-06, LIFE-07, D-11..D-14): with
// short thresholds a quiet subscription gets the check-in line, keep resets
// the clock, and the expired line arrives before the subscription is
// removed and recorded as a cancellation.
func TestLifecycleE2E(t *testing.T) {
	e := newE2E(t, "\n[lifecycle]\ncheckin_after = \"2s\"\nexpire_after = \"6s\"\n")
	e.gh.addPR(e2eRef)
	e.mustRun("w1:p1", "sub", e2eURL)

	const checkin = "check-in: no activity for"
	e.waitPrompt("w1:p1", checkin, 5*time.Second)
	line := e.prompts("w1:p1", checkin)[0]
	for _, w := range []string{"[pr-relay] o/r#12 ", "pr-relay keep " + e2eURL, "pr-relay unsub " + e2eURL} {
		if !strings.Contains(line, w) {
			t.Errorf("check-in line lacks %q: %q", w, line)
		}
	}

	r := e.mustRun("w1:p1", "keep", e2eURL)
	if !strings.HasPrefix(r.stdout, "kept o/r#12") {
		t.Fatalf("keep:\n%s", r)
	}
	kept := len(e.prompts("w1:p1", checkin))
	time.Sleep(1500 * time.Millisecond)
	if n := len(e.prompts("w1:p1", checkin)); n != kept {
		t.Fatalf("a check-in arrived within 1.5s of keep (%d -> %d): %q", kept, n, e.promptTexts("w1:p1"))
	}

	e.waitPrompt("w1:p1", "expired: no activity for", 15*time.Second)
	exp := e.prompts("w1:p1", "expired: no activity for")[0]
	if !strings.Contains(exp, "re-subscribe: pr-relay sub "+e2eURL) {
		t.Errorf("expired line lacks the re-subscribe command: %q", exp)
	}
	e.eventually(10*time.Second, "subscription removal after expiry", func() bool {
		return strings.Contains(e.mustRun("", "ls", "--all").stdout, "no subscriptions")
	})
	st := e.mustRun("", "status")
	if !strings.Contains(st.stdout, "o/r#12 w1:p1 expired") {
		t.Errorf("status lists no expired cancellation:\n%s", st)
	}
}
