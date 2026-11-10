package notification

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
)

func (s *Service) planRunTx(ctx context.Context, tx *sql.Tx, ref service.RunRef, firstConclusionIDs []int64, nowMS int64) ([]candidate, error) {
	task, err := service.NotificationTaskTx(ctx, tx, ref.TaskID)
	if err != nil {
		return nil, err
	}
	if task.SearchGeneration != ref.SearchGeneration {
		return nil, &service.ServiceError{Kind: service.ErrorConflict, Reason: *notificationReason("findings.changed", nil)}
	}
	var hasChannel bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_channels tc JOIN channels c ON c.id=tc.channel_id
		WHERE tc.task_id=? AND c.enabled=1)`, task.ID).Scan(&hasChannel); err != nil {
		return nil, err
	}
	// 内存里的本轮编号不足以覆盖结论保存后、首次判定前的崩溃。
	rows, err := tx.QueryContext(ctx, `SELECT id FROM findings WHERE task_id=? AND search_generation=? ORDER BY found_at,id`, task.ID, task.SearchGeneration)
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
	if err := cancelOldGenerationIntentsTx(ctx, tx, task, nowMS); err != nil {
		return nil, err
	}
	result := []candidate{}
	for _, id := range ids {
		value, err := loadNotificationCandidateTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		decision, err := decideFirst(task, value, hasChannel)
		if err != nil {
			return nil, err
		}
		if decision.Apply {
			applied, err := applyFirstDecisionTx(ctx, tx, task, value, decision)
			if err != nil {
				return nil, err
			}
			if !applied {
				continue
			}
			if decision.Kind != nil {
				if err := insertPlannedIntentTx(ctx, tx, value, intentKind(*decision.Kind), value.Listing.Price, nil, nowMS); err != nil {
					return nil, err
				}
			}
			value, err = loadNotificationCandidateTx(ctx, tx, id)
			if err != nil {
				return nil, err
			}
		}
		pending, err := loadPendingPlannedIntentTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if pending != nil {
			s.mu.Lock()
			_, busy := s.intentInFlight[pending.ID]
			s.mu.Unlock()
			if busy {
				continue
			}
			selected, err := preparePendingIntentTx(ctx, tx, task, value, *pending, hasChannel, nowMS)
			if err != nil {
				return nil, err
			}
			if selected != nil {
				result = append(result, *selected)
			}
			continue
		}
		if err := planPriceIntentTx(ctx, tx, task, value, hasChannel, nowMS); err != nil {
			return nil, err
		}
		pending, err = loadPendingPlannedIntentTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if pending != nil {
			value, err = loadNotificationCandidateTx(ctx, tx, id)
			if err != nil {
				return nil, err
			}
			value.Intent = *pending
			result = append(result, value)
		}
	}
	return result, nil
}

func loadNotificationCandidateTx(ctx context.Context, tx *sql.Tx, id int64) (candidate, error) {
	value, err := service.NotificationFindingTx(ctx, tx, id)
	if err != nil {
		return candidate{}, err
	}
	return candidate{Finding: value.Finding, Listing: value.Listing, ItemSpecs: value.ItemSpecs,
		Seller: value.Seller, SellerBlocked: value.SellerBlocked, ReanalysisOrigin: value.ReanalysisOrigin}, nil
}

func applyFirstDecisionTx(ctx context.Context, tx *sql.Tx, task service.Task, value candidate, decision firstDecision) (bool, error) {
	var reason any
	if decision.SkipReason != nil {
		raw, err := json.Marshal(decision.SkipReason)
		if err != nil {
			return false, err
		}
		reason = string(raw)
	}
	finding := value.Finding
	result, err := tx.ExecContext(ctx, `UPDATE findings SET notify_kind=?,notify_skip_reason=?,reanalysis_origin=0,
		recommended_price=CASE WHEN ? THEN COALESCE(recommended_price,?) ELSE recommended_price END
		WHERE id=? AND task_id=? AND search_generation=? AND processing_revision=?
		AND notify_kind IS NULL AND notify_skip_reason IS NULL AND decision IN ('scored','matched','rejected')
		AND EXISTS(SELECT 1 FROM tasks WHERE id=? AND search_generation=?)`,
		decision.Kind, reason, decision.SetRecommendedPrice, decision.RecommendedPrice,
		finding.ID, task.ID, task.SearchGeneration, finding.ProcessingRevision, task.ID, task.SearchGeneration)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func insertPlannedIntentTx(ctx context.Context, tx *sql.Tx, value candidate, kind intentKind, price, previous *int64, nowMS int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO notification_intents(finding_id,task_id,kind,state,created_at,updated_at,price,previous_price)
		SELECT id,task_id,?,'pending',?,?,?,? FROM findings WHERE id=? AND task_id=? AND search_generation=?
		AND processing_revision=? AND NOT EXISTS(SELECT 1 FROM notification_intents WHERE finding_id=? AND state='pending')`,
		kind, nowMS, nowMS, price, previous, value.Finding.ID, value.Finding.TaskID,
		value.Finding.SearchGeneration, value.Finding.ProcessingRevision, value.Finding.ID)
	return err
}

