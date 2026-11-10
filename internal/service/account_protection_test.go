package service

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func protectionTestService(t *testing.T) *Service {
	t.Helper()
	s := accountTestService(t)
	if _, err := s.settings.Apply(t.Context(), settings.Values{"crawl.quiet_hours": []byte(`{"enabled":false,"start":"01:00","end":"07:30"}`)}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCoolingLogoutRetainsPauseAndBlocksScan(t *testing.T) {
	s := protectionTestService(t)
	id := accountTestInsert(t, s, "冷却退出", AccountCooling, 1)
	until := s.now().Add(time.Hour).UnixMilli()
	if _, err := s.db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=?,verify_level=3,cooldown_manual_used=0 WHERE id=?`, until, id); err != nil {
		t.Fatal(err)
	}
	view, err := s.LogoutAccount(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != AccountCooling || view.CooldownUntil == nil || *view.CooldownUntil != until {
		t.Errorf("退出改变了尚未结束的冷却：%+v", view)
	}
	session, err := s.OpenLogin(t.Context(), id)
	if err == nil {
		_ = s.CloseSession(t.Context(), id, session.ID)
		t.Error("冷却期间退出后仍能扫码")
	} else {
		accountTestProblem(t, err, ErrorConflict, "run.account_cooling")
	}
	tx := accountTestTx(t, s)
	loggedOut, err := accountLoggedOutTx(t.Context(), tx, id)
	if err != nil || !loggedOut {
		t.Errorf("冷却退出没有持久身份标记：%v %v", loggedOut, err)
	}
}

func TestFutureDeadlineBlocksScanRegardlessOfDisplayState(t *testing.T) {
	snapshot, err := settings.Resolve(nil, settings.Environment{TZ: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	until := now.Add(time.Hour).UnixMilli()
	for _, state := range []AccountState{AccountActive, AccountCooling, AccountDenied, AccountExpired, AccountSignedOut, AccountNeedsVerification} {
		t.Run(string(state), func(t *testing.T) {
			account := Account{ID: 1, State: state, CooldownUntil: &until}
			block, err := manualSessionBlock(account, SessionLogin, snapshot, now)
			want := "run.account_cooling"
			if state == AccountDenied {
				want = "run.account_denied"
			}
			if err != nil || block == nil || block.Reason.Code != want || block.NextAllowedAt == nil || *block.NextAllowedAt != until {
				t.Fatalf("状态转换绕过实际截止：%+v %v", block, err)
			}
			block, err = manualSessionBlock(account, SessionLogin, snapshot, time.UnixMilli(until))
			if err != nil || block != nil {
				t.Fatalf("实际截止已过仍然拦扫码：%+v %v", block, err)
			}
		})
	}
}

func TestLoggedOutCoolingNeverGetsManualOpportunity(t *testing.T) {
	for _, loggedOut := range []bool{false, true} {
		t.Run(map[bool]string{false: "正常冷却", true: "已经退出"}[loggedOut], func(t *testing.T) {
			s := protectionTestService(t)
			id := accountTestInsert(t, s, "冷却", AccountCooling, 1)
			until := s.now().Add(time.Hour).UnixMilli()
			if _, err := s.db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=?,cooldown_manual_used=0 WHERE id=?`, until, id); err != nil {
				t.Fatal(err)
			}
			taskID := accountTestTask(t, s, "冷却手动", &id, true, true)
			tx := accountTestTx(t, s)
			if loggedOut {
				if err := setAccountLoggedOutTx(t.Context(), tx, id); err != nil {
					t.Fatal(err)
				}
			}
			task, err := loadTaskTx(t.Context(), tx, taskID)
			if err != nil {
				t.Fatal(err)
			}
			start, err := s.preflightTx(t.Context(), tx, task, TriggerManual)
			if err != nil {
				t.Fatal(err)
			}
			if loggedOut && (start.Blocked == nil || start.Blocked.Reason.Code != "run.account_signed_out") {
				t.Fatalf("已退出账号用掉冷却手动机会：%+v", start.Blocked)
			}
			if !loggedOut && start.Blocked != nil {
				t.Fatalf("正常冷却的那一次手动机会被取消：%+v", start.Blocked)
			}
		})
	}
}

