package ghquery

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/drodimin/pr-relay/internal/model"
)

// RateLimit mirrors the GraphQL rateLimit object returned with every PRState query.
type RateLimit struct {
	Cost      int
	Limit     int
	Remaining int
	Used      int
	NodeCount int
	ResetAt   string
}

// knownConclusions lists every CheckConclusionState value in GitHub's schema.
var knownConclusions = map[string]bool{
	"ACTION_REQUIRED": true,
	"CANCELLED":       true,
	"FAILURE":         true,
	"NEUTRAL":         true,
	"SKIPPED":         true,
	"STALE":           true,
	"STARTUP_FAILURE": true,
	"SUCCESS":         true,
	"TIMED_OUT":       true,
}

// Wire structs mirror the JSON response. They stay private to this file and
// never end up in a Snapshot.

type wireActor struct {
	Typename string `json:"__typename"`
	Login    string `json:"login"`
}

type wireUserRef struct {
	User *struct {
		Login string `json:"login"`
	} `json:"user"`
}

type wireOID struct {
	OID string `json:"oid"`
}

type wireResponse struct {
	Data *struct {
		RateLimit  *wireRateLimit `json:"rateLimit"`
		Repository *struct {
			PullRequest *wirePR `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"errors"`
}

type wireRateLimit struct {
	Cost      int    `json:"cost"`
	Limit     int    `json:"limit"`
	Remaining int    `json:"remaining"`
	Used      int    `json:"used"`
	ResetAt   string `json:"resetAt"`
	NodeCount int    `json:"nodeCount"`
}

type wirePR struct {
	ID         string     `json:"id"`
	URL        string     `json:"url"`
	State      string     `json:"state"`
	Merged     bool       `json:"merged"`
	MergedBy   *wireActor `json:"mergedBy"`
	HeadRefOID string     `json:"headRefOid"`
	BaseRefOID string     `json:"baseRefOid"`
	Mergeable  string     `json:"mergeable"`
	Commits    struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Commit struct {
				OID       string       `json:"oid"`
				Author    *wireUserRef `json:"author"`
				Committer *wireUserRef `json:"committer"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	StatusCheckRollup *struct {
		Commit   *wireOID `json:"commit"`
		Contexts struct {
			PageInfo struct {
				HasNextPage bool `json:"hasNextPage"`
			} `json:"pageInfo"`
			Nodes []wireContext `json:"nodes"`
		} `json:"contexts"`
	} `json:"statusCheckRollup"`
	Reviews struct {
		TotalCount int          `json:"totalCount"`
		Nodes      []wireReview `json:"nodes"`
	} `json:"reviews"`
	Comments struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			ID     string     `json:"id"`
			URL    string     `json:"url"`
			Author *wireActor `json:"author"`
		} `json:"nodes"`
	} `json:"comments"`
	TimelineItems struct {
		Nodes []struct {
			Typename     string     `json:"__typename"`
			ID           string     `json:"id"`
			Actor        *wireActor `json:"actor"`
			BeforeCommit *wireOID   `json:"beforeCommit"`
			AfterCommit  *wireOID   `json:"afterCommit"`
		} `json:"nodes"`
	} `json:"timelineItems"`
}

type wireContext struct {
	Typename   string  `json:"__typename"`
	DatabaseID int64   `json:"databaseId"`
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	Conclusion *string `json:"conclusion"`
	URL        string  `json:"url"`
	DetailsURL string  `json:"detailsUrl"`
	CheckSuite *struct {
		App *struct {
			Slug string `json:"slug"`
		} `json:"app"`
		WorkflowRun *struct {
			RunAttempt int `json:"runAttempt"`
			Workflow   *struct {
				Name string `json:"name"`
			} `json:"workflow"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`
}

type wireReview struct {
	ID       string     `json:"id"`
	State    string     `json:"state"`
	URL      string     `json:"url"`
	Body     string     `json:"body"` // read only to derive HasBody, never copied out
	Author   *wireActor `json:"author"`
	Comments struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			ID      string `json:"id"`
			URL     string `json:"url"`
			Path    string `json:"path"`
			ReplyTo *struct {
				ID string `json:"id"`
			} `json:"replyTo"`
			Author *wireActor `json:"author"`
		} `json:"nodes"`
	} `json:"comments"`
}

// ghost is the actor GitHub shows for deleted accounts (a null author).
var ghost = model.Actor{Login: "ghost", Type: "Ghost"}

func actor(a *wireActor) model.Actor {
	if a == nil {
		return ghost
	}
	return model.Actor{Login: a.Login, Type: a.Typename}
}

func userRef(u *wireUserRef) (model.Actor, bool) {
	if u == nil || u.User == nil {
		return model.Actor{}, false
	}
	return model.Actor{Login: u.User.Login, Type: "User"}, true
}

// Decode turns a PRState GraphQL response into a Snapshot. It is pure: it
// performs no I/O and keeps no review or comment body text.
func Decode(data []byte) (model.Snapshot, RateLimit, error) {
	var resp wireResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return model.Snapshot{}, RateLimit{}, fmt.Errorf("ghquery: decode response: %w", err)
	}
	var rl RateLimit
	if resp.Data != nil && resp.Data.RateLimit != nil {
		w := resp.Data.RateLimit
		rl = RateLimit{Cost: w.Cost, Limit: w.Limit, Remaining: w.Remaining, Used: w.Used, NodeCount: w.NodeCount, ResetAt: w.ResetAt}
	}
	if len(resp.Errors) > 0 {
		msgs := make([]string, 0, len(resp.Errors))
		for _, e := range resp.Errors {
			if e.Type != "" {
				msgs = append(msgs, e.Type+": "+e.Message)
			} else {
				msgs = append(msgs, e.Message)
			}
		}
		return model.Snapshot{}, rl, fmt.Errorf("ghquery: graphql errors: %s", strings.Join(msgs, "; "))
	}
	if resp.Data == nil || resp.Data.Repository == nil || resp.Data.Repository.PullRequest == nil {
		return model.Snapshot{}, rl, errors.New("ghquery: response has no pullRequest")
	}
	pr := resp.Data.Repository.PullRequest
	ref, err := model.ParsePRURL(pr.URL)
	if err != nil {
		return model.Snapshot{}, rl, fmt.Errorf("ghquery: %w", err)
	}

	s := model.Snapshot{
		Ref:           ref,
		NodeID:        pr.ID,
		State:         pr.State,
		HeadOID:       pr.HeadRefOID,
		BaseOID:       pr.BaseRefOID,
		Mergeable:     pr.Mergeable,
		CommitsTotal:  pr.Commits.TotalCount,
		ReviewsTotal:  pr.Reviews.TotalCount,
		CommentsTotal: pr.Comments.TotalCount,
	}
	if pr.MergedBy != nil {
		s.MergedBy = actor(pr.MergedBy)
	} else if pr.Merged {
		s.MergedBy = ghost
	}

	for _, n := range pr.Commits.Nodes {
		a, ok := userRef(n.Commit.Author)
		if !ok {
			a, _ = userRef(n.Commit.Committer)
		}
		s.Commits = append(s.Commits, model.Commit{OID: n.Commit.OID, Author: a})
	}

	decodeChecks(&s, pr)

	for _, r := range pr.Reviews.Nodes {
		rv := model.Review{
			ID:            r.ID,
			State:         r.State,
			URL:           r.URL,
			Author:        actor(r.Author),
			HasBody:       strings.TrimSpace(r.Body) != "",
			CommentsTotal: r.Comments.TotalCount,
		}
		for _, c := range r.Comments.Nodes {
			rc := model.ReviewComment{ID: c.ID, URL: c.URL, Path: c.Path, Author: actor(c.Author)}
			if c.ReplyTo != nil {
				rc.ReplyToID = c.ReplyTo.ID
			}
			rv.Comments = append(rv.Comments, rc)
		}
		s.Reviews = append(s.Reviews, rv)
	}

	for _, c := range pr.Comments.Nodes {
		s.Comments = append(s.Comments, model.IssueComment{ID: c.ID, URL: c.URL, Author: actor(c.Author)})
	}

	// timelineItems(last:N) is chronological, so the last ClosedEvent is the newest.
	for _, it := range pr.TimelineItems.Nodes {
		switch it.Typename {
		case "ClosedEvent":
			s.ClosedBy = actor(it.Actor)
		case "HeadRefForcePushedEvent":
			fp := model.ForcePush{ID: it.ID, Actor: actor(it.Actor)}
			if it.BeforeCommit != nil {
				fp.BeforeOID = it.BeforeCommit.OID
			}
			if it.AfterCommit != nil {
				fp.AfterOID = it.AfterCommit.OID
			}
			s.ForcePushes = append(s.ForcePushes, fp)
		}
	}
	return s, rl, nil
}

// decodeChecks applies D-06/D-08/D-09: only CheckRun nodes, only when the
// rollup belongs to the current head, newest databaseId per Key, sorted by Key.
func decodeChecks(s *model.Snapshot, pr *wirePR) {
	roll := pr.StatusCheckRollup
	if roll == nil {
		s.CIKnown = true
		return
	}
	if roll.Commit == nil || roll.Commit.OID != pr.HeadRefOID {
		s.CIKnown = false
		return
	}
	s.CIKnown = true
	s.ChecksTruncated = roll.Contexts.PageInfo.HasNextPage

	newest := map[string]model.CheckRun{}
	for _, n := range roll.Contexts.Nodes {
		if n.Typename != "CheckRun" {
			continue // StatusContext and anything else (D-09)
		}
		cr := model.CheckRun{
			Name:       n.Name,
			DatabaseID: n.DatabaseID,
			Status:     n.Status,
			URL:        n.URL,
			DetailsURL: n.DetailsURL,
		}
		if n.Conclusion != nil {
			cr.Conclusion = *n.Conclusion
		}
		if cs := n.CheckSuite; cs != nil {
			if cs.App != nil {
				cr.App = cs.App.Slug
			}
			if wr := cs.WorkflowRun; wr != nil {
				cr.RunAttempt = wr.RunAttempt
				if wr.Workflow != nil {
					cr.Workflow = wr.Workflow.Name
				}
			}
		}
		if cr.Workflow != "" {
			cr.Key = cr.Workflow + "/" + cr.Name
		} else {
			cr.Key = cr.App + "/" + cr.Name
		}
		if prev, ok := newest[cr.Key]; !ok || cr.DatabaseID > prev.DatabaseID {
			newest[cr.Key] = cr
		}
	}

	s.Checks = make([]model.CheckRun, 0, len(newest))
	for _, cr := range newest {
		s.Checks = append(s.Checks, cr)
	}
	sort.Slice(s.Checks, func(i, j int) bool { return s.Checks[i].Key < s.Checks[j].Key })

	seen := map[string]bool{}
	for _, cr := range s.Checks {
		if cr.Conclusion != "" && !knownConclusions[cr.Conclusion] && !seen[cr.Conclusion] {
			seen[cr.Conclusion] = true
			s.UnknownConclusions = append(s.UnknownConclusions, cr.Conclusion)
		}
	}
}
