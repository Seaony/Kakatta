package modelservice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
)

const trialTitleJSON = `{"items":[{"no":1,"kind":"sale","specs":[{"name":"型号","value":"轻舟14"},{"name":"内存","value":"16G"},{"name":"存储","value":"512G"}],"brand_new":false,"verdict":"pass","reason":"个人出售"},{"no":2,"kind":"wanted","specs":[],"brand_new":null,"verdict":"pass","reason":"求购"},{"no":3,"kind":"accessory","specs":[],"brand_new":null,"verdict":"pass","reason":"保护壳"},{"no":4,"kind":"rental","specs":[],"brand_new":null,"verdict":"pass","reason":"租赁"},{"no":5,"kind":"sale","specs":[{"name":"内存","value":"8G"},{"name":"存储","value":"256G"}],"brand_new":false,"verdict":"pass","reason":"低配出售"}]}`
const trialFactsJSON = `{"specs":[{"name":"型号","value":"轻舟14"},{"name":"内存","value":"16G"},{"name":"存储","value":"512G"}],"brand_new":false,"musts":[{"id":"m1","status":"met","source":"text","evidence":"功能正常"}],"bonuses":[{"id":"b2","status":"met","source":"text","evidence":"带充电器"}],"checks":[{"key":"cosmetic_damage","status":"risk","source":"text","evidence":"外壳有细微划痕"}],"adjust":{"value":0,"reason":"按清单核对"},"summary":"自用笔记本，外壳有细微划痕"}`

type trialCaptured struct {
	body map[string]any
	auth string
}

type trialUpstream struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  []trialCaptured
}

func trialServer(t *testing.T, handle func(http.ResponseWriter, *http.Request, int, map[string]any)) *trialUpstream {
	t.Helper()
	upstream := &trialUpstream{}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("模型请求不是有效JSON：%v", err)
			w.WriteHeader(400)
			return
		}
		upstream.mu.Lock()
		index := len(upstream.calls)
		upstream.calls = append(upstream.calls, trialCaptured{body: body, auth: r.Header.Get("Authorization")})
		upstream.mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Cookie") != "" {
			t.Errorf("请求路径、方法或Cookie错误：%s %s", r.Method, r.URL.Path)
		}
		if handle != nil {
			handle(w, r, index, body)
			return
		}
		trialReply(w, trialPayload(body), "stop", trialUsage(2, 3, nil))
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

func (s *trialUpstream) snapshot() []trialCaptured {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]trialCaptured(nil), s.calls...)
}

func trialUser(body map[string]any) any {
	messages := body["messages"].([]any)
	return messages[len(messages)-1].(map[string]any)["content"]
}

func trialIsTitle(body map[string]any) bool { _, ok := trialUser(body).(string); return ok }

func trialPayload(body map[string]any) string {
	if trialIsTitle(body) {
		return trialTitleJSON
	}
	return trialFactsJSON
}

func trialUsage(input, output int64, reasoning *int64) map[string]any {
	usage := map[string]any{"prompt_tokens": input, "completion_tokens": output}
	if reasoning != nil {
		usage["completion_tokens_details"] = map[string]any{"reasoning_tokens": *reasoning}
	}
	return usage
}

func trialReply(w http.ResponseWriter, content, finish string, usage map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	response := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": finish}}}
	if usage != nil {
		response["usage"] = usage
	}
	_ = json.NewEncoder(w).Encode(response)
}

func trialProvider(t *testing.T, f *modelFixture, upstream *trialUpstream) ProviderView {
	t.Helper()
	input := modelInput()
	input.BaseURL = upstream.server.URL + "/v1"
	input.TextModel = modelPointer("trial-text")
	input.VisionModel = "trial-vision"
	input.APIKey = modelPointer("trial-only-key")
	return f.create(t, input)
}

