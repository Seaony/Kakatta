package modelservice

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/service"
)

func usageAt(t *testing.T, text string) int64 {
	t.Helper()
	value, err := time.Parse(time.RFC3339, text)
	if err != nil {
		t.Fatal(err)
	}
	return value.UnixMilli()
}

func usageCoverage(t *testing.T, f *modelFixture, started int64) {
	t.Helper()
	f.exec(t, `INSERT INTO meta(key,value) VALUES('stats.started_at',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.FormatInt(started, 10))
}

func usageCall(t *testing.T, f *modelFixture, at int64, task any, purpose string, ok, duration, input, output, reasoning any) {
	t.Helper()
	f.exec(t, `INSERT INTO ai_calls(at,task_id,purpose,ok,duration_ms,input_tokens,output_tokens,reasoning_tokens) VALUES(?,?,?,?,?,?,?,?)`, at, task, purpose, ok, duration, input, output, reasoning)
}

func TestUsageAggregatesPhysicalCallsAndDeletedTasks(t *testing.T) {
	f := newModelFixture(t)
	now := f.now.Load()
	usageCoverage(t, f, now-31*24*60*60*1000)
	f.exec(t, `INSERT INTO tasks(id,name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES(1,'在用任务','商品',1,30,'ai','good',0),(2,'删除任务','商品',1,30,'ai','good',1)`)
	usageCall(t, f, now-5, int64(1), "analysis", 1, 100, 10, 20, 5)
	usageCall(t, f, now-4, int64(2), "analysis", 0, 300, 30, 40, nil)
	usageCall(t, f, now-3, nil, "profile", 1, 200, nil, nil, nil)
	usageCall(t, f, now-2, nil, "probe", 1, 0, 0, 0, 0)
	f.exec(t, `DELETE FROM tasks WHERE id=2`)
	report, err := f.s.Usage(t.Context(), UsageQuery{Window: UsageToday})
	if err != nil {
		t.Fatal(err)
	}
	want := UsageTotal{Calls: 4, OKCalls: 3, DurationMS: 600, InputTokens: 40, OutputTokens: 60, ReasoningTokens: modelPointer(int64(5)), MissingUsageCalls: 1, SuccessRate: modelPointer(.75), AverageDurationMS: modelPointer(150.)}
	if !reflect.DeepEqual(report.Total, want) || !report.Incomplete {
		t.Fatalf("物理调用、缺usage或reasoning重复统计：%+v", report)
	}
	if len(report.ByTask) != 3 || len(report.ByPurpose) != 3 {
		t.Fatalf("任务或用途漏组：%+v", report)
	}
	var taskCalls, purposeCalls int64
	for _, group := range report.ByTask {
		taskCalls += group.Total.Calls
		if group.TaskID == nil {
			if group.TaskName != nil || group.Deleted || group.Total.Calls != 2 {
				t.Fatalf("未关联任务组有误：%+v", group)
			}
		} else if *group.TaskID == 1 {
			if group.TaskName == nil || *group.TaskName != "在用任务" || group.Deleted {
				t.Fatalf("当前任务组有误：%+v", group)
			}
		} else if *group.TaskID != 2 || !group.Deleted || group.TaskName != nil {
			t.Fatalf("删除任务历史丢失：%+v", group)
		}
	}
	for _, group := range report.ByPurpose {
		purposeCalls += group.Total.Calls
	}
	if taskCalls != report.Total.Calls || purposeCalls != report.Total.Calls {
		t.Fatal("总计与显示分组不一致")
	}
	filtered, err := f.s.Usage(t.Context(), UsageQuery{Window: UsageToday, TaskID: modelPointer(int64(2)), Purpose: modelPointer("analysis")})
	if err != nil || filtered.Total.Calls != 1 || filtered.Total.OKCalls != 0 || len(filtered.ByTask) != 1 || !filtered.ByTask[0].Deleted || len(filtered.ByPurpose) != 1 {
		t.Fatalf("历史任务筛选错误：%+v %v", filtered, err)
	}
}

func TestUsageNaturalDayWindowsAndHalfOpenBoundary(t *testing.T) {
	for _, test := range []struct {
		name, timezone, now, today, week, month string
	}{
		{"春季23小时", "America/New_York", "2024-03-11T03:59:59Z", "2024-03-10T05:00:00Z", "2024-03-04T05:00:00Z", "2024-02-10T05:00:00Z"},
		{"秋季25小时", "America/New_York", "2024-11-04T04:59:59Z", "2024-11-03T04:00:00Z", "2024-10-28T04:00:00Z", "2024-10-05T04:00:00Z"},
		{"午夜不存在", "America/Sao_Paulo", "2018-11-04T20:00:00Z", "2018-11-04T03:00:00Z", "2018-10-29T03:00:00Z", "2018-10-06T03:00:00Z"},
		{"跳过民用日期", "Pacific/Apia", "2012-01-05T00:00:00Z", "2012-01-04T10:00:00Z", "2011-12-30T10:00:00Z", "2011-12-07T10:00:00Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newModelFixture(t)
			now := usageAt(t, test.now)
			// 测试历史时区时，先换掉夹具的较晚保护时刻。
			f.exec(t, `UPDATE meta SET value=? WHERE key=?`, `{"version":1,"highest_utc":`+strconv.FormatInt(now, 10)+`,"timezone":"UTC","windows":[]}`, appsettings.ClockKey)
			f.now.Store(now)
			applyModelSettings(t, f, map[string]any{"general.timezone": test.timezone})
			usageCoverage(t, f, usageAt(t, "2010-01-01T00:00:00Z"))
			for _, period := range []struct {
				window UsageWindow
				start  string
			}{{UsageToday, test.today}, {Usage7Days, test.week}, {Usage30Days, test.month}} {
				f.exec(t, `DELETE FROM ai_calls`)
				from := usageAt(t, period.start)
				for _, at := range []int64{from - 1, from, now - 1, now, now + 1} {
					usageCall(t, f, at, nil, "test", 1, 0, 1, 1, nil)
				}
				report, err := f.s.Usage(t.Context(), UsageQuery{Window: period.window})
				if err != nil || report.From != from || report.Until != now || report.Timezone != test.timezone || report.Total.Calls != 2 || report.Incomplete {
					t.Fatalf("自然日或[from,until)边界错误：%+v %v", report, err)
				}
				f.now.Store(now + 1)
				next, err := f.s.Usage(t.Context(), UsageQuery{Window: period.window})
				if err != nil || next.Total.Calls != 3 {
					t.Fatalf("当前毫秒的调用没有在下一毫秒计入：%+v %v", next, err)
				}
				f.now.Store(now)
			}
		})
	}
}

func TestUsageEmptyAndUnknownValues(t *testing.T) {
	f := newModelFixture(t)
	usageCoverage(t, f, f.now.Load()-86400000)
	empty, err := f.s.Usage(t.Context(), UsageQuery{Window: UsageToday})
	if err != nil || empty.ByTask == nil || empty.ByPurpose == nil || empty.Total.SuccessRate != nil || empty.Total.AverageDurationMS != nil || empty.Total.ReasoningTokens != nil || empty.Incomplete {
		t.Fatalf("空统计的null/[]有误：%+v %v", empty, err)
	}
	usageCall(t, f, f.now.Load()-1, nil, "title_screen", nil, nil, nil, nil, nil)
	unknown, err := f.s.Usage(t.Context(), UsageQuery{Window: UsageToday})
	if err != nil || unknown.Total.Calls != 1 || unknown.Total.OKCalls != 0 || unknown.Total.SuccessRate != nil || unknown.Total.AverageDurationMS != nil || unknown.Total.MissingUsageCalls != 1 || !unknown.Incomplete {
		t.Fatalf("未知数据被伪装成0成功率或0耗时：%+v %v", unknown, err)
	}
	usageCall(t, f, f.now.Load()-1, nil, "market", 0, 0, 0, 0, nil)
	filtered, err := f.s.Usage(t.Context(), UsageQuery{Window: UsageToday, Purpose: modelPointer("market")})
	if err != nil || filtered.Total.SuccessRate == nil || *filtered.Total.SuccessRate != 0 || filtered.Total.AverageDurationMS == nil || *filtered.Total.AverageDurationMS != 0 || filtered.Incomplete {
		t.Fatalf("已知的真实0被当成未知：%+v %v", filtered, err)
	}
}

func TestUsageCoverageDoesNotRestoreDiscardedHistory(t *testing.T) {
	f := newModelFixture(t)
	now := f.now.Load()
	unknown, err := f.s.Usage(t.Context(), UsageQuery{Window: Usage30Days})
	if err != nil || unknown.RetainedFrom != nil || !unknown.Incomplete {
		t.Fatalf("缺历史证明不能宣称完整：%+v %v", unknown, err)
	}
	usageCoverage(t, f, now-40*86400000)
	cutoff := now - 7*86400000
	f.exec(t, `INSERT INTO meta(key,value) VALUES('stats.coverage.ai_calls',?)`, strconv.FormatInt(cutoff, 10))
	applyModelSettings(t, f, map[string]any{"retention.stats_days": 90})
	report, err := f.s.Usage(t.Context(), UsageQuery{Window: Usage30Days})
	if err != nil || report.RetainedFrom == nil || *report.RetainedFrom != cutoff || !report.Incomplete {
		t.Fatalf("调大保留期凭空恢复了历史：%+v %v", report, err)
	}
	today, err := f.s.Usage(t.Context(), UsageQuery{Window: UsageToday})
	if err != nil || today.Incomplete {
		t.Fatalf("实际覆盖完整的今天不应标缺失：%+v %v", today, err)
	}
}

func TestUsageValidationAndRealInt64(t *testing.T) {
	f := newModelFixture(t)
	for _, query := range []UsageQuery{{}, {Window: "week"}, {Window: UsageToday, Purpose: modelPointer("")}, {Window: UsageToday, Purpose: modelPointer("other")}, {Window: UsageToday, TaskID: modelPointer(int64(0))}, {Window: UsageToday, TaskID: modelPointer(int64(-1))}, {Window: UsageToday, TaskID: modelPointer(limits.MaxSafeInteger + 1)}} {
		_, err := f.s.Usage(t.Context(), query)
		var business *service.ServiceError
		if !errors.As(err, &business) || business.Kind != service.ErrorBadRequest || business.Reason.Code != "service.invalid_query" || len(business.Reason.Args) != 0 {
			t.Fatalf("非法筛选未被拒绝：%+v %v", query, err)
		}
	}
	usageCoverage(t, f, f.now.Load()-86400000)
	const external = limits.MaxSafeInteger + 1
	usageCall(t, f, f.now.Load()-1, limits.MaxSafeInteger, "test", 1, 1, external, 5, external)
	report, err := f.s.Usage(t.Context(), UsageQuery{Window: UsageToday, TaskID: modelPointer(limits.MaxSafeInteger)})
	if err != nil || report.Total.InputTokens != external || report.Total.ReasoningTokens == nil || *report.Total.ReasoningTokens != external {
		t.Fatalf("真实外部int64被套用了safe筛选限制：%+v %v", report, err)
	}
	budget, err := f.s.BudgetStatus(t.Context())
	if err != nil || budget.ProtectedUsed != external+5 || budget.Blocked || budget.NextCheckAt != nil {
		t.Fatalf("真实外部int64预算或reasoning重复统计：%+v %v", budget, err)
	}
}

func TestUsageCorruptStorageFails(t *testing.T) {
	for _, change := range []string{
		`UPDATE ai_calls SET input_tokens=-1`,
		`UPDATE ai_calls SET output_tokens=-1`,
		`UPDATE ai_calls SET reasoning_tokens=-1`,
		`UPDATE ai_calls SET duration_ms=-1`,
		`UPDATE ai_calls SET task_id=-1`,
		`UPDATE ai_calls SET input_tokens=1.5`,
		`UPDATE ai_calls SET input_tokens=9223372036854775807,output_tokens=1`,
		`INSERT INTO ai_calls(at,purpose,ok,duration_ms,input_tokens,output_tokens) SELECT at,purpose,ok,9223372036854775807,0,0 FROM ai_calls`,
		`INSERT INTO ai_calls(at,purpose,ok,duration_ms,input_tokens,output_tokens) SELECT at,purpose,ok,0,9223372036854775807,0 FROM ai_calls`,
		`INSERT INTO ai_calls(at,purpose,ok,duration_ms,input_tokens,output_tokens,reasoning_tokens) SELECT at,purpose,ok,0,0,0,9223372036854775807 FROM ai_calls`,
		`UPDATE ai_calls SET purpose=NULL`,
		`UPDATE ai_calls SET at=NULL`,
		`UPDATE meta SET value='01' WHERE key='stats.started_at'`,
	} {
		t.Run(change, func(t *testing.T) {
			f := newModelFixture(t)
			usageCoverage(t, f, f.now.Load()-86400000)
			usageCall(t, f, f.now.Load()-1, nil, "analysis", 1, 1, 1, 1, 1)
			f.exec(t, change)
			_, err := f.s.Usage(t.Context(), UsageQuery{Window: UsageToday})
			var business *service.ServiceError
			if !errors.As(err, &business) || business.Reason.Code != "service.storage_failure" {
				t.Fatalf("损坏或溢出没有使整次查询失败：%v", err)
			}
		})
	}
}

func TestBudgetReadProtectsTimezoneAndRollback(t *testing.T) {
	for _, timezone := range []string{"Asia/Shanghai", "America/Los_Angeles"} {
		t.Run(timezone, func(t *testing.T) {
			f := newModelFixture(t)
			now := usageAt(t, "2024-01-02T12:00:00Z")
			f.now.Store(now)
			applyModelSettings(t, f, map[string]any{"ai.daily_token_limit": 10})
			usageCall(t, f, usageAt(t, "2024-01-02T01:00:00Z"), nil, "test", 1, 0, 6, 4, 2)
			applyModelSettings(t, f, map[string]any{"general.timezone": timezone})
			before := modelScalar[string](t, f, `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey)
			f.now.Store(now - 3600000)
			status, err := f.s.BudgetStatus(t.Context())
			wantStart, wantNext := usageAt(t, "2024-01-02T00:00:00Z"), usageAt(t, "2024-01-03T00:00:00Z")
			if timezone == "Asia/Shanghai" {
				wantStart = usageAt(t, "2024-01-01T16:00:00Z")
			}
			if err != nil || !status.Blocked || status.ProtectedUsed != 10 || status.ProtectedStart != wantStart || status.EffectiveNow != now || status.NextCheckAt == nil || *status.NextCheckAt != wantNext {
				t.Fatalf("改时区或回拨恢复了预算：%+v %v", status, err)
			}
			if timezone == "America/Los_Angeles" {
				if status.ProtectedStart != usageAt(t, "2024-01-02T00:00:00Z") {
					t.Fatal("向西改时区提前释放用量")
				}
			}
			usageCoverage(t, f, usageAt(t, "2024-01-01T00:00:00Z"))
			report, err := f.s.Usage(t.Context(), UsageQuery{Window: UsageToday})
			if err != nil || report.Until != now || report.From == status.ProtectedStart && timezone == "America/Los_Angeles" {
				t.Fatalf("自然统计窗混入保护预算窗：%+v %v", report, err)
			}
			if modelScalar[string](t, f, `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey) != before {
				t.Fatal("只读查询写回了额度时钟")
			}
			f.now.Store(wantNext)
			recovered, err := f.s.BudgetStatus(t.Context())
			if err != nil || recovered.Blocked || recovered.ProtectedUsed != 0 || recovered.NextCheckAt != nil {
				t.Fatalf("旧窗口结束后没有释放：%+v %v", recovered, err)
			}
		})
	}
}

func TestBudgetReadUsesDSTAndAllPurposes(t *testing.T) {
	f := newModelFixture(t)
	now := usageAt(t, "2024-03-10T12:00:00Z")
	f.now.Store(now)
	applyModelSettings(t, f, map[string]any{"general.timezone": "America/New_York", "ai.daily_token_limit": 1})
	// 消去切换时区的旧窗口，保留当天的23小时真实日界。
	f.exec(t, `UPDATE meta SET value=? WHERE key=?`, `{"version":1,"highest_utc":`+strconv.FormatInt(now, 10)+`,"timezone":"America/New_York","windows":[]}`, appsettings.ClockKey)
	for _, purpose := range []string{"title_screen", "analysis", "profile", "market", "test", "probe"} {
		usageCall(t, f, now-1, nil, purpose, 0, 0, 1, 2, 2)
	}
	status, err := f.s.BudgetStatus(t.Context())
	if err != nil || status.ProtectedUsed != 18 || status.NextCheckAt == nil || *status.NextCheckAt != usageAt(t, "2024-03-11T04:00:00Z") {
		t.Fatalf("预算漏计用途/失败请求或DST日界错误：%+v %v", status, err)
	}
	applyModelSettings(t, f, map[string]any{"ai.daily_token_limit": 0})
	status, err = f.s.BudgetStatus(t.Context())
	if err != nil || status.Blocked || status.ProtectedUsed != 18 || status.NextCheckAt != nil {
		t.Fatalf("不限额的状态错误：%+v %v", status, err)
	}
}

func TestUsageAndBudgetReadHaveNoWriteSideEffects(t *testing.T) {
	for _, hasEvent := range []bool{false, true} {
		t.Run(strconv.FormatBool(hasEvent), func(t *testing.T) {
			f := newModelFixture(t)
			usageCoverage(t, f, f.now.Load()-86400000)
			applyModelSettings(t, f, map[string]any{"ai.daily_token_limit": 1})
			usageCall(t, f, f.now.Load()-1, nil, "analysis", 1, 1, 1, 1, nil)
			if hasEvent {
				f.exec(t, `INSERT INTO system_events(kind,subject_id,created_at,message) VALUES('ai_budget',0,1,'{"code":"ai.budget_reached","args":{}}')`)
			}
			before := modelScalar[string](t, f, `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey)
			for _, table := range []string{"meta", "system_events", "ai_calls", "settings"} {
				for _, action := range []string{"INSERT", "UPDATE", "DELETE"} {
					f.exec(t, `CREATE TRIGGER forbid_usage_`+table+`_`+action+` BEFORE `+action+` ON `+table+` BEGIN SELECT RAISE(ABORT,'usage read wrote data'); END`)
				}
			}
			for _, now := range []int64{f.now.Load(), f.now.Load() + 2*86400000} {
				f.now.Store(now)
				if _, err := f.s.BudgetStatus(t.Context()); err != nil {
					t.Errorf("预算查询发生写入：%v", err)
				}
				if _, err := f.s.Usage(t.Context(), UsageQuery{Window: UsageToday}); err != nil {
					t.Errorf("用量查询发生写入：%v", err)
				}
			}
			if modelScalar[string](t, f, `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey) != before || modelScalar[bool](t, f, `SELECT EXISTS(SELECT 1 FROM system_events WHERE resolved_at IS NULL)`) != hasEvent || len(f.published) != 0 || len(f.notified) != 0 {
				t.Fatal("读取改了时钟、解决事件或发布了消息")
			}
		})
	}
}

func TestBudgetReadMultipleOldWindowsAndPartialRelease(t *testing.T) {
	for _, limit := range []int64{5, 7} {
		t.Run(strconv.FormatInt(limit, 10), func(t *testing.T) {
			f := newModelFixture(t)
			now := usageAt(t, "2024-01-02T12:00:00Z")
			f.now.Store(now)
			applyModelSettings(t, f, map[string]any{"ai.daily_token_limit": limit})
			usageCall(t, f, usageAt(t, "2024-01-02T01:00:00Z"), nil, "test", 1, 0, 4, 0, nil)
			usageCall(t, f, usageAt(t, "2024-01-02T09:00:00Z"), nil, "test", 1, 0, 6, 0, nil)
			for _, timezone := range []string{"America/Los_Angeles", "Asia/Shanghai"} {
				applyModelSettings(t, f, map[string]any{"general.timezone": timezone})
			}
			status, err := f.s.BudgetStatus(t.Context())
			boundary := usageAt(t, "2024-01-03T08:00:00Z")
			if limit == 7 {
				boundary = usageAt(t, "2024-01-03T00:00:00Z")
			}
			if err != nil || !status.Blocked || status.ProtectedUsed != 10 || status.NextCheckAt == nil || *status.NextCheckAt != boundary {
				t.Fatalf("遗漏旧窗口或只能等完全清零才恢复：%+v %v", status, err)
			}
			f.now.Store(boundary - 1)
			before, err := f.s.BudgetStatus(t.Context())
			if err != nil || !before.Blocked || before.NextCheckAt == nil || *before.NextCheckAt != boundary {
				t.Fatalf("恢复边界前提前放行：%+v %v", before, err)
			}
			f.now.Store(boundary)
			after, err := f.s.BudgetStatus(t.Context())
			if err != nil || after.Blocked || after.NextCheckAt != nil {
				t.Fatalf("恢复边界没有放行：%+v %v", after, err)
			}
		})
	}
}

func TestBudgetReadCorruptStorageFails(t *testing.T) {
	for _, change := range []string{
		`UPDATE ai_calls SET input_tokens=-1`,
		`UPDATE ai_calls SET at=NULL`,
		`UPDATE ai_calls SET at=at+60000`,
		`UPDATE ai_calls SET input_tokens=9223372036854775807,output_tokens=1`,
		`INSERT INTO ai_calls(at,purpose,input_tokens,output_tokens) SELECT at,purpose,9223372036854775807,0 FROM ai_calls`,
		`UPDATE meta SET value='{}' WHERE key='quota.clock'`,
	} {
		t.Run(change, func(t *testing.T) {
			f := newModelFixture(t)
			usageCall(t, f, f.now.Load()-1, nil, "analysis", 1, 0, 1, 1, nil)
			f.exec(t, change)
			_, err := f.s.BudgetStatus(t.Context())
			var business *service.ServiceError
			if !errors.As(err, &business) || business.Reason.Code != "service.storage_failure" {
				t.Fatalf("损坏预算没有阻止返回：%v", err)
			}
		})
	}
}

func TestUsageAndBudgetReadCancelWhileWaitingForDatabase(t *testing.T) {
	f := newModelFixture(t)
	connection, err := f.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	for _, read := range []func(context.Context) error{
		func(ctx context.Context) error { _, err := f.s.Usage(ctx, UsageQuery{Window: UsageToday}); return err },
		func(ctx context.Context) error { _, err := f.s.BudgetStatus(ctx); return err },
	} {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
		err := read(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("等待数据库时不响应取消：%v", err)
		}
	}
}

func TestUsageAndBudgetReadCancellation(t *testing.T) {
	f := newModelFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.s.Usage(ctx, UsageQuery{Window: UsageToday}); !errors.Is(err, context.Canceled) {
		t.Fatalf("用量查询吞掉取消：%v", err)
	}
	if _, err := f.s.BudgetStatus(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("预算查询吞掉取消：%v", err)
	}
	if err := f.s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, read := range []func() error{
		func() error { _, err := f.s.Usage(t.Context(), UsageQuery{Window: UsageToday}); return err },
		func() error { _, err := f.s.BudgetStatus(t.Context()); return err },
	} {
		var business *service.ServiceError
		if err := read(); !errors.As(err, &business) || business.Reason.Code != "runtime.closed" {
			t.Fatalf("关闭后仍可读取：%v", err)
		}
	}
}
