package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/regions"
	"github.com/Seaony/Kakatta/internal/settings"
)

const taskColumns = `id,name,keyword,enabled,account_id,account_pinned,min_price,max_price,
	personal_only,free_shipping_only,publish_window,region_province,region_city,region_district,
	max_pages,interval_minutes,crawl_overrides_json,judge_mode,requirement,profile_json,
	budget_min,budget_max,match_keywords_json,exclude_keywords_json,analyze_on_first_run,
	notify_policy,notify_price_drop,search_generation,baseline_pending,sort_index,created_at,
	updated_at,last_run_at,last_success_at,next_run_at,last_run_outcome,last_error,
	consecutive_failures,failure_paused_until,no_good_hint_dismissed_at`

type taskScanner interface{ Scan(...any) error }

const taskProfileWatermarkPrefix = "task.profile_next_no."

func loadTaskProfileWatermarkTx(ctx context.Context, tx *sql.Tx, task Task) (int, error) {
	watermark := max(1, task.Profile.NextNo)
	if task.Profile.NextNo < 0 {
		return 0, errors.New("任务清单编号水位损坏")
	}
	for _, items := range [][]analysis.Requirement{task.Profile.Musts, task.Profile.Bonuses} {
		for _, item := range items {
			prefix := byte('m')
			if strings.HasPrefix(item.ID, "b") {
				prefix = 'b'
			}
			if n, valid := taskProfileID(item.ID, prefix); valid {
				if n == math.MaxInt {
					return 0, errors.New("任务清单编号已耗尽")
				}
				watermark = max(watermark, n+1)
			}
		}
	}
	for _, item := range task.Profile.Risks {
		if n, valid := taskProfileID(item.ID, 'c'); valid {
			if n == math.MaxInt {
				return 0, errors.New("任务清单编号已耗尽")
			}
			watermark = max(watermark, n+1)
		}
	}
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, taskProfileWatermarkPrefix+strconv.FormatInt(task.ID, 10)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return watermark, nil
	}
	if err != nil {
		return 0, err
	}
	stored, err := strconv.Atoi(raw)
	if err != nil || stored < 1 || strconv.Itoa(stored) != raw {
		return 0, errors.New("任务清单编号水位损坏")
	}
	return max(watermark, stored), nil
}

func writeTaskProfileWatermarkTx(ctx context.Context, tx *sql.Tx, taskID int64, watermark int) error {
	if watermark < 1 {
		return errors.New("任务清单编号水位必须是正整数")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		taskProfileWatermarkPrefix+strconv.FormatInt(taskID, 10), strconv.Itoa(watermark))
	return err
}

func scanTask(row taskScanner) (Task, error) {
	var task Task
	var overrides, profile, matches, excludes string
	var lastError sql.NullString
	err := row.Scan(&task.ID, &task.Name, &task.Keyword, &task.Enabled, &task.AccountID,
		&task.AccountPinned, &task.MinPrice, &task.MaxPrice, &task.PersonalOnly,
		&task.FreeShippingOnly, &task.PublishWindow, &task.RegionProvince, &task.RegionCity,
		&task.RegionDistrict, &task.MaxPages, &task.IntervalMinutes, &overrides, &task.JudgeMode,
		&task.Requirement, &profile, &task.BudgetMin, &task.BudgetMax, &matches, &excludes,
		&task.AnalyzeOnFirstRun, &task.NotifyPolicy, &task.NotifyPriceDrop, &task.SearchGeneration,
		&task.BaselinePending, &task.SortIndex, &task.CreatedAt, &task.UpdatedAt, &task.LastRunAt,
		&task.LastSuccessAt, &task.NextRunAt, &task.LastRunOutcome, &lastError,
		&task.ConsecutiveFailures, &task.FailurePausedUntil, &task.NoGoodHintDismissedAt)
	if err != nil {
		return Task{}, err
	}
	for _, value := range []struct {
		raw    string
		prefix byte
		target any
	}{{overrides, '{', &task.CrawlOverrides}, {profile, '{', &task.Profile}, {matches, '[', &task.MatchKeywords}, {excludes, '[', &task.ExcludeKeywords}} {
		trimmed := bytes.TrimSpace([]byte(value.raw))
		if len(trimmed) == 0 || trimmed[0] != value.prefix || json.Unmarshal(trimmed, value.target) != nil {
			return Task{}, errors.New("任务保存的配置损坏")
		}
	}
	if lastError.Valid {
		var message locales.Message
		if json.Unmarshal([]byte(lastError.String), &message) != nil || message.Code == "" {
			return Task{}, errors.New("任务保存的错误原因损坏")
		}
		task.LastError = &message
	}
	task.ChannelIDs = []int64{}
	return task, nil
}

