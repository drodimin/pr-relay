// Package model defines the pure data contract shared by the decoder, the
// diff engine, the filter and the renderer. It performs no I/O and depends on
// the standard library only.
package model

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// PRRef identifies a single pull request on github.com.
type PRRef struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

// String returns "owner/repo#N".
func (r PRRef) String() string { return fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number) }

// URL returns the canonical https://github.com/owner/repo/pull/N URL.
func (r PRRef) URL() string {
	return fmt.Sprintf("https://github.com/%s/%s/pull/%d", r.Owner, r.Repo, r.Number)
}

// Valid reports whether Owner and Repo match ^[A-Za-z0-9._-]{1,100}$, are not
// "." or "..", and Number > 0. Rejecting the dot segments keeps a ref from
// ever acting as a path traversal component in a persistence key (WR-07).
func (r PRRef) Valid() bool {
	return validName(r.Owner) && validName(r.Repo) && r.Number > 0
}

func validName(s string) bool {
	return s != "." && s != ".." && nameRE.MatchString(s)
}

// ParsePRURL accepts only https://github.com/{owner}/{repo}/pull/{n}, with an
// optional trailing slash, and returns a Valid ref.
func ParsePRURL(u string) (PRRef, error) {
	bad := func(why string) (PRRef, error) {
		return PRRef{}, fmt.Errorf("invalid pull request URL %q: %s", u, why)
	}
	pu, err := url.Parse(u)
	if err != nil {
		return bad("unparseable")
	}
	if pu.Scheme != "https" {
		return bad("scheme must be https")
	}
	if pu.User != nil {
		return bad("userinfo not allowed")
	}
	if pu.Port() != "" || pu.Host != "github.com" {
		return bad("host must be exactly github.com")
	}
	if pu.RawQuery != "" || pu.ForceQuery || pu.Fragment != "" || pu.Opaque != "" {
		return bad("query, fragment and opaque parts not allowed")
	}
	if pu.RawPath != "" && pu.RawPath != pu.Path {
		return bad("escaped path not allowed")
	}
	p := strings.TrimSuffix(pu.Path, "/")
	if !strings.HasPrefix(p, "/") {
		return bad("path must be /{owner}/{repo}/pull/{number}")
	}
	parts := strings.Split(p[1:], "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return bad("path must be /{owner}/{repo}/pull/{number}")
	}
	num := parts[3]
	if num == "" || strings.Trim(num, "0123456789") != "" {
		return bad("number must be decimal")
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return bad("number out of range")
	}
	ref := PRRef{Owner: parts[0], Repo: parts[1], Number: n}
	if !ref.Valid() {
		return bad("owner, repo or number invalid")
	}
	return ref, nil
}

// ParsePRLink is the lenient front door for agent-supplied pull request links
// (SUB-09, DMN-08, D-06). It accepts http or https, a missing scheme, the
// www. prefix, any host case, a trailing /files, /commits or /checks tab, a
// query and a fragment. It then rebuilds the canonical URL and hands it to the
// strict ParsePRURL, which stays the single source of truth for validation.
// Repository links, issue links, owner/repo#N shorthand and other hosts are
// rejected with distinct messages.
func ParsePRLink(s string) (PRRef, error) {
	orig := strings.TrimSpace(s)
	bad := func(why string) (PRRef, error) {
		return PRRef{}, fmt.Errorf("invalid pull request link %q: %s", orig, why)
	}
	if orig == "" {
		return bad("empty")
	}
	in := orig
	if !strings.Contains(in, "://") {
		low := strings.ToLower(in)
		ghPrefix := strings.HasPrefix(low, "github.com/") || strings.HasPrefix(low, "www.github.com/")
		if i := strings.IndexByte(in, '#'); i >= 0 && !ghPrefix && !strings.Contains(in[i+1:], "/") {
			return PRRef{}, fmt.Errorf("%q is not a link; pass the full pull request URL", orig)
		}
		first, _, _ := strings.Cut(in, "/")
		if !ghPrefix && !strings.ContainsAny(first, ".:") {
			// Bare "owner/repo[/...]": read it as a github.com path so a
			// repository gets the repository message, not a host error.
			in = "github.com/" + in
		}
		in = "https://" + in
	}
	pu, err := url.Parse(in)
	if err != nil {
		return bad("unparseable")
	}
	if pu.Scheme != "https" && pu.Scheme != "http" {
		return bad("scheme must be http or https")
	}
	if pu.User != nil {
		return bad("userinfo not allowed")
	}
	host := strings.ToLower(pu.Host)
	if host == "www.github.com" {
		host = "github.com"
	}
	if pu.Port() != "" || host != "github.com" {
		return PRRef{}, fmt.Errorf("only github.com is supported (got %s)", pu.Host)
	}
	if pu.Opaque != "" || (pu.RawPath != "" && pu.RawPath != pu.Path) {
		return bad("escaped path not allowed")
	}
	var segs []string
	if p := strings.Trim(pu.Path, "/"); p != "" {
		segs = strings.Split(p, "/")
	}
	for _, seg := range segs {
		if seg == "" {
			return bad("empty path segment")
		}
	}
	if len(segs) >= 3 && segs[2] == "issues" {
		return PRRef{}, fmt.Errorf("%q is an issue, not a pull request", orig)
	}
	if len(segs) == 2 {
		return PRRef{}, fmt.Errorf("%q is a repository; pr-relay watches single pull requests", orig)
	}
	if len(segs) < 4 || segs[2] != "pull" {
		return bad("path must be /{owner}/{repo}/pull/{number}")
	}
	if len(segs) > 4 {
		switch segs[4] {
		case "files", "commits", "checks":
		default:
			return bad("unsupported pull request URL path")
		}
	}
	ref, err := ParsePRURL("https://github.com/" + segs[0] + "/" + segs[1] + "/pull/" + segs[3])
	if err != nil {
		return PRRef{}, fmt.Errorf("invalid pull request link %q: %w", orig, err)
	}
	return ref, nil
}

