package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func marketTestPtr[T any](value T) *T { return &value }

func marketTestJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func marketTestService(t *testing.T) (*Service, *sql.DB, Task, int64) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "market.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	preferences, err := appsettings.New(t.Context(), db.DB(), settings.Environment{TZ: "UTC"}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	profile := analysis.Profile{
		Specs:   []analysis.ProfileSpec{{Name: "存储", Options: []string{"128G", "256G"}, Values: []string{"128G"}, Source: analysis.SourceUser}},
		Musts:   []analysis.Requirement{{ID: "m1", Text: "国行", Source: analysis.SourceUser, Rev: 1}},
		Bonuses: []analysis.Requirement{}, Risks: []analysis.ProfileRisk{}, AllowedKinds: []analysis.Kind{analysis.KindSale}, NextNo: 2,
	}
	result, err := db.DB().ExecContext(t.Context(), `INSERT INTO tasks(name,keyword,judge_mode,max_pages,interval_minutes,sort_index,notify_policy,profile_json)
		VALUES('平板','平板','ai',1,30,0,'good',?)`, marketTestJSON(t, profile))
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	task := Task{ID: id, SearchGeneration: 1, TaskFields: TaskFields{Name: "平板", Keyword: "平板", JudgeMode: analysis.ModeAI, Profile: profile, ExcludeKeywords: []string{}}}
	return &Service{db: db.DB(), settings: preferences, now: func() time.Time { return now }}, db.DB(), task, now.UnixMilli()
}

func marketTestSeed(t *testing.T, db *sql.DB, task Task, itemID, title string, price *int64, day string, nowMS int64) {
	t.Helper()
	_, err := db.ExecContext(t.Context(), `INSERT INTO listings(item_id,title,desktop_url,price,first_seen_at,last_seen_at,seller_id)
		VALUES(?,?,?,999999,?,?,?)`, itemID, title, "https://example.invalid/item/"+itemID, nowMS, nowMS, "seller-"+itemID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(t.Context(), `INSERT INTO task_sightings(task_id,item_id,day,price,last_seen_at) VALUES(?,?,?,?,?)`, task.ID, itemID, day, price, nowMS)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(t.Context(), `INSERT INTO task_item_specs(task_id,item_id,kind,specs_json,brand_new,config_key,excluded,source,updated_at)
		VALUES(?,?,'sale',?,0,'128G',0,'detail',?)`, task.ID, itemID,
		marketTestJSON(t, []analysis.SpecValue{{Name: "存储", Value: marketTestPtr("128G")}}), nowMS)
	if err != nil {
		t.Fatal(err)
	}
}

func marketTestLatest(t *testing.T, db *sql.DB, task Task, ids []string) {
	t.Helper()
	raw := marketTestJSON(t, map[string]any{"run_id": int64(1), "search_generation": task.SearchGeneration, "item_ids": ids})
	_, err := db.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		"task.latest_search."+strconv.FormatInt(task.ID, 10), raw)
	if err != nil {
		t.Fatal(err)
	}
}

