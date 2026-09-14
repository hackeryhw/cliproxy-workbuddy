package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// -----------------------------------------------------------------------------
// Error classification
// -----------------------------------------------------------------------------

func TestUpstreamErrorCarriesStatusCode(t *testing.T) {
	err := &upstreamError{Status: http.StatusTooManyRequests, Body: "slow down"}

	// The host classifies executor failures through this interface. Without it
	// the status reads as 0 and the host never treats the failure as retryable.
	var sc interface{ StatusCode() int }
	if !errors.As(error(err), &sc) {
		t.Fatal("upstreamError does not satisfy the host's statusCode interface")
	}
	if got := sc.StatusCode(); got != http.StatusTooManyRequests {
		t.Errorf("StatusCode() = %d, want %d", got, http.StatusTooManyRequests)
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("Error() = %q, want the status in the message", err.Error())
	}
}

func TestRetryableUpstreamStatus(t *testing.T) {
	retryable := []int{403, 408, 429, 500, 502, 503, 504}
	for _, code := range retryable {
		if !retryableUpstreamStatus(&upstreamError{Status: code}) {
			t.Errorf("status %d should be retryable", code)
		}
	}

	// Request-shaped failures hit every account identically, so rotating would
	// only multiply the load without changing the outcome.
	permanent := []int{400, 401, 404, 422}
	for _, code := range permanent {
		if retryableUpstreamStatus(&upstreamError{Status: code, Body: `{"code":1,"msg":"bad"}`}) {
			t.Errorf("status %d must not trigger a rotation", code)
		}
	}

	if retryableUpstreamStatus(nil) {
		t.Error("nil error must not be retryable")
	}
}

// 11128 "Illegal API invocation from an unapproved channel" arrives as HTTP 400
// but is a verdict about the credential, not the payload: the official client
// succeeds on the same call, so another account may too.
func TestUnapprovedChannelRotatesDespiteStatus400(t *testing.T) {
	body := `{"code":11128,"msg":"Illegal API invocation from an unapproved channel",` +
		`"requestId":"0d3228c8","displayMsg":{"en":"blocked by security policy"}}`
	err := &upstreamError{Status: http.StatusBadRequest, Body: body}

	if !retryableUpstreamStatus(err) {
		t.Error("code 11128 on a 400 must rotate: the refusal is credential-scoped")
	}

	// The same status without that code stays a hard failure.
	if retryableUpstreamStatus(&upstreamError{Status: http.StatusBadRequest, Body: `{"code":40001}`}) {
		t.Error("an ordinary 400 must not rotate")
	}
}

func TestContainsBusinessCode(t *testing.T) {
	if !containsBusinessCode(`{"code":11128,"msg":"x"}`, 11128) {
		t.Error("exact code match not detected")
	}
	if containsBusinessCode(`{"code":111280,"msg":"x"}`, 11128) {
		t.Error("prefix of a longer code must not match")
	}
	if containsBusinessCode(`{"code":"11128"}`, 11128) {
		t.Error("a quoted string code is not the numeric field we match")
	}
	if containsBusinessCode("", 11128) {
		t.Error("empty body must not match")
	}
}

// -----------------------------------------------------------------------------
// Rotator
// -----------------------------------------------------------------------------

// credentialJSONFor builds a credential whose access token identifies the
// account, which is how hostAuthIndex matches a pool entry.
func credentialJSONFor(token string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(
		`{"type":"workbuddy","account":{"uid":"%s","enterpriseId":"","nickname":"%s"},`+
			`"auth":{"accessToken":"%s","refreshToken":"r-%s",`+
			`"expiresAt":4102444800,"domain":"copilot.tencent.com"}}`,
		token, token, token, token))
}

