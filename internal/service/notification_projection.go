package service

import (
	"context"
	"database/sql"
	"time"
)

// NotificationFinding 复用现有读取结果，只补通知需要的当前状态。
type NotificationFinding struct {
	Finding          Finding
	Listing          Listing
	ItemSpecs        *TaskItemSpecs
	Seller           *SellerProfile
	SellerBlocked    bool
	ReanalysisOrigin bool
}

func NotificationTaskTx(ctx context.Context, tx *sql.Tx, taskID int64) (Task, error) {
	return loadTaskTx(ctx, tx, taskID)
}

func NotificationRetryExpired(failures int64, firstFailedAt *int64, nowMS int64) bool {
	return failures >= 3 || firstFailedAt != nil && nowMS > *firstFailedAt && uint64(nowMS)-uint64(*firstFailedAt) > uint64((24*time.Hour).Milliseconds())
}

func NotificationFindingTx(ctx context.Context, tx *sql.Tx, findingID int64) (NotificationFinding, error) {
	var value NotificationFinding
	var err error
	value.Finding, err = loadFindingTx(ctx, tx, findingID)
	if err != nil {
		return NotificationFinding{}, err
	}
	value.Listing, err = loadListingTx(ctx, tx, value.Finding.ItemID)
	if err != nil {
		return NotificationFinding{}, err
	}
	value.ItemSpecs, err = loadItemSpecsTx(ctx, tx, value.Finding.TaskID, value.Finding.ItemID)
	if err != nil {
		return NotificationFinding{}, err
	}
	if value.Listing.SellerID != nil {
		value.Seller, err = loadSellerTx(ctx, tx, *value.Listing.SellerID)
		if err != nil {
			return NotificationFinding{}, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blocked_sellers WHERE seller_id=?)`,
			*value.Listing.SellerID).Scan(&value.SellerBlocked); err != nil {
			return NotificationFinding{}, err
		}
	}
	if err := tx.QueryRowContext(ctx, `SELECT reanalysis_origin FROM findings WHERE id=?`, findingID).
		Scan(&value.ReanalysisOrigin); err != nil {
		return NotificationFinding{}, err
	}
	return value, nil
}
