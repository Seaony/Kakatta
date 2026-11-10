package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/store"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func accountStateDB(t *testing.T) (*sql.DB, settings.Values, int64) {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	snapshot, err := settings.Resolve(nil, settings.Environment{TZ: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	return database.DB(), snapshot.Values, time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC).UnixMilli()
}

func insertStateAccount(t *testing.T, db *sql.DB, state AccountState) int64 {
	t.Helper()
	result, err := db.ExecContext(t.Context(), `INSERT INTO accounts(name,state,created_at) VALUES('测试账号',?,1)`, state)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func stateTx(t *testing.T, db *sql.DB, action func(context.Context, *sql.Tx) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := action(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("转换入口提前结束了调用方事务", err)
	}
}

func stateAccount(t *testing.T, db *sql.DB, id int64) Account {
	t.Helper()
	var account Account
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) (err error) {
		account, err = loadAccountTx(ctx, tx, id)
		return err
	})
	return account
}

func stateEventCount(t *testing.T, db *sql.DB, kind string, unresolved bool) int64 {
	t.Helper()
	query := `SELECT count(*) FROM system_events WHERE kind=?`
	if unresolved {
		query += ` AND resolved_at IS NULL`
	}
	var count int64
	if err := db.QueryRowContext(t.Context(), query, kind).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestAccountVerificationLevelsAndEvents(t *testing.T) {
	db, values, now := accountStateDB(t)
	id := insertStateAccount(t, db, AccountActive)
	url := "http://fixture.invalid/verification"
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		account, err := applyAccountRiskTx(ctx, tx, id, web.ErrNeedVerify, url, values, now)
		if err == nil && (account.State != AccountNeedsVerification || account.VerifyLevel != 1 || account.VerifyURL == nil || *account.VerifyURL != url) {
			t.Fatalf("首次验证状态错误：%+v", account)
		}
		return err
	})
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		account, err := applyAccountRiskTx(ctx, tx, id, web.ErrNeedVerify, url, values, now+1000)
		if err == nil && (account.VerifyLevel != 1 || account.StateChangedAt == nil || *account.StateChangedAt != now) {
			t.Fatal("重复验证增加等级或改写首次状态时间")
		}
		return err
	})
	if stateEventCount(t, db, "account_verify", false) != 1 {
		t.Fatal("同一次验证重复产生事件")
	}
	verified := now + 2000
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		account, err := completeVerificationTx(ctx, tx, id, values, verified)
		if err == nil && (account.State != AccountCooling || account.CooldownUntil == nil || *account.CooldownUntil != verified+int64(2*time.Hour/time.Millisecond) || account.CooldownManualUsed || account.VerifyURL != nil) {
			t.Fatalf("验证后冷却错误：%+v", account)
		}
		return err
	})
	if stateEventCount(t, db, "account_verify", true) != 0 {
		t.Fatal("通过验证后仍留下未解决的验证事件")
	}
	triggered := verified + int64(5*time.Minute/time.Millisecond)
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		account, err := applyAccountRiskTx(ctx, tx, id, web.ErrNeedVerify, url, values, triggered)
		if err == nil && (account.VerifyLevel != 2 || account.LastCooldownEndAt == nil || *account.LastCooldownEndAt != triggered || account.CooldownUntil != nil) {
			t.Fatalf("冷却内再次验证没有加级或记录打断：%+v", account)
		}
		return err
	})
	if stateEventCount(t, db, "account_verify", false) != 2 || stateEventCount(t, db, "account_verify", true) != 1 {
		t.Fatal("冷却内新的验证没有单独提醒")
	}
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		account, err := completeVerificationTx(ctx, tx, id, values, triggered+1000)
		if err == nil && *account.CooldownUntil != triggered+1000+int64(4*time.Hour/time.Millisecond) {
			t.Fatal("第二级冷却未翻倍")
		}
		return err
	})
}

