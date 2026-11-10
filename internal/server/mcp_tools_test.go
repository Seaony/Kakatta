package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/api"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPToolsSchemasAndCapabilities(t *testing.T) {
	f, token := newMCPFixture(t)
	response := mcpRequest(t, f, token.Token, mcpBody(t, "tools/list", map[string]any{}))
	requireStatus(t, response, http.StatusOK)
	var envelope struct {
		Result struct {
			Tools []*mcp.Tool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	var names []string
	var hasDefault func(any) bool
	hasDefault = func(value any) bool {
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				if key == "default" || hasDefault(child) {
					return true
				}
			}
		case []any:
			for _, child := range value {
				if hasDefault(child) {
					return true
				}
			}
		}
		return false
	}
	for _, tool := range envelope.Result.Tools {
		names = append(names, tool.Name)
		if tool.InputSchema == nil || tool.OutputSchema == nil || hasDefault(tool.InputSchema) || hasDefault(tool.OutputSchema) {
			t.Fatal("所有工具必须声明结构，且不能提前填默认值")
		}
		if tool.Annotations.ReadOnlyHint != (tool.Name == "list_tasks" || tool.Name == "query_findings") {
			t.Fatal("只有查询工具可以标只读")
		}
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"create_task", "list_tasks", "pause_task", "query_findings", "resume_task", "run_task_now"}) {
		t.Fatalf("工具范围不一致：%v", names)
	}
	initialize := mcpRequest(t, f, token.Token, mcpBody(t, "initialize", map[string]any{
		"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "本地回归", "version": "1"},
	}))
	requireStatus(t, initialize, http.StatusOK)
	var initial struct {
		Result struct {
			Capabilities mcp.ServerCapabilities `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(initialize.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	caps := initial.Result.Capabilities
	if caps.Tools == nil || caps.Tools.ListChanged || caps.Logging != nil || caps.Resources != nil || caps.Prompts != nil || initialize.Header().Get("Mcp-Session-Id") != "" {
		t.Fatal("静态六工具不能推出长会话或额外订阅")
	}
}

func TestMCPCreatePauseResumeUsesCurrentDefaultsAndService(t *testing.T) {
	f, token := newMCPFixture(t)
	if _, err := f.settings.Apply(t.Context(), settings.Values{"crawl.task_interval_minutes": json.RawMessage(`75`)}); err != nil {
		t.Fatal(err)
	}
	call := func(name string, args any) *mcp.CallToolResult {
		return mcpToolResult(t, mcpRequest(t, f, token.Token, mcpBody(t, "tools/call", map[string]any{"name": name, "arguments": args})))
	}
	created := call("create_task", map[string]any{"keyword": "相机", "enabled": false, "min_price": nil, "max_pages": 4})
	if created.IsError {
		t.Fatalf("创建失败：%s", created.Content[0].(*mcp.TextContent).Text)
	}
	saved := mcpOutput[service.TaskSaveResult](t, created)
	if saved.Task.IntervalMinutes != 75 || saved.Task.MinPrice != nil || saved.Task.Enabled || saved.Task.MaxPages != 4 || len(saved.Warnings) == 0 {
		t.Fatal("MCP必须保留null、false、推荐值风险，并让业务读取当前默认值")
	}
	path := fmt.Sprintf("/api/v1/tasks/%d", saved.Task.ID)
	web := f.request(http.MethodGet, path, "", true)
	requireStatus(t, web, http.StatusOK)
	if actual := decodeBody[service.TaskView](t, web); !reflect.DeepEqual(actual.Task, saved.Task) {
		t.Fatal("网页与MCP没有读取同一个任务")
	}
	args := map[string]any{"task_id": saved.Task.ID}
	resumed := call("resume_task", args)
	if resumed.IsError || !mcpOutput[service.TaskSaveResult](t, resumed).Task.Enabled {
		t.Fatal("恢复必须经UpdateTask写入enabled")
	}
	paused := call("pause_task", args)
	if paused.IsError || mcpOutput[service.TaskSaveResult](t, paused).Task.Enabled {
		t.Fatal("暂停必须经UpdateTask写入enabled")
	}
	second := call("pause_task", args)
	if second.IsError || mcpOutput[service.TaskSaveResult](t, second).Task.Enabled {
		t.Fatal("重复暂停不能改变语义")
	}
	run := call("run_task_now", args)
	if !run.IsError {
		t.Fatal("没有可用账号不能放过立即运行的前置检查")
	}
	mcpFailure := mcpOutput[ErrorEnvelope](t, run)
	restFailure := decodeBody[ErrorEnvelope](t, f.request(http.MethodPost, path+"/run", "", true))
	if !reflect.DeepEqual(mcpFailure.Error, restFailure.Error) || !reflect.DeepEqual(mcpFailure.Fields, restFailure.Fields) || mcpFailure.RequestID == "" {
		t.Fatal("MCP必须保留网页同一前置原因、恢复时间和字段")
	}
}

func TestMCPQueriesUseRealPagesAndCursorBindings(t *testing.T) {
	f, token := newMCPFixture(t)
	taskID := resultTask(t, f)
	resultFinding(t, f, taskID, "mcp-first", 1, false)
	resultFinding(t, f, taskID, "mcp-second", 2, false)
	for _, keyword := range []string{"相机A", "相机B"} {
		requireStatus(t, f.request(http.MethodPost, "/api/v1/tasks", `{"keyword":"`+keyword+`","enabled":false}`, true), http.StatusCreated)
	}
	call := func(name string, args any) *mcp.CallToolResult {
		return mcpToolResult(t, mcpRequest(t, f, token.Token, mcpBody(t, "tools/call", map[string]any{"name": name, "arguments": args})))
	}
	findings := call("query_findings", map[string]any{"task_id": taskID, "view": "all", "sort": "latest", "limit": 1})
	if findings.IsError {
		t.Fatal("真实发现查询失败")
	}
	page := mcpOutput[service.Page[service.FindingView]](t, findings)
	web := decodeBody[service.Page[service.FindingView]](t, f.request(http.MethodGet, fmt.Sprintf("/api/v1/findings?task_id=%d&view=all&sort=latest&limit=1", taskID), "", true))
	if !reflect.DeepEqual(page, web) || page.Total != 2 || len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatal("MCP筛选排序与分页必须等于网页")
	}
	second := call("query_findings", map[string]any{"task_id": taskID, "view": "all", "sort": "latest", "limit": 1, "cursor": *page.NextCursor})
	if second.IsError || mcpOutput[service.Page[service.FindingView]](t, second).Items[0].ID == page.Items[0].ID {
		t.Fatal("分页不能重复第一页")
	}
	if !call("list_tasks", map[string]any{"limit": 1, "cursor": *page.NextCursor}).IsError {
		t.Fatal("不能跨资源使用游标")
	}
	if !call("query_findings", map[string]any{"view": "other", "sort": "latest", "limit": 1, "cursor": *page.NextCursor}).IsError {
		t.Fatal("不能跨筛选使用游标")
	}
	tasks := call("list_tasks", map[string]any{"q": "相机", "enabled": false, "limit": 2})
	if tasks.IsError || mcpOutput[service.Page[service.TaskView]](t, tasks).Total != 3 {
		t.Fatal("任务查询必须使用真实筛选")
	}
}

func TestMCPInvalidInputsAndErrorSafety(t *testing.T) {
	f, token := newMCPFixture(t)
	for _, test := range []struct {
		name string
		args any
	}{
		{"create_task", map[string]any{"keyword": "相机", "enabled": nil}},
		{"create_task", map[string]any{"keyword": "相机", "unknown": "秘密标记"}},
		{"list_tasks", map[string]any{"limit": 0}},
		{"list_tasks", map[string]any{"limit": 201}},
		{"list_tasks", map[string]any{"enabled": "false"}},
		{"query_findings", map[string]any{"sort": ""}},
		{"query_findings", map[string]any{"decision": "bogus"}},
		{"pause_task", map[string]any{}},
		{"pause_task", map[string]any{"task_id": 0}},
		{"pause_task", map[string]any{"task_id": 9007199254740992}},
		{"run_task_now", map[string]any{"task_id": 1, "force": true}},
	} {
		result := mcpToolResult(t, mcpRequest(t, f, token.Token, mcpBody(t, "tools/call", map[string]any{"name": test.name, "arguments": test.args})))
		if !result.IsError {
			t.Fatalf("非法输入被接受：%s", test.name)
		}
		envelope := mcpOutput[ErrorEnvelope](t, result)
		if envelope.Error.Code != "http.invalid_request" || envelope.RequestID == "" || strings.Contains(result.Content[0].(*mcp.TextContent).Text, "秘密标记") {
			t.Fatal("schema错误必须是脱敏结构化错误")
		}
	}
	if count := resultCount(t, f, "SELECT count(*) FROM tasks"); count != 0 {
		t.Fatal("非法输入不能留下任务")
	}
	for _, failure := range []error{errors.New("代理密码秘密"), &service.ServiceError{Kind: "unknown", Reason: message("代理密码秘密")}} {
		if value := mcpErrorEnvelope(t.Context(), failure); value.Error.Code != "http.internal" {
			t.Fatal("未知错误不能透传内部内容")
		}
	}
	next := int64(9007199254740991)
	reason := locales.Message{Code: "run.account_gap", Args: map[string]any{"seconds": 1}}
	failure := &service.ServiceError{Kind: service.ErrorConflict, Reason: reason, NextAllowedAt: &next}
	if result := mcpErrorEnvelope(t.Context(), failure); result.Error.Code != reason.Code || result.Error.Args["next_allowed_at"] != &next || len(reason.Args) != 1 {
		t.Fatal("错误恢复时间必须保留且不能修改原原因")
	}
}

func TestMCPSchemaCopiesOnlyReachableComponents(t *testing.T) {
	var document mcpAPIDocument
	if err := json.Unmarshal(api.JSON(), &document); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(document.Components.Schemas["TaskCreate"])
	copy, err := document.schema(document.Components.Schemas["TaskCreate"])
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(document.Components.Schemas["TaskCreate"])
	if string(before) != string(after) {
		t.Fatal("schema搬运不能修改共用接口描述")
	}
	defs := copy["$defs"].(map[string]any)
	if len(defs) == 0 || len(defs) >= len(document.Components.Schemas) {
		t.Fatal("只能附带本工具引用到的结构")
	}
	if _, err := document.schema(map[string]any{"$ref": "https://outside.invalid/schema"}); err == nil {
		t.Fatal("不能解析外部schema地址")
	}
	if _, err := document.schema(map[string]any{"$ref": "#/components/schemas/Missing"}); err == nil {
		t.Fatal("缺少结构应拒绝装配")
	}
}

func TestMCPRunNowCannotBypassAccountOrQuotaGates(t *testing.T) {
	f, token := newMCPFixture(t)
	resultExec(t, f, `INSERT INTO accounts(id,name,state) VALUES(1,'本地假账号','active')`)
	taskID := resultTask(t, f)
	resultExec(t, f, `UPDATE tasks SET account_id=1 WHERE id=?`, taskID)
	call := func() ErrorEnvelope {
		result := mcpToolResult(t, mcpRequest(t, f, token.Token, mcpBody(t, "tools/call", map[string]any{"name": "run_task_now", "arguments": map[string]any{"task_id": taskID}})))
		if !result.IsError {
			t.Fatal("前置检查失败时不能接纳运行")
		}
		return mcpOutput[ErrorEnvelope](t, result)
	}
	for _, test := range []struct{ state, code string }{
		{"signed_out", "run.account_signed_out"}, {"expired", "run.account_expired"},
		{"needs_verification", "run.account_needs_verification"}, {"denied", "run.account_denied"},
	} {
		resultExec(t, f, `UPDATE accounts SET state=?,cooldown_until=? WHERE id=1`, test.state, f.clock.Load()+time.Hour.Milliseconds())
		if result := call(); result.Error.Code != test.code {
			t.Fatalf("账号门禁没有复用：%s %s", test.state, result.Error.Code)
		}
	}
	resultExec(t, f, `UPDATE accounts SET state='active',cooldown_until=NULL,last_run_ended_at=?,next_gap_seconds=180 WHERE id=1`, f.clock.Load())
	if result := call(); result.Error.Code != "run.account_gap" || result.Error.Args["next_allowed_at"] == nil {
		t.Fatal("账号最小间隔必须保留恢复时间")
	}
	resultExec(t, f, `UPDATE accounts SET last_run_ended_at=NULL WHERE id=1`)
	for i := 0; i < 30; i++ {
		resultExec(t, f, `INSERT INTO quota_events(account_id,kind,at) VALUES(1,'detail',?)`, f.clock.Load())
	}
	if result := call(); result.Error.Code != "run.hourly_quota" {
		t.Fatalf("MCP绕过每小时额度：%s", result.Error.Code)
	}
	resultExec(t, f, `DELETE FROM quota_events`)
	if _, err := f.settings.Apply(t.Context(), settings.Values{"crawl.account_daily_search_visits": json.RawMessage(`4`)}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		resultExec(t, f, `INSERT INTO quota_events(account_id,kind,at) VALUES(1,'search',?)`, f.clock.Load()-2*time.Hour.Milliseconds())
	}
	if result := call(); result.Error.Code != "run.daily_search_quota" {
		t.Fatalf("MCP绕过每日搜索额度：%s", result.Error.Code)
	}
	if count := resultCount(t, f, `SELECT count(*) FROM run_records WHERE outcome!='skipped'`); count != 0 {
		t.Fatal("门禁失败仍然创建了抓取运行")
	}
	resultExec(t, f, `DELETE FROM quota_events`)
	accepted := mcpToolResult(t, mcpRequest(t, f, token.Token, mcpBody(t, "tools/call", map[string]any{"name": "run_task_now", "arguments": map[string]any{"task_id": taskID}})))
	if accepted.IsError {
		t.Fatal("门禁满足后应由同一服务接纳请求")
	}
	if submission := mcpOutput[service.RunSubmission](t, accepted); submission.TaskID != taskID || submission.Status != service.SubmissionQueued {
		t.Fatal("立即运行必须返回真实接纳DTO")
	}
}