func cancelOldGenerationIntentsTx(ctx context.Context, tx *sql.Tx, task service.Task, nowMS int64) error {
	raw, err := json.Marshal(notificationReason("notify.cancelled_search", nil))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE notification_intents SET state='cancelled',updated_at=?,reason=?
		WHERE task_id=? AND state='pending' AND NOT EXISTS(SELECT 1 FROM findings f
		WHERE f.id=notification_intents.finding_id AND f.task_id=? AND f.search_generation=?)`,
		nowMS, string(raw), task.ID, task.ID, task.SearchGeneration)
	return err
}

func loadPendingPlannedIntentTx(ctx context.Context, tx *sql.Tx, findingID int64) (*intent, error) {
	var value intent
	var reason sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id,finding_id,task_id,kind,state,created_at,updated_at,price,previous_price,
		failure_count,first_failed_at,sent_at,reason FROM notification_intents WHERE finding_id=? AND state='pending'`, findingID).
		Scan(&value.ID, &value.FindingID, &value.TaskID, &value.Kind, &value.State, &value.CreatedAt, &value.UpdatedAt,
			&value.Price, &value.PreviousPrice, &value.FailureCount, &value.FirstFailedAt, &value.SentAt, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if reason.Valid {
		if err := json.Unmarshal([]byte(reason.String), &value.Reason); err != nil {
			return nil, err
		}
	}
	return &value, nil
}

