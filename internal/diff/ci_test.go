package diff

import (
	"reflect"
	"testing"

	"github.com/drodimin/pr-relay/internal/model"
)

const (
	cDone = "COMPLETED"
	cRun  = "IN_PROGRESS"
)

func TestConclusionMapping(t *testing.T) {
	cases := map[string]bool{
		"ACTION_REQUIRED": true,
		"CANCELLED":       false,
		"FAILURE":         true,
		"NEUTRAL":         false,
		"SKIPPED":         false,
		"STALE":           false,
		"STARTUP_FAILURE": true,
		"SUCCESS":         false,
		"TIMED_OUT":       true,
		"":                false,
		"SOMETHING_NEW":   false,
	}
	for c, want := range cases {
		if got := IsFailed(c); got != want {
			t.Errorf("IsFailed(%q) = %v, want %v", c, got, want)
		}
	}
}

func TestCIFailedOncePerSHA(t *testing.T) {
	steps, _ := run(
		snap(withCheck("ci/test", cRun, "", 1)),
		snap(withCheck("ci/test", cDone, "FAILURE", 1)),
		snap(withCheck("ci/test", cDone, "FAILURE", 1)),
	)
	f := ofType(steps[0], model.EventCIFailed)
	if len(f) != 1 {
		t.Fatalf("want 1 ci.failed, got %+v", steps[0])
	}
	e := f[0]
	if e.CheckName != "test" || e.CheckWorkflow != "ci" || e.CheckURL != "https://github.com/o/r/runs/1" ||
		e.HeadOID != "h1" || e.Key != "ci.failed:h1:ci/test" {
		t.Fatalf("ci.failed fields wrong: %+v", e)
	}
	if n := len(ofType(steps[1], model.EventCIFailed)); n != 0 {
		t.Fatalf("repeat emitted %d ci.failed", n)
	}
}

func TestCIFailRerunFail(t *testing.T) {
	steps, _ := run(
		snap(withCheck("ci/test", cRun, "", 1)),
		snap(withCheck("ci/test", cDone, "FAILURE", 1)),
		snap(withCheck("ci/test", cRun, "", 2)),
		snap(withCheck("ci/test", cDone, "FAILURE", 2)),
		snap(withCheck("ci/test", cDone, "FAILURE", 2)),
	)
	if n := len(ofType(flatten(steps), model.EventCIFailed)); n != 1 {
		t.Fatalf("D-08: want exactly 1 ci.failed, got %d", n)
	}
}

func TestCIFailRerunPass(t *testing.T) {
	steps, _ := run(
		snap(withCheck("ci/test", cRun, "", 1)),
		snap(withCheck("ci/test", cDone, "FAILURE", 1)),
		snap(withCheck("ci/test", cRun, "", 2)),
		snap(withCheck("ci/test", cDone, "SUCCESS", 2)),
		snap(withCheck("ci/test", cDone, "SUCCESS", 2)),
	)
	all := flatten(steps)
	if n := len(ofType(all, model.EventCIFailed)); n != 1 {
		t.Fatalf("want 1 ci.failed, got %d", n)
	}
	// Nothing extra on the passing rerun itself.
	if len(steps[2]) != 0 {
		t.Fatalf("passing rerun emitted immediately: %+v", steps[2])
	}
	s := ofType(all, model.EventCISettled)
	if len(s) != 1 || !s[0].Passed {
		t.Fatalf("settled should report final passing state: %+v", s)
	}
}

func TestCIFailedResetOnNewHead(t *testing.T) {
	steps, _ := run(
		snap(withCommits("h1"), withCheck("ci/test", cRun, "", 1)),
		snap(withCommits("h1"), withCheck("ci/test", cDone, "FAILURE", 1)),
		snap(withHead("h2"), withCommits("h1", "h2"), withCheck("ci/test", cRun, "", 3)),
		snap(withHead("h2"), withCommits("h1", "h2"), withCheck("ci/test", cDone, "FAILURE", 3)),
	)
	f := ofType(flatten(steps), model.EventCIFailed)
	if len(f) != 2 || f[1].Key != "ci.failed:h2:ci/test" || f[1].HeadOID != "h2" {
		t.Fatalf("D-06 reset: %+v", f)
	}
}

