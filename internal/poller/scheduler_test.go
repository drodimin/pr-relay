package poller

import (
	"context"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/drodimin/pr-relay/internal/config"
	"github.com/drodimin/pr-relay/internal/ghclient"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/store"
)

// sched runs an Engine's scheduler against the in-memory fake GitHub inside a
// synctest bubble. Every helper must be called from inside the bubble.
type sched struct {
	t      *testing.T
	f      *fakeGH
	e      *Engine
	cancel context.CancelFunc
	done   chan error
}

type schedOpt struct {
	state  *store.State
	tokens ghclient.TokenProvider
	cfg    func(*config.Config)
}

func newSched(t *testing.T, o schedOpt) *sched {
	t.Helper()
	f := newFakeGH(t)
	st := o.state
	if st == nil {
		st = store.NewState()
	}
	tokens := o.tokens
	if tokens == nil {
		tokens = staticTokens{}
	}
	cfg := config.Defaults()
	if o.cfg != nil {
		o.cfg(&cfg)
	}
	e, err := New(Options{
		Saver:   &memSaver{},
		State:   st,
		Fetcher: newClientWith(t, f, tokens),
		Config:  cfg,
		Logger:  slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &sched{t: t, f: f, e: e}
}

func (s *sched) start() {
	s.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan error, 1)
	go func() { s.done <- s.e.Run(ctx) }()
	synctest.Wait()
}

func (s *sched) stop() {
	s.t.Helper()
	s.cancel()
	if err := <-s.done; err != nil {
		s.t.Errorf("Run returned %v", err)
	}
}

// advance moves fake time forward by d and lets every goroutine settle.
func (s *sched) advance(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

func (s *sched) sub(ref model.PRRef, pane string) SubInfo {
	s.t.Helper()
	info, err := s.e.Subscribe(SubRequest{PR: ref, HerdrSocket: sock, PaneID: pane})
	if err != nil {
		s.t.Fatalf("Subscribe: %v", err)
	}
	return info
}

func prN(n int) model.PRRef { return model.PRRef{Owner: "acme", Repo: "widgets", Number: n} }

func gaps(ts []time.Time) []time.Duration {
	var out []time.Duration
	for i := 1; i < len(ts); i++ {
		out = append(out, ts[i].Sub(ts[i-1]))
	}
	return out
}

func TestRunStopsOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSched(t, schedOpt{})
		s.start()
		s.advance(10 * time.Second)
		s.stop()
	})
}

func TestRunIdleNoRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSched(t, schedOpt{})
		s.start()
		s.advance(10 * time.Minute)
		if n := s.f.requestCount(); n != 0 {
			t.Errorf("requests with no subscriptions = %d, want 0", n)
		}
		s.stop()
	})
}

func TestRunWakesOnSubscribe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSched(t, schedOpt{})
		s.f.addPR(prA)
		s.start()
		s.advance(10 * time.Second)
		subAt := time.Now()
		s.sub(prA, "w1:p1")
		synctest.Wait()
		pt := s.f.pollTimes(store.PRKey(prA))
		if len(pt) != 1 || !pt[0].Equal(subAt) {
			t.Fatalf("poll times %v, want one at %v", pt, subAt)
		}
		s.stop()
	})
}

func TestRunSkipsTerminalAndUnsubscribed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st := store.NewState()
		now := time.Now()
		c := prN(3)
		for i, ref := range []model.PRRef{prA, prB, c} {
			st.PRs[store.PRKey(ref)] = &store.PRRecord{Ref: ref, Gen: uint64(i + 1), CreatedAt: now, RateAnchor: now}
		}
		st.NextGen = 3
		st.PRs[store.PRKey(prB)].Terminal = true
		for _, ref := range []model.PRRef{prA, prB} {
			k := store.PRKey(ref)
			id := store.SubID(sock, "w1:p1", k)
			st.Subs[id] = &store.Subscription{ID: id, PR: k, HerdrSocket: sock, PaneID: "w1:p1", CreatedAt: now, UpdatedAt: now, ClockAt: now}
		}
		s := newSched(t, schedOpt{state: st})
		for _, ref := range []model.PRRef{prA, prB, c} {
			s.f.addPR(ref)
		}
		s.start()
		s.advance(10 * time.Minute)
		if n := s.f.requestsFor(store.PRKey(prA)); n < 5 {
			t.Errorf("PR A polled %d times in 10m, want >= 5", n)
		}
		for _, ref := range []model.PRRef{prB, c} {
			if n := s.f.requestsFor(store.PRKey(ref)); n != 0 {
				t.Errorf("%s polled %d times, want 0", ref, n)
			}
		}
		s.stop()
	})
}