func TestAccountVerificationResetAndMaximum(t *testing.T) {
	for _, test := range []struct {
		name      string
		lastDelta int64
		level     int64
		wantLevel int64
	}{
		{"窗口内", int64(24*time.Hour/time.Millisecond) - 1, 4, 5},
		{"正好结束窗口", int64(24 * time.Hour / time.Millisecond), 4, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, values, now := accountStateDB(t)
			id := insertStateAccount(t, db, AccountActive)
			if _, err := db.ExecContext(t.Context(), `UPDATE accounts SET verify_level=?,last_cooldown_end_at=? WHERE id=?`, test.level, now-test.lastDelta, id); err != nil {
				t.Fatal(err)
			}
			stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
				account, err := applyAccountRiskTx(ctx, tx, id, web.ErrNeedVerify, "http://fixture.invalid/verify", values, now)
				if err == nil && account.VerifyLevel != test.wantLevel {
					t.Fatalf("等级=%d，期望%d", account.VerifyLevel, test.wantLevel)
				}
				return err
			})
			stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
				account, err := completeVerificationTx(ctx, tx, id, values, now+1000)
				if err == nil && (*account.CooldownUntil <= now+1000 || *account.CooldownUntil > now+1000+int64(24*time.Hour/time.Millisecond)) {
					t.Fatal("加倍冷却越过最大值或发生溢出")
				}
				return err
			})
		})
	}
}

