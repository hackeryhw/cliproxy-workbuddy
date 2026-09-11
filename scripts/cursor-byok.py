#!/usr/bin/env python3
"""Point Cursor BYOK at this Host's CLIProxyAPI, or restore the previous keys.

Cursor stores Override OpenAI Base URL / API key in
~/Library/Application Support/Cursor/User/globalStorage/state.vscdb,
not in settings.json. Quit Cursor completely (Cmd+Q) before running this,
or Cursor will overwrite the write on exit.

  python3 scripts/cursor-byok.py apply     # write 8317 BYOK
  python3 scripts/cursor-byok.py restore   # put back the snapshot
  python3 scripts/cursor-byok.py restore --safe  # just turn BYOK off
"""
from __future__ import annotations

import argparse
import json
import os
import sqlite3
import subprocess
import sys
from datetime import datetime, timezone
from hashlib import pbkdf2_hmac
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CONFIG = ROOT / "config.yaml"
DB = Path.home() / "Library/Application Support/Cursor/User/globalStorage/state.vscdb"
BACKUP = (
    Path.home()
    / "Library/Application Support/Cursor/User/globalStorage/cliproxyapi-byok-backup.json"
)
APP_KEY = (
    "src.vs.platform.reactivestorage.browser.reactiveStorageServiceImpl"
    ".persistentStorage.applicationUser"
)
PLAIN_KEY = "cursorAuth/openAIKey"
SECRET_KEY = "secret://cursorAuth/openAIKey"
AZURE_SECRET_KEY = "secret://cursorAuth/azureApiKey"
SNAPSHOT_KEYS = (APP_KEY, PLAIN_KEY, SECRET_KEY, AZURE_SECRET_KEY)
MODELS = [
    "gemini-3.8-flash-high",
    "gemini-3.1-pro-low",
    "gemini-pro-agent",
    "claude-sonnet-4-6",
]
BASE_URL = "http://127.0.0.1:8317/v1"


def caller_key() -> str:
    text = CONFIG.read_text()
    lines = text.splitlines()
    for i, line in enumerate(lines):
        if line.strip() == "api-keys:":
            nxt = lines[i + 1].strip().lstrip("-").strip().strip('"').strip("'")
            if nxt:
                return nxt
    raise SystemExit(f"No api-keys entry in {CONFIG}")


def cursor_running() -> bool:
    r = subprocess.run(
        ["pgrep", "-f", "/Applications/Cursor.app/Contents/MacOS/"],
        capture_output=True,
        text=True,
    )
    return r.returncode == 0 and bool(r.stdout.strip())


def require_cursor_quit() -> None:
    if cursor_running():
        print(
            "Cursor is still running. Quit it fully with Cmd+Q, then run this script again.",
            file=sys.stderr,
        )
        sys.exit(2)
    if not DB.exists():
        raise SystemExit(f"Missing {DB}")


def encrypt_cursor_secret(plaintext: str) -> str | None:
    r = subprocess.run(
        ["security", "find-generic-password", "-s", "Cursor Safe Storage", "-w"],
        capture_output=True,
        text=True,
    )
    if r.returncode != 0 or not r.stdout.strip():
        return None
    master = r.stdout.strip().encode()
    key = pbkdf2_hmac("sha1", master, b"saltysalt", 1003, dklen=16)
    iv = b"\x20" * 16
    data = plaintext.encode("utf-8")
    pad = 16 - (len(data) % 16)
    data = data + bytes([pad]) * pad
    try:
        from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
    except ImportError:
        return None
    encryptor = Cipher(algorithms.AES(key), modes.CBC(iv)).encryptor()
    blob = b"v10" + encryptor.update(data) + encryptor.finalize()
    return json.dumps({"type": "Buffer", "data": list(blob)}, separators=(",", ":"))


def read_keys(cur: sqlite3.Cursor) -> dict[str, str | None]:
    out: dict[str, str | None] = {}
    for key in SNAPSHOT_KEYS:
        row = cur.execute("SELECT value FROM ItemTable WHERE key=?", (key,)).fetchone()
        out[key] = row[0] if row else None
    return out


def write_keys(cur: sqlite3.Cursor, keys: dict[str, str | None]) -> None:
    for key, value in keys.items():
        if value is None:
            cur.execute("DELETE FROM ItemTable WHERE key=?", (key,))
        else:
            cur.execute(
                "INSERT OR REPLACE INTO ItemTable (key, value) VALUES (?, ?)",
                (key, value),
            )


def save_backup(keys: dict[str, str | None]) -> None:
    BACKUP.parent.mkdir(parents=True, exist_ok=True)
    payload = {"saved_at": datetime.now(timezone.utc).isoformat(), "keys": keys}
    BACKUP.write_text(json.dumps(payload))
    os.chmod(BACKUP, 0o600)


