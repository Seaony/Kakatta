package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	modelservice "github.com/Seaony/Kakatta/internal/analysis/service"
	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/config"
	"github.com/Seaony/Kakatta/internal/notification"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

type coreFlowHarness struct {
	t                                *testing.T
	ctx                              context.Context
	cfg                              config.Config
	paths                            config.Paths
	key                              [32]byte
	token                            string
	g                                *runtimeGraph
	server, model, push              *httptest.Server
	xianyu                           *fake.Xianyu
	accountID, providerID, channelID int64
	mu                               sync.Mutex
	modelKinds                       []string
	modelActive, maxModelActive      int
	products                         []map[string]any
	pushed                           []map[string]string
}

func newCoreFlowHarness(t *testing.T, ctx context.Context, modelHook func(http.ResponseWriter, *http.Request, string) bool, pushHook func(http.ResponseWriter, *http.Request, map[string]string) bool) *coreFlowHarness {
	t.Helper()
	if os.Getenv("KAKATTA_CORE_FLOW_TESTS") != "1" {
		t.Skip("由 deploy/test-core-flow.sh 在断网容器里运行")
	}
	cfg, paths, key := graphConfig(t)
	return newCoreFlowHarnessWithPaths(t, ctx, cfg, paths, key, modelHook, pushHook)
}

func newCoreFlowHarnessWithPaths(t *testing.T, ctx context.Context, cfg config.Config, paths config.Paths, key [32]byte, modelHook func(http.ResponseWriter, *http.Request, string) bool, pushHook func(http.ResponseWriter, *http.Request, map[string]string) bool) *coreFlowHarness {
	t.Helper()
	h := &coreFlowHarness{t: t, ctx: ctx, cfg: cfg, paths: paths, key: key, xianyu: fake.Start()}
	t.Cleanup(h.Close)
	h.xianyu.EnableAutomaticLogin()
	h.xianyu.EnableSellerProfile()
	h.model = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.modelActive++
		h.maxModelActive = max(h.maxModelActive, h.modelActive)
		h.mu.Unlock()
		defer func() {
			h.mu.Lock()
			h.modelActive--
			h.mu.Unlock()
		}()
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer local-core-flow-key" {
			h.t.Errorf("模型请求没有经过正式兼容客户端：%s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected local request", http.StatusBadRequest)
			return
		}
		var request struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			ResponseFormat struct {
				Schema struct {
					Name string `json:"name"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 2 {
			h.t.Errorf("模型请求格式错误：%v", err)
			http.Error(w, "invalid local request", http.StatusBadRequest)
			return
		}
		kind := request.ResponseFormat.Schema.Name
		var text string
		if err := json.Unmarshal(request.Messages[1].Content, &text); err != nil {
			var parts []struct {
				Type  string `json:"type"`
				Text  string `json:"text"`
				Image struct {
					URL string `json:"url"`
				} `json:"image_url"`
			}
			if err := json.Unmarshal(request.Messages[1].Content, &parts); err != nil || len(parts) != 2 || parts[0].Type != "image_url" || !strings.HasPrefix(parts[0].Image.URL, "data:image/") || parts[1].Type != "text" {
				h.t.Errorf("详情图片没有经过正式本地下载处理：%v", err)
				http.Error(w, "invalid local images", http.StatusBadRequest)
				return
			}
			text = parts[1].Text
		}
		blocks := strings.Split(text, "```json\n")
		if len(blocks) != 3 {
			h.t.Error("模型输入缺少需求清单或商品原文")
			http.Error(w, "invalid local prompt", http.StatusBadRequest)
			return
		}
		source := strings.SplitN(blocks[2], "\n```", 2)[0]
		var reply any
		switch kind {
		case "title_screen":
			var batch []struct {
				No int `json:"no"`
			}
			if err := json.Unmarshal([]byte(source), &batch); err != nil || len(batch) == 0 {
				h.t.Errorf("标题列表无效：%v", err)
				return
			}
			items := []map[string]any{}
			for _, item := range batch {
				items = append(items, map[string]any{"no": item.No, "kind": "sale", "specs": []any{map[string]any{"name": "型号", "value": "MacBook Pro"}}, "brand_new": false, "verdict": "pass", "reason": ""})
			}
			reply = map[string]any{"items": items}
		case "listing_facts":
			var product map[string]any
			if err := json.Unmarshal([]byte(source), &product); err != nil {
				h.t.Errorf("商品原文无效：%v", err)
				return
			}
			h.mu.Lock()
			h.products = append(h.products, product)
			h.mu.Unlock()
			reply = map[string]any{"specs": []any{map[string]any{"name": "型号", "value": "MacBook Pro"}}, "brand_new": false, "musts": []any{}, "bonuses": []any{}, "checks": []any{}, "seller_signals": []any{}, "price_note": nil, "extra_findings": []any{}, "adjust": map[string]any{"value": 0, "reason": ""}, "summary": "自用设备，描述无拆修"}
		default:
			h.t.Errorf("意外的模型用途：%s", kind)
			http.Error(w, "unexpected local purpose", http.StatusBadRequest)
			return
		}
		h.mu.Lock()
		h.modelKinds = append(h.modelKinds, kind)
		h.mu.Unlock()
		if modelHook != nil && modelHook(w, r, kind) {
			return
		}
		content, _ := json.Marshal(reply)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(content)}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 10}})
	}))
	h.push = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.Method != http.MethodPost {
			h.t.Errorf("推送请求没有经过正式渠道适配器：%v", err)
			http.Error(w, "invalid local push", http.StatusBadRequest)
			return
		}
		h.mu.Lock()
		h.pushed = append(h.pushed, body)
		h.mu.Unlock()
		if pushHook != nil && pushHook(w, r, body) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	h.openGraph()
	h.token = graphAdmin(t, h.g).Token
	values := map[string]any{"general.update_check": false, "crawl.quiet_hours": map[string]any{"enabled": false, "start": "01:00", "end": "07:30"}, "crawl.account_min_gap_seconds": 2, "crawl.account_min_gap_jitter_seconds": 0, "crawl.account_hourly_visits": 200, "crawl.interval_jitter_percent": 0, "crawl.max_details_per_run": 10, "ai.max_concurrent_requests": 1, "ai.image_count": 1}
	for _, name := range []string{"home_stay_seconds", "page_wait_seconds", "detail_pre_wait_seconds", "detail_stay_seconds", "action_wait_seconds"} {
		values["crawl."+name] = map[string]float64{"min": .01, "max": .01}
	}
	values["crawl.typing_interval_ms"] = map[string]float64{"min": 1, "max": 1}
	h.Request(http.MethodPatch, "/api/v1/settings", map[string]any{"values": values}, nil, http.StatusOK)
	h.startGraph()
	var account service.AccountView
	h.Request(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "本地扫码账号"}, &account, http.StatusCreated)
	h.accountID = account.ID
	h.BeginLogin(account.ID)
	coreWait(t, ctx, "本地模拟扫码登录完成", func() bool {
		return graphScalar(t, h.g, "SELECT count(*) FROM accounts WHERE id=? AND state='active'", h.accountID) == 1 && graphScalar(t, h.g, "SELECT count(*) FROM meta WHERE key=?", fmt.Sprintf("runtime.session.%d", h.accountID)) == 0
	})
	var provider modelservice.ProviderSaveResult
	h.Request(http.MethodPost, "/api/v1/ai/providers", map[string]any{"name": "本地模型", "preset": nil, "base_url": h.model.URL + "/v1", "api_key": "local-core-flow-key", "vision_model": "local-vision", "text_model": "local-text", "enabled": true}, &provider, http.StatusCreated)
	h.providerID = provider.Provider.ID
	var channel notification.Channel
	h.Request(http.MethodPost, "/api/v1/channels", map[string]any{"name": "本地推送回执", "type": "webhook", "secrets": map[string]any{"url": h.push.URL}}, &channel, http.StatusCreated)
	h.channelID = channel.ID
	return h
}

