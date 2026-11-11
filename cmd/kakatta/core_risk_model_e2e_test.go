package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
)

func TestCoreFlowVerificationStopsBrowserModelsAndProductPush(t *testing.T) {
	if os.Getenv("KAKATTA_CORE_FLOW_TESTS") != "1" {
		t.Skip("由 deploy/test-core-flow.sh 在断网容器里运行")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	h := newCoreFlowHarness(t, ctx, nil, nil)
	h.xianyu.SetMaskOnSearch(true)
	h.Request(http.MethodPatch, "/api/v1/settings", map[string]any{"values": map[string]any{"notify.system_channel_ids": []int64{h.channelID}}}, nil, http.StatusOK)
	task := h.CreateTask(map[string]any{"enabled": true, "personal_only": true, "max_pages": 2})
	h.WaitRuns(task.ID, 1)
	coreWait(t, ctx, "风控账号与系统提醒保存完成", func() bool {
		return graphScalar(t, h.g, `SELECT count(*) FROM accounts WHERE id=? AND state='needs_verification'`, h.accountID) == 1 &&
			graphScalar(t, h.g, `SELECT count(*) FROM system_events WHERE kind='account_verify' AND subject_id=? AND resolved_at IS NULL AND notified_at IS NOT NULL`, h.accountID) == 1
	})
	if graphScalar(t, h.g, `SELECT count(*) FROM run_records WHERE task_id=? AND trigger='auto' AND outcome='failed' AND failure_kind='risk_verify' AND detail_count=0 AND seller_page_count=0`, task.ID) != 1 ||
		graphScalar(t, h.g, `SELECT count(*) FROM tasks WHERE id=? AND account_id=? AND consecutive_failures=0`, task.ID, h.accountID) != 1 {
		t.Fatal("搜索验证未按风控中断，或错误地累计任务失败/更换账号")
	}
	var verifyURL string
	if err := h.g.database.DB().QueryRowContext(ctx, `SELECT verify_url FROM accounts WHERE id=? AND verify_level=1`, h.accountID).Scan(&verifyURL); err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(verifyURL)
	if err != nil || target == nil || target.Host != h.xianyu.APIHost() || target.Path != "/search" {
		t.Fatalf("人工接手网址不对应发生验证的本地搜索页：%q，%v", verifyURL, err)
	}
	for _, query := range h.xianyu.SearchQueries() {
		if strings.Contains(query, "publish=") || strings.Contains(query, "page=2") || strings.Contains(query, "step=") {
			t.Fatalf("遇到搜索验证后仍筛选或翻页：%s", query)
		}
	}
	if h.xianyu.DetailHits() != 0 || h.xianyu.SellerHits() != (fake.SellerVisitCounts{}) || len(h.ModelKinds()) != 0 {
		t.Fatal("遇到搜索验证后仍打开详情、卖家或调用模型")
	}
	for _, table := range []string{"listings", "findings", "task_sightings", "task_item_specs", "notification_intents", "ai_calls"} {
		if graphScalar(t, h.g, "SELECT count(*) FROM "+table) != 0 {
			t.Fatalf("搜索尚未完成就写入了%s", table)
		}
	}
	if graphScalar(t, h.g, `SELECT count(*) FROM quota_events WHERE account_id=? AND kind='search'`, h.accountID) != 3 ||
		graphScalar(t, h.g, `SELECT count(*) FROM quota_events WHERE account_id=? AND kind='detail'`, h.accountID) != 1 {
		t.Fatal("风控没有保留扫码、首页和提交搜索已经消费的访问额度")
	}
	pushes := h.Pushes()
	if len(pushes) != 1 || pushes[0]["kind"] != "system" || pushes[0]["item_id"] != "" || !strings.Contains(pushes[0]["title"], "需要验证") {
		t.Fatalf("风控只能发送一次系统验证提醒，不能发商品推荐：%v", pushes)
	}
	if graphScalar(t, h.g, `SELECT count(*) FROM deliveries WHERE kind='system' AND ok=1 AND event_id IN (SELECT id FROM system_events WHERE kind='account_verify' AND subject_id=?)`, h.accountID) != 1 ||
		graphScalar(t, h.g, `SELECT count(*) FROM deliveries WHERE kind<>'system'`) != 0 {
		t.Fatal("风控系统提醒与投递记录不对应，或误发了商品通知")
	}
	coreWait(t, ctx, "原浏览器进入等待人工验证的保留阶段", func() bool {
		var account service.AccountView
		h.Request(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d", h.accountID), nil, &account, http.StatusOK)
		return account.State == service.AccountNeedsVerification && account.Session != nil && account.Session.Phase == service.SessionVerifyHold && account.Session.ExpiresAt != nil
	})
	var status service.SystemStatus
	h.Request(http.MethodGet, "/api/v1/system/status", nil, &status, http.StatusOK)
	if status.Browsers.Active != 1 || len(status.Browsers.Sessions) != 1 || status.Browsers.Sessions[0].AccountID != h.accountID {
		t.Fatalf("保留的验证浏览器没有继续占账号和全局页面名额：%+v", status.Browsers)
	}
	searches := len(h.xianyu.SearchQueries())
	var blocked struct {
		Error locales.Message `json:"error"`
	}
	h.Request(http.MethodPost, fmt.Sprintf("/api/v1/tasks/%d/run", task.ID), nil, &blocked, http.StatusConflict)
	if blocked.Error.Code != "run.account_needs_verification" || len(h.xianyu.SearchQueries()) != searches || h.xianyu.DetailHits() != 0 || len(h.ModelKinds()) != 0 || len(h.Pushes()) != 1 {
		t.Fatalf("验证账号的手动运行没有立即被挡住，或重新访问/重复提醒：%+v", blocked)
	}
	t.Log("搜索验证立即停止筛选、详情和模型；账号保留人工画面，只发一次系统提醒，手动运行仍被挡住")
}

func TestCoreFlowModelTimeoutKeepsOneFindingAndRecoversFromCachedDetail(t *testing.T) {
	if os.Getenv("KAKATTA_CORE_FLOW_TESTS") != "1" {
		t.Skip("由 deploy/test-core-flow.sh 在断网容器里运行")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	var analysisCalls atomic.Int64
	timedOut := make(chan struct{})
	h := newCoreFlowHarness(t, ctx, func(_ http.ResponseWriter, r *http.Request, kind string) bool {
		if kind != "listing_facts" || analysisCalls.Add(1) != 1 {
			return false
		}
		select {
		case <-r.Context().Done():
			close(timedOut)
		case <-ctx.Done():
			t.Error("模型服务没有收到正式客户端的请求超时取消")
		}
		return true
	}, nil)
	h.xianyu.SetSearchItems(nil, nil)
	h.Request(http.MethodPatch, "/api/v1/settings", map[string]any{"values": map[string]any{"ai.request_timeout_seconds": 1}}, nil, http.StatusOK)
	task := h.CreateTask(map[string]any{"enabled": true, "personal_only": true})
	h.WaitRuns(task.ID, 1)
	if graphScalar(t, h.g, `SELECT count(*) FROM tasks WHERE id=? AND baseline_pending=0`, task.ID) != 1 || len(h.ModelKinds()) != 0 || len(h.Pushes()) != 0 {
		t.Fatal("空基线没有通过正式运行完成，或空结果调用了模型/推送")
	}
	h.xianyu.SetSearchItems([]fake.SearchItem{{ID: "201", Title: "MacBook Pro 自用设备", Price: "8000", SellerID: "seller201"}}, nil)
	h.Run(task.ID)
	h.WaitRuns(task.ID, 2)
	coreWait(t, ctx, "模型端收到超时取消", func() bool {
		select {
		case <-timedOut:
			return true
		default:
			return false
		}
	})
	if graphScalar(t, h.g, `SELECT count(*) FROM run_records WHERE task_id=? AND outcome='success' AND failure_kind IS NULL`, task.ID) != 2 ||
		graphScalar(t, h.g, `SELECT count(*) FROM tasks WHERE id=? AND consecutive_failures=0`, task.ID) != 1 {
		t.Fatal("单件模型请求超时被错误地当成整轮超时或页面失败")
	}
	if graphScalar(t, h.g, `SELECT count(*) FROM findings WHERE task_id=? AND item_id='201' AND decision='failed' AND stage='detail' AND retryable=1 AND attempts=2 AND json_extract(reason,'$.code')='analysis.client.timeout' AND is_baseline=0 AND notified_at IS NULL`, task.ID) != 1 {
		t.Fatal("模型超时没有把原发现留在可重试的详情阶段，或误写成等待分析/成功")
	}
	findingID := graphScalar(t, h.g, `SELECT id FROM findings WHERE task_id=? AND item_id='201'`, task.ID)
	revision := graphScalar(t, h.g, `SELECT processing_revision FROM findings WHERE id=?`, findingID)
	fetchedAt := graphScalar(t, h.g, `SELECT COALESCE(detail_fetched_at,0) FROM listings WHERE item_id='201'`)
	if fetchedAt == 0 || h.xianyu.DetailHits() != 1 || h.xianyu.SellerHits() != (fake.SellerVisitCounts{Pages: 1, Head: 1, Items: 1, Ratings: 1}) {
		t.Fatal("模型超时前没有实际取得并缓存详情及卖家资料")
	}
	if !reflect.DeepEqual(h.ModelKinds(), []string{"title_screen", "listing_facts"}) ||
		graphScalar(t, h.g, `SELECT count(*) FROM ai_calls WHERE task_id=?`, task.ID) != 2 ||
		graphScalar(t, h.g, `SELECT count(*) FROM ai_calls WHERE task_id=? AND purpose='analysis' AND ok=0 AND error_kind='timeout' AND input_tokens IS NULL AND output_tokens IS NULL`, task.ID) != 1 ||
		graphScalar(t, h.g, `SELECT COALESCE(sum(COALESCE(input_tokens,0)+COALESCE(output_tokens,0)),0) FROM ai_calls WHERE task_id=?`, task.ID) != 30 {
		t.Fatal("模型超时没有逐物理请求记账，或立即重复请求/虚构了未知用量")
	}
	if graphScalar(t, h.g, `SELECT count(*) FROM ai_providers WHERE id=? AND enabled=1 AND error_state IS NULL`, h.providerID) != 1 ||
		graphScalar(t, h.g, `SELECT count(*) FROM notification_intents`) != 0 || graphScalar(t, h.g, `SELECT count(*) FROM deliveries`) != 0 || len(h.Pushes()) != 0 {
		t.Fatal("临时超时停用了服务商，或未完成判断就创建商品通知")
	}
	searchVisits := graphScalar(t, h.g, `SELECT count(*) FROM quota_events WHERE account_id=? AND kind='search'`, h.accountID)
	detailVisits := graphScalar(t, h.g, `SELECT count(*) FROM quota_events WHERE account_id=? AND kind='detail'`, h.accountID)
	if searchVisits != 9 || detailVisits != 4 {
		t.Fatalf("超时前的扫码、空基线及实际浏览访问记账不一致：search=%d detail=%d", searchVisits, detailVisits)
	}
	h.Run(task.ID)
	h.WaitRuns(task.ID, 3)
	if graphScalar(t, h.g, `SELECT count(*) FROM findings WHERE id=? AND task_id=? AND processing_revision=? AND decision='scored' AND stage='done' AND attempts=3 AND retryable=0 AND notified_at IS NOT NULL`, findingID, task.ID, revision) != 1 ||
		graphScalar(t, h.g, `SELECT count(*) FROM findings`) != 1 ||
		graphScalar(t, h.g, `SELECT detail_fetched_at FROM listings WHERE item_id='201'`) != fetchedAt {
		t.Fatal("重试没有恢复同一发现与原详情缓存，或重置了尝试历史")
	}
	if h.xianyu.DetailHits() != 1 || h.xianyu.SellerHits() != (fake.SellerVisitCounts{Pages: 1, Head: 1, Items: 1, Ratings: 1}) ||
		graphScalar(t, h.g, `SELECT count(*) FROM quota_events WHERE account_id=? AND kind='detail'`, h.accountID) != detailVisits ||
		graphScalar(t, h.g, `SELECT count(*) FROM quota_events WHERE account_id=? AND kind='search'`, h.accountID) != searchVisits+4 {
		t.Fatal("恢复时重复抓取缓存详情/卖家，或没有为真实搜索访问记账")
	}
	if !reflect.DeepEqual(h.ModelKinds(), []string{"title_screen", "listing_facts", "listing_facts"}) || analysisCalls.Load() != 2 ||
		graphScalar(t, h.g, `SELECT count(*) FROM ai_calls WHERE task_id=?`, task.ID) != 3 ||
		graphScalar(t, h.g, `SELECT count(*) FROM ai_calls WHERE task_id=? AND purpose='analysis' AND ok=0 AND error_kind='timeout'`, task.ID) != 1 ||
		graphScalar(t, h.g, `SELECT COALESCE(sum(COALESCE(input_tokens,0)+COALESCE(output_tokens,0)),0) FROM ai_calls WHERE task_id=?`, task.ID) != 60 {
		t.Fatal("恢复时重复整理标题、重复物理请求或重复计算用量")
	}
	pushes := h.Pushes()
	if len(pushes) != 1 || pushes[0]["kind"] != "recommended" || pushes[0]["item_id"] != "201" ||
		graphScalar(t, h.g, `SELECT count(*) FROM notification_intents WHERE finding_id=? AND kind='recommended' AND state='sent' AND failure_count=0`, findingID) != 1 ||
		graphScalar(t, h.g, `SELECT count(*) FROM notification_intents`) != 1 ||
		graphScalar(t, h.g, `SELECT count(*) FROM deliveries WHERE kind='recommended' AND ok=1 AND item_count=1`) != 1 {
		t.Fatalf("只有恢复后的首次结论才应创建一次推荐并送达：%v", pushes)
	}
	t.Log("请求级超时只影响一件商品；下一轮复用详情和卖家，恢复同一发现，三次物理请求准确记账且只推一次")
}