func TestRotatorSkipsTriedAndExcluded(t *testing.T) {
	original := credentialPool
	t.Cleanup(func() { credentialPool = original })

	credentialPool = func() []pluginapi.HostAuthFileEntry {
		return []pluginapi.HostAuthFileEntry{
			{AuthIndex: "idx-1", Provider: providerName, Name: "a.json", Label: "A"},
			{AuthIndex: "idx-2", Provider: providerName, Name: "b.json", Label: "B"},
			{AuthIndex: "idx-3", Provider: providerName, Name: "c.json", Label: "C"},
		}
	}
	originalTransport := hostTransport
	t.Cleanup(func() { hostTransport = originalTransport })
	hostTransport = func(method string, request []byte) ([]byte, error) {
		if method != pluginabi.MethodHostAuthGet {
			return nil, fmt.Errorf("unexpected host call %q", method)
		}
		var req struct {
			AuthIndex string `json:"auth_index"`
		}
		_ = json.Unmarshal(request, &req)
		return okEnvelope(pluginapi.HostAuthGetResponse{
			AuthIndex: req.AuthIndex,
			JSON:      credentialJSONFor(req.AuthIndex),
		})
	}

	rot := newRotator()
	rot.mark("idx-1")

	first, ok := rot.next("idx-1")
	if !ok {
		t.Fatal("next() found nothing although two untried accounts remain")
	}
	if first.Entry.AuthIndex != "idx-2" {
		t.Errorf("first pick = %q, want idx-2", first.Entry.AuthIndex)
	}

	rot.mark("idx-2")
	second, ok := rot.next("idx-2")
	if !ok {
		t.Fatal("next() found nothing although idx-3 remains")
	}
	if second.Entry.AuthIndex != "idx-3" {
		t.Errorf("second pick = %q, want idx-3", second.Entry.AuthIndex)
	}

	rot.mark("idx-3")
	if _, ok := rot.next("idx-3"); ok {
		t.Error("next() offered an account after the pool was exhausted")
	}
}

func TestHostAuthIndexMatchesByAccessToken(t *testing.T) {
	originalTransport := hostTransport
	t.Cleanup(func() { hostTransport = originalTransport })
	hostTransport = func(method string, request []byte) ([]byte, error) {
		if method != pluginabi.MethodHostAuthGet {
			return nil, fmt.Errorf("unexpected host call %q", method)
		}
		var req struct {
			AuthIndex string `json:"auth_index"`
		}
		_ = json.Unmarshal(request, &req)
		return okEnvelope(pluginapi.HostAuthGetResponse{
			AuthIndex: req.AuthIndex,
			JSON:      credentialJSONFor(req.AuthIndex),
		})
	}

	pool := []pluginapi.HostAuthFileEntry{
		{AuthIndex: "idx-1"},
		{AuthIndex: "idx-2"},
		{AuthIndex: "idx-3"},
	}

	sa, err := parseStored(credentialJSONFor("idx-2"))
	if err != nil {
		t.Fatalf("parseStored: %v", err)
	}
	if got := hostAuthIndex(pool, sa); got != "idx-2" {
		t.Errorf("hostAuthIndex = %q, want idx-2", got)
	}

	// A credential that is not in the pool yields no index rather than a wrong
	// one, so the caller falls back to the tried-set alone.
	stranger, err := parseStored(credentialJSONFor("not-in-pool"))
	if err != nil {
		t.Fatalf("parseStored: %v", err)
	}
	if got := hostAuthIndex(pool, stranger); got != "" {
		t.Errorf("hostAuthIndex for a foreign credential = %q, want empty", got)
	}
}

// -----------------------------------------------------------------------------
// Streaming rotation
// -----------------------------------------------------------------------------

