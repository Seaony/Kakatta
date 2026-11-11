package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/eventstore"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
)

type Service struct {
	db         *sql.DB
	settings   *appsettings.Store
	cipher     *security.Cipher
	dataDir    string
	resources  ResourceFactory
	models     Models
	notifier   Notifier
	imageCache ImageCache
	publish    func(string, *locales.Message, json.RawMessage) error
	now        func() time.Time
	pacer      *pacer.Pacer
	events     eventstore.Store

	readSnapshot func(context.Context, func(*sql.Tx) error) error

	initOnce            sync.Once
	mu                  sync.Mutex
	ctx                 context.Context
	cancel              context.CancelCauseFunc
	closing             bool
	maintenance         int
	pausedTasks         map[int64]int
	pausedAccounts      map[int64]int
	wake                chan struct{}
	cleanupWake         chan struct{}
	changed             chan struct{}
	done                chan struct{}
	jobs                sync.WaitGroup
	background          sync.WaitGroup
	systemCtx           context.Context
	cancelSystem        context.CancelCauseFunc
	runs                map[int64]*runExecution
	sessions            map[string]*sessionWork
	queue               []*startRequest
	nextWakeAt          *int64
	pageLimit           int64
	runtimeErr          error
	mutations           int
	schedulerStarted    bool
	closeDone           chan struct{}
	closeErr            error
	barrier             chan struct{}
	stopParent          func() bool
	maintenanceWarnings []locales.Message
	insights            map[int64]*insightWork
	prepareDone         chan struct{}
	prepareErr          error
	activationPending   bool
}

func (e *ServiceError) Error() string { return e.Reason.Code }

type causedServiceError struct {
	service *ServiceError
	cause   error
}

func (e *causedServiceError) Error() string   { return e.service.Error() }
func (e *causedServiceError) Unwrap() []error { return []error{e.service, e.cause} }

func storageFailure(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var business *ServiceError
	if errors.As(err, &business) {
		return err
	}
	return &causedServiceError{service: &ServiceError{
		Kind: ErrorUnavailable, Reason: locales.Message{Code: "service.storage_failure", Args: map[string]any{}},
		Fields: []settings.Issue{},
	}, cause: err}
}

// 查询测试可直接构造 Service；仅初始化内存，不隐式启动调度或浏览器。
func (s *Service) initRuntime() {
	s.initOnce.Do(func() {
		if s.now == nil {
			s.now = time.Now
		}
		if s.pacer == nil {
			s.pacer = pacer.New(nil)
		}
		if s.ctx == nil {
			s.ctx, s.cancel = context.WithCancelCause(context.Background())
		}
		s.systemCtx, s.cancelSystem = context.WithCancelCause(s.ctx)
		s.wake = make(chan struct{}, 1)
		s.cleanupWake = make(chan struct{}, 1)
		s.changed = make(chan struct{})
		s.done = make(chan struct{})
		s.closeDone = make(chan struct{})
		s.barrier = make(chan struct{}, 1)
		s.barrier <- struct{}{}
		s.runs = map[int64]*runExecution{}
		s.sessions = map[string]*sessionWork{}
		s.pausedTasks = map[int64]int{}
		s.pausedAccounts = map[int64]int{}
		s.insights = map[int64]*insightWork{}
	})
}

type preflightResult struct {
	Task       Task
	Account    Account
	Snapshot   settings.Snapshot
	Config     settings.Values
	Clock      quota.ClockState
	Now        time.Time
	Blocked    *RuntimeBlocked
	NeedsUser  bool
	UserReason *locales.Message
}

