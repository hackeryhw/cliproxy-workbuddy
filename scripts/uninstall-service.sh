#!/usr/bin/env bash
set -euo pipefail

LABEL="local.cliproxyapi"
PLIST_DST="$HOME/Library/LaunchAgents/${LABEL}.plist"
UID_NUM="$(id -u)"
DOMAIN="gui/${UID_NUM}"

launchctl bootout "$DOMAIN" "$PLIST_DST" 2>/dev/null || true
echo "Stopped background service ${LABEL}."
