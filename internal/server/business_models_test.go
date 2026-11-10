package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	modelservice "github.com/Seaony/Kakatta/internal/analysis/service"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notification"
	"github.com/Seaony/Kakatta/internal/settings"
)

func newModelHTTPFixture(t *testing.T) (*businessFixture, *modelservice.Service) {
	t.Helper()
	f := prepareBusinessFixture(t)
	notifications := registerNotificationHTTPFixture(t, f, nil)
	return f, registerModelHTTPFixture(t, f, notifications)
}

func registerModelHTTPFixture(t *testing.T, f *businessFixture, notifications *notification.Service) *modelservice.Service {
	t.Helper()
	models, err := modelservice.New(t.Context(), modelservice.Dependencies{
		DB: f.store.DB(), Settings: f.settings.base, Cipher: f.cipher,
		Now: func() time.Time { return time.UnixMilli(f.clock.Load()) },
		Publish: func(kind string, reason *locales.Message, data json.RawMessage) error {
			return f.server.Publish(Event{Type: kind, Reason: reason, Data: data})
		},
		System: notifications.System,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := models.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := f.server.RegisterModels(models); err != nil {
		t.Fatal(err)
	}
	return models
}

func modelHTTPInput(baseURL string) modelservice.ProviderInput {
	text, key := "local-text", "model-private-key"
	return modelservice.ProviderInput{Name: "本地模型", BaseURL: baseURL, VisionModel: "local-vision", TextModel: &text, APIKey: &key, Enabled: true}
}

func modelHTTPJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertModelHTTPSafe(t *testing.T, response *httptest.ResponseRecorder, extra ...string) {
	t.Helper()
	for _, forbidden := range append([]string{"model-private-key", "private-sql-marker", "config_token", "api_key_enc", `"api_key":`, `"uuid":`, `"request":`, `"calls":`}, extra...) {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("模型网页响应泄露秘密或内部请求字段：%s", forbidden)
		}
	}
}

func createModelHTTPProvider(t *testing.T, f *businessFixture, input modelservice.ProviderInput) modelservice.ProviderView {
	t.Helper()
	response := f.request(http.MethodPost, "/api/v1/ai/providers", modelHTTPJSON(t, input), true)
	requireStatus(t, response, http.StatusCreated)
	assertModelHTTPSafe(t, response)
	return decodeBody[modelservice.ProviderSaveResult](t, response).Provider
}

func TestBusinessModelsProviderCRUDKeepsFullInputAndSecrets(t *testing.T) {
	f, _ := newModelHTTPFixture(t)
	input := modelHTTPInput("http://127.0.0.1:1/v1/chat/completions///")
	created := f.request(http.MethodPost, "/api/v1/ai/providers", modelHTTPJSON(t, input), true)
	requireStatus(t, created, http.StatusCreated)
	saved := decodeBody[modelservice.ProviderSaveResult](t, created)
	provider := saved.Provider
	if !provider.Enabled || !provider.APIKeySet || provider.Preset != nil || provider.BaseURL != "http://127.0.0.1:1/v1" || len(saved.Warnings) != 1 || saved.Warnings[0].Code != "ai.url_normalized" {
		t.Fatal("完整保存或地址规范化没有透传真实服务结果")
	}
	path := fmt.Sprintf("/api/v1/ai/providers/%d", provider.ID)
	var uuid, token string
	var encrypted []byte
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT uuid,api_key_enc,(SELECT value FROM meta WHERE key='provider.config.'||ai_providers.id) FROM ai_providers WHERE id=?`, provider.ID).Scan(&uuid, &encrypted, &token); err != nil {
		t.Fatal(err)
	}
	if len(encrypted) == 0 || bytes.Contains(encrypted, []byte("model-private-key")) || token == "" {
		t.Fatal("密钥必须加密，配置必须具有真实代次")
	}
	assertModelHTTPSafe(t, created, token)
	base := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(modelHTTPJSON(t, modelHTTPInput("http://127.0.0.1:1/v1"))), &base); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		target := "/api/v1/ai/providers"
		if method == http.MethodPut {
			target = path
		}
		for _, field := range []string{"name", "preset", "base_url", "vision_model", "text_model", "enabled"} {
			patch := make(map[string]json.RawMessage, len(base))
			for key, value := range base {
				patch[key] = value
			}
			delete(patch, field)
			response := f.request(method, target, modelHTTPJSON(t, patch), true)
			requireStatus(t, response, http.StatusBadRequest)
			failure := decodeBody[ErrorEnvelope](t, response)
			if len(failure.Fields) != 1 || failure.Fields[0].Key != field {
				t.Fatalf("缺少完整输入必须指出字段：%s", field)
			}
			if field != "preset" && field != "text_model" {
				patch[field] = json.RawMessage(`null`)
				response := f.request(method, target, modelHTTPJSON(t, patch), true)
				requireStatus(t, response, http.StatusBadRequest)
				if failure := decodeBody[ErrorEnvelope](t, response); len(failure.Fields) != 1 || failure.Fields[0].Key != field {
					t.Fatalf("非法null必须指出字段：%s", field)
				}
			}
		}
	}
	base["preset"], base["text_model"], base["enabled"] = json.RawMessage(`null`), json.RawMessage(`null`), json.RawMessage(`false`)
	for _, keyState := range []string{"omitted", "null", "empty"} {
		delete(base, "api_key")
		if keyState == "null" {
			base["api_key"] = json.RawMessage(`null`)
		} else if keyState == "empty" {
			base["api_key"] = json.RawMessage(`""`)
		}
		updated := f.request(http.MethodPut, path, modelHTTPJSON(t, base), true)
		requireStatus(t, updated, http.StatusOK)
		assertModelHTTPSafe(t, updated, token)
		view := decodeBody[modelservice.ProviderSaveResult](t, updated).Provider
		if view.Enabled || view.TextModel != nil || view.Preset != nil || view.APIKeySet != (keyState != "empty") {
			t.Fatal("可空字段、false、密钥省略/null保留或空字符串清除语义丢失")
		}
		var stored []byte
		if err := f.store.DB().QueryRowContext(t.Context(), `SELECT api_key_enc FROM ai_providers WHERE id=?`, provider.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if keyState != "empty" && !bytes.Equal(stored, encrypted) || keyState == "empty" && len(stored) != 0 {
			t.Fatal("网页密钥状态与真实加密保存不符")
		}
	}
	for _, body := range []string{`{}`, `{"enabled":null}`} {
		requireStatus(t, f.request(http.MethodPut, path+"/enabled", body, true), http.StatusBadRequest)
	}
	enabled := f.request(http.MethodPut, path+"/enabled", `{"enabled":true}`, true)
	requireStatus(t, enabled, http.StatusOK)
	if !decodeBody[modelservice.ProviderView](t, enabled).Enabled {
		t.Fatal("启用动作没有改变实际服务商")
	}
	for _, target := range []string{path, "/api/v1/ai/providers"} {
		response := f.request(http.MethodGet, target, "", true)
		requireStatus(t, response, http.StatusOK)
		assertModelHTTPSafe(t, response, token)
	}
	deleted := f.request(http.MethodDelete, path, "", true)
	requireStatus(t, deleted, http.StatusNoContent)
	if deleted.Body.Len() != 0 {
		t.Fatal("删除服务商的204不能附带响应体")
	}
	for _, route := range []struct{ method, suffix string }{{"GET", ""}, {"DELETE", ""}, {"POST", "/test"}} {
		requireStatus(t, f.request(route.method, path+route.suffix, "", true), http.StatusNotFound)
	}
	order := f.request(http.MethodPut, "/api/v1/ai/providers/order", `{"ids":[]}`, true)
	requireStatus(t, order, http.StatusOK)
	if strings.TrimSpace(order.Body.String()) != "[]" {
		t.Fatal("空服务商完整排序必须返回数组")
	}
}

func TestBusinessModelsOrderUsesWholeSetWithoutFindingBatchLimit(t *testing.T) {
	f, models := newModelHTTPFixture(t)
	ids := make([]int64, 0, 201)
	for range 201 {
		input := modelHTTPInput("http://127.0.0.1:1/v1")
		input.APIKey = nil
		result, err := models.CreateProvider(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, result.Provider.ID)
	}
	slices.Reverse(ids)
	response := f.request(http.MethodPut, "/api/v1/ai/providers/order", modelHTTPJSON(t, map[string]any{"ids": ids}), true)
	requireStatus(t, response, http.StatusOK)
	providers := decodeBody[[]modelservice.ProviderView](t, response)
	if len(providers) != len(ids) {
		t.Fatal("服务商完整排序不能套用发现批量200条上限")
	}
	for index, provider := range providers {
		if provider.ID != ids[index] || provider.SortIndex != int64(index) {
			t.Fatal("排序不能悄悄去重、截断或改序")
		}
	}
	duplicate := slices.Clone(ids)
	duplicate[0] = duplicate[1]
	for _, invalid := range [][]int64{ids[:len(ids)-1], duplicate, append(slices.Clone(ids), 9007199254740992)} {
		requireStatus(t, f.request(http.MethodPut, "/api/v1/ai/providers/order", modelHTTPJSON(t, map[string]any{"ids": invalid}), true), http.StatusUnprocessableEntity)
	}
	after, err := models.ListProviders(t.Context())
	if err != nil || !reflect.DeepEqual(providers, after) {
		t.Fatal("非法完整集合不能部分保存")
	}
}

func TestBusinessModelsRoutesProtectAndStrictlyDecodeInputs(t *testing.T) {
	f, models := newModelHTTPFixture(t)
	paths := map[string]bool{}
	for _, route := range modelRoutes(f.server, models) {
		method, path, _ := strings.Cut(route.pattern, " ")
		path = sampleRoutePath(path)
		if _, actual := f.server.mux.Handler(&http.Request{Method: method, URL: &url.URL{Path: path}}); actual != route.pattern {
			t.Fatalf("模型路由没有真实注册：%s", route.pattern)
		}
		paths[route.pattern] = false
		requireStatus(t, f.request(method, path, "", false), http.StatusUnauthorized)
		requireStatus(t, f.request(method, path+"?unexpected=private-query", "", true), http.StatusBadRequest)
		if method != http.MethodGet {
			requireStatus(t, f.request(method, path, "", true, func(r *http.Request) { r.Header.Set("Origin", "https://attacker.invalid") }), http.StatusForbidden)
		}
	}
	for _, operation := range testAPIOperations(t) {
		key := operation.Method + " " + operation.Path
		if _, exists := paths[key]; exists {
			if len(operation.Security) == 0 || operation.ImplementationStatus == "contract" {
				t.Fatalf("模型契约必须明确鉴权且已实际接线：%s", key)
			}
			paths[key] = true
		} else if strings.HasPrefix(operation.Path, "/api/v1/ai/") || strings.HasPrefix(operation.Path, "/api/v1/prompts") {
			t.Fatalf("模型契约缺少真实处理器：%s", key)
		}
	}
	for path, described := range paths {
		if !described {
			t.Fatalf("模型真实路由缺少契约：%s", path)
		}
	}
	var absent *modelservice.Service
	if f.server.RegisterModels(nil) == nil || f.server.RegisterModels(absent) == nil || f.server.RegisterModels(models) == nil {
		t.Fatal("空服务、类型空值或重复装配必须明确拒绝")
	}
	for _, body := range []string{`{"name":"a","name":"b"}`, `{"name":"a"}{}`, `{"unknown":"private-value"}`, "{\"name\":\"\xff\"}"} {
		response := f.request(http.MethodPost, "/api/v1/ai/providers", body, true)
		requireStatus(t, response, http.StatusBadRequest)
		assertModelHTTPSafe(t, response, "private-value")
	}
	requireStatus(t, f.request(http.MethodPost, "/api/v1/ai/providers", `{}`, true, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }), http.StatusUnsupportedMediaType)
	requireStatus(t, f.request(http.MethodPost, "/api/v1/ai/profile", `{"keyword":"`+strings.Repeat("x", maxBody)+`"}`, true), http.StatusRequestEntityTooLarge)
	for _, test := range []struct{ query, field string }{{"window=", "window"}, {"window=31d", "window"}, {"purpose=", "purpose"}, {"purpose=other", "purpose"}, {"task_id=9007199254740992", "task_id"}, {"task_id=0", "task_id"}} {
		response := f.request(http.MethodGet, "/api/v1/ai/usage?"+test.query, "", true)
		requireStatus(t, response, http.StatusBadRequest)
		if failure := decodeBody[ErrorEnvelope](t, response); len(failure.Fields) != 1 || failure.Fields[0].Key != test.field {
			t.Fatalf("用量查询非法字段必须明确指出：%s", test.field)
		}
	}
	for _, body := range []string{`{}`, `{"ids":null}`} {
		requireStatus(t, f.request(http.MethodPut, "/api/v1/ai/providers/order", body, true), http.StatusBadRequest)
	}
	for _, invalid := range []struct{ method, path, body, field string }{
		{"PUT", "/api/v1/ai/providers/order", `{"ids":[9223372036854775808]}`, "ids.0"},
		{"POST", "/api/v1/ai/profile", `{"keyword":"相机","requirement":"","current":{},"task_id":9223372036854775808}`, "task_id"},
		{"POST", "/api/v1/ai/profile", `{"keyword":"相机","requirement":"","current":{"next_no":9223372036854775808}}`, "current.next_no"},
	} {
		response := f.request(invalid.method, invalid.path, invalid.body, true)
		requireStatus(t, response, http.StatusBadRequest)
		if value := decodeBody[ErrorEnvelope](t, response); len(value.Fields) != 1 || value.Fields[0].Key != invalid.field {
			t.Fatalf("解码整数溢出必须保留字段定位：want=%s got=%+v", invalid.field, value.Fields)
		}
	}
	for _, invalid := range []struct{ body, field string }{
		{`{"keyword":"相机","current":{}}`, "requirement"},
		{`{"requirement":"","current":{}}`, "keyword"},
		{`{"keyword":"相机","requirement":""}`, "current"},
		{`{"keyword":"相机","requirement":"","current":null}`, "current"},
		{`{"keyword":null,"requirement":"","current":{}}`, "keyword"},
		{`{"keyword":"相机","requirement":null,"current":{}}`, "requirement"},
	} {
		response := f.request(http.MethodPost, "/api/v1/ai/profile", invalid.body, true)
		requireStatus(t, response, http.StatusBadRequest)
		if value := decodeBody[ErrorEnvelope](t, response); len(value.Fields) != 1 || value.Fields[0].Key != invalid.field {
			t.Fatal("生成输入缺失或null必须指出原字段")
		}
	}
	for _, body := range []string{`{"keyword":"相机","requirement":"","current":{},"task_id":0}`, `{"keyword":"","requirement":"","current":{}}`} {
		requireStatus(t, f.request(http.MethodPost, "/api/v1/ai/profile", body, true), http.StatusUnprocessableEntity)
	}
}

func TestBusinessModelsPromptsSaveAndRestoreRealAssets(t *testing.T) {
	f, _ := newModelHTTPFixture(t)
	response := f.request(http.MethodGet, "/api/v1/prompts", "", true)
	requireStatus(t, response, http.StatusOK)
	views := decodeBody[[]modelservice.PromptView](t, response)
	for _, view := range views {
		path := "/api/v1/prompts/" + string(view.Kind)
		custom := view.Default + "\n本地附加说明"
		saved := f.request(http.MethodPut, path, modelHTTPJSON(t, map[string]string{"content": custom}), true)
		requireStatus(t, saved, http.StatusOK)
		value := decodeBody[modelservice.PromptView](t, saved)
		if !value.Customized || value.Content != custom || value.UpdatedAt == nil || value.Kind != view.Kind {
			t.Fatal("提示词没有保存到真实服务")
		}
		for _, body := range []string{`{}`, `{"content":null}`} {
			requireStatus(t, f.request(http.MethodPut, path, body, true), http.StatusBadRequest)
		}
		restored := f.request(http.MethodDelete, path, "", true)
		requireStatus(t, restored, http.StatusOK)
		value = decodeBody[modelservice.PromptView](t, restored)
		if value.Customized || value.Content != view.Default || value.Default != view.Default || value.UpdatedAt != nil {
			t.Fatal("恢复必须返回真实默认资产和完整视图")
		}
	}
	if len(views) == 0 {
		t.Fatal("提示词列表不能用空成功代替真实资产")
	}
	requireStatus(t, f.request(http.MethodPut, "/api/v1/prompts/unknown", `{"content":"invalid"}`, true), http.StatusUnprocessableEntity)
	requireStatus(t, f.request(http.MethodDelete, "/api/v1/prompts/unknown", "", true), http.StatusUnprocessableEntity)
}

type modelHTTPAI struct {
	server *httptest.Server
	calls  atomic.Int64
}

func newModelHTTPAI(t *testing.T, handle func(http.ResponseWriter, *http.Request, string) bool) *modelHTTPAI {
	t.Helper()
	upstream := &modelHTTPAI{}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model          string `json:"model"`
			ResponseFormat struct {
				Schema struct {
					Name string `json:"name"`
				} `json:"json_schema"`
			} `json:"response_format"`
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		upstream.calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "Bearer model-private-key" {
			t.Error("真实模型服务没有使用保存的本地地址、认证或无Cookie协议")
		}
		kind := request.ResponseFormat.Schema.Name
		if kind == "listing_facts" {
			if request.Model != "local-vision" || len(request.Messages) < 2 || !bytes.Contains(request.Messages[1].Content, []byte("data:image/jpeg;base64,")) {
				t.Error("视觉试分析没有使用正式图片模型和内嵌样例图")
			}
		} else if request.Model != "local-text" {
			t.Error("标题或清单没有使用保存的文字模型")
		}
		if handle != nil && handle(w, r, kind) {
			return
		}
		text := `{"summary":"会帮你找：相机","specs":[{"name":"型号","options":["相机"],"values":["相机"],"source":"user"}],"musts":[{"text":"功能正常","source":"user"}],"bonuses":[],"risks":[],"allowed_kinds":["sale"],"budget_min":100.005,"budget_max":200,"keyword":"相机","exclude_words":["求购"]}`
		switch kind {
		case "title_screen":
			items := []map[string]any{}
			for number := 1; number <= 5; number++ {
				items = append(items, map[string]any{"no": number, "kind": "sale", "specs": []any{}, "brand_new": nil, "verdict": "pass", "reason": "本地样例"})
			}
			text = modelHTTPJSON(t, map[string]any{"items": items})
		case "listing_facts":
			text = `{"specs":[],"brand_new":null,"musts":[],"bonuses":[],"checks":[],"adjust":{"value":0,"reason":"按清单核对"},"summary":"本地样例"}`
		case "task_profile":
		default:
			t.Errorf("模型请求没有使用正式结构化输出用途：%s", kind)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{"content": text}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 4, "completion_tokens_details": map[string]int{"reasoning_tokens": 1}},
		})
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

func TestBusinessModelsTrialGenerationUsageAndBudgetUseRealService(t *testing.T) {
	f, models := newModelHTTPFixture(t)
	upstream := newModelHTTPAI(t, nil)
	provider := createModelHTTPProvider(t, f, modelHTTPInput(upstream.server.URL+"/v1"))
	if _, err := f.settings.Apply(t.Context(), settings.Values{"ai.daily_token_limit": json.RawMessage(`1`)}); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/ai/providers/%d/test", provider.ID)
	requireStatus(t, f.request(http.MethodPost, path, `{"sample":"private-sample"}`, true), http.StatusBadRequest)
	trial := f.request(http.MethodPost, path, "", true)
	requireStatus(t, trial, http.StatusOK)
	assertModelHTTPSafe(t, trial)
	report := decodeBody[modelservice.TrialReport](t, trial)
	if !report.Passed || report.Text.State != modelservice.TrialPassed || report.Vision.State != modelservice.TrialPassed || report.Text.Model == nil || *report.Text.Model != "local-text" || report.Vision.Model == nil || *report.Vision.Model != "local-vision" || upstream.calls.Load() != 2 {
		t.Fatal("试分析必须真实执行文字与视觉两步，达到每日额度也允许测试")
	}
	f.advance(time.Millisecond)
	budgetResponse := f.request(http.MethodGet, "/api/v1/ai/budget", "", true)
	requireStatus(t, budgetResponse, http.StatusOK)
	budget := decodeBody[modelservice.BudgetStatus](t, budgetResponse)
	if !budget.Blocked || budget.Limit != 1 || budget.ProtectedUsed != 14 || budget.NextCheckAt == nil {
		t.Fatal("预算接口必须反映真实两次调用的保护用量")
	}
	generate := `{"keyword":"相机","requirement":"功能正常","current":{}}`
	blocked := f.request(http.MethodPost, "/api/v1/ai/profile", generate, true)
	requireStatus(t, blocked, http.StatusServiceUnavailable)
	if errorCode(blocked) != "analysis.client.daily_limit" || upstream.calls.Load() != 2 {
		t.Fatal("生成清单不能绕过正式每日额度")
	}
	if _, err := f.settings.Apply(t.Context(), settings.Values{"ai.daily_token_limit": json.RawMessage(`0`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO tasks(id,name,keyword,enabled,max_pages,interval_minutes,judge_mode,notify_policy,sort_index,requirement,profile_json,budget_max)
		VALUES(1,'原任务','原关键词',0,1,30,'keyword','recommended',0,'原需求','{}',5000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.DB().ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES('task.profile_next_no.1','10')`); err != nil {
		t.Fatal(err)
	}
	generated := f.request(http.MethodPost, "/api/v1/ai/profile", `{"task_id":1,"keyword":"相机","requirement":"功能正常","current":{}}`, true)
	requireStatus(t, generated, http.StatusOK)
	assertModelHTTPSafe(t, generated, `"provider_id":`, `"model":`, `"json_mode":`)
	draft := decodeBody[modelservice.GeneratedDraft](t, generated)
	if draft.Profile.Summary == "" || draft.Suggestions.BudgetMin == nil || *draft.Suggestions.BudgetMin != 10001 || draft.Suggestions.BudgetMax == nil || *draft.Suggestions.BudgetMax != 20000 || len(draft.Suggestions.ExcludeWords) != 1 || upstream.calls.Load() != 3 {
		t.Fatal("生成接口没有返回真实合并草稿和以分计的建议")
	}
	var original, watermark string
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT keyword||requirement||profile_json||budget_max FROM tasks WHERE id=1`).Scan(&original); err != nil || original != "原关键词原需求{}5000" {
		t.Fatal("生成草稿不能自动保存任务")
	}
	if err := f.store.DB().QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='task.profile_next_no.1'`).Scan(&watermark); err != nil || watermark != "10" {
		t.Fatal("生成草稿不能推进已保存编号水位")
	}
	f.advance(time.Millisecond)
	for _, window := range []string{"", "?window=today", "?window=7d", "?window=30d"} {
		response := f.request(http.MethodGet, "/api/v1/ai/usage"+window, "", true)
		requireStatus(t, response, http.StatusOK)
		usage := decodeBody[modelservice.UsageReport](t, response)
		if usage.Total.Calls != 3 || usage.Total.OKCalls != 3 || usage.Total.InputTokens != 9 || usage.Total.OutputTokens != 12 || usage.Total.MissingUsageCalls != 0 {
			t.Fatal("用量窗口必须返回真实调用统计，默认窗口为今天")
		}
	}
	filtered := f.request(http.MethodGet, "/api/v1/ai/usage?task_id=1&purpose=profile", "", true)
	requireStatus(t, filtered, http.StatusOK)
	usage := decodeBody[modelservice.UsageReport](t, filtered)
	if usage.Total.Calls != 1 || len(usage.ByTask) != 1 || usage.ByTask[0].TaskID == nil || *usage.ByTask[0].TaskID != 1 || len(usage.ByPurpose) != 1 || usage.ByPurpose[0].Purpose != string(client.Profile) {
		t.Fatal("任务和用途筛选不能被HTTP接线丢掉")
	}
	actual, err := models.Usage(t.Context(), modelservice.UsageQuery{Window: modelservice.UsageToday, TaskID: usage.ByTask[0].TaskID, Purpose: &usage.ByPurpose[0].Purpose})
	if err != nil || !reflect.DeepEqual(actual, usage) {
		t.Fatal("接口统计与真实模型服务结果不一致")
	}
}

