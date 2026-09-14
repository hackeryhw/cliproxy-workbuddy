// Growth-center task automation for the WorkBuddy plugin.
//
// Upstream semantics (verified against workbuddy2api-panel, 2026-09):
//   - List/accept live on copilot.tencent.com (/v2/activity/growth/tasks*).
//   - Progress is lit by behaviour events (e.g. chat_request_send on
//     www.codebuddy.cn/v2/report), not by a "complete task" API.
//   - Rewards are claimed on www.workbuddy.cn with the task code in the path.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	codebuddyBase    = "https://www.codebuddy.cn"
	workbuddyWebBase = "https://www.workbuddy.cn"

	endpointGrowthTasks    = upstreamBase + "/v2/activity/growth/tasks"
	endpointGrowthAccept   = upstreamBase + "/v2/activity/growth/tasks/accept"
	endpointBuddyAgreement = upstreamBase + "/activity/growth/buddy/agreement"
	endpointBuddyFirst     = upstreamBase + "/activity/growth/buddy/first"
	endpointReport         = codebuddyBase + "/v2/report"

	growthReportGap           = 1050 * time.Millisecond
	buddyTaskIncompleteMarker = "first_buddy task not completed yet"
)

var (
	growthClaimPolls   = 8
	growthClaimPollGap = 4 * time.Second
	growthClaimSweeps  = 2
)

// growthHTTPClient is the transport seam for growth/report/chat calls.
var growthHTTPClient = sharedHTTPClient

// growthTask is one entry from the growth task list.
type growthTask struct {
	TaskCode     string `json:"task_code"`
	Title        string `json:"title,omitempty"`
	Target       int64  `json:"target"`
	Current      int64  `json:"current"`
	AcceptStatus string `json:"accept_status,omitempty"`
	Claimable    bool   `json:"claimable,omitempty"`
	Claimed      bool   `json:"claimed,omitempty"`
}

type autoTaskResult struct {
	TaskCode string `json:"task_code"`
	Desc     string `json:"desc,omitempty"`
	Status   string `json:"status"` // done | skipped | error
	Message  string `json:"message,omitempty"`
	Credit   int64  `json:"credit,omitempty"`
	Energy   int64  `json:"energy,omitempty"`
	Claimed  bool   `json:"claimed,omitempty"`
}

type autoTaskAction struct {
	Code string
	Desc string
	Run  func(*storedAuth) (string, error)
}

const expertSummonGap = 6 * time.Second

var growthAutoActions = []autoTaskAction{
	{Code: "chat_5", Desc: "上报 5 条对话活跃事件", Run: runGrowthChat5},
	{Code: "first_buddy", Desc: "上报解锁 → 同意协议 → 领养 Buddy", Run: runGrowthFirstBuddy},
	{Code: "Model_chat_GLM5.2", Desc: "accept → glm-5.2 对话 → 对齐上报", Run: runGrowthModelChat},
	{Code: "RichMeow_Chat", Desc: "桌面指纹完整对话事件链", Run: runGrowthRichMeow},
	{Code: "Buddy_App", Desc: "上报 buddyapp 进入事件链", Run: runGrowthBuddyApp},
	{Code: "Buddy_App_QQ", Desc: "上报 buddyapp 进入事件链（企鹅教师助手）", Run: runGrowthBuddyApp},
	{Code: "automation_1", Desc: "上报定时任务创建事件", Run: runGrowthAutomationCreate},
	{Code: "Library_read", Desc: "上报资料库介绍阅读事件", Run: runGrowthLibraryRead},
	{Code: "template_5", Desc: "上报 template_used 事件组 ×5", Run: runGrowthTemplateUse},
	{Code: "playbook_prompt", Desc: "上报 playbook_prompt_send 事件组", Run: runGrowthPlaybookPrompt},
	{Code: "create_canvas", Desc: "上报设计创意画布创建事件组", Run: runGrowthCreateCanvas},
	{Code: "expert_5", Desc: "真实专家召唤+使用链 ×5", Run: runGrowthExpertUse},
	{Code: "Expert_team_use_3", Desc: "真实专家团召唤+使用链 ×3", Run: runGrowthExpertTeamUse},
	{Code: "Hp_Appearance", Desc: "设置主题 API + 皮肤生效事件", Run: runGrowthAppearance},
	{Code: "skill_1", Desc: "真实对话 + skill_info 技能加载事件", Run: runGrowthSkillFresh},
	{Code: "Expert_lighthouse", Desc: "腾讯轻量云专家召唤+使用链", Run: runGrowthExpertLighthouse},
	{Code: "black_cat", Desc: "夜间窗口 glm-5.2 对话补足", Run: runGrowthBlackCat},
}

