package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func overviewTime(t *testing.T, timezone, text string) time.Time {
	t.Helper()
	location, err := time.LoadLocation(timezone)
	if err != nil {
		t.Fatal(err)
	}
	value, err := time.ParseInLocation("2006-01-02 15:04:05", text, location)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestOverviewCivilWindowsAndRepeatedHours(t *testing.T) {
	for _, test := range []struct {
		timezone, at string
		buckets      int
		first        string
	}{
		{"UTC", "2026-11-11 12:00:00", 24, "2026-11-11 00:00:00"},
		{"America/New_York", "2026-03-08 12:00:00", 23, "2026-03-08 00:00:00"},
		{"America/New_York", "2026-11-01 12:00:00", 25, "2026-11-01 00:00:00"},
		{"America/Santiago", "2026-09-06 12:00:00", 23, "2026-09-06 01:00:00"},
		{"America/Havana", "2025-11-02 12:00:00", 25, "2025-11-02 00:00:00"},
	} {
		t.Run(test.timezone+test.at, func(t *testing.T) {
			now := overviewTime(t, test.timezone, test.at)
			window := overviewPeriod("today", now)
			if len(window.activity) != test.buckets || window.from.Format("2006-01-02 15:04:05") != test.first {
				t.Fatalf("真实日界/小时数错误：%s %d", window.from, len(window.activity))
			}
			for i, bucket := range window.activity {
				if bucket.ToAt-bucket.FromAt != time.Hour.Milliseconds() {
					t.Fatal("小时不是实际UTC间隔", bucket)
				}
				if i > 0 && bucket.FromAt != window.activity[i-1].ToAt {
					t.Fatal("小时区间缺口或重叠")
				}
			}
			_, end := quota.DayBounds(now)
			if window.activity[0].FromAt != window.from.UnixMilli() || window.activity[len(window.activity)-1].ToAt != end.UnixMilli() {
				t.Fatal("小时桶没有覆盖完整民用日")
			}
		})
	}
	fall := overviewPeriod("today", overviewTime(t, "America/New_York", "2026-11-01 12:00:00"))
	location, _ := time.LoadLocation("America/New_York")
	if time.UnixMilli(fall.activity[1].FromAt).In(location).Hour() != 1 || time.UnixMilli(fall.activity[2].FromAt).In(location).Hour() != 1 || fall.activity[1].FromAt == fall.activity[2].FromAt {
		t.Fatal("重复小时被合并")
	}
}

func TestOverviewCalendarComparisonsDoNotSubtractTwentyFourHours(t *testing.T) {
	for _, test := range []struct{ at, want string }{
		{"2026-03-09 02:30:00", "2026-03-08T03:00:00-04:00"},
		{"2026-11-02 01:30:00", "2026-11-01T01:30:00-04:00"},
	} {
		window := overviewPeriod("today", overviewTime(t, "America/New_York", test.at))
		if got := window.previousTo.Format(time.RFC3339); got != test.want {
			t.Fatalf("昨日同钟点：%s，期望%s", got, test.want)
		}
	}
	now := overviewTime(t, "Asia/Singapore", "2026-06-10 12:30:00")
	week := overviewPeriod("7d", now)
	if len(week.activity) != 7 || week.from.Format(time.DateOnly) != "2026-06-04" || week.previousFrom.Format(time.DateOnly) != "2026-05-28" || week.previousTo.Format("2006-01-02 15:04") != "2026-06-03 12:30" {
		t.Fatalf("近七日/比较窗口错误：%+v", week)
	}
	month := overviewPeriod("30d", now)
	if len(month.activity) != 30 || !month.previousFrom.IsZero() || !month.previousTo.IsZero() {
		t.Fatal("近三十日不应产生比较")
	}
	apia, _ := time.LoadLocation("Pacific/Apia")
	missing := time.Date(2011, 12, 30, 0, 0, 0, 0, time.UTC)
	if got := overviewDateStart(missing, apia); got.Format(time.DateOnly) != "2011-12-31" {
		t.Fatal("不存在的民用日没有移到下一真实日界", got)
	}
	if got := overviewPeriod("today", overviewTime(t, "America/New_York", "2040-12-31 12:00:00")); len(got.activity) != 24 {
		t.Fatal("未来闰年末日界不前进")
	}
}

func TestOverviewComparisonThresholdAndNoSamples(t *testing.T) {
	for _, test := range []struct {
		current, previous, delta int64
		ratio                    *float64
	}{
		{7, 0, 7, nil}, {40, 49, -9, nil}, {55, 50, 5, overviewFloat(.1)}, {0, 100, -100, overviewFloat(-1)},
	} {
		got := overviewMetric(test.current, test.previous)
		if got.Delta != test.delta || got.PreviousCount != test.previous || !reflect.DeepEqual(got.Ratio, test.ratio) {
			t.Fatalf("比较口径错误：%+v", got)
		}
	}
	item := overviewTask(Task{ID: 1, TaskFields: TaskFields{Name: "空任务", Enabled: true}}, TaskRuntime{}, map[int64]overviewAccount{}, nil, nil, 1, 2)
	if item.SuccessRate != nil || item.AverageDurationMS != nil || item.RecommendationRate != nil {
		t.Fatal("没有样本时比例/耗时不是未知")
	}
}

func overviewFloat(value float64) *float64 { return &value }

func overviewFixture(t *testing.T) (*Service, *sql.DB, time.Time) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "kakatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := overviewTime(t, "UTC", "2026-11-11 12:00:00")
	preferences, err := appsettings.New(t.Context(), db.DB(), settings.Environment{TZ: "UTC"}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{db: db.DB(), settings: preferences, now: func() time.Time { return now }}
	overviewExec(t, db.DB(), `INSERT INTO meta(key,value) VALUES('stats.started_at',?)`, now.AddDate(0, 0, -40).UnixMilli())
	return s, db.DB(), now
}

func overviewExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("写入测试事实失败：%s：%v", query, err)
	}
}

