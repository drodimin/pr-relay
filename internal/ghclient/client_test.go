package ghclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drodimin/pr-relay/internal/ghquery"
	"github.com/drodimin/pr-relay/internal/model"
)

const testToken = "t1-secret-token"

var batchRefs = []model.PRRef{
	{Owner: "synth-owner", Repo: "synth-repo", Number: 7},
	{Owner: "other-owner", Repo: "other.repo", Number: 42},
}

type staticTokens struct {
	tok   string
	calls atomic.Int32
}

func (s *staticTokens) Token(_ context.Context) (string, string, error) {
	s.calls.Add(1)
	return s.tok, "test", nil
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../ghquery/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// newTestClient starts an httptest server running h and a Client pointed at it.
func newTestClient(t *testing.T, h http.HandlerFunc, tp TokenProvider) (*Client, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	if tp == nil {
		tp = &staticTokens{tok: testToken}
	}
	c, err := New(Options{Endpoint: srv.URL + "/graphql", Tokens: tp, UserAgent: "pr-relay/test"})
	if err != nil {
		t.Fatal(err)
	}
	return c, &n
}

func gqlErrors(msgs ...string) []byte {
	type e struct {
		Type    string `json:"type,omitempty"`
		Message string `json:"message"`
	}
	var es []e
	for _, m := range msgs {
		typ := ""
		if strings.HasPrefix(m, "RATE_LIMITED:") {
			typ = "RATE_LIMITED"
			m = strings.TrimPrefix(m, "RATE_LIMITED:")
		}
		es = append(es, e{Type: typ, Message: m})
	}
	b, _ := json.Marshal(map[string]any{"data": nil, "errors": es})
	return b
}

func TestBatchHappy(t *testing.T) {
	body := fixture(t, "batch-basic.json")
	c, n := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "bearer "+testToken {
			t.Errorf("Authorization header mismatch")
		}
		if got := r.Header.Get("User-Agent"); got != "pr-relay/test" {
			t.Errorf("User-Agent = %q", got)
		}
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if !strings.HasPrefix(req.Query, "query PRBatch") {
			t.Errorf("query starts %q", req.Query[:min(len(req.Query), 20)])
		}
		if req.Variables["o0"] != "synth-owner" || req.Variables["r0"] != "synth-repo" || req.Variables["n0"] != float64(7) {
			t.Errorf("variables = %v", req.Variables)
		}
		w.Header().Set("X-GitHub-Request-Id", "ABCD:1234")
		_, _ = w.Write(body)
	}, nil)

	res, err := c.Batch(t.Context(), batchRefs, ghquery.NormalWindow)
	if err != nil {
		t.Fatal(err)
	}
	if n.Load() != 1 {
		t.Fatalf("requests = %d, want 1", n.Load())
	}
	if res.Viewer != "drodimin" {
		t.Errorf("Viewer = %q", res.Viewer)
	}
	if res.RateLimit.Cost != 2 || res.RateLimit.Remaining != 4987 {
		t.Errorf("RateLimit = %+v", res.RateLimit)
	}
	want := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	if !res.ResetAt.Equal(want) {
		t.Errorf("ResetAt = %v, want %v", res.ResetAt, want)
	}
	if len(res.Items) != 2 || res.Items[0].Err != nil || res.Items[1].Err != nil {
		t.Fatalf("Items = %+v", res.Items)
	}
	if res.RequestID != "ABCD:1234" {
		t.Errorf("RequestID = %q", res.RequestID)
	}
	if res.TokenSource != "test" || c.TokenSource() != "test" {
		t.Errorf("TokenSource = %q / %q", res.TokenSource, c.TokenSource())
	}
}

func TestBatchBuildError(t *testing.T) {
	c, n := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {}, nil)
	if _, err := c.Batch(t.Context(), nil, ghquery.NormalWindow); err == nil {
		t.Fatal("want error for empty batch")
	}
	if n.Load() != 0 {
		t.Fatalf("requests = %d, want 0", n.Load())
	}
}

