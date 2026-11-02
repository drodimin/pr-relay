package poller

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
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

func TestRunRejectsSecondRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSched(t, schedOpt{})
		s.start()
		if err := s.e.Run(context.Background()); err == nil {
			t.Error("second concurrent Run returned nil")
		}
		s.stop()
	})
}

func inRange(t *testing.T, what string, d, lo, hi time.Duration) {
	t.Helper()
	if d < lo || d > hi {
		t.Errorf("%s = %v, want in [%v, %v]", what, d, lo, hi)
	}
}

// after returns the times in ts strictly after at.
func after(ts []time.Time, at time.Time) []time.Time {
	var out []time.Time
	for _, x := range ts {
		if x.After(at) {
			out = append(out, x)
		}
	}
	return out
}

func TestOnePollPerPR(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSched(t, schedOpt{})
		s.f.addPR(prA)
		s.f.addPR(prB)
		for i := range 5 {
			s.sub(prA, "w1:p"+strconv.Itoa(i+1))
		}
		s.sub(prB, "w2:p1")
		s.start()
		s.advance(30 * time.Minute)
		s.stop()
		ka, kb := store.PRKey(prA), store.PRKey(prB)
		for i, req := range s.f.aliasesPerRequest() {
			n := 0
			for _, k := range req {
				if k == ka {
					n++
				}
			}
			if n > 1 {
				t.Errorf("request %d carries PR A %d times", i, n)
			}
		}
		na, nb := s.f.requestsFor(ka), s.f.requestsFor(kb)
		if na != nb || na < 15 {
			t.Errorf("PR A polled %d times, PR B %d; want equal and >= 15", na, nb)
		}
	})
}

func TestQuietRamp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSched(t, schedOpt{})
		s.f.addPR(prA)
		start := time.Now()
		s.sub(prA, "w1:p1")
		s.start()
		s.advance(4*time.Hour + 40*time.Minute)
		s.stop()
		pt := s.f.pollTimes(store.PRKey(prA))
		g := gaps(pt)
		if len(g) == 0 {
			t.Fatal("no polls")
		}
		inRange(t, "first gap", g[0], 54*time.Second, time.Minute)
		for i := 1; i < len(g); i++ {
			if g[i] < g[i-1] {
				t.Errorf("gap %d (%v) shorter than gap %d (%v)", i, g[i], i-1, g[i-1])
			}
		}
		n := 0
		for _, x := range pt {
			if x.Before(start.Add(4 * time.Hour)) {
				n++
			}
		}
		if n < 50 || n > 65 {
			t.Errorf("polls in the first 4h = %d, want 50..65", n)
		}
		late := 0
		for i := 1; i < len(pt); i++ {
			if !pt[i-1].Before(start.Add(4 * time.Hour)) {
				late++
				inRange(t, "gap after 4h", pt[i].Sub(pt[i-1]), 14*time.Minute+24*time.Second, 16*time.Minute)
			}
		}
		if late == 0 {
			t.Error("no gap measured after 4h")
		}
	})
}

// quietThen subscribes to PR A, lets it go quiet for 2h (interval about 8m),
// calls fn, runs 25 more minutes and returns the poll times after fn ran and
// the time fn ran.
func quietThen(t *testing.T, fn func(s *sched)) ([]time.Time, time.Time) {
	t.Helper()
	var out []time.Time
	var at time.Time
	synctest.Test(t, func(t *testing.T) {
		s := newSched(t, schedOpt{})
		s.f.addPR(prA)
		s.sub(prA, "w1:p1")
		s.start()
		s.advance(2 * time.Hour)
		at = time.Now()
		fn(s)
		synctest.Wait()
		s.advance(25 * time.Minute)
		s.stop()
		out = after(s.f.pollTimes(store.PRKey(prA)), at)
	})
	return out, at
}