func TestSettledConfirmation(t *testing.T) {
	steps, m := run(
		snap(withCheck("ci/build", cRun, "", 1), withCheck("ci/test", cRun, "", 2)),
		snap(withCheck("ci/build", cDone, "SUCCESS", 1), withCheck("ci/test", cDone, "FAILURE", 2)),
		snap(withCheck("ci/build", cDone, "SUCCESS", 1), withCheck("ci/test", cDone, "FAILURE", 2)),
		snap(withCheck("ci/build", cDone, "SUCCESS", 1), withCheck("ci/test", cDone, "FAILURE", 2)),
	)
	if n := len(ofType(steps[0], model.EventCISettled)); n != 0 {
		t.Fatalf("settled on first all-complete observation")
	}
	if m.SettleSeq != 1 {
		t.Fatalf("SettleSeq %d", m.SettleSeq)
	}
	s := ofType(steps[1], model.EventCISettled)
	if len(s) != 1 {
		t.Fatalf("want settled on confirmation, got %+v", steps[1])
	}
	e := s[0]
	if e.Passed || e.Total != 2 || !reflect.DeepEqual(e.FailedNames, []string{"test"}) ||
		e.HeadOID != "h1" || e.Key != "ci.settled:h1:1" {
		t.Fatalf("settled fields wrong: %+v", e)
	}
	if len(steps[2]) != 0 {
		t.Fatalf("repeat emitted: %+v", steps[2])
	}
}

func TestSettledFingerprintChange(t *testing.T) {
	steps, _ := run(
		snap(withCheck("ci/build", cRun, "", 1)),
		snap(withCheck("ci/build", cDone, "SUCCESS", 1)),
		// Late-registered workflow appears already complete.
		snap(withCheck("ci/build", cDone, "SUCCESS", 1), withCheck("lint/lint", cDone, "FAILURE", 5)),
		snap(withCheck("ci/build", cDone, "SUCCESS", 1), withCheck("lint/lint", cDone, "FAILURE", 5)),
	)
	if n := len(ofType(steps[1], model.EventCISettled)); n != 0 {
		t.Fatalf("settled despite fingerprint change")
	}
	s := ofType(flatten(steps), model.EventCISettled)
	if len(s) != 1 || s[0].Passed || s[0].Total != 2 {
		t.Fatalf("want one failed settled with 2 checks: %+v", s)
	}
}

func TestSettledWaitingNotComplete(t *testing.T) {
	steps, m := run(
		snap(withCheck("ci/build", cRun, "", 1), withCheck("deploy/prod", "WAITING", "", 2)),
		snap(withCheck("ci/build", cDone, "SUCCESS", 1), withCheck("deploy/prod", "WAITING", "", 2)),
		snap(withCheck("ci/build", cDone, "SUCCESS", 1), withCheck("deploy/prod", "WAITING", "", 2)),
		snap(withCheck("ci/build", cDone, "SUCCESS", 1), withCheck("deploy/prod", "WAITING", "", 2)),
	)
	if n := len(ofType(flatten(steps), model.EventCISettled)); n != 0 {
		t.Fatalf("settled while WAITING")
	}
	if m.SettleCandidate != "" {
		t.Fatalf("candidate kept while incomplete: %q", m.SettleCandidate)
	}
}

func TestSettledZeroChecks(t *testing.T) {
	steps, _ := run(snap(), snap(), snap(), snap())
	if n := len(flatten(steps)); n != 0 {
		t.Fatalf("zero checks emitted %d events", n)
	}
}

