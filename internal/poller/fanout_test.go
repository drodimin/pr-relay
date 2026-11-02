package poller

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/ghclient"
	"github.com/drodimin/pr-relay/internal/ghquery"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/store"
)

func user(login string) model.Actor { return model.Actor{Login: login, Type: "User"} }

func TestMaskAllows(t *testing.T) {
	for _, typ := range model.AllEventTypes() {
		if !maskAllows(nil, typ) {
			t.Errorf("nil mask rejects %s", typ)
		}
	}
	mask := []model.EventType{model.EventCIFailed}
	for _, typ := range model.AllEventTypes() {
		if got, want := maskAllows(mask, typ), typ == model.EventCIFailed; got != want {
			t.Errorf("mask [ci.failed] allows %s = %v, want %v", typ, got, want)
		}
	}
}

func TestClassify(t *testing.T) {
	ownComment := model.Event{Type: model.EventComment, Key: "comment:1", Actor: user("me")}
	ownPush := model.Event{Type: model.EventCommits, Key: "commits:1:h", Authors: []model.Actor{user("me")}}
	otherComment := model.Event{Type: model.EventComment, Key: "comment:2", Actor: user("bob")}
	ownReview := model.Event{Type: model.EventReview, Key: "review:1", Actor: user("ME")}

	cases := []struct {
		name      string
		raw       []model.Event
		delivered int
		activity  bool
	}{
		{"own comment", []model.Event{ownComment}, 0, false},
		{"own review", []model.Event{ownReview}, 0, false},
		{"own push", []model.Event{ownPush}, 0, true},
		{"other comment", []model.Event{otherComment}, 1, true},
		{"nothing", nil, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, a := classify(c.raw, "me")
			if len(d) != c.delivered || a != c.activity {
				t.Fatalf("classify = (%d delivered, activity %v), want (%d, %v)", len(d), a, c.delivered, c.activity)
			}
		})
	}
}

func commentEv(id string) model.Event {
	return model.Event{Type: model.EventComment, Key: "comment:" + id, Actor: user("bob")}
}

func TestAppendPending(t *testing.T) {
	sub := &store.Subscription{}
	seq := uint64(10)
	evs := []model.Event{commentEv("1"), commentEv("2"), commentEv("3"), commentEv("4"), commentEv("5")}
	added := appendPending(sub, evs, &seq, 3, t0)
	if added != 5 {
		t.Errorf("added = %d, want 5", added)
	}
	if len(sub.Pending) != 3 {
		t.Fatalf("pending = %d, want 3", len(sub.Pending))
	}
	wantKeys := []string{"comment:3", "comment:4", "comment:5"}
	for i, p := range sub.Pending {
		if p.Event.Key != wantKeys[i] {
			t.Errorf("pending[%d] = %s, want %s", i, p.Event.Key, wantKeys[i])
		}
		if p.Seq <= 10 {
			t.Errorf("pending[%d].Seq = %d, not above starting seq", i, p.Seq)
		}
		if i > 0 && p.Seq <= sub.Pending[i-1].Seq {
			t.Errorf("seq not strictly increasing at %d", i)
		}
		if !p.DetectedAt.Equal(t0) {
			t.Errorf("DetectedAt = %v", p.DetectedAt)
		}
	}
	if sub.Dropped != 2 {
		t.Errorf("Dropped = %d, want 2", sub.Dropped)
	}
	if sub.DroppedSeq <= sub.Pending[2].Seq {
		t.Errorf("DroppedSeq %d not above kept seq %d", sub.DroppedSeq, sub.Pending[2].Seq)
	}
	if seq != sub.DroppedSeq {
		t.Errorf("state seq %d != DroppedSeq %d", seq, sub.DroppedSeq)
	}
}

