# pr-relay-fixtures

Scratch repo used to drive real pull request scenarios (CI, reviews, comments, conflicts, merges) for pr-relay.
Its PRs are captured as GraphQL snapshots by `scripts/capture.sh` in https://github.com/drodimin/pr-relay.
Nothing here is real code: `src/*.go` are numbered comment lines used for conflicts and inline comments.
The template lives in pr-relay under `scripts/fixture-repo/`.
