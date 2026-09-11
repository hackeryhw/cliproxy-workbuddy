package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// -----------------------------------------------------------------------------
// Pure logic
// -----------------------------------------------------------------------------

func TestRouteForPathMatchesHostFullPaths(t *testing.T) {
	cases := map[string]string{
		"/v0/resource/plugins/workbuddy/checkin":           consolePagePath,
		"/v0/management/plugins/workbuddy/checkin/status":  mgmtStatusPath,
		"/v0/management/plugins/workbuddy/checkin/claim":   mgmtClaimPath,
		"/v0/management/plugins/workbuddy/quota":           mgmtQuotaPath,
		"/v0/management/plugins/workbuddy/checkin/status/": mgmtStatusPath,
		"/v0/management/plugins/workbuddy/unknown":         "",
		"": "",
	}
	for path, want := range cases {
		if got := routeForPath(path); got != want {
			t.Errorf("routeForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestIsHunyuanModelCoversHy4Generation(t *testing.T) {
	yes := []string{"hy3", "hy3-x", "hy3-preview-agent", "hy4-preview", "hy4-preview-f", "hunyuan-chat"}
	for _, id := range yes {
		if !isHunyuanModel(id) {
			t.Errorf("isHunyuanModel(%q) = false, want true", id)
		}
	}
	no := []string{"auto", "glm-5.3", "kimi-k3-1", "deepseek-v4.1-flash", "minimax-m3", "fast-model", ""}
	for _, id := range no {
		if isHunyuanModel(id) {
			t.Errorf("isHunyuanModel(%q) = true, want false", id)
		}
	}
}

func TestForceMaxThinkingPinsHy4(t *testing.T) {
	for _, model := range []string{"hy4-preview-f", "hy4-preview", "hy3-x"} {
		obj := map[string]any{"model": model}
		if !forceMaxThinking(obj) {
			t.Fatalf("forceMaxThinking(%q) = false, want true", model)
		}
		if obj["reasoning_effort"] != "high" {
			t.Fatalf("forceMaxThinking(%q) effort = %v, want high", model, obj["reasoning_effort"])
		}
	}
	// Already high must report "unchanged" so the payload is not rewritten.
	obj := map[string]any{"model": "hy4-preview-f", "reasoning_effort": "high"}
	if forceMaxThinking(obj) {
		t.Error("forceMaxThinking reported a change for an already-high request")
	}
	// Non-Hunyuan models keep whatever the client chose.
	obj = map[string]any{"model": "glm-5.3", "reasoning_effort": "low"}
	if forceMaxThinking(obj) {
		t.Error("forceMaxThinking touched a non-Hunyuan model")
	}
}

func TestCleanChunkNormalizesFinishReasonAndStripsEmptyDelta(t *testing.T) {
	in := `{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,` +
		`"delta":{"role":"assistant","content":"","reasoning_content":"hi","tool_calls":[],"refusal":"","extra_fields":null},` +
		`"logprobs":null,"finish_reason":""}]}`

	out := cleanChunkJSON(in)

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("cleanChunkJSON produced invalid JSON: %v", err)
	}
	choice := got["choices"].([]any)[0].(map[string]any)

	if fr, present := choice["finish_reason"]; !present || fr != nil {
		t.Errorf("finish_reason = %#v (present=%v), want explicit null", fr, present)
	}
	delta := choice["delta"].(map[string]any)
	for _, empty := range []string{"content", "tool_calls", "refusal", "extra_fields"} {
		if _, present := delta[empty]; present {
			t.Errorf("delta still carries empty field %q: %v", empty, delta[empty])
		}
	}
	if delta["reasoning_content"] != "hi" {
		t.Errorf("reasoning_content = %v, want hi", delta["reasoning_content"])
	}
	if delta["role"] != "assistant" {
		t.Errorf("role = %v, want assistant", delta["role"])
	}
}

func TestCleanChunkLeavesRealFinishReasonAlone(t *testing.T) {
	in := `{"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}]}`
	out := cleanChunkJSON(in)
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if fr := got["choices"].([]any)[0].(map[string]any)["finish_reason"]; fr != "stop" {
		t.Errorf("finish_reason = %v, want stop", fr)
	}
}

func TestStreamEventDataDropsKeepalives(t *testing.T) {
	drop := []string{
		"",
		": heartbeat",
		":heartbeat",
		": keep-alive",
		"event: ping",
		"id: 1",
		"retry: 3000",
		"data: [DONE]",
		"[DONE]",
	}
	for _, line := range drop {
		if got := streamEventData(line); got != "" {
			t.Errorf("streamEventData(%q) = %q, want empty", line, got)
		}
	}

	payload := `{"id":"x","object":"chat.completion.chunk"}`
	if got := streamEventData("data: " + payload); got != payload {
		t.Errorf("streamEventData kept framed JSON as %q", got)
	}
}

func TestCleanChunkDropsNonJSONKeepalives(t *testing.T) {
	if got := cleanChunkJSON(": heartbeat"); got != "" {
		t.Errorf("cleanChunkJSON passed keepalive through: %q", got)
	}
}

func TestAggregateSSESkipsHeartbeatLines(t *testing.T) {
	raw := strings.Join([]string{
		": heartbeat",
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		": keep-alive",
		"data: [DONE]",
		"",
	}, "\n")
	chunks := aggregateSSE(strings.NewReader(raw), false)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1 real payload (heartbeats must not leak)", len(chunks))
	}
	if !json.Valid(chunks[0].Payload) {
		t.Fatalf("leaked non-JSON chunk: %s", chunks[0].Payload)
	}
	if !strings.Contains(string(chunks[0].Payload), `"hi"`) {
		t.Errorf("lost the real delta: %s", chunks[0].Payload)
	}
}

func TestIsConversationalFiltersNonChatEntries(t *testing.T) {
	keep := []configModel{
		{ID: "hy4-preview-f", SupportsToolCall: true, MaxInputTokens: 1000000},
		{ID: "glm-5.3", SupportsToolCall: true, MaxInputTokens: 1000000},
	}
	for _, m := range keep {
		if !isConversational(m) {
			t.Errorf("isConversational(%q) = false, want true", m.ID)
		}
	}
	drop := []configModel{
		{ID: "hunyuan-image-v3.0", SupportsToolCall: false, MaxInputTokens: 0},
		{ID: "codewise-completions", SupportsToolCall: true, MaxInputTokens: 200000},
		{ID: "hunyuan-3b", SupportsToolCall: false, MaxInputTokens: 0},
		{ID: "default-1.2", SupportsToolCall: true, MaxInputTokens: 200000},
		{ID: "completion-gf", SupportsToolCall: true, MaxInputTokens: 200000},
		{ID: "deepseek-v3-1-lkeap", SupportsToolCall: true, MaxInputTokens: 96000},
		{ID: "kimi-k2-instruct-taiji", SupportsToolCall: true, MaxInputTokens: 31000},
	}
	for _, m := range drop {
		if isConversational(m) {
			t.Errorf("isConversational(%q) = true, want false", m.ID)
		}
	}
}

func TestBuildModelInfoFallsBackForLegacyAlias(t *testing.T) {
	// Legacy alias absent from the live catalogue: the static table supplies
	// name and context length, and no credit suffix is appended.
	info := buildModelInfo("hy3-preview-agent", configModel{})
	if info.DisplayName == "" || info.DisplayName == "hy3-preview-agent" {
		t.Errorf("legacy alias kept a raw id as display name: %q", info.DisplayName)
	}
	if info.ContextLength == 0 {
		t.Error("legacy alias lost its static context length")
	}
	if info.MaxCompletionTokens == 0 {
		t.Error("legacy alias lost its completion ceiling")
	}

	// Live entry: the credit multiplier is surfaced for the picker.
	live := buildModelInfo("hy4-preview-f", configModel{
		ID: "hy4-preview-f", Name: "Hy4 preview", Credits: "x0.00",
		MaxInputTokens: 1000000, MaxOutputTokens: 64000, SupportsToolCall: true,
	})
	if live.ContextLength != 1000000 || live.MaxCompletionTokens != 64000 {
		t.Errorf("live entry limits wrong: %+v", live)
	}
	if want := "Hy4 preview · x0.00"; live.DisplayName != want {
		t.Errorf("DisplayName = %q, want %q", live.DisplayName, want)
	}
}

func TestAlreadyCheckedIn(t *testing.T) {
	if !alreadyCheckedIn(json.RawMessage(`{"today_checked_in":true}`)) {
		t.Error("expected true for today_checked_in=true")
	}
	if alreadyCheckedIn(json.RawMessage(`{"today_checked_in":false}`)) {
		t.Error("expected false for today_checked_in=false")
	}
	if alreadyCheckedIn(nil) {
		t.Error("expected false for empty payload")
	}
	if alreadyCheckedIn(json.RawMessage(`not json`)) {
		t.Error("expected false for malformed payload")
	}
}

func TestClaimHintRewritesAlreadyClaimedCode(t *testing.T) {
	if got := claimHint(&billingResult{Code: 10001, Msg: "今天已签到，请明天再来"}); got != "今天已签到，请明天再来" {
		t.Errorf("claimHint passed through wrong text: %q", got)
	}
	if got := claimHint(&billingResult{Code: 10001, Msg: "something else"}); got != "今天已签到，请明天再来" {
		t.Errorf("claimHint fallback = %q", got)
	}
	if got := claimHint(&billingResult{Code: 0, Msg: "OK"}); got != "OK" {
		t.Errorf("claimHint changed a successful message: %q", got)
	}
	if got := claimHint(nil); got != "" {
		t.Errorf("claimHint(nil) = %q, want empty", got)
	}
}

func TestManagementRegistrationShapesMatchHostContract(t *testing.T) {
	raw, err := handleManagementRegister()
	if err != nil {
		t.Fatalf("handleManagementRegister: %v", err)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Routes []struct {
				Method string `json:"Method"`
				Path   string `json:"Path"`
			} `json:"routes"`
			Resources []struct {
				Path string `json:"Path"`
				Menu string `json:"Menu"`
			} `json:"resources"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode registration: %v", err)
	}
	if !env.OK {
		t.Fatal("registration envelope reported ok=false")
	}
	if len(env.Result.Routes) != 6 {
		t.Errorf("routes = %d, want 6", len(env.Result.Routes))
	}
	// Every advertised route must be reachable through routeForPath with the
	// full host prefix, otherwise the panel would hit a 404.
	for _, r := range env.Result.Routes {
		full := "/v0/management" + r.Path
		if routeForPath(full) != r.Path {
			t.Errorf("registered route %q is not routable as %q", r.Path, full)
		}
	}
	if routeForPath("/v0/management"+env.Result.Resources[0].Path) != consolePagePath {
		t.Errorf("resource route %q is not routable", env.Result.Resources[0].Path)
	}
	if len(env.Result.Resources) != 1 {
		t.Fatalf("resources = %d, want 1", len(env.Result.Resources))
	}
	if env.Result.Resources[0].Path != consolePagePath {
		t.Errorf("resource path = %q, want %q", env.Result.Resources[0].Path, consolePagePath)
	}
	if env.Result.Resources[0].Menu == "" {
		t.Error("resource route has no menu label")
	}

	// The host rejects routes containing ":" or "*"; the accepted ones must not
	// carry either, and every path must be absolute.
	for _, r := range env.Result.Routes {
		if r.Path == "" || r.Path[0] != '/' {
			t.Errorf("route path %q is not absolute", r.Path)
		}
		if containsAny(r.Path, ":*") {
			t.Errorf("route path %q contains a forbidden character", r.Path)
		}
	}
}

func containsAny(s, chars string) bool {
	for _, c := range chars {
		for _, sc := range s {
			if c == sc {
				return true
			}
		}
	}
	return false
}

func TestManagementHandleServesConsolePage(t *testing.T) {
	req := `{"Method":"GET","Path":"/v0/resource/plugins/workbuddy/checkin"}`
	raw, err := handleManagementHandle([]byte(req))
	if err != nil {
		t.Fatalf("handleManagementHandle: %v", err)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			StatusCode int    `json:"StatusCode"`
			Body       []byte `json:"Body"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if env.Result.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", env.Result.StatusCode)
	}
	body := string(env.Result.Body)
	if !containsAny(body, "") && body == "" {
		t.Fatal("console page body is empty")
	}
	if !contains(body, "立即签到") {
		t.Error("console page is missing the check-in button")
	}
	if !contains(body, "刷新凭证") {
		t.Error("console page is missing the credential refresh button")
	}
	// The panel keeps its management key behind an encrypted storage adapter, so
	// the page recovers it by watching the parent window (it is embedded in a
	// same-origin iframe) rather than reading storage.
	for _, needle := range []string{
		"installSnifferOn(window.parent)",
		"setRequestHeader",
		"workbuddy-console-key",
	} {
		if !contains(body, needle) {
			t.Errorf("console page is missing key recovery piece %q", needle)
		}
	}
	// It must NOT try to read the panel's own storage: that value is ciphertext,
	// and sending it would burn CPA's five-strike management ban.
	if contains(body, "cli-proxy-auth") {
		t.Error("console page reads the panel's own storage; that value is encrypted and would fail authentication")
	}
	for _, route := range []string{"/accounts", "/checkin/claim", "/refresh"} {
		if !contains(body, route) {
			t.Errorf("console page does not call %q", route)
		}
	}
	// The page must not fall back to the panel's expired-marker route: the host
	// does not auto-refresh this provider, so that call reports success while
	// nothing rotates.
	if contains(body, "/auth-files/fields") {
		t.Error("console page still marks the credential expired for the host to refresh; the plugin rotates it itself")
	}
	// A stray backtick would have terminated the Go raw string literal, so the
	// page would be truncated; assert the closing markup survived.
	if !contains(body, "</html>") {
		t.Error("console page is truncated (no closing </html>)")
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestManagementHandleUnknownPathIs404(t *testing.T) {
	raw, err := handleManagementHandle([]byte(`{"Method":"GET","Path":"/v0/management/plugins/workbuddy/nope"}`))
	if err != nil {
		t.Fatalf("handleManagementHandle: %v", err)
	}
	var env struct {
		Result struct {
			StatusCode int `json:"StatusCode"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Result.StatusCode != 404 {
		t.Errorf("StatusCode = %d, want 404", env.Result.StatusCode)
	}
}

// -----------------------------------------------------------------------------
// Live upstream checks
//
// These exercise the real CodeBuddy endpoints using the credential the desktop
// plugin already persisted. They are skipped when that file is absent so the
// suite still runs on a fresh checkout.
// -----------------------------------------------------------------------------

func liveCredential(t *testing.T) *storedAuth {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	dir := filepath.Join(home, ".cli-proxy-api")
	candidates := []string{filepath.Join(dir, authFileName)}
	if matches, errGlob := filepath.Glob(filepath.Join(dir, "workbuddy*.json")); errGlob == nil {
		candidates = append(candidates, matches...)
	}
	seen := map[string]bool{}
	for _, path := range candidates {
		if seen[path] {
			continue
		}
		seen[path] = true
		raw, errRead := os.ReadFile(path)
		if errRead != nil {
			continue
		}
		sa, errParse := parseStored(raw)
		if errParse != nil {
			continue
		}
		return sa
	}
	t.Skipf("no live workbuddy credential under %s", dir)
	return nil
}

func TestLiveCheckinStatus(t *testing.T) {
	sa := liveCredential(t)
	result, err := queryCheckinStatus(sa)
	if err != nil {
		t.Fatalf("queryCheckinStatus: %v", err)
	}
	if result.Code != 0 {
		t.Fatalf("check-in status code = %d msg=%q", result.Code, result.Msg)
	}
	var status struct {
		Active         bool   `json:"active"`
		TodayCheckedIn bool   `json:"today_checked_in"`
		DailyCredit    int    `json:"daily_credit"`
		StreakDays     int    `json:"streak_days"`
		ActivityName   string `json:"activity_name"`
	}
	if err := json.Unmarshal(result.Data, &status); err != nil {
		t.Fatalf("decode data: %v (raw=%s)", err, truncate(string(result.Data), 200))
	}
	if !status.Active {
		t.Log("check-in activity is not active right now")
	}
	t.Logf("activity=%q streak=%d daily=%d today_checked_in=%v",
		status.ActivityName, status.StreakDays, status.DailyCredit, status.TodayCheckedIn)
}

func TestLiveQuota(t *testing.T) {
	sa := liveCredential(t)
	result, err := queryQuota(sa)
	if err != nil {
		t.Fatalf("queryQuota: %v", err)
	}
	if result.Code != 0 {
		t.Fatalf("quota code = %d msg=%q", result.Code, result.Msg)
	}
	var quota struct {
		Packages []struct {
			PackageCode         string `json:"PackageCode"`
			CycleTotalCapacity  string `json:"CycleTotalCapacity"`
			CycleRemainCapacity string `json:"CycleRemainCapacity"`
			CapacityUnit        string `json:"CapacityUnit"`
		} `json:"Packages"`
		IsPaidUser bool `json:"IsPaidUser"`
	}
	if err := json.Unmarshal(result.Data, &quota); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if len(quota.Packages) == 0 {
		t.Fatal("quota returned no packages")
	}
	for _, p := range quota.Packages {
		t.Logf("package=%s total=%s remain=%s unit=%s",
			p.PackageCode, p.CycleTotalCapacity, p.CycleRemainCapacity, p.CapacityUnit)
	}
	t.Logf("isPaidUser=%v", quota.IsPaidUser)
}

// TestLiveConfigModels proves the dynamic catalogue actually reaches the
// plugin and that the hy4 generation — the model the old hardcoded list missed
// entirely — is present.
func TestLiveConfigModels(t *testing.T) {
	sa := liveCredential(t)

	// Bypass any cached generation so the assertion reflects a fresh read.
	configCacheMu.Lock()
	delete(configCache, sa.Account.UID)
	configCacheMu.Unlock()

	models, err := fetchConfigModels(sa)
	if err != nil {
		t.Fatalf("fetchConfigModels: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("fetchConfigModels returned no models")
	}

	ids := make(map[string]bool, len(models))
	for _, m := range models {
		ids[m.ID] = true
		if m.ID == "" {
			t.Error("model with empty id")
		}
	}

	for _, want := range []string{"hy4-preview-f", "glm-5.3", "deepseek-v4.1-flash", "kimi-k3-1", "minimax-m3"} {
		if !ids[want] {
			t.Errorf("live catalogue is missing %q", want)
		}
	}
	for _, leaked := range []string{"codewise-completions", "default-1.2", "completion-gf", "hunyuan-image-v3.0-art", "deepseek-v3-1-lkeap"} {
		if ids[leaked] {
			t.Errorf("non-conversational entry %q leaked into the chat model list", leaked)
		}
	}

	// Degradation guard: the union of the live catalogue and the static
	// fallback must be strictly larger than the static list alone. If the
	// /v3/config parse silently yields nothing, the result collapses to
	// exactly wbModels() and this catches it — a plain "hy4-preview-f is
	// present" assertion does not, because the static list also has it.
	static := wbModels()
	if len(models) <= len(static) {
		t.Fatalf("live catalogue (%d) did not add anything beyond the static fallback (%d): the /v3/config parse produced no models", len(models), len(static))
	}
	t.Logf("live catalogue size=%d (static fallback=%d)", len(models), len(static))
}

// -----------------------------------------------------------------------------
// Management console flow, driven through a stubbed host transport
//
// The console reaches the host through hostTransport. Swapping it for a stub
// exercises the real credential lookup, dispatch, response encoding and
// upstream calls end to end, leaving only the browser click itself unproven.
// -----------------------------------------------------------------------------

// withFakeHost swaps hostTransport for a stub that answers the credential
// lookup RPCs. saved receives the payload of any host.auth.save call, so a test
// can assert on what a refresh would have written to disk.
func withFakeHost(t *testing.T, credential json.RawMessage, provider string) (saved *[]pluginapi.HostAuthSaveRequest) {
	t.Helper()
	original := hostTransport
	t.Cleanup(func() { hostTransport = original })

	recorded := make([]pluginapi.HostAuthSaveRequest, 0, 1)
	hostTransport = func(method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostAuthList:
			return okEnvelope(authFileListResponse{Files: []pluginapi.HostAuthFileEntry{{
				AuthIndex: "test-auth-index",
				Provider:  provider,
				Name:      authFileName,
				Label:     "WorkBuddy (test)",
			}}})
		case pluginabi.MethodHostAuthGet:
			return okEnvelope(pluginapi.HostAuthGetResponse{
				AuthIndex: "test-auth-index",
				Name:      authFileName,
				JSON:      credential,
			})
		case pluginabi.MethodHostAuthSave:
			var req pluginapi.HostAuthSaveRequest
			if err := json.Unmarshal(request, &req); err != nil {
				return nil, err
			}
			recorded = append(recorded, req)
			return okEnvelope(pluginapi.HostAuthSaveResponse{Name: req.Name, Path: "/tmp/" + req.Name})
		case pluginabi.MethodHostLog:
			// The console logs through the host; the stub records and accepts.
			var entry map[string]any
			_ = json.Unmarshal(request, &entry)
			return okEnvelope(map[string]any{"logged": true})
		default:
			return nil, fmt.Errorf("unexpected host call %q", method)
		}
	}
	return &recorded
}

func TestActiveCredentialResolvesThroughHostRPC(t *testing.T) {
	sa := liveCredential(t)
	raw, err := json.Marshal(sa)
	if err != nil {
		t.Fatalf("marshal credential: %v", err)
	}
	withFakeHost(t, raw, providerName)

	got, label, err := activeCredential()
	if err != nil {
		t.Fatalf("activeCredential: %v", err)
	}
	if got == nil || got.Account.UID != sa.Account.UID {
		t.Errorf("resolved wrong credential: %+v", got)
	}
	if got.Auth.AccessToken == "" {
		t.Error("resolved credential carries no access token")
	}
	if label == "" {
		t.Error("resolved credential has no label")
	}
}

// TestActiveCredentialFallsBackOnUnknownProviderLabel guards the second pass:
// if the host labels the credential something other than "workbuddy", the
// console must still find it rather than reporting no credential.
func TestActiveCredentialFallsBackOnUnknownProviderLabel(t *testing.T) {
	sa := liveCredential(t)
	raw, err := json.Marshal(sa)
	if err != nil {
		t.Fatalf("marshal credential: %v", err)
	}
	withFakeHost(t, raw, "codebuddy-cn")

	got, _, err := activeCredential()
	if err != nil {
		t.Fatalf("fallback pass did not recover the credential: %v", err)
	}
	if got.Account.UID != sa.Account.UID {
		t.Errorf("resolved wrong credential: %+v", got)
	}
}

// TestActiveCredentialRejectsUnrelatedCredentialProvesParseGate checks that a
// non-workbuddy payload is refused, so the console cannot accidentally act on
// another provider's account.
func TestActiveCredentialRejectsUnrelatedCredential(t *testing.T) {
	withFakeHost(t, json.RawMessage(`{"type":"antigravity","access_token":"x"}`), providerName)

	if _, _, err := activeCredential(); err == nil {
		t.Fatal("activeCredential accepted a non-workbuddy credential")
	}
}

func TestStatusHandlerReturnsLiveQuotaAndCheckin(t *testing.T) {
	sa := liveCredential(t)
	raw, err := json.Marshal(sa)
	if err != nil {
		t.Fatalf("marshal credential: %v", err)
	}
	withFakeHost(t, raw, providerName)

	resp := handleStatusRequest("", "")
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, body=%s", resp.StatusCode, truncate(string(resp.Body), 200))
	}
	var snap snapshot
	if err := json.Unmarshal(resp.Body, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if !snap.OK {
		t.Fatalf("snapshot not ok: message=%q", snap.Message)
	}
	if snap.Account == "" {
		t.Error("snapshot has no account label")
	}
	if len(snap.Quota) == 0 {
		t.Error("snapshot carries no quota payload")
	}
	if len(snap.Checkin) == 0 {
		t.Error("snapshot carries no check-in payload")
	}
	t.Logf("account=%s quota=%d bytes checkin=%d bytes", snap.Account, len(snap.Quota), len(snap.Checkin))
}

// TestClaimHandlerShortCircuitsWhenAlreadyCheckedIn proves the guard: when the
// account already claimed today, the handler must report that instead of firing
// a claim the upstream would reject.
func TestClaimHandlerShortCircuitsWhenAlreadyCheckedIn(t *testing.T) {
	sa := liveCredential(t)
	raw, err := json.Marshal(sa)
	if err != nil {
		t.Fatalf("marshal credential: %v", err)
	}
	withFakeHost(t, raw, providerName)

	status, errStatus := queryCheckinStatus(sa)
	if errStatus != nil {
		t.Skipf("check-in status unavailable: %v", errStatus)
	}
	if !alreadyCheckedIn(status.Data) {
		t.Skip("today is not claimed yet, so the guard cannot be exercised without claiming")
	}

	resp := handleClaimRequest("", "")
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, body=%s", resp.StatusCode, truncate(string(resp.Body), 200))
	}
	var snap snapshot
	if err := json.Unmarshal(resp.Body, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.OK {
		t.Error("claim reported success while the account was already checked in")
	}
	if snap.Message == "" {
		t.Error("already-checked-in path returned no message")
	}
	if len(snap.Quota) == 0 {
		t.Error("already-checked-in path did not refresh the quota")
	}
	t.Logf("message=%q", snap.Message)
}

func TestManagementHandleRoutesFullHostPaths(t *testing.T) {
	sa := liveCredential(t)
	raw, err := json.Marshal(sa)
	if err != nil {
		t.Fatalf("marshal credential: %v", err)
	}
	withFakeHost(t, raw, providerName)

	for _, tc := range []struct{ path, method string }{
		{"/v0/management/plugins/workbuddy/quota", "GET"},
		{"/v0/management/plugins/workbuddy/checkin/status", "GET"},
	} {
		req, _ := json.Marshal(mgmtRequest{Method: tc.method, Path: tc.path})
		out, errHandle := handleManagementHandle(req)
		if errHandle != nil {
			t.Fatalf("%s: handleManagementHandle: %v", tc.path, errHandle)
		}
		var env struct {
			OK     bool `json:"ok"`
			Result struct {
				StatusCode int    `json:"StatusCode"`
				Body       []byte `json:"Body"`
			} `json:"result"`
		}
		if err := json.Unmarshal(out, &env); err != nil {
			t.Fatalf("%s: decode: %v", tc.path, err)
		}
		if !env.OK || env.Result.StatusCode != 200 {
			t.Errorf("%s: ok=%v status=%d body=%s", tc.path, env.OK, env.Result.StatusCode, truncate(string(env.Result.Body), 160))
			continue
		}
		var snap snapshot
		if err := json.Unmarshal(env.Result.Body, &snap); err != nil {
			t.Errorf("%s: decode snapshot: %v (%s)", tc.path, err, truncate(string(env.Result.Body), 160))
			continue
		}
		if !snap.OK {
			t.Errorf("%s: snapshot not ok: %q", tc.path, snap.Message)
		}
	}
}

// TestLiveClaimCheckinIsIdempotent proves the claim route is reachable from the
// plugin's own code and that re-claiming is a business code, never a transport
// failure. Safe to run repeatedly: the upstream enforces one claim per day.
func TestLiveClaimCheckinIsIdempotent(t *testing.T) {
	sa := liveCredential(t)
	result, err := claimCheckin(sa)
	if err != nil {
		t.Fatalf("claimCheckin raised a transport error: %v", err)
	}
	if result.Code != 0 && result.Code != 10001 {
		t.Fatalf("unexpected claim code %d msg=%q http=%d", result.Code, result.Msg, result.HTTPStatus)
	}
	t.Logf("claim code=%d http=%d msg=%q", result.Code, result.HTTPStatus, result.Msg)
}

// -----------------------------------------------------------------------------
// Claim branches, driven through a canned billing transport
//
// Everything here is hermetic: a synthetic credential plus a stub transport, so
// the success path (which a given account only reaches once per day) is still
// covered on every run.
// -----------------------------------------------------------------------------

// testCredentialJSON is a fully synthetic workbuddy credential.
const testCredentialJSON = `{"type":"workbuddy",` +
	`"account":{"uid":"test-uid","enterpriseId":"","nickname":"tester"},` +
	`"auth":{"accessToken":"test-access-token","refreshToken":"test-refresh-token",` +
	`"expiresAt":4102444800,"domain":"copilot.tencent.com"}}`

type stubRoundTripper func(*http.Request) (*http.Response, error)

func (f stubRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// withStubBilling answers billing POSTs by path suffix. A path missing from the
// map answers 200 with an empty successful envelope.
func withStubBilling(t *testing.T, statusByPath map[string]int, status int, bodyByPath map[string]string, body string) {
	t.Helper()
	original := billingHTTPClient
	t.Cleanup(func() { billingHTTPClient = original })

	billingHTTPClient = func() *http.Client {
		return &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
			code, payload := status, body
			for suffix, candidate := range bodyByPath {
				if strings.HasSuffix(r.URL.Path, suffix) {
					payload = candidate
					if c, ok := statusByPath[suffix]; ok {
						code = c
					}
					break
				}
			}
			return &http.Response{
				StatusCode: code,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(payload)),
				Request:    r,
			}, nil
		})}
	}
}

func seedConsoleCredential(t *testing.T) {
	t.Helper()
	withFakeHost(t, json.RawMessage(testCredentialJSON), providerName)
}

func TestClaimHandlerReportsUpstreamSuccess(t *testing.T) {
	seedConsoleCredential(t)
	withStubBilling(t,
		map[string]int{},
		200,
		map[string]string{
			"/checkin-activity-status":       `{"code":0,"msg":"OK","data":{"today_checked_in":false,"streak_days":0}}`,
			"/daily-checkin":                 `{"code":0,"msg":"签到成功，获得 100 积分","data":{"credit":100}}`,
			"/checkin-activity-status-after": "",
		},
		`{"code":0,"msg":"OK","data":{}}`,
	)

	resp := handleClaimRequest("", "")
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, body=%s", resp.StatusCode, truncate(string(resp.Body), 200))
	}
	var snap snapshot
	if err := json.Unmarshal(resp.Body, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if !snap.OK {
		t.Errorf("successful claim reported ok=false: message=%q", snap.Message)
	}
	if !contains(snap.Message, "签到成功") {
		t.Errorf("upstream message not relayed: %q", snap.Message)
	}
}

func TestClaimHandlerRelaysAlreadyClaimedCode(t *testing.T) {
	seedConsoleCredential(t)
	// Status says "not yet claimed" so the handler proceeds to claim, and the
	// claim itself answers the already-claimed business code — the race the
	// plugin cannot rule out locally.
	withStubBilling(t,
		map[string]int{"/daily-checkin": 400},
		200,
		map[string]string{
			"/checkin-activity-status": `{"code":0,"msg":"OK","data":{"today_checked_in":false}}`,
			"/daily-checkin":           `{"code":10001,"msg":"今天已签到，请明天再来"}`,
		},
		`{"code":0,"msg":"OK","data":{}}`,
	)

	resp := handleClaimRequest("", "")
	var snap snapshot
	if err := json.Unmarshal(resp.Body, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.OK {
		t.Error("already-claimed code was reported as success")
	}
	if snap.Message != "今天已签到，请明天再来" {
		t.Errorf("message = %q, want the upstream wording", snap.Message)
	}
}

func TestClaimHandlerSurfacesTransportFailure(t *testing.T) {
	seedConsoleCredential(t)
	original := billingHTTPClient
	t.Cleanup(func() { billingHTTPClient = original })
	billingHTTPClient = func() *http.Client {
		return &http.Client{Transport: stubRoundTripper(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("connection reset")
		})}
	}

	resp := handleClaimRequest("", "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("StatusCode = %d, want 502; body=%s", resp.StatusCode, truncate(string(resp.Body), 200))
	}
	var payload struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if payload.OK || payload.Error.Code != "claim_failed" {
		t.Errorf("unexpected error payload: %+v", payload)
	}
}

func TestStatusHandlerToleratesEmptyCheckinData(t *testing.T) {
	seedConsoleCredential(t)
	withStubBilling(t, map[string]int{}, 200, map[string]string{
		"/checkin-activity-status":   `{"code":0,"msg":"OK","data":null}`,
		"/get-user-resource-summary": `{"code":0,"msg":"OK","data":{"Packages":[],"IsPaidUser":false}}`,
	}, `{"code":0,"msg":"OK","data":{}}`)

	resp := handleStatusRequest("", "")
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, body=%s", resp.StatusCode, truncate(string(resp.Body), 200))
	}
	var snap snapshot
	if err := json.Unmarshal(resp.Body, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if !snap.OK {
		t.Errorf("status not ok: %q", snap.Message)
	}
	if snap.Account == "" {
		t.Error("no account label")
	}
}

// TestConsolePageBrowserLogic runs the console page's own JavaScript.
//
// The Go suite covers everything the plugin does server-side; this closes the
// remaining gap by executing the page's script in node with a stubbed DOM,
// storage and fetch, asserting on the HTTP calls it actually issues — notably
// that it recovers the management key from the control panel's storage slot and
// sends it as a bearer token. Skipped when node is unavailable.
func TestConsolePageBrowserLogic(t *testing.T) {
	node, errNode := exec.LookPath("node")
	if errNode != nil {
		t.Skip("node not available")
	}
	scriptPath := filepath.Join("scripts", "verify-console-page.mjs")
	if _, errStat := os.Stat(scriptPath); errStat != nil {
		t.Skipf("verification script missing: %v", errStat)
	}

	pageFile := filepath.Join(t.TempDir(), "console.html")
	if err := os.WriteFile(pageFile, []byte(consoleHTML), 0o600); err != nil {
		t.Fatalf("write page fixture: %v", err)
	}

	out, errRun := exec.Command(node, scriptPath, pageFile).CombinedOutput()
	if errRun != nil {
		t.Fatalf("console page browser logic failed: %v\n%s", errRun, out)
	}
	if !strings.Contains(string(out), "all browser-side assertions passed") {
		t.Fatalf("verification script did not report success:\n%s", out)
	}
	t.Log("browser-side assertions passed")
}

// -----------------------------------------------------------------------------
// Per-account targeting (panel integration)
// -----------------------------------------------------------------------------

// hostWithTwoAccounts stubs a host that exposes two workbuddy credentials.
func hostWithTwoAccounts(t *testing.T) {
	t.Helper()
	original := hostTransport
	t.Cleanup(func() { hostTransport = original })

	second := json.RawMessage(`{"type":"workbuddy",` +
		`"account":{"uid":"second-uid","enterpriseId":"","nickname":"second"},` +
		`"auth":{"accessToken":"second-token","refreshToken":"r2",` +
		`"expiresAt":4102444800,"domain":"copilot.tencent.com"}}`)

	hostTransport = func(method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostAuthList:
			return okEnvelope(authFileListResponse{Files: []pluginapi.HostAuthFileEntry{
				{AuthIndex: "idx-1", Provider: providerName, Name: "workbuddy-a.json", Label: "账号 A"},
				{AuthIndex: "idx-2", Provider: providerName, Name: "workbuddy-b.json", Label: "账号 B"},
			}})
		case pluginabi.MethodHostAuthGet:
			var req struct {
				AuthIndex string `json:"auth_index"`
			}
			_ = json.Unmarshal(request, &req)
			if req.AuthIndex == "idx-2" {
				return okEnvelope(pluginapi.HostAuthGetResponse{AuthIndex: "idx-2", Name: "workbuddy-b.json", JSON: second})
			}
			return okEnvelope(pluginapi.HostAuthGetResponse{AuthIndex: "idx-1", Name: "workbuddy-a.json", JSON: json.RawMessage(testCredentialJSON)})
		case pluginabi.MethodHostLog:
			return okEnvelope(map[string]any{"logged": true})
		default:
			return nil, fmt.Errorf("unexpected host call %q", method)
		}
	}
}

func TestListWorkbuddyEntriesReturnsAllAccounts(t *testing.T) {
	hostWithTwoAccounts(t)

	entries, err := listWorkbuddyEntries()
	if err != nil {
		t.Fatalf("listWorkbuddyEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[0].AuthIndex != "idx-1" || entries[1].AuthIndex != "idx-2" {
		t.Errorf("unexpected order: %s, %s", entries[0].AuthIndex, entries[1].AuthIndex)
	}
}

func TestResolveAccountTargetsTheNamedAccount(t *testing.T) {
	hostWithTwoAccounts(t)

	first, err := resolveAccount("idx-1")
	if err != nil {
		t.Fatalf("resolveAccount(idx-1): %v", err)
	}
	second, err := resolveAccount("idx-2")
	if err != nil {
		t.Fatalf("resolveAccount(idx-2): %v", err)
	}
	if first.Credential.Auth.AccessToken == second.Credential.Auth.AccessToken {
		t.Error("both auth indexes resolved to the same credential")
	}
	if second.Credential.Account.UID != "second-uid" {
		t.Errorf("idx-2 resolved to uid %q, want second-uid", second.Credential.Account.UID)
	}
	// The entry travels with the credential: persisting a refresh needs the auth
	// file name the host knows this account by.
	if second.Entry.Name != "workbuddy-b.json" {
		t.Errorf("idx-2 carries auth file %q, want workbuddy-b.json", second.Entry.Name)
	}

	if _, err := resolveAccount("idx-missing"); err == nil {
		t.Error("resolveAccount accepted an unknown auth index")
	}
}

func TestResolveTargetHonoursAuthIndexParam(t *testing.T) {
	hostWithTwoAccounts(t)

	// No index: falls back to the first account.
	_, labelDefault, err := resolveTarget("")
	if err != nil {
		t.Fatalf("resolveTarget(\"\"): %v", err)
	}
	// Named index: targets that account.
	_, labelSecond, err := resolveTarget("idx-2")
	if err != nil {
		t.Fatalf("resolveTarget(idx-2): %v", err)
	}
	if labelDefault == labelSecond {
		t.Errorf("targeting did not switch accounts (both %q)", labelDefault)
	}
	if labelSecond != "账号 B" {
		t.Errorf("label = %q, want 账号 B", labelSecond)
	}
}

func TestMgmtRequestReadsAuthIndexFromQueryAndBody(t *testing.T) {
	fromQuery := mgmtRequest{Query: url.Values{"auth_index": []string{"idx-9"}}}
	if got := fromQuery.authIndex(); got != "idx-9" {
		t.Errorf("query lookup = %q, want idx-9", got)
	}
	fromBody := mgmtRequest{Body: []byte(`{"auth_index":"idx-7"}`)}
	if got := fromBody.authIndex(); got != "idx-7" {
		t.Errorf("body lookup = %q, want idx-7", got)
	}
	if got := (mgmtRequest{}).authIndex(); got != "" {
		t.Errorf("empty request = %q, want empty", got)
	}
	if got := (mgmtRequest{Body: []byte("not json")}).authIndex(); got != "" {
		t.Errorf("malformed body = %q, want empty", got)
	}
}

// TestAccountsRouteStaysCheap proves the panel's first paint costs no upstream
// work: /accounts must answer from the host's credential list alone. The credits
// are what make a card slow (two round trips each), so they are fetched only
// through /accounts/status when the operator asks.
func TestAccountsRouteStaysCheap(t *testing.T) {
	hostWithTwoAccounts(t)

	var upstreamCalls int
	original := billingHTTPClient
	t.Cleanup(func() { billingHTTPClient = original })
	billingHTTPClient = func() *http.Client {
		return &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
			upstreamCalls++
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"code":0,"msg":"OK","data":{}}`)),
				Request:    r,
			}, nil
		})}
	}

	req, _ := json.Marshal(mgmtRequest{Method: "GET", Path: "/v0/management" + mgmtAccountsPath})
	raw, err := handleManagementHandle(req)
	if err != nil {
		t.Fatalf("handleManagementHandle: %v", err)
	}
	body := decodeMgmtResult(t, raw)

	if upstreamCalls != 0 {
		t.Errorf("/accounts made %d upstream call(s); it must not touch the network", upstreamCalls)
	}

	var list accountsEnvelope
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode accounts: %v", err)
	}
	if !list.OK {
		t.Fatal("accounts payload reported ok=false")
	}
	if len(list.Accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(list.Accounts))
	}
	for _, a := range list.Accounts {
		if a.AuthIndex == "" || a.Label == "" {
			t.Errorf("account shell incomplete: %+v", a)
		}
	}
}