// streamHost builds a host stub that records emitted chunks and closes the
// stream, so a test can drive pumpUpstreamStream without a live host.
func streamHost(t *testing.T, pool []pluginapi.HostAuthFileEntry, poolErr error) *[]string {
	t.Helper()
	original := hostTransport
	t.Cleanup(func() { hostTransport = original })

	emitted := make([]string, 0)
	hostTransport = func(method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostAuthList:
			if poolErr != nil {
				return nil, poolErr
			}
			return okEnvelope(authFileListResponse{Files: pool})
		case pluginabi.MethodHostAuthGet:
			var req struct {
				AuthIndex string `json:"auth_index"`
			}
			_ = json.Unmarshal(request, &req)
			return okEnvelope(pluginapi.HostAuthGetResponse{
				AuthIndex: req.AuthIndex,
				JSON:      credentialJSONFor(req.AuthIndex),
			})
		case pluginabi.MethodHostStreamEmit:
			var req struct {
				Payload []byte `json:"payload"`
			}
			_ = json.Unmarshal(request, &req)
			emitted = append(emitted, string(req.Payload))
			return okEnvelope(map[string]any{"emitted": true})
		case pluginabi.MethodHostStreamClose:
			return okEnvelope(map[string]any{"closed": true})
		case pluginabi.MethodHostLog:
			return okEnvelope(map[string]any{"logged": true})
		default:
			return nil, fmt.Errorf("unexpected host call %q", method)
		}
	}
	return &emitted
}

// sseBody is one well-formed upstream stream carrying a single content chunk.
func sseBody(content string) string {
	return "data: {\"choices\":[{\"delta\":{\"content\":\"" + content + "\"}}]}\n\n"
}

// withStubUpstream serves the given responses in order, one per HTTP attempt.
func withStubUpstream(t *testing.T, responses []*http.Response) *int {
	t.Helper()
	original := upstreamHTTPDo
	t.Cleanup(func() { upstreamHTTPDo = original })

	calls := 0
	upstreamHTTPDo = func(req *http.Request) (*http.Response, error) {
		if calls >= len(responses) {
			return nil, fmt.Errorf("unexpected upstream attempt %d", calls+1)
		}
		resp := responses[calls]
		calls++
		return resp, nil
	}
	return &calls
}

func sseResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func errorResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func mustCredential(t *testing.T, token string) *storedAuth {
	t.Helper()
	sa, err := parseStored(credentialJSONFor(token))
	if err != nil {
		t.Fatalf("parseStored: %v", err)
	}
	return sa
}

// A throttled account must hand the request to another one, and the client must
// see only the successful attempt.
func TestStreamRotationRetriesOn429(t *testing.T) {
	pool := []pluginapi.HostAuthFileEntry{
		{AuthIndex: "idx-1", Provider: providerName, Label: "A"},
		{AuthIndex: "idx-2", Provider: providerName, Label: "B"},
	}
	emitted := streamHost(t, pool, nil)

	withStubUpstream(t, []*http.Response{
		errorResponse(http.StatusTooManyRequests, `{"msg":"rate limited"}`),
		sseResponse(http.StatusOK, sseBody("hello")),
	})

	sa := mustCredential(t, "idx-1")
	pumpUpstreamStream([]byte(`{"model":"hy3"}`), sa, "stream-1", false)

	if len(*emitted) != 1 {
		t.Fatalf("emitted %d chunks, want exactly 1: %v", len(*emitted), *emitted)
	}
	if !strings.Contains((*emitted)[0], "hello") {
		t.Errorf("chunk = %q, want the second account's content", (*emitted)[0])
	}
	for _, chunk := range *emitted {
		if strings.Contains(chunk, "rate limited") {
			t.Error("the failed attempt leaked to the client")
		}
	}
}

// Every account being throttled must surface the failure once, not loop.
func TestStreamRotationGivesUpWhenPoolExhausted(t *testing.T) {
	pool := []pluginapi.HostAuthFileEntry{
		{AuthIndex: "idx-1", Provider: providerName, Label: "A"},
		{AuthIndex: "idx-2", Provider: providerName, Label: "B"},
	}
	emitted := streamHost(t, pool, nil)

	withStubUpstream(t, []*http.Response{
		errorResponse(http.StatusTooManyRequests, `{"msg":"limited-1"}`),
		errorResponse(http.StatusTooManyRequests, `{"msg":"limited-2"}`),
	})

	sa := mustCredential(t, "idx-1")
	pumpUpstreamStream([]byte(`{"model":"hy3"}`), sa, "stream-2", false)

	if len(*emitted) != 1 {
		t.Fatalf("emitted %d payloads, want 1 error payload: %v", len(*emitted), *emitted)
	}
	if !strings.Contains((*emitted)[0], "error") {
		t.Errorf("payload = %q, want an error envelope", (*emitted)[0])
	}
}

