# Gemini API Proxy

This context is the local CLIProxyAPI deployment that turns Host-owned Gemini subscriptions, and optional OpenAI-compat upstreams, into HTTP APIs for Callers on the LAN. It is not a fork of CLIProxyAPI.

## Language

**Proxy**:
The CLIProxyAPI process running on the host Mac. It adapts Gemini subscriptions and OpenAI-compat upstreams into OpenAI/Gemini/Claude-compatible HTTP APIs.
_Avoid_: reverse proxy, 反代理

**Host**:
The Mac that runs the Proxy.
_Avoid_: server, machine, box

**Account**:
One Google identity with a Gemini Pro subscription. This deployment has two Accounts. Each Account is one Provider credential. Callers do not pick an Account.
_Avoid_: user, 账号 (unqualified), API key

**Gemini subscription**:
Gemini Pro on an Account, used via Google OAuth (Antigravity), not an AI Studio key.
_Avoid_: API key, Gemini API key, Vertex

**Provider credential**:
The OAuth tokens written to `auth-dir` after that Account's Google login. The Proxy uses these to call Gemini.
_Avoid_: API key, token file (as the domain name)

**OpenAI-compat upstream**:
An OpenAI-compatible HTTP API the Proxy can route to by model alias. Distinct from an Account. The first one is chatgpt2api on the Host.
_Avoid_: provider (unqualified), Account, chatgpt2api (as the Caller-facing product)

**Routing**:
How the Proxy chooses among Accounts for a request. This deployment uses fill-first: exhaust one Account, then the other. Failover when quota is gone. Not a Caller-facing switch. Model aliases for an OpenAI-compat upstream are a separate path; they do not pick a Gemini Account.
_Avoid_: 切换, load balancing (as the name for fill-first), pinning

**Operator**:
The person at the Host who starts the Proxy and owns the Accounts. The Proxy only needs to run while the Operator is present.
_Avoid_: user, admin

**HTTP API**:
The endpoints the Proxy serves. We present the OpenAI-compatible dialect first (`/v1/chat/completions`); Gemini-native exists but is not the primary surface.
_Avoid_: Web UI, Cursor integration, Gemini-native (as the default), 反代理出的接口 (as the product name)

**Caller**:
Anything on the LAN that sends HTTP requests to the HTTP API. The first Callers are raw HTTP clients (curl, SDKs), not a specific app.
_Avoid_: user, client, consumer

**Caller key**:
A secret in the Proxy config that a Caller must send (Bearer) to be accepted. Distinct from the Gemini subscription and from an OpenAI-compat upstream's own key.
_Avoid_: API key (unqualified), password, token

**This repository**:
Config and run files for operating the Proxy. Upstream CLIProxyAPI is consumed as a Host binary, not vendored.
_Avoid_: fork, source tree, Docker image (as the run form)
