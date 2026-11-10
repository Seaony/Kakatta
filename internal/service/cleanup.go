package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
)

var cleanupCountTables = []string{
	"findings", "task_sightings", "task_item_specs", "listings", "price_points",
	"seller_profiles", "market_insights", "run_records", "run_steps", "run_skip_events",
	"ai_calls", "deliveries", "delivery_items", "notification_intents", "delivery_intents",
	"quota_events", "system_events", "auth_tokens",
}

func (s *Service) Cleanup(ctx context.Context) (CleanupResult, error) {
	result := CleanupResult{Deleted: map[string]int64{}, Warnings: []locales.Message{}}
	if s.imageCache == nil {
		return result, storageFailure(errors.New("图片缓存服务缺失"))
	}
	active, release, err := s.cleanupProtection(ctx)
	if err != nil {
		return result, storageFailure(err)
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, storageFailure(err)
	}
	defer tx.Rollback()
	snapshot, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return result, err
	}
	dayStart, err := clock.DayStart()
	if err != nil {
		return result, storageFailure(err)
	}
	var imageMB int64
	if err := json.Unmarshal(snapshot.Values["cache.image_max_mb"], &imageMB); err != nil || imageMB < 1 {
		return result, storageFailure(errors.New("图片缓存容量设置无效"))
	}
	result, err = cleanupSQLTx(ctx, tx, snapshot.Values, now.UnixMilli(), active.ActiveTaskIDs, active.ActiveRunIDs, dayStart.UnixMilli())
	if err != nil {
		return result, storageFailure(err)
	}
	if err := appsettings.SaveClock(ctx, tx, clock); err != nil {
		return result, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return result, storageFailure(err)
	}
	release()
	result.ImagesDeleted, err = s.imageCache.Prune(ctx, imageMB*(1<<20))
	if err != nil {
		result.Warnings = append(result.Warnings, locales.Message{Code: "maintenance.image_cleanup_failed", Args: map[string]any{}})
	}
	s.recordMaintenanceWarnings(result.Warnings)
	_ = s.emit("finding.changed", map[string]any{"all": true})
	_ = s.emit("run.changed", map[string]any{"all": true})
	_ = s.emit("system_event.changed", map[string]any{"all": true})
	return result, nil
}

