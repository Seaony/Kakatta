package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/Seaony/Kakatta/internal/eventstore"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func accountMessage(code string, args map[string]any) locales.Message {
	if args == nil {
		args = map[string]any{}
	}
	return locales.Message{Code: code, Args: args}
}

func accountEvents(nowMS int64) eventstore.Store {
	return eventstore.Store{Now: func() time.Time { return time.UnixMilli(nowMS) }}
}

func accountSetting(values settings.Values, key string) (int64, error) {
	var value int64
	if err := json.Unmarshal(values[key], &value); err != nil || value <= 0 {
		return 0, errors.New("账号保护设置无效")
	}
	return value, nil
}

func accountDeadline(nowMS, units, unitMS int64) (int64, error) {
	if nowMS <= 0 || units <= 0 || unitMS <= 0 || units > (math.MaxInt64-nowMS)/unitMS {
		return 0, errors.New("账号保护时间超出范围")
	}
	return nowMS + units*unitMS, nil
}

func accountIsDenied(account Account, nowMS int64) bool {
	return account.State == AccountDenied && account.CooldownUntil != nil && *account.CooldownUntil > nowMS
}

func changeAccountState(account *Account, state AccountState, reason *locales.Message, nowMS int64) {
	if account.State != state {
		account.StateChangedAt = &nowMS
	}
	account.State, account.StateReason = state, reason
}

// 登录失效也保留冷却；到期后仍按原截止保留连续验证的判断窗口。
func finishOldCooldown(account *Account, nowMS int64) {
	if account.CooldownUntil == nil || *account.CooldownUntil > nowMS {
		return
	}
	if account.State != AccountDenied {
		ended := *account.CooldownUntil
		account.LastCooldownEndAt = &ended
	}
	account.CooldownUntil = nil
	account.CooldownManualUsed = false
}

func writeAccountStateTx(ctx context.Context, tx *sql.Tx, account Account) error {
	var reason any
	if account.StateReason != nil {
		raw, err := json.Marshal(account.StateReason)
		if err != nil {
			return err
		}
		reason = string(raw)
	}
	result, err := tx.ExecContext(ctx, `UPDATE accounts SET state=?,state_reason=?,state_changed_at=?,
		cooldown_until=?,verify_level=?,verify_url=?,last_cooldown_end_at=?,cooldown_manual_used=? WHERE id=?`,
		account.State, reason, account.StateChangedAt, account.CooldownUntil, account.VerifyLevel,
		account.VerifyURL, account.LastCooldownEndAt, account.CooldownManualUsed, account.ID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return &ServiceError{Kind: ErrorNotFound, Reason: accountMessage("accounts.not_found", nil), Fields: []settings.Issue{}}
	}
	return nil
}