func overviewAddTask(t *testing.T, db *sql.DB, id int64, account *int64) {
	t.Helper()
	overviewExec(t, db, `INSERT INTO tasks(id,name,keyword,account_id,max_pages,interval_minutes,judge_mode,notify_policy,sort_index,budget_max) VALUES(?,?,?, ?,1,30,'keyword','recommended',?,10000)`, id, "任务", "相机", account, id)
}

func overviewAddFinding(t *testing.T, db *sql.DB, now time.Time, id, task int64, item, decision string, score any, grade any, baseline bool, reject any, layer any) {
	t.Helper()
	overviewExec(t, db, `INSERT OR IGNORE INTO listings(item_id,title,desktop_url,price,first_seen_at,last_seen_at) VALUES(?,?,?,5000,?,?)`, item, "正常商品", "https://example.invalid/item/"+item, now.Add(-time.Hour).UnixMilli(), now.Add(-time.Minute).UnixMilli())
	overviewExec(t, db, `INSERT INTO findings(id,task_id,item_id,search_generation,found_at,is_baseline,decision,stage,score,grade,is_read,analyzed_at,reject_kind,decided_layer) VALUES(?,?,?,1,?,?,?,'done',?,?,0,?,?,?)`, id, task, item, now.Add(-time.Hour).UnixMilli(), baseline, decision, score, grade, now.Add(-30*time.Minute).UnixMilli(), reject, layer)
}