// TestAccountStatusRouteFetchesOnDemand covers the expensive half, including
// per-account targeting.
func TestAccountStatusRouteFetchesOnDemand(t *testing.T) {
	hostWithTwoAccounts(t)
	withStubBilling(t, map[string]int{}, 200, map[string]string{
		"/get-user-resource-summary": `{"code":0,"msg":"OK","data":{"Packages":[` +
			`{"PackageCode":"PKG-A","CycleTotalCapacity":"1600","CycleRemainCapacity":"1500",` +
			`"CycleUsedCapacity":"100","CapacityUnit":"credits"}],"IsPaidUser":false}}`,
		"/checkin-activity-status": `{"code":0,"msg":"OK","data":{` +
			`"today_checked_in":true,"streak_days":3,"daily_credit":100,"today_credit":100,` +
			`"total_credits":300,"activity_name":"开学季"}}`,
	}, `{"code":0,"msg":"OK","data":{}}`)

	// All accounts.
	req, _ := json.Marshal(mgmtRequest{Method: "GET", Path: "/v0/management" + mgmtAccountStatusPath})
	raw, err := handleManagementHandle(req)
	if err != nil {
		t.Fatalf("handleManagementHandle: %v", err)
	}
	var all statusEnvelope
	if err := json.Unmarshal(decodeMgmtResult(t, raw), &all); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if len(all.Accounts) != 2 {
		t.Fatalf("status accounts = %d, want 2", len(all.Accounts))
	}
	for _, a := range all.Accounts {
		if !a.CheckedInToday || a.StreakDays != 3 || a.ActivityName != "开学季" {
			t.Errorf("check-in state not parsed for %s: %+v", a.AuthIndex, a)
		}
		if a.Credits == nil || a.Credits.Remain != 1500 || a.Credits.Total != 1600 {
			t.Errorf("credits wrong for %s: %+v", a.AuthIndex, a.Credits)
		}
	}

	// One named account.
	req, _ = json.Marshal(mgmtRequest{
		Method: "GET",
		Path:   "/v0/management" + mgmtAccountStatusPath,
		Query:  url.Values{"auth_index": []string{"idx-2"}},
	})
	raw, err = handleManagementHandle(req)
	if err != nil {
		t.Fatalf("handleManagementHandle: %v", err)
	}
	var one statusEnvelope
	if err := json.Unmarshal(decodeMgmtResult(t, raw), &one); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if len(one.Accounts) != 1 {
		t.Fatalf("targeted status accounts = %d, want 1", len(one.Accounts))
	}
	if one.Accounts[0].AuthIndex != "idx-2" {
		t.Errorf("targeted the wrong account: %+v", one.Accounts[0])
	}
}

