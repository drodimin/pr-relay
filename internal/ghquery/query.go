// Package ghquery holds the single GraphQL query pr-relay sends for a pull
// request and a pure decoder that turns its response into a model.Snapshot.
package ghquery

import _ "embed"

// Query is the PRState GraphQL operation. scripts/capture.sh sends the same
// file, so captured fixtures always match the production query.
//
//go:embed pr.graphql
var Query string
