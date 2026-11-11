package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/remote"
	"github.com/Seaony/Kakatta/internal/settings"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

type manualOpenCounter struct{ calls atomic.Int64 }

func (f *manualOpenCounter) Open(context.Context, ResourceSpec) (SessionResource, error) {
	f.calls.Add(1)
	return nil, errors.New("本用例不允许打开资源")
}

func TestManualStartRechecksReducedPageLimitInSecondTransaction(t *testing.T) {
	s := sessionTestService(t)
	if _, err := s.settings.Apply(t.Context(), settings.Values{"crawl.global_pages": []byte(`2`)}); err != nil {
		t.Fatal(err)
	}
	occupiedID := accountTestInsert(t, s, "已占页面", AccountActive, 1)
	queuedID := accountTestInsert(t, s, "待登录", AccountSignedOut, 2)
	view, err := s.OpenLogin(t.Context(), queuedID)
	if err != nil {
		t.Fatal(err)
	}
	request := s.queuedStarts()[0]
	occupied, reserved, err := s.reserveStart(startRequest{AccountID: occupiedID, Kind: SessionRun}, 2, s.now())
	if err != nil || !reserved {
		t.Fatalf("没有建立原有页面占用：%v %v", reserved, err)
	}
	s.completeResourceOpen(occupied, nil)
	factory := new(manualOpenCounter)
	s.resources = factory
	firstNow, releaseNow := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseNow:
		default:
			close(releaseNow)
		}
	})
	base := s.now()
	var first atomic.Bool
	s.now = func() time.Time {
		if first.CompareAndSwap(false, true) {
			close(firstNow)
			<-releaseNow
		}
		return base
	}
	type result struct {
		started bool
		blocked *RuntimeBlocked
		err     error
	}
	finished := make(chan result, 1)
	go func() {
		started, blocked, err := s.tryStartSession(t.Context(), request)
		finished <- result{started, blocked, err}
	}()
	select {
	case <-firstNow:
	case <-time.After(5 * time.Second):
		t.Fatal("首事务没有到达门闩")
	}
	s.mu.Lock()
	close(releaseNow)
	readCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	var one int
	readErr := s.db.QueryRowContext(readCtx, `SELECT 1`).Scan(&one)
	var saveErr error
	if readErr == nil {
		_, saveErr = s.settings.Apply(readCtx, settings.Values{"crawl.global_pages": []byte(`1`)})
	}
	cancel()
	s.mu.Unlock()
	if readErr != nil || saveErr != nil {
		t.Fatalf("首事务和预留之间无法缩小上限：%v %v", readErr, saveErr)
	}
	select {
	case result := <-finished:
		if result.err != nil || result.started || result.blocked != nil {
			t.Errorf("第二事务沿用了旧页面上限：%v %+v %v", result.started, result.blocked, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("第二事务没有返回")
	}
	queue := s.queuedStarts()
	if len(queue) != 1 || queue[0].SessionID != request.SessionID || !queue[0].QueuedAt.Equal(request.QueuedAt) {
		t.Errorf("页面忙时丢掉了原人工请求：%+v", queue)
	}
	current, err := s.GetSession(t.Context(), queuedID, view.ID)
	if err != nil || current.Phase != SessionQueued || current.StartedAt != nil || current.ExpiresAt != nil {
		t.Errorf("页面忙时没有恢复原排队态：%+v %v", current, err)
	}
	s.mu.Lock()
	session := s.sessions[view.ID]
	contextKept := session != nil && session.Context.Err() == nil
	s.mu.Unlock()
	if !contextKept || factory.calls.Load() != 0 {
		t.Errorf("页面忙时取消了请求或打开资源：%v %d", contextKept, factory.calls.Load())
	}
	var markers, runs int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM meta WHERE key=?`, "runtime.session."+fmt.Sprint(queuedID)).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM run_records`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	var ended, gap *int64
	if err := s.db.QueryRowContext(t.Context(), `SELECT last_run_ended_at,next_gap_seconds FROM accounts WHERE id=?`, queuedID).Scan(&ended, &gap); err != nil {
		t.Fatal(err)
	}
	if markers != 0 || runs != 0 || ended != nil || gap != nil {
		t.Errorf("页面忙仍留下启动或间隔记录：marker%d run%d ended%v gap%v", markers, runs, ended, gap)
	}
}

type sessionEventNotifier struct {
	db        *sql.DB
	service   *Service
	published *int
	ids       []int64
	states    []AccountState
	err       error
}

func (n *sessionEventNotifier) FlushRun(context.Context, RunRef, []int64) (NotifyResult, error) {
	return NotifyResult{}, errors.New("人工事件不能调用运行推送")
}

func (n *sessionEventNotifier) System(ctx context.Context, id int64) error {
	read, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var state AccountState
	if err := n.db.QueryRowContext(read, `SELECT a.state FROM system_events e JOIN accounts a ON a.id=e.subject_id WHERE e.id=? AND e.resolved_at IS NULL`, id).Scan(&state); err != nil {
		return fmt.Errorf("推送时不能读取已提交账号：%w", err)
	}
	if *n.published == 0 || len(n.service.wake) == 0 {
		return errors.New("推送早于提交后的事件发布或调度唤醒")
	}
	n.ids = append(n.ids, id)
	n.states = append(n.states, state)
	return n.err
}

func TestSessionAccountDispatchesOnlyNewCommittedEvents(t *testing.T) {
	s := sessionTestService(t)
	id := accountTestInsert(t, s, "人工风险", AccountActive, 1)
	work := &sessionWork{View: SessionView{AccountID: id}}
	var published int
	s.publish = func(string, *locales.Message, json.RawMessage) error { published++; return nil }
	notifier := &sessionEventNotifier{db: s.db, service: s, published: &published}
	s.notifier = notifier
	change := func(ctx context.Context, tx *sql.Tx, values settings.Values, nowMS int64) (Account, error) {
		return applyAccountRiskTx(ctx, tx, id, web.ErrNeedVerify, "https://www.goofish.com/item?id=1", values, nowMS)
	}
	for range 2 {
		account, err := s.updateSessionAccount(t.Context(), work, change)
		if err != nil || account.State != AccountNeedsVerification {
			t.Fatalf("人工风险更新失败：%+v %v", account, err)
		}
	}
	if len(notifier.ids) != 1 || notifier.states[0] != AccountNeedsVerification {
		t.Fatalf("同一个未解决事件重复推送：%v %v", notifier.ids, notifier.states)
	}
	if _, err := s.updateSessionAccount(t.Context(), work, func(ctx context.Context, tx *sql.Tx, values settings.Values, nowMS int64) (Account, error) {
		return completeVerificationTx(ctx, tx, id, values, nowMS)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.updateSessionAccount(t.Context(), work, change); err != nil {
		t.Fatal(err)
	}
	if len(notifier.ids) != 2 || notifier.ids[0] == notifier.ids[1] {
		t.Fatalf("已解决后新风险没有单独推送：%v", notifier.ids)
	}
}

func TestSessionAccountRollbackNeverDispatches(t *testing.T) {
	for _, commitFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(commitFailure), func(t *testing.T) {
			s := sessionTestService(t)
			id := accountTestInsert(t, s, "回滚", AccountActive, 1)
			work := &sessionWork{View: SessionView{AccountID: id}}
			var published int
			s.publish = func(string, *locales.Message, json.RawMessage) error { published++; return nil }
			notifier := &sessionEventNotifier{db: s.db, service: s, published: &published}
			s.notifier = notifier
			if commitFailure {
				if _, err := s.db.ExecContext(t.Context(), `CREATE TRIGGER reject_session_clock BEFORE UPDATE ON meta WHEN NEW.key='quota.clock' BEGIN SELECT RAISE(FAIL,'blocked'); END`); err != nil {
					t.Fatal(err)
				}
			}
			_, err := s.updateSessionAccount(t.Context(), work, func(ctx context.Context, tx *sql.Tx, values settings.Values, nowMS int64) (Account, error) {
				account, err := applyAccountRiskTx(ctx, tx, id, web.ErrNeedVerify, "https://www.goofish.com/", values, nowMS)
				if err == nil && !commitFailure {
					err = errors.New("本次更新回滚")
				}
				return account, err
			})
			if err == nil || len(notifier.ids) != 0 || published != 0 {
				t.Fatalf("回滚仍然发出事件：%v %v %d", err, notifier.ids, published)
			}
			var state AccountState
			var count int
			if err := s.db.QueryRowContext(t.Context(), `SELECT state FROM accounts WHERE id=?`, id).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM system_events`).Scan(&count); err != nil || count != 0 || state != AccountActive {
				t.Fatalf("账号或事件没有同事务回滚：%s %d %v", state, count, err)
			}
		})
	}
}

func TestSessionAccountKeepsCommittedStateOnNotifierFailure(t *testing.T) {
	s := sessionTestService(t)
	id := accountTestInsert(t, s, "推送写入失败", AccountActive, 1)
	work := &sessionWork{View: SessionView{AccountID: id}}
	var published int
	s.publish = func(string, *locales.Message, json.RawMessage) error { published++; return nil }
	failure := errors.New("通知存储失败")
	notifier := &sessionEventNotifier{db: s.db, service: s, published: &published, err: failure}
	s.notifier = notifier
	account, err := s.updateSessionAccount(t.Context(), work, func(ctx context.Context, tx *sql.Tx, values settings.Values, nowMS int64) (Account, error) {
		return applyAccountRiskTx(ctx, tx, id, web.ErrDenied, "", values, nowMS)
	})
	if !errors.Is(err, failure) || account.State != AccountDenied || len(notifier.ids) != 1 {
		t.Fatalf("推送基础设施失败被吞掉或假装回滚：%+v %v %v", account, err, notifier.ids)
	}
	var stored AccountState
	if err := s.db.QueryRowContext(t.Context(), `SELECT state FROM accounts WHERE id=?`, id).Scan(&stored); err != nil || stored != AccountDenied {
		t.Fatalf("提交后的拒绝状态丢失：%s %v", stored, err)
	}
}

func TestSessionAccountMissingNotifierIsExplicitFailure(t *testing.T) {
	s := sessionTestService(t)
	id := accountTestInsert(t, s, "缺少通知", AccountActive, 1)
	work := &sessionWork{View: SessionView{AccountID: id}}
	account, err := s.updateSessionAccount(t.Context(), work, func(ctx context.Context, tx *sql.Tx, values settings.Values, nowMS int64) (Account, error) {
		return applyAccountRiskTx(ctx, tx, id, web.ErrNeedVerify, "https://www.goofish.com/", values, nowMS)
	})
	var problem *ServiceError
	if !errors.As(err, &problem) || problem.Kind != ErrorUnavailable || account.State != AccountNeedsVerification {
		t.Fatalf("缺失通知依赖被当作成功：%+v %v", account, err)
	}
}

func TestRecordSessionRiskPersistsAfterSessionCancellationWithoutChrome(t *testing.T) {
	s := sessionTestService(t)
	id := accountTestInsert(t, s, "已取消人工会话", AccountActive, 1)
	life, cancel := context.WithCancelCause(s.ctx)
	cancel(context.Canceled)
	work := &sessionWork{Context: life, View: SessionView{AccountID: id}}
	var published int
	s.publish = func(string, *locales.Message, json.RawMessage) error { published++; return nil }
	notifier := &sessionEventNotifier{db: s.db, service: s, published: &published}
	s.notifier = notifier
	if err := s.recordSessionRisk(work, web.ErrDenied); err != nil {
		t.Fatal(err)
	}
	if len(notifier.ids) != 1 || notifier.states[0] != AccountDenied {
		t.Fatalf("会话取消丢掉了已经看到的拒绝：%v %v", notifier.ids, notifier.states)
	}
}

func TestRecordSessionRiskStorageFailureKeepsMarkerAndClosesGate(t *testing.T) {
	s := sessionTestService(t)
	id := accountTestInsert(t, s, "人工持久化失败", AccountActive, 1)
	life, cancel := context.WithCancelCause(s.ctx)
	at := s.now().UnixMilli()
	work := &sessionWork{Context: life, Cancel: cancel, Closed: make(chan struct{}), View: SessionView{ID: "failed-risk", AccountID: id, Phase: SessionVerifying, StartedAt: &at}}
	s.sessions[work.View.ID] = work
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveSessionMarkerTx(t.Context(), tx, id, sessionMarker{Version: 1, SessionID: work.View.ID, StartedAt: at}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `CREATE TRIGGER reject_manual_risk BEFORE UPDATE ON meta WHEN NEW.key='quota.clock' BEGIN SELECT RAISE(FAIL,'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.recordSessionRisk(work, web.ErrDenied); err == nil {
		t.Fatal("风险保存失败被吞掉")
	}
	if work.ProtectionErr == nil || s.runtimeErr == nil {
		t.Fatal("风险保存失败未保留保护错误或闭锁运行")
	}
	if err := s.closeManualSession(work); err == nil {
		t.Fatal("关闭把风险保护错误当成成功")
	}
	var markers int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM meta WHERE key LIKE 'runtime.session.%'`).Scan(&markers); err != nil || markers != 1 {
		t.Fatalf("风险失败后删除了恢复标记：%d %v", markers, err)
	}
	if s.sessions[work.View.ID] != work {
		t.Fatal("风险失败后释放了账号名额")
	}
	if _, err := s.OpenLogin(t.Context(), id); err == nil {
		t.Fatal("全局闭锁后仍能接受新人工会话")
	}
}

func TestRecordSessionRiskNotifierFailureClosesRuntimeGate(t *testing.T) {
	s := sessionTestService(t)
	id := accountTestInsert(t, s, "人工推送失败", AccountActive, 1)
	work := &sessionWork{View: SessionView{AccountID: id}}
	var published int
	s.publish = func(string, *locales.Message, json.RawMessage) error { published++; return nil }
	failure := errors.New("推送基础设施不可用")
	s.notifier = &sessionEventNotifier{db: s.db, service: s, published: &published, err: failure}
	if err := s.recordSessionRisk(work, web.ErrDenied); !errors.Is(err, failure) || !errors.Is(s.runtimeErr, failure) {
		t.Fatalf("人工推送基础设施错误未闭锁：%v %v", err, s.runtimeErr)
	}
}

func TestHeldVerificationPreflightDoesNotRequireRecoveryURL(t *testing.T) {
	s := sessionTestService(t)
	id := accountTestInsert(t, s, "当前画面仍在", AccountNeedsVerification, 1)
	if _, err := s.db.ExecContext(t.Context(), `UPDATE accounts SET verify_level=1,verify_url=NULL WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	account, _, _, blocked, err := s.manualPreflight(t.Context(), id, SessionLogin)
	if err != nil || blocked != nil || account.State != AccountNeedsVerification || account.VerifyURL != nil {
		t.Fatalf("保留现有画面被恢复网址挡住：%+v %+v %v", account, blocked, err)
	}
	_, _, _, _, err = s.manualPreflight(t.Context(), id, SessionVerification)
	accountTestProblem(t, err, ErrorConflict, "runtime.verification_not_pending")
}

type manualFactory struct {
	entered chan context.Context
	cleaned chan struct{}
}

func (f *manualFactory) Open(ctx context.Context, _ ResourceSpec) (SessionResource, error) {
	f.entered <- ctx
	<-ctx.Done()
	<-f.cleaned
	return nil, ctx.Err()
}

func sessionTestService(t *testing.T) *Service {
	t.Helper()
	now := time.Now()
	return sessionTestServiceWithClock(t, func() time.Time { return now })
}

func sessionTestServiceWithClock(t *testing.T, now func() time.Time) *Service {
	t.Helper()
	s := accountTestServiceWithClock(t, now)
	if _, err := s.settings.Apply(t.Context(), settings.Values{"crawl.quiet_hours": []byte(`{"enabled":false,"start":"01:00","end":"07:30"}`)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestManualQueueDoesNotStartDeadlineAndSurvivesHTTPContext(t *testing.T) {
	s := sessionTestService(t)
	id := accountTestInsert(t, s, "登录", AccountSignedOut, 1)
	ctx, cancel := context.WithCancel(t.Context())
	view, err := s.OpenLogin(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if view.Phase != SessionQueued || view.StartedAt != nil || view.ExpiresAt != nil || view.RemotePath != nil {
		t.Fatalf("排队提前开始了会话：%+v", view)
	}
	s.mu.Lock()
	session := s.sessions[view.ID]
	s.mu.Unlock()
	if session.Context.Err() != nil {
		t.Fatal("HTTP取消带走了已接纳会话")
	}
	if err := s.CloseSession(t.Context(), id, view.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.queuedStarts()) != 0 || s.sessionSnapshot(id) != nil {
		t.Fatal("关闭排队会话没有移除请求")
	}
	if err := s.CloseSession(t.Context(), id, view.ID); err != nil {
		t.Fatal("重复关闭不幂等", err)
	}
}

func TestManualQueueBypassesQuotaAndKeepsCooldown(t *testing.T) {
	for _, initial := range []time.Time{
		time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC),
		time.Date(2037, 1, 1, 12, 0, 0, 0, time.UTC),
	} {
		t.Run(initial.Format(time.RFC3339), func(t *testing.T) {
			now := initial
			s := sessionTestServiceWithClock(t, func() time.Time { return now })
			id := accountTestInsert(t, s, "旧冷却", AccountCooling, 1)
			until := s.now().Add(time.Hour).UnixMilli()
			if _, err := s.db.ExecContext(t.Context(), `UPDATE accounts SET cooldown_until=?,verify_level=2,cooldown_manual_used=1 WHERE id=?`, until, id); err != nil {
				t.Fatal(err)
			}
			if _, err := s.settings.Apply(t.Context(), settings.Values{
				"crawl.account_hourly_visits": []byte(`1`), "crawl.account_daily_search_visits": []byte(`1`), "crawl.account_daily_detail_visits": []byte(`1`),
			}); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := s.db.ExecContext(t.Context(), `INSERT INTO quota_events(account_id,kind,at) VALUES(?,'search',?)`, id, s.now().UnixMilli()); err != nil {
					t.Fatal(err)
				}
			}
			_, err := s.OpenLogin(t.Context(), id)
			accountTestProblem(t, err, ErrorConflict, "run.account_cooling")
			if len(s.queuedStarts()) != 0 || s.sessionSnapshot(id) != nil {
				t.Fatal("冷却未到期仍接纳了扫码会话")
			}
			now = now.Add(2 * time.Hour)
			for range 2 {
				if _, err := s.db.ExecContext(t.Context(), `INSERT INTO quota_events(account_id,kind,at) VALUES(?,'search',?)`, id, s.now().UnixMilli()); err != nil {
					t.Fatal(err)
				}
			}
			view, err := s.OpenLogin(t.Context(), id)
			if err != nil || view.Phase != SessionQueued {
				t.Fatalf("额度已用完不应拦主动登录：%+v %v", view, err)
			}
			if err := s.CloseSession(t.Context(), id, view.ID); err != nil {
				t.Fatal(err)
			}
			var state AccountState
			var keptUntil int64
			var manualUsed bool
			if err := s.db.QueryRowContext(t.Context(), `SELECT state,cooldown_until,cooldown_manual_used FROM accounts WHERE id=?`, id).Scan(&state, &keptUntil, &manualUsed); err != nil || state != AccountCooling || keptUntil != until || !manualUsed {
				t.Fatalf("排队清除了账号保护：%s %d %v %v", state, keptUntil, manualUsed, err)
			}
			if _, err := s.db.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES(?, '1')`, "account.cleanup."+fmt.Sprint(id)); err != nil {
				t.Fatal(err)
			}
			_, err = s.OpenLogin(t.Context(), id)
			accountTestProblem(t, err, ErrorConflict, "accounts.cleanup_failed")
		})
	}
}

func TestManualStartRechecksRiskBeforeResourceOpen(t *testing.T) {
	s := sessionTestService(t)
	id := accountTestInsert(t, s, "验证", AccountSignedOut, 1)
	view, err := s.OpenLogin(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	until := s.now().Add(24 * time.Hour).UnixMilli()
	if _, err := s.db.ExecContext(t.Context(), `UPDATE accounts SET state='denied',cooldown_until=? WHERE id=?`, until, id); err != nil {
		t.Fatal(err)
	}
	request := s.queuedStarts()[0]
	started, blocked, err := s.tryStartSession(t.Context(), request)
	if err != nil || started || blocked == nil || blocked.Reason.Code != "run.account_denied" {
		t.Fatalf("排队后风险变化仍被启动：%v %+v %v", started, blocked, err)
	}
	if _, err := s.GetSession(t.Context(), id, view.ID); err == nil {
		t.Fatal("被阻断的排队会话未移除")
	}
	s.removeQueued(request)
}

func TestManualCloseWaitsForResourceCleanupBeforeGapAndRelease(t *testing.T) {
	s := sessionTestService(t)
	first := accountTestInsert(t, s, "一个账号", AccountSignedOut, 1)
	second := accountTestInsert(t, s, "另一个账号", AccountSignedOut, 2)
	factory := &manualFactory{entered: make(chan context.Context, 1), cleaned: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-factory.cleaned:
		default:
			close(factory.cleaned)
		}
	})
	s.resources = factory
	httpCtx, cancelHTTP := context.WithCancel(t.Context())
	view, err := s.OpenLogin(httpCtx, first)
	if err != nil {
		t.Fatal(err)
	}
	cancelHTTP()
	request := s.queuedStarts()[0]
	started, blocked, err := s.tryStartSession(t.Context(), request)
	if err != nil || !started || blocked != nil {
		t.Fatalf("人工会话未启动：%v %+v %v", started, blocked, err)
	}
	s.removeQueued(request)
	var resourceContext context.Context
	select {
	case resourceContext = <-factory.entered:
	case <-time.After(time.Second):
		t.Fatal("资源工厂未运行")
	}
	if resourceContext.Err() != nil {
		t.Fatal("资源使用了HTTP上下文")
	}
	closed := make(chan error, 1)
	go func() { closed <- s.CloseSession(context.Background(), first, view.ID) }()
	select {
	case <-resourceContext.Done():
	case <-time.After(time.Second):
		t.Fatal("关闭没有取消启动中的资源")
	}
	select {
	case err := <-closed:
		t.Fatal("资源尚未清理就结束关闭", err)
	default:
	}
	other, err := s.OpenLogin(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := s.queuedStarts()[0]
	started, blocked, err = s.tryStartSession(t.Context(), secondRequest)
	if err != nil || started || blocked != nil {
		t.Fatalf("关闭期间提前释放全局名额：%v %+v %v", started, blocked, err)
	}
	var markerCount int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM meta WHERE key LIKE 'runtime.session.%'`).Scan(&markerCount); err != nil || markerCount != 1 {
		t.Fatalf("关闭前恢复标记丢失：%d %v", markerCount, err)
	}
	close(factory.cleaned)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("资源清理后未结束关闭")
	}
	var ended, gap *int64
	if err := s.db.QueryRowContext(t.Context(), `SELECT last_run_ended_at,next_gap_seconds FROM accounts WHERE id=?`, first).Scan(&ended, &gap); err != nil || ended == nil || gap == nil || *gap < 180 {
		t.Fatalf("关闭后没有持久化账号间隔：%v %v %v", ended, gap, err)
	}
	if err := s.CloseSession(t.Context(), second, other.ID); err != nil {
		t.Fatal(err)
	}
}

func TestViewerUsesOneFullWindowAndRemoteReadDoesNotOpen(t *testing.T) {
	base := time.Now()
	s := &Service{now: func() time.Time { return base.Add(29 * time.Minute) }}
	s.initRuntime()
	life, cancel := context.WithCancelCause(s.ctx)
	defer cancel(context.Canceled)
	hold := base.Add(30 * time.Minute)
	work := &sessionWork{Context: life, Cancel: cancel, Closed: make(chan struct{}),
		View: SessionView{ID: "hold", AccountID: 1, Kind: SessionVerification, Phase: SessionVerifyHold}, HoldDeadline: hold, Deadline: hold}
	s.sessions["hold"] = work
	if _, err := s.RemoteSession(t.Context(), "hold"); err == nil {
		t.Fatal("没有远程资源却返回就绪")
	}
	if err := s.ViewerOpened(t.Context(), "hold"); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetSession(t.Context(), 1, "hold")
	if err != nil || view.ExpiresAt == nil || *view.ExpiresAt != base.Add(39*time.Minute).UnixMilli() {
		t.Fatalf("第29分钟接手没有十分钟：%+v %v", view, err)
	}
	if err := s.ViewerOpened(t.Context(), "hold"); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetSession(t.Context(), 1, "hold")
	if *again.ExpiresAt != *view.ExpiresAt {
		t.Fatal("重复viewer延长了期限")
	}
	if err := s.CloseSession(t.Context(), 1, "hold"); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredViewerReturnsBeforeClosingItsRemote(t *testing.T) {
	now := time.Now()
	s := &Service{now: func() time.Time { return now }}
	s.initRuntime()
	life, cancel := context.WithCancelCause(s.ctx)
	defer cancel(context.Canceled)
	work := &sessionWork{Context: life, Cancel: cancel, Closed: make(chan struct{}),
		View: SessionView{ID: "old", AccountID: 1, Kind: SessionVerification, Phase: SessionVerifyHold}, HoldDeadline: now.Add(-time.Minute)}
	s.sessions["old"] = work
	accountTestProblem(t, s.ViewerOpened(t.Context(), "old"), ErrorConflict, "runtime.session_changed")
	if work.Closing {
		t.Fatal("viewer回调同步开始关闭，会等待自己的远程连接退出")
	}
	select {
	case <-work.Closed:
		t.Fatal("viewer回调同步关闭了资源")
	default:
	}
	if err := s.CloseSession(t.Context(), 1, "old"); err != nil {
		t.Fatal(err)
	}
}

func TestManualSessionRetainsProtectionAndBypassesOnlyQuota(t *testing.T) {
	defaults, err := settings.Resolve(nil, settings.Environment{TZ: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	cooldown := now.Add(24 * time.Hour).UnixMilli()
	account := Account{ID: 1, State: AccountCooling, CooldownUntil: &cooldown, CooldownManualUsed: true}
	if block, err := manualSessionBlock(account, SessionLogin, defaults, now); err != nil || block == nil || block.Reason.Code != "run.account_cooling" || block.NextAllowedAt == nil || *block.NextAllowedAt != cooldown {
		t.Fatalf("登录绕过未结束的冷却：%+v %v", block, err)
	}
	account.State = AccountDenied
	block, err := manualSessionBlock(account, SessionLogin, defaults, now)
	if err != nil || block == nil || block.Reason.Code != "run.account_denied" || *block.NextAllowedAt != cooldown {
		t.Fatalf("人工登录绕过拒绝暂停：%+v %v", block, err)
	}
	account.State, account.CooldownUntil = AccountActive, nil
	ended, gap := now.UnixMilli(), int64(180)
	account.LastRunEndedAt, account.NextGapSeconds = &ended, &gap
	block, err = manualSessionBlock(account, SessionLogin, defaults, now)
	if err != nil || block == nil || block.Reason.Code != "run.account_gap" || *block.NextAllowedAt != now.Add(3*time.Minute).UnixMilli() {
		t.Fatalf("人工登录绕过账号间隔：%+v %v", block, err)
	}
	account.LastRunEndedAt, account.NextGapSeconds = nil, nil
	quietNow := time.Date(2026, 11, 11, 2, 0, 0, 0, time.UTC)
	block, err = manualSessionBlock(account, SessionLogin, defaults, quietNow)
	if err != nil || block == nil || block.Reason.Code != "run.quiet_hours" || *block.NextAllowedAt != time.Date(2026, 11, 11, 7, 30, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("人工登录绕过静默时段：%+v %v", block, err)
	}
	if _, err := manualSessionBlock(account, SessionVerification, defaults, now); err == nil {
		t.Fatal("无待处理验证仍然允许重开")
	}
}

func TestGetSessionReturnsIndependentView(t *testing.T) {
	s := &Service{}
	s.initRuntime()
	at, taskID, runID := int64(100), int64(2), int64(3)
	path := "/remote/sessions/one/"
	s.sessions["one"] = &sessionWork{View: SessionView{ID: "one", AccountID: 1,
		Kind: SessionVerification, Phase: SessionVerifyHold, StartedAt: &at, ExpiresAt: &at,
		TaskID: &taskID, RunID: &runID, RemotePath: &path, Viewport: &remote.Viewport{Width: 100, Height: 100}}}
	view, err := s.GetSession(context.Background(), 1, "one")
	if err != nil {
		t.Fatal(err)
	}
	*view.StartedAt, *view.ExpiresAt, *view.TaskID, *view.RunID = 999, 999, 999, 999
	*view.RemotePath, view.Viewport.Width = "changed", 999
	again, err := s.GetSession(context.Background(), 1, "one")
	if err != nil || *again.StartedAt != 100 || *again.ExpiresAt != 100 || *again.TaskID != 2 || *again.RunID != 3 || *again.RemotePath != path || again.Viewport.Width != 100 {
		t.Fatalf("调用方修改污染了会话：%+v %v", again, err)
	}
	for _, query := range []struct {
		account int64
		id      string
	}{{2, "one"}, {1, "missing"}} {
		_, err := s.GetSession(context.Background(), query.account, query.id)
		var failure *ServiceError
		if !errors.As(err, &failure) || failure.Kind != ErrorNotFound {
			t.Fatalf("无关账号或旧会话不应读到会话：%v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.GetSession(ctx, 1, "one"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消读取没有停止：%v", err)
	}
}

func TestFirstViewerGetsFullTenMinutes(t *testing.T) {
	start := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	holdDeadline := start.Add(30 * time.Minute)
	for _, minute := range []int{1, 29} {
		t.Run(fmt.Sprint(minute), func(t *testing.T) {
			opened := start.Add(time.Duration(minute) * time.Minute)
			deadline, firstViewed := firstViewerDeadline(holdDeadline, time.Time{}, opened)
			if !deadline.Equal(opened.Add(10*time.Minute)) || !firstViewed.Equal(opened) {
				t.Fatalf("首次接手没有完整十分钟：截止%v，首次%v", deadline, firstViewed)
			}
			for _, later := range []time.Time{opened.Add(time.Minute), opened.Add(9 * time.Minute)} {
				nextDeadline, nextFirst := firstViewerDeadline(deadline, firstViewed, later)
				if !nextDeadline.Equal(deadline) || !nextFirst.Equal(firstViewed) {
					t.Fatalf("重复打开续期了：截止%v，首次%v", nextDeadline, nextFirst)
				}
			}
		})
	}
}

func TestManualRiskPreservesWrappedSignalsAndPriority(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{"正常", nil, nil},
		{"普通错误", errors.New("页面错误"), nil},
		{"验证", fmt.Errorf("首页：%w", web.ErrNeedVerify), web.ErrNeedVerify},
		{"失效", fmt.Errorf("自己的主页：%w", web.ErrLoginExpired), web.ErrLoginExpired},
		{"拒绝", &web.RiskError{Kind: web.ErrDenied, Where: "确认", Signal: "deny"}, web.ErrDenied},
		{"环境", fmt.Errorf("页面：%w", web.ErrEnvBlocked), web.ErrEnvBlocked},
		{"验证不能盖住失效", errors.Join(web.ErrNeedVerify, web.ErrLoginExpired), web.ErrLoginExpired},
		{"早期验证不能盖住拒绝", errors.Join(web.ErrNeedVerify, web.ErrDenied), web.ErrDenied},
		{"失效不能盖住环境", errors.Join(web.ErrLoginExpired, web.ErrEnvBlocked), web.ErrEnvBlocked},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := manualRisk(test.err); got != test.want {
				t.Fatalf("人工信号分类错误：got %v, want %v", got, test.want)
			}
		})
	}
}

func TestManualSignalErrorsKeepRiskAndHTTPMeaning(t *testing.T) {
	for _, test := range []struct {
		err  error
		code string
	}{
		{web.ErrNeedVerify, "run.account_needs_verification"},
		{errors.Join(web.ErrNeedVerify, web.ErrVerificationRefresh), "remote.verification_refresh_confirm"},
		{errors.Join(web.ErrNeedVerify, web.ErrVerificationRefresh, web.ErrDenied), "run.account_denied"},
		{web.ErrLoginExpired, "run.account_expired"},
		{web.ErrDenied, "run.account_denied"},
		{web.ErrEnvBlocked, "run.account_denied"},
	} {
		err := manualSignalError(fmt.Errorf("确认：%w", test.err))
		var problem *ServiceError
		if !errors.Is(err, test.err) || !errors.As(err, &problem) || problem.Kind != ErrorConflict || problem.Reason.Code != test.code {
			t.Fatalf("人工风控丢失了业务语义：%v", err)
		}
	}
}

func TestManualFrameClearsViewportAndStopsUpdatingAfterClose(t *testing.T) {
	s := sessionTestService(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	work := &sessionWork{Context: ctx, Remote: &remote.Session{}, View: SessionView{Phase: SessionVerifying, RefreshVerification: true}}
	s.updateManualFrame(work, nil, true)
	if work.View.Viewport != nil || !work.View.RefreshVerification {
		t.Fatalf("重开时仍然没有验证框，应保留提示：%+v", work.View)
	}
	view := &remote.Viewport{X: 30, Y: 60, Width: 300, Height: 200}
	s.updateManualFrame(work, view, false)
	if work.View.Viewport == nil || *work.View.Viewport != *view || work.View.RefreshVerification {
		t.Fatalf("验证区域没有发布：%+v", work.View)
	}
	s.updateManualFrame(work, nil, true)
	if work.View.Viewport != nil || work.View.RefreshVerification {
		t.Fatalf("验证框消失后没有清除旧区域：%+v", work.View)
	}
	work.Closing = true
	s.updateManualFrame(work, view, false)
	if work.View.Viewport != nil || work.View.RefreshVerification {
		t.Fatalf("关闭时迟到的结果改写了画面：%+v", work.View)
	}
}
