package poller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/ghclient"
	"github.com/drodimin/pr-relay/internal/ghquery"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/store"
)

// subTFetcher is a scripted Fetcher. fn answers every Batch call; calls
// counts them.
type subTFetcher struct {
	mu    sync.Mutex
	calls int
	fn    func(ctx context.Context, refs []model.PRRef) (ghclient.BatchResult, error)
}

func (f *subTFetcher) Batch(ctx context.Context, refs []model.PRRef, _ ghquery.Window) (ghclient.BatchResult, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.fn(ctx, refs)
}

func (f *subTFetcher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// subTSnapshot answers with one item per ref in state st.
func subTSnapshot(st string) func(context.Context, []model.PRRef) (ghclient.BatchResult, error) {
	return func(_ context.Context, refs []model.PRRef) (ghclient.BatchResult, error) {
		res := ghclient.BatchResult{Viewer: "me", RateLimit: ghquery.RateLimit{Cost: 1, Limit: 5000, Remaining: 4999}}
		for _, r := range refs {
			res.Items = append(res.Items, ghquery.AliasResult{Ref: r, Snapshot: model.Snapshot{Ref: r, State: st, HeadOID: "head1"}})
		}
		return res, nil
	}
}

func subTHarness(t *testing.T, f *subTFetcher) *harness {
	t.Helper()
	return newHarness(t, func(o *Options) { o.Fetcher = f })
}

func subTReq(ref model.PRRef, pane string, events ...model.EventType) SubRequest {
	return SubRequest{PR: ref, HerdrSocket: sock, PaneID: pane, Events: events}
}

func (s *memSaver) subTCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

func TestSubscribeVerifiedOK(t *testing.T) {
	f := &subTFetcher{fn: subTSnapshot("OPEN")}
	h := subTHarness(t, f)
	now := h.clk.Now()
	info, err := h.e.SubscribeVerified(context.Background(), subTReq(prA, "w1:p1"))
	if err != nil {
		t.Fatalf("SubscribeVerified: %v", err)
	}
	if !info.Created {
		t.Errorf("Created = false")
	}
	if f.count() != 1 {
		t.Errorf("Batch calls = %d, want 1", f.count())
	}
	rec := h.record(prA)
	if !rec.Memory.Baselined || rec.Memory.State != "OPEN" {
		t.Errorf("Memory = %+v", rec.Memory)
	}
	if !rec.LastPollAt.Equal(now) || !rec.RateAnchor.Equal(now) {
		t.Errorf("LastPollAt %v RateAnchor %v, want %v", rec.LastPollAt, rec.RateAnchor, now)
	}
	if want := now.Add(h.e.cfg.Poll.MinInterval.Duration); !info.NextPollAt.Equal(want) {
		t.Errorf("NextPollAt = %v, want %v", info.NextPollAt, want)
	}
	if got := h.e.state.Viewer; got != "me" {
		t.Errorf("Viewer = %q", got)
	}
	if d := h.pending(info.SubID); len(d.Events) != 0 {
		t.Errorf("pending = %+v", d.Events)
	}
}

// TestSubscribeVerifiedFakeGH runs the probe through the real ghclient over
// the fake GitHub, then checks the first scheduled poll is silent.
func TestSubscribeVerifiedFakeGH(t *testing.T) {
	h := newHarness(t)
	info, err := h.e.SubscribeVerified(context.Background(), subTReq(prA, "w1:p1"))
	if err != nil {
		t.Fatalf("SubscribeVerified: %v", err)
	}
	if h.f.requestCount() != 1 {
		t.Errorf("requests = %d", h.f.requestCount())
	}
	h.clk.Advance(time.Minute)
	if r := h.pollNow(prA); r.NewEvents != 0 || r.Err != nil {
		t.Errorf("first poll = %+v", r)
	}
	if d := h.pending(info.SubID); len(d.Events) != 0 {
		t.Errorf("pending = %+v", d.Events)
	}
}

func TestSubscribeVerifiedErrors(t *testing.T) {
	item := func(err error) func(context.Context, []model.PRRef) (ghclient.BatchResult, error) {
		return func(_ context.Context, refs []model.PRRef) (ghclient.BatchResult, error) {
			return ghclient.BatchResult{Items: []ghquery.AliasResult{{Ref: refs[0], Err: err}}}, nil
		}
	}
	batch := func(err error) func(context.Context, []model.PRRef) (ghclient.BatchResult, error) {
		return func(context.Context, []model.PRRef) (ghclient.BatchResult, error) {
			return ghclient.BatchResult{}, err
		}
	}
	cases := []struct {
		name  string
		fn    func(context.Context, []model.PRRef) (ghclient.BatchResult, error)
		class string
	}{
		{"not_found", item(fmt.Errorf("%w: acme/widgets#1", ghquery.ErrNotFound)), "not_found"},
		{"forbidden", item(fmt.Errorf("%w: acme/widgets#1", ghquery.ErrForbidden)), "forbidden"},
		{"unauthorized", batch(ghclient.ErrUnauthorized), "unauthorized"},
		{"no_token", batch(ghclient.ErrNoToken), "no_token"},
		{"rate_limited", batch(&ghclient.PrimaryExhaustedError{ResetAt: t0.Add(time.Hour)}), "rate_limited"},
		{"secondary_limit", batch(&ghclient.SecondaryLimitError{RetryAfter: time.Minute, Status: 403}), "secondary_limit"},
		{"transient", batch(&ghclient.TransientError{Status: 502}), "transient"},
		{"timeout", func(ctx context.Context, _ []model.PRRef) (ghclient.BatchResult, error) {
			<-ctx.Done()
			return ghclient.BatchResult{}, fmt.Errorf("ghclient: request: %w", ctx.Err())
		}, "timeout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &subTFetcher{fn: c.fn}
			h := subTHarness(t, f)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			_, err := h.e.SubscribeVerified(ctx, subTReq(prA, "w1:p1"))
			var ve *VerifyError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want *VerifyError", err)
			}
			if ve.Class != c.class {
				t.Errorf("Class = %q, want %q (err %v)", ve.Class, c.class, err)
			}
			if h.saver.subTCount() != 0 {
				t.Errorf("saves = %d, want 0", h.saver.subTCount())
			}
			if len(h.e.List()) != 0 || len(h.e.state.PRs) != 0 {
				t.Errorf("state changed: subs %d prs %d", len(h.e.List()), len(h.e.state.PRs))
			}
		})
	}
}