func insertStateTask(t *testing.T, db *sql.DB, accountID int64, pinned bool) int64 {
	t.Helper()
	result, err := db.ExecContext(t.Context(), `INSERT INTO tasks(name,keyword,account_id,account_pinned,max_pages,interval_minutes,judge_mode,notify_policy,sort_index,next_run_at) VALUES('任务','相机',?,?,1,15,'keyword','recommended',0,0)`, accountID, pinned)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAccountExpiryPreservesCooldownAndReassignsOnlyAutomaticTasks(t *testing.T) {
	db, values, now := accountStateDB(t)
	id := insertStateAccount(t, db, AccountCooling)
	other := insertStateAccount(t, db, AccountActive)
	until := now + int64(time.Hour/time.Millisecond)
	if _, err := db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=?,cooldown_manual_used=1,verify_level=3 WHERE id=?`, until, id); err != nil {
		t.Fatal(err)
	}
	automatic, pinned := insertStateTask(t, db, id, false), insertStateTask(t, db, id, true)
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		account, err := applyAccountRiskTx(ctx, tx, id, web.ErrLoginExpired, "", values, now)
		if err == nil && (account.State != AccountExpired || account.CooldownUntil == nil || *account.CooldownUntil != until || !account.CooldownManualUsed || account.VerifyLevel != 3) {
			t.Fatalf("失效清掉了原保护：%+v", account)
		}
		return err
	})
	for task, want := range map[int64]int64{automatic: other, pinned: id} {
		var accountID, next int64
		if err := db.QueryRowContext(t.Context(), `SELECT account_id,next_run_at FROM tasks WHERE id=?`, task).Scan(&accountID, &next); err != nil || accountID != want || task == automatic && next != now {
			t.Fatalf("任务绑定/重排错误：%d %d %v", task, accountID, err)
		}
	}
	nickname, avatar := "新的昵称", "http://fixture.invalid/avatar.jpg"
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		account, err := completeLoginTx(ctx, tx, id, &nickname, &avatar, now+1000)
		var problem *ServiceError
		if !errors.As(err, &problem) || problem.Reason.Code != "run.account_cooling" || account.State != AccountExpired || *account.CooldownUntil != until || !account.CooldownManualUsed || account.VerifyLevel != 3 {
			t.Fatalf("重新登录绕过原冷却：%+v %v", account, err)
		}
		return nil
	})
	if stateEventCount(t, db, "account_login", true) != 1 {
		t.Fatal("被冷却拦住的登录提前解决了失效事件")
	}
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		account, err := completeLoginTx(ctx, tx, id, &nickname, &avatar, until+5000)
		if err == nil && (account.State != AccountActive || account.CooldownUntil != nil || account.LastCooldownEndAt == nil || *account.LastCooldownEndAt != until || account.CooldownManualUsed || account.Nickname == nil || *account.Nickname != nickname || account.AvatarURL == nil || *account.AvatarURL != avatar) {
			t.Fatalf("旧冷却到期没有按原截止记结束：%+v", account)
		}
		return err
	})
	if stateEventCount(t, db, "account_login", true) != 0 {
		t.Fatal("实际登录成功后仍未解决失效事件")
	}
}

func TestAccountDeniedReplacesVerificationAndRejectsLateResults(t *testing.T) {
	db, values, now := accountStateDB(t)
	id := insertStateAccount(t, db, AccountActive)
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		_, err := applyAccountRiskTx(ctx, tx, id, web.ErrNeedVerify, "http://fixture.invalid/verify", values, now)
		return err
	})
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		account, err := applyAccountRiskTx(ctx, tx, id, errors.Join(web.ErrNeedVerify, web.ErrEnvBlocked), "", values, now+1000)
		if err == nil && (account.State != AccountDenied || account.VerifyURL != nil || account.CooldownManualUsed || account.StateReason == nil || account.StateReason.Code != "accounts.environment_denied") {
			t.Fatalf("环境拒绝没有替换待验证：%+v", account)
		}
		return err
	})
	if stateEventCount(t, db, "account_verify", true) != 0 || stateEventCount(t, db, "account_deny", true) != 1 {
		t.Fatal("旧验证事件残留或拒绝未提醒")
	}
	before := stateAccount(t, db, id)
	for _, late := range []func(context.Context, *sql.Tx) (Account, error){
		func(ctx context.Context, tx *sql.Tx) (Account, error) {
			return applyAccountRiskTx(ctx, tx, id, web.ErrNeedVerify, "http://fixture.invalid/new", values, now+2000)
		},
		func(ctx context.Context, tx *sql.Tx) (Account, error) {
			return applyAccountRiskTx(ctx, tx, id, web.ErrLoginExpired, "", values, now+2000)
		},
		func(ctx context.Context, tx *sql.Tx) (Account, error) {
			return completeVerificationTx(ctx, tx, id, values, now+2000)
		},
		func(ctx context.Context, tx *sql.Tx) (Account, error) {
			return completeLoginTx(ctx, tx, id, nil, nil, now+2000)
		},
	} {
		stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
			account, err := late(ctx, tx)
			if !errors.Is(err, web.ErrDenied) || account.State != AccountDenied || *account.CooldownUntil != *before.CooldownUntil {
				t.Fatalf("晚到结果绕过有效拒绝：%+v %v", account, err)
			}
			return nil
		})
	}
	if current := stateAccount(t, db, id); *current.CooldownUntil != *before.CooldownUntil || current.VerifyURL != nil {
		t.Fatal("晚到结果改写了拒绝暂停")
	}
}

func TestAccountStateAndEventFailureRollBackTogether(t *testing.T) {
	db, values, now := accountStateDB(t)
	id := insertStateAccount(t, db, AccountActive)
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER state_event_failure BEFORE INSERT ON system_events BEGIN SELECT RAISE(ABORT,'假事件写失败'); END`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyAccountRiskTx(t.Context(), tx, id, web.ErrNeedVerify, "http://fixture.invalid/verify", values, now); err == nil {
		t.Fatal("事件写失败被当成已完成")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if account := stateAccount(t, db, id); account.State != AccountActive || account.VerifyLevel != 0 || account.VerifyURL != nil {
		t.Fatal("事件失败后仍提交账号状态")
	}
	if stateEventCount(t, db, "account_verify", false) != 0 {
		t.Fatal("回滚后仍留下提醒")
	}
}

func TestAccountResolveAndReassignmentFailuresRollBackTogether(t *testing.T) {
	for _, test := range []struct {
		name    string
		signal  error
		trigger string
	}{
		{"解决验证事件失败", web.ErrDenied, `CREATE TRIGGER state_resolve_failure BEFORE UPDATE OF resolved_at ON system_events BEGIN SELECT RAISE(ABORT,'假事件解决失败'); END`},
		{"改分配失败", web.ErrLoginExpired, `CREATE TRIGGER state_assignment_failure BEFORE UPDATE OF account_id ON tasks BEGIN SELECT RAISE(ABORT,'假任务改分配失败'); END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, values, now := accountStateDB(t)
			id := insertStateAccount(t, db, AccountActive)
			insertStateAccount(t, db, AccountActive)
			task := insertStateTask(t, db, id, false)
			stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
				_, err := applyAccountRiskTx(ctx, tx, id, web.ErrNeedVerify, "http://fixture.invalid/verify", values, now)
				return err
			})
			if _, err := db.ExecContext(t.Context(), test.trigger); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := applyAccountRiskTx(t.Context(), tx, id, test.signal, "", values, now+1000); err == nil {
				t.Fatal("状态相关写入失败被忽略")
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			account := stateAccount(t, db, id)
			if account.State != AccountNeedsVerification || account.VerifyURL == nil || stateEventCount(t, db, "account_verify", true) != 1 {
				t.Fatal("状态与事件没有一起回滚")
			}
			var assignment int64
			if err := db.QueryRowContext(t.Context(), `SELECT account_id FROM tasks WHERE id=?`, task).Scan(&assignment); err != nil || assignment != id {
				t.Fatal("任务绑定没有一起回滚")
			}
		})
	}
}

func TestAccountDueTransitionsUseOriginalCooldownEnd(t *testing.T) {
	db, _, now := accountStateDB(t)
	cooling := insertStateAccount(t, db, AccountCooling)
	denied := insertStateAccount(t, db, AccountDenied)
	needs := insertStateAccount(t, db, AccountNeedsVerification)
	ended := now - int64(time.Hour/time.Millisecond)
	for _, id := range []int64{cooling, denied, needs} {
		if _, err := db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=?,cooldown_manual_used=1,verify_level=4,last_cooldown_end_at=2 WHERE id=?`, ended, id); err != nil {
			t.Fatal(err)
		}
	}
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		ids, err := advanceAccountStatesTx(ctx, tx, now)
		if err == nil && (len(ids) != 2 || ids[0] != cooling || ids[1] != denied) {
			t.Fatalf("到期账号集合错误：%v", ids)
		}
		return err
	})
	if account := stateAccount(t, db, cooling); account.State != AccountActive || *account.LastCooldownEndAt != ended || account.CooldownUntil != nil || account.CooldownManualUsed {
		t.Fatalf("迟醒延长了验证窗口：%+v", account)
	}
	if account := stateAccount(t, db, denied); account.State != AccountActive || *account.LastCooldownEndAt != 2 {
		t.Fatal("把拒绝暂停算作验证冷却结束")
	}
	if account := stateAccount(t, db, needs); account.State != AccountNeedsVerification || account.VerifyLevel != 4 {
		t.Fatal("到期逻辑擅自放过仍需验证的账号")
	}
}