func applyAccountRiskTx(ctx context.Context, tx *sql.Tx, accountID int64, riskErr error, verifyURL string, values settings.Values, nowMS int64) (Account, error) {
	account, err := loadAccountTx(ctx, tx, accountID)
	if err != nil {
		return Account{}, err
	}
	denied := errors.Is(riskErr, web.ErrDenied) || errors.Is(riskErr, web.ErrEnvBlocked)
	if !denied && accountIsDenied(account, nowMS) {
		return account, web.ErrDenied
	}
	if !denied && !errors.Is(riskErr, web.ErrNeedVerify) && !errors.Is(riskErr, web.ErrLoginExpired) {
		return account, errors.New("账号风控信号无效")
	}
	finishOldCooldown(&account, nowMS)
	events := accountEvents(nowMS)
	switch {
	case denied:
		hours, err := accountSetting(values, "crawl.deny_pause_hours")
		if err != nil {
			return Account{}, err
		}
		until, err := accountDeadline(nowMS, hours, int64(time.Hour/time.Millisecond))
		if err != nil {
			return Account{}, err
		}
		if account.State != AccountDenied && account.CooldownUntil != nil {
			account.LastCooldownEndAt = &nowMS
		}
		account.CooldownUntil, account.VerifyURL, account.CooldownManualUsed = &until, nil, false
		code := "accounts.access_denied"
		if errors.Is(riskErr, web.ErrEnvBlocked) {
			code = "accounts.environment_denied"
		}
		reason := accountMessage(code, map[string]any{"name": account.Name, "hours": hours, "count": hours})
		changeAccountState(&account, AccountDenied, &reason, nowMS)
		if err := writeAccountStateTx(ctx, tx, account); err != nil {
			return Account{}, err
		}
		if _, err := events.ResolveTx(ctx, tx, "account_verify", account.ID); err != nil {
			return Account{}, err
		}
		if _, err := events.RaiseTx(ctx, tx, "account_deny", account.ID, reason); err != nil {
			return Account{}, err
		}
	case errors.Is(riskErr, web.ErrLoginExpired):
		account.VerifyURL = nil
		reason := accountMessage("run.account_expired", nil)
		changeAccountState(&account, AccountExpired, &reason, nowMS)
		if err := writeAccountStateTx(ctx, tx, account); err != nil {
			return Account{}, err
		}
		if _, err := events.ResolveTx(ctx, tx, "account_verify", account.ID); err != nil {
			return Account{}, err
		}
		reassigned, err := reassignAutomaticTasksTx(ctx, tx, account.ID, nowMS)
		if err != nil {
			return Account{}, err
		}
		message := accountMessage("accounts.login_expired", map[string]any{"name": account.Name, "count": len(reassigned)})
		if _, err := events.RaiseTx(ctx, tx, "account_login", account.ID, message); err != nil {
			return Account{}, err
		}
	default:
		if account.State != AccountNeedsVerification {
			resetHours, err := accountSetting(values, "crawl.verify_cooldown_reset_hours")
			if err != nil {
				return Account{}, err
			}
			resetMS := int64(time.Hour / time.Millisecond)
			resetMS *= resetHours
			inCooldown := account.CooldownUntil != nil && *account.CooldownUntil > nowMS
			recentCooldown := account.LastCooldownEndAt != nil && (*account.LastCooldownEndAt > nowMS-resetMS)
			if inCooldown || recentCooldown {
				account.VerifyLevel++
			} else {
				account.VerifyLevel = 1
			}
			if inCooldown {
				account.LastCooldownEndAt, account.CooldownUntil = &nowMS, nil
			}
		}
		if account.VerifyLevel < 1 {
			account.VerifyLevel = 1
		}
		if verifyURL != "" {
			account.VerifyURL = &verifyURL
		}
		reason := accountMessage("run.account_needs_verification", nil)
		changeAccountState(&account, AccountNeedsVerification, &reason, nowMS)
		if err := writeAccountStateTx(ctx, tx, account); err != nil {
			return Account{}, err
		}
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE account_id=? AND enabled=1`, account.ID).Scan(&count); err != nil {
			return Account{}, err
		}
		message := accountMessage("accounts.verification_required", map[string]any{"name": account.Name, "count": count})
		if _, err := events.RaiseTx(ctx, tx, "account_verify", account.ID, message); err != nil {
			return Account{}, err
		}
	}
	return account, nil
}

func completeVerificationTx(ctx context.Context, tx *sql.Tx, accountID int64, values settings.Values, nowMS int64) (Account, error) {
	account, err := loadAccountTx(ctx, tx, accountID)
	if err != nil {
		return Account{}, err
	}
	if accountIsDenied(account, nowMS) {
		return account, web.ErrDenied
	}
	if account.State != AccountNeedsVerification || account.VerifyLevel < 1 {
		return account, &ServiceError{Kind: ErrorConflict, Reason: accountMessage("runtime.session_changed", nil), Fields: []settings.Issue{}}
	}
	minutes, err := accountSetting(values, "crawl.verify_cooldown_base_minutes")
	if err != nil {
		return Account{}, err
	}
	maximum, err := accountSetting(values, "crawl.verify_cooldown_max_minutes")
	if err != nil || minutes > maximum {
		return Account{}, errors.New("验证冷却设置无效")
	}
	for level := int64(1); level < account.VerifyLevel && minutes < maximum; level++ {
		if minutes > maximum/2 {
			minutes = maximum
		} else {
			minutes *= 2
		}
	}
	until, err := accountDeadline(nowMS, minutes, int64(time.Minute/time.Millisecond))
	if err != nil {
		return Account{}, err
	}
	account.CooldownUntil, account.VerifyURL, account.CooldownManualUsed = &until, nil, false
	reason := accountMessage("run.account_cooling", nil)
	changeAccountState(&account, AccountCooling, &reason, nowMS)
	if err := writeAccountStateTx(ctx, tx, account); err != nil {
		return Account{}, err
	}
	if _, err := accountEvents(nowMS).ResolveTx(ctx, tx, "account_verify", account.ID); err != nil {
		return Account{}, err
	}
	return account, nil
}

func completeLoginTx(ctx context.Context, tx *sql.Tx, accountID int64, nickname, avatarURL *string, nowMS int64) (Account, error) {
	account, err := loadAccountTx(ctx, tx, accountID)
	if err != nil {
		return Account{}, err
	}
	if accountIsDenied(account, nowMS) {
		return account, web.ErrDenied
	}
	finishOldCooldown(&account, nowMS)
	account.VerifyURL = nil
	if account.CooldownUntil != nil && *account.CooldownUntil > nowMS {
		reason := accountMessage("run.account_cooling", nil)
		changeAccountState(&account, AccountCooling, &reason, nowMS)
	} else {
		account.CooldownUntil, account.CooldownManualUsed = nil, false
		changeAccountState(&account, AccountActive, nil, nowMS)
	}
	if err := writeAccountStateTx(ctx, tx, account); err != nil {
		return Account{}, err
	}
	if nickname != nil {
		account.Nickname = nickname
	}
	if avatarURL != nil {
		account.AvatarURL = avatarURL
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET nickname=?,avatar_url=? WHERE id=?`, account.Nickname, account.AvatarURL, account.ID); err != nil {
		return Account{}, err
	}
	events := accountEvents(nowMS)
	for _, kind := range []string{"account_login", "account_verify", "account_deny"} {
		if _, err := events.ResolveTx(ctx, tx, kind, account.ID); err != nil {
			return Account{}, err
		}
	}
	if err := clearAccountLoggedOutTx(ctx, tx, account.ID); err != nil {
		return Account{}, err
	}
	if _, err := assignUnboundTasksTx(ctx, tx, nowMS); err != nil {
		return Account{}, err
	}
	return account, nil
}

