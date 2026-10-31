#!/usr/bin/env bash
# Capture PRState GraphQL snapshots of one PR into numbered fixture files.
#
# usage: PR_RELAY_FIXTURE_REPO=<owner>/<repo> scripts/capture.sh <owner> <repo> <number> <outdir> [interval_s=5]
#
# - Sends the exact query the daemon embeds (internal/ghquery/pr.graphql).
# - Refuses to run unless PR_RELAY_FIXTURE_REPO names the same repo, so
#   fixtures are only ever captured from the declared dummy repo.
# - Rewrites every non-blank `body` to PRRELAY-BODY-SENTINEL before writing,
#   so no comment or review text reaches a committed fixture.
# - Writes a new NN.json only when the payload (minus data.rateLimit) changed.
# - Stops on SIGINT/SIGTERM or when <outdir>/STOP exists.
set -euo pipefail

usage() {
  echo "usage: PR_RELAY_FIXTURE_REPO=<owner>/<repo> $0 <owner> <repo> <number> <outdir> [interval_s=5]" >&2
  exit 2
}

[[ $# -ge 4 && $# -le 5 ]] || usage
owner=$1 repo=$2 number=$3 out=$4 interval=${5:-5}

if [[ "${PR_RELAY_FIXTURE_REPO:-}" != "$owner/$repo" ]]; then
  echo "capture.sh: refusing to run: PR_RELAY_FIXTURE_REPO='${PR_RELAY_FIXTURE_REPO:-}' does not equal '$owner/$repo'." >&2
  echo "capture.sh: fixtures may only be captured from the declared dummy repo." >&2
  exit 1
fi
[[ $number =~ ^[1-9][0-9]*$ ]] || { echo "capture.sh: number must be a positive integer" >&2; exit 2; }
[[ $interval =~ ^[0-9]+$ ]] || { echo "capture.sh: interval must be whole seconds" >&2; exit 2; }

for tool in gh jq; do
  command -v "$tool" >/dev/null || { echo "capture.sh: $tool not found on PATH" >&2; exit 1; }
done
if command -v sha256sum >/dev/null; then
  hash() { sha256sum | cut -c1-16; }
else
  hash() { shasum -a 256 | cut -c1-16; }
fi

# Resolve outdir before changing directory so relative paths keep working.
mkdir -p "$out"
out=$(cd "$out" && pwd)
cd "$(git rev-parse --show-toplevel)"

stop=0
trap 'stop=1' INT TERM

# Continue numbering after any existing NN.json files.
i=0
for f in "$out"/[0-9][0-9]*.json; do
  [[ -e $f ]] || continue
  n=$(basename "$f"); n=${n%%[!0-9]*}
  (( 10#$n >= i )) && i=$(( 10#$n + 1 ))
done

normalize='walk(
  if type == "object" and (.body | type) == "string" and (.body | test("\\S"))
  then .body = "PRRELAY-BODY-SENTINEL"
  else . end
)'

last=""
while (( stop == 0 )) && [[ ! -e "$out/STOP" ]]; do
  if raw=$(gh api graphql -F owner="$owner" -F repo="$repo" -F number="$number" -F query=@internal/ghquery/pr.graphql); then
    norm=$(jq -S "$normalize" <<<"$raw")
    h=$(jq -S -c 'del(.data.rateLimit)' <<<"$norm" | hash)
    if [[ $h != "$last" ]]; then
      file="$out/$(printf '%02d' "$i").json"
      printf '%s\n' "$norm" > "$file"
      echo "$file"
      i=$((i + 1))
      last=$h
    fi
  else
    echo "capture.sh: gh api graphql failed; retrying" >&2
  fi
  (( stop == 0 )) || break
  sleep "$interval" &
  wait $! || true
done
echo "capture.sh: stopped after $i file(s) in $out" >&2