// growthAutoTaskCodes is the set of task codes the panel treats as automatable.
func growthAutoTaskCodes() map[string]bool {
	codes := make(map[string]bool, len(growthAutoActions))
	for _, act := range growthAutoActions {
		codes[act.Code] = true
	}
	return codes
}

// growthAutoTasksStatus counts automatable tasks that still need action.
// allDone is true only when the account has at least one automatable task and
// every one of them is already claimed (reward received).
func growthAutoTasksStatus(sa *storedAuth) (pending int, allDone bool, err error) {
	tasks, err := listGrowthTasks(sa)
	if err != nil {
		return 0, false, err
	}
	auto := growthAutoTaskCodes()
	found := 0
	for _, t := range tasks {
		if !auto[t.TaskCode] {
			continue
		}
		found++
		if !t.Claimed {
			pending++
		}
	}
	return pending, found > 0 && pending == 0, nil
}

var (
	taskAutoMu     sync.Mutex
	taskAutoActive = map[string]bool{}
)

func tryLockTaskAuto(authIndex string) bool {
	taskAutoMu.Lock()
	defer taskAutoMu.Unlock()
	if taskAutoActive[authIndex] {
		return false
	}
	taskAutoActive[authIndex] = true
	return true
}

func unlockTaskAuto(authIndex string) {
	taskAutoMu.Lock()
	delete(taskAutoActive, authIndex)
	taskAutoMu.Unlock()
}