func wantFast(t *testing.T, pt []time.Time) {
	t.Helper()
	g := gaps(pt)
	if len(g) < 2 {
		t.Fatalf("gaps %v, want >= 2", g)
	}
	for i := range 2 {
		inRange(t, "gap "+strconv.Itoa(i), g[i], 54*time.Second, time.Minute)
	}
}

func wantSlow(t *testing.T, pt []time.Time) {
	t.Helper()
	g := gaps(pt)
	if len(g) < 1 {
		t.Fatalf("gaps %v, want >= 1", g)
	}
	inRange(t, "gap", g[0], 7*time.Minute+12*time.Second, 8*time.Minute)
}

func TestActivityReset(t *testing.T) {
	t.Run("other comment", func(t *testing.T) {
		pt, _ := quietThen(t, func(s *sched) {
			s.f.addComment(prA, "IC_alice", "alice", "User", time.Now())
		})
		wantFast(t, pt)
	})
	t.Run("own push", func(t *testing.T) {
		pt, _ := quietThen(t, func(s *sched) { s.f.setHead(prA, "head-own", "me") })
		wantFast(t, pt)
	})
	t.Run("own comment", func(t *testing.T) {
		pt, _ := quietThen(t, func(s *sched) {
			s.f.addComment(prA, "IC_own", "me", "User", time.Now())
		})
		wantSlow(t, pt)
	})
}

func TestSecondSubNoReset(t *testing.T) {
	pt, _ := quietThen(t, func(s *sched) { s.sub(prA, "w9:p9") })
	wantSlow(t, pt)
}

func TestResetRate(t *testing.T) {
	t.Run("reset", func(t *testing.T) {
		pt, at := quietThen(t, func(s *sched) {
			if err := s.e.ResetRate(prA); err != nil {
				t.Errorf("ResetRate: %v", err)
			}
		})
		if len(pt) == 0 {
			t.Fatal("no poll after ResetRate")
		}
		inRange(t, "first poll after reset", pt[0].Sub(at), 0, time.Minute)
		wantFast(t, pt)
	})
	t.Run("keep", func(t *testing.T) {
		pt, _ := quietThen(t, func(s *sched) {
			id := s.e.List()[0].ID
			if err := s.e.Keep(id); err != nil {
				t.Errorf("Keep: %v", err)
			}
		})
		wantSlow(t, pt)
	})
}

func secondaryResp(retryAfter string) fakeResp {
	h := http.Header{}
	if retryAfter != "" {
		h.Set("Retry-After", retryAfter)
	}
	return fakeResp{status: http.StatusForbidden, header: h,
		body: `{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`}
}

func TestSecondaryBackoff(t *testing.T) {
	t.Run("retry-after", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newSched(t, schedOpt{})
			s.f.addPR(prA)
			s.f.queue(secondaryResp("120"))
			s.sub(prA, "w1:p1")
			s.start()
			s.advance(5 * time.Minute)
			if got := s.e.Status().SecondaryUntil; got.IsZero() {
				t.Error("Status.SecondaryUntil not set")
			}
			s.stop()
			rt := s.f.requestTimes()
			if len(rt) < 3 {
				t.Fatalf("requests %v, want >= 3", rt)
			}
			if g := rt[1].Sub(rt[0]); g != 120*time.Second {
				t.Errorf("wait after Retry-After 120 = %v, want 120s", g)
			}
		})
	})
	t.Run("doubling", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newSched(t, schedOpt{})
			s.f.addPR(prA)
			s.f.queue(secondaryResp(""), secondaryResp(""), secondaryResp(""))
			s.sub(prA, "w1:p1")
			s.start()
			s.advance(10 * time.Minute)
			s.stop()
			g := gaps(s.f.requestTimes())
			want := []time.Duration{60 * time.Second, 120 * time.Second, 240 * time.Second}
			if len(g) < len(want) {
				t.Fatalf("gaps %v, want at least %v", g, want)
			}
			for i, w := range want {
				if g[i] != w {
					t.Errorf("wait %d = %v, want %v", i, g[i], w)
				}
			}
		})
	})
	t.Run("batch shrinks", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newSched(t, schedOpt{})
			for i := 1; i <= 15; i++ {
				s.f.addPR(prN(i))
				s.sub(prN(i), "w1:p1")
			}
			s.f.queue(secondaryResp("60"))
			s.start()
			s.advance(2 * time.Minute)
			s.stop()
			reqs := s.f.aliasesPerRequest()
			if len(reqs) < 3 {
				t.Fatalf("requests %d, want >= 3", len(reqs))
			}
			got := []int{len(reqs[0]), len(reqs[1]), len(reqs[2])}
			if !slices.Equal(got, []int{15, 10, 5}) {
				t.Errorf("aliases per request = %v, want [15 10 5]", got)
			}
		})
	})
}

