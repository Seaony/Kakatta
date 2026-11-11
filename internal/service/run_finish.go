package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/analysis/prompts"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

// 抓取和判断已停止并汇合；这里只处理已保存的结果，不持事务发通知。
func (s *Service) finishRun(ctx context.Context, run *runExecution, stopCause error) error {
	if run == nil || run.Ref.RunID < 1 || run.Ref.TaskID < 1 {
		return storageFailure(errors.New("运行结束信息缺失"))
	}
	s.initRuntime()
	record := finishRecord(run, stopCause)
	infrastructure := runInfrastructureFailure(stopCause)
	changed := errors.Is(stopCause, errSearchChanged) || errors.Is(stopCause, errTaskDeleted)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageFailure(err)
	}
	defer tx.Rollback()
	ended, err := runAlreadyEndedTx(ctx, tx, run.Ref.RunID)
	if err != nil || ended {
		return storageFailure(err)
	}
	task, err := loadTaskTx(ctx, tx, run.Ref.TaskID)
	if err != nil {
		return storageFailure(err)
	}
	changed = changed || task.SearchGeneration != run.Ref.SearchGeneration
	_, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return err
	}
	if !changed && !infrastructure && task.JudgeMode == analysis.ModeAI {
		if _, err := s.recalculateTaskTx(ctx, tx, task, now.UnixMilli()); err != nil {
			return storageFailure(err)
		}
	}
	if err := appsettings.SaveClock(ctx, tx, clock); err != nil {
		return storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure(err)
	}

	var notifyErr error
	if !changed && !infrastructure {
		if s.notifier == nil {
			notifyErr = errors.New("运行通知依赖缺失")
		} else {
			var result NotifyResult
			result, notifyErr = s.notifier.FlushRun(ctx, run.Ref, append([]int64(nil), run.FirstConclusionIDs...))
			record.NotifiedCount = &result.NotifiedCount
			record.Warnings = append(record.Warnings, result.Warnings...)
		}
		if notifyErr != nil {
			failure := FailureConfig
			outcome := RunFailed
			record.Outcome, record.FailureKind = &outcome, &failure
			record.Message = &locales.Message{Code: "service.storage_failure", Args: map[string]any{}}
		}
	}

	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Join(storageFailure(err), notifyErr)
	}
	defer tx.Rollback()
	ended, err = runAlreadyEndedTx(ctx, tx, run.Ref.RunID)
	if err != nil || ended {
		return errors.Join(storageFailure(err), notifyErr)
	}
	task, err = loadTaskTx(ctx, tx, run.Ref.TaskID)
	if err != nil {
		return errors.Join(storageFailure(err), notifyErr)
	}
	changed = changed || task.SearchGeneration != run.Ref.SearchGeneration
	if changed {
		cancelled := finishRecord(run, errSearchChanged)
		record.Outcome, record.FailureKind, record.Message = cancelled.Outcome, cancelled.FailureKind, cancelled.Message
	}
	snapshot, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return errors.Join(err, notifyErr)
	}
	endedAt := now.UnixMilli()
	record.EndedAt = &endedAt
	recommended, err := countRunRecommendationsTx(ctx, tx, run)
	if err != nil {
		return errors.Join(storageFailure(err), notifyErr)
	}
	record.RecommendedCount = &recommended
	eventIDs := []int64{}
	eventsChanged := false
	if !changed {
		eventID, eventChanged, err := s.finishTaskTx(ctx, tx, task, record, snapshot, now)
		if err != nil {
			return errors.Join(storageFailure(err), notifyErr)
		}
		eventsChanged = eventsChanged || eventChanged
		if eventID != 0 {
			eventIDs = append(eventIDs, eventID)
		}
	}
	record.Warnings, err = normalizeRunWarnings(record.Warnings)
	if err != nil {
		return errors.Join(storageFailure(err), notifyErr)
	}
	if err := finishRunTx(ctx, tx, record); err != nil {
		return errors.Join(storageFailure(err), notifyErr)
	}
	if !changed {
		eventID, eventChanged, err := s.finishLayoutEventTx(ctx, tx, run, now)
		if err != nil {
			return errors.Join(storageFailure(err), notifyErr)
		}
		eventsChanged = eventsChanged || eventChanged
		if eventID != 0 {
			eventIDs = append(eventIDs, eventID)
		}
	}
	if err := appsettings.SaveClock(ctx, tx, clock); err != nil {
		return errors.Join(storageFailure(err), notifyErr)
	}
	if err := tx.Commit(); err != nil {
		return errors.Join(storageFailure(err), notifyErr)
	}
	run.RecommendedCount, run.Record = recommended, record
	s.wakeScheduler()
	emitErr := s.emit("run.changed", record)
	if !changed {
		emitErr = errors.Join(emitErr, s.emit("task.changed", map[string]any{"task_id": task.ID}))
	}
	if eventsChanged {
		emitErr = errors.Join(emitErr, s.emit("system_event.changed", map[string]any{"all": true}))
	}
	if notifyErr != nil {
		return errors.Join(storageFailure(notifyErr), emitErr)
	}
	if infrastructure {
		return errors.Join(storageFailure(stopCause), emitErr)
	}
	for _, id := range eventIDs {
		if err := s.notifier.System(ctx, id); err != nil {
			return errors.Join(err, emitErr)
		}
	}
	if !changed && record.Outcome != nil && *record.Outcome == RunSuccess {
		s.startRunInsight(run)
	}
	return emitErr
}

