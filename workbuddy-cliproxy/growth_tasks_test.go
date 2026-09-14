package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func withStubGrowth(t *testing.T, handler stubRoundTripper) {
	t.Helper()
	original := growthHTTPClient
	t.Cleanup(func() { growthHTTPClient = original })
	growthHTTPClient = func() *http.Client {
		return &http.Client{Transport: handler}
	}
}

func withFastGrowthPolls(t *testing.T) {
	t.Helper()
	origPolls, origGap, origSweeps := growthClaimPolls, growthClaimPollGap, growthClaimSweeps
	growthClaimPolls = 2
	growthClaimPollGap = time.Millisecond
	growthClaimSweeps = 1
	t.Cleanup(func() {
		growthClaimPolls = origPolls
		growthClaimPollGap = origGap
		growthClaimSweeps = origSweeps
	})
}

func growthTasksListBody() string {
	return `{"code":0,"msg":"OK","data":{"tasks":[` +
		`{"task_code":"chat_5","title":"聊天5次","accept_status":"accepted","progress":{"current":0,"target":5}},` +
		`{"task_code":"first_buddy","title":"领养Buddy","accept_status":"not_accepted","progress":{"current":0,"target":1}},` +
		`{"task_code":"Model_chat_GLM5.2","title":"体验GLM-5.2","accept_status":"accepted","progress":{"current":1,"target":1}}` +
		`]}}`
}

func growthJSONResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestRunGrowthTasksAutoClaimsCompletedTask(t *testing.T) {
	withFastGrowthPolls(t)
	sa := testStoredAccount("growth-uid", "tester")
	reportCount := 0
	claimPaths := []string{}

	withStubGrowth(t, stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/v2/activity/growth/tasks") && r.Method == http.MethodGet:
			return growthJSONResp(200, growthTasksListBody()), nil
		case strings.Contains(r.URL.Path, "/v2/activity/growth/tasks/accept"):
			return growthJSONResp(200, `{"code":0,"msg":"OK","data":{}}`), nil
		case strings.Contains(r.URL.Path, "/v2/report"):
			reportCount++
			return growthJSONResp(200, `{"code":0,"msg":"OK","data":{}}`), nil
		case strings.Contains(r.URL.Path, "/activity/growth/buddy/agreement"):
			return growthJSONResp(200, `{"code":0,"msg":"OK","data":{}}`), nil
		case strings.Contains(r.URL.Path, "/activity/growth/buddy/first"):
			return growthJSONResp(200, `{"code":0,"msg":"OK","data":{}}`), nil
		case strings.Contains(r.URL.Path, "/activity/growth/tasks/Model_chat_GLM5.2/claim"):
			claimPaths = append(claimPaths, r.URL.Path)
			return growthJSONResp(200, `{"code":0,"msg":"OK","data":{"credit":100,"energy":5}}`), nil
		case strings.Contains(r.URL.Path, "/v2/chat/completions"):
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
				Request:    r,
			}, nil
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			return nil, nil
		}
	}))

	results, summary, err := runGrowthTasksAuto(sa)
	if err != nil {
		t.Fatalf("runGrowthTasksAuto: %v", err)
	}
	if reportCount < 5 {
		t.Errorf("expected at least 5 report calls for chat_5, got %d", reportCount)
	}
	if len(claimPaths) < 1 {
		t.Fatalf("expected at least one claim, got %v", claimPaths)
	}
	found := false
	for _, item := range results {
		if item.TaskCode == "Model_chat_GLM5.2" && item.Claimed && item.Credit == 100 {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing claimed glm task in results: %+v", results)
	}
	if !strings.Contains(summary, "完成") {
		t.Errorf("summary = %q", summary)
	}
}

func TestGrowthAutoTasksStatusAllDone(t *testing.T) {
	sa := testStoredAccount("growth-uid", "tester")
	withStubGrowth(t, stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/v2/activity/growth/tasks") && r.Method == http.MethodGet {
			return growthJSONResp(200, `{"code":0,"msg":"OK","data":{"tasks":[`+
				`{"task_code":"chat_5","accept_status":"claimed","progress":{"current":5,"target":5}},`+
				`{"task_code":"first_buddy","accept_status":"claimed","progress":{"current":1,"target":1}},`+
				`{"task_code":"Model_chat_GLM5.2","accept_status":"claimed","progress":{"current":1,"target":1}}`+
				`]}}`), nil
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		return nil, nil
	}))

	pending, allDone, err := growthAutoTasksStatus(sa)
	if err != nil {
		t.Fatalf("growthAutoTasksStatus: %v", err)
	}
	if pending != 0 || !allDone {
		t.Fatalf("pending=%d allDone=%v", pending, allDone)
	}
}