// A bad request is the same for every account, so it must not rotate: burning
// the pool on a malformed request only multiplies upstream load.
func TestStreamRotationDoesNotRetryPermanentError(t *testing.T) {
	pool := []pluginapi.HostAuthFileEntry{
		{AuthIndex: "idx-1", Provider: providerName, Label: "A"},
		{AuthIndex: "idx-2", Provider: providerName, Label: "B"},
	}
	streamHost(t, pool, nil)

	calls := withStubUpstream(t, []*http.Response{
		errorResponse(http.StatusBadRequest, `{"msg":"bad payload"}`),
	})

	sa := mustCredential(t, "idx-1")
	pumpUpstreamStream([]byte(`{"model":"hy3"}`), sa, "stream-3", false)

	if *calls != 1 {
		t.Errorf("upstream attempts = %d, want 1 (no rotation on 400)", *calls)
	}
}

// Once a chunk has reached the client the response is committed; retrying would
// splice two attempts into one stream.
func TestStreamRotationStopsAfterFirstChunk(t *testing.T) {
	pool := []pluginapi.HostAuthFileEntry{
		{AuthIndex: "idx-1", Provider: providerName, Label: "A"},
		{AuthIndex: "idx-2", Provider: providerName, Label: "B"},
	}
	streamHost(t, pool, nil)

	// The first response is 200 and streams one chunk, then the connection
	// dies mid-stream. That is not a rotation case: bytes are already out.
	calls := withStubUpstream(t, []*http.Response{
		sseResponse(http.StatusOK, sseBody("first")),
	})

	sa := mustCredential(t, "idx-1")
	pumpUpstreamStream([]byte(`{"model":"hy3"}`), sa, "stream-4", false)

	if *calls != 1 {
		t.Errorf("upstream attempts = %d, want 1 (no retry after first chunk)", *calls)
	}
}

// A credential that cannot be parsed must be skipped rather than aborting the
// rotation.
func TestRotatorSkipsUnparseableCredential(t *testing.T) {
	originalTransport := hostTransport
	t.Cleanup(func() { hostTransport = originalTransport })
	hostTransport = func(method string, request []byte) ([]byte, error) {
		if method != pluginabi.MethodHostAuthGet {
			return nil, fmt.Errorf("unexpected host call %q", method)
		}
		var req struct {
			AuthIndex string `json:"auth_index"`
		}
		_ = json.Unmarshal(request, &req)
		if req.AuthIndex == "idx-2" {
			// No accessToken -> parseStored rejects it.
			return okEnvelope(pluginapi.HostAuthGetResponse{
				AuthIndex: "idx-2",
				JSON:      json.RawMessage(`{"type":"workbuddy","auth":{}}`),
			})
		}
		return okEnvelope(pluginapi.HostAuthGetResponse{
			AuthIndex: req.AuthIndex,
			JSON:      credentialJSONFor(req.AuthIndex),
		})
	}

	original := credentialPool
	t.Cleanup(func() { credentialPool = original })
	credentialPool = func() []pluginapi.HostAuthFileEntry {
		return []pluginapi.HostAuthFileEntry{
			{AuthIndex: "idx-1"},
			{AuthIndex: "idx-2"},
			{AuthIndex: "idx-3"},
		}
	}

	rot := newRotator()
	rot.mark("idx-1")

	got, ok := rot.next("idx-1")
	if !ok {
		t.Fatal("next() found nothing although idx-3 is usable")
	}
	if got.Entry.AuthIndex != "idx-3" {
		t.Errorf("next() = %q, want idx-3 (idx-2 is unparseable)", got.Entry.AuthIndex)
	}
}
