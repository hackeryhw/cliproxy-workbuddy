// Desktop and web behaviour telemetry for growth tasks.
// Ported from workbuddy2api-panel internal/upstream/desktop.go (2026-09-12).
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

const (
	desktopReportPath    = "/v2/report"
	desktopAppearanceSet = "/v2/user-asset/appearance/set"
	endpointExpertList   = upstreamBase + "/portal/operation-platform/market/expert/list"
)

type desktopEvent map[string]any

type marketExpert struct {
	ExpertID      string `json:"expert_id"`
	ExpertType    string `json:"expert_type"`
	DisplayNameZH string `json:"display_name_zh"`
	ProfessionZH  string `json:"profession_zh"`
	Version       string `json:"version"`
	Categories    []any  `json:"categories"`
}

var desktopRequestIDRegex = regexp.MustCompile(`^(cmb-)?[0-9a-f]{32}$`)

func accountNickname(sa *storedAuth) string {
	if sa == nil {
		return ""
	}
	return sa.Account.Nickname
}

func deriveDesktopID(sa *storedAuth, salt string) string {
	sum := sha256.Sum256([]byte(salt + ":" + sa.Account.UID))
	return hex.EncodeToString(sum[:18])
}

func desktopFingerprint(sa *storedAuth) map[string]any {
	now := time.Now().UnixMilli()
	return map[string]any{
		"timezone": "Asia/Shanghai", "reportDelay": 2000,
		"userId": sa.Account.UID, "username": accountNickname(sa),
		"userNickname": accountNickname(sa), "product": "SaaS",
		"releaseDate": int64(1789036585355),
		"commit":      "5f9692923c93033111c51ad7b003eb80204a9b75",
		"ideName": "WorkBuddy", "ideType": "WorkBuddy", "ideVersion": "5.5.6",
		"machineId": deriveDesktopID(sa, "machine"), "sessionId": deriveDesktopID(sa, "session"),
		"extName": "workbuddy-desktop", "extVersion": "5.5.6",
		"os": "win32", "arch": "x64", "osVersion": "10.0.26220",
		"cpuCores": 20, "memorySize": 24, "timestamp": now, "presentAt": now,
	}
}

func reportDesktopEvent(sa *storedAuth, events ...desktopEvent) error {
	if len(events) == 0 {
		return fmt.Errorf("desktop report: no events")
	}
	fp := desktopFingerprint(sa)
	arr := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		m := map[string]any{}
		for k, v := range fp {
			m[k] = v
		}
		for k, v := range ev {
			m[k] = v
		}
		arr = append(arr, m)
	}
	raw, err := json.Marshal(arr)
	if err != nil {
		return err
	}
	_, _, err = doJSON(growthHTTPClient(), http.MethodPost, upstreamBase+desktopReportPath, func(r *http.Request) {
		applyBillingHeaders(r, sa)
		r.Header.Set("Accept", "application/json, text/plain, */*")
		r.Header.Set("Content-Type", "application/json;charset=UTF-8")
		r.Header.Set("X-Product", "SaaS")
		r.Header.Set("X-Request-ID", deriveDesktopID(sa, "req")+fmt.Sprintf("%d", time.Now().UnixNano()%1e6))
	}, bytes.NewReader(raw))
	return err
}