// TestSubscribeVerifiedGateRefusal: a budget in secondary backoff refuses the
// probe without calling GitHub.
func TestSubscribeVerifiedGateRefusal(t *testing.T) {
	f := &subTFetcher{fn: subTSnapshot("OPEN")}
	h := subTHarness(t, f)
	h.e.mu.Lock()
	h.e.budget.OnSecondary(h.clk.Now(), time.Minute)
	h.e.mu.Unlock()
	_, err := h.e.SubscribeVerified(context.Background(), subTReq(prA, "w1:p1"))
	var ve *VerifyError
	if !errors.As(err, &ve) || ve.Class != "secondary_limit" {
		t.Fatalf("err = %v, want secondary_limit", err)
	}
	if f.count() != 0 {
		t.Errorf("Batch calls = %d, want 0", f.count())
	}

	h2 := subTHarness(t, f)
	h2.e.mu.Lock()
	h2.e.budget.OnPrimaryExhausted(h2.clk.Now().Add(time.Hour))
	h2.e.mu.Unlock()
	_, err = h2.e.SubscribeVerified(context.Background(), subTReq(prA, "w1:p1"))
	if !errors.As(err, &ve) || ve.Class != "rate_limited" {
		t.Fatalf("err = %v, want rate_limited", err)
	}
	if f.count() != 0 {
		t.Errorf("Batch calls = %d, want 0", f.count())
	}
}

func TestSubscribeVerifiedClosed(t *testing.T) {
	for _, st := range []string{"MERGED", "CLOSED"} {
		t.Run(st, func(t *testing.T) {
			f := &subTFetcher{fn: subTSnapshot(st)}
			h := subTHarness(t, f)
			_, err := h.e.SubscribeVerified(context.Background(), subTReq(prA, "w1:p1"))
			var ve *VerifyError
			if !errors.As(err, &ve) || ve.Class != "pr_closed" {
				t.Fatalf("err = %v, want pr_closed", err)
			}
			if h.saver.subTCount() != 0 || len(h.e.List()) != 0 || len(h.e.state.PRs) != 0 {
				t.Errorf("state changed")
			}
		})
	}
}