func TestOverviewReadsCompleteSnapshotAndCountsIntentOnce(t *testing.T) {
	s, db, now := overviewFixture(t)
	overviewExec(t, db, `INSERT INTO accounts(id,name,state) VALUES(1,'可用','active'),(2,'失效','expired')`)
	a1, a2 := int64(1), int64(2)
	overviewAddTask(t, db, 1, &a1)
	overviewAddTask(t, db, 2, &a2)
	for _, finding := range []struct {
		id, task       int64
		item, decision string
		score, grade   any
		baseline       bool
		reject, layer  any
	}{
		{1, 1, "keyword", "matched", nil, nil, false, nil, 3},
		{2, 1, "good", "scored", 85, "good", false, nil, 3},
		{3, 1, "fair", "scored", 70, "fair", false, nil, 3},
		{4, 1, "unknown-price", "scored", nil, nil, false, nil, 3},
		{5, 1, "avoid", "scored", nil, "avoid", false, nil, 3},
		{6, 1, "over-budget", "scored", 85, "good", false, nil, 3},
		{7, 1, "dismissed", "scored", 95, "steal", false, nil, 3},
		{8, 1, "blocked", "scored", 85, "good", false, nil, 3},
		{9, 1, "baseline", "matched", nil, nil, true, nil, 3},
		{10, 2, "shared", "scored", 95, "steal", false, nil, 3},
		{11, 1, "shared", "scored", 70, "fair", false, nil, 3},
		{12, 1, "pending", "pending", 85, "good", false, nil, nil},
		{13, 1, "failed", "failed", 85, "good", false, nil, nil},
		{14, 1, "junk", "rejected", nil, nil, false, "junk", 2},
		{15, 1, "unmatched", "rejected", nil, nil, false, "unmatched", 3},
	} {
		overviewAddFinding(t, db, now, finding.id, finding.task, finding.item, finding.decision, finding.score, finding.grade, finding.baseline, finding.reject, finding.layer)
	}
	overviewExec(t, db, `UPDATE findings SET analyzed_at=? WHERE id=10`, now.Add(-time.Hour).UnixMilli())
	overviewExec(t, db, `UPDATE listings SET price=11000 WHERE item_id='over-budget'`)
	overviewExec(t, db, `UPDATE listings SET is_dismissed=1 WHERE item_id='dismissed'`)
	overviewExec(t, db, `UPDATE listings SET seller_id='bad-seller' WHERE item_id='blocked'`)
	overviewExec(t, db, `INSERT INTO blocked_sellers(seller_id) VALUES('bad-seller')`)
	for _, run := range []struct {
		task         any
		start        time.Time
		duration     *int64
		outcome      string
		failure      any
		scanned, new int64
	}{
		{1, now.Add(-2 * time.Hour), overviewInt(10000), "success", nil, 20, 8},
		{1, now.Add(-time.Hour), overviewInt(20000), "failed", "network", 10, 5},
		{1, now.Add(-30 * time.Minute), nil, "failed", "restart", 4, 2},
		{nil, now.Add(-3 * time.Hour), overviewInt(1000), "success", nil, 6, 1},
		{1, now.Add(-20 * time.Minute), overviewInt(10), "failed", "risk_verify", 0, 0},
		{1, now.AddDate(0, 0, -1).Add(-2 * time.Hour), overviewInt(1000), "success", nil, 49, 5},
		{1, now, overviewInt(1000), "success", nil, 999, 999},
	} {
		var ended any
		if run.duration != nil {
			ended = run.start.UnixMilli() + *run.duration
		}
		overviewExec(t, db, `INSERT INTO run_records(task_id,task_name,trigger,started_at,ended_at,outcome,failure_kind,scanned_count,new_count) VALUES(?,'保留的任务名','auto',?,?,?,?,?,?)`, run.task, run.start.UnixMilli(), ended, run.outcome, run.failure, run.scanned, run.new)
	}
	overviewExec(t, db, `INSERT INTO run_records(task_id,task_name,trigger,started_at,ended_at,outcome,repeat_count) VALUES(1,'任务','auto',?,?,'skipped',99)`, now.Add(-10*time.Minute).UnixMilli(), now.Add(-time.Minute).UnixMilli())
	for _, call := range []struct{ ok, duration int64 }{{1, 10}, {1, 20}, {0, 30}} {
		overviewExec(t, db, `INSERT INTO ai_calls(at,ok,input_tokens,output_tokens,reasoning_tokens,duration_ms) VALUES(?,?,10,20,20,?)`, now.Add(-time.Hour).UnixMilli(), call.ok, call.duration)
	}
	for i, ok := range []int{1, 1, 1, 0} {
		overviewExec(t, db, `INSERT INTO deliveries(id,at,kind,ok) VALUES(?,?,'summary',?)`, i+1, now.Add(-time.Hour).UnixMilli(), ok)
	}
	for _, intent := range []struct {
		id          int
		kind, state string
		sent        any
	}{{1, "price_drop", "sent", now.Add(-time.Hour).UnixMilli()}, {2, "price_drop", "sent", now.Add(-30 * time.Minute).UnixMilli()}, {3, "price_upgrade", "sent", now.Add(-time.Hour).UnixMilli()}, {4, "price_drop", "pending", nil}} {
		overviewExec(t, db, `INSERT INTO notification_intents(id,finding_id,task_id,kind,state,created_at,updated_at,sent_at) VALUES(?,1,1,?,?,?,?,?)`, intent.id, intent.kind, intent.state, now.Add(-time.Hour).UnixMilli(), now.Add(-time.Hour).UnixMilli(), intent.sent)
	}
	overviewExec(t, db, `INSERT INTO delivery_intents(delivery_id,intent_id) VALUES(1,1),(2,1),(3,2)`)
	overviewExec(t, db, `INSERT INTO system_events(kind,subject_id,message,created_at) VALUES('account_verify',1,'{"code":"run.account_needs_verification","args":{}}',?)`, now.Add(-time.Hour).UnixMilli())
	overviewExec(t, db, `INSERT INTO system_events(kind,subject_id,created_at,resolved_at) VALUES('account_login',2,?,?)`, now.Add(-time.Hour).UnixMilli(), now.Add(-time.Minute).UnixMilli())
	var clockBefore string
	if err := db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='quota.clock'`).Scan(&clockBefore); err != nil {
		t.Fatal(err)
	}
	overviewExec(t, db, `CREATE TRIGGER overview_clock_stays_read_only BEFORE UPDATE ON meta WHEN OLD.key IN ('quota.clock','quota.clock_initialized') BEGIN SELECT RAISE(ABORT,'概览不能写额度时钟'); END`)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	view, err := s.GetOverview(ctx, "today")
	if err != nil {
		t.Fatal(err)
	}
	if view.Cards != (OverviewCards{ScannedCount: 40, NewCount: 16, RecommendedCount: 5, RecommendedAICount: 3, RecommendedKeywordCount: 2, PriceDropCount: 2}) {
		t.Fatalf("统计不含全部事实或重复算投递：%+v", view.Cards)
	}
	if view.Comparison == nil || view.Comparison.Scanned.PreviousCount != 49 || view.Comparison.Scanned.Delta != -9 || view.Comparison.Scanned.Ratio != nil {
		t.Fatalf("昨日相同时刻比较不正确：%+v", view.Comparison)
	}
	var newTotal, seenTotal int64
	for _, bucket := range view.Activity {
		newTotal += bucket.NewCount
		seenTotal += bucket.SeenCount
	}
	if newTotal != view.Cards.NewCount || newTotal+seenTotal != view.Cards.ScannedCount {
		t.Fatal("活动柱和统计卡不相等")
	}
	if len(view.Tasks) != 2 || view.Tasks[0].NewFindingCount != 13 || view.Tasks[0].RecommendedCount != 3 || view.Tasks[0].RecommendationRate == nil || math.Abs(*view.Tasks[0].RecommendationRate-3.0/13) > 1e-12 {
		t.Fatalf("推荐率混入基线或扫描分母：%+v", view.Tasks)
	}
	if view.Tasks[0].SuccessRate == nil || math.Abs(*view.Tasks[0].SuccessRate-1.0/3) > 1e-12 {
		t.Fatal("未结束占位或重复跳过进入成功率", view.Tasks[0])
	}
	if view.Tasks[0].AverageDurationMS == nil || math.Abs(*view.Tasks[0].AverageDurationMS-30010.0/3) > 1e-9 {
		t.Fatal("未结束或跳过进入平均耗时", view.Tasks[0])
	}
	if view.Tasks[1].State != OverviewPaused || view.Tasks[1].NextRunAt != nil {
		t.Fatal("失效账号不能显示成监控中", view.Tasks[1])
	}
	if view.Tasks[0].MinPrice != nil || len(view.Tasks[0].Trend) != 7 {
		t.Fatal("没有最新接纳搜索时不能从最后见过时间猜最低价")
	}
	if view.Accounts != (AccountOverview{Total: 2, Active: 1}) || view.AI.Count != 3 || view.AI.Tokens != 90 || view.AI.AverageDurationMS == nil || *view.AI.AverageDurationMS != 20 || view.AI.SuccessRate == nil || math.Abs(*view.AI.SuccessRate-2.0/3) > 1e-12 {
		t.Fatalf("账号/模型小卡错误：%+v %+v", view.Accounts, view.AI)
	}
	if view.Notifications.Count != 4 || view.Notifications.SuccessRate == nil || *view.Notifications.SuccessRate != .75 {
		t.Fatal("投递小卡错误", view.Notifications)
	}
	if view.Exceptions.RiskFailureCount != 1 || view.Exceptions.NetworkFailureCount != 1 || len(view.Exceptions.UnresolvedEvents) != 1 {
		t.Fatal("异常小卡丢失或纳入已解决事件", view.Exceptions)
	}
	grades := map[string]int64{}
	for _, grade := range view.GradeDistribution {
		grades[grade.Kind] = grade.Count
	}
	if !reflect.DeepEqual(grades, map[string]int64{"steal": 1, "good": 3, "fair": 2, "pricey": 0, "avoid": 1, "unpriced": 1, "unmatched": 1, "junk": 1}) {
		t.Fatalf("档位不是最近有效分析去重：%v", grades)
	}
	var clockAfter string
	if err := db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='quota.clock'`).Scan(&clockAfter); err != nil {
		t.Fatal(err)
	}
	if clockBefore != clockAfter {
		t.Fatal("只读概览写了额度时钟")
	}
}

