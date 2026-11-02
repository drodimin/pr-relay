// Package ghquery holds the single GraphQL query pr-relay sends for a pull
// request and a pure decoder that turns its response into a model.Snapshot.
package ghquery

import (
	_ "embed"
	"strings"
)

// Query is the PRState GraphQL operation plus the PRFields fragment it
// spreads. scripts/capture.sh sends the same file, so captured fixtures always
// match the production query. The activity windows are defaulted variables,
// so a caller that passes only owner, repo and number gets the defaults.
//
//go:embed pr.graphql
var Query string

// Fragment is the `fragment PRFields on PullRequest { ... }` definition taken
// from pr.graphql. The batch builder (plan 02-05) appends it to its PRBatch
// operation so single and batched polls select exactly the same fields.
var Fragment string

const fragmentKeyword = "fragment PRFields"

func init() {
	i := strings.Index(Query, fragmentKeyword)
	if i < 0 {
		panic("ghquery: pr.graphql has no " + fragmentKeyword)
	}
	Fragment = Query[i:]
}
