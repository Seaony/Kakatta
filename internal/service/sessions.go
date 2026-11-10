package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/remote"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func (s *Service) OpenLogin(ctx context.Context, accountID int64) (SessionView, error) {
	return s.openManualSession(ctx, accountID, SessionLogin)
}

func (s *Service) OpenVerification(ctx context.Context, accountID int64) (SessionView, error) {
	return s.openManualSession(ctx, accountID, SessionVerification)
}

func (s *Service) openManualSession(ctx context.Context, accountID int64, kind SessionKind) (SessionView, error) {
	done, err := s.beginMutation()
	if err != nil {
		return SessionView{}, err
	}
	defer done()
	if err := ctx.Err(); err != nil {
		return SessionView{}, err
	}
	s.mu.Lock()
	for _, existing := range s.sessions {
		if existing.View.AccountID == accountID && !existing.Closing && (existing.View.Kind == kind || kind == SessionVerification && existing.View.Phase == SessionVerifyHold) {
			accepted := existing.View.Phase != SessionQueued
			for _, queued := range s.queue {
				accepted = accepted || queued.SessionID == existing.View.ID
			}
			if !accepted {
				continue
			}
			view := copySessionView(existing.View)
			s.mu.Unlock()
			return view, nil
		}
	}
	s.mu.Unlock()
	_, _, now, blocked, err := s.manualPreflight(ctx, accountID, kind)
	if err != nil {
		return SessionView{}, err
	}
	if blocked != nil {
		return SessionView{}, &ServiceError{Kind: ErrorConflict, Reason: blocked.Reason, NextAllowedAt: blocked.NextAllowedAt}
	}
	id := rand.Text()
	at := now.UnixMilli()
	life, cancel := context.WithCancelCause(s.ctx)
	session := &sessionWork{Context: life, Cancel: cancel, Closed: make(chan struct{}), OpenDone: make(chan struct{}),
		View: SessionView{ID: id, AccountID: accountID, Kind: kind, Phase: SessionQueued, QueuedAt: &at}}
	view := copySessionView(session.View)
	s.mu.Lock()
	s.sessions[id] = session
	s.mu.Unlock()
	request := startRequest{AccountID: accountID, Kind: kind, SessionID: id, QueuedAt: now}
	if err := s.enqueueStart(request); err != nil {
		s.releaseReservation(id)
		return SessionView{}, err
	}
	_ = s.emit("session.changed", view)
	return view, nil
}

func (s *Service) manualPreflight(ctx context.Context, accountID int64, kind SessionKind) (Account, settings.Snapshot, time.Time, *RuntimeBlocked, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, settings.Snapshot{}, time.Time{}, nil, storageFailure(err)
	}
	defer tx.Rollback()
	snapshot, clock, now, err := s.observeNowTx(ctx, tx)
	if err != nil {
		return Account{}, snapshot, now, nil, err
	}
	account, err := loadAccountTx(ctx, tx, accountID)
	if err != nil {
		return account, snapshot, now, nil, storageFailure(err)
	}
	blocked, gateErr := manualAccountBlockTx(ctx, tx, account, kind, snapshot, now)
	err = appsettings.SaveClock(ctx, tx, clock)
	if err == nil {
		err = tx.Commit()
	}
	if err == nil {
		err = gateErr
	}
	return account, snapshot, now, blocked, storageFailure(err)
}

func manualAccountBlockTx(ctx context.Context, tx *sql.Tx, account Account, kind SessionKind, snapshot settings.Snapshot, now time.Time) (*RuntimeBlocked, error) {
	pending, err := accountCleanupPendingTx(ctx, tx, account.ID)
	if err != nil {
		return nil, err
	}
	if pending {
		return &RuntimeBlocked{Reason: locales.Message{Code: "accounts.cleanup_failed", Args: map[string]any{}}}, nil
	}
	return manualSessionBlock(account, kind, snapshot, now)
}

