package diff

import (
	"sort"
	"strconv"
	"strings"

	"github.com/drodimin/pr-relay/internal/model"
)

// CI state machine (ci.failed, ci.settled).
//
// Only check runs (s.Checks) are read. The decoder already dropped
// StatusContext entries, so they never influence CI events (D-09).
//
// Discretion choices:
//   - Failed conclusions are FAILURE, TIMED_OUT, STARTUP_FAILURE and
//     ACTION_REQUIRED. ACTION_REQUIRED is often a fork-approval gate but is
//     still counted as failed. SUCCESS, NEUTRAL, SKIPPED, CANCELLED, STALE and
//     any unknown value are neutral.
//   - ci.failed fires as soon as a check concludes failed, at most once per
//     check Key per head SHA (D-06, D-08).
//   - ci.settled needs two consecutive snapshots in which every check is
//     COMPLETED with an identical fingerprint (sorted key@databaseId@conclusion).
//     This catches late-registered workflows without a clock. Any
//     non-COMPLETED status (including WAITING) clears the candidate.
//   - Zero check runs never settle: "no CI" cannot be told apart from "CI not
//     registered yet".
//   - A truncated check list (ChecksTruncated) suppresses ci.settled because
//     the complete set is unknown; ci.failed still fires.
//   - After a settle, a rerun re-arms tracking. A new ci.settled is emitted
//     only when the newly confirmed result differs from SettledResult (D-19).

const statusCompleted = "COMPLETED"

// IsFailed reports whether a check conclusion counts as a failure.
func IsFailed(conclusion string) bool {
	switch conclusion {
	case "FAILURE", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED":
		return true
	}
	return false
}

func checkFailed(c model.CheckRun) bool {
	return c.Status == statusCompleted && IsFailed(c.Conclusion)
}

// allComplete reports whether a settle can be evaluated for s: CI is known,
// the list is complete and non-empty, and every check is COMPLETED.
func allComplete(s model.Snapshot) bool {
	if !s.CIKnown || s.ChecksTruncated || len(s.Checks) == 0 {
		return false
	}
	for _, c := range s.Checks {
		if c.Status != statusCompleted {
			return false
		}
	}
	return true
}

func fingerprint(checks []model.CheckRun) string {
	parts := make([]string, 0, len(checks))
	for _, c := range checks {
		parts = append(parts, c.Key+"@"+strconv.FormatInt(c.DatabaseID, 10)+"@"+c.Conclusion)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n")
}

// failingSorted returns the failing checks sorted by Key.
func failingSorted(checks []model.CheckRun) []model.CheckRun {
	var out []model.CheckRun
	for _, c := range checks {
		if checkFailed(c) {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func settledResult(failing []model.CheckRun) string {
	if len(failing) == 0 {
		return "passed"
	}
	keys := make([]string, len(failing))
	for i, c := range failing {
		keys[i] = c.Key
	}
	return "failed:" + strings.Join(keys, ",")
}

// baselineCI records the current CI state silently.
func baselineCI(m *model.Memory, s model.Snapshot) {
	if !s.CIKnown {
		return
	}
	for _, c := range s.Checks {
		if checkFailed(c) {
			m.FailedReported[c.Key] = true
		}
	}
	if allComplete(s) {
		m.SettleCandidate = fingerprint(s.Checks)
		m.SettledResult = settledResult(failingSorted(s.Checks))
	}
}

// stepCI emits ci.failed and ci.settled events for the current head and
// updates per-head CI memory. Head-change resets happen in stepLifecycle.
func stepCI(m *model.Memory, s model.Snapshot) []model.Event {
	if !s.CIKnown {
		return nil
	}
	var evs []model.Event
	for _, c := range s.Checks {
		if !checkFailed(c) || m.FailedReported[c.Key] {
			continue
		}
		m.FailedReported[c.Key] = true
		evs = append(evs, model.Event{
			Type:          model.EventCIFailed,
			Key:           "ci.failed:" + s.HeadOID + ":" + c.Key,
			CheckName:     c.Name,
			CheckWorkflow: c.Workflow,
			CheckURL:      c.URL,
			HeadOID:       s.HeadOID,
		})
	}

	if !allComplete(s) {
		m.SettleCandidate = ""
		return evs
	}
	fp := fingerprint(s.Checks)
	if m.SettleCandidate != fp {
		// First observation of this complete set: wait for confirmation.
		m.SettleCandidate = fp
		return evs
	}
	failing := failingSorted(s.Checks)
	res := settledResult(failing)
	if res == m.SettledResult {
		return evs
	}
	m.SettledResult = res
	m.SettleSeq++
	e := model.Event{
		Type:    model.EventCISettled,
		Key:     "ci.settled:" + s.HeadOID + ":" + strconv.Itoa(m.SettleSeq),
		HeadOID: s.HeadOID,
		Passed:  len(failing) == 0,
		Total:   len(s.Checks),
	}
	for _, c := range failing {
		e.FailedNames = append(e.FailedNames, c.Name)
	}
	return append(evs, e)
}
