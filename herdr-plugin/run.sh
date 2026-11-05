#!/bin/sh
# herdr runs manifest commands as argv without a shell.
# This is the PATH guard: a missing pr-relay logs one line and exits 0.
if ! command -v pr-relay >/dev/null 2>&1; then
  echo "pr-relay not found on PATH; install via brew install drodimin/tap/pr-relay, go install github.com/drodimin/pr-relay/cmd/pr-relay@latest, or a release download" >&2
  exit 0
fi
exec pr-relay "$@"