func TestBusinessModelsTrialDistinguishesCapabilityAndInfrastructureFailures(t *testing.T) {
	for _, mode := range []string{"capability", "receipt", "config-changed", "generation-auth"} {
		t.Run(mode, func(t *testing.T) {
			f, models := newModelHTTPFixture(t)
			var providerID atomic.Int64
			upstream := newModelHTTPAI(t, func(w http.ResponseWriter, r *http.Request, kind string) bool {
				switch {
				case mode == "capability" && kind == "listing_facts":
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"image unsupported model-private-key"}`))
					return true
				case mode == "generation-auth":
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":"invalid model-private-key"}`))
					return true
				case mode == "config-changed":
					input := modelHTTPInput("http://" + r.Host + "/v1")
					input.VisionModel = "changed-vision"
					if _, err := models.UpdateProvider(r.Context(), providerID.Load(), input); err != nil {
						t.Error(err)
					}
				}
				return false
			})
			provider := createModelHTTPProvider(t, f, modelHTTPInput(upstream.server.URL+"/v1"))
			providerID.Store(provider.ID)
			if mode == "receipt" {
				if _, err := f.store.DB().ExecContext(t.Context(), `CREATE TRIGGER reject_model_receipt BEFORE INSERT ON ai_calls BEGIN SELECT RAISE(ABORT,'private-sql-marker'); END`); err != nil {
					t.Fatal(err)
				}
			}
			path, body := fmt.Sprintf("/api/v1/ai/providers/%d/test", provider.ID), ""
			status := http.StatusServiceUnavailable
			if mode == "capability" {
				status = http.StatusOK
			} else if mode == "config-changed" {
				status = http.StatusConflict
			} else if mode == "generation-auth" {
				path, body = "/api/v1/ai/profile", `{"keyword":"相机","requirement":"","current":{}}`
			}
			response := f.request(http.MethodPost, path, body, true)
			requireStatus(t, response, status)
			assertModelHTTPSafe(t, response)
			if mode == "capability" {
				report := decodeBody[modelservice.TrialReport](t, response)
				if report.Passed || report.Text.State != modelservice.TrialPassed || report.Vision.State != modelservice.TrialFailed || report.Vision.Reason == nil || report.Vision.Reason.Code != "analysis.client.unsupported_image" || upstream.calls.Load() != 2 {
					t.Fatal("普通模型能力失败应保留两步报告并返回200")
				}
			} else {
				code := map[string]string{"receipt": "service.storage_failure", "config-changed": "analysis.client.config_changed", "generation-auth": "analysis.client.auth"}[mode]
				if errorCode(response) != code || upstream.calls.Load() != 1 {
					t.Fatal("配置变更、基础设施或生成失败应返回安全业务错误，并停止后续请求")
				}
			}
			if mode == "receipt" {
				again := f.request(http.MethodPost, path, body, true)
				requireStatus(t, again, http.StatusServiceUnavailable)
				if upstream.calls.Load() != 1 {
					t.Fatal("下一次HTTP试分析不能绕过共享记账故障锁")
				}
			}
		})
	}
}

