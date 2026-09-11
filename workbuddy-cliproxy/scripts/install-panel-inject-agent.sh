#!/usr/bin/env bash
# Install a LaunchAgent that keeps the WorkBuddy panel integration applied.
#
# Runs once at login and then every 60s; the worker exits immediately when the
# block is already up to date, so the steady-state cost is one node start per
# minute (a few milliseconds of CPU).
set -euo pipefail

SOURCE="$(cd "$(dirname "$0")/.." && pwd)"
LABEL="local.workbuddy-panel-inject"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
NODE="$(command -v node)"

# The worker must live on the internal disk: launchd cannot execute scripts from
# an external volume (and that volume may not even be mounted at login).
INSTALL="$HOME/Library/Application Support/workbuddy-panel"

if [ -z "$NODE" ]; then
  echo "node not found on PATH" >&2
  exit 1
fi

mkdir -p "$HOME/Library/LaunchAgents" "$HOME/.cli-proxy-api/logs" \
         "$INSTALL/scripts" "$INSTALL/panel"

cp "$SOURCE/scripts/panel-inject-watch.sh" "$INSTALL/scripts/"
cp "$SOURCE/scripts/inject-panel.mjs"      "$INSTALL/scripts/"
cp "$SOURCE/panel/workbuddy-panel.js"      "$INSTALL/panel/"
chmod +x "$INSTALL/scripts/panel-inject-watch.sh"

ROOT="$INSTALL"

cat >"$PLIST" <<PLISTEOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>$LABEL</string>
	<key>ProgramArguments</key>
	<array>
		<string>$ROOT/scripts/panel-inject-watch.sh</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>$(dirname "$NODE"):/usr/bin:/bin:/usr/sbin:/sbin</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>StartInterval</key>
	<integer>60</integer>
	<key>StandardOutPath</key>
	<string>$HOME/.cli-proxy-api/logs/workbuddy-panel-inject.stdout.log</string>
	<key>StandardErrorPath</key>
	<string>$HOME/.cli-proxy-api/logs/workbuddy-panel-inject.stderr.log</string>
</dict>
</plist>
PLISTEOF

launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$PLIST"
launchctl kickstart -k "gui/$(id -u)/$LABEL"

echo "installed : $PLIST"
echo "payload   : $ROOT  (copied off the external volume)"
echo "worker    : $ROOT/scripts/panel-inject-watch.sh"
echo "log       : $HOME/.cli-proxy-api/logs/workbuddy-panel-inject.log"
echo "status    :"
launchctl print "gui/$(id -u)/$LABEL" 2>/dev/null | grep -E "^\s+(state|pid|runs) " | sed 's/^/  /' || true
