#!/bin/sh
# herdr runs manifest commands as argv without a shell.
# Leading options:
#   --open PANE_ID  open this plugin's popup pane PANE_ID (manifest [[panes]] id)
#   --hold          run pr-relay, then wait for Enter so the popup stays readable
# Otherwise this is the PATH guard: a missing pr-relay logs one line and exits 0.
missing_msg="pr-relay not found on PATH; install via go install github.com/drodimin/pr-relay/cmd/pr-relay@latest, or a release download"

case "$1" in
--open)
  pane_id="$2"
  herdr_bin="${HERDR_BIN_PATH:-herdr}"
  exec "$herdr_bin" plugin pane open --plugin pr-relay --entrypoint "$pane_id"
  ;;
--hold)
  shift
  if command -v pr-relay >/dev/null 2>&1; then
    pr-relay "$@"
  else
    echo "$missing_msg" >&2
  fi
  echo "press Enter to close"
  read _ || true
  exit 0
  ;;
esac

if ! command -v pr-relay >/dev/null 2>&1; then
  echo "$missing_msg" >&2
  exit 0
fi
exec pr-relay "$@"