func TestUnauthorizedRefresh(t *testing.T) {
	dir := isolateAuth(t)
	writeHosts(t, dir, "old")
	body := fixture(t, "batch-basic.json")
	c, n := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "bearer new":
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
		}
	}, &TokenSource{})
	// Prime the cache with the old token, then simulate gh auth login.
	if _, _, err := c.token(t.Context()); err != nil {
		t.Fatal(err)
	}
	writeHosts(t, dir, "new")

	res, err := c.Batch(t.Context(), batchRefs, ghquery.NormalWindow)
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if n.Load() != 2 {
		t.Fatalf("requests = %d, want 2", n.Load())
	}
	if res.TokenSource != "oauth_token" {
		t.Errorf("TokenSource = %q", res.TokenSource)
	}
}

func TestUnauthorizedTwice(t *testing.T) {
	tp := &staticTokens{tok: testToken}
	c, n := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials ` + testToken + `"}`))
	}, tp)
	_, err := c.Batch(t.Context(), batchRefs, ghquery.NormalWindow)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if n.Load() != 2 {
		t.Fatalf("requests = %d, want 2", n.Load())
	}
	if tp.calls.Load() != 2 {
		t.Fatalf("token resolutions = %d, want 2", tp.calls.Load())
	}
	assertNoSecret(t, err, testToken)
}

func TestSecondaryLimit(t *testing.T) {
	cases := []struct {
		name   string
		status int
		header string
		body   []byte
		want   time.Duration
	}{
		{"403 retry-after", 403, "30", []byte(`{"message":"slow down"}`), 30 * time.Second},
		{"429 no header", 429, "", []byte(`{}`), 0},
		{"200 secondary message", 200, "", gqlErrors("You have exceeded a Secondary Rate Limit. Please wait."), 0},
		{"403 secondary message", 403, "", []byte(`{"message":"You have exceeded a secondary rate limit"}`), 0},
		{"200 retry-after", 200, "5", gqlErrors("slow"), 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write(tc.body)
			}, nil)
			_, err := c.Batch(t.Context(), batchRefs, ghquery.NormalWindow)
			var se *SecondaryLimitError
			if !errors.As(err, &se) {
				t.Fatalf("err = %v, want *SecondaryLimitError", err)
			}
			if se.RetryAfter != tc.want || se.Status != tc.status {
				t.Fatalf("got %+v, want RetryAfter %v Status %d", se, tc.want, tc.status)
			}
		})
	}
}

func TestPrimaryExhausted(t *testing.T) {
	reset := time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Ratelimit-Remaining", "0")
		w.Header().Set("X-Ratelimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		_, _ = w.Write(gqlErrors("RATE_LIMITED:API rate limit exceeded"))
	}, nil)
	_, err := c.Batch(t.Context(), batchRefs, ghquery.NormalWindow)
	var pe *PrimaryExhaustedError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *PrimaryExhaustedError", err)
	}
	if !pe.ResetAt.Equal(reset) {
		t.Fatalf("ResetAt = %v, want %v", pe.ResetAt, reset)
	}
}

func TestPrimaryExhaustedFromBody(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fixture(t, "batch-rate-limited.json"))
	}, nil)
	_, err := c.Batch(t.Context(), batchRefs, ghquery.NormalWindow)
	var pe *PrimaryExhaustedError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *PrimaryExhaustedError", err)
	}
}

func TestPrimaryExhausted403Header(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Ratelimit-Remaining", "0")
		w.Header().Set("X-Ratelimit-Reset", "1793545200")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}, nil)
	_, err := c.Batch(t.Context(), batchRefs, ghquery.NormalWindow)
	var pe *PrimaryExhaustedError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *PrimaryExhaustedError", err)
	}
	if pe.ResetAt.Unix() != 1793545200 {
		t.Fatalf("ResetAt = %v", pe.ResetAt)
	}
}

