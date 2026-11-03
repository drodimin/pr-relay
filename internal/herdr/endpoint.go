package herdr

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Endpoint defaults.
const (
	defaultBackoffBase = 250 * time.Millisecond
	defaultBackoffMax  = 30 * time.Second
	defaultStableAfter = 60 * time.Second
	// maxSubscribeRetries bounds immediate re-list + resubscribe after a
	// pane_not_found race (Pitfall 2) before falling back to backoff.
	maxSubscribeRetries = 3
	// jitterFrac scales Jitter() into a +-20% spread of each backoff delay.
	jitterFrac = 0.2
	eventsCap  = 64
)

// globalSubscriptions are subscribed on every connection. They need no
// pane id, so they never fail with pane_not_found.
var globalSubscriptions = []Subscription{
	{Type: SubPaneClosed}, {Type: SubPaneMoved}, {Type: SubPaneAgentDetected},
	{Type: SubPaneExited}, {Type: SubTabClosed}, {Type: SubWorkspaceClosed},
}

// EndpointOptions configures NewEndpoint. Socket is required; Dial defaults
// to DialUnix (with its ownership guard, T-03-19), Logger to a discarding
// logger, Now to time.Now, Jitter to a uniform value in [-1, 1], and the
// durations to 250ms, 30s and 60s.
type EndpointOptions struct {
	Socket string
	Dial   DialFunc
	Logger *slog.Logger
	Now    func() time.Time
	// Jitter returns a value in [-1, 1]; each backoff delay is multiplied
	// by 1 + 0.2*Jitter().
	Jitter func() float64
	// BackoffBase is the first reconnect delay; it doubles up to
	// BackoffMax.
	BackoffBase, BackoffMax time.Duration
	// StableAfter is how long a connection must last for the backoff to
	// reset to BackoffBase when it drops.
	StableAfter time.Duration
}

// Endpoint keeps one supervised, read-only events.subscribe connection to
// one herdr socket (DLV-09: run one Endpoint per socket). It reconnects
// with capped, jittered backoff when herdr goes away (DLV-08) and emits
// KindReconnected after every successful (re)subscribe so consumers can
// re-check their panes. It never writes to an open events connection.
type Endpoint struct {
	client *Client
	log    *slog.Logger
	now    func() time.Time
	jitter func() float64
	base   time.Duration
	max    time.Duration
	stable time.Duration

	running atomic.Bool
	events  chan Event
	resub   chan struct{} // cap 1, coalescing

	mu        sync.Mutex
	panes     []string
	up        bool
	upSince   time.Time
	downSince time.Time
}

// NewEndpoint returns an Endpoint for o.Socket. Call Run to start it.
func NewEndpoint(o EndpointOptions) (*Endpoint, error) {
	c, err := NewClient(Options{Socket: o.Socket, Dial: o.Dial, Logger: o.Logger})
	if err != nil {
		return nil, err
	}
	ep := &Endpoint{
		client: c,
		log:    c.log,
		now:    o.Now,
		jitter: o.Jitter,
		base:   o.BackoffBase,
		max:    o.BackoffMax,
		stable: o.StableAfter,
		events: make(chan Event, eventsCap),
		resub:  make(chan struct{}, 1),
	}
	if ep.now == nil {
		ep.now = time.Now
	}
	if ep.jitter == nil {
		ep.jitter = func() float64 { return rand.Float64()*2 - 1 }
	}
	if ep.base <= 0 {
		ep.base = defaultBackoffBase
	}
	if ep.max <= 0 {
		ep.max = defaultBackoffMax
	}
	if ep.max < ep.base {
		ep.max = ep.base
	}
	if ep.stable <= 0 {
		ep.stable = defaultStableAfter
	}
	ep.downSince = ep.now()
	return ep, nil
}

// Socket returns the cleaned socket path.
func (ep *Endpoint) Socket() string { return ep.client.Socket() }

