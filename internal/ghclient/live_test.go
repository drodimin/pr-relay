package ghclient

import (
	"errors"
	"os"
	"testing"

	"github.com/drodimin/pr-relay/internal/ghquery"
	"github.com/drodimin/pr-relay/internal/model"
)

// TestLiveBatch probes research assumptions A1 (GitHub accepts the PRBatch
// document with a fragment and variables) and A2 (a missing PR fails only
// its own alias) against the real API. It is opt-in: it runs only when
// PR_RELAY_LIVE=1, uses the user's gh login, sends one read-only query and
// mutates nothing. Plan 02-12 runs it.
func TestLiveBatch(t *testing.T) {
	if os.Getenv("PR_RELAY_LIVE") != "1" {
		t.Skip("live GitHub test; set PR_RELAY_LIVE=1 to run")
	}
	c, err := New(Options{Endpoint: DefaultEndpoint, Tokens: &TokenSource{}, UserAgent: "pr-relay/live-test"})
	if err != nil {
		t.Fatal(err)
	}
	refs := []model.PRRef{
		{Owner: "drodimin", Repo: "pr-relay-fixtures", Number: 1},
		{Owner: "drodimin", Repo: "pr-relay-fixtures", Number: 99999},
	}
	res, err := c.Batch(t.Context(), refs, ghquery.NormalWindow)
	if err != nil {
		t.Fatalf("batch-level error (A1): %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(res.Items))
	}
	if res.Items[0].Err != nil {
		t.Fatalf("existing PR: %v", res.Items[0].Err)
	}
	if res.Items[0].Snapshot.HeadOID == "" {
		t.Fatal("existing PR has empty HeadOID")
	}
	if !errors.Is(res.Items[1].Err, ghquery.ErrNotFound) {
		t.Fatalf("missing PR err = %v, want ghquery.ErrNotFound (A2)", res.Items[1].Err)
	}
	if res.Viewer == "" {
		t.Fatal("viewer login empty")
	}
	if res.RateLimit.Cost < 1 {
		t.Fatalf("rateLimit.cost = %d, want >= 1", res.RateLimit.Cost)
	}
	t.Logf("cost=%d remaining=%d nodeCount=%d", res.RateLimit.Cost, res.RateLimit.Remaining, res.RateLimit.NodeCount)
}