func (s *Service) emit(kind string, data any) error {
	s.mu.Lock()
	pending := s.activationPending
	s.mu.Unlock()
	if pending || s.publish == nil {
		return nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.publish(kind, nil, raw)
}

func (s *Service) wakeScheduler() {
	s.initRuntime()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) signalRuntimeLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func New(ctx context.Context, deps Dependencies) (*Service, error) {
	s, err := NewStopped(ctx, deps)
	if err != nil {
		return nil, err
	}
	if err = s.Prepare(ctx); err == nil {
		err = s.Start()
	}
	if err != nil {
		return nil, errors.Join(err, s.Close())
	}
	return s, nil
}

// NewStopped 只构造内存；恢复流程换入隔离目录后才调用 Prepare。
func NewStopped(ctx context.Context, deps Dependencies) (*Service, error) {
	if deps.DB == nil || deps.Settings == nil || deps.Cipher == nil || strings.TrimSpace(deps.DataDir) == "" || missingRuntimeDependency(deps.Resources) || missingRuntimeDependency(deps.Models) || missingRuntimeDependency(deps.Notifier) || missingRuntimeDependency(deps.ImageCache) || deps.Publish == nil {
		return nil, errors.New("业务服务依赖不完整")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := &Service{db: deps.DB, settings: deps.Settings, cipher: deps.Cipher, dataDir: deps.DataDir, resources: deps.Resources, models: deps.Models, notifier: deps.Notifier, imageCache: deps.ImageCache, publish: deps.Publish, now: deps.Now, pacer: deps.Pacer, readSnapshot: deps.ReadSnapshot}
	s.ctx, s.cancel = context.WithCancelCause(ctx)
	s.initRuntime()
	s.events = eventstore.Store{DB: s.db, Now: s.now}
	s.maintenance = 1
	s.activationPending = true
	return s, nil
}

// Prepare 只做一次同步本地准备，成功后仍不接纳运行或发布事件。
func (s *Service) Prepare(ctx context.Context) error {
	s.initRuntime()
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return errServiceClosed
	}
	if s.ctx.Err() != nil {
		err := errors.Join(s.ctx.Err(), context.Cause(s.ctx))
		s.mu.Unlock()
		return err
	}
	if done := s.prepareDone; done != nil {
		select {
		case <-done:
			err := s.prepareErr
			s.mu.Unlock()
			return err
		default:
		}
		s.mu.Unlock()
		select {
		case <-done:
			s.mu.Lock()
			err := s.prepareErr
			s.mu.Unlock()
			return err
		case <-ctx.Done():
			return errors.Join(ctx.Err(), context.Cause(ctx))
		}
	}
	done := make(chan struct{})
	s.prepareDone = done
	s.mu.Unlock()
	work, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(s.ctx, func() { cancel(context.Cause(s.ctx)) })
	err := s.prepareLocal(work)
	if work.Err() != nil {
		err = errors.Join(err, work.Err(), context.Cause(work))
	}
	if s.ctx.Err() != nil {
		err = errors.Join(err, s.ctx.Err(), context.Cause(s.ctx))
	}
	stop()
	cancel(nil)
	err = storageFailure(err)
	s.mu.Lock()
	if s.closing && err == nil {
		err = errServiceClosed
	}
	s.prepareErr = err
	close(done)
	s.mu.Unlock()
	return err
}

func (s *Service) prepareLocal(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	snapshot, clock, now, err := s.observeNowTx(ctx, tx)
	if err == nil {
		err = recoverRuntimeTx(ctx, tx, snapshot.Values, now.UnixMilli())
	}
	if err == nil {
		var raw string
		err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='stats.started_at'`).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('stats.started_at',?)`, strconv.FormatInt(now.UnixMilli(), 10))
		} else if err == nil {
			at, e := strconv.ParseInt(raw, 10, 64)
			if e != nil || at <= 0 || strconv.FormatInt(at, 10) != raw {
				err = errors.New("统计开始时间损坏")
			}
		}
	}
	if err == nil {
		err = appsettings.SaveClock(ctx, tx, clock)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err == nil {
		var limit int64
		err = json.Unmarshal(snapshot.Values["crawl.global_pages"], &limit)
		if err == nil {
			s.mu.Lock()
			s.pageLimit = limit
			s.mu.Unlock()
		}
	}
	if err == nil {
		err = s.retryAccountCleanup(ctx)
	}
	if err == nil {
		var result CleanupResult
		result, err = s.Cleanup(ctx)
		s.recordMaintenanceWarnings(result.Warnings)
	}
	return err
}

// Start 不做本地准备，只在提交恢复后开放门禁并启动原有循环。
func (s *Service) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errServiceClosed
	}
	if s.ctx.Err() != nil {
		return errors.Join(s.ctx.Err(), context.Cause(s.ctx))
	}
	if s.schedulerStarted {
		return nil
	}
	if s.prepareDone == nil {
		return errors.New("业务服务尚未准备")
	}
	select {
	case <-s.prepareDone:
	default:
		return errors.New("业务服务尚未完成准备")
	}
	if s.prepareErr != nil {
		return s.prepareErr
	}
	if s.runtimeErr != nil {
		return s.runtimeErr
	}
	s.activationPending = false
	s.maintenance--
	s.schedulerStarted = true
	s.background.Add(1)
	go func() { defer s.background.Done(); s.cleanupLoop(s.ctx) }()
	go func() { defer close(s.done); s.schedulerLoop(s.ctx) }()
	s.stopParent = context.AfterFunc(s.ctx, func() { _ = s.Close() })
	s.signalRuntimeLocked()
	return nil
}

func missingRuntimeDependency(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Pointer, reflect.Func, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan:
		return v.IsNil()
	}
	return false
}

