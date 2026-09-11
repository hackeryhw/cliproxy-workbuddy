#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$ROOT/bin/cli-proxy-api"
CONFIG="$ROOT/config.yaml"

if [[ ! -x "$BIN" ]]; then
  echo "Missing $BIN — run scripts/download.sh first." >&2
  exit 1
fi
if [[ ! -f "$CONFIG" ]]; then
  echo "Missing $CONFIG — copy config.example.yaml to config.yaml and set api-keys." >&2
  exit 1
fi

mkdir -p "$HOME/.cli-proxy-api"
cd "$ROOT"
exec "$BIN" -config "$CONFIG"
