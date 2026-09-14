#!/usr/bin/env bash
# Build stealth plugin and deploy into local CLIProxyAPI plugins directory.
set -euo pipefail

# Source dir is stealth-plugin/ itself; the CPA root is its parent.
SRC="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SRC/.." && pwd)"

if [ -n "${CPA_PLUGINS:-}" ]; then
  PLUGIN_DIR="$CPA_PLUGINS"
elif [ -d "$ROOT/plugins" ]; then
  PLUGIN_DIR="$ROOT/plugins"
else
  echo "plugins dir not found under $ROOT" >&2
  echo "set CPA_PLUGINS to the CPA plugins directory" >&2
  exit 1
fi

cd "$SRC"
echo "==> Building stealth.dylib..."
CGO_ENABLED=1 go build -buildmode=c-shared -o stealth.dylib .
cp stealth.dylib "$PLUGIN_DIR/stealth.dylib"
chmod 700 "$PLUGIN_DIR/stealth.dylib"
echo "==> Installed to $PLUGIN_DIR/stealth.dylib"

LABEL="gui/$(id -u)/local.cliproxyapi"
if launchctl print "$LABEL" >/dev/null 2>&1; then
  launchctl kickstart -k "$LABEL"
  echo "==> Restarted local.cliproxyapi"
fi
echo "==> Done!"
