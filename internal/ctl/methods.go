package ctl

import "time"

// This file is the method contract shared by the daemon handlers and the CLI
// commands. It holds only names and types.

// Method names. hello and shutdown are exempt from the version gate and
// their schemas never change.
const (
	MethodHello    = "hello"
	MethodShutdown = "shutdown"
	MethodSub      = "sub"
	MethodUnsub    = "unsub"
	MethodKeep     = "keep"
	MethodPoll     = "poll"
	MethodLs       = "ls"
	MethodStatus   = "status"
	MethodReset    = "reset"
)

// Target is a delivery destination: the canonical herdr socket (as resolved
// by herdr.ResolveSocket) and the pane on it.
type Target struct {
	HerdrSocket string `json:"herdr_socket"`
	PaneID      string `json:"pane_id"`
}

// SubParams subscribes Target to a PR. Empty Events means all event types.
type SubParams struct {
	URL    string   `json:"url"`
	Events []string `json:"events,omitempty"`
	Target
}

// SubResult describes the subscription after sub.
type SubResult struct {
	PR         string        `json:"pr"` // "owner/repo#N"
	URL        string        `json:"url"`
	Events     []string      `json:"events,omitempty"` // normalised; empty = all
	Created    bool          `json:"created"`
	Interval   time.Duration `json:"interval"`
	NextPollAt time.Time     `json:"next_poll_at"`
	// CoSubscribers lists other panes on the PR: pane IDs, as "socket:pane"
	// when their herdr socket differs from the caller's.
	CoSubscribers []string `json:"co_subscribers,omitempty"`
	Warning       string   `json:"warning,omitempty"` // D-10
}

// UnsubParams removes Target's subscription to URL, or every subscription of
// the pane when All is set.
type UnsubParams struct {
	URL string `json:"url,omitempty"`
	All bool   `json:"all,omitempty"`
	Target
}

// UnsubResult reports how many subscriptions were removed.
type UnsubResult struct {
	Removed int `json:"removed"`
}

// KeepParams renews Target's subscription to URL.
type KeepParams struct {
	URL string `json:"url"`
	Target
}

// KeepResult reports the renewed check-in and expiry times.
type KeepResult struct {
	PR         string    `json:"pr"`
	CheckinDue time.Time `json:"checkin_due"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// PollParams asks for an immediate poll of URL.
type PollParams struct {
	URL string `json:"url"`
}

// PollResult reports what the forced poll found.
type PollResult struct {
	PR         string        `json:"pr"`
	NewEvents  int           `json:"new_events"`
	Interval   time.Duration `json:"interval"`
	NextPollAt time.Time     `json:"next_poll_at"`
}

// LsParams lists subscriptions. An empty PaneID (or All) lists every pane.
type LsParams struct {
	All bool `json:"all,omitempty"`
	Target
}

// LsRow is one subscription in ls output.
type LsRow struct {
	PR            string        `json:"pr"`
	URL           string        `json:"url"`
	HerdrSocket   string        `json:"herdr_socket"`
	PaneID        string        `json:"pane_id"`
	Events        []string      `json:"events,omitempty"`
	Interval      time.Duration `json:"interval"`
	LastEventAt   time.Time     `json:"last_event_at"`
	LastEventType string        `json:"last_event_type,omitempty"`
	CheckinDue    time.Time     `json:"checkin_due"`
	ExpiresAt     time.Time     `json:"expires_at"`
	Ending        string        `json:"ending,omitempty"`
}

// LsResult is the ls reply.
type LsResult struct {
	Now  time.Time `json:"now"`
	Rows []LsRow   `json:"rows"`
}

// StatusResult is the status reply.
type StatusResult struct {
	Version         string            `json:"version"`
	PID             int               `json:"pid"`
	StartedAt       time.Time         `json:"started_at"`
	Now             time.Time         `json:"now"`
	Socket          string            `json:"socket"`
	StateDir        string            `json:"state_dir"`
	LogPath         string            `json:"log_path"`
	AuthState       string            `json:"auth_state"`
	AuthSource      string            `json:"auth_source"`
	Limit           int               `json:"limit"`
	Remaining       int               `json:"remaining"`
	ResetAt         time.Time         `json:"reset_at"`
	SpentLastHour   int               `json:"spent_last_hour"`
	Stretch         float64           `json:"stretch"`
	SecondaryUntil  time.Time         `json:"secondary_until"`
	PRs             int               `json:"prs"`
	Subs            int               `json:"subs"`
	QuarantinedPath string            `json:"quarantined_path,omitempty"`
	PRErrors        map[string]string `json:"pr_errors,omitempty"`
	Cancellations   []CancelRow       `json:"cancellations,omitempty"`
	Herdr           []SocketRow       `json:"herdr,omitempty"`
}

// CancelRow is one recent subscription cancellation shown by status.
type CancelRow struct {
	PR     string    `json:"pr"`
	PaneID string    `json:"pane_id"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// SocketRow is the delivery state of one herdr socket shown by status.
type SocketRow struct {
	Socket  string    `json:"socket"`
	Up      bool      `json:"up"`
	Since   time.Time `json:"since"`
	Pending int       `json:"pending"`
}

// ResetResult reports how many subscriptions reset removed.
type ResetResult struct {
	Removed int `json:"removed"`
}

// HelloResult is the (frozen, empty) hello reply; the daemon identity is in
// Response.Daemon.
type HelloResult struct{}

// ShutdownResult is the (frozen, empty) shutdown reply.
type ShutdownResult struct{}
