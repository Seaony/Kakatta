package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
	"github.com/chromedp/chromedp"
)

func formalVisit(t *testing.T, browser context.Context, events *webSmokeEvents, path, heading string) {
	t.Helper()
	visitWebSmokePage(t, browser, events, webSmokePage{name: "formal", path: path, heading: heading}, locales.ZhCN, 1280)
}

func formalTaskID(t *testing.T) int64 {
	t.Helper()
	return formalScalar(t, "SELECT id FROM tasks WHERE name='正式流程相机监控'")
}

func formalReadyToRun(t *testing.T, browser context.Context) {
	t.Helper()
	formalWait(t, browser, "账号自然间隔和当前运行结束", 90*time.Second, func() bool {
		return formalScalar(t, "SELECT count(*) FROM run_records WHERE ended_at IS NULL") == 0 &&
			formalScalar(t, "SELECT COALESCE(last_run_ended_at,0)+COALESCE(next_gap_seconds,0)*1000 FROM accounts WHERE name='本地扫码账号'") <= time.Now().UnixMilli()
	})
}

func formalRunOnce(t *testing.T, browser context.Context, events *webSmokeEvents) int64 {
	t.Helper()
	formalReadyToRun(t, browser)
	taskID := formalTaskID(t)
	previous := formalScalar(t, "SELECT COALESCE(max(id),0) FROM run_records WHERE task_id=?", taskID)
	formalVisit(t, browser, events, "/tasks", "tasks.title")
	waitWebSmoke(t, browser, events, `(label) => [...document.querySelectorAll('main button')].some(button => button.textContent.trim() === label && !button.disabled)`, smokeTranslation(t, locales.ZhCN, "tasks.run_now"))
	clickWebSmokeButton(t, browser, events, smokeTranslation(t, locales.ZhCN, "tasks.run_now"))
	formalWait(t, browser, "网页手动运行完成", 3*time.Minute, func() bool {
		return formalScalar(t, "SELECT count(*) FROM run_records WHERE task_id=? AND id>? AND trigger='manual' AND ended_at IS NOT NULL", taskID, previous) > 0
	})
	return formalScalar(t, "SELECT max(id) FROM run_records WHERE task_id=? AND trigger='manual' AND id>?", taskID, previous)
}

func formalSearchWithNewItem(t *testing.T, id, title string) {
	t.Helper()
	first, second := coreComparisonPages()
	first = append([]fake.SearchItem{{ID: id, Title: title, Price: "8000", SellerID: "seller" + id}}, first...)
	formalControl(t, "search", map[string]any{"pages": [][]fake.SearchItem{first, second}})
}

func formalShowRun(t *testing.T, browser context.Context, events *webSmokeEvents, runID int64) {
	t.Helper()
	formalVisit(t, browser, events, fmt.Sprintf("/logs?run_id=%d", runID), "logs.title")
	waitWebSmoke(t, browser, events, `(heading) => [...document.querySelectorAll('main h4')].some(node => node.textContent === heading) && document.querySelectorAll('main section ol li').length > 0`, smokeTranslation(t, locales.ZhCN, "logs.steps"))
}