// decodeMgmtResult unwraps the plugin envelope and returns the management
// response body, failing when the status is not 200.
func decodeMgmtResult(t *testing.T, raw []byte) []byte {
	t.Helper()
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			StatusCode int    `json:"StatusCode"`
			Body       []byte `json:"Body"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !env.OK || env.Result.StatusCode != 200 {
		t.Fatalf("ok=%v status=%d body=%s", env.OK, env.Result.StatusCode, truncate(string(env.Result.Body), 200))
	}
	return env.Result.Body
}

func TestClaimRouteTargetsNamedAccount(t *testing.T) {
	hostWithTwoAccounts(t)

	var claimedPath string
	original := billingHTTPClient
	t.Cleanup(func() { billingHTTPClient = original })
	billingHTTPClient = func() *http.Client {
		return &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
			payload := `{"code":0,"msg":"OK","data":{"today_checked_in":false}}`
			if strings.HasSuffix(r.URL.Path, "/daily-checkin") {
				claimedPath = r.URL.Path
				payload = `{"code":0,"msg":"签到成功","data":{}}`
			}
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(payload)),
				Request:    r,
			}, nil
		})}
	}

	req, _ := json.Marshal(mgmtRequest{
		Method: "POST",
		Path:   "/v0/management" + mgmtClaimPath,
		Query:  url.Values{"auth_index": []string{"idx-2"}},
	})
	raw, err := handleManagementHandle(req)
	if err != nil {
		t.Fatalf("handleManagementHandle: %v", err)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			StatusCode int    `json:"StatusCode"`
			Body       []byte `json:"Body"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Result.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", env.Result.StatusCode, truncate(string(env.Result.Body), 200))
	}
	var snap snapshot
	if err := json.Unmarshal(env.Result.Body, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if !snap.OK {
		t.Errorf("claim not ok: %q", snap.Message)
	}
	if snap.Account != "账号 B" {
		t.Errorf("claim hit account %q, want 账号 B", snap.Account)
	}
	if claimedPath == "" {
		t.Error("no claim request was issued")
	}
}