func TestTrialProviderUsesFormalTwoStepsAndBudgetException(t *testing.T) {
	f := newModelFixture(t)
	var id int64
	upstream := trialServer(t, func(w http.ResponseWriter, _ *http.Request, _ int, body map[string]any) {
		f.s.slots.mu.Lock()
		active := f.s.slots.active
		f.s.slots.mu.Unlock()
		if active != 1 {
			t.Errorf("试分析没有使用共享名额：%d", active)
		}
		if !modelScalar[bool](t, f, `SELECT error_state IS NOT NULL FROM ai_providers WHERE id=?`, id) {
			t.Error("完整两步前清除了错误状态")
		}
		if trialIsTitle(body) {
			trialReply(w, trialTitleJSON, "stop", trialUsage(3, 4, nil))
			return
		}
		trialReply(w, "<think>先核查</think>"+trialFactsJSON, "stop", trialUsage(7, 5, modelPointer(int64(2))))
	})
	provider := trialProvider(t, f, upstream)
	id = provider.ID
	f.exec(t, `UPDATE ai_providers SET error_state='{"code":"analysis.client.balance","args":{}}',error_at=? WHERE id=?`, f.now.Load(), id)
	applyModelSettings(t, f, map[string]any{"ai.daily_token_limit": 1, "ai.max_concurrent_requests": 1})
	f.exec(t, `INSERT INTO ai_calls(at,provider_id,model,purpose,ok,json_mode,input_tokens,output_tokens) VALUES(?,?,'seed','analysis',1,'schema',1,0)`, f.now.Load(), id)
	report, err := f.s.TestProvider(t.Context(), id)
	if err != nil || !report.Passed || report.Text.State != TrialPassed || report.Vision.State != TrialPassed {
		t.Fatalf("正式两步失败：%+v %v", report, err)
	}
	calls := upstream.snapshot()
	if len(calls) != 2 || calls[0].body["model"] != "trial-text" || calls[1].body["model"] != "trial-vision" {
		t.Fatalf("没有分别测试两个模型：%+v", calls)
	}
	if calls[0].body["max_tokens"] != float64(3000) || calls[1].body["max_tokens"] != float64(2000) || calls[0].body["temperature"] != .1 || calls[1].body["temperature"] != .1 {
		t.Fatal("试分析参数偏离正式两层")
	}
	for _, call := range calls {
		format := call.body["response_format"].(map[string]any)
		if format["type"] != "json_schema" || call.auth != "Bearer trial-only-key" {
			t.Fatal("没有沿正式Schema或鉴权发送")
		}
	}
	text := trialUser(calls[0].body).(string)
	for _, token := range []string{`"no":1`, `"no":2`, `"no":3`, `"no":4`, `"no":5`, "轻舟14", "求购", "保护壳", "租赁"} {
		if !strings.Contains(text, token) {
			t.Fatalf("标题样例不完整：%s", token)
		}
	}
	parts := trialUser(calls[1].body).([]any)
	if len(parts) != 2 || parts[0].(map[string]any)["type"] != "image_url" || parts[1].(map[string]any)["type"] != "text" {
		t.Fatal("必须先发唯一照片，再发文字")
	}
	photo := parts[0].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	encoded, ok := strings.CutPrefix(photo, "data:image/jpeg;base64,")
	if !ok {
		t.Fatal("样例照片不是本地data URL")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	bounds := decoded.Bounds()
	if max(bounds.Dx(), bounds.Dy()) != 1024 || min(bounds.Dx(), bounds.Dy()) < 512 {
		t.Fatalf("样例照片是占位尺寸：%v", bounds)
	}
	if report.Text.InputTokens != 3 || report.Text.OutputTokens != 4 || report.Vision.InputTokens != 7 || report.Vision.OutputTokens != 5 || report.Vision.ReasoningTokens == nil || *report.Vision.ReasoningTokens != 2 || report.Vision.Thinking == nil || !*report.Vision.Thinking {
		t.Fatalf("实际用量或思考信息丢失：%+v", report)
	}
	if report.Text.ReasoningTokens != nil || report.Text.UsageIncomplete || report.Vision.UsageIncomplete || report.Text.DurationMS == nil || report.Vision.DurationMS == nil || report.Vision.HTTPStatus == nil || *report.Vision.HTTPStatus != 200 {
		t.Fatalf("缺失/耗时/HTTP映射错误：%+v", report)
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE purpose='test'`) != 2 || modelScalar[int](t, f, `SELECT sum(input_tokens+output_tokens) FROM ai_calls WHERE purpose='test'`) != 19 {
		t.Fatal("试分析日限例外没有正常记账")
	}
	if !modelScalar[bool](t, f, `SELECT error_state IS NULL AND error_at IS NULL FROM ai_providers WHERE id=?`, id) {
		t.Fatal("两步通过后没有清错误状态")
	}
	f.s.slots.mu.Lock()
	active := f.s.slots.active
	f.s.slots.mu.Unlock()
	if active != 0 {
		t.Fatal("试分析泄漏了全局名额")
	}
}

func TestTrialProviderPreservesCapabilityFailuresAndPartialResults(t *testing.T) {
	for _, tc := range []struct {
		name, phase, detail, reason string
		status, count               int
		truncated                   bool
	}{
		{"auth", "text", "bad trial-only-key", "auth", 401, 2, false},
		{"forbidden", "text", "", "auth", 403, 2, false},
		{"balance", "text", "", "balance", 402, 2, false},
		{"not_found", "text", "", "not_found", 404, 2, false},
		{"rate", "text", "slow down", "rate_limit", 429, 2, false},
		{"server", "vision", "busy", "server", 503, 2, false},
		{"image", "vision", "image unsupported", "unsupported_image", 400, 2, false},
		{"parse", "vision", "not JSON", "parse", 200, 3, false},
		{"thinking", "text", "cut off", "thinking_too_long", 200, 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newModelFixture(t)
			upstream := trialServer(t, func(w http.ResponseWriter, _ *http.Request, _ int, body map[string]any) {
				failed := trialIsTitle(body) == (tc.phase == "text")
				if !failed {
					trialReply(w, trialPayload(body), "stop", trialUsage(2, 3, nil))
					return
				}
				if tc.status == 200 {
					finish := "stop"
					if tc.truncated {
						finish = "length"
					}
					trialReply(w, tc.detail, finish, trialUsage(2, 3, nil))
					return
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": tc.detail})
			})
			p := trialProvider(t, f, upstream)
			f.exec(t, `UPDATE ai_providers SET error_state='{"code":"analysis.client.balance","args":{}}',error_at=? WHERE id=?`, f.now.Load(), p.ID)
			report, err := f.s.TestProvider(t.Context(), p.ID)
			if err != nil || report.Passed || report.Reason == nil {
				t.Fatalf("能力失败应完整返回报告：%+v %v", report, err)
			}
			bad, good := report.Text, report.Vision
			if tc.phase == "vision" {
				bad, good = report.Vision, report.Text
			}
			if bad.State != TrialFailed || bad.Reason == nil || bad.Reason.Code != "analysis.client."+tc.reason || good.State != TrialPassed {
				t.Fatalf("失败或部分成功被吞掉：%+v", report)
			}
			if len(upstream.snapshot()) != tc.count || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE purpose='test'`) != tc.count {
				t.Fatal("请求或逐次记账数量错误")
			}
			if !modelScalar[bool](t, f, `SELECT error_state IS NOT NULL FROM ai_providers WHERE id=?`, p.ID) {
				t.Fatal("部分失败清除了错误状态")
			}
			raw, _ := json.Marshal(report)
			if strings.Contains(string(raw), "trial-only-key") {
				t.Fatalf("报告暴露Key：%s", raw)
			}
		})
	}
}