func desktopChatSequence(conversationID, requestID, messageID, modelID, modelName string) []desktopEvent {
	mk := func(code string, extra map[string]any) desktopEvent {
		ev := desktopEvent{"eventCode": code}
		for k, v := range extra {
			ev[k] = v
		}
		return ev
	}
	uuid := requestID
	return []desktopEvent{
		mk("agent_task_created", map[string]any{
			"source": "LOCAL", "name": "working", "task_target": "local", "mode": "craft",
			"requestModelId": modelID, "requestModelName": modelName,
			"has_repo": false, "repo_type": "none", "workspace_type": "empty",
			"has_connector": false, "connector_types": []any{},
			"has_mention": false, "mention_types": []any{},
			"has_template": false, "action": "", "template_name": "",
			"has_expert": false, "expert_id": "", "expert_name": "", "expert_industry_id": "",
			"has_skill": false, "skill_names": []any{},
			"conversationId": conversationID, "messageId": messageID,
			"buddyId": "", "buddyName": "",
		}),
		mk("chat_message_send", map[string]any{
			"messageId": messageID + "-assistant", "historyCount": 0,
			"isContextTruncated": false, "currentStepCount": 1,
			"traceId": uuid, "rootRequestId": requestID, "parentConversationId": conversationID,
			"agentName": "cli", "agentType": "main",
		}),
		mk("chat_request_send", map[string]any{
			"inputLength": 24, "isPlan": false, "isAutoExecuteTerminal": false,
			"isAutoModify": false, "codebaseEnable": false, "maxToken": 0,
			"maxSteps": 500, "temperature": 0, "maxRetries": 0,
			"mentionContexts": []any{}, "knowledgeId": []any{}, "knowledgeName": []any{},
			"codebaseId": "", "mentionContextCount": 0, "command": "",
			"recommendId": "", "skillId": "", "skillCount": 0, "totalCount": 0,
			"traceId": uuid, "rootRequestId": requestID, "parentConversationId": conversationID,
			"agentName": "cli", "agentType": "main",
			"codebuddy.session_id": conversationID, "codebuddy.conversation_request_id": requestID,
		}),
		mk("chat_message_response", map[string]any{
			"messageId": messageID + "-assistant", "responseModelId": modelID,
			"inputToken": 120, "outputToken": 80, "totalToken": 200,
			"cachedTokens": 0, "cachedWriteTokens": 0, "cachedMissTokens": 0,
			"isSuccessful": true, "messageErrorCode": "", "finishReason": "stop",
			"firstTokenAt": time.Now().UnixMilli(), "traceId": uuid,
			"conversationId": conversationID, "rootRequestId": requestID, "parentConversationId": conversationID,
			"agentName": "cli", "agentType": "main",
			"codebuddy.session_id": conversationID, "codebuddy.conversation_request_id": requestID,
		}),
		mk("chat_message_status", map[string]any{
			"messageId": messageID + "-assistant", "messageErrorCode": "0",
			"traceId": uuid, "rootRequestId": requestID, "parentConversationId": conversationID,
			"agentName": "cli", "agentType": "main",
		}),
		mk("chat_request_response", map[string]any{
			"mode": "craft", "toolCallCount": 0,
			"inputToken": 120, "outputToken": 80, "totalToken": 200,
			"cachedTokens": 0, "cachedWriteTokens": 0, "cachedMissTokens": 0,
			"isSuccessful": true, "messageErrorCode": "", "finishReason": "stop",
			"rootRequestId": requestID, "parentConversationId": conversationID,
		}),
	}
}

func setAppearanceTheme(sa *storedAuth, resourceKey string) error {
	raw, _ := json.Marshal(map[string]string{"kind": "theme", "resource_key": resourceKey})
	_, _, err := doJSON(growthHTTPClient(), http.MethodPost, upstreamBase+desktopAppearanceSet, func(r *http.Request) {
		applyBillingHeaders(r, sa)
		r.Header.Set("Accept", "application/json, text/plain, */*")
		r.Header.Set("Content-Type", "application/json;charset=UTF-8")
		r.Header.Set("X-Product", "SaaS")
	}, bytes.NewReader(raw))
	return err
}

func desktopBuddyAppSequence(buddyID, buddyName string) []desktopEvent {
	mk := func(code string, extra map[string]any) desktopEvent {
		ev := desktopEvent{
			"eventCode": code, "mode": "LOCAL",
			"buddyId": buddyID, "buddyName": buddyName,
		}
		for k, v := range extra {
			ev[k] = v
		}
		return ev
	}
	return []desktopEvent{
		mk("buddyapp_discover_click", nil),
		mk("buddyapp_show", map[string]any{"elementId": buddyID, "elementName": buddyName, "position": 2}),
		mk("buddyapp_enter_click", map[string]any{"elementId": buddyID, "elementName": buddyName, "position": 2, "isFirstPage": "1"}),
		mk("buddyapp_auth_confirm_click", map[string]any{"elementId": buddyID, "elementName": buddyName}),
		mk("buddyapp_bindaccount_skip_click", map[string]any{"elementId": buddyID, "elementName": buddyName}),
	}
}

