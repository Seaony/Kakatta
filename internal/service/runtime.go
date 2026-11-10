package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/prompts"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/remote"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

var (
	errUserCancelled = errors.New("run.user_cancelled")
	errSearchChanged = errors.New("run.search_changed")
	errTaskDeleted   = errors.New("run.task_deleted")
	errServiceClosed = errors.New("run.service_closed")
	errRunTimeout    = errors.New("run.timeout")
	errRunNetwork    = errors.New("run.network")
)

type runExecution struct {
	Ref                RunRef
	Task               Task
	Account            Account
	Snapshot           settings.Snapshot
	Config             settings.Values
	Prompts            prompts.Snapshot
	SessionID          string
	Context            context.Context
	Cancel             context.CancelCauseFunc
	Done               chan struct{}
	Resource           SessionResource
	Spec               ResourceSpec
	CachedOnly         bool
	Backlog            []Finding
	SearchSaved        SearchSaved
	P                  []WorkRef
	FirstConclusionIDs []int64
	Record             RunRecord
	PageCount          int64
	FirstPageCount     int64
	DetailCount        int64
	SellerPageCount    int64
	Layer2Count        int64
	Layer2Rejected     int64
	AnalyzedCount      int64
	RecommendedCount   int64
	Warnings           []locales.Message
	LayoutError        bool
	finishMu           sync.Mutex
	releaseOnce        sync.Once
}

type sessionWork struct {
	opMu          sync.Mutex
	protectionMu  sync.Mutex
	View          SessionView
	Resource      SessionResource
	Remote        *remote.Session
	OpenDone      chan struct{}
	openOnce      sync.Once
	Context       context.Context
	Cancel        context.CancelCauseFunc
	HoldDeadline  time.Time
	FirstViewedAt time.Time
	Deadline      time.Time
	Timer         *time.Timer
	Closing       bool
	Closed        chan struct{}
	CloseErr      error
	ProtectionErr error
	closeOnce     sync.Once
}

type startRequest struct {
	TaskID    int64
	AccountID int64
	Kind      SessionKind
	SessionID string
	QueuedAt  time.Time
	Trigger   RunTrigger
}

type insightWork struct {
	AccountID int64
	Context   context.Context
	Cancel    context.CancelCauseFunc
	Done      chan struct{}
}

func (s *Service) claimInsight(taskID, accountID int64) (context.Context, func(), bool) {
	s.initRuntime()
	s.mu.Lock()
	if s.closing || s.maintenance > 0 || s.runtimeErr != nil || s.pausedTasks[taskID] > 0 || s.pausedAccounts[accountID] > 0 || s.insights[taskID] != nil {
		s.mu.Unlock()
		return nil, nil, false
	}
	ctx, cancel := context.WithCancelCause(s.ctx)
	job := &insightWork{AccountID: accountID, Context: ctx, Cancel: cancel, Done: make(chan struct{})}
	s.insights[taskID] = job
	s.jobs.Add(1)
	s.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			cancel(nil)
			s.mu.Lock()
			if s.insights[taskID] == job {
				delete(s.insights, taskID)
			}
			close(job.Done)
			s.mu.Unlock()
			s.jobs.Done()
		})
	}
	return ctx, release, true
}

func (s *Service) taskRuntimeSnapshot(taskID int64) TaskRuntime {
	s.initRuntime()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taskRuntimeSnapshotLocked(taskID)
}

func (s *Service) taskRuntimeSnapshotLocked(taskID int64) TaskRuntime {
	view := TaskRuntime{TaskID: taskID, Phase: TaskIdle}
	if run := s.runs[taskID]; run != nil {
		view.Phase = TaskRunning
		if run.Ref.RunID > 0 {
			view.RunID = int64Copy(&run.Ref.RunID)
		}
		if run.Account.ID > 0 {
			view.AccountID = int64Copy(&run.Account.ID)
		}
		if session := s.sessions[run.SessionID]; session != nil {
			view.StartedAt = int64Copy(session.View.StartedAt)
		}
		return view
	}
	for _, request := range s.queue {
		if request.TaskID == taskID && request.Kind == SessionRun {
			at := request.QueuedAt.UnixMilli()
			view.Phase, view.QueuedAt = TaskQueued, &at
			view.AccountID = int64Copy(&request.AccountID)
			break
		}
	}
	return view
}

