// Account rotation for the workbuddy plugin.
//
// The host picks one credential per request and hands it over in
// ExecutorRequest.StorageJSON. It will not try a second credential for us,
// because a plugin executor returns a plain error whose HTTP status the host
// cannot see (statusCodeFromError reads a StatusCode() int interface, and
// shouldRetryAfterError bails out on status 0). So the plugin rotates by
// itself: when the upstream rejects the request with a status that means "this
// account is unusable right now", we fetch the next credential and try again.
//
// This is invisible to the client as long as it happens before the first byte
// reaches it. For streaming that is a hard requirement -- see pumpUpstreamStream
// -- so rotation only ever runs while the stream is still untouched.

package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// upstreamError is an upstream HTTP failure that carries its status code.
//
// CodeBuddy reports business failures as HTTP 200 with a non-zero envelope code,
// but rate limits, auth failures and gateway faults do come back as real status
// codes. Recording the code on the error (rather than formatting it into the
// message) is what lets the plugin classify a failure without re-parsing a
// string.
type upstreamError struct {
	Status int
	Body   string
}

func (e *upstreamError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("upstream %d", e.Status)
	}
	return fmt.Sprintf("upstream %d: %s", e.Status, e.Body)
}

// StatusCode satisfies the interface the host uses to classify executor errors.
// Without it the host sees status 0 and never treats the failure as retryable.
func (e *upstreamError) StatusCode() int { return e.Status }

// newUpstreamError drains a failed response body into an upstreamError, capping
// what is retained so a huge HTML error page cannot bloat a log line.
func newUpstreamError(resp *http.Response) *upstreamError {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &upstreamError{Status: resp.StatusCode, Body: truncate(string(body), 200)}
}

// channelRejectedCode is CodeBuddy's "Illegal API invocation from an unapproved
// channel" business code, returned inside an HTTP 400 body.
//
// It looks like a request-shaped failure but is not: the verdict is reached
// against the credential/channel pairing, so a different account can succeed
// where this one was refused.
const channelRejectedCode = 11128

// retryableUpstreamStatus reports whether another account is worth trying for
// this failure.
//
// The status set mirrors what the host itself considers retryable
// (403/408/5xx) plus 429, which for a pooled provider means "this account is
// rate limited, another one may not be".
//
// 400/404/422 are otherwise request-shaped failures that every account would
// hit identically, so rotating would only multiply the load -- with one
// exception, handled by the body: an unapproved-channel rejection is a verdict
// about the credential, not the payload.
func retryableUpstreamStatus(err *upstreamError) bool {
	if err == nil {
		return false
	}
	if err.Status == http.StatusBadRequest && containsBusinessCode(err.Body, channelRejectedCode) {
		return true
	}
	switch err.Status {
	case http.StatusForbidden,
		http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// containsBusinessCode reports whether a CodeBuddy error body carries the given
// business code.
//
// Matching is textual because only a truncated string of the body is retained,
// and the shape is always {"code":<n>,...}. The delimiter after the digits is
// required so that e.g. 111280 is not mistaken for 11128 -- rotating on an
// unrelated failure would burn a healthy credential.
func containsBusinessCode(body string, code int) bool {
	needle := fmt.Sprintf(`"code":%d`, code)
	idx := strings.Index(body, needle)
	if idx < 0 {
		return false
	}
	// The match must end at a delimiter, not in the middle of a longer number.
	rest := body[idx+len(needle):]
	if rest == "" {
		return true
	}
	switch rest[0] {
	case ',', '}', ']', ' ', '\n', '\r', '\t':
		return true
	default:
		return false
	}
}

// maxRotateAttempts bounds how many distinct credentials one request may burn.
// A pool of eight accounts should not be walked end to end on every failing
// request: the point is to survive a dead or throttled account, not to brute
// force the whole pool while the upstream is down.
const maxRotateAttempts = 3

// rotateWait is the pause between account attempts. Kept short because a 429 on
// one account usually clears on a different account immediately; this exists to
// avoid hammering an upstream that is failing fast for a shared reason.
const rotateWait = 250 * time.Millisecond

// credentialPool enumerates the accounts this request may fall back to.
//
// It is a package-level seam so tests can substitute a fixed pool. The returned
// slice is cached for the lifetime of one rotation, because listWorkbuddyEntries
// costs one host.auth.get round trip per credential and re-reading it on every
// attempt would add seconds of latency to a request that is already failing.
var credentialPool = func() []pluginapi.HostAuthFileEntry {
	entries, err := listWorkbuddyEntries()
	if err != nil {
		pluginLog("warn", "", "workbuddy rotation: credential list failed", map[string]any{
			"error": err.Error(),
		})
		return nil
	}
	return entries
}

// rotator walks the credential pool for one request.
//
// It holds the enumerated pool plus the set of accounts already attempted, so a
// request never re-reads a credential it has already burnt and never retries the
// one the host originally selected.
type rotator struct {
	pool  []pluginapi.HostAuthFileEntry
	tried map[string]struct{}
}

// newRotator enumerates the pool once for one request. The caller marks the
// host-selected credential as tried before asking for the next one.
func newRotator() *rotator {
	return &rotator{
		pool:  credentialPool(),
		tried: make(map[string]struct{}),
	}
}

// next returns the next usable credential that has not been tried yet. It
// returns false when the pool is exhausted or every remaining entry fails to
// parse, which is the signal to give up and surface the original error.
func (r *rotator) next(excludeAuthID string) (rotateTry, bool) {
	for _, entry := range r.pool {
		if _, seen := r.tried[entry.AuthIndex]; seen {
			continue
		}
		if strings.TrimSpace(entry.AuthIndex) == "" || entry.AuthIndex == excludeAuthID {
			continue
		}
		sa, err := credentialForEntry(entry)
		if err != nil {
			// A credential that will not parse cannot serve the request; mark
			// it so we do not re-read it on the next round.
			r.tried[entry.AuthIndex] = struct{}{}
			continue
		}
		return rotateTry{Entry: entry, Credential: sa}, true
	}
	return rotateTry{}, false
}

// mark records that an account has been attempted.
func (r *rotator) mark(authIndex string) {
	if authIndex != "" {
		r.tried[authIndex] = struct{}{}
	}
}

// rotateTry is one candidate credential offered to the caller.
type rotateTry struct {
	Entry      pluginapi.HostAuthFileEntry
	Credential *storedAuth
}

// hostAuthIndex recovers the auth index of the credential the host selected.
//
// The host sends the credential's persisted JSON in ExecutorRequest.StorageJSON
// (see pluginhost.storageJSONFromAuth), which carries no file identity -- so the
// index has to be recovered by matching the account against the enumerated pool.
// The access token is the reliable key: it is what identifies the account
// upstream, and it survives renames of the auth file.
//
// An empty result is not fatal. It only means we could not tell which pool entry
// the host chose, and the caller then falls back to `tried` alone, which is
// still correct -- it just costs one redundant attempt against the same account.
func hostAuthIndex(pool []pluginapi.HostAuthFileEntry, sa *storedAuth) string {
	if sa == nil {
		return ""
	}
	token := strings.TrimSpace(sa.Auth.AccessToken)
	if token == "" {
		return ""
	}
	for _, entry := range pool {
		raw, errJSON := credentialJSON(entry.AuthIndex)
		if errJSON != nil {
			continue
		}
		candidate, errParse := parseStored(raw)
		if errParse != nil {
			continue
		}
		if strings.TrimSpace(candidate.Auth.AccessToken) == token {
			return entry.AuthIndex
		}
	}
	return ""
}