func formalMonitoring(t *testing.T, browser context.Context, events *webSmokeEvents, _ formalMetadata) {
	taskID := formalTaskID(t)
	formalWait(t, browser, "首轮基线完成", 3*time.Minute, func() bool {
		return formalScalar(t, "SELECT count(*) FROM run_records WHERE task_id=? AND ended_at IS NOT NULL", taskID) >= 1
	})
	if formalScalar(t, "SELECT count(*) FROM findings WHERE task_id=? AND is_baseline=1 AND decision='scored'", taskID) < 5 ||
		formalScalar(t, "SELECT count(*) FROM deliveries WHERE task_id=? AND kind='recommended'", taskID) != 0 {
		t.Fatal("全新任务的比较基线没有正常分析，或首轮误发了推荐")
	}
	runID := formalRunOnce(t, browser, events)
	if formalText(t, "SELECT outcome FROM run_records WHERE id=?", runID) != "success" {
		t.Fatal("网页手动运行没有成功")
	}
	formalShowRun(t, browser, events, runID)
	formalSnapshot(t, browser, events, "manual-run-steps")

	formalSearchWithNewItem(t, "106", "MacBook Pro 正式流程新品")
	formalWait(t, browser, "真实定时调度发现新品并送达", 3*time.Minute, func() bool {
		return formalScalar(t, `SELECT count(*) FROM deliveries d JOIN delivery_items di ON di.delivery_id=d.id JOIN findings f ON f.id=di.finding_id WHERE f.task_id=? AND f.item_id='106' AND d.ok=1 AND d.kind='recommended'`, taskID) == 1
	})
	if formalScalar(t, "SELECT count(*) FROM findings WHERE task_id=? AND item_id='106' AND is_baseline=0 AND decision='scored' AND score>=80", taskID) != 1 ||
		formalScalar(t, "SELECT count(*) FROM run_records WHERE task_id=? AND trigger='auto' AND outcome='success'", taskID) < 1 ||
		formalScalar(t, "SELECT count(*) FROM ai_calls WHERE task_id=? AND purpose='analysis' AND ok=1", taskID) < 6 {
		t.Fatal("定时新品没有经过真实抓取、模型、算分和唯一推送")
	}
	formalVisit(t, browser, events, fmt.Sprintf("/results?task_id=%d", taskID), "results.title")
	waitWebSmoke(t, browser, events, `() => document.querySelector('main').innerText.includes('MacBook Pro 正式流程新品') && document.querySelector('main').innerText.includes('已推送')`)
	state := formalFixtureState(t)
	pushes, ok := state["pushes"].([]any)
	if !ok {
		t.Fatal("本地推送回执缺少正文")
	}
	matched := 0
	for _, value := range pushes {
		push, ok := value.(map[string]any)
		if !ok {
			t.Fatal("本地推送回执格式无效")
		}
		if push["item_id"] != "106" {
			continue
		}
		matched++
		if push["kind"] != "recommended" || !strings.Contains(fmt.Sprint(push["title"]), "MacBook Pro 正式流程新品") ||
			fmt.Sprint(push["score"]) != fmt.Sprint(formalScalar(t, "SELECT score FROM findings WHERE task_id=? AND item_id='106'", taskID)) || push["http_status"] != float64(204) {
			t.Fatal("本地推送的商品、标题、分数或送达状态不正确")
		}
	}
	if matched != 1 {
		t.Fatal("本地渠道没有恰好收到一次新品推送", matched)
	}
	formalSnapshot(t, browser, events, "scheduled-new-item")
	formalRecord(t, 6, "网页手动运行和一分钟真实定时调度成功；基线不推，新增商品经过详情、卖家、模型和规则后唯一送达，逐步日志与结果可读")
}

