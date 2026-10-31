#!/usr/bin/env bash
# spikes/run-interactive.sh - human-driven herdr spikes S0, S1, S7, S2a, S2b, S6, S3, S5.
#
# Run by a HUMAN in a plain terminal that is NOT inside any herdr pane.
# Every herdr call goes through `hr` (spikes/lib.sh), which only ever talks
# to the scratch session socket ~/.config/herdr/sessions/prspike/herdr.sock.
# Every prompt payload starts with "# " so it is a comment if it lands in a shell.
# This script never stops or deletes a session; it only tells you when to.
set -u

SPIKES_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SPIKES_DIR/.." && pwd)"
# shellcheck source=spikes/lib.sh
source "$SPIKES_DIR/lib.sh"

if [ -n "$SPIKE_CALLER_PANE_ID" ] || [ -n "$SPIKE_CALLER_SESSION" ]; then
	echo "REFUSING: this shell is inside a herdr pane (HERDR_PANE_ID='$SPIKE_CALLER_PANE_ID' HERDR_SESSION='$SPIKE_CALLER_SESSION')." >&2
	echo "Open a plain terminal window that is not inside herdr and run this script there." >&2
	exit 96
fi
command -v herdr >/dev/null || { echo "herdr not on PATH" >&2; exit 1; }
guard_socket

# ---------- helpers ----------

pause() {
	printf '\n>>> %s\n    [press Enter when ready] ' "$*"
	read -r _
}

# ask <step> <question> <allowed answers, space separated>
ask() {
	local step="$1" q="$2" allowed="$3" ans
	while :; do
		printf '\n??? %s [%s] ' "$q" "$allowed"
		read -r ans
		for a in $allowed; do
			if [ "$ans" = "$a" ]; then
				log "$step" "ANSWER q=\"$q\" a=$ans"
				return 0
			fi
		done
		echo "    please answer one of: $allowed"
	done
}

# note <step>: free-text observation from the human (optional).
note() {
	local step="$1" text
	printf '    optional note (Enter to skip): '
	read -r text
	[ -n "$text" ] && log "$step" "NOTE $text"
	return 0
}

# run <step> <label> hr-args...: run one guarded herdr call, log output, rc, latency.
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
	printf '    [%s] rc=%s (%sms) %s\n' "$label" "$rc" "$((t1 - t0))" "$(printf '%s' "$out" | head -c 600)"
	return 0
}

# multiline <n> <tag>: n lines, every one a shell comment.
multiline() {
	local n="$1" tag="$2" i s=""
	for ((i = 1; i <= n; i++)); do
		[ "$i" -gt 1 ] && s+=$'\n'
		s+="# pr-relay spike: $tag line $i of $n"
	done
	printf '%s' "$s"
}