func overviewInt(value int64) *int64 { return &value }

func TestOverviewCoverageDoesNotReturnAfterRetentionIncreases(t *testing.T) {
	s, db, now := overviewFixture(t)
	cutoff := now.AddDate(0, 0, -7).UnixMilli()
	overviewExec(t, db, `INSERT INTO meta(key,value) VALUES('stats.coverage.findings',?)`, cutoff)
	if _, err := s.settings.Apply(t.Context(), settings.Values{"retention.findings_days": json.RawMessage(`30`)}); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetOverview(t.Context(), "30d")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Coverage.IsPartial || view.Coverage.FromAt == nil || *view.Coverage.FromAt != cutoff || view.Comparison != nil {
		t.Fatalf("调大保留期伪造了历史：%+v", view.Coverage)
	}
	overviewExec(t, db, `DELETE FROM meta WHERE key='stats.started_at'`)
	view, err = s.GetOverview(t.Context(), "today")
	if err != nil || !view.Coverage.IsPartial || view.Coverage.FromAt != nil || view.Comparison != nil {
		t.Fatalf("没有覆盖证明却当成完整零活动：%+v %v", view, err)
	}
	overviewExec(t, db, `UPDATE meta SET value='not-a-time' WHERE key='stats.coverage.findings'`)
	if _, err = s.GetOverview(t.Context(), "today"); err == nil {
		t.Fatal("坏覆盖记录被吞掉")
	}
}

