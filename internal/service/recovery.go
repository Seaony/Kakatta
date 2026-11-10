package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	"github.com/Seaony/Kakatta/internal/settings"
)

// nowMS 必须来自同一事务的 observeNowTx，时钟和恢复结果由调用方一起提交。
func recoverRuntimeTx(ctx context.Context, tx *sql.Tx, values settings.Values, nowMS int64) error {
	if nowMS <= 0 {
		return errors.New("运行恢复时间无效")
	}
	minimumGap, err := accountSetting(values, "crawl.account_min_gap_seconds")
	if err != nil {
		return err
	}
	markers, err := listSessionMarkersTx(ctx, tx)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(markers))
	accounts := make(map[int64]Account, len(markers))
	for id, marker := range markers {
		if marker.StartedAt > nowMS {
			return errors.New("运行恢复标记晚于保护时间")
		}
		account, err := loadAccountTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if account.NextGapSeconds != nil && *account.NextGapSeconds < 0 {
			return errors.New("账号运行间隔记录无效")
		}
		if marker.RunID != nil {
			var runAccount sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT account_id FROM run_records WHERE id=?`, *marker.RunID).Scan(&runAccount); err != nil {
				return err
			}
			if !runAccount.Valid || runAccount.Int64 != id {
				return errors.New("运行恢复标记与账号不一致")
			}
		}
		ids = append(ids, id)
		accounts[id] = account
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	message, err := json.Marshal(accountMessage("run.restart", nil))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_records SET outcome='failed',failure_kind='restart',ended_at=?,message=? WHERE ended_at IS NULL`, nowMS, string(message)); err != nil {
		return err
	}
	for _, id := range ids {
		gap := minimumGap
		if previous := accounts[id].NextGapSeconds; previous != nil && *previous > gap {
			gap = *previous
		}
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET last_run_ended_at=?,next_gap_seconds=? WHERE id=?`, nowMS, gap, id); err != nil {
			return err
		}
		if err := deleteSessionMarkerTx(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}