func marketTestRead(t *testing.T, db *sql.DB, task Task, nowMS int64) analysis.MarketResult {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	market, err := loadMarketTx(t.Context(), tx, task, nowMS, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	return market
}

func TestMarketReadsCurrentSourcesAndExactLatestSet(t *testing.T) {
	_, db, task, now := marketTestService(t)
	day := "2026-11-11"
	for i := 0; i < 5; i++ {
		marketTestSeed(t, db, task, "ok-"+strconv.Itoa(i), "平板", marketTestPtr(int64(1000+i*10)), day, now)
	}
	_, err := db.ExecContext(t.Context(), `INSERT INTO findings(task_id,item_id,search_generation,found_at,is_baseline,decision,reject_kind,stage,decided_layer,is_read)
		VALUES(?,'ok-0',1,?,0,'rejected','filter','done',1,0)`, task.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{"excluded", "dismissed", "unknown", "junk", "word", "old"} {
		itemDay := day
		if item == "old" {
			itemDay = "2026-10-10"
		}
		marketTestSeed(t, db, task, item, "平板 "+item, marketTestPtr(int64(1020)), itemDay, now)
	}
	for _, statement := range []string{
		`UPDATE task_item_specs SET excluded=1 WHERE item_id='excluded'`,
		`UPDATE listings SET is_dismissed=1 WHERE item_id='dismissed'`,
		`UPDATE task_item_specs SET specs_json='[]',config_key=NULL WHERE item_id='unknown'`,
		`UPDATE task_item_specs SET kind='wanted' WHERE item_id='junk'`,
		`INSERT INTO blocked_sellers(seller_id) VALUES('seller-ok-0')`,
	} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	task.ExcludeKeywords = []string{"word"}
	marketTestLatest(t, db, task, []string{"ok-2"})
	market := marketTestRead(t, db, task, now)
	if len(market.Groups) != 1 || market.Groups[0].Fair.Cents == nil || *market.Groups[0].Fair.Cents != 1020 || market.Groups[0].Fair.Sample != 5 {
		t.Fatalf("未按当前资料筛选、或误用全局快照价格/第一层结果：%+v", market)
	}
	if market.Groups[0].Cheapest == nil || market.Groups[0].Cheapest.ItemID != "ok-2" {
		t.Fatalf("现在最便宜没有使用准确的最近搜索集合：%+v", market.Groups[0].Cheapest)
	}
	marketTestLatest(t, db, task, []string{})
	if got := marketTestRead(t, db, task, now); got.Groups[0].Cheapest != nil {
		t.Fatal("空搜索错误沿用了旧集合")
	}
	marketTestLatest(t, db, task, []string{"ok-0"})
	task.SearchGeneration++
	if got := marketTestRead(t, db, task, now); got.Groups[0].Cheapest != nil {
		t.Fatal("旧搜索代次的集合被当成当前结果")
	}
	task.ExcludeKeywords = []string{"平板"}
	if got := marketTestRead(t, db, task, now); len(got.Groups) != 0 {
		t.Fatal("修改当前排除词后仍用了旧的筛选结果")
	}
}

func TestMarketKeepsLatestUnknownPriceAndKeywordSamples(t *testing.T) {
	_, db, task, now := marketTestService(t)
	for i := 0; i < 5; i++ {
		marketTestSeed(t, db, task, strconv.Itoa(i), "平板", marketTestPtr(int64(1000+i*10)), "2026-11-11", now)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO task_sightings(task_id,item_id,day,price,last_seen_at) VALUES(?,'0','2026-11-10',1000,?)`, task.ID, now-86400000); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE task_sightings SET price=NULL WHERE item_id='0' AND day='2026-11-11'`); err != nil {
		t.Fatal(err)
	}
	market := marketTestRead(t, db, task, now)
	if len(market.Groups) != 1 || market.Groups[0].Fair.Sample != 4 || market.Groups[0].Fair.Cents != nil {
		t.Fatalf("最新未知价格退回了旧价：%+v", market)
	}
	task.JudgeMode, task.Profile = analysis.ModeKeyword, analysis.Profile{}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM task_item_specs WHERE task_id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE listings SET title='求购 平板' WHERE item_id='1'`); err != nil {
		t.Fatal(err)
	}
	market = marketTestRead(t, db, task, now)
	if len(market.Groups) != 1 || market.Groups[0].ConfigKey != "*" || market.Groups[0].Fair.Sample != 3 || market.Groups[0].Fair.Cents == nil || *market.Groups[0].Fair.Cents != 1030 {
		t.Fatalf("关键词行情错误依赖规格或包含求购：%+v", market)
	}
}

func TestMarketPublicPaginationInsightsAndFailurePaths(t *testing.T) {
	s, db, task, now := marketTestService(t)
	for i := 0; i < 6; i++ {
		marketTestSeed(t, db, task, strconv.Itoa(i), "平板", marketTestPtr(int64(1000+i/2)), "2026-11-11", now)
	}
	marketTestLatest(t, db, task, []string{"0", "1"})
	s.now = func() time.Time { return time.UnixMilli(now - 48*60*60*1000) }
	var clockBefore string
	if err := db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&clockBefore); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetMarket(t.Context(), task.ID)
	if err != nil || view.Insight != nil || len(view.Market.Groups) != 1 {
		t.Fatalf("初始行情读取错误：%+v %v", view, err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO market_insights(task_id,generated_at,stats_json,text,provider_id,model) VALUES(?,?,?,?,3,'本地模型')`,
		task.ID, now, marketTestJSON(t, view.Market), "已保存解读"); err != nil {
		t.Fatal(err)
	}
	view, err = s.GetMarket(t.Context(), task.ID)
	if err != nil || view.Insight == nil || view.Insight.Text == nil || *view.Insight.Text != "已保存解读" || view.Insight.Stats == nil {
		t.Fatalf("读取旧解读失败或伪造新分析：%+v %v", view, err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE market_insights SET stats_json='null' WHERE task_id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	view, err = s.GetMarket(t.Context(), task.ID)
	if err != nil || view.Insight == nil || view.Insight.Stats != nil {
		t.Fatal("未知的旧行情快照被伪装成空统计", err)
	}
	q := MarketSampleQuery{ListQuery: ListQuery{Limit: 2}, ConfigKey: "128G", BrandNew: marketTestPtr(false)}
	ids := []string{}
	var firstCursor string
	for {
		page, err := s.ListMarketSamples(t.Context(), task.ID, q)
		if err != nil || page.Total != 6 || page.Items == nil {
			t.Fatalf("行情分页错误：%+v %v", page, err)
		}
		for _, item := range page.Items {
			ids = append(ids, item.ItemID)
			if item.Title != "平板" || item.ConfigKey != "128G" || item.Price > 1002 || item.DesktopURL == "" {
				t.Fatalf("分页没返回实际样本：%+v", item)
			}
		}
		if page.NextCursor == nil {
			break
		}
		q.Cursor = *page.NextCursor
		if firstCursor == "" {
			firstCursor = q.Cursor
		}
	}
	if !reflect.DeepEqual(ids, []string{"0", "1", "2", "3", "4", "5"}) {
		t.Fatalf("同价锚点丢失或重复：%v", ids)
	}
	var clockAfter string
	if err := db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&clockAfter); err != nil || clockAfter != clockBefore {
		t.Fatal("只读行情推进了保护时钟", err)
	}
	q.Cursor, q.ConfigKey = firstCursor, "256G"
	_, err = s.ListMarketSamples(t.Context(), task.ID, q)
	var invalid *ServiceError
	if !errors.As(err, &invalid) || invalid.Kind != ErrorBadRequest {
		t.Fatalf("游标允许换筛选：%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.GetMarket(ctx, task.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("读取吞掉取消：%v", err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE task_item_specs SET specs_json='{}' WHERE item_id='0'`); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetMarket(t.Context(), task.ID)
	var unavailable *ServiceError
	if !errors.As(err, &unavailable) || unavailable.Kind != ErrorUnavailable {
		t.Fatalf("坏存储数据被当成空行情：%v", err)
	}
}
