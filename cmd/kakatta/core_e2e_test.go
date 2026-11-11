package main

import (
	"context"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/xianyu/fake"
)

func TestCoreFlowFromSchedulerToBrowserModelsAndWebhook(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	h := newCoreFlowHarness(t, ctx, nil, nil)
	g, xianyu := h.g, h.xianyu
	page1, page2 := coreComparisonPages()
	xianyu.SetSearchItems(page1, page2)

	task := h.CreateTask(map[string]any{"enabled": true, "max_pages": 2, "interval_minutes": 1})
	taskID := task.ID
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
	if graphScalar(t, g, "SELECT count(*) FROM deliveries WHERE task_id=? AND channel_id=? AND kind='recommended' AND ok=1 AND item_count=1", taskID, h.channelID) != 1 || graphScalar(t, g, "SELECT count(*) FROM delivery_items di JOIN findings f ON f.id=di.finding_id WHERE f.item_id='106'") != 1 {
		t.Fatal("真实推送回执和商品关联记录不一致")
	}
	if graphScalar(t, g, "SELECT count(*) FROM ai_calls WHERE task_id=? AND ok=1 AND purpose='title_screen'", taskID) != 2 || graphScalar(t, g, "SELECT count(*) FROM ai_calls WHERE task_id=? AND ok=1 AND purpose='analysis'", taskID) != 6 || graphScalar(t, g, "SELECT count(*) FROM ai_calls WHERE ok=0") != 0 {
		t.Fatal("正式模型调用或用量记账不一致")
	}
	if graphScalar(t, g, "SELECT count(*) FROM quota_events WHERE account_id=? AND kind='search'", h.accountID) != 11 || graphScalar(t, g, "SELECT count(*) FROM quota_events WHERE account_id=? AND kind='detail'", h.accountID) != 19 {
		t.Fatal("登录、搜索、翻页、详情、卖家和评价没有逐次记入账号额度")
	}
	modelKinds, products, pushed := h.ModelKinds(), h.Products(), h.Pushes()
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
