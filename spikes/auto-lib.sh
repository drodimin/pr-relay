# shellcheck shell=bash
# spikes/auto-lib.sh - helpers for driving the herdr spikes without a human at
# the keyboard. Source it; every herdr call still goes through lib.sh's guarded
# `hr` (prspike socket only), and every pane is checked with guard_target.
#
# Human keyboard actions from run-interactive.sh are replaced by herdr itself:
#   typing a draft      -> hr pane send-text <pane> <text>   (no Enter)
#   approve / deny      -> hr pane send-keys <pane> <keys>
#   "what happened?"    -> hr pane read <pane>, logged as OBS and judged from it

# shellcheck source=lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

A="${SPIKE_PANE_A:-w1:p1}"
B="${SPIKE_PANE_B:-w1:p2}"

# run <step> <label> hr-args...: one guarded herdr call; log rc, latency, output.
run() {
	local step="$1" label="$2" out rc t0 t1
	shift 2
	t0="$(now_ms)"
	out="$(hr "$@" 2>&1)"
	rc=$?
	t1="$(now_ms)"
	log "$step" "CALL $label: herdr $(printf '%q ' "$@" | cut -c1-200)"
	log "$step" "RC=$rc latency_ms=$((t1 - t0))"
	log "$step" "OUT $(printf '%s' "$out" | head -c 4000 | tr '\n' ' ')"
	printf '[%s] rc=%s (%sms) %s\n' "$label" "$rc" "$((t1 - t0))" "$(printf '%s' "$out" | head -c 400)"
	return 0
}

# obs <step> <pane> [lines]: log the visible pane text as an observation.
obs() {
	local step="$1" pane="$2" lines="${3:-20}" out
	guard_target "$pane"
	out="$(hr pane read "$pane" --source visible 2>&1 | grep -v '^[[:space:]]*$' | tail -n "$lines")"
	log "$step" "OBS $pane: $(printf '%s' "$out" | tr '\n' '|' | head -c 3000)"
	printf '%s\n' "$out"
}

# status <pane>: print agent_status of a pane (or "n/a").
status() {
	hr agent get "$1" 2>/dev/null | grep -o '"agent_status":"[a-z_]*"' | cut -d'"' -f4 || echo n/a
}

# wait_status <pane> <timeout_s> <status...>: poll until agent_status is one of them.
wait_status() {
	local pane="$1" t="$2" s i
	shift 2
	for ((i = 0; i < t * 2; i++)); do
		s="$(status "$pane")"
		for want in "$@"; do [ "$s" = "$want" ] && { echo "$s"; return 0; }; done
		sleep 0.5
	done
	echo "TIMEOUT(last=$s)"
	return 1
}

# answer <step> <question> <answer> <evidence>: record a judged answer.
answer() {
	log "$1" "ANSWER(auto) q=\"$2\" a=$3 evidence=\"$4\""
	printf 'ANSWER %s: %s -> %s\n' "$1" "$2" "$3"
}

multiline() {
	local n="$1" tag="$2" i s=""
	for ((i = 1; i <= n; i++)); do
		[ "$i" -gt 1 ] && s+=$'\n'
		s+="# pr-relay spike: $tag line $i of $n"
	done
	printf '%s' "$s"
}

sized() {
	local bytes="$1" tag="$2" head pad
	head="# pr-relay spike: $tag ${bytes}B "
	pad=$((bytes - ${#head}))
	printf '%s' "$head"
	perl -e 'print "x" x $ARGV[0]' "$pad"
}
