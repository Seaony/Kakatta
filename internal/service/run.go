package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func (s *Service) preflightTx(ctx context.Context, tx *sql.Tx, task Task, trigger RunTrigger) (preflightResult, error) {
	result := preflightResult{Task: task}
	snapshot, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return result, err
	}
	result.Snapshot, result.Clock, result.Now = snapshot, clock, now
	overrides := json.RawMessage(`{}`)
	if task.CrawlOverrides != nil {
		overrides, err = json.Marshal(task.CrawlOverrides)
		if err != nil {
			return result, storageFailure(err)
		}
	}
	result.Config, _, err = settings.ResolveTask(snapshot, task.IntervalMinutes, overrides)
	if err != nil {
		return result, storageFailure(err)
	}
	if trigger != TriggerAuto && trigger != TriggerManual {
		return result, storageFailure(errors.New("运行触发方式无效"))
	}
	if trigger == TriggerAuto {
		if !task.Enabled {
			result.Blocked = runBlock("run.disabled", nil)
			return result, nil
		}
		for _, gate := range []struct {
			code string
			at   *int64
		}{{"run.not_due", task.NextRunAt}, {"run.failure_paused", task.FailurePausedUntil}} {
			if gate.at != nil && *gate.at > now.UnixMilli() {
				mergeRunBlock(&result.Blocked, runBlock(gate.code, gate.at))
			}
		}
	}
	if task.AccountID == nil {
		setRunUserReason(&result, trigger, "run.no_account")
		return result, nil
	}
	result.Account, err = loadAccountTx(ctx, tx, *task.AccountID)
	if err != nil {
		return result, storageFailure(err)
	}
	account := result.Account
	if account.LastRunEndedAt != nil {
		if account.NextGapSeconds == nil {
			return result, storageFailure(errors.New("账号间隔记录缺失"))
		}
		until, err := accountDeadline(*account.LastRunEndedAt, *account.NextGapSeconds, 1000)
		if err != nil {
			return result, storageFailure(err)
		}
		if until > now.UnixMilli() {
			mergeRunBlock(&result.Blocked, runBlock("run.account_gap", &until))
		}
	}
	switch account.State {
	case AccountSignedOut, AccountExpired, AccountNeedsVerification:
		code := map[AccountState]string{AccountSignedOut: "run.account_signed_out", AccountExpired: "run.account_expired", AccountNeedsVerification: "run.account_needs_verification"}[account.State]
		setRunUserReason(&result, trigger, code)
		return result, nil
	case AccountCooling, AccountDenied:
		if account.CooldownUntil == nil {
			return result, storageFailure(errors.New("账号暂停截止时间缺失"))
		}
		if *account.CooldownUntil > now.UnixMilli() && (account.State == AccountDenied || trigger != TriggerManual || account.CooldownManualUsed) {
			code := "run.account_cooling"
			if account.State == AccountDenied {
				code = "run.account_denied"
			}
			mergeRunBlock(&result.Blocked, runBlock(code, account.CooldownUntil))
		}
	case AccountActive:
	default:
		return result, storageFailure(errors.New("账号状态无效"))
	}
	limits, err := quotaLimits(snapshot)
	if err != nil {
		return result, storageFailure(err)
	}
	_, visits, err := loadQuotaTx(ctx, tx, account.ID, clock, now, limits)
	if err != nil {
		return result, storageFailure(err)
	}
	reason, readyAt, err := quotaReadyAt(visits, clock, requiredEntryVisits(task.TaskFields), quota.Search, limits)
	if err != nil {
		return result, storageFailure(err)
	}
	if reason != nil {
		mergeRunBlock(&result.Blocked, &RuntimeBlocked{Reason: *reason, NextAllowedAt: readyAt})
	}
	if task.JudgeMode == analysis.ModeAI {
		blocked, err := aiRunBudgetTx(ctx, tx, snapshot, clock)
		if err != nil {
			return result, storageFailure(err)
		}
		mergeRunBlock(&result.Blocked, blocked)
	} else if task.JudgeMode != analysis.ModeKeyword {
		return result, storageFailure(errors.New("任务判断方式无效"))
	}
	var quiet settings.QuietHours
	if err := json.Unmarshal(snapshot.Values["crawl.quiet_hours"], &quiet); err != nil {
		return result, storageFailure(err)
	}
	location, err := time.LoadLocation(clock.Timezone)
	if err != nil {
		return result, storageFailure(err)
	}
	ready := now
	if result.Blocked != nil && result.Blocked.NextAllowedAt != nil {
		ready = time.UnixMilli(*result.Blocked.NextAllowedAt)
	}
	until, err := quietUntil(ready, quiet, location)
	if err != nil {
		return result, storageFailure(err)
	}
	if until != nil {
		at := until.UnixMilli()
		mergeRunBlock(&result.Blocked, runBlock("run.quiet_hours", &at))
	}
	if task.JudgeMode != analysis.ModeAI {
		return result, nil
	}
	if len(task.Profile.Specs) == 0 && len(task.Profile.Musts) == 0 {
		setRunUserReason(&result, trigger, "run.empty_profile")
		return result, nil
	}
	var available bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_providers WHERE enabled=1 AND error_state IS NULL)`).Scan(&available); err != nil {
		return result, storageFailure(err)
	}
	if !available {
		setRunUserReason(&result, trigger, "run.no_ai_provider")
	}
	return result, nil
}

func runBlock(code string, at *int64) *RuntimeBlocked {
	return &RuntimeBlocked{Reason: locales.Message{Code: code, Args: map[string]any{}}, NextAllowedAt: int64Copy(at)}
}

func setRunUserReason(result *preflightResult, trigger RunTrigger, code string) {
	blocked := runBlock(code, nil)
	result.UserReason, result.NeedsUser = &blocked.Reason, true
	// 自动计划仍等已有的有限时间门禁；展示原因独立，不能承诺到时就能运行。
	if trigger == TriggerManual || result.Blocked == nil || result.Blocked.NextAllowedAt == nil {
		result.Blocked = blocked
	}
}

func mergeRunBlock(current **RuntimeBlocked, next *RuntimeBlocked) {
	if next == nil {
		return
	}
	if *current == nil || (*current).NextAllowedAt != nil && (next.NextAllowedAt == nil || *next.NextAllowedAt > *(*current).NextAllowedAt) {
		*current = next
	}
}

func aiRunBudgetTx(ctx context.Context, tx *sql.Tx, snapshot settings.Snapshot, clock quota.ClockState) (*RuntimeBlocked, error) {
	var limit int64
	if err := json.Unmarshal(snapshot.Values["ai.daily_token_limit"], &limit); err != nil || limit < 0 {
		return nil, errors.New("模型每日上限无效")
	}
	if limit == 0 {
		return nil, nil
	}
	state := clock
	for remaining := len(clock.Windows) + 3; remaining > 0; remaining-- {
		start, err := state.DayStart()
		if err != nil {
			return nil, err
		}
		var used int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(COALESCE(input_tokens,0)+COALESCE(output_tokens,0)),0) FROM ai_calls WHERE at>=?`, start.UnixMilli()).Scan(&used); err != nil {
			return nil, err
		}
		if used < limit {
			if state.HighestUTC == clock.HighestUTC {
				return nil, nil
			}
			return runBlock("run.ai_daily_limit", &state.HighestUTC), nil
		}
		boundary, err := nextQuotaDayBoundary(state)
		if err != nil {
			return nil, err
		}
		state, _, err = quota.AdvanceClock(state, time.UnixMilli(boundary), state.Timezone)
		if err != nil {
			return nil, err
		}
	}
	return nil, errors.New("无法确定模型每日额度恢复时间")
}

