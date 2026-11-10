package notification

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/service"
)

type systemRetryState struct {
	failureCount  int64
	firstFailedAt *int64
}

type systemAttempt struct {
	outcome    channelOutcome
	deliveryID int64
}

func (s *Service) System(ctx context.Context, eventID int64) error {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return err
	}
	defer done()
	if eventID < 1 {
		return errors.New("系统事件编号无效")
	}
	s.mu.Lock()
	if _, exists := s.eventInFlight[eventID]; exists {
		s.mu.Unlock()
		return nil
	}
	if s.eventInFlight == nil {
		s.eventInFlight = make(map[int64]struct{})
	}
	s.eventInFlight[eventID] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.eventInFlight, eventID)
		s.mu.Unlock()
	}()

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return s.storageFailure(err)
	}
	defer tx.Rollback()
	event, err := s.loadSystemEventTx(ctx, tx, eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return s.storageFailure(err)
	}
	if event.ResolvedAt != nil || event.NotifiedAt != nil {
		return nil
	}
	retry, err := loadSystemRetryTx(ctx, tx, eventID)
	if err != nil {
		return s.storageFailure(err)
	}
	if retry.expired(s.now().UnixMilli()) {
		return nil
	}
	snapshot, err := s.settings.SnapshotTx(ctx, tx)
	if err != nil {
		return s.storageFailure(err)
	}
	var selected []int64
	if err := json.Unmarshal(snapshot.Values["notify.system_channel_ids"], &selected); err != nil {
		return s.storageFailure(err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,uuid FROM channels WHERE enabled=1 ORDER BY id`)
	if err != nil {
		return s.storageFailure(err)
	}
	channels := []channelIdentity{}
	for rows.Next() {
		var channel channelIdentity
		if err := rows.Scan(&channel.ID, &channel.UUID); err != nil {
			rows.Close()
			return s.storageFailure(err)
		}
		if systemChannelSelected(selected, channel.ID) {
			channels = append(channels, channel)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return s.storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return s.storageFailure(err)
	}

	// 每次唤醒只执行一轮，下一轮交给现有调度器。
	var jobs sync.WaitGroup
	attempts := make([]systemAttempt, len(channels))
	failures := make([]error, len(channels))
	for index, channel := range channels {
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			failures[index] = s.sendSystemEvent(ctx, event, channel, &attempts[index])
		}()
	}
	jobs.Wait()
	var result error
	for _, err := range failures {
		result = errors.Join(result, err)
	}
	result = errors.Join(result, s.finishSystemRound(ctx, event.ID, retry, attempts))
	return errors.Join(result, ctx.Err())
}

func systemChannelSelected(selected []int64, id int64) bool {
	if len(selected) == 0 {
		return true
	}
	for _, value := range selected {
		if value == id {
			return true
		}
	}
	return false
}

func (s *Service) sendSystemEvent(ctx context.Context, event service.SystemEvent, identity channelIdentity, attempt *systemAttempt) error {
	outcome := &attempt.outcome
	outcome.ChannelID = identity.ID
	ctx, finish, err := s.beginChannel(ctx, identity)
	if err != nil {
		return err
	}
	defer finish()
	turn, err := s.queues.acquire(ctx, identity)
	if err != nil {
		return err
	}
	var kind notify.Type
	var endedAt time.Time
	defer func() { turn.finish(kind, outcome.CalledSender, endedAt) }()

	var record channelRecord
	var message notify.Message
	for {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return s.storageFailure(err)
		}
		current, err := s.loadSystemEventTx(ctx, tx, event.ID)
		if errors.Is(err, sql.ErrNoRows) || err == nil && current.ResolvedAt != nil {
			tx.Rollback()
			return nil
		}
		if err != nil {
			tx.Rollback()
			return s.storageFailure(err)
		}
		retry, err := loadSystemRetryTx(ctx, tx, event.ID)
		if err != nil {
			tx.Rollback()
			return s.storageFailure(err)
		}
		if retry.expired(s.now().UnixMilli()) {
			tx.Rollback()
			return nil
		}
		record, err = s.loadChannelTx(ctx, tx, identity.ID)
		if notificationNotFound(err) || err == nil && (!record.Enabled || record.UUID != identity.UUID) {
			tx.Rollback()
			return nil
		}
		if err != nil {
			tx.Rollback()
			return s.storageFailure(err)
		}
		snapshot, err := s.settings.SnapshotTx(ctx, tx)
		if err != nil {
			tx.Rollback()
			return s.storageFailure(err)
		}
		var selected []int64
		if err := json.Unmarshal(snapshot.Values["notify.system_channel_ids"], &selected); err != nil {
			tx.Rollback()
			return s.storageFailure(err)
		}
		if !systemChannelSelected(selected, identity.ID) {
			tx.Rollback()
			return nil
		}
		if kind != record.Type {
			kind = record.Type
			if err := tx.Commit(); err != nil {
				return s.storageFailure(err)
			}
			if err := turn.wait(ctx, kind); err != nil {
				return err
			}
			continue
		}
		if err := tx.Commit(); err != nil {
			return s.storageFailure(err)
		}
		options, _, err := s.loadFormatOptions(ctx)
		if err != nil {
			return s.storageFailure(err)
		}
		link, err := systemEventLink(options.PublicURL, event)
		if err != nil {
			return s.storageFailure(err)
		}
		var heldUntil *int64
		if event.Kind != nil && *event.Kind == service.EventAccountVerify && event.SubjectID != nil && s.verificationHeldUntil != nil {
			nowMS := s.now().UnixMilli()
			heldUntil = s.verificationHeldUntil(*event.SubjectID, nowMS)
			if heldUntil != nil && *heldUntil <= nowMS {
				heldUntil = nil
			}
		}
		message, err = formatSystem(event, options, link, heldUntil)
		if err != nil {
			return s.storageFailure(err)
		}
		break
	}
	outcome.ChannelName = record.Name
	config, err := s.channelConfig(record)
	if err != nil {
		var invalid *notify.ConfigError
		if !errors.As(err, &invalid) {
			return s.storageFailure(err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		outcome.Attempted = true
		outcome.EndedAt = s.now().UnixMilli()
		outcome.Result = notify.Result{ErrorCode: "invalid_config"}
		return nil
	}
	defer clear(config.Secrets)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkStorage(); err != nil {
		return err
	}
	outcome.Attempted, outcome.CalledSender = true, true
	outcome.StartedAt = s.now().UnixMilli()
	outcome.Result = s.sender.Send(ctx, config, message)
	endedAt = s.now()
	outcome.EndedAt = endedAt.UnixMilli()
	return s.saveReceipt(ctx, func(receiptCtx context.Context) error {
		tx, err := s.db.BeginTx(receiptCtx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var exists bool
		if attempt.deliveryID != 0 {
			if err := tx.QueryRowContext(receiptCtx, `SELECT EXISTS(SELECT 1 FROM deliveries WHERE id=? AND event_id=? AND channel_id=? AND at=? AND kind='system' AND ok=?)`, attempt.deliveryID, event.ID, outcome.ChannelID, outcome.EndedAt, outcome.Result.OK).Scan(&exists); err != nil {
				return err
			}
		}
		if !exists {
			attempt.deliveryID, err = s.recordSystemAttemptTx(receiptCtx, tx, event.ID, *outcome)
			if err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		_ = s.emit("system_event.changed", map[string]any{"event_id": event.ID})
		_ = s.emit("channel.changed", map[string]any{"channel_id": outcome.ChannelID})
		return nil
	})
}

func (s *Service) loadSystemEventTx(ctx context.Context, tx *sql.Tx, eventID int64) (service.SystemEvent, error) {
	var event service.SystemEvent
	var raw sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT id,kind,subject_id,message,created_at,resolved_at,notified_at FROM system_events WHERE id=?`, eventID).
		Scan(&event.ID, &event.Kind, &event.SubjectID, &raw, &event.CreatedAt, &event.ResolvedAt, &event.NotifiedAt); err != nil {
		return event, err
	}
	if raw.Valid {
		decoder := json.NewDecoder(bytes.NewBufferString(raw.String))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		if err := decoder.Decode(&event.Message); err != nil {
			return service.SystemEvent{}, errors.New("系统事件的原因记录损坏")
		}
		if decoder.Decode(new(any)) != io.EOF || event.Message == nil || event.Message.Code == "" {
			return service.SystemEvent{}, errors.New("系统事件的原因记录损坏")
		}
		if event.Message.Args == nil {
			event.Message.Args = map[string]any{}
		}
	}
	return event, nil
}

