// Package store persists all of pr-relay's durable state in one JSON file
// (D-01): subscriptions, the per-PR diff Memory, every subscription's pending
// events, the global event sequence counter and the cadence anchors.
//
// # Copy-on-write contract
//
// The store is a single-file transactional outbox. Callers never mutate the
// live State in place. They Clone it, mutate the clone, Save the clone, and
// only after Save succeeds swap it in as the live State. In-memory state must
// never be ahead of disk: a new Memory and the events derived from it reach
// the disk in the same rename, so events are created exactly once (EVT-09)
// and survive restarts (LIFE-03).
//
// # File layout
//
// Inside the state directory (mode 0700):
//
//	state.json              the current state (mode 0600)
//	state.lock              flock held for the lifetime of the owning process
//	state.json.tmp-*        in-flight atomic writes; removed at Open
//	state.json.corrupt-*    quarantined unreadable files (D-20)
//
// # Schema version policy
//
// State.Version is SchemaVersion for files written by this binary. Open
// migrates older versions forward through a migration hook, refuses files
// written by a newer binary (they are never quarantined, so a downgrade cannot
// wipe state), and quarantines files with no usable version.
//
// The package holds no business rules. The engine decides what to change.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/drodimin/pr-relay/internal/model"
)

// SchemaVersion is the state file schema version this binary writes.
const SchemaVersion = 1

// State is the whole durable state of the daemon.
type State struct {
	// Version is the schema version (SchemaVersion when written by this binary).
	Version int `json:"version"`
	// Seq is the global monotonic pending-event sequence. Every pending event
	// gets the next value; acks are expressed as "up to Seq".
	Seq uint64 `json:"seq"`
	// NextGen numbers PR record generations. A record that is removed and
	// recreated gets a new Gen, so stale in-flight poll results are dropped.
	NextGen uint64 `json:"next_gen"`
	// Viewer is the last seen viewer.login of the authenticated gh user.
	Viewer string `json:"viewer"`
	// PRs holds one record per watched PR, keyed by PRKey.
	PRs map[string]*PRRecord `json:"prs"`
	// Subs holds every subscription, keyed by SubID.
	Subs map[string]*Subscription `json:"subs"`
}

// PRRecord is the per-PR polling state shared by every subscriber (DMN-02).
type PRRecord struct {
	// Ref is the PR this record watches.
	Ref model.PRRef `json:"ref"`
	// Memory is the diff state carried between polls. Baselined is false
	// until the first successful poll.
	Memory model.Memory `json:"memory"`
	// Gen is the record generation, taken from State.NextGen on creation.
	Gen uint64 `json:"gen"`
	// CreatedAt is when the record was created.
	CreatedAt time.Time `json:"created_at"`
	// RateAnchor anchors the cadence curve. It is set on the first
	// subscription (D-13), on activity (D-12) and by ResetRate/PollNow
	// (D-15). keep (D-14) does not move it.
	RateAnchor time.Time `json:"rate_anchor"`
	// LastActivity is the time of the last D-12 activity only.
	LastActivity time.Time `json:"last_activity"`
	// LastPollAt is the time of the last completed poll.
	LastPollAt time.Time `json:"last_poll_at"`
	// LastError is the last per-PR error class (NOT_FOUND, FORBIDDEN, ...),
	// shown by status.
	LastError string `json:"last_error,omitempty"`
	// Terminal means the PR is MERGED and is no longer polled.
	Terminal bool `json:"terminal,omitempty"`
}

// Subscription is one agent pane's interest in one PR.
type Subscription struct {
	// ID is SubID(HerdrSocket, PaneID, PR).
	ID string `json:"id"`
	// PR is the PRKey of the watched PR.
	PR string `json:"pr"`
	// HerdrSocket is the herdr socket path captured at subscribe time.
	HerdrSocket string `json:"herdr_socket"`
	// PaneID is the herdr pane, e.g. "w1:p3".
	PaneID string `json:"pane_id"`
	// AgentSession identifies the agent session in the pane, if known.
	AgentSession string `json:"agent_session"`
	// TerminalID identifies the terminal in the pane, if known.
	TerminalID string `json:"terminal_id"`
	// Events is the event-type mask.
	Events []model.EventType `json:"events"`
	// CreatedAt is when the subscription was first created.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is when the subscription was last changed.
	UpdatedAt time.Time `json:"updated_at"`
	// ClockAt is the check-in/expiry clock. Re-subscribing (D-18) and keep
	// (D-14) reset it.
	ClockAt time.Time `json:"clock_at"`
	// Pending holds events not yet acknowledged, in Seq order.
	Pending []PendingEvent `json:"pending"`
	// Dropped counts events dropped by the pending cap since the marker was
	// last cleared (D-19 "some updates dropped").
	Dropped int `json:"dropped,omitempty"`
	// DroppedSeq is the Seq at which the drop marker applies. An Ack
	// covering DroppedSeq clears the marker.
	DroppedSeq uint64 `json:"dropped_seq,omitempty"`
}

// PendingEvent is one undelivered event in a subscription's outbox.
type PendingEvent struct {
	Seq        uint64      `json:"seq"`
	Event      model.Event `json:"event"`
	DetectedAt time.Time   `json:"detected_at"`
}

// NewState returns an empty State at SchemaVersion.
func NewState() *State {
	return &State{
		Version: SchemaVersion,
		PRs:     map[string]*PRRecord{},
		Subs:    map[string]*Subscription{},
	}
}

// Clone returns a deep copy of s: mutating the copy never changes s. Nil
// maps become empty maps; nil slices stay nil. Clone of nil is nil.
func (s *State) Clone() *State {
	if s == nil {
		return nil
	}
	c := *s
	c.PRs = make(map[string]*PRRecord, len(s.PRs))
	for k, r := range s.PRs {
		if r == nil {
			c.PRs[k] = nil
			continue
		}
		rc := *r
		rc.Memory = r.Memory.Clone()
		c.PRs[k] = &rc
	}
	c.Subs = make(map[string]*Subscription, len(s.Subs))
	for k, sub := range s.Subs {
		if sub == nil {
			c.Subs[k] = nil
			continue
		}
		sc := *sub
		if sub.Events != nil {
			sc.Events = append([]model.EventType(nil), sub.Events...)
		}
		if sub.Pending != nil {
			sc.Pending = make([]PendingEvent, len(sub.Pending))
			for i, p := range sub.Pending {
				p.Event = cloneEvent(p.Event)
				sc.Pending[i] = p
			}
		}
		c.Subs[k] = &sc
	}
	return &c
}

func cloneEvent(e model.Event) model.Event {
	if e.FailedNames != nil {
		e.FailedNames = append([]string(nil), e.FailedNames...)
	}
	if e.Authors != nil {
		e.Authors = append([]model.Actor(nil), e.Authors...)
	}
	return e
}

// PRKey is the State.PRs key for r: "owner/repo#N", lower-cased so case
// variants of one PR share one record (DMN-02). r must already be Valid.
func PRKey(r model.PRRef) string { return strings.ToLower(r.String()) }

// SubID is the stable subscription ID for one pane's interest in one PR:
// the hex of the first 16 bytes of sha256(herdrSocket \x00 paneID \x00 prKey).
func SubID(herdrSocket, paneID, prKey string) string {
	sum := sha256.Sum256([]byte(herdrSocket + "\x00" + paneID + "\x00" + prKey))
	return hex.EncodeToString(sum[:16])
}
