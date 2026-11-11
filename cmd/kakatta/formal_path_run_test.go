package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
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
	formalNetworkPhase(browser, "06运行")
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

	previousAuto := formalScalar(t, "SELECT COALESCE(max(id),0) FROM run_records WHERE task_id=?", taskID)
	formalSearchWithNewItem(t, "106", "MacBook Pro 正式流程新品")
	formalWait(t, browser, "真实定时调度发现新品并送达", 3*time.Minute, func() bool {
		return formalScalar(t, `SELECT count(*) FROM deliveries d JOIN delivery_items di ON di.delivery_id=d.id JOIN findings f ON f.id=di.finding_id WHERE f.task_id=? AND f.item_id='106' AND d.ok=1 AND d.kind='recommended'`, taskID) == 1
	})
	if formalScalar(t, "SELECT count(*) FROM findings WHERE task_id=? AND item_id='106' AND is_baseline=0 AND decision='scored' AND score>=80", taskID) != 1 ||
		formalScalar(t, `SELECT count(*) FROM run_records r JOIN findings f ON f.task_id=r.task_id
			WHERE f.task_id=? AND f.item_id='106' AND r.id>? AND r.trigger='auto' AND r.outcome='success'
			AND f.found_at>=r.started_at AND f.found_at<=r.ended_at AND r.new_count>=1 AND r.analyzed_count>=1 AND r.notified_count>=1`, taskID, previousAuto) != 1 ||
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
	score := fmt.Sprint(formalScalar(t, "SELECT score FROM findings WHERE task_id=? AND item_id='106'", taskID))
	grade := smokeTranslation(t, locales.ZhCN, "analysis.grade."+formalText(t, "SELECT grade FROM findings WHERE task_id=? AND item_id='106'", taskID))
	summary := formalText(t, "SELECT COALESCE(summary,'') FROM findings WHERE task_id=? AND item_id='106'", taskID)
	for _, value := range pushes {
		push, ok := value.(map[string]any)
		if !ok {
			t.Fatal("本地推送回执格式无效")
		}
		if push["item_id"] != "106" {
			continue
		}
		matched++
		title := fmt.Sprint(push["title"])
		if push["kind"] != "recommended" || push["item_title"] != "MacBook Pro 正式流程新品" ||
			!strings.Contains(title, "正式流程相机监控") || !strings.Contains(title, grade) || !strings.Contains(title, score) || !strings.Contains(title, "¥8,000") ||
			fmt.Sprint(push["score"]) != score || push["grade"] != grade || push["price"] != "¥8,000" || summary == "" || !strings.Contains(fmt.Sprint(push["body"]), summary) || push["http_status"] != float64(204) {
			t.Fatal("本地推送的商品原题、通知标题、正文结论、分数、价格或送达状态不正确")
		}
	}
	if matched != 1 {
		t.Fatal("本地渠道没有恰好收到一次新品推送", matched)
	}
	formalSnapshot(t, browser, events, "scheduled-new-item")
	formalRecord(t, 6, "网页手动运行和一分钟真实定时调度成功；基线不推，新增商品经过详情、卖家、模型和规则后唯一送达，逐步日志与结果可读")
}

