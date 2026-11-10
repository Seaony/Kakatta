package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
)

func selectedFindingIDs(ids []int64) ([]int64, error) {
	selected := make([]int64, 0, min(len(ids), 200))
	seen := make(map[int64]bool, cap(selected))
	for _, id := range ids {
		if id <= 0 || id > limits.MaxSafeInteger {
			return nil, invalidFindingSelection()
		}
		if !seen[id] {
			seen[id] = true
			selected = append(selected, id)
			if len(selected) > 200 {
				return nil, invalidFindingSelection()
			}
		}
	}
	if len(selected) == 0 {
		return nil, invalidFindingSelection()
	}
	return selected, nil
}

func invalidFindingSelection() error {
	return &ServiceError{Kind: ErrorInvalid, Reason: locales.Message{Code: "findings.invalid_selection"}}
}

func missingBatchFinding(id int64) BatchItem {
	return BatchItem{ID: id, Status: BatchSkipped, Reason: &locales.Message{Code: "findings.not_found"}}
}

func (s *Service) SetRead(ctx context.Context, ids []int64, read bool) (BatchResult, error) {
	selected, err := selectedFindingIDs(ids)
	if err != nil {
		return BatchResult{}, err
	}
	release, err := s.beginMutation()
	if err != nil {
		return BatchResult{}, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BatchResult{}, storageFailure(err)
	}
	defer tx.Rollback()
	result := BatchResult{Requested: int64(len(selected)), Items: make([]BatchItem, 0, len(selected))}
	changed := []int64{}
	for _, id := range selected {
		var previous bool
		err := tx.QueryRowContext(ctx, `SELECT is_read FROM findings WHERE id=?`, id).Scan(&previous)
		if errors.Is(err, sql.ErrNoRows) {
			result.Items = append(result.Items, missingBatchFinding(id))
			result.Skipped++
			continue
		}
		if err != nil {
			return BatchResult{}, storageFailure(err)
		}
		if previous != read {
			if _, err := tx.ExecContext(ctx, `UPDATE findings SET is_read=? WHERE id=?`, read, id); err != nil {
				return BatchResult{}, storageFailure(err)
			}
			changed = append(changed, id)
		}
		result.Items = append(result.Items, BatchItem{ID: id, Status: BatchApplied})
		result.Applied++
	}
	if err := tx.Commit(); err != nil {
		return BatchResult{}, storageFailure(err)
	}
	if len(changed) > 0 {
		_ = s.emit("finding.changed", map[string]any{"ids": changed})
	}
	return result, nil
}

