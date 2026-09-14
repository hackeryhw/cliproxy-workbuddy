// Management console for the workbuddy plugin.
//
// The host calls plugin.register; when the plugin advertises the management_api
// capability the host then calls management.register and mounts whatever routes
// come back. Two route families are registered:
//
//   - Management routes under /v0/management/... : authenticated with the
//     management key, and perform the actual work (status, claim, quota).
//   - One resource route under /v0/resource/plugins/workbuddy/checkin : an
//     unauthenticated, GET-only HTML page. It is deliberately read-only; every
//     state change goes back through the authenticated management routes, so an
//     unauthenticated URL can never trigger an upstream action.
//
// The page reads the browser's stored management key ('cpa-management-key',
// the same slot the built-in control panel writes) so a click works without
// retyping the key.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Route paths. Management paths are relative and resolved by the host under
// /v0/management; the resource path is resolved under
// /v0/resource/plugins/<pluginID>.
const (
	mgmtStatusPath        = "/plugins/workbuddy/checkin/status"
	mgmtClaimPath         = "/plugins/workbuddy/checkin/claim"
	mgmtQuotaPath         = "/plugins/workbuddy/quota"
	mgmtRefreshPath       = "/plugins/workbuddy/refresh"
	mgmtAccountsPath      = "/plugins/workbuddy/accounts"
	mgmtAccountStatusPath = "/plugins/workbuddy/accounts/status"
	mgmtTasksAutoPath     = "/plugins/workbuddy/tasks/auto_all"
	consolePagePath       = "/checkin"
)

// -----------------------------------------------------------------------------
// Wire shapes
// -----------------------------------------------------------------------------

// The host marshals pluginapi.ManagementRoute/ResourceRoute with Go's default
// field names (those types carry no json tags), and it installs its own Handler
// after decoding, so this plugin only ever sends the routing metadata.
type mgmtRouteRegistration struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Menu        string `json:"Menu,omitempty"`
	Description string `json:"Description,omitempty"`
}

type mgmtResourceRegistration struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu,omitempty"`
	Description string `json:"Description,omitempty"`
}

type mgmtRegistration struct {
	Routes    []mgmtRouteRegistration    `json:"routes"`
	Resources []mgmtResourceRegistration `json:"resources"`
}