func TestTransient(t *testing.T) {
	t.Run("502", func(t *testing.T) {
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}, nil)
		_, err := c.Batch(t.Context(), batchRefs, ghquery.NormalWindow)
		var te *TransientError
		if !errors.As(err, &te) || te.Status != 502 {
			t.Fatalf("err = %v, want *TransientError status 502", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-release:
			case <-time.After(3 * time.Second):
			}
		}))
		t.Cleanup(srv.Close)
		t.Cleanup(func() { close(release) }) // runs before srv.Close
		c, err := New(Options{
			Endpoint: srv.URL + "/graphql",
			Tokens:   &staticTokens{tok: testToken},
			HTTP:     &http.Client{Timeout: 100 * time.Millisecond},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Batch(t.Context(), batchRefs, ghquery.NormalWindow)
		var te *TransientError
		if !errors.As(err, &te) {
			t.Fatalf("err = %v, want *TransientError", err)
		}
		assertNoSecret(t, err, testToken)
	})
}

func TestForbiddenBatch(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	}, nil)
	_, err := c.Batch(t.Context(), batchRefs, ghquery.NormalWindow)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	var se *SecondaryLimitError
	if errors.As(err, &se) {
		t.Fatal("plain 403 classified as secondary limit")
	}
}

func TestOtherStatus(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}, nil)
	_, err := c.Batch(t.Context(), batchRefs, ghquery.NormalWindow)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want a 404 status error", err)
	}
}

func TestResponseLimit(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.Repeat(" ", 1<<20)
		for range 17 {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
	}, nil)
	_, err := c.Batch(t.Context(), batchRefs, ghquery.NormalWindow)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want size limit error", err)
	}
}

func TestEndpointGuard(t *testing.T) {
	tp := &staticTokens{tok: testToken}
	for _, bad := range []string{
		"http://example.com/graphql",
		"https://api.github.com.evil.example/graphql",
		"ftp://127.0.0.1/graphql",
		"http://127.0.0.1.example.com/graphql",
		"://bad",
	} {
		if _, err := New(Options{Endpoint: bad, Tokens: tp}); err == nil {
			t.Errorf("New(%q) accepted, want error", bad)
		}
	}
	for _, good := range []string{
		"http://127.0.0.1:8080/graphql",
		"http://[::1]:9999/graphql",
		"http://localhost:1234/graphql",
		DefaultEndpoint,
	} {
		if _, err := New(Options{Endpoint: good, Tokens: tp}); err != nil {
			t.Errorf("New(%q): %v", good, err)
		}
	}
	c, err := New(Options{Tokens: tp})
	if err != nil {
		t.Fatal(err)
	}
	if c.endpoint != DefaultEndpoint {
		t.Fatalf("default endpoint = %q", c.endpoint)
	}
}

func TestInvalidate(t *testing.T) {
	tp := &staticTokens{tok: testToken}
	body := fixture(t, "batch-basic.json")
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}, tp)
	for range 2 {
		if _, err := c.Batch(t.Context(), batchRefs, ghquery.NormalWindow); err != nil {
			t.Fatal(err)
		}
	}
	if tp.calls.Load() != 1 {
		t.Fatalf("token resolutions = %d, want 1 (cached)", tp.calls.Load())
	}
	c.Invalidate()
	if _, err := c.Batch(t.Context(), batchRefs, ghquery.NormalWindow); err != nil {
		t.Fatal(err)
	}
	if tp.calls.Load() != 2 {
		t.Fatalf("token resolutions = %d, want 2 after Invalidate", tp.calls.Load())
	}
}

func TestNoTokenPropagates(t *testing.T) {
	isolateAuth(t)
	c, n := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {}, &TokenSource{})
	_, err := c.Batch(t.Context(), batchRefs, ghquery.NormalWindow)
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
	if n.Load() != 0 {
		t.Fatalf("requests = %d, want 0", n.Load())
	}
}
