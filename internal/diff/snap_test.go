package diff

import (
	"fmt"
	"strings"

	"github.com/drodimin/pr-relay/internal/model"
)

// snap builds a test snapshot. Defaults: Ref o/r#1, State OPEN, Mergeable
// MERGEABLE, HeadOID "h1", BaseOID "b1", CIKnown true, no checks.
func snap(opts ...func(*model.Snapshot)) model.Snapshot {
	s := model.Snapshot{
		Ref:       model.PRRef{Owner: "o", Repo: "r", Number: 1},
		NodeID:    "PR_1",
		State:     "OPEN",
		HeadOID:   "h1",
		BaseOID:   "b1",
		Mergeable: "MERGEABLE",
		CIKnown:   true,
	}
	for _, o := range opts {
		o(&s)
	}
	return s
}

func withHead(oid string) func(*model.Snapshot) {
	return func(s *model.Snapshot) { s.HeadOID = oid }
}

func withBase(oid string) func(*model.Snapshot) {
	return func(s *model.Snapshot) { s.BaseOID = oid }
}

func withMergeable(v string) func(*model.Snapshot) {
	return func(s *model.Snapshot) { s.Mergeable = v }
}

func withState(v string) func(*model.Snapshot) {
	return func(s *model.Snapshot) { s.State = v }
}

func withMergedBy(login string) func(*model.Snapshot) {
	return func(s *model.Snapshot) { s.MergedBy = model.Actor{Login: login, Type: "User"} }
}

func withClosedBy(login string) func(*model.Snapshot) {
	return func(s *model.Snapshot) { s.ClosedBy = model.Actor{Login: login, Type: "User"} }
}

// withCommits sets the commit list (oldest first). Each commit is authored by
// "a-<oid>" as a User.
func withCommits(oids ...string) func(*model.Snapshot) {
	return func(s *model.Snapshot) {
		s.Commits = nil
		for _, o := range oids {
			s.Commits = append(s.Commits, model.Commit{OID: o, Author: model.Actor{Login: "a-" + o, Type: "User"}})
		}
		s.CommitsTotal = len(oids)
	}
}

func withForcePush(id, before, after, login string) func(*model.Snapshot) {
	return func(s *model.Snapshot) {
		s.ForcePushes = append(s.ForcePushes, model.ForcePush{
			ID: id, BeforeOID: before, AfterOID: after,
			Actor: model.Actor{Login: login, Type: "User"},
		})
	}
}

// withCheck adds a check run. key is "workflow/name".
func withCheck(key, status, conclusion string, dbid int64) func(*model.Snapshot) {
	return func(s *model.Snapshot) {
		wf, name, ok := strings.Cut(key, "/")
		if !ok {
			wf, name = "", key
		}
		s.Checks = append(s.Checks, model.CheckRun{
			Key: key, Name: name, Workflow: wf, App: "github-actions",
			DatabaseID: dbid, Status: status, Conclusion: conclusion,
			URL:        fmt.Sprintf("https://github.com/o/r/runs/%d", dbid),
			DetailsURL: fmt.Sprintf("https://github.com/o/r/actions/runs/%d/job/1", dbid),
			RunAttempt: 1,
		})
	}
}

func noCI() func(*model.Snapshot) {
	return func(s *model.Snapshot) { s.CIKnown = false }
}

func truncated() func(*model.Snapshot) {
	return func(s *model.Snapshot) { s.ChecksTruncated = true }
}

// run baselines on the first snapshot then steps through the rest, returning
// the events of each step and the final memory.
func run(snaps ...model.Snapshot) ([][]model.Event, model.Memory) {
	m := Baseline(snaps[0])
	var out [][]model.Event
	for _, s := range snaps[1:] {
		r := Step(m, s)
		out = append(out, r.Events)
		m = r.Memory
	}
	return out, m
}

func flatten(steps [][]model.Event) []model.Event {
	var all []model.Event
	for _, s := range steps {
		all = append(all, s...)
	}
	return all
}

func ofType(evs []model.Event, t model.EventType) []model.Event {
	var out []model.Event
	for _, e := range evs {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}
