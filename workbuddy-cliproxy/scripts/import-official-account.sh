#!/usr/bin/env bash
# Copy the account currently signed in to the official WorkBuddy app into CPA.
#
# Official desktop writes:
#   ~/Library/Application Support/CodeBuddyExtension/Data/Public/auth/workbuddy-desktop.info
# This script writes:
#   ~/.cli-proxy-api/workbuddy-<uid>.json
#
# Same uid as an existing plugin file is refused (no overwrite, no duplicate).
# Log in a *new* account in WorkBuddy.app, then run this once.
set -euo pipefail

AUTH_DIR="${CLIPROXY_AUTH_DIR:-$HOME/.cli-proxy-api}"
SRC="${WORKBUDDY_DESKTOP_AUTH:-$HOME/Library/Application Support/CodeBuddyExtension/Data/Public/auth/workbuddy-desktop.info}"

if [[ ! -f "$SRC" ]]; then
  echo "official WorkBuddy credential not found:" >&2
  echo "  $SRC" >&2
  echo "open WorkBuddy.app, sign in the new account, then run this again." >&2
  exit 1
fi

python3 - "$SRC" "$AUTH_DIR" <<'PY'
import json, sys, time
from datetime import datetime, timezone
from pathlib import Path

src = Path(sys.argv[1])
auth_dir = Path(sys.argv[2])

raw = json.loads(src.read_text())
acct = raw.get("account") or {}
auth = raw.get("auth") or {}

uid = str(acct.get("uid") or "").strip()
nickname = str(acct.get("nickname") or uid).strip()
enterprise = str(
    acct.get("enterpriseId")
    or (acct.get("sso") or {}).get("domain")
    or ""
).strip()
access = auth.get("accessToken") or ""
refresh = auth.get("refreshToken") or ""
domain = str(auth.get("domain") or "copilot.tencent.com").strip()

if not uid:
    raise SystemExit("official file has no account.uid")
if not access or not refresh:
    raise SystemExit("official file has no accessToken/refreshToken — finish login in WorkBuddy.app first")


def to_unix(v):
    if v is None:
        return 0
    n = int(v)
    if n > 10**12:
        return n // 1000
    return n


expires_at = to_unix(auth.get("expiresAt"))
issued_at = to_unix(auth.get("lastRefreshTime"))
if not issued_at:
    issued_at = int(time.time())
if not expires_at:
    raise SystemExit("official file has no expiresAt")

existing = []
if auth_dir.is_dir():
    for path in sorted(auth_dir.glob("workbuddy*.json")):
        try:
            doc = json.loads(path.read_text())
        except Exception:
            continue
        other = str((doc.get("account") or {}).get("uid") or "").strip()
        if other == uid:
            existing.append(path.name)

if existing:
    print(f"uid {uid} ({nickname}) is already in the plugin:", file=sys.stderr)
    for name in existing:
        print(f"  {auth_dir / name}", file=sys.stderr)
    print("same account is not copied. sign in a different account in WorkBuddy.app.", file=sys.stderr)
    sys.exit(2)

auth_dir.mkdir(parents=True, exist_ok=True)
dest = auth_dir / f"workbuddy-{uid}.json"
document = {
    "type": "workbuddy",
    "disabled": False,
    "account": {
        "uid": uid,
        "enterpriseId": enterprise,
        "nickname": nickname,
    },
    "auth": {
        "accessToken": access,
        "refreshToken": refresh,
        "expiresAt": expires_at,
        "issuedAt": issued_at,
        "domain": domain,
    },
    "expired": datetime.fromtimestamp(expires_at, tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    "last_refresh": datetime.fromtimestamp(issued_at, tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    "refresh_interval_seconds": 604800,
}

tmp = dest.with_suffix(".json.tmp")
tmp.write_text(json.dumps(document, ensure_ascii=False, separators=(",", ":")))
tmp.chmod(0o600)
tmp.replace(dest)
dest.chmod(0o600)

print(f"copied uid {uid}")
print(f"nickname  {nickname}")
print(f"expires   {document['expired']}")
print(f"wrote     {dest}")
PY