func (s *Service) recordSystemAttemptTx(ctx context.Context, tx *sql.Tx, eventID int64, outcome channelOutcome) (int64, error) {
	if !outcome.Attempted || !outcome.CalledSender {
		return 0, nil
	}
	if eventID < 1 || outcome.ChannelID < 1 {
		return 0, errors.New("系统通知的发送记录无效")
	}
	var failure any
	if !outcome.Result.OK {
		message := deliveryError(outcome.ChannelName, outcome.Result)
		raw, err := json.Marshal(message)
		if err != nil {
			return 0, err
		}
		failure = string(raw)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO deliveries(at,channel_id,task_id,kind,item_count,ok,error,event_id) VALUES(?,?,NULL,'system',0,?,?,?)`, outcome.EndedAt, outcome.ChannelID, outcome.Result.OK, failure, eventID)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if outcome.Result.OK {
		// 请求发出后事件可以解决；回执仍只能记回原来的事件编号。
		if _, err := tx.ExecContext(ctx, `UPDATE system_events SET notified_at=COALESCE(notified_at,?) WHERE id=?`, outcome.EndedAt, eventID); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func loadSystemRetryTx(ctx context.Context, tx *sql.Tx, eventID int64) (systemRetryState, error) {
	var state systemRetryState
	if err := tx.QueryRowContext(ctx, `SELECT failure_count,first_failed_at FROM system_events WHERE id=?`, eventID).Scan(&state.failureCount, &state.firstFailedAt); err != nil {
		return state, err
	}
	if state.failureCount < 0 || (state.failureCount == 0) != (state.firstFailedAt == nil) || state.firstFailedAt != nil && *state.firstFailedAt <= 0 {
		return systemRetryState{}, errors.New("系统事件的重试记录损坏")
	}
	return state, nil
}

func (state systemRetryState) expired(nowMS int64) bool {
	return state.failureCount >= 3 || state.firstFailedAt != nil && nowMS > *state.firstFailedAt && uint64(nowMS)-uint64(*state.firstFailedAt) > uint64((24*time.Hour).Milliseconds())
}

func (s *Service) finishSystemRound(ctx context.Context, eventID int64, initial systemRetryState, attempts []systemAttempt) error {
	if len(attempts) == 0 || ctx.Err() != nil {
		return nil
	}
	var failedAt int64
	for _, attempt := range attempts {
		if !attempt.outcome.Attempted || attempt.outcome.Result.OK || attempt.outcome.Result.ErrorCode == "cancelled" {
			return nil
		}
		failedAt = max(failedAt, attempt.outcome.EndedAt)
	}
	return s.saveReceipt(ctx, func(saveCtx context.Context) error {
		tx, err := s.db.BeginTx(saveCtx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, attempt := range attempts {
			if !attempt.outcome.CalledSender {
				continue
			}
			var saved bool
			if err := tx.QueryRowContext(saveCtx, `SELECT EXISTS(SELECT 1 FROM deliveries WHERE id=? AND event_id=? AND channel_id=? AND at=? AND kind='system' AND ok=0)`, attempt.deliveryID, eventID, attempt.outcome.ChannelID, attempt.outcome.EndedAt).Scan(&saved); err != nil {
				return err
			}
			if !saved {
				return errors.New("系统事件的发送记录尚未保存")
			}
		}
		// 同轮只累计一次；补记遇到已提交的计数时不再加一。
		if _, err := tx.ExecContext(saveCtx, `UPDATE system_events SET failure_count=failure_count+1,first_failed_at=COALESCE(first_failed_at,?) WHERE id=? AND failure_count=? AND notified_at IS NULL`, failedAt, eventID, initial.failureCount); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		_ = s.emit("system_event.changed", map[string]any{"event_id": eventID})
		return nil
	})
}