// Actor is a GitHub account. Type is the GraphQL __typename ("User", "Bot",
// "Mannequin", "Organization"), "Ghost" for a null author, or "" when a commit
// author is unknown.
type Actor struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// IsUser reports whether the actor is a real user account (Type == "User").
func (a Actor) IsUser() bool { return a.Type == "User" }

// CheckRun is one Checks API check run on the head commit.
type CheckRun struct {
	Key        string // Workflow+"/"+Name(+"@"+Event when Event != "") when Workflow != "", else App+"/"+Name
	Name       string
	Workflow   string
	App        string
	DatabaseID int64
	Status     string // CheckStatusState verbatim: COMPLETED|IN_PROGRESS|PENDING|QUEUED|REQUESTED|WAITING
	Conclusion string // CheckConclusionState verbatim, "" until COMPLETED
	URL        string
	DetailsURL string
	RunAttempt int
	Event      string // checkSuite.workflowRun.event ("push", "pull_request", ...), "" when absent
}

// ReviewComment is an inline comment attached to a review. It carries no body.
type ReviewComment struct {
	ID        string
	URL       string
	Path      string
	ReplyToID string
	Author    Actor
}

// Review is a submitted pull request review. HasBody replaces the body text.
type Review struct {
	ID            string
	State         string
	URL           string
	Author        Actor
	HasBody       bool
	Comments      []ReviewComment
	CommentsTotal int
	SubmittedAt   time.Time // submittedAt, zero for PENDING or unparseable
}

// IssueComment is a top-level PR conversation comment. It carries no body.
type IssueComment struct {
	ID        string
	URL       string
	Author    Actor
	CreatedAt time.Time // createdAt, zero when absent or unparseable
}

// Commit is one commit on the PR head branch. Author comes from
// author.user, else committer.user, else Actor{} (unknown).
type Commit struct {
	OID    string
	Author Actor
}

// ForcePush is a HeadRefForcePushedEvent timeline item.
type ForcePush struct {
	ID        string
	BeforeOID string
	AfterOID  string
	Actor     Actor
}

// Snapshot is the decoded state of one pull request at one poll.
type Snapshot struct {
	Ref                PRRef
	NodeID             string
	State              string // OPEN|CLOSED|MERGED
	MergedBy           Actor
	ClosedBy           Actor
	HeadOID            string
	BaseOID            string
	Mergeable          string   // MERGEABLE|CONFLICTING|UNKNOWN
	Commits            []Commit // oldest -> newest, as returned by commits(last:30)
	CommitsTotal       int
	CIKnown            bool       // true when rollup is null or rollup.commit.oid == HeadOID
	Checks             []CheckRun // CheckRun only, newest DatabaseID per Key, sorted by Key
	ChecksTruncated    bool       // contexts.pageInfo.hasNextPage
	UnknownConclusions []string   // conclusion values outside the 9 known enums
	Reviews            []Review
	ReviewsTotal       int
	Comments           []IssueComment
	CommentsTotal      int
	ForcePushes        []ForcePush
}

// EventType names a class of PR update delivered to agents.
type EventType string

// Event types, listed in in-step emission order.
const (
	EventClosed    EventType = "closed"
	EventConflict  EventType = "conflict"
	EventCommits   EventType = "commits"
	EventCIFailed  EventType = "ci.failed"
	EventCISettled EventType = "ci.settled"
	EventReview    EventType = "review"
	EventComment   EventType = "comment"
)

// AllEventTypes returns every event type in emission order.
func AllEventTypes() []EventType {
	return []EventType{EventClosed, EventConflict, EventCommits, EventCIFailed, EventCISettled, EventReview, EventComment}
}

