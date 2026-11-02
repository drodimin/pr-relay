package ghquery

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/drodimin/pr-relay/internal/model"
)

// TestDecodeBatchLiveFixture decodes one real two-alias PRBatch response,
// captured by plan 02-12 from drodimin/pr-relay-fixtures #1 (ci-fail-pass)
// and #13 (ci-flaky-rerun) with BuildBatch(NormalWindow). It guards the
// decoder against drift between the synthetic batch fixtures and what
// GitHub actually returns.
func TestDecodeBatchLiveFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/batch-live.json")
	if err != nil {
		t.Fatal(err)
	}
	refs := []model.PRRef{
		{Owner: "drodimin", Repo: "pr-relay-fixtures", Number: 1},
		{Owner: "drodimin", Repo: "pr-relay-fixtures", Number: 13},
	}
	bd, err := DecodeBatch(raw, refs)
	if err != nil {
		t.Fatalf("DecodeBatch: %v", err)
	}
	if bd.Viewer == "" {
		t.Error("viewer login empty")
	}
	if bd.RateLimit.Cost < 1 {
		t.Errorf("rateLimit.cost = %d, want >= 1", bd.RateLimit.Cost)
	}
	if len(bd.Warnings) != 0 {
		t.Errorf("batch warnings = %v, want none", bd.Warnings)
	}
	if len(bd.Items) != len(refs) {
		t.Fatalf("items = %d, want %d", len(bd.Items), len(refs))
	}
	for i, it := range bd.Items {
		if it.Err != nil {
			t.Errorf("item %d (%s): %v", i, refs[i].String(), it.Err)
			continue
		}
		if it.Snapshot.Ref != refs[i] {
			t.Errorf("item %d ref = %+v, want %+v", i, it.Snapshot.Ref, refs[i])
		}
		if it.Snapshot.HeadOID == "" {
			t.Errorf("item %d (%s): empty HeadOID", i, refs[i].String())
		}
		if it.Snapshot.CIKnown {
			for _, c := range it.Snapshot.Checks {
				if c.Key == "" {
					t.Errorf("item %d: check %q has an empty Key", i, c.Name)
				}
			}
		}
	}

	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	var bad []string
	badBodies(generic, "$", &bad)
	for _, p := range bad {
		t.Errorf("%s holds a body other than \"\" or the sentinel", p)
	}
}