func (s *Service) tryStartSession(ctx context.Context, request startRequest) (bool, *RuntimeBlocked, error) {
	done, err := s.beginMutation()
	if err != nil {
		return false, nil, err
	}
	defer done()
	_, snapshot, now, blocked, err := s.manualPreflight(ctx, request.AccountID, request.Kind)
	if err != nil || blocked != nil {
		s.releaseReservation(request.SessionID)
		return false, blocked, err
	}
	var limit int64
	if err := json.Unmarshal(snapshot.Values["crawl.global_pages"], &limit); err != nil || limit < 1 {
		return false, nil, storageFailure(errors.New("全局页面限制无效"))
	}
	session, reserved, err := s.reserveStart(request, limit, now)
	if err != nil || !reserved {
		return false, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.releaseReservation(request.SessionID)
		return false, nil, storageFailure(err)
	}
	defer tx.Rollback()
	snapshot, clock, now, err := s.observeNowTx(ctx, tx)
	if err == nil {
		err = json.Unmarshal(snapshot.Values["crawl.global_pages"], &limit)
	}
	if err == nil {
		var confirmed bool
		confirmed, err = s.confirmReservation(session, limit)
		if err == nil && !confirmed {
			_ = tx.Rollback()
			s.restoreQueuedReservation(session)
			return false, nil, nil
		}
	}
	var account Account
	if err == nil {
		account, err = loadAccountTx(ctx, tx, request.AccountID)
	}
	if err == nil {
		blocked, err = manualAccountBlockTx(ctx, tx, account, request.Kind, snapshot, now)
	}
	var spec ResourceSpec
	if err == nil && blocked == nil {
		budget := s.newVisitBudget(session.Context, account.ID, nil, true)
		spec, err = s.resourceSpec(ctx, tx, account, snapshot, nil, budget, nil)
		if err == nil {
			err = saveSessionMarkerTx(ctx, tx, account.ID, sessionMarker{Version: 1, SessionID: session.View.ID, StartedAt: now.UnixMilli()})
		}
	}
	if err == nil {
		err = appsettings.SaveClock(ctx, tx, clock)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil || blocked != nil {
		s.releaseReservation(request.SessionID)
		return false, blocked, storageFailure(err)
	}
	s.mu.Lock()
	s.jobs.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.jobs.Done()
		s.runManualSession(session, account, spec)
	}()
	return true, nil, nil
}

func (s *Service) runManualSession(session *sessionWork, account Account, spec ResourceSpec) {
	deadline := s.now().Add(10 * time.Minute)
	if err := s.setSessionPhase(session.View.ID, SessionOpening, &deadline); err != nil {
		s.completeResourceOpen(session, nil)
		_ = s.closeManualSession(session)
		return
	}
	resource, err := s.resources.Open(session.Context, spec)
	s.completeResourceOpen(session, resource)
	if err != nil || resource == nil || resource.Runner() == nil || resource.Chrome() == nil {
		_ = s.closeManualSession(session)
		return
	}
	if err := session.Context.Err(); err != nil {
		_ = s.closeManualSession(session)
		return
	}
	runner := resource.Runner()
	if session.View.Kind == SessionLogin {
		err = runner.OpenLogin(session.Context)
		if err == nil {
			runner.BeginVerification()
			err = runner.CheckVerification(session.Context)
		}
		if err == nil {
			err = s.publishManualRemote(session, SessionWaitingLogin)
		}
		if err == nil {
			err = runner.WaitForLogin(session.Context, max(deadline.Sub(s.now()), 0))
		}
		if err == nil {
			head, profileErr := runner.OwnProfile(session.Context)
			var problem *ServiceError
			if web.IsStop(profileErr) || session.Context.Err() != nil || errors.As(profileErr, &problem) && problem.Kind == ErrorUnavailable {
				err = profileErr
			} else {
				var nick, avatar *string
				if profileErr == nil {
					if head.Nick != "" {
						nick = &head.Nick
					}
					if head.Avatar != "" {
						avatar = &head.Avatar
					}
				}
				_, err = s.updateSessionAccount(session.Context, session, func(ctx context.Context, tx *sql.Tx, _ settings.Values, nowMS int64) (Account, error) {
					return completeLoginTx(ctx, tx, account.ID, nick, avatar, nowMS)
				})
				if err != nil {
					_ = s.closeManualSession(session)
					return
				}
			}
		}
	} else {
		kind := quota.Search
		if target, parseErr := url.Parse(*account.VerifyURL); parseErr == nil && (target.Path == "/item" || target.Path == "/personal") {
			kind = quota.Detail
		}
		err = spec.Budget.Acquire(kind, "重新打开验证网址")
		if err == nil {
			_, err = resource.Chrome().NavigateContext(session.Context, *account.VerifyURL, 30*time.Second)
		}
		if err == nil {
			runner.BeginVerification()
			err = runner.CheckVerification(session.Context)
		}
		if err == nil {
			err = s.publishManualRemote(session, SessionVerifying)
		}
		if err == nil {
			s.monitorVerification(session)
			return
		}
	}
	if web.IsStop(err) {
		if stateErr := s.recordSessionRisk(session, err); stateErr == nil && manualRisk(err) == web.ErrNeedVerify {
			_ = s.holdVerification(session.View.ID, err)
			return
		}
	}
	_ = s.closeManualSession(session)
}

