package deliver

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drodimin/pr-relay/internal/config"
	"github.com/drodimin/pr-relay/internal/herdr"
	"github.com/drodimin/pr-relay/internal/poller"
)

// defaultResync is how often the manager re-reads the subscription list
// without any other trigger (new subscriptions without pending events, so
// their pane close events are watched).
const defaultResync = 60 * time.Second

// Options configures New. Engine is required. Dial defaults to
// herdr.DialUnix (with its socket ownership guard, T-03-29), Logger to a
// discarding logger, Now to time.Now, QuietPeriod to
// config.DefaultQuietPeriod, RestoreGrace to 10s, MaxBytes to
// compose.DefaultMaxBytes and Resync to 60s.
type Options struct {
	Engine       Engine
	Dial         herdr.DialFunc
	Logger       *slog.Logger
	Now          func() time.Time
	QuietPeriod  time.Duration
	RestoreGrace time.Duration
	MaxBytes     int
	Resync       time.Duration
	// Jitter returns [-1,1] scaling the herdr reconnect backoff; nil means
	// random; tests inject 0 (WR-06).
	Jitter func() float64
}

// Manager delivers pending updates for every subscription. It keeps one
// herdr Endpoint per distinct (cleaned) socket and one worker per pane with
// pending updates, so panes are always addressed as (socket, pane id)
// (DLV-09, D-02). It routes herdr lifecycle events to cancellations and
// worker pokes, and reports panes on an unreachable herdr as unable to
// receive (LIFE-08).
type Manager struct {
	eng    Engine
	dial   herdr.DialFunc
	log    *slog.Logger
	now    func() time.Time
	cfg    workerConfig
	resync time.Duration
	jitter func() float64

	running atomic.Bool

	// Run-goroutine state: only the Run goroutine reads or writes these.
	endpoints map[string]*epState
	workers   map[poller.PaneRef]*workerState
	checks    map[poller.PaneRef]bool
	known     map[poller.PaneRef]bool
	events    chan taggedEvent
	exited    chan *workerState
	checked   chan checkResult
	wg        sync.WaitGroup
}

// epState is one socket's endpoint, its request client and its lifetime.
type epState struct {
	socket string
	ep     *herdr.Endpoint
	cl     *herdr.Client
	ctx    context.Context
	cancel context.CancelFunc
	up     bool
}

// workerState is one running worker. ref carries the cleaned socket.
type workerState struct {
	ref    poller.PaneRef
	w      *worker
	es     *epState
	cancel context.CancelFunc
}

// taggedEvent is a herdr event with the endpoint it came from.
type taggedEvent struct {
	es *epState
	ev herdr.Event
}

// checkResult is the outcome of a tab/workspace-closed pane check.
type checkResult struct {
	es   *epState
	ref  poller.PaneRef
	gone bool
}

// paneAgg collects what sync needs to know about one (socket, pane).
type paneAgg struct {
	raws    []poller.PaneRef
	pending bool
}

var errManagerRunning = errors.New("deliver: manager already running")

