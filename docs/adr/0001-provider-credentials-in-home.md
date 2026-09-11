# Provider credentials live in the home directory

This repo holds Proxy config and how to start it; it must not hold Google login state. Provider credentials stay in `~/.cli-proxy-api/` on the Host so OAuth tokens cannot be committed, copied with the repo, or mixed with the Caller key.

Considered: a gitignored folder in this repository. Rejected because tokens would still sit next to config and are easy to copy, zip, or add by mistake.