def load_backup() -> dict[str, str | None]:
    if not BACKUP.exists():
        raise SystemExit(
            f"No backup at {BACKUP}. Use restore --safe to only turn BYOK off."
        )
    payload = json.loads(BACKUP.read_text())
    keys = payload.get("keys")
    if not isinstance(keys, dict):
        raise SystemExit(f"Backup {BACKUP} is missing keys")
    return {k: keys.get(k) for k in SNAPSHOT_KEYS}


def apply() -> None:
    require_cursor_quit()
    if not CONFIG.exists():
        raise SystemExit(f"Missing {CONFIG}")

    key = caller_key()
    con = sqlite3.connect(str(DB))
    cur = con.cursor()
    existing = read_keys(cur)
    if existing[APP_KEY] is None:
        raise SystemExit("No applicationUser blob in state.vscdb")
    if not BACKUP.exists():
        save_backup(existing)
        print(f"Saved restore snapshot to {BACKUP}")
    else:
        print(f"Keeping existing snapshot at {BACKUP}")

    blob = json.loads(existing[APP_KEY])
    ai = blob.setdefault("aiSettings", {})
    uam = list(ai.get("userAddedModels") or [])
    moe = list(ai.get("modelOverrideEnabled") or [])
    for model in MODELS:
        if model not in uam:
            uam.append(model)
        if model not in moe:
            moe.append(model)
    ai["userAddedModels"] = uam
    ai["modelOverrideEnabled"] = moe
    blob["aiSettings"] = ai
    blob["useOpenAIKey"] = True
    blob["openAIBaseUrl"] = BASE_URL
    azure = blob.get("azureState") or {}
    azure["useAzure"] = False
    base = (azure.get("baseUrl") or "").rstrip("/")
    if base.endswith(":8317/v1") or base.endswith(":8317"):
        azure["baseUrl"] = ""
        azure["deployment"] = ""
    blob["azureState"] = azure

    next_keys = dict(existing)
    next_keys[APP_KEY] = json.dumps(blob, separators=(",", ":"), ensure_ascii=False)
    next_keys[PLAIN_KEY] = key
    secret = encrypt_cursor_secret(key)
    if secret:
        next_keys[SECRET_KEY] = secret
        secret_note = "encrypted secret://cursorAuth/openAIKey written"
    else:
        next_keys[SECRET_KEY] = None
        secret_note = "could not encrypt; removed secret:// cell so plaintext key is used"
    write_keys(cur, next_keys)
    con.commit()
    con.close()
    print("Wrote Cursor BYOK:")
    print("  useOpenAIKey = true")
    print(f"  openAIBaseUrl = {BASE_URL}")
    print(f"  models = {', '.join(MODELS)}")
    print("  Azure override for 8317 cleared")
    print(f"  {secret_note}")
    print("Reopen Cursor, start a new Agent chat, pick gemini-3.8-flash-high.")
    print("If Cursor will not open, quit it and run scripts/cursor-byok-restore.sh")


def restore(*, safe: bool) -> None:
    require_cursor_quit()
    con = sqlite3.connect(str(DB))
    cur = con.cursor()
    if safe:
        existing = read_keys(cur)
        if existing[APP_KEY] is None:
            raise SystemExit("No applicationUser blob in state.vscdb")
        blob = json.loads(existing[APP_KEY])
        blob["useOpenAIKey"] = False
        blob["openAIBaseUrl"] = ""
        azure = blob.get("azureState") or {}
        azure["useAzure"] = False
        azure["baseUrl"] = ""
        azure["deployment"] = ""
        blob["azureState"] = azure
        write_keys(
            cur,
            {
                APP_KEY: json.dumps(blob, separators=(",", ":"), ensure_ascii=False),
            },
        )
        con.commit()
        con.close()
        print("Turned off OpenAI/Azure BYOK (left stored keys in place).")
        print("Reopen Cursor.")
        return

    keys = load_backup()
    write_keys(cur, keys)
    con.commit()
    con.close()
    print(f"Restored Cursor BYOK keys from {BACKUP}")
    print("Reopen Cursor.")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="cmd", required=True)
    sub.add_parser("apply", help="point Cursor at 127.0.0.1:8317")
    restore_p = sub.add_parser("restore", help="undo apply from the snapshot")
    restore_p.add_argument(
        "--safe",
        action="store_true",
        help="do not use the snapshot; only disable BYOK so Cursor can start",
    )
    args = parser.parse_args()
    if args.cmd == "apply":
        apply()
    else:
        restore(safe=args.safe)


if __name__ == "__main__":
    main()
