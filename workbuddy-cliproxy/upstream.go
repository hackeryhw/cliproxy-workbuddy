// Upstream service calls for the workbuddy plugin.
//
// This file owns every CodeBuddy endpoint that is not the chat completion
// itself: the live model catalogue (/v3/config), the credit meter, and the
// daily check-in pair. The chat path and the auth lifecycle stay in main.go.
//
// Endpoint notes verified against WorkBuddy 5.5.6 on 2026-09-11:
//   - /v3/config is GET and returns the account's full model catalogue under
//     data.models, plus the agent-scoped subset under data.agents[].models.
//   - The billing routes live on the bare /billing prefix (no /v2) and are
//     POST-only; GET answers 404 from the openresty router.
//   - A failed daily check-in is a business code, not an HTTP status: claiming
//     twice in one day answers HTTP 400 with code 10001.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// -----------------------------------------------------------------------------
// Host RPC helper
// -----------------------------------------------------------------------------

// hostEnvelope mirrors the host's RPC reply wrapper. hostCall returns this
// whole envelope, so every caller would otherwise have to unwrap result itself.
type hostEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *envelopeError  `json:"error"`
}

// hostTransport is the single seam through which this plugin reaches the host.
// Production points it at hostCall (the captured C function-pointer table);
// tests replace it with a stub so the management console's credential lookup
// and dispatch can be exercised without a live plugin host.
var hostTransport = hostCall

// hostCallResult invokes a host RPC and decodes its result payload into out.
func hostCallResult(method string, request []byte, out any) error {
	raw, err := hostTransport(method, request)
	if err != nil {
		return err
	}
	var env hostEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%s: decode envelope: %w", method, err)
	}
	if !env.OK {
		if env.Error != nil {
			return fmt.Errorf("%s: %s", method, env.Error.Message)
		}
		return fmt.Errorf("%s: host returned ok=false", method)
	}
	if out == nil || len(env.Result) == 0 {
		return nil
	}
	return json.Unmarshal(env.Result, out)
}

// -----------------------------------------------------------------------------
// Credential lookup (used by the management console)
// -----------------------------------------------------------------------------

// authFileListResponse is the host's reply to host.auth.list.
type authFileListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

// listWorkbuddyEntries returns every host credential that parses as a
// workbuddy credential, in host order (name-sorted).
//
// The host's provider label is preferred but not required: parseStored is the
// real gate, since it demands an accessToken. Relying on the label alone would
// break the console if the host ever renamed its providers or stopped
// populating the field.
func listWorkbuddyEntries() ([]pluginapi.HostAuthFileEntry, error) {
	var list authFileListResponse
	if err := hostCallResult(pluginabi.MethodHostAuthList, nil, &list); err != nil {
		return nil, fmt.Errorf("host.auth.list: %w", err)
	}

	labelled := make([]pluginapi.HostAuthFileEntry, 0, len(list.Files))
	other := make([]pluginapi.HostAuthFileEntry, 0, len(list.Files))
	for _, entry := range list.Files {
		if entry.Disabled || entry.Unavailable || strings.TrimSpace(entry.AuthIndex) == "" {
			continue
		}
		if entry.Provider == providerName {
			labelled = append(labelled, entry)
			continue
		}
		other = append(other, entry)
	}

	usable := make([]pluginapi.HostAuthFileEntry, 0, len(labelled))
	for _, group := range [][]pluginapi.HostAuthFileEntry{labelled, other} {
		for _, entry := range group {
			raw, err := credentialJSON(entry.AuthIndex)
			if err != nil {
				continue
			}
			if _, err := parseStored(raw); err != nil {
				continue
			}
			usable = append(usable, entry)
		}
	}
	return usable, nil
}

// credentialForEntry resolves one host entry into its parsed credential.
func credentialForEntry(entry pluginapi.HostAuthFileEntry) (*storedAuth, error) {
	raw, err := credentialJSON(entry.AuthIndex)
	if err != nil {
		return nil, err
	}
	return parseStored(raw)
}

// activeCredential returns the credential to act on when the caller did not
// name one. It is the credential half of activeTarget, kept for callers that
// only need the parsed credential.
func activeCredential() (*storedAuth, string, error) {
	target, err := activeTarget()
	if err != nil {
		return nil, "", err
	}
	return target.Credential, target.Label, nil
}

