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
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// PRRef identifies a single pull request on github.com.
type PRRef struct {
	Owner  string
	Repo   string
	Number int
}

// String returns "owner/repo#N".
func (r PRRef) String() string { return fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number) }

// URL returns the canonical https://github.com/owner/repo/pull/N URL.
func (r PRRef) URL() string {
	return fmt.Sprintf("https://github.com/%s/%s/pull/%d", r.Owner, r.Repo, r.Number)
}

// Valid reports whether Owner and Repo match ^[A-Za-z0-9._-]{1,100}$ and Number > 0.
func (r PRRef) Valid() bool {
	return nameRE.MatchString(r.Owner) && nameRE.MatchString(r.Repo) && r.Number > 0
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

// Actor is a GitHub account. Type is the GraphQL __typename ("User", "Bot",
// "Mannequin", "Organization"), "Ghost" for a null author, or "" when a commit
// author is unknown.
type Actor struct {
	Login string
	Type  string
}

// IsUser reports whether the actor is a real user account (Type == "User").
func (a Actor) IsUser() bool { return a.Type == "User" }

// CheckRun is one Checks API check run on the head commit.
type CheckRun struct {
	Key        string // Workflow+"/"+Name when Workflow != "", else App+"/"+Name
	Name       string
	Workflow   string
	App        string
	DatabaseID int64
	Status     string // CheckStatusState verbatim: COMPLETED|IN_PROGRESS|PENDING|QUEUED|REQUESTED|WAITING
	Conclusion string // CheckConclusionState verbatim, "" until COMPLETED
	URL        string
	DetailsURL string
	RunAttempt int
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
}

// IssueComment is a top-level PR conversation comment. It carries no body.
type IssueComment struct {
	ID     string
	URL    string
	Author Actor
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
	Type  EventType
	Key   string // stable dedupe key
	Actor Actor
	URL   string

	// ci.failed (HeadOID also on ci.settled and commits)
	CheckName     string
	CheckWorkflow string
	CheckURL      string
	HeadOID       string

	// ci.settled
	Passed      bool
	Total       int
	FailedNames []string

	// review: Verdict is the raw GraphQL state (APPROVED|CHANGES_REQUESTED|COMMENTED|DISMISSED)
	Verdict     string
	InlineCount int

	// comment: CommentKind is "comment" (issue), "reply" (thread reply) or
	// "inline" (non-reply inline on an already-seen review)
	CommentKind string
	Path        string

	// commits: Count 0 = unknown
	OldHeadOID  string
	Count       int
	ForcePushed bool
	Authors     []Actor

	// conflict: true = became conflicting, false = cleared
	Conflicting bool

	// closed
	Merged bool
}

// Memory is the per-PR diff state carried between steps.
type Memory struct {
	Baselined       bool
	HeadOID         string
	BaseOID         string
	State           string
	Mergeable       string          // last RESOLVED value, "" = never resolved
	FailedReported  map[string]bool // CheckRun.Key -> ci.failed emitted for HeadOID
	SettleCandidate string          // fingerprint of all-complete check set seen last step, "" none
	SettledResult   string          // "" | "passed" | "failed:<sorted,comma,joined,keys>"
	SettleSeq       int
	SeenReviews     map[string]bool
	SeenComments    map[string]bool
	SeenForcePushes map[string]bool
	ReviewsTotal    int
	CommentsTotal   int
}

func cloneSet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
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
	return c
}