func runAlreadyEndedTx(ctx context.Context, tx *sql.Tx, id int64) (bool, error) {
	var endedAt *int64
	err := tx.QueryRowContext(ctx, `SELECT ended_at FROM run_records WHERE id=?`, id).Scan(&endedAt)
	return endedAt != nil, err
}

func countRunRecommendationsTx(ctx context.Context, tx *sql.Tx, run *runExecution) (int64, error) {
	concluded := make(map[int64]bool, len(run.FirstConclusionIDs))
	for _, id := range run.FirstConclusionIDs {
		concluded[id] = true
	}
	// 每项保存本次处理的发现编号和工作版本，不能借后来重新分析的结果。
	works := make([][2]int64, 0, len(run.P))
	for _, work := range run.P {
		if work.RunRef == run.Ref && concluded[work.FindingID] {
			works = append(works, [2]int64{work.FindingID, work.ProcessingRevision})
		}
	}
	if len(works) == 0 {
		return 0, nil
	}
	raw, err := json.Marshal(works)
	if err != nil {
		return 0, err
	}
	var count int64
	err = tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT f.id)`+findingFromSQL+`
		JOIN json_each(?) work ON f.id=json_extract(work.value,'$[0]')
		WHERE f.task_id=? AND f.search_generation=? AND f.processing_revision=json_extract(work.value,'$[1]')
		AND f.stage='done' AND (`+findingVisibleSQL+`) AND (`+findingRecommendationBudgetSQL+`)
		AND (f.decision='matched' OR (f.decision='scored' AND f.grade IS NOT 'avoid'
		AND (f.score>=80 OR (f.score IS NULL AND f.grade IS NULL))))`, string(raw), run.Ref.TaskID, run.Ref.SearchGeneration).Scan(&count)
	return count, err
}

func finishRecord(run *runExecution, cause error) RunRecord {
	record := run.Record
	record.ID = run.Ref.RunID
	record.PageCount = &run.PageCount
	if record.ScannedCount == nil {
		record.ScannedCount = &run.SearchSaved.ScannedCount
	}
	record.NewCount, record.Layer1Rejected = &run.SearchSaved.NewCount, &run.SearchSaved.Layer1Rejected
	record.Layer2Count, record.Layer2Rejected = &run.Layer2Count, &run.Layer2Rejected
	record.DetailCount, record.SellerPageCount = &run.DetailCount, &run.SellerPageCount
	record.AnalyzedCount, record.RecommendedCount = &run.AnalyzedCount, &run.RecommendedCount
	record.Warnings = append(append([]locales.Message{}, record.Warnings...), run.Warnings...)
	outcome, failure, code := RunSuccess, FailureKind(""), "run.completed"
	args := map[string]any{"count": run.PageCount, "page_count": run.PageCount, "analyzed_count": run.AnalyzedCount}
	if cause == nil && run.SearchSaved.NewCount == 0 {
		code, args = "run.no_new_items", map[string]any{}
	}
	if cause != nil {
		outcome, args = RunFailed, map[string]any{}
		switch {
		case errors.Is(cause, errSearchChanged), errors.Is(cause, errTaskDeleted):
			failure, code = FailureCancelled, "run.cancelled"
		case errors.Is(cause, web.ErrEnvBlocked):
			failure, code = FailureRiskEnv, "run.risk_env"
		case errors.Is(cause, web.ErrDenied):
			failure, code = FailureRiskDeny, "run.risk_deny"
		case errors.Is(cause, web.ErrNeedVerify):
			failure, code = FailureRiskVerify, "run.risk_verify"
		case errors.Is(cause, web.ErrLoginExpired):
			outcome, failure, code = RunSkipped, FailureLogin, "run.login_expired"
		case runInfrastructureFailure(cause):
			failure, code = FailureConfig, "service.storage_failure"
		case errors.Is(cause, client.ErrConfigChanged):
			failure, code = FailureConfig, "run.model_config_changed"
		case errors.Is(cause, errServiceClosed):
			failure, code = FailureRestart, "run.restart"
		case errors.Is(cause, errRunTimeout):
			failure, code = FailureTimeout, "run.timeout"
		case errors.Is(cause, errUserCancelled):
			failure, code = FailureCancelled, "run.cancelled"
		case errors.Is(cause, errRunNetwork):
			failure, code = FailureNetwork, "run.network_failed"
		case errors.Is(cause, context.DeadlineExceeded):
			failure, code = FailurePage, "run.page_failed"
		case errors.Is(cause, context.Canceled):
			failure, code = FailureCancelled, "run.cancelled"
		case errors.Is(cause, web.ErrQuota):
			outcome, failure, code = RunSkipped, FailureQuota, "run.quota"
		default:
			var network net.Error
			if errors.As(cause, &network) || record.FailureKind != nil && *record.FailureKind == FailureNetwork {
				failure, code = FailureNetwork, "run.network_failed"
			} else {
				failure, code = FailurePage, "run.page_failed"
			}
		}
	}
	record.Outcome, record.FailureKind = &outcome, nil
	if failure != "" {
		record.FailureKind = &failure
	}
	record.Message = &locales.Message{Code: code, Args: args}
	return record
}

func runInfrastructureFailure(cause error) bool {
	var serviceError *ServiceError
	return errors.Is(cause, client.ErrInfrastructure) || errors.As(cause, &serviceError) && serviceError.Kind == ErrorUnavailable
}

func (s *Service) finishTaskTx(ctx context.Context, tx *sql.Tx, task Task, record RunRecord, snapshot settings.Snapshot, now time.Time) (int64, bool, error) {
	next, err := nextRunAt(task, snapshot, now, s.pacer)
	if err != nil {
		return 0, false, err
	}
	failures, paused, baseline := task.ConsecutiveFailures, task.FailurePausedUntil, task.BaselinePending
	lastSuccess := task.LastSuccessAt
	var eventID int64
	var eventChanged bool
	events := s.events
	events.Now = func() time.Time { return now }
	if *record.Outcome == RunSuccess {
		failures, paused, baseline = 0, nil, false
		at := now.UnixMilli()
		lastSuccess = &at
		resolved, err := events.ResolveTx(ctx, tx, string(EventTaskFailurePause), task.ID)
		if err != nil {
			return 0, false, err
		}
		eventChanged = resolved
	} else if record.FailureKind != nil && (*record.FailureKind == FailureNetwork || *record.FailureKind == FailurePage || *record.FailureKind == FailureTimeout) {
		if failures < 0 {
			return 0, false, errors.New("任务连续失败次数无效")
		}
		failures++
		var threshold, minutes int64
		if err := json.Unmarshal(snapshot.Values["crawl.failure_threshold"], &threshold); err != nil {
			return 0, false, err
		}
		if err := json.Unmarshal(snapshot.Values["crawl.failure_pause_minutes"], &minutes); err != nil {
			return 0, false, err
		}
		if failures >= threshold {
			until := now.Add(time.Duration(minutes) * time.Minute).UnixMilli()
			paused = &until
			message := locales.Message{Code: "run.failure_pause", Args: map[string]any{"task": task.Name, "count": failures, "minutes": minutes, "reason": *record.Message}}
			created, err := events.RaiseTx(ctx, tx, string(EventTaskFailurePause), task.ID, message)
			if err != nil {
				return 0, false, err
			}
			eventChanged = created
			if created {
				eventID, err = pendingSystemEventIDTx(ctx, tx, EventTaskFailurePause, task.ID)
				if err != nil {
					return 0, false, err
				}
			}
		}
	}
	var lastError any
	if *record.Outcome != RunSuccess {
		raw, err := json.Marshal(record.Message)
		if err != nil {
			return 0, false, err
		}
		lastError = string(raw)
	}
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET last_run_at=?,last_success_at=?,next_run_at=?,
		last_run_outcome=?,last_error=?,consecutive_failures=?,failure_paused_until=?,baseline_pending=?
		WHERE id=? AND search_generation=?`, record.StartedAt, lastSuccess, next.UnixMilli(),
		*record.Outcome, lastError, failures, paused, baseline, task.ID, task.SearchGeneration)
	return eventID, eventChanged, err
}