func advanceAccountStatesTx(ctx context.Context, tx *sql.Tx, nowMS int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM accounts WHERE state IN ('cooling','denied') AND cooldown_until<=? ORDER BY id`, nowMS)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	events := accountEvents(nowMS)
	for _, id := range ids {
		account, err := loadAccountTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		kind := "account_deny"
		state := AccountActive
		var reason *locales.Message
		loggedOut := false
		if account.State == AccountCooling {
			ended := *account.CooldownUntil
			account.LastCooldownEndAt = &ended
			kind = "account_verify"
		} else {
			loggedOut, err = accountLoggedOutTx(ctx, tx, account.ID)
			if err != nil {
				return nil, err
			}
			if loggedOut {
				state = AccountSignedOut
				message := accountMessage("accounts.signed_out", nil)
				reason = &message
			}
		}
		account.CooldownUntil, account.VerifyURL, account.CooldownManualUsed = nil, nil, false
		changeAccountState(&account, state, reason, nowMS)
		if err := writeAccountStateTx(ctx, tx, account); err != nil {
			return nil, err
		}
		if loggedOut {
			if err := clearAccountLoggedOutTx(ctx, tx, account.ID); err != nil {
				return nil, err
			}
		}
		if _, err := events.ResolveTx(ctx, tx, kind, id); err != nil {
			return nil, err
		}
	}
	if len(ids) > 0 {
		if _, err := assignUnboundTasksTx(ctx, tx, nowMS); err != nil {
			return nil, err
		}
	}
	return ids, nil
}