func (s *Service) Close() error {
	s.initRuntime()
	s.mu.Lock()
	if s.closing {
		done := s.closeDone
		s.mu.Unlock()
		<-done
		return s.closeErr
	}
	s.closing = true
	if s.stopParent != nil {
		s.stopParent()
	}
	preparing := s.prepareDone
	s.signalRuntimeLocked()
	s.mu.Unlock()
	s.cancel(errServiceClosed)
	if preparing != nil {
		<-preparing
	}
	_ = s.waitRuntime(context.Background(), func() bool { return s.mutations == 0 })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	resume, err := s.quiesceScope(ctx, 0, 0, 0, errServiceClosed)
	if resume != nil {
		resume()
	}
	cancel()
	// 已接纳的执行者负责收尾；不能因关闭请求取消而提前放出名额。
	s.jobs.Wait()
	s.background.Wait()
	s.mu.Lock()
	started := s.schedulerStarted
	s.mu.Unlock()
	if started {
		<-s.done
	}
	s.mu.Lock()
	s.closeErr = err
	close(s.closeDone)
	s.mu.Unlock()
	return err
}

func (s *Service) cleanupLoop(ctx context.Context) {
	last := s.now()
	for ctx.Err() == nil {
		snapshot, err := s.settings.Get(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.setRuntimeError(storageFailure(err))
			}
			return
		}
		var hours int64
		if err = json.Unmarshal(snapshot.Values["cleanup.interval_hours"], &hours); err != nil || hours < 1 {
			if err == nil {
				err = errors.New("清理间隔无效")
			}
			s.setRuntimeError(storageFailure(err))
			return
		}
		wait := last.Add(time.Duration(hours) * time.Hour).Sub(s.now())
		if wait > 0 {
			if err := s.waitCleanup(ctx, wait); err != nil {
				if ctx.Err() == nil {
					s.setRuntimeError(storageFailure(err))
				}
				return
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.cleanupWake:
			continue
		default:
		}
		result, cleanupErr := s.Cleanup(ctx)
		s.recordMaintenanceWarnings(result.Warnings)
		if err = cleanupErr; err != nil {
			if ctx.Err() == nil {
				s.setRuntimeError(err)
			}
			return
		}
		last = s.now()
	}
}

func (s *Service) waitCleanup(ctx context.Context, delay time.Duration) error {
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.pacer.Wait(waitCtx, pacer.Between(delay, delay)) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		cancel()
		<-done
		return ctx.Err()
	case <-s.cleanupWake:
		cancel()
		err := <-done
		if errors.Is(err, context.Canceled) {
			return ctx.Err()
		}
		return err
	}
}

func (s *Service) advanceDeadlines(ctx context.Context) (*time.Time, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageFailure(err)
	}
	defer tx.Rollback()
	snapshot, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return nil, storageFailure(err)
	}
	ids, err := advanceAccountStatesTx(ctx, tx, now.UnixMilli())
	if err != nil {
		return nil, storageFailure(err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM tasks WHERE failure_paused_until IS NOT NULL AND failure_paused_until<=?`, now.UnixMilli())
	if err != nil {
		return nil, storageFailure(err)
	}
	var tasks []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		tasks = append(tasks, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, storageFailure(err)
	}
	for _, id := range tasks {
		// 清掉暂停前保留真正可运行的时间，避免旧计划恢复后插队。
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET
			next_run_at=CASE WHEN next_run_at IS NULL OR next_run_at<failure_paused_until THEN failure_paused_until ELSE next_run_at END,
			failure_paused_until=NULL WHERE id=?`, id); err != nil {
			return nil, storageFailure(err)
		}
		if _, err = s.events.ResolveTx(ctx, tx, "task_failure_pause", id); err != nil {
			return nil, storageFailure(err)
		}
	}
	budgetEvent, budgetChanged, budgetWake, err := s.refreshAIBudgetTx(ctx, tx, snapshot, clock, now)
	if err != nil {
		return nil, storageFailure(err)
	}
	var next sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT min(at) FROM (SELECT cooldown_until AS at FROM accounts WHERE state IN ('cooling','denied') AND cooldown_until>? UNION ALL SELECT failure_paused_until FROM tasks WHERE failure_paused_until>?)`, now.UnixMilli(), now.UnixMilli()).Scan(&next)
	if err == nil {
		err = appsettings.SaveClock(ctx, tx, clock)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return nil, storageFailure(err)
	}
	for _, id := range ids {
		_ = s.emit("account.changed", map[string]any{"account_id": id})
	}
	for _, id := range tasks {
		_ = s.emit("task.changed", map[string]any{"task_id": id})
	}
	if len(ids) > 0 {
		_ = s.emit("task.changed", map[string]any{"all": true})
	}
	if budgetChanged {
		_ = s.emit("system_event.changed", map[string]any{"all": true})
	}
	_ = budgetEvent
	s.retrySystemEventsAsync(now.UnixMilli())
	if !next.Valid {
		return budgetWake, nil
	}
	at := time.UnixMilli(next.Int64).UTC()
	if budgetWake != nil && budgetWake.Before(at) {
		return budgetWake, nil
	}
	return &at, nil
}

func (s *Service) retrySystemEventsAsync(nowMS int64) {
	s.initRuntime()
	s.mu.Lock()
	if s.closing || s.maintenance > 0 || s.runtimeErr != nil {
		s.mu.Unlock()
		return
	}
	ctx := s.systemCtx
	s.jobs.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.jobs.Done()
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM system_events WHERE resolved_at IS NULL AND notified_at IS NULL AND failure_count<3 AND (first_failed_at IS NULL OR first_failed_at>=?) ORDER BY id`, nowMS-int64(24*time.Hour/time.Millisecond))
		if err != nil {
			if ctx.Err() == nil {
				s.setRuntimeError(storageFailure(err))
			}
			return
		}
		ids := []int64{}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			if ctx.Err() == nil {
				s.setRuntimeError(storageFailure(err))
			}
			return
		}
		for _, id := range ids {
			s.mu.Lock()
			if s.closing || s.maintenance > 0 || ctx.Err() != nil {
				s.mu.Unlock()
				return
			}
			s.jobs.Add(1)
			s.mu.Unlock()
			go func(id int64) {
				defer s.jobs.Done()
				if missingRuntimeDependency(s.notifier) {
					s.setRuntimeError(storageFailure(errors.New("通知服务缺失")))
					return
				}
				if err := s.notifier.System(ctx, id); err != nil && ctx.Err() == nil {
					s.setRuntimeError(storageFailure(err))
				}
			}(id)
		}
	}()
}

