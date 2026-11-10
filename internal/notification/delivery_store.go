package notification

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/service"
)

func loadDeliveryIntentTx(ctx context.Context, tx *sql.Tx, id int64) (intent, error) {
	var value intent
	var reason sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id,finding_id,task_id,kind,state,created_at,updated_at,price,previous_price,failure_count,first_failed_at,sent_at,reason FROM notification_intents WHERE id=?`, id).
		Scan(&value.ID, &value.FindingID, &value.TaskID, &value.Kind, &value.State, &value.CreatedAt, &value.UpdatedAt, &value.Price, &value.PreviousPrice, &value.FailureCount, &value.FirstFailedAt, &value.SentAt, &reason)
	if err != nil {
		return intent{}, err
	}
	if reason.Valid {
		value.Reason, err = decodeNotificationMessage([]byte(reason.String))
		if err != nil || value.Reason == nil || value.Reason.Code == "" {
			return intent{}, errors.New("通知意图的原因记录损坏")
		}
	}
	return value, nil
}

func taskDeliveryKind(group messageGroup) (string, error) {
	if group.Summary {
		return "summary", nil
	}
	if len(group.Members) != 1 {
		return "", errors.New("单条通知的成员数量无效")
	}
	switch group.Members[0].Intent.Kind {
	case intentRecommended, intentPriceUpgrade:
		return "recommended", nil
	case intentNew:
		return "all_new", nil
	case intentPriceDrop:
		return "price_drop", nil
	default:
		return "", errors.New("通知意图的种类无效")
	}
}

func (s *Service) recordTaskAttemptTx(ctx context.Context, tx *sql.Tx, ref service.RunRef, group messageGroup, kind string, outcome channelOutcome) (int64, error) {
	if !outcome.Attempted || !outcome.CalledSender || len(group.Members) == 0 {
		return 0, errors.New("没有实际发送，不能记录投递")
	}
	var errorJSON any
	if !outcome.Result.OK {
		raw, err := json.Marshal(deliveryError(outcome.ChannelName, outcome.Result))
		if err != nil {
			return 0, err
		}
		errorJSON = string(raw)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO deliveries(at,channel_id,task_id,kind,item_count,ok,error) VALUES(?,?,?,?,?,?,?)`, outcome.EndedAt, outcome.ChannelID, ref.TaskID, kind, len(group.Members), outcome.Result.OK, errorJSON)
	if err != nil {
		return 0, err
	}
	deliveryID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, value := range group.Members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO delivery_items(delivery_id,finding_id) VALUES(?,?)`, deliveryID, value.Finding.ID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO delivery_intents(delivery_id,intent_id) VALUES(?,?)`, deliveryID, value.Intent.ID); err != nil {
			return 0, err
		}
		if !outcome.Result.OK {
			continue
		}
		// 真实送达留在原意图上；改代或删除以后，不把旧结果写回当前发现。
		if _, err := tx.ExecContext(ctx, `UPDATE notification_intents SET state='sent',sent_at=COALESCE(sent_at,?),updated_at=?,reason=NULL WHERE id=? AND finding_id=? AND task_id=? AND kind=? AND state IN ('pending','cancelled','abandoned')`,
			outcome.EndedAt, outcome.EndedAt, value.Intent.ID, value.Finding.ID, ref.TaskID, value.Intent.Kind); err != nil {
			return 0, err
		}
		switch value.Intent.Kind {
		case intentRecommended, intentNew, intentPriceUpgrade:
			_, err = tx.ExecContext(ctx, `UPDATE findings SET recommended_price=CASE WHEN ?='recommended' THEN
				CASE WHEN notified_at IS NULL OR recommended_price IS NULL OR ?<recommended_price THEN ? ELSE recommended_price END
				ELSE recommended_price END,notified_at=COALESCE(notified_at,?) WHERE id=? AND task_id=? AND search_generation=? AND EXISTS(SELECT 1 FROM tasks WHERE id=? AND search_generation=?) AND (?='price_upgrade' OR notify_kind=?)`,
				string(value.Intent.Kind), value.Intent.Price, value.Intent.Price, outcome.EndedAt, value.Finding.ID, ref.TaskID, ref.SearchGeneration, ref.TaskID, ref.SearchGeneration, string(value.Intent.Kind), string(value.Intent.Kind))
		case intentPriceDrop:
			_, err = tx.ExecContext(ctx, `UPDATE findings SET last_drop_notified_price=?,drop_notified_at=? WHERE id=? AND task_id=? AND search_generation=? AND EXISTS(SELECT 1 FROM tasks WHERE id=? AND search_generation=?) AND (last_drop_notified_price IS NULL OR ?<last_drop_notified_price)`,
				value.Intent.Price, outcome.EndedAt, value.Finding.ID, ref.TaskID, ref.SearchGeneration, ref.TaskID, ref.SearchGeneration, value.Intent.Price)
		default:
			err = errors.New("通知意图的种类无效")
		}
		if err != nil {
			return 0, err
		}
	}
	return deliveryID, nil
}

