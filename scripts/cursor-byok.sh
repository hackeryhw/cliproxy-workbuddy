#!/usr/bin/env bash
# Point Cursor at this Host's HTTP API. Quit Cursor (Cmd+Q) first.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
exec python3 "$ROOT/scripts/cursor-byok.py" apply
