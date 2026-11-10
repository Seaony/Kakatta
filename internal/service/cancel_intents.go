package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/Seaony/Kakatta/internal/locales"
)

func cancelPendingIntentsForFindingsTx(ctx context.Context, tx *sql.Tx, ids []int64, reason locales.Message, nowMS int64) error {
	for start := 0; start < len(ids); start += 400 {
		end := min(start+400, len(ids))
		args := make([]any, end-start)
		for i, id := range ids[start:end] {
			args[i] = id
		}
		where := `finding_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(args)), ",") + `)`
		if err := cancelPendingIntentsTx(ctx, tx, where, args, reason, nowMS); err != nil {
			return err
		}
	}
	return nil
}

func cancelPendingIntentsForItemsTx(ctx context.Context, tx *sql.Tx, itemIDs []string, reason locales.Message, nowMS int64) error {
	for start := 0; start < len(itemIDs); start += 400 {
		end := min(start+400, len(itemIDs))
		args := make([]any, end-start)
		for i, itemID := range itemIDs[start:end] {
			args[i] = itemID
		}
		where := `finding_id IN (SELECT id FROM findings WHERE item_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(args)), ",") + `))`
		if err := cancelPendingIntentsTx(ctx, tx, where, args, reason, nowMS); err != nil {
			return err
		}
	}
	return nil
}

func cancelPendingIntentsForSellerTx(ctx context.Context, tx *sql.Tx, sellerID string, reason locales.Message, nowMS int64) error {
	if sellerID == "" {
		return nil
	}
	return cancelPendingIntentsTx(ctx, tx,
		`finding_id IN (SELECT f.id FROM findings f JOIN listings l ON l.item_id=f.item_id WHERE l.seller_id=?)`,
		[]any{sellerID}, reason, nowMS)
}

// 调用方传固定条件，所有编号和卖家值仍用参数绑定。
func cancelPendingIntentsTx(ctx context.Context, tx *sql.Tx, where string, values []any, reason locales.Message, nowMS int64) error {
	raw, err := json.Marshal(reason)
	if err != nil {
		return err
	}
	args := append([]any{string(raw), nowMS}, values...)
	_, err = tx.ExecContext(ctx, `UPDATE notification_intents SET state='cancelled',reason=?,updated_at=? WHERE state='pending' AND `+where, args...)
	return err
}