func intentRetryExpired(value intent, nowMS int64) bool {
	return value.FailureCount >= 3 || value.FirstFailedAt != nil && nowMS > *value.FirstFailedAt && uint64(nowMS)-uint64(*value.FirstFailedAt) > uint64((24*time.Hour).Milliseconds())
}

func abandonDeliveryIntentTx(ctx context.Context, tx *sql.Tx, value intent, nowMS int64) error {
	reason, err := json.Marshal(locales.Message{Code: "notify.retry_exhausted"})
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE notification_intents SET state='abandoned',reason=?,updated_at=? WHERE id=? AND state='pending'`, string(reason), nowMS, value.ID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 || value.Kind != intentRecommended && value.Kind != intentNew {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE findings SET notify_skip_reason=? WHERE id=? AND task_id=? AND notified_at IS NULL AND notify_kind=? AND EXISTS(SELECT 1 FROM tasks WHERE tasks.id=findings.task_id AND tasks.search_generation=findings.search_generation)`,
		string(reason), value.FindingID, value.TaskID, string(value.Kind))
	return err
}

func (s *Service) finishRoundTx(ctx context.Context, tx *sql.Tx, group messageGroup, outcomes []channelOutcome, nowMS int64) error {
	if len(outcomes) == 0 {
		return nil
	}
	for _, member := range group.Members {
		// 摘要等待时可能删去不同成员，只有各渠道都实际包含本意图才算全败。
		allFailed := true
		for _, outcome := range outcomes {
			if !outcome.Attempted || outcome.Result.OK || outcome.Result.ErrorCode == "cancelled" || !slices.Contains(outcome.IntentIDs, member.Intent.ID) {
				allFailed = false
				break
			}
		}
		if !allFailed {
			continue
		}
		value, err := loadDeliveryIntentTx(ctx, tx, member.Intent.ID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if value.State != intentPending {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE notification_intents SET failure_count=failure_count+1,first_failed_at=COALESCE(first_failed_at,?),updated_at=? WHERE id=? AND state='pending'`, nowMS, nowMS, value.ID); err != nil {
			return err
		}
		value.FailureCount++
		if value.FirstFailedAt == nil {
			value.FirstFailedAt = &nowMS
		}
		if intentRetryExpired(value, nowMS) {
			if err := abandonDeliveryIntentTx(ctx, tx, value, nowMS); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) refreshTaskGroupTx(ctx context.Context, tx *sql.Tx, ref service.RunRef, original messageGroup, nowMS int64) (service.Task, messageGroup, error) {
	task, err := service.NotificationTaskTx(ctx, tx, ref.TaskID)
	if err != nil {
		return service.Task{}, messageGroup{}, err
	}
	group := messageGroup{Summary: original.Summary}
	if task.SearchGeneration != ref.SearchGeneration {
		return task, group, nil
	}
	for _, member := range original.Members {
		value, err := loadDeliveryIntentTx(ctx, tx, member.Intent.ID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return service.Task{}, messageGroup{}, err
		}
		if value.FindingID != member.Finding.ID || value.TaskID != ref.TaskID || value.Kind != member.Intent.Kind || value.State != intentPending && value.State != intentSent {
			continue
		}
		current, err := loadNotificationCandidateTx(ctx, tx, value.FindingID)
		if notificationNotFound(err) {
			continue
		}
		if err != nil {
			return service.Task{}, messageGroup{}, err
		}
		current.Intent = value
		if current.Finding.TaskID != ref.TaskID || current.Finding.SearchGeneration != ref.SearchGeneration {
			continue
		}
		if value.State == intentPending && intentRetryExpired(value, nowMS) {
			if err := abandonDeliveryIntentTx(ctx, tx, value, nowMS); err != nil {
				return service.Task{}, messageGroup{}, err
			}
			continue
		}
		if !concluded(current.Finding.Decision) {
			continue
		}
		priceChange := value.Kind == intentPriceDrop || value.Kind == intentPriceUpgrade
		if current.ReanalysisOrigin && value.Kind != intentPriceDrop {
			eligible, reason, err := revalidateReanalysisIntent(task, current, value, true)
			if err != nil {
				return service.Task{}, messageGroup{}, err
			}
			if !eligible && (reason == nil || priceChange && isBudgetReason(reason)) {
				continue
			}
			if !eligible && value.State == intentPending {
				if err := finishPlannedIntentTx(ctx, tx, current, value, intentCancelled, reason, nowMS); err != nil {
					return service.Task{}, messageGroup{}, err
				}
			}
			if eligible && value.Kind == intentPriceUpgrade && (current.Listing.Price == nil || value.PreviousPrice == nil || *current.Listing.Price >= *value.PreviousPrice) {
				continue
			}
			consumed, err := consumeReanalysisOriginTx(ctx, tx, current, value, eligible, nowMS)
			if err != nil {
				return service.Task{}, messageGroup{}, err
			}
			if !eligible || !consumed {
				continue
			}
			current, err = loadNotificationCandidateTx(ctx, tx, value.FindingID)
			if err != nil {
				return service.Task{}, messageGroup{}, err
			}
			value, err = loadDeliveryIntentTx(ctx, tx, value.ID)
			if err != nil {
				return service.Task{}, messageGroup{}, err
			}
			current.Intent = value
		}
		if task.NotifyPolicy == "none" || current.Listing.IsDismissed || current.SellerBlocked {
			continue
		}
		if value.Kind == intentRecommended || value.Kind == intentNew {
			if value.Kind == intentRecommended && current.Finding.Decision == "rejected" || isAvoid(current.Finding) || rejectedAs(current.Finding, "filter", "excluded", "junk") {
				continue
			}
		}
		if task.BudgetMax != nil {
			if current.Listing.Price == nil {
				if !priceChange && value.State == intentPending {
					if err := finishPlannedIntentTx(ctx, tx, current, value, intentCancelled, notificationReason("notify.price_unknown_budget", nil), nowMS); err != nil {
						return service.Task{}, messageGroup{}, err
					}
				}
				continue
			}
			if value.Price == nil || *value.Price > *task.BudgetMax || *current.Listing.Price > *task.BudgetMax {
				continue
			}
		}
		if priceChange {
			if !task.NotifyPriceDrop || current.Finding.Decision == "rejected" || isAvoid(current.Finding) {
				continue
			}
			if value.Kind == intentPriceUpgrade {
				eligible, _, err := revalidateReanalysisIntent(task, current, value, true)
				if err != nil {
					return service.Task{}, messageGroup{}, err
				}
				if !eligible {
					continue
				}
			}
			if current.Listing.Price == nil || value.Price == nil || value.PreviousPrice == nil || *current.Listing.Price >= *value.PreviousPrice || *current.Listing.Price != *value.Price {
				continue
			}
		}
		group.Members = append(group.Members, current)
	}
	return task, group, nil
}