func TestOverviewInvalidPeriodAndEmptySnapshot(t *testing.T) {
	s, db, _ := overviewFixture(t)
	for _, period := range []string{"", "week", "TODAY", "7"} {
		_, err := s.GetOverview(t.Context(), period)
		var issue *ServiceError
		if !errors.As(err, &issue) || issue.Kind != ErrorBadRequest {
			t.Fatalf("非法范围被默默改为今天：%q %v", period, err)
		}
	}
	view, err := s.GetOverview(t.Context(), "today")
	if err != nil {
		t.Fatal(err)
	}
	if view.Tasks == nil || view.GradeDistribution == nil || view.Exceptions.UnresolvedEvents == nil || len(view.Tasks) != 0 || view.AI.SuccessRate != nil || view.AI.AverageDurationMS != nil || view.Notifications.SuccessRate != nil {
		t.Fatalf("空快照假造比例或返回null列表：%+v", view)
	}
	overviewExec(t, db, `INSERT INTO run_records(task_name,trigger,started_at,outcome,scanned_count,new_count) VALUES('错误计数','auto',?,'failed',1,2)`, view.FromAt+1)
	if _, err := s.GetOverview(t.Context(), "today"); err == nil {
		t.Fatal("新数量超过扫描量时被静默截成零")
	}
}

