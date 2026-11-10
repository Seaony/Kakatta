package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// SignOutRestoredAccountsTx 只处理候选库里的账号，不操作浏览器目录或提交事务。
func SignOutRestoredAccountsTx(ctx context.Context, tx *sql.Tx, nowMS int64) error {
	if tx == nil || nowMS <= 0 {
		return errors.New("恢复账号的事务或时间无效")
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM accounts ORDER BY id`)
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, _, err := signOutAccountTx(ctx, tx, id, nowMS); err != nil {
			return err
		}
	}
	return nil
}

func signedOutAccountState(state AccountState, until *int64, nowMS int64) (AccountState, bool, error) {
	if (state == AccountCooling || state == AccountDenied) && until == nil {
		return state, false, errors.New("账号暂停截止时间缺失")
	}
	if until != nil && *until > nowMS {
		if state == AccountDenied {
			return AccountDenied, true, nil
		}
		return AccountCooling, true, nil
	}
	return AccountSignedOut, false, nil
}

// 不解析原来的原因，保留仍有效的暂停及历史消息中的数字原文。
func signOutAccountTx(ctx context.Context, tx *sql.Tx, id, nowMS int64) (AccountState, bool, error) {
	var previous AccountState
	var until *int64
	if err := tx.QueryRowContext(ctx, `SELECT state,cooldown_until FROM accounts WHERE id=?`, id).Scan(&previous, &until); err != nil {
		return previous, false, err
	}
	state, protected, err := signedOutAccountState(previous, until, nowMS)
	if err != nil {
		return state, false, err
	}
	if protected {
		if err := setAccountLoggedOutTx(ctx, tx, id); err != nil {
			return state, false, err
		}
	} else if err := clearAccountLoggedOutTx(ctx, tx, id); err != nil {
		return state, false, err
	}
	if state == previous {
		_, err = tx.ExecContext(ctx, `UPDATE accounts SET verify_url=NULL WHERE id=?`, id)
	} else {
		code := "accounts.signed_out"
		if state == AccountCooling {
			code = "run.account_cooling"
		}
		reason, marshalErr := json.Marshal(accountMessage(code, nil))
		if marshalErr != nil {
			return state, false, marshalErr
		}
		_, err = tx.ExecContext(ctx, `UPDATE accounts SET state=?,state_reason=?,state_changed_at=?,verify_url=NULL WHERE id=?`, state, string(reason), nowMS, id)
	}
	if err != nil {
		return state, false, err
	}
	if previous == AccountDenied && !protected {
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET cooldown_until=NULL WHERE id=?`, id); err != nil {
			return state, false, err
		}
	}
	eventsChanged := false
	for _, kind := range []string{"account_verify", "account_login", "account_deny"} {
		if kind == "account_deny" && state == AccountDenied {
			continue
		}
		changed, err := accountEvents(nowMS).ResolveTx(ctx, tx, kind, id)
		if err != nil {
			return state, false, err
		}
		eventsChanged = eventsChanged || changed
	}
	return state, eventsChanged, nil
}
