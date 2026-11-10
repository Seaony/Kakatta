package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/settings"
)

func stateRecoveryService(t *testing.T, db *sql.DB, highest, wall int64) *Service {
	t.Helper()
	preferences, err := appsettings.New(t.Context(), db, settings.Environment{TZ: "UTC"}, func() time.Time { return time.UnixMilli(highest) })
	if err != nil {
		t.Fatal(err)
	}
	return &Service{db: db, settings: preferences, now: func() time.Time { return time.UnixMilli(wall) }}
}

func insertRecoveryRun(t *testing.T, db *sql.DB, accountID, startedAt int64, ended bool) int64 {
	t.Helper()
	var end any
	if ended {
		end = startedAt + 1000
	}
	result, err := db.ExecContext(t.Context(), `INSERT INTO run_records(task_name,trigger,account_id,started_at,ended_at,outcome) VALUES('原任务','auto',?,?,?,'success')`, accountID, startedAt, end)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func recoverStateTransaction(ctx context.Context, tx *sql.Tx, service *Service) error {
	snapshot, clock, now, err := service.observeNowTx(ctx, tx)
	if err != nil {
		return err
	}
	if err := recoverRuntimeTx(ctx, tx, snapshot.Values, now.UnixMilli()); err != nil {
		return err
	}
	return appsettings.SaveClock(ctx, tx, clock)
}

func TestRecoveryUsesHighestUTCAndRetainsAccountProtection(t *testing.T) {
	db, _, wall := accountStateDB(t)
	highest := wall + int64(8*time.Hour/time.Millisecond)
	service := stateRecoveryService(t, db, highest, wall)
	marked := insertStateAccount(t, db, AccountNeedsVerification)
	shortGap := insertStateAccount(t, db, AccountExpired)
	unmarked := insertStateAccount(t, db, AccountDenied)
	until := highest + int64(time.Hour/time.Millisecond)
	if _, err := db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=?,verify_level=4,verify_url='http://fixture.invalid/verify',last_cooldown_end_at=3,cooldown_manual_used=1,last_run_ended_at=4,next_gap_seconds=300 WHERE id=?`, until, marked); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE accounts SET last_run_ended_at=5,next_gap_seconds=30 WHERE id IN (?,?)`, shortGap, unmarked); err != nil {
		t.Fatal(err)
	}
	unfinished := insertRecoveryRun(t, db, marked, wall-1000, false)
	finished := insertRecoveryRun(t, db, unmarked, wall-2000, true)
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		if err := saveSessionMarkerTx(ctx, tx, marked, sessionMarker{Version: 1, SessionID: "held", StartedAt: wall - 1000, RunID: &unfinished}); err != nil {
			return err
		}
		if err := saveSessionMarkerTx(ctx, tx, shortGap, sessionMarker{Version: 1, SessionID: "login", StartedAt: wall - 1000}); err != nil {
			return err
		}
		for _, query := range []string{
			`INSERT INTO meta(key,value) VALUES('auth.generation','保留'),('account.cleanup.999','保留'),('other.value','保留')`,
			`INSERT INTO quota_events(account_id,kind,at) VALUES(1,'search',100)`,
			`INSERT INTO ai_calls(provider_id,purpose,at,ok) VALUES(NULL,'analysis',100,1)`,
		} {
			if _, err := tx.ExecContext(ctx, query); err != nil {
				return err
			}
		}
		return nil
	})
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error { return recoverStateTransaction(ctx, tx, service) })
	account := stateAccount(t, db, marked)
	if account.LastRunEndedAt == nil || *account.LastRunEndedAt != highest || account.NextGapSeconds == nil || *account.NextGapSeconds != 300 {
		t.Fatalf("重启减少间隔或使用倒退时间：%+v", account)
	}
	if account.State != AccountNeedsVerification || *account.CooldownUntil != until || account.VerifyLevel != 4 || !account.CooldownManualUsed || account.VerifyURL == nil || *account.LastCooldownEndAt != 3 {
		t.Fatalf("重启清掉账号风险保护：%+v", account)
	}
	if account := stateAccount(t, db, shortGap); *account.LastRunEndedAt != highest || *account.NextGapSeconds != 180 || account.State != AccountExpired {
		t.Fatal("重启没有从恢复时刻补足配置最小间隔")
	}
	if account := stateAccount(t, db, unmarked); *account.LastRunEndedAt != 5 || *account.NextGapSeconds != 30 || account.State != AccountDenied {
		t.Fatal("没有会话标记的账号也被重置")
	}
	var endedAt int64
	var outcome, failure string
	if err := db.QueryRowContext(t.Context(), `SELECT ended_at,outcome,failure_kind FROM run_records WHERE id=?`, unfinished).Scan(&endedAt, &outcome, &failure); err != nil || endedAt != highest || outcome != "failed" || failure != "restart" {
		t.Fatalf("未结束运行没有按保护时间结算：%d %s %s %v", endedAt, outcome, failure, err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT ended_at,outcome FROM run_records WHERE id=?`, finished).Scan(&endedAt, &outcome); err != nil || endedAt != wall-1000 || outcome != "success" {
		t.Fatal("已完成运行被改写")
	}
	for _, test := range []struct {
		query string
		want  int64
	}{
		{`SELECT count(*) FROM meta WHERE key LIKE 'runtime.session.%'`, 0},
		{`SELECT count(*) FROM meta WHERE key IN ('auth.generation','account.cleanup.999','other.value') AND value='保留'`, 3},
		{`SELECT count(*) FROM quota_events`, 1},
		{`SELECT count(*) FROM ai_calls`, 1},
	} {
		var count int64
		if err := db.QueryRowContext(t.Context(), test.query).Scan(&count); err != nil || count != test.want {
			t.Fatalf("保护数据或恢复标记错误：%s %d %v", test.query, count, err)
		}
	}
	var raw string
	if err := db.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, appsettings.ClockKey).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var clock struct {
		HighestUTC int64 `json:"highest_utc"`
	}
	if json.Unmarshal([]byte(raw), &clock) != nil || clock.HighestUTC != highest {
		t.Fatal("恢复重建或降低了额度时钟")
	}
}

func TestRecoveryRejectsBrokenMarkersBeforeChangingRecords(t *testing.T) {
	for _, test := range []struct {
		name, key, raw string
	}{
		{"坏JSON", "runtime.session.1", `{`},
		{"未知字段", "runtime.session.1", `{"version":1,"session_id":"old","started_at":1,"run_id":null,"extra":true}`},
		{"错误账号键", "runtime.session.01", `{"version":1,"session_id":"old","started_at":1,"run_id":null}`},
		{"账号不存在", "runtime.session.999", `{"version":1,"session_id":"old","started_at":1,"run_id":null}`},
		{"运行不存在", "runtime.session.1", `{"version":1,"session_id":"old","started_at":1,"run_id":999}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, values, now := accountStateDB(t)
			id := insertStateAccount(t, db, AccountActive)
			run := insertRecoveryRun(t, db, id, now-1000, false)
			if _, err := db.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES(?,?)`, test.key, test.raw); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := recoverRuntimeTx(t.Context(), tx, values, now); err == nil {
				t.Fatal("坏恢复记录被当成没有运行")
			}
			var ended sql.NullInt64
			if err := tx.QueryRowContext(t.Context(), `SELECT ended_at FROM run_records WHERE id=?`, run).Scan(&ended); err != nil || ended.Valid {
				t.Fatal("验证标记完成前已经改写运行")
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecoveryClockAndStorageFailuresPreserveMarkers(t *testing.T) {
	for _, mutation := range []string{
		`DELETE FROM meta WHERE key='quota.clock'`,
		`UPDATE meta SET value='broken' WHERE key='quota.clock'`,
		`CREATE TRIGGER recovery_failure BEFORE UPDATE OF last_run_ended_at ON accounts BEGIN SELECT RAISE(ABORT,'假间隔写失败'); END`,
	} {
		t.Run(mutation, func(t *testing.T) {
			db, _, now := accountStateDB(t)
			service := stateRecoveryService(t, db, now, now)
			id := insertStateAccount(t, db, AccountActive)
			run := insertRecoveryRun(t, db, id, now-1000, false)
			stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
				return saveSessionMarkerTx(ctx, tx, id, sessionMarker{Version: 1, SessionID: "old", StartedAt: now - 1000, RunID: &run})
			})
			if _, err := db.ExecContext(t.Context(), mutation); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := recoverStateTransaction(t.Context(), tx, service); err == nil {
				t.Fatal("恢复失败仍然放行")
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			var count int64
			if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM meta WHERE key LIKE 'runtime.session.%'`).Scan(&count); err != nil || count != 1 {
				t.Fatal("失败后清掉了保底间隔标记")
			}
			var ended sql.NullInt64
			if err := db.QueryRowContext(t.Context(), `SELECT ended_at FROM run_records WHERE id=?`, run).Scan(&ended); err != nil || ended.Valid {
				t.Fatal("恢复失败后仍提交部分结果")
			}
		})
	}
}