func TestBusinessModelsProfileDraftRejectsNestedNullBeforeRequests(t *testing.T) {
	f, _ := newModelHTTPFixture(t)
	upstream := newModelHTTPAI(t, nil)
	createModelHTTPProvider(t, f, modelHTTPInput(upstream.server.URL+"/v1"))
	request := func(current, taskID string) *httptest.ResponseRecorder {
		t.Helper()
		return f.request(http.MethodPost, "/api/v1/ai/profile", `{"keyword":"相机","requirement":"功能正常","task_id":`+taskID+`,"current":`+current+`}`, true)
	}
	valid := `{"musts":[{"text":"功能正常","source":"user","edited":true}]}`
	requireStatus(t, request(valid, "null"), http.StatusOK)
	before := upstream.calls.Load()
	for _, invalid := range []string{
		`{"musts":[{"text":"功能正常","source":"user","edited":null}]}`,
		`{"musts":null}`,
		`{"musts":[null]}`,
		`{"summary":null}`,
		`{"musts":[{"text":null,"source":"user"}]}`,
		`{"specs":[{"name":"型号","options":null,"values":[],"source":"user"}]}`,
		`{"specs":[{"name":"型号","options":[],"values":[null],"source":"user"}]}`,
	} {
		response := request(invalid, "null")
		requireStatus(t, response, http.StatusBadRequest)
		value := decodeBody[ErrorEnvelope](t, response)
		if len(value.Fields) != 1 || value.Fields[0].Key != "current" || upstream.calls.Load() != before {
			t.Fatal("清单非可空成员的null必须指向current，且不能发出模型请求")
		}
	}
	overflow := request(`{"next_no":9223372036854775808,"musts":[{"text":"功能正常","source":"user","edited":null}]}`, "null")
	requireStatus(t, overflow, http.StatusBadRequest)
	if value := decodeBody[ErrorEnvelope](t, overflow); len(value.Fields) != 1 || value.Fields[0].Key != "current.next_no" || upstream.calls.Load() != before {
		t.Fatal("解码必须先于清单形状检查，保留整数溢出的原字段")
	}
	requireStatus(t, request(`{}`, "null"), http.StatusOK)
	if upstream.calls.Load() != before+1 {
		t.Fatal("合法空清单与可空task_id必须仍可生成")
	}
}