func int64Copy(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copySessionView(view SessionView) SessionView {
	view.RunID, view.TaskID = int64Copy(view.RunID), int64Copy(view.TaskID)
	view.QueuedAt, view.StartedAt, view.ExpiresAt = int64Copy(view.QueuedAt), int64Copy(view.StartedAt), int64Copy(view.ExpiresAt)
	if view.RemotePath != nil {
		path := *view.RemotePath
		view.RemotePath = &path
	}
	if view.Viewport != nil {
		viewport := *view.Viewport
		view.Viewport = &viewport
	}
	return view
}

func (s *Service) sessionSnapshot(accountID int64) *SessionView {
	s.initRuntime()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, session := range s.sessions {
		if session.View.AccountID == accountID {
			view := copySessionView(session.View)
			return &view
		}
	}
	return nil
}

func (s *Service) runtimeStatusSnapshot() (SchedulerStatus, BrowserStatus) {
	s.initRuntime()
	s.mu.Lock()
	defer s.mu.Unlock()
	scheduler := SchedulerStatus{Running: s.schedulerStarted && !s.closing && s.runtimeErr == nil, Accepting: !s.closing && s.maintenance == 0 && s.runtimeErr == nil,
		NextWakeAt: int64Copy(s.nextWakeAt), QueuedCount: int64(len(s.queue)), RunningCount: int64(len(s.runs))}
	browsers := BrowserStatus{Limit: s.pageLimit, Sessions: []SessionView{}}
	for _, session := range s.sessions {
		view := copySessionView(session.View)
		browsers.Sessions = append(browsers.Sessions, view)
		if view.Phase != SessionQueued {
			browsers.Active++
		}
	}
	sort.Slice(browsers.Sessions, func(i, j int) bool { return browsers.Sessions[i].ID < browsers.Sessions[j].ID })
	return scheduler, browsers
}

type sessionMarker struct {
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
	StartedAt int64  `json:"started_at"`
	RunID     *int64 `json:"run_id"`
}

func encodeSessionMarker(marker sessionMarker) (string, error) {
	if marker.Version != 1 || marker.SessionID == "" || marker.StartedAt <= 0 || marker.RunID != nil && *marker.RunID <= 0 {
		return "", errors.New("运行恢复标记无效")
	}
	raw, err := json.Marshal(marker)
	return string(raw), err
}

func decodeSessionMarker(raw string) (sessionMarker, error) {
	var marker sessionMarker
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return marker, errors.New("运行恢复标记损坏")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return marker, errors.New("运行恢复标记损坏")
	}
	_, err := encodeSessionMarker(marker)
	return marker, err
}

func saveSessionMarkerTx(ctx context.Context, tx *sql.Tx, accountID int64, marker sessionMarker) error {
	if accountID < 1 || accountID > 12767 {
		return errors.New("运行账号编号无效")
	}
	raw, err := encodeSessionMarker(marker)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?)`, "runtime.session."+strconv.FormatInt(accountID, 10), raw)
	return err
}

func listSessionMarkersTx(ctx context.Context, tx *sql.Tx) (map[int64]sessionMarker, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key,value FROM meta WHERE key LIKE 'runtime.session.%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	markers := map[int64]sessionMarker{}
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		suffix := strings.TrimPrefix(key, "runtime.session.")
		id, err := strconv.ParseInt(suffix, 10, 64)
		if err != nil || id < 1 || id > 12767 || strconv.FormatInt(id, 10) != suffix {
			return nil, errors.New("运行恢复标记账号无效")
		}
		marker, err := decodeSessionMarker(raw)
		if err != nil {
			return nil, fmt.Errorf("账号%d：%w", id, err)
		}
		markers[id] = marker
	}
	return markers, rows.Err()
}

func deleteSessionMarkerTx(ctx context.Context, tx *sql.Tx, accountID int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key=?`, "runtime.session."+strconv.FormatInt(accountID, 10))
	return err
}

func (s *Service) enqueueStart(request startRequest) error {
	s.initRuntime()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.maintenance > 0 || s.runtimeErr != nil || s.pausedTasks[request.TaskID] > 0 || s.pausedAccounts[request.AccountID] > 0 {
		return &ServiceError{Kind: ErrorUnavailable, Reason: locales.Message{Code: "runtime.closed", Args: map[string]any{}}}
	}
	if request.Kind == SessionRun && s.runs[request.TaskID] != nil {
		return &ServiceError{Kind: ErrorConflict, Reason: locales.Message{Code: "run.already_requested", Args: map[string]any{}}}
	}
	for _, queued := range s.queue {
		if request.Kind == SessionRun && queued.Kind == SessionRun && queued.TaskID == request.TaskID || request.Kind != SessionRun && queued.AccountID == request.AccountID {
			return &ServiceError{Kind: ErrorConflict, Reason: locales.Message{Code: "run.already_requested", Args: map[string]any{}}}
		}
	}
	copy := request
	if copy.SessionID == "" {
		copy.SessionID = rand.Text()
	}
	if copy.QueuedAt.IsZero() {
		copy.QueuedAt = s.now()
	}
	s.queue = append(s.queue, &copy)
	s.signalRuntimeLocked()
	return nil
}

func (s *Service) queuedStarts() []startRequest {
	s.initRuntime()
	s.mu.Lock()
	defer s.mu.Unlock()
	queue := make([]startRequest, len(s.queue))
	for i, request := range s.queue {
		queue[i] = *request
	}
	return queue
}

func (s *Service) removeQueued(request startRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, queued := range s.queue {
		if queued.Kind == request.Kind && queued.TaskID == request.TaskID && queued.SessionID == request.SessionID && queued.QueuedAt.Equal(request.QueuedAt) {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			s.signalRuntimeLocked()
			return
		}
	}
}

func (s *Service) setNextWake(at *int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextWakeAt = int64Copy(at)
}

func (s *Service) setRuntimeError(err error) {
	s.initRuntime()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtimeErr = err
	s.signalRuntimeLocked()
}

