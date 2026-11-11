package notification

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/service"
)

func (s *Service) FlushRun(ctx context.Context, ref service.RunRef, firstConclusionIDs []int64) (service.NotifyResult, error) {
	ctx, done, err := s.beginJob(ctx)
	if err != nil {
		return service.NotifyResult{}, err
	}
	defer done()
	options, maximum, err := s.loadFormatOptions(ctx)
	if err != nil {
		return service.NotifyResult{}, s.storageFailure(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return service.NotifyResult{}, s.storageFailure(err)
	}
	defer tx.Rollback()
	values, err := s.planRunTx(ctx, tx, ref, firstConclusionIDs, s.now().UnixMilli())
	if err != nil {
		return service.NotifyResult{}, s.storageFailure(err)
	}
	task, err := service.NotificationTaskTx(ctx, tx, ref.TaskID)
	if notificationNotFound(err) {
		return service.NotifyResult{}, nil
	}
	if err != nil {
		return service.NotifyResult{}, s.storageFailure(err)
	}
	channels, err := taskDeliveryChannelsTx(ctx, tx, ref.TaskID)
	if err != nil {
		return service.NotifyResult{}, s.storageFailure(err)
	}
	s.mu.Lock()
	if s.intentInFlight == nil {
		s.intentInFlight = make(map[int64]struct{})
	}
	claimed := make([]candidate, 0, len(values))
	for _, value := range values {
		if _, busy := s.intentInFlight[value.Intent.ID]; !busy {
			s.intentInFlight[value.Intent.ID] = struct{}{}
			claimed = append(claimed, value)
		}
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		for _, value := range claimed {
			delete(s.intentInFlight, value.Intent.ID)
		}
		s.mu.Unlock()
	}()
	if err := tx.Commit(); err != nil {
		return service.NotifyResult{}, s.storageFailure(err)
	}
	groups, err := groupMessages(task, claimed, maximum)
	if err != nil {
		return service.NotifyResult{}, err
	}
	options.TaskResultsURL, err = notificationTaskResultsURL(options.PublicURL, ref.TaskID)
	if err != nil {
		return service.NotifyResult{}, notificationStorageError(err)
	}
	findingLinks := make(map[int64]string, len(claimed))
	findingByIntent := make(map[int64]int64, len(claimed))
	for _, value := range claimed {
		link, err := notificationFindingURL(options.PublicURL, ref.TaskID, value.Finding.ID)
		if err != nil {
			return service.NotifyResult{}, notificationStorageError(err)
		}
		findingLinks[value.Finding.ID] = link
		findingByIntent[value.Intent.ID] = value.Finding.ID
	}
	options.FindingURL = func(id int64) string { return findingLinks[id] }
	result := service.NotifyResult{Warnings: []locales.Message{}}
	successes := map[int64]struct{}{}
	var flushErr error
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			flushErr = errors.Join(flushErr, err)
			break
		}
		outcomes := make([]channelOutcome, len(channels))
		failures := make([]error, len(channels))
		var jobs sync.WaitGroup
		for index, channel := range channels {
			jobs.Add(1)
			go func() {
				defer jobs.Done()
				outcomes[index], failures[index] = s.sendTaskGroup(ctx, ref, task, group, channel, options)
			}()
		}
		jobs.Wait()
		var roundErr error
		for index, outcome := range outcomes {
			roundErr = errors.Join(roundErr, failures[index])
			if outcome.Result.OK {
				for _, id := range outcome.IntentIDs {
					successes[findingByIntent[id]] = struct{}{}
				}
			} else if outcome.Attempted {
				result.Warnings = append(result.Warnings, *deliveryError(outcome.ChannelName, outcome.Result))
			}
		}
		flushErr = errors.Join(flushErr, roundErr)
		if roundErr != nil {
			break
		}
		settleCtx, cancel := settlementContext(ctx)
		err := s.finishTaskRound(settleCtx, group, outcomes)
		cancel()
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				err = errors.New("通知轮次结算保存超时")
			}
			flushErr = errors.Join(flushErr, s.storageFailure(err))
			break
		}
	}
	result.NotifiedCount = int64(len(successes))
	return result, errors.Join(flushErr, ctx.Err())
}

func taskDeliveryChannelsTx(ctx context.Context, tx *sql.Tx, taskID int64) ([]channelIdentity, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.uuid FROM channels c JOIN task_channels tc ON tc.channel_id=c.id WHERE tc.task_id=? AND c.enabled=1 ORDER BY c.id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	channels := []channelIdentity{}
	for rows.Next() {
		var value channelIdentity
		if err := rows.Scan(&value.ID, &value.UUID); err != nil {
			return nil, err
		}
		channels = append(channels, value)
	}
	return channels, rows.Err()
}

