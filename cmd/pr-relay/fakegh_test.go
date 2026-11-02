package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/model"
)

// fakeGH is a real-time GraphQL server standing in for api.github.com in
// subprocess tests. It answers PRBatch requests (variables o<i>/r<i>/n<i>,
// aliases pr<i>) with PRs built from ghquery's synthetic-basic.json, made
// open and stripped of timeline items, whose issue comments the test grows.
type fakeGH struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	template  map[string]any
	prs       map[string]*fakePR
	remaining int
	requests  int
}

type fakePR struct {
	ref      model.PRRef
	comments []map[string]any
}

func newFakeGH(t *testing.T) *fakeGH {
	t.Helper()
	raw, err := os.ReadFile("../../internal/ghquery/testdata/synthetic-basic.json")
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
		t.Fatalf("parse template: %v", err)
	}
	f := &fakeGH{t: t, template: doc.Data.Repository.PullRequest, prs: map[string]*fakePR{}, remaining: 5000}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGH) url() string { return f.srv.URL + "/graphql" }

func fakeKey(owner, repo string, n int) string {
	return strings.ToLower(fmt.Sprintf("%s/%s#%d", owner, repo, n))
}

// addPR registers a PR and returns its github.com URL.
func (f *fakeGH) addPR(ref model.PRRef) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prs[fakeKey(ref.Owner, ref.Repo, ref.Number)] = &fakePR{ref: ref}
	return ref.URL()
}

// addComment appends an issue comment by another user, created now.
func (f *fakeGH) addComment(ref model.PRRef, id string) {
	f.addCommentAt(ref, id, time.Now())
}

func (f *fakeGH) addCommentAt(ref model.PRRef, id string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pr := f.prs[fakeKey(ref.Owner, ref.Repo, ref.Number)]
	pr.comments = append(pr.comments, map[string]any{
		"id":        id,
		"url":       ref.URL() + "#issuecomment-" + id,
		"createdAt": at.UTC().Format(time.RFC3339),
		"author":    map[string]any{"__typename": "User", "login": "otheruser"},
	})
}

// driveComments adds comments named prefix<i> every tick until total exist,
// independent of any process restarts. It closes done when finished.
func (f *fakeGH) driveComments(ref model.PRRef, prefix string, total int, tick time.Duration, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	tk := time.NewTicker(tick)
	defer tk.Stop()
	for i := 0; i < total; i++ {
		select {
		case <-stop:
			return
		case <-tk.C:
		}
		f.addComment(ref, prefix+strconv.Itoa(i))
	}
}

func (f *fakeGH) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func (f *fakeGH) prJSON(pr *fakePR, commentsLast int) (map[string]any, error) {
	// Deep copy the template through JSON so per-request edits never leak.
	b, err := json.Marshal(f.template)
	if err != nil {
		return nil, err
	}
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, err
	}
	obj["number"] = pr.ref.Number
	obj["url"] = pr.ref.URL()
	obj["state"] = "OPEN"
	obj["closed"] = false
	obj["merged"] = false
	obj["timelineItems"] = map[string]any{"nodes": []any{}}
	visible := pr.comments
	if commentsLast >= 0 && len(visible) > commentsLast {
		visible = visible[len(visible)-commentsLast:]
	}
	nodes := make([]any, len(visible))
	for i, c := range visible {
		nodes[i] = c
	}
	obj["comments"] = map[string]any{"totalCount": len(pr.comments), "nodes": nodes}
	return obj, nil
}

func (f *fakeGH) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(body, &req); err != nil || !strings.Contains(req.Query, "PRBatch") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	commentsLast := 30
	if v, ok := req.Variables["commentsLast"].(float64); ok {
		commentsLast = int(v)
	}

	f.mu.Lock()
	f.requests++
	f.remaining--
	data := map[string]any{
		"rateLimit": map[string]any{
			"cost": 1, "limit": 5000, "remaining": f.remaining, "used": 5000 - f.remaining,
			"nodeCount": 100, "resetAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
		"viewer": map[string]any{"login": "selfuser"},
	}
	for i := 0; ; i++ {
		n := strconv.Itoa(i)
		o, ok1 := req.Variables["o"+n].(string)
		rp, ok2 := req.Variables["r"+n].(string)
		num, ok3 := req.Variables["n"+n].(float64)
		if !ok1 || !ok2 || !ok3 {
			break
		}
		pr := f.prs[fakeKey(o, rp, int(num))]
		if pr == nil {
			data["pr"+n] = map[string]any{"pullRequest": nil}
			continue
		}
		obj, err := f.prJSON(pr, commentsLast)
		if err != nil {
			f.mu.Unlock()
			http.Error(w, "encode", http.StatusInternalServerError)
			return
		}
		data["pr"+n] = map[string]any{"pullRequest": obj}
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}
