#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CONFIG="$ROOT/config.yaml"
HOST="${1:-127.0.0.1}"
KEY="$(awk '/^api-keys:/{getline; gsub(/[-" ]/, ""); print}' "$CONFIG")"

curl -sS "http://${HOST}:8317/v1/chat/completions" \
  -H "Authorization: Bearer ${KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini-3.1-pro-low","messages":[{"role":"user","content":"Reply with the single word pong."}],"max_tokens":16}'
echo