func (s *Service) sendTaskGroup(ctx context.Context, ref service.RunRef, task service.Task, group messageGroup, channel channelIdentity, options formatOptions) (channelOutcome, error) {
	outcome := channelOutcome{ChannelID: channel.ID}
	ctx, done, err := s.beginChannel(ctx, channel)
	if err != nil {
		return outcome, err
	}
	defer done()
	turn, err := s.queues.acquire(ctx, channel)
	if err != nil {
		return outcome, err
	}
	var kind notify.Type
	endedAt := s.now()
	defer func() { turn.finish(kind, outcome.CalledSender, endedAt) }()
	// 拿到许可后和等完间隔后各重读一次，队列里不保存解密后的配置。
	record, _, _, err := s.taskSendSnapshot(ctx, ref, group, channel)
	if err != nil || !record.Enabled {
		return outcome, err
	}
	for {
		kind = record.Type
		if err := turn.wait(ctx, kind); err != nil {
			return outcome, err
		}
		record, task, group, err = s.taskSendSnapshot(ctx, ref, group, channel)
		if err != nil || !record.Enabled || len(group.Members) == 0 {
			return outcome, err
		}
		if record.Type == kind {
			break
		}
	}
	outcome.ChannelName = record.Name
	for _, member := range group.Members {
		outcome.IntentIDs = append(outcome.IntentIDs, member.Intent.ID)
	}
	config, err := s.channelConfig(record)
	if err != nil {
		var invalid *notify.ConfigError
		if !errors.As(err, &invalid) {
			return outcome, s.storageFailure(err)
		}
		outcome.Attempted = true
		outcome.Result = notify.Result{ErrorCode: "invalid_config"}
		return outcome, nil
	}
	defer clear(config.Secrets)
	message, err := formatGroup(task, group, config, options)
	kindName, kindErr := taskDeliveryKind(group)
	if err != nil || kindErr != nil {
		outcome.Attempted = true
		outcome.Result = notify.Result{ErrorCode: "invalid_message"}
		return outcome, nil
	}
	outcome.FormatLevel = message.FormatLevel
	if err := s.checkStorage(); err != nil {
		return outcome, err
	}
	if err := ctx.Err(); err != nil {
		return outcome, err
	}

	outcome.StartedAt = s.now().UnixMilli()
	outcome.CalledSender = true
	outcome.Attempted = true
	outcome.Result = s.sender.Send(ctx, config, message)
	endedAt = s.now()
	outcome.EndedAt = endedAt.UnixMilli()
	// 先记下真实结果，再响应取消；成功的渠道不会在下一轮补发。
	receipt := &taskDeliveryReceipt{ref: ref, group: group, kind: kindName, outcome: outcome}
	if err := s.saveReceipt(ctx, func(saveCtx context.Context) error { return s.saveTaskReceipt(saveCtx, receipt) }); err != nil {
		return outcome, err
	}
	if !outcome.Result.OK && ctx.Err() != nil {
		return outcome, ctx.Err()
	}
	return outcome, nil
}

func (s *Service) taskSendSnapshot(ctx context.Context, ref service.RunRef, group messageGroup, identity channelIdentity) (channelRecord, service.Task, messageGroup, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return channelRecord{}, service.Task{}, messageGroup{}, s.storageFailure(err)
	}
	defer tx.Rollback()
	record, err := s.loadChannelTx(ctx, tx, identity.ID)
	if notificationNotFound(err) {
		return channelRecord{}, service.Task{}, messageGroup{}, nil
	}
	if err != nil {
		return channelRecord{}, service.Task{}, messageGroup{}, s.storageFailure(err)
	}
	var bound bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_channels WHERE task_id=? AND channel_id=?)`, ref.TaskID, identity.ID).Scan(&bound); err != nil {
		return channelRecord{}, service.Task{}, messageGroup{}, s.storageFailure(err)
	}
	if record.UUID != identity.UUID || !record.Enabled || !bound {
		record.Enabled = false
		return record, service.Task{}, messageGroup{}, nil
	}
	task, current, err := s.refreshTaskGroupTx(ctx, tx, ref, group, s.now().UnixMilli())
	if notificationNotFound(err) {
		record.Enabled = false
		return record, service.Task{}, messageGroup{}, nil
	}
	if err != nil {
		return channelRecord{}, service.Task{}, messageGroup{}, s.storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return channelRecord{}, service.Task{}, messageGroup{}, s.storageFailure(err)
	}
	return record, task, current, nil
}

type taskDeliveryReceipt struct {
	ref        service.RunRef
	group      messageGroup
	kind       string
	outcome    channelOutcome
	deliveryID int64
}

func (s *Service) saveTaskReceipt(ctx context.Context, receipt *taskDeliveryReceipt) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists bool
	if receipt.deliveryID != 0 {
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM deliveries WHERE id=? AND channel_id=? AND task_id=? AND at=? AND kind=? AND ok=?)`, receipt.deliveryID, receipt.outcome.ChannelID, receipt.ref.TaskID, receipt.outcome.EndedAt, receipt.kind, receipt.outcome.Result.OK).Scan(&exists); err != nil {
			return err
		}
	}
	if !exists {
		receipt.deliveryID, err = s.recordTaskAttemptTx(ctx, tx, receipt.ref, receipt.group, receipt.kind, receipt.outcome)
		if err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	ids := make([]int64, 0, len(receipt.group.Members))
	for _, member := range receipt.group.Members {
		ids = append(ids, member.Finding.ID)
	}
	_ = s.emit("finding.changed", map[string]any{"task_id": receipt.ref.TaskID, "ids": ids})
	_ = s.emit("channel.changed", map[string]any{"channel_id": receipt.outcome.ChannelID})
	return nil
}

func (s *Service) finishTaskRound(ctx context.Context, group messageGroup, outcomes []channelOutcome) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.finishRoundTx(ctx, tx, group, outcomes, s.now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

func notificationNotFound(err error) bool {
	var failure *service.ServiceError
	return errors.Is(err, sql.ErrNoRows) || errors.As(err, &failure) && failure.Kind == service.ErrorNotFound
}