func (s *Service) reserveStart(request startRequest, limit int64, now time.Time) (*sessionWork, bool, error) {
	s.initRuntime()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.maintenance > 0 || s.runtimeErr != nil {
		return nil, false, &ServiceError{Kind: ErrorUnavailable, Reason: locales.Message{Code: "runtime.closed", Args: map[string]any{}}}
	}
	if s.pausedTasks[request.TaskID] > 0 || s.pausedAccounts[request.AccountID] > 0 {
		return nil, false, nil
	}
	if request.SessionID != "" {
		current := false
		for _, queued := range s.queue {
			if queued.SessionID == request.SessionID && queued.QueuedAt.Equal(request.QueuedAt) {
				current = true
				break
			}
		}
		if !current {
			return nil, false, &ServiceError{Kind: ErrorConflict, Reason: locales.Message{Code: "runtime.session_changed", Args: map[string]any{}}}
		}
	}
	var occupied int64
	if request.TaskID > 0 && s.runs[request.TaskID] != nil {
		return nil, false, nil
	}
	for _, session := range s.sessions {
		if session.View.Phase == SessionQueued {
			continue
		}
		if session.View.AccountID == request.AccountID {
			return nil, false, nil
		}
		occupied++
	}
	if occupied >= limit {
		return nil, false, nil
	}
	id := request.SessionID
	if id == "" {
		id = rand.Text()
	}
	session := s.sessions[id]
	if session == nil {
		ctx, cancel := context.WithCancelCause(s.ctx)
		session = &sessionWork{Context: ctx, Cancel: cancel, Closed: make(chan struct{}), OpenDone: make(chan struct{}), View: SessionView{ID: id, AccountID: request.AccountID, Kind: request.Kind}}
		s.sessions[id] = session
	}
	at := now.UnixMilli()
	session.View.Phase, session.View.StartedAt = SessionOpening, &at
	if request.TaskID > 0 {
		session.View.TaskID = int64Copy(&request.TaskID)
	}
	s.pageLimit = limit
	s.signalRuntimeLocked()
	return session, true, nil
}

func (s *Service) releaseReservation(sessionID string) {
	s.mu.Lock()
	session := s.sessions[sessionID]
	if session != nil && session.Resource == nil {
		delete(s.sessions, sessionID)
		session.Cancel(errServiceClosed)
		session.openOnce.Do(func() {
			if session.OpenDone != nil {
				close(session.OpenDone)
			}
		})
		session.closeOnce.Do(func() { close(session.Closed) })
		s.signalRuntimeLocked()
	}
	queue := s.queue[:0]
	for _, request := range s.queue {
		if request.SessionID != sessionID {
			queue = append(queue, request)
		}
	}
	s.queue = queue
	s.mu.Unlock()
}

func (s *Service) completeResourceOpen(session *sessionWork, resource SessionResource) {
	s.mu.Lock()
	session.Resource = resource
	s.mu.Unlock()
	session.openOnce.Do(func() {
		if session.OpenDone != nil {
			close(session.OpenDone)
		}
	})
}