func TestSubscribeVerifiedJoin(t *testing.T) {
	f := &subTFetcher{fn: subTSnapshot("OPEN")}
	h := subTHarness(t, f)
	if _, err := h.e.SubscribeVerified(context.Background(), subTReq(prA, "w1:p1")); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(10 * time.Second)
	info, err := h.e.SubscribeVerified(context.Background(), subTReq(prA, "w1:p2"))
	if err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 {
		t.Errorf("Batch calls = %d, want 1 (join must not fetch)", f.count())
	}
	if !info.Created {
		t.Error("Created = false")
	}
	want := []PaneRef{{HerdrSocket: sock, PaneID: "w1:p1"}}
	if !reflect.DeepEqual(info.CoSubscribers, want) {
		t.Errorf("CoSubscribers = %+v", info.CoSubscribers)
	}

	// A baselined record that is no longer OPEN refuses without a fetch.
	h.e.mu.Lock()
	h.e.state.PRs[store.PRKey(prA)].Memory.State = "MERGED"
	h.e.mu.Unlock()
	_, err = h.e.SubscribeVerified(context.Background(), subTReq(prA, "w1:p3"))
	var ve *VerifyError
	if !errors.As(err, &ve) || ve.Class != "pr_closed" {
		t.Fatalf("err = %v, want pr_closed", err)
	}
	if f.count() != 1 {
		t.Errorf("Batch calls = %d", f.count())
	}
}

// TestSubscribeVerifiedErroredRecordProbes: a watched PR with a LastError is
// probed again rather than joined blindly.
func TestSubscribeVerifiedErroredRecordProbes(t *testing.T) {
	f := &subTFetcher{fn: subTSnapshot("OPEN")}
	h := subTHarness(t, f)
	if _, err := h.e.SubscribeVerified(context.Background(), subTReq(prA, "w1:p1")); err != nil {
		t.Fatal(err)
	}
	h.e.mu.Lock()
	h.e.state.PRs[store.PRKey(prA)].LastError = "transient"
	h.e.mu.Unlock()
	if _, err := h.e.SubscribeVerified(context.Background(), subTReq(prA, "w1:p2")); err != nil {
		t.Fatal(err)
	}
	if f.count() != 2 {
		t.Errorf("Batch calls = %d, want 2", f.count())
	}
}

func TestSubscribeVerifiedConcurrent(t *testing.T) {
	var n int
	var nmu sync.Mutex
	f := &subTFetcher{fn: func(ctx context.Context, refs []model.PRRef) (ghclient.BatchResult, error) {
		nmu.Lock()
		n++
		head := fmt.Sprintf("head%d", n)
		nmu.Unlock()
		res, _ := subTSnapshot("OPEN")(ctx, refs)
		res.Items[0].Snapshot.HeadOID = head
		return res, nil
	}}
	h := subTHarness(t, f)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, pane := range []string{"w1:p1", "w1:p2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = h.e.SubscribeVerified(context.Background(), subTReq(prA, pane))
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("SubscribeVerified: %v", err)
		}
	}
	if len(h.e.List()) != 2 {
		t.Fatalf("subs = %d", len(h.e.List()))
	}
	// The record's Memory is the first committed probe's; a second probe,
	// if one ran, did not overwrite it.
	rec := h.record(prA)
	if rec.Memory.HeadOID != "head1" {
		t.Errorf("HeadOID = %q, want head1 (first probe)", rec.Memory.HeadOID)
	}
	// Probes are serialized and re-check the record, so the second caller
	// joins the first one's baseline.
	if f.count() != 1 {
		t.Errorf("Batch calls = %d, want 1", f.count())
	}
}

