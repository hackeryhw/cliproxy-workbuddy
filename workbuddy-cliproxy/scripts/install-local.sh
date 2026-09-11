#!/usr/bin/env bash
# Build the WorkBuddy plugin and drop it into the local CLIProxyAPI plugins dir.
#
# Layout this script expects (both on the internal disk):
#   <cpa-root>/plugins/workbuddy.dylib
#   <cpa-root>/workbuddy-cliproxy/   ← this repo
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if [ -n "${CPA_PLUGINS:-}" ]; then
  PLUGIN_DIR="$CPA_PLUGINS"
elif [ -d "$ROOT/../plugins" ]; then
  PLUGIN_DIR="$(cd "$ROOT/../plugins" && pwd)"
else
  PLUGIN_DIR="$HOME/Projects/Gemini API/plugins"
fi

if [ ! -d "$PLUGIN_DIR" ]; then
  echo "plugins dir not found: $PLUGIN_DIR" >&2
  echo "set CPA_PLUGINS or nest this repo next to the CPA plugins/ directory" >&2
  exit 1
fi

cd "$ROOT"
CGO_ENABLED=1 go build -buildmode=c-shared -o workbuddy.dylib .
cp workbuddy.dylib "$PLUGIN_DIR/workbuddy.dylib"
chmod 700 "$PLUGIN_DIR/workbuddy.dylib"
echo "installed $PLUGIN_DIR/workbuddy.dylib"

LABEL="gui/$(id -u)/local.cliproxyapi"
if launchctl print "$LABEL" >/dev/null 2>&1; then
  launchctl kickstart -k "$LABEL"
  echo "restarted local.cliproxyapi"
fi