func blockedRunError(blocked *RuntimeBlocked) error {
	return &ServiceError{Kind: ErrorConflict, Reason: blocked.Reason, NextAllowedAt: int64Copy(blocked.NextAllowedAt), Fields: []settings.Issue{}}
}

func (s *Service) RunNow(ctx context.Context, taskID int64) (RunSubmission, error) {
	if err := ctx.Err(); err != nil {
		return RunSubmission{}, err
	}
	release, err := s.beginMutation()
	if err != nil {
		return RunSubmission{}, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunSubmission{}, storageFailure(err)
	}
	defer tx.Rollback()
	task, err := loadTaskTx(ctx, tx, taskID)
	if err != nil {
		return RunSubmission{}, storageFailure(err)
	}
	preflight, err := s.preflightTx(ctx, tx, task, TriggerManual)
	if err != nil {
		return RunSubmission{}, err
	}
	if preflight.Blocked != nil {
		if _, err := s.recordPreflightSkipTx(ctx, tx, preflight, TriggerManual); err != nil {
			return RunSubmission{}, storageFailure(err)
		}
	}
	if err := appsettings.SaveClock(ctx, tx, preflight.Clock); err != nil {
		return RunSubmission{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return RunSubmission{}, storageFailure(err)
	}
	if preflight.Blocked != nil {
		_ = s.emit("runs.changed", map[string]any{"task_id": taskID})
		return RunSubmission{}, blockedRunError(preflight.Blocked)
	}
	request := startRequest{TaskID: taskID, AccountID: preflight.Account.ID, Kind: SessionRun, QueuedAt: preflight.Now, Trigger: TriggerManual}
	if err := s.enqueueStart(request); err != nil {
		return RunSubmission{}, err
	}
	_ = s.emit("tasks.runtime_changed", s.taskRuntimeSnapshot(taskID))
	s.wakeScheduler()
	return RunSubmission{TaskID: taskID, Status: SubmissionQueued}, nil
}

func (s *Service) CancelRun(ctx context.Context, taskID int64) error {
	if taskID < 1 {
		return taskNotFound()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageFailure(err)
	}
	_, err = loadTaskTx(ctx, tx, taskID)
	closeErr := tx.Rollback()
	if err != nil {
		return storageFailure(err)
	}
	if closeErr != nil {
		return storageFailure(closeErr)
	}
	// 等执行者时不能占住唯一数据库连接，收尾还要用它保存结果。
	return s.cancelTask(ctx, taskID, errUserCancelled)
}

func (s *Service) recordPreflightSkipTx(ctx context.Context, tx *sql.Tx, preflight preflightResult, trigger RunTrigger) (RunRecord, error) {
	if preflight.Blocked == nil {
		return RunRecord{}, errors.New("运行跳过原因缺失")
	}
	at, taskID := preflight.Now.UnixMilli(), preflight.Task.ID
	record := RunRecord{TaskID: &taskID, TaskName: preflight.Task.Name, Trigger: trigger, StartedAt: &at, Message: &preflight.Blocked.Reason}
	if preflight.Account.ID > 0 {
		record.AccountID, record.AccountName = int64Copy(&preflight.Account.ID), &preflight.Account.Name
	}
	switch preflight.Blocked.Reason.Code {
	case "run.hourly_quota", "run.daily_search_quota", "run.quota_capacity":
		failure := FailureQuota
		record.FailureKind = &failure
	case "run.account_signed_out", "run.account_expired":
		failure := FailureLogin
		record.FailureKind = &failure
	}
	record, err := recordSkipTx(ctx, tx, record, at)
	if err != nil {
		return RunRecord{}, err
	}
	raw, err := json.Marshal(preflight.Blocked.Reason)
	if err != nil {
		return RunRecord{}, err
	}
	if trigger == TriggerAuto {
		next, err := nextRunAt(preflight.Task, preflight.Snapshot, preflight.Now, s.pacer)
		if err != nil {
			return RunRecord{}, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET last_run_at=?,last_run_outcome='skipped',last_error=?,next_run_at=? WHERE id=?`, at, string(raw), next.UnixMilli(), taskID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET last_run_at=?,last_run_outcome='skipped',last_error=? WHERE id=?`, at, string(raw), taskID)
	}
	return record, err
}

func (s *Service) tryStartRun(ctx context.Context, request startRequest) (bool, *RuntimeBlocked, error) {
	release, err := s.beginMutation()
	if err != nil {
		return false, nil, err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, nil, storageFailure(err)
	}
	defer tx.Rollback()
	task, err := loadTaskTx(ctx, tx, request.TaskID)
	if err != nil {
		return false, nil, storageFailure(err)
	}
	preflight, err := s.preflightTx(ctx, tx, task, request.Trigger)
	if err != nil {
		return false, nil, err
	}
	if err := s.saveRunPreflightTx(ctx, tx, preflight, request.Trigger); err != nil {
		return false, nil, err
	}
	if preflight.Blocked != nil {
		return false, preflight.Blocked, nil
	}
	var pageLimit int64
	if err := json.Unmarshal(preflight.Snapshot.Values["crawl.global_pages"], &pageLimit); err != nil || pageLimit < 1 {
		return false, nil, storageFailure(errors.New("全局页面上限无效"))
	}
	request.AccountID = preflight.Account.ID
	session, reserved, err := s.reserveStart(request, pageLimit, preflight.Now)
	if err != nil || !reserved {
		return false, nil, err
	}
	registered, retryReservation := false, false
	defer func() {
		if !registered {
			if retryReservation {
				s.restoreQueuedReservation(session)
			} else {
				s.releaseReservation(session.View.ID)
			}
		}
	}()
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, nil, storageFailure(err)
	}
	defer tx.Rollback()
	task, err = loadTaskTx(ctx, tx, request.TaskID)
	if err != nil {
		return false, nil, storageFailure(err)
	}
	preflight, err = s.preflightTx(ctx, tx, task, request.Trigger)
	if err != nil {
		return false, nil, err
	}
	if preflight.Blocked != nil {
		if err := s.saveRunPreflightTx(ctx, tx, preflight, request.Trigger); err != nil {
			return false, nil, err
		}
		return false, preflight.Blocked, nil
	}
	// 绑定变了就交回调度器重新取名额，不把旧账号的名额拿给新账号。
	if preflight.Account.ID != request.AccountID {
		return false, nil, nil
	}
	if err := json.Unmarshal(preflight.Snapshot.Values["crawl.global_pages"], &pageLimit); err != nil || pageLimit < 1 {
		return false, nil, storageFailure(errors.New("全局页面上限无效"))
	}
	confirmed, err := s.confirmReservation(session, pageLimit)
	if err != nil {
		return false, nil, err
	}
	if !confirmed {
		retryReservation = true
		return false, nil, nil
	}
	at, taskID := preflight.Now.UnixMilli(), task.ID
	record := RunRecord{TaskID: &taskID, TaskName: task.Name, Trigger: request.Trigger,
		AccountID: int64Copy(&preflight.Account.ID), AccountName: &preflight.Account.Name, StartedAt: &at}
	if preflight.Account.ProxyID != nil {
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM proxies WHERE id=?`, *preflight.Account.ProxyID).Scan(&name); err != nil {
			return false, nil, storageFailure(err)
		}
		record.ProxyName = &name
	}
	record, err = beginRunTx(ctx, tx, record)
	if err != nil {
		return false, nil, storageFailure(err)
	}
	var timeoutMinutes int64
	if err := json.Unmarshal(preflight.Config["run_timeout_minutes"], &timeoutMinutes); err != nil || timeoutMinutes < 1 {
		return false, nil, storageFailure(errors.New("运行超时无效"))
	}
	deadline, stopDeadline := context.WithTimeoutCause(session.Context, time.Duration(timeoutMinutes)*time.Minute, errRunTimeout)
	runContext, cancelCause := context.WithCancelCause(deadline)
	run := &runExecution{Ref: RunRef{RunID: record.ID, TaskID: task.ID, SearchGeneration: task.SearchGeneration},
		Task: task, Account: preflight.Account, Snapshot: preflight.Snapshot, Config: preflight.Config,
		SessionID: session.View.ID, Context: runContext, Done: make(chan struct{}), Record: record,
		Cancel: func(cause error) { cancelCause(cause); stopDeadline() }, Warnings: []locales.Message{}}
	defer func() {
		if !registered {
			run.Cancel(errServiceClosed)
		}
	}()
	run.Prompts, err = loadPromptSnapshotTx(ctx, tx)
	if err != nil {
		return false, nil, storageFailure(err)
	}
	run.Spec, err = s.resourceSpec(ctx, tx, preflight.Account, preflight.Snapshot, preflight.Config,
		s.newVisitBudget(run.Context, preflight.Account.ID, &run.Ref.RunID, false), s.runLogger(run))
	if err != nil {
		return false, nil, storageFailure(err)
	}
	if err := saveSessionMarkerTx(ctx, tx, preflight.Account.ID, sessionMarker{Version: 1, SessionID: session.View.ID, StartedAt: at, RunID: &run.Ref.RunID}); err != nil {
		return false, nil, storageFailure(err)
	}
	if request.Trigger == TriggerManual && preflight.Account.State == AccountCooling && preflight.Account.CooldownUntil != nil && *preflight.Account.CooldownUntil > at {
		result, err := tx.ExecContext(ctx, `UPDATE accounts SET cooldown_manual_used=1 WHERE id=? AND state='cooling' AND cooldown_manual_used=0 AND cooldown_until>?`, preflight.Account.ID, at)
		if err != nil {
			return false, nil, storageFailure(err)
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return false, nil, storageFailure(errors.New("冷却手动机会已改变"))
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET last_used_at=? WHERE id=?`, at, preflight.Account.ID); err != nil {
		return false, nil, storageFailure(err)
	}
	if err := appsettings.SaveClock(ctx, tx, preflight.Clock); err != nil {
		return false, nil, storageFailure(err)
	}
	s.mu.Lock()
	closed := s.closing || s.maintenance > 0 || s.pausedTasks[task.ID] > 0 || s.pausedAccounts[preflight.Account.ID] > 0
	s.mu.Unlock()
	if closed || session.Context.Err() != nil {
		return false, nil, sessionError(ErrorUnavailable, "runtime.closed")
	}
	if err := tx.Commit(); err != nil {
		return false, nil, storageFailure(err)
	}
	if err := s.registerRun(run, session); err != nil {
		s.completeResourceOpen(session, nil)
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		finishErr := s.finishRun(cleanup, run, errServiceClosed)
		settleErr := s.settleRunSession(cleanup, run, errServiceClosed)
		if finishErr != nil || settleErr != nil {
			s.setRuntimeError(errors.Join(finishErr, settleErr))
		}
		return false, nil, errors.Join(err, finishErr, settleErr)
	}
	registered = true
	_ = s.emit("runs.changed", map[string]any{"task_id": task.ID, "run_id": record.ID})
	go s.executeRun(run, session)
	return true, nil, nil
}