// Synthetic lifecycle events queued by the poller itself rather than detected
// on GitHub (LIFE-06, LIFE-07). They are deliberately absent from
// AllEventTypes, so they can never be selected or masked through --events.
const (
	EventCheckin EventType = "checkin"
	EventExpired EventType = "expired"
)

// IsLifecycle reports whether t is a synthetic lifecycle event.
func (t EventType) IsLifecycle() bool { return t == EventCheckin || t == EventExpired }

// Rank is the index of t in AllEventTypes, or 99 if t is unknown.
func (t EventType) Rank() int {
	for i, x := range AllEventTypes() {
		if x == t {
			return i
		}
	}
	return 99
}

// Event is one detected PR update. Only the fields relevant to Type are set.
type Event struct {
	Type  EventType `json:"type"`
	Key   string    `json:"key"` // stable dedupe key
	Actor Actor     `json:"actor,omitempty"`
	URL   string    `json:"url,omitempty"`

	// ci.failed (HeadOID also on ci.settled and commits)
	CheckName     string `json:"check_name,omitempty"`
	CheckWorkflow string `json:"check_workflow,omitempty"`
	CheckURL      string `json:"check_url,omitempty"`
	HeadOID       string `json:"head_oid,omitempty"`

	// ci.settled
	Passed      bool     `json:"passed,omitempty"`
	Total       int      `json:"total,omitempty"`
	FailedNames []string `json:"failed_names,omitempty"`

	// review: Verdict is the raw GraphQL state (APPROVED|CHANGES_REQUESTED|COMMENTED|DISMISSED)
	Verdict     string `json:"verdict,omitempty"`
	InlineCount int    `json:"inline_count,omitempty"`

	// comment: CommentKind is "comment" (issue), "reply" (thread reply) or
	// "inline" (non-reply inline on an already-seen review)
	CommentKind string `json:"comment_kind,omitempty"`
	Path        string `json:"path,omitempty"`

	// commits: Count 0 = unknown
	OldHeadOID  string  `json:"old_head_oid,omitempty"`
	Count       int     `json:"count,omitempty"`
	ForcePushed bool    `json:"force_pushed,omitempty"`
	Authors     []Actor `json:"authors,omitempty"`

	// conflict: true = became conflicting, false = cleared
	Conflicting bool `json:"conflicting,omitempty"`

	// closed
	Merged bool `json:"merged,omitempty"`

	// checkin, expired: the configured idle threshold in seconds (D-12)
	IdleSecs int64 `json:"idle_secs,omitempty"`
}

// Memory is the per-PR diff state carried between steps. It is schema v1,
// persisted by internal/store as JSON; the field set is final for Phase 2 and
// later Phase 2 plans must not add Memory fields.
type Memory struct {
	Baselined       bool            `json:"baselined"`
	HeadOID         string          `json:"head_oid"`
	BaseOID         string          `json:"base_oid"`
	State           string          `json:"state"`
	Mergeable       string          `json:"mergeable"`        // last RESOLVED value, "" = never resolved
	FailedReported  map[string]bool `json:"failed_reported"`  // CheckRun.Key -> ci.failed emitted for HeadOID
	SettleCandidate string          `json:"settle_candidate"` // fingerprint of all-complete check set seen last step, "" none
	SettledResult   string          `json:"settled_result"`   // "" | "passed" | "failed:<sorted,comma,joined,keys>"
	SettleSeq       int             `json:"settle_seq"`
	SeenReviews     map[string]bool `json:"seen_reviews"`
	SeenComments    map[string]bool `json:"seen_comments"`
	SeenForcePushes map[string]bool `json:"seen_force_pushes"`
	ReviewsTotal    int             `json:"reviews_total"`
	CommentsTotal   int             `json:"comments_total"`

	Epoch               int            `json:"epoch"`                 // WR-02: bumped on every head change and every state transition
	CommitsTotal        int            `json:"commits_total"`         // WR-01: last seen Snapshot.CommitsTotal
	CommentsWatermark   time.Time      `json:"comments_watermark"`    // CR-01: max issue-comment createdAt seen
	ReviewsWatermark    time.Time      `json:"reviews_watermark"`     // CR-01: max submittedAt of non-PENDING reviews seen
	ReviewCommentTotals map[string]int `json:"review_comment_totals"` // WR-06: review ID -> last seen Review.CommentsTotal
}

func cloneSet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneCounts(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Clone returns a deep copy of m. Nil maps become empty maps.
func (m Memory) Clone() Memory {
	c := m
	c.FailedReported = cloneSet(m.FailedReported)
	c.SeenReviews = cloneSet(m.SeenReviews)
	c.SeenComments = cloneSet(m.SeenComments)
	c.SeenForcePushes = cloneSet(m.SeenForcePushes)
	c.ReviewCommentTotals = cloneCounts(m.ReviewCommentTotals)
	return c
}