func TestTrialProviderFallsBackToVisionModelForExplicitDisabledProvider(t *testing.T) {
	f := newModelFixture(t)
	upstream := trialServer(t, nil)
	input := modelInput()
	input.BaseURL = upstream.server.URL + "/v1"
	input.VisionModel, input.Enabled = "shared-model", false
	p := f.create(t, input)
	report, err := f.s.TestProvider(t.Context(), p.ID)
	if err != nil || !report.Passed || report.Text.Model == nil || *report.Text.Model != "shared-model" || report.Vision.Model == nil || *report.Vision.Model != "shared-model" {
		t.Fatalf("显式试分析被启用状态挡住或没有回落模型：%+v %v", report, err)
	}
	if len(upstream.snapshot()) != 2 || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE purpose='test'`) != 2 {
		t.Fatal("同一个模型仍需分别测试两层并记账")
	}
}

func TestTrialProviderReportsLocalConnectionAndTimeoutAsCapabilityFailures(t *testing.T) {
	for _, mode := range []string{"localhost", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			f := newModelFixture(t)
			upstream := trialServer(t, func(_ http.ResponseWriter, r *http.Request, _ int, _ map[string]any) {
				<-r.Context().Done()
			})
			p := trialProvider(t, f, upstream)
			if mode == "localhost" {
				upstream.server.Close()
			} else {
				applyModelSettings(t, f, map[string]any{"ai.request_timeout_seconds": 1})
			}
			report, err := f.s.TestProvider(t.Context(), p.ID)
			if err != nil || report.Passed || report.Text.State != TrialFailed || report.Vision.State != TrialFailed {
				t.Fatalf("连接能力失败应保留两步报告：%+v %v", report, err)
			}
			for _, step := range []TrialStepReport{report.Text, report.Vision} {
				if step.Reason == nil || step.Reason.Code != "analysis.client."+mode || step.HTTPStatus != nil || !step.UsageIncomplete || step.DurationMS == nil {
					t.Fatalf("连接失败元数据错误：%+v", step)
				}
			}
			if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE purpose='test' AND error_kind=?`, mode) != 2 {
				t.Fatal("连接失败没有逐次记账")
			}
		})
	}
}