func (s *Service) saveRunPreflightTx(ctx context.Context, tx *sql.Tx, preflight preflightResult, trigger RunTrigger) error {
	userReady := preflight.NeedsUser && preflight.UserReason != nil && preflight.Blocked != nil &&
		preflight.Blocked.NextAllowedAt == nil && preflight.Blocked.Reason.Code == preflight.UserReason.Code
	skipped := preflight.Blocked != nil && (trigger == TriggerManual || userReady)
	if skipped {
		if _, err := s.recordPreflightSkipTx(ctx, tx, preflight, trigger); err != nil {
			return storageFailure(err)
		}
	}
	if err := appsettings.SaveClock(ctx, tx, preflight.Clock); err != nil {
		return storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure(err)
	}
	if skipped {
		_ = s.emit("runs.changed", map[string]any{"task_id": preflight.Task.ID})
	}
	return nil
}

func (s *Service) executeRun(run *runExecution, session *sessionWork) {
	defer s.releaseRun(run)
	var cause error
	if run.Context.Err() == nil {
		// 只在启动期间联动取消；资源建成后独立保留，验证等待不能被运行期限杀掉。
		stopOpening := context.AfterFunc(run.Context, func() { session.Cancel(context.Cause(run.Context)) })
		resource, err := s.resources.Open(session.Context, run.Spec)
		stopOpening()
		s.completeResourceOpen(session, resource)
		s.mu.Lock()
		run.Resource = resource
		if err == nil && resource != nil && !session.Closing {
			session.View.Phase = SessionRunning
			s.signalRuntimeLocked()
		}
		s.mu.Unlock()
		if err != nil {
			cause = errors.Join(errRunNetwork, err)
		} else if resource == nil || resource.Runner() == nil || resource.Chrome() == nil {
			cause = storageFailure(errors.New("运行资源没有可用页面"))
		}
	} else {
		s.completeResourceOpen(session, nil)
	}
	if cause == nil && run.Context.Err() == nil {
		for _, phase := range []func(context.Context, *runExecution) error{s.runSearch, s.runTitles, s.runDetail} {
			if cause = phase(run.Context, run); cause != nil || run.Context.Err() != nil {
				break
			}
		}
	}
	// 已识别风险必须保留；取消不能把拒绝信号覆盖成普通 cancelled。
	if stopped := context.Cause(run.Context); stopped != nil {
		cause = errors.Join(cause, stopped)
	}
	run.Cancel(cause)
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(run.Context), 30*time.Second)
	defer cancel()
	finishErr := s.finishRun(cleanup, run, cause)
	settleErr := s.settleRunSession(cleanup, run, cause)
	if finishErr != nil || settleErr != nil {
		s.setRuntimeError(errors.Join(finishErr, settleErr))
	}
}