// TestPanelIntegrationScriptLogic runs the control-panel integration script
// against a miniature panel DOM.
//
// The script decorates the panel's credential cards and quota page. A silent
// selector regression here would leave the buttons simply absent with no error
// anywhere, so the assertions cover both the DOM it produces and the HTTP calls
// it issues when those buttons are clicked.
func TestPanelIntegrationScriptLogic(t *testing.T) {
	node, errNode := exec.LookPath("node")
	if errNode != nil {
		t.Skip("node not available")
	}
	scriptPath := filepath.Join("scripts", "verify-panel-script.mjs")
	if _, errStat := os.Stat(scriptPath); errStat != nil {
		t.Skipf("verification script missing: %v", errStat)
	}

	out, errRun := exec.Command(node, scriptPath).CombinedOutput()
	if errRun != nil {
		t.Fatalf("panel integration script failed: %v\n%s", errRun, out)
	}
	if !strings.Contains(string(out), "all panel integration assertions passed") {
		t.Fatalf("verification script did not report success:\n%s", out)
	}
	t.Log("panel integration assertions passed")
}

// TestPanelPayloadMatchesPanelDom guards the class prefixes the integration
// script selects on. The panel is a CSS-module build, so a prefix typo or a
// renamed module would make every selector miss while the script still loads
// cleanly — exactly the failure this catches. Skipped when no panel is present.
func TestPanelPayloadMatchesPanelDom(t *testing.T) {
	panelPath := os.Getenv("WORKBUDDY_PANEL_HTML")
	if panelPath == "" {
		t.Skip("set WORKBUDDY_PANEL_HTML to the panel file to check selector coverage")
	}
	panel, errRead := os.ReadFile(panelPath)
	if errRead != nil {
		t.Skipf("panel not readable: %v", errRead)
	}
	payload, errPayload := os.ReadFile(filepath.Join("panel", "workbuddy-panel.js"))
	if errPayload != nil {
		t.Fatalf("read payload: %v", errPayload)
	}

	html := string(panel)

	// Derive the selector list from the payload itself instead of a hand-kept
	// slice: the guard has to fail when the panel renames a class the payload
	// still queries, and has to keep covering lookups added later. A stale
	// literal list quietly stops checking anything.
	selectorPattern := regexp.MustCompile(`\[class\*="([^"]+)"\]`)
	found := selectorPattern.FindAllStringSubmatch(string(payload), -1)
	if len(found) == 0 {
		t.Fatal("payload declares no [class*=...] selectors — the extraction regex is stale")
	}
	checked := make(map[string]bool)
	for _, match := range found {
		selector := match[1]
		if checked[selector] {
			continue
		}
		checked[selector] = true
		if !strings.Contains(html, selector) {
			t.Errorf("payload queries [class*=%q] but the panel DOM has no class containing it — the injected selector would never match", selector)
		}
	}
	t.Logf("checked %d distinct panel class prefixes in %s", len(checked), panelPath)
}

