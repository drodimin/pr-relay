package ghclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drodimin/pr-relay/internal/ghquery"
	"github.com/drodimin/pr-relay/internal/model"
)

// DefaultEndpoint is the only non-loopback endpoint the token is sent to.
const DefaultEndpoint = "https://api.github.com/graphql"

// maxBody caps a response body (T-02-28). DecodeBatch expects bounded input.
const maxBody = 16 << 20

// drainLimit bounds how much of an unread body is discarded before Close so
// the connection can be reused without reading an unbounded stream.
const drainLimit = 64 << 10

// defaultHTTPTimeout is the whole-request timeout of the default http.Client.
const defaultHTTPTimeout = 30 * time.Second

// TokenProvider yields a token and its source. *TokenSource implements it.
type TokenProvider interface {
	Token(ctx context.Context) (string, string, error)
}

// Options configures New. Zero fields take defaults: DefaultEndpoint, a
// TokenSource for github.com, an http.Client with a 30s timeout, User-Agent
// "pr-relay" and a discarding logger.
type Options struct {
	Endpoint  string
	Tokens    TokenProvider
	HTTP      *http.Client
	UserAgent string
	Logger    *slog.Logger
}

// BatchResult is one decoded PRBatch round trip. ResetAt is RateLimit.ResetAt
// parsed as RFC3339 (zero when absent or malformed).
type BatchResult struct {
	Viewer      string
	RateLimit   ghquery.RateLimit
	ResetAt     time.Time
	Items       []ghquery.AliasResult
	RequestID   string
	Duration    time.Duration
	TokenSource string
}

// Client sends PRBatch queries to GitHub's GraphQL API with the user's gh
// token. It is safe for concurrent use.
type Client struct {
	endpoint string
	tokens   TokenProvider
	http     *http.Client
	ua       string
	log      *slog.Logger

	mu  sync.Mutex
	tok string
	src string
}

// New validates o and returns a Client. A non-default Endpoint must be an
// http(s) URL whose host is 127.0.0.1, ::1 or localhost, so the token can
// never be redirected off the machine (T-02-26).
func New(o Options) (*Client, error) {
	ep := o.Endpoint
	if ep == "" {
		ep = DefaultEndpoint
	}
	if ep != DefaultEndpoint {
		if err := checkLoopback(ep); err != nil {
			return nil, err
		}
	}
	c := &Client{endpoint: ep, tokens: o.Tokens, http: o.HTTP, ua: o.UserAgent, log: o.Logger}
	if c.tokens == nil {
		c.tokens = &TokenSource{}
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if c.ua == "" {
		c.ua = "pr-relay"
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	return c, nil
}

func checkLoopback(ep string) error {
	u, err := url.Parse(ep)
	if err != nil {
		return errors.New("ghclient: invalid endpoint URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("ghclient: endpoint scheme %q not allowed", u.Scheme)
	}
	switch u.Hostname() {
	case "127.0.0.1", "::1", "localhost":
		return nil
	}
	return fmt.Errorf("ghclient: endpoint host %q not allowed (only %s or loopback)", u.Hostname(), DefaultEndpoint)
}

// TokenSource reports where the cached token came from, or "" if none is
// cached. It never returns the token.
func (c *Client) TokenSource() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.src
}

// Invalidate drops the cached token so the next Batch re-resolves it. The
// scheduler calls it before retrying after ErrNoToken.
func (c *Client) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tok, c.src = "", ""
}

// token returns the cached token, resolving it first if needed.
func (c *Client) token(ctx context.Context) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok != "" {
		return c.tok, c.src, nil
	}
	tok, src, err := c.tokens.Token(ctx)
	if err != nil {
		return "", "", err
	}
	if tok == "" {
		return "", "", ErrNoToken
	}
	c.tok, c.src = tok, src
	return tok, src, nil
}

type response struct {
	status    int
	header    http.Header
	body      []byte
	requestID string
}