func (s *Service) runLogger(run *runExecution) web.Logger {
	return func(event string, fields map[string]any) {
		if run.Context.Err() != nil {
			return
		}
		message, status := browserRunMessage(event, fields)
		tx, err := s.db.BeginTx(run.Context, nil)
		if err != nil {
			s.stopRunLogging(run, err)
			return
		}
		defer tx.Rollback()
		_, clock, now, err := s.observeNowTx(run.Context, tx)
		if err == nil {
			at := now.UnixMilli()
			_, err = appendRunStepTx(run.Context, tx, RunStep{RunID: run.Ref.RunID, At: &at, Status: &status, Message: &message})
		}
		if err == nil {
			err = appsettings.SaveClock(run.Context, tx, clock)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			s.stopRunLogging(run, err)
		}
	}
}

func (s *Service) stopRunLogging(run *runExecution, err error) {
	failure := storageFailure(err)
	var unavailable *ServiceError
	if errors.As(failure, &unavailable) && unavailable.Kind == ErrorUnavailable {
		s.setRuntimeError(failure)
	}
	run.Cancel(failure)
}

func browserRunMessage(event string, fields map[string]any) (locales.Message, StepStatus) {
	code := map[string]string{
		"首页已打开": "home_opened", "登录首页已打开": "home_opened", "找到搜索框": "search_input_found", "清空了搜索框": "search_input_cleared",
		"进入搜索结果页": "search_entered", "关闭了广告": "advertisement_closed", "选了发布时间": "published_selected",
		"等待": "wait", "查找页面元素": "element_lookup", "接口返回": "response", "打开了详情页": "detail_opened",
		"找到地区筛选": "region_filter_found", "找到价格筛选": "price_filter_found", "找到筛选元素": "filter_found",
		"登录框已出现": "login_box_seen", "本地登录预检警告": "login_cookie_warning",
	}[event]
	if code == "" && strings.HasSuffix(event, "没有反应，重做一次") {
		code = "retry"
	}
	status := StepOK
	if code == "login_cookie_warning" || code == "retry" {
		status = StepWarn
	}
	if found, known := fields["找到"].(bool); code == "element_lookup" && known && !found {
		status = StepWarn
	}
	if code == "" {
		code, status = "operation", StepWarn
	}
	args := map[string]any{}
	// 只保存明确的诊断值，不保存页面地址、输入文字、Cookie 或接口正文。
	for source, target := range map[string]string{"耗时毫秒": "duration_ms", "毫秒": "duration_ms", "图片数": "image_count", "有描述": "has_description", "找到": "found", "规则版本": "rule_version"} {
		if value, exists := fields[source]; exists {
			args[target] = value
		}
	}
	return locales.Message{Code: "run.browser_" + code, Args: args}, status
}
