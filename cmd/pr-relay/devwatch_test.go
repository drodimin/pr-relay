package main

import (
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/model"
)

// TestDevWatchOutput runs the real binary against the fake GitHub and checks
// the D-21 output: rendered event lines, a poll line with cost and budget,
// and per-PR interval and next poll time.
func TestDevWatchOutput(t *testing.T) {
	h := newHarness(t)
	ref := model.PRRef{Owner: "watch-owner", Repo: "watch-repo", Number: 7}
	prURL := h.gh.addPR(ref)

	start := time.Now()
	p := h.start(false, prURL)

	// The comment must land after the baseline poll, or it is absorbed.
	deadline := time.Now().Add(10 * time.Second)
	for h.gh.requestCount() == 0 {
		if time.Now().After(deadline) {
			_ = p.cmd.Process.Kill()
			_ = p.wait()
			t.Fatalf("no poll reached the fake\n%s", p.output())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if d := 500*time.Millisecond - time.Since(start); d > 0 {
		time.Sleep(d)
	}
	h.gh.addComment(ref, "IC_watch1")
	time.Sleep(2 * time.Second)

	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := p.wait(); err != nil {
		t.Fatalf("exit after SIGTERM: %v\n%s", err, p.output())
	}

	out := p.stdout.String()
	var event bool
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "[pr-relay] ") && strings.Contains(l, ref.String()) && strings.Contains(l, "otheruser") {
			event = true
		}
	}
	if !event {
		t.Errorf("no rendered event line for %s\n%s", ref, p.output())
	}
	if !regexp.MustCompile(`(?m)^poll prs=1 cost=\d+ remaining=\d+/\d+`).MatchString(out) {
		t.Errorf("no poll line\n%s", p.output())
	}
	if !regexp.MustCompile(`(?m)interval=.* next=`).MatchString(out) {
		t.Errorf("no interval/next line\n%s", p.output())
	}
}
