package main

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/xianyu/fake"
)

func TestCoreFlowFailedPushSurvivesGraphRestartWithoutDuplicate(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	var attempts atomic.Int64
	h := newCoreFlowHarness(t, ctx, nil, func(w http.ResponseWriter, _ *http.Request, body map[string]string) bool {
		if body["kind"] != "recommended" {
			t.Errorf("意外的推送用途：%s", body["kind"])
		}
		if attempts.Add(1) == 1 {
			http.Error(w, "本地渠道暂不可用", http.StatusServiceUnavailable)
			return true
		}
		return false
	})
	page1, page2 := coreComparisonPages()
	h.xianyu.SetSearchItems(page1, page2)
	task := h.CreateTask(map[string]any{"max_pages": 2})
	h.Run(task.ID)
	h.WaitRuns(task.ID, 1)
	if graphScalar(t, h.g, "SELECT count(*) FROM findings WHERE task_id=? AND is_baseline=1 AND decision='scored'", task.ID) != 5 || attempts.Load() != 0 {
		t.Fatal("正常基线没有完成，或误发了首轮通知")
	}
	h.xianyu.SetSearchItems(append([]fake.SearchItem{{ID: "106", Title: "MacBook Pro 自用升级低价出", Price: "8000", SellerID: "seller106"}}, page1[:2]...), page2)
	h.Run(task.ID)
	h.WaitRuns(task.ID, 2)
	var intentID, findingID, firstFailed int64
	if err := h.g.database.DB().QueryRowContext(ctx, "SELECT id,finding_id,first_failed_at FROM notification_intents WHERE task_id=? AND kind='recommended' AND state='pending' AND failure_count=1", task.ID).Scan(&intentID, &findingID, &firstFailed); err != nil {
		t.Fatal("首次渠道失败没有留下待发意图", err)
	}
	if firstFailed <= 0 || attempts.Load() != 1 || len(h.Pushes()) != 1 || graphScalar(t, h.g, "SELECT count(*) FROM deliveries WHERE task_id=? AND ok=0", task.ID) != 1 || graphScalar(t, h.g, "SELECT count(*) FROM findings WHERE id=? AND notified_at IS NULL AND score>=80 AND stage='done'", findingID) != 1 {
		t.Fatal("渠道失败被同轮重试，或丢失商品分析与失败投递记录")
	}
	if graphScalar(t, h.g, "SELECT count(*) FROM run_records WHERE task_id=? AND outcome='success' AND json_array_length(warnings_json)>0", task.ID) != 1 {
		t.Fatal("推送失败没有作为成功扫描的渠道警告保留")
	}
	coreWait(t, ctx, "旧运行资源释放", func() bool {
		return graphScalar(t, h.g, "SELECT count(*) FROM meta WHERE key=?", fmt.Sprintf("runtime.session.%d", h.accountID)) == 0
	})
	quotaBefore := graphScalar(t, h.g, "SELECT count(*) FROM quota_events")
	if quotaBefore != 30 || graphScalar(t, h.g, "SELECT count(*) FROM ai_calls") != 8 {
		t.Fatal("首次失败前的访问与模型记账不一致")
	}
	h.Restart()
	if graphScalar(t, h.g, "SELECT count(*) FROM notification_intents WHERE id=? AND state='pending' AND failure_count=1 AND first_failed_at=?", intentID, firstFailed) != 1 || graphScalar(t, h.g, "SELECT count(*) FROM quota_events") != quotaBefore || graphScalar(t, h.g, "SELECT count(*) FROM ai_calls") != 8 || graphScalar(t, h.g, "SELECT count(*) FROM accounts WHERE id=? AND state='active'", h.accountID) != 1 {
		t.Fatal("正常关闭重开丢失待发意图、失败窗口、账号状态或用量")
	}
	h.Run(task.ID)
	h.WaitRuns(task.ID, 3)
	if graphScalar(t, h.g, "SELECT count(*) FROM notification_intents WHERE id=? AND state='sent' AND sent_at IS NOT NULL AND failure_count=1 AND first_failed_at=?", intentID, firstFailed) != 1 || graphScalar(t, h.g, "SELECT count(*) FROM findings WHERE id=? AND notified_at IS NOT NULL AND recommended_price=800000", findingID) != 1 {
		t.Fatal("重开后的成功推送没有结算原意图，或改写了失败历史")
	}
	if attempts.Load() != 2 || graphScalar(t, h.g, "SELECT count(*) FROM deliveries WHERE task_id=? AND kind='recommended'", task.ID) != 2 || graphScalar(t, h.g, "SELECT count(*) FROM deliveries WHERE task_id=? AND kind='recommended' AND ok=1", task.ID) != 1 || graphScalar(t, h.g, "SELECT count(*) FROM delivery_intents WHERE intent_id=?", intentID) != 2 || graphScalar(t, h.g, "SELECT count(*) FROM delivery_items WHERE finding_id=?", findingID) != 2 {
		t.Fatal("待发重试的物理请求、失败和成功回执、意图关联不一致")
	}
	coreWait(t, ctx, "成功推送后的资源释放", func() bool {
		return graphScalar(t, h.g, "SELECT count(*) FROM meta WHERE key=?", fmt.Sprintf("runtime.session.%d", h.accountID)) == 0
	})
	quotaAfter := graphScalar(t, h.g, "SELECT count(*) FROM quota_events")
	h.Restart()
	if graphScalar(t, h.g, "SELECT count(*) FROM notification_intents WHERE id=? AND state='sent'", intentID) != 1 || graphScalar(t, h.g, "SELECT count(*) FROM quota_events") != quotaAfter {
		t.Fatal("第二次正常关闭重开丢失已送达事实或恢复了访问额度")
	}
	h.Run(task.ID)
	h.WaitRuns(task.ID, 4)
	if attempts.Load() != 2 || len(h.Pushes()) != 2 || graphScalar(t, h.g, "SELECT count(*) FROM notification_intents") != 1 || graphScalar(t, h.g, "SELECT count(*) FROM deliveries") != 2 || graphScalar(t, h.g, "SELECT count(*) FROM ai_calls") != 8 || graphScalar(t, h.g, "SELECT count(*) FROM findings") != 6 || graphScalar(t, h.g, "SELECT count(*) FROM run_records WHERE task_id=? AND outcome='success'", task.ID) != 4 {
		t.Fatal("已成功的推荐重开后重复投递，或缓存商品重新分析、新建发现")
	}
	if h.xianyu.DetailHits() != 6 || h.xianyu.SellerHits().Pages != 6 || len(h.ModelKinds()) != 8 {
		t.Fatal("重试待发意图时重新抓取了缓存详情、卖家或重新调用模型")
	}
	for _, push := range h.Pushes() {
		if push["kind"] != "recommended" || push["item_id"] != "106" || push["score"] == "" {
			t.Fatalf("两次物理推送没有保持同一推荐内容：%v", push)
		}
	}
	t.Log("首次推送503后正常关闭重开，原意图继续并成功；再次重开运行没有重复投递，访问额度和模型用量持续保留")
}
