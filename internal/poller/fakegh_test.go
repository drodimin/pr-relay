package poller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/ghclient"
	"github.com/drodimin/pr-relay/internal/ghquery"
	"github.com/drodimin/pr-relay/internal/model"
	"github.com/drodimin/pr-relay/internal/store"
)

// fakeResp is a scripted raw response returned instead of the generated one.
type fakeResp struct {
	status int
	header http.Header
	body   string
}

// fakeGH is an in-memory GitHub GraphQL endpoint implemented as an
// http.RoundTripper. It never opens a socket, so it also works inside a
// testing/synctest bubble (plan 02-10).
type fakeGH struct {
	t *testing.T

	mu         sync.Mutex
	viewer     string
	template   map[string]any
	prs        map[string]map[string]any // PRKey -> pullRequest object (full history)
	requests   [][]string                // PR keys per request, alias order
	vars       []map[string]any          // variables per request
	nextStatus []fakeResp
	// wideStatus scripts responses for wide-window (gap re-fetch) requests
	// only, in order; normal-window requests are unaffected.
	wideStatus []fakeResp
	remaining  int
	cost       int
	resetAt    string
	reqAt      []time.Time // time.Now per request
	reqCost    []int       // cost reported per request (0 for scripted)
	// rejectToken, when set, makes every request bearing it return 401.
	rejectToken string
	// churn, when set, appends a comment by another user to every requested
	// PR before answering, so every poll sees fresh activity.
	churn  bool
	churnN int
	// costFn, when set, gives the cost of a request for n PRs.
	costFn func(n int) int

	// block, when non-nil, makes RoundTrip signal entered and then wait for
	// block to be closed (or the request context to end).
	block   chan struct{}
	entered chan struct{}
}