func desktopAutomationCreateEvent(name string) desktopEvent {
	return desktopEvent{
		"eventCode": "automated_task_create_suc", "name": name,
		"source": "manually", "modelId": "fast-model", "modelIsThinking": true,
		"connectorCount": 0, "skills": "", "skillCount": 0,
		"scheduleType": "once", "mode": "LOCAL",
	}
}

func reportWebEvent(sa *storedAuth, eventCode, pageURL, elementID, elementName string) error {
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36"
	ev := map[string]any{
		"eventCode": eventCode, "timestamp": time.Now().UnixMilli(), "reportDelay": 0,
		"pageURL": pageURL, "elementId": elementID, "elementName": elementName,
		"os": "Win32", "arch": "", "osVersion": "10.0", "userAgent": ua,
		"machineId": deriveDesktopID(sa, "webmachine"), "userId": sa.Account.UID,
		"userNickname": accountNickname(sa), "enterpriseId": sa.Account.EnterpriseID,
	}
	raw, err := json.Marshal([]map[string]any{ev})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, workbuddyWebBase+"/v2/report", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+sa.Auth.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-client-platform", "web")
	req.Header.Set("Origin", workbuddyWebBase)
	req.Header.Set("Referer", pageURL)
	req.Header.Set("User-Agent", ua)
	if sa.Account.UID != "" {
		req.Header.Set("X-User-Id", sa.Account.UID)
	}
	resp, err := growthHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("web report http=%d: %s", resp.StatusCode, truncate(string(body), 160))
	}
	var env apiEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return err
	}
	if env.Code != 0 {
		return fmt.Errorf("web report code=%d msg=%s", env.Code, env.Msg)
	}
	return nil
}

func desktopTemplateUseSequence(conversationID, requestID, templateID, templateName string) []desktopEvent {
	events := desktopChatSequence(conversationID, requestID, "msg-"+templateID, "fast-model", "fast-model")
	events = append(events,
		desktopEvent{
			"eventCode": "agent_task_created_with_template", "mode": "working",
			"isCustomModel": false, "id": templateID, "name": templateName, "requestId": requestID,
		},
		desktopEvent{"eventCode": "template_used", "template_id": templateID, "task_mode": "working"},
	)
	return events
}

func desktopPlaybookPromptSequence(conversationID, requestID, caseID, caseName string) []desktopEvent {
	events := desktopChatSequence(conversationID, requestID, "msg-pb", "fast-model", "fast-model")
	events = append(events,
		desktopEvent{
			"eventCode": "web_element_click", "pageName": "playbook_detail",
			"elementId": "playbook_ctaClick", "elementName": caseName, "source": "discover",
		},
		desktopEvent{
			"eventCode": "playbook_cta_click", "source": "discover", "position": 0,
			"id": caseID, "name": caseName, "type": "document",
			"categoryId": "", "categoryName": "",
		},
		desktopEvent{
			"eventCode": "playbook_prompt_send", "conversationId": conversationID, "requestId": requestID,
			"id": caseID, "name": caseName, "type": "document",
			"categoryId": "", "categoryName": "",
		},
	)
	return events
}

func desktopDesignCanvasSequence(conversationID, requestID string) []desktopEvent {
	events := desktopChatSequence(conversationID, requestID, "msg-canvas", "fast-model", "fast-model")
	return append(events,
		desktopEvent{
			"eventCode": "wbx_design_canvas_task_create", "conversationId": conversationID,
			"requestId": requestID, "source": "summon_keyword", "cost": 12000, "isSuccessful": true,
		},
		desktopEvent{
			"eventCode": "wbx_design_canvas_open", "conversationId": conversationID,
			"requestId": requestID, "id": "ardot-file-" + requestID[len(requestID)-8:],
			"source": "summon_keyword", "type": "page", "cost": 13000, "isSuccessful": true,
		},
	)
}