func taskNotFound() error {
	return &ServiceError{Kind: ErrorNotFound, Reason: locales.Message{Code: "tasks.not_found"}, Fields: []settings.Issue{}}
}

func loadTaskTx(ctx context.Context, tx *sql.Tx, id int64) (Task, error) {
	task, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, taskNotFound()
	}
	if err != nil {
		return Task{}, err
	}
	if err := loadTaskChannelsTx(ctx, tx, &task); err != nil {
		return Task{}, err
	}
	return task, nil
}

func loadTaskChannelsTx(ctx context.Context, tx *sql.Tx, task *Task) error {
	rows, err := tx.QueryContext(ctx, `SELECT channel_id FROM task_channels WHERE task_id=? ORDER BY channel_id`, task.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	task.ChannelIDs = []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		task.ChannelIDs = append(task.ChannelIDs, id)
	}
	return rows.Err()
}

func taskValues(task Task) ([]any, error) {
	raw := make([]string, 4)
	for i, value := range []any{task.CrawlOverrides, task.Profile, task.MatchKeywords, task.ExcludeKeywords} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		raw[i] = string(encoded)
	}
	var lastError any
	if task.LastError != nil {
		encoded, err := json.Marshal(task.LastError)
		if err != nil {
			return nil, err
		}
		lastError = string(encoded)
	}
	return []any{task.Name, task.Keyword, task.Enabled, task.AccountID, task.AccountPinned,
		task.MinPrice, task.MaxPrice, task.PersonalOnly, task.FreeShippingOnly, task.PublishWindow,
		task.RegionProvince, task.RegionCity, task.RegionDistrict, task.MaxPages, task.IntervalMinutes,
		raw[0], task.JudgeMode, task.Requirement, raw[1], task.BudgetMin, task.BudgetMax, raw[2], raw[3],
		task.AnalyzeOnFirstRun, task.NotifyPolicy, task.NotifyPriceDrop, task.SearchGeneration,
		task.BaselinePending, task.SortIndex, task.CreatedAt, task.UpdatedAt, task.LastRunAt,
		task.LastSuccessAt, task.NextRunAt, task.LastRunOutcome, lastError, task.ConsecutiveFailures,
		task.FailurePausedUntil, task.NoGoodHintDismissedAt}, nil
}

func insertTaskTx(ctx context.Context, tx *sql.Tx, task *Task) error {
	values, err := taskValues(*task)
	if err != nil {
		return err
	}
	_, columns, _ := strings.Cut(taskColumns, ",")
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
	result, err := tx.ExecContext(ctx, `INSERT INTO tasks(`+columns+`) VALUES(`+placeholders+`)`, values...)
	if err != nil {
		return err
	}
	task.ID, err = result.LastInsertId()
	if err != nil {
		return err
	}
	if err := saveTaskChannelsTx(ctx, tx, task.ID, task.ChannelIDs); err != nil {
		return err
	}
	return writeTaskProfileWatermarkTx(ctx, tx, task.ID, max(1, task.Profile.NextNo))
}

