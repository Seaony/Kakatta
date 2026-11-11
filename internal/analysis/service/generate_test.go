package modelservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/analysis/prompts"
	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
)

const generationEmpty = `{"summary":"会帮你找：笔记本","specs":[],"musts":[],"bonuses":[],"risks":[],"allowed_kinds":[],"budget_min":null,"budget_max":null,"keyword":null,"exclude_words":[]}`

type generationUpstream struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  []map[string]any
}

func generationServer(t *testing.T, handle func(http.ResponseWriter, *http.Request, int, map[string]any)) *generationUpstream {
	t.Helper()
	u := &generationUpstream{}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("模型请求不是JSON")
			w.WriteHeader(400)
			return
		}
		u.mu.Lock()
		index := len(u.calls)
		u.calls = append(u.calls, body)
		u.mu.Unlock()
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost || r.Header.Get("Cookie") != "" {
			t.Error("模型请求路径、方法或Cookie错误")
		}
		if handle != nil {
			handle(w, r, index, body)
			return
		}
		generationReply(w, generationEmpty)
	}))
	t.Cleanup(u.server.Close)
	return u
}

func (u *generationUpstream) snapshot() []map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]map[string]any{}, u.calls...)
}

func generationReply(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": text}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 3, "completion_tokens": 4}})
}

func generationProvider(t *testing.T, f *modelFixture, u *generationUpstream) ProviderView {
	t.Helper()
	input := modelInput()
	input.BaseURL, input.TextModel, input.VisionModel = u.server.URL+"/v1", modelPointer("generation-text"), "generation-vision"
	input.APIKey = modelPointer("generation-private-key")
	return f.create(t, input)
}