// Events returns the event channel (capacity 64). Sends block until the
// consumer reads or Run's ctx ends; a consumer that falls far behind makes
// herdr send events_lost, which triggers a resubscribe.
func (ep *Endpoint) Events() <-chan Event { return ep.events }

// SetPanes sets the panes whose agent status changes are watched. A
// changed set closes the current events connection, and Run resubscribes
// at once with the new list; an unchanged set does nothing.
func (ep *Endpoint) SetPanes(panes []string) {
	p := slices.Clone(panes)
	slices.Sort(p)
	p = slices.Compact(p)
	ep.mu.Lock()
	same := slices.Equal(p, ep.panes)
	if !same {
		ep.panes = p
	}
	ep.mu.Unlock()
	if same {
		return
	}
	select {
	case ep.resub <- struct{}{}:
	default:
	}
}

// Up reports whether the events connection is currently subscribed.
func (ep *Endpoint) Up() bool {
	ep.mu.Lock()
	defer ep.mu.Unlock()
	return ep.up
}

// UpSince returns when the endpoint last came up (zero while down).
func (ep *Endpoint) UpSince() time.Time {
	ep.mu.Lock()
	defer ep.mu.Unlock()
	if !ep.up {
		return time.Time{}
	}
	return ep.upSince
}

// DownSince returns when the endpoint went down (zero while up). Before
// the first successful connect it is the construction time.
func (ep *Endpoint) DownSince() time.Time {
	ep.mu.Lock()
	defer ep.mu.Unlock()
	if ep.up {
		return time.Time{}
	}
	return ep.downSince
}

var errEndpointRunning = errors.New("herdr: endpoint already running")

// endReason says why a subscribed stream ended.
type endReason int

const (
	endCtx     endReason = iota // Run's ctx is done
	endResub                    // closed by SetPanes: resubscribe now
	endLost                     // events_lost: resubscribe now
	endDropped                  // herdr went away: back off
)

// Run supervises the events connection until ctx is done, then returns
// nil. A second concurrent Run returns an error.
func (ep *Endpoint) Run(ctx context.Context) error {
	if !ep.running.CompareAndSwap(false, true) {
		return errEndpointRunning
	}
	defer ep.running.Store(false)
	socket := ep.Socket()
	delay := ep.base
	attempt := 0
	for ctx.Err() == nil {
		// Every connect snapshots the current pane set, so a poke queued
		// before this point is already satisfied.
		select {
		case <-ep.resub:
		default:
		}
		attempt++
		stream, err := ep.connect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			ep.log.Warn("herdr events connect failed", "socket", socket, "attempt", attempt, "err", err)
			ep.markDown(ctx)
			if !ep.sleep(ctx, ep.jittered(delay)) {
				return nil
			}
			delay = min(delay*2, ep.max)
			continue
		}
		ep.log.Info("herdr events connected", "socket", socket, "attempt", attempt)
		attempt = 0
		connectedAt := ep.now()
		ep.markUp(connectedAt)
		if !ep.emit(ctx, Event{Kind: KindReconnected, At: connectedAt}) {
			_ = stream.Close()
			return nil
		}
		reason, rerr := ep.read(ctx, stream)
		switch reason {
		case endCtx:
			return nil
		case endResub:
			ep.log.Debug("herdr events resubscribing for new pane set", "socket", socket)
			continue
		case endLost:
			ep.log.Info("herdr events lost; resubscribing", "socket", socket)
			continue
		}
		if ep.now().Sub(connectedAt) >= ep.stable {
			delay = ep.base
		}
		ep.log.Warn("herdr events connection dropped", "socket", socket, "err", rerr)
		ep.markDown(ctx)
		if !ep.sleep(ctx, ep.jittered(delay)) {
			return nil
		}
		delay = min(delay*2, ep.max)
	}
	return nil
}