func TestLateLoginCannotClearFuturePause(t *testing.T) {
	for _, state := range []AccountState{AccountActive, AccountCooling, AccountDenied, AccountExpired, AccountSignedOut} {
		t.Run(string(state), func(t *testing.T) {
			db, _, now := accountStateDB(t)
			id := insertStateAccount(t, db, state)
			until := now + int64(time.Hour/time.Millisecond)
			if _, err := db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=? WHERE id=?`, until, id); err != nil {
				t.Fatal(err)
			}
			stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
				account, err := completeLoginTx(ctx, tx, id, nil, nil, now)
				if state == AccountDenied {
					if !errors.Is(err, web.ErrDenied) {
						t.Fatalf("晚到扫码未保留拒绝优先：%v", err)
					}
				} else {
					var problem *ServiceError
					if !errors.As(err, &problem) || problem.Kind != ErrorConflict || problem.Reason.Code != "run.account_cooling" || problem.NextAllowedAt == nil || *problem.NextAllowedAt != until {
						t.Fatalf("晚到扫码绕过冷却：%+v %v", account, err)
					}
				}
				return nil
			})
			if account := stateAccount(t, db, id); account.State != state || account.CooldownUntil == nil || *account.CooldownUntil != until {
				t.Fatal("被拦的扫码仍改写了原保护")
			}
		})
	}
}

func TestCoolingLogoutDeadlineEndsSignedOut(t *testing.T) {
	db, _, now := accountStateDB(t)
	id := insertStateAccount(t, db, AccountCooling)
	until := now - 1000
	if _, err := db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=?,verify_level=3,cooldown_manual_used=1 WHERE id=?`, until, id); err != nil {
		t.Fatal(err)
	}
	stateTx(t, db, func(ctx context.Context, tx *sql.Tx) error {
		if err := setAccountLoggedOutTx(ctx, tx, id); err != nil {
			return err
		}
		_, err := advanceAccountStatesTx(ctx, tx, now)
		return err
	})
	account := stateAccount(t, db, id)
	if account.State != AccountSignedOut || account.LastCooldownEndAt == nil || *account.LastCooldownEndAt != until || account.CooldownUntil != nil {
		t.Fatalf("退出的冷却到期后变成可运行：%+v", account)
	}
}

func TestAutomaticReadyAtUsesRealCooldownInAnyState(t *testing.T) {
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	until := now.Add(time.Hour).UnixMilli()
	start := preflightResult{Now: now, Account: Account{State: AccountActive, CooldownUntil: &until}}
	if got := automaticReadyAt(start); got.UnixMilli() != until {
		t.Fatalf("状态名盖过真实冷却截止：%v", got)
	}
	start.Account.CooldownUntil = nil
	if got := automaticReadyAt(start); !got.Equal(now) {
		t.Fatalf("没有冷却仍延后：%v", got)
	}
}

func TestLoggedOutAccountsNeverRunInAnyDisplayState(t *testing.T) {
	for _, state := range []AccountState{AccountActive, AccountCooling, AccountDenied, AccountExpired, AccountSignedOut, AccountNeedsVerification} {
		t.Run(string(state), func(t *testing.T) {
			s := protectionTestService(t)
			id := accountTestInsert(t, s, "已退出", state, 1)
			until := s.now().Add(time.Hour).UnixMilli()
			if _, err := s.db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=?,cooldown_manual_used=0 WHERE id=?`, until, id); err != nil {
				t.Fatal(err)
			}
			taskID := accountTestTask(t, s, "不能运行", &id, true, true)
			tx := accountTestTx(t, s)
			if err := setAccountLoggedOutTx(t.Context(), tx, id); err != nil {
				t.Fatal(err)
			}
			task, err := loadTaskTx(t.Context(), tx, taskID)
			if err != nil {
				t.Fatal(err)
			}
			for _, trigger := range []RunTrigger{TriggerAuto, TriggerManual} {
				start, err := s.preflightTx(t.Context(), tx, task, trigger)
				if err != nil || start.Blocked == nil {
					t.Fatalf("已退出账号经%s放行：%+v %v", trigger, start.Blocked, err)
				}
				if state == AccountDenied && (start.Blocked.Reason.Code != "run.account_denied" || start.Blocked.NextAllowedAt == nil || *start.Blocked.NextAllowedAt != until) {
					t.Fatalf("已退出标记盖过了拒绝优先：%+v", start.Blocked)
				}
			}
		})
	}
}

func TestRealCooldownBlocksAutomaticAndManualRunsAfterStateChange(t *testing.T) {
	s := protectionTestService(t)
	id := accountTestInsert(t, s, "状态已改", AccountActive, 1)
	until := s.now().Add(time.Hour).UnixMilli()
	if _, err := s.db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=?,cooldown_manual_used=0 WHERE id=?`, until, id); err != nil {
		t.Fatal(err)
	}
	taskID := accountTestTask(t, s, "真实期限", &id, true, true)
	tx := accountTestTx(t, s)
	task, err := loadTaskTx(t.Context(), tx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, trigger := range []RunTrigger{TriggerAuto, TriggerManual} {
		start, err := s.preflightTx(t.Context(), tx, task, trigger)
		if err != nil || start.Blocked == nil || start.Blocked.Reason.Code != "run.account_cooling" || start.Blocked.NextAllowedAt == nil || *start.Blocked.NextAllowedAt != until {
			t.Fatalf("状态名让%s绕过实际冷却：%+v %v", trigger, start.Blocked, err)
		}
	}
}