func TestOverviewTaskStatePriorityKeepsOneState(t *testing.T) {
	account := int64(1)
	failed := "failed"
	task := Task{ID: 1, TaskFields: TaskFields{Enabled: false, AccountID: &account}, LastRunOutcome: &failed, FailurePausedUntil: overviewInt(100)}
	accounts := map[int64]overviewAccount{1: {state: AccountCooling}}
	if got := overviewTask(task, TaskRuntime{Phase: TaskRunning}, accounts, nil, nil, 1, 2); got.State != OverviewChecking {
		t.Fatal("手动检查中的停用任务没有优先显示正在检查", got)
	}
	if got := overviewTask(task, TaskRuntime{}, accounts, nil, nil, 1, 2); got.State != OverviewPaused {
		t.Fatal("停用被最近失败遮住", got)
	}
	task.Enabled = true
	if got := overviewTask(task, TaskRuntime{}, accounts, nil, nil, 1, 2); got.State != OverviewPaused {
		t.Fatal("账号冷却被最近失败遮住", got)
	}
	for _, state := range []AccountState{AccountSignedOut, AccountExpired, AccountNeedsVerification, AccountDenied, AccountCooling} {
		accounts[1] = overviewAccount{state: state}
		got := overviewTask(task, TaskRuntime{}, accounts, nil, nil, 1, 2)
		if got.Reason == nil {
			t.Fatal("没有原状态说明时丢失账号阻断原因", state)
		}
		for _, language := range []locales.Language{locales.ZhCN, locales.En} {
			if _, err := locales.Format(language, *got.Reason); err != nil {
				t.Fatalf("账号阻断说明不能在%s显示：%v", language, err)
			}
		}
	}
	accounts[1] = overviewAccount{state: AccountActive}
	if got := overviewTask(task, TaskRuntime{}, accounts, nil, nil, 1, 2); got.State != OverviewError {
		t.Fatal("失败保护未显示出错", got)
	}
}

func TestOverviewRollbackKeepsObservedTimeAndDoesNotWriteClock(t *testing.T) {
	s, db, now := overviewFixture(t)
	var raw string
	if err := db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='quota.clock'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var state quota.ClockState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatal(err)
	}
	state.HighestUTC = now.Add(6 * time.Hour).UnixMilli()
	clock, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	overviewExec(t, db, `UPDATE meta SET value=? WHERE key='quota.clock'`, string(clock))
	overviewExec(t, db, `INSERT INTO run_records(task_name,trigger,started_at,ended_at,outcome,scanned_count,new_count) VALUES('已观察的事实','auto',?,?,'success',7,2)`, now.Add(time.Hour).UnixMilli(), now.Add(time.Hour+time.Second).UnixMilli())
	view, err := s.GetOverview(t.Context(), "today")
	if err != nil {
		t.Fatal(err)
	}
	if view.AsOf != state.HighestUTC || view.ToAt != state.HighestUTC || view.Cards.ScannedCount != 7 || view.Cards.NewCount != 2 {
		t.Fatalf("回拨把已记录业务当未来排除了：%+v", view)
	}
	var after string
	if err := db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='quota.clock'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != string(clock) {
		t.Fatal("纯读统计更新了保护时间或旧窗口")
	}
}