// Batch fetches refs in one PRBatch query. On a 401 it re-resolves the token
// and retries once. Failures come back as the typed errors in errors.go.
func (c *Client) Batch(ctx context.Context, refs []model.PRRef, w ghquery.Window) (BatchResult, error) {
	query, vars, err := ghquery.BuildBatch(refs, w)
	if err != nil {
		return BatchResult{}, err
	}
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return BatchResult{}, fmt.Errorf("ghclient: encode request: %w", err)
	}

	start := time.Now()
	var resp *response
	var src string
	for attempt := 0; ; attempt++ {
		var tok string
		tok, src, err = c.token(ctx)
		if err != nil {
			return BatchResult{}, err
		}
		resp, err = c.post(ctx, payload, tok)
		if err != nil {
			return BatchResult{}, err
		}
		c.log.Debug("github graphql", "status", resp.status, "request_id", resp.requestID,
			"token_source", src, "attempt", attempt+1, "duration", time.Since(start))
		if resp.status != http.StatusUnauthorized {
			break
		}
		c.Invalidate()
		if attempt >= 1 {
			return BatchResult{}, ErrUnauthorized
		}
	}
	dur := time.Since(start)

	if se := secondaryLimit(resp); se != nil {
		return BatchResult{}, se
	}
	if resp.status != http.StatusOK {
		if resp.header.Get("X-Ratelimit-Remaining") == "0" {
			return BatchResult{}, &PrimaryExhaustedError{ResetAt: headerReset(resp.header)}
		}
		switch {
		case resp.status >= 500:
			return BatchResult{}, &TransientError{Status: resp.status}
		case resp.status == http.StatusForbidden:
			return BatchResult{}, ErrForbidden
		default:
			return BatchResult{}, fmt.Errorf("ghclient: unexpected status %d", resp.status)
		}
	}

	bd, err := ghquery.DecodeBatch(resp.body, refs)
	resetAt, _ := time.Parse(time.RFC3339, bd.RateLimit.ResetAt)
	if err != nil {
		if errors.Is(err, ghquery.ErrRateLimited) {
			if r := headerReset(resp.header); !r.IsZero() {
				resetAt = r
			}
			return BatchResult{}, &PrimaryExhaustedError{ResetAt: resetAt}
		}
		return BatchResult{}, fmt.Errorf("ghclient: %w", err)
	}
	// A 200 with usable data is returned even when x-ratelimit-remaining is
	// 0: the data is valid and RateLimit.Remaining tells the scheduler to
	// stop until ResetAt.
	return BatchResult{
		Viewer:      bd.Viewer,
		RateLimit:   bd.RateLimit,
		ResetAt:     resetAt,
		Items:       bd.Items,
		RequestID:   resp.requestID,
		Duration:    dur,
		TokenSource: src,
	}, nil
}

// post sends one request. Transport and body-read failures come back as
// *TransientError (or the caller's context error); the token, headers and
// body never appear in an error.
func (c *Client) post(ctx context.Context, payload []byte, tok string) (*response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("ghclient: build request: %w", err)
	}
	req.Header.Set("Authorization", "bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.ua)

	hr, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("ghclient: request: %w", ctx.Err())
		}
		return nil, &TransientError{Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(hr.Body, drainLimit))
		_ = hr.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(hr.Body, maxBody+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("ghclient: read response: %w", ctx.Err())
		}
		return nil, &TransientError{Status: hr.StatusCode, Err: errors.New("read response body failed")}
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("ghclient: response exceeds %d MiB limit", maxBody>>20)
	}
	return &response{
		status:    hr.StatusCode,
		header:    hr.Header,
		body:      body,
		requestID: hr.Header.Get("X-GitHub-Request-Id"),
	}, nil
}

// secondaryLimit classifies a secondary rate limit: any 429, a Retry-After
// header on a 200 or 403, or a "secondary rate limit" message in the body.
func secondaryLimit(r *response) *SecondaryLimitError {
	ra, hasRA := retryAfter(r.header)
	switch {
	case r.status == http.StatusTooManyRequests:
		return &SecondaryLimitError{RetryAfter: ra, Status: r.status}
	case hasRA && (r.status == http.StatusOK || r.status == http.StatusForbidden):
		return &SecondaryLimitError{RetryAfter: ra, Status: r.status}
	case (r.status == http.StatusOK || r.status == http.StatusForbidden) && secondaryMessage(r.body):
		return &SecondaryLimitError{Status: r.status}
	}
	return nil
}

func secondaryMessage(body []byte) bool {
	var w struct {
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &w) != nil {
		return false
	}
	if isSecondary(w.Message) {
		return true
	}
	for _, e := range w.Errors {
		if isSecondary(e.Message) {
			return true
		}
	}
	return false
}

func isSecondary(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "secondary rate limit")
}

// retryAfter parses Retry-After as delta seconds or an HTTP date. The bool
// reports whether the header was present at all.
func retryAfter(h http.Header) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			n = 0
		}
		return time.Duration(n) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d.Round(time.Second), true
		}
	}
	return 0, true
}

// headerReset reads x-ratelimit-reset (unix seconds), or zero.
func headerReset(h http.Header) time.Time {
	n, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-Ratelimit-Reset")), 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}
