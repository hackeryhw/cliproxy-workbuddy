#!/usr/bin/env bash
# Keep the WorkBuddy panel integration applied even after the panel updates.
#
# The control panel's own updater replaces management.html with the official
# build, which drops any injected block. Rather than freezing panel updates
# (remote-management.disable-auto-update-panel), this watcher re-applies the
# integration whenever it detects the block is missing or stale, so the panel
# stays on the official release channel.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
INJECTOR="$ROOT/scripts/inject-panel.mjs"
TARGET="${WORKBUDDY_PANEL_HTML:-/Users/haoweiyang/Projects/Gemini API/static/management.html}"
LOG="${WORKBUDDY_PANEL_LOG:-$HOME/.cli-proxy-api/logs/workbuddy-panel-inject.log}"
NODE="$(command -v node || true)"

log() {
  mkdir -p "$(dirname "$LOG")" 2>/dev/null || true
  printf '[%s] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" >>"$LOG" 2>/dev/null || true
}

if [ -z "$NODE" ]; then
  log "node not found on PATH; nothing to do"
  exit 0
fi
if [ ! -f "$INJECTOR" ] || [ ! -f "$TARGET" ]; then
  log "missing injector ($INJECTOR) or target ($TARGET)"
  exit 0
fi

# Fast path: already injected and current.
if "$NODE" "$INJECTOR" "$TARGET" --check >/dev/null 2>&1; then
  exit 0
fi

if OUT="$("$NODE" "$INJECTOR" "$TARGET" 2>&1)"; then
  log "re-applied panel integration (panel had been updated or reset)"
else
  log "injection failed: $OUT"
fi