# sized <bytes> <tag>: single-line comment payload of exactly <bytes> bytes.
sized() {
	local bytes="$1" tag="$2" head pad
	head="# pr-relay spike: $tag ${bytes}B "
	pad=$((bytes - ${#head}))
	printf '%s' "$head"
	perl -e 'print "x" x $ARGV[0]' "$pad"
}

# ---------- setup ----------

cat <<EOF
pr-relay herdr spikes. Socket: $HERDR_SOCKET_PATH
Logs: $SPIKES_OUT/<step>.log

You need, inside the prspike session:
  pane A: a scratch agent (fresh Claude Code / Codex in an empty temp dir)
  pane B: a bare shell
EOF

# ---------- S0 ----------
echo; echo "=== S0: session + agent listing"
log s0 "socket=$HERDR_SOCKET_PATH herdr=$(command herdr --version 2>&1)"
run s0 "session list" session list
run s0 "agent list" agent list
ask s0 "Does the agent list above show ONLY your prspike panes (not your default-session agents)?" "y n"
if tail -1 "$SPIKES_OUT/s0.log" | grep -q 'a=n$'; then
	echo "ABORTING: listing does not look like prspike." >&2
	exit 95
fi

printf '\nPane id of scratch AGENT (pane A): '
read -r AGENT
guard_target "$AGENT"
printf 'Pane id of bare SHELL (pane B): '
read -r SHELLP
guard_target "$SHELLP"
log s0 "agent_pane=$AGENT shell_pane=$SHELLP"
run s0 "agent get A" agent get "$AGENT"
run s0 "agent get B" agent get "$SHELLP"

# ---------- S1: typing collision ----------
echo; echo "=== S1: prompt vs half-typed draft"
for target in "$AGENT" "$SHELLP"; do
	guard_target "$target"
	pause "In pane $target type: hello   (do NOT press Enter there). Then come back here."
	run s1 "prompt over draft ($target)" agent prompt "$target" "$(payload "S1 draft collision on $target")"
	ask s1 "Pane $target: what happened? c=concatenated with draft, s=submitted draft first, r=replaced draft, x=rejected" "c s r x"
	note s1
	pause "Clear any leftover input in pane $target (Ctrl-C / Ctrl-U), wait for the agent to finish."
done

# ---------- S7: prompt after done, before focus ----------
echo; echo "=== S7: prompt after a completed turn, pane not focused"
guard_target "$AGENT"
pause "Let the agent in $AGENT finish its turn. Keep THIS terminal focused; do not click into $AGENT."
run s7 "status before" agent get "$AGENT"
run s7 "prompt after done" agent prompt "$AGENT" "$(payload "S7 after done, unfocused")"
ask s7 "Was the S7 prompt accepted and submitted in $AGENT?" "y n"
note s7

# ---------- S2a: agent.wait while blocked ----------
echo; echo "=== S2a: agent wait across a blocked (permission) state"
guard_target "$AGENT"
pause "In $AGENT, make the agent ask for a permission (e.g. ask it to run 'ls /tmp' as a shell command). Leave the permission prompt OPEN."
run s2a "status while blocked" agent get "$AGENT"
WAIT_OUT="$SPIKES_OUT/s2a-bgwait.out"
: >"$WAIT_OUT"
(
	t0="$(now_ms)"
	out="$(hr agent wait "$AGENT" --until idle --until done --timeout 60000 2>&1)"
	rc=$?
	t1="$(now_ms)"
	printf 'RC=%s elapsed_ms=%s OUT=%s\n' "$rc" "$((t1 - t0))" "$(printf '%s' "$out" | tr '\n' ' ')" >"$WAIT_OUT"
) &
BG=$!
log s2a "started background: herdr agent wait $AGENT --until idle --until done --timeout 60000 at $(now_ms)"
pause "Background wait is running. Wait ~10 s, then APPROVE the permission prompt in $AGENT. Press Enter after approving."
log s2a "human approved at $(now_ms)"
echo "    waiting for background wait to return (max 60 s)..."
wait "$BG"
log s2a "BGWAIT $(cat "$WAIT_OUT")"
echo "    $(cat "$WAIT_OUT" | head -c 600)"
ask s2a "Did the wait return only after the agent finished (not at the moment it was blocked)?" "y n"

pause "Make the agent ask for a permission AGAIN and leave it OPEN (status blocked)."
run s2a "wait while blocked (default until, 5s)" agent wait "$AGENT" --timeout 5000
ask s2a "Did that wait return immediately with blocked?" "y n"

# ---------- S2b: prompt --wait semantics (blocked first, agent still blocked) ----------
echo; echo "=== S2b: prompt --wait on blocked, idle agent and bare shell"
guard_target "$AGENT"
run s2b "prompt --wait on BLOCKED agent" agent prompt "$AGENT" "$(payload "S2b blocked")" --wait --until working --timeout 15000
pause "Now DENY/close the permission prompt in $AGENT and let it become idle/done."
run s2b "status idle" agent get "$AGENT"
run s2b "prompt --wait on IDLE agent" agent prompt "$AGENT" "$(payload "S2b idle")" --wait --until working --timeout 15000
guard_target "$SHELLP"
run s2b "prompt --wait on bare SHELL" agent prompt "$SHELLP" "$(payload "S2b shell")" --wait --until working --timeout 15000
note s2b

# ---------- S6: multi-line and size ----------
echo; echo "=== S6: multi-line and payload size"
for target in "$AGENT" "$SHELLP"; do
	guard_target "$target"
	pause "Make sure $target is idle with an empty input line."
	run s6 "2-line to $target" agent prompt "$target" "$(multiline 2 "S6")"
	ask s6 "$target after 2-line: 1=one submission, n=split submissions, x=rejected" "1 n x"
	pause "Let $target settle (agent finishes its turn)."
	run s6 "30-line to $target" agent prompt "$target" "$(multiline 30 "S6")"
	ask s6 "$target after 30-line: 1=one submission, n=split submissions, x=rejected" "1 n x"
	for size in 900 1100 4096 16384; do
		pause "Let $target settle before the ${size}-byte payload."
		run s6 "${size}B to $target" agent prompt "$target" "$(sized "$size" "S6")"
		ask s6 "$target after ${size}B: 1=one submission, n=split submissions, x=rejected" "1 n x"
	done
	note s6
done

# ---------- S3: identity across restart ----------
echo; echo "=== S3: pane_id / terminal_id / agent_session across session restart"
run s3 "agent list BEFORE" agent list
echo
echo ">>> In your herdr terminal (the one attached to prspike), YOU run:"
echo "      herdr session stop prspike"
echo "    then re-attach:"
echo "      herdr session attach prspike"
echo "    Restart the agent in pane A with native restore if offered."
pause "Press Enter once prspike is attached again and the agent is running."
guard_socket
run s3 "agent list AFTER" agent list
note s3

# ---------- S5: setsid survival ----------
echo; echo "=== S5: does a setsid child survive the session stop?"
HB="$SPIKES_OUT/setsid-heartbeat.txt"
cat <<EOF

>>> In a prspike pane (e.g. pane B), YOU run:
      cd $REPO_ROOT && go build -o spikes/setsid-probe/setsid-probe ./spikes/setsid-probe && spikes/setsid-probe/setsid-probe
EOF
pause "Press Enter after it prints the child pid and a few seconds have passed."
log s5 "BEFORE $(tail -2 "$HB" 2>/dev/null | tr '\n' ' ')"
echo "    heartbeat: $(tail -1 "$HB" 2>/dev/null)"
echo
echo ">>> Now YOU run, in your herdr terminal:"
echo "      herdr session stop prspike"
echo "    then wait 20 seconds."
pause "Press Enter after the 20 seconds."
log s5 "STOPPED-AT approx $(date +%s)"
sleep 3
log s5 "AFTER $(tail -2 "$HB" 2>/dev/null | tr '\n' ' ')"
log s5 "PS $(ps -o pid,ppid,sess,command -ax 2>/dev/null | grep '[s]etsid-probe child' | tr '\n' ' ')"
echo "    heartbeat now: $(tail -1 "$HB" 2>/dev/null)"
ask s5 "Is the heartbeat timestamp still advancing (newer than the stop time)?" "y n"
note s5

echo
echo "=== Done. Logs are in $SPIKES_OUT"
echo "Optional cleanup (run it yourself; this script never does):"
echo "  herdr session stop prspike && herdr session delete prspike"
echo "  pkill -f 'setsid-probe child'   # or let it exit on its own after 15 minutes"