func (s *Service) publishManualRemote(session *sessionWork, phase SessionPhase) error {
	remoteSession, err := session.Resource.OpenRemote(session.Context)
	if err != nil {
		return err
	}
	if remoteSession == nil {
		return errors.New("远程画面未创建")
	}
	s.mu.Lock()
	if session.Closing || session.Context.Err() != nil {
		s.mu.Unlock()
		return context.Canceled
	}
	session.Remote = remoteSession
	path := "/remote/sessions/" + session.View.ID + "/"
	session.View.RemotePath = &path
	s.mu.Unlock()
	if err := s.setSessionPhase(session.View.ID, phase, nil); err != nil {
		return err
	}
	view, err := s.GetSession(session.Context, session.View.AccountID, session.View.ID)
	if err == nil {
		err = s.emit("session.changed", view)
	}
	return err
}

func (s *Service) holdVerification(sessionID string, _ error) error {
	s.mu.Lock()
	session := s.sessions[sessionID]
	if session == nil || session.Closing {
		s.mu.Unlock()
		return sessionError(ErrorConflict, "runtime.session_changed")
	}
	if session.View.Phase == SessionVerifyHold || session.View.Phase == SessionVerifying {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	session.opMu.Lock()
	defer session.opMu.Unlock()
	runner := session.Resource.Runner()
	runner.BeginVerification()
	if err := runner.CheckVerification(session.Context); err != nil {
		if web.IsStop(err) {
			_ = s.recordSessionRisk(session, err)
		}
		return errors.Join(err, s.closeManualSession(session))
	}
	// 已有画面不用重新打开网址，取不到恢复网址也能留给用户处理。
	_, snapshot, _, blocked, err := s.manualPreflight(session.Context, session.View.AccountID, SessionLogin)
	if err != nil {
		return errors.Join(err, s.closeManualSession(session))
	}
	if blocked != nil && (blocked.Reason.Code == "run.account_denied" || blocked.Reason.Code == "accounts.cleanup_failed") {
		return errors.Join(&ServiceError{Kind: ErrorConflict, Reason: blocked.Reason, NextAllowedAt: blocked.NextAllowedAt}, s.closeManualSession(session))
	}
	var minutes int64
	if err := json.Unmarshal(snapshot.Values["crawl.verify_hold_minutes"], &minutes); err != nil || minutes < 1 {
		return errors.Join(errors.New("验证保留时长无效"), s.closeManualSession(session))
	}
	deadline := s.now().Add(time.Duration(minutes) * time.Minute)
	s.mu.Lock()
	session.View.Kind, session.HoldDeadline = SessionVerification, deadline
	s.mu.Unlock()
	if err := s.setSessionPhase(sessionID, SessionVerifyHold, &deadline); err != nil {
		return errors.Join(err, s.closeManualSession(session))
	}
	if err := s.publishManualRemote(session, SessionVerifyHold); err != nil {
		return errors.Join(err, s.closeManualSession(session))
	}
	s.mu.Lock()
	alreadyViewing := session.Remote != nil && session.Remote.HasViewer()
	s.mu.Unlock()
	if alreadyViewing {
		if err := s.ViewerOpened(session.Context, sessionID); err != nil {
			return errors.Join(err, s.closeManualSession(session))
		}
	}
	s.mu.Lock()
	s.jobs.Add(1)
	s.mu.Unlock()
	go func() { defer s.jobs.Done(); s.monitorVerification(session) }()
	return nil
}

func (s *Service) monitorVerification(session *sessionWork) {
	for {
		if err := session.Context.Err(); err != nil {
			return
		}
		session.opMu.Lock()
		err := session.Resource.Runner().CheckVerification(session.Context)
		session.opMu.Unlock()
		if err != nil && !errors.Is(err, web.ErrNeedVerify) {
			if web.IsStop(err) {
				_ = s.recordSessionRisk(session, err)
			}
			_ = s.closeManualSession(session)
			return
		}
		if err := s.pacer.Wait(session.Context, pacer.Between(time.Second, time.Second)); err != nil {
			return
		}
	}
}

func (s *Service) updateSessionAccount(ctx context.Context, session *sessionWork, update func(context.Context, *sql.Tx, settings.Values, int64) (Account, error)) (Account, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, storageFailure(err)
	}
	defer tx.Rollback()
	snapshot, clock, now, err := s.observeNowTx(ctx, tx)
	var account Account
	var before, after []int64
	if err == nil {
		before, err = accountEventIDsTx(ctx, tx, session.View.AccountID)
	}
	if err == nil {
		account, err = update(ctx, tx, snapshot.Values, now.UnixMilli())
	}
	if err == nil {
		after, err = accountEventIDsTx(ctx, tx, session.View.AccountID)
	}
	if err == nil {
		err = appsettings.SaveClock(ctx, tx, clock)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return account, storageFailure(err)
	}
	_ = s.emit("account.changed", map[string]any{"account_id": account.ID})
	_ = s.emit("task.changed", map[string]any{"account_id": account.ID})
	s.wakeScheduler()
	return account, s.dispatchSystemEvents(ctx, newEventIDs(before, after))
}