func TestTrialProviderReportsAllCompatibilityCallsAndWarnings(t *testing.T) {
	f := newModelFixture(t)
	var textCalls int
	upstream := trialServer(t, func(w http.ResponseWriter, _ *http.Request, _ int, body map[string]any) {
		if trialIsTitle(body) {
			textCalls++
			switch textCalls {
			case 1:
				w.WriteHeader(400)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "response_format unsupported", "usage": trialUsage(1, 2, modelPointer(int64(1)))})
			case 2:
				w.WriteHeader(422)
				_, _ = io.WriteString(w, `{"error":"json_object unsupported"}`)
			default:
				trialReply(w, `{"items":[]}`, "stop", trialUsage(3, 4, nil))
			}
			return
		}
		trialReply(w, `{"checks":[{"key":"unknown-check","status":"unknown"}],"adjust":{"value":0,"reason":""},"summary":"正常样例"}`, "stop", trialUsage(2, 3, nil))
	})
	p := trialProvider(t, f, upstream)
	report, err := f.s.TestProvider(t.Context(), p.ID)
	if err != nil || !report.Passed {
		t.Fatalf("正式宽松解析结果应通过并保留警告：%+v %v", report, err)
	}
	if report.Text.JSONMode == nil || *report.Text.JSONMode != client.JSONText || report.Vision.JSONMode == nil || *report.Vision.JSONMode != client.JSONSchema || report.Text.InputTokens != 4 || report.Text.OutputTokens != 6 || !report.Text.UsageIncomplete || report.Text.ReasoningTokens == nil || *report.Text.ReasoningTokens != 1 {
		t.Fatalf("兼容请求的元数据未全部累积：%+v", report)
	}
	if len(report.Text.Warnings) != 5 || len(report.Vision.Warnings) != 1 || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE purpose='test'`) != 4 {
		t.Fatalf("警告或调用记录错误：%+v", report)
	}
}

func TestTrialProviderReturnsAccountingAndSQLFaults(t *testing.T) {
	for _, phase := range []string{"prompts", "receipt", "final_state"} {
		t.Run(phase, func(t *testing.T) {
			f := newModelFixture(t)
			upstream := trialServer(t, nil)
			p := trialProvider(t, f, upstream)
			switch phase {
			case "prompts":
				f.exec(t, `DROP TABLE prompts`)
			case "receipt":
				f.exec(t, `CREATE TRIGGER trial_fail_receipt BEFORE INSERT ON ai_calls BEGIN SELECT RAISE(ABORT,'trial-receipt-secret'); END`)
			case "final_state":
				f.exec(t, `CREATE TRIGGER trial_fail_clear BEFORE UPDATE OF error_state ON ai_providers WHEN NEW.error_state IS NULL BEGIN SELECT RAISE(ABORT,'trial-clear-secret'); END`)
			}
			report, err := f.s.TestProvider(t.Context(), p.ID)
			if err == nil || report.Passed || report.Reason == nil {
				t.Fatalf("基础设施故障不能伪装普通失败或成功：%+v %v", report, err)
			}
			var problem *service.ServiceError
			if !errors.As(err, &problem) || problem.Kind != service.ErrorUnavailable {
				t.Fatalf("基础设施错误没有安全映射为503：%v", err)
			}
			want := 0
			if phase == "receipt" {
				want = 1
				if report.Text.State != TrialFailed || report.Vision.State != TrialNotRun || !errors.Is(err, client.ErrInfrastructure) {
					t.Fatalf("记账故障吞掉了步骤边界：%+v %v", report, err)
				}
			}
			if phase == "final_state" {
				want = 2
				if report.Text.State != TrialPassed || report.Vision.State != TrialPassed {
					t.Fatalf("两次HTTP成功应保留能力结果：%+v", report)
				}
			}
			if len(upstream.snapshot()) != want {
				t.Fatalf("故障后继续发出了请求：%d/%d", len(upstream.snapshot()), want)
			}
			raw, _ := json.Marshal(report)
			if strings.Contains(string(raw), "trial-receipt-secret") || strings.Contains(string(raw), "trial-clear-secret") {
				t.Fatalf("SQL详情进入UI：%s", raw)
			}
			if phase == "receipt" {
				_, again := f.s.TestProvider(t.Context(), p.ID)
				if again == nil || len(upstream.snapshot()) != 1 {
					t.Fatal("再次试分析绕过了记账故障锁")
				}
			}
		})
	}
}

func TestTrialProviderCancellationPreservesPartialReportAndReceipt(t *testing.T) {
	f := newModelFixture(t)
	entered := make(chan struct{})
	upstream := trialServer(t, func(_ http.ResponseWriter, r *http.Request, _ int, _ map[string]any) {
		close(entered)
		<-r.Context().Done()
	})
	p := trialProvider(t, f, upstream)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type outcome struct {
		report TrialReport
		err    error
	}
	done := make(chan outcome, 1)
	go func() { report, err := f.s.TestProvider(ctx, p.ID); done <- outcome{report, err} }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("模型请求未开始")
	}
	cancel()
	select {
	case result := <-done:
		if !errors.Is(result.err, context.Canceled) || result.report.Passed || result.report.Text.State != TrialFailed || result.report.Vision.State != TrialNotRun || result.report.Text.Reason == nil || result.report.Text.Reason.Code != "analysis.client.cancelled" {
			t.Fatalf("取消或部分结果丢失：%+v %v", result.report, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消未结束")
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE purpose='test' AND error_kind='cancelled'`) != 1 || len(upstream.snapshot()) != 1 {
		t.Fatal("取消丢失记账或继续第二步")
	}
}