// credentialJSON fetches one credential's persisted JSON by auth index.
func credentialJSON(authIndex string) (json.RawMessage, error) {
	if strings.TrimSpace(authIndex) == "" {
		return nil, fmt.Errorf("empty auth index")
	}
	body, _ := json.Marshal(map[string]string{"auth_index": authIndex})
	var resp pluginapi.HostAuthGetResponse
	if err := hostCallResult(pluginabi.MethodHostAuthGet, body, &resp); err != nil {
		return nil, err
	}
	if len(resp.JSON) == 0 {
		return nil, fmt.Errorf("credential %s has no JSON payload", authIndex)
	}
	return resp.JSON, nil
}

func credentialLabel(entry pluginapi.HostAuthFileEntry) string {
	switch {
	case entry.Label != "":
		return entry.Label
	case entry.Account != "":
		return entry.Account
	case entry.Name != "":
		return entry.Name
	default:
		return entry.AuthIndex
	}
}

// -----------------------------------------------------------------------------
// Shared billing request plumbing
// -----------------------------------------------------------------------------

// billingHTTPClient is the transport seam for the billing surface. Production
// points it at the shared client; tests replace it with a canned transport so
// the claim branches — including the success path a user only hits once a day —
// can be exercised without touching the network.
var billingHTTPClient = sharedHTTPClient

// billingResult keeps the upstream business envelope intact so callers can
// relay Tencent's own message (for example the "already checked in" notice)
// instead of a generic transport error.
type billingResult struct {
	HTTPStatus int
	Code       int
	Msg        string
	Data       json.RawMessage
	Raw        json.RawMessage
}

// callBillingEndpoint POSTs an empty JSON object to one billing route. It
// deliberately does not treat a non-zero business code as a Go error: a claim
// that already happened today is a normal outcome the console must display.
func callBillingEndpoint(sa *storedAuth, endpoint string) (*billingResult, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, err
	}
	applyBillingHeaders(req, sa)

	resp, err := billingHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("http_error: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	result := &billingResult{HTTPStatus: resp.StatusCode, Raw: json.RawMessage(raw)}

	var env struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err == nil {
		result.Code = env.Code
		result.Msg = env.Msg
		result.Data = env.Data
	} else if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	return result, nil
}

// applyBillingHeaders mirrors the headers the WorkBuddy desktop client uses for
// the billing surface. X-Domain is load-bearing: without it the APISIX gateway
// in front of copilot.tencent.com can refuse the request.
func applyBillingHeaders(req *http.Request, sa *storedAuth) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "zh")
	req.Header.Set("User-Agent", clientUA)
	req.Header.Set("X-Domain", "copilot.tencent.com")
	if sa != nil && sa.Auth.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+sa.Auth.AccessToken)
	}
	if sa != nil && sa.Account.UID != "" {
		req.Header.Set("X-User-Id", sa.Account.UID)
	}
}

// -----------------------------------------------------------------------------
// Credit meter + daily check-in
// -----------------------------------------------------------------------------

// queryQuota reads the account's credit packages.
func queryQuota(sa *storedAuth) (*billingResult, error) {
	return callBillingEndpoint(sa, endpointQuota)
}

// queryCheckinStatus reads today's check-in state. Read-only: safe to call on
// every console page load.
func queryCheckinStatus(sa *storedAuth) (*billingResult, error) {
	return callBillingEndpoint(sa, endpointCheckinStatus)
}

// claimCheckin performs the daily check-in. Callers should gate this behind a
// status read so an already-claimed day is reported, not attempted.
func claimCheckin(sa *storedAuth) (*billingResult, error) {
	return callBillingEndpoint(sa, endpointCheckinClaim)
}

// -----------------------------------------------------------------------------
// Live model catalogue
// -----------------------------------------------------------------------------

// configModel mirrors one entry of data.models in /v3/config.
type configModel struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Credits          string `json:"credits"`
	MaxInputTokens   int64  `json:"maxInputTokens"`
	MaxOutputTokens  int64  `json:"maxOutputTokens"`
	OnlyReasoning    bool   `json:"onlyReasoning"`
	SupportsImages   bool   `json:"supportsImages"`
	SupportsToolCall bool   `json:"supportsToolCall"`
}