// mgmtRequest mirrors pluginapi.ManagementRequest, whose Body is []byte and
// therefore travels base64-encoded.
type mgmtRequest struct {
	Method  string      `json:"Method"`
	Path    string      `json:"Path"`
	Headers http.Header `json:"Headers"`
	Query   url.Values  `json:"Query"`
	Body    []byte      `json:"Body"`
	// HostCallbackID scopes plugin-initiated host calls (logging) back to the
	// originating request, which is what puts the console's own log lines next
	// to that request in the host log.
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// authIndex reads the target account from the request. The panel passes it as
// a query parameter on links and as a JSON field when posting, so both are
// accepted; an empty result means "whichever account comes first".
func (r mgmtRequest) authIndex() string {
	if r.Query != nil {
		if v := strings.TrimSpace(r.Query.Get("auth_index")); v != "" {
			return v
		}
	}
	if len(r.Body) > 0 {
		var payload struct {
			AuthIndex string `json:"auth_index"`
		}
		if json.Unmarshal(r.Body, &payload) == nil {
			if v := strings.TrimSpace(payload.AuthIndex); v != "" {
				return v
			}
		}
	}
	return ""
}

// mgmtResponse mirrors pluginapi.ManagementResponse.
type mgmtResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

// -----------------------------------------------------------------------------
// RPC entry points
// -----------------------------------------------------------------------------

// handleManagementRegister advertises the console routes to the host.
func handleManagementRegister() ([]byte, error) {
	pluginLog("info", "", "workbuddy console routes registered", map[string]any{
		"routes":    []string{mgmtStatusPath, mgmtClaimPath, mgmtQuotaPath, mgmtRefreshPath, mgmtAccountsPath, mgmtAccountStatusPath, mgmtTasksAutoPath},
		"resources": []string{consolePagePath},
	})
	return okEnvelope(mgmtRegistration{
		Routes: []mgmtRouteRegistration{
			{
				Method:      http.MethodGet,
				Path:        mgmtStatusPath,
				Description: "WorkBuddy credit balance and daily check-in state.",
			},
			{
				Method:      http.MethodPost,
				Path:        mgmtClaimPath,
				Description: "Claim today's WorkBuddy check-in reward.",
			},
			{
				Method:      http.MethodGet,
				Path:        mgmtQuotaPath,
				Description: "WorkBuddy credit packages only.",
			},
			{
				Method:      http.MethodPost,
				Path:        mgmtRefreshPath,
				Description: "Rotate this account's WorkBuddy access token and persist it.",
			},
			{
				Method:      http.MethodGet,
				Path:        mgmtAccountsPath,
				Description: "Every WorkBuddy account (names only, no upstream calls).",
			},
			{
				Method:      http.MethodGet,
				Path:        mgmtAccountStatusPath,
				Description: "Credits and check-in state; pass auth_index for one account.",
			},
			{
				Method:      http.MethodPost,
				Path:        mgmtTasksAutoPath,
				Description: "Auto-complete growth-center tasks for one account.",
			},
		},
		Resources: []mgmtResourceRegistration{
			{
				Path:        consolePagePath,
				Menu:        "WorkBuddy 签到",
				Description: "Daily check-in console for WorkBuddy / CodeBuddy.",
			},
		},
	})
}

// handleManagementHandle dispatches one host-delivered request.
func handleManagementHandle(raw []byte) ([]byte, error) {
	var req mgmtRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return okEnvelope(jsonError(http.StatusBadRequest, "bad_request", err.Error()))
	}

	switch routeForPath(req.Path) {
	case consolePagePath:
		return okEnvelope(htmlResponse(consoleHTML))
	case mgmtStatusPath:
		return okEnvelope(handleStatusRequest(req.HostCallbackID, req.authIndex()))
	case mgmtQuotaPath:
		return okEnvelope(handleQuotaRequest(req.HostCallbackID, req.authIndex()))
	case mgmtClaimPath:
		return okEnvelope(handleClaimRequest(req.HostCallbackID, req.authIndex()))
	case mgmtRefreshPath:
		return okEnvelope(handleRefreshRequest(req.HostCallbackID, req.authIndex()))
	case mgmtAccountsPath:
		return okEnvelope(handleAccountsRequest())
	case mgmtAccountStatusPath:
		return okEnvelope(handleAccountStatusRequest(req.HostCallbackID, req.authIndex()))
	case mgmtTasksAutoPath:
		return okEnvelope(handleTasksAutoRequest(req.HostCallbackID, req.authIndex()))
	default:
		return okEnvelope(jsonError(http.StatusNotFound, "not_found", "unknown management path: "+req.Path))
	}
}

// routeForPath maps a request path onto one of the registered routes.
//
// The host forwards the full request path (/v0/management/... or
// /v0/resource/plugins/<id>/...), not the plugin-relative path used at
// registration, so matching is done on suffixes. The more specific check-in
// routes are tested before the console page, whose suffix is a prefix of them.
func routeForPath(path string) string {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	switch {
	case strings.HasSuffix(path, mgmtStatusPath):
		return mgmtStatusPath
	case strings.HasSuffix(path, mgmtClaimPath):
		return mgmtClaimPath
	case strings.HasSuffix(path, mgmtQuotaPath):
		return mgmtQuotaPath
	case strings.HasSuffix(path, mgmtRefreshPath):
		return mgmtRefreshPath
	case strings.HasSuffix(path, mgmtAccountStatusPath):
		return mgmtAccountStatusPath
	case strings.HasSuffix(path, mgmtAccountsPath):
		return mgmtAccountsPath
	case strings.HasSuffix(path, mgmtTasksAutoPath):
		return mgmtTasksAutoPath
	case strings.HasSuffix(path, consolePagePath):
		return consolePagePath
	default:
		return ""
	}
}

// -----------------------------------------------------------------------------
// Handlers
// -----------------------------------------------------------------------------

// snapshot bundles everything the console displays in one round trip.
type snapshot struct {
	Account string          `json:"account,omitempty"`
	Checkin json.RawMessage `json:"checkin,omitempty"`
	Quota   json.RawMessage `json:"quota,omitempty"`
	Message string          `json:"message,omitempty"`
	OK      bool            `json:"ok"`
}