func preparePendingIntentTx(ctx context.Context, tx *sql.Tx, task service.Task, value candidate, pending intent, hasChannel bool, nowMS int64) (*candidate, error) {
	if pending.TaskID != task.ID {
		return nil, errors.New("通知意图与任务不一致")
	}
	if pending.FailureCount >= 3 || pending.FirstFailedAt != nil && nowMS > *pending.FirstFailedAt &&
		uint64(nowMS)-uint64(*pending.FirstFailedAt) > uint64(24*time.Hour/time.Millisecond) {
		if err := finishPlannedIntentTx(ctx, tx, value, pending, intentAbandoned, notificationReason("notify.retry_exhausted", nil), nowMS); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if value.ReanalysisOrigin && (pending.Kind == intentRecommended || pending.Kind == intentNew || pending.Kind == intentPriceUpgrade) {
		eligible, reason, err := revalidateReanalysisIntent(task, value, pending, hasChannel)
		if err != nil {
			return nil, err
		}
		if !eligible {
			if reason == nil {
				return nil, nil
			}
			if err := finishPlannedIntentTx(ctx, tx, value, pending, intentCancelled, reason, nowMS); err != nil {
				return nil, err
			}
			_, err = consumeReanalysisOriginTx(ctx, tx, value, pending, false, nowMS)
			return nil, err
		}
		if pending.Kind == intentPriceUpgrade && (value.Listing.Price == nil || pending.PreviousPrice == nil || *value.Listing.Price >= *pending.PreviousPrice) {
			return nil, nil
		}
		consumed, err := consumeReanalysisOriginTx(ctx, tx, value, pending, true, nowMS)
		if err != nil || !consumed {
			return nil, err
		}
		value, err = loadNotificationCandidateTx(ctx, tx, value.Finding.ID)
		if err != nil {
			return nil, err
		}
		updated, err := loadPendingPlannedIntentTx(ctx, tx, value.Finding.ID)
		if err != nil || updated == nil {
			return nil, err
		}
		value.Intent = *updated
		return &value, nil
	}
	if value.Listing.IsDismissed || value.SellerBlocked {
		return nil, finishPlannedIntentTx(ctx, tx, value, pending, intentCancelled, notificationReason("notify.cancelled_unavailable", nil), nowMS)
	}
	if task.NotifyPolicy == "none" || (pending.Kind == intentPriceDrop || pending.Kind == intentPriceUpgrade) && !task.NotifyPriceDrop {
		return nil, finishPlannedIntentTx(ctx, tx, value, pending, intentCancelled, notificationReason("notify.cancelled_policy", nil), nowMS)
	}
	if !concluded(value.Finding.Decision) || !hasChannel {
		return nil, nil
	}
	if pending.Kind == intentRecommended || pending.Kind == intentNew {
		if isAvoid(value.Finding) || pending.Kind == intentRecommended && value.Finding.Decision == analysis.DecisionRejected ||
			rejectedAs(value.Finding, analysis.RejectFilter, analysis.RejectExcluded, analysis.RejectJunk) {
			return nil, nil
		}
	}
	if pending.Kind == intentPriceDrop || pending.Kind == intentPriceUpgrade {
		if value.Finding.Decision == analysis.DecisionRejected || isAvoid(value.Finding) {
			return nil, finishPlannedIntentTx(ctx, tx, value, pending, intentCancelled, notificationReason("notify.cancelled_unavailable", nil), nowMS)
		}
	}
	if pending.Kind != intentPriceDrop {
		if exceedsBudget(task, value.Listing) {
			return nil, nil
		}
		if value.Listing.Price == nil && task.BudgetMax != nil {
			return nil, finishPlannedIntentTx(ctx, tx, value, pending, intentCancelled, notificationReason("notify.price_unknown_budget", nil), nowMS)
		}
	}
	if pending.Kind == intentPriceUpgrade {
		eligible, _, err := revalidateReanalysisIntent(task, value, pending, hasChannel)
		if err != nil || !eligible {
			return nil, err
		}
	}
	if pending.Kind == intentPriceDrop || pending.Kind == intentPriceUpgrade {
		if value.Listing.Price == nil || pending.PreviousPrice == nil || *value.Listing.Price >= *pending.PreviousPrice {
			return nil, nil
		}
	}
	if pending.Kind == intentRecommended || pending.Kind == intentNew {
		if value.Finding.NotifySkipReason != nil {
			return nil, finishPlannedIntentTx(ctx, tx, value, pending, intentCancelled, notificationReason("notify.cancelled_unavailable", nil), nowMS)
		}
	}
	if pending.Kind == intentPriceUpgrade {
		result, err := tx.ExecContext(ctx, `UPDATE findings SET recommended_price=?
			WHERE id=? AND task_id=? AND search_generation=? AND processing_revision=?`, value.Listing.Price,
			value.Finding.ID, value.Finding.TaskID, value.Finding.SearchGeneration, value.Finding.ProcessingRevision)
		if err != nil {
			return nil, err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return nil, err
		}
		value.Finding.RecommendedPrice = copyPrice(value.Listing.Price)
	}
	// 重发沿用同一提醒的失败窗口，价格快照取本次实际看到的现价。
	if _, err := tx.ExecContext(ctx, `UPDATE notification_intents SET price=?,updated_at=? WHERE id=? AND state='pending'`,
		value.Listing.Price, nowMS, pending.ID); err != nil {
		return nil, err
	}
	pending.Price, pending.UpdatedAt = copyPrice(value.Listing.Price), nowMS
	value.Intent = pending
	return &value, nil
}

func finishPlannedIntentTx(ctx context.Context, tx *sql.Tx, value candidate, pending intent, state intentState, reason *locales.Message, nowMS int64) error {
	raw, err := json.Marshal(reason)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE notification_intents SET state=?,updated_at=?,reason=?
		WHERE id=? AND finding_id=? AND task_id=? AND state='pending'
		AND EXISTS(SELECT 1 FROM findings f JOIN tasks t ON t.id=f.task_id WHERE f.id=? AND f.task_id=?
		AND f.search_generation=? AND f.processing_revision=? AND t.search_generation=?)`,
		state, nowMS, string(raw), pending.ID, value.Finding.ID, value.Finding.TaskID,
		value.Finding.ID, value.Finding.TaskID, value.Finding.SearchGeneration, value.Finding.ProcessingRevision, value.Finding.SearchGeneration)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return err
	}
	writeCancellation := state == intentCancelled && (value.ReanalysisOrigin || reason != nil && reason.Code == "notify.price_unknown_budget")
	if state == intentAbandoned && (pending.Kind == intentRecommended || pending.Kind == intentNew) || writeCancellation {
		_, err = tx.ExecContext(ctx, `UPDATE findings SET notify_skip_reason=? WHERE id=? AND task_id=? AND search_generation=?
			AND processing_revision=? AND (? OR notified_at IS NULL)`, string(raw), value.Finding.ID, value.Finding.TaskID,
			value.Finding.SearchGeneration, value.Finding.ProcessingRevision, writeCancellation)
	}
	return err
}

func consumeReanalysisOriginTx(ctx context.Context, tx *sql.Tx, value candidate, pending intent, eligible bool, nowMS int64) (bool, error) {
	setPrice := eligible && pending.State == intentPending && (pending.Kind == intentRecommended || pending.Kind == intentPriceUpgrade)
	finding := value.Finding
	result, err := tx.ExecContext(ctx, `UPDATE findings SET reanalysis_origin=0,
		recommended_price=CASE WHEN ? THEN ? ELSE recommended_price END
		WHERE id=? AND task_id=? AND search_generation=? AND processing_revision=? AND reanalysis_origin=1
		AND (notify_kind IS NOT NULL OR notify_skip_reason IS NOT NULL)
		AND EXISTS(SELECT 1 FROM tasks WHERE id=? AND search_generation=?)`,
		setPrice, value.Listing.Price, finding.ID, finding.TaskID, finding.SearchGeneration, finding.ProcessingRevision,
		finding.TaskID, finding.SearchGeneration)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return false, err
	}
	if eligible && pending.State == intentPending {
		result, err := tx.ExecContext(ctx, `UPDATE notification_intents SET price=?,updated_at=?
			WHERE id=? AND finding_id=? AND task_id=? AND state='pending'`, value.Listing.Price, nowMS, pending.ID, finding.ID, finding.TaskID)
		if err != nil {
			return false, err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			if err == nil {
				err = errors.New("重新分析的待发意图已变化")
			}
			return false, err
		}
	}
	return true, nil
}

func planPriceIntentTx(ctx context.Context, tx *sql.Tx, task service.Task, value candidate, hasChannel bool, nowMS int64) error {
	finding := value.Finding
	if !concluded(finding.Decision) || value.Listing.IsDismissed || value.SellerBlocked ||
		finding.Decision == analysis.DecisionRejected || isAvoid(finding) || value.Listing.Price == nil {
		return nil
	}
	if finding.RecommendedPrice == nil && task.JudgeMode == analysis.ModeAI && finding.Decision == analysis.DecisionScored &&
		finding.ScoredPrice != nil && *value.Listing.Price < *finding.ScoredPrice {
		if err := validateNotificationFacts(task, finding); err != nil {
			return err
		}
		reached, err := reachesRecommendation(task, value)
		if err != nil || !reached {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE findings SET recommended_price=? WHERE id=? AND task_id=? AND search_generation=?
			AND processing_revision=? AND recommended_price IS NULL`, value.Listing.Price, finding.ID, finding.TaskID,
			finding.SearchGeneration, finding.ProcessingRevision)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return err
		}
		if task.NotifyPolicy != "none" && task.NotifyPriceDrop && hasChannel {
			return insertPlannedIntentTx(ctx, tx, value, intentPriceUpgrade, value.Listing.Price, finding.ScoredPrice, nowMS)
		}
		return nil
	}
	if finding.RecommendedPrice == nil || task.NotifyPolicy == "none" || !task.NotifyPriceDrop || !hasChannel {
		return nil
	}
	baseline := finding.LastDropNotifiedPrice
	if baseline == nil {
		baseline = finding.RecommendedPrice
	}
	if *value.Listing.Price >= *baseline {
		return nil
	}
	// 相同价格的已终止提醒不能每轮新建而绕过重发上限。
	var lastPrice *int64
	err := tx.QueryRowContext(ctx, `SELECT price FROM notification_intents WHERE finding_id=? AND kind IN ('recommended','price_drop','price_upgrade')
		AND state IN ('abandoned','cancelled') ORDER BY id DESC LIMIT 1`, finding.ID).Scan(&lastPrice)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if lastPrice != nil && *value.Listing.Price >= *lastPrice {
		return nil
	}
	return insertPlannedIntentTx(ctx, tx, value, intentPriceDrop, value.Listing.Price, baseline, nowMS)
}