func (h *coreFlowHarness) loginBoxSequence(accountID int64) uint64 {
	h.t.Helper()
	files, err := filepath.Glob(filepath.Join(h.cfg.DataDir, "diagnostics", "accounts", fmt.Sprint(accountID), "events-*.jsonl"))
	if err != nil {
		h.t.Fatal(err)
	}
	var latest uint64
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			h.t.Fatal(err)
		}
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			var event struct {
				Sequence uint64 `json:"sequence"`
				Event    string `json:"event"`
			}
			if json.Unmarshal(line, &event) == nil && event.Event == "登录框已出现" {
				latest = max(latest, event.Sequence)
			}
		}
	}
	return latest
}

func (h *coreFlowHarness) BeginLogin(accountID int64) {
	h.t.Helper()
	previous := h.loginBoxSequence(accountID)
	h.Request(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/login", accountID), nil, nil, http.StatusAccepted)
	coreWait(h.t, h.ctx, "正式登录流程看见本次扫码框", func() bool {
		return h.loginBoxSequence(accountID) > previous
	})
	h.xianyu.CompleteAutomaticLogin()
}

func (h *coreFlowHarness) Request(method, path string, body, target any, status int) {
	h.t.Helper()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
	}
	r, err := http.NewRequestWithContext(h.ctx, method, h.server.URL+path, bytes.NewReader(data))
	if err != nil {
		h.t.Fatal(err)
	}
	r.AddCookie(&http.Cookie{Name: "kakatta_session", Value: h.token})
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := h.server.Client().Do(r)
	if err != nil {
		h.t.Fatal(err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != status {
		h.t.Fatalf("正式接口 %s %s 返回 %d，期望 %d：%s（%v）", method, path, response.StatusCode, status, result, err)
	}
	if target != nil {
		if err := json.Unmarshal(result, target); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *coreFlowHarness) CreateTask(extra map[string]any) service.Task {
	h.t.Helper()
	profile := analysis.Profile{Summary: "自用 MacBook Pro", Specs: []analysis.ProfileSpec{{Name: "型号", Options: []string{"MacBook Pro"}, Values: []string{"MacBook Pro"}, Source: analysis.SourceUser, Edited: true}}, AllowedKinds: []analysis.Kind{analysis.KindSale}, NextNo: 1}
	values := map[string]any{"name": "自用电脑", "keyword": "MacBook", "enabled": false, "account_id": h.accountID, "account_pinned": true, "max_pages": 1, "personal_only": true, "interval_minutes": 30, "judge_mode": "ai", "requirement": "自用 MacBook Pro", "profile": profile, "analyze_on_first_run": true, "notify_policy": "good", "channel_ids": []int64{h.channelID}}
	for key, value := range extra {
		values[key] = value
	}
	var saved service.TaskSaveResult
	h.Request(http.MethodPost, "/api/v1/tasks", values, &saved, http.StatusCreated)
	return saved.Task
}

func (h *coreFlowHarness) WaitRuns(taskID, count int64) {
	h.t.Helper()
	coreWait(h.t, h.ctx, "正式运行结束", func() bool {
		return graphScalar(h.t, h.g, "SELECT count(*) FROM run_records WHERE task_id=? AND ended_at IS NOT NULL", taskID) >= count
	})
}

func (h *coreFlowHarness) Run(taskID int64) {
	h.t.Helper()
	coreWait(h.t, h.ctx, "账号资源释放并满足运行间隔", func() bool {
		var account service.AccountView
		h.Request(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d", h.accountID), nil, &account, http.StatusOK)
		if account.State != service.AccountActive {
			h.t.Fatalf("手动运行前账号状态异常：%s", account.State)
		}
		return account.Session == nil && (account.LastRunEndedAt == nil || account.NextGapSeconds != nil && time.Now().UnixMilli() >= *account.LastRunEndedAt+*account.NextGapSeconds*1000)
	})
	h.Request(http.MethodPost, fmt.Sprintf("/api/v1/tasks/%d/run", taskID), nil, nil, http.StatusAccepted)
}

func (h *coreFlowHarness) ModelKinds() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.modelKinds...)
}

func (h *coreFlowHarness) ModelConcurrency() (active, maximum int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.modelActive, h.maxModelActive
}
func (h *coreFlowHarness) Products() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]map[string]any(nil), h.products...)
}
func (h *coreFlowHarness) Pushes() []map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]map[string]string(nil), h.pushed...)
}