func TestGrowthAutoTasksStatusPending(t *testing.T) {
	sa := testStoredAccount("growth-uid", "tester")
	withStubGrowth(t, stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/v2/activity/growth/tasks") && r.Method == http.MethodGet {
			return growthJSONResp(200, growthTasksListBody()), nil
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		return nil, nil
	}))

	pending, allDone, err := growthAutoTasksStatus(sa)
	if err != nil {
		t.Fatalf("growthAutoTasksStatus: %v", err)
	}
	if pending != 3 || allDone {
		t.Fatalf("pending=%d allDone=%v", pending, allDone)
	}
}

func TestHandleTasksAutoRequestRequiresAuthIndex(t *testing.T) {
	resp := handleTasksAutoRequest("", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("StatusCode = %d", resp.StatusCode)
	}
}

func TestSweepGrowthClaimsPicksUpDelayedProgress(t *testing.T) {
	withFastGrowthPolls(t)
	sa := testStoredAccount("growth-uid", "tester")
	listCalls := 0
	withStubGrowth(t, stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/v2/activity/growth/tasks") && r.Method == http.MethodGet {
			listCalls++
			body := `{"code":0,"msg":"OK","data":{"tasks":[` +
				`{"task_code":"Model_chat_GLM5.2","accept_status":"accepted","progress":{"current":0,"target":1}}` +
				`]}}`
			if listCalls >= 2 {
				body = `{"code":0,"msg":"OK","data":{"tasks":[` +
					`{"task_code":"Model_chat_GLM5.2","accept_status":"accepted","progress":{"current":1,"target":1}}` +
					`]}}`
			}
			return growthJSONResp(200, body), nil
		}
		if strings.Contains(r.URL.Path, "/activity/growth/tasks/Model_chat_GLM5.2/claim") {
			return growthJSONResp(200, `{"code":0,"msg":"OK","data":{"credit":100,"energy":5}}`), nil
		}
		return growthJSONResp(404, `{"code":404,"msg":"not found"}`), nil
	}))

	items := sweepGrowthClaims(sa)
	if len(items) != 1 || !items[0].Claimed || items[0].Credit != 100 {
		t.Fatalf("sweep did not claim delayed task: %+v (listCalls=%d)", items, listCalls)
	}
}

func TestHandleTasksAutoRequestRunsPipeline(t *testing.T) {
	withFastGrowthPolls(t)
	seedConsoleCredential(t)
	withStubGrowth(t, stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/v2/activity/growth/tasks") && r.Method == http.MethodGet {
			return growthJSONResp(200, growthTasksListBody()), nil
		}
		if strings.Contains(r.URL.Path, "/v2/activity/growth/tasks/accept") {
			return growthJSONResp(200, `{"code":0,"msg":"OK","data":{}}`), nil
		}
		if strings.Contains(r.URL.Path, "/v2/report") {
			return growthJSONResp(200, `{"code":0,"msg":"OK","data":{}}`), nil
		}
		if strings.Contains(r.URL.Path, "/activity/growth/buddy/") {
			return growthJSONResp(200, `{"code":0,"msg":"OK","data":{}}`), nil
		}
		if strings.Contains(r.URL.Path, "/activity/growth/tasks/") && strings.HasSuffix(r.URL.Path, "/claim") {
			return growthJSONResp(200, `{"code":0,"msg":"OK","data":{"credit":100,"energy":5}}`), nil
		}
		if strings.Contains(r.URL.Path, "/v2/chat/completions") {
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
				Request:    r,
			}, nil
		}
		return growthJSONResp(404, `{"code":404,"msg":"not found"}`), nil
	}))

	body, _ := json.Marshal(map[string]string{"auth_index": "test-auth-index"})
	req, _ := json.Marshal(mgmtRequest{
		Method: http.MethodPost,
		Path:   "/v0/management" + mgmtTasksAutoPath,
		Body:   body,
	})
	raw, err := handleManagementHandle(req)
	if err != nil {
		t.Fatalf("handleManagementHandle: %v", err)
	}
	var payload tasksAutoEnvelope
	if err := json.Unmarshal(decodeMgmtResult(t, raw), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !payload.OK {
		t.Fatalf("payload not ok: %+v", payload)
	}
	if len(payload.Results) == 0 {
		t.Fatal("expected task results")
	}
}
