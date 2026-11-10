package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/store"
)

func txStore(t *testing.T) (context.Context, Store) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "kakatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if db.DB().Stats().MaxOpenConnections != 1 {
		t.Fatal("测试必须使用单连接数据库")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx, Store{DB: db.DB(), Now: func() time.Time { return time.Date(2026, 11, 11, 8, 0, 0, 123000000, time.UTC) }}
}

func beginEventTx(t *testing.T, ctx context.Context, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

func accountState(t *testing.T, ctx context.Context, db *sql.DB, id int64) string {
	t.Helper()
	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM accounts WHERE id=?`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func eventCounts(t *testing.T, ctx context.Context, db *sql.DB) (total, unresolved int) {
	t.Helper()
	if err := db.QueryRowContext(ctx, `SELECT count(*),count(CASE WHEN resolved_at IS NULL THEN 1 END) FROM system_events`).Scan(&total, &unresolved); err != nil {
		t.Fatal(err)
	}
	return total, unresolved
}

func changeAccount(t *testing.T, ctx context.Context, tx *sql.Tx, id int64, state string) {
	t.Helper()
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET state=? WHERE id=?`, state, id); err != nil {
		t.Fatal(err)
	}
}

func TestAccountAndEventsShareCommitAndRollback(t *testing.T) {
	ctx, s := txStore(t)
	result, err := s.DB.ExecContext(ctx, `INSERT INTO accounts(name,state) VALUES('测试账号','active')`)
	if err != nil {
		t.Fatal(err)
	}
	accountID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	message := locales.Message{Code: "account.verification_required"}
	tx := beginEventTx(t, ctx, s.DB)
	changeAccount(t, ctx, tx, accountID, "needs_verification")
	if fresh, err := s.RaiseTx(ctx, tx, "account_verify", accountID, message); err != nil || !fresh {
		t.Fatalf("事务内新建事件失败：%v", err)
	}
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT message FROM system_events`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved locales.Message
	if err := json.Unmarshal([]byte(raw), &saved); err != nil || saved.Code != message.Code || saved.Args == nil {
		t.Fatalf("事务事件没有保留原因代码和参数：%s %v", raw, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal("事件入口擅自结束了事务", err)
	}
	if total, _ := eventCounts(t, ctx, s.DB); total != 0 || accountState(t, ctx, s.DB, accountID) != "active" {
		t.Fatal("账号与事件没有一起回滚")
	}
	tx = beginEventTx(t, ctx, s.DB)
	changeAccount(t, ctx, tx, accountID, "needs_verification")
	if fresh, err := s.RaiseTx(ctx, tx, "account_verify", accountID, message); err != nil || !fresh {
		t.Fatalf("回滚后不能重新新建事件：%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("事件入口擅自结束了事务", err)
	}
	if total, unresolved := eventCounts(t, ctx, s.DB); total != 1 || unresolved != 1 || accountState(t, ctx, s.DB, accountID) != "needs_verification" {
		t.Fatal("账号与事件没有一起提交")
	}
	tx = beginEventTx(t, ctx, s.DB)
	changeAccount(t, ctx, tx, accountID, "active")
	if changed, err := s.ResolveTx(ctx, tx, "account_verify", accountID); err != nil || !changed {
		t.Fatalf("事务内解决事件失败：%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, unresolved := eventCounts(t, ctx, s.DB); unresolved != 1 || accountState(t, ctx, s.DB, accountID) != "needs_verification" {
		t.Fatal("解决事件和账号状态没有一起回滚")
	}
	tx = beginEventTx(t, ctx, s.DB)
	changeAccount(t, ctx, tx, accountID, "active")
	if changed, err := s.ResolveTx(ctx, tx, "account_verify", accountID); err != nil || !changed {
		t.Fatalf("回滚后不能重新解决事件：%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var resolvedAt int64
	if err := s.DB.QueryRowContext(ctx, `SELECT resolved_at FROM system_events`).Scan(&resolvedAt); err != nil || resolvedAt != s.now() {
		t.Fatal("解决时间没有保存为UTC毫秒", err)
	}
	if _, unresolved := eventCounts(t, ctx, s.DB); unresolved != 0 || accountState(t, ctx, s.DB, accountID) != "active" {
		t.Fatal("解决事件和账号状态没有一起提交")
	}
}

func TestTxEventDeduplicationAndNewOccurrence(t *testing.T) {
	ctx, s := txStore(t)
	tx := beginEventTx(t, ctx, s.DB)
	message := locales.Message{Code: "ai.provider_error"}
	for i, input := range []struct {
		kind    string
		subject int64
		fresh   bool
	}{{"ai_error", 1, true}, {"ai_error", 1, false}, {"ai_error", 2, true}, {"task_failure_pause", 1, true}} {
		fresh, err := s.RaiseTx(ctx, tx, input.kind, input.subject, message)
		if err != nil || fresh != input.fresh {
			t.Fatalf("第%d次去重结果错误：%v %v", i+1, fresh, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx = beginEventTx(t, ctx, s.DB)
	if fresh, err := s.RaiseTx(ctx, tx, "ai_error", 1, message); err != nil || fresh {
		t.Fatalf("未解决事件跨事务重复新建：%v", err)
	}
	if changed, err := s.ResolveTx(ctx, tx, "ai_error", 1); err != nil || !changed {
		t.Fatalf("未能解决已有事件：%v", err)
	}
	if changed, err := s.ResolveTx(ctx, tx, "ai_error", 1); err != nil || changed {
		t.Fatalf("重复解决错误：%v", err)
	}
	if fresh, err := s.RaiseTx(ctx, tx, "ai_error", 1, message); err != nil || !fresh {
		t.Fatalf("解决后没有新建再次发生的事件：%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if total, unresolved := eventCounts(t, ctx, s.DB); total != 4 || unresolved != 3 {
		t.Fatalf("去重/再次发生的数量错误：%d %d", total, unresolved)
	}
}

func TestTxEntryPointsDoNotNeedStoreConnection(t *testing.T) {
	ctx, owner := txStore(t)
	s := Store{Now: owner.Now}
	tx := beginEventTx(t, ctx, owner.DB)
	if fresh, err := s.RaiseTx(ctx, tx, "ai_budget", 0, locales.Message{Code: "ai.daily_limit"}); err != nil || !fresh {
		t.Fatalf("事务入口依赖Store.DB：%v", err)
	}
	if changed, err := s.ResolveTx(ctx, tx, "ai_budget", 0); err != nil || !changed {
		t.Fatalf("事务入口依赖Store.DB：%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("事务入口提前提交或回滚", err)
	}
}

func TestTxEntryPointErrorsAreReturned(t *testing.T) {
	ctx, s := txStore(t)
	message := locales.Message{Code: "test.event"}
	if fresh, err := s.RaiseTx(ctx, nil, "ai_error", 1, message); err == nil || fresh {
		t.Fatal("空事务没有返回错误")
	}
	if changed, err := s.ResolveTx(ctx, nil, "ai_error", 1); err == nil || changed {
		t.Fatal("空事务没有返回错误")
	}
	for _, commit := range []bool{true, false} {
		tx := beginEventTx(t, ctx, s.DB)
		var err error
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			t.Fatal(err)
		}
		if fresh, err := s.RaiseTx(ctx, tx, "ai_error", 1, message); !errors.Is(err, sql.ErrTxDone) || fresh {
			t.Fatalf("已结束事务的错误被吞：%v", err)
		}
		if changed, err := s.ResolveTx(ctx, tx, "ai_error", 1); !errors.Is(err, sql.ErrTxDone) || changed {
			t.Fatalf("已结束事务的错误被吞：%v", err)
		}
	}
	tx := beginEventTx(t, ctx, s.DB)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if fresh, err := s.RaiseTx(cancelled, tx, "ai_error", 1, message); !errors.Is(err, context.Canceled) || fresh {
		t.Fatalf("取消错误被吞：%v", err)
	}
	if changed, err := s.ResolveTx(cancelled, tx, "ai_error", 1); !errors.Is(err, context.Canceled) || changed {
		t.Fatalf("取消错误被吞：%v", err)
	}
	if fresh, err := s.RaiseTx(ctx, tx, "ai_error", 1, locales.Message{Code: "invalid", Args: map[string]any{"unsupported": make(chan int)}}); err == nil || fresh {
		t.Fatal("无法编码的原因被当成成功")
	}
	if fresh, err := s.RaiseTx(ctx, tx, "unknown_kind", 1, message); err == nil || fresh {
		t.Fatal("数据库约束错误被吞")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("失败入口擅自结束调用者事务", err)
	}
	if total, _ := eventCounts(t, ctx, s.DB); total != 0 {
		t.Fatal("失败操作仍写入了事件")
	}
}