func TestOverviewUnknownPriceNeedsBudgetProofWithoutHidingDefaultResult(t *testing.T) {
	s, db, now := overviewFixture(t)
	overviewAddTask(t, db, 1, nil)
	overviewAddTask(t, db, 2, nil)
	overviewExec(t, db, `UPDATE tasks SET budget_max=NULL WHERE id=2`)
	overviewAddFinding(t, db, now, 1, 1, "unknown", "matched", nil, nil, false, nil, 3)
	overviewAddFinding(t, db, now, 2, 2, "unknown", "matched", nil, nil, false, nil, 3)
	overviewExec(t, db, `UPDATE listings SET price=NULL WHERE item_id='unknown'`)
	view, err := s.GetOverview(t.Context(), "today")
	if err != nil {
		t.Fatal(err)
	}
	if view.Cards.RecommendedCount != 1 || view.Cards.RecommendedKeywordCount != 1 || view.Tasks[0].RecommendedCount != 0 || view.Tasks[1].RecommendedCount != 1 {
		t.Fatalf("有预算但未知价被算成推荐，或无预算被误挡：%+v %+v", view.Cards, view.Tasks)
	}
	results, err := s.ListFindings(t.Context(), FindingQuery{View: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if results.Total != 2 || len(results.Items) != 2 {
		t.Fatalf("未知价被统计门槛误藏到默认列表之外：%+v", results)
	}
	for _, result := range results.Items {
		if result.Decision != analysis.DecisionMatched || result.Listing.Price != nil {
			t.Fatal("默认结果的价格或关键词结论被统计改写", result)
		}
	}
}

func TestOverviewUsesRunGatesInsteadOfOnlyTaskPlan(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, *Service, *sql.DB, time.Time)
		want    func(time.Time) *int64
	}{
		{"account-gap", func(t *testing.T, s *Service, db *sql.DB, now time.Time) {
			overviewExec(t, db, `UPDATE accounts SET last_run_ended_at=?,next_gap_seconds=600 WHERE id=1`, now.UnixMilli())
		}, func(now time.Time) *int64 { return overviewInt(now.Add(10 * time.Minute).UnixMilli()) }},
		{"hourly-quota", func(t *testing.T, s *Service, db *sql.DB, now time.Time) {
			for i := 0; i < 30; i++ {
				overviewExec(t, db, `INSERT INTO quota_events(account_id,kind,at) VALUES(1,'search',?)`, now.Add(-time.Minute).UnixMilli())
			}
		}, func(now time.Time) *int64 { return overviewInt(now.Add(59 * time.Minute).UnixMilli()) }},
		{"cooling", func(t *testing.T, s *Service, db *sql.DB, now time.Time) {
			overviewExec(t, db, `UPDATE accounts SET state='cooling',cooldown_until=? WHERE id=1`, now.Add(30*time.Minute).UnixMilli())
		}, func(now time.Time) *int64 { return overviewInt(now.Add(30 * time.Minute).UnixMilli()) }},
		{"quiet-hours", func(t *testing.T, s *Service, db *sql.DB, now time.Time) {
			if _, err := s.settings.Apply(t.Context(), settings.Values{"crawl.quiet_hours": json.RawMessage(`{"enabled":true,"start":"00:00","end":"23:00"}`)}); err != nil {
				t.Fatal(err)
			}
		}, func(now time.Time) *int64 {
			return overviewInt(time.Date(now.Year(), now.Month(), now.Day(), 23, 0, 0, 0, time.UTC).UnixMilli())
		}},
		{"ai-daily-limit", func(t *testing.T, s *Service, db *sql.DB, now time.Time) {
			overviewExec(t, db, `UPDATE tasks SET judge_mode='ai',notify_policy='good',profile_json='{"musts":[{"id":"m1","text":"国行","source":"user","edited":false,"rev":1}],"next_no":2}' WHERE id=1`)
			overviewExec(t, db, `INSERT INTO ai_providers(uuid,name,base_url,vision_model,sort_index) VALUES('test','本地假模型','https://example.invalid','test',0)`)
			overviewExec(t, db, `INSERT INTO ai_calls(at,input_tokens,output_tokens) VALUES(?,10,0)`, now.Add(-time.Hour).UnixMilli())
			if _, err := s.settings.Apply(t.Context(), settings.Values{"ai.daily_token_limit": json.RawMessage(`10`)}); err != nil {
				t.Fatal(err)
			}
		}, func(now time.Time) *int64 {
			return overviewInt(time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC).UnixMilli())
		}},
		{"quota-capacity", func(t *testing.T, s *Service, db *sql.DB, now time.Time) {
			if _, err := s.settings.Apply(t.Context(), settings.Values{"crawl.account_hourly_visits": json.RawMessage(`1`)}); err != nil {
				t.Fatal(err)
			}
		}, func(time.Time) *int64 { return nil }},
		{"expired-needs-user", func(t *testing.T, s *Service, db *sql.DB, now time.Time) {
			overviewExec(t, db, `UPDATE accounts SET state='expired' WHERE id=1`)
		}, func(time.Time) *int64 { return nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, db, now := overviewFixture(t)
			overviewExec(t, db, `INSERT INTO accounts(id,name,state) VALUES(1,'可用','active')`)
			id := int64(1)
			overviewAddTask(t, db, 1, &id)
			overviewExec(t, db, `UPDATE tasks SET next_run_at=? WHERE id=1`, now.Add(time.Minute).UnixMilli())
			test.prepare(t, s, db, now)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			view, err := s.GetOverview(ctx, "today")
			if err != nil {
				t.Fatal(err)
			}
			if len(view.Tasks) != 1 || !reflect.DeepEqual(view.Tasks[0].NextRunAt, test.want(now)) {
				t.Fatalf("只用了计划时间、没取更晚门禁：%+v，期望%v", view.Tasks, test.want(now))
			}
		})
	}
}