func (s *Service) refreshAIBudgetTx(ctx context.Context, tx *sql.Tx, snapshot settings.Snapshot, clock quota.ClockState, now time.Time) (int64, bool, *time.Time, error) {
	var limit int64
	if err := json.Unmarshal(snapshot.Values["ai.daily_token_limit"], &limit); err != nil || limit < 0 {
		return 0, false, nil, errors.New("模型每日上限无效")
	}
	events := s.events
	events.Now = func() time.Time { return now }
	var used int64
	if limit > 0 {
		start, err := clock.DayStart()
		if err != nil {
			return 0, false, nil, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(COALESCE(input_tokens,0)+COALESCE(output_tokens,0)),0) FROM ai_calls WHERE at>=?`, start.UnixMilli()).Scan(&used); err != nil {
			return 0, false, nil, err
		}
		if used < 0 {
			return 0, false, nil, errors.New("模型用量记录无效")
		}
	}
	if limit == 0 || used < limit {
		changed, err := events.ResolveTx(ctx, tx, "ai_budget", 0)
		return 0, changed, nil, err
	}
	created, err := events.RaiseTx(ctx, tx, "ai_budget", 0, locales.Message{Code: "run.ai_daily_limit", Args: map[string]any{"tokens": used}})
	if err != nil {
		return 0, false, nil, err
	}
	var id int64
	if created {
		if err = tx.QueryRowContext(ctx, `SELECT id FROM system_events WHERE kind='ai_budget' AND subject_id=0 AND resolved_at IS NULL`).Scan(&id); err != nil {
			return 0, false, nil, err
		}
	}
	blocked, err := aiRunBudgetTx(ctx, tx, snapshot, clock)
	if err != nil {
		return 0, false, nil, err
	}
	var wake *time.Time
	if blocked != nil && blocked.NextAllowedAt != nil {
		at := time.UnixMilli(*blocked.NextAllowedAt).UTC()
		wake = &at
	}
	return id, created, wake, nil
}

func (s *Service) recordMaintenanceWarnings(warnings []locales.Message) {
	s.initRuntime()
	copy := make([]locales.Message, len(warnings))
	for i, message := range warnings {
		copy[i] = message
		copy[i].Args = map[string]any{}
		for key, value := range message.Args {
			copy[i].Args[key] = value
		}
	}
	s.mu.Lock()
	changed := !reflect.DeepEqual(s.maintenanceWarnings, copy)
	s.maintenanceWarnings = copy
	s.mu.Unlock()
	if changed {
		for _, warning := range copy {
			slog.Warn("图片缓存清理未完成", "code", warning.Code)
		}
		_ = s.emit("maintenance.changed", map[string]any{"warnings": copy})
	}
}

func (s *Service) maintenanceWarningsSnapshot() []locales.Message {
	s.initRuntime()
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]locales.Message, len(s.maintenanceWarnings))
	for i, message := range s.maintenanceWarnings {
		result[i] = message
		result[i].Args = map[string]any{}
		for key, value := range message.Args {
			result[i].Args[key] = value
		}
	}
	return result
}