func (s *Service) recordSessionRisk(session *sessionWork, risk error) error {
	var readURL func(context.Context) (string, error)
	if session.Resource != nil && session.Resource.Chrome() != nil {
		readURL = session.Resource.Chrome().URLContext
	}
	ids, err := s.persistSessionRisk(session, risk, readURL)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = s.dispatchSystemEvents(ctx, ids)
	if err != nil {
		s.setRuntimeError(err)
	}
	return err
}

func (s *Service) ConfirmVerification(ctx context.Context, accountID int64, sessionID string) (AccountView, error) {
	s.mu.Lock()
	session := s.sessions[sessionID]
	if session == nil || session.View.AccountID != accountID || session.Closing || session.Resource == nil || session.View.Phase != SessionVerifyHold && session.View.Phase != SessionVerifying {
		s.mu.Unlock()
		return AccountView{}, sessionError(ErrorConflict, "runtime.session_changed")
	}
	s.mu.Unlock()
	session.opMu.Lock()
	defer session.opMu.Unlock()
	if err := session.Resource.Runner().ConfirmVerification(ctx); err != nil {
		if risk := manualRisk(err); risk != nil && risk != web.ErrNeedVerify {
			stateErr := s.recordSessionRisk(session, err)
			closeErr := s.closeManualSession(session)
			if stateErr != nil || closeErr != nil {
				return AccountView{}, errors.Join(stateErr, closeErr, err)
			}
			return AccountView{}, manualSignalError(err)
		}
		return AccountView{}, manualSignalError(err)
	}
	_, err := s.updateSessionAccount(ctx, session, func(ctx context.Context, tx *sql.Tx, values settings.Values, nowMS int64) (Account, error) {
		return completeVerificationTx(ctx, tx, accountID, values, nowMS)
	})
	if err != nil {
		return AccountView{}, errors.Join(manualSignalError(err), s.closeManualSession(session))
	}
	if err := s.closeManualSession(session); err != nil {
		return AccountView{}, err
	}
	return s.GetAccount(ctx, accountID)
}

func (s *Service) closeManualSession(session *sessionWork) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.closeSessionWork(ctx, session)
}

func (s *Service) CloseSession(ctx context.Context, accountID int64, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.initRuntime()
	s.mu.Lock()
	session := s.sessions[sessionID]
	if session == nil || session.View.AccountID != accountID {
		s.mu.Unlock()
		return nil
	}
	request := startRequest{Kind: session.View.Kind, SessionID: sessionID}
	for _, queued := range s.queue {
		if queued.SessionID == sessionID {
			request = *queued
			break
		}
	}
	s.mu.Unlock()
	s.removeQueued(request)
	return s.closeManualSession(session)
}

func (s *Service) RemoteSession(ctx context.Context, sessionID string) (*remote.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.initRuntime()
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[sessionID]
	if session == nil || session.Closing || session.Remote == nil {
		return nil, sessionError(ErrorConflict, "runtime.remote_not_ready")
	}
	return session.Remote, nil
}

