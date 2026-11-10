package appsettings

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/settings"
)

func TestSnapshotTxReadsCallerValuesWithoutTakingOwnership(t *testing.T) {
	proxy := "127.0.0.1"
	s, db := testStore(t, settings.Environment{TZ: "UTC", TrustedProxies: &proxy}, nil)
	if db.Stats().MaxOpenConnections != 1 {
		t.Fatal("此回归必须使用单连接真实SQLite")
	}
	beforeClock := clockState(t, db)
	s.now = func() time.Time { t.Fatal("读取设置不应观察时钟"); return time.Time{} }
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for key, value := range map[string]string{
		"general.language": `"en"`, "general.timezone": `"Asia/Tokyo"`,
		"security.trusted_proxies": `["10.0.0.1"]`, "ai.image_count": `5`,
	} {
		if _, err := tx.ExecContext(t.Context(), `INSERT INTO settings(key,value_json) VALUES(?,?)`, key, value); err != nil {
			t.Fatal(err)
		}
	}
	// 误用Get会在单连接池等待自己的事务，到期限后失败。
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	waits := db.Stats().WaitCount
	snapshot, err := s.SnapshotTx(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if string(snapshot.Values["general.language"]) != `"en"` || string(snapshot.Values["general.timezone"]) != `"Asia/Tokyo"` || snapshot.Sources["general.language"] != settings.SourceStored {
		t.Fatalf("没有读到本事务未提交的设置：%+v", snapshot)
	}
	if string(snapshot.Values["security.trusted_proxies"]) != `["127.0.0.1"]` || snapshot.Sources["security.trusted_proxies"] != settings.SourceEnvironment {
		t.Fatal("事务读取没有应用只读环境覆盖")
	}
	if len(snapshot.Warnings) != 1 || snapshot.Warnings[0].Key != "ai.image_count" || db.Stats().WaitCount != waits {
		t.Fatal("没有复用设置校验，或额外申请了连接")
	}
	if _, err := tx.ExecContext(t.Context(), `UPDATE settings SET value_json='"zh-CN"' WHERE key='general.language'`); err != nil {
		t.Fatalf("读取设置结束了调用方事务：%v", err)
	}
	latest, err := s.SnapshotTx(ctx, tx)
	if err != nil || string(latest.Values["general.language"]) != `"zh-CN"` || string(snapshot.Values["general.language"]) != `"en"` {
		t.Fatal("没有读到当前事务后续修改，或污染了上一次快照")
	}
	// 时区改动只用于验证读取，避免测试提交一个没保护旧日界的时区变更。
	if _, err := tx.ExecContext(t.Context(), `DELETE FROM settings WHERE key='general.timezone'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("提交权不再属于调用方：%v", err)
	}
	var saved string
	if err := db.QueryRowContext(t.Context(), `SELECT value_json FROM settings WHERE key='general.language'`).Scan(&saved); err != nil || saved != `"zh-CN"` {
		t.Fatalf("调用方提交没有保存事务内修改：%q，%v", saved, err)
	}
	if !reflect.DeepEqual(beforeClock, clockState(t, db)) {
		t.Fatal("事务快照修改了额度时钟")
	}
}

func TestSnapshotTxErrorsLeaveCallerTransactionUsable(t *testing.T) {
	s, db := testStore(t, settings.Environment{TZ: "UTC"}, nil)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO settings(key,value_json) VALUES('ai.image_count','0')`); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`0`, `{`} {
		if _, err := tx.ExecContext(t.Context(), `UPDATE settings SET value_json=? WHERE key='ai.image_count'`, bad); err != nil {
			t.Fatal(err)
		}
		snapshot, err := s.SnapshotTx(t.Context(), tx)
		var invalid *settings.ValidationError
		if !errors.As(err, &invalid) || snapshot.Values != nil {
			t.Fatalf("坏设置被跳过或返回了部分快照：%q，%v", bad, err)
		}
	}
	if _, err := tx.ExecContext(t.Context(), `UPDATE settings SET value_json='4' WHERE key='ai.image_count'`); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if snapshot, err := s.SnapshotTx(canceled, tx); !errors.Is(err, context.Canceled) || snapshot.Values != nil {
		t.Fatalf("取消未正确返回：%v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO settings(key,value_json) VALUES('general.language','"en"')`); err != nil {
		t.Fatalf("快照错误取消了原事务：%v", err)
	}
	if snapshot, err := s.SnapshotTx(t.Context(), tx); err != nil || string(snapshot.Values["general.language"]) != `"en"` {
		t.Fatalf("修复设置后原事务不能继续读取：%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM settings`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("回滚后仍留下快照读取期间的改动：%d，%v", count, err)
	}
	if _, err := s.SnapshotTx(t.Context(), tx); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("已回滚事务没有返回原错误：%v", err)
	}
}

func TestSnapshotTxDoesNotReadOrRepairQuotaClock(t *testing.T) {
	for _, mutation := range []string{
		`DELETE FROM meta WHERE key IN ('quota.clock','quota.clock_initialized')`,
		`UPDATE meta SET value='broken' WHERE key='quota.clock'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			empty := ""
			s, db := testStore(t, settings.Environment{TZ: "UTC", TrustedProxies: &empty}, nil)
			original := clockState(t, db)
			s.now = func() time.Time { t.Fatal("事务设置读取不能观察时钟"); return time.Time{} }
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(t.Context(), mutation); err != nil {
				t.Fatal(err)
			}
			var rawBefore, rawAfter string
			beforeErr := tx.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, ClockKey).Scan(&rawBefore)
			snapshot, err := s.SnapshotTx(t.Context(), tx)
			if err != nil || string(snapshot.Values["security.trusted_proxies"]) != `[]` || snapshot.Sources["security.trusted_proxies"] != settings.SourceEnvironment {
				t.Fatalf("快照被额度时钟影响或空环境覆盖丢失：%v", err)
			}
			afterErr := tx.QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key=?`, ClockKey).Scan(&rawAfter)
			if rawBefore != rawAfter || !errors.Is(afterErr, beforeErr) {
				t.Fatal("快照重建或保存了额度时钟")
			}
			if _, err := tx.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES('snapshot.tx','caller')`); err != nil {
				t.Fatal(err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(original, clockState(t, db)) {
				t.Fatal("回滚没有恢复原时钟")
			}
		})
	}
}
