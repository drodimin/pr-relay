package poller

import "time"

// Auth states reported in AuthState.State.
const (
	AuthUnknown      = "unknown"
	AuthOK           = "ok"
	AuthNoToken      = "no_token"
	AuthUnauthorized = "unauthorized"
)

// AuthState is the scheduler's view of GitHub authentication. Source names
// where the token came from (GH_TOKEN, hosts.yml, ...); the token itself is
// never exposed (T-02-38).
type AuthState struct {
	State  string
	Source string
	// Since is when State last changed (zero while unknown).
	Since time.Time
}

// Status is a point-in-time snapshot of the poller for the status command
// (Phase 4).
type Status struct {
	Auth AuthState

	Limit, Remaining int
	ResetAt          time.Time
	SpentLastHour    int
	Stretch          float64
	SecondaryUntil   time.Time
	BatchSize        int

	// PRs counts watched PR records, Subs subscriptions.
	PRs, Subs int

	// QuarantinedPath is the corrupt state file set aside at startup (D-20).
	QuarantinedPath string
	QuarantinedAt   time.Time

	// Cancellations counts the in-memory recent-cancellations ring (D-17);
	// RecentCancellations returns the entries.
	Cancellations int

	// PRErrors maps PR key to its last error class, for PRs that have one.
	PRErrors map[string]string
}

// Status returns a copy of the poller's current state.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	b := e.budget
	s := Status{
		Auth:            e.auth,
		Limit:           b.Limit,
		Remaining:       b.Remaining,
		ResetAt:         b.ResetAt,
		SpentLastHour:   b.Spent(now),
		Stretch:         e.stretch,
		SecondaryUntil:  b.SecondaryUntil,
		BatchSize:       b.BatchSize,
		PRs:             len(e.state.PRs),
		Subs:            len(e.state.Subs),
		QuarantinedPath: e.quarPath,
		QuarantinedAt:   e.quarAt,
		Cancellations:   len(e.cancels),
	}
	if s.Auth.State == "" {
		s.Auth.State = AuthUnknown
	}
	for k, rec := range e.state.PRs {
		if rec.LastError == "" {
			continue
		}
		if s.PRErrors == nil {
			s.PRErrors = map[string]string{}
		}
		s.PRErrors[k] = rec.LastError
	}
	return s
}

// SetQuarantine records that a corrupt state file was moved aside to path at
// startup, so Status can report it (D-20).
func (e *Engine) SetQuarantine(path string, at time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.quarPath, e.quarAt = path, at
}