func (s *Service) ViewerOpened(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.initRuntime()
	s.mu.Lock()
	session := s.sessions[sessionID]
	if session == nil || session.Closing {
		s.mu.Unlock()
		return sessionError(ErrorConflict, "runtime.session_changed")
	}
	if session.View.Phase != SessionVerifyHold || !session.FirstViewedAt.IsZero() {
		s.mu.Unlock()
		return nil
	}
	now := s.now()
	if !session.HoldDeadline.IsZero() && !now.Before(session.HoldDeadline) {
		s.mu.Unlock()
		// 这是远程连接的回调，原定时器负责关闭，不能同步等自己退出。
		return sessionError(ErrorConflict, "runtime.session_changed")
	}
	deadline, first := firstViewerDeadline(session.Deadline, session.FirstViewedAt, now)
	session.Deadline, session.FirstViewedAt = deadline, first
	if session.Timer != nil {
		session.Timer.Stop()
	}
	s.mu.Unlock()
	if err := s.setSessionPhase(sessionID, SessionVerifying, &deadline); err != nil {
		return err
	}
	view, err := s.GetSession(ctx, session.View.AccountID, sessionID)
	if err == nil {
		err = s.emit("session.changed", view)
	}
	return err
}

func (s *Service) GetSession(ctx context.Context, accountID int64, sessionID string) (SessionView, error) {
	if err := ctx.Err(); err != nil {
		return SessionView{}, err
	}
	s.initRuntime()
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[sessionID]
	if session == nil || session.View.AccountID != accountID {
		return SessionView{}, sessionError(ErrorNotFound, "runtime.session_not_found")
	}
	return copySessionView(session.View), nil
}

func sessionError(kind ErrorKind, code string) error {
	return &ServiceError{Kind: kind, Reason: locales.Message{Code: code, Args: map[string]any{}}}
}

func manualSessionBlock(account Account, kind SessionKind, snapshot settings.Snapshot, now time.Time) (*RuntimeBlocked, error) {
	var reason *locales.Message
	ready := now
	if account.State == AccountDenied {
		if account.CooldownUntil == nil {
			return nil, errors.New("拒绝暂停没有结束时间")
		}
		if until := time.UnixMilli(*account.CooldownUntil); until.After(ready) {
			ready = until
			reason = &locales.Message{Code: "run.account_denied", Args: map[string]any{}}
		}
	}
	if reason == nil && kind == SessionVerification && (account.State != AccountNeedsVerification || account.VerifyURL == nil || *account.VerifyURL == "") {
		return nil, sessionError(ErrorConflict, "runtime.verification_not_pending")
	}
	if account.LastRunEndedAt != nil {
		if account.NextGapSeconds == nil {
			return nil, errors.New("账号间隔记录缺失")
		}
		untilMS, err := accountDeadline(*account.LastRunEndedAt, *account.NextGapSeconds, 1000)
		if err != nil {
			return nil, err
		}
		if until := time.UnixMilli(untilMS); until.After(ready) {
			ready = until
			if reason == nil {
				reason = &locales.Message{Code: "run.account_gap", Args: map[string]any{}}
			}
		}
	}
	var zone string
	var quiet settings.QuietHours
	if err := json.Unmarshal(snapshot.Values["general.timezone"], &zone); err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(zone)
	if err != nil || zone == "" || zone == "Local" {
		return nil, errors.New("人工会话时区无效")
	}
	if err := json.Unmarshal(snapshot.Values["crawl.quiet_hours"], &quiet); err != nil {
		return nil, err
	}
	until, err := quietUntil(ready, quiet, location)
	if err != nil {
		return nil, err
	}
	if until != nil {
		ready = *until
		if reason == nil {
			reason = &locales.Message{Code: "run.quiet_hours", Args: map[string]any{}}
		}
	}
	if reason == nil {
		return nil, nil
	}
	at := ready.UnixMilli()
	return &RuntimeBlocked{Reason: *reason, NextAllowedAt: &at}, nil
}

func firstViewerDeadline(current, firstViewed, openedAt time.Time) (time.Time, time.Time) {
	if !firstViewed.IsZero() {
		return current, firstViewed
	}
	return openedAt.Add(10 * time.Minute), openedAt
}

func manualRisk(err error) error {
	for _, signal := range []error{web.ErrEnvBlocked, web.ErrDenied, web.ErrLoginExpired, web.ErrNeedVerify} {
		if errors.Is(err, signal) {
			return signal
		}
	}
	return nil
}

func manualSignalError(err error) error {
	code := ""
	switch manualRisk(err) {
	case web.ErrNeedVerify:
		code = "run.account_needs_verification"
	case web.ErrLoginExpired:
		code = "run.account_expired"
	case web.ErrDenied, web.ErrEnvBlocked:
		code = "run.account_denied"
	}
	if code == "" {
		return err
	}
	return &causedServiceError{service: &ServiceError{Kind: ErrorConflict, Reason: locales.Message{Code: code, Args: map[string]any{}}}, cause: err}
}
