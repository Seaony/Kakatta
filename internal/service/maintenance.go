package service

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
)

func (s *Service) ClearBusinessData(ctx context.Context, confirmation string) (CleanupResult, error) {
	result := CleanupResult{Deleted: map[string]int64{}, Warnings: []locales.Message{}}
	if confirmation != "清除" {
		reason := locales.Message{Code: "maintenance.confirmation_required", Args: map[string]any{}}
		return result, &ServiceError{Kind: ErrorInvalid, Reason: reason, Fields: []settings.Issue{{Key: "confirmation", Reason: reason}}}
	}
	if s.imageCache == nil {
		return result, storageFailure(errors.New("图片缓存服务缺失"))
	}
	resume, err := s.quiesce(ctx)
	if err != nil {
		return result, storageFailure(err)
	}
	defer resume()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, storageFailure(err)
	}
	defer tx.Rollback()
	_, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return result, err
	}
	result, err = clearBusinessSQLTx(ctx, tx, now.UnixMilli())
	if err != nil {
		return result, storageFailure(err)
	}
	if err := appsettings.SaveClock(ctx, tx, clock); err != nil {
		return result, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return result, storageFailure(err)
	}
	resume()
	result.ImagesDeleted, err = s.imageCache.Clear(ctx)
	if err != nil {
		result.Warnings = append(result.Warnings, locales.Message{Code: "maintenance.image_cleanup_failed", Args: map[string]any{}})
	}
	s.recordMaintenanceWarnings(result.Warnings)
	_ = s.emit("task.changed", map[string]any{"all": true})
	_ = s.emit("finding.changed", map[string]any{"all": true})
	_ = s.emit("run.changed", map[string]any{"all": true})
	_ = s.emit("system_event.changed", map[string]any{"all": true})
	return result, nil
}

func (s *Service) ClearImageCache(ctx context.Context) (CleanupResult, error) {
	result := CleanupResult{Deleted: map[string]int64{}, Warnings: []locales.Message{}}
	if s.imageCache == nil {
		return result, storageFailure(errors.New("图片缓存服务缺失"))
	}
	finish, err := s.beginMutation()
	if err != nil {
		return result, err
	}
	defer finish()
	result.ImagesDeleted, err = s.imageCache.Clear(ctx)
	if err != nil {
		s.recordMaintenanceWarnings([]locales.Message{{Code: "maintenance.image_cleanup_failed", Args: map[string]any{}}})
		return result, storageFailure(err)
	}
	s.recordMaintenanceWarnings(nil)
	return result, nil
}

func clearBusinessSQLTx(ctx context.Context, tx *sql.Tx, now int64) (CleanupResult, error) {
	result := CleanupResult{Deleted: map[string]int64{}, Warnings: []locales.Message{}}
	tables := append(append([]string(nil), cleanupCountTables...), "tasks", "task_channels", "seen_items", "blocked_sellers")
	before, err := cleanupCountsTx(ctx, tx, tables)
	if err != nil {
		return result, err
	}
	for _, query := range []string{
		`DELETE FROM notification_intents`,
		`DELETE FROM deliveries`,
		`DELETE FROM tasks`,
		`DELETE FROM listings`,
		`DELETE FROM seller_profiles`,
		`DELETE FROM blocked_sellers`,
		`DELETE FROM run_records`,
		`DELETE FROM system_events WHERE kind IN ('task_failure_pause','xianyu_layout')`,
	} {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return result, err
		}
	}
	metaDeleted, err := clearBusinessMetaTx(ctx, tx)
	if err != nil {
		return result, err
	}
	for _, source := range []string{"findings", "run_records", "run_skip_events", "task_sightings", "deliveries", "notification_intents"} {
		if err := raiseStatsCoverageTx(ctx, tx, source, now); err != nil {
			return result, err
		}
	}
	result.Deleted, err = cleanupDeletedCountsTx(ctx, tx, before)
	if err != nil {
		return result, err
	}
	result.Deleted["meta"] = metaDeleted
	return result, nil
}

func clearBusinessMetaTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key FROM meta WHERE key GLOB 'task.latest_search.*' OR key GLOB 'task.profile_next_no.*' OR key GLOB 'migration.legacy_notify_failures.*'`)
	if err != nil {
		return 0, err
	}
	keys := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return 0, err
		}
		idText := key
		for _, prefix := range []string{"task.latest_search.", "task.profile_next_no.", "migration.legacy_notify_failures."} {
			if strings.HasPrefix(key, prefix) {
				idText = strings.TrimPrefix(key, prefix)
				break
			}
		}
		id, err := strconv.ParseInt(idText, 10, 64)
		if err == nil && id > 0 && strconv.FormatInt(id, 10) == idText {
			keys = append(keys, key)
		}
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if readErr != nil {
		return 0, readErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	var deleted int64
	for _, key := range keys {
		result, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key=?`, key)
		if err != nil {
			return 0, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		deleted += count
	}
	return deleted, nil
}
