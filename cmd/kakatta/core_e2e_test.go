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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	modelservice "github.com/Seaony/Kakatta/internal/analysis/service"
	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/notification"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func TestCoreFlowFromSchedulerToBrowserModelsAndWebhook(t *testing.T) {
	if os.Getenv("KAKATTA_CORE_FLOW_TESTS") != "1" {
		t.Skip("由 deploy/test-core-flow.sh 在断网容器里运行")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	xianyu := fake.Start()
	defer xianyu.Close()
	xianyu.EnableAutomaticLogin()
	xianyu.EnableSellerProfile()
	page1 := []fake.SearchItem{{ID: "101", Title: "MacBook Pro 自用一", Price: "10000"}, {ID: "102", Title: "MacBook Pro 自用二", Price: "10000"}, {ID: "103", Title: "MacBook Pro 自用三", Price: "10000"}}
	page2 := []fake.SearchItem{{ID: "103", Title: "MacBook Pro 自用三", Price: "10000"}, {ID: "104", Title: "MacBook Pro 自用四", Price: "10000"}, {ID: "105", Title: "MacBook Pro 自用五", Price: "10000"}}
	for _, items := range [][]fake.SearchItem{page1, page2} {
		for i := range items {
			items[i].SellerID = "seller" + items[i].ID
		}
	}
	xianyu.SetSearchItems(page1, page2)

	var mu sync.Mutex
	var modelKinds []string
	var products []map[string]any
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer local-core-flow-key" {
			t.Errorf("模型请求没有经过正式兼容客户端：%s %s", r.Method, r.URL.Path)
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
			t.Errorf("模型请求格式错误：%v", err)
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
				t.Errorf("详情图片没有经过正式本地下载处理：%v", err)
				http.Error(w, "invalid local images", http.StatusBadRequest)
				return
			}
			text = parts[1].Text
		}
		blocks := strings.Split(text, "```json\n")
		if len(blocks) != 3 {
			t.Error("模型输入缺少需求清单或商品原文")
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
				t.Errorf("标题列表无效：%v", err)
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
				t.Errorf("商品原文无效：%v", err)
				return
			}
			mu.Lock()
			products = append(products, product)
			mu.Unlock()
			reply = map[string]any{"specs": []any{map[string]any{"name": "型号", "value": "MacBook Pro"}}, "brand_new": false, "musts": []any{}, "bonuses": []any{}, "checks": []any{}, "seller_signals": []any{}, "price_note": nil, "extra_findings": []any{}, "adjust": map[string]any{"value": 0, "reason": ""}, "summary": "自用设备，描述无拆修"}
		default:
			t.Errorf("意外的模型用途：%s", kind)
			http.Error(w, "unexpected local purpose", http.StatusBadRequest)
			return
		}
		mu.Lock()
		modelKinds = append(modelKinds, kind)
		mu.Unlock()
		content, _ := json.Marshal(reply)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(content)}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 10}})
	}))
	defer model.Close()
	var pushed []map[string]string
	push := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.Method != http.MethodPost {
			t.Errorf("推送请求没有经过正式渠道适配器：%v", err)
			http.Error(w, "invalid local push", http.StatusBadRequest)
			return
		}
		mu.Lock()
		pushed = append(pushed, body)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer push.Close()

	cfg, paths, key := graphConfig(t)
	g, err := newRuntimeGraphWithSite(ctx, cfg, paths, key, backup.KeyDefaultFile, "core-flow", web.Site{Home: xianyu.Home(), APIHost: xianyu.APIHost()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if t.Failed() {
			coreDump(t, g)
		}
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := g.close(cleanup); err != nil {
			t.Error(err)
		}
	}()
	admin := graphAdmin(t, g)
	server := httptest.NewServer(g.handler)
	defer server.Close()
	request := func(method, path string, body, target any, status int) {
		t.Helper()
		var data []byte
		if body != nil {
			data, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		r, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		r.AddCookie(&http.Cookie{Name: "kakatta_session", Value: admin.Token})
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		result, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status {
			t.Fatalf("正式接口 %s %s 返回 %d，期望 %d：%s（%v）", method, path, response.StatusCode, status, result, err)
		}
		if target != nil {
			if err := json.Unmarshal(result, target); err != nil {
				t.Fatal(err)
			}
		}
	}
	values := map[string]any{"general.update_check": false, "crawl.quiet_hours": map[string]any{"enabled": false, "start": "01:00", "end": "07:30"}, "crawl.account_min_gap_seconds": 2, "crawl.account_min_gap_jitter_seconds": 0, "crawl.account_hourly_visits": 200, "crawl.interval_jitter_percent": 0, "crawl.max_details_per_run": 10, "ai.max_concurrent_requests": 1, "ai.image_count": 1}
	for _, name := range []string{"home_stay_seconds", "page_wait_seconds", "detail_pre_wait_seconds", "detail_stay_seconds", "action_wait_seconds"} {
		values["crawl."+name] = map[string]float64{"min": .01, "max": .01}
	}
	values["crawl.typing_interval_ms"] = map[string]float64{"min": 1, "max": 1}
	request(http.MethodPatch, "/api/v1/settings", map[string]any{"values": values}, nil, http.StatusOK)
	if err := g.core.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err := g.start(); err != nil {
		t.Fatal(err)
	}
	var account service.AccountView
	request(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "本地扫码账号"}, &account, http.StatusCreated)
	request(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/login", account.ID), nil, nil, http.StatusAccepted)
	coreWait(t, ctx, "本地模拟扫码登录完成", func() bool {
		return graphScalar(t, g, "SELECT count(*) FROM accounts WHERE id=? AND state='active'", account.ID) == 1 && graphScalar(t, g, "SELECT count(*) FROM meta WHERE key=?", fmt.Sprintf("runtime.session.%d", account.ID)) == 0
	})
	var provider modelservice.ProviderSaveResult
	request(http.MethodPost, "/api/v1/ai/providers", map[string]any{"name": "本地模型", "preset": nil, "base_url": model.URL + "/v1", "api_key": "local-core-flow-key", "vision_model": "local-vision", "text_model": "local-text", "enabled": true}, &provider, http.StatusCreated)
	var channel notification.Channel
	request(http.MethodPost, "/api/v1/channels", map[string]any{"name": "本地推送回执", "type": "webhook", "secrets": map[string]any{"url": push.URL}}, &channel, http.StatusCreated)
	profile := analysis.Profile{Summary: "自用 MacBook Pro", Specs: []analysis.ProfileSpec{{Name: "型号", Options: []string{"MacBook Pro"}, Values: []string{"MacBook Pro"}, Source: analysis.SourceUser, Edited: true}}, AllowedKinds: []analysis.Kind{analysis.KindSale}, NextNo: 1}
	var saved service.TaskSaveResult
	request(http.MethodPost, "/api/v1/tasks", map[string]any{"name": "自用电脑", "keyword": "MacBook", "enabled": true, "account_id": account.ID, "account_pinned": true, "max_pages": 2, "personal_only": true, "interval_minutes": 1, "judge_mode": "ai", "requirement": "自用 MacBook Pro", "profile": profile, "analyze_on_first_run": true, "notify_policy": "good", "channel_ids": []int64{channel.ID}}, &saved, http.StatusCreated)
	taskID := saved.Task.ID
	coreWait(t, ctx, "第一轮自动基线分析", func() bool {
		return graphScalar(t, g, "SELECT count(*) FROM run_records WHERE task_id=? AND ended_at IS NOT NULL", taskID) >= 1
	})
	if graphScalar(t, g, "SELECT count(*) FROM findings WHERE task_id=? AND is_baseline=1 AND decision='scored' AND stage='done'", taskID) != 5 || graphScalar(t, g, "SELECT count(*) FROM deliveries") != 0 {
		t.Fatal("基线商品未全部走完分析，或首轮误发了推送")
	}
	if graphScalar(t, g, "SELECT count(*) FROM run_records WHERE task_id=? AND trigger='auto' AND outcome='success' AND page_count=2 AND scanned_count=5 AND detail_count=5 AND seller_page_count=5", taskID) != 1 {
		t.Fatal("首轮没有实际搜索两页、去重并读取详情和卖家主页")
	}
	t.Log("首轮5件基线已完成详情、卖家和模型分析，未发送推送；等待下一次正式调度")
	xianyu.SetSearchItems(append([]fake.SearchItem{{ID: "106", Title: "MacBook Pro 自用升级低价出", Price: "8000", SellerID: "seller106"}}, page1[:2]...), page2)
	coreWait(t, ctx, "第二轮自动发现和推送", func() bool {
		return graphScalar(t, g, "SELECT count(*) FROM run_records WHERE task_id=? AND ended_at IS NOT NULL", taskID) >= 2
	})
	if graphScalar(t, g, "SELECT count(*) FROM run_records WHERE task_id=? AND trigger='auto' AND outcome='success'", taskID) != 2 || graphScalar(t, g, "SELECT count(*) FROM listings") != 6 || graphScalar(t, g, "SELECT count(*) FROM findings WHERE task_id=?", taskID) != 6 {
		t.Fatal("连续两轮调度的运行、商品或发现数不一致")
	}
	if graphScalar(t, g, "SELECT count(*) FROM findings WHERE task_id=? AND item_id='106' AND is_baseline=0 AND decision='scored' AND score>=80 AND grade IN ('good','steal') AND notify_kind='recommended'", taskID) != 1 {
		t.Fatal("低价新品没有经过行情比价、算分和推荐判定")
	}
	if graphScalar(t, g, "SELECT count(*) FROM deliveries WHERE task_id=? AND channel_id=? AND kind='recommended' AND ok=1 AND item_count=1", taskID, channel.ID) != 1 || graphScalar(t, g, "SELECT count(*) FROM delivery_items di JOIN findings f ON f.id=di.finding_id WHERE f.item_id='106'") != 1 {
		t.Fatal("真实推送回执和商品关联记录不一致")
	}
	if graphScalar(t, g, "SELECT count(*) FROM ai_calls WHERE task_id=? AND ok=1 AND purpose='title_screen'", taskID) != 2 || graphScalar(t, g, "SELECT count(*) FROM ai_calls WHERE task_id=? AND ok=1 AND purpose='analysis'", taskID) != 6 || graphScalar(t, g, "SELECT count(*) FROM ai_calls WHERE ok=0") != 0 {
		t.Fatal("正式模型调用或用量记账不一致")
	}
	if graphScalar(t, g, "SELECT count(*) FROM quota_events WHERE account_id=? AND kind='search'", account.ID) != 11 || graphScalar(t, g, "SELECT count(*) FROM quota_events WHERE account_id=? AND kind='detail'", account.ID) != 19 {
		t.Fatal("登录、搜索、翻页、详情、卖家和评价没有逐次记入账号额度")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(modelKinds) != 8 || len(products) != 6 || len(pushed) != 1 || pushed[0]["item_id"] != "106" || pushed[0]["kind"] != "recommended" || pushed[0]["score"] == "" {
		t.Fatalf("本地模型和推送实际收到的数据不一致：模型 %v，商品 %d，推送 %v", modelKinds, len(products), pushed)
	}
	for _, product := range products {
		seller, ok := product["seller"].(map[string]any)
		if !ok || seller["introduction"] != "自用设备升级" || seller["recent_items"] == nil || seller["recent_rates"] == nil || product["description"] != "电池 92%，无拆修" {
			t.Fatalf("页面取得的详情和卖家资料没有进入正式模型请求：%v", product)
		}
	}
	hits := xianyu.SellerHits()
	if xianyu.DetailHits() != 6 || hits.Pages != 6 || hits.Items != 6 || hits.Ratings != 6 {
		t.Fatalf("真实浏览器抓取和卖家缓存次数不一致：详情 %d，卖家 %+v", xianyu.DetailHits(), hits)
	}
	t.Log("两轮正式自动调度完成：两页去重、5件基线不推、1件新品完成详情和卖家读取、数值算分、模型用量记账和唯一推送")
}

func coreWait(t *testing.T, ctx context.Context, what string, ready func() bool) {
	t.Helper()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatalf("等待%s超时：%v", what, ctx.Err())
		case <-tick.C:
		}
	}
}

func coreDump(t *testing.T, g *runtimeGraph) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, query := range []string{
		"SELECT id,outcome,COALESCE(failure_kind,''),COALESCE(message,''),COALESCE(warnings_json,'') FROM run_records ORDER BY id",
		"SELECT item_id,decision,stage,COALESCE(reason,''),COALESCE(score,-1),COALESCE(grade,'') FROM findings ORDER BY id",
		"SELECT item_id,day,price,last_seen_at FROM task_sightings ORDER BY item_id,day",
		"SELECT item_id,kind,specs_json,config_key,excluded FROM task_item_specs ORDER BY item_id",
		"SELECT kind,run_id,count(*) FROM quota_events GROUP BY kind,run_id ORDER BY kind,run_id",
	} {
		rows, err := g.database.DB().QueryContext(ctx, query)
		if err != nil {
			t.Log("读取本地回归诊断失败", err)
			continue
		}
		columns, _ := rows.Columns()
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if rows.Scan(pointers...) == nil {
				t.Log(columns, values)
			}
		}
		rows.Close()
	}
}
