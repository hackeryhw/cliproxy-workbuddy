#!/usr/bin/env bash
# 以「全部流量强制走 Burp」的方式启动 WorkBuddy
#
# 双层覆盖：
#   1. Electron/Chromium 渲染层  -> --proxy-server
#   2. Node.js 后台守护进程      -> HTTP_PROXY / HTTPS_PROXY
#   3. Node.js 不信任系统钥匙串  -> NODE_EXTRA_CA_CERTS 指向 Burp CA
set -euo pipefail

BURP_HOST="127.0.0.1"
BURP_PORT="8080"
PROXY="http://${BURP_HOST}:${BURP_PORT}"
CA_CERT="/Volumes/MyPassport/Projects/ProxyCLI/PortSwigger_CA.crt"
APP="/Applications/WorkBuddy.app/Contents/MacOS/Electron"

if [[ ! -x "$APP" ]]; then
  echo "找不到 WorkBuddy 可执行文件: $APP" >&2
  exit 1
fi

echo "代理   : $PROXY"
echo "CA 证书: $CA_CERT"

export HTTP_PROXY="$PROXY"
export HTTPS_PROXY="$PROXY"
export http_proxy="$PROXY"
export https_proxy="$PROXY"
export ALL_PROXY="$PROXY"
export NO_PROXY="localhost,127.0.0.1"
export no_proxy="localhost,127.0.0.1"
export NODE_EXTRA_CA_CERTS="$CA_CERT"

exec "$APP" \
  --proxy-server="$PROXY" \
  --proxy-bypass-list="localhost;127.0.0.1" \
  --ignore-certificate-errors
