package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/xianyu/fake"
)

const coreSoakRounds = 30

func coreSoakSequence(index int) []fake.SearchRound {
	anchors := make([]fake.SearchItem, 5)
	for i := range anchors {
		anchors[i] = fake.SearchItem{ID: fmt.Sprint(index*1000 + i + 1), Title: "MacBook Pro 长跑锚点", Price: "10000", SellerID: fmt.Sprintf("anchor-%d-%d", index, i)}
	}
	sequence := make([]fake.SearchRound, coreSoakRounds)
	for round := range sequence {
		sequence[round] = fake.SearchRound{Page1: append([]fake.SearchItem(nil), anchors[:3]...), Page2: append([]fake.SearchItem(nil), anchors[2:]...)}
		if round == 0 {
			continue
		}
		price := "8000"
		if (round-1)%3 != 0 {
			price = "7000"
		}
		sequence[round].Page2 = append(sequence[round].Page2, fake.SearchItem{
			ID: fmt.Sprint(index*1000 + 100 + (round-1)/3), Title: "MacBook Pro 长跑新品", Price: price, SellerID: fmt.Sprintf("new-%d", index),
		})
	}
	return sequence
}

func TestCoreSoakAutomaticThirtyRounds(t *testing.T) {
	if os.Getenv("KAKATTA_CORE_SOAK_TEST") != "1" {
		t.Skip("只由 deploy/test-core-soak.sh 执行有限的真实自动长跑")
	}
	directory := os.Getenv("KAKATTA_CORE_SOAK_DIR")
	if directory == "" {
		t.Fatal("长跑缺少原始记录目录")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 55*time.Minute)
	defer cancel()
	var firstTitles atomic.Int64
	firstPair := make(chan struct{})
	pushFile, err := os.Create(filepath.Join(directory, "push-ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer pushFile.Close()
	progressFile, err := os.Create(filepath.Join(directory, "progress.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer progressFile.Close()
	type wantedPush struct{ price string }
	expected, received := map[string]wantedPush{}, map[string]bool{}
	for index := 1; index <= 4; index++ {
		for cycle := range 10 {
			item := fmt.Sprint(index*1000 + 100 + cycle)
			for kind, price := range map[string]string{"recommended": "¥8,000", "price_drop": "¥7,000"} {
				expected[fmt.Sprintf("长跑任务%d/%s/%s", index, item, kind)] = wantedPush{price}
			}
		}
	}
	var pushMu sync.Mutex
	pushErrors := make(chan error, 1)
	h := newCoreFlowHarness(t, ctx, func(_ http.ResponseWriter, r *http.Request, kind string) bool {
		if kind != "title_screen" {
			return false
		}
		number := firstTitles.Add(1)
		if number == 2 {
			close(firstPair)
		}
		if number <= 2 {
			select {
			case <-firstPair:
			case <-r.Context().Done():
				t.Error("双账号首轮标题请求未能同时进入全局模型名额")
			}
		}
		return false
	}, func(_ http.ResponseWriter, _ *http.Request, body map[string]string) bool {
		key := body["task"] + "/" + body["item_id"] + "/" + body["kind"]
		pushMu.Lock()
		defer pushMu.Unlock()
		want, known := expected[key]
		var mismatch error
		switch {
		case !known:
			mismatch = fmt.Errorf("长跑出现清单外推送：%s", key)
		case received[key]:
			mismatch = fmt.Errorf("长跑重复推送：%s", key)
		case body["price"] != want.price:
			mismatch = fmt.Errorf("长跑推送价格错误：%s，得到%s，期望%s", key, body["price"], want.price)
		case body["kind"] == "price_drop" && (!strings.Contains(body["body"], "8,000") || !strings.Contains(body["body"], "7,000")):
			mismatch = fmt.Errorf("长跑降价正文缺少正确的原价和现价：%s", key)
		}
		if mismatch == nil {
			received[key] = true
		}
		if err := json.NewEncoder(pushFile).Encode(map[string]any{"at": time.Now().UTC(), "key": key, "message": body}); mismatch == nil {
			mismatch = err
		}
		if mismatch != nil {
			select {
			case pushErrors <- mismatch:
			default:
			}
		}
		return false
	})
	stopMetrics := startCoreSoakMetrics(t, h, directory)
	defer func() {
		h.Close()
		stopMetrics()
		source, err := os.Open(h.paths.Database)
		if err != nil {
			t.Error(err)
			return
		}
		defer source.Close()
		target, err := os.OpenFile(filepath.Join(directory, "closed-database.sqlite"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			t.Error(err)
			return
		}
		_, copyErr := io.Copy(target, source)
		if closeErr := target.Close(); copyErr != nil || closeErr != nil {
			t.Errorf("保存已关闭的长跑数据库失败：%v %v", copyErr, closeErr)
		}
	}()
	timezone := "UTC"
	if hour := time.Now().UTC().Hour(); hour < 6 || hour >= 18 {
		timezone = "Etc/GMT+12"
	}
	h.Request(http.MethodPatch, "/api/v1/settings", map[string]any{"values": map[string]any{
		"general.timezone": timezone, "crawl.global_pages": 2, "ai.max_concurrent_requests": 2,
		"crawl.account_hourly_visits": 1000, "crawl.account_daily_search_visits": 1000, "crawl.account_daily_detail_visits": 1000,
	}}, nil, http.StatusOK)
	var second service.AccountView
	h.Request(http.MethodPost, "/api/v1/accounts", map[string]any{"name": "长跑第二账号"}, &second, http.StatusCreated)
	h.BeginLogin(second.ID)
	coreWait(t, ctx, "两个账号完成真实模拟登录", func() bool {
		h.Request(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d", second.ID), nil, &second, http.StatusOK)
		return second.State == service.AccountActive && second.Session == nil
	})
	tasks := make([]service.Task, 4)
	words := make([]string, 4)
	for i := range tasks {
		words[i] = fmt.Sprintf("MacBook soak%d", i+1)
		h.xianyu.SetSearchSequence(words[i], coreSoakSequence(i+1))
		account := h.accountID
		if i >= 2 {
			account = second.ID
		}
		tasks[i] = h.CreateTask(map[string]any{"name": fmt.Sprintf("长跑任务%d", i+1), "keyword": words[i], "account_id": account, "interval_minutes": 1, "max_pages": 2, "notify_price_drop": true})
	}
	for _, task := range tasks {
		h.Request(http.MethodPatch, fmt.Sprintf("/api/v1/tasks/%d", task.ID), map[string]any{"enabled": true}, nil, http.StatusOK)
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	completed, stopped := make([]int64, 4), make([]bool, 4)
	lastRound, lastProgress := int64(0), time.Now()
	for {
		select {
		case failure := <-pushErrors:
			t.Fatal(failure)
		default:
		}
		allStopped := true
		for i, task := range tasks {
			count := graphScalar(t, h.g, `SELECT count(*) FROM run_records WHERE task_id=? AND ended_at IS NOT NULL`, task.ID)
			if graphScalar(t, h.g, `SELECT count(*) FROM run_records WHERE task_id=? AND ended_at IS NOT NULL AND (trigger<>'auto' OR outcome<>'success' OR failure_kind IS NOT NULL)`, task.ID) != 0 || count > coreSoakRounds {
				t.Fatalf("长跑任务%d有失败、跳过、非自动或超出30次的运行", task.ID)
			}
			if count != completed[i] {
				completed[i] = count
				t.Logf("长跑任务%d已完成%d/%d次真实自动运行", task.ID, count, coreSoakRounds)
			}
			if count == coreSoakRounds && !stopped[i] {
				h.Request(http.MethodPatch, fmt.Sprintf("/api/v1/tasks/%d", task.ID), map[string]any{"enabled": false}, nil, http.StatusOK)
				stopped[i] = true
			}
			allStopped = allStopped && stopped[i]
		}
		var status service.SystemStatus
		h.Request(http.MethodGet, "/api/v1/system/status", nil, &status, http.StatusOK)
		if !status.Scheduler.Running || !status.Scheduler.Accepting || status.Browsers.Active > 2 {
			t.Fatalf("长跑调度停止或突破页面名额：%+v", status)
		}
		seenAccounts := map[int64]bool{}
		for _, session := range status.Browsers.Sessions {
			if session.Phase != service.SessionQueued {
				if seenAccounts[session.AccountID] {
					t.Fatalf("账号%d同时持有多个页面", session.AccountID)
				}
				seenAccounts[session.AccountID] = true
			}
		}
		if _, maximum := h.ModelConcurrency(); maximum > 2 {
			t.Fatalf("模型物理请求突破全局并发：%d", maximum)
		}
		round := min(completed[0], completed[1], completed[2], completed[3])
		if round > lastRound || time.Since(lastProgress) >= time.Minute {
			rows := map[string]int64{}
			for _, table := range []string{"run_records", "run_steps", "findings", "ai_calls", "deliveries", "quota_events"} {
				rows[table] = graphScalar(t, h.g, "SELECT count(*) FROM "+table)
			}
			for _, pragma := range []string{"page_count", "page_size", "freelist_count"} {
				rows[pragma] = graphScalar(t, h.g, "PRAGMA "+pragma)
			}
			if err := json.NewEncoder(progressFile).Encode(map[string]any{"at": time.Now().UTC(), "all_tasks_round": round, "completed": completed, "active_pages": status.Browsers.Active, "rows": rows}); err != nil {
				t.Fatal(err)
			}
			t.Logf("全部任务已完成%d/30轮；各任务=%v；当前页面=%d；记录数=%v", round, completed, status.Browsers.Active, rows)
			lastRound, lastProgress = round, time.Now()
		}
		if allStopped && status.Browsers.Active == 0 && status.Scheduler.RunningCount == 0 && status.Scheduler.QueuedCount == 0 && graphScalar(t, h.g, `SELECT count(*) FROM meta WHERE key LIKE 'runtime.session.%'`) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("真实自动长跑没有在55分钟内完成：%v", completed)
		case <-tick.C:
		}
	}
	coreWait(t, ctx, "所有浏览器和显示进程真正退出", func() bool {
		processes, err := coreSoakBrowserProcesses()
		if err != nil {
			t.Fatal(err)
		}
		return len(processes) == 0
	})
	coreSoakAssertFinal(t, h, tasks, words)
	pushMu.Lock()
	for key := range expected {
		if !received[key] {
			t.Errorf("长跑漏推：%s", key)
		}
	}
	count := len(received)
	pushMu.Unlock()
	if count != 80 || len(h.Pushes()) != 80 {
		t.Fatalf("实际推送数不符：独立清单=%d，HTTP=%d", count, len(h.Pushes()))
	}
	if active, maximum := h.ModelConcurrency(); active != 0 || maximum != 2 || len(h.ModelKinds()) != 104 || len(h.Products()) != 60 {
		t.Fatalf("模型并发或逐物理请求数不符：active=%d max=%d calls=%d products=%d", active, maximum, len(h.ModelKinds()), len(h.Products()))
	}
	runtime.GC()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	summary, err := os.Create(filepath.Join(directory, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer summary.Close()
	if err := json.NewEncoder(summary).Encode(map[string]any{"completed": completed, "pushes": count, "model_calls": 104, "listings": 60, "after_gc_heap_alloc": memory.HeapAlloc, "after_gc_heap_objects": memory.HeapObjects, "timezone": timezone}); err != nil {
		t.Fatal(err)
	}
	t.Log("两账号四任务完成120次真实自动运行、80次唯一推送、104次模型请求；资源释放、精确计数及数据库检查通过")
}

func TestCoreSoakScenarioKeepsFiveAnchorPrices(t *testing.T) {
	profile := analysis.Profile{Specs: []analysis.ProfileSpec{{Name: "型号", Options: []string{"MacBook Pro"}, Values: []string{"MacBook Pro"}}}}
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
	known, prices := map[string]fake.SearchItem{}, map[string]int64{}
	points := 0
	for index, round := range coreSoakSequence(1) {
		seen := map[string]bool{}
		for _, items := range [][]fake.SearchItem{round.Page1, round.Page2} {
			for _, item := range items {
				known[item.ID], seen[item.ID] = item, true
				amount, err := strconv.ParseInt(item.Price, 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				if old, found := prices[item.ID]; !found || old != amount*100 {
					points++
				}
				prices[item.ID] = amount * 100
			}
		}
		want := 6
		if index == 0 {
			want = 5
		}
		if len(seen) != want {
			t.Fatalf("第%d轮去重后的商品数=%d，期望%d", index+1, len(seen), want)
		}
		observations := []analysis.MarketObservation{}
		for id, item := range known {
			price, model := prices[id], "MacBook Pro"
			observations = append(observations, analysis.MarketObservation{ItemID: id, SellerID: item.SellerID, Day: "2026-11-11", LastSeenAt: now, Title: item.Title, Kind: analysis.KindSale, Price: &price, Specs: []analysis.SpecValue{{Name: "型号", Value: &model}}})
		}
		market := analysis.CalculateMarket(analysis.MarketInput{Mode: analysis.ModeAI, Profile: profile, Observations: observations, NowMS: now, TimeZone: "UTC"})
		if len(market.Groups) != 1 || market.Groups[0].Fair.Cents == nil || *market.Groups[0].Fair.Cents != 1000000 || market.Groups[0].Fair.Sample != 5 {
			t.Fatalf("第%d轮没有维持五个10000元合理价锚点：%+v", index+1, market)
		}
	}
	if len(known) != 15 || points != 25 {
		t.Fatalf("30轮样例的商品或价点计数错误：items=%d points=%d", len(known), points)
	}
}

func coreSoakAssertFinal(t *testing.T, h *coreFlowHarness, tasks []service.Task, words []string) {
	t.Helper()
	for i, task := range tasks {
		if h.xianyu.SearchSequenceStarts(words[i]) != coreSoakRounds || graphScalar(t, h.g, `SELECT count(*) FROM findings WHERE task_id=?`, task.ID) != 15 ||
			graphScalar(t, h.g, `SELECT count(*) FROM findings WHERE task_id=? AND decision='scored' AND stage='done' AND fair_price=1000000 AND fair_sample=5`, task.ID) != 15 ||
			graphScalar(t, h.g, `SELECT count(*) FROM findings WHERE task_id=? AND is_baseline=1 AND json_extract(notify_skip_reason,'$.code')='notify.baseline'`, task.ID) != 5 ||
			graphScalar(t, h.g, `SELECT count(*) FROM findings WHERE task_id=? AND is_baseline=0 AND notified_at IS NOT NULL AND last_drop_notified_price=700000 AND drop_notified_at IS NOT NULL`, task.ID) != 10 ||
			graphScalar(t, h.g, `SELECT count(*) FROM ai_calls WHERE task_id=? AND purpose='title_screen' AND ok=1`, task.ID) != 11 ||
			graphScalar(t, h.g, `SELECT count(*) FROM ai_calls WHERE task_id=? AND purpose='analysis' AND ok=1`, task.ID) != 15 ||
			graphScalar(t, h.g, `SELECT sum(scanned_count) FROM run_records WHERE task_id=?`, task.ID) != 179 {
			t.Fatalf("任务%d的真实轮次、基线、数值算分、降价或模型物理计数不符", task.ID)
		}
	}
	for table, want := range map[string]int64{"run_records": 120, "listings": 60, "findings": 60, "seen_items": 60, "task_item_specs": 60, "task_sightings": 60, "seller_profiles": 24, "price_points": 100, "ai_calls": 104, "notification_intents": 80, "deliveries": 80, "delivery_items": 80, "delivery_intents": 80, "quota_events": 712} {
		if got := graphScalar(t, h.g, "SELECT count(*) FROM "+table); got != want {
			t.Errorf("%s有%d行，期望%d", table, got, want)
		}
	}
	for _, test := range []struct {
		query string
		want  int64
	}{
		{`SELECT count(*) FROM notification_intents WHERE kind='recommended' AND state='sent' AND failure_count=0 AND price=800000`, 40},
		{`SELECT count(*) FROM notification_intents WHERE kind='price_drop' AND state='sent' AND failure_count=0 AND price=700000 AND previous_price=800000`, 40},
		{`SELECT count(*) FROM deliveries WHERE ok=1 AND item_count=1`, 80},
		{`SELECT count(*) FROM delivery_intents di JOIN delivery_items df ON df.delivery_id=di.delivery_id JOIN notification_intents i ON i.id=di.intent_id WHERE i.finding_id=df.finding_id`, 80},
		{`SELECT count(*) FROM ai_calls WHERE ok=0`, 0},
		{`SELECT sum(COALESCE(input_tokens,0)+COALESCE(output_tokens,0)) FROM ai_calls`, 3120},
		{`SELECT count(*) FROM quota_events WHERE kind='search'`, 602},
		{`SELECT count(*) FROM quota_events WHERE kind='detail'`, 110},
		{`SELECT count(*) FROM tasks WHERE enabled=0 AND baseline_pending=0 AND consecutive_failures=0`, 4},
		{`SELECT count(*) FROM (SELECT run_id FROM run_steps GROUP BY run_id HAVING count(*)>100 OR count(*)<>max(seq))`, 0},
	} {
		if got := graphScalar(t, h.g, test.query); got != test.want {
			t.Errorf("长跑最终查询得到%d，期望%d：%s", got, test.want, test.query)
		}
	}
	var integrity string
	if err := h.g.database.DB().QueryRowContext(t.Context(), `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Errorf("长跑数据库完整性：%s，%v", integrity, err)
	}
	if graphScalar(t, h.g, `SELECT count(*) FROM pragma_foreign_key_check`) != 0 {
		t.Error("长跑数据库出现外键损坏")
	}
}
