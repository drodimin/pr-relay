package deliver

import (
	"maps"
	"sort"
	"sync"
	"time"

	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/poller"
	"github.com/drodimin/pr-relay/internal/store"
)

// fakeEngine is an in-memory Engine. It uses only a plain mutex and
// channels, so it is safe inside synctest bubbles.
type fakeEngine struct {
	mu       sync.Mutex
	subs     map[string]*store.Subscription
	views    map[string]poller.PRView
	seq      uint64
	acks     []map[string]uint64
	cancels  []fakeCancel
	reach    map[poller.PaneRef]time.Time
	reachLog []fakeReach
	failAck  error
	pending  chan struct{}
	// onPending, when set, runs (without mu) at the start of Pending.
	onPending func(subID string)
}

type fakeCancel struct {
	SubID, Reason string
}

type fakeReach struct {
	Pane  poller.PaneRef
	Since time.Time
}

var _ Engine = (*fakeEngine)(nil)

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		subs:    map[string]*store.Subscription{},
		views:   map[string]poller.PRView{},
		reach:   map[poller.PaneRef]time.Time{},
		pending: make(chan struct{}, 1),
	}
}

func (f *fakeEngine) addSub(s store.Subscription) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := s
	c.Pending = append([]store.PendingEvent(nil), s.Pending...)
	f.subs[s.ID] = &c
}

func (f *fakeEngine) setView(key string, v poller.PRView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.views[key] = v
}

// addPending appends events with increasing Seq and pokes PendingWake.
func (f *fakeEngine) addPending(subID string, evs ...model.Event) {
	f.mu.Lock()
	sub := f.subs[subID]
	if sub != nil {
		for _, e := range evs {
			f.seq++
			sub.Pending = append(sub.Pending, store.PendingEvent{Seq: f.seq, Event: e, DetectedAt: time.Now()})
		}
	}
	f.mu.Unlock()
	select {
	case f.pending <- struct{}{}:
	default:
	}
}

// setDropped sets a drop marker on subID at a fresh Seq.
func (f *fakeEngine) setDropped(subID string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	f.subs[subID].Dropped = n
	f.subs[subID].DroppedSeq = f.seq
}

func (f *fakeEngine) pendingCount(subID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.subs[subID]; s != nil {
		return len(s.Pending)
	}
	return -1
}

func (f *fakeEngine) hasSub(subID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subs[subID] != nil
}

func (f *fakeEngine) ackLog() []map[string]uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]uint64, len(f.acks))
	for i, a := range f.acks {
		out[i] = maps.Clone(a)
	}
	return out
}

func (f *fakeEngine) cancelLog() []fakeCancel {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCancel(nil), f.cancels...)
}

func (f *fakeEngine) reachFor(p poller.PaneRef) (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.reach[p]
	return t, ok
}

func (f *fakeEngine) reachHistory() []fakeReach {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeReach(nil), f.reachLog...)
}

func (f *fakeEngine) setFailAck(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAck = err
}

func (f *fakeEngine) List() []store.Subscription {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Subscription, 0, len(f.subs))
	for _, s := range f.subs {
		c := *s
		c.Pending = append([]store.PendingEvent(nil), s.Pending...)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (f *fakeEngine) Pending(subID string) (poller.Drain, error) {
	f.mu.Lock()
	hook := f.onPending
	f.mu.Unlock()
	if hook != nil {
		hook(subID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.subs[subID]
	if s == nil {
		return poller.Drain{}, poller.ErrUnknownSub
	}
	d := poller.Drain{Events: append([]store.PendingEvent(nil), s.Pending...), Dropped: s.Dropped,
		DroppedSeq: s.DroppedSeq, UpToSeq: s.DroppedSeq}
	for _, p := range s.Pending {
		d.UpToSeq = max(d.UpToSeq, p.Seq)
	}
	return d, nil
}

func (f *fakeEngine) PRView(key string) (poller.PRView, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.views[key]
	return v, ok
}

func (f *fakeEngine) AckMany(acks map[string]uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acks = append(f.acks, maps.Clone(acks))
	if f.failAck != nil {
		return f.failAck
	}
	for id, upTo := range acks {
		s := f.subs[id]
		if s == nil {
			continue
		}
		var kept []store.PendingEvent
		for _, p := range s.Pending {
			if p.Seq > upTo {
				kept = append(kept, p)
			}
		}
		s.Pending = kept
		if s.DroppedSeq != 0 && s.DroppedSeq <= upTo {
			s.Dropped, s.DroppedSeq = 0, 0
		}
	}
	return nil
}

func (f *fakeEngine) Cancel(subID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs[subID] == nil {
		return poller.ErrUnknownSub
	}
	delete(f.subs, subID)
	f.cancels = append(f.cancels, fakeCancel{SubID: subID, Reason: reason})
	return nil
}

func (f *fakeEngine) SetPaneReachability(p poller.PaneRef, cannotSince time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cannotSince.IsZero() {
		delete(f.reach, p)
	} else {
		f.reach[p] = cannotSince
	}
	f.reachLog = append(f.reachLog, fakeReach{Pane: p, Since: cannotSince})
}

func (f *fakeEngine) PendingWake() <-chan struct{} { return f.pending }