func TestAppendPendingDedupeAndMask(t *testing.T) {
	sub := &store.Subscription{Events: []model.EventType{model.EventComment}}
	seq := uint64(0)
	appendPending(sub, []model.Event{commentEv("1")}, &seq, 200, t0)
	added := appendPending(sub, []model.Event{
		commentEv("1"),
		commentEv("2"),
		commentEv("2"),
		{Type: model.EventCIFailed, Key: "ci.failed:0:h:x"},
	}, &seq, 200, t0)
	if added != 1 || len(sub.Pending) != 2 {
		t.Fatalf("added %d, pending %d; want 1 and 2", added, len(sub.Pending))
	}
	if sub.Dropped != 0 || sub.DroppedSeq != 0 {
		t.Errorf("unexpected drop marker %d/%d", sub.Dropped, sub.DroppedSeq)
	}
	if seq != 2 {
		t.Errorf("seq = %d, want 2", seq)
	}
}

func TestRefilter(t *testing.T) {
	in := []store.PendingEvent{
		{Seq: 1, Event: commentEv("1")},
		{Seq: 2, Event: model.Event{Type: model.EventCIFailed, Key: "ci.failed:0:h:x"}},
		{Seq: 3, Event: commentEv("3")},
	}
	orig := append([]store.PendingEvent(nil), in...)
	out := refilter(in, []model.EventType{model.EventComment})
	if len(out) != 2 || out[0].Seq != 1 || out[1].Seq != 3 {
		t.Fatalf("refilter = %+v", out)
	}
	if !reflect.DeepEqual(in, orig) {
		t.Fatal("refilter mutated its input")
	}
	if all := refilter(in, nil); len(all) != 3 {
		t.Fatalf("nil mask kept %d, want 3", len(all))
	}
}

func TestFakeGH(t *testing.T) {
	f := newFakeGH(t)
	a := model.PRRef{Owner: "o", Repo: "r", Number: 1}
	missing := model.PRRef{Owner: "o", Repo: "r", Number: 2}
	f.addPR(a)
	for i := 0; i < 35; i++ {
		f.addComment(a, "IC_"+strconv.Itoa(i), "bob", "User", t0.Add(time.Duration(i)*time.Second))
	}
	c := newTestClient(t, f)

	res, err := c.Batch(context.Background(), []model.PRRef{a, missing}, ghquery.NormalWindow)
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if res.Viewer != "me" || res.RateLimit.Cost != 1 || res.RateLimit.Remaining != 4900 {
		t.Errorf("viewer/ratelimit = %q %+v", res.Viewer, res.RateLimit)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items = %d", len(res.Items))
	}
	if err := res.Items[0].Err; err != nil {
		t.Fatalf("item 0: %v", err)
	}
	s := res.Items[0].Snapshot
	if s.State != "OPEN" || s.CommentsTotal != 35 || len(s.Comments) != 30 {
		t.Errorf("snapshot state %s total %d visible %d", s.State, s.CommentsTotal, len(s.Comments))
	}
	if !errors.Is(res.Items[1].Err, ghquery.ErrNotFound) {
		t.Errorf("missing PR err = %v, want ErrNotFound", res.Items[1].Err)
	}
	got := f.aliasesPerRequest()
	want := [][]string{{store.PRKey(a), store.PRKey(missing)}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("aliases = %v, want %v", got, want)
	}
	if f.requestsFor(store.PRKey(a)) != 1 {
		t.Errorf("requestsFor = %d", f.requestsFor(store.PRKey(a)))
	}

	wide, err := c.Batch(context.Background(), []model.PRRef{a}, ghquery.WideWindow)
	if err != nil {
		t.Fatalf("wide Batch: %v", err)
	}
	if n := len(wide.Items[0].Snapshot.Comments); n != 35 {
		t.Errorf("wide window visible = %d, want 35", n)
	}

	f.queue(fakeResp{status: http.StatusTooManyRequests, header: http.Header{"Retry-After": {"30"}}, body: `{}`})
	_, err = c.Batch(context.Background(), []model.PRRef{a}, ghquery.NormalWindow)
	var se *ghclient.SecondaryLimitError
	if !errors.As(err, &se) || se.RetryAfter != 30*time.Second {
		t.Fatalf("scripted 429 err = %v", err)
	}
	if _, err := c.Batch(context.Background(), []model.PRRef{a}, ghquery.NormalWindow); err != nil {
		t.Fatalf("after script: %v", err)
	}
}