// -----------------------------------------------------------------------------
// Credential refresh
// -----------------------------------------------------------------------------

// callManagementRoute posts a request to one management path and returns the
// handler's response. The path is given plugin-relative, as the console's own
// constants are, and is prefixed the way the host prefixes it.
func callManagementRoute(t *testing.T, path string, body any) mgmtResponse {
	t.Helper()
	request := mgmtRequest{Method: http.MethodPost, Path: "/v0/management" + path}
	if body != nil {
		encoded, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			t.Fatalf("marshal request body: %v", errMarshal)
		}
		request.Body = encoded
	}
	raw, errRequest := json.Marshal(request)
	if errRequest != nil {
		t.Fatalf("marshal management request: %v", errRequest)
	}
	answer, errHandle := handleManagementHandle(raw)
	if errHandle != nil {
		t.Fatalf("handleManagementHandle: %v", errHandle)
	}
	var env struct {
		OK     bool         `json:"ok"`
		Result mgmtResponse `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(answer, &env); errUnmarshal != nil {
		t.Fatalf("decode management envelope: %v", errUnmarshal)
	}
	if !env.OK {
		t.Fatalf("management envelope reported failure: %s", answer)
	}
	return env.Result
}

// TestAuthDataPublishesExpiryAndRefreshInterval pins the two metadata keys the
// host needs before it will ever refresh this provider. Losing either one is
// silent: the account keeps working until its access token lapses, and then
// every request fails with nothing left to rotate the credential.
func TestAuthDataPublishesExpiryAndRefreshInterval(t *testing.T) {
	sa := liveCredential(t)
	data := toAuthData(sa)

	rawInterval, present := data.Metadata["refresh_interval_seconds"]
	if !present {
		t.Fatal("auth data carries no refresh_interval_seconds: the host never schedules a refresh for this provider")
	}
	interval, isInt := rawInterval.(int)
	if !isInt || interval <= 0 {
		t.Fatalf("refresh_interval_seconds = %#v, want a positive number of seconds", rawInterval)
	}
	// Once a refresh lands the host re-refreshes every interval, so an interval
	// at or past the token lifetime would refresh on every single check.
	if interval >= int((60 * 24 * time.Hour).Seconds()) {
		t.Errorf("refresh interval %ds is not comfortably inside the ~60 day token lifetime", interval)
	}

	published, present := data.Metadata["expired"].(string)
	if !present {
		t.Fatal("auth data carries no expired timestamp: the host cannot time the refresh")
	}
	expires, errParse := time.Parse(time.RFC3339, published)
	if errParse != nil {
		t.Fatalf("expired %q is not RFC3339: %v", published, errParse)
	}
	if expires.Unix() != sa.Auth.ExpiresAt {
		t.Errorf("published expiry = %d, want the credential's own %d", expires.Unix(), sa.Auth.ExpiresAt)
	}
	if data.Metadata["type"] != providerName {
		t.Errorf("metadata type = %v, want %q", data.Metadata["type"], providerName)
	}

	// last_refresh is what stops the host rotating the token again on every
	// restart. It is only publishable once the credential records when its token
	// was issued, so a credential written before that field existed simply omits
	// it and gets one more rotation — the next one records the time for good.
	if rawIssued, present := sa.Auth.IssuedAt, sa.Auth.IssuedAt > 0; present {
		publishedRefresh, ok := data.Metadata["last_refresh"].(string)
		if !ok {
			t.Fatalf("credential records issuedAt=%d but no last_refresh was published", rawIssued)
		}
		issued, errIssued := time.Parse(time.RFC3339, publishedRefresh)
		if errIssued != nil {
			t.Fatalf("last_refresh %q is not RFC3339: %v", publishedRefresh, errIssued)
		}
		if issued.Unix() != rawIssued {
			t.Errorf("last_refresh = %d, want the credential's issuedAt %d", issued.Unix(), rawIssued)
		}
	} else if _, present := data.Metadata["last_refresh"]; present {
		t.Error("last_refresh was published for a credential that records no issue time")
	}
}

func testStoredAccount(uid, nickname string) *storedAuth {
	return &storedAuth{
		Auth:    storedTokens{AccessToken: "tok", RefreshToken: "ref", ExpiresAt: 4102444800, Domain: "copilot.tencent.com"},
		Account: storedAccount{UID: uid, Nickname: nickname},
	}
}

func decodeAuthParse(t *testing.T, raw []byte) pluginapi.AuthParseResponse {
	t.Helper()
	var env struct {
		OK     bool                        `json:"ok"`
		Result pluginapi.AuthParseResponse `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode parse envelope: %v (%s)", err, raw)
	}
	if !env.OK {
		t.Fatalf("parse envelope reported failure: %s", raw)
	}
	return env.Result
}

