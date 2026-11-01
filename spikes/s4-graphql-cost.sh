#!/usr/bin/env bash
# spikes/s4-graphql-cost.sh - spike S4: shape and cost of the PRState query.
#
# No herdr involved; needs only an authenticated `gh`. Read-only GitHub calls.
#
# Target: PR_RELAY_FIXTURE_REPO (owner/name) if set, else cli/cli (public).
# Single-PR runs use PR_RELAY_FIXTURE_PR (default 1). Batch runs use the
# most recent N PR numbers of the target repo.
#
# For 1, 5 and 20 PRs it runs the query with rateLimit(dryRun: true) and for
# real, recording cost, nodeCount, remaining and latency to spikes/out/s4.log.
set -euo pipefail

SPIKES_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SPIKES_DIR/.." && pwd)"
OUT="$SPIKES_DIR/out"
QUERY_SRC="$REPO_ROOT/internal/ghquery/pr.graphql"
LOG="$OUT/s4.log"
mkdir -p "$OUT"

REPO="${PR_RELAY_FIXTURE_REPO:-cli/cli}"
OWNER="${REPO%%/*}"
NAME="${REPO#*/}"
SINGLE_PR="${PR_RELAY_FIXTURE_PR:-1}"

now_ms() { perl -MTime::HiRes=time -e 'printf "%d\n", time()*1000'; }
log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" | tee -a "$LOG"; }

[ -f "$QUERY_SRC" ] || { echo "missing $QUERY_SRC" >&2; exit 1; }
command -v gh >/dev/null || { echo "gh not found" >&2; exit 1; }
command -v jq >/dev/null || { echo "jq not found" >&2; exit 1; }

# Selection set of pullRequest { ... }: lines after the pullRequest line,
# minus the closing braces of pullRequest, repository and the query.
SELECTION="$OUT/s4-selection.graphql"
awk '/pullRequest\(number: \$number\) \{/{on=1; next} on{print}' "$QUERY_SRC" | sed '$d' | sed '$d' | sed '$d' >"$SELECTION"

# run_query <label> <query-file> [gh -F args...]
run_query() {
	local label="$1" qfile="$2"
	shift 2
	local resp="$OUT/s4-$label.json" t0 t1 rc=0
	t0="$(now_ms)"
	gh api graphql -F query=@"$qfile" "$@" >"$resp" 2>"$resp.err" || rc=$?
	t1="$(now_ms)"
	local rl errs
	rl="$(jq -c '.data.rateLimit // empty' "$resp" 2>/dev/null || true)"
	errs="$(jq -c '[.errors[]?.message] | .[0:3]' "$resp" 2>/dev/null || echo '[]')"
	log "label=$label rc=$rc latency_ms=$((t1 - t0)) bytes=$(wc -c <"$resp" | tr -d ' ') rateLimit=$rl errors=$errs stderr=$(head -c 300 "$resp.err" | tr '\n' ' ')"
}

dryrun_copy() { sed 's/rateLimit {/rateLimit(dryRun: true) {/' "$1" >"$2"; }

log "=== S4 start repo=$REPO single_pr=$SINGLE_PR gh=$(gh --version | head -1)"

# (a) single PR: the real query file, dryRun copy and real.
DRY1="$OUT/s4-pr-dryrun.graphql"
dryrun_copy "$QUERY_SRC" "$DRY1"
run_query "1pr-dryrun" "$DRY1" -F owner="$OWNER" -F repo="$NAME" -F number="$SINGLE_PR"
run_query "1pr-real" "$QUERY_SRC" -F owner="$OWNER" -F repo="$NAME" -F number="$SINGLE_PR"

# (b) aliased batches: pr0..prN-1 each with the same selection.
# (bash 3.2 compatible: no mapfile on macOS /bin/bash)
NUMBERS=()
while IFS= read -r num; do
	[ -n "$num" ] && NUMBERS+=("$num")
done < <(gh pr list -R "$REPO" --state all --limit 20 --json number --jq '.[].number')
log "batch pr numbers: ${NUMBERS[*]}"

build_batch() {
	local n="$1" file="$2" i
	{
		echo "query PRBatch {"
		echo "  rateLimit { cost limit remaining used resetAt nodeCount }"
		for ((i = 0; i < n; i++)); do
			echo "  pr$i: repository(owner: \"$OWNER\", name: \"$NAME\") {"
			echo "    pullRequest(number: ${NUMBERS[$i]}) {"
			cat "$SELECTION"
			echo "    }"
			echo "  }"
		done
		echo "}"
	} >"$file"
}

for n in 5 20; do
	if [ "${#NUMBERS[@]}" -lt "$n" ]; then
		log "skip batch $n: only ${#NUMBERS[@]} PRs in $REPO"
		continue
	fi
	q="$OUT/s4-batch$n.graphql"
	build_batch "$n" "$q"
	dryrun_copy "$q" "$OUT/s4-batch$n-dryrun.graphql"
	run_query "${n}pr-dryrun" "$OUT/s4-batch$n-dryrun.graphql"
	run_query "${n}pr-real" "$q"
done

log "=== S4 done"
