# shellcheck shell=bash
# spikes/lib.sh - safety guard for the pr-relay herdr spikes (D-16).
#
# Source this file; do not execute it. It pins every herdr call to the
# scratch session "prspike" and refuses to talk to any other socket.
#
# The executor and the user's real agents live in the DEFAULT herdr session,
# and pane ids such as w1:p1 exist in both sessions. The socket path is the
# protection, not the pane id.

SPIKES_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SPIKES_OUT="$SPIKES_DIR/out"
mkdir -p "$SPIKES_OUT"
chmod 700 "$SPIKES_OUT" 2>/dev/null || true

# Remember whether we were started inside a herdr pane before scrubbing it.
SPIKE_CALLER_PANE_ID="${HERDR_PANE_ID:-}"
SPIKE_CALLER_SESSION="${HERDR_SESSION:-}"

PRSPIKE_SOCKET="$HOME/.config/herdr/sessions/prspike/herdr.sock"
DEFAULT_SOCKET="$HOME/.config/herdr/herdr.sock"

# Override anything inherited from a default-session pane.
export HERDR_SOCKET_PATH="$PRSPIKE_SOCKET"
unset HERDR_SESSION HERDR_PANE_ID

# guard_socket: exit 97 unless HERDR_SOCKET_PATH is the prspike socket and it exists.
guard_socket() {
	local sock="${HERDR_SOCKET_PATH:-}"
	case "$sock" in
	*/sessions/prspike/*) ;;
	*)
		echo "GUARD: HERDR_SOCKET_PATH='$sock' is not a prspike socket; refusing" >&2
		exit 97
		;;
	esac
	if [ "$sock" = "$DEFAULT_SOCKET" ]; then
		echo "GUARD: refusing the default session socket" >&2
		exit 97
	fi
	if [ ! -S "$sock" ]; then
		echo "GUARD: socket '$sock' does not exist; start the prspike session first (herdr --session prspike)" >&2
		exit 97
	fi
}

# hr: the only way spike scripts call herdr. Always guarded, always prspike.
hr() {
	guard_socket
	HERDR_SOCKET_PATH="$PRSPIKE_SOCKET" HERDR_SESSION= HERDR_PANE_ID= command herdr "$@"
}

# guard_target <pane>: exit 98 unless <pane> is listed by `herdr agent list` in prspike.
guard_target() {
	local pane="${1:-}"
	guard_socket
	if [ -z "$pane" ]; then
		echo "GUARD: empty pane id" >&2
		exit 98
	fi
	local listing
	if ! listing="$(hr agent list 2>&1)"; then
		echo "GUARD: herdr agent list failed: $listing" >&2
		exit 98
	fi
	local found=""
	if command -v jq >/dev/null 2>&1 && printf '%s' "$listing" | jq -e . >/dev/null 2>&1; then
		# Accept either a bare array or an object wrapping one (result.agents, agents, ...).
		if printf '%s' "$listing" | jq -e --arg p "$pane" \
			'[.. | objects | select(has("pane_id")) | .pane_id] | index($p) != null' >/dev/null 2>&1; then
			found=1
		fi
	else
		# Non-JSON fallback: the pane id must appear as a whole token.
		if printf '%s\n' "$listing" | grep -Eq "(^|[^A-Za-z0-9:])${pane}([^A-Za-z0-9:]|$)"; then
			found=1
		fi
	fi
	if [ -z "$found" ]; then
		echo "GUARD: pane '$pane' not found in prspike agent list; refusing" >&2
		exit 98
	fi
}

# payload <text>: every prompt is a shell comment, harmless if it lands in a shell.
payload() {
	printf '# pr-relay spike: %s' "$*"
}

# log <file> <msg...>: append a timestamped line to spikes/out/<file>.log.
log() {
	local file="$1"
	shift
	printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >>"$SPIKES_OUT/$file.log"
}

# now_ms: wall-clock milliseconds (portable: perl Time::HiRes works on macOS).
now_ms() {
	perl -MTime::HiRes=time -e 'printf "%d\n", time()*1000'
}
