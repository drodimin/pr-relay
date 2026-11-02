package poller

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/config"
	"github.com/drodimin/pr-relay/internal/ghquery"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/store"
)

var (
	prA = model.PRRef{Owner: "acme", Repo: "widgets", Number: 1}
	prB = model.PRRef{Owner: "acme", Repo: "widgets", Number: 2}
)

const sock = "/tmp/herdr.sock"

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type memSaver struct {
	mu    sync.Mutex
	saves int
	fail  func(*store.State) bool
}

func (s *memSaver) Save(st *store.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil && s.fail(st) {
		return errors.New("save: disk full")
	}
	s.saves++
	return nil
}

func (s *memSaver) setFail(fn func(*store.State) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = fn
}

type logCapture struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (l *logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (l *logCapture) Handle(_ context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recs = append(l.recs, r.Clone())
	return nil
}
func (l *logCapture) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *logCapture) WithGroup(string) slog.Handler      { return l }

func (l *logCapture) named(msg string) []slog.Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []slog.Record
	for _, r := range l.recs {
		if r.Message == msg {
			out = append(out, r)
		}
	}
	return out
}

type harness struct {
	t     *testing.T
	f     *fakeGH
	clk   *fakeClock
	saver *memSaver
	logs  *logCapture
	e     *Engine
}

func newHarness(t *testing.T, mod ...func(*Options)) *harness {
	t.Helper()
	h := &harness{
		t:     t,
		f:     newFakeGH(t),
		clk:   &fakeClock{t: t0},
		saver: &memSaver{},
		logs:  &logCapture{},
	}
	h.f.addPR(prA)
	h.f.addPR(prB)
	o := Options{
		Saver:   h.saver,
		State:   store.NewState(),
		Fetcher: newTestClient(t, h.f),
		Config:  config.Defaults(),
		Logger:  slog.New(h.logs),
		Now:     h.clk.Now,
	}
	for _, m := range mod {
		m(&o)
	}
	e, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.e = e
	return h
}

func (h *harness) sub(ref model.PRRef, pane string, events ...model.EventType) SubInfo {
	h.t.Helper()
	info, err := h.e.Subscribe(SubRequest{PR: ref, HerdrSocket: sock, PaneID: pane, Events: events})
	if err != nil {
		h.t.Fatalf("Subscribe(%s, %s): %v", ref, pane, err)
	}
	return info
}

func (h *harness) pollNow(ref model.PRRef) PollResult {
	h.t.Helper()
	r, err := h.e.PollNow(context.Background(), ref)
	if err != nil {
		h.t.Fatalf("PollNow(%s): %v", ref, err)
	}
	return r
}

func (h *harness) pending(id string) Drain {
	h.t.Helper()
	d, err := h.e.Pending(id)
	if err != nil {
		h.t.Fatalf("Pending: %v", err)
	}
	return d
}

func (h *harness) record(ref model.PRRef) store.PRRecord {
	h.t.Helper()
	h.e.mu.Lock()
	defer h.e.mu.Unlock()
	r := h.e.state.PRs[store.PRKey(ref)]
	if r == nil {
		h.t.Fatalf("no record for %s", ref)
	}
	c := *r
	c.Memory = r.Memory.Clone()
	return c
}

// at returns a GitHub timestamp strictly after everything seen so far.
func (h *harness) at() time.Time {
	h.clk.Advance(2 * time.Second)
	return h.clk.Now()
}

func TestBaselineSilent(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 5; i++ {
		h.f.addComment(prA, "IC_"+strconv.Itoa(i), "bob", "User", h.at())
	}
	h.f.addReview(prA, "PRR_1", "alice", "APPROVED", h.at())

	info := h.sub(prA, "w1:p1")
	if !info.Created || info.SubID == "" {
		t.Fatalf("SubInfo = %+v", info)
	}
	if !info.NextPollAt.Equal(h.clk.Now()) {
		t.Errorf("first NextPollAt = %v, want now %v", info.NextPollAt, h.clk.Now())
	}
	if r := h.record(prA); r.Memory.Baselined || r.Gen == 0 || !r.RateAnchor.Equal(h.clk.Now()) {
		t.Errorf("new record = baselined %v gen %d anchor %v", r.Memory.Baselined, r.Gen, r.RateAnchor)
	}
	if res := h.pollNow(prA); res.NewEvents != 0 {
		t.Errorf("baseline NewEvents = %d", res.NewEvents)
	}
	if d := h.pending(info.SubID); len(d.Events) != 0 {
		t.Fatalf("baseline produced %d pending events", len(d.Events))
	}
	if !h.record(prA).Memory.Baselined {
		t.Fatal("record not baselined after first poll")
	}

	h.f.addComment(prA, "IC_new", "carol", "User", h.at())
	if res := h.pollNow(prA); res.NewEvents != 1 {
		t.Errorf("NewEvents = %d, want 1", res.NewEvents)
	}
	d := h.pending(info.SubID)
	if len(d.Events) != 1 || d.Events[0].Event.Key != "comment:IC_new" {
		t.Fatalf("pending = %+v", d.Events)
	}
}