func generationOutput(t *testing.T, changes map[string]any) string {
	t.Helper()
	var output map[string]any
	if err := json.Unmarshal([]byte(generationEmpty), &output); err != nil {
		t.Fatal(err)
	}
	for key, value := range changes {
		output[key] = value
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func generationSeedTask(t *testing.T, f *modelFixture, profile analysis.Profile, watermark string, mode string) {
	t.Helper()
	encoded, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	policy := "good"
	if mode == "keyword" {
		policy = "recommended"
	}
	f.exec(t, `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index,requirement,profile_json,budget_max)
		VALUES(1,'原任务','原关键词',1,30,?,?,0,'原需求',?,300000)`, mode, policy, string(encoded))
	f.exec(t, `INSERT INTO meta(key,value) VALUES('task.profile_next_no.1',?)`, watermark)
}

func generationProblem(t *testing.T, err error, kind service.ErrorKind, code string) *service.ServiceError {
	t.Helper()
	var problem *service.ServiceError
	if !errors.As(err, &problem) || problem.Kind != kind || problem.Reason.Code != code {
		t.Fatalf("错误没有安全分类：%v", err)
	}
	for _, lang := range []locales.Language{locales.ZhCN, locales.En, locales.Ja} {
		if _, err := locales.Format(lang, problem.Reason); err != nil {
			t.Fatal(err)
		}
	}
	return problem
}

func TestGenerateProfileUsesFormalPromptAndPreservesDraftWithoutSaving(t *testing.T) {
	f := newModelFixture(t)
	old := analysis.Profile{Musts: []analysis.Requirement{{ID: "m1", Text: "国行", Source: analysis.SourceUser, Rev: 3}},
		Risks: []analysis.ProfileRisk{{ID: "c2", Name: "屏幕", Hint: "无亮斑", Source: analysis.SourceUser, Attitude: analysis.Mind, Rev: 4}}, NextNo: 3}
	generationSeedTask(t, f, old, "10", "ai")
	current := analysis.Profile{
		Specs:   []analysis.ProfileSpec{{Name: "存储", Options: []string{"256G", "512G"}, Values: []string{"512G"}, Source: analysis.SourceUser, Edited: true}},
		Musts:   []analysis.Requirement{{ID: "m1", Text: "日版", Source: analysis.SourceUser, Edited: true, Rev: 999}},
		Bonuses: []analysis.Requirement{{ID: "b10", Text: "原盒", Source: analysis.SourceUser, Edited: true, Rev: 999}},
		Risks: []analysis.ProfileRisk{
			{ID: "c2", Name: "屏幕", Hint: "无亮斑、无压伤", Source: analysis.SourceUser, Attitude: analysis.Reject, Edited: true, Rev: 999},
			{ID: "c11", Name: "压伤", Hint: "不能有压伤", Source: analysis.SourceUser, Attitude: analysis.Confirm, Edited: true, Rev: 999},
		}, NextNo: 12,
	}
	output := generationOutput(t, map[string]any{
		"specs":      []any{map[string]any{"name": "存储", "options": []string{"256G"}, "values": []string{"256G"}, "source": "ai"}},
		"musts":      []any{map[string]any{"text": "日版", "source": "user"}, map[string]any{"text": "有发票", "source": "user"}},
		"risks":      []any{map[string]any{"id": "id_lock", "name": "模型乱改名", "hint": "模型乱改说明", "question": nil, "attitude": nil, "source": "ai"}},
		"budget_min": 1000.005, "budget_max": 2000, "keyword": "轻舟14", "exclude_words": []string{"求购", "回收"},
	})
	u := generationServer(t, func(w http.ResponseWriter, _ *http.Request, _ int, _ map[string]any) {
		f.s.slots.mu.Lock()
		active := f.s.slots.active
		f.s.slots.mu.Unlock()
		if active != 1 {
			t.Error("生成没有沿共享名额")
		}
		generationReply(w, "<think>先核对用户的要求</think>"+output)
	})
	p := generationProvider(t, f, u)
	definitions, err := prompts.Definitions()
	if err != nil {
		t.Fatal(err)
	}
	var template string
	for _, definition := range definitions {
		if definition.Kind == client.Profile {
			template = definition.Default + "\n本地保存的模板标记"
		}
	}
	if _, err := f.s.SavePrompt(t.Context(), client.Profile, template); err != nil {
		t.Fatal(err)
	}
	beforeInput, _ := json.Marshal(current)
	beforeTask := modelScalar[string](t, f, `SELECT profile_json || requirement || keyword || budget_max FROM tasks WHERE id=1`)
	events := promptEventCount(f)
	requirement := "  日版，预算一两千；保留我的手改清单。  "
	draft, err := f.s.GenerateProfile(t.Context(), GenerateProfileInput{TaskID: modelPointer(int64(1)), Keyword: "  轻舟14  ", Requirement: requirement, Current: current})
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Profile.Specs) != 1 || !reflect.DeepEqual(draft.Profile.Specs[0], current.Specs[0]) || len(draft.Profile.Musts) != 1 || draft.Profile.Musts[0].ID != "m1" || draft.Profile.Musts[0].Text != "日版" || draft.Profile.Musts[0].Rev != 4 {
		t.Fatalf("编辑条目或真实版本丢失：%+v", draft.Profile)
	}
	bonuses := map[string]analysis.Requirement{}
	for _, item := range draft.Profile.Bonuses {
		bonuses[item.Text] = item
	}
	if bonuses["原盒"].ID != "b10" || bonuses["原盒"].Rev != 1 || bonuses["有发票"].Source != analysis.SourceAI || len(draft.Warnings) != 1 || draft.Warnings[0].Code != "analysis.must_downgraded" {
		t.Fatalf("来源核对或草稿新号丢失：%+v %+v", draft.Profile, draft.Warnings)
	}
	risks := map[string]analysis.ProfileRisk{}
	for _, item := range draft.Profile.Risks {
		risks[item.ID] = item
	}
	if risks["c2"].Rev != 5 || risks["c2"].Hint != current.Risks[0].Hint || risks["c11"].Rev != 1 || !risks["c11"].Edited || risks["c11"].Attitude != analysis.Confirm || risks["id_lock"].Name != "" || risks["off_platform"].Source != analysis.SourceSystem {
		t.Fatalf("自定义/字典/通用项没有沿正式规则：%+v", draft.Profile.Risks)
	}
	if draft.Suggestions.BudgetMin == nil || *draft.Suggestions.BudgetMin != 100001 || draft.Suggestions.BudgetMax == nil || *draft.Suggestions.BudgetMax != 200000 || draft.Suggestions.Keyword == nil || *draft.Suggestions.Keyword != "轻舟14" || len(draft.Suggestions.ExcludeWords) != 2 {
		t.Fatalf("建议换算错误：%+v", draft.Suggestions)
	}
	calls := u.snapshot()
	if len(calls) != 1 || calls[0]["model"] != "generation-text" || calls[0]["temperature"] != .3 || calls[0]["max_tokens"] != float64(2000) || calls[0]["response_format"].(map[string]any)["type"] != "json_schema" {
		t.Fatalf("生成没有用正式参数：%+v", calls)
	}
	messages := calls[0]["messages"].([]any)
	user := messages[1].(map[string]any)["content"].(string)
	if messages[0].(map[string]any)["content"] != template+"\n\n"+prompts.SellerDataSystemSuffix || !strings.Contains(user, "我的要求："+requirement) || !strings.Contains(user, `"id":"c11"`) || strings.Contains(user, `"rev"`) || strings.Contains(user, "next_no") || !strings.Contains(user, `"id":"seller_new"`) {
		t.Fatalf("模板、原文或输入投影不正确：%s", user)
	}
	afterInput, _ := json.Marshal(current)
	if !bytes.Equal(beforeInput, afterInput) || modelScalar[string](t, f, `SELECT profile_json || requirement || keyword || budget_max FROM tasks WHERE id=1`) != beforeTask || modelScalar[string](t, f, `SELECT value FROM meta WHERE key='task.profile_next_no.1'`) != "10" {
		t.Fatal("生成改写了输入、任务或水位")
	}
	f.mu.Lock()
	published := append([]string{}, f.published[events:]...)
	f.mu.Unlock()
	if !reflect.DeepEqual(published, []string{"ai.usage.changed"}) {
		t.Fatalf("生成发布了用量以外的保存事件：%v", published)
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE task_id=1 AND provider_id=? AND purpose='profile' AND ok=1`, p.ID) != 1 || len(draft.Request.Calls) != 1 || !draft.Request.Thinking {
		t.Fatal("没有逐次正常计费或保留内部请求信息")
	}
	raw, _ := json.Marshal(draft)
	if strings.Contains(string(raw), "provider_id") || strings.Contains(string(raw), "generation-private-key") {
		t.Fatalf("内部请求信息进入结果：%s", raw)
	}
}

func TestGenerateProfileUsesCompatibilityWithoutRetryingThinkingOrLocalMergeErrors(t *testing.T) {
	for _, phase := range []string{"compatibility", "thinking", "local-merge"} {
		t.Run(phase, func(t *testing.T) {
			f := newModelFixture(t)
			output := generationEmpty
			if phase == "thinking" {
				output = "<think>还没有结束的思考"
			}
			if phase == "local-merge" {
				output = generationOutput(t, map[string]any{"specs": []any{map[string]any{"name": "存储", "options": []string{"256G"}, "values": []string{"512G"}, "source": "user"}}})
			}
			u := generationServer(t, func(w http.ResponseWriter, _ *http.Request, index int, _ map[string]any) {
				if phase == "compatibility" && index == 0 {
					w.WriteHeader(400)
					_, _ = w.Write([]byte(`{"error":"response_format unsupported"}`))
					return
				}
				generationReply(w, output)
			})
			p := generationProvider(t, f, u)
			draft, err := f.s.GenerateProfile(t.Context(), GenerateProfileInput{Keyword: "相机"})
			calls := 1
			if phase == "compatibility" {
				calls = 2
				if err != nil || len(draft.Request.Calls) != 2 || draft.Request.JSONMode != client.JSONObject || modelScalar[string](t, f, `SELECT json_mode FROM ai_compat WHERE provider_id=? AND model='generation-text'`, p.ID) != "object" {
					t.Fatalf("兼容处理没有沿共享客户端：%+v %v", draft, err)
				}
			} else {
				code := "analysis.profile_invalid"
				if phase == "thinking" {
					code = "analysis.client.profile_thinking"
				}
				generationProblem(t, err, service.ErrorUnavailable, code)
				if draft.Profile.NextNo != 0 {
					t.Fatal("失败仍返回了可保存的草稿")
				}
			}
			if len(u.snapshot()) != calls || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE purpose='profile'`) != calls {
				t.Fatal("兼容没有逐次记账，或本地错误多发了请求")
			}
		})
	}
}

