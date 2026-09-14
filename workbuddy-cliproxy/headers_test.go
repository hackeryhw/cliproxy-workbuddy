package main

import (
	"net/http"
	"testing"
)

// mustCredentialForHeaders builds a credential whose domain is the WorkBuddy
// app issuer, the case that produced the 11128 rejection.
func mustCredentialForHeaders(t *testing.T) *storedAuth {
	t.Helper()
	sa, err := parseStored([]byte(`{"type":"workbuddy",` +
		`"account":{"uid":"uid-1","enterpriseId":"","nickname":"n"},` +
		`"auth":{"accessToken":"tok","refreshToken":"ref",` +
		`"expiresAt":4102444800,"domain":"www.workbuddy.cn"}}`))
	if err != nil {
		t.Fatalf("parseStored: %v", err)
	}
	return sa
}

// The gateway fingerprints the calling channel from these headers. Their absence
// is what produced HTTP 400 code=11128 "unapproved channel", so they are a
// contract, not decoration.
func TestBackendHeadersDeclareChannelIdentity(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, endpointChat, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	backendHeaders(req, mustCredentialForHeaders(t))

	required := map[string]string{
		"X-IDE-Name":     "WorkBuddy",
		"X-IDE-Type":     "WorkBuddy",
		"X-IDE-Version":  clientVersion,
		"X-Agent-Intent": "craft",
		"X-Agent-Type":   "main",
		"X-Product":      "SaaS",
	}
	for name, want := range required {
		if got := req.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	// Correlation ids must be present and non-empty. Their value is arbitrary,
	// so only presence is asserted.
	for _, name := range []string{
		"X-Request-ID",
		"X-Conversation-ID",
		"X-Conversation-Message-ID",
		"X-Conversation-Request-ID",
	} {
		if req.Header.Get(name) == "" {
			t.Errorf("%s is missing; the gateway rejects requests without it", name)
		}
	}

	if got := req.Header.Get("x-codebuddy-request"); got != "1" {
		t.Errorf("x-codebuddy-request = %q, want \"1\"", got)
	}
}

// The credential's own issuer domain is passed through untouched: the official
// desktop client sends www.workbuddy.cn for app-signed-in accounts, and pinning
// it to copilot.tencent.com would misreport the token's realm.
func TestBackendHeadersPassThroughIssuerDomain(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, endpointChat, nil)
	backendHeaders(req, mustCredentialForHeaders(t))

	if got := req.Header.Get("X-Domain"); got != "www.workbuddy.cn" {
		t.Errorf("X-Domain = %q, want the credential's own issuer domain", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q", got)
	}
	if got := req.Header.Get("X-User-Id"); got != "uid-1" {
		t.Errorf("X-User-Id = %q", got)
	}
}

// Each request must carry its own correlation id; a reused id would let the
// gateway collapse unrelated calls (or flag the replay).
func TestBackendHeadersUseFreshCorrelationIDs(t *testing.T) {
	first, _ := http.NewRequest(http.MethodPost, endpointChat, nil)
	backendHeaders(first, mustCredentialForHeaders(t))
	second, _ := http.NewRequest(http.MethodPost, endpointChat, nil)
	backendHeaders(second, mustCredentialForHeaders(t))

	if first.Header.Get("X-Request-ID") == second.Header.Get("X-Request-ID") {
		t.Error("two requests share an X-Request-ID; ids must be generated per request")
	}
}