func TestAccountDeniedLogoutExpiresToSignedOut(t *testing.T) {
	db, _, now := accountStateDB(t)
	id := insertStateAccount(t, db, AccountDenied)
	if _, err := db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=?,verify_level=3 WHERE id=?`, now-1000, id); err != nil {
		t.Fatal(err)
	}
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error { return setAccountLoggedOutTx(ctx, tx, id) })
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := advanceAccountStatesTx(ctx, tx, now); err != nil {
			return err
		}
		loggedOut, err := accountLoggedOutTx(ctx, tx, id)
		if err == nil && loggedOut {
			t.Fatal("拒绝结束后还保留已经结算的登出标记")
		}
		return err
	})
	account := stateAccount(t, db, id)
	if account.State != AccountSignedOut || account.CooldownUntil != nil || account.VerifyLevel != 3 || account.StateReason == nil || account.StateReason.Code != "accounts.signed_out" {
		t.Fatalf("登出后拒绝到期被误恢复成可运行：%+v", account)
	}
}

func TestAccountSuccessfulLoginClearsLoggedOutMarkerAfterDeny(t *testing.T) {
	for _, activeDeny := range []bool{true, false} {
		t.Run(map[bool]string{true: "拒绝未到期", false: "拒绝已到期"}[activeDeny], func(t *testing.T) {
			db, _, now := accountStateDB(t)
			id := insertStateAccount(t, db, AccountDenied)
			until := now - 1000
			if activeDeny {
				until = now + 1000
			}
			if _, err := db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=? WHERE id=?`, until, id); err != nil {
				t.Fatal(err)
			}
			stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error { return setAccountLoggedOutTx(ctx, tx, id) })
			stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
				account, err := completeLoginTx(ctx, tx, id, nil, nil, now)
				if activeDeny {
					if !errors.Is(err, web.ErrDenied) || account.State != AccountDenied {
						t.Fatal("原拒绝暂停被扫码结果提前解除")
					}
				} else if err != nil || account.State != AccountActive || account.LastCooldownEndAt != nil {
					t.Fatalf("已完成登录没有恢复，或把拒绝期当成验证冷却：%+v %v", account, err)
				}
				loggedOut, markerErr := accountLoggedOutTx(ctx, tx, id)
				if markerErr != nil || loggedOut != activeDeny {
					t.Fatal("登出标记的清除时机错误")
				}
				return markerErr
			})
		})
	}
}