// New returns a Manager. Call Run to start it.
func New(o Options) (*Manager, error) {
	if o.Engine == nil {
		return nil, errors.New("deliver: nil engine")
	}
	m := &Manager{
		eng:    o.Engine,
		dial:   o.Dial,
		log:    o.Logger,
		now:    o.Now,
		resync: o.Resync,
		jitter: o.Jitter,
		cfg: workerConfig{
			QuietPeriod:  o.QuietPeriod,
			RestoreGrace: o.RestoreGrace,
			MaxBytes:     o.MaxBytes,
		},
	}
	if m.dial == nil {
		m.dial = herdr.DialUnix
	}
	if m.log == nil {
		m.log = slog.New(slog.DiscardHandler)
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.resync <= 0 {
		m.resync = defaultResync
	}
	if m.cfg.QuietPeriod <= 0 {
		m.cfg.QuietPeriod = config.DefaultQuietPeriod
	}
	m.cfg = m.cfg.withDefaults()
	return m, nil
}

// Run delivers until ctx is done, then stops every endpoint, worker and
// check, waits for them and returns nil. A second concurrent Run returns an
// error.
func (m *Manager) Run(ctx context.Context) error {
	if !m.running.CompareAndSwap(false, true) {
		return errManagerRunning
	}
	defer m.running.Store(false)

	ctx, cancel := context.WithCancel(ctx)
	m.endpoints = map[string]*epState{}
	m.workers = map[poller.PaneRef]*workerState{}
	m.checks = map[poller.PaneRef]bool{}
	m.known = map[poller.PaneRef]bool{}
	m.events = make(chan taggedEvent)
	m.exited = make(chan *workerState)
	m.checked = make(chan checkResult)
	defer func() {
		cancel()
		m.wg.Wait()
		m.endpoints, m.workers, m.checks, m.known = nil, nil, nil, nil
	}()

	m.sync(ctx)
	tick := time.NewTicker(m.resync)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-m.eng.PendingWake():
			m.sync(ctx)
		case <-tick.C:
			m.sync(ctx)
		case te := <-m.events:
			m.handle(ctx, te)
		case ws := <-m.exited:
			if m.workers[ws.ref] == ws {
				delete(m.workers, ws.ref)
			}
			ws.cancel()
			m.sync(ctx)
		case r := <-m.checked:
			delete(m.checks, r.ref)
			if r.gone && m.endpoints[r.ref.HerdrSocket] == r.es {
				m.cancelPane(r.ref, poller.ReasonPaneGone)
				m.sync(ctx)
			}
		}
	}
}

// sync reconciles endpoints, workers and reachability with the current
// subscription list.
func (m *Manager) sync(ctx context.Context) {
	bySock := map[string]map[string]*paneAgg{}
	for _, s := range m.eng.List() {
		if s.HerdrSocket == "" || s.PaneID == "" {
			continue
		}
		sock := filepath.Clean(s.HerdrSocket) // Pitfall 10: one key per path
		panes := bySock[sock]
		if panes == nil {
			panes = map[string]*paneAgg{}
			bySock[sock] = panes
		}
		agg := panes[s.PaneID]
		if agg == nil {
			agg = &paneAgg{}
			panes[s.PaneID] = agg
		}
		raw := poller.PaneRef{HerdrSocket: s.HerdrSocket, PaneID: s.PaneID}
		if !slices.Contains(agg.raws, raw) {
			agg.raws = append(agg.raws, raw)
		}
		if !agg.pending {
			d, err := m.eng.Pending(s.ID)
			agg.pending = err == nil && (len(d.Events) > 0 || d.Dropped > 0)
		}
	}

	// Retire endpoints (and their workers) whose last sub went away.
	for sock, es := range m.endpoints {
		if _, ok := bySock[sock]; !ok {
			m.retire(sock, es)
		}
	}

	known := map[poller.PaneRef]bool{}
	for sock, panes := range bySock {
		ids := make([]string, 0, len(panes))
		for id := range panes {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		es := m.endpoints[sock]
		if es == nil {
			var err error
			if es, err = m.startEndpoint(ctx, sock, ids); err != nil {
				m.log.Error("herdr endpoint not started", "socket", sock, "err", err)
				continue
			}
		} else {
			es.ep.SetPanes(ids)
		}
		for _, id := range ids {
			agg := panes[id]
			for _, r := range agg.raws {
				known[r] = true
			}
			ref := poller.PaneRef{HerdrSocket: sock, PaneID: id}
			if m.workers[ref] != nil {
				continue // the worker reports reachability
			}
			if agg.pending {
				m.startWorker(ctx, es, ref)
				continue
			}
			// LIFE-08: a pane on an unreachable herdr cannot receive
			// from the moment it went down.
			var since time.Time
			if !es.ep.Up() {
				since = es.ep.DownSince()
			}
			for _, r := range agg.raws {
				m.eng.SetPaneReachability(r, since)
			}
		}
	}
	// Panes no longer subscribed: forget their reachability.
	for r := range m.known {
		if !known[r] {
			m.eng.SetPaneReachability(r, time.Time{})
		}
	}
	m.known = known
}

func (m *Manager) startEndpoint(ctx context.Context, sock string, ids []string) (*epState, error) {
	ep, err := herdr.NewEndpoint(herdr.EndpointOptions{Socket: sock, Dial: m.dial, Logger: m.log, Now: m.now,
		Jitter: m.jitter})
	if err != nil {
		return nil, err
	}
	cl, err := herdr.NewClient(herdr.Options{Socket: sock, Dial: m.dial, Logger: m.log})
	if err != nil {
		return nil, err
	}
	ep.SetPanes(ids) // before Run, so the first subscribe already has them
	ectx, cancel := context.WithCancel(ctx)
	es := &epState{socket: sock, ep: ep, cl: cl, ctx: ectx, cancel: cancel}
	m.endpoints[sock] = es
	m.wg.Add(2)
	go func() {
		defer m.wg.Done()
		_ = ep.Run(ectx)
	}()
	go func() {
		defer m.wg.Done()
		for {
			select {
			case <-ectx.Done():
				return
			case ev := <-ep.Events():
				select {
				case m.events <- taggedEvent{es: es, ev: ev}:
				case <-ectx.Done():
					return
				}
			}
		}
	}()
	return es, nil
}

// retire stops a socket's endpoint and its workers.
func (m *Manager) retire(sock string, es *epState) {
	es.cancel()
	delete(m.endpoints, sock)
	for ref, ws := range m.workers {
		if ws.es == es {
			ws.cancel()
			delete(m.workers, ref)
		}
	}
	m.log.Debug("herdr endpoint retired", "socket", sock)
}

func (m *Manager) startWorker(ctx context.Context, es *epState, ref poller.PaneRef) {
	wctx, cancel := context.WithCancel(es.ctx)
	w := newWorker(ref, es.cl, es.ep, m.eng, m.cfg, m.log)
	w.now = m.now
	w.report = m.reportPane
	ws := &workerState{ref: ref, w: w, es: es, cancel: cancel}
	m.workers[ref] = ws
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		w.run(wctx)
		select {
		case m.exited <- ws:
		case <-ctx.Done():
		}
	}()
}