func pendingSystemEventIDTx(ctx context.Context, tx *sql.Tx, kind SystemEventKind, subjectID int64) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM system_events WHERE kind=? AND subject_id=? AND resolved_at IS NULL`, kind, subjectID).Scan(&id)
	return id, err
}

func (s *Service) finishLayoutEventTx(ctx context.Context, tx *sql.Tx, run *runExecution, now time.Time) (int64, bool, error) {
	events := s.events
	events.Now = func() time.Time { return now }
	if !run.LayoutError && run.PageCount > 0 && run.FirstPageCount > 0 {
		resolved, err := events.ResolveTx(ctx, tx, string(EventXianyuLayout), 0)
		return 0, resolved, err
	}
	layout := run.LayoutError
	reason := locales.Message{Code: "run.layout_response_invalid", Args: map[string]any{}}
	if !layout && run.PageCount > 0 && run.FirstPageCount == 0 {
		var hadItems bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_records WHERE task_id=? AND scanned_count>0)`, run.Ref.TaskID).Scan(&hadItems); err != nil {
			return 0, false, err
		}
		if hadItems {
			var emptyRuns int
			// 旧失败记录的扫描数可能未保存，只有本次首屏已知或旧次成功才能确认空页。
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM
				(SELECT id,outcome,page_count,scanned_count FROM run_records WHERE task_id=? AND ended_at IS NOT NULL ORDER BY started_at DESC,id DESC LIMIT 3)
				WHERE page_count=1 AND scanned_count=0 AND (id=? OR outcome='success')`, run.Ref.TaskID, run.Ref.RunID).Scan(&emptyRuns); err != nil {
				return 0, false, err
			}
			layout = emptyRuns == 3
			if layout {
				reason = locales.Message{Code: "run.layout_empty_repeated", Args: map[string]any{"count": int64(3)}}
			}
		}
	}
	if !layout {
		return 0, false, nil
	}
	created, err := events.RaiseTx(ctx, tx, string(EventXianyuLayout), 0, locales.Message{Code: "run.layout_changed", Args: map[string]any{"reason": reason}})
	if err != nil || !created {
		return 0, false, err
	}
	id, err := pendingSystemEventIDTx(ctx, tx, EventXianyuLayout, 0)
	return id, true, err
}

// 行情解读不占页面名额；仍跟随服务寿命，关闭服务时等待其退出。
func (s *Service) startRunInsight(run *runExecution) {
	ctx, release, ok := s.claimInsight(run.Ref.TaskID, run.Account.ID)
	if !ok {
		return
	}
	go func() {
		defer release()
		if err := s.generateRunInsight(ctx, run); err != nil && ctx.Err() == nil {
			s.recordInsightWarning(run.Ref.RunID)
		}
	}()
}

func (s *Service) generateRunInsight(ctx context.Context, run *runExecution) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	task, err := loadTaskTx(ctx, tx, run.Ref.TaskID)
	if err != nil || task.SearchGeneration != run.Ref.SearchGeneration {
		return err
	}
	_, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return err
	}
	var eligible bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_providers WHERE enabled=1 AND error_state IS NULL)`).Scan(&eligible); err != nil {
		return err
	}
	insight, err := loadMarketInsightTx(ctx, tx, task.ID)
	if err != nil {
		return err
	}
	if insight != nil && insight.GeneratedAt != nil && now.Sub(time.UnixMilli(*insight.GeneratedAt)) < 6*time.Hour {
		eligible = false
	}
	var market analysis.MarketResult
	var request client.Request
	if eligible {
		market, err = loadMarketTx(ctx, tx, task, now.UnixMilli(), clock.Timezone)
		if err != nil {
			return err
		}
		location, _ := time.LoadLocation(clock.Timezone)
		day, samples := now.In(location).Format(time.DateOnly), 0
		for _, group := range market.Groups {
			if !group.Selected {
				continue
			}
			for _, daily := range group.Daily {
				if daily.Day == day {
					samples += daily.Sample
				}
			}
		}
		eligible = samples >= 10
		if eligible {
			request, err = prompts.BuildMarket(run.Prompts, &task.ID, market)
			if err != nil {
				return err
			}
		}
	}
	if err := appsettings.SaveClock(ctx, tx, clock); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if !eligible {
		return nil
	}
	if s.models == nil {
		return errors.New("行情模型依赖缺失")
	}
	result, err := s.models.Complete(ctx, request)
	if err != nil {
		return err
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	task, err = loadTaskTx(ctx, tx, run.Ref.TaskID)
	if err != nil || task.SearchGeneration != run.Ref.SearchGeneration {
		return err
	}
	_, clock, now, err = s.observeNowTx(ctx, tx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(market)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO market_insights(task_id,generated_at,stats_json,text,provider_id,model) VALUES(?,?,?,?,?,?)`,
		task.ID, now.UnixMilli(), string(raw), result.Text, result.ProviderID, result.Model); err != nil {
		return err
	}
	if err := appsettings.SaveClock(ctx, tx, clock); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.emit("market.changed", map[string]any{"task_id": task.ID})
}

func (s *Service) recordInsightWarning(runID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	_, err = appendRunStepTx(ctx, tx, RunStep{RunID: runID, At: finishTimePointer(s.now()), Status: finishStepPointer(StepFail),
		Message: &locales.Message{Code: "run.market_failed", Args: map[string]any{}}})
	if err == nil {
		_ = tx.Commit()
	}
}

func finishTimePointer(value time.Time) *int64       { at := value.UnixMilli(); return &at }
func finishStepPointer(value StepStatus) *StepStatus { return &value }
