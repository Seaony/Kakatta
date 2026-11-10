package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func queryPtr[T any](value T) *T { return &value }

func TestFindingQueryTaskIDEnforcesSafeIntegers(t *testing.T) {
	q, err := normalizeFindingQuery(FindingQuery{TaskID: queryPtr(limits.MaxSafeInteger)})
	if err != nil || q.TaskID == nil || *q.TaskID != limits.MaxSafeInteger {
		t.Fatalf("安全编号未原样保留：%+v %v", q, err)
	}
	for _, value := range []int64{0, -1, limits.MaxSafeInteger + 1, limits.MinSafeInteger - 1} {
		_, err := normalizeFindingQuery(FindingQuery{TaskID: &value})
		assertQueryError(t, err)
	}
}

func queryFixture(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "query.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if database.DB().Stats().MaxOpenConnections != 1 {
		t.Fatal("查询回归必须使用正式的单连接设置")
	}
	return &Service{db: database.DB()}, database.DB()
}

func queryExec(t *testing.T, db *sql.DB, statement string, args ...any) sql.Result {
	t.Helper()
	result, err := db.ExecContext(t.Context(), statement, args...)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func queryTask(t *testing.T, db *sql.DB, budget *int64) int64 {
	t.Helper()
	result := queryExec(t, db, `INSERT INTO tasks(name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index,budget_max)
		VALUES('查询任务','camera',1,60,'ai','good',0,?)`, budget)
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func queryListing(t *testing.T, db *sql.DB, itemID, title string, price *int64) {
	t.Helper()
	queryExec(t, db, `INSERT INTO listings(item_id,title,desktop_url,first_seen_at,last_seen_at,price)
		VALUES(?,?,?,100,200,?)`, itemID, title, "https://example.invalid/item/"+itemID, price)
}

func queryFinding(t *testing.T, db *sql.DB, taskID int64, itemID string, decision analysis.Decision, grade *analysis.Grade, score *int64, foundAt int64) int64 {
	t.Helper()
	stage := StageDone
	if decision == DecisionPending || decision == DecisionFailed {
		stage = StageDetail
	}
	result := queryExec(t, db, `INSERT INTO findings(task_id,item_id,search_generation,found_at,is_baseline,decision,stage,is_read,grade,score)
		VALUES(?,?,1,?,0,?,?,0,?,?)`, taskID, itemID, foundAt, decision, stage, grade, score)
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func findingIDs(items []FindingView) []int64 {
	ids := make([]int64, len(items))
	for i := range items {
		ids[i] = items[i].ID
	}
	return ids
}

func assertFindingSet(t *testing.T, page Page[FindingView], expected ...int64) {
	t.Helper()
	got := findingIDs(page.Items)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	sort.Slice(expected, func(i, j int) bool { return expected[i] < expected[j] })
	if !reflect.DeepEqual(got, expected) || page.Total != int64(len(expected)) {
		t.Fatalf("结果集合错误：got=%v total=%d want=%v", got, page.Total, expected)
	}
}

func TestFindingViewsSeparateDefaultOtherAndHidden(t *testing.T) {
	s, db := queryFixture(t)
	taskID := queryTask(t, db, queryPtr(int64(10000)))
	ids := map[string]int64{}
	for index, test := range []struct {
		name     string
		decision analysis.Decision
		grade    *analysis.Grade
		score    *int64
		price    int64
	}{
		{"steal", analysis.DecisionScored, queryPtr(analysis.GradeSteal), queryPtr(int64(91)), 8000},
		{"good", analysis.DecisionScored, queryPtr(analysis.GradeGood), queryPtr(int64(83)), 8000},
		{"fair", analysis.DecisionScored, queryPtr(analysis.GradeFair), queryPtr(int64(70)), 8000},
		{"unknown", analysis.DecisionScored, nil, nil, 8000},
		{"avoid", analysis.DecisionScored, queryPtr(analysis.GradeAvoid), nil, 8000},
		{"pricey", analysis.DecisionScored, queryPtr(analysis.GradePricey), queryPtr(int64(55)), 8000},
		{"missing-grade", analysis.DecisionScored, nil, queryPtr(int64(55)), 8000},
		{"over", analysis.DecisionScored, queryPtr(analysis.GradeSteal), queryPtr(int64(95)), 12000},
		{"dismissed", analysis.DecisionScored, queryPtr(analysis.GradeSteal), queryPtr(int64(99)), 8000},
		{"blocked", analysis.DecisionScored, queryPtr(analysis.GradeSteal), queryPtr(int64(98)), 8000},
		{"pending", DecisionPending, queryPtr(analysis.GradeSteal), queryPtr(int64(97)), 8000},
		{"failed", DecisionFailed, queryPtr(analysis.GradeSteal), queryPtr(int64(96)), 8000},
		{"matched", analysis.DecisionMatched, nil, nil, 8000},
		{"rejected", analysis.DecisionRejected, nil, nil, 8000},
		{"skipped", DecisionSkipped, nil, nil, 8000},
	} {
		queryListing(t, db, test.name, test.name, &test.price)
		ids[test.name] = queryFinding(t, db, taskID, test.name, test.decision, test.grade, test.score, int64(index+100))
	}
	queryExec(t, db, `UPDATE listings SET is_dismissed=1 WHERE item_id='dismissed'`)
	queryExec(t, db, `UPDATE listings SET seller_id='blocked-seller' WHERE item_id='blocked'`)
	queryExec(t, db, `INSERT INTO blocked_sellers(seller_id) VALUES('blocked-seller')`)
	queryExec(t, db, `UPDATE listings SET last_drop_amount=500,last_drop_at=1000 WHERE item_id='good'`)

	for _, test := range []struct {
		q    FindingQuery
		want []int64
	}{
		{FindingQuery{}, []int64{ids["steal"], ids["good"], ids["fair"], ids["unknown"], ids["matched"]}},
		{FindingQuery{View: "other"}, []int64{ids["avoid"], ids["pricey"], ids["missing-grade"], ids["over"], ids["rejected"], ids["skipped"]}},
		{FindingQuery{View: "hidden"}, []int64{ids["dismissed"], ids["blocked"]}},
		{FindingQuery{View: "all", Decision: queryPtr(DecisionFailed)}, []int64{ids["failed"]}},
		{FindingQuery{View: "all", OverBudget: queryPtr(true)}, []int64{ids["over"]}},
		{FindingQuery{PriceDrop: queryPtr(true)}, []int64{ids["good"]}},
	} {
		page, err := s.ListFindings(t.Context(), test.q)
		if err != nil {
			t.Fatal(err)
		}
		assertFindingSet(t, page, test.want...)
	}
	// 改任务模式不会把历史的已算分或已命中结论藏掉。
	queryExec(t, db, `UPDATE tasks SET judge_mode='keyword',profile_json='{}' WHERE id=?`, taskID)
	page, err := s.ListFindings(t.Context(), FindingQuery{})
	if err != nil {
		t.Fatal(err)
	}
	assertFindingSet(t, page, ids["steal"], ids["good"], ids["fair"], ids["unknown"], ids["matched"])
}

func TestFindingBudgetDisplayDoesNotPretendUnknownPricesCanBeRecommended(t *testing.T) {
	s, db := queryFixture(t)
	limited := queryTask(t, db, queryPtr(int64(10000)))
	unlimited := queryTask(t, db, nil)
	ids := map[string]int64{}
	for index, test := range []struct {
		name  string
		task  int64
		price *int64
	}{
		{"unknown-limited", limited, nil},
		{"under", limited, queryPtr(int64(9000))},
		{"equal", limited, queryPtr(int64(10000))},
		{"over", limited, queryPtr(int64(11000))},
		{"unknown-unlimited", unlimited, nil},
	} {
		queryListing(t, db, test.name, test.name, test.price)
		ids[test.name] = queryFinding(t, db, test.task, test.name, analysis.DecisionScored, nil, nil, int64(index+1))
	}
	page, err := s.ListFindings(t.Context(), FindingQuery{})
	if err != nil {
		t.Fatal(err)
	}
	assertFindingSet(t, page, ids["unknown-limited"], ids["under"], ids["equal"], ids["unknown-unlimited"])
	page, err = s.ListFindings(t.Context(), FindingQuery{View: "all", OverBudget: queryPtr(true)})
	if err != nil {
		t.Fatal(err)
	}
	assertFindingSet(t, page, ids["over"])
	page, err = s.ListFindings(t.Context(), FindingQuery{View: "all", OverBudget: queryPtr(false)})
	if err != nil {
		t.Fatal(err)
	}
	assertFindingSet(t, page, ids["unknown-limited"], ids["under"], ids["equal"], ids["unknown-unlimited"])
	rows, err := db.QueryContext(t.Context(), "SELECT f.id"+findingFromSQL+"WHERE "+findingRecommendationBudgetSQL+" ORDER BY f.id")
	if err != nil {
		t.Fatal(err)
	}
	qualified := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		qualified = append(qualified, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(qualified, []int64{ids["under"], ids["equal"], ids["unknown-unlimited"]}) {
		t.Fatalf("推荐预算门禁放过了有上限的未知价格：%v", qualified)
	}
}

func TestFindingPagingUsesEffectiveScoreAndStableAnchors(t *testing.T) {
	s, db := queryFixture(t)
	taskID := queryTask(t, db, nil)
	ids := []int64{}
	for i := 0; i < 5; i++ {
		itemID := fmt.Sprintf("same-%d", i)
		queryListing(t, db, itemID, itemID, queryPtr(int64(1000)))
		ids = append(ids, queryFinding(t, db, taskID, itemID, analysis.DecisionScored, queryPtr(analysis.GradeGood), queryPtr(int64(85)), 1000))
	}
	queryListing(t, db, "pending", "pending", nil)
	pendingID := queryFinding(t, db, taskID, "pending", DecisionPending, queryPtr(analysis.GradeSteal), queryPtr(int64(99)), 5000)
	queryExec(t, db, `UPDATE findings SET score_breakdown_json='{}' WHERE id=?`, pendingID)
	queryListing(t, db, "unknown", "unknown", nil)
	unknownID := queryFinding(t, db, taskID, "unknown", analysis.DecisionScored, nil, nil, 2000)
	q := FindingQuery{View: "all", ListQuery: ListQuery{Limit: 2}}
	seen := []int64{}
	var firstCursor string
	for {
		page, err := s.ListFindings(t.Context(), q)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 7 {
			t.Fatalf("页内total不是完整筛选数：%d", page.Total)
		}
		for _, view := range page.Items {
			if view.ID == pendingID && (view.Score != nil || view.Grade != nil || view.ScoreBreakdown != nil) {
				t.Fatal("待重新分析仍公开了旧分数")
			}
		}
		seen = append(seen, findingIDs(page.Items)...)
		if page.NextCursor == nil {
			break
		}
		q.Cursor = *page.NextCursor
		if firstCursor == "" {
			firstCursor = q.Cursor
			q.Limit = 3
		}
	}
	want := []int64{ids[4], ids[3], ids[2], ids[1], ids[0], pendingID, unknownID}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("同分或空分数翻页重复/漏项：got=%v want=%v", seen, want)
	}
	_, err := s.ListFindings(t.Context(), FindingQuery{View: "hidden", ListQuery: ListQuery{Cursor: firstCursor}})
	assertQueryError(t, err)
	_, err = s.ListFindings(t.Context(), FindingQuery{View: "all", Sort: "latest", ListQuery: ListQuery{Cursor: firstCursor}})
	assertQueryError(t, err)
	page, err := s.ListFindings(t.Context(), FindingQuery{View: "all", Sort: "latest"})
	if err != nil || !reflect.DeepEqual(findingIDs(page.Items), []int64{pendingID, unknownID, ids[4], ids[3], ids[2], ids[1], ids[0]}) {
		t.Fatalf("最新发现排序错误：%v %v", findingIDs(page.Items), err)
	}
}

func TestFindingNeighborsKeepTheRequestedContextWhenCurrentNoLongerMatches(t *testing.T) {
	s, db := queryFixture(t)
	taskID := queryTask(t, db, nil)
	ids := []int64{}
	for i, score := range []int64{90, 80, 70} {
		itemID := fmt.Sprintf("neighbor-%d", i)
		queryListing(t, db, itemID, itemID, nil)
		ids = append(ids, queryFinding(t, db, taskID, itemID, analysis.DecisionScored, queryPtr(analysis.GradeGood), &score, int64(100+i)))
	}
	otherTask := queryTask(t, db, nil)
	queryListing(t, db, "other-task", "other-task", nil)
	otherID := queryFinding(t, db, otherTask, "other-task", analysis.DecisionScored, queryPtr(analysis.GradeGood), queryPtr(int64(85)), 500)
	view, err := s.GetFinding(t.Context(), ids[1], FindingQuery{})
	if err != nil || view.PreviousID == nil || *view.PreviousID != ids[0] || view.NextID == nil || *view.NextID != ids[2] {
		t.Fatalf("缺省详情没有限定所属任务：%+v %v", view, err)
	}
	if view.IsRead {
		t.Fatal("只读详情不能偷偷修改已读状态")
	}
	view, err = s.GetFinding(t.Context(), ids[1], FindingQuery{Q: "  "})
	if err != nil || view.PreviousID == nil || *view.PreviousID != ids[0] {
		t.Fatalf("空白搜索词把缺省详情改成全任务上下文：%+v %v", view, err)
	}
	view, err = s.GetFinding(t.Context(), ids[1], FindingQuery{View: "default"})
	if err != nil || view.PreviousID == nil || *view.PreviousID != otherID {
		t.Fatalf("显式全任务上下文被改成了所属任务：%+v %v", view, err)
	}
	queryExec(t, db, `UPDATE findings SET decision='rejected',grade=NULL,score=60 WHERE id=?`, ids[1])
	view, err = s.GetFinding(t.Context(), ids[1], FindingQuery{TaskID: &taskID, View: "default"})
	if err != nil || view.PreviousID == nil || *view.PreviousID != ids[2] || view.NextID != nil {
		t.Fatalf("当前记录不再符合筛选时放宽了上下文：%+v %v", view, err)
	}
}

func TestFindingSearchIsLiteralAndFailuresDoNotReturnPartialPages(t *testing.T) {
	s, db := queryFixture(t)
	taskID := queryTask(t, db, nil)
	for _, name := range []string{"camera 50%_off", "camera 50XXoff"} {
		queryListing(t, db, name, name, nil)
		queryFinding(t, db, taskID, name, analysis.DecisionMatched, nil, nil, 1)
	}
	page, err := s.ListFindings(t.Context(), FindingQuery{Q: "%_"})
	if err != nil || page.Total != 1 || page.Items[0].Listing.Title != "camera 50%_off" {
		t.Fatalf("百分号或下划线变成了通配符：%+v %v", page, err)
	}
	for _, q := range []FindingQuery{{View: "unknown"}, {Sort: "random"}, {TaskID: queryPtr(int64(0))}, {Decision: queryPtr(analysis.Decision("running"))}, {Q: string([]byte{0xff})}} {
		_, err := s.ListFindings(t.Context(), q)
		assertQueryError(t, err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = s.ListFindings(ctx, FindingQuery{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("单连接等待取消没有向上传递：%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestFindingViewUsesCurrentVersionQuestionsAndLatestChannelResults(t *testing.T) {
	s, db := queryFixture(t)
	taskID := queryTask(t, db, nil)
	profile := analysis.Profile{NextNo: 2, Risks: []analysis.ProfileRisk{{ID: "c1", Name: "屏幕", Attitude: analysis.Confirm, Source: analysis.SourceUser, Rev: 2}}}
	raw, _ := json.Marshal(profile)
	queryExec(t, db, `UPDATE tasks SET profile_json=? WHERE id=?`, string(raw), taskID)
	queryListing(t, db, "versioned", "camera", nil)
	id := queryFinding(t, db, taskID, "versioned", analysis.DecisionScored, queryPtr(analysis.GradeGood), queryPtr(int64(88)), 1)
	facts := analysis.Facts{Checks: []analysis.CheckFact{{Key: "c1", Status: analysis.CheckOK, Rev: 1}}}
	raw, _ = json.Marshal(facts)
	queryExec(t, db, `UPDATE findings SET analysis_json=? WHERE id=?`, string(raw), id)
	channelResult := queryExec(t, db, `INSERT INTO channels(uuid,type,name,config_json) VALUES('query-channel','bark','Bark','{}')`)
	channelID, _ := channelResult.LastInsertId()
	for index, ok := range []bool{false, true} {
		result := queryExec(t, db, `INSERT INTO deliveries(at,channel_id,task_id,kind,item_count,ok) VALUES(?,?,?,'recommended',1,?)`, index+10, channelID, taskID, ok)
		deliveryID, _ := result.LastInsertId()
		queryExec(t, db, `INSERT INTO delivery_items(delivery_id,finding_id) VALUES(?,?)`, deliveryID, id)
	}
	view, err := s.GetFinding(t.Context(), id, FindingQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Questions) != 1 || view.Questions[0].Key != "c1" {
		t.Fatalf("旧版本ok事实遮住新检查问题：%+v", view.Questions)
	}
	if len(view.Checks) != 1 || view.Checks[0].Status != analysis.CheckUnknown || view.Checks[0].Source != analysis.EvidenceUnknown {
		t.Fatalf("接口没有给出版本失效后的有效结论：%+v", view.Checks)
	}
	if view.Score == nil || *view.Score != 88 {
		t.Fatal("构建联系问题时覆盖了持久分数")
	}
	if len(view.Deliveries) != 1 || view.Deliveries[0].OK == nil || !*view.Deliveries[0].OK || view.Deliveries[0].ChannelName == nil || *view.Deliveries[0].ChannelName != "Bark" {
		t.Fatalf("同渠道没有返回最近一次结果：%+v", view.Deliveries)
	}
	var savedScore int64
	var read bool
	if err := db.QueryRowContext(t.Context(), `SELECT score,is_read FROM findings WHERE id=?`, id).Scan(&savedScore, &read); err != nil || savedScore != 88 || read {
		t.Fatalf("详情查询写入了业务状态：%d %v %v", savedScore, read, err)
	}
	for _, rev := range []int64{2, 1} {
		overrides, _ := json.Marshal(analysis.UserOverrides{Risks: map[string]analysis.RiskOverride{"c1": {Status: analysis.CheckOK, Rev: rev}}})
		queryExec(t, db, `UPDATE findings SET user_overrides_json=? WHERE id=?`, string(overrides), id)
		view, err := s.GetFinding(t.Context(), id, FindingQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if rev == 2 && (view.Checks[0].Status != analysis.CheckOK || view.Checks[0].Source != analysis.EvidenceUser || len(view.Questions) != 0) {
			t.Fatalf("当前版本的手动覆盖没有成为有效结论：%+v", view)
		}
		if rev == 1 && (view.Checks[0].Status != analysis.CheckUnknown || len(view.Questions) != 1) {
			t.Fatalf("旧版本覆盖仍生效：%+v", view)
		}
		if view.Analysis.Checks[0].Rev != 1 || view.Analysis.Checks[0].Status != analysis.CheckOK || *view.Score != 88 {
			t.Fatal("只读有效结论覆盖了原事实或上次分数")
		}
	}
}

func queryMarketFixture(t *testing.T, profile analysis.Profile) (*Service, *sql.DB, int64, time.Time) {
	t.Helper()
	s, db := queryFixture(t)
	now := time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC)
	preferences, err := appsettings.New(t.Context(), db, settings.Environment{TZ: "Asia/Singapore"}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s.settings, s.now = preferences, func() time.Time { return now.Add(-48 * time.Hour) }
	taskID := queryTask(t, db, nil)
	raw, _ := json.Marshal(profile)
	queryExec(t, db, `UPDATE tasks SET profile_json=? WHERE id=?`, string(raw), taskID)
	return s, db, taskID, now
}

func queryItemSpecs(t *testing.T, db *sql.DB, taskID int64, itemID string, profile analysis.Profile, values []analysis.SpecValue) {
	t.Helper()
	resolved := analysis.ResolveSpecs(profile, values, nil, analysis.SpecDetail)
	raw, _ := json.Marshal(values)
	queryExec(t, db, `INSERT INTO task_item_specs(task_id,item_id,kind,specs_json,config_key,source,updated_at)
		VALUES(?,?,'sale',?,?,'detail',1)`, taskID, itemID, string(raw), resolved.ConfigKey)
}

func queryMarketSample(t *testing.T, db *sql.DB, taskID int64, itemID string, profile analysis.Profile, values []analysis.SpecValue, price int64, now time.Time) {
	t.Helper()
	queryListing(t, db, itemID, itemID, &price)
	queryItemSpecs(t, db, taskID, itemID, profile, values)
	queryExec(t, db, `INSERT INTO task_sightings(task_id,item_id,day,price,last_seen_at) VALUES(?,?,'2026-11-12',?,?)`, taskID, itemID, price, now.UnixMilli())
}

func TestFindingMarketComparisonIsCurrentAndKeepsScoringSnapshot(t *testing.T) {
	profile := analysis.Profile{Specs: []analysis.ProfileSpec{{Name: "存储", Options: []string{"128G", "256G"}, Values: []string{"128G", "256G"}}},
		Musts: []analysis.Requirement{{ID: "m1", Text: "国行", Source: analysis.SourceUser, Rev: 1}}, AllowedKinds: []analysis.Kind{analysis.KindSale}, NextNo: 2}
	s, db, taskID, now := queryMarketFixture(t, profile)
	upper := []analysis.SpecValue{{Name: "存储", Value: queryPtr("256G")}}
	lower := []analysis.SpecValue{{Name: "存储", Value: queryPtr("128G")}}
	queryListing(t, db, "target", "target", queryPtr(int64(12000)))
	queryItemSpecs(t, db, taskID, "target", profile, upper)
	id := queryFinding(t, db, taskID, "target", analysis.DecisionScored, queryPtr(analysis.GradeGood), queryPtr(int64(88)), 1)
	queryExec(t, db, `UPDATE findings SET fair_price=99999,fair_sample=99 WHERE id=?`, id)
	for index := 0; index < 5; index++ {
		queryMarketSample(t, db, taskID, fmt.Sprintf("upper-%d", index), profile, upper, int64(13000+index*100), now)
		queryMarketSample(t, db, taskID, fmt.Sprintf("lower-%d", index), profile, lower, int64(9000+index*100), now)
	}
	var clockBefore string
	if err := db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&clockBefore); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetFinding(t.Context(), id, FindingQuery{})
	if err != nil {
		t.Fatal(err)
	}
	quote := view.MarketComparison
	if quote == nil || quote.AsOf != now.UnixMilli() || quote.ConfigKey != "256G" || quote.Fair.Cents == nil || *quote.Fair.Cents != 13200 || quote.Fair.Sample != 5 {
		t.Fatalf("没有用权威时间和有效时区读取当前行情：%+v", quote)
	}
	if len(quote.AdjacentPrices) != 1 || quote.AdjacentPrices[0].ItemPremium != 2800 || quote.AdjacentPrices[0].TypicalPremium != 4000 {
		t.Fatalf("相邻配置差价没有复用当前行情：%+v", quote.AdjacentPrices)
	}
	if *view.FairPrice != 99999 || *view.FairSample != 99 || *view.Score != 88 {
		t.Fatal("当前行情覆盖了上次打分依据")
	}
	var clockAfter string
	if err := db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&clockAfter); err != nil || clockAfter != clockBefore {
		t.Fatalf("只读详情修改了保护时钟：%v", err)
	}
	page, err := s.ListFindings(t.Context(), FindingQuery{})
	if err != nil || page.Items[0].MarketComparison != nil {
		t.Fatalf("列表给每张卡片重算了行情：%+v %v", page, err)
	}
	prices, err := s.ListMarketSamples(t.Context(), taskID, MarketSampleQuery{ConfigKey: quote.ConfigKey, BrandNew: &quote.BrandNew})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, sample := range prices.Items {
		ids = append(ids, sample.ItemID)
	}
	if !reflect.DeepEqual(ids, quote.Fair.ItemIDs) {
		t.Fatalf("比价表样本与当前fair实际池不同：%v %v", ids, quote.Fair.ItemIDs)
	}
}

func TestFindingMarketComparisonUsesTheActualRelaxedSamplePool(t *testing.T) {
	profile := analysis.Profile{Specs: []analysis.ProfileSpec{
		{Name: "型号", Options: []string{"A"}, Values: []string{"A"}},
		{Name: "存储", Options: []string{"128G", "256G"}, Values: []string{"256G"}},
	}, Musts: []analysis.Requirement{{ID: "m1", Text: "国行", Source: analysis.SourceUser, Rev: 1}}, AllowedKinds: []analysis.Kind{analysis.KindSale}, NextNo: 2}
	s, db, taskID, now := queryMarketFixture(t, profile)
	upper := []analysis.SpecValue{{Name: "型号", Value: queryPtr("A")}, {Name: "存储", Value: queryPtr("256G")}}
	lower := []analysis.SpecValue{{Name: "型号", Value: queryPtr("A")}, {Name: "存储", Value: queryPtr("128G")}}
	queryListing(t, db, "target", "target", queryPtr(int64(11000)))
	queryItemSpecs(t, db, taskID, "target", profile, upper)
	id := queryFinding(t, db, taskID, "target", analysis.DecisionScored, nil, nil, 1)
	for index := 0; index < 5; index++ {
		values := upper
		if index >= 3 {
			values = lower
		}
		queryMarketSample(t, db, taskID, fmt.Sprintf("pool-%d", index), profile, values, int64(10000+index*100), now)
	}
	view, err := s.GetFinding(t.Context(), id, FindingQuery{})
	if err != nil {
		t.Fatal(err)
	}
	quote := view.MarketComparison
	if quote == nil || quote.Fair.Days != 30 || quote.Fair.DroppedSpec == nil || *quote.Fair.DroppedSpec != "存储" || len(quote.Fair.ItemIDs) != 5 {
		t.Fatalf("没有保留实际放宽池：%+v", quote)
	}
	page, err := s.ListMarketSamples(t.Context(), taskID, MarketSampleQuery{ConfigKey: quote.ConfigKey, BrandNew: &quote.BrandNew})
	if err != nil || page.Total != 5 {
		t.Fatalf("样本分页丢掉了放宽配置的参与者：%+v %v", page, err)
	}
}

func TestFindingMarketComparisonHandlesNoDimensionsAndInsufficientOrUnknownSpecs(t *testing.T) {
	profile := analysis.Profile{Musts: []analysis.Requirement{{ID: "m1", Text: "国行", Source: analysis.SourceUser, Rev: 1}}, AllowedKinds: []analysis.Kind{analysis.KindSale}, NextNo: 2}
	s, db, taskID, now := queryMarketFixture(t, profile)
	queryMarketSample(t, db, taskID, "only", profile, []analysis.SpecValue{}, 10000, now)
	id := queryFinding(t, db, taskID, "only", analysis.DecisionScored, nil, nil, 1)
	view, err := s.GetFinding(t.Context(), id, FindingQuery{})
	if err != nil || view.MarketComparison == nil || view.MarketComparison.ConfigKey != "*" || view.MarketComparison.Fair.Cents != nil || view.MarketComparison.Fair.Sample != 1 || view.MarketComparison.AdjacentPrices == nil {
		t.Fatalf("无规格或样本不足被当成错误/捏造合理价：%+v %v", view.MarketComparison, err)
	}
	profile.Specs = []analysis.ProfileSpec{{Name: "存储", Options: []string{"128G"}, Values: []string{"128G"}}}
	raw, _ := json.Marshal(profile)
	queryExec(t, db, `UPDATE tasks SET profile_json=? WHERE id=?`, string(raw), taskID)
	view, err = s.GetFinding(t.Context(), id, FindingQuery{})
	if err != nil || view.MarketComparison != nil {
		t.Fatalf("未知配置冒充了已知配置行情：%+v %v", view.MarketComparison, err)
	}
}

func TestFindingEffectiveChecksKeepSavedSellerAssessmentAfterCacheExpires(t *testing.T) {
	s, db := queryFixture(t)
	taskID := queryTask(t, db, nil)
	profile := analysis.Profile{NextNo: 1, Risks: []analysis.ProfileRisk{{ID: "seller_dealer", Attitude: analysis.Reject, Source: analysis.SourceSystem, Rev: 1}}}
	raw, _ := json.Marshal(profile)
	queryExec(t, db, `UPDATE tasks SET profile_json=? WHERE id=?`, string(raw), taskID)
	queryListing(t, db, "saved-seller", "camera", nil)
	queryExec(t, db, `UPDATE listings SET seller_id='seller' WHERE item_id='saved-seller'`)
	id := queryFinding(t, db, taskID, "saved-seller", analysis.DecisionScored, queryPtr(analysis.GradeAvoid), queryPtr(int64(39)), 1)
	assessment := analysis.SellerResult{Type: analysis.SellerDealerSuspect,
		Checks: []analysis.EffectiveCheck{
			{Key: "seller_dealer", Status: analysis.CheckRisk, Source: analysis.EvidenceProgram},
			{Key: "seller_new", Status: analysis.CheckUnknown, Source: analysis.EvidenceUnknown},
			{Key: "seller_bad_reviews", Status: analysis.CheckUnknown, Source: analysis.EvidenceUnknown},
			{Key: "seller_partial_refund", Status: analysis.CheckUnknown, Source: analysis.EvidenceUnknown},
		}, Reasons: []locales.Message{{Code: "analysis.seller_dealer_items", Args: map[string]any{"count": 3}}}}
	raw, _ = json.Marshal(assessment)
	queryExec(t, db, `UPDATE findings SET analysis_json='{}',seller_checks_json=? WHERE id=?`, string(raw), id)
	queryExec(t, db, `INSERT INTO seller_profiles(seller_id,recent_items_json,recent_rates_json,fetched_at) VALUES('seller','[]','[]',1)`)
	for _, clearCache := range []bool{false, true} {
		if clearCache {
			queryExec(t, db, `DELETE FROM seller_profiles WHERE seller_id='seller'`)
		}
		view, err := s.GetFinding(t.Context(), id, FindingQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if view.SellerAssessment == nil || view.SellerAssessment.Type != analysis.SellerDealerSuspect || len(view.Checks) != 1 || view.Checks[0].Status != analysis.CheckRisk || view.Checks[0].Source != analysis.EvidenceProgram {
			t.Fatalf("原始缓存覆盖或抹掉已保存的卖家结论：clear=%v view=%+v", clearCache, view)
		}
		if *view.Score != 39 || *view.Grade != analysis.GradeAvoid {
			t.Fatal("只读检查改写了历史分数或档位")
		}
	}
	var saved string
	if err := db.QueryRowContext(t.Context(), `SELECT seller_checks_json FROM findings WHERE id=?`, id).Scan(&saved); err != nil || saved != string(raw) {
		t.Fatalf("GET改写了持久画像：%s %v", saved, err)
	}
}