func TestAccountInvalidLoggedOutMarkerDoesNotReleaseDeny(t *testing.T) {
	db, _, now := accountStateDB(t)
	id := insertStateAccount(t, db, AccountDenied)
	if _, err := db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=? WHERE id=?`, now-1000, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES('account.logged_out.1','broken')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := advanceAccountStatesTx(t.Context(), tx, now); err == nil {
		t.Fatal("坏登出标记被当成未登出")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if account := stateAccount(t, db, id); account.State != AccountDenied {
		t.Fatal("错误之后仍提交了到期放行")
	}
}

func TestAccountStateMessagesRenderInBothLanguages(t *testing.T) {
	db, values, now := accountStateDB(t)
	id := insertStateAccount(t, db, AccountActive)
	messages := []locales.Message{}
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		account, err := applyAccountRiskTx(ctx, tx, id, web.ErrNeedVerify, "http://fixture.invalid/verify", values, now)
		if err != nil {
			return err
		}
		messages = append(messages, *account.StateReason)
		account, err = completeVerificationTx(ctx, tx, id, values, now+1000)
		if err != nil {
			return err
		}
		messages = append(messages, *account.StateReason)
		account, err = applyAccountRiskTx(ctx, tx, id, web.ErrLoginExpired, "", values, now+2000)
		if err != nil {
			return err
		}
		messages = append(messages, *account.StateReason)
		account, err = applyAccountRiskTx(ctx, tx, id, web.ErrDenied, "", values, now+3000)
		if err != nil {
			return err
		}
		messages = append(messages, *account.StateReason)
		account, err = applyAccountRiskTx(ctx, tx, id, web.ErrEnvBlocked, "", values, now+4000)
		if err != nil {
			return err
		}
		messages = append(messages, *account.StateReason)
		return nil
	})
	rows, err := db.QueryContext(t.Context(), `SELECT message FROM system_events`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var raw string
		var message locales.Message
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &message); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	for _, message := range messages {
		for _, language := range []locales.Language{locales.ZhCN, locales.En} {
			if text, err := locales.Format(language, message); err != nil || text == "" {
				t.Fatalf("状态消息不能显示：%s %s %v", language, message.Code, err)
			}
		}
	}
}