func TestSecondSubJoins(t *testing.T) {
	h := newHarness(t)
	a := h.sub(prA, "w1:p1")
	h.pollNow(prA)
	anchor := h.record(prA).RateAnchor
	before := h.f.requestCount()

	h.clk.Advance(5 * time.Minute)
	b := h.sub(prA, "w1:p2")
	if !b.Created || b.SubID == a.SubID {
		t.Fatalf("second sub = %+v", b)
	}
	if h.f.requestCount() != before {
		t.Error("Subscribe made a fetch")
	}
	if got := h.record(prA).RateAnchor; !got.Equal(anchor) {
		t.Errorf("RateAnchor moved %v -> %v", anchor, got)
	}
	want := []PaneRef{{HerdrSocket: sock, PaneID: "w1:p1"}}
	if !reflect.DeepEqual(b.CoSubscribers, want) {
		t.Errorf("CoSubscribers = %+v, want %+v", b.CoSubscribers, want)
	}
	if !h.record(prA).Memory.Baselined {
		t.Error("joining sub lost the baseline")
	}
	if b.Interval <= 0 || b.NextPollAt.IsZero() {
		t.Errorf("schedule = %v %v", b.Interval, b.NextPollAt)
	}
	if all := h.e.CoSubscribers(prA); len(all) != 2 {
		t.Errorf("CoSubscribers(prA) = %+v", all)
	}
}

func TestResubscribeUpsert(t *testing.T) {
	h := newHarness(t)
	first := h.sub(prA, "w1:p1")
	h.pollNow(prA)
	h.f.addComment(prA, "IC_1", "bob", "User", h.at())
	h.f.setHead(prA, "head2", "bob")
	h.pollNow(prA)
	d := h.pending(first.SubID)
	if len(d.Events) != 2 {
		t.Fatalf("pending before resub = %+v", d.Events)
	}
	mem := h.record(prA).Memory
	clockBefore := h.e.List()[0].ClockAt

	h.clk.Advance(time.Minute)
	again := h.sub(prA, "w1:p1", model.EventComment)
	if again.Created || again.SubID != first.SubID {
		t.Fatalf("resubscribe = %+v", again)
	}
	subs := h.e.List()
	if len(subs) != 1 {
		t.Fatalf("%d subscriptions, want 1", len(subs))
	}
	s := subs[0]
	if !reflect.DeepEqual(s.Events, []model.EventType{model.EventComment}) {
		t.Errorf("mask = %v", s.Events)
	}
	if len(s.Pending) != 1 || s.Pending[0].Event.Type != model.EventComment {
		t.Errorf("pending after resub = %+v", s.Pending)
	}
	if !s.ClockAt.After(clockBefore) || !s.UpdatedAt.Equal(h.clk.Now()) {
		t.Errorf("ClockAt %v UpdatedAt %v", s.ClockAt, s.UpdatedAt)
	}
	if !reflect.DeepEqual(h.record(prA).Memory, mem) {
		t.Error("resubscribe changed Memory")
	}
}

func TestSubscribeValidation(t *testing.T) {
	h := newHarness(t)
	if _, err := h.e.Subscribe(SubRequest{PR: model.PRRef{Owner: "..", Repo: "r", Number: 1}, PaneID: "p"}); err == nil {
		t.Error("invalid ref accepted")
	}
	if _, err := h.e.Subscribe(SubRequest{PR: prA, PaneID: "p", Events: []model.EventType{"bogus"}}); err == nil {
		t.Error("unknown event type accepted")
	}
	if len(h.e.List()) != 0 {
		t.Error("failed subscribe left a subscription")
	}
}