func (h *coreFlowHarness) openGraph() {
	h.t.Helper()
	g, err := newRuntimeGraphWithSite(h.ctx, h.cfg, h.paths, h.key, backup.KeyDefaultFile, "core-flow", web.Site{Home: h.xianyu.Home(), APIHost: h.xianyu.APIHost()})
	if err != nil {
		h.t.Fatal(err)
	}
	h.g = g
	h.server = httptest.NewServer(g.handler)
}

func (h *coreFlowHarness) startGraph() {
	h.t.Helper()
	if err := h.g.core.Prepare(h.ctx); err != nil {
		h.t.Fatal(err)
	}
	if err := h.g.start(); err != nil {
		h.t.Fatal(err)
	}
}

func (h *coreFlowHarness) closeGraph() error {
	if h.server != nil {
		h.server.Close()
		h.server = nil
	}
	if h.g == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.g.close(ctx); err != nil {
		return err
	}
	h.g = nil
	return nil
}

func (h *coreFlowHarness) Restart() {
	h.t.Helper()
	if err := h.closeGraph(); err != nil {
		h.t.Fatal(err)
	}
	h.openGraph()
	h.startGraph()
	h.Request(http.MethodGet, "/api/v1/accounts", nil, nil, http.StatusOK)
}

func (h *coreFlowHarness) Close() {
	if h.g != nil && h.t.Failed() {
		coreDump(h.t, h.g)
	}
	if err := h.closeGraph(); err != nil {
		h.t.Error(err)
	}
	if h.model != nil {
		h.model.Close()
	}
	if h.push != nil {
		h.push.Close()
	}
	if h.xianyu != nil {
		h.xianyu.Close()
	}
	clear(h.key[:])
}

func coreComparisonPages() ([]fake.SearchItem, []fake.SearchItem) {
	page1 := []fake.SearchItem{{ID: "101", Title: "MacBook Pro 自用一", Price: "10000"}, {ID: "102", Title: "MacBook Pro 自用二", Price: "10000"}, {ID: "103", Title: "MacBook Pro 自用三", Price: "10000"}}
	page2 := []fake.SearchItem{{ID: "103", Title: "MacBook Pro 自用三", Price: "10000"}, {ID: "104", Title: "MacBook Pro 自用四", Price: "10000"}, {ID: "105", Title: "MacBook Pro 自用五", Price: "10000"}}
	for _, items := range [][]fake.SearchItem{page1, page2} {
		for i := range items {
			items[i].SellerID = "seller" + items[i].ID
		}
	}
	return page1, page2
}