func marketExpertList(sa *storedAuth, expertType string) ([]marketExpert, error) {
	body, _ := json.Marshal(map[string]any{
		"page": 1, "page_size": 20, "sort_by": "reco_rank", "sort_order": "desc", "expert_type": expertType,
	})
	data, _, err := doJSON(growthHTTPClient(), http.MethodPost, endpointExpertList, func(r *http.Request) {
		applyBillingHeaders(r, sa)
	}, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var out struct {
		Experts []marketExpert `json:"experts"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out.Experts, nil
}

func desktopChatWithExpert(sa *storedAuth, expertID string) (conversationID, requestID string, err error) {
	conversationID = fmt.Sprintf("wb-task-conv-%d", time.Now().UnixNano())
	body, _ := json.Marshal(map[string]any{
		"model": "fast-model",
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a helpful assistant. 当前处于中文环境，使用简体中文回答。"},
			map[string]any{"role": "user", "content": "1+1等于几？直接回答。"},
		},
		"agent": "cli", "temperature": 1, "stream": true,
		"stream_options": map[string]any{"include_usage": true},
	})
	req, err := http.NewRequest(http.MethodPost, endpointChat, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	applyBillingHeaders(req, sa)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Conversation-ID", conversationID)
	req.Header.Set("X-Request-ID", fmt.Sprintf("%d", time.Now().UnixNano()))
	req.Header.Set("X-Agent-Intent", "craft")
	req.Header.Set("X-Agent-Type", "main")
	req.Header.Set("X-IDE-Name", "WorkBuddy")
	req.Header.Set("X-IDE-Type", "WorkBuddy")
	req.Header.Set("X-IDE-Version", "5.5.6")
	req.Header.Set("x-codebuddy-request", "1")
	if expertID != "" {
		req.Header.Set("X-Expert-Id", expertID)
	}
	resp, err := growthHTTPClient().Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("chat http=%d: %s", resp.StatusCode, truncate(string(raw), 160))
	}
	buf := make([]byte, 0, 1<<20)
	tmp := make([]byte, 8192)
	for {
		n, rerr := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if i := bytes.Index(buf, []byte(`"id":"`)); i >= 0 {
				rest := buf[i+6:]
				if end := bytes.IndexByte(rest, '"'); end > 0 {
					id := string(rest[:end])
					if desktopRequestIDRegex.MatchString(id) {
						return conversationID, id, nil
					}
				}
			}
		}
		if rerr != nil || len(buf) > 1<<20 {
			break
		}
	}
	return "", "", fmt.Errorf("SSE 中未找到服务端 requestId")
}

func desktopExpertSummonSequence(e marketExpert) []desktopEvent {
	cat := "expert-all"
	if len(e.Categories) > 0 {
		if s, ok := e.Categories[0].(string); ok {
			cat = s
		}
	}
	ver := e.Version
	if ver == "" {
		ver = "1.0.0"
	}
	return []desktopEvent{
		{
			"eventCode": "web_element_click", "source": e.ExpertID, "type": cat, "version": ver,
			"elementId": "expert_summon_click", "elementName": "立即召唤",
			"pageURL": "/C:/Program%20Files/WorkBuddy/resources/app.asar/renderer/index.html",
		},
		{
			"eventCode": "expert_summon_click", "id": e.ExpertID, "name": e.DisplayNameZH,
			"expertTitle": e.ProfessionZH, "type": "expert-all", "position": 0,
			"expertType": e.ExpertType, "version": ver, "mode": "LOCAL",
		},
		{
			"eventCode": "expert_summoned", "id": e.ExpertID, "name": e.DisplayNameZH,
			"expertTitle": e.ProfessionZH, "type": "expert-all",
		},
	}
}

func desktopExpertActualUseEvent(e marketExpert, conversationID, requestID string, mode string) desktopEvent {
	cat := "expert-all"
	if len(e.Categories) > 0 {
		if s, ok := e.Categories[0].(string); ok {
			cat = s
		}
	}
	ver := e.Version
	if ver == "" {
		ver = "1.0.0"
	}
	return desktopEvent{
		"eventCode": "expert_actual_use",
		"id": e.ExpertID, "name": e.DisplayNameZH, "expertTitle": e.ProfessionZH,
		"type": cat, "expertType": e.ExpertType, "source": "builtin", "version": ver,
		"cost": 9000, "characterCount": 14, "mode": mode,
		"conversationId": conversationID, "requestId": requestID,
		"messageId": "msg-" + requestID[len(requestID)-8:],
		"requestModelId": "fast-model", "requestModelName": "fast-model",
	}
}