func TestTrialProviderUsesNewSavedConfigurationAndRejectsInFlightOldConfig(t *testing.T) {
	f := newModelFixture(t)
	upstream := trialServer(t, nil)
	p := trialProvider(t, f, upstream)
	if report, err := f.s.TestProvider(t.Context(), p.ID); err != nil || !report.Passed {
		t.Fatalf("第一次试分析：%+v %v", report, err)
	}
	input := modelInput()
	input.BaseURL = upstream.server.URL + "/v1"
	input.TextModel = modelPointer("saved-text")
	input.VisionModel = "saved-vision"
	input.APIKey = modelPointer("saved-only-key")
	if _, err := f.s.UpdateProvider(t.Context(), p.ID, input); err != nil {
		t.Fatal(err)
	}
	if report, err := f.s.TestProvider(t.Context(), p.ID); err != nil || !report.Passed {
		t.Fatalf("保存后试分析：%+v %v", report, err)
	}
	calls := upstream.snapshot()
	if len(calls) != 4 || calls[2].body["model"] != "saved-text" || calls[3].body["model"] != "saved-vision" || calls[2].auth != "Bearer saved-only-key" || calls[3].auth != "Bearer saved-only-key" {
		t.Fatal("连接复用导致用了保存前配置")
	}

	var changed atomic.Bool
	late := trialServer(t, func(w http.ResponseWriter, r *http.Request, _ int, body map[string]any) {
		if changed.CompareAndSwap(false, true) {
			updated := input
			updated.Name = "在途修改"
			if _, err := f.s.UpdateProvider(r.Context(), p.ID, updated); err != nil {
				t.Errorf("更新失败：%v", err)
			}
		}
		trialReply(w, trialPayload(body), "stop", trialUsage(2, 3, nil))
	})
	input.BaseURL = late.server.URL + "/v1"
	if _, err := f.s.UpdateProvider(t.Context(), p.ID, input); err != nil {
		t.Fatal(err)
	}
	// 改Key使在途响应不能清新配置状态；改名称本身不换代。
	input.APIKey = modelPointer("new-in-flight-key")
	report, err := f.s.TestProvider(t.Context(), p.ID)
	if !errors.Is(err, client.ErrConfigChanged) || report.Passed || report.Text.State != TrialFailed || report.Vision.State != TrialNotRun || len(late.snapshot()) != 1 {
		t.Fatalf("旧请求结果覆盖了新配置：%+v %v", report, err)
	}
	var problem *service.ServiceError
	if !errors.As(err, &problem) || problem.Kind != service.ErrorConflict || problem.Reason.Code != "analysis.client.config_changed" {
		t.Fatalf("配置换代没有返回安全409：%v", err)
	}
}

