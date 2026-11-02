package ghquery

import (
	"encoding/json"
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

// Per-alias sentinels. Callers test them with errors.Is.
var ErrNotFound = errors.New("ghquery: pull request not found")

// ErrForbidden marks a pull request the token cannot read.
var ErrForbidden = errors.New("ghquery: pull request not accessible")

// ErrRateLimited matches a *BatchError carrying a RATE_LIMITED error.
var ErrRateLimited = errors.New("ghquery: rate limited")

// AliasResult is the outcome for one ref of a batch. Err is non-nil when the
// PR could not be used (ErrNotFound, ErrForbidden, a ref mismatch or a decode
// failure); Warnings carries tolerated errors scoped inside the pullRequest,
// such as FORBIDDEN on a workflowRun, rendered as "TYPE: message".
type AliasResult struct {
	Ref      model.PRRef
	Snapshot model.Snapshot
	Warnings []string
	Err      error
}

// BatchData is a decoded PRBatch response. Items follow the request order.
// Warnings holds operation-level errors that came with usable alias data.
type BatchData struct {
	Viewer    string
	RateLimit RateLimit
	Items     []AliasResult
	Warnings  []string
}

// BatchError is a batch-level failure: an error with no alias path while no
// alias returned data, or any RATE_LIMITED error.
type BatchError struct {
	Types    []string
	Messages []string
}

func (e *BatchError) Error() string {
	return "ghquery: batch errors: " + strings.Join(e.Messages, "; ")
}

// Is reports true for ErrRateLimited when any error type is RATE_LIMITED.
func (e *BatchError) Is(target error) bool {
	if target != ErrRateLimited {
		return false
	}
	for _, t := range e.Types {
		if t == "RATE_LIMITED" {
			return true
		}
	}
	return false
}

func newBatchError(errs []GraphQLError) *BatchError {
	be := &BatchError{Messages: errorMessages(errs)}
	for _, e := range errs {
		be.Types = append(be.Types, e.Type)
	}
	return be
}

type wireBatch struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []GraphQLError             `json:"errors"`
}

func isNull(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null"
}

// aliasScoped is the batch form of prScoped: an error is tolerated only when
// its path runs <alias>.pullRequest.<field>..., so it nulled a sub-field of a
// pull request that otherwise decoded (WR-04).
func aliasScoped(errs []GraphQLError) bool {
	for _, e := range errs {
		if len(e.Path) < 3 {
			return false
		}
		if p1, _ := e.Path[1].(string); p1 != "pullRequest" {
			return false
		}
	}
	return true
}

func hasType(errs []GraphQLError, typ string) bool {
	for _, e := range errs {
		if e.Type == typ {
			return true
		}
	}
	return false
}

// DecodeBatch turns a PRBatch response for refs (in the order passed to
// BuildBatch) into per-alias results. It is pure: no I/O, no body text kept.
// One bad alias never fails the others (T-02-16). A batch-level error is
// returned as *BatchError when the response carries RATE_LIMITED, or when a
// path-less error comes with no alias data at all. Response size limiting is
// the caller's job (ghclient, plan 02-07); data must already be bounded.
func DecodeBatch(data []byte, refs []model.PRRef) (BatchData, error) {
	if len(refs) == 0 {
		return BatchData{}, errors.New("ghquery: decode batch: no pull requests")
	}
	var resp wireBatch
	if err := json.Unmarshal(data, &resp); err != nil {
		return BatchData{}, fmt.Errorf("ghquery: decode batch: %w", err)
	}

	var bd BatchData
	if raw, ok := resp.Data["rateLimit"]; ok && !isNull(raw) {
		var w wireRateLimit
		if err := json.Unmarshal(raw, &w); err == nil {
			bd.RateLimit = RateLimit{Cost: w.Cost, Limit: w.Limit, Remaining: w.Remaining, Used: w.Used, NodeCount: w.NodeCount, ResetAt: w.ResetAt}
		}
	}
	if raw, ok := resp.Data["viewer"]; ok && !isNull(raw) {
		var v struct {
			Login string `json:"login"`
		}
		if err := json.Unmarshal(raw, &v); err == nil {
			bd.Viewer = v.Login
		}
	}

	index := make(map[string]int, len(refs))
	for i := range refs {
		index[alias(i)] = i
	}
	var batchErrs []GraphQLError
	aliasErrs := make([][]GraphQLError, len(refs))
	rateLimited := false
	for _, e := range resp.Errors {
		if e.Type == "RATE_LIMITED" {
			rateLimited = true
		}
		var p0 string
		if len(e.Path) > 0 {
			p0, _ = e.Path[0].(string)
		}
		if i, ok := index[p0]; ok && e.Type != "RATE_LIMITED" {
			aliasErrs[i] = append(aliasErrs[i], e)
			continue
		}
		batchErrs = append(batchErrs, e)
	}
	if rateLimited {
		return bd, newBatchError(resp.Errors)
	}
	if len(batchErrs) > 0 {
		anyData := false
		for i := range refs {
			if !isNull(resp.Data[alias(i)]) {
				anyData = true
				break
			}
		}
		if !anyData {
			return bd, newBatchError(batchErrs)
		}
		bd.Warnings = errorMessages(batchErrs)
	}

	bd.Items = make([]AliasResult, len(refs))
	for i, ref := range refs {
		bd.Items[i] = decodeAlias(ref, resp.Data[alias(i)], aliasErrs[i])
	}
	return bd, nil
}

// decodeAlias decodes one `prN: repository { pullRequest }` value.
func decodeAlias(ref model.PRRef, raw json.RawMessage, errs []GraphQLError) AliasResult {
	res := AliasResult{Ref: ref}
	var repo struct {
		PullRequest *wirePR `json:"pullRequest"`
	}
	if !isNull(raw) {
		if err := json.Unmarshal(raw, &repo); err != nil {
			res.Err = fmt.Errorf("ghquery: decode %s: %w", ref.String(), err)
			return res
		}
	}
	msgs := strings.Join(errorMessages(errs), "; ")
	if repo.PullRequest == nil {
		sentinel := ErrNotFound
		if hasType(errs, "FORBIDDEN") {
			sentinel = ErrForbidden
		}
		if msgs != "" {
			res.Err = fmt.Errorf("%w: %s: %s", sentinel, ref.String(), msgs)
		} else {
			res.Err = fmt.Errorf("%w: %s", sentinel, ref.String())
		}
		return res
	}
	if len(errs) > 0 && !aliasScoped(errs) {
		if hasType(errs, "FORBIDDEN") {
			res.Err = fmt.Errorf("%w: %s: %s", ErrForbidden, ref.String(), msgs)
		} else {
			res.Err = fmt.Errorf("ghquery: %s: graphql errors: %s", ref.String(), msgs)
		}
		return res
	}
	s, err := snapshotFromWire(repo.PullRequest)
	if err != nil {
		res.Err = fmt.Errorf("ghquery: decode %s: %w", ref.String(), err)
		return res
	}
	got := s.Ref
	if !strings.EqualFold(got.Owner, ref.Owner) || !strings.EqualFold(got.Repo, ref.Repo) || got.Number != ref.Number {
		res.Err = fmt.Errorf("ghquery: ref mismatch: requested %s, response is %s", ref.String(), got.String())
		return res
	}
	// The match is case-insensitive; keep the requested spelling so callers
	// can key results by the ref they asked for (T-02-17).
	s.Ref = ref
	res.Snapshot = s
	if len(errs) > 0 {
		res.Warnings = errorMessages(errs)
	}
	return res
}