func handleStatusRequest(callbackID, authIndex string) mgmtResponse {
	sa, label, err := resolveTarget(authIndex)
	if err != nil {
		return jsonError(http.StatusBadGateway, "credential_unavailable", err.Error())
	}

	snap := snapshot{Account: label, OK: true}
	defer func() {
		pluginLog("info", callbackID, "workbuddy console status served", map[string]any{
			"account": label, "ok": snap.OK,
		})
	}()
	if result, errStatus := queryCheckinStatus(sa); errStatus == nil {
		snap.Checkin = result.Data
		if result.Code != 0 {
			snap.Message = result.Msg
		}
	} else {
		snap.OK = false
		snap.Message = errStatus.Error()
	}
	if result, errQuota := queryQuota(sa); errQuota == nil {
		snap.Quota = result.Data
	} else if snap.Message == "" {
		snap.Message = errQuota.Error()
	}
	return jsonResponse(snap)
}

func handleQuotaRequest(callbackID, authIndex string) mgmtResponse {
	sa, label, err := resolveTarget(authIndex)
	if err != nil {
		return jsonError(http.StatusBadGateway, "credential_unavailable", err.Error())
	}
	result, errQuota := queryQuota(sa)
	if errQuota != nil {
		pluginLog("warn", callbackID, "workbuddy quota query failed", map[string]any{
			"account": label, "error": errQuota.Error(),
		})
		return jsonError(http.StatusBadGateway, "quota_failed", errQuota.Error())
	}
	pluginLog("info", callbackID, "workbuddy quota served", map[string]any{"account": label})
	return jsonResponse(snapshot{
		Account: label,
		Quota:   result.Data,
		Message: result.Msg,
		OK:      result.Code == 0,
	})
}