func TestTrialProviderReturnsRealNotFoundBeforeAnyRequest(t *testing.T) {
	f := newModelFixture(t)
	upstream := trialServer(t, nil)
	p := trialProvider(t, f, upstream)
	if err := f.s.DeleteProvider(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{p.ID, p.ID + 1} {
		report, err := f.s.TestProvider(t.Context(), id)
		var problem *service.ServiceError
		if !errors.As(err, &problem) || problem.Kind != service.ErrorNotFound || problem.Reason.Code != "ai.provider_not_found" || report.Passed || report.Text.State != TrialNotRun || report.Vision.State != TrialNotRun {
			t.Fatalf("不存在的服务商没有返回真实404：%+v %v", report, err)
		}
	}
	if len(upstream.snapshot()) != 0 || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls`) != 0 {
		t.Fatal("404仍发出了模型请求")
	}
}

func TestTrialRequestFailureKeepsSafeReasonsAndOriginalCauses(t *testing.T) {
	for _, tc := range []struct {
		cause error
		kind  service.ErrorKind
		code  string
	}{
		{client.ErrConfigChanged, service.ErrorConflict, "analysis.client.config_changed"},
		{client.ErrInfrastructure, service.ErrorUnavailable, "analysis.client.infrastructure"},
		{client.ErrDailyLimit, service.ErrorUnavailable, "analysis.client.daily_limit"},
		{client.ErrNoProvider, service.ErrorUnavailable, "analysis.client.no_provider"},
		{errors.New("private-SQL-and-response-body"), service.ErrorUnavailable, "service.storage_failure"},
		{&client.Failure{Last: locales.Message{Code: "analysis.client.auth", Args: map[string]any{"status": 401, "detail": "已遮敏详情"}}}, service.ErrorUnavailable, "analysis.client.auth"},
	} {
		err := requestFailure(tc.cause)
		var problem *service.ServiceError
		if !errors.Is(err, tc.cause) || !errors.As(err, &problem) || problem.Kind != tc.kind || problem.Reason.Code != tc.code {
			t.Fatalf("请求错误丢失类型或原因：%v", err)
		}
		raw, _ := json.Marshal(problem)
		if strings.Contains(string(raw), "private-SQL-and-response-body") {
			t.Fatalf("原始错误泄露到响应：%s", raw)
		}
	}
	for _, cause := range []error{nil, context.Canceled, fmt.Errorf("中止：%w", context.DeadlineExceeded), missingProvider()} {
		if requestFailure(cause) != cause {
			t.Fatal("已有业务错误或取消被改变")
		}
	}
}

func TestTrialStepMapsMissingUsageWithoutFakingNotRunSuccess(t *testing.T) {
	if step, err := trialStep(client.TestStep{}); err != nil || step.State != TrialNotRun || step.Model != nil || step.Thinking != nil || step.DurationMS != nil {
		t.Fatalf("零值步骤不能通过：%+v %v", step, err)
	}
	failure := &client.Failure{Last: locales.Message{Code: "analysis.client.parse", Args: map[string]any{"status": 200, "detail": "已遮密详情"}}}
	result := client.TestStep{Err: failure, Result: client.Result{Model: "meta-model", JSONMode: client.JSONSchema, Thinking: true, Duration: 1234 * time.Millisecond, Calls: []client.Call{
		{Start: client.CallStart{Model: "actual-model", JSONMode: client.JSONSchema}, End: client.CallEnd{HTTPStatus: 400, Usage: client.Usage{Present: true, InputTokens: 2, OutputTokens: 3, ReasoningTokens: modelPointer(int64(1))}}},
		{Start: client.CallStart{Model: "actual-model", JSONMode: client.JSONObject}, End: client.CallEnd{HTTPStatus: 200, Usage: client.Usage{Present: false, InputTokens: 99, OutputTokens: 99}}},
		{Start: client.CallStart{Model: "actual-model", JSONMode: client.JSONText}, End: client.CallEnd{HTTPStatus: 200, Usage: client.Usage{Present: true, InputTokens: 4, OutputTokens: 5, ReasoningTokens: modelPointer(int64(2))}}},
	}}}
	step, err := trialStep(result)
	if err != nil || step.State != TrialFailed || step.InputTokens != 6 || step.OutputTokens != 8 || step.ReasoningTokens == nil || *step.ReasoningTokens != 3 || !step.UsageIncomplete || step.DurationMS == nil || *step.DurationMS != 1234 || step.JSONMode == nil || *step.JSONMode != client.JSONText || step.Model == nil || *step.Model != "actual-model" {
		t.Fatalf("实际逐Call映射错误：%+v %v", step, err)
	}
	step.Reason.Args["detail"] = "报告修改"
	if failure.Last.Args["detail"] != "已遮密详情" {
		t.Fatal("报告修改影响了原始原因")
	}
	result.Result.Calls[0].End.Usage.InputTokens = math.MaxInt64 - 3
	if step, err := trialStep(result); err == nil || !step.UsageIncomplete {
		t.Fatal("用量累计溢出被吞掉")
	}
}

func TestTrialMessagesAreSafeAndTranslated(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, client.ErrConfigChanged, client.ErrDailyLimit, client.ErrNoProvider, client.ErrInfrastructure, errors.New("private-SQL-body")} {
		reason := trialReason(err)
		for _, lang := range []locales.Language{locales.ZhCN, locales.En, locales.Ja} {
			if _, formatErr := locales.Format(lang, *reason); formatErr != nil {
				t.Fatalf("缺原因文案：%s %v", reason.Code, formatErr)
			}
		}
		if strings.Contains(fmt.Sprint(reason), "private-SQL-body") {
			t.Fatal("未知错误详情进入UI")
		}
	}
}