func newFakeGH(t *testing.T) *fakeGH {
	t.Helper()
	raw, err := os.ReadFile("../ghquery/testdata/synthetic-basic.json")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	var doc struct {
		Data struct {
			Repository struct {
				PullRequest map[string]any `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode template: %v", err)
	}
	return &fakeGH{
		t:         t,
		viewer:    "me",
		template:  doc.Data.Repository.PullRequest,
		prs:       map[string]map[string]any{},
		remaining: 4900,
		cost:      1,
		resetAt:   "2026-11-01T13:00:00Z",
	}
}

func deepCopy(v map[string]any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}

// addPR registers an OPEN, mergeable PR with no reviews, comments, timeline
// items or checks, and the template's three commits.
func (f *fakeGH) addPR(ref model.PRRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr := deepCopy(f.template)
	pr["id"] = "PR_" + strings.ToLower(ref.String())
	pr["number"] = ref.Number
	pr["url"] = ref.URL()
	pr["state"] = "OPEN"
	pr["merged"] = false
	pr["closed"] = false
	pr["mergedBy"] = nil
	pr["mergeable"] = "MERGEABLE"
	pr["statusCheckRollup"] = nil
	pr["reviews"] = map[string]any{"totalCount": 0, "nodes": []any{}}
	pr["comments"] = map[string]any{"totalCount": 0, "nodes": []any{}}
	pr["timelineItems"] = map[string]any{"nodes": []any{}}
	f.prs[store.PRKey(ref)] = pr
}

func (f *fakeGH) mutate(ref model.PRRef, fn func(pr map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr := f.prs[store.PRKey(ref)]
	if pr == nil {
		f.t.Fatalf("fakeGH: no PR %s", ref)
	}
	fn(pr)
}

func actorJSON(login, typename string) map[string]any {
	return map[string]any{"__typename": typename, "login": login}
}

func appendConn(pr map[string]any, field string, node any) {
	c := pr[field].(map[string]any)
	c["nodes"] = append(c["nodes"].([]any), node)
	c["totalCount"] = toInt(c["totalCount"]) + 1
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

// addComment appends an issue comment and bumps comments.totalCount.
func (f *fakeGH) addComment(ref model.PRRef, id, login, typename string, at time.Time) {
	f.mutate(ref, func(pr map[string]any) {
		appendConn(pr, "comments", map[string]any{
			"id":        id,
			"url":       ref.URL() + "#issuecomment-" + id,
			"createdAt": at.UTC().Format(time.RFC3339),
			"author":    actorJSON(login, typename),
		})
	})
}

// addReview appends a submitted review with no body and no inline comments.
func (f *fakeGH) addReview(ref model.PRRef, id, login, state string, at time.Time) {
	f.mutate(ref, func(pr map[string]any) {
		appendConn(pr, "reviews", map[string]any{
			"id":          id,
			"state":       state,
			"url":         ref.URL() + "#pullrequestreview-" + id,
			"submittedAt": at.UTC().Format(time.RFC3339),
			"body":        "",
			"author":      actorJSON(login, "User"),
			"comments":    map[string]any{"totalCount": 0, "nodes": []any{}},
		})
	})
}

// setHead pushes one commit oid authored by login and moves the head to it.
func (f *fakeGH) setHead(ref model.PRRef, oid, login string) {
	f.mutate(ref, func(pr map[string]any) {
		appendConn(pr, "commits", map[string]any{"commit": map[string]any{
			"oid":       oid,
			"author":    map[string]any{"user": map[string]any{"login": login}},
			"committer": map[string]any{"user": map[string]any{"login": login}},
		}})
		pr["headRefOid"] = oid
	})
}

// queue scripts the next raw responses, in order.
func (f *fakeGH) queue(r ...fakeResp) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextStatus = append(f.nextStatus, r...)
}

func (f *fakeGH) requestsFor(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, req := range f.requests {
		for _, k := range req {
			if k == key {
				n++
			}
		}
	}
	return n
}

func (f *fakeGH) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeGH) aliasesPerRequest() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.requests))
	for i, r := range f.requests {
		out[i] = append([]string(nil), r...)
	}
	return out
}

func (f *fakeGH) varsOf(i int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.vars[i]
}

// lastN keeps the newest n nodes of conn.
func lastN(conn map[string]any, n int) {
	nodes, _ := conn["nodes"].([]any)
	if n >= 0 && len(nodes) > n {
		conn["nodes"] = nodes[len(nodes)-n:]
	}
}

func firstN(conn map[string]any, n int) {
	nodes, _ := conn["nodes"].([]any)
	if n >= 0 && len(nodes) > n {
		conn["nodes"] = nodes[:n]
	}
}

// windowed returns a copy of pr cut to the query's activity windows.
func windowed(pr map[string]any, vars map[string]any) map[string]any {
	c := deepCopy(pr)
	reviews := c["reviews"].(map[string]any)
	lastN(reviews, toInt(vars["reviewsLast"]))
	for _, n := range reviews["nodes"].([]any) {
		if rc, ok := n.(map[string]any)["comments"].(map[string]any); ok {
			firstN(rc, toInt(vars["reviewCommentsFirst"]))
		}
	}
	lastN(c["comments"].(map[string]any), toInt(vars["commentsLast"]))
	lastN(c["commits"].(map[string]any), 30)
	return c
}

func (f *fakeGH) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	var in struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("fakeGH: bad request body: %w", err)
	}
	var keys []string
	for i := 0; ; i++ {
		n := strconv.Itoa(i)
		o, ok := in.Variables["o"+n].(string)
		if !ok {
			break
		}
		r, _ := in.Variables["r"+n].(string)
		num := toInt(in.Variables["n"+n])
		keys = append(keys, store.PRKey(model.PRRef{Owner: o, Repo: r, Number: num}))
	}

	f.mu.Lock()
	f.requests = append(f.requests, keys)
	f.vars = append(f.vars, in.Variables)
	f.reqAt = append(f.reqAt, time.Now())
	f.reqCost = append(f.reqCost, 0)
	block, entered := f.block, f.entered
	reject := f.rejectToken != "" && req.Header.Get("Authorization") == "bearer "+f.rejectToken
	f.mu.Unlock()

	if block != nil {
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
		}
		select {
		case <-block:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if reject {
		return f.response(req, http.StatusUnauthorized, nil, `{"message":"Bad credentials"}`), nil
	}
	if len(f.wideStatus) > 0 && toInt(in.Variables["commentsLast"]) == ghquery.WideWindow.CommentsLast {
		r := f.wideStatus[0]
		f.wideStatus = f.wideStatus[1:]
		return f.response(req, r.status, r.header, r.body), nil
	}
	if len(f.nextStatus) > 0 {
		r := f.nextStatus[0]
		f.nextStatus = f.nextStatus[1:]
		return f.response(req, r.status, r.header, r.body), nil
	}

	if f.costFn != nil {
		f.cost = f.costFn(len(keys))
	}
	f.reqCost[len(f.reqCost)-1] = f.cost
	if f.churn {
		for _, k := range keys {
			pr := f.prs[k]
			if pr == nil {
				continue
			}
			f.churnN++
			id := "IC_churn" + strconv.Itoa(f.churnN)
			appendConn(pr, "comments", map[string]any{
				"id":        id,
				"url":       pr["url"].(string) + "#issuecomment-" + id,
				"createdAt": time.Now().UTC().Format(time.RFC3339),
				"author":    actorJSON("alice", "User"),
			})
			// Keep the stored history short; totalCount still grows.
			lastN(pr["comments"].(map[string]any), 3)
		}
	}
	data := map[string]any{
		"rateLimit": map[string]any{
			"cost": f.cost, "limit": 5000, "remaining": f.remaining, "used": 5000 - f.remaining,
			"resetAt": f.resetAt, "nodeCount": 10 * len(keys),
		},
		"viewer": map[string]any{"login": f.viewer},
	}
	var errs []any
	for i, k := range keys {
		alias := "pr" + strconv.Itoa(i)
		pr := f.prs[k]
		if pr == nil {
			data[alias] = map[string]any{"pullRequest": nil}
			errs = append(errs, map[string]any{
				"type": "NOT_FOUND", "path": []any{alias, "pullRequest"},
				"message": "Could not resolve to a PullRequest with the number of " + strconv.Itoa(toInt(in.Variables["n"+strconv.Itoa(i)])) + ".",
			})
			continue
		}
		data[alias] = map[string]any{"pullRequest": windowed(pr, in.Variables)}
	}
	out := map[string]any{"data": data}
	if len(errs) > 0 {
		out["errors"] = errs
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return f.response(req, http.StatusOK, nil, string(b)), nil
}

func (f *fakeGH) response(req *http.Request, status int, h http.Header, body string) *http.Response {
	if h == nil {
		h = http.Header{}
	}
	h.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode:    status,
		Status:        strconv.Itoa(status) + " " + http.StatusText(status),
		Header:        h,
		Body:          io.NopCloser(bytes.NewReader([]byte(body))),
		ContentLength: int64(len(body)),
		Request:       req,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
	}
}

// set runs fn under the fake's lock, for changing scripted fields.
func (f *fakeGH) set(fn func(f *fakeGH)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// pollTimes returns the time of every request that carried key.
func (f *fakeGH) pollTimes(key string) []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []time.Time
	for i, req := range f.requests {
		for _, k := range req {
			if k == key {
				out = append(out, f.reqAt[i])
				break
			}
		}
	}
	return out
}

// requestTimes returns the time of every request.
func (f *fakeGH) requestTimes() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.reqAt...)
}

// requestCosts returns the cost reported for every request (0 for scripted
// responses).
func (f *fakeGH) requestCosts() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.reqCost...)
}

// swapTokens is a TokenProvider whose answer can be changed mid-test. It
// records when each call happened.
type swapTokens struct {
	mu    sync.Mutex
	tok   string
	src   string
	err   error
	calls []time.Time
}

func (s *swapTokens) Token(context.Context) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, time.Now())
	if s.err != nil {
		return "", "", s.err
	}
	return s.tok, s.src, nil
}

func (s *swapTokens) set(tok, src string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tok, s.src, s.err = tok, src, err
}

func (s *swapTokens) callTimes() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.calls...)
}

// newClientWith returns a ghclient.Client over f with the given tokens.
func newClientWith(t *testing.T, f *fakeGH, tokens ghclient.TokenProvider) *ghclient.Client {
	t.Helper()
	c, err := ghclient.New(ghclient.Options{Tokens: tokens, HTTP: &http.Client{Transport: f}})
	if err != nil {
		t.Fatalf("ghclient.New: %v", err)
	}
	return c
}

type staticTokens struct{}

func (staticTokens) Token(context.Context) (string, string, error) {
	return "test-token", "GH_TOKEN", nil
}

// newTestClient returns a ghclient.Client whose transport is f.
func newTestClient(t *testing.T, f *fakeGH) *ghclient.Client {
	t.Helper()
	c, err := ghclient.New(ghclient.Options{
		Tokens: staticTokens{},
		HTTP:   &http.Client{Transport: f},
	})
	if err != nil {
		t.Fatalf("ghclient.New: %v", err)
	}
	return c
}