func decodeAuthRefresh(t *testing.T, raw []byte) pluginapi.AuthRefreshResponse {
	t.Helper()
	var env struct {
		OK     bool                          `json:"ok"`
		Result pluginapi.AuthRefreshResponse `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode refresh envelope: %v (%s)", err, raw)
	}
	if !env.OK {
		t.Fatalf("refresh envelope reported failure: %s", raw)
	}
	return env.Result
}

// TestLoginAuthDataNamesFilePerAccount is why a second scan-login must not
// overwrite the first: the host persists AuthData.FileName as the auth file,
// and AuthData.ID as the in-memory key. A constant workbuddy.json / workbuddy
// identity collapses every Buddy account onto one slot.
func TestLoginAuthDataNamesFilePerAccount(t *testing.T) {
	first := toAuthData(testStoredAccount("111", "甲"))
	second := toAuthData(testStoredAccount("222", "乙"))

	if first.FileName != "workbuddy-111.json" {
		t.Errorf("first FileName = %q, want workbuddy-111.json", first.FileName)
	}
	if second.FileName != "workbuddy-222.json" {
		t.Errorf("second FileName = %q, want workbuddy-222.json", second.FileName)
	}
	if first.ID != first.FileName || second.ID != second.FileName {
		t.Errorf("IDs %q / %q should match their file names", first.ID, second.ID)
	}
	if first.ID == second.ID {
		t.Fatal("two accounts received the same auth ID; the host will keep only one")
	}
	if first.Label != "甲" || second.Label != "乙" {
		t.Errorf("labels = %q / %q, want the account nicknames", first.Label, second.Label)
	}
}

func TestLoginAuthDataFallsBackToUIDWhenNicknameMissing(t *testing.T) {
	data := toAuthData(testStoredAccount("56657220", ""))
	if data.Label != "56657220" {
		t.Errorf("Label = %q, want the uid when nickname is empty", data.Label)
	}
}

func TestLoginAuthDataReusesExistingFileForSameUID(t *testing.T) {
	hostWithTwoAccounts(t)

	data := toAuthData(testStoredAccount("test-uid", "tester"))
	if data.FileName != "workbuddy-a.json" {
		t.Errorf("FileName = %q, want workbuddy-a.json so a re-login updates the existing file", data.FileName)
	}
	if data.ID != "workbuddy-a.json" {
		t.Errorf("ID = %q, want the existing file's identity", data.ID)
	}
}

func TestParseAuthKeepsOnDiskFileName(t *testing.T) {
	storage, err := json.Marshal(testStoredAccount("111", "甲"))
	if err != nil {
		t.Fatalf("marshal credential: %v", err)
	}
	request, err := json.Marshal(pluginapi.AuthParseRequest{
		Provider: providerName,
		FileName: "workbuddy.json",
		RawJSON:  storage,
	})
	if err != nil {
		t.Fatalf("marshal parse request: %v", err)
	}
	raw, errHandle := handleParseAuth(request)
	if errHandle != nil {
		t.Fatalf("handleParseAuth: %v", errHandle)
	}
	parsed := decodeAuthParse(t, raw)
	if !parsed.Handled {
		t.Fatal("parse did not claim a workbuddy credential")
	}
	if parsed.Auth.FileName != "workbuddy.json" {
		t.Errorf("FileName = %q, want the on-disk name so an existing first account is not renamed", parsed.Auth.FileName)
	}
	if parsed.Auth.ID != "workbuddy.json" {
		t.Errorf("ID = %q, want the on-disk file name", parsed.Auth.ID)
	}
	if parsed.Auth.Label != "甲" {
		t.Errorf("Label = %q, want 甲", parsed.Auth.Label)
	}
}

func TestRefreshAuthLeavesFileIdentityToTheHost(t *testing.T) {
	sa, err := parseStored([]byte(testCredentialJSON))
	if err != nil {
		t.Fatalf("parse test credential: %v", err)
	}
	storage, err := json.Marshal(sa)
	if err != nil {
		t.Fatalf("marshal storage: %v", err)
	}
	withStubTokenRefresh(t, "rotated-access-token", "rotated-refresh-token", 3600)

	request, err := json.Marshal(pluginapi.AuthRefreshRequest{
		AuthID:       "workbuddy-a.json",
		AuthProvider: providerName,
		StorageJSON:  storage,
	})
	if err != nil {
		t.Fatalf("marshal refresh request: %v", err)
	}
	raw, errHandle := handleRefreshAuth(request)
	if errHandle != nil {
		t.Fatalf("handleRefreshAuth: %v", errHandle)
	}
	refreshed := decodeAuthRefresh(t, raw)
	if refreshed.Auth.FileName != "" || refreshed.Auth.ID != "" {
		t.Errorf("refresh returned FileName=%q ID=%q; a non-empty identity can rename the auth file", refreshed.Auth.FileName, refreshed.Auth.ID)
	}
}

// withStubTokenRefresh replaces the refresh transport with one that answers the
// token-rotation call.
func withStubTokenRefresh(t *testing.T, accessToken, refreshToken string, expiresIn int64) *int {
	t.Helper()
	original := tokenRefreshClient
	t.Cleanup(func() { tokenRefreshClient = original })

	calls := 0
	tokenRefreshClient = func() *http.Client {
		return &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
			calls++
			if !strings.HasSuffix(r.URL.Path, "/token/refresh") {
				t.Errorf("refresh hit %s, want the token refresh route", r.URL.Path)
			}
			if got := r.Header.Get("X-Refresh-Token"); got == "" {
				t.Error("refresh was sent without the stored refresh token")
			}
			payload, _ := json.Marshal(map[string]any{
				"code": 0, "msg": "OK",
				"data": map[string]any{
					"accessToken": accessToken, "refreshToken": refreshToken,
					"expiresIn": expiresIn, "domain": "copilot.tencent.com",
				},
			})
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(payload)),
				Request:    r,
			}, nil
		})}
	}
	return &calls
}

// TestRefreshRouteRotatesAndPersists drives the console's refresh button end to
// end: routing, the upstream exchange, and what is written back to the host.
func TestRefreshRouteRotatesAndPersists(t *testing.T) {
	sa := liveCredential(t)
	credential, errMarshal := json.Marshal(map[string]any{
		"type":     providerName,
		"disabled": false,
		"note":     "a field the plugin does not model",
		"account":  map[string]any{"uid": sa.Account.UID, "enterpriseId": sa.Account.EnterpriseID},
		"auth": map[string]any{
			"accessToken": sa.Auth.AccessToken, "refreshToken": sa.Auth.RefreshToken,
			"expiresAt": sa.Auth.ExpiresAt, "domain": sa.Auth.Domain,
		},
	})
	if errMarshal != nil {
		t.Fatalf("marshal credential: %v", errMarshal)
	}
	saved := withFakeHost(t, credential, providerName)
	calls := withStubTokenRefresh(t, "rotated-access-token", "rotated-refresh-token", 3600)

	response := callManagementRoute(t, mgmtRefreshPath, map[string]any{"auth_index": "test-auth-index"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("refresh status = %d body=%s", response.StatusCode, truncate(string(response.Body), 200))
	}
	var payload refreshEnvelope
	if errUnmarshal := json.Unmarshal(response.Body, &payload); errUnmarshal != nil {
		t.Fatalf("decode refresh response: %v (%s)", errUnmarshal, response.Body)
	}
	if !payload.OK || payload.ExpiresAt == "" {
		t.Errorf("refresh response = %+v, want ok with an expiry", payload)
	}
	if *calls != 1 {
		t.Errorf("token refresh calls = %d, want exactly 1", *calls)
	}

	if len(*saved) != 1 {
		t.Fatalf("host.auth.save calls = %d, want 1 — a rotated token that is never persisted is lost on restart", len(*saved))
	}
	write := (*saved)[0]
	if write.Name != authFileName {
		t.Errorf("saved to %q, want %q", write.Name, authFileName)
	}
	var document map[string]any
	if errUnmarshal := json.Unmarshal(write.JSON, &document); errUnmarshal != nil {
		t.Fatalf("saved payload is not JSON: %v", errUnmarshal)
	}
	section, _ := document["auth"].(map[string]any)
	if section == nil {
		t.Fatalf("saved payload has no auth section: %s", write.JSON)
	}
	if section["accessToken"] != "rotated-access-token" {
		t.Errorf("saved accessToken = %v, want the rotated one", section["accessToken"])
	}
	if section["refreshToken"] != "rotated-refresh-token" {
		t.Errorf("saved refreshToken = %v, want the rotated one", section["refreshToken"])
	}
	if expiresAt, _ := section["expiresAt"].(float64); expiresAt <= float64(time.Now().Unix()) {
		t.Errorf("saved expiresAt = %v, want a future timestamp", section["expiresAt"])
	}
	// The document's own expiry marker drives the host's refresh timing, so it
	// has to describe the token that was just written, not the previous one.
	savedExpiry, _ := document["expired"].(string)
	parsedExpiry, errParse := time.Parse(time.RFC3339, savedExpiry)
	if errParse != nil {
		t.Errorf("saved expired = %q, want RFC3339: %v", savedExpiry, errParse)
	} else if expiresAt, _ := section["expiresAt"].(float64); parsedExpiry.Unix() != int64(expiresAt) {
		t.Errorf("saved expired = %d but auth.expiresAt = %d; the file contradicts itself", parsedExpiry.Unix(), int64(expiresAt))
	}
	if _, ok := document["refresh_interval_seconds"]; !ok {
		t.Errorf("saved payload dropped refresh_interval_seconds, so the host would stop scheduling refreshes: %s", write.JSON)
	}
	// Without this the host cannot tell the rotated token from a never-refreshed
	// one, and rotates again on the next restart.
	issuedAt, _ := section["issuedAt"].(float64)
	if issuedAt <= 0 {
		t.Errorf("saved payload does not record when the token was issued: %s", write.JSON)
	}
	if savedRefresh, _ := document["last_refresh"].(string); savedRefresh == "" {
		t.Errorf("saved payload does not publish last_refresh: %s", write.JSON)
	}
	// The plugin only models auth and account; a merge that re-encoded its own
	// struct would drop these, and losing "type" makes the host stop treating
	// the file as this provider at all.
	if document["type"] != providerName {
		t.Errorf("saved type = %v, want %q kept from the original document", document["type"], providerName)
	}
	if document["note"] != "a field the plugin does not model" {
		t.Errorf("saved payload dropped an unmodelled field: %s", write.JSON)
	}
	if _, ok := document["disabled"]; !ok {
		t.Errorf("saved payload dropped disabled: %s", write.JSON)
	}
}

// TestRefreshRouteRejectsUnknownAccount keeps a stale button, or a hand-made
// request, from rotating the wrong credential.
func TestRefreshRouteRejectsUnknownAccount(t *testing.T) {
	withFakeHost(t, json.RawMessage(testCredentialJSON), providerName)
	calls := withStubTokenRefresh(t, "should-not-be-used", "", 3600)

	response := callManagementRoute(t, mgmtRefreshPath, map[string]any{"auth_index": "no-such-index"})
	if response.StatusCode == http.StatusOK {
		t.Errorf("refresh of an unknown account reported success: %s", response.Body)
	}
	if *calls != 0 {
		t.Errorf("token refresh calls = %d, want 0 for an unknown account", *calls)
	}
}

// TestRefreshRouteSurfacesUpstreamRejection covers the case an operator can act
// on: the upstream refuses to rotate, so the console must not claim success.
func TestRefreshRouteSurfacesUpstreamRejection(t *testing.T) {
	saved := withFakeHost(t, json.RawMessage(testCredentialJSON), providerName)

	original := tokenRefreshClient
	t.Cleanup(func() { tokenRefreshClient = original })
	tokenRefreshClient = func() *http.Client {
		return &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 401,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"code":10004,"msg":"invalid refresh token"}`)),
				Request:    r,
			}, nil
		})}
	}

	response := callManagementRoute(t, mgmtRefreshPath, map[string]any{"auth_index": "test-auth-index"})
	if response.StatusCode == http.StatusOK {
		t.Errorf("a rejected refresh reported success: %s", response.Body)
	}
	if len(*saved) != 0 {
		t.Errorf("a rejected refresh still wrote %d credential file(s)", len(*saved))
	}
}