func (s *Service) UpdateListing(ctx context.Context, itemID string, patch ListingPatch) (ListingView, error) {
	if !validLocalID(itemID) {
		return ListingView{}, listingPatchError("item_id")
	}
	favorite, dismissed, err := listingFlagPatch(patch)
	if err != nil {
		return ListingView{}, err
	}
	release, err := s.beginMutation()
	if err != nil {
		return ListingView{}, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ListingView{}, storageFailure(err)
	}
	defer tx.Rollback()
	nowMS := s.now().UnixMilli()
	changed, err := setListingFlagsTx(ctx, tx, itemID, favorite, dismissed, nowMS)
	if err != nil {
		return ListingView{}, storageFailure(err)
	}
	if dismissed != nil && *dismissed {
		if err := cancelPendingIntentsForItemsTx(ctx, tx, []string{itemID}, locales.Message{Code: "notify.cancelled_dismissed"}, nowMS); err != nil {
			return ListingView{}, storageFailure(err)
		}
	}
	view, err := getListingViewTx(ctx, tx, itemID)
	if err != nil {
		return ListingView{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return ListingView{}, storageFailure(err)
	}
	if changed {
		_ = s.emit("finding.changed", map[string]any{"all": true, "item_ids": []string{itemID}})
	}
	return view, nil
}

func listingFlagPatch(patch ListingPatch) (favorite, dismissed *bool, err error) {
	keys := make([]string, 0, len(patch))
	for key := range patch {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key != "is_favorite" && key != "is_dismissed" {
			return nil, nil, listingPatchError(key)
		}
		var value *bool
		if err := json.Unmarshal(patch[key], &value); err != nil || value == nil {
			return nil, nil, listingPatchError(key)
		}
		if key == "is_favorite" {
			favorite = value
		} else {
			dismissed = value
		}
	}
	return favorite, dismissed, nil
}

func listingPatchError(key string) error {
	reason := locales.Message{Code: "listings.invalid_patch"}
	return &ServiceError{Kind: ErrorInvalid, Reason: reason, Fields: []settings.Issue{{Key: key, Reason: reason}}}
}

func (s *Service) SetDismissed(ctx context.Context, ids []int64, dismissed bool) (BatchResult, error) {
	selected, err := selectedFindingIDs(ids)
	if err != nil {
		return BatchResult{}, err
	}
	release, err := s.beginMutation()
	if err != nil {
		return BatchResult{}, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BatchResult{}, storageFailure(err)
	}
	defer tx.Rollback()
	result := BatchResult{Requested: int64(len(selected)), Items: make([]BatchItem, 0, len(selected))}
	itemIDs := []string{}
	seenItems := map[string]bool{}
	changedItems := []string{}
	nowMS := s.now().UnixMilli()
	for _, id := range selected {
		var itemID string
		err := tx.QueryRowContext(ctx, `SELECT item_id FROM findings WHERE id=?`, id).Scan(&itemID)
		if errors.Is(err, sql.ErrNoRows) {
			result.Items = append(result.Items, missingBatchFinding(id))
			result.Skipped++
			continue
		}
		if err != nil {
			return BatchResult{}, storageFailure(err)
		}
		if !seenItems[itemID] {
			changed, err := setListingFlagsTx(ctx, tx, itemID, nil, &dismissed, nowMS)
			if err != nil {
				return BatchResult{}, storageFailure(err)
			}
			itemIDs = append(itemIDs, itemID)
			seenItems[itemID] = true
			if changed {
				changedItems = append(changedItems, itemID)
			}
		}
		result.Items = append(result.Items, BatchItem{ID: id, Status: BatchApplied})
		result.Applied++
	}
	if dismissed {
		if err := cancelPendingIntentsForItemsTx(ctx, tx, itemIDs, locales.Message{Code: "notify.cancelled_dismissed"}, nowMS); err != nil {
			return BatchResult{}, storageFailure(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return BatchResult{}, storageFailure(err)
	}
	if len(changedItems) > 0 {
		_ = s.emit("finding.changed", map[string]any{"all": true, "item_ids": changedItems})
	}
	return result, nil
}

func (s *Service) DeleteFindings(ctx context.Context, ids []int64) (BatchResult, error) {
	selected, err := selectedFindingIDs(ids)
	if err != nil {
		return BatchResult{}, err
	}
	release, err := s.beginMutation()
	if err != nil {
		return BatchResult{}, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BatchResult{}, storageFailure(err)
	}
	defer tx.Rollback()
	result := BatchResult{Requested: int64(len(selected)), Items: make([]BatchItem, 0, len(selected))}
	deleted := []int64{}
	for _, id := range selected {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM findings WHERE id=?)`, id).Scan(&exists); err != nil {
			return BatchResult{}, storageFailure(err)
		}
		if !exists {
			result.Items = append(result.Items, missingBatchFinding(id))
			result.Skipped++
			continue
		}
		deleted = append(deleted, id)
		result.Items = append(result.Items, BatchItem{ID: id, Status: BatchApplied})
		result.Applied++
	}
	if err := cancelPendingIntentsForFindingsTx(ctx, tx, deleted, locales.Message{Code: "notify.cancelled_deleted"}, s.now().UnixMilli()); err != nil {
		return BatchResult{}, storageFailure(err)
	}
	for _, id := range deleted {
		if _, err := tx.ExecContext(ctx, `DELETE FROM findings WHERE id=?`, id); err != nil {
			return BatchResult{}, storageFailure(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return BatchResult{}, storageFailure(err)
	}
	if len(deleted) > 0 {
		_ = s.emit("finding.changed", map[string]any{"ids": deleted})
	}
	return result, nil
}

func (s *Service) DeleteTaskFindings(ctx context.Context, taskID int64) (int64, error) {
	if taskID <= 0 {
		return 0, &ServiceError{Kind: ErrorInvalid, Reason: locales.Message{Code: "tasks.invalid_id"}}
	}
	release, err := s.beginMutation()
	if err != nil {
		return 0, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, storageFailure(err)
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=?)`, taskID).Scan(&exists); err != nil {
		return 0, storageFailure(err)
	}
	if !exists {
		return 0, &ServiceError{Kind: ErrorNotFound, Reason: locales.Message{Code: "tasks.not_found"}}
	}
	rows, err := tx.QueryContext(ctx, `SELECT f.id FROM findings f JOIN listings l ON l.item_id=f.item_id WHERE f.task_id=? AND l.is_favorite=0 ORDER BY f.id`, taskID)
	if err != nil {
		return 0, storageFailure(err)
	}
	deleted := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, storageFailure(err)
		}
		deleted = append(deleted, id)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		return 0, storageFailure(errors.Join(err, closeErr))
	}
	if err := cancelPendingIntentsForFindingsTx(ctx, tx, deleted, locales.Message{Code: "notify.cancelled_deleted"}, s.now().UnixMilli()); err != nil {
		return 0, storageFailure(err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM findings WHERE task_id=? AND item_id IN (SELECT item_id FROM listings WHERE is_favorite=0)`, taskID)
	if err != nil {
		return 0, storageFailure(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return 0, storageFailure(err)
	}
	if count > 0 {
		_ = s.emit("finding.changed", map[string]any{"all": true, "task_id": taskID})
	}
	return count, nil
}

func (s *Service) BlockSeller(ctx context.Context, sellerID string, nick *string) (BlockedSeller, error) {
	if !validLocalID(sellerID) {
		return BlockedSeller{}, &ServiceError{Kind: ErrorInvalid, Reason: locales.Message{Code: "blocked_sellers.invalid_id"}}
	}
	if nick != nil && !utf8.ValidString(*nick) {
		return BlockedSeller{}, &ServiceError{Kind: ErrorInvalid, Reason: locales.Message{Code: "blocked_sellers.invalid_nick"}}
	}
	release, err := s.beginMutation()
	if err != nil {
		return BlockedSeller{}, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BlockedSeller{}, storageFailure(err)
	}
	defer tx.Rollback()
	nowMS := s.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO blocked_sellers(seller_id,nick,created_at) VALUES(?,?,?) ON CONFLICT(seller_id) DO UPDATE SET nick=COALESCE(excluded.nick,blocked_sellers.nick)`, sellerID, nick, nowMS); err != nil {
		return BlockedSeller{}, storageFailure(err)
	}
	if err := cancelPendingIntentsForSellerTx(ctx, tx, sellerID, locales.Message{Code: "notify.cancelled_blocked"}, nowMS); err != nil {
		return BlockedSeller{}, storageFailure(err)
	}
	var saved BlockedSeller
	if err := tx.QueryRowContext(ctx, `SELECT seller_id,nick,created_at FROM blocked_sellers WHERE seller_id=?`, sellerID).Scan(&saved.SellerID, &saved.Nick, &saved.CreatedAt); err != nil {
		return BlockedSeller{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return BlockedSeller{}, storageFailure(err)
	}
	_ = s.emit("finding.changed", map[string]any{"all": true, "seller_id": sellerID})
	return saved, nil
}

func (s *Service) UnblockSeller(ctx context.Context, sellerID string) error {
	if !validLocalID(sellerID) {
		return &ServiceError{Kind: ErrorInvalid, Reason: locales.Message{Code: "blocked_sellers.invalid_id"}}
	}
	release, err := s.beginMutation()
	if err != nil {
		return err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageFailure(err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM blocked_sellers WHERE seller_id=?`, sellerID)
	if err != nil {
		return storageFailure(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure(err)
	}
	if count > 0 {
		_ = s.emit("finding.changed", map[string]any{"all": true, "seller_id": sellerID})
	}
	return nil
}

type blockedSellerCursor struct {
	CreatedAt *int64 `json:"created_at"`
	SellerID  string `json:"seller_id"`
}

func (s *Service) ListBlockedSellers(ctx context.Context, query ListQuery) (Page[BlockedSeller], error) {
	query, err := normalizeListQuery(query)
	if err != nil {
		return Page[BlockedSeller]{}, err
	}
	var anchor blockedSellerCursor
	if err := decodeCursor(query.Cursor, "blocked_sellers", ListQuery{}, &anchor); err != nil {
		return Page[BlockedSeller]{}, err
	}
	if query.Cursor != "" && !validLocalID(anchor.SellerID) {
		return Page[BlockedSeller]{}, queryError()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Page[BlockedSeller]{}, storageFailure(err)
	}
	defer tx.Rollback()
	result := Page[BlockedSeller]{Items: []BlockedSeller{}}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blocked_sellers`).Scan(&result.Total); err != nil {
		return Page[BlockedSeller]{}, storageFailure(err)
	}
	sqlText := `SELECT seller_id,nick,created_at FROM blocked_sellers`
	args := []any{}
	if query.Cursor != "" {
		if anchor.CreatedAt == nil {
			sqlText += ` WHERE created_at IS NULL AND seller_id>?`
			args = append(args, anchor.SellerID)
		} else {
			sqlText += ` WHERE created_at IS NULL OR created_at<? OR (created_at=? AND seller_id>?)`
			args = append(args, *anchor.CreatedAt, *anchor.CreatedAt, anchor.SellerID)
		}
	}
	sqlText += ` ORDER BY created_at IS NULL,created_at DESC,seller_id ASC LIMIT ?`
	args = append(args, query.Limit+1)
	rows, err := tx.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return Page[BlockedSeller]{}, storageFailure(err)
	}
	for rows.Next() {
		var seller BlockedSeller
		if err := rows.Scan(&seller.SellerID, &seller.Nick, &seller.CreatedAt); err != nil {
			rows.Close()
			return Page[BlockedSeller]{}, storageFailure(err)
		}
		result.Items = append(result.Items, seller)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		return Page[BlockedSeller]{}, storageFailure(errors.Join(err, closeErr))
	}
	if int64(len(result.Items)) > query.Limit {
		result.Items = result.Items[:query.Limit]
		last := result.Items[len(result.Items)-1]
		cursor, err := encodeCursor("blocked_sellers", ListQuery{}, blockedSellerCursor{CreatedAt: last.CreatedAt, SellerID: last.SellerID})
		if err != nil {
			return Page[BlockedSeller]{}, err
		}
		result.NextCursor = &cursor
	}
	if err := tx.Commit(); err != nil {
		return Page[BlockedSeller]{}, storageFailure(err)
	}
	return result, nil
}

func validLocalID(id string) bool {
	return utf8.ValidString(id) && strings.TrimSpace(id) != ""
}
