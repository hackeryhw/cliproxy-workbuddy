#!/usr/bin/env bash
# Undo scripts/cursor-byok.sh. Quit Cursor (Cmd+Q) first.
#
# Default: restore the snapshot taken before apply.
#   scripts/cursor-byok-restore.sh
# If Cursor still will not start, only turn BYOK off:
#   scripts/cursor-byok-restore.sh --safe
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
exec python3 "$ROOT/scripts/cursor-byok.py" restore "$@"