func listGrowthTasks(sa *storedAuth) ([]growthTask, error) {
	data, _, err := doJSON(growthHTTPClient(), http.MethodGet, endpointGrowthTasks, func(r *http.Request) {
		applyBillingHeaders(r, sa)
	}, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Tasks []struct {
			TaskCode     string          `json:"task_code"`
			Title        string          `json:"title"`
			AcceptStatus string          `json:"accept_status"`
			Target       int64           `json:"target"`
			Current      int64           `json:"current"`
			Progress     json.RawMessage `json:"progress"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	out := make([]growthTask, 0, len(resp.Tasks))
	for _, t := range resp.Tasks {
		cur, tgt := t.Current, t.Target
		if len(t.Progress) > 0 && string(t.Progress) != "null" {
			var pr struct {
				Current int64 `json:"current"`
				Target  int64 `json:"target"`
			}
			if json.Unmarshal(t.Progress, &pr) == nil && (pr.Target > 0 || pr.Current > 0) {
				cur, tgt = pr.Current, pr.Target
			}
		}
		claimed := t.AcceptStatus == "claimed"
		out = append(out, growthTask{
			TaskCode:     t.TaskCode,
			Title:        t.Title,
			Target:       tgt,
			Current:      cur,
			AcceptStatus: t.AcceptStatus,
			Claimable:    !claimed && tgt > 0 && cur >= tgt,
			Claimed:      claimed,
		})
	}
	return out, nil
}

func growthTaskByCode(sa *storedAuth, code string) (*growthTask, error) {
	tasks, err := listGrowthTasks(sa)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		if tasks[i].TaskCode == code {
			return &tasks[i], nil
		}
	}
	return nil, nil
}

func acceptGrowthTasks(sa *storedAuth, codes []string) error {
	if len(codes) == 0 {
		return nil
	}
	raw, _ := json.Marshal(map[string]any{"task_codes": codes})
	_, _, err := doJSON(growthHTTPClient(), http.MethodPost, endpointGrowthAccept, func(r *http.Request) {
		applyBillingHeaders(r, sa)
	}, bytes.NewReader(raw))
	return err
}

func claimGrowthReward(sa *storedAuth, taskCode string) (credit, energy int64, err error) {
	req, err := http.NewRequest(http.MethodPost,
		workbuddyWebBase+"/activity/growth/tasks/"+url.PathEscape(taskCode)+"/claim", nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+sa.Auth.AccessToken)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", workbuddyWebBase)
	req.Header.Set("Referer", workbuddyWebBase+"/profile/growth-center")
	req.Header.Set("x-client-platform", "web")
	req.Header.Set("User-Agent", clientUA)
	if sa.Account.UID != "" {
		req.Header.Set("X-User-Id", sa.Account.UID)
	}
	if sa.Account.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", sa.Account.EnterpriseID)
	}
	if sa.Auth.Domain != "" {
		req.Header.Set("X-Domain", sa.Auth.Domain)
	}

	resp, err := growthHTTPClient().Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return 0, 0, fmt.Errorf("claim http=%d: %s", resp.StatusCode, truncate(string(raw), 160))
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0, 0, err
	}
	if env.Code != 0 {
		return 0, 0, fmt.Errorf("claim code=%d msg=%s", env.Code, env.Msg)
	}
	var reward struct {
		AlreadyClaimed bool  `json:"already_claimed"`
		Credit         int64 `json:"credit"`
		Energy         int64 `json:"energy"`
	}
	if err := json.Unmarshal(env.Data, &reward); err != nil {
		return 0, 0, err
	}
	if reward.AlreadyClaimed {
		return 0, 0, nil
	}
	return reward.Credit, reward.Energy, nil
}

func reportChatActivity(sa *storedAuth, conversationID, requestID, modelID, modelName string) error {
	if requestID == "" {
		requestID = conversationID
	}
	if modelID == "" {
		modelID = "deepseek-v4-flash"
	}
	if modelName == "" {
		modelName = "DeepSeek V4 Flash"
	}
	now := time.Now().UnixMilli()
	ev := map[string]any{
		"eventCode": "chat_request_send", "timestamp": now, "reportDelay": 0,
		"mode": "craft", "conversationId": conversationID, "requestId": requestID,
		"inputLength": 12, "requestModelId": modelID, "requestModelName": modelName,
		"isPlan": false, "isAutoExecuteTerminal": false, "isAutoModify": false,
		"codebaseEnable": false, "maxToken": 0, "maxSteps": 0, "temperature": 0,
		"maxRetries": 0, "mentionContexts": []any{}, "knowledgeId": []any{},
		"knowledgeName": []any{}, "codebaseId": "", "mentionContextCount": 0,
		"command": "", "expertId": "", "recommendId": "", "skillId": "",
		"skillCount": 0, "totalCount": 0, "fileUri": "", "presentAt": now,
		"traceId": "", "rootRequestId": requestID, "parentConversationId": conversationID,
		"agentName": "default", "agentType": "conversation", "userId": sa.Account.UID,
	}
	raw, err := json.Marshal([]map[string]any{ev})
	if err != nil {
		return err
	}
	_, _, err = doJSON(growthHTTPClient(), http.MethodPost, endpointReport, func(r *http.Request) {
		applyBillingHeaders(r, sa)
	}, bytes.NewReader(raw))
	return err
}

func buddyAgreement(sa *storedAuth) error {
	raw, _ := json.Marshal(map[string]any{"agree": true})
	_, _, err := doJSON(growthHTTPClient(), http.MethodPost, endpointBuddyAgreement, func(r *http.Request) {
		applyBillingHeaders(r, sa)
	}, bytes.NewReader(raw))
	return err
}

func buddyFirst(sa *storedAuth) error {
	_, _, err := doJSON(growthHTTPClient(), http.MethodPost, endpointBuddyFirst, func(r *http.Request) {
		applyBillingHeaders(r, sa)
	}, bytes.NewReader([]byte("{}")))
	return err
}

func isBuddyTaskIncomplete(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), buddyTaskIncompleteMarker)
}

func growthChatOnce(sa *storedAuth, modelID, prompt string) error {
	body, _ := json.Marshal(map[string]any{
		"model": modelID,
		"messages": []map[string]any{
			{"role": "user", "content": prompt},
		},
		"stream": true,
	})
	req, err := http.NewRequest(http.MethodPost, endpointChat, bytes.NewReader(body))
	if err != nil {
		return err
	}
	backendHeaders(req, sa)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := growthHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chat http=%d: %s", resp.StatusCode, truncate(string(raw), 160))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}

// growthTaskReadyToClaim reports whether a task should receive a claim attempt.
// Upstream scoring is async: progress may reach target before claimable flips.
func growthTaskReadyToClaim(t *growthTask) bool {
	if t == nil || t.Claimed {
		return false
	}
	if t.Claimable {
		return true
	}
	if t.Target > 0 && t.Current >= t.Target {
		return true
	}
	return t.AcceptStatus == "completed"
}

func growthTaskProgressText(t *growthTask) string {
	if t == nil {
		return "?"
	}
	if t.Target > 0 {
		return fmt.Sprintf("%d/%d", t.Current, t.Target)
	}
	if t.Claimed {
		return "claimed"
	}
	return t.AcceptStatus
}

func growthTaskByCodeWaiting(sa *storedAuth, code string) (*growthTask, error) {
	t, err := growthTaskByCode(sa, code)
	if err != nil || t == nil {
		return t, err
	}
	if t.Claimed || growthTaskReadyToClaim(t) {
		return t, nil
	}
	for i := 1; i < growthClaimPolls; i++ {
		time.Sleep(growthClaimPollGap)
		t2, err2 := growthTaskByCode(sa, code)
		if err2 != nil {
			return t, nil
		}
		if t2 != nil {
			t = t2
			if t.Claimed || growthTaskReadyToClaim(t) {
				return t, nil
			}
		}
	}
	return t, nil
}

// attemptGrowthClaim polls until ready, then claims. Returns an updated message.
func attemptGrowthClaim(sa *storedAuth, code, baseMsg string) (string, int64, int64, bool) {
	after, _ := growthTaskByCodeWaiting(sa, code)
	if after == nil {
		return baseMsg, 0, 0, false
	}
	if after.Claimed {
		return baseMsg, 0, 0, true
	}
	if !growthTaskReadyToClaim(after) {
		if after.Target > 0 {
			return baseMsg + fmt.Sprintf("（进度 %s）", growthTaskProgressText(after)), 0, 0, false
		}
		return baseMsg, 0, 0, false
	}
	credit, energy, err := claimGrowthReward(sa, code)
	if err != nil {
		return baseMsg + "；达标但领奖失败: " + err.Error(), 0, 0, false
	}
	if credit > 0 || energy > 0 {
		return baseMsg + fmt.Sprintf("；已领奖 +%d 分 +%d 能", credit, energy), credit, energy, true
	}
	return baseMsg + "；奖励此前已领取", 0, 0, true
}

// sweepGrowthClaims retries claim for every automatable task that is still open.
// A second pass catches tasks whose progress landed after the action loop moved on.
func sweepGrowthClaims(sa *storedAuth) []autoTaskResult {
	var out []autoTaskResult
	for _, act := range growthAutoActions {
		before, err := growthTaskByCode(sa, act.Code)
		if err != nil || before == nil || before.Claimed {
			continue
		}
		msg, credit, energy, claimed := attemptGrowthClaim(sa, act.Code, "收尾领奖")
		if !claimed && credit == 0 && energy == 0 {
			continue
		}
		out = append(out, autoTaskResult{
			TaskCode: act.Code,
			Desc:     act.Desc,
			Status:   "done",
			Message:  msg,
			Credit:   credit,
			Energy:   energy,
			Claimed:  claimed,
		})
	}
	return out
}

func acceptPendingGrowthTasks(sa *storedAuth) (int, error) {
	tasks, err := listGrowthTasks(sa)
	if err != nil {
		return 0, err
	}
	var codes []string
	for _, t := range tasks {
		if t.Claimed || t.AcceptStatus == "accepted" || t.AcceptStatus == "completed" {
			continue
		}
		codes = append(codes, t.TaskCode)
	}
	if len(codes) == 0 {
		return 0, nil
	}
	if err := acceptGrowthTasks(sa, codes); err != nil {
		return 0, err
	}
	return len(codes), nil
}

func runGrowthChat5(sa *storedAuth) (string, error) {
	t, err := growthTaskByCode(sa, "chat_5")
	if err != nil {
		return "", err
	}
	if t == nil {
		return "", fmt.Errorf("任务不存在")
	}
	target := t.Target
	if target <= 0 {
		target = 5
	}
	need := target - t.Current
	if need <= 0 {
		return "进度已达标，无需上报", nil
	}
	for i := int64(0); i < need; i++ {
		cid := fmt.Sprintf("wb-task-chat5-%d-%d", time.Now().UnixMilli(), i)
		if err := reportChatActivity(sa, cid, "", "", ""); err != nil {
			return fmt.Sprintf("上报第 %d/%d 条失败: %v", i+1, need, err), nil
		}
		if i < need-1 {
			time.Sleep(growthReportGap)
		}
	}
	return fmt.Sprintf("已补报 %d 条对话事件", need), nil
}

func runGrowthFirstBuddy(sa *storedAuth) (string, error) {
	if err := reportChatActivity(sa, fmt.Sprintf("wb-task-adopt-%d", time.Now().UnixMilli()), "", "", ""); err != nil {
		return "", fmt.Errorf("前置上报: %w", err)
	}
	time.Sleep(growthReportGap)
	if err := buddyAgreement(sa); err != nil {
		return "", fmt.Errorf("同意协议: %w", err)
	}
	if err := buddyFirst(sa); err != nil {
		if isBuddyTaskIncomplete(err) {
			return "前置已上报，但领养门槛未过（需当日活跃），请稍后重试", nil
		}
		return "", fmt.Errorf("领取 Buddy: %w", err)
	}
	return "已领取 Buddy", nil
}

func runGrowthModelChat(sa *storedAuth) (string, error) {
	const code, modelID, modelName = "Model_chat_GLM5.2", "glm-5.2", "GLM-5.2"
	_ = acceptGrowthTasks(sa, []string{code})
	time.Sleep(growthReportGap)
	if err := growthChatOnce(sa, modelID, "hi，请回复一句话"); err != nil {
		return "", fmt.Errorf("对话: %w", err)
	}
	time.Sleep(growthReportGap)
	cid := fmt.Sprintf("wb-task-glm52-%d", time.Now().UnixMilli())
	if err := reportChatActivity(sa, cid, cid, modelID, modelName); err != nil {
		return "对话已完成，但进度上报失败：" + err.Error(), nil
	}
	return "已完成 glm-5.2 对话并上报", nil
}

func runGrowthRichMeow(sa *storedAuth) (string, error) {
	ms := time.Now().UnixMilli()
	conv := fmt.Sprintf("wb-task-rm-%d", ms)
	req := fmt.Sprintf("wb-task-rm-req-%d", ms)
	msg := fmt.Sprintf("req-%d-user", ms)
	if err := reportDesktopEvent(sa, desktopChatSequence(conv, req, msg, "fast-model", "fast-model")...); err != nil {
		return "", err
	}
	return "已上报桌面端完整对话事件链", nil
}

func runGrowthBuddyApp(sa *storedAuth) (string, error) {
	events := desktopBuddyAppSequence("cb_y5Dy46tPQGGWtueMxXbe", "企鹅教师助手")
	if err := reportDesktopEvent(sa, events...); err != nil {
		return "", err
	}
	return "已上报 buddyapp 进入五连事件", nil
}

func runGrowthAutomationCreate(sa *storedAuth) (string, error) {
	if err := reportDesktopEvent(sa, desktopAutomationCreateEvent("wb-task 自动化")); err != nil {
		return "", err
	}
	return "已上报定时任务创建事件", nil
}

func runGrowthLibraryRead(sa *storedAuth) (string, error) {
	const docURL = "https://www.workbuddy.cn/space/d/o0KWYeynteVv06UnAZqIFm"
	if err := reportWebEvent(sa, "web_element_click", docURL, "library_doc_intro_click", "WorkBuddy资料库介绍"); err != nil {
		return "", err
	}
	return "已上报资料库介绍阅读事件", nil
}

func runGrowthTemplateUse(sa *storedAuth) (string, error) {
	templates := [][2]string{{"1", "深度研究"}, {"2", "周报生成"}, {"3", "竞品分析"}, {"4", "活动策划"}, {"5", "代码评审"}}
	for i, tp := range templates {
		ms := time.Now().UnixMilli()
		conv := fmt.Sprintf("wb-task-tpl-%d-%d", ms, i)
		req := fmt.Sprintf("wb-task-tpl-req-%d-%d", ms, i)
		if err := reportDesktopEvent(sa, desktopTemplateUseSequence(conv, req, tp[0], tp[1])...); err != nil {
			return fmt.Sprintf("第 %d 组模板事件上报失败: %v", i+1, err), nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return "已上报 template_used ×5", nil
}

func runGrowthPlaybookPrompt(sa *storedAuth) (string, error) {
	ms := time.Now().UnixMilli()
	conv := fmt.Sprintf("wb-task-pb-%d", ms)
	req := fmt.Sprintf("wb-task-pb-req-%d", ms)
	if err := reportDesktopEvent(sa, desktopPlaybookPromptSequence(conv, req, "pm-gtm-launch-plan", "新产品上市 GTM 发布计划一页纸")...); err != nil {
		return "", err
	}
	return "已上报 playbook_cta_click + playbook_prompt_send", nil
}

func runGrowthCreateCanvas(sa *storedAuth) (string, error) {
	ms := time.Now().UnixMilli()
	conv := fmt.Sprintf("wb-task-canvas-%d", ms)
	req := fmt.Sprintf("wb-task-canvas-req-%d", ms)
	if err := reportDesktopEvent(sa, desktopDesignCanvasSequence(conv, req)...); err != nil {
		return "", err
	}
	return "已上报 wbx_design_canvas_task_create/open", nil
}

func runGrowthExpertUse(sa *storedAuth) (string, error) {
	return runGrowthExpertBatch(sa, "agent", 5)
}

func runGrowthExpertTeamUse(sa *storedAuth) (string, error) {
	return runGrowthExpertBatch(sa, "team", 3)
}

func runGrowthExpertBatch(sa *storedAuth, expertType string, count int) (string, error) {
	experts, err := marketExpertList(sa, expertType)
	if err != nil {
		return "", fmt.Errorf("拉取专家列表: %w", err)
	}
	if len(experts) == 0 {
		return "", fmt.Errorf("专家市场列表为空")
	}
	ok := 0
	for i, e := range experts {
		if ok >= count {
			break
		}
		if err := reportDesktopEvent(sa, desktopExpertSummonSequence(e)...); err != nil {
			continue
		}
		conv, req, cerr := desktopChatWithExpert(sa, e.ExpertID)
		if cerr != nil {
			continue
		}
		events := append(
			desktopChatSequence(conv, req, "msg-"+req[len(req)-8:], "fast-model", "fast-model"),
			desktopExpertActualUseEvent(e, conv, req, "craft"),
		)
		if err := reportDesktopEvent(sa, events...); err != nil {
			continue
		}
		ok++
		if i < len(experts)-1 {
			time.Sleep(expertSummonGap)
		}
	}
	return fmt.Sprintf("已对 %d 位真实专家完成召唤+使用链（类型 %s）", ok, expertType), nil
}

func runGrowthAppearance(sa *storedAuth) (string, error) {
	const themeKey = "theme-tkmw7j"
	if err := setAppearanceTheme(sa, themeKey); err != nil {
		return "", fmt.Errorf("设置主题: %w", err)
	}
	time.Sleep(2 * time.Second)
	if err := reportDesktopEvent(sa, desktopEvent{
		"eventCode": "appearance_skin_apply", "action": "apply", "source": "settings_close",
		"id": themeKey, "vipLevel": 0, "series": "", "type": "unknown",
	}); err != nil {
		return "", err
	}
	return "已设置主题并上报皮肤生效事件", nil
}

func runGrowthSkillFresh(sa *storedAuth) (string, error) {
	conv, req, err := desktopChatWithExpert(sa, "")
	if err != nil {
		return "", fmt.Errorf("真实对话: %w", err)
	}
	msgID := "msg-" + req[len(req)-8:]
	events := desktopChatSequence(conv, req, msgID, "fast-model", "fast-model")
	for _, ev := range events {
		if ev["eventCode"] == "chat_message_response" {
			ev["finishReason"] = "tool_calls"
		}
	}
	events = append(events, desktopEvent{
		"eventCode": "skill_info", "id": "润泽小馆·日报撰写",
		"skillId": "skill_2097350077599879168", "skillVersion": "1.0.0",
		"toolStatus": "success", "fileCount": 56, "source": "workbuddy-desktop",
		"conversationId": conv, "requestId": req, "messageId": msgID,
		"requestModelId": "fast-model", "requestModelName": "fast-model", "traceId": req,
	})
	if err := reportDesktopEvent(sa, events...); err != nil {
		return "", fmt.Errorf("skill_info 事件: %w", err)
	}
	return "已上报真实对话 + skill_info 技能加载事件", nil
}

func runGrowthExpertLighthouse(sa *storedAuth) (string, error) {
	const lhID = "ex_2cvvUZQhDyeJ"
	lh := marketExpert{
		ExpertID: lhID, ExpertType: "agent",
		DisplayNameZH: "腾讯轻量云专家", ProfessionZH: "腾讯轻量云专家", Version: "1.0.2",
	}
	if experts, err := marketExpertList(sa, "agent"); err == nil {
		for _, e := range experts {
			if e.ExpertID == lhID {
				lh = e
				break
			}
		}
	}
	if err := reportDesktopEvent(sa, desktopExpertSummonSequence(lh)...); err != nil {
		return "", fmt.Errorf("召唤链: %w", err)
	}
	conv, req, err := desktopChatWithExpert(sa, lhID)
	if err != nil {
		return "", fmt.Errorf("真实对话: %w", err)
	}
	events := desktopChatSequence(conv, req, "msg-"+req[len(req)-8:], "fast-model", "fast-model")
	for _, ev := range events {
		if ev["eventCode"] == "agent_task_created" {
			ev["has_expert"] = true
			ev["expert_id"] = lh.ExpertID
			ev["expert_name"] = lh.DisplayNameZH
			ev["expert_industry_id"] = ""
		}
	}
	use := desktopExpertActualUseEvent(lh, conv, req, "LOCAL")
	use["type"] = ""
	use["cost"] = 0
	events = append(events, use)
	if err := reportDesktopEvent(sa, events...); err != nil {
		return "", fmt.Errorf("使用事件: %w", err)
	}
	return "已上报轻量云专家召唤+使用链", nil
}

func inNightWindow(now time.Time) bool {
	h := now.Hour()
	return h >= 23 || h < 8
}

func blackcatNeed(sa *storedAuth) (int64, error) {
	t, err := growthTaskByCode(sa, "black_cat")
	if err != nil || t == nil {
		return 0, err
	}
	if t.Claimed || t.Current >= t.Target {
		return 0, nil
	}
	target := t.Target
	if target <= 0 {
		target = 3
	}
	return target - t.Current, nil
}

func runNightChats(sa *storedAuth, need int) (int64, error) {
	var ok int64
	for i := 0; i < need; i++ {
		if err := growthChatOnce(sa, "glm-5.2", "1+1等于几？直接回答。"); err != nil {
			return ok, fmt.Errorf("第 %d 次对话失败: %w", i+1, err)
		}
		cid := fmt.Sprintf("wb-task-night-%d-%d", time.Now().UnixMilli(), i)
		if err := reportChatActivity(sa, cid, cid, "glm-5.2", "GLM-5.2"); err != nil {
			return ok, fmt.Errorf("第 %d 次上报失败: %w", i+1, err)
		}
		ok++
		if i < need-1 {
			time.Sleep(4 * time.Second)
		}
	}
	return ok, nil
}

func runGrowthBlackCat(sa *storedAuth) (string, error) {
	if !inNightWindow(time.Now()) {
		return "当前不在 23:00–08:00 计数窗口，请夜间再试", nil
	}
	need, err := blackcatNeed(sa)
	if err != nil {
		return "", err
	}
	if need <= 0 {
		return "进度已达标，无需补足", nil
	}
	ok, err := runNightChats(sa, int(need))
	if err != nil {
		return fmt.Sprintf("完成 %d/%d 次后中断: %v", ok, need, err), nil
	}
	return fmt.Sprintf("已完成 %d 次夜间对话并上报", ok), nil
}

func runGrowthTasksAuto(sa *storedAuth) ([]autoTaskResult, string, error) {
	var out []autoTaskResult

	if n, err := acceptPendingGrowthTasks(sa); err == nil && n > 0 {
		out = append(out, autoTaskResult{
			TaskCode: "(批量接受)",
			Status:   "done",
			Message:  fmt.Sprintf("已接受 %d 个任务", n),
		})
		time.Sleep(growthReportGap)
	} else if err != nil {
		out = append(out, autoTaskResult{
			TaskCode: "(批量接受)",
			Status:   "error",
			Message:  "接受任务失败（不阻塞后续）: " + err.Error(),
		})
	}

	var done, skipped, failed int
	for _, act := range growthAutoActions {
		item := autoTaskResult{TaskCode: act.Code, Desc: act.Desc}
		before, err := growthTaskByCode(sa, act.Code)
		if err != nil {
			item.Status = "error"
			item.Message = "查询失败: " + err.Error()
			out = append(out, item)
			failed++
			continue
		}
		if before == nil {
			item.Status = "skipped"
			item.Message = "该账号无此任务"
			out = append(out, item)
			skipped++
			continue
		}
		if before.Claimed {
			item.Status = "skipped"
			item.Message = "已领取奖励"
			out = append(out, item)
			skipped++
			continue
		}
		if growthTaskReadyToClaim(before) {
			item.Status = "done"
			msg, credit, energy, claimed := attemptGrowthClaim(sa, act.Code,
				fmt.Sprintf("进度已达标（%s）", growthTaskProgressText(before)))
			item.Message = msg
			item.Credit = credit
			item.Energy = energy
			item.Claimed = claimed
			out = append(out, item)
			done++
			continue
		}

		msg, err := act.Run(sa)
		if err != nil {
			item.Status = "error"
			item.Message = err.Error()
			out = append(out, item)
			failed++
			continue
		}
		finalMsg, credit, energy, claimed := attemptGrowthClaim(sa, act.Code, msg)
		item.Status = "done"
		item.Message = finalMsg
		item.Credit = credit
		item.Energy = energy
		item.Claimed = claimed
		out = append(out, item)
		done++
		time.Sleep(growthReportGap)
	}

	for round := 0; round < growthClaimSweeps; round++ {
		swept := sweepGrowthClaims(sa)
		for _, item := range swept {
			out = append(out, item)
			if item.Claimed || item.Credit > 0 || item.Energy > 0 {
				done++
			}
		}
		if round < growthClaimSweeps-1 {
			time.Sleep(growthClaimPollGap)
		}
	}

	summary := fmt.Sprintf("完成 %d 项，跳过 %d 项", done, skipped)
	if failed > 0 {
		summary += fmt.Sprintf("，失败 %d 项", failed)
	}
	return out, summary, nil
}