// configPayload is the INNER data object of /v3/config.
//
// doJSON already unwraps the {code,msg,data} envelope and hands back the inner
// payload, so this type must not expect another "data" wrapper: doing so
// silently parsed to zero models and degraded the whole feature to the static
// fallback list.
type configPayload struct {
	Models []configModel `json:"models"`
	Agents []struct {
		Name   string   `json:"name"`
		Models []string `json:"models"`
	} `json:"agents"`
}

type configCacheEntry struct {
	models  []pluginapi.ModelInfo
	expires time.Time
}

var (
	configCacheMu sync.Mutex
	configCache   = map[string]configCacheEntry{}
)

// fetchConfigModels reads the account's live model catalogue.
//
// The agent-scoped list (data.agents[].models) is authoritative for what this
// account may actually dispatch, so those ids lead the result. The wider
// catalogue follows, filtered down to conversational entries, and the built-in
// static list backfills anything the catalogue omits (legacy aliases that still
// route upstream). Cached briefly: model discovery runs on every credential
// change and would otherwise hammer the upstream.
func fetchConfigModels(sa *storedAuth) ([]pluginapi.ModelInfo, error) {
	if sa == nil || strings.TrimSpace(sa.Auth.AccessToken) == "" {
		return nil, fmt.Errorf("no access token")
	}

	cacheKey := sa.Account.UID
	if cacheKey == "" {
		cacheKey = "default"
	}
	configCacheMu.Lock()
	if entry, ok := configCache[cacheKey]; ok && time.Now().Before(entry.expires) {
		models := entry.models
		configCacheMu.Unlock()
		return models, nil
	}
	configCacheMu.Unlock()

	data, _, err := doJSON(sharedHTTPClient(), http.MethodGet, endpointConfig, func(r *http.Request) {
		backendHeaders(r, sa)
	}, nil)
	if err != nil {
		return nil, err
	}

	var cfg configPayload
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("decode /v3/config: %w", err)
	}

	catalogue := make(map[string]configModel, len(cfg.Models))
	for _, m := range cfg.Models {
		if m.ID != "" {
			catalogue[m.ID] = m
		}
	}

	seen := make(map[string]bool)
	models := make([]pluginapi.ModelInfo, 0, len(catalogue)+len(cfg.Agents))

	appendModel := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		models = append(models, buildModelInfo(id, catalogue[id]))
	}

	// 1. Agent-scoped models: exactly what this credential may dispatch.
	for _, agent := range cfg.Agents {
		for _, id := range agent.Models {
			appendModel(id)
		}
	}
	// 2. The rest of the catalogue, minus non-conversational entries.
	for _, m := range cfg.Models {
		if !isConversational(m) {
			continue
		}
		appendModel(m.ID)
	}
	// 3. Static fallback ids missing from the catalogue (legacy aliases).
	for _, known := range wbModels() {
		appendModel(known.ID)
	}

	configCacheMu.Lock()
	configCache[cacheKey] = configCacheEntry{models: models, expires: time.Now().Add(configTTL)}
	configCacheMu.Unlock()

	return models, nil
}

// isConversational filters non-chat entries out of the catalogue.
//
// Dropped here: image generators, editor completion/jump helpers, and entries
// carrying neither tool calling nor an input budget. The catalogue also lists
// several tencent-internal deployment aliases ("default-1.x" Claude shims,
// "-lkeap"/"-taiji" regional endpoints, "completion-*") that a chat client
// should not be offered.
func isConversational(m configModel) bool {
	lower := strings.ToLower(m.ID)

	for _, needle := range []string{"image", "lkeap", "taiji"} {
		if strings.Contains(lower, needle) {
			return false
		}
	}
	for _, prefix := range []string{"codewise-", "completion-", "default-"} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	if !m.SupportsToolCall && m.MaxInputTokens == 0 {
		return false
	}
	return true
}

