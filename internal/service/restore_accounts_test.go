package service

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
)

func TestRestoredAccountsShareSignOutRulesAndPreserveProtectedFacts(t *testing.T) {
	db, _, now := accountStateDB(t)
	const originalReason = `{"code":"historical.reason","args":{"large":9007199254740993123456789,"other":1e1000}}`
	for _, test := range []struct {
		state AccountState
		until *int64
		want  AccountState
		mark  bool
	}{
		{AccountActive, nil, AccountSignedOut, false},
		{AccountNeedsVerification, nil, AccountSignedOut, false},
		{AccountExpired, nil, AccountSignedOut, false},
		{AccountSignedOut, nil, AccountSignedOut, false},
		{AccountCooling, new(now + 3600000), AccountCooling, true},
		{AccountDenied, new(now + 3600000), AccountDenied, true},
		{AccountExpired, new(now + 3600000), AccountCooling, true},
		{AccountCooling, new(now - 1000), AccountSignedOut, false},
		{AccountDenied, new(now - 1000), AccountSignedOut, false},
	} {
		name := fmt.Sprintf("%s-%v", test.state, test.mark)
		t.Run(name, func(t *testing.T) {
			id := insertStateAccount(t, db, test.state)
			if _, err := db.ExecContext(t.Context(), `UPDATE accounts SET state_reason=?,state_changed_at=111,cooldown_until=?,verify_url='old-verification',verify_level=4,cooldown_manual_used=1,last_cooldown_end_at=222,last_run_ended_at=333,next_gap_seconds=240,nickname='旧昵称' WHERE id=?`, originalReason, test.until, id); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"account_verify", "account_login", "account_deny"} {
				if _, err := db.ExecContext(t.Context(), `INSERT INTO system_events(kind,subject_id,message,created_at) VALUES(?,?,?,1)`, kind, id, originalReason); err != nil {
					t.Fatal(err)
				}
			}
			stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error { return SignOutRestoredAccountsTx(ctx, tx, now) })
			var state AccountState
			var reason, nickname string
			var changed, level, lastEnd, runEnd, gap int64
			var manual bool
			var until, verify *int64
			if err := db.QueryRowContext(t.Context(), `SELECT state,state_reason,state_changed_at,cooldown_until,verify_level,cooldown_manual_used,last_cooldown_end_at,last_run_ended_at,next_gap_seconds,nickname,CASE WHEN verify_url IS NULL THEN NULL ELSE 1 END FROM accounts WHERE id=?`, id).
				Scan(&state, &reason, &changed, &until, &level, &manual, &lastEnd, &runEnd, &gap, &nickname, &verify); err != nil {
				t.Fatal(err)
			}
			if state != test.want || level != 4 || !manual || lastEnd != 222 || runEnd != 333 || gap != 240 || nickname != "旧昵称" || verify != nil {
				t.Fatalf("恢复清掉了保护事实或保留了待验证：%s %d %v %d %d %d %q %v", state, level, manual, lastEnd, runEnd, gap, nickname, verify)
			}
			if test.want == test.state && (reason != originalReason || changed != 111) {
				t.Fatalf("不必要地改写了原原因或状态时间：%s %d", reason, changed)
			}
			if test.mark && (until == nil || *until != *test.until) {
				t.Fatal("恢复缩短了未结束的暂停")
			}
			stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
				marker, err := accountLoggedOutTx(ctx, tx, id)
				if err == nil && marker != test.mark {
					t.Fatalf("恢复的退出标记不符：%v", marker)
				}
				return err
			})
			rows, err := db.QueryContext(t.Context(), `SELECT kind,resolved_at,message FROM system_events WHERE subject_id=?`, id)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var kind, message string
				var resolved *int64
				if err := rows.Scan(&kind, &resolved, &message); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				keepDeny := kind == "account_deny" && test.want == AccountDenied
				if keepDeny != (resolved == nil) || message != originalReason {
					rows.Close()
					t.Fatalf("账号事件没有按真实状态结算或历史原文被改：%s %v %s", kind, resolved, message)
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			rows.Close()
		})
	}
}

func TestRestoreAccountSignOutIsOneCallerTransaction(t *testing.T) {
	db, _, now := accountStateDB(t)
	first := insertStateAccount(t, db, AccountActive)
	second := insertStateAccount(t, db, AccountCooling)
	if _, err := db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=?,verify_url='keep' WHERE id=?`, now+3600000, second); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO system_events(kind,subject_id,message) VALUES('account_verify',?,'{"code":"old","args":{}}')`, second); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER restore_account_event_failure BEFORE UPDATE OF resolved_at ON system_events BEGIN SELECT RAISE(ABORT,'假事件失败'); END`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := SignOutRestoredAccountsTx(t.Context(), tx, now); err == nil {
		t.Fatal("恢复事件失败被忽略")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal("恢复入口擅自提交或回滚调用者事务", err)
	}
	if a := stateAccount(t, db, first); a.State != AccountActive {
		t.Fatal("失败前改过的账号没一起回滚")
	}
	if a := stateAccount(t, db, second); a.State != AccountCooling || a.VerifyURL == nil {
		t.Fatal("保护账号没有一起回滚")
	}
	var markers int64
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM meta WHERE key LIKE 'account.logged_out.%'`).Scan(&markers); err != nil || markers != 0 {
		t.Fatal("回滚后仍留下恢复退出标记")
	}
}

func TestRestoredCooldownBlocksScanAfterDatabaseReopenAndEndsSignedOut(t *testing.T) {
	s := protectionTestService(t)
	old := s.now()
	id := accountTestInsert(t, s, "恢复冷却", AccountCooling, 1)
	until := old.Add(time.Hour).UnixMilli()
	if _, err := s.db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=?,verify_level=4,cooldown_manual_used=0 WHERE id=?`, until, id); err != nil {
		t.Fatal(err)
	}
	stateTx(t, s.db, func(ctx context.Context, tx *sql.Tx) error {
		return SignOutRestoredAccountsTx(ctx, tx, old.UnixMilli())
	})
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(t.Context(), filepath.Join(s.dataDir, "accounts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	clock := old.Add(time.Minute)
	preferences, err := appsettings.New(t.Context(), reopened.DB(), settings.Environment{TZ: "UTC"}, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	restored := &Service{db: reopened.DB(), settings: preferences, now: func() time.Time { return clock }}
	restored.initRuntime()
	defer restored.cancel(context.Canceled)
	_, _, _, blocked, err := restored.manualPreflight(t.Context(), id, SessionLogin)
	if err != nil || blocked == nil || blocked.Reason.Code != "run.account_cooling" || blocked.NextAllowedAt == nil || *blocked.NextAllowedAt != until {
		t.Fatalf("恢复后重启绕过冷却扫码：%+v %v", blocked, err)
	}
	clock = time.UnixMilli(until)
	stateTx(t, restored.db, func(ctx context.Context, tx *sql.Tx) error {
		_, err := advanceAccountStatesTx(ctx, tx, clock.UnixMilli())
		return err
	})
	if account := stateAccount(t, restored.db, id); account.State != AccountSignedOut || account.LastCooldownEndAt == nil || *account.LastCooldownEndAt != until {
		t.Fatal("恢复的账号冷却结束后假称已有登录态")
	}
	_, _, _, blocked, err = restored.manualPreflight(t.Context(), id, SessionLogin)
	if err != nil || blocked != nil {
		t.Fatalf("冷却结束后仍拦住重新扫码：%+v %v", blocked, err)
	}
}