// refreshEnvelope is the /refresh payload. It reports the new expiry so the
// operator sees the click did something, instead of having to infer it.
type refreshEnvelope struct {
	OK        bool   `json:"ok"`
	Account   string `json:"account,omitempty"`
	Name      string `json:"name,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Message   string `json:"message,omitempty"`
}

// handleRefreshRequest rotates one account's access token and persists it.
//
// This does the work itself rather than marking the credential expired and
// letting the host refresh it. Marking only works for providers the host has a
// refresh lead for, and a dynamic plugin cannot register one — so the marker
// route would report success while nothing ever happened. Doing it here also
// makes the result immediate: the caller gets the new expiry back.
func handleRefreshRequest(callbackID, authIndex string) mgmtResponse {
	target, err := resolveAccount(authIndex)
	if err != nil {
		return jsonError(http.StatusBadGateway, "credential_unavailable", err.Error())
	}

	updated, errRefresh := refreshCredential(target.Credential)
	if errRefresh != nil {
		pluginLog("warn", callbackID, "workbuddy credential refresh failed", map[string]any{
			"account": target.Label, "error": errRefresh.Error(),
		})
		return jsonError(http.StatusBadGateway, "refresh_failed", errRefresh.Error())
	}

	if errSave := saveCredential(target.Entry, updated); errSave != nil {
		pluginLog("warn", callbackID, "workbuddy credential refresh not persisted", map[string]any{
			"account": target.Label, "error": errSave.Error(),
		})
		return jsonError(http.StatusBadGateway, "refresh_not_persisted", errSave.Error())
	}

	expires := time.Unix(updated.Auth.ExpiresAt, 0).UTC()
	pluginLog("info", callbackID, "workbuddy credential refreshed", map[string]any{
		"account": target.Label, "expires_at": expires.Format(time.RFC3339),
	})
	return jsonResponse(refreshEnvelope{
		OK:        true,
		Account:   target.Label,
		Name:      target.Entry.Name,
		ExpiresAt: expires.Format(time.RFC3339),
		Message:   "凭证已刷新，有效期至 " + expires.Local().Format("2006-01-02 15:04"),
	})
}

// handleClaimRequest performs the daily check-in.
//
// It reads status first so an already-claimed day is answered with Tencent's
// own wording instead of a failed claim, and re-reads everything afterwards so
// the console can show the updated balance immediately.
func handleClaimRequest(callbackID, authIndex string) mgmtResponse {
	sa, label, err := resolveTarget(authIndex)
	if err != nil {
		return jsonError(http.StatusBadGateway, "credential_unavailable", err.Error())
	}

	snap := snapshot{Account: label}

	if status, errStatus := queryCheckinStatus(sa); errStatus == nil && status.Code == 0 {
		if alreadyCheckedIn(status.Data) {
			snap.OK = false
			snap.Message = "今天已签到，请明天再来"
			snap.Checkin = status.Data
			if result, errQuota := queryQuota(sa); errQuota == nil {
				snap.Quota = result.Data
			}
			pluginLog("info", callbackID, "workbuddy check-in skipped: already claimed today", map[string]any{
				"account": label,
			})
			return jsonResponse(snap)
		}
	}

	claim, errClaim := claimCheckin(sa)
	if errClaim != nil {
		return jsonError(http.StatusBadGateway, "claim_failed", errClaim.Error())
	}

	snap.OK = claim.Code == 0
	snap.Message = claim.Msg
	if hint := claimHint(claim); hint != "" {
		snap.Message = hint
	}

	// Refresh state after the claim so the console reflects the new balance.
	if status, errStatus := queryCheckinStatus(sa); errStatus == nil {
		snap.Checkin = status.Data
	}
	if result, errQuota := queryQuota(sa); errQuota == nil {
		snap.Quota = result.Data
	}
	pluginLog(claimLogLevel(snap.OK), callbackID, "workbuddy check-in claim finished", map[string]any{
		"account": label, "ok": snap.OK, "message": snap.Message,
	})
	return jsonResponse(snap)
}

// claimLogLevel keeps an already-claimed day out of the error stream: the
// upstream reports it as business code 10001, which is a normal outcome.
func claimLogLevel(ok bool) string {
	if ok {
		return "info"
	}
	return "warn"
}

// resolveTarget picks the credential to act on: the named account when the
// caller supplied one, otherwise the first usable account.
func resolveTarget(authIndex string) (*storedAuth, string, error) {
	target, err := resolveAccount(authIndex)
	if err != nil {
		return nil, "", err
	}
	return target.Credential, target.Label, nil
}

// accountTarget is a resolved credential together with the host entry it came
// from. Handlers that only read the credential use Credential; the refresh
// handler also needs Entry, because persisting a rotated token requires the
// auth file name the host knows it by.
type accountTarget struct {
	Entry      pluginapi.HostAuthFileEntry
	Credential *storedAuth
	Label      string
}

// resolveAccount resolves the named account, or the first usable one when the
// caller did not name any.
func resolveAccount(authIndex string) (*accountTarget, error) {
	if strings.TrimSpace(authIndex) == "" {
		return activeTarget()
	}
	entries, err := listWorkbuddyEntries()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.AuthIndex == authIndex {
			return targetForEntry(entry)
		}
	}
	return nil, fmt.Errorf("no usable %s credential for auth_index %s", providerName, authIndex)
}

// activeTarget returns the account management actions default to when the
// caller did not name one. Management-triggered actions target a single
// account, so the first usable entry is used, matching the panel's ordering.
func activeTarget() (*accountTarget, error) {
	entries, err := listWorkbuddyEntries()
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("no usable %s credential found", providerName)
	}
	return targetForEntry(entries[0])
}

func targetForEntry(entry pluginapi.HostAuthFileEntry) (*accountTarget, error) {
	sa, err := credentialForEntry(entry)
	if err != nil {
		return nil, err
	}
	return &accountTarget{Entry: entry, Credential: sa, Label: credentialLabel(entry)}, nil
}

// accountsEnvelope is the /accounts payload: cheap account shells, no upstream
// calls, so the panel can paint its cards instantly.
type accountsEnvelope struct {
	OK       bool             `json:"ok"`
	Accounts []accountSummary `json:"accounts"`
}

// statusEnvelope is the /accounts/status payload: the expensive half.
type statusEnvelope struct {
	OK       bool            `json:"ok"`
	Accounts []accountReport `json:"accounts"`
}

// handleAccountsRequest lists accounts without touching upstream. Kept cheap on
// purpose: the panel paints its cards from this and fetches credits only when
// the operator asks, because a credit lookup costs two upstream round trips.
func handleAccountsRequest() mgmtResponse {
	summaries, err := listAccountSummaries()
	if err != nil {
		return jsonError(http.StatusBadGateway, "accounts_failed", err.Error())
	}
	return jsonResponse(accountsEnvelope{OK: true, Accounts: summaries})
}

// tasksAutoEnvelope is the /tasks/auto_all payload.
type tasksAutoEnvelope struct {
	OK      bool             `json:"ok"`
	Account string           `json:"account,omitempty"`
	Results []autoTaskResult `json:"results"`
	Message string           `json:"message,omitempty"`
}

// handleTasksAutoRequest runs the growth-task automation pipeline for one account.
func handleTasksAutoRequest(callbackID, authIndex string) mgmtResponse {
	if strings.TrimSpace(authIndex) == "" {
		return jsonError(http.StatusBadRequest, "auth_index_required", "auth_index is required")
	}
	target, err := resolveAccount(authIndex)
	if err != nil {
		return jsonError(http.StatusBadGateway, "credential_unavailable", err.Error())
	}
	if !tryLockTaskAuto(authIndex) {
		return jsonError(http.StatusConflict, "task_busy", "该账号有任务正在执行中，请稍后再试")
	}
	defer unlockTaskAuto(authIndex)

	results, summary, err := runGrowthTasksAuto(target.Credential)
	if err != nil {
		pluginLog("warn", callbackID, "workbuddy growth tasks auto failed", map[string]any{
			"account": target.Label, "error": err.Error(),
		})
		return jsonError(http.StatusBadGateway, "tasks_auto_failed", err.Error())
	}
	pluginLog("info", callbackID, "workbuddy growth tasks auto finished", map[string]any{
		"account": target.Label, "summary": summary, "count": len(results),
	})
	return jsonResponse(tasksAutoEnvelope{
		OK:      true,
		Account: target.Label,
		Results: results,
		Message: summary,
	})
}

// handleAccountStatusRequest is the on-demand half: credits plus check-in state,
// for one account when auth_index is given, otherwise for all of them.
func handleAccountStatusRequest(callbackID, authIndex string) mgmtResponse {
	reports, err := collectAccounts(authIndex)
	if err != nil {
		pluginLog("warn", callbackID, "workbuddy account status failed", map[string]any{
			"auth_index": authIndex, "error": err.Error(),
		})
		return jsonError(http.StatusBadGateway, "status_failed", err.Error())
	}
	pluginLog("info", callbackID, "workbuddy account status served", map[string]any{
		"count": len(reports), "auth_index": authIndex,
	})
	return jsonResponse(statusEnvelope{OK: true, Accounts: reports})
}

// alreadyCheckedIn reports whether data.today_checked_in is true.
func alreadyCheckedIn(data json.RawMessage) bool {
	if len(data) == 0 {
		return false
	}
	var probe struct {
		TodayCheckedIn bool `json:"today_checked_in"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	return probe.TodayCheckedIn
}

// claimHint translates the shared already-claimed business code into a
// user-facing sentence. Tencent answers HTTP 400 with code 10001 for that case,
// which must read as a normal outcome rather than an error.
func claimHint(result *billingResult) string {
	if result == nil {
		return ""
	}
	if result.Code == 10001 {
		if strings.Contains(result.Msg, "已签到") {
			return result.Msg
		}
		return "今天已签到，请明天再来"
	}
	return result.Msg
}

// -----------------------------------------------------------------------------
// Response helpers
// -----------------------------------------------------------------------------

func jsonResponse(v any) mgmtResponse {
	body, err := json.Marshal(v)
	if err != nil {
		return jsonError(http.StatusInternalServerError, "encode_failed", err.Error())
	}
	return mgmtResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	}
}

func jsonError(status int, code, message string) mgmtResponse {
	body, _ := json.Marshal(map[string]any{
		"ok":    false,
		"error": map[string]string{"code": code, "message": message},
	})
	return mgmtResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	}
}

func htmlResponse(page string) mgmtResponse {
	return mgmtResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: []byte(page),
	}
}
