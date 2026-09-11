# Two Gemini Accounts are a fill-first pool

The Operator has two Gemini Pro Accounts and asked for “switching.” Callers still see one HTTP API and one Caller key. The Proxy logs both Accounts in and routes with `fill-first`: exhaust one Provider credential, then the other, and fail over when quota is gone.

Caller-pinned Accounts (separate Caller keys, ports, or processes) were rejected: CLIProxyAPI has no first-class Gemini OAuth pin, and the need is quota, not identity isolation.