func TestCIUnknownRollup(t *testing.T) {
	m := Baseline(snap(withCheck("ci/test", cRun, "", 1)))
	m.SettleCandidate = "keep"
	before := m.Clone()
	r := Step(m, snap(noCI(), withCheck("ci/test", cDone, "FAILURE", 1)))
	if len(r.Events) != 0 {
		t.Fatalf("CIKnown=false emitted: %+v", r.Events)
	}
	if !reflect.DeepEqual(r.Memory.FailedReported, before.FailedReported) ||
		r.Memory.SettleCandidate != "keep" || r.Memory.SettledResult != before.SettledResult ||
		r.Memory.SettleSeq != before.SettleSeq {
		t.Fatalf("CIKnown=false touched CI memory: %+v", r.Memory)
	}
}

func TestSettledRearmD19(t *testing.T) {
	steps, _ := run(
		snap(withCheck("ci/test", cRun, "", 1)),
		snap(withCheck("ci/test", cDone, "FAILURE", 1)),
		snap(withCheck("ci/test", cDone, "FAILURE", 1)), // settled failed:test (seq 1)
		snap(withCheck("ci/test", cRun, "", 2)),         // rerun re-arms
		snap(withCheck("ci/test", cDone, "SUCCESS", 2)),
		snap(withCheck("ci/test", cDone, "SUCCESS", 2)), // settled passed (seq 2)
		snap(withCheck("ci/test", cDone, "SUCCESS", 2)),
	)
	s := ofType(flatten(steps), model.EventCISettled)
	if len(s) != 2 {
		t.Fatalf("want 2 settled, got %+v", s)
	}
	if s[0].Passed || s[0].Key != "ci.settled:h1:1" {
		t.Fatalf("first settled: %+v", s[0])
	}
	if !s[1].Passed || s[1].Key != "ci.settled:h1:2" || len(s[1].FailedNames) != 0 {
		t.Fatalf("second settled: %+v", s[1])
	}
}

func TestSettledRearmSameResult(t *testing.T) {
	steps, m := run(
		snap(withCheck("ci/test", cRun, "", 1)),
		snap(withCheck("ci/test", cDone, "FAILURE", 1)),
		snap(withCheck("ci/test", cDone, "FAILURE", 1)),
		snap(withCheck("ci/test", cRun, "", 2)),
		snap(withCheck("ci/test", cDone, "FAILURE", 2)),
		snap(withCheck("ci/test", cDone, "FAILURE", 2)),
		snap(withCheck("ci/test", cDone, "FAILURE", 2)),
	)
	all := flatten(steps)
	if n := len(ofType(all, model.EventCISettled)); n != 1 {
		t.Fatalf("same result re-settled: %d settled events", n)
	}
	if n := len(ofType(all, model.EventCIFailed)); n != 1 {
		t.Fatalf("rerun fail re-fired ci.failed: %d", n)
	}
	if m.SettleSeq != 1 {
		t.Fatalf("SettleSeq %d", m.SettleSeq)
	}
}

func TestBaselineCI(t *testing.T) {
	s := snap(
		withCheck("ci/build", cDone, "SUCCESS", 1),
		withCheck("ci/test", cDone, "TIMED_OUT", 2),
	)
	m := Baseline(s)
	if m.SettledResult != "failed:ci/test" {
		t.Fatalf("baseline SettledResult %q", m.SettledResult)
	}
	if !m.FailedReported["ci/test"] || m.FailedReported["ci/build"] {
		t.Fatalf("baseline FailedReported %+v", m.FailedReported)
	}
	steps, _ := run(s, s, s)
	if n := len(flatten(steps)); n != 0 {
		t.Fatalf("identical steps after CI baseline emitted %d", n)
	}
}

func TestSettledTruncated(t *testing.T) {
	steps, _ := run(
		snap(truncated(), withCheck("ci/test", cRun, "", 1)),
		snap(truncated(), withCheck("ci/test", cDone, "FAILURE", 1)),
		snap(truncated(), withCheck("ci/test", cDone, "FAILURE", 1)),
		snap(truncated(), withCheck("ci/test", cDone, "FAILURE", 1)),
	)
	all := flatten(steps)
	if n := len(ofType(all, model.EventCISettled)); n != 0 {
		t.Fatalf("settled while truncated")
	}
	if n := len(ofType(all, model.EventCIFailed)); n != 1 {
		t.Fatalf("ci.failed should still fire when truncated: %d", n)
	}
}