func TestOneAliasPerPR(t *testing.T) {
	h := newHarness(t)
	h.sub(prA, "w1:p1")
	h.sub(prA, "w1:p2")
	h.sub(prA, "w2:p1")
	h.sub(prB, "w1:p1")
	_, err := h.e.pollRefs(context.Background(), []string{store.PRKey(prA), store.PRKey(prB)}, false)
	if err != nil {
		t.Fatalf("pollRefs: %v", err)
	}
	got := h.f.aliasesPerRequest()
	if len(got) != 1 {
		t.Fatalf("%d requests, want 1", len(got))
	}
	if len(got[0]) != 2 {
		t.Fatalf("aliases = %v, want 2", got[0])
	}
	if h.f.requestsFor(store.PRKey(prA)) != 1 || h.f.requestsFor(store.PRKey(prB)) != 1 {
		t.Errorf("aliases = %v", got[0])
	}
}

func TestCommitFailureDoesNotAdvance(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	h.pollNow(prA)
	h.f.addComment(prA, "IC_1", "bob", "User", h.at())

	snap := func() (model.Memory, uint64, []store.PendingEvent) {
		h.e.mu.Lock()
		defer h.e.mu.Unlock()
		st := h.e.state.Clone()
		return st.PRs[store.PRKey(prA)].Memory, st.Seq, st.Subs[info.SubID].Pending
	}
	mem0, seq0, pend0 := snap()

	h.saver.setFail(func(st *store.State) bool {
		for _, s := range st.Subs {
			if len(s.Pending) > 0 {
				return true
			}
		}
		return false
	})
	if _, err := h.e.PollNow(context.Background(), prA); err == nil {
		t.Fatal("PollNow succeeded although Save failed")
	}
	mem1, seq1, pend1 := snap()
	if !reflect.DeepEqual(mem0, mem1) || seq0 != seq1 || !reflect.DeepEqual(pend0, pend1) {
		t.Fatal("in-memory state advanced past a failed Save")
	}

	h.saver.setFail(nil)
	if res := h.pollNow(prA); res.NewEvents != 1 {
		t.Errorf("retry NewEvents = %d, want 1", res.NewEvents)
	}
	h.pollNow(prA)
	d := h.pending(info.SubID)
	if len(d.Events) != 1 || d.Events[0].Event.Key != "comment:IC_1" {
		t.Fatalf("pending = %+v, want comment:IC_1 exactly once", d.Events)
	}
}

func TestActivityResetsAnchor(t *testing.T) {
	h := newHarness(t)
	h.sub(prA, "w1:p1")
	key := []string{store.PRKey(prA)}
	poll := func() {
		t.Helper()
		if _, err := h.e.pollRefs(context.Background(), key, false); err != nil {
			t.Fatalf("pollRefs: %v", err)
		}
	}
	poll() // baseline
	anchor := h.record(prA).RateAnchor

	h.clk.Advance(3 * time.Minute)
	h.f.addComment(prA, "IC_own", "me", "User", h.at())
	poll()
	if got := h.record(prA).RateAnchor; !got.Equal(anchor) {
		t.Errorf("own comment moved RateAnchor to %v", got)
	}

	h.clk.Advance(3 * time.Minute)
	h.f.setHead(prA, "head-own", "me")
	pushAt := h.clk.Now()
	poll()
	r := h.record(prA)
	if !r.RateAnchor.Equal(pushAt) || !r.LastActivity.Equal(pushAt) {
		t.Errorf("own push: anchor %v activity %v, want %v", r.RateAnchor, r.LastActivity, pushAt)
	}

	h.clk.Advance(3 * time.Minute)
	h.f.addReview(prA, "PRR_x", "alice", "APPROVED", h.at())
	reviewAt := h.clk.Now()
	poll()
	if got := h.record(prA).RateAnchor; !got.Equal(reviewAt) {
		t.Errorf("other's review: anchor %v, want %v", got, reviewAt)
	}
}