func formalResume(t *testing.T, browser context.Context, events *webSmokeEvents, _ formalMetadata, stage string) {
	formalNetworkPhase(browser, "08恢复/"+stage)
	events.mu.Lock()
	events.allowSignedOut = false
	events.mu.Unlock()
	runWebSmoke(t, browser, events, chromedp.Navigate(formalBaseURL+"/"))
	waitWebSmoke(t, browser, events, `() => location.pathname === '/overview' && !!document.querySelector('main h1') && !document.querySelector('input[name=password]')`)
	data, err := os.ReadFile("/work/reports/persistence.json")
	if err != nil {
		t.Fatal("缺少重启前的数据核对记录", err)
	}
	var before map[string]string
	if json.Unmarshal(data, &before) != nil {
		t.Fatal("重启前的数据核对记录格式无效")
	}
	current := formalPersistenceWithin(t, before)
	for _, key := range []string{"settings", "task", "account", "protection", "web_session", "master_key"} {
		if before[key] == "" || before[key] != current[key] {
			t.Fatal("容器重开改变了原有配置、账号保护或登录会话", key)
		}
	}
	for _, table := range formalPersistenceTables {
		if before[table.key] == "" || before[table.key] != current[table.key] {
			t.Fatal("容器重开丢失或改变了已有历史记录", table.key)
		}
	}
	for _, key := range []string{"clock", "last_run_ended_at"} {
		oldValue, oldErr := strconv.ParseInt(before[key], 10, 64)
		newValue, newErr := strconv.ParseInt(current[key], 10, 64)
		if oldErr != nil || newErr != nil || oldValue <= 0 || newValue < oldValue {
			t.Fatal("容器重开倒退了额度时钟或账号间隔记录", key)
		}
	}
	if formalText(t, "SELECT * FROM pragma_integrity_check") != "ok" || formalScalar(t, "SELECT count(*) FROM pragma_foreign_key_check") != 0 {
		t.Fatal("容器重开后的数据库完整性异常")
	}
	if formalScalar(t, `SELECT count(*) FROM auth_tokens WHERE kind='web_session' AND revoked_at IS NULL
		AND expires_at-last_used_at=? AND expires_at>?`, (30*24*time.Hour).Milliseconds(), time.Now().UnixMilli()) != 1 {
		t.Fatal("重启或升级后没有复用仍然有效的原三十天网页会话")
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
	if formalCountFromState(t, formalFixtureState(t), "login_completions") != formalCountFromSnapshot(t, before, "login_completions") {
		t.Fatal("重启或升级后继续运行不应重新扫码")
	}
	if formalScalar(t, `SELECT count(*) FROM (SELECT di.finding_id FROM delivery_items di JOIN deliveries d ON d.id=di.delivery_id
		WHERE d.task_id=? AND d.ok=1 AND d.kind='recommended' GROUP BY di.finding_id HAVING count(*)>1)`, formalTaskID(t)) != 0 {
		t.Fatal("重启或升级后的运行重复推送了已经送达的商品")
	}
	formalShowRun(t, browser, events, runID)
	formalSnapshot(t, browser, events, stage+"-continued-run")
	formalRecord(t, 8, stage+"：原三十天会话直接进入总览，未重新初始化或输密码；账号登录、保护、额度、任务和历史保留，未重新扫码并唯一推送新品")
	data, err = json.Marshal(formalPersistence(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/work/reports/persistence.json", data, 0600); err != nil {
		t.Fatal("保存下一次重开的数据核对记录失败", err)
	}
	if err := os.WriteFile("/work/reports/"+stage+"-persisted.txt", []byte("原会话和保护记录保留，继续运行通过\n"), 0600); err != nil {
		t.Fatal("保存重开验收记录失败", err)
	}
}

func formalPersistence(t *testing.T) map[string]string {
	return formalPersistenceWithin(t, nil)
}

func formalPersistenceWithin(t *testing.T, limits map[string]string) map[string]string {
	t.Helper()
	settings := formalText(t, "SELECT COALESCE(json_group_object(key,value_json),'{}') FROM (SELECT key,value_json FROM settings ORDER BY key)")
	task := formalText(t, `SELECT json_object('name',name,'keyword',keyword,'enabled',enabled,'account_id',account_id,'account_pinned',account_pinned,
		'min_price',min_price,'max_price',max_price,'personal_only',personal_only,'free_shipping_only',free_shipping_only,
		'publish_window',publish_window,'province',region_province,'city',region_city,'district',region_district,'max_pages',max_pages,'interval_minutes',interval_minutes,
		'crawl_overrides',crawl_overrides_json,'match_keywords',match_keywords_json,'exclude_keywords',exclude_keywords_json,'first_analysis',analyze_on_first_run,
		'judge_mode',judge_mode,'profile',profile_json,'requirement',requirement,'budget_min',budget_min,'budget_max',budget_max,
		'notify_policy',notify_policy,'notify_drop',notify_price_drop,'channel_ids',(SELECT json_group_array(channel_id) FROM (SELECT channel_id FROM task_channels WHERE task_id=tasks.id ORDER BY channel_id)))
		FROM tasks WHERE name='正式流程相机监控'`)
	account := formalText(t, `SELECT json_object('id',id,'name',name,'proxy',proxy_id,'nickname',nickname,'created_at',created_at) FROM accounts WHERE name='本地扫码账号'`)
	protection := formalText(t, `SELECT json_object('verify_level',verify_level,'last_cooldown_end_at',last_cooldown_end_at,'cooldown_manual_used',cooldown_manual_used) FROM accounts WHERE name='本地扫码账号'`)
	session := formalText(t, `SELECT COALESCE(json_group_array(json_object('id',id,'created_at',created_at,'revoked_at',revoked_at)),'[]') FROM (SELECT * FROM auth_tokens WHERE kind='web_session' ORDER BY id)`)
	key, err := os.ReadFile("/data/secret.key")
	if err != nil {
		t.Fatal("读取独立测试目录的主密钥指纹失败", err)
	}
	result := map[string]string{
		"settings": fmt.Sprintf("%x", sha256.Sum256([]byte(settings))), "task": fmt.Sprintf("%x", sha256.Sum256([]byte(task))),
		"account": fmt.Sprintf("%x", sha256.Sum256([]byte(account))), "protection": fmt.Sprintf("%x", sha256.Sum256([]byte(protection))),
		"web_session": fmt.Sprintf("%x", sha256.Sum256([]byte(session))), "master_key": fmt.Sprintf("%x", sha256.Sum256(key)),
		"clock":             formalText(t, `SELECT CAST(json_extract(value,'$.highest_utc') AS TEXT) FROM meta WHERE key='quota.clock'`),
		"last_run_ended_at": formalText(t, `SELECT CAST(last_run_ended_at AS TEXT) FROM accounts WHERE name='本地扫码账号'`),
		"login_completions": strconv.FormatInt(formalCountFromState(t, formalFixtureState(t), "login_completions"), 10),
	}
	for _, table := range formalPersistenceTables {
		limit := formalScalar(t, table.limitQuery)
		if limits != nil {
			limit = formalCountFromSnapshot(t, limits, table.key+"_max")
		}
		if limit <= 0 {
			t.Fatal("正式流程必须先生成实际记录，才能核对重启保留", table.key)
		}
		rows := formalText(t, table.rowsQuery, limit)
		result[table.key] = fmt.Sprintf("%x", sha256.Sum256([]byte(rows)))
		result[table.key+"_max"] = strconv.FormatInt(limit, 10)
	}
	return result
}

func formalCountFromSnapshot(t *testing.T, snapshot map[string]string, key string) int64 {
	t.Helper()
	value, err := strconv.ParseInt(snapshot[key], 10, 64)
	if err != nil || value < 0 {
		t.Fatal("重启前的数据核对记录缺少合法计数", key)
	}
	return value
}

var formalPersistenceTables = []struct{ key, limitQuery, rowsQuery string }{
	{"history", "SELECT COALESCE(max(id),0) FROM findings", `SELECT COALESCE(json_group_array(value),'[]') FROM (
		SELECT json_object('id',f.id,'task',f.task_id,'item',f.item_id,'found_at',f.found_at,'baseline',f.is_baseline,
		'analysis',f.analysis_json,'title',l.title,'first_seen_at',l.first_seen_at,'description',l.description,'images',l.image_urls_json) AS value
		FROM findings f JOIN listings l ON l.item_id=f.item_id WHERE f.id<=? ORDER BY f.id)`},
	{"runs", "SELECT COALESCE(max(id),0) FROM run_records WHERE ended_at IS NOT NULL AND outcome<>'skipped'", `SELECT COALESCE(json_group_array(value),'[]') FROM (
		SELECT json_object('id',id,'task',task_id,'trigger',trigger,'started',started_at,'ended',ended_at,'outcome',outcome,'failure',failure_kind,'message',message,
		'pages',page_count,'scanned',scanned_count,'new',new_count,'analyzed',analyzed_count,'notified',notified_count) AS value
		FROM run_records WHERE id<=? AND ended_at IS NOT NULL AND outcome<>'skipped' ORDER BY id)`},
	{"quota", "SELECT COALESCE(max(id),0) FROM quota_events", `SELECT COALESCE(json_group_array(value),'[]') FROM (
		SELECT json_object('id',id,'account',account_id,'kind',kind,'run',run_id,'at',at) AS value FROM quota_events WHERE id<=? ORDER BY id)`},
	{"model_calls", "SELECT COALESCE(max(id),0) FROM ai_calls WHERE ok IS NOT NULL", `SELECT COALESCE(json_group_array(value),'[]') FROM (
		SELECT json_object('id',id,'at',at,'task',task_id,'provider',provider_id,'purpose',purpose,'ok',ok,'http_status',http_status,'error',error_kind,
		'input',input_tokens,'output',output_tokens,'reasoning',reasoning_tokens) AS value FROM ai_calls WHERE id<=? AND ok IS NOT NULL ORDER BY id)`},
	{"deliveries", "SELECT COALESCE(max(id),0) FROM deliveries WHERE ok IS NOT NULL", `SELECT COALESCE(json_group_array(value),'[]') FROM (
		SELECT json_object('id',id,'at',at,'task',task_id,'channel',channel_id,'kind',kind,'count',item_count,'ok',ok,'error',error) AS value
		FROM deliveries WHERE id<=? AND ok IS NOT NULL ORDER BY id)`},
}

func TestFormalPersistenceQueriesMatchSchema(t *testing.T) {
	g := graphFixture(t)
	for _, table := range formalPersistenceTables {
		t.Run(table.key, func(t *testing.T) {
			var maximum int64
			if err := g.database.DB().QueryRowContext(t.Context(), table.limitQuery).Scan(&maximum); err != nil {
				t.Fatal("正式路径的历史边界查询不符合表结构", err)
			}
			var rows string
			if err := g.database.DB().QueryRowContext(t.Context(), table.rowsQuery, maximum).Scan(&rows); err != nil || rows != "[]" {
				t.Fatal("正式路径的历史核对查询不符合表结构或空集合含义", rows, err)
			}
		})
	}
}

func TestFormalPersistenceQuotaBoundAllowsAppendAndDetectsLoss(t *testing.T) {
	g := graphFixture(t)
	if _, err := g.database.DB().Exec(`INSERT INTO accounts(id,name,state) VALUES(1,'核对用假账号','active');
		INSERT INTO quota_events(id,account_id,kind,at) VALUES(1,1,'search',1000),(2,1,'detail',2000)`); err != nil {
		t.Fatal(err)
	}
	var query string
	for _, table := range formalPersistenceTables {
		if table.key == "quota" {
			query = table.rowsQuery
		}
	}
	read := func() string {
		t.Helper()
		var rows string
		if err := g.database.DB().QueryRowContext(t.Context(), query, 2).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	before := read()
	if _, err := g.database.DB().Exec(`INSERT INTO quota_events(id,account_id,kind,at) VALUES(3,1,'search',3000)`); err != nil {
		t.Fatal(err)
	}
	if read() != before {
		t.Fatal("正常追加访问记录不能被误判为历史改变")
	}
	if _, err := g.database.DB().Exec(`DELETE FROM quota_events WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if read() == before {
		t.Fatal("旧访问记录丢失必须使核对失败")
	}
}
