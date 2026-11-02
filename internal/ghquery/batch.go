package ghquery

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/drodimin/pr-relay/internal/model"
)

// Window sets how much recent activity the PRFields fragment selects.
type Window struct {
	ReviewsLast         int
	CommentsLast        int
	ReviewCommentsFirst int
}

// NormalWindow is the activity window of a routine poll.
var NormalWindow = Window{ReviewsLast: 20, CommentsLast: 30, ReviewCommentsFirst: 30}

// WideWindow is the gap re-fetch window used when a normal window may have
// missed activity between polls.
var WideWindow = Window{ReviewsLast: 100, CommentsLast: 100, ReviewCommentsFirst: 100}

// MaxBatch is the most pull requests one PRBatch operation carries (D-02).
const MaxBatch = 20

// alias returns the response key for the i-th ref of a batch.
func alias(i int) string { return "pr" + strconv.Itoa(i) }

// BuildBatch returns one aliased PRBatch GraphQL operation covering refs, in
// order, plus its variables. Every owner, repo and number travels as a
// variable; aliases are derived from the index only, so no caller-supplied
// value ever reaches the query text (T-02-15). It rejects an empty batch,
// more than MaxBatch refs, any ref that is not Valid, and duplicate refs
// (owner and repo compared case-insensitively), so each PR is fetched once
// per poll whatever its subscriber count (DMN-02).
func BuildBatch(refs []model.PRRef, w Window) (string, map[string]any, error) {
	if len(refs) == 0 {
		return "", nil, errors.New("ghquery: build batch: no pull requests")
	}
	if len(refs) > MaxBatch {
		return "", nil, fmt.Errorf("ghquery: build batch: %d pull requests exceeds the limit of %d", len(refs), MaxBatch)
	}
	seen := make(map[string]bool, len(refs))
	for i, r := range refs {
		if !r.Valid() {
			return "", nil, fmt.Errorf("ghquery: build batch: ref %d is not a valid pull request reference", i)
		}
		k := strings.ToLower(r.String())
		if seen[k] {
			return "", nil, fmt.Errorf("ghquery: build batch: duplicate pull request %s", r.String())
		}
		seen[k] = true
	}

	vars := make(map[string]any, 3*len(refs)+3)
	var b strings.Builder
	b.WriteString("query PRBatch(")
	for i, r := range refs {
		n := strconv.Itoa(i)
		b.WriteString("$o" + n + ": String!, $r" + n + ": String!, $n" + n + ": Int!, ")
		vars["o"+n] = r.Owner
		vars["r"+n] = r.Repo
		vars["n"+n] = r.Number
	}
	b.WriteString("$reviewsLast: Int!, $commentsLast: Int!, $reviewCommentsFirst: Int!) {\n")
	b.WriteString("  rateLimit { cost limit remaining used resetAt nodeCount }\n")
	b.WriteString("  viewer { login }\n")
	for i := range refs {
		n := strconv.Itoa(i)
		b.WriteString("  " + alias(i) + ": repository(owner: $o" + n + ", name: $r" + n + ") { pullRequest(number: $n" + n + ") { ...PRFields } }\n")
	}
	b.WriteString("}\n\n")
	b.WriteString(Fragment)

	vars["reviewsLast"] = w.ReviewsLast
	vars["commentsLast"] = w.CommentsLast
	vars["reviewCommentsFirst"] = w.ReviewCommentsFirst
	return b.String(), vars, nil
}
