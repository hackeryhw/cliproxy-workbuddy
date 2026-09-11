#!/usr/bin/env bash
# Download the pinned CLIProxyAPI binary. WorkBuddy 0.3.2 was verified only
# against 7.2.149 (commit 2a6b87ac). Bumping this without retesting the plugin
# will break login, auth files, and the management panel.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$ROOT/bin/cli-proxy-api"

PINNED_VERSION="v7.2.149"
PINNED_COMMIT="2a6b87ac"
PINNED_BUILT_AT="2026-09-03T13:31:22Z"
VERSION="${CLIPROXY_VERSION:-$PINNED_VERSION}"

if [[ "$VERSION" != "$PINNED_VERSION" && "${ALLOW_UNPINNED_CPA:-}" != "1" ]]; then
  echo "this tree is pinned to CLIProxyAPI $PINNED_VERSION (commit $PINNED_COMMIT)." >&2
  echo "WorkBuddy 0.3.2 is not verified on $VERSION. refuse to download." >&2
  echo "override only after retesting the plugin: ALLOW_UNPINNED_CPA=1 CLIPROXY_VERSION=$VERSION $0" >&2
  exit 1
fi

ARCH="$(uname -m)"
case "$ARCH" in
  arm64|aarch64) ASSET_ARCH="aarch64" ;;
  x86_64) ASSET_ARCH="amd64" ;;
  *)
    echo "Unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

VER_NUM="${VERSION#v}"
URL="https://github.com/router-for-me/CLIProxyAPI/releases/download/${VERSION}/CLIProxyAPI_${VER_NUM}_darwin_${ASSET_ARCH}.tar.gz"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$ROOT/bin"
curl -fsSL "$URL" | tar -xz -C "$TMP"
install -m 0755 "$TMP/cli-proxy-api" "$BIN"
xattr -dr com.apple.quarantine "$BIN" 2>/dev/null || true

banner="$("$BIN" -h 2>&1 | head -1 || true)"
expected="CLIProxyAPI Version: ${PINNED_VERSION#v}, Commit: ${PINNED_COMMIT}, BuiltAt: ${PINNED_BUILT_AT}"
if [[ "$VERSION" == "$PINNED_VERSION" && "$banner" != "$expected" ]]; then
  rm -f "$BIN"
  echo "downloaded binary is not the pinned host:" >&2
  echo "  got:      $banner" >&2
  echo "  expected: $expected" >&2
  exit 1
fi

echo "Installed $BIN ($VERSION, commit $PINNED_COMMIT)"