func TestGenerateProfileKeepsUnsavedNumbersAcrossRequestsAndFreshDrafts(t *testing.T) {
	f := newModelFixture(t)
	generationSeedTask(t, f, analysis.Profile{NextNo: 10}, "10", "ai")
	u := generationServer(t, nil)
	generationProvider(t, f, u)
	input := GenerateProfileInput{TaskID: modelPointer(int64(1)), Keyword: "相机", Current: analysis.Profile{
		Bonuses: []analysis.Requirement{{ID: "b10", Text: "原盒", Source: analysis.SourceUser, Edited: true, Rev: 999}},
		Risks:   []analysis.ProfileRisk{{ID: "c11", Name: "压伤", Attitude: analysis.Mind, Source: analysis.SourceUser, Edited: true, Rev: 999}}, NextNo: 12,
	}}
	first, err := f.s.GenerateProfile(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Profile.Bonuses) != 1 || first.Profile.Bonuses[0].ID != "b10" || first.Profile.NextNo != 12 {
		t.Fatalf("第一次生成改了草稿号：%+v", first.Profile)
	}
	input.Current = first.Profile
	input.Current.Bonuses = []analysis.Requirement{}
	for _, taskID := range []*int64{modelPointer(int64(1)), nil} {
		input.TaskID = taskID
		next, err := f.s.GenerateProfile(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, risk := range next.Profile.Risks {
			if risk.ID == "c11" {
				found = risk.Edited && risk.Rev == 1 && risk.Name == "压伤"
			}
		}
		if !found || next.Profile.NextNo != 12 || len(next.Profile.Bonuses) != 0 {
			t.Fatalf("删条目后草稿号变化或复用：%+v", next.Profile)
		}
	}
	if modelScalar[string](t, f, `SELECT value FROM meta WHERE key='task.profile_next_no.1'`) != "10" {
		t.Fatal("未保存的生成推进了持久水位")
	}
}

func TestGenerateProfileRevisesFinalMeaningAgainstStoredProfile(t *testing.T) {
	f := newModelFixture(t)
	old := analysis.Profile{Musts: []analysis.Requirement{{ID: "m1", Text: "国行", Source: analysis.SourceUser, Rev: 3}}, NextNo: 2}
	generationSeedTask(t, f, old, "2", "ai")
	output := generationOutput(t, map[string]any{"musts": []any{map[string]any{"text": "国行", "source": "user"}}})
	u := generationServer(t, func(w http.ResponseWriter, _ *http.Request, _ int, _ map[string]any) { generationReply(w, output) })
	generationProvider(t, f, u)
	current := analysis.Profile{Musts: []analysis.Requirement{{ID: "m1", Text: "国 行", Source: analysis.SourceUser, Rev: 999}}, NextNo: 2}
	draft, err := f.s.GenerateProfile(t.Context(), GenerateProfileInput{TaskID: modelPointer(int64(1)), Keyword: "国行", Current: current})
	if err != nil || len(draft.Profile.Musts) != 1 || draft.Profile.Musts[0].ID != "m1" || draft.Profile.Musts[0].Text != "国行" || draft.Profile.Musts[0].Rev != 3 {
		t.Fatalf("中间草稿被当成两次持久修改：%+v %v", draft.Profile, err)
	}
}

func TestGenerateProfileUsesIndependentWatermarkAfterKeywordMode(t *testing.T) {
	f := newModelFixture(t)
	generationSeedTask(t, f, analysis.Profile{}, "37", "keyword")
	u := generationServer(t, nil)
	generationProvider(t, f, u)
	draft, err := f.s.GenerateProfile(t.Context(), GenerateProfileInput{TaskID: modelPointer(int64(1)), Keyword: "相机", Current: analysis.Profile{
		Musts: []analysis.Requirement{{Text: "国行", Source: analysis.SourceUser, Edited: true, Rev: 999}}, NextNo: 1,
	}})
	if err != nil || len(draft.Profile.Musts) != 1 || draft.Profile.Musts[0].ID != "m37" || draft.Profile.Musts[0].Rev != 1 || draft.Profile.NextNo != 38 {
		t.Fatalf("清空清单后重新发放了历史编号：%+v %v", draft.Profile, err)
	}
}

func TestGenerateProfileRejectsInvalidInputAndStorageBeforeRequests(t *testing.T) {
	for _, tc := range []string{"task-zero", "task-overflow", "keyword-empty", "keyword-long", "task-missing", "deleted-id", "invalid-profile", "broken-watermark", "prompts-sql"} {
		t.Run(tc, func(t *testing.T) {
			f := newModelFixture(t)
			generationSeedTask(t, f, analysis.Profile{NextNo: 10}, "10", "ai")
			u := generationServer(t, nil)
			generationProvider(t, f, u)
			input := GenerateProfileInput{TaskID: modelPointer(int64(1)), Keyword: "相机"}
			kind, code := service.ErrorInvalid, "tasks.invalid"
			switch tc {
			case "task-zero":
				input.TaskID = modelPointer(int64(0))
			case "task-overflow":
				input.TaskID = modelPointer(int64(limits.MaxSafeInteger + 1))
			case "keyword-empty":
				input.Keyword = "  "
			case "keyword-long":
				input.Keyword = strings.Repeat("相", 51)
			case "task-missing":
				input.TaskID = modelPointer(int64(2))
				kind, code = service.ErrorNotFound, "tasks.not_found"
			case "deleted-id":
				input.Current.Musts = []analysis.Requirement{{ID: "m3", Text: "国行", Source: analysis.SourceUser, Edited: true}}
			case "invalid-profile":
				input.Current.Musts = []analysis.Requirement{{Text: "", Source: analysis.SourceUser, Edited: true}}
			case "broken-watermark":
				f.exec(t, `UPDATE meta SET value='not-a-number' WHERE key='task.profile_next_no.1'`)
				kind, code = service.ErrorUnavailable, "service.storage_failure"
			case "prompts-sql":
				f.exec(t, `DROP TABLE prompts`)
				kind, code = service.ErrorUnavailable, "service.storage_failure"
			}
			_, err := f.s.GenerateProfile(t.Context(), input)
			generationProblem(t, err, kind, code)
			if len(u.snapshot()) != 0 || modelScalar[int](t, f, `SELECT count(*) FROM ai_calls`) != 0 {
				t.Fatal("输入或存储失败仍发送了请求")
			}
		})
	}
}

func TestGenerateProfileDropsInvalidBudgetsWithoutRetryOrRawValueLogs(t *testing.T) {
	for _, budgets := range []string{`"budget_min":-987654321,"budget_max":12.345`, `"budget_min":"private-budget-text","budget_max":1e999`, `"budget_min":40,"budget_max":20`} {
		t.Run(budgets, func(t *testing.T) {
			f := newModelFixture(t)
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(previous)
			output := strings.Replace(generationEmpty, `"budget_min":null,"budget_max":null`, budgets, 1)
			u := generationServer(t, func(w http.ResponseWriter, _ *http.Request, _ int, _ map[string]any) { generationReply(w, output) })
			generationProvider(t, f, u)
			draft, err := f.s.GenerateProfile(t.Context(), GenerateProfileInput{Keyword: "相机"})
			if err != nil || len(u.snapshot()) != 1 || len(draft.Profile.Risks) == 0 || len(draft.Warnings) != 0 {
				t.Fatalf("预算建议让正常清单失败或重发：%+v %v", draft, err)
			}
			if draft.Suggestions.BudgetMin != nil || (strings.Contains(budgets, "12.345") && (draft.Suggestions.BudgetMax == nil || *draft.Suggestions.BudgetMax != 1235)) || (!strings.Contains(budgets, "12.345") && draft.Suggestions.BudgetMax != nil) {
				t.Fatalf("无效预算未被逐项去掉：%+v", draft.Suggestions)
			}
			if !strings.Contains(logs.String(), "budget_min") || strings.Contains(logs.String(), "987654321") || strings.Contains(logs.String(), "private-budget-text") || strings.Contains(logs.String(), "1e999") {
				t.Fatalf("预算日志缺少固定字段或泄露值：%s", logs.String())
			}
		})
	}
}

func TestGenerateProfileDoesNotRotateOrBypassBudgetAndMapsFailures(t *testing.T) {
	for _, tc := range []string{"auth", "parse", "daily-limit", "receipt", "no-provider", "config-changed"} {
		t.Run(tc, func(t *testing.T) {
			f := newModelFixture(t)
			var p ProviderView
			u := generationServer(t, func(w http.ResponseWriter, r *http.Request, _ int, _ map[string]any) {
				switch tc {
				case "auth":
					w.WriteHeader(401)
					_, _ = w.Write([]byte(`{"error":"bad generation-private-key"}`))
				case "parse":
					generationReply(w, "not-json")
				case "config-changed":
					updated := modelInput()
					updated.BaseURL, updated.APIKey = "http://"+r.Host+"/v1", modelPointer("new-key")
					if _, err := f.s.UpdateProvider(r.Context(), p.ID, updated); err != nil {
						t.Errorf("在途更新失败：%v", err)
					}
					generationReply(w, generationEmpty)
				default:
					generationReply(w, generationEmpty)
				}
			})
			p = generationProvider(t, f, u)
			second := generationServer(t, nil)
			generationProvider(t, f, second)
			wantCalls := 1
			kind, code := service.ErrorUnavailable, "analysis.client."+tc
			switch tc {
			case "daily-limit":
				applyModelSettings(t, f, map[string]any{"ai.daily_token_limit": 1})
				f.exec(t, `INSERT INTO ai_calls(at,provider_id,model,purpose,ok,json_mode,input_tokens,output_tokens) VALUES(?,?,'seed','analysis',1,'schema',1,0)`, f.now.Load(), p.ID)
				wantCalls, code = 0, "analysis.client.daily_limit"
			case "receipt":
				f.exec(t, `CREATE TRIGGER generation_fail_receipt BEFORE INSERT ON ai_calls BEGIN SELECT RAISE(ABORT,'private-generation-sql'); END`)
				code = "service.storage_failure"
			case "no-provider":
				f.exec(t, `UPDATE ai_providers SET enabled=0`)
				wantCalls, code = 0, "analysis.client.no_provider"
			case "config-changed":
				kind, code = service.ErrorConflict, "analysis.client.config_changed"
			}
			_, err := f.s.GenerateProfile(t.Context(), GenerateProfileInput{Keyword: "相机"})
			problem := generationProblem(t, err, kind, code)
			if len(u.snapshot()) != wantCalls || len(second.snapshot()) != 0 {
				t.Fatal("生成失败后重试或轮换了供应商")
			}
			if tc == "config-changed" && !errors.Is(err, client.ErrConfigChanged) || tc == "receipt" && !errors.Is(err, client.ErrInfrastructure) || tc == "daily-limit" && !errors.Is(err, client.ErrDailyLimit) {
				t.Fatal("安全映射丢失了原错误类型")
			}
			raw, _ := json.Marshal(problem)
			if strings.Contains(string(raw), "generation-private-key") || strings.Contains(string(raw), "private-generation-sql") {
				t.Fatalf("失败详情泄露秘密：%s", raw)
			}
			if tc == "receipt" {
				_, again := f.s.GenerateProfile(t.Context(), GenerateProfileInput{Keyword: "相机"})
				if again == nil || len(u.snapshot()) != 1 {
					t.Fatal("下一次生成绕过记账故障锁")
				}
			}
		})
	}
}

func TestGenerateProfileCancellationKeepsReceiptAndDoesNotReturnDraft(t *testing.T) {
	f := newModelFixture(t)
	entered := make(chan struct{})
	u := generationServer(t, func(_ http.ResponseWriter, r *http.Request, _ int, _ map[string]any) {
		close(entered)
		<-r.Context().Done()
	})
	generationProvider(t, f, u)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type outcome struct {
		draft GeneratedDraft
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		draft, err := f.s.GenerateProfile(ctx, GenerateProfileInput{Keyword: "相机"})
		done <- outcome{draft, err}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("模型请求未开始")
	}
	cancel()
	select {
	case result := <-done:
		if !errors.Is(result.err, context.Canceled) || result.draft.Profile.NextNo != 0 || len(result.draft.Profile.Risks) != 0 {
			t.Fatalf("取消返回了草稿或丢失原错误：%+v %v", result.draft, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("生成没有响应取消")
	}
	if modelScalar[int](t, f, `SELECT count(*) FROM ai_calls WHERE purpose='profile' AND error_kind='cancelled'`) != 1 || len(u.snapshot()) != 1 {
		t.Fatal("取消后没有正常记账")
	}
}