func TestOverviewDoesNotPredictResourceReleaseTime(t *testing.T) {
	for _, test := range []struct {
		name           string
		sessionAccount int64
		pages          int64
	}{
		{"same-account", 1, 2}, {"global-page", 2, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, db, now := overviewFixture(t)
			overviewExec(t, db, `INSERT INTO accounts(id,name,state) VALUES(1,'任务账号','active'),(2,'其他账号','active')`)
			id := int64(1)
			overviewAddTask(t, db, 1, &id)
			overviewExec(t, db, `UPDATE tasks SET next_run_at=? WHERE id=1`, now.Add(time.Minute).UnixMilli())
			pages, _ := json.Marshal(test.pages)
			if _, err := s.settings.Apply(t.Context(), settings.Values{"crawl.global_pages": pages}); err != nil {
				t.Fatal(err)
			}
			s.initRuntime()
			s.mu.Lock()
			s.sessions["manual"] = &sessionWork{View: SessionView{ID: "manual", AccountID: test.sessionAccount, Kind: SessionLogin, Phase: SessionWaitingLogin}}
			s.mu.Unlock()
			view, err := s.GetOverview(t.Context(), "today")
			if err != nil {
				t.Fatal(err)
			}
			if view.Tasks[0].NextRunAt != nil {
				t.Fatal("从任务计划猜测浏览器资源释放时间", *view.Tasks[0].NextRunAt)
			}
		})
	}
}

func TestOverviewUsesSameUserActionBlockAsTaskRuntime(t *testing.T) {
	for _, test := range []struct {
		name, profile, code string
	}{
		{"no-provider", `{"musts":[{"id":"m1","text":"国行","source":"user","edited":false,"rev":1}],"next_no":2}`, "run.no_ai_provider"},
		{"empty-profile", `{}`, "run.empty_profile"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, db, now := overviewFixture(t)
			overviewExec(t, db, `INSERT INTO accounts(id,name,state,last_run_ended_at,next_gap_seconds) VALUES(1,'可用','active',?,600)`, now.UnixMilli())
			id := int64(1)
			overviewAddTask(t, db, 1, &id)
			overviewExec(t, db, `UPDATE tasks SET judge_mode='ai',notify_policy='good',profile_json=?,next_run_at=? WHERE id=1`, test.profile, now.Add(time.Minute).UnixMilli())
			overviewExec(t, db, `CREATE TRIGGER overview_user_block_is_read_only BEFORE UPDATE ON meta WHEN OLD.key IN ('quota.clock','quota.clock_initialized') BEGIN SELECT RAISE(ABORT,'读取用户处理原因不能写时钟'); END`)
			view, err := s.GetOverview(t.Context(), "today")
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := s.GetTaskRuntime(t.Context(), 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(view.Tasks) != 1 || view.Tasks[0].NextRunAt != nil || view.Tasks[0].Reason == nil || view.Tasks[0].Reason.Code != test.code {
				t.Fatalf("较晚的计划或间隔遮住了需人处理原因：%+v", view.Tasks)
			}
			if runtime.Blocked == nil || runtime.Blocked.NextAllowedAt != nil || !reflect.DeepEqual(*view.Tasks[0].Reason, runtime.Blocked.Reason) {
				t.Fatalf("概览和任务运行态使用了两套展示判据：%+v %+v", view.Tasks[0], runtime)
			}
		})
	}
}