func TestKeepAndResetRate(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	h.pollNow(prA)
	rec0 := h.record(prA)
	sub0 := h.e.List()[0]
	saves := h.saver.saves

	h.clk.Advance(time.Minute)
	if err := h.e.Keep(info.SubID); err != nil {
		t.Fatalf("Keep: %v", err)
	}
	sub1 := h.e.List()[0]
	if !sub1.ClockAt.Equal(h.clk.Now()) {
		t.Errorf("ClockAt = %v", sub1.ClockAt)
	}
	sub1.ClockAt = sub0.ClockAt
	if !reflect.DeepEqual(sub0, sub1) {
		t.Errorf("Keep changed more than ClockAt")
	}
	if !reflect.DeepEqual(h.record(prA), rec0) {
		t.Error("Keep changed the PR record")
	}

	h.clk.Advance(time.Minute)
	if err := h.e.ResetRate(prA); err != nil {
		t.Fatalf("ResetRate: %v", err)
	}
	rec1 := h.record(prA)
	if !rec1.RateAnchor.Equal(h.clk.Now()) {
		t.Errorf("RateAnchor = %v", rec1.RateAnchor)
	}
	rec1.RateAnchor = rec0.RateAnchor
	if !reflect.DeepEqual(rec1, rec0) {
		t.Error("ResetRate changed more than RateAnchor")
	}
	if h.saver.saves != saves+2 {
		t.Errorf("saves = %d, want %d", h.saver.saves, saves+2)
	}
	if err := h.e.Keep("nope"); !errors.Is(err, ErrUnknownSub) {
		t.Errorf("Keep(unknown) = %v", err)
	}
	if err := h.e.ResetRate(prB); !errors.Is(err, ErrNotWatched) {
		t.Errorf("ResetRate(unwatched) = %v", err)
	}
}

func TestPollNowUnwatched(t *testing.T) {
	h := newHarness(t)
	if _, err := h.e.PollNow(context.Background(), prB); !errors.Is(err, ErrNotWatched) {
		t.Fatalf("PollNow(unwatched) = %v, want ErrNotWatched", err)
	}
	if h.f.requestCount() != 0 {
		t.Error("unwatched PollNow fetched")
	}
}

func TestPollNowNotFound(t *testing.T) {
	h := newHarness(t)
	gone := model.PRRef{Owner: "acme", Repo: "widgets", Number: 404}
	h.sub(gone, "w1:p1")
	res := h.pollNow(gone)
	if !errors.Is(res.Err, ghquery.ErrNotFound) {
		t.Fatalf("PollResult.Err = %v, want ErrNotFound", res.Err)
	}
	r := h.record(gone)
	if r.LastError != "not_found" || r.LastPollAt.IsZero() {
		t.Errorf("record LastError %q LastPollAt %v", r.LastError, r.LastPollAt)
	}
}

func TestGapRefetch(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	h.pollNow(prA)
	for i := 0; i < 40; i++ {
		h.f.addComment(prA, "IC_"+strconv.Itoa(i), "bob", "User", h.at())
	}
	before := h.f.requestCount()
	res := h.pollNow(prA)
	if n := h.f.requestCount() - before; n != 2 {
		t.Fatalf("%d requests, want normal + wide", n)
	}
	if v := toInt(h.f.varsOf(before)["commentsLast"]); v != ghquery.NormalWindow.CommentsLast {
		t.Errorf("first request commentsLast = %d", v)
	}
	if v := toInt(h.f.varsOf(before + 1)["commentsLast"]); v != 100 {
		t.Errorf("re-fetch commentsLast = %d, want 100", v)
	}
	d := h.pending(info.SubID)
	if len(d.Events) != 40 || res.NewEvents != 40 {
		t.Fatalf("pending %d, NewEvents %d, want 40", len(d.Events), res.NewEvents)
	}
	if d.Dropped != 0 {
		t.Errorf("Dropped = %d after a resolved gap", d.Dropped)
	}
}

func TestPersistentGapMarksDropped(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	h.pollNow(prA)
	for i := 0; i < 110; i++ {
		h.f.addComment(prA, "IC_"+strconv.Itoa(i), "bob", "User", h.at())
	}
	h.pollNow(prA)
	d := h.pending(info.SubID)
	if len(d.Events) != 100 || d.Dropped == 0 {
		t.Fatalf("pending %d dropped %d, want 100 and a drop marker", len(d.Events), d.Dropped)
	}
}