func formalResume(t *testing.T, browser context.Context, events *webSmokeEvents, _ formalMetadata, stage string) {
	runWebSmoke(t, browser, events, chromedp.Navigate(formalBaseURL+"/"))
	waitWebSmoke(t, browser, events, `() => location.pathname === '/overview' || location.pathname === '/login'`)
	path, err := chromedp.Run(browser, chromedp.Evaluate[string](`location.pathname`))
	if err != nil {
		t.Fatal(err)
	}
	if path == "/login" {
		runWebSmoke(t, browser, events,
			chromedp.SendKeys(chromedp.CSS(`input[name=username]`), formalUsername),
			chromedp.SendKeys(chromedp.CSS(`input[name=password]`), formalPassword),
			chromedp.Click(chromedp.CSS(`main button[type=submit]`)))
	}
	waitWebSmoke(t, browser, events, `() => location.pathname === '/overview'`)
	data, err := os.ReadFile("/work/reports/persistence.json")
	if err != nil {
		t.Fatal("缺少重启前的数据核对记录", err)
	}
	var before map[string]string
	if json.Unmarshal(data, &before) != nil {
		t.Fatal("重启前的数据核对记录格式无效")
	}
	current := formalPersistence(t)
	if before["settings"] != current["settings"] || before["task"] != current["task"] ||
		formalText(t, "SELECT * FROM pragma_integrity_check") != "ok" || formalScalar(t, "SELECT count(*) FROM pragma_foreign_key_check") != 0 {
		t.Fatal("容器重开改变了设置或任务配置，或数据库完整性异常")
	}
	if formalScalar(t, "SELECT count(*) FROM accounts WHERE name='本地扫码账号' AND state IN ('active','cooling')") != 1 ||
		formalScalar(t, "SELECT count(*) FROM tasks WHERE name='正式流程相机监控'") != 1 ||
		formalScalar(t, "SELECT count(*) FROM findings WHERE item_id='106' AND decision='scored'") != 1 ||
		formalText(t, "SELECT value_json FROM settings WHERE key='general.onboarding_done'") != "true" ||
		formalScalar(t, "SELECT count(*) FROM ai_providers WHERE name='本地模型' AND enabled=1") != 1 ||
		formalScalar(t, "SELECT count(*) FROM channels WHERE name='本地Webhook' AND enabled=1") != 1 {
		t.Fatal("容器重开后账号、任务、历史商品或设置没有保留")
	}
	formalVisit(t, browser, events, "/accounts", "accounts.title")
	waitWebSmoke(t, browser, events, `() => document.querySelector('main').innerText.includes('本地扫码账号') && !document.querySelector('main iframe')`)
	formalSnapshot(t, browser, events, stage+"-account")
	itemID := "109"
	if stage == "upgrade" {
		itemID = "110"
	}
	formalSearchWithNewItem(t, itemID, "MacBook Pro "+stage+"后新品")
	runID := formalRunOnce(t, browser, events)
	if formalText(t, "SELECT outcome FROM run_records WHERE id=?", runID) != "success" {
		t.Fatal("重启或升级后不能用原账号继续正常运行")
	}
	formalWait(t, browser, "重开后的新品送达", 90*time.Second, func() bool {
		return formalScalar(t, `SELECT count(*) FROM deliveries d JOIN delivery_items di ON di.delivery_id=d.id JOIN findings f ON f.id=di.finding_id WHERE f.item_id=? AND d.ok=1`, itemID) == 1
	})
	formalShowRun(t, browser, events, runID)
	formalSnapshot(t, browser, events, stage+"-continued-run")
	formalRecord(t, 8, stage+"：没有重新初始化，账号登录、任务、历史商品和设置保留，原账号继续抓取并唯一推送新商品")
	_ = os.WriteFile("/work/reports/"+stage+"-persisted.txt", []byte("保留数据并继续运行通过\n"), 0600)
}

func formalPersistence(t *testing.T) map[string]string {
	t.Helper()
	settings := formalText(t, "SELECT COALESCE(json_group_object(key,value_json),'{}') FROM (SELECT key,value_json FROM settings ORDER BY key)")
	task := formalText(t, `SELECT json_object('name',name,'keyword',keyword,'enabled',enabled,'account_id',account_id,'account_pinned',account_pinned,
		'min_price',min_price,'max_price',max_price,'personal_only',personal_only,'max_pages',max_pages,'interval_minutes',interval_minutes,
		'judge_mode',judge_mode,'profile',profile_json,'requirement',requirement,'budget_min',budget_min,'budget_max',budget_max,
		'notify_policy',notify_policy,'channel_ids',(SELECT json_group_array(channel_id) FROM (SELECT channel_id FROM task_channels WHERE task_id=tasks.id ORDER BY channel_id)))
		FROM tasks WHERE name='正式流程相机监控'`)
	return map[string]string{"settings": fmt.Sprintf("%x", sha256.Sum256([]byte(settings))), "task": fmt.Sprintf("%x", sha256.Sum256([]byte(task)))}
}
