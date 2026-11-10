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

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func logTestPointer[T any](value T) *T { return &value }

func logTestService(t *testing.T) (*Service, *time.Time) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Date(2030, 11, 11, 9, 45, 0, 0, time.UTC)
	preferences, err := appsettings.New(t.Context(), db.DB(), settings.Environment{TZ: "UTC"}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if db.DB().Stats().MaxOpenConnections != 1 {
		t.Fatal("日志回归需要真实单连接数据库")
	}
	return &Service{db: db.DB(), settings: preferences, now: func() time.Time { return now }}, &now
}

func logTestTx(t *testing.T, s *Service) *sql.Tx {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

func logTestTask(t *testing.T, s *Service, name string) int64 {
	t.Helper()
	result, err := s.db.ExecContext(t.Context(), "INSERT INTO tasks(name,keyword,max_pages,interval_minutes,judge_mode,notify_policy,sort_index) VALUES(?,?,1,30,'keyword','recommended',0)", name, "相机")
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func logTestRecord(taskID int64, name string, started int64) RunRecord {
	return RunRecord{TaskID: &taskID, TaskName: name, Trigger: TriggerAuto, StartedAt: &started,
		AccountID: logTestPointer(int64(12)), AccountName: logTestPointer("备用账号"), ProxyName: logTestPointer("本地代理")}
}

func logTestFinished(t *testing.T, s *Service, taskID int64, name string, start, duration int64, outcome RunOutcome) RunRecord {
	t.Helper()
	tx := logTestTx(t, s)
	run, err := beginRunTx(t.Context(), tx, logTestRecord(taskID, name, start))
	if err != nil {
		t.Fatal(err)
	}
	run.Outcome, run.EndedAt = &outcome, logTestPointer(start+duration)
	run.FailureKind = nil
	if outcome == RunFailed {
		run.FailureKind = logTestPointer(FailureNetwork)
	}
	run.Message = &locales.Message{Code: "run.test"}
	run.ScannedCount = logTestPointer(int64(7))
	if err := finishRunTx(t.Context(), tx, run); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return run
}

func logTestErrorKind(t *testing.T, err error, kind ErrorKind) {
	t.Helper()
	var failure *ServiceError
	if !errors.As(err, &failure) || failure.Kind != kind {
		t.Fatalf("错误类别不符：期望%s，实际%v", kind, err)
	}
}

func TestRunLogBeginFinishShareCallerTransaction(t *testing.T) {
	s, now := logTestService(t)
	taskID := logTestTask(t, s, "摄影器材")
	tx := logTestTx(t, s)
	input := logTestRecord(taskID, "摄影器材", now.UnixMilli())
	input.Outcome = logTestPointer(RunSuccess)
	input.EndedAt = logTestPointer(now.UnixMilli() + 10)
	input.Message = &locales.Message{Code: "旧结果"}
	run, err := beginRunTx(t.Context(), tx, input)
	if err != nil {
		t.Fatal(err)
	}
	if run.ID <= 0 || run.EndedAt != nil || run.Outcome == nil || *run.Outcome != RunFailed ||
		run.FailureKind == nil || *run.FailureKind != FailureRestart || run.Message != nil {
		t.Fatalf("开始记录没有保守占位：%+v", run)
	}
	var ended sql.NullInt64
	var outcome, failure string
	if err := tx.QueryRowContext(t.Context(), "SELECT outcome,failure_kind,ended_at FROM run_records WHERE id=?", run.ID).Scan(&outcome, &failure, &ended); err != nil ||
		outcome != "failed" || failure != "restart" || ended.Valid {
		t.Fatalf("数据库占位错误：%s %s %+v %v", outcome, failure, ended, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal("日志入口擅自结束事务", err)
	}
	var count int64
	if err := s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM run_records").Scan(&count); err != nil || count != 0 {
		t.Fatal("开始回滚仍留下记录", count, err)
	}
	tx = logTestTx(t, s)
	run, err = beginRunTx(t.Context(), tx, logTestRecord(taskID, "摄影器材", now.UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	run.EndedAt, run.Outcome, run.FailureKind = logTestPointer(now.UnixMilli()+1000), logTestPointer(RunSuccess), nil
	run.ScannedCount = logTestPointer(int64(10))
	run.Message = &locales.Message{Code: "run.test"}
	run.Warnings = []locales.Message{
		{Code: "run.test_warning", Args: map[string]any{"item": "相机", "count": 1}},
		{Code: "run.test_warning", Args: map[string]any{"count": 1, "item": "相机"}},
		{Code: "run.test_warning", Args: map[string]any{"item": "镜头", "count": 1}},
	}
	if err := finishRunTx(t.Context(), tx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), "UPDATE tasks SET enabled=0 WHERE id=?", taskID); err != nil {
		t.Fatal("结束入口擅自结束事务", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	detail, err := s.GetRun(t.Context(), run.ID)
	if err != nil || detail.Run.Outcome == nil || *detail.Run.Outcome != RunSuccess ||
		detail.Run.ScannedCount == nil || *detail.Run.ScannedCount != 10 || len(detail.Run.Warnings) != 2 ||
		detail.Steps.Items == nil {
		t.Fatalf("完整结果未保留：%+v %v", detail, err)
	}
	run.Warnings[0].Args["item"] = "外部修改"
	detail, err = s.GetRun(t.Context(), run.ID)
	if err != nil || detail.Run.Warnings[0].Args["item"] != "相机" {
		t.Fatal("返回值与输入共享了警告参数", detail, err)
	}
	tx = logTestTx(t, s)
	logTestErrorKind(t, finishRunTx(t.Context(), tx, run), ErrorConflict)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), "DELETE FROM tasks WHERE id=?", taskID); err != nil {
		t.Fatal(err)
	}
	detail, err = s.GetRun(t.Context(), run.ID)
	if err != nil || detail.Run.TaskID != nil || detail.Run.TaskName != "摄影器材" ||
		detail.Run.AccountName == nil || *detail.Run.AccountName != "备用账号" {
		t.Fatalf("删除任务损坏历史快照：%+v %v", detail, err)
	}
}

func TestRunLogUnfinishedPlaceholderIsNotAnOutcome(t *testing.T) {
	s, now := logTestService(t)
	taskID := logTestTask(t, s, "正在检查")
	tx := logTestTx(t, s)
	run, err := beginRunTx(t.Context(), tx, logTestRecord(taskID, "正在检查", now.Add(-time.Minute).UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListRuns(t.Context(), RunQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Outcome != nil || page.Items[0].FailureKind != nil || page.Items[0].Message != nil {
		t.Fatalf("未结束记录泄露失败占位：%+v %v", page, err)
	}
	raw, err := json.Marshal(page.Items[0])
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil ||
		string(object["outcome"]) != "null" || string(object["failure_kind"]) != "null" || string(object["warnings"]) != "[]" {
		t.Fatalf("未知字段或空列表编码错误：%s %v", raw, err)
	}
	page, err = s.ListRuns(t.Context(), RunQuery{Outcome: logTestPointer(RunFailed)})
	if err != nil || page.Total != 0 {
		t.Fatal("未结束记录进入失败筛选", page, err)
	}
	page, err = s.ListRuns(t.Context(), RunQuery{FailureKind: logTestPointer(FailureRestart)})
	if err != nil || page.Total != 0 {
		t.Fatal("未结束记录进入重启失败筛选", page, err)
	}
	overview, err := s.GetRunLogOverview(t.Context())
	if err != nil || overview.TotalCount != 0 || overview.SuccessRate != nil || overview.AverageDurationMS != nil {
		t.Fatal("未结束记录进入完成统计", overview, err)
	}
	tx = logTestTx(t, s)
	run.EndedAt, run.Outcome = logTestPointer(*run.StartedAt-1), logTestPointer(RunSuccess)
	logTestErrorKind(t, finishRunTx(t.Context(), tx, run), ErrorInvalid)
}

func TestRunLogSkipFoldingPreservesEveryOccurrence(t *testing.T) {
	s, now := logTestService(t)
	taskID := logTestTask(t, s, "跳过测试")
	first := now.Add(-25 * time.Hour).UnixMilli()
	second := now.Add(-3 * time.Hour).UnixMilli()
	third := now.Add(-30 * time.Minute).UnixMilli()
	otherTaskID := logTestTask(t, s, "另一个任务")
	var folded RunRecord
	for i, at := range []int64{first, second, third} {
		tx := logTestTx(t, s)
		input := logTestRecord(taskID, "跳过测试", at)
		input.Message = &locales.Message{Code: "run.no_account", Args: map[string]any{"observed": i}}
		run, err := recordSkipTx(t.Context(), tx, input, at)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && run.ID != folded.ID {
			t.Fatal("相同任务原因没有连续折叠")
		}
		folded = run
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			logTestFinished(t, s, otherTaskID, "另一个任务", at+10, 10, RunSuccess)
		}
	}
	if folded.RepeatCount != 3 || folded.StartedAt == nil || *folded.StartedAt != first ||
		folded.EndedAt == nil || *folded.EndedAt != third {
		t.Fatalf("折叠时间或次数错误：%+v", folded)
	}
	rows, err := s.db.QueryContext(t.Context(), "SELECT seq,at FROM run_skip_events WHERE run_id=? ORDER BY seq", folded.ID)
	if err != nil {
		t.Fatal(err)
	}
	var actual []int64
	for rows.Next() {
		var seq, at int64
		if err := rows.Scan(&seq, &at); err != nil || seq != int64(len(actual)+1) {
			t.Fatal("实际跳过序号错误", seq, err)
		}
		actual = append(actual, at)
	}
	rows.Close()
	if !reflect.DeepEqual(actual, []int64{first, second, third}) {
		t.Fatal("逐次发生时间被折叠丢失", actual)
	}
	overview, err := s.GetRunLogOverview(t.Context())
	if err != nil || overview.SkippedCount != 2 || overview.TotalCount != 2 ||
		overview.Buckets[21].SkippedCount != 1 || overview.Buckets[23].SkippedCount != 1 {
		t.Fatalf("跨小时跳过被算到最后一小时：%+v %v", overview, err)
	}
	logTestFinished(t, s, taskID, "跳过测试", now.Add(-20*time.Minute).UnixMilli(), 100, RunSuccess)
	tx := logTestTx(t, s)
	input := logTestRecord(taskID, "跳过测试", now.UnixMilli())
	input.Message = &locales.Message{Code: "run.no_account"}
	next, err := recordSkipTx(t.Context(), tx, input, now.UnixMilli())
	if err != nil || next.ID == folded.ID || next.RepeatCount != 1 {
		t.Fatal("真正开始过的运行没有分隔折叠", next, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx = logTestTx(t, s)
	input.Message.Code = "run.account_gap"
	different, err := recordSkipTx(t.Context(), tx, input, now.UnixMilli()+1)
	if err != nil || different.ID == next.ID {
		t.Fatal("不同原因错误折叠", different, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM run_records WHERE id=?", different.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("跳过回滚仍留下记录", count, err)
	}
}

func TestRunLogQueriesHaveStablePaginationAndLiteralSearch(t *testing.T) {
	s, now := logTestService(t)
	taskID := logTestTask(t, s, "分页")
	start := now.Add(-time.Hour).UnixMilli()
	first := logTestFinished(t, s, taskID, "Alpha_100%", start, 200, RunSuccess)
	second := logTestFinished(t, s, taskID, "Beta", start, 400, RunFailed)
	third := logTestFinished(t, s, taskID, "Gamma", start+1, 100, RunSuccess)
	legacy, err := s.db.ExecContext(t.Context(), "INSERT INTO run_records(task_name,trigger,outcome,ended_at) VALUES('旧记录','auto','success',?)", start+20)
	if err != nil {
		t.Fatal(err)
	}
	legacyID, _ := legacy.LastInsertId()
	page, err := s.ListRuns(t.Context(), RunQuery{ListQuery: ListQuery{Limit: 1}})
	if err != nil || page.Total != 4 || len(page.Items) != 1 || page.Items[0].ID != third.ID || page.NextCursor == nil {
		t.Fatal("分页首条错误", page, err)
	}
	next, err := s.ListRuns(t.Context(), RunQuery{ListQuery: ListQuery{Limit: 2, Cursor: *page.NextCursor}})
	if err != nil || next.Total != 4 || len(next.Items) != 2 || next.Items[0].ID != second.ID || next.Items[1].ID != first.ID || next.NextCursor == nil {
		t.Fatal("相同开始时间没有按编号稳定排序", next, err)
	}
	last, err := s.ListRuns(t.Context(), RunQuery{ListQuery: ListQuery{Cursor: *next.NextCursor}})
	if err != nil || len(last.Items) != 1 || last.Items[0].ID != legacyID || last.Items[0].StartedAt != nil || last.NextCursor != nil {
		t.Fatal("未知开始时间没有放最后", last, err)
	}
	_, err = s.ListRuns(t.Context(), RunQuery{ListQuery: ListQuery{Cursor: *page.NextCursor}, Q: "Beta"})
	logTestErrorKind(t, err, ErrorBadRequest)
	for query, count := range map[string]int{"%": 1, "_": 1, "alpha": 1, "%' OR 1=1--": 0, "备用账号": 3, "本地代理": 3} {
		filtered, err := s.ListRuns(t.Context(), RunQuery{Q: query})
		if err != nil || filtered.Total != int64(count) {
			t.Fatalf("搜索不是字面匹配：%q %+v %v", query, filtered, err)
		}
	}
	filtered, err := s.ListRuns(t.Context(), RunQuery{FromAt: &start, ToAt: logTestPointer(start + 1)})
	if err != nil || filtered.Total != 2 {
		t.Fatal("时间范围未按左闭右开或包含未知时间", filtered, err)
	}
	for _, query := range []RunQuery{
		{ListQuery: ListQuery{Limit: 201}}, {ListQuery: ListQuery{Limit: -1}},
		{ListQuery: ListQuery{Cursor: "bad!"}}, {TaskID: logTestPointer(int64(0))},
		{Outcome: logTestPointer(RunOutcome("running"))}, {FailureKind: logTestPointer(FailureKind("unknown"))},
		{FromAt: &start, ToAt: &start}, {Q: string([]byte{0xff})},
	} {
		_, err := s.ListRuns(t.Context(), query)
		logTestErrorKind(t, err, ErrorBadRequest)
	}
}

func TestRunLogOverviewUsesExactRollingDayAndExcludesSkipsFromRate(t *testing.T) {
	s, now := logTestService(t)
	taskID := logTestTask(t, s, "过去一天")
	from := now.Add(-24 * time.Hour).UnixMilli()
	logTestFinished(t, s, taskID, "边界内", from, 1000, RunSuccess)
	logTestFinished(t, s, taskID, "失败", now.Add(-time.Hour).UnixMilli(), 3000, RunFailed)
	logTestFinished(t, s, taskID, "边界外", from-1, 100, RunSuccess)
	logTestFinished(t, s, taskID, "尚未进入窗口", now.UnixMilli(), 100, RunFailed)
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO run_records(task_name,trigger,outcome,ended_at) VALUES('未知开始','auto','success',?)", now.UnixMilli()-10); err != nil {
		t.Fatal(err)
	}
	for _, at := range []int64{from, now.Add(-2 * time.Hour).UnixMilli(), now.UnixMilli()} {
		tx := logTestTx(t, s)
		input := logTestRecord(taskID, "跳过", at)
		input.Message = &locales.Message{Code: "run.account_gap"}
		if _, err := recordSkipTx(t.Context(), tx, input, at); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	overview, err := s.GetRunLogOverview(t.Context())
	if err != nil || overview.FromAt != from || overview.ToAt != now.UnixMilli() || len(overview.Buckets) != 24 ||
		overview.TotalCount != 4 || overview.SuccessCount != 1 || overview.FailedCount != 1 || overview.SkippedCount != 2 ||
		overview.SuccessRate == nil || *overview.SuccessRate != .5 || overview.AverageDurationMS == nil || *overview.AverageDurationMS != 2000 {
		t.Fatalf("24小时统计或分母错误：%+v %v", overview, err)
	}
	var summed int64
	for i, bucket := range overview.Buckets {
		if bucket.FromAt != from+int64(i)*3600000 || bucket.ToAt-bucket.FromAt != 3600000 {
			t.Fatal("滚动小时桶没有覆盖完整一天", bucket)
		}
		summed += bucket.SuccessCount + bucket.FailedCount + bucket.SkippedCount
	}
	if summed != overview.TotalCount || overview.Buckets[0].SuccessCount != 1 || overview.Buckets[0].SkippedCount != 1 ||
		overview.Buckets[22].SkippedCount != 1 || overview.Buckets[23].FailedCount != 1 {
		t.Fatal("概览卡与全部小时桶合计不相等", overview)
	}
	previous := overview.AsOf
	*now = now.Add(-time.Hour)
	overview, err = s.GetRunLogOverview(t.Context())
	if err != nil || overview.AsOf != previous {
		t.Fatal("统计时钟绕过了已观察时间保护", overview, err)
	}
}

func TestRunLogRejectsCorruptionAndPreservesFailures(t *testing.T) {
	s, now := logTestService(t)
	taskID := logTestTask(t, s, "错误路径")
	run := logTestFinished(t, s, taskID, "错误路径", now.Add(-time.Hour).UnixMilli(), 100, RunSuccess)
	if _, err := s.db.ExecContext(t.Context(), "UPDATE run_records SET message='not-json' WHERE id=?", run.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.GetRun(t.Context(), run.ID)
	logTestErrorKind(t, err, ErrorUnavailable)
	if _, err := s.db.ExecContext(t.Context(), "UPDATE run_records SET message=NULL,warnings_json='{}' WHERE id=?", run.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.ListRuns(t.Context(), RunQuery{})
	logTestErrorKind(t, err, ErrorUnavailable)
	if _, err := s.db.ExecContext(t.Context(), "UPDATE run_records SET warnings_json='' WHERE id=?", run.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.ListRuns(t.Context(), RunQuery{})
	logTestErrorKind(t, err, ErrorUnavailable)
	if _, err := s.db.ExecContext(t.Context(), "UPDATE run_records SET warnings_json='[null]' WHERE id=?", run.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.ListRuns(t.Context(), RunQuery{})
	logTestErrorKind(t, err, ErrorUnavailable)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.ListRuns(cancelled, RunQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatal("取消被误记成存储错误", err)
	}
	_, err = s.GetRun(t.Context(), math.MaxInt64)
	logTestErrorKind(t, err, ErrorNotFound)
	tx := logTestTx(t, s)
	input := logTestRecord(taskID, "坏警告", now.UnixMilli())
	input.Warnings = []locales.Message{{Code: "run.bad", Args: map[string]any{"count": math.NaN()}}}
	if _, err := beginRunTx(t.Context(), tx, input); err == nil {
		t.Fatal("无法序列化的警告仍写入")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = s.ListRuns(t.Context(), RunQuery{})
	logTestErrorKind(t, err, ErrorUnavailable)
}

func TestRunLogSkipFailureRollsBackFoldedCounter(t *testing.T) {
	s, now := logTestService(t)
	taskID := logTestTask(t, s, "原子跳过")
	input := logTestRecord(taskID, "原子跳过", now.Add(-time.Hour).UnixMilli())
	input.Message = &locales.Message{Code: "run.no_account"}
	tx := logTestTx(t, s)
	run, err := recordSkipTx(t.Context(), tx, input, *input.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), "CREATE TRIGGER reject_skip_event BEFORE INSERT ON run_skip_events BEGIN SELECT RAISE(ABORT,'skip event unavailable'); END"); err != nil {
		t.Fatal(err)
	}
	tx = logTestTx(t, s)
	if _, err := recordSkipTx(t.Context(), tx, input, now.UnixMilli()); err == nil {
		t.Fatal("实际发生时间写入失败被吞掉")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal("跳过入口擅自结束调用方事务", err)
	}
	tx = logTestTx(t, s)
	saved, err := loadRunTx(t.Context(), tx, run.ID)
	if err != nil || saved.RepeatCount != 1 || saved.EndedAt == nil || *saved.EndedAt != *input.StartedAt {
		t.Fatal("事件失败后折叠计数仍被提交", saved, err)
	}
	var count int64
	if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM run_skip_events WHERE run_id=?", run.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("逐次事实与折叠计数不一致", count, err)
	}
}

func TestRunLogCoverageReportsMissingLegacySkipFacts(t *testing.T) {
	s, now := logTestService(t)
	overview, err := s.GetRunLogOverview(t.Context())
	if err != nil || overview.Coverage.FromAt != nil || !overview.Coverage.IsPartial || overview.Coverage.ToAt != overview.ToAt {
		t.Fatal("未知覆盖范围被当成完整数据", overview.Coverage, err)
	}
	baseline := now.Add(-40 * 24 * time.Hour).UnixMilli()
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO meta(key,value) VALUES('stats.started_at',?)", baseline); err != nil {
		t.Fatal(err)
	}
	overview, err = s.GetRunLogOverview(t.Context())
	if err != nil || overview.Coverage.IsPartial || overview.Coverage.FromAt == nil || *overview.Coverage.FromAt != baseline {
		t.Fatal("完整覆盖范围错误", overview.Coverage, err)
	}
	legacy := now.Add(-2 * time.Hour).UnixMilli()
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO meta(key,value) VALUES('stats.coverage.run_skip_events',?)", legacy); err != nil {
		t.Fatal(err)
	}
	overview, err = s.GetRunLogOverview(t.Context())
	if err != nil || !overview.Coverage.IsPartial || overview.Coverage.FromAt == nil || *overview.Coverage.FromAt != legacy {
		t.Fatal("旧折叠跳过的时间缺口未标明", overview.Coverage, err)
	}
	retained := now.Add(-time.Hour).UnixMilli()
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO meta(key,value) VALUES('stats.coverage.run_records',?)", retained); err != nil {
		t.Fatal(err)
	}
	overview, err = s.GetRunLogOverview(t.Context())
	if err != nil || overview.Coverage.FromAt == nil || *overview.Coverage.FromAt != retained {
		t.Fatal("没有采用全部统计来源最晚的覆盖起点", overview.Coverage, err)
	}
	if _, err := s.db.ExecContext(t.Context(), "UPDATE meta SET value='invalid' WHERE key='stats.coverage.run_skip_events'"); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetRunLogOverview(t.Context())
	logTestErrorKind(t, err, ErrorUnavailable)
}

func TestRunLogOverviewDoesNotWriteProtectedClock(t *testing.T) {
	s, _ := logTestService(t)
	if _, err := s.db.ExecContext(t.Context(), "CREATE TRIGGER refuse_clock_write BEFORE UPDATE OF value ON meta WHEN NEW.key='quota.clock' BEGIN SELECT RAISE(ABORT,'read only overview'); END"); err != nil {
		t.Fatal(err)
	}
	overview, err := s.GetRunLogOverview(t.Context())
	if err != nil || overview.TotalCount != 0 {
		t.Fatal("只读概览仍试图更新额度时钟", overview, err)
	}
}

func TestRunLogStartedRunThatSkipsAlsoRecordsOccurrence(t *testing.T) {
	s, now := logTestService(t)
	taskID := logTestTask(t, s, "运行后额度不足")
	started := now.Add(-2 * time.Minute).UnixMilli()
	ended := now.Add(-time.Minute).UnixMilli()
	tx := logTestTx(t, s)
	run, err := beginRunTx(t.Context(), tx, logTestRecord(taskID, "运行后额度不足", started))
	if err != nil {
		t.Fatal(err)
	}
	run.EndedAt, run.Outcome, run.FailureKind = &ended, logTestPointer(RunSkipped), logTestPointer(FailureQuota)
	run.Message = &locales.Message{Code: "run.hourly_quota"}
	if err := finishRunTx(t.Context(), tx, run); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	overview, err := s.GetRunLogOverview(t.Context())
	if err != nil || overview.TotalCount != 1 || overview.SkippedCount != 1 || overview.Buckets[23].SkippedCount != 1 {
		t.Fatal("真正开始后因额度或登录失效跳过的运行没有计入", overview, err)
	}
	var seq, at int64
	if err := s.db.QueryRowContext(t.Context(), "SELECT seq,at FROM run_skip_events WHERE run_id=?", run.ID).Scan(&seq, &at); err != nil || seq != 1 || at != ended {
		t.Fatal("跳过事实没有记录结束判断时点", seq, at, err)
	}
}

func TestRunLogTimeFilterUsesSkipOccurrencesAndWindowCount(t *testing.T) {
	s, now := logTestService(t)
	from, to := now.Add(-24*time.Hour).UnixMilli(), now.UnixMilli()
	taskA := logTestTask(t, s, "window-a")
	taskB := logTestTask(t, s, "window-b")
	taskC := logTestTask(t, s, "window-c")
	addSkips := func(taskID int64, name string, times ...int64) RunRecord {
		t.Helper()
		var last RunRecord
		for _, at := range times {
			tx := logTestTx(t, s)
			input := logTestRecord(taskID, name, at)
			input.Message = &locales.Message{Code: "run.no_account"}
			input.FailureKind = logTestPointer(FailureConfig)
			var err error
			last, err = recordSkipTx(t.Context(), tx, input, at)
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		return last
	}
	first := now.Add(-25 * time.Hour).UnixMilli()
	second, third := now.Add(-3*time.Hour).UnixMilli(), now.Add(-30*time.Minute).UnixMilli()
	a := addSkips(taskA, "window-a", first, second, third)
	b := addSkips(taskB, "window-b", from, to)
	addSkips(taskC, "window-c", from-1, to)
	inside := logTestFinished(t, s, taskA, "普通窗口内", now.Add(-2*time.Hour).UnixMilli(), 100, RunSuccess)
	logTestFinished(t, s, taskA, "普通窗口外结束在内", from-1, 1000, RunSuccess)
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO run_records(task_name,trigger,started_at,ended_at,outcome,repeat_count) VALUES('旧折叠缺事实','auto',?,?,'skipped',7)", from+1, to-1); err != nil {
		t.Fatal(err)
	}
	query := RunQuery{
		TaskID: &taskA, Outcome: logTestPointer(RunSkipped), FailureKind: logTestPointer(FailureConfig),
		Q: "window", FromAt: &from, ToAt: &to,
	}
	page, err := s.ListRuns(t.Context(), query)
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != a.ID ||
		page.Items[0].RepeatCount != 2 || page.Items[0].StartedAt == nil || *page.Items[0].StartedAt != first {
		t.Fatalf("25小时前的折叠行没有按窗口内两次命中：%+v %v", page, err)
	}
	unbounded, err := s.ListRuns(t.Context(), RunQuery{TaskID: &taskA, Outcome: logTestPointer(RunSkipped)})
	if err != nil || len(unbounded.Items) != 1 || unbounded.Items[0].RepeatCount != 3 {
		t.Fatal("没有时间筛选时改变了总次数", unbounded, err)
	}
	detail, err := s.GetRun(t.Context(), a.ID)
	if err != nil || detail.Run.RepeatCount != 3 {
		t.Fatal("窗口查询改写了存储总次数", detail, err)
	}
	for _, test := range []struct {
		from, to *int64
		want     int64
	}{
		{from: &second, to: &third, want: 1},
		{from: &third, want: 1},
		{to: &second, want: 1},
	} {
		page, err := s.ListRuns(t.Context(), RunQuery{TaskID: &taskA, Outcome: logTestPointer(RunSkipped), FromAt: test.from, ToAt: test.to})
		if err != nil || len(page.Items) != 1 || page.Items[0].RepeatCount != test.want {
			t.Fatalf("单边或左闭右开窗口计数错误：%+v %v", page, err)
		}
	}
	page, err = s.ListRuns(t.Context(), RunQuery{ListQuery: ListQuery{Limit: 1}, FromAt: &from, ToAt: &to})
	if err != nil || page.Total != 3 || len(page.Items) != 1 || page.Items[0].ID != inside.ID || page.NextCursor == nil {
		t.Fatal("窗口筛选没有保留普通运行的开始时间规则", page, err)
	}
	next, err := s.ListRuns(t.Context(), RunQuery{ListQuery: ListQuery{Limit: 2, Cursor: *page.NextCursor}, FromAt: &from, ToAt: &to})
	if err != nil || next.Total != 3 || len(next.Items) != 2 || next.Items[0].ID != b.ID || next.Items[0].RepeatCount != 1 ||
		next.Items[1].ID != a.ID || next.Items[1].RepeatCount != 2 || next.NextCursor != nil {
		t.Fatal("折叠窗口分页排序或计数不稳定", next, err)
	}
}