func saveTaskTx(ctx context.Context, tx *sql.Tx, task Task) error {
	watermark, err := loadTaskProfileWatermarkTx(ctx, tx, task)
	if err != nil {
		return err
	}
	values, err := taskValues(task)
	if err != nil {
		return err
	}
	_, columns, _ := strings.Cut(taskColumns, ",")
	assignments := strings.Split(columns, ",")
	for i, column := range assignments {
		assignments[i] = strings.TrimSpace(column) + "=?"
	}
	values = append(values, task.ID)
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET `+strings.Join(assignments, ",")+` WHERE id=?`, values...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return taskNotFound()
	}
	if err := saveTaskChannelsTx(ctx, tx, task.ID, task.ChannelIDs); err != nil {
		return err
	}
	return writeTaskProfileWatermarkTx(ctx, tx, task.ID, watermark)
}

func saveTaskChannelsTx(ctx context.Context, tx *sql.Tx, taskID int64, ids []int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_channels WHERE task_id=?`, taskID); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_channels(task_id,channel_id) VALUES(?,?)`, taskID, id); err != nil {
			return err
		}
	}
	return nil
}

func taskDefaultsTx(ctx context.Context, tx *sql.Tx, global settings.Snapshot) (TaskFields, error) {
	fields := TaskFields{Enabled: true, PersonalOnly: true, PublishWindow: "any", JudgeMode: analysis.ModeKeyword,
		CrawlOverrides: settings.Values{}, Profile: analysis.Profile{}, MatchKeywords: []string{}, ExcludeKeywords: []string{},
		AnalyzeOnFirstRun: true, NotifyPolicy: "recommended", NotifyPriceDrop: true, ChannelIDs: []int64{}}
	if err := json.Unmarshal(global.Values["crawl.max_pages"], &fields.MaxPages); err != nil {
		return TaskFields{}, err
	}
	if err := json.Unmarshal(global.Values["crawl.task_interval_minutes"], &fields.IntervalMinutes); err != nil {
		return TaskFields{}, err
	}
	var configured bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_providers WHERE enabled=1)`).Scan(&configured); err != nil {
		return TaskFields{}, err
	}
	if configured {
		fields.JudgeMode, fields.Profile, fields.NotifyPolicy = analysis.ModeAI, analysis.DefaultProfile(), "good"
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM channels WHERE enabled=1 ORDER BY id`)
	if err != nil {
		return TaskFields{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return TaskFields{}, err
		}
		fields.ChannelIDs = append(fields.ChannelIDs, id)
	}
	return fields, rows.Err()
}

func (s *Service) TaskDefaults(ctx context.Context) (TaskFields, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskFields{}, storageFailure(err)
	}
	defer tx.Rollback()
	global, err := s.settings.SnapshotTx(ctx, tx)
	if err != nil {
		return TaskFields{}, storageFailure(err)
	}
	fields, err := taskDefaultsTx(ctx, tx, global)
	if err != nil {
		return TaskFields{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return TaskFields{}, storageFailure(err)
	}
	return fields, nil
}

func (s *Service) CreateTask(ctx context.Context, patch TaskPatch) (TaskSaveResult, error) {
	release, err := s.beginMutation()
	if err != nil {
		return TaskSaveResult{}, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	defer tx.Rollback()
	global, err := s.settings.SnapshotTx(ctx, tx)
	if err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	defaults, err := taskDefaultsTx(ctx, tx, global)
	if err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	fields, err := applyTaskPatch(defaults, patch)
	if err != nil {
		return TaskSaveResult{}, err
	}
	fields, warnings, err := normalizeTaskFields(nil, fields, patch, global)
	if err != nil {
		return TaskSaveResult{}, err
	}
	if !fields.AccountPinned {
		fields.AccountID, err = pickAccountTx(ctx, tx)
		if err != nil {
			return TaskSaveResult{}, storageFailure(err)
		}
	}
	if err := validateTaskReferencesTx(ctx, tx, fields); err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	now := s.now().UnixMilli()
	task := Task{TaskFields: fields, SearchGeneration: 1, BaselinePending: true, CreatedAt: &now, UpdatedAt: &now, NextRunAt: &now}
	var previousSort int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_index),0) FROM tasks`).Scan(&previousSort); err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	task.SortIndex = previousSort + 1
	if err := insertTaskTx(ctx, tx, &task); err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	release()
	s.wakeScheduler()
	_ = s.emit("task.changed", map[string]any{"task_id": task.ID})
	return TaskSaveResult{Task: task, Warnings: warnings, Effects: TaskSaveEffects{Generation: 1, AddedExcludeWords: []string{}}}, nil
}