func TestResubRevivesExpired(t *testing.T) {
	f := &subTFetcher{fn: subTSnapshot("OPEN")}
	h := subTHarness(t, f)
	info, err := h.e.SubscribeVerified(context.Background(), subTReq(prA, "w1:p1"))
	if err != nil {
		t.Fatal(err)
	}
	h.setSub(info.SubID, func(s *store.Subscription) {
		s.Ending, s.EndSeq = ReasonExpired, 9
		s.CheckinAt = h.clk.Now()
		s.Pending = []store.PendingEvent{
			{Seq: 8, Event: model.Event{Type: model.EventComment}},
			{Seq: 9, Event: model.Event{Type: model.EventExpired}},
		}
	})
	h.clk.Advance(time.Hour)
	again, err := h.e.SubscribeVerified(context.Background(), subTReq(prA, "w1:p1", model.EventComment, model.EventReview))
	if err != nil {
		t.Fatalf("resub: %v", err)
	}
	if again.Created || again.SubID != info.SubID {
		t.Errorf("resub = %+v", again)
	}
	s, ok := h.liveSub(info.SubID)
	if !ok {
		t.Fatal("sub gone")
	}
	if s.Ending != "" || s.EndSeq != 0 || !s.CheckinAt.IsZero() {
		t.Errorf("Ending %q EndSeq %d CheckinAt %v", s.Ending, s.EndSeq, s.CheckinAt)
	}
	if len(s.Pending) != 1 || s.Pending[0].Seq != 8 {
		t.Errorf("pending = %+v", s.Pending)
	}
	if !s.ClockAt.Equal(h.clk.Now()) {
		t.Errorf("ClockAt = %v", s.ClockAt)
	}
	if !reflect.DeepEqual(s.Events, []model.EventType{model.EventComment, model.EventReview}) {
		t.Errorf("Events = %v", s.Events)
	}
	if f.count() != 1 {
		t.Errorf("Batch calls = %d", f.count())
	}

	// A closed-ending sub is not revived.
	h.setSub(info.SubID, func(s *store.Subscription) {
		s.Ending, s.EndSeq = ReasonClosed, 10
	})
	_, err = h.e.SubscribeVerified(context.Background(), subTReq(prA, "w1:p1"))
	var ve *VerifyError
	if !errors.As(err, &ve) || ve.Class != "pr_closed" {
		t.Fatalf("err = %v, want pr_closed", err)
	}
	if s, _ := h.liveSub(info.SubID); s.Ending != ReasonClosed {
		t.Errorf("closed sub changed: %+v", s)
	}
}

func TestUnsubscribePane(t *testing.T) {
	h := newHarness(t)
	h.sub(prA, "w1:p1")
	h.sub(prB, "w1:p1")
	h.sub(prA, "w1:p2")

	n, err := h.e.UnsubscribePane(sock, "w1:p1", &prA)
	if err != nil || n != 1 {
		t.Fatalf("UnsubscribePane(PR1) = %d, %v", n, err)
	}
	if got := len(h.e.List()); got != 2 {
		t.Fatalf("subs = %d, want 2", got)
	}

	n, err = h.e.UnsubscribePane(sock+"/./", "w1:p1", nil)
	if err != nil || n != 1 {
		t.Fatalf("UnsubscribePane(all, uncleaned socket) = %d, %v", n, err)
	}
	subs := h.e.List()
	if len(subs) != 1 || subs[0].PaneID != "w1:p2" || subs[0].PR != store.PRKey(prA) {
		t.Fatalf("subs = %+v", subs)
	}
	h.e.mu.Lock()
	_, hasB := h.e.state.PRs[store.PRKey(prB)]
	h.e.mu.Unlock()
	if hasB {
		t.Error("PR2 record kept after its last subscription went")
	}

	saves := h.saver.subTCount()
	n, err = h.e.UnsubscribePane(sock, "w1:p1", nil)
	if err != nil || n != 0 {
		t.Fatalf("repeat = %d, %v", n, err)
	}
	if h.saver.subTCount() != saves {
		t.Error("no-op UnsubscribePane saved")
	}
	if len(h.e.RecentCancellations()) != 0 {
		t.Error("user unsubscribe recorded a cancellation")
	}
}

func TestReset(t *testing.T) {
	h := newHarness(t)
	h.sub(prA, "w1:p1")
	h.sub(prB, "w1:p1")
	h.sub(prA, "w1:p2")
	n, err := h.e.Reset()
	if err != nil || n != 3 {
		t.Fatalf("Reset = %d, %v", n, err)
	}
	if len(h.e.List()) != 0 {
		t.Error("subs left")
	}
	h.e.mu.Lock()
	prs := len(h.e.state.PRs)
	h.e.mu.Unlock()
	if prs != 0 {
		t.Errorf("PR records left: %d", prs)
	}
	saves := h.saver.subTCount()
	n, err = h.e.Reset()
	if err != nil || n != 0 {
		t.Fatalf("second Reset = %d, %v", n, err)
	}
	if h.saver.subTCount() != saves {
		t.Error("no-op Reset saved")
	}
}