// buildModelInfo renders one catalogue entry as host model metadata. When the
// catalogue is silent about an id (legacy alias) the built-in static entry
// supplies the display name and context length.
func buildModelInfo(id string, entry configModel) pluginapi.ModelInfo {
	const fallbackCompletion int64 = 8192

	display := entry.Name
	context := entry.MaxInputTokens
	completion := entry.MaxOutputTokens

	if display == "" || context == 0 {
		for _, known := range wbModels() {
			if known.ID != id {
				continue
			}
			if display == "" {
				display = known.DisplayName
			}
			if context == 0 {
				context = known.ContextLength
			}
			break
		}
	}
	if display == "" {
		display = id
	}
	// Only fill in a default when the catalogue is silent. Upstream is the
	// authority on limits: clamping to a guessed ceiling silently under-reported
	// real models (hy4-preview-f advertises 64000 output tokens, and MiniMax M3
	// and DeepSeek V4.1 Flash advertise 128000).
	if completion == 0 {
		completion = fallbackCompletion
	}
	if entry.Credits != "" {
		display = display + " · " + entry.Credits
	}

	return pluginapi.ModelInfo{
		ID:                         id,
		Object:                     "model",
		OwnedBy:                    providerName,
		DisplayName:                display,
		Name:                       id,
		SupportedGenerationMethods: []string{"chat"},
		ContextLength:              context,
		MaxCompletionTokens:        completion,
		InputTokenLimit:            context,
		OutputTokenLimit:           completion,
		UserDefined:                true,
	}
}

// -----------------------------------------------------------------------------
// Host logging
// -----------------------------------------------------------------------------

// pluginLog writes a line into the host log through host.log.
//
// callbackID (when non-empty) ties the line to the management request that
// triggered it, so a console click leaves a trace next to its own request in
// the CPA log. Failures are swallowed: logging must never break a user action.
func pluginLog(level, callbackID, message string, fields map[string]any) {
	payload := map[string]any{
		"level":   level,
		"message": message,
	}
	if callbackID != "" {
		payload["host_callback_id"] = callbackID
	}
	if len(fields) > 0 {
		payload["fields"] = fields
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = hostTransport(pluginabi.MethodHostLog, body)
}

// -----------------------------------------------------------------------------
// Per-account reporting for the control panel
// -----------------------------------------------------------------------------

// creditSummary is the credit position the panel shows on one account card.
type creditSummary struct {
	Total  float64 `json:"total"`
	Remain float64 `json:"remain"`
	Used   float64 `json:"used"`
	Unit   string  `json:"unit,omitempty"`
	IsPaid bool    `json:"is_paid"`
}

// accountReport is one workbuddy account, flattened for the panel.
//
// A per-account failure is reported in Error rather than failing the whole
// list: one expired credential must not blank the other cards.
type accountReport struct {
	AuthIndex      string         `json:"auth_index"`
	Name           string         `json:"name,omitempty"`
	Label          string         `json:"label,omitempty"`
	UID            string         `json:"uid,omitempty"`
	CheckedInToday bool           `json:"checked_in_today"`
	StreakDays     int            `json:"streak_days"`
	DailyCredit    int            `json:"daily_credit"`
	TodayCredit    int            `json:"today_credit"`
	TotalCredits   int            `json:"total_credits"`
	ActivityName       string         `json:"activity_name,omitempty"`
	GrowthTasksPending int            `json:"growth_tasks_pending,omitempty"`
	GrowthTasksAllDone bool           `json:"growth_tasks_all_done"`
	Credits            *creditSummary `json:"credits,omitempty"`
	Error              string         `json:"error,omitempty"`
}

// quotaPayload mirrors the credit summary payload.
type quotaPayload struct {
	Packages []struct {
		PackageCode         string `json:"PackageCode"`
		CycleTotalCapacity  string `json:"CycleTotalCapacity"`
		CycleRemainCapacity string `json:"CycleRemainCapacity"`
		CycleUsedCapacity   string `json:"CycleUsedCapacity"`
		CapacityUnit        string `json:"CapacityUnit"`
	} `json:"Packages"`
	IsPaidUser bool `json:"IsPaidUser"`
}

// checkinPayload mirrors the check-in status payload.
type checkinPayload struct {
	TodayCheckedIn bool   `json:"today_checked_in"`
	StreakDays     int    `json:"streak_days"`
	DailyCredit    int    `json:"daily_credit"`
	TodayCredit    int    `json:"today_credit"`
	TotalCredits   int    `json:"total_credits"`
	ActivityName   string `json:"activity_name"`
}

// capacityValue parses one capacity string. Upstream sends decimals as strings
// (for example "482.47000001"); a value that does not parse counts as zero
// rather than aborting the whole card.
func capacityValue(raw string) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0
	}
	return value
}

