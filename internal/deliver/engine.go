package deliver

import (
	"time"

	"github.com/drodimin/pr-relay/internal/poller"
	"github.com/drodimin/pr-relay/internal/store"
)

// Engine is the part of *poller.Engine that delivery uses. Every method
// returns copies, so callers never alias engine state.
type Engine interface {
	// List returns every subscription.
	List() []store.Subscription
	// Pending returns a subscription's pending queue (poller.ErrUnknownSub
	// when it no longer exists).
	Pending(subID string) (poller.Drain, error)
	// PRView returns a PR's current polled state by PR key.
	PRView(key string) (poller.PRView, bool)
	// AckMany acks several subscriptions in one save.
	AckMany(acks map[string]uint64) error
	// Cancel removes a subscription and logs why.
	Cancel(subID, reason string) error
	// SetPaneReachability records since when a pane cannot receive (zero:
	// it can).
	SetPaneReachability(p poller.PaneRef, cannotSince time.Time)
	// PendingWake fires when a subscription gets new pending events.
	PendingWake() <-chan struct{}
}

var _ Engine = (*poller.Engine)(nil)
