package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func quotaService(t *testing.T) (*Service, *time.Time, int64) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "quota.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if db.DB().Stats().MaxOpenConnections != 1 {
		t.Fatal("额度事务必须在真实单连接数据库上验证")
	}
	now := gateAt("2026-11-10T20:00:00Z")
	preferences, err := appsettings.New(t.Context(), db.DB(), settings.Environment{TZ: "UTC"}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.DB().ExecContext(t.Context(), `INSERT INTO accounts(name,state) VALUES('本地测试','active')`)
	if err != nil {
		t.Fatal(err)
	}
	accountID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return &Service{db: db.DB(), settings: preferences, now: func() time.Time { return now }}, &now, accountID
}

func quotaRecordCount(t *testing.T, s *Service) int64 {
	t.Helper()
	var count int64
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM quota_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func savedQuotaClock(t *testing.T, s *Service) quota.ClockState {
	t.Helper()
	var raw string
	if err := s.db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var state quota.ClockState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func applyQuotaLimits(t *testing.T, s *Service, hourly, search, detail string) {
	t.Helper()
	if _, err := s.settings.Apply(t.Context(), settings.Values{
		"crawl.account_hourly_visits":       json.RawMessage(hourly),
		"crawl.account_daily_search_visits": json.RawMessage(search),
		"crawl.account_daily_detail_visits": json.RawMessage(detail),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestVisitBudgetCommitsBeforeReturningAndKeepsRunID(t *testing.T) {
	s, _, accountID := quotaService(t)
	runID := int64(17)
	budget := s.newVisitBudget(t.Context(), accountID, &runID, false)
	runID = 90
	if err := budget.Acquire(quota.Search, "首页"); err != nil {
		t.Fatal(err)
	}
	var savedRun, savedAt int64
	if err := s.db.QueryRowContext(t.Context(), `SELECT run_id,at FROM quota_events`).Scan(&savedRun, &savedAt); err != nil || savedRun != 17 || savedAt != s.now().UnixMilli() {
		t.Fatalf("调用返回时记录未提交或runID被外部改写：%d %d %v", savedRun, savedAt, err)
	}
	if state := savedQuotaClock(t, s); state.HighestUTC != savedAt {
		t.Fatal("访问与时钟没有同事务保存")
	}
}

func TestVisitBudgetConcurrentLastSlotsAndKindLimits(t *testing.T) {
	s, _, accountID := quotaService(t)
	applyQuotaLimits(t, s, "5", "20", "20")
	budget := s.newVisitBudget(t.Context(), accountID, nil, false)
	var accepted, denied atomic.Int64
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			err := budget.Acquire(quota.Search, "并发访问")
			if err == nil {
				accepted.Add(1)
			} else if errors.Is(err, web.ErrQuota) {
				denied.Add(1)
			} else {
				t.Errorf("额度竞争发生其他错误：%v", err)
			}
		}()
	}
	workers.Wait()
	if accepted.Load() != 5 || denied.Load() != 7 || quotaRecordCount(t, s) != 5 {
		t.Fatalf("并发突破上限：通过%d，拒绝%d", accepted.Load(), denied.Load())
	}
}

func TestVisitBudgetDeniedStillSavesProtectedTime(t *testing.T) {
	s, now, accountID := quotaService(t)
	applyQuotaLimits(t, s, "1", "20", "20")
	budget := s.newVisitBudget(t.Context(), accountID, nil, false)
	if err := budget.Acquire(quota.Detail, "详情"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(10 * time.Minute)
	err := budget.Acquire(quota.Search, "被小时上限挡住")
	var business *ServiceError
	if !errors.Is(err, web.ErrQuota) || !errors.As(err, &business) || business.Reason.Code != "run.hourly_quota" || business.NextAllowedAt == nil {
		t.Fatalf("额度错误没有结构化原因：%v", err)
	}
	if savedQuotaClock(t, s).HighestUTC != now.UnixMilli() || quotaRecordCount(t, s) != 1 {
		t.Fatal("拒绝没有保存观察时间，或多扣了一次")
	}
	*now = now.Add(-time.Hour)
	if err := budget.Acquire(quota.Search, "回调时间"); !errors.Is(err, web.ErrQuota) || quotaRecordCount(t, s) != 1 {
		t.Fatalf("系统时间倒退恢复了额度：%v", err)
	}
}

func TestVisitBudgetManualExceptionStillCountsAndCancellationDoesNot(t *testing.T) {
	s, _, accountID := quotaService(t)
	applyQuotaLimits(t, s, "1", "1", "1")
	ordinary := s.newVisitBudget(t.Context(), accountID, nil, false)
	if err := ordinary.Acquire(quota.Search, "搜索"); err != nil {
		t.Fatal(err)
	}
	manual := s.newVisitBudget(t.Context(), accountID, nil, true)
	for _, kind := range []quota.Kind{quota.Search, quota.Search, quota.Detail, quota.Detail} {
		if err := manual.Acquire(kind, "人工会话的程序访问"); err != nil {
			t.Fatalf("人工额度例外被拒绝：%v", err)
		}
	}
	if quotaRecordCount(t, s) != 5 {
		t.Fatal("人工额度例外没有照样计数")
	}
	if err := ordinary.Acquire(quota.Detail, "普通详情"); !errors.Is(err, web.ErrQuota) {
		t.Fatalf("人工操作后普通额度仍被放行：%v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.newVisitBudget(canceled, accountID, nil, true).Acquire(quota.Search, "取消"); !errors.Is(err, context.Canceled) || quotaRecordCount(t, s) != 5 {
		t.Fatalf("取消后仍写额度：%v", err)
	}
}

func TestVisitBudgetStorageFailuresNeverBecomeQuotaOrAllowAccess(t *testing.T) {
	for _, mutation := range []string{
		`DELETE FROM meta WHERE key='quota.clock_initialized'`,
		`UPDATE meta SET value='broken' WHERE key='quota.clock'`,
		`CREATE TRIGGER fail_quota_clock BEFORE UPDATE ON meta WHEN NEW.key='quota.clock' BEGIN SELECT RAISE(FAIL,'clock write failure'); END`,
		`CREATE TABLE deferred_guard(account_id INTEGER REFERENCES accounts(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_quota_commit AFTER INSERT ON quota_events BEGIN INSERT INTO deferred_guard(account_id) VALUES(-1); END`,
	} {
		t.Run(mutation, func(t *testing.T) {
			s, _, accountID := quotaService(t)
			if _, err := s.db.ExecContext(t.Context(), mutation); err != nil {
				t.Fatal(err)
			}
			err := s.newVisitBudget(t.Context(), accountID, nil, true).Acquire(quota.Search, "不能继续访问")
			var business *ServiceError
			if err == nil || errors.Is(err, web.ErrQuota) || !errors.As(err, &business) || business.Kind != ErrorUnavailable {
				t.Fatalf("存储失败被放行或误作普通额度不足：%v", err)
			}
			if quotaRecordCount(t, s) != 0 {
				t.Fatal("失败事务未回滚访问记录")
			}
		})
	}
}

func TestVisitBudgetDeniedClockFailureIsStorageFailure(t *testing.T) {
	s, now, accountID := quotaService(t)
	applyQuotaLimits(t, s, "1", "20", "20")
	budget := s.newVisitBudget(t.Context(), accountID, nil, false)
	if err := budget.Acquire(quota.Search, "首次"); err != nil {
		t.Fatal(err)
	}
	previous := savedQuotaClock(t, s)
	*now = now.Add(time.Minute)
	if _, err := s.db.ExecContext(t.Context(), `CREATE TRIGGER fail_denied_clock BEFORE UPDATE ON meta WHEN NEW.key='quota.clock' BEGIN SELECT RAISE(FAIL,'denied clock write failure'); END`); err != nil {
		t.Fatal(err)
	}
	err := budget.Acquire(quota.Search, "原本应该额度不足")
	var business *ServiceError
	if err == nil || errors.Is(err, web.ErrQuota) || !errors.As(err, &business) || business.Kind != ErrorUnavailable {
		t.Fatalf("记录时间失败被误判为普通额度不足：%v", err)
	}
	if savedQuotaClock(t, s).HighestUTC != previous.HighestUTC || quotaRecordCount(t, s) != 1 {
		t.Fatal("失败时钟事务有部分写入")
	}
}

func TestVisitBudgetAccountIsolationAndMissingAccount(t *testing.T) {
	s, _, accountID := quotaService(t)
	applyQuotaLimits(t, s, "1", "1", "1")
	if err := s.newVisitBudget(t.Context(), accountID, nil, false).Acquire(quota.Search, "第一个账号"); err != nil {
		t.Fatal(err)
	}
	result, err := s.db.ExecContext(t.Context(), `INSERT INTO accounts(name,state) VALUES('另一个账号','active')`)
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.newVisitBudget(t.Context(), otherID, nil, false).Acquire(quota.Search, "第二个账号"); err != nil || quotaRecordCount(t, s) != 2 {
		t.Fatalf("第一个账号的额度影响了第二个账号：%v", err)
	}
	if err := s.newVisitBudget(t.Context(), otherID+100, nil, true).Acquire(quota.Search, "不存在的账号"); err == nil || errors.Is(err, web.ErrQuota) || quotaRecordCount(t, s) != 2 {
		t.Fatalf("不存在的账号被允许访问：%v", err)
	}
}

func TestLoadQuotaTxUsesProtectedStartAndLeavesCallerTransaction(t *testing.T) {
	s, now, accountID := quotaService(t)
	if _, err := s.settings.Apply(t.Context(), settings.Values{"general.timezone": json.RawMessage(`"Asia/Shanghai"`)}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		at   time.Time
		kind quota.Kind
	}{
		{gateAt("2026-11-09T23:59:00Z"), quota.Search},
		{gateAt("2026-11-10T01:00:00Z"), quota.Search},
		{now.Add(-time.Hour), quota.Search},
		{now.Add(-time.Minute), quota.Detail},
	} {
		if _, err := s.db.ExecContext(t.Context(), `INSERT INTO quota_events(account_id,kind,at) VALUES(?,?,?)`, accountID, row.kind, row.at.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	snapshot, state, effective, err := s.observeNowTx(t.Context(), tx)
	if err != nil {
		t.Fatal(err)
	}
	limits, err := quotaLimits(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	view, visits, err := loadQuotaTx(t.Context(), tx, accountID, state, effective, limits)
	if err != nil || view.HourlyUsed != 1 || view.DailySearchUsed != 2 || view.DailyDetailUsed != 1 || len(visits) != 3 || view.DayStartAt != gateAt("2026-11-10T00:00:00Z").UnixMilli() {
		t.Fatalf("受保护额度快照错误：%+v %v %v", view, visits, err)
	}
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES('quota.reader','caller')`); err != nil {
		t.Fatal("读取额度提前结束了原事务", err)
	}
	if err := appsettings.SaveClock(t.Context(), tx, state); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := s.db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='quota.reader'`).Scan(&value); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("额度读取没有保留调用方回滚权")
	}
}

func TestReadObservedNowTxOnlyReadsTheSavedClock(t *testing.T) {
	s, now, _ := quotaService(t)
	if _, err := s.settings.Apply(t.Context(), settings.Values{"general.timezone": json.RawMessage(`"Asia/Shanghai"`)}); err != nil {
		t.Fatal(err)
	}
	before := savedQuotaClock(t, s)
	for _, observed := range []time.Time{now.Add(-time.Hour), now.Add(6 * time.Hour)} {
		tx, err := s.db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		got, err := readObservedNowTx(t.Context(), tx, observed)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		want := max(observed.UnixMilli(), before.HighestUTC)
		if got.UnixMilli() != want || got.Location() != time.UTC {
			t.Fatalf("只读时间未取墙钟和保护时间较晚者：%v，期望 %d", got, want)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("只读入口提前结束事务：%v", err)
		}
		if after := savedQuotaClock(t, s); !reflect.DeepEqual(before, after) {
			t.Fatal("只读查询更新了最高时间或旧时区窗口")
		}
	}
}

func TestReadObservedNowTxBrokenProtectionNeverFallsBackToWallClock(t *testing.T) {
	for _, mutation := range []string{
		`DELETE FROM meta WHERE key='quota.clock'`,
		`DELETE FROM meta WHERE key='quota.clock_initialized'`,
		`UPDATE meta SET value='0' WHERE key='quota.clock_initialized'`,
		`UPDATE meta SET value='broken' WHERE key='quota.clock'`,
		`UPDATE meta SET value='{}' WHERE key='quota.clock'`,
		`UPDATE meta SET value=json_set(value,'$.unknown',1) WHERE key='quota.clock'`,
		`UPDATE meta SET value=value||' {}' WHERE key='quota.clock'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			s, now, _ := quotaService(t)
			if _, err := s.db.ExecContext(t.Context(), mutation); err != nil {
				t.Fatal(err)
			}
			tx, err := s.db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if got, err := readObservedNowTx(t.Context(), tx, *now); err == nil || !got.IsZero() {
				t.Fatalf("保护记录损坏却回退墙钟：%v，%v", got, err)
			}
		})
	}
}

func TestReadObservedNowTxReturnsCancellationAndTransactionErrors(t *testing.T) {
	s, now, _ := quotaService(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readObservedNowTx(canceled, tx, *now); !errors.Is(err, context.Canceled) {
		t.Fatalf("没有保留取消原因：%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := readObservedNowTx(t.Context(), tx, *now); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("没有保留事务已结束原因：%v", err)
	}
	if _, err := readObservedNowTx(t.Context(), nil, *now); err == nil {
		t.Fatal("空事务未拒绝")
	}
}
