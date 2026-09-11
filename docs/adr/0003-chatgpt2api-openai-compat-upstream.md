# chatgpt2api is an OpenAI-compat upstream on the Proxy

Callers already have one HTTP API and one Caller key on port 8317. ChatGPT-web models from chatgpt2api are reached through that same surface as an `openai-compatibility` upstream (`http://127.0.0.1:8000/v1`), not by pointing Callers at chatgpt2api's own port.

Considered: a second Caller-facing base URL on :8000 with chatgpt2api's auth-key. Rejected because it splits keys, ports, and model lists. chatgpt2api stays Host-local; it must be running for those model aliases to work.