// summarizeCredits folds the package list into one balance.
func summarizeCredits(payload json.RawMessage) *creditSummary {
	var quota quotaPayload
	if len(payload) == 0 || json.Unmarshal(payload, &quota) != nil {
		return nil
	}
	summary := &creditSummary{IsPaid: quota.IsPaidUser}
	for _, pkg := range quota.Packages {
		summary.Total += capacityValue(pkg.CycleTotalCapacity)
		summary.Remain += capacityValue(pkg.CycleRemainCapacity)
		summary.Used += capacityValue(pkg.CycleUsedCapacity)
		if summary.Unit == "" {
			summary.Unit = strings.TrimSpace(pkg.CapacityUnit)
		}
	}
	return summary
}

// buildAccountReport gathers one account's credit balance and check-in state.
// Both upstream calls are attempted independently so a failing check-in
// endpoint still leaves the balance visible.
func buildAccountReport(entry pluginapi.HostAuthFileEntry) accountReport {
	report := accountReport{
		AuthIndex: entry.AuthIndex,
		Name:      entry.Name,
		Label:     credentialLabel(entry),
	}

	sa, errCredential := credentialForEntry(entry)
	if errCredential != nil {
		report.Error = errCredential.Error()
		return report
	}
	report.UID = sa.Account.UID

	// The upstream calls are independent, so run them together: the panel
	// polls this on a timer and a serial trio visibly delays the cards.
	var (
		quotaResult        *billingResult
		quotaErr           error
		checkinResult      *billingResult
		checkinErr         error
		growthPending      int
		growthAllDone      bool
		growthErr          error
		wg                 sync.WaitGroup
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		quotaResult, quotaErr = queryQuota(sa)
	}()
	go func() {
		defer wg.Done()
		checkinResult, checkinErr = queryCheckinStatus(sa)
	}()
	go func() {
		defer wg.Done()
		growthPending, growthAllDone, growthErr = growthAutoTasksStatus(sa)
	}()
	wg.Wait()

	report.GrowthTasksPending = growthPending
	report.GrowthTasksAllDone = growthAllDone

	problems := make([]string, 0, 3)

	switch {
	case quotaErr != nil:
		problems = append(problems, "quota: "+quotaErr.Error())
	case quotaResult.Code == 0:
		report.Credits = summarizeCredits(quotaResult.Data)
	default:
		problems = append(problems, "quota: "+quotaResult.Msg)
	}

	switch {
	case checkinErr != nil:
		problems = append(problems, "checkin: "+checkinErr.Error())
	case checkinResult.Code == 0:
		var checkin checkinPayload
		if json.Unmarshal(checkinResult.Data, &checkin) == nil {
			report.CheckedInToday = checkin.TodayCheckedIn
			report.StreakDays = checkin.StreakDays
			report.DailyCredit = checkin.DailyCredit
			report.TodayCredit = checkin.TodayCredit
			report.TotalCredits = checkin.TotalCredits
			report.ActivityName = checkin.ActivityName
		}
	default:
		problems = append(problems, "checkin: "+checkinResult.Msg)
	}

	if growthErr != nil {
		problems = append(problems, "growth_tasks: "+growthErr.Error())
	}

	if len(problems) > 0 {
		report.Error = strings.Join(problems, "; ")
	}
	return report
}

// accountSummary is the cheap half of an account: everything the panel can show
// without touching the network. Rendering shells from this and fetching credits
// on demand keeps the page responsive, because a credit lookup costs two
// upstream round trips.
type accountSummary struct {
	AuthIndex string `json:"auth_index"`
	Name      string `json:"name,omitempty"`
	Label     string `json:"label,omitempty"`
	UID       string `json:"uid,omitempty"`
}

// listAccountSummaries returns every usable account without any upstream call.
func listAccountSummaries() ([]accountSummary, error) {
	entries, err := listWorkbuddyEntries()
	if err != nil {
		return nil, err
	}
	// listWorkbuddyEntries already resolved and parsed each credential, so no
	// further host call is needed here — this stays a pure local fan-out.
	out := make([]accountSummary, 0, len(entries))
	for _, entry := range entries {
		out = append(out, accountSummary{
			AuthIndex: entry.AuthIndex,
			Name:      entry.Name,
			Label:     credentialLabel(entry),
		})
	}
	return out, nil
}