// connect pings, lists panes and subscribes. A pane_not_found race (a
// watched pane vanished between pane.list and subscribe) is retried at
// once with a fresh list, at most maxSubscribeRetries times.
func (ep *Endpoint) connect(ctx context.Context) (*Stream, error) {
	pong, err := ep.client.Ping(ctx)
	if err != nil {
		return nil, err
	}
	if pong.Protocol != Protocol {
		ep.log.Warn("herdr protocol mismatch", "socket", ep.Socket(), "want", Protocol, "got", pong.Protocol)
	}
	for try := 0; ; try++ {
		list, err := ep.client.PaneList(ctx)
		if err != nil {
			return nil, err
		}
		stream, err := ep.client.Subscribe(ctx, ep.subscriptions(list))
		if err == nil {
			return stream, nil
		}
		if Code(err) != CodePaneNotFound || try >= maxSubscribeRetries {
			return nil, err
		}
		ep.log.Info("herdr subscribe raced a closed pane; retrying", "socket", ep.Socket(), "attempt", try+1)
	}
}

// subscriptions builds the globals plus one status subscription for each
// watched pane present in list (Pitfall 2: one missing pane would fail the
// whole subscribe).
func (ep *Endpoint) subscriptions(list []PaneInfo) []Subscription {
	present := make(map[string]bool, len(list))
	for _, p := range list {
		present[p.PaneID] = true
	}
	ep.mu.Lock()
	panes := ep.panes
	ep.mu.Unlock()
	subs := slices.Clone(globalSubscriptions)
	for _, id := range panes {
		if present[id] {
			subs = append(subs, Subscription{Type: SubAgentStatusChanged, PaneID: id})
		}
	}
	return subs
}

// read forwards events until the stream ends. A watcher closes the stream
// when ctx is done or SetPanes pokes; nothing is ever written to it.
func (ep *Endpoint) read(ctx context.Context, stream *Stream) (endReason, error) {
	done := make(chan struct{})
	var resubbed atomic.Bool
	go func() {
		select {
		case <-ctx.Done():
		case <-ep.resub:
			resubbed.Store(true)
		case <-done:
			return
		}
		_ = stream.Close()
	}()
	defer close(done)
	defer stream.Close()
	for {
		ev, err := stream.Next()
		if err != nil {
			switch {
			case ctx.Err() != nil:
				return endCtx, nil
			case resubbed.Load():
				return endResub, nil
			case Code(err) == CodeEventsLost:
				return endLost, err
			case IsUnreachable(err) || Code(err) != "":
				return endDropped, err
			default:
				// A malformed line: framing is intact, so keep reading.
				ep.log.Warn("herdr event ignored", "socket", ep.Socket(), "err", err)
				continue
			}
		}
		if ev.Kind == "" {
			continue
		}
		ev.At = ep.now()
		if !ep.emit(ctx, ev) {
			return endCtx, nil
		}
	}
}

func (ep *Endpoint) emit(ctx context.Context, ev Event) bool {
	select {
	case ep.events <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

func (ep *Endpoint) markUp(at time.Time) {
	ep.mu.Lock()
	ep.up = true
	ep.upSince = at
	ep.mu.Unlock()
}

// markDown records the up-to-down transition and emits KindDisconnected
// once for it; repeated failures while down emit nothing.
func (ep *Endpoint) markDown(ctx context.Context) {
	ep.mu.Lock()
	wasUp := ep.up
	if wasUp {
		ep.up = false
		ep.downSince = ep.now()
	}
	at := ep.downSince
	ep.mu.Unlock()
	if wasUp {
		ep.emit(ctx, Event{Kind: KindDisconnected, At: at})
	}
}

func (ep *Endpoint) jittered(d time.Duration) time.Duration {
	j := min(max(ep.jitter(), -1), 1)
	return time.Duration(float64(d) * (1 + jitterFrac*j))
}

// sleep waits d, returning early (true) on a SetPanes poke; the next
// connect snapshots the new pane set. It returns false when ctx ends.
func (ep *Endpoint) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ep.resub:
		return true
	case <-ctx.Done():
		return false
	}
}