// reportPane fans a worker's reachability out to every stored spelling of
// its (cleaned) socket, because the engine keys reachability by the stored
// PaneRef. It is called from worker goroutines and only uses the Engine.
func (m *Manager) reportPane(ref poller.PaneRef, since time.Time) {
	seen := map[poller.PaneRef]bool{}
	for _, s := range m.eng.List() {
		raw := poller.PaneRef{HerdrSocket: s.HerdrSocket, PaneID: s.PaneID}
		if s.PaneID != ref.PaneID || filepath.Clean(s.HerdrSocket) != ref.HerdrSocket || seen[raw] {
			continue
		}
		seen[raw] = true
		m.eng.SetPaneReachability(raw, since)
	}
}

// handle routes one herdr event from es.
func (m *Manager) handle(ctx context.Context, te taggedEvent) {
	es := te.es
	if m.endpoints[es.socket] != es {
		return // from a retired endpoint
	}
	ev := te.ev
	switch ev.Kind {
	case herdr.KindPaneClosed:
		// Authoritative: cancel at once (Pitfall 6 exception).
		if m.cancelPane(poller.PaneRef{HerdrSocket: es.socket, PaneID: ev.PaneID}, poller.ReasonPaneClosed) {
			m.sync(ctx)
		}
	case herdr.KindPaneMoved:
		// A move that keeps the id (or names no previous id) is not the
		// pane going away: only re-check it (WR-05).
		if ev.PreviousPaneID == "" || ev.PreviousPaneID == ev.PaneID {
			if ws := m.workers[poller.PaneRef{HerdrSocket: es.socket, PaneID: ev.PaneID}]; ws != nil {
				ws.w.poke()
			}
			return
		}
		// A cross-workspace move is the pane being gone (D-15, user
		// decision): no re-keying to the new id.
		if m.cancelPane(poller.PaneRef{HerdrSocket: es.socket, PaneID: ev.PreviousPaneID}, poller.ReasonPaneGone) {
			m.sync(ctx)
		}
	case herdr.KindTabClosed, herdr.KindWorkspaceClosed:
		// These carry no pane ids (A7): re-check every subscribed pane.
		for _, id := range m.subscribedPanes(es.socket) {
			m.startCheck(ctx, es, poller.PaneRef{HerdrSocket: es.socket, PaneID: id})
		}
	case herdr.KindAgentStatusChanged, herdr.KindAgentDetected, herdr.KindPaneExited:
		if ws := m.workers[poller.PaneRef{HerdrSocket: es.socket, PaneID: ev.PaneID}]; ws != nil {
			ws.w.poke()
		}
	case herdr.KindReconnected:
		if !es.up {
			es.up = true
			m.log.Info("herdr reachable", "socket", es.socket)
		}
		// Lifecycle events are not replayed: every worker re-checks its
		// pane (agent.get, and pane.get for a closed pane, LIFE-04).
		for _, ws := range m.workers {
			if ws.es == es {
				ws.w.poke()
			}
		}
		m.sync(ctx)
		// Panes without a worker (nothing pending) get one pane.get check
		// each, so a pane closed during the outage is cancelled (WR-04,
		// LIFE-04). startCheck dedups and paneGone keeps the RestoreGrace
		// guard, so a pane still being restored is never cancelled.
		// KindReconnected also fires on pane-set resubscribes (IN-03,
		// deferred): that costs one extra pane.get per worker-less pane.
		for _, id := range m.subscribedPanes(es.socket) {
			ref := poller.PaneRef{HerdrSocket: es.socket, PaneID: id}
			if m.workers[ref] == nil {
				m.startCheck(ctx, es, ref)
			}
		}
	case herdr.KindDisconnected:
		if es.up {
			es.up = false
			m.log.Info("herdr unreachable", "socket", es.socket)
		}
		m.sync(ctx)
	}
}

