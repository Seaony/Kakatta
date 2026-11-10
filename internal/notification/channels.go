package notification

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Seaony/Kakatta/internal/notify"
	"github.com/Seaony/Kakatta/internal/service"
)

func (s *Service) ListChannels(ctx context.Context) ([]Channel, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, notificationStorageError(err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+channelColumns+` FROM channels ORDER BY id`)
	if err != nil {
		return nil, notificationStorageError(err)
	}
	defer rows.Close()
	result := []Channel{}
	for rows.Next() {
		record, err := scanChannel(rows)
		if err != nil {
			return nil, notificationStorageError(err)
		}
		view, err := s.channelView(record)
		if err != nil {
			return nil, notificationStorageError(err)
		}
		result = append(result, view)
	}
	if err := rows.Err(); err != nil {
		return nil, notificationStorageError(err)
	}
	return result, nil
}

func (s *Service) GetChannel(ctx context.Context, id int64) (Channel, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Channel{}, notificationStorageError(err)
	}
	defer tx.Rollback()
	record, err := s.loadChannelTx(ctx, tx, id)
	if err != nil {
		return Channel{}, notificationStorageError(err)
	}
	view, err := s.channelView(record)
	return view, notificationStorageError(err)
}

func (s *Service) CreateChannel(ctx context.Context, patch ChannelPatch) (Channel, error) {
	ctx, finish, err := s.beginJob(ctx)
	if err != nil {
		return Channel{}, err
	}
	defer finish()
	record, err := s.patchChannel(nil, patch)
	if err != nil {
		return Channel{}, notificationStorageError(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Channel{}, notificationStorageError(err)
	}
	defer tx.Rollback()
	if err := checkChannelNameTx(ctx, tx, record); err != nil {
		return Channel{}, notificationStorageError(err)
	}
	insert, err := tx.ExecContext(ctx, `INSERT INTO channels(uuid,type,name,enabled,config_json,secret_enc) VALUES(?,?,?,?,?,?)`,
		record.UUID, record.Type, record.Name, record.Enabled, string(record.Config), record.SecretEnc)
	if err != nil {
		return Channel{}, notificationStorageError(err)
	}
	record.ID, err = insert.LastInsertId()
	if err != nil {
		return Channel{}, notificationStorageError(err)
	}
	view, err := s.channelView(record)
	if err != nil {
		return Channel{}, notificationStorageError(err)
	}
	if err := tx.Commit(); err != nil {
		return Channel{}, notificationStorageError(err)
	}
	s.publishChannelChange(record.ID)
	return view, nil
}

func (s *Service) UpdateChannel(ctx context.Context, id int64, patch ChannelPatch) (Channel, error) {
	ctx, finish, err := s.beginJob(ctx)
	if err != nil {
		return Channel{}, err
	}
	defer finish()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Channel{}, notificationStorageError(err)
	}
	defer tx.Rollback()
	previous, err := s.loadChannelTx(ctx, tx, id)
	if err != nil {
		return Channel{}, notificationStorageError(err)
	}
	record, err := s.patchChannel(&previous, patch)
	if err != nil {
		return Channel{}, notificationStorageError(err)
	}
	if err := checkChannelNameTx(ctx, tx, record); err != nil {
		return Channel{}, notificationStorageError(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE channels SET type=?,name=?,enabled=?,config_json=?,secret_enc=? WHERE id=? AND uuid=?`,
		record.Type, record.Name, record.Enabled, string(record.Config), record.SecretEnc, id, record.UUID); err != nil {
		return Channel{}, notificationStorageError(err)
	}
	view, err := s.channelView(record)
	if err != nil {
		return Channel{}, notificationStorageError(err)
	}
	if err := tx.Commit(); err != nil {
		return Channel{}, notificationStorageError(err)
	}
	s.publishChannelChange(id)
	return view, nil
}

func (s *Service) publishChannelChange(id int64) {
	if s.publish != nil {
		raw, err := json.Marshal(map[string]int64{"channel_id": id})
		if err == nil {
			_ = s.publish("channel.changed", nil, raw)
		}
	}
}

func (s *Service) loadSavedChannel(ctx context.Context, id int64) (channelRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return channelRecord{}, err
	}
	defer tx.Rollback()
	record, err := s.loadChannelTx(ctx, tx, id)
	if err != nil {
		return channelRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return channelRecord{}, err
	}
	return record, nil
}

func (s *Service) TestChannel(ctx context.Context, id int64) (ChannelTestResult, error) {
	ctx, finish, err := s.beginJob(ctx)
	if err != nil {
		return ChannelTestResult{}, err
	}
	defer finish()
	record, err := s.loadSavedChannel(ctx, id)
	if err != nil {
		return ChannelTestResult{}, notificationStorageError(err)
	}
	identity := channelIdentity{ID: record.ID, UUID: record.UUID}
	ctx, channelDone, err := s.beginChannel(ctx, identity)
	if err != nil {
		return ChannelTestResult{}, err
	}
	defer channelDone()
	turn, err := s.queues.acquire(ctx, identity)
	if err != nil {
		return ChannelTestResult{}, err
	}
	calledSender := false
	var endedAt time.Time
	defer func() { turn.finish(record.Type, calledSender, endedAt) }()
	for {
		record, err = s.loadSavedChannel(ctx, id)
		if err != nil {
			return ChannelTestResult{}, notificationStorageError(err)
		}
		if record.UUID != identity.UUID {
			return ChannelTestResult{}, channelError(service.ErrorConflict, "channels.changed", "")
		}
		if !record.Enabled {
			return ChannelTestResult{}, channelError(service.ErrorConflict, "channels.disabled", "")
		}
		kind := record.Type
		if err := turn.wait(ctx, kind); err != nil {
			return ChannelTestResult{}, err
		}
		record, err = s.loadSavedChannel(ctx, id)
		if err != nil {
			return ChannelTestResult{}, notificationStorageError(err)
		}
		if record.UUID != identity.UUID {
			return ChannelTestResult{}, channelError(service.ErrorConflict, "channels.changed", "")
		}
		if !record.Enabled {
			return ChannelTestResult{}, channelError(service.ErrorConflict, "channels.disabled", "")
		}
		if record.Type == kind {
			break
		}
	}
	config, err := s.channelConfig(record)
	if err != nil {
		return ChannelTestResult{}, notificationStorageError(err)
	}
	defer clear(config.Secrets)
	options, _, err := s.loadFormatOptions(ctx)
	if err != nil {
		return ChannelTestResult{}, notificationStorageError(err)
	}
	message, err := testMessage(options.Language)
	if err != nil {
		return ChannelTestResult{}, notificationStorageError(err)
	}
	if err := s.checkStorage(); err != nil {
		return ChannelTestResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ChannelTestResult{}, err
	}
	calledSender = true
	result := s.sender.Send(ctx, config, message)
	endedAt = s.now()
	var view ChannelTestResult
	var deliveryID int64
	if err := s.saveReceipt(ctx, func(saveCtx context.Context) error {
		var saveErr error
		view, saveErr = s.recordChannelTest(saveCtx, record, endedAt, result, &deliveryID)
		return saveErr
	}); err != nil {
		return ChannelTestResult{}, err
	}
	s.publishChannelChange(id)
	return view, nil
}

func (s *Service) DeleteChannel(ctx context.Context, id int64) (ChannelDeleteResult, error) {
	ctx, finish, err := s.beginJob(ctx)
	if err != nil {
		return ChannelDeleteResult{}, err
	}
	defer finish()
	record, err := s.loadSavedChannel(ctx, id)
	if err != nil {
		return ChannelDeleteResult{}, notificationStorageError(err)
	}
	identity := channelIdentity{ID: id, UUID: record.UUID}
	deleted := false
	defer func() {
		if deleted {
			s.forgetChannel(identity)
		} else {
			s.resumeChannel(identity)
		}
	}()
	if err := s.stopChannel(ctx, identity); err != nil {
		return ChannelDeleteResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChannelDeleteResult{}, notificationStorageError(err)
	}
	defer tx.Rollback()
	current, err := s.loadChannelTx(ctx, tx, id)
	if err != nil {
		return ChannelDeleteResult{}, notificationStorageError(err)
	}
	if current.UUID != identity.UUID {
		return ChannelDeleteResult{}, channelError(service.ErrorConflict, "channels.changed", "")
	}
	snapshot, err := s.settings.SnapshotTx(ctx, tx)
	if err != nil {
		return ChannelDeleteResult{}, notificationStorageError(err)
	}
	var targetIDs []int64
	if err := json.Unmarshal(snapshot.Values["notify.system_channel_ids"], &targetIDs); err != nil {
		return ChannelDeleteResult{}, notificationStorageError(err)
	}
	remaining := make([]int64, 0, len(targetIDs))
	for _, target := range targetIDs {
		if target != id {
			remaining = append(remaining, target)
		}
	}
	if len(targetIDs) != 0 && len(remaining) == 0 {
		return ChannelDeleteResult{}, channelError(service.ErrorConflict, "channels.system_target_required", "")
	}
	result := ChannelDeleteResult{ID: id, AffectedTaskIDs: []int64{}}
	rows, err := tx.QueryContext(ctx, `SELECT task_id FROM task_channels WHERE channel_id=? ORDER BY task_id`, id)
	if err != nil {
		return ChannelDeleteResult{}, notificationStorageError(err)
	}
	for rows.Next() {
		var taskID int64
		if err := rows.Scan(&taskID); err != nil {
			rows.Close()
			return ChannelDeleteResult{}, notificationStorageError(err)
		}
		result.AffectedTaskIDs = append(result.AffectedTaskIDs, taskID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ChannelDeleteResult{}, notificationStorageError(err)
	}
	if err := rows.Close(); err != nil {
		return ChannelDeleteResult{}, notificationStorageError(err)
	}
	if len(remaining) != len(targetIDs) {
		raw, err := json.Marshal(remaining)
		if err != nil {
			return ChannelDeleteResult{}, notificationStorageError(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value_json,updated_at) VALUES('notify.system_channel_ids',?,?)
			ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json,updated_at=excluded.updated_at`, string(raw), s.now().UnixMilli()); err != nil {
			return ChannelDeleteResult{}, notificationStorageError(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_channels WHERE channel_id=?`, id); err != nil {
		return ChannelDeleteResult{}, notificationStorageError(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM channels WHERE id=? AND uuid=?`, id, identity.UUID); err != nil {
		return ChannelDeleteResult{}, notificationStorageError(err)
	}
	if err := tx.Commit(); err != nil {
		return ChannelDeleteResult{}, notificationStorageError(err)
	}
	deleted = true
	s.publishChannelChange(id)
	return result, nil
}

func (s *Service) ChannelDeliveries(ctx context.Context, id int64) ([]service.DeliverySummary, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, notificationStorageError(err)
	}
	defer tx.Rollback()
	if _, err := s.loadChannelTx(ctx, tx, id); err != nil {
		return nil, notificationStorageError(err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT d.id,d.at,d.channel_id,c.name,d.task_id,d.kind,d.item_count,d.ok,d.error
		FROM deliveries d LEFT JOIN channels c ON c.id=d.channel_id
		WHERE d.channel_id=? ORDER BY (d.at IS NULL),d.at DESC,d.id DESC LIMIT 20`, id)
	if err != nil {
		return nil, notificationStorageError(err)
	}
	defer rows.Close()
	result := []service.DeliverySummary{}
	for rows.Next() {
		var item service.DeliverySummary
		var failure sql.NullString
		if err := rows.Scan(&item.ID, &item.At, &item.ChannelID, &item.ChannelName, &item.TaskID, &item.Kind, &item.ItemCount, &item.OK, &failure); err != nil {
			return nil, notificationStorageError(err)
		}
		if failure.Valid {
			item.Error, err = decodeNotificationMessage([]byte(failure.String))
			if err != nil {
				return nil, notificationStorageError(err)
			}
		}
		result = append(result, item)
	}
	return result, notificationStorageError(rows.Err())
}

func (s *Service) DeliveryItems(ctx context.Context, deliveryID int64) ([]DeliveryItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, notificationStorageError(err)
	}
	defer tx.Rollback()
	var kind sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM deliveries WHERE id=?`, deliveryID).Scan(&kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, channelError(service.ErrorNotFound, "deliveries.not_found", "")
		}
		return nil, notificationStorageError(err)
	}
	result := []DeliveryItem{}
	if kind.String == "system" || kind.String == "test" {
		return result, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT di.finding_id,COALESCE(d.task_id,f.task_id),l.title
		FROM delivery_items di JOIN deliveries d ON d.id=di.delivery_id
		LEFT JOIN findings f ON f.id=di.finding_id LEFT JOIN listings l ON l.item_id=f.item_id
		WHERE di.delivery_id=? ORDER BY di.finding_id`, deliveryID)
	if err != nil {
		return nil, notificationStorageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var item DeliveryItem
		if err := rows.Scan(&item.FindingID, &item.TaskID, &item.Title); err != nil {
			return nil, notificationStorageError(err)
		}
		result = append(result, item)
	}
	return result, notificationStorageError(rows.Err())
}

func sameChannelConfiguration(left, right channelRecord) bool {
	return left.ID == right.ID && left.UUID == right.UUID && left.Type == right.Type && left.Name == right.Name &&
		left.Enabled == right.Enabled && bytes.Equal(left.Config, right.Config) && bytes.Equal(left.SecretEnc, right.SecretEnc)
}

func (s *Service) recordChannelTest(ctx context.Context, record channelRecord, at time.Time, result notify.Result, deliveryID *int64) (ChannelTestResult, error) {
	view := ChannelTestResult{ChannelID: record.ID, At: at.UnixMilli(), OK: result.OK, Error: deliveryError(record.Name, result)}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChannelTestResult{}, err
	}
	defer tx.Rollback()
	var failure any
	if view.Error != nil {
		raw, err := json.Marshal(view.Error)
		if err != nil {
			return ChannelTestResult{}, err
		}
		failure = string(raw)
	}
	if *deliveryID != 0 {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM deliveries
			WHERE id=? AND channel_id=? AND at=? AND kind='test' AND task_id IS NULL AND item_count=0 AND ok=? AND error IS ?)`,
			*deliveryID, record.ID, view.At, result.OK, failure).Scan(&exists); err != nil {
			return ChannelTestResult{}, err
		}
		if exists {
			view.DeliveryID = *deliveryID
			return view, nil
		}
	}
	insert, err := tx.ExecContext(ctx, `INSERT INTO deliveries(at,channel_id,kind,item_count,ok,error) VALUES(?,?,'test',0,?,?)`, view.At, record.ID, result.OK, failure)
	if err != nil {
		return ChannelTestResult{}, err
	}
	view.DeliveryID, err = insert.LastInsertId()
	if err != nil {
		return ChannelTestResult{}, err
	}
	*deliveryID = view.DeliveryID
	current, err := s.loadChannelTx(ctx, tx, record.ID)
	if err != nil {
		return ChannelTestResult{}, err
	}
	if sameChannelConfiguration(record, current) {
		if _, err := tx.ExecContext(ctx, `UPDATE channels SET last_test_at=?,last_test_ok=?,last_error=? WHERE id=? AND uuid=?`, view.At, result.OK, failure, record.ID, record.UUID); err != nil {
			return ChannelTestResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ChannelTestResult{}, err
	}
	return view, nil
}
