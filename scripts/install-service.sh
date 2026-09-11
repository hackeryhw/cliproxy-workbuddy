#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LABEL="local.cliproxyapi"
PLIST_SRC="$ROOT/scripts/local.cliproxyapi.plist"
PLIST_DST="$HOME/Library/LaunchAgents/${LABEL}.plist"
UID_NUM="$(id -u)"
DOMAIN="gui/${UID_NUM}"

if [[ ! -x "$ROOT/bin/cli-proxy-api" ]]; then
  echo "Missing $ROOT/bin/cli-proxy-api — run scripts/download.sh first." >&2
  exit 1
fi
if [[ ! -f "$ROOT/config.yaml" ]]; then
  echo "Missing $ROOT/config.yaml" >&2
  exit 1
fi

mkdir -p "$HOME/.cli-proxy-api/logs"
mkdir -p "$HOME/Library/LaunchAgents"
cp "$PLIST_SRC" "$PLIST_DST"

# Stop a leftover foreground instance so launchd can bind :8317
if PIDS="$(lsof -nP -tiTCP:8317 -sTCP:LISTEN 2>/dev/null || true)"; then
  if [[ -n "$PIDS" ]]; then
    echo "Stopping existing process on :8317 ($PIDS)"
    # shellcheck disable=SC2086
    kill $PIDS 2>/dev/null || true
    sleep 1
  fi
fi

launchctl bootout "$DOMAIN" "$PLIST_DST" 2>/dev/null || true
launchctl bootstrap "$DOMAIN" "$PLIST_DST"
launchctl enable "$DOMAIN/${LABEL}"
launchctl kickstart -k "$DOMAIN/${LABEL}"

echo "CLIProxyAPI is running in the background as ${LABEL} (port 8317, KeepAlive + login load)."
echo "Logs: $HOME/.cli-proxy-api/logs/cliproxyapi.stdout.log"