func validateTaskReferencesTx(ctx context.Context, tx *sql.Tx, fields TaskFields) error {
	if err := regions.Validate(fields.RegionProvince, fields.RegionCity, fields.RegionDistrict); err != nil {
		field := "region_province"
		var invalid *regions.ValidationError
		if errors.As(err, &invalid) {
			field = invalid.Field
		}
		return invalidTaskField(field, locales.Message{Code: "tasks.invalid_region"})
	}
	if fields.AccountID != nil {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id=?)`, *fields.AccountID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return invalidTaskField("account_id", locales.Message{Code: "settings.reference_not_found"})
		}
	}
	for _, id := range fields.ChannelIDs {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM channels WHERE id=?)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return invalidTaskField("channel_ids", locales.Message{Code: "settings.reference_not_found"})
		}
	}
	return nil
}

func (s *Service) UpdateTask(ctx context.Context, id int64, patch TaskPatch) (TaskSaveResult, error) {
	release, err := s.beginMutation()
	if err != nil {
		return TaskSaveResult{}, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	defer tx.Rollback()
	old, err := loadTaskTx(ctx, tx, id)
	if err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	global, err := s.settings.SnapshotTx(ctx, tx)
	if err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	fields, err := applyTaskPatch(old.TaskFields, patch)
	if err != nil {
		return TaskSaveResult{}, err
	}
	watermark, err := loadTaskProfileWatermarkTx(ctx, tx, old)
	if err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	versionBase := old
	versionBase.Profile.NextNo = watermark
	fields, warnings, err := normalizeTaskFields(&versionBase, fields, patch, global)
	if err != nil {
		return TaskSaveResult{}, err
	}
	if !fields.AccountPinned && (old.AccountPinned || !reflect.DeepEqual(old.AccountID, fields.AccountID)) {
		fields.AccountID, err = pickAccountTx(ctx, tx)
		if err != nil {
			return TaskSaveResult{}, storageFailure(err)
		}
	}
	if err := validateTaskReferencesTx(ctx, tx, fields); err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	now := s.now().UnixMilli()
	updated := old
	updated.TaskFields, updated.UpdatedAt = fields, &now
	effects := TaskSaveEffects{Generation: old.SearchGeneration, AddedExcludeWords: addedExcludeWords(old.ExcludeKeywords, fields.ExcludeKeywords)}
	if taskSearchChanged(old, updated) {
		updated.SearchGeneration++
		updated.BaselinePending = true
		effects.SearchReset, effects.Generation = true, updated.SearchGeneration
		if err := invalidateSearchGenerationTx(ctx, tx, old, updated.SearchGeneration, now); err != nil {
			return TaskSaveResult{}, storageFailure(err)
		}
	}
	if old.IntervalMinutes != updated.IntervalMinutes {
		intervalMS := updated.IntervalMinutes * 60000
		next := now + intervalMS
		updated.NextRunAt = &next
	}
	profileChanged := !reflect.DeepEqual(old.Profile, updated.Profile)
	if profileChanged {
		updated.NoGoodHintDismissedAt = &now
	}
	// 切换成关键词时清单为空，但编号不能重新发给旧条目的历史事实。
	if err := writeTaskProfileWatermarkTx(ctx, tx, id, watermark); err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	if err := saveTaskTx(ctx, tx, updated); err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	if old.JudgeMode != updated.JudgeMode {
		if err := migrateTaskModeTx(ctx, tx, updated, now); err != nil {
			return TaskSaveResult{}, storageFailure(err)
		}
	} else if updated.JudgeMode == analysis.ModeAI {
		if profileChanged {
			profileEffects, err := s.reprofileEffectsTx(ctx, tx, old, updated, now)
			if err != nil {
				return TaskSaveResult{}, storageFailure(err)
			}
			effects.RescoredCount = profileEffects.RescoredCount
			effects.NeedsReanalysisCount = profileEffects.NeedsReanalysisCount
			effects.TitleRejectedCount = profileEffects.TitleRejectedCount
		} else if !reflect.DeepEqual(old.BudgetMin, updated.BudgetMin) || !reflect.DeepEqual(old.BudgetMax, updated.BudgetMax) || !reflect.DeepEqual(old.ExcludeKeywords, updated.ExcludeKeywords) {
			effects.RescoredCount, err = s.recalculateTaskTx(ctx, tx, updated, now)
			if err != nil {
				return TaskSaveResult{}, storageFailure(err)
			}
		}
	}
	effects.ExistingExcludeMatchCount, err = countAddedExcludesTx(ctx, tx, updated.ID, effects.AddedExcludeWords)
	if err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return TaskSaveResult{}, storageFailure(err)
	}
	release()
	if effects.SearchReset {
		// 只取消旧代，不能误关提交后刚启动的新代运行。
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		_ = s.cancelTaskGeneration(cleanup, id, old.SearchGeneration, errSearchChanged)
		cancel()
	}
	s.wakeScheduler()
	_ = s.emit("task.changed", map[string]any{"task_id": id})
	return TaskSaveResult{Task: updated, Effects: effects, Warnings: warnings}, nil
}

func invalidateSearchGenerationTx(ctx context.Context, tx *sql.Tx, old Task, generation, nowMS int64) error {
	message := locales.Message{Code: "run.search_changed"}
	reason, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE findings SET decision='skipped',stage='done',reason=?,retryable=0,score=NULL,grade=NULL,score_breakdown_json=NULL
		WHERE task_id=? AND search_generation<>? AND (decision='pending' OR (decision='failed' AND retryable=1))`, string(reason), old.ID, generation); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE findings SET notify_kind=NULL,notify_skip_reason=?
		WHERE task_id=? AND search_generation<>? AND notified_at IS NULL AND notify_skip_reason IS NULL`, string(reason), old.ID, generation); err != nil {
		return err
	}
	return cancelPendingIntentsTx(ctx, tx, `task_id=? AND finding_id IN (SELECT id FROM findings WHERE task_id=? AND search_generation<>?)`,
		[]any{old.ID, old.ID, generation}, message, nowMS)
}

func migrateTaskModeTx(ctx context.Context, tx *sql.Tx, task Task, nowMS int64) error {
	stage := StageTitle
	if task.JudgeMode == analysis.ModeKeyword {
		stage = StageDetail
	}
	if _, err := tx.ExecContext(ctx, `UPDATE findings SET stage=?,processing_revision=processing_revision+1
		WHERE task_id=? AND search_generation=? AND (decision='pending' OR (decision='failed' AND retryable=1 AND attempts<3))`,
		stage, task.ID, task.SearchGeneration); err != nil {
		return err
	}
	message := locales.Message{Code: "notify.mode_changed"}
	reason, err := json.Marshal(message)
	if err != nil {
		return err
	}
	// 旧结论不按新方式补发；还没得出结论的队列继续正常处理。
	if _, err := tx.ExecContext(ctx, `UPDATE findings SET notify_kind=NULL,notify_skip_reason=?
		WHERE task_id=? AND notified_at IS NULL AND notify_skip_reason IS NULL
		AND (decision IN ('scored','matched','rejected') OR notify_kind IS NOT NULL)`, string(reason), task.ID); err != nil {
		return err
	}
	return cancelPendingIntentsTx(ctx, tx, `task_id=?`, []any{task.ID}, message, nowMS)
}

func (s *Service) DeleteTask(ctx context.Context, id int64) error {
	resume, err := s.quiesceTask(ctx, id, errTaskDeleted)
	if err != nil {
		return err
	}
	defer resume()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageFailure(err)
	}
	defer tx.Rollback()
	if _, err := loadTaskTx(ctx, tx, id); err != nil {
		return storageFailure(err)
	}
	if err := cancelPendingIntentsTx(ctx, tx, `task_id=?`, []any{id}, locales.Message{Code: "tasks.not_found"}, s.now().UnixMilli()); err != nil {
		return storageFailure(err)
	}
	if _, err := s.events.ResolveTx(ctx, tx, "task_failure_pause", id); err != nil {
		return storageFailure(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE id=?`, id); err != nil {
		return storageFailure(err)
	}
	if err := deleteTaskSearchTx(ctx, tx, id); err != nil {
		return storageFailure(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key=?`, taskProfileWatermarkPrefix+strconv.FormatInt(id, 10)); err != nil {
		return storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure(err)
	}
	s.wakeScheduler()
	_ = s.emit("task.changed", map[string]any{"task_id": id})
	return nil
}

func (s *Service) DismissNoGoodHint(ctx context.Context, id int64) error {
	release, err := s.beginMutation()
	if err != nil {
		return err
	}
	defer release()
	result, err := s.db.ExecContext(ctx, `UPDATE tasks SET no_good_hint_dismissed_at=? WHERE id=?`, s.now().UnixMilli(), id)
	if err != nil {
		return storageFailure(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return storageFailure(err)
	}
	if count != 1 {
		return taskNotFound()
	}
	release()
	_ = s.emit("task.changed", map[string]any{"task_id": id})
	return nil
}

func (s *Service) taskViewTx(ctx context.Context, tx *sql.Tx, task Task) (TaskView, error) {
	view := TaskView{Task: task, Runtime: s.taskRuntimeSnapshot(task.ID), FindingCounts: map[string]int64{
		"total": 0, "pending": 0, "scored": 0, "matched": 0, "rejected": 0, "failed": 0, "skipped": 0,
	}}
	rows, err := tx.QueryContext(ctx, `SELECT decision,COUNT(*) FROM findings WHERE task_id=? GROUP BY decision`, task.ID)
	if err != nil {
		return TaskView{}, err
	}
	for rows.Next() {
		var decision string
		var count int64
		if err := rows.Scan(&decision, &count); err != nil {
			rows.Close()
			return TaskView{}, err
		}
		view.FindingCounts[decision] = count
		view.FindingCounts["total"] += count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return TaskView{}, err
	}
	view.NoGoodHint, err = loadNoGoodHintTx(ctx, tx, task)
	return view, err
}

func (s *Service) GetTask(ctx context.Context, id int64) (TaskView, error) {
	s.initRuntime()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskView{}, storageFailure(err)
	}
	defer tx.Rollback()
	task, err := loadTaskTx(ctx, tx, id)
	if err != nil {
		return TaskView{}, storageFailure(err)
	}
	view, err := s.taskViewTx(ctx, tx, task)
	if err != nil {
		return TaskView{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return TaskView{}, storageFailure(err)
	}
	return view, nil
}

func (s *Service) ListTasks(ctx context.Context, query TaskQuery) (Page[TaskView], error) {
	s.initRuntime()
	if !utf8.ValidString(query.Q) {
		return Page[TaskView]{}, &ServiceError{Kind: ErrorBadRequest, Reason: locales.Message{Code: "service.invalid_query"}, Fields: []settings.Issue{}}
	}
	query.Q = strings.TrimSpace(query.Q)
	var err error
	query.ListQuery, err = normalizeListQuery(query.ListQuery)
	if err != nil {
		return Page[TaskView]{}, err
	}
	binding := query
	binding.ListQuery = ListQuery{}
	var anchor struct {
		SortIndex int64 `json:"sort_index"`
		ID        int64 `json:"id"`
	}
	if err := decodeCursor(query.Cursor, "tasks", binding, &anchor); err != nil {
		return Page[TaskView]{}, err
	}
	if query.Cursor != "" && anchor.ID <= 0 {
		return Page[TaskView]{}, &ServiceError{Kind: ErrorBadRequest, Reason: locales.Message{Code: "service.invalid_query"}, Fields: []settings.Issue{}}
	}
	where, args := []string{"1=1"}, []any{}
	if query.Q != "" {
		where = append(where, `(instr(lower(name),lower(?))>0 OR instr(lower(keyword),lower(?))>0)`)
		args = append(args, query.Q, query.Q)
	}
	if query.Enabled != nil {
		where = append(where, `enabled=?`)
		args = append(args, *query.Enabled)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Page[TaskView]{}, storageFailure(err)
	}
	defer tx.Rollback()
	page := Page[TaskView]{Items: []TaskView{}}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE `+strings.Join(where, " AND "), args...).Scan(&page.Total); err != nil {
		return Page[TaskView]{}, storageFailure(err)
	}
	if query.Cursor != "" {
		where = append(where, `(sort_index>? OR (sort_index=? AND id>?))`)
		args = append(args, anchor.SortIndex, anchor.SortIndex, anchor.ID)
	}
	args = append(args, query.Limit+1)
	rows, err := tx.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE `+strings.Join(where, " AND ")+` ORDER BY sort_index,id LIMIT ?`, args...)
	if err != nil {
		return Page[TaskView]{}, storageFailure(err)
	}
	tasks := []Task{}
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			rows.Close()
			return Page[TaskView]{}, storageFailure(err)
		}
		tasks = append(tasks, task)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Page[TaskView]{}, storageFailure(err)
	}
	if int64(len(tasks)) > query.Limit {
		tasks = tasks[:query.Limit]
		last := tasks[len(tasks)-1]
		anchor.SortIndex, anchor.ID = last.SortIndex, last.ID
		next, err := encodeCursor("tasks", binding, anchor)
		if err != nil {
			return Page[TaskView]{}, storageFailure(err)
		}
		page.NextCursor = &next
	}
	for i := range tasks {
		if err := loadTaskChannelsTx(ctx, tx, &tasks[i]); err != nil {
			return Page[TaskView]{}, storageFailure(err)
		}
		view, err := s.taskViewTx(ctx, tx, tasks[i])
		if err != nil {
			return Page[TaskView]{}, storageFailure(err)
		}
		page.Items = append(page.Items, view)
	}
	if err := tx.Commit(); err != nil {
		return Page[TaskView]{}, storageFailure(err)
	}
	return page, nil
}

func countAddedExcludesTx(ctx context.Context, tx *sql.Tx, taskID int64, words []string) (int64, error) {
	ids, err := matchingExcludesTx(ctx, tx, taskID, words)
	return int64(len(ids)), err
}

func matchingExcludesTx(ctx context.Context, tx *sql.Tx, taskID int64, words []string) ([]int64, error) {
	ids := []int64{}
	if len(words) == 0 {
		return ids, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT f.id,l.title,l.description FROM findings f JOIN listings l ON l.item_id=f.item_id
		WHERE f.task_id=? AND f.decision<>'rejected' AND l.is_favorite=0 ORDER BY f.id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var title string
		var description sql.NullString
		if err := rows.Scan(&id, &title, &description); err != nil {
			return nil, err
		}
		if len(analysis.MatchingKeywords(title+"\n"+description.String, words)) != 0 {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

func (s *Service) HideAddedExcludes(ctx context.Context, taskID int64, words []string) (int64, error) {
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
	task, err := loadTaskTx(ctx, tx, taskID)
	if err != nil {
		return 0, storageFailure(err)
	}
	words = analysis.ParseKeywords(strings.Join(words, "\n"))
	allowed := map[string]bool{}
	for _, word := range task.ExcludeKeywords {
		allowed[word] = true
	}
	for _, word := range words {
		if !allowed[word] {
			return 0, &ServiceError{Kind: ErrorConflict, Reason: locales.Message{Code: "tasks.exclusions_changed"}, Fields: []settings.Issue{}}
		}
	}
	ids, err := matchingExcludesTx(ctx, tx, taskID, words)
	if err != nil {
		return 0, storageFailure(err)
	}
	for _, id := range ids {
		var title string
		var description sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT l.title,l.description FROM listings l JOIN findings f ON f.item_id=l.item_id WHERE f.id=?`, id).Scan(&title, &description); err != nil {
			return 0, storageFailure(err)
		}
		hits := analysis.MatchingKeywords(title+"\n"+description.String, words)
		if len(hits) == 0 {
			return 0, storageFailure(errors.New("排除词匹配结果在事务内改变"))
		}
		reason, err := json.Marshal(locales.Message{Code: "analysis.keyword_excluded", Args: map[string]any{"word": hits[0]}})
		if err != nil {
			return 0, storageFailure(err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE findings SET decision='rejected',reject_kind='excluded',stage='done',
			decided_layer=1,reason=?,score=NULL,grade=NULL,score_breakdown_json=NULL,retryable=0,
			processing_revision=processing_revision+1 WHERE id=?`, string(reason), id)
		if err != nil {
			return 0, storageFailure(err)
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			if err == nil {
				err = errors.New("发现排除更新行数不正确")
			}
			return 0, storageFailure(err)
		}
		if err := cancelPendingIntentsForFindingsTx(ctx, tx, []int64{id}, locales.Message{Code: "analysis.keyword_excluded", Args: map[string]any{"word": hits[0]}}, s.now().UnixMilli()); err != nil {
			return 0, storageFailure(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, storageFailure(err)
	}
	release()
	_ = s.emit("finding.changed", map[string]any{"task_id": taskID})
	return int64(len(ids)), nil
}

func (s *Service) reprofileEffectsTx(ctx context.Context, tx *sql.Tx, old, updated Task, nowMS int64) (TaskSaveEffects, error) {
	effects := TaskSaveEffects{Generation: updated.SearchGeneration, AddedExcludeWords: []string{}}
	var err error
	effects.RescoredCount, err = s.recalculateTaskTx(ctx, tx, updated, nowMS)
	if err != nil {
		return effects, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM findings WHERE task_id=? AND decision='rejected'
		AND decided_layer=2 AND reject_kind='unmatched'`, updated.ID).Scan(&effects.TitleRejectedCount); err != nil {
		return effects, err
	}
	changed := changedProfileItems(old.Profile, updated.Profile)
	if len(changed.musts)+len(changed.bonuses)+len(changed.checks)+len(changed.specs) == 0 {
		return effects, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT analysis_json,user_overrides_json FROM findings WHERE task_id=?
		AND analysis_json IS NOT NULL AND decided_layer=3 AND (decision='scored' OR (decision='rejected' AND reject_kind='unmatched'))`, updated.ID)
	if err != nil {
		return effects, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var overridesRaw sql.NullString
		if err := rows.Scan(&raw, &overridesRaw); err != nil {
			return effects, err
		}
		var facts analysis.Facts
		var overrides analysis.UserOverrides
		if json.Unmarshal([]byte(raw), &facts) != nil || overridesRaw.Valid && json.Unmarshal([]byte(overridesRaw.String), &overrides) != nil {
			return effects, errors.New("已有商品分析事实损坏")
		}
		if changed.needsFacts(facts, overrides) {
			effects.NeedsReanalysisCount++
		}
	}
	return effects, rows.Err()
}

type changedTaskProfile struct {
	musts, bonuses, checks map[string]int64
	specs                  map[string]bool
}

func changedProfileItems(old, next analysis.Profile) changedTaskProfile {
	changed := changedTaskProfile{musts: map[string]int64{}, bonuses: map[string]int64{}, checks: map[string]int64{}, specs: map[string]bool{}}
	for _, pair := range []struct {
		old, next []analysis.Requirement
		out       map[string]int64
	}{{old.Musts, next.Musts, changed.musts}, {old.Bonuses, next.Bonuses, changed.bonuses}} {
		versions := map[string]int64{}
		for _, item := range pair.old {
			versions[item.ID] = item.Rev
		}
		for _, item := range pair.next {
			if version, found := versions[item.ID]; !found || version != item.Rev {
				pair.out[item.ID] = item.Rev
			}
		}
	}
	versions := map[string]int64{}
	for _, item := range old.Risks {
		versions[item.ID] = item.Rev
	}
	for _, item := range next.Risks {
		if version, found := versions[item.ID]; !found || version != item.Rev {
			changed.checks[item.ID] = item.Rev
		}
	}
	priorSpecs := map[string]bool{}
	for _, spec := range old.Specs {
		priorSpecs[spec.Name] = true
	}
	for _, spec := range next.Specs {
		if !priorSpecs[spec.Name] {
			changed.specs[spec.Name] = true
		}
	}
	return changed
}

func (changed changedTaskProfile) needsFacts(facts analysis.Facts, overrides analysis.UserOverrides) bool {
	for _, pair := range []struct {
		changed map[string]int64
		facts   []analysis.RequirementFact
	}{{changed.musts, facts.Musts}, {changed.bonuses, facts.Bonuses}} {
		for id, revision := range pair.changed {
			found := false
			for _, fact := range pair.facts {
				if fact.ID == id && fact.Rev == revision {
					found = true
					break
				}
			}
			if !found {
				return true
			}
		}
	}
	for key, revision := range changed.checks {
		if override, exists := overrides.Risks[key]; exists && override.Rev == revision {
			continue
		}
		found := false
		for _, fact := range facts.Checks {
			if fact.Key == key && fact.Rev == revision {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	for name := range changed.specs {
		found := false
		for _, spec := range facts.Specs {
			if spec.Name == name {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}