// TestSaveCredentialRefusesUnnamedCredential pins the guard: the host save API
// is keyed by file name, and a credential reachable only by runtime index must
// not be written to an invented file.
func TestSaveCredentialRefusesUnnamedCredential(t *testing.T) {
	withFakeHost(t, json.RawMessage(testCredentialJSON), providerName)
	sa := liveCredential(t)

	errSave := saveCredential(pluginapi.HostAuthFileEntry{AuthIndex: "test-auth-index", Name: "not-a-json-name"}, sa)
	if errSave == nil {
		t.Fatal("saveCredential accepted an auth file name without a .json suffix")
	}
}

// TestRefreshEndpointIsRegistered guards the route against a copy-paste slip:
// the console posts to it, and an unregistered path answers 404 with no hint
// that the plugin never advertised it.
func TestRefreshEndpointIsRegistered(t *testing.T) {
	raw, errRegister := handleManagementRegister()
	if errRegister != nil {
		t.Fatalf("handleManagementRegister: %v", errRegister)
	}
	var env struct {
		OK     bool             `json:"ok"`
		Result mgmtRegistration `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		t.Fatalf("decode registration: %v", errUnmarshal)
	}
	if !env.OK {
		t.Fatalf("registration envelope reported failure: %s", raw)
	}
	for _, route := range env.Result.Routes {
		if route.Path == mgmtRefreshPath {
			if route.Method != http.MethodPost {
				t.Errorf("refresh route method = %s, want POST", route.Method)
			}
			return
		}
	}
	t.Errorf("refresh route %s is not advertised to the host", mgmtRefreshPath)
}