func TestStaleGenDropped(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	h.pollNow(prA)
	h.f.addComment(prA, "IC_late", "bob", "User", h.at())

	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	h.f.mu.Lock()
	h.f.block, h.f.entered = block, entered
	h.f.mu.Unlock()

	type out struct {
		res []PollResult
		err error
	}
	done := make(chan out, 1)
	go func() {
		r, err := h.e.pollRefs(context.Background(), []string{store.PRKey(prA)}, false)
		done <- out{r, err}
	}()
	<-entered
	if err := h.e.Unsubscribe(info.SubID); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	again := h.sub(prA, "w1:p1")
	h.f.mu.Lock()
	h.f.block = nil
	h.f.mu.Unlock()
	close(block)
	o := <-done
	if o.err != nil {
		t.Fatalf("pollRefs: %v", o.err)
	}
	if len(o.res) == 1 && o.res[0].NewEvents != 0 {
		t.Errorf("stale result produced %d events", o.res[0].NewEvents)
	}
	if r := h.record(prA); r.Memory.Baselined {
		t.Error("stale result was applied to the new record")
	}
	if d := h.pending(again.SubID); len(d.Events) != 0 {
		t.Errorf("pending = %+v", d.Events)
	}
}

func TestMergedTerminal(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	h.pollNow(prA)
	h.f.mutate(prA, func(pr map[string]any) {
		pr["state"] = "MERGED"
		pr["merged"] = true
		pr["mergedBy"] = actorJSON("alice", "User")
	})
	h.pollNow(prA)
	d := h.pending(info.SubID)
	if len(d.Events) != 1 || d.Events[0].Event.Type != model.EventClosed || !d.Events[0].Event.Merged {
		t.Fatalf("pending = %+v", d.Events)
	}
	if !h.record(prA).Terminal {
		t.Error("merged PR not Terminal")
	}
}

func TestPendingCapDropped(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Config.Subscriptions.PendingCap = 3 })
	info := h.sub(prA, "w1:p1")
	h.pollNow(prA)
	for i := 0; i < 5; i++ {
		h.f.addComment(prA, "IC_"+strconv.Itoa(i), "bob", "User", h.at())
	}
	h.pollNow(prA)
	d := h.pending(info.SubID)
	if len(d.Events) != 3 || d.Dropped != 2 {
		t.Fatalf("pending %d dropped %d, want 3 and 2", len(d.Events), d.Dropped)
	}
	for _, p := range d.Events {
		if p.Seq > d.UpToSeq {
			t.Errorf("UpToSeq %d below event seq %d", d.UpToSeq, p.Seq)
		}
	}
	if err := h.e.Ack(info.SubID, d.UpToSeq); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	d = h.pending(info.SubID)
	if len(d.Events) != 0 || d.Dropped != 0 {
		t.Fatalf("after Ack pending %d dropped %d", len(d.Events), d.Dropped)
	}
	if err := h.e.Ack("nope", 1); !errors.Is(err, ErrUnknownSub) {
		t.Errorf("Ack(unknown) = %v", err)
	}
}

func TestAckPartial(t *testing.T) {
	h := newHarness(t)
	info := h.sub(prA, "w1:p1")
	h.pollNow(prA)
	h.f.addComment(prA, "IC_1", "bob", "User", h.at())
	h.pollNow(prA)
	h.f.addComment(prA, "IC_2", "bob", "User", h.at())
	h.pollNow(prA)
	d := h.pending(info.SubID)
	if len(d.Events) != 2 {
		t.Fatalf("pending = %d", len(d.Events))
	}
	if err := h.e.Ack(info.SubID, d.Events[0].Seq); err != nil {
		t.Fatal(err)
	}
	d = h.pending(info.SubID)
	if len(d.Events) != 1 || d.Events[0].Event.Key != "comment:IC_2" {
		t.Fatalf("after partial ack = %+v", d.Events)
	}
}

func TestCostLogged(t *testing.T) {
	h := newHarness(t)
	h.sub(prA, "w1:p1")
	h.pollNow(prA)
	h.pollNow(prA)
	recs := h.logs.named("poll")
	if len(recs) != 2 {
		t.Fatalf("%d poll records, want 2", len(recs))
	}
	want := []string{"prs", "cost", "remaining", "limit", "reset_at", "node_count", "stretch", "batch_size", "duration_ms"}
	for _, r := range recs {
		got := map[string]slog.Value{}
		r.Attrs(func(a slog.Attr) bool {
			got[a.Key] = a.Value
			return true
		})
		for _, k := range want {
			if _, ok := got[k]; !ok {
				t.Errorf("poll record missing %q (have %v)", k, got)
			}
		}
		if got["cost"].Int64() != 1 || got["remaining"].Int64() != 4900 || got["limit"].Int64() != 5000 {
			t.Errorf("cost/remaining/limit = %v/%v/%v", got["cost"], got["remaining"], got["limit"])
		}
	}
}