// collectAccounts reports the expensive half — credits and check-in state — for
// the named account, or for every account when authIndex is empty.
//
// Accounts are gathered concurrently: each one costs two upstream round trips,
// so a serial loop scales the latency linearly with the number of credentials.
func collectAccounts(authIndex string) ([]accountReport, error) {
	entries, err := listWorkbuddyEntries()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(authIndex) != "" {
		filtered := entries[:0:0]
		for _, entry := range entries {
			if entry.AuthIndex == authIndex {
				filtered = append(filtered, entry)
			}
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("no usable %s credential for auth_index %s", providerName, authIndex)
		}
		entries = filtered
	}
	reports := make([]accountReport, len(entries))
	var wg sync.WaitGroup
	for i, entry := range entries {
		wg.Add(1)
		go func(idx int, e pluginapi.HostAuthFileEntry) {
			defer wg.Done()
			reports[idx] = buildAccountReport(e)
		}(i, entry)
	}
	wg.Wait()
	return reports, nil
}

// -----------------------------------------------------------------------------
// Persisting a refreshed credential
// -----------------------------------------------------------------------------

// saveCredential writes a refreshed credential back through the host so the new
// token survives a restart and is picked up by the running instance.
//
// The document is merged rather than re-encoded from storedAuth: the plugin only
// models the auth and account sections, so marshalling its own struct would drop
// every other key — including "type", whose loss makes the host stop recognising
// the credential as this provider at all.
func saveCredential(entry pluginapi.HostAuthFileEntry, updated *storedAuth) error {
	if updated == nil {
		return fmt.Errorf("save: no credential")
	}
	name := strings.TrimSpace(entry.Name)
	if name == "" || !strings.HasSuffix(strings.ToLower(name), ".json") {
		return fmt.Errorf("save: host reported no auth file name for auth_index %s", entry.AuthIndex)
	}

	document := map[string]any{}
	if raw, errExisting := credentialJSON(entry.AuthIndex); errExisting == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &document)
	}
	if document == nil {
		document = map[string]any{}
	}

	section, _ := document["auth"].(map[string]any)
	if section == nil {
		section = map[string]any{}
	}
	section["accessToken"] = updated.Auth.AccessToken
	section["expiresAt"] = updated.Auth.ExpiresAt
	if updated.Auth.IssuedAt > 0 {
		section["issuedAt"] = updated.Auth.IssuedAt
	}
	if updated.Auth.RefreshToken != "" {
		section["refreshToken"] = updated.Auth.RefreshToken
	}
	if updated.Auth.Domain != "" {
		section["domain"] = updated.Auth.Domain
	}
	document["auth"] = section
	// Keep the document's own markers in step with the new token. The host reads
	// "expired" and "last_refresh" when it decides whether to refresh, and stale
	// values left behind here would describe a token that is no longer on disk —
	// a stale expiry refreshes too eagerly, a stale last_refresh not at all.
	document["expired"] = time.Unix(updated.Auth.ExpiresAt, 0).UTC().Format(time.RFC3339)
	if updated.Auth.IssuedAt > 0 {
		document["last_refresh"] = time.Unix(updated.Auth.IssuedAt, 0).UTC().Format(time.RFC3339)
	}
	document["refresh_interval_seconds"] = refreshIntervalSeconds
	if _, ok := document["type"]; !ok {
		document["type"] = providerName
	}
	if _, ok := document["disabled"]; !ok {
		document["disabled"] = false
	}

	payload, errMarshal := json.Marshal(document)
	if errMarshal != nil {
		return fmt.Errorf("save: encode credential: %w", errMarshal)
	}
	request, errRequest := json.Marshal(pluginapi.HostAuthSaveRequest{Name: name, JSON: payload})
	if errRequest != nil {
		return fmt.Errorf("save: encode request: %w", errRequest)
	}
	if errSave := hostCallResult(pluginabi.MethodHostAuthSave, request, nil); errSave != nil {
		return fmt.Errorf("host.auth.save: %w", errSave)
	}
	return nil
}