// subscribedPanes lists the pane ids subscribed on a cleaned socket.
func (m *Manager) subscribedPanes(sock string) []string {
	var ids []string
	for _, s := range m.eng.List() {
		if filepath.Clean(s.HerdrSocket) == sock && !slices.Contains(ids, s.PaneID) {
			ids = append(ids, s.PaneID)
		}
	}
	slices.Sort(ids)
	return ids
}

// cancelPane cancels every sub of ref (cleaned socket) with reason and
// stops its worker. It reports whether any sub was cancelled. The engine
// logs each cancellation (D-17).
func (m *Manager) cancelPane(ref poller.PaneRef, reason string) bool {
	if ref.PaneID == "" {
		return false
	}
	found := false
	for _, s := range m.eng.List() {
		if s.PaneID != ref.PaneID || filepath.Clean(s.HerdrSocket) != ref.HerdrSocket {
			continue
		}
		found = true
		if err := m.eng.Cancel(s.ID, reason); err != nil && !errors.Is(err, poller.ErrUnknownSub) {
			m.log.Error("cancel failed", "sub", s.ID, "reason", reason, "err", err)
		}
	}
	if ws := m.workers[ref]; ws != nil {
		ws.cancel()
		delete(m.workers, ref)
	}
	return found
}

// startCheck runs one bounded pane.get check for ref, at most one per pane
// at a time. A pane counts as gone only on pane_not_found once the
// endpoint has been up for RestoreGrace (T-03-27).
func (m *Manager) startCheck(ctx context.Context, es *epState, ref poller.PaneRef) {
	if m.checks[ref] {
		return
	}
	m.checks[ref] = true
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		gone := m.paneGone(es, ref.PaneID)
		select {
		case m.checked <- checkResult{es: es, ref: ref, gone: gone}:
		case <-ctx.Done():
		}
	}()
}

func (m *Manager) paneGone(es *epState, pane string) bool {
	ctx := es.ctx
	for ctx.Err() == nil {
		if !es.ep.Up() {
			return false // reconnect makes workers re-check
		}
		upFor := m.now().Sub(es.ep.UpSince())
		if upFor < m.cfg.RestoreGrace {
			t := time.NewTimer(m.cfg.RestoreGrace - upFor)
			select {
			case <-ctx.Done():
				t.Stop()
				return false
			case <-t.C:
			}
			continue
		}
		_, err := es.cl.PaneGet(ctx, pane)
		if herdr.Code(err) != herdr.CodePaneNotFound {
			return false
		}
		return es.ep.Up() && m.now().Sub(es.ep.UpSince()) >= m.cfg.RestoreGrace
	}
	return false
}