// 两次事务之间可能收紧上限，真正开始前按最新值重查预留。
func (s *Service) confirmReservation(session *sessionWork, limit int64) (bool, error) {
	if limit < 1 {
		return false, storageFailure(errors.New("全局页面上限无效"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.runtimeErr != nil {
		return false, &ServiceError{Kind: ErrorUnavailable, Reason: locales.Message{Code: "runtime.closed", Args: map[string]any{}}}
	}
	if s.sessions[session.View.ID] != session || session.Closing || session.View.Phase != SessionOpening {
		return false, &ServiceError{Kind: ErrorConflict, Reason: locales.Message{Code: "runtime.session_changed", Args: map[string]any{}}}
	}
	var occupied int64
	for _, current := range s.sessions {
		if current.View.Phase != SessionQueued {
			occupied++
		}
	}
	s.pageLimit = limit
	return occupied <= limit, nil
}

func (s *Service) restoreQueuedReservation(session *sessionWork) {
	s.mu.Lock()
	queued := false
	for _, request := range s.queue {
		if request.SessionID == session.View.ID {
			queued = true
			break
		}
	}
	if queued && s.sessions[session.View.ID] == session && session.Resource == nil && !session.Closing {
		session.View.Phase, session.View.StartedAt = SessionQueued, nil
		s.signalRuntimeLocked()
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.releaseReservation(session.View.ID)
}

func (s *Service) registerRun(run *runExecution, session *sessionWork) error {
	var limit int64
	if err := json.Unmarshal(run.Snapshot.Values["crawl.global_pages"], &limit); err != nil || limit < 1 {
		return storageFailure(errors.New("全局页面上限无效"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.runtimeErr != nil {
		return &ServiceError{Kind: ErrorUnavailable, Reason: locales.Message{Code: "runtime.closed", Args: map[string]any{}}}
	}
	if s.runs[run.Ref.TaskID] != nil {
		return errors.New("任务已经运行")
	}
	var occupied int64
	for _, work := range s.sessions {
		if work.View.Phase != SessionQueued {
			occupied++
		}
	}
	if occupied > limit {
		return &ServiceError{Kind: ErrorConflict, Reason: locales.Message{Code: "runtime.pages_busy", Args: map[string]any{}}}
	}
	s.pageLimit = limit
	run.SessionID = session.View.ID
	if run.Done == nil {
		run.Done = make(chan struct{})
	}
	session.View.RunID = int64Copy(&run.Ref.RunID)
	s.runs[run.Ref.TaskID] = run
	s.jobs.Add(1)
	s.signalRuntimeLocked()
	return nil
}

func (s *Service) releaseRun(run *runExecution) {
	run.releaseOnce.Do(func() {
		s.mu.Lock()
		if s.runs[run.Ref.TaskID] == run {
			delete(s.runs, run.Ref.TaskID)
		}
		close(run.Done)
		s.signalRuntimeLocked()
		s.mu.Unlock()
		s.jobs.Done()
	})
}

func (s *Service) setSessionPhase(id string, phase SessionPhase, deadline *time.Time) error {
	s.mu.Lock()
	session := s.sessions[id]
	if session == nil || session.Closing {
		s.mu.Unlock()
		return &ServiceError{Kind: ErrorConflict, Reason: locales.Message{Code: "runtime.session_changed", Args: map[string]any{}}}
	}
	session.View.Phase = phase
	if deadline != nil {
		session.Deadline = *deadline
		ms := deadline.UnixMilli()
		session.View.ExpiresAt = &ms
		if session.Timer != nil {
			session.Timer.Stop()
		}
		expected := *deadline
		session.Timer = time.AfterFunc(max(expected.Sub(s.now()), 0), func() {
			s.mu.Lock()
			current := s.sessions[id]
			due := current == session && session.Deadline.Equal(expected) && !session.Closing
			if due {
				session.Closing = true
			}
			s.mu.Unlock()
			if due {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				_ = s.closeSessionWork(ctx, session)
			}
		})
	}
	s.signalRuntimeLocked()
	s.mu.Unlock()
	return nil
}

func (s *Service) closeSessionWork(ctx context.Context, session *sessionWork) error {
	waitCtx := ctx
	session.closeOnce.Do(func() {
		// 关闭已开始，写回保护不能随网页请求取消。
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		ctx = cleanup
		s.mu.Lock()
		session.Closing = true
		session.View.Phase = SessionClosing
		if session.Timer != nil {
			session.Timer.Stop()
		}
		s.signalRuntimeLocked()
		s.mu.Unlock()
		session.Cancel(errServiceClosed)
		if session.View.StartedAt == nil {
			session.openOnce.Do(func() {
				if session.OpenDone != nil {
					close(session.OpenDone)
				}
			})
		}
		if session.OpenDone != nil {
			<-session.OpenDone
		}
		s.mu.Lock()
		resource := session.Resource
		s.mu.Unlock()
		if resource != nil {
			session.CloseErr = resource.Close()
		}
		session.protectionMu.Lock()
		defer session.protectionMu.Unlock()
		s.mu.Lock()
		session.CloseErr = errors.Join(session.CloseErr, session.ProtectionErr)
		s.mu.Unlock()
		if session.CloseErr == nil && session.View.StartedAt != nil {
			tx, err := s.db.BeginTx(ctx, nil)
			if err == nil {
				defer tx.Rollback()
				var present bool
				err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meta WHERE key=?)`, "runtime.session."+strconv.FormatInt(session.View.AccountID, 10)).Scan(&present)
				if err == nil && !present && resource != nil {
					err = errors.New("运行恢复标记缺失")
				}
				if err == nil && present {
					var snapshot settings.Snapshot
					var now time.Time
					snapshot, clock, observed, e := s.observeNowTx(ctx, tx)
					err = e
					now = observed
					if err == nil {
						var minimum, jitter int64
						err = json.Unmarshal(snapshot.Values["crawl.account_min_gap_seconds"], &minimum)
						if err == nil {
							err = json.Unmarshal(snapshot.Values["crawl.account_min_gap_jitter_seconds"], &jitter)
						}
						if err == nil {
							gap := int64(s.pacer.Pick(pacer.Between(time.Duration(minimum)*time.Second, time.Duration(minimum+jitter)*time.Second)) / time.Second)
							_, err = tx.ExecContext(ctx, `UPDATE accounts SET last_run_ended_at=?,next_gap_seconds=? WHERE id=?`, now.UnixMilli(), gap, session.View.AccountID)
						}
						if err == nil {
							err = deleteSessionMarkerTx(ctx, tx, session.View.AccountID)
						}
						if err == nil {
							err = appsettings.SaveClock(ctx, tx, clock)
						}
					}
				}
				if err == nil {
					err = tx.Commit()
				}
			}
			session.CloseErr = storageFailure(err)
		}
		s.mu.Lock()
		if session.CloseErr == nil {
			if s.sessions[session.View.ID] == session {
				delete(s.sessions, session.View.ID)
			}
			queue := s.queue[:0]
			for _, request := range s.queue {
				if request.SessionID != session.View.ID {
					queue = append(queue, request)
				}
			}
			s.queue = queue
		} else {
			s.runtimeErr = session.CloseErr
		}
		close(session.Closed)
		s.signalRuntimeLocked()
		s.mu.Unlock()
	})
	select {
	case <-session.Closed:
		return session.CloseErr
	case <-waitCtx.Done():
		return waitCtx.Err()
	}
}

func (s *Service) settleRunSession(ctx context.Context, run *runExecution, cause error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	ctx = cleanup
	s.mu.Lock()
	session := s.sessions[run.SessionID]
	s.mu.Unlock()
	if session == nil {
		return nil
	}
	if web.IsStop(cause) {
		var readURL func(context.Context) (string, error)
		if run.Resource != nil && run.Resource.Chrome() != nil {
			readURL = run.Resource.Chrome().URLContext
		}
		ids, err := s.persistSessionRisk(session, cause, readURL)
		if err != nil {
			_ = s.closeSessionWork(ctx, session)
			return err
		}
		if errors.Is(cause, web.ErrNeedVerify) {
			s.mu.Lock()
			allowed := !s.closing && s.maintenance == 0
			s.mu.Unlock()
			if allowed {
				if err := s.holdVerification(run.SessionID, cause); err != nil {
					return errors.Join(err, s.closeSessionWork(ctx, session))
				}
				return s.dispatchSystemEvents(ctx, ids)
			}
		}
		return errors.Join(s.closeSessionWork(ctx, session), s.dispatchSystemEvents(ctx, ids))
	}
	return s.closeSessionWork(ctx, session)
}

// 网址只辅助人工接手，读取失败不能占用保存风控状态的时间。
func (s *Service) persistSessionRisk(session *sessionWork, risk error, readURL func(context.Context) (string, error)) ([]int64, error) {
	session.protectionMu.Lock()
	defer session.protectionMu.Unlock()
	target := ""
	if readURL != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if url, err := readURL(ctx); err == nil {
			target = url
		}
		cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fail := func(err error) ([]int64, error) {
		protected := &causedServiceError{service: &ServiceError{Kind: ErrorUnavailable, Reason: locales.Message{Code: "service.storage_failure", Args: map[string]any{}}, Fields: []settings.Issue{}}, cause: err}
		s.mu.Lock()
		session.ProtectionErr = protected
		s.mu.Unlock()
		s.setRuntimeError(protected)
		return nil, protected
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	before, err := accountEventIDsTx(ctx, tx, session.View.AccountID)
	var after []int64
	if err == nil {
		snapshot, clock, now, observeErr := s.observeNowTx(ctx, tx)
		err = observeErr
		if err == nil {
			var account Account
			account, err = applyAccountRiskTx(ctx, tx, session.View.AccountID, risk, target, snapshot.Values, now.UnixMilli())
			// 已生效的拒绝不能被晚到的验证或登录信号替换。
			if errors.Is(err, web.ErrDenied) && accountIsDenied(account, now.UnixMilli()) {
				err = nil
			}
		}
		if err == nil {
			err = appsettings.SaveClock(ctx, tx, clock)
		}
	}
	if err == nil {
		after, err = accountEventIDsTx(ctx, tx, session.View.AccountID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return fail(err)
	}
	_ = s.emit("account.changed", map[string]any{"account_id": session.View.AccountID})
	_ = s.emit("task.changed", map[string]any{"account_id": session.View.AccountID})
	s.wakeScheduler()
	return newEventIDs(before, after), nil
}

func accountEventIDsTx(ctx context.Context, tx *sql.Tx, accountID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM system_events WHERE subject_id=? AND kind IN ('account_verify','account_login','account_deny') AND resolved_at IS NULL ORDER BY id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func newEventIDs(before, after []int64) []int64 {
	old := make(map[int64]bool, len(before))
	for _, id := range before {
		old[id] = true
	}
	result := []int64{}
	for _, id := range after {
		if !old[id] {
			result = append(result, id)
		}
	}
	return result
}

func (s *Service) dispatchSystemEvents(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if missingRuntimeDependency(s.notifier) {
		return storageFailure(errors.New("通知服务缺失"))
	}
	if err := s.emit("system_event.changed", map[string]any{"event_ids": append([]int64(nil), ids...)}); err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.notifier.System(ctx, id); err != nil {
			return storageFailure(err)
		}
	}
	return nil
}

func (s *Service) resourceSpec(ctx context.Context, tx *sql.Tx, account Account, snapshot settings.Snapshot, config settings.Values, budget web.Budget, log web.Logger) (ResourceSpec, error) {
	spec := ResourceSpec{AccountID: account.ID, ProfileDir: filepath.Join(s.dataDir, "browser", strconv.FormatInt(account.ID, 10)), Pacer: s.pacer, Budget: budget, Log: log}
	if err := json.Unmarshal(snapshot.Values["general.timezone"], &spec.Timezone); err != nil {
		return spec, err
	}
	if err := json.Unmarshal(snapshot.Values["cache.browser_cache_mb"], &spec.CacheMB); err != nil {
		return spec, err
	}
	if account.ProxyID != nil {
		proxy, err := loadProxyURLTx(ctx, tx, s.cipher, *account.ProxyID)
		if err != nil {
			return spec, err
		}
		spec.Proxy = proxy
	}
	for _, field := range []struct {
		key    string
		unit   time.Duration
		target *pacer.Range
	}{
		{"typing_interval_ms", time.Millisecond, &spec.Typing},
		{"home_stay_seconds", time.Second, &spec.Pace.HomeStay},
		{"action_wait_seconds", time.Second, &spec.Pace.Action},
		{"page_wait_seconds", time.Second, &spec.Pace.PageWait},
		{"detail_pre_wait_seconds", time.Second, &spec.Pace.PreDetail},
		{"detail_stay_seconds", time.Second, &spec.Pace.Detail},
	} {
		var value settings.Range
		raw := config[field.key]
		if len(raw) == 0 {
			raw = snapshot.Values["crawl."+field.key]
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			return spec, err
		}
		*field.target = pacer.Between(time.Duration(value.Min*float64(field.unit)), time.Duration(value.Max*float64(field.unit)))
	}
	return spec, nil
}

type runtimeProtection struct {
	ActiveTaskIDs    []int64
	ActiveRunIDs     []int64
	ActiveAccountIDs []int64
}

func (s *Service) beginMutation() (func(), error) {
	s.initRuntime()
	s.mu.Lock()
	if s.closing || s.maintenance > 0 || s.runtimeErr != nil {
		s.mu.Unlock()
		return nil, &ServiceError{Kind: ErrorUnavailable, Reason: locales.Message{Code: "runtime.closed", Args: map[string]any{}}}
	}
	s.mutations++
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() { s.mu.Lock(); s.mutations--; close(s.changed); s.changed = make(chan struct{}); s.mu.Unlock() })
	}, nil
}

func (s *Service) cleanupProtection(ctx context.Context) (runtimeProtection, func(), error) {
	s.initRuntime()
	select {
	case <-s.barrier:
	case <-ctx.Done():
		return runtimeProtection{}, nil, ctx.Err()
	}
	s.mu.Lock()
	s.maintenance++
	s.signalRuntimeLocked()
	s.mu.Unlock()
	resume := s.resumeBarrier(0, 0)
	if err := s.waitRuntime(ctx, func() bool { return s.mutations == 0 }); err != nil {
		resume()
		return runtimeProtection{}, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	protected := runtimeProtection{ActiveTaskIDs: []int64{}, ActiveRunIDs: []int64{}, ActiveAccountIDs: []int64{}}
	for taskID, run := range s.runs {
		protected.ActiveTaskIDs = append(protected.ActiveTaskIDs, taskID)
		protected.ActiveRunIDs = append(protected.ActiveRunIDs, run.Ref.RunID)
	}
	for _, session := range s.sessions {
		if session.View.Phase != SessionQueued {
			protected.ActiveAccountIDs = append(protected.ActiveAccountIDs, session.View.AccountID)
			if session.View.TaskID != nil {
				protected.ActiveTaskIDs = append(protected.ActiveTaskIDs, *session.View.TaskID)
			}
			if session.View.RunID != nil {
				protected.ActiveRunIDs = append(protected.ActiveRunIDs, *session.View.RunID)
			}
		}
	}
	return protected, resume, nil
}

func (s *Service) waitRuntime(ctx context.Context, ready func() bool) error {
	for {
		s.mu.Lock()
		if ready() {
			s.mu.Unlock()
			return nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (s *Service) resumeBarrier(taskID, accountID int64) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if taskID > 0 {
				s.pausedTasks[taskID]--
			} else if accountID > 0 {
				s.pausedAccounts[accountID]--
			} else {
				s.maintenance--
			}
			s.barrier <- struct{}{}
			s.signalRuntimeLocked()
			s.mu.Unlock()
		})
	}
}

func (s *Service) quiesce(ctx context.Context) (func(), error) {
	resume, err := s.quiesceScope(ctx, 0, 0, 0, errUserCancelled)
	if err != nil {
		return nil, err
	}
	if missingRuntimeDependency(s.notifier) {
		resume()
		return nil, storageFailure(errors.New("通知维护服务缺失"))
	}
	resumeNotifier, err := s.notifier.Quiesce(ctx)
	if err != nil {
		resume()
		return nil, err
	}
	if resumeNotifier == nil {
		resume()
		return nil, storageFailure(errors.New("通知维护恢复入口缺失"))
	}
	var once sync.Once
	return func() { once.Do(func() { resumeNotifier(); resume() }) }, nil
}
func (s *Service) quiesceAccount(ctx context.Context, id int64) (func(), error) {
	if id < 1 || id > 12767 {
		return nil, &ServiceError{Kind: ErrorNotFound, Reason: locales.Message{Code: "accounts.not_found", Args: map[string]any{}}}
	}
	return s.quiesceScope(ctx, 0, id, 0, errUserCancelled)
}
func (s *Service) quiesceTask(ctx context.Context, id int64, cause error) (func(), error) {
	if id < 1 {
		return nil, &ServiceError{Kind: ErrorNotFound, Reason: locales.Message{Code: "tasks.not_found", Args: map[string]any{}}}
	}
	return s.quiesceScope(ctx, id, 0, 0, cause)
}

func (s *Service) cancelTask(ctx context.Context, id int64, cause error) error {
	resume, err := s.quiesceTask(ctx, id, cause)
	if err == nil {
		resume()
	}
	return err
}
func (s *Service) cancelTaskGeneration(ctx context.Context, id, generation int64, cause error) error {
	resume, err := s.quiesceScope(ctx, id, 0, generation, cause)
	if err == nil {
		resume()
	}
	return err
}

func (s *Service) quiesceScope(ctx context.Context, taskID, accountID, generation int64, cause error) (func(), error) {
	s.initRuntime()
	select {
	case <-s.barrier:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s.mu.Lock()
	if taskID > 0 {
		s.pausedTasks[taskID]++
	} else if accountID > 0 {
		s.pausedAccounts[accountID]++
	} else {
		s.maintenance++
	}
	s.signalRuntimeLocked()
	s.mu.Unlock()
	resume := s.resumeBarrier(taskID, accountID)
	if err := s.waitRuntime(ctx, func() bool { return s.mutations == 0 }); err != nil {
		resume()
		return nil, err
	}
	s.mu.Lock()
	var runs []*runExecution
	var sessions []*sessionWork
	var insights []*insightWork
	for id, job := range s.insights {
		if (taskID == 0 || id == taskID) && (accountID == 0 || job.AccountID == accountID) {
			insights = append(insights, job)
			job.Cancel(cause)
		}
	}
	for _, run := range s.runs {
		if (taskID == 0 || run.Ref.TaskID == taskID) && (accountID == 0 || run.Account.ID == accountID) && (generation == 0 || run.Ref.SearchGeneration == generation) {
			runs = append(runs, run)
			run.Cancel(cause)
		}
	}
	queue := s.queue[:0]
	for _, request := range s.queue {
		if (taskID == 0 || request.TaskID == taskID) && (accountID == 0 || request.AccountID == accountID) && generation == 0 {
			if session := s.sessions[request.SessionID]; session != nil {
				sessions = append(sessions, session)
			}
		} else {
			queue = append(queue, request)
		}
	}
	s.queue = queue
	for _, session := range s.sessions {
		if (taskID == 0 || session.View.TaskID != nil && *session.View.TaskID == taskID) && (accountID == 0 || session.View.AccountID == accountID) && generation == 0 {
			if session.View.TaskID == nil || s.runs[*session.View.TaskID] == nil {
				sessions = append(sessions, session)
			}
		}
	}
	s.signalRuntimeLocked()
	s.mu.Unlock()
	for _, session := range sessions {
		if err := s.closeSessionWork(ctx, session); err != nil {
			resume()
			return nil, err
		}
	}
	for _, run := range runs {
		select {
		case <-run.Done:
		case <-ctx.Done():
			resume()
			return nil, ctx.Err()
		}
	}
	for _, job := range insights {
		select {
		case <-job.Done:
		case <-ctx.Done():
			resume()
			return nil, ctx.Err()
		}
	}
	if generation == 0 {
		s.mu.Lock()
		sessions = nil
		for _, session := range s.sessions {
			if (taskID == 0 || session.View.TaskID != nil && *session.View.TaskID == taskID) && (accountID == 0 || session.View.AccountID == accountID) {
				sessions = append(sessions, session)
			}
		}
		s.mu.Unlock()
		for _, session := range sessions {
			if err := s.closeSessionWork(ctx, session); err != nil {
				resume()
				return nil, err
			}
		}
	}
	if taskID == 0 && accountID == 0 && generation == 0 {
		s.mu.Lock()
		s.cancelSystem(cause)
		s.mu.Unlock()
		s.jobs.Wait()
		s.mu.Lock()
		if !s.closing {
			s.systemCtx, s.cancelSystem = context.WithCancelCause(s.ctx)
		}
		s.mu.Unlock()
	}
	return resume, nil
}

func (s *Service) VerificationHeldUntil(accountID, nowMS int64) *int64 {
	s.initRuntime()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return nil
	}
	for _, session := range s.sessions {
		if session.View.AccountID == accountID && session.Resource != nil && !session.Closing && (session.View.Phase == SessionVerifyHold || session.View.Phase == SessionVerifying) && session.View.ExpiresAt != nil && *session.View.ExpiresAt > nowMS {
			return int64Copy(session.View.ExpiresAt)
		}
	}
	return nil
}

func (s *Service) taskRuntimeSnapshots() map[int64]TaskRuntime {
	s.initRuntime()
	s.mu.Lock()
	ids := map[int64]bool{}
	for id := range s.runs {
		ids[id] = true
	}
	for _, q := range s.queue {
		if q.TaskID > 0 {
			ids[q.TaskID] = true
		}
	}
	defer s.mu.Unlock()
	views := make(map[int64]TaskRuntime, len(ids))
	for id := range ids {
		views[id] = s.taskRuntimeSnapshotLocked(id)
	}
	return views
}

func (s *Service) reanalyzeCached(ctx context.Context, work WorkRef) error {
	s.initRuntime()
	release, err := s.beginMutation()
	if err != nil {
		return err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageFailure(err)
	}
	defer tx.Rollback()
	task, err := loadTaskTx(ctx, tx, work.TaskID)
	if err != nil {
		return storageFailure(err)
	}
	if task.JudgeMode == "ai" && s.models == nil {
		return &ServiceError{Kind: ErrorUnavailable, Reason: locales.Message{Code: "run.no_ai_provider", Args: map[string]any{}}}
	}
	finding, err := loadFindingTx(ctx, tx, work.FindingID)
	if err != nil {
		return storageFailure(err)
	}
	if task.SearchGeneration != work.SearchGeneration || finding.ProcessingRevision != work.ProcessingRevision {
		return findingChangedError()
	}
	snapshot, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return storageFailure(err)
	}
	item, err := loadListingTx(ctx, tx, finding.ItemID)
	if err != nil {
		return storageFailure(err)
	}
	if !detailCached(item.DetailFetchedAt, now.UnixMilli()) {
		return nil
	}
	overrides, err := json.Marshal(task.CrawlOverrides)
	if err != nil {
		return storageFailure(err)
	}
	if task.CrawlOverrides == nil {
		overrides = json.RawMessage(`{}`)
	}
	config, _, err := settings.ResolveTask(snapshot, task.IntervalMinutes, overrides)
	if err != nil {
		return storageFailure(err)
	}
	promptSnapshot, err := loadPromptSnapshotTx(ctx, tx)
	if err != nil {
		return storageFailure(err)
	}
	var account Account
	if task.AccountID != nil {
		account, err = loadAccountTx(ctx, tx, *task.AccountID)
		if err != nil {
			return storageFailure(err)
		}
	}
	var spec ResourceSpec
	if account.ProxyID != nil {
		spec.Proxy, err = loadProxyURLTx(ctx, tx, s.cipher, *account.ProxyID)
		if err != nil {
			return storageFailure(err)
		}
	}
	if err = appsettings.SaveClock(ctx, tx, clock); err != nil {
		return storageFailure(err)
	}
	if err = tx.Commit(); err != nil {
		return storageFailure(err)
	}
	life, cancel := context.WithCancelCause(s.ctx)
	stop := context.AfterFunc(ctx, func() { cancel(ctx.Err()) })
	defer stop()
	defer cancel(nil)
	run := &runExecution{Ref: work.RunRef, Task: task, Account: account, Snapshot: snapshot, Config: config, Prompts: promptSnapshot, Spec: spec, CachedOnly: true, Context: life, Cancel: cancel, Done: make(chan struct{}), Backlog: []Finding{finding}, P: []WorkRef{work}, Warnings: []locales.Message{}}
	s.mu.Lock()
	if s.closing || s.maintenance > 0 || s.pausedTasks[task.ID] > 0 {
		s.mu.Unlock()
		return &ServiceError{Kind: ErrorUnavailable, Reason: locales.Message{Code: "runtime.closed", Args: map[string]any{}}}
	}
	if s.runs[task.ID] != nil {
		s.mu.Unlock()
		return nil
	}
	s.runs[task.ID] = run
	s.jobs.Add(1)
	s.signalRuntimeLocked()
	s.mu.Unlock()
	release()
	defer s.releaseRun(run)
	if finding.Stage == StageTitle {
		if err := s.runTitles(life, run); err != nil {
			return err
		}
		filtered := run.P[:0]
		for _, candidate := range run.P {
			if candidate.FindingID == work.FindingID {
				filtered = append(filtered, candidate)
			}
		}
		run.P = filtered
	}
	if err := s.runDetail(life, run); err != nil {
		return err
	}
	_ = s.emit("finding.changed", map[string]any{"finding_id": work.FindingID})
	return nil
}

func (s *Service) GetTaskRuntime(ctx context.Context, taskID int64) (TaskRuntime, error) {
	if taskID < 1 {
		return TaskRuntime{}, &ServiceError{Kind: ErrorNotFound, Reason: locales.Message{Code: "tasks.not_found", Args: map[string]any{}}}
	}
	s.initRuntime()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskRuntime{}, storageFailure(err)
	}
	defer tx.Rollback()
	task, err := loadTaskTx(ctx, tx, taskID)
	if err != nil {
		return TaskRuntime{}, storageFailure(err)
	}
	view := s.taskRuntimeSnapshot(taskID)
	if view.Phase == TaskIdle {
		preflight, err := s.preflightTx(ctx, tx, task, TriggerAuto)
		if err != nil {
			return TaskRuntime{}, storageFailure(err)
		}
		view.Blocked = displayPreflightBlock(preflight)
	}
	if err = tx.Commit(); err != nil {
		return TaskRuntime{}, storageFailure(err)
	}
	return view, nil
}

func displayPreflightBlock(result preflightResult) *RuntimeBlocked {
	if result.UserReason != nil {
		return &RuntimeBlocked{Reason: *result.UserReason}
	}
	return result.Blocked
}

func (s *Service) guardedWork(ctx context.Context, run *runExecution, where string, work func(context.Context) error) error {
	if run.Resource == nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		return work(ctx)
	}
	runner := run.Resource.Runner()
	if runner == nil {
		return storageFailure(errors.New("运行页面未就绪"))
	}
	return runner.Guard(ctx, where, work)
}