func TestBatchErrorNoSave(t *testing.T) {
	h := newHarness(t)
	h.sub(prA, "w1:p1")
	h.pollNow(prA)
	saves := h.saver.saves
	h.f.queue(fakeResp{status: 502, body: `{}`})
	if _, err := h.e.pollRefs(context.Background(), []string{store.PRKey(prA)}, false); err == nil {
		t.Fatal("pollRefs ignored a 502")
	}
	if h.saver.saves != saves {
		t.Error("batch error triggered a Save")
	}
	if r := h.record(prA); r.LastError == "" {
		t.Error("LastError not set after batch failure")
	}
}

func TestUnsubscribeRemovesRecord(t *testing.T) {
	h := newHarness(t)
	a := h.sub(prA, "w1:p1")
	b := h.sub(prA, "w1:p2")
	if err := h.e.Unsubscribe(a.SubID); err != nil {
		t.Fatal(err)
	}
	h.record(prA)
	if err := h.e.Unsubscribe(b.SubID); err != nil {
		t.Fatal(err)
	}
	h.e.mu.Lock()
	_, ok := h.e.state.PRs[store.PRKey(prA)]
	h.e.mu.Unlock()
	if ok {
		t.Error("record kept after last unsubscribe")
	}
	if err := h.e.Unsubscribe(a.SubID); !errors.Is(err, ErrUnknownSub) {
		t.Errorf("double Unsubscribe = %v", err)
	}
}

func TestEngineRestart(t *testing.T) {
	dir := t.TempDir()
	st, state, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(o *Options) { o.Saver = st; o.State = state })
	info := h.sub(prA, "w1:p1")
	h.sub(prB, "w1:p2", model.EventComment)
	h.pollNow(prA)
	h.f.addComment(prA, "IC_1", "bob", "User", h.at())
	h.pollNow(prA)
	subs := h.e.List()
	memA := h.record(prA).Memory
	if len(h.pending(info.SubID).Events) != 1 {
		t.Fatal("setup: expected one pending event")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, state2, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	e2, err := New(Options{Saver: st2, State: state2, Fetcher: newTestClient(t, h.f), Config: config.Defaults(), Now: h.clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	if got := e2.List(); !reflect.DeepEqual(got, subs) {
		t.Errorf("subscriptions after restart:\n got %+v\nwant %+v", got, subs)
	}
	e2.mu.Lock()
	mem2 := e2.state.PRs[store.PRKey(prA)].Memory
	e2.mu.Unlock()
	if !reflect.DeepEqual(mem2, memA) {
		t.Errorf("Memory after restart differs")
	}
	// The restarted engine continues without re-delivering.
	if r, err := e2.PollNow(context.Background(), prA); err != nil || r.NewEvents != 0 {
		t.Errorf("restart poll = %+v, %v", r, err)
	}
}

func TestPollNowRateLimited(t *testing.T) {
	h := newHarness(t)
	h.sub(prA, "w1:p1")
	h.pollNow(prA)
	h.f.queue(fakeResp{status: 429, header: map[string][]string{"Retry-After": {"60"}}, body: `{}`})
	if _, err := h.e.PollNow(context.Background(), prA); err == nil {
		t.Fatal("429 not reported")
	}
	before := h.f.requestCount()
	_, err := h.e.PollNow(context.Background(), prA)
	var rl *RateLimitedError
	if !errors.As(err, &rl) || !rl.Until.Equal(h.clk.Now().Add(60*time.Second)) {
		t.Fatalf("forced poll during secondary limit = %v", err)
	}
	if h.f.requestCount() != before {
		t.Error("forced poll sent a request during a secondary limit")
	}
	h.clk.Advance(61 * time.Second)
	h.pollNow(prA)
}

func TestSchedule(t *testing.T) {
	h := newHarness(t)
	if d, at := h.e.Schedule(prA); d != 0 || !at.IsZero() {
		t.Errorf("unwatched Schedule = %v %v", d, at)
	}
	h.sub(prA, "w1:p1")
	if d, at := h.e.Schedule(prA); d != time.Minute || !at.Equal(h.clk.Now()) {
		t.Errorf("new PR Schedule = %v %v, want 1m now", d, at)
	}
	h.pollNow(prA)
	polled := h.clk.Now()
	if d, at := h.e.Schedule(prA); d != time.Minute || !at.Equal(polled.Add(time.Minute)) {
		t.Errorf("after poll Schedule = %v %v", d, at)
	}
}