func TestBudgetCapStretchScheduler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Nobody drains the queues here; a small cap keeps state clones cheap.
		s := newSched(t, schedOpt{cfg: func(c *config.Config) { c.Subscriptions.PendingCap = 3 }})
		// 0.3 points per PR: 60 PRs at 60s project 1080/h, about 2x the
		// 500-point cap.
		s.f.set(func(f *fakeGH) {
			f.churn = true
			f.costFn = func(n int) int { return (3*n + 9) / 10 }
		})
		for i := 1; i <= 60; i++ {
			s.f.addPR(prN(i))
			s.sub(prN(i), "w1:p1")
		}
		s.start()
		s.advance(2 * time.Hour)
		st := s.e.Status()
		s.stop()
		if st.Stretch <= 1.5 {
			t.Errorf("stretch = %v, want > 1.5", st.Stretch)
		}
		rt, rc := s.f.requestTimes(), s.f.requestCosts()
		maxBatch := 0
		for _, c := range rc {
			maxBatch = max(maxBatch, c)
		}
		for i := range rt {
			sum := 0
			for j := 0; j <= i; j++ {
				if rt[i].Sub(rt[j]) < time.Hour {
					sum += rc[j]
				}
			}
			if sum > 500+maxBatch {
				t.Fatalf("spend in the hour up to request %d = %d, want <= %d", i, sum, 500+maxBatch)
			}
		}
		if len(rt) < 100 {
			t.Errorf("only %d requests in 2h", len(rt))
		}
	})
}

func TestBudgetLowNeverStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSched(t, schedOpt{})
		s.f.set(func(f *fakeGH) { f.remaining = 300 })
		s.f.addPR(prA)
		s.sub(prA, "w1:p1")
		s.start()
		s.advance(time.Hour)
		st := s.e.Status()
		s.stop()
		if st.Stretch < 3 {
			t.Errorf("stretch = %v, want >= 3 (1000/300)", st.Stretch)
		}
		g := gaps(s.f.pollTimes(store.PRKey(prA)))
		if len(g) < 3 {
			t.Fatalf("gaps %v over 1h, want >= 3 (polling must continue)", g)
		}
		// Unstretched the first gaps would be about 60s; stretched by
		// 1000/300 they are at least 3m, and never beyond the 60m ceiling.
		inRange(t, "first gap", g[0], 3*time.Minute, 200*time.Second)
		for i, d := range g {
			inRange(t, "gap "+strconv.Itoa(i), d, 3*time.Minute, time.Hour)
		}
	})
}

func TestBudgetPrimaryWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSched(t, schedOpt{})
		s.f.addPR(prA)
		reset := time.Now().Add(10 * time.Minute)
		h := http.Header{}
		h.Set("X-Ratelimit-Remaining", "0")
		h.Set("X-Ratelimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		s.f.queue(fakeResp{status: http.StatusOK, header: h,
			body: `{"data":null,"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`})
		s.sub(prA, "w1:p1")
		s.start()
		s.advance(9 * time.Minute)
		if st := s.e.Status(); st.Remaining != 0 || !st.ResetAt.Equal(reset) {
			t.Errorf("Status remaining %d reset %v, want 0 and %v", st.Remaining, st.ResetAt, reset)
		}
		s.advance(6 * time.Minute)
		s.stop()
		rt := s.f.requestTimes()
		if len(rt) < 3 {
			t.Fatalf("requests %v, want >= 3 (resumed after reset)", rt)
		}
		inRange(t, "wait for reset", rt[1].Sub(reset), 0, 5*time.Second)
		// Quiet for 10m since the anchor, so the next step is 90s.
		inRange(t, "gap after reset", rt[2].Sub(rt[1]), 81*time.Second, 90*time.Second)
	})
}

func TestNoTokenSlowRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tok := &swapTokens{err: ghclient.ErrNoToken}
		s := newSched(t, schedOpt{tokens: tok})
		s.f.addPR(prA)
		s.sub(prA, "w1:p1")
		s.start()
		s.advance(16 * time.Minute)
		st := s.e.Status()
		if st.Auth.State != AuthNoToken {
			t.Errorf("Auth.State = %q, want %q", st.Auth.State, AuthNoToken)
		}
		if st.PRErrors[store.PRKey(prA)] != "no_token" {
			t.Errorf("PRErrors = %v", st.PRErrors)
		}
		calls := tok.callTimes()
		if len(calls) != 4 {
			t.Errorf("token attempts in 16m = %d, want 4", len(calls))
		}
		for i, g := range gaps(calls) {
			if g != authRetry {
				t.Errorf("retry gap %d = %v, want %v", i, g, authRetry)
			}
		}
		if n := s.f.requestCount(); n != 0 {
			t.Errorf("requests without a token = %d", n)
		}

		tok.set("tok", "GH_TOKEN", nil)
		s.advance(5 * time.Minute)
		st = s.e.Status()
		s.stop()
		if st.Auth.State != AuthOK || st.Auth.Source != "GH_TOKEN" {
			t.Errorf("Auth = %+v, want ok from GH_TOKEN", st.Auth)
		}
		if s.f.requestCount() == 0 {
			t.Error("no request after the token appeared")
		}
		if len(st.PRErrors) != 0 {
			t.Errorf("PRErrors after recovery = %v", st.PRErrors)
		}
	})
}

func TestTokenChangeAfter401(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tok := &swapTokens{tok: "old", src: "hosts.yml"}
		s := newSched(t, schedOpt{tokens: tok})
		s.f.set(func(f *fakeGH) { f.rejectToken = "old" })
		s.f.addPR(prA)
		s.sub(prA, "w1:p1")
		s.start()
		s.advance(time.Minute)
		if st := s.e.Status(); st.Auth.State != AuthUnauthorized {
			t.Errorf("Auth.State = %q, want %q", st.Auth.State, AuthUnauthorized)
		}
		before := s.f.requestCount()

		// gh auth login rewrote hosts.yml: the provider now yields "new".
		tok.set("new", "hosts.yml", nil)
		s.advance(5 * time.Minute)
		st := s.e.Status()
		s.stop()
		if st.Auth.State != AuthOK {
			t.Errorf("Auth.State = %q after token change, want ok", st.Auth.State)
		}
		if s.f.requestCount() <= before {
			t.Error("no request after the token change")
		}
		if st.PRErrors[store.PRKey(prA)] != "" {
			t.Errorf("PR error after recovery = %q", st.PRErrors[store.PRKey(prA)])
		}
	})
}

func TestStatusQuarantine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newSched(t, schedOpt{})
		st := s.e.Status()
		if st.Auth.State != AuthUnknown || st.BatchSize != config.DefaultBatchSize || st.Limit != 5000 {
			t.Errorf("initial status %+v", st)
		}
		at := time.Now()
		s.e.SetQuarantine("/state/state.json.corrupt-1", at)
		st = s.e.Status()
		if st.QuarantinedPath != "/state/state.json.corrupt-1" || !st.QuarantinedAt.Equal(at) {
			t.Errorf("quarantine = %q %v", st.QuarantinedPath, st.QuarantinedAt)
		}
	})
}