func cleanupSQLTx(ctx context.Context, tx *sql.Tx, values settings.Values, now int64, activeTasks, activeRuns []int64, aiDayStart int64) (CleanupResult, error) {
	result := CleanupResult{Deleted: map[string]int64{}, Warnings: []locales.Message{}}
	before, err := cleanupCountsTx(ctx, tx, cleanupCountTables)
	if err != nil {
		return result, err
	}
	cutoffs := map[string]int64{}
	var findingsDays int64
	for _, key := range []string{"retention.findings_days", "retention.stats_days", "retention.run_steps_days", "retention.seller_profile_days", "retention.orphan_listing_days"} {
		var days int64
		if err := json.Unmarshal(values[key], &days); err != nil || days < 0 {
			return result, errors.New("清理保留期无效")
		}
		cutoffs[key] = cleanupBefore(now, days*24*time.Hour.Milliseconds())
		if key == "retention.findings_days" {
			findingsDays = days
		}
	}
	taskClause, taskArgs := cleanupOutside("task_id", activeTasks)
	runClause, runArgs := cleanupOutside("id", activeRuns)
	stepClause, stepArgs := cleanupOutside("run_id", activeRuns)
	optionalTaskClause, optionalTaskArgs := cleanupOutside("COALESCE(task_id,0)", activeTasks)
	deleteRows := func(query string, args ...any) error {
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	}

	if findingsDays != 0 {
		args := append([]any{cutoffs["retention.findings_days"]}, taskArgs...)
		where := `found_at < ? AND NOT EXISTS(SELECT 1 FROM listings l WHERE l.item_id=findings.item_id AND l.is_favorite=1)` + taskClause
		rows, err := tx.QueryContext(ctx, `SELECT id FROM findings WHERE `+where, args...)
		if err != nil {
			return result, err
		}
		ids := []int64{}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return result, err
			}
			ids = append(ids, id)
		}
		readErr := rows.Err()
		closeErr := rows.Close()
		if readErr != nil {
			return result, readErr
		}
		if closeErr != nil {
			return result, closeErr
		}
		if err := cancelPendingIntentsForFindingsTx(ctx, tx, ids, locales.Message{Code: "maintenance.finding_expired", Args: map[string]any{}}, now); err != nil {
			return result, err
		}
		if err := deleteRows(`DELETE FROM findings WHERE `+where, args...); err != nil {
			return result, err
		}
		if err := raiseStatsCoverageTx(ctx, tx, "findings", cutoffs["retention.findings_days"]); err != nil {
			return result, err
		}
	}
	statsCutoff := cutoffs["retention.stats_days"]
	aiCutoff := min(statsCutoff, aiDayStart)
	queries := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM task_sightings WHERE last_seen_at < ?` + taskClause, append([]any{statsCutoff}, taskArgs...)},
		{`DELETE FROM task_item_specs WHERE NOT EXISTS(SELECT 1 FROM findings f WHERE f.task_id=task_item_specs.task_id AND f.item_id=task_item_specs.item_id) AND NOT EXISTS(SELECT 1 FROM task_sightings s WHERE s.task_id=task_item_specs.task_id AND s.item_id=task_item_specs.item_id)` + taskClause, taskArgs},
		{`DELETE FROM listings WHERE is_favorite=0 AND is_dismissed=0 AND last_seen_at < ? AND NOT EXISTS(SELECT 1 FROM findings f WHERE f.item_id=listings.item_id) AND NOT EXISTS(SELECT 1 FROM task_sightings s WHERE s.item_id=listings.item_id)`, []any{cutoffs["retention.orphan_listing_days"]}},
		{`DELETE FROM market_insights WHERE generated_at < ? AND EXISTS(SELECT 1 FROM market_insights newer WHERE newer.task_id=market_insights.task_id AND (newer.generated_at>market_insights.generated_at OR (newer.generated_at=market_insights.generated_at AND newer.id>market_insights.id)))` + taskClause, append([]any{statsCutoff}, taskArgs...)},
		{`DELETE FROM run_records WHERE ended_at IS NOT NULL AND ended_at < ?` + runClause, append([]any{statsCutoff}, runArgs...)},
		{`DELETE FROM run_steps WHERE at < ? AND EXISTS(SELECT 1 FROM run_records r WHERE r.id=run_steps.run_id AND r.ended_at IS NOT NULL)` + stepClause, append([]any{cutoffs["retention.run_steps_days"]}, stepArgs...)},
		{`DELETE FROM ai_calls WHERE at < ?` + optionalTaskClause, append([]any{aiCutoff}, optionalTaskArgs...)},
		{`DELETE FROM deliveries WHERE at < ?` + optionalTaskClause, append([]any{statsCutoff}, optionalTaskArgs...)},
		{`DELETE FROM notification_intents WHERE state <> 'pending' AND updated_at < ?` + taskClause, append([]any{statsCutoff}, taskArgs...)},
		{`DELETE FROM quota_events WHERE at < ?`, []any{cleanupBefore(now, 48*time.Hour.Milliseconds())}},
		{`DELETE FROM system_events WHERE resolved_at IS NOT NULL AND resolved_at < ?`, []any{statsCutoff}},
		{`DELETE FROM auth_tokens WHERE revoked_at < ? OR expires_at < ? OR (kind='web_session' AND created_at < ?)`, []any{cleanupBefore(now, 30*24*time.Hour.Milliseconds()), cleanupBefore(now, 30*24*time.Hour.Milliseconds()), cleanupBefore(now, 120*24*time.Hour.Milliseconds())}},
	}
	for _, q := range queries {
		if err := deleteRows(q.query, q.args...); err != nil {
			return result, err
		}
	}
	sellerQuery := `DELETE FROM seller_profiles WHERE fetched_at < ?`
	sellerArgs := []any{cutoffs["retention.seller_profile_days"]}
	if len(activeTasks) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(activeTasks)), ",")
		sellerQuery += ` AND NOT EXISTS(SELECT 1 FROM listings l JOIN findings f ON f.item_id=l.item_id WHERE l.seller_id=seller_profiles.seller_id AND f.task_id IN (` + placeholders + `))`
		sellerArgs = append(sellerArgs, taskArgs...)
	}
	if err := deleteRows(sellerQuery, sellerArgs...); err != nil {
		return result, err
	}
	for _, source := range []string{"run_records", "run_skip_events", "task_sightings", "deliveries", "notification_intents"} {
		if err := raiseStatsCoverageTx(ctx, tx, source, statsCutoff); err != nil {
			return result, err
		}
	}
	if err := raiseStatsCoverageTx(ctx, tx, "ai_calls", aiCutoff); err != nil {
		return result, err
	}
	result.Deleted, err = cleanupDeletedCountsTx(ctx, tx, before)
	return result, err
}

func cleanupBefore(now, age int64) int64 {
	return now - age
}

func cleanupOutside(column string, ids []int64) (string, []any) {
	if len(ids) == 0 {
		return "", nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return " AND " + column + " NOT IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")", args
}

func cleanupCountsTx(ctx context.Context, tx *sql.Tx, tables []string) (map[string]int64, error) {
	counts := make(map[string]int64, len(tables))
	for _, table := range tables {
		var count int64
		// 表名只来自本包固定清理清单，用户输入不会进入语句。
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			return nil, err
		}
		counts[table] = count
	}
	return counts, nil
}

func cleanupDeletedCountsTx(ctx context.Context, tx *sql.Tx, before map[string]int64) (map[string]int64, error) {
	deleted := make(map[string]int64, len(before))
	for table, old := range before {
		var count int64
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			return nil, err
		}
		deleted[table] = old - count
	}
	return deleted, nil
}

func raiseStatsCoverageTx(ctx context.Context, tx *sql.Tx, source string, cutoff int64) error {
	key := "stats.coverage." + source
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		previous, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || previous <= 0 || strconv.FormatInt(previous, 10) != raw {
			return fmt.Errorf("统计覆盖记录损坏：%s", source)
		}
		cutoff = max(previous, cutoff)
	}
	cutoff = max(int64(1), cutoff)
	_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, strconv.FormatInt(cutoff, 10))
	return err
}
